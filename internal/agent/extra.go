package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/compat"
	"github.com/turin-dev/aven/internal/config"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/tarx"
)

var (
	instanceRe = regexp.MustCompile(`^ins_[a-z2-7]{4,40}$`)
	projectRe  = regexp.MustCompile(`^[a-zA-Z0-9_]{1,64}$`)
	targetRe   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?\.[a-zA-Z0-9]([a-zA-Z0-9_-]{0,62}[a-zA-Z0-9])?\.(?:aven|keel)\.internal$`)
	uploadIDRe = regexp.MustCompile(`^[a-f0-9]{24}$`)
)

// TCP proxy 공개 포트는 1024 이상만 받는다(80/443/22 같은 시스템 포트를 forwarder가 가로채지 못하게).
const minTCPPublicPort = 1024

// 업로드 한도. 볼륨은 크므로 넉넉히, 소스는 tarx 한도와 같다.
const (
	maxVolumeUpload = 64 << 30
	maxSourceUpload = tarx.MaxBytes
	maxUploads      = 4
	uploadIdle      = 15 * time.Minute
)

// ValidateTCPProxy는 tcpproxy.ensure 요청을 검사한다.
func ValidateTCPProxy(r agentprotocol.TCPProxyRequest) error {
	if !instanceRe.MatchString(r.InstanceID) {
		return fmt.Errorf("instance %q is not allowed", r.InstanceID)
	}
	if !projectRe.MatchString(r.ProjectID) || !projectRe.MatchString(r.EnvID) {
		return errors.New("project/environment id is not allowed")
	}
	if !networkRe.MatchString(r.Network) {
		return fmt.Errorf("network %q is not allowed (only aven environment networks)", r.Network)
	}
	if !targetRe.MatchString(r.Target) {
		return fmt.Errorf("tcp proxy target %q is not allowed (only <service>.<env>.aven.internal)", r.Target)
	}
	if strings.HasSuffix(r.Target, ".keel.internal") {
		compat.NoteLegacyDNS()
	}
	if r.PublicPort < minTCPPublicPort || r.PublicPort > 65535 {
		return fmt.Errorf("public port must be %d..65535", minTCPPublicPort)
	}
	if r.ContainerPort < 1 || r.ContainerPort > 65535 {
		return errors.New("container port must be 1..65535")
	}
	return nil
}

func tcpName(instanceID string) (next, prev string) {
	base := strings.TrimPrefix(instanceID, "ins_")
	return "aven-tcp-" + base, "keel-tcp-" + base
}

// bindPrefix는 publish 주소 앞에 붙는 bind 주소다(agent 설정; 서버는 정하지 못한다).
func (h *Handler) tcpPublish(public, container int) string {
	pub := fmt.Sprintf("%d:%d", public, container)
	if h.TCPBind != "" {
		host := h.TCPBind
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		pub = host + ":" + pub
	}
	return pub
}

func (h *Handler) tcpEnsure(ctx context.Context, r agentprotocol.TCPProxyRequest) error {
	if err := ValidateTCPProxy(r); err != nil {
		return err
	}
	next, prev := tcpName(r.InstanceID)
	sig := fmt.Sprintf("%d:%d:%s", r.PublicPort, r.ContainerPort, r.Target)
	states, err := h.Docker.InspectMany(ctx, []string{next, prev})
	if err != nil {
		return err
	}
	name := next
	if states[prev] != nil && states[next] == nil {
		name = prev
		compat.NoteLegacyResource("tcp-proxy", "container "+prev, "container "+next)
	}
	if st := states[name]; st != nil {
		if st.Labels[node.LabelManaged] != "true" || st.Labels[node.LabelRole] != node.RoleTCPProxy {
			return fmt.Errorf("container %s is not a aven TCP proxy", name)
		}
		got := st.Labels["aven.tcp"]
		if got == "" {
			got = st.Labels["keel.tcp"]
		}
		if got == sig && st.Running {
			return nil
		}
		if err := h.Docker.Remove(ctx, name, 1); err != nil {
			return err
		}
	}
	return h.Docker.Run(ctx, node.RunSpec{
		Name: name, Image: node.TCPForwardImage, Network: r.Network, Restart: "unless-stopped",
		Labels: map[string]string{node.LabelManaged: "true", node.LabelRole: node.RoleTCPProxy, node.LabelInstance: r.InstanceID,
			node.LabelEnv: r.EnvID, node.LabelProject: r.ProjectID, "aven.tcp": sig, "keel.tcp": sig},
		Publish: []string{h.tcpPublish(r.PublicPort, r.ContainerPort)},
		Cmd: []string{fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr", r.ContainerPort),
			fmt.Sprintf("TCP:%s:%d", r.Target, r.ContainerPort)},
	})
}

func (h *Handler) tcpRemove(ctx context.Context, r agentprotocol.TCPRemoveRequest) error {
	if !instanceRe.MatchString(r.InstanceID) {
		return fmt.Errorf("instance %q is not allowed", r.InstanceID)
	}
	next, prev := tcpName(r.InstanceID)
	states, err := h.Docker.InspectMany(ctx, []string{next, prev})
	if err != nil {
		return err
	}
	var last error
	for _, name := range []string{next, prev} {
		st := states[name]
		if st == nil {
			continue
		}
		if st.Labels[node.LabelManaged] != "true" || st.Labels[node.LabelRole] != node.RoleTCPProxy {
			return fmt.Errorf("container %s is not a aven TCP proxy", name)
		}
		if err := h.Docker.Remove(ctx, name, 1); err != nil {
			last = err
		}
	}
	return last
}

// keelLabels는 aven.* 라벨만 남긴다(이벤트로 서버에 보내는 라벨).
func keelLabels(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		if (strings.HasPrefix(k, "aven.") || strings.HasPrefix(k, "keel.")) && labelKeyRe.MatchString(k) && labelValRe.MatchString(v) {
			out[k] = v
		}
	}
	return out
}

// ---------------- 큰 데이터: 볼륨 export / 업로드 ----------------

// chunkWriter는 쓰인 데이터를 MaxChunkBytes 조각으로 나눠 stream으로 보낸다.
type chunkWriter struct {
	ctx    context.Context
	stream func(any)
	seq    int
	n      int64
}

func (w *chunkWriter) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 {
		if err := w.ctx.Err(); err != nil {
			return total - len(p), err
		}
		n := len(p)
		if n > agentprotocol.MaxChunkBytes {
			n = agentprotocol.MaxChunkBytes
		}
		w.stream(agentprotocol.DataChunk{Seq: w.seq, Data: append([]byte(nil), p[:n]...)})
		w.seq++
		w.n += int64(n)
		p = p[n:]
	}
	return total, nil
}

type upload struct {
	kind    string
	volume  string
	image   string
	db      *agentprotocol.DBToolRequest
	path    string
	f       *os.File
	seq     int
	bytes   int64
	limit   int64
	touched time.Time
	busy    bool
}

type uploads struct {
	mu sync.Mutex
	m  map[string]*upload
}

func (h *Handler) uploadDir() string { return filepath.Join(h.WorkDir, "uploads") }

func (u *uploads) drop(id string) {
	u.mu.Lock()
	up := u.m[id]
	delete(u.m, id)
	u.mu.Unlock()
	if up != nil {
		up.f.Close()
		os.Remove(up.path)
	}
}

// DropUploads는 진행 중인 업로드를 모두 버린다(연결이 끊겼을 때).
func (h *Handler) DropUploads() {
	h.up.mu.Lock()
	ids := make([]string, 0, len(h.up.m))
	for id := range h.up.m {
		ids = append(ids, id)
	}
	h.up.mu.Unlock()
	for _, id := range ids {
		h.up.drop(id)
	}
}

func (h *Handler) uploadBegin(r agentprotocol.UploadBeginRequest) (*agentprotocol.UploadBeginResult, error) {
	var limit int64
	switch r.Kind {
	case agentprotocol.UploadVolume:
		if !volumeRe.MatchString(r.Volume) || r.DB != nil || r.Image != "" {
			return nil, fmt.Errorf("volume %q is not allowed", r.Volume)
		}
		limit = maxVolumeUpload
	case agentprotocol.UploadImage:
		if r.Volume != "" || r.DB != nil || !keelImageRe.MatchString(r.Image) {
			return nil, fmt.Errorf("image %q is not allowed (only keel/<service>:<tag>)", r.Image)
		}
		limit = maxImageUpload
	case agentprotocol.UploadSource:
		if r.Volume != "" || r.DB != nil || r.Image != "" {
			return nil, errors.New("volume is only for kind=volume")
		}
		limit = maxSourceUpload
	case agentprotocol.UploadDBTool:
		if r.DB == nil || r.Volume != "" || r.Image != "" {
			return nil, errors.New("db.restore needs a database target (and no volume)")
		}
		if err := validateDBTool(*r.DB, true); err != nil {
			return nil, err
		}
		limit = maxVolumeUpload
	default:
		return nil, fmt.Errorf("upload kind %q is not allowed", r.Kind)
	}
	// 오래 멈춘 업로드를 먼저 치운다.
	h.up.mu.Lock()
	var stale []string
	for id, u := range h.up.m {
		if !u.busy && time.Since(u.touched) > uploadIdle {
			stale = append(stale, id)
		}
	}
	h.up.mu.Unlock()
	for _, id := range stale {
		h.up.drop(id)
	}
	h.up.mu.Lock()
	if len(h.up.m) >= maxUploads {
		h.up.mu.Unlock()
		return nil, errors.New("too many uploads in progress")
	}
	h.up.mu.Unlock()
	id, err := agentprotocol.Secret()
	if err != nil {
		return nil, err
	}
	id = id[:24]
	if err := os.MkdirAll(h.uploadDir(), 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(h.uploadDir(), id+".part")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	h.up.mu.Lock()
	if h.up.m == nil {
		h.up.m = map[string]*upload{}
	}
	h.up.m[id] = &upload{kind: r.Kind, volume: r.Volume, image: r.Image, path: path, f: f, limit: limit, touched: time.Now(), db: r.DB}
	h.up.mu.Unlock()
	return &agentprotocol.UploadBeginResult{UploadID: id}, nil
}

func (h *Handler) uploadChunk(r agentprotocol.UploadChunkRequest) error {
	if !uploadIDRe.MatchString(r.UploadID) {
		return errors.New("invalid upload")
	}
	if len(r.Data) == 0 || len(r.Data) > agentprotocol.MaxChunkBytes {
		return fmt.Errorf("chunk must be 1..%d bytes", agentprotocol.MaxChunkBytes)
	}
	h.up.mu.Lock()
	u := h.up.m[r.UploadID]
	if u == nil || u.busy {
		h.up.mu.Unlock()
		return errors.New("unknown upload")
	}
	if r.Seq != u.seq {
		h.up.mu.Unlock()
		return fmt.Errorf("unexpected chunk %d (want %d)", r.Seq, u.seq)
	}
	if u.bytes+int64(len(r.Data)) > u.limit {
		h.up.mu.Unlock()
		h.up.drop(r.UploadID)
		return errors.New("upload too large")
	}
	u.busy = true
	h.up.mu.Unlock()
	_, err := u.f.Write(r.Data)
	h.up.mu.Lock()
	u.busy = false
	u.touched = time.Now()
	if err == nil {
		u.seq++
		u.bytes += int64(len(r.Data))
	}
	h.up.mu.Unlock()
	if err != nil {
		h.up.drop(r.UploadID)
	}
	return err
}

func (h *Handler) uploadFinish(ctx context.Context, r agentprotocol.UploadFinishRequest) (any, error) {
	if !uploadIDRe.MatchString(r.UploadID) {
		return nil, errors.New("invalid upload")
	}
	h.up.mu.Lock()
	u := h.up.m[r.UploadID]
	if u == nil || u.busy {
		h.up.mu.Unlock()
		return nil, errors.New("unknown upload")
	}
	u.busy = true
	h.up.mu.Unlock()
	defer h.up.drop(r.UploadID)
	if err := u.f.Sync(); err != nil {
		return nil, err
	}
	if r.Bytes != u.bytes {
		return nil, fmt.Errorf("upload incomplete: received %d bytes, expected %d", u.bytes, r.Bytes)
	}
	f, err := os.Open(u.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	switch u.kind {
	case agentprotocol.UploadVolume:
		if err := h.volumeIdle(ctx, u.volume); err != nil {
			return nil, err
		}
		return nil, h.Docker.ImportVolume(ctx, u.volume, "keel-import-"+r.UploadID[:12], f)
	case agentprotocol.UploadDBTool:
		return nil, h.dbRestore(ctx, *u.db, f)
	case agentprotocol.UploadImage:
		return nil, h.loadImage(ctx, f, u.image)
	default: // source
		id, err := agentprotocol.Secret()
		if err != nil {
			return nil, err
		}
		id = id[:24]
		dir := filepath.Join(h.WorkDir, id)
		src := filepath.Join(dir, "src")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		if err := tarx.Extract(f, src); err != nil {
			os.RemoveAll(dir)
			return nil, fmt.Errorf("extract upload: %w", err)
		}
		res := &agentprotocol.FetchResult{Workspace: id}
		root, err := safeJoin(src, r.RootDir)
		if err != nil {
			os.RemoveAll(dir)
			return nil, err
		}
		res.ConfigName, res.Config = readConfig(root)
		return res, nil
	}
}

// volumeIdle은 볼륨을 쓰고 있는 실행 중 컨테이너가 없는지 확인한다(import는 볼륨을 비우므로).
func (h *Handler) volumeIdle(ctx context.Context, volume string) error {
	managed, err := h.Docker.ListManaged(ctx)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(managed))
	for n := range managed {
		names = append(names, n)
	}
	if len(names) == 0 {
		return nil
	}
	states, err := h.Docker.InspectMany(ctx, names)
	if err != nil {
		return err
	}
	for n, st := range states {
		if !st.Running {
			continue
		}
		for _, v := range st.Volumes {
			if v == volume {
				return fmt.Errorf("volume %s is in use by running container %s", volume, n)
			}
		}
	}
	return nil
}

func readConfig(root string) (name string, data []byte) {
	for _, n := range config.Files {
		f, err := os.Open(filepath.Join(root, n))
		if err != nil {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(f, 1<<20))
		f.Close()
		if err == nil {
			return n, b
		}
		break
	}
	return "", nil
}

// ---------------- 서버로 보내는 요청 (agent → server) ----------------

type upLink struct {
	write   func(context.Context, agentprotocol.Message) error
	mu      sync.Mutex
	pending map[string]chan agentprotocol.Message
}

type upState struct {
	mu   sync.Mutex
	link *upLink
}

func (h *Handler) setLink(l *upLink) {
	h.link.mu.Lock()
	h.link.link = l
	h.link.mu.Unlock()
}

func (h *Handler) getLink() *upLink {
	h.link.mu.Lock()
	defer h.link.mu.Unlock()
	return h.link.link
}

func (l *upLink) deliver(m agentprotocol.Message) {
	l.mu.Lock()
	ch := l.pending[m.ID]
	l.mu.Unlock()
	if ch != nil {
		select {
		case ch <- m:
		default:
		}
	}
}

// AskServer는 연결된 서버에 요청 하나를 보내고 답을 기다린다. 연결이 없으면 오류.
func (h *Handler) AskServer(ctx context.Context, op string, req, res any) error {
	l := h.getLink()
	if l == nil {
		return errors.New("not connected to the server")
	}
	id, err := agentprotocol.Secret()
	if err != nil {
		return err
	}
	id = id[:24]
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	ch := make(chan agentprotocol.Message, 1)
	l.mu.Lock()
	if l.pending == nil {
		l.pending = map[string]chan agentprotocol.Message{}
	}
	l.pending[id] = ch
	l.mu.Unlock()
	defer func() { l.mu.Lock(); delete(l.pending, id); l.mu.Unlock() }()
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = l.write(wctx, agentprotocol.Message{ID: id, Operation: op, Payload: payload, Up: true})
	cancel()
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case m := <-ch:
		if m.Error != "" {
			return errors.New(m.Error)
		}
		if res != nil && len(m.Payload) > 0 {
			return json.Unmarshal(m.Payload, res)
		}
		return nil
	}
}

// ---------------- wake handler ----------------

// WakeTimeout은 서버가 서비스를 깨우는 데 기다리는 최대 시간이다.
const WakeTimeout = 40 * time.Second

// ValidateWakeResult는 서버가 알려준 wake 대상을 검사한다.
func ValidateWakeResult(r agentprotocol.WakeResult) error {
	if !containerRe.MatchString(r.Container) {
		return fmt.Errorf("container %q is not allowed", r.Container)
	}
	if !networkRe.MatchString(r.Network) {
		return fmt.Errorf("network %q is not allowed", r.Network)
	}
	if r.Port < 1 || r.Port > 65535 {
		return errors.New("invalid port")
	}
	return nil
}

// WakeHandler는 이 노드의 keel-proxy가 잠든 서비스로 보낸 요청을 받는다.
// 서버에 깨워 달라고 요청한 뒤, 깨어난 컨테이너(이 노드의 aven 앱)로 요청을 넘긴다.
// 같은 socket에서 와일드카드 도메인의 on-demand TLS 허가 확인(proxy.TLSAskHost)도 받는다.
func (h *Handler) WakeHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if proxy.HandleTLSAsk(w, r, h.TLSAllowed) {
			return
		}
		host := strings.ToLower(r.Host)
		if hh, _, err := net.SplitHostPort(host); err == nil {
			host = hh
		}
		if len(host) > 253 || !domainRe.MatchString(host) {
			http.Error(w, "invalid host", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), WakeTimeout)
		defer cancel()
		var res agentprotocol.WakeResult
		if err := h.AskServer(ctx, agentprotocol.OpWake, agentprotocol.WakeRequest{Host: host}, &res); err != nil {
			http.Error(w, "service is waking up, please retry: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		if err := ValidateWakeResult(res); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		states, err := h.Docker.InspectMany(ctx, []string{res.Container})
		st := states[res.Container]
		if err != nil || st == nil || st.Labels[node.LabelManaged] != "true" || st.Labels[node.LabelRole] != node.RoleApp || !st.Running {
			http.Error(w, "no running replica", http.StatusBadGateway)
			return
		}
		ips := st.IP(res.Network)
		if len(ips) == 0 {
			http.Error(w, "no running replica", http.StatusBadGateway)
			return
		}
		rp := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: net.JoinHostPort(ips[0], strconv.Itoa(res.Port))})
		orig := rp.Director
		rp.Director = func(req *http.Request) {
			orig(req)
			req.Host = r.Host
		}
		rp.ServeHTTP(w, r)
	})
}

// setRouteHosts는 서버가 마지막으로 적용한 라우트의 호스트와 on-demand TLS 허가 여부를 기억한다.
// 호스트의 라우트 중 하나라도 Unverified면(소유 확인 전 신규 도메인) 허가하지 않는다.
func (h *Handler) setRouteHosts(routes []proxy.Route) {
	hosts := map[string]bool{}
	for _, r := range routes {
		for _, host := range r.Hosts {
			ok, seen := hosts[host]
			if !seen {
				ok = true
			}
			hosts[host] = ok && !r.Unverified
		}
	}
	h.hostsMu.Lock()
	h.hosts = hosts
	h.hostsMu.Unlock()
}

// TLSAllowed는 이 노드의 keel-proxy가 host의 인증서를 on-demand로 받아도 되는지다:
// 서버가 이 노드에 적용한 라우트의 호스트(정확히 같은 것이 먼저, 없으면 와일드카드)이고, 그 라우트가 Unverified가 아닐 때만.
// agent에는 DB가 없으므로 확인 여부는 서버가 라우트에 실어 보낸다(확인되면 서버가 Unverified 없이 다시 보낸다).
func (h *Handler) TLSAllowed(host string) bool {
	h.hostsMu.Lock()
	defer h.hostsMu.Unlock()
	if ok, seen := h.hosts[host]; seen {
		return ok
	}
	w := proxy.WildcardFor(host)
	return w != "" && h.hosts[w]
}

// WakeRoute는 잠든 서비스의 라우트가 가리키는 upstream이다(keel-proxy와 같이 쓰는 볼륨의 unix socket).
const WakeUpstream = proxy.WakeDial

// lineTail은 helper 컨테이너 stderr의 마지막 몇 줄을 모은다(오류 메시지용).
type lineTail struct {
	mu    sync.Mutex
	lines []string
}

func (t *lineTail) add(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, s)
	if len(t.lines) > 8 {
		t.lines = t.lines[1:]
	}
}

func (t *lineTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, " | ")
}

// portFree는 port.free다: 이 노드의 TCP 프록시 bind 주소에서 port를 지금 listen할 수 있는지 본다(바로 닫는다).
func (h *Handler) portFree(r agentprotocol.PortFreeRequest) (agentprotocol.PortFreeResult, error) {
	if r.Port < minTCPPublicPort || r.Port > 65535 {
		return agentprotocol.PortFreeResult{}, fmt.Errorf("port must be %d..65535", minTCPPublicPort)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(h.TCPBind, strconv.Itoa(r.Port)))
	if err != nil {
		return agentprotocol.PortFreeResult{Free: false}, nil
	}
	ln.Close()
	return agentprotocol.PortFreeResult{Free: true}, nil
}
