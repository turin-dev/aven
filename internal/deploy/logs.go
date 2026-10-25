package deploy

import (
	"bufio"
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/store"
)

// LogEntry는 deploy-logs / environment logs SSE의 data 형식이다.
type LogEntry struct {
	T         string `json:"t"`
	Replica   string `json:"replica"`
	Stream    string `json:"stream"`
	Msg       string `json:"msg"`
	ServiceID string `json:"service_id,omitempty"`
}

func entry(replica string, l node.LogLine) LogEntry {
	return LogEntry{T: l.T.UTC().Format(time.RFC3339Nano), Replica: replica, Stream: l.Stream, Msg: l.Msg}
}

func (m *Manager) runtimeLogPath(depID string) string {
	return filepath.Join(m.runtimeLogDir(), depID+".jsonl")
}

func (m *Manager) cronLogPath(runID string) string {
	return filepath.Join(m.cronLogDir(), runID+".jsonl")
}

// saveRuntimeLogs는 컨테이너를 지우기 전에 마지막 로그를 파일로 보관한다(이전 배포 로그 조회용).
func (m *Manager) saveRuntimeLogs(ctx context.Context, d *store.Deployment, names []string) {
	if d == nil {
		return
	}
	var all []LogEntry
	for _, n := range names {
		nodeID := d.NodeOf(n)
		if !m.nodeOnline(nodeID) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		m.driverForNode(nodeID).FollowLogs(ctx, n, "1000", false, func(l node.LogLine) { all = append(all, entry(n, l)) })
		cancel()
	}
	if len(all) == 0 {
		return
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].T < all[j].T })
	writeJSONL(m.runtimeLogPath(d.ID), all)
}

func writeJSONL(path string, entries []LogEntry) {
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, e := range entries {
		enc.Encode(e)
	}
	w.Flush()
}

func readJSONL(path string, tail int) []LogEntry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []LogEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var e LogEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	if tail > 0 && len(out) > tail {
		out = out[len(out)-tail:]
	}
	return out
}

// StreamDeployLogs는 배포의 런타임 로그를 넘긴다(모든 replica 병합).
// 처음엔 replica별 tail을 모아 시간순으로 정렬해 보내고, follow면 이후 로그를 따라간다.
// 컨테이너가 없으면 보관된 로그를 재생하고 끝난다. cron 배포는 실행 기록의 로그를 재생한다.
func (m *Manager) StreamDeployLogs(ctx context.Context, d *store.Deployment, tail int, follow bool, out func(LogEntry)) error {
	if len(d.AllContainers()) == 0 {
		runs, _ := m.Store.ListCronRuns(ctx, d.ID, 50)
		var all []LogEntry
		for i := len(runs) - 1; i >= 0; i-- {
			all = append(all, readJSONL(m.cronLogPath(runs[i].ID), 0)...)
		}
		if tail > 0 && len(all) > tail {
			all = all[len(all)-tail:]
		}
		for _, e := range all {
			out(e)
		}
		return nil
	}
	// 여러 노드에 나눠 띄웠으면 노드마다 그 노드의 replica 로그를 가져와 합친다.
	drvOf := func(c string) Driver { return m.driverForNode(d.NodeOf(c)) }
	var live []string
	for i, g := range d.Groups() {
		if !m.nodeOnline(g.NodeID) && i > 0 {
			continue
		}
		states, err := m.driverForNode(g.NodeID).InspectMany(ctx, g.Containers)
		if err != nil {
			if i == 0 {
				return err
			}
			continue
		}
		for _, c := range g.Containers {
			if states[c] != nil {
				live = append(live, c)
			}
		}
	}
	if len(live) == 0 {
		for _, e := range readJSONL(m.runtimeLogPath(d.ID), tail) {
			out(e)
		}
		return nil
	}
	since := time.Now().UTC()
	var initial []LogEntry
	for _, c := range live {
		lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		drvOf(c).FollowLogs(lctx, c, itoa(tail), false, func(l node.LogLine) { initial = append(initial, entry(c, l)) })
		cancel()
	}
	sort.SliceStable(initial, func(i, j int) bool { return initial[i].T < initial[j].T })
	if tail > 0 && len(initial) > tail {
		initial = initial[len(initial)-tail:]
	}
	for _, e := range initial {
		out(e)
	}
	if !follow {
		return nil
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, c := range live {
		wg.Add(1)
		go func(c string) {
			defer wg.Done()
			drvOf(c).LogsSince(ctx, c, "all", since.Format(time.RFC3339Nano), true, func(l node.LogLine) {
				mu.Lock()
				out(entry(c, l))
				mu.Unlock()
			})
		}(c)
	}
	wg.Wait()
	return nil
}

func itoa(n int) string {
	if n <= 0 {
		return "all"
	}
	b := make([]byte, 0, 8)
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// ---------------- 검색 필터 ----------------

// Filter는 로그 검색어다: 단어(모두 포함), "구문", -제외어, -"제외 구문". 대소문자 무시.
type Filter struct {
	Include []string
	Exclude []string
}

func ParseFilter(q string) Filter {
	var f Filter
	q = strings.TrimSpace(q)
	for len(q) > 0 {
		neg := false
		if q[0] == '-' && len(q) > 1 {
			neg = true
			q = q[1:]
		}
		var term string
		if q[0] == '"' {
			end := strings.IndexByte(q[1:], '"')
			if end < 0 {
				term, q = q[1:], ""
			} else {
				term, q = q[1:1+end], q[2+end:]
			}
		} else {
			end := strings.IndexAny(q, " \t")
			if end < 0 {
				term, q = q, ""
			} else {
				term, q = q[:end], q[end:]
			}
		}
		q = strings.TrimSpace(q)
		term = strings.ToLower(term)
		if term == "" {
			continue
		}
		if neg {
			f.Exclude = append(f.Exclude, term)
		} else {
			f.Include = append(f.Include, term)
		}
	}
	return f
}

func (f Filter) Match(s string) bool {
	s = strings.ToLower(s)
	for _, t := range f.Include {
		if !strings.Contains(s, t) {
			return false
		}
	}
	for _, t := range f.Exclude {
		if strings.Contains(s, t) {
			return false
		}
	}
	return true
}

// ---------------- HTTP 로그 허브 ----------------

// HTTPLogHub는 Caddy access log를 인스턴스별로 나눠 최근 n개를 보관하고 구독자에게 보낸다.
type HTTPLogHub struct {
	mu    sync.Mutex
	n     int
	hosts map[string]string    // host → instanceID
	last  map[string]time.Time // instanceID → 마지막 요청 시각
	ring  map[string][]proxy.AccessLog
	subs  map[string]map[chan proxy.AccessLog]struct{}
	// counts는 인스턴스별 요청 수를 10초 단위로 센다(보관 개수 n과 상관없이 요청률을 알 수 있다. 자동 확장 rps용).
	counts map[string]*reqCounter
	now    func() time.Time // 테스트용
}

// reqBucketS는 요청 수 버킷 크기(초), reqBuckets는 버킷 수(합쳐 6분)다.
const (
	reqBucketS = 10
	reqBuckets = 36
)

type reqCounter struct {
	n     [reqBuckets]int64
	epoch [reqBuckets]int64 // 버킷이 센 구간(unix/10). 다르면 오래된 버킷이다
}

func (c *reqCounter) inc(t time.Time) {
	e := t.Unix() / reqBucketS
	i := e % reqBuckets
	if c.epoch[i] != e {
		c.epoch[i], c.n[i] = e, 0
	}
	c.n[i]++
}

// since는 [now-window, now] 구간의 요청 수다(버킷 단위라 최대 10초 오차).
func (c *reqCounter) since(now time.Time, window time.Duration) int64 {
	cur := now.Unix() / reqBucketS
	from := now.Add(-window).Unix() / reqBucketS
	var sum int64
	for i := 0; i < reqBuckets; i++ {
		if e := c.epoch[i]; e > from && e <= cur {
			sum += c.n[i]
		}
	}
	return sum
}

func NewHTTPLogHub(n int) *HTTPLogHub {
	return &HTTPLogHub{n: n, hosts: map[string]string{}, last: map[string]time.Time{}, ring: map[string][]proxy.AccessLog{}, subs: map[string]map[chan proxy.AccessLog]struct{}{},
		counts: map[string]*reqCounter{}}
}

func (h *HTTPLogHub) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// RequestRate는 인스턴스가 최근 window 동안 받은 초당 요청 수다(모든 노드의 access log 합).
func (h *HTTPLogHub) RequestRate(instanceID string, window time.Duration) float64 {
	if window <= 0 {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.counts[instanceID]
	if c == nil {
		return 0
	}
	return float64(c.since(h.clock(), window)) / window.Seconds()
}

func (h *HTTPLogHub) SetHosts(hosts map[string]string) {
	h.mu.Lock()
	h.hosts = hosts
	h.mu.Unlock()
}

func (h *HTTPLogHub) Add(l proxy.AccessLog) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id, ok := h.lookup(l.Host)
	if !ok {
		return
	}
	h.last[id] = time.Now()
	if h.counts == nil {
		h.counts = map[string]*reqCounter{}
	}
	c := h.counts[id]
	if c == nil {
		c = &reqCounter{}
		h.counts[id] = c
	}
	c.inc(h.clock())
	r := append(h.ring[id], l)
	if len(r) > h.n {
		r = r[len(r)-h.n:]
	}
	h.ring[id] = r
	for ch := range h.subs[id] {
		select {
		case ch <- l:
		default:
		}
	}
}

// Subscribe는 지금까지의 로그와 이후 채널을 돌려준다.
func (h *HTTPLogHub) Subscribe(instanceID string) ([]proxy.AccessLog, chan proxy.AccessLog, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	past := append([]proxy.AccessLog(nil), h.ring[instanceID]...)
	ch := make(chan proxy.AccessLog, 256)
	if h.subs[instanceID] == nil {
		h.subs[instanceID] = map[chan proxy.AccessLog]struct{}{}
	}
	h.subs[instanceID][ch] = struct{}{}
	return past, ch, func() {
		h.mu.Lock()
		delete(h.subs[instanceID], ch)
		h.mu.Unlock()
	}
}

// IsErrorLine은 level=error 필터 조건이다: stderr이거나 error/fatal/panic을 포함(대소문자 무시).
func IsErrorLine(l LogEntry) bool {
	if l.Stream == "stderr" {
		return true
	}
	m := strings.ToLower(l.Msg)
	return strings.Contains(m, "error") || strings.Contains(m, "fatal") || strings.Contains(m, "panic")
}

// HTTPWindow는 최근 구간의 HTTP 요청 요약이다(알림 규칙의 http_5xx_rate, http_p95_ms).
type HTTPWindow struct {
	Requests  int
	Errors5xx int
	P95MS     float64
}

// Window는 since 이후 access log의 요청 수, 5xx 수, 응답 시간 p95(ms)다. 보관 중인 최근 n개 안에서만 센다.
func (h *HTTPLogHub) Window(instanceID string, since time.Time) HTTPWindow {
	h.mu.Lock()
	ring := append([]proxy.AccessLog(nil), h.ring[instanceID]...)
	h.mu.Unlock()
	return SummarizeHTTP(ring, since)
}

// SummarizeHTTP는 since 이후 로그의 요약이다(T가 RFC3339가 아니면 건너뛴다).
func SummarizeHTTP(logs []proxy.AccessLog, since time.Time) HTTPWindow {
	var w HTTPWindow
	var durs []float64
	for _, l := range logs {
		t, err := time.Parse(time.RFC3339Nano, l.T)
		if err != nil || t.Before(since) {
			continue
		}
		w.Requests++
		if l.Status >= 500 && l.Status <= 599 {
			w.Errors5xx++
		}
		durs = append(durs, l.DurationMS)
	}
	if len(durs) > 0 {
		sort.Float64s(durs)
		// nearest-rank p95
		i := int(math.Ceil(0.95*float64(len(durs)))) - 1
		w.P95MS = durs[max(i, 0)]
	}
	return w
}

// LastRequest는 인스턴스에 마지막으로 HTTP 요청이 들어온 시각이다(모르면 zero).
func (h *HTTPLogHub) LastRequest(instanceID string) time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.last[instanceID]
}

// Touch는 요청이 있었던 것으로 기록한다(wake 직후 바로 다시 재우지 않도록).
func (h *HTTPLogHub) Touch(instanceID string) {
	h.mu.Lock()
	h.last[instanceID] = time.Now()
	h.mu.Unlock()
}

// InstanceForHost는 호스트가 속한 인스턴스다. 정확히 같은 호스트가 없으면 와일드카드(*.example.com) 도메인을 본다.
func (h *HTTPLogHub) InstanceForHost(host string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lookup(host)
}

func (h *HTTPLogHub) lookup(host string) (string, bool) {
	if id, ok := h.hosts[host]; ok {
		return id, true
	}
	if w := proxy.WildcardFor(host); w != "" {
		id, ok := h.hosts[w]
		return id, ok
	}
	return "", false
}
