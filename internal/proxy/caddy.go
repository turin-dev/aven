// Package proxy는 노드의 Caddy를 관리한다.
// 라우트는 매번 DB 기준으로 통째로 다시 만들어 Admin API(/load)로 넣는다. 그래서 부분 수정 때문에 생기는 꼬임이 없다.
package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/edge"
)

const (
	ContainerName       = "aven-proxy"
	LegacyContainerName = "keel-proxy"
)

// wake handler용 unix socket. aven-server와 aven-proxy가 같은 docker volume을 나눠 쓴다
// (컨테이너 → 호스트 TCP는 호스트 방화벽에 막힐 수 있어서 쓰지 않는다).
// 예전 설치의 keel-proxy가 이미 떠 있으면 그 컨테이너·볼륨·마운트 경로를 그대로 쓴다.
const (
	WakeVolume       = "aven-proxy-wake"
	LegacyWakeVolume = "keel-proxy-wake"
	WakeDir          = "/run/aven"
	LegacyWakeDir    = "/run/keel"
	WakeSocket       = WakeDir + "/wake.sock"
	// AdminSocket은 Caddy 관리 API다. 공유 볼륨 안의 unix socket이라 같은 네트워크의 컨테이너(aven 앱, 외부 네트워크)는
	// 닿지 못하고 호스트의 aven-server/aven-agent(root)만 쓴다. 예전에는 컨테이너 안 0.0.0.0:2019였는데 Host 헤더만 검사했다.
	AdminSocket = WakeDir + "/caddy-admin.sock"
	WakeDial    = "unix/" + WakeSocket

	DataVolume         = "aven-caddy-data"
	LegacyDataVolume   = "keel-caddy-data"
	ConfigVolume       = "aven-caddy-config"
	LegacyConfigVolume = "keel-caddy-config"
)

// 와일드카드 도메인(*.example.com)의 on-demand TLS 허가 확인(ask).
// Caddy의 http permission 모듈은 unix socket으로 요청하지 못하므로, keel-proxy 컨테이너 안의 loopback에만 여는
// 작은 서버(TLSAskListen)가 요청을 wake socket으로 넘긴다. Host를 TLSAskHost로 바꿔 보내므로
// wake handler(aven-server / aven-agent)가 일반 wake 요청과 구분한다. 외부(:80/:443)에서는 닿지 않는다.
const (
	TLSAskListen = "127.0.0.1:5380"
	TLSAskHost   = "aven-tls-ask.invalid"
	TLSAskPath   = "/api/internal/tls-ask"
)

// IsWildcard는 호스트가 와일드카드(*.example.com)인지다.
func IsWildcard(host string) bool { return strings.HasPrefix(host, "*.") }

// WildcardFor는 host를 잡는 와일드카드 이름이다("a.example.com" → "*.example.com").
// 와일드카드는 한 단계 하위 이름만 잡는다(Caddy host matcher와 같다). 해당 없으면 "".
func WildcardFor(host string) string {
	i := strings.IndexByte(host, '.')
	if i <= 0 || IsWildcard(host) || !strings.Contains(host[i+1:], ".") {
		return ""
	}
	return "*" + host[i:]
}

// HandleTLSAsk는 r이 TLS ask 요청이면 처리하고 true를 돌려준다. allowed(domain)이 true일 때만 200이다.
func HandleTLSAsk(w http.ResponseWriter, r *http.Request, allowed func(host string) bool) bool {
	host := strings.ToLower(r.Host)
	if h, _, err := splitHostPort(host); err == nil {
		host = h
	}
	if host != TLSAskHost {
		return false
	}
	if r.Method != http.MethodGet || r.URL.Path != TLSAskPath {
		http.Error(w, "not found", http.StatusNotFound)
		return true
	}
	d := strings.ToLower(strings.TrimSuffix(r.URL.Query().Get("domain"), "."))
	if d == "" || len(d) > 253 || IsWildcard(d) || strings.Trim(d, "abcdefghijklmnopqrstuvwxyz0123456789.-") != "" {
		http.Error(w, "invalid domain", http.StatusBadRequest)
		return true
	}
	if !allowed(d) {
		http.Error(w, "domain is not configured", http.StatusNotFound)
		return true
	}
	w.WriteHeader(http.StatusOK)
	return true
}

type TLSMode string

const (
	TLSAuto     TLSMode = "auto"     // Let's Encrypt 자동 발급, http→https 리다이렉트
	TLSInternal TLSMode = "internal" // Caddy 내부 CA (테스트용)
	TLSOff      TLSMode = "off"      // HTTP만
)

type Config struct {
	Image     string // 기본 caddy:2-alpine
	HTTPPort  int    // 호스트에 publish할 포트
	HTTPSPort int
	// MetricsHosts가 있으면 :80에서 Host가 이 값(노드의 사설 주소)이고 경로가 /metrics인 요청에 Prometheus 지표를 준다
	// (사설·tailnet 출발지만). Traefik이 같은 방식으로 지표를 냈으므로 기존 수집 대상(<사설 IP>:80)을 그대로 쓸 수 있다.
	MetricsHosts []string
	AdminPort    int    // 테스트용: 0이 아니면 관리 API를 unix socket 대신 127.0.0.1:AdminPort(TCP)로 부른다
	BindIP       string // 비우면 모든 주소(IPv4+IPv6). 테스트 때는 127.0.0.1
	TLS          TLSMode
	ACMEEmail    string
	DockerBin    string

	// OTLPEndpoint/TraceToken이 있으면 Caddy의 trace exporter를 aven-server 수신기로 보낸다(edge span).
	OTLPEndpoint string
	TraceToken   string
}

// Route는 호스트(들)의 규칙 하나다. 같은 호스트에 경로가 다른 Route가 여러 개 있을 수 있다.
// 호스트 단위 설정(TLS, Redirect, Auth, TrustProto)은 그 호스트를 가진 Route 중 처음 값이 있는 것을 쓴다.
type Route struct {
	Hosts       []string
	Upstreams   []string // "container:port". 여러 개면 round-robin으로 나눈다(replicas)
	Path        string   // 비면(또는 "/", "/*") 나머지 전부. "/x/*"는 접두어, 그 밖에는 정확히 일치
	StripPrefix bool     // 접두어 규칙에서 접두어를 떼고 넘긴다
	// PathsOnly면 이 호스트는 경로 규칙에 맞는 요청만 보내고 나머지는 404다(기본 서비스가 없어서 생기는 502 대신).
	// omitempty: 옛 agent는 모르는 필드를 거부하므로 쓰지 않을 때는 보내지 않는다.
	PathsOnly bool `json:",omitempty"`
	// SetHeaders는 upstream 요청에 덮어쓸 헤더다(aven-server 자신으로 가는 라우트의 게이트웨이 비밀용, 로컬 노드 전용).
	SetHeaders map[string]string `json:",omitempty"`

	TLS        TLSMode // 비면 전역 기본값
	TrustProto bool    // 앞단의 X-Forwarded-Proto를 upstream에 그대로 전달
	Redirect   *Redirect
	Auth       *BasicAuth

	// Trace면 이 라우트의 요청마다 edge span을 만들고 traceparent를 upstream에 넘긴다. TraceAttributes는 span 속성이다.
	Trace           bool
	TraceAttributes map[string]string
	// Edge는 호스트를 가진 서비스 인스턴스의 엣지 설정(CDN/WAF/규칙)이다. 있으면 요청이 엣지 엔진을 거친다(edge.go).
	InstanceID string         `json:",omitempty"`
	Edge       *edge.Settings `json:",omitempty"`

	// CheckToken이 있으면 이 호스트의 DomainCheckPath 요청에 "aven-domain-check=<token>"으로 답한다(도메인 소유 확인, netcheck).
	// 예전 /.well-known/keel/domain-check 도 같은 토큰으로 답한다.
	// auth, 리다이렉트, 엣지, 경로 규칙보다 먼저 처리한다. omitempty: 옛 agent는 모르는 필드를 거부한다(서버가 빼고 다시 보낸다).
	CheckToken string `json:",omitempty"`
	// Unverified면 아직 소유가 확인되지 않은 신규 도메인이다. 자동 TLS 호스트는 미리 인증서를 받지 않고 on-demand 정책에 두며,
	// TLS ask(TLSAllowed)는 이 호스트(와일드카드면 그 하위 이름)를 허가하지 않는다. 확인되면 서버가 이 값 없이 다시 보낸다.
	Unverified bool `json:",omitempty"`
}

// DomainCheckPath는 도메인 소유 확인 경로다(netcheck.CheckPath와 같다).
const DomainCheckPath = "/.well-known/aven/domain-check"

// LegacyDomainCheckPath는 예전 확인 경로다(netcheck.LegacyCheckPath와 같다).
const LegacyDomainCheckPath = "/.well-known/keel/domain-check"

// DomainCheckBody는 DomainCheckPath의 응답 본문이다(netcheck.CheckBody와 같다).
func DomainCheckBody(token string) string { return "aven-domain-check=" + token }

// LegacyDomainCheckBody는 예전 확인 본문이다(netcheck.LegacyCheckBody와 같다).
func LegacyDomainCheckBody(token string) string { return "keel-domain-check=" + token }

// domainCheckHandler는 확인 경로 요청에만 토큰으로 답하고 나머지는 다음 handler로 넘긴다.
// 본문에 새 값과 예전 값을 함께 넣어, 아직 keel-domain-check= 를 찾는 확인도 통과한다.
func domainCheckHandler(token string) map[string]any {
	return map[string]any{"handler": "subroute", "routes": []map[string]any{{
		"match": []map[string]any{{"path": []string{DomainCheckPath, LegacyDomainCheckPath}}},
		"handle": []map[string]any{{"handler": "static_response", "status_code": 200,
			"body":    DomainCheckBody(token) + "\n" + LegacyDomainCheckBody(token),
			"headers": map[string][]string{"Content-Type": {"text/plain; charset=utf-8"}, "Cache-Control": {"no-store"}}}},
		"terminal": true,
	}}}
}

type Redirect struct {
	To        string
	Permanent bool
}

// BasicAuth의 Hash는 bcrypt 해시 문자열이다.
type BasicAuth struct {
	User string
	Hash string
}

// AccessLogger는 Caddy access log의 logger 이름이다. stdout으로 JSON을 내보내고 aven이 `docker logs`로 따라간다.
const AccessLogger = "http.log.access.aven"

type Caddy struct {
	cfg  Config
	edge EdgeSink // nil이면 엣지 설정을 무시한다(엔진 socket을 열지 못한 경우)

	adminMu   sync.Mutex
	adminPath string // AdminSocket의 호스트 경로(WakeVolume의 mountpoint 아래)
	client    *http.Client

	nameMu    sync.Mutex
	haveNames bool
	lay       runtimeNames
}

func New(cfg Config) *Caddy {
	if cfg.Image == "" {
		cfg.Image = "caddy:2-alpine"
	}
	if cfg.DockerBin == "" {
		cfg.DockerBin = "docker"
	}
	return &Caddy{cfg: cfg}
}

func (c *Caddy) adminURL(path string) string {
	if c.cfg.AdminPort != 0 {
		return fmt.Sprintf("http://127.0.0.1:%d%s", c.cfg.AdminPort, path)
	}
	return "http://aven-proxy-admin" + path
}

// adminClient는 관리 API용 HTTP 클라이언트다. socket의 호스트 경로를 처음 쓸 때 찾는다.
func (c *Caddy) adminClient(ctx context.Context) (*http.Client, error) {
	c.adminMu.Lock()
	defer c.adminMu.Unlock()
	if c.client != nil {
		return c.client, nil
	}
	if c.cfg.AdminPort != 0 {
		c.client = &http.Client{}
		return c.client, nil
	}
	if c.adminPath == "" {
		out, err := exec.CommandContext(ctx, c.cfg.DockerBin, "volume", "inspect", "--format", "{{.Mountpoint}}", c.Resolve(ctx).WakeVolume).Output()
		if err != nil {
			return nil, fmt.Errorf("locate caddy admin socket: %w", err)
		}
		c.adminPath = filepath.Join(strings.TrimSpace(string(out)), filepath.Base(AdminSocket))
	}
	sock := c.adminPath
	c.client = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
		DisableKeepAlives: true,
	}}
	return c.client, nil
}

// EnsureRunning은 aven-proxy 컨테이너가 없으면 띄운다. 이미 떠 있는 keel-proxy는 지우지 않는다.
func (c *Caddy) EnsureRunning(ctx context.Context, network string) error {
	n := c.Resolve(ctx)
	if _, err := exec.CommandContext(ctx, c.cfg.DockerBin, "volume", "inspect", n.WakeVolume).CombinedOutput(); err != nil {
		if out, err := exec.CommandContext(ctx, c.cfg.DockerBin, "volume", "create", "--label", "aven.managed=true", "--label", "keel.managed=true", n.WakeVolume).CombinedOutput(); err != nil {
			return fmt.Errorf("create wake volume: %v: %s", err, out)
		}
	}
	// wake volume이 없는 예전 proxy는 다시 만든다(인증서는 caddy data 볼륨에 남는다).
	if mounts, err := exec.CommandContext(ctx, c.cfg.DockerBin, "inspect", "--format", "{{range .Mounts}}{{.Name}} {{end}}", n.Container).CombinedOutput(); err == nil &&
		!strings.Contains(string(mounts), n.WakeVolume) {
		exec.CommandContext(ctx, c.cfg.DockerBin, "rm", "-f", n.Container).Run()
	}
	// 관리 API를 TCP(2019)로 publish하던 예전 proxy는 다시 만든다. --resume이 autosave의 예전 admin 설정(0.0.0.0:2019)을
	// 다시 읽지 않도록 autosave도 지운다(라우트는 aven이 곧 다시 넣는다).
	if ports, err := exec.CommandContext(ctx, c.cfg.DockerBin, "inspect", "--format", "{{json .HostConfig.PortBindings}}", n.Container).CombinedOutput(); err == nil &&
		strings.Contains(string(ports), `"2019/tcp"`) {
		exec.CommandContext(ctx, c.cfg.DockerBin, "rm", "-f", n.Container).Run()
		exec.CommandContext(ctx, c.cfg.DockerBin, "run", "--rm", "--network", "none", "-v", n.ConfigVolume+":/config", "--entrypoint", "rm", c.cfg.Image,
			"-f", "/config/caddy/autosave.json").Run()
	}
	// tracing exporter 설정(OTEL_*)이 다른 proxy도 다시 만든다. 환경 변수는 컨테이너를 만들 때만 정해진다.
	if _, err := exec.CommandContext(ctx, c.cfg.DockerBin, "inspect", n.Container).CombinedOutput(); err == nil && !c.traceEnvMatches(ctx) {
		exec.CommandContext(ctx, c.cfg.DockerBin, "rm", "-f", n.Container).Run()
	}
	out, err := exec.CommandContext(ctx, c.cfg.DockerBin, "inspect", "--format", "{{.State.Running}}", n.Container).CombinedOutput()
	if err == nil && strings.TrimSpace(string(out)) == "true" {
		return c.waitAdmin(ctx)
	}
	if err == nil { // 있는데 멈춰 있음
		if out, err := exec.CommandContext(ctx, c.cfg.DockerBin, "start", n.Container).CombinedOutput(); err != nil {
			return fmt.Errorf("start proxy: %v: %s", err, out)
		}
		return c.waitAdmin(ctx)
	}
	// admin은 공유 볼륨 안의 unix socket에서만 연다(네트워크로는 닿지 않는다).
	bootstrap := `{"admin":{"listen":"` + c.adminListenPath() + `"}}`
	args := []string{"run", "-d", "--name", n.Container, "--network", network, "--restart", "unless-stopped",
		"--label", "aven.managed=true", "--label", "keel.managed=true", "--label", "aven.role=proxy", "--label", "keel.role=proxy",
		"--add-host", "host.docker.internal:host-gateway",
		"-p", c.publish(c.cfg.HTTPPort, "80"),
		"-p", c.publish(c.cfg.HTTPSPort, "443"),
		"-p", c.publish(c.cfg.HTTPSPort, "443/udp"),
		"-v", n.DataVolume + ":/data", "-v", n.ConfigVolume + ":/config", "-v", n.WakeVolume + ":" + n.WakeDir,
		"-e", "CADDY_BOOTSTRAP",
		"-e", "OTEL_EXPORTER_OTLP_ENDPOINT", "-e", "OTEL_EXPORTER_OTLP_PROTOCOL", "-e", "OTEL_EXPORTER_OTLP_HEADERS",
		"--user", "0:0", "--entrypoint", "sh", c.cfg.Image,
		"-c", `printf '%s' "$CADDY_BOOTSTRAP" > /config/bootstrap.json && exec caddy run --config /config/bootstrap.json --resume`,
	}
	cmd := exec.CommandContext(ctx, c.cfg.DockerBin, args...)
	// 비밀(토큰)이 프로세스 인자에 보이지 않게 환경 변수로 넘긴다. 같은 이름은 뒤의 값이 이긴다.
	cmd.Env = append(append(os.Environ(), "CADDY_BOOTSTRAP="+bootstrap), c.traceEnv()...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("run proxy: %v: %s", err, out)
	}
	return c.waitAdmin(ctx)
}

func (c *Caddy) publish(hostPort int, ctr string) string {
	if c.cfg.BindIP == "" {
		return fmt.Sprintf("%d:%s", hostPort, ctr)
	}
	return fmt.Sprintf("%s:%d:%s", c.cfg.BindIP, hostPort, ctr)
}

func (c *Caddy) waitAdmin(ctx context.Context) error {
	deadline := time.Now().Add(20 * time.Second)
	for {
		if err := c.Ping(ctx); err == nil {
			return nil
		} else if time.Now().After(deadline) {
			return fmt.Errorf("caddy admin not reachable: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (c *Caddy) do(ctx context.Context, method, path string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, method, c.adminURL(path), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client, err := c.adminClient(ctx)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("caddy %s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func (c *Caddy) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return c.do(ctx, http.MethodGet, "/config/", nil)
}

type pathRule struct {
	path   string // 정규화된 규칙("" = catch-all)
	strip  bool
	routes []string
	ups    []string

	trace      bool
	traceAttrs map[string]string
}

type hostCfg struct {
	host       string
	tls        TLSMode
	trustProto bool
	redirect   *Redirect
	auth       *BasicAuth
	rules      []pathRule
	instanceID string
	edge       *edge.Settings
	useEdge    bool // 이 호스트의 요청을 엣지 엔진으로 보낸다
	pathsOnly  bool // 규칙에 없는 경로는 404(Route.PathsOnly)
	setHeaders map[string]string
	viaEdge    bool   // origin 서버 라우트(엣지 엔진이 보낸 요청)
	checkToken string // Route.CheckToken
	unverified bool   // Route.Unverified
	wakeDir    string // 비면 WakeDir. 예전 proxy는 /run/keel
}

// normPath는 경로 규칙을 정규화한다. "", "/", "/*"는 catch-all("")이다.
func normPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" || p == "/*" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

// pathMatchers는 Caddy path matcher 값이다. "/api/*"는 "/api"와 "/api/…"를 모두 잡는다.
func pathMatchers(p string) []string {
	if base, ok := strings.CutSuffix(p, "/*"); ok {
		return []string{base, base + "/*"}
	}
	return []string{p}
}

func pathPrefix(p string) (string, bool) { return strings.CutSuffix(p, "/*") }

// ruleLess는 더 구체적인 경로가 먼저 오게 한다: 정확히 일치 → 접두어(긴 것 먼저) → catch-all.
func ruleLess(a, b pathRule) bool {
	rank := func(r pathRule) int {
		switch {
		case r.path == "":
			return 2
		case strings.Contains(r.path, "*"):
			return 1
		}
		return 0
	}
	if ra, rb := rank(a), rank(b); ra != rb {
		return ra < rb
	}
	if len(a.path) != len(b.path) {
		return len(a.path) > len(b.path)
	}
	return a.path < b.path
}

func (c *Caddy) effectiveTLS(h *hostCfg) TLSMode {
	if h.tls != "" {
		return h.tls
	}
	if c.cfg.TLS == "" {
		return TLSAuto
	}
	return c.cfg.TLS
}

func upstreamList(upstreams []string) []map[string]any {
	ups := []map[string]any{}
	for _, u := range upstreams {
		ups = append(ups, map[string]any{"dial": u})
	}
	return ups
}

// StreamCloseDelay는 설정을 다시 적용할 때 WebSocket 같은 업그레이드된 연결을 닫기 전에 기다리는 시간이다.
// Caddy 기본값(0)은 적용 즉시 모두 닫는다(1001 Going Away). aven은 배포마다 설정을 적용하므로, 그대로면 원격 agent
// 연결이 끊겨 그 순간 원격 노드로 이미지를 보내던 배포가 실패한다. 이 시간 안에 끝나지 않은 연결만 닫힌다.
const StreamCloseDelay = "30m"

func reverseProxy(upstreams []string) map[string]any {
	rp := map[string]any{"handler": "reverse_proxy", "upstreams": upstreamList(upstreams), "stream_close_delay": StreamCloseDelay}
	if len(upstreams) > 1 {
		// 여러 replica: round-robin, 실패한 upstream은 잠시 빼고 다른 replica로 재시도한다.
		rp["load_balancing"] = map[string]any{
			"selection_policy": map[string]any{"policy": "round_robin"},
			"try_duration":     "5s",
		}
		rp["health_checks"] = map[string]any{"passive": map[string]any{"fail_duration": "10s", "max_fails": 1}}
	}
	return rp
}

const origProtoHeader = "X-Aven-Orig-Proto"

func (h *hostCfg) handlers() []map[string]any {
	var out []map[string]any
	// 도메인 확인 응답은 auth·리다이렉트·엣지·경로 규칙(PathsOnly 404)보다 먼저다. origin 서버(viaEdge)에는 넣지 않는다:
	// :443 서버가 엣지로 넘기기 전에 이미 답한다.
	if h.checkToken != "" && !h.viaEdge {
		out = append(out, domainCheckHandler(h.checkToken))
	}
	if h.auth != nil && h.auth.User != "" && h.auth.Hash != "" {
		out = append(out, map[string]any{"handler": "authentication", "providers": map[string]any{"http_basic": map[string]any{
			"hash":  map[string]any{"algorithm": "bcrypt"},
			"realm": "restricted",
			"accounts": []map[string]any{{"username": h.auth.User,
				"password": base64.StdEncoding.EncodeToString([]byte(h.auth.Hash))}},
		}}})
	}
	if h.redirect != nil {
		to := h.redirect.To
		if !strings.Contains(to, "://") {
			to = "https://" + to
		}
		status := 307
		if h.redirect.Permanent {
			status = 308
		}
		out = append(out, map[string]any{"handler": "static_response", "status_code": status,
			"headers": map[string][]string{"Location": {strings.TrimRight(to, "/") + "{http.request.uri}"}}})
		return out
	}
	if h.useEdge {
		return append(out, h.edgeForward())
	}
	// 엣지를 거치는 호스트는 origin 라우트(viaEdge)에서 경로 규칙별로 span을 만든다.
	mkRP := func(ups []string, traced bool, attrs map[string]string) []map[string]any {
		var handlers []map[string]any
		if traced {
			handlers = append(handlers, traceHandlers(attrs)...)
		}
		rp := reverseProxy(ups)
		if len(h.setHeaders) > 0 {
			set := map[string][]string{}
			for k, v := range h.setHeaders {
				set[k] = []string{v}
			}
			rp["headers"] = map[string]any{"request": map[string]any{"set": set}}
		} else if h.viaEdge {
			rp["headers"] = originHeaders()
		} else if h.trustProto {
			// Caddy는 신뢰하지 않는 클라이언트의 X-Forwarded-Proto를 덮어쓴다. 원래 값을 다른 헤더에 보관했다가 되돌린다.
			rp["headers"] = map[string]any{"request": map[string]any{
				"set":    map[string][]string{"X-Forwarded-Proto": {"{http.request.header." + origProtoHeader + "}"}},
				"delete": []string{origProtoHeader},
			}}
		}
		return append(handlers, rp)
	}
	if h.trustProto && !h.viaEdge {
		out = append(out, map[string]any{"handler": "headers", "request": map[string]any{
			"set": map[string][]string{origProtoHeader: {"{http.request.header.X-Forwarded-Proto}"}}}})
	}
	if len(h.rules) == 1 && h.rules[0].path == "" {
		return append(out, mkRP(h.rules[0].ups, h.rules[0].trace, h.rules[0].traceAttrs)...)
	}
	sort.Slice(h.rules, func(i, j int) bool { return ruleLess(h.rules[i], h.rules[j]) })
	var sub []map[string]any
	hasDefault := false
	for _, r := range h.rules {
		handle := []map[string]any{}
		if r.strip {
			if pre, ok := pathPrefix(r.path); ok && pre != "" {
				handle = append(handle, map[string]any{"handler": "rewrite", "strip_path_prefix": pre})
			}
		}
		handle = append(handle, mkRP(r.ups, r.trace, r.traceAttrs)...)
		rt := map[string]any{"handle": handle, "terminal": true}
		if r.path == "" {
			hasDefault = true
		} else {
			rt["match"] = []map[string]any{{"path": pathMatchers(r.path)}}
		}
		sub = append(sub, rt)
	}
	if !hasDefault {
		status, body := 502, "no upstream"
		if h.pathsOnly {
			status, body = 404, "not found"
		}
		sub = append(sub, map[string]any{"handle": []map[string]any{{"handler": "static_response", "status_code": status, "body": body}}, "terminal": true})
	}
	return append(out, map[string]any{"handler": "subroute", "routes": sub})
}

// metricsSourceRanges는 /metrics를 받을 출발지다: 사설망, Tailscale(CGNAT, ULA), 루프백.
var metricsSourceRanges = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "127.0.0.0/8", "fc00::/7", "::1/128"}

// Build는 라우트 목록으로 Caddy 전체 설정 JSON을 만든다.
func (c *Caddy) Build(routes []Route) ([]byte, error) {
	byHost := map[string]*hostCfg{}
	for _, r := range routes {
		for _, host := range r.Hosts {
			h := byHost[host]
			if h == nil {
				h = &hostCfg{host: host, wakeDir: c.socketDir()}
				byHost[host] = h
			}
			if h.edge == nil && r.Edge != nil && r.Edge.Enabled() {
				h.edge, h.instanceID = r.Edge, r.InstanceID
			}
			if h.tls == "" {
				h.tls = r.TLS
			}
			h.trustProto = h.trustProto || r.TrustProto
			h.pathsOnly = h.pathsOnly || r.PathsOnly
			h.unverified = h.unverified || r.Unverified
			if h.checkToken == "" {
				h.checkToken = r.CheckToken
			}
			if h.setHeaders == nil && len(r.SetHeaders) > 0 {
				h.setHeaders = r.SetHeaders
			}
			if h.redirect == nil {
				h.redirect = r.Redirect
			}
			if h.auth == nil {
				h.auth = r.Auth
			}
			if len(r.Upstreams) > 0 {
				p := normPath(r.Path)
				dup := false
				for _, e := range h.rules {
					if e.path == p {
						dup = true // 같은 경로가 둘이면 먼저 온 것(정렬된 입력)을 쓴다
					}
				}
				if !dup {
					h.rules = append(h.rules, pathRule{path: p, strip: r.StripPrefix, ups: r.Upstreams, trace: r.Trace, traceAttrs: r.TraceAttributes})
				}
			}
		}
	}
	names := make([]string, 0, len(byHost))
	for n, h := range byHost {
		if h.redirect != nil || len(h.rules) > 0 {
			names = append(names, n)
		}
	}
	// 구체적인 호스트 라우트가 와일드카드보다 먼저 와야 한다(Caddy는 순서대로 처음 맞는 terminal 라우트를 쓴다).
	sort.Slice(names, func(i, j int) bool {
		if wi, wj := IsWildcard(names[i]), IsWildcard(names[j]); wi != wj {
			return !wi
		}
		return names[i] < names[j]
	})
	var tlsRoutes, plainRoutes []map[string]any
	var acmeHosts, internalHosts, onDemandHosts []string
	var originRoutes []map[string]any
	for _, n := range names {
		h := byHost[n]
		if c.edge != nil && h.edge != nil && h.redirect == nil {
			// 엣지 엔진을 거친 요청은 origin 서버에서 원래 경로 규칙대로 컨테이너로 간다.
			o := *h
			o.viaEdge, o.auth = true, nil
			originRoutes = append(originRoutes, map[string]any{"match": []map[string]any{{"host": []string{n}}}, "handle": o.handlers(), "terminal": true})
			h.useEdge = true
		}
		rt := map[string]any{"match": []map[string]any{{"host": []string{n}}}, "handle": h.handlers(), "terminal": true}
		switch c.effectiveTLS(h) {
		case TLSOff:
			plainRoutes = append(plainRoutes, rt)
		case TLSInternal:
			// 내부 CA는 와일드카드 인증서를 바로 발급한다.
			tlsRoutes = append(tlsRoutes, rt)
			internalHosts = append(internalHosts, n)
		default:
			tlsRoutes = append(tlsRoutes, rt)
			if IsWildcard(n) || h.unverified {
				// 와일드카드 인증서는 DNS challenge가 필요하다. 대신 하위 이름마다 첫 TLS handshake 때 발급한다(on-demand, ask로 허가).
				// 소유가 확인되지 않은 신규 호스트(Unverified)도 미리 받지 않는다: ask가 확인된 뒤에만 허가한다.
				// legacy·확인된 호스트는 지금처럼 acmeHosts(관리 정책)에 남는다.
				onDemandHosts = append(onDemandHosts, n)
			} else {
				acmeHosts = append(acmeHosts, n)
			}
		}
	}
	logs := map[string]any{"default_logger_name": strings.TrimPrefix(AccessLogger, "http.log.access.")}
	if tlsRoutes == nil {
		tlsRoutes = []map[string]any{}
	}
	if plainRoutes == nil {
		plainRoutes = []map[string]any{}
	}
	// 어떤 라우트에도 맞지 않는 호스트(중지/삭제된 서비스 등)는 Caddy 기본값인 빈 200 대신 404를 준다.
	// :80 서버는 TLS 호스트가 없을 때만 붙인다. 있으면 Caddy가 뒤에 덧붙이는 http→https 리다이렉트를 가리게 된다.
	if len(c.cfg.MetricsHosts) > 0 {
		plainRoutes = append([]map[string]any{{
			"match": []map[string]any{{"host": c.cfg.MetricsHosts, "path": []string{"/metrics"},
				"remote_ip": map[string]any{"ranges": metricsSourceRanges}}},
			"handle":   []map[string]any{{"handler": "metrics"}},
			"terminal": true,
		}}, plainRoutes...)
	}
	nTLS, nPlain := len(tlsRoutes), len(plainRoutes)
	notFound := map[string]any{"handle": []map[string]any{{"handler": "static_response", "status_code": 404, "body": "not found"}}, "terminal": true}
	tlsRoutes = append(tlsRoutes, notFound)
	if nTLS == 0 {
		plainRoutes = append(plainRoutes, notFound)
	}
	tlsServer := map[string]any{"listen": []string{":443"}, "routes": tlsRoutes, "logs": logs}
	if len(onDemandHosts) > 0 {
		// *.example.com 자체의 인증서는 미리 받지 않는다(http→https 리다이렉트는 그대로).
		tlsServer["automatic_https"] = map[string]any{"skip_certificates": onDemandHosts}
	}
	plainServer := map[string]any{"listen": []string{":80"}, "routes": plainRoutes, "logs": logs,
		"automatic_https": map[string]any{"disable": true}}
	servers := map[string]any{}
	if c.cfg.TLS == TLSOff {
		servers["aven"] = plainServer
		if nTLS > 0 {
			servers["aven-tls"] = tlsServer
		}
	} else {
		servers["aven"] = tlsServer
		if nPlain > 0 {
			servers["aven-http"] = plainServer
		}
	}
	if len(originRoutes) > 0 {
		servers[EdgeOriginServer] = originServer(c.socketDir(), originRoutes)
	}
	apps := map[string]any{}
	var policies []map[string]any
	acme := func() map[string]any {
		issuer := map[string]any{"module": "acme"}
		if c.cfg.ACMEEmail != "" {
			issuer["email"] = c.cfg.ACMEEmail
		}
		return issuer
	}
	// Caddy는 이름에 맞는 첫 automation policy를 쓴다. 구체 호스트 policy가 와일드카드 policy보다
	// 먼저 와야 *.example.com(auto)과 a.example.com(internal)이 함께 있을 때 a가 ACME로 새지 않는다.
	var internalExact, internalWild []string
	for _, n := range internalHosts {
		if IsWildcard(n) {
			internalWild = append(internalWild, n)
		} else {
			internalExact = append(internalExact, n)
		}
	}
	if len(acmeHosts) > 0 {
		policies = append(policies, map[string]any{"subjects": acmeHosts, "issuers": []any{acme()}})
	}
	if len(internalExact) > 0 {
		policies = append(policies, map[string]any{"subjects": internalExact, "issuers": []any{map[string]any{"module": "internal"}}})
	}
	if len(onDemandHosts) > 0 {
		policies = append(policies, map[string]any{"subjects": onDemandHosts, "issuers": []any{acme()}, "on_demand": true})
	}
	if len(internalWild) > 0 {
		policies = append(policies, map[string]any{"subjects": internalWild, "issuers": []any{map[string]any{"module": "internal"}}})
	}
	if len(policies) > 0 {
		automation := map[string]any{"policies": policies}
		if len(onDemandHosts) > 0 {
			// Caddy 2.8+의 http permission 모듈. GET <endpoint>?domain=<이름>이 200이면 발급한다.
			automation["on_demand"] = map[string]any{"permission": map[string]any{"module": "http", "endpoint": "http://" + TLSAskListen + TLSAskPath}}
			servers["aven-tls-ask"] = map[string]any{
				"listen":          []string{TLSAskListen},
				"automatic_https": map[string]any{"disable": true},
				"routes": []map[string]any{{"handle": []map[string]any{{
					"handler":   "reverse_proxy",
					"upstreams": []map[string]any{{"dial": c.wakeDial()}},
					"headers":   map[string]any{"request": map[string]any{"set": map[string][]string{"Host": {TLSAskHost}}}},
				}}}},
			}
		}
		apps["tls"] = map[string]any{"automation": automation}
	}
	httpApp := map[string]any{"servers": servers}
	if len(c.cfg.MetricsHosts) > 0 {
		// 호스트별 요청 지표(caddy_http_requests_total{host=...}): 대시보드가 앱(도메인)별로 나눠 본다.
		httpApp["metrics"] = map[string]any{"per_host": true}
	}
	apps["http"] = httpApp
	cfg := map[string]any{
		"admin": map[string]any{"listen": c.adminListenPath()},
		"logging": map[string]any{"logs": map[string]any{
			"default": map[string]any{"exclude": []string{AccessLogger}},
			"access": map[string]any{
				"writer":  map[string]any{"output": "stdout"},
				"encoder": map[string]any{"format": "json"},
				"include": []string{AccessLogger},
			},
		}},
		"apps": apps,
	}
	return json.Marshal(cfg)
}

// Apply는 전체 설정을 원자적으로 교체한다. 실패하면 Caddy가 이전 설정을 유지한다.
func (c *Caddy) Apply(ctx context.Context, routes []Route) error {
	if c.edge != nil {
		c.edge.Configure(EdgePolicies(routes))
	}
	body, err := c.Build(routes)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	err = c.do(ctx, http.MethodPost, "/load", body)
	if err != nil && hasTracedRoute(routes) {
		return c.applyUntraced(ctx, routes, err)
	}
	return err
}

// AccessLog는 Caddy access log 한 줄을 API 형식으로 바꾼 것이다.
type AccessLog struct {
	T          string  `json:"t"`
	Method     string  `json:"method"`
	Host       string  `json:"host"`
	Path       string  `json:"path"`
	Status     int     `json:"status"`
	DurationMS float64 `json:"duration_ms"`
	Bytes      int64   `json:"bytes"`
	ClientIP   string  `json:"client_ip"`
	UserAgent  string  `json:"user_agent"`
	TraceID    string  `json:"trace_id,omitempty"` // edge가 trace한 요청이면 그 trace ID
}

// ParseAccessLog는 Caddy JSON access log 한 줄을 해석한다. access log가 아니면 ok=false.
func ParseAccessLog(line string) (AccessLog, bool) {
	var r struct {
		Logger  string  `json:"logger"`
		TS      float64 `json:"ts"`
		Request struct {
			ClientIP string              `json:"client_ip"`
			RemoteIP string              `json:"remote_ip"`
			Method   string              `json:"method"`
			Host     string              `json:"host"`
			URI      string              `json:"uri"`
			Headers  map[string][]string `json:"headers"`
		} `json:"request"`
		Duration float64 `json:"duration"`
		Size     int64   `json:"size"`
		Status   int     `json:"status"`
		TraceID  string  `json:"traceID"`
	}
	if i := strings.IndexByte(line, '{'); i > 0 {
		line = line[i:] // docker logs --timestamps 접두어 제거
	}
	if json.Unmarshal([]byte(line), &r) != nil || r.Logger != AccessLogger {
		return AccessLog{}, false
	}
	host := r.Request.Host
	if h, _, err := splitHostPort(host); err == nil {
		host = h
	}
	ip := r.Request.ClientIP
	if ip == "" {
		ip = r.Request.RemoteIP
	}
	ua := ""
	if v := r.Request.Headers["User-Agent"]; len(v) > 0 {
		ua = v[0]
	}
	sec := int64(r.TS)
	t := time.Unix(sec, int64((r.TS-float64(sec))*1e9)).UTC()
	return AccessLog{T: t.Format(time.RFC3339Nano), Method: r.Request.Method, Host: strings.ToLower(host), Path: r.Request.URI,
		Status: r.Status, DurationMS: r.Duration * 1000, Bytes: r.Size, ClientIP: ip, UserAgent: ua, TraceID: r.TraceID}, true
}

func splitHostPort(h string) (string, string, error) {
	if !strings.Contains(h, ":") || strings.HasSuffix(h, "]") {
		return strings.Trim(h, "[]"), "", nil
	}
	i := strings.LastIndexByte(h, ':')
	return strings.Trim(h[:i], "[]"), h[i+1:], nil
}

// FollowAccessLogs는 keel-proxy 컨테이너 stdout을 따라가며 access log를 넘긴다. ctx가 끝날 때까지 재시도한다.
func (c *Caddy) FollowAccessLogs(ctx context.Context, out func(AccessLog)) {
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, c.cfg.DockerBin, "logs", "-f", "--since", "1s", c.Resolve(ctx).Container)
		cmd.WaitDelay = 2 * time.Second
		so, err := cmd.StdoutPipe()
		if err == nil && cmd.Start() == nil {
			sc := bufio.NewScanner(so)
			sc.Buffer(make([]byte, 64*1024), 1024*1024)
			for sc.Scan() {
				if l, ok := ParseAccessLog(sc.Text()); ok {
					out(l)
				}
			}
			cmd.Wait()
		}
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
}

// Connect는 proxy 컨테이너를 환경 네트워크에 붙인다.
func (c *Caddy) Connect(ctx context.Context, network string) error {
	network = c.resolveNetwork(ctx, network)
	out, err := exec.CommandContext(ctx, c.cfg.DockerBin, "network", "connect", network, c.Resolve(ctx).Container).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "already") {
		return fmt.Errorf("connect proxy to %s: %v: %s", network, err, out)
	}
	return nil
}
