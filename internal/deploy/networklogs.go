package deploy

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/netlog"
	"github.com/turin-dev/aven/internal/store"
)

const (
	// NetworkLogRetention은 DNS/flow 기록 보관 기간이다.
	NetworkLogRetention = 7 * 24 * time.Hour
	// NetworkLogMaxRows는 보관할 기록 수의 상한이다(바쁜 노드가 DB를 키우지 않게, 오래된 것부터 지운다).
	NetworkLogMaxRows = 500_000

	networkLogQueue = 8192
	networkLogBatch = 500
)

// DNSLogRecord는 보관·전송하는 DNS 기록이다(서비스 이름을 붙인다).
type DNSLogRecord struct {
	netlog.DNSQuery
	Service string `json:"service"`
}

// FlowLogRecord는 보관·전송하는 flow 기록이다(서비스와 상대 서비스 이름을 붙인다).
type FlowLogRecord struct {
	netlog.Flow
	Service string `json:"service"`
}

type networkLogSubscription struct {
	envID    string
	kind     string
	services map[string]bool
	ch       chan *store.NetworkLog
}

type networkLogNames struct {
	at     time.Time
	byID   map[string]string
	byName map[string]string
}

// NetworkLogHub는 노드 수집기의 기록에 이름을 붙여 SQLite에 보관하고, 로그 화면과 CLI 구독자에게 보낸다.
type NetworkLogHub struct {
	store *store.Store
	log   *slog.Logger
	in    chan *store.NetworkLog

	mu        sync.Mutex
	subs      map[*networkLogSubscription]struct{}
	status    map[string]netlog.Status
	lastPrune time.Time
	dropped   int

	namesMu sync.Mutex
	names   map[string]networkLogNames
}

func NewNetworkLogHub(st *store.Store, log *slog.Logger) *NetworkLogHub {
	return &NetworkLogHub{store: st, log: log, in: make(chan *store.NetworkLog, networkLogQueue),
		subs: map[*networkLogSubscription]struct{}{}, status: map[string]netlog.Status{}, names: map[string]networkLogNames{}}
}

// Run은 들어온 기록을 모아서(최대 0.5초, 500개) 한 트랜잭션으로 저장하고, 새로 저장된 것만 구독자에게 보낸다.
func (h *NetworkLogHub) Run(ctx context.Context) {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	var batch []*store.NetworkLog
	for {
		select {
		case <-ctx.Done():
			h.flush(batch)
			return
		case l := <-h.in:
			if batch = append(batch, l); len(batch) >= networkLogBatch {
				h.flush(batch)
				batch = nil
			}
		case <-tick.C:
			h.flush(batch)
			batch = nil
		}
	}
}

func (h *NetworkLogHub) flush(batch []*store.NetworkLog) {
	h.mu.Lock()
	dropped := h.dropped
	h.dropped = 0
	h.mu.Unlock()
	if dropped > 0 && h.log != nil {
		h.log.Warn("network logs: queue full, records dropped", "count", dropped)
	}
	if len(batch) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	inserted, err := h.store.AddNetworkLogs(ctx, batch)
	if err != nil {
		if h.log != nil {
			h.log.Warn("store network logs", "count", len(batch), "err", err)
		}
		return
	}
	h.publish(inserted)
	h.mu.Lock()
	prune := time.Since(h.lastPrune) >= 10*time.Minute
	if prune {
		h.lastPrune = time.Now()
	}
	h.mu.Unlock()
	if prune {
		if _, err := h.store.PruneNetworkLogs(ctx, time.Now().Add(-NetworkLogRetention), NetworkLogMaxRows); err != nil && h.log != nil {
			h.log.Warn("prune network logs", "err", err)
		}
	}
}

func (h *NetworkLogHub) publish(logs []*store.NetworkLog) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, l := range logs {
		for sub := range h.subs {
			if sub.envID != l.EnvironmentID || (sub.kind != "" && sub.kind != l.Kind) ||
				(len(sub.services) > 0 && !sub.services[l.ServiceID] && !sub.services[l.RelatedServiceID]) {
				continue
			}
			select {
			case sub.ch <- l:
			default:
				close(sub.ch)
				delete(h.subs, sub)
			}
		}
	}
}

// Add는 노드 수집기의 기록 하나를 받는다(막히지 않는다: 대기열이 차면 버리고 다음 flush에서 경고한다).
func (h *NetworkLogHub) Add(nodeID string, e netlog.Event) {
	if h == nil || h.store == nil {
		return
	}
	if e.Kind == "status" {
		if e.Status != nil {
			st := *e.Status
			if st.Node == "" {
				st.Node = nodeID
			}
			h.mu.Lock()
			h.status[nodeID] = st
			h.mu.Unlock()
		}
		return
	}
	l := h.enrich(e)
	if l == nil {
		return
	}
	select {
	case h.in <- l:
	default:
		h.mu.Lock()
		h.dropped++
		h.mu.Unlock()
	}
}

// enrich는 기록에 서비스 이름과 (mesh forwarder처럼 이름만 아는) 상대 서비스 ID를 붙여 저장할 행으로 만든다.
func (h *NetworkLogHub) enrich(e netlog.Event) *store.NetworkLog {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	switch {
	case e.Kind == "dns" && e.DNS != nil:
		r := DNSLogRecord{DNSQuery: *e.DNS}
		if r.Environment == "" || r.ServiceID == "" {
			return nil
		}
		names := h.serviceNames(ctx, r.Environment)
		if r.Service = names.byID[r.ServiceID]; r.Service == "" {
			return nil
		}
		data, err := json.Marshal(r)
		if err != nil {
			return nil
		}
		return &store.NetworkLog{ID: e.ID, Kind: "dns", EnvironmentID: r.Environment, ServiceID: r.ServiceID, RecordedAt: r.T, Data: data}
	case e.Kind == "network" && e.Flow != nil:
		r := FlowLogRecord{Flow: *e.Flow}
		if r.Environment == "" || r.ServiceID == "" {
			return nil
		}
		names := h.serviceNames(ctx, r.Environment)
		if r.Service = names.byID[r.ServiceID]; r.Service == "" {
			return nil // 그 환경의 프로젝트에 없는 서비스(지워졌거나 잘못된 노드 기록)
		}
		if r.PeerKind == "service" {
			if r.PeerServiceID == "" {
				r.PeerServiceID = names.byName[r.Peer]
			}
			if n := names.byID[r.PeerServiceID]; n != "" {
				r.Peer = n
			}
		}
		data, err := json.Marshal(r)
		if err != nil {
			return nil
		}
		return &store.NetworkLog{ID: e.ID, Kind: "network", EnvironmentID: r.Environment, ServiceID: r.ServiceID,
			RelatedServiceID: r.PeerServiceID, RecordedAt: r.T, Data: data}
	}
	return nil
}

func (h *NetworkLogHub) serviceNames(ctx context.Context, envID string) networkLogNames {
	h.namesMu.Lock()
	if c, ok := h.names[envID]; ok && time.Since(c.at) < time.Minute {
		h.namesMu.Unlock()
		return c
	}
	h.namesMu.Unlock()
	c := networkLogNames{at: time.Now(), byID: map[string]string{}, byName: map[string]string{}}
	if env, err := h.store.GetEnvironment(ctx, envID); err == nil {
		if services, err := h.store.ListServices(ctx, env.ProjectID); err == nil {
			for _, svc := range services {
				c.byID[svc.ID], c.byName[svc.Name] = svc.Name, svc.ID
			}
		}
	}
	h.namesMu.Lock()
	h.names[envID] = c
	h.namesMu.Unlock()
	return c
}

// CaptureStatus는 노드 수집기 상태를 노드 ID 순서로 돌려준다.
func (h *NetworkLogHub) CaptureStatus() []netlog.Status {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]netlog.Status, 0, len(h.status))
	for _, st := range h.status {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out
}

// Subscribe는 새 기록 구독을 먼저 걸고 보관된 기록을 읽는다(그 사이 기록이 빠지지 않게. 겹치는 기록은 호출한 쪽이 ID로 거른다).
// past는 시간 순서다.
func (h *NetworkLogHub) Subscribe(ctx context.Context, q store.NetworkLogQuery, follow bool) (past []*store.NetworkLog, ch chan *store.NetworkLog, cancel func(), err error) {
	cancel = func() {}
	if h == nil || h.store == nil {
		return nil, nil, cancel, nil
	}
	if follow {
		services := make(map[string]bool, len(q.Services))
		for _, id := range q.Services {
			services[id] = true
		}
		sub := &networkLogSubscription{envID: q.EnvironmentID, kind: q.Kind, services: services, ch: make(chan *store.NetworkLog, 1024)}
		h.mu.Lock()
		h.subs[sub] = struct{}{}
		h.mu.Unlock()
		ch = sub.ch
		cancel = func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if _, ok := h.subs[sub]; ok {
				delete(h.subs, sub)
				close(sub.ch)
			}
		}
	}
	past, err = h.store.ListNetworkLogs(ctx, q)
	if err != nil {
		cancel()
		return nil, nil, func() {}, err
	}
	sort.Slice(past, func(i, j int) bool {
		if past[i].RecordedAt.Equal(past[j].RecordedAt) {
			return past[i].ID < past[j].ID
		}
		return past[i].RecordedAt.Before(past[j].RecordedAt)
	})
	return past, ch, cancel, nil
}

// NetworkLogView는 기록을 API/CLI로 보낼 평평한 JSON 객체로 만든다. viewer가 flow의 상대 서비스면
// 그 서비스 쪽에서 본 방향(ingress/egress), 바이트, 상대로 뒤집는다(같은 연결을 두 서비스 로그에서 각자 보게).
func NetworkLogView(l *store.NetworkLog, viewer string) map[string]any {
	var out map[string]any
	if json.Unmarshal(l.Data, &out) != nil || out == nil {
		out = map[string]any{}
	}
	out["id"], out["kind"], out["service_id"] = l.ID, l.Kind, l.ServiceID
	if l.Kind == "network" && viewer != "" && viewer != l.ServiceID && viewer == l.RelatedServiceID {
		self, selfName := l.ServiceID, out["service"]
		out["service_id"], out["service"] = viewer, out["peer"]
		out["peer_service_id"], out["peer"], out["peer_kind"] = self, selfName, "service"
		out["bytes_sent"], out["bytes_received"] = out["bytes_received"], out["bytes_sent"]
		if out["direction"] == "egress" {
			out["direction"] = "ingress"
		} else {
			out["direction"] = "egress"
		}
		delete(out, "replica")
	}
	return out
}

// StartLocalNetworkLogs는 로컬 노드(aven-server)의 DNS/flow 수집기를 띄운다(원격 노드는 agent가 수집해 netlog.follow로 보낸다).
func (m *Manager) StartLocalNetworkLogs(ctx context.Context) {
	if m.NetworkLogs == nil || m.Docker == nil {
		return
	}
	c := &netlog.Collector{NodeID: store.LocalNodeID, Docker: m.Docker, Log: m.Log}
	go c.Run(ctx)
	go c.Follow(ctx, func(e netlog.Event) { m.NetworkLogs.Add(store.LocalNodeID, e) })
}

// ---------------- 필터 ----------------

// NetworkLogFilter는 DNS/flow 로그의 q 필터다. ParseFilter와 같은 문법(단어, "구", -제외, AND)에
// `@필드:값` 조건을 더한다: 문자열 필드는 정확히 같아야 하고(대소문자 무시), 숫자 필드는 `>=400`, `<10`, `100..200`도 된다.
// 별칭: DNS `@domain`(그 이름이나 하위 도메인), flow `@port`(출발/도착 포트), `@host`(출발/도착 주소), `@src`, `@dst`,
// `@peer`(상대 이름 또는 ID), `@dropped:true`(refused/timeout).
type NetworkLogFilter struct {
	words, notWords []string
	attrs, notAttrs []attrTerm
}

type attrTerm struct{ key, value string }

func ParseNetworkLogFilter(q string) NetworkLogFilter {
	base := ParseFilter(q)
	var f NetworkLogFilter
	split := func(terms []string, words *[]string, attrs *[]attrTerm) {
		for _, t := range terms {
			if k, v, ok := strings.Cut(t, ":"); ok && strings.HasPrefix(k, "@") && len(k) > 1 && v != "" {
				*attrs = append(*attrs, attrTerm{key: strings.TrimPrefix(k, "@"), value: v})
			} else {
				*words = append(*words, t)
			}
		}
	}
	split(base.Include, &f.words, &f.attrs)
	split(base.Exclude, &f.notWords, &f.notAttrs)
	return f
}

// Empty는 조건이 없는지다.
func (f NetworkLogFilter) Empty() bool {
	return len(f.words)+len(f.notWords)+len(f.attrs)+len(f.notAttrs) == 0
}

// Match는 NetworkLogView 결과(rec)가 조건에 맞는지 본다.
func (f NetworkLogFilter) Match(rec map[string]any) bool {
	if f.Empty() {
		return true
	}
	if len(f.words)+len(f.notWords) > 0 {
		b, _ := json.Marshal(rec)
		text := strings.ToLower(string(b))
		for _, w := range f.words {
			if !strings.Contains(text, w) {
				return false
			}
		}
		for _, w := range f.notWords {
			if strings.Contains(text, w) {
				return false
			}
		}
	}
	for _, a := range f.attrs {
		if !matchAttr(rec, a) {
			return false
		}
	}
	for _, a := range f.notAttrs {
		if matchAttr(rec, a) {
			return false
		}
	}
	return true
}

var attrAliases = map[string][]string{
	"port": {"src_port", "dst_port"}, "host": {"src_addr", "dst_addr"}, "src": {"src_addr"}, "dst": {"dst_addr"},
	"peer": {"peer", "peer_service_id"}, "service": {"service", "service_id"}, "answer": {"answers"},
	"peerkind": {"peer_kind"}, "querytype": {"qtype"}, "queryzone": {"zone"}, "protocol": {"protocol"}, "l4protocol": {"protocol"},
}

func matchAttr(rec map[string]any, a attrTerm) bool {
	key := strings.ToLower(a.key)
	switch key {
	case "domain":
		q, _ := rec["qname"].(string)
		q, d := strings.TrimSuffix(strings.ToLower(q), "."), strings.TrimSuffix(a.value, ".")
		return q == d || strings.HasSuffix(q, "."+d)
	case "dropped":
		st, _ := rec["status"].(string)
		dropped := st == "refused" || st == "timeout"
		return strconv.FormatBool(dropped) == a.value
	}
	fields := attrAliases[key]
	if fields == nil {
		fields = []string{key}
	}
	for _, k := range fields {
		if matchValue(rec[k], a.value) {
			return true
		}
	}
	return false
}

func matchValue(v any, want string) bool {
	switch x := v.(type) {
	case string:
		return strings.EqualFold(x, want)
	case float64:
		return matchNumber(x, want)
	case bool:
		return strconv.FormatBool(x) == want
	case []any:
		for _, e := range x {
			if matchValue(e, want) {
				return true
			}
		}
	}
	return false
}

func matchNumber(x float64, want string) bool {
	num := func(s string) (float64, bool) {
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		return f, err == nil
	}
	if lo, hi, ok := strings.Cut(want, ".."); ok {
		l, ok1 := num(lo)
		h, ok2 := num(hi)
		return ok1 && ok2 && x >= l && x <= h
	}
	for _, op := range []string{">=", "<=", ">", "<", "="} {
		if rest, ok := strings.CutPrefix(want, op); ok {
			n, ok := num(rest)
			if !ok {
				return false
			}
			switch op {
			case ">=":
				return x >= n
			case "<=":
				return x <= n
			case ">":
				return x > n
			case "<":
				return x < n
			}
			return x == n
		}
	}
	n, ok := num(want)
	return ok && x == n
}
