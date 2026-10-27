package proxy

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// 관리 API는 네트워크에 열지 않는다(같은 네트워크의 컨테이너가 Host 헤더를 꾸며 설정을 바꿀 수 있었다).
func TestAdminNotOnNetwork(t *testing.T) {
	b, err := New(Config{TLS: TLSAuto}).Build([]Route{{Hosts: []string{"a.example.com"}, Upstreams: []string{"keel-a-1:3000"}}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(b)
	if strings.Contains(cfg, "2019") || strings.Contains(cfg, "0.0.0.0") {
		t.Fatalf("admin API must only listen on the unix socket: %s", cfg)
	}
}

func TestBuildAuto(t *testing.T) {
	c := New(Config{TLS: TLSAuto, ACMEEmail: "me@example.com"})
	b, err := c.Build([]Route{{Hosts: []string{"b.example.com"}, Upstreams: []string{"keel-b-1:80"}}, {Hosts: []string{"a.example.com"}, Upstreams: []string{"keel-a-1:3000"}}})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"listen":[":443"]`, `"dial":"keel-a-1:3000"`, `"email":"me@example.com"`, `"admin":{"listen":"unix//run/aven/caddy-admin.sock|0600"}`} {
		if !strings.Contains(s, want) {
			t.Errorf("config missing %s: %s", want, s)
		}
	}
	if strings.Index(s, "a.example.com") > strings.Index(s, "b.example.com") {
		t.Error("routes should be sorted for stable config")
	}
}

func TestBuildOff(t *testing.T) {
	c := New(Config{TLS: TLSOff})
	b, _ := c.Build(nil)
	s := string(b)
	if !strings.Contains(s, `"listen":[":80"]`) || !strings.Contains(s, `"disable":true`) || strings.Contains(s, `"tls"`) {
		t.Errorf("unexpected: %s", s)
	}
}

func TestBuildReplicasAndAccessLog(t *testing.T) {
	c := New(Config{TLS: TLSOff})
	b, err := c.Build([]Route{
		{Hosts: []string{"lb.example.com"}, Upstreams: []string{"keel-web-1:8080", "keel-web-2:8080"}},
		{Hosts: []string{"single.example.com"}, Upstreams: []string{"keel-one-1:80"}},
		{Hosts: []string{"empty.example.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Logging struct {
			Logs map[string]struct {
				Include []string          `json:"include"`
				Exclude []string          `json:"exclude"`
				Writer  map[string]string `json:"writer"`
			} `json:"logs"`
		} `json:"logging"`
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Logs   map[string]string `json:"logs"`
					Routes []struct {
						Match  []map[string][]string `json:"match"`
						Handle []map[string]any      `json:"handle"`
					} `json:"routes"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	srv := cfg.Apps.HTTP.Servers["aven"]
	if srv.Logs["default_logger_name"] != "aven" {
		t.Errorf("access logs not enabled: %v", srv.Logs)
	}
	acc := cfg.Logging.Logs["access"]
	if acc.Writer["output"] != "stdout" || len(acc.Include) != 1 || acc.Include[0] != AccessLogger {
		t.Errorf("access log sink: %+v", acc)
	}
	if ex := cfg.Logging.Logs["default"].Exclude; len(ex) != 1 || ex[0] != AccessLogger {
		t.Errorf("default log should exclude access: %v", ex)
	}
	if len(srv.Routes) != 3 { // 2 + 마지막 404 catch-all
		t.Fatalf("routes without upstreams must be skipped: %d", len(srv.Routes))
	}
	if last := srv.Routes[2]; len(last.Match) != 0 || last.Handle[0]["status_code"] != float64(404) {
		t.Errorf("unmatched hosts must get 404 catch-all: %+v", last)
	}
	lb := srv.Routes[0].Handle[0]
	if ups := lb["upstreams"].([]any); len(ups) != 2 {
		t.Errorf("want 2 upstreams, got %v", ups)
	}
	if lb["load_balancing"] == nil || lb["health_checks"] == nil {
		t.Errorf("replicas need lb + passive health: %v", lb)
	}
	if srv.Routes[1].Handle[0]["load_balancing"] != nil {
		t.Error("single upstream should not set lb")
	}
}

func TestParseAccessLog(t *testing.T) {
	line := `2026-10-16T12:00:00.000000000Z {"level":"info","ts":1760616000.5,"logger":"http.log.access.aven","msg":"handled request","request":{"remote_ip":"::1","client_ip":"2a01::5","proto":"HTTP/1.1","method":"GET","host":"App.Example.com:8080","uri":"/x?y=1","headers":{"User-Agent":["curl/8"]}},"duration":0.0125,"size":42,"status":200}`
	l, ok := ParseAccessLog(line)
	if !ok {
		t.Fatal("not parsed")
	}
	if l.Host != "app.example.com" || l.Method != "GET" || l.Path != "/x?y=1" || l.Status != 200 || l.Bytes != 42 ||
		l.ClientIP != "2a01::5" || l.UserAgent != "curl/8" || l.DurationMS < 12 || l.DurationMS > 13 || !strings.HasPrefix(l.T, "2025-10-16T12:00:00.5") {
		t.Errorf("unexpected: %+v", l)
	}
	if _, ok := ParseAccessLog(`{"logger":"http.handlers.reverse_proxy","msg":"x"}`); ok {
		t.Error("non-access log accepted")
	}
}

func buildCfg(t *testing.T, c *Caddy, routes []Route) (map[string]any, string) {
	t.Helper()
	b, err := c.Build(routes)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m, string(b)
}

func servers(m map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for k, v := range m["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any) {
		out[k] = v.(map[string]any)
	}
	return out
}

func TestPathRulesOrdering(t *testing.T) {
	c := New(Config{TLS: TLSOff})
	_, s := buildCfg(t, c, []Route{
		{Hosts: []string{"h.example.com"}, Upstreams: []string{"keel-web-1:3000"}},
		{Hosts: []string{"h.example.com"}, Path: "/api/*", Upstreams: []string{"keel-api-1:8080"}, StripPrefix: true},
		{Hosts: []string{"h.example.com"}, Path: "/api/v1/*", Upstreams: []string{"keel-api2-1:8080"}},
		{Hosts: []string{"h.example.com"}, Path: "/healthz", Upstreams: []string{"keel-api-1:8080"}},
	})
	order := []string{`"/healthz"`, `"/api/v1/*"`, `"/api/*"`, `keel-web-1:3000`}
	last := -1
	for _, o := range order {
		i := strings.Index(s, o)
		if i < 0 || i < last {
			t.Fatalf("%s out of order in %s", o, s)
		}
		last = i
	}
	for _, want := range []string{`"strip_path_prefix":"/api"`, `"subroute"`, `"/api","/api/*"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s: %s", want, s)
		}
	}
	// deterministic
	_, s2 := buildCfg(t, c, []Route{
		{Hosts: []string{"h.example.com"}, Path: "/healthz", Upstreams: []string{"keel-api-1:8080"}},
		{Hosts: []string{"h.example.com"}, Path: "/api/v1/*", Upstreams: []string{"keel-api2-1:8080"}},
		{Hosts: []string{"h.example.com"}, Path: "/api/*", Upstreams: []string{"keel-api-1:8080"}, StripPrefix: true},
		{Hosts: []string{"h.example.com"}, Upstreams: []string{"keel-web-1:3000"}},
	})
	if s != s2 {
		t.Error("config depends on route input order")
	}
}

func TestPathRulesWithoutDefault502(t *testing.T) {
	c := New(Config{TLS: TLSOff})
	_, s := buildCfg(t, c, []Route{{Hosts: []string{"h.example.com"}, Path: "/x", Upstreams: []string{"keel-a-1:1"}}})
	if !strings.Contains(s, `"status_code":502`) {
		t.Errorf("want 502 fallback: %s", s)
	}
}

func TestPerDomainTLSModes(t *testing.T) {
	// 전역 auto: off 도메인은 :80 서버에 들어가고 리다이렉트가 없다.
	c := New(Config{TLS: TLSAuto})
	m, s := buildCfg(t, c, []Route{
		{Hosts: []string{"a.example.com"}, Upstreams: []string{"keel-a-1:1"}},
		{Hosts: []string{"tunnel.example.com"}, Upstreams: []string{"keel-b-1:1"}, TLS: TLSOff},
		{Hosts: []string{"int.example.com"}, Upstreams: []string{"keel-c-1:1"}, TLS: TLSInternal},
	})
	sv := servers(m)
	if len(sv) != 2 || sv["aven"]["listen"].([]any)[0] != ":443" || sv["aven-http"]["listen"].([]any)[0] != ":80" {
		t.Fatalf("servers: %s", s)
	}
	if !strings.Contains(string(mustJSON(sv["aven-http"])), "tunnel.example.com") || strings.Contains(string(mustJSON(sv["aven-http"])), "a.example.com") {
		t.Errorf("off host must be only in http server: %s", s)
	}
	if sv["aven-http"]["automatic_https"].(map[string]any)["disable"] != true {
		t.Error("http server must not auto-redirect")
	}
	if !strings.Contains(s, `"module":"internal"`) || !strings.Contains(s, `"module":"acme"`) {
		t.Errorf("want both issuers: %s", s)
	}
	// 전역 off: auto 도메인만 TLS 서버
	c = New(Config{TLS: TLSOff})
	m, _ = buildCfg(t, c, []Route{
		{Hosts: []string{"a.example.com"}, Upstreams: []string{"keel-a-1:1"}},
		{Hosts: []string{"s.example.com"}, Upstreams: []string{"keel-b-1:1"}, TLS: TLSAuto},
	})
	sv = servers(m)
	if sv["aven"]["listen"].([]any)[0] != ":80" || sv["aven-tls"]["listen"].([]any)[0] != ":443" {
		t.Errorf("servers: %v", sv)
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// 중지된 서비스의 호스트처럼 맞는 라우트가 없으면 Caddy 기본값(빈 200) 대신 404여야 한다.
func TestUnmatchedHost404CatchAll(t *testing.T) {
	lastIs404 := func(srv map[string]any) bool {
		rs := srv["routes"].([]any)
		last := rs[len(rs)-1].(map[string]any)
		h := last["handle"].([]any)[0].(map[string]any)
		return last["match"] == nil && h["status_code"] == float64(404)
	}
	// 전역 off, 라우트 없음: :80 서버에 404만
	m, _ := buildCfg(t, New(Config{TLS: TLSOff}), nil)
	sv := servers(m)
	if len(sv) != 1 || !lastIs404(sv["aven"]) {
		t.Fatalf("off/empty: %v", sv)
	}
	// 전역 auto: :443에 404, TLS 호스트가 있으니 :80(off 호스트)에는 붙이지 않는다(http→https 리다이렉트 보존)
	m, s := buildCfg(t, New(Config{TLS: TLSAuto}), []Route{
		{Hosts: []string{"a.example.com"}, Upstreams: []string{"keel-a-1:1"}},
		{Hosts: []string{"tunnel.example.com"}, Upstreams: []string{"keel-b-1:1"}, TLS: TLSOff},
	})
	sv = servers(m)
	if !lastIs404(sv["aven"]) || lastIs404(sv["aven-http"]) {
		t.Errorf("auto: %s", s)
	}
	// 전역 auto, 라우트 없음: :80 서버를 새로 만들지 않는다
	m, _ = buildCfg(t, New(Config{TLS: TLSAuto}), nil)
	if sv = servers(m); len(sv) != 1 || !lastIs404(sv["aven"]) {
		t.Errorf("auto/empty: %v", sv)
	}
}

func TestRedirectAndAuthAndTrust(t *testing.T) {
	c := New(Config{TLS: TLSAuto})
	_, s := buildCfg(t, c, []Route{
		{Hosts: []string{"www.example.com"}, Redirect: &Redirect{To: "example.com", Permanent: true}},
		{Hosts: []string{"tmp.example.com"}, Redirect: &Redirect{To: "https://example.com/x/", Permanent: false}},
		{Hosts: []string{"p.example.com"}, Upstreams: []string{"keel-a-1:1"}, Auth: &BasicAuth{User: "bob", Hash: "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ0123"}},
		{Hosts: []string{"t.example.com"}, Upstreams: []string{"keel-a-1:1"}, TLS: TLSOff, TrustProto: true},
	})
	for _, want := range []string{`"status_code":308`, `"status_code":307`, `https://example.com{http.request.uri}`, `https://example.com/x{http.request.uri}`,
		`"http_basic"`, `"algorithm":"bcrypt"`, `"username":"bob"`, `X-Aven-Orig-Proto`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s: %s", want, s)
		}
	}
	if strings.Contains(s, "$2a$10$abc") {
		t.Error("hash must be base64 encoded")
	}
}

func TestWildcardFor(t *testing.T) {
	for in, want := range map[string]string{
		"a.example.com": "*.example.com", "x.y.example.com": "*.y.example.com",
		"example.com": "", "*.example.com": "", "localhost": "",
	} {
		if got := WildcardFor(in); got != want {
			t.Errorf("WildcardFor(%q) = %q, want %q", in, got, want)
		}
	}
}

type wildcardCfg struct {
	Apps struct {
		HTTP struct {
			Servers map[string]struct {
				Listen         []string       `json:"listen"`
				AutomaticHTTPS map[string]any `json:"automatic_https"`
				Routes         []struct {
					Match  []map[string][]string `json:"match"`
					Handle []map[string]any      `json:"handle"`
				} `json:"routes"`
			} `json:"servers"`
		} `json:"http"`
		TLS struct {
			Automation struct {
				OnDemand *struct {
					Permission map[string]string `json:"permission"`
				} `json:"on_demand"`
				Policies []struct {
					Subjects []string `json:"subjects"`
					OnDemand bool     `json:"on_demand"`
					Issuers  []map[string]any
				} `json:"policies"`
			} `json:"automation"`
		} `json:"tls"`
	} `json:"apps"`
}

func TestBuildWildcardAutoTLS(t *testing.T) {
	c := New(Config{TLS: TLSAuto})
	b, err := c.Build([]Route{
		{Hosts: []string{"*.example.com"}, Upstreams: []string{"keel-wild-1:80"}},
		{Hosts: []string{"api.example.com"}, Upstreams: []string{"keel-api-1:80"}},
		{Hosts: []string{"zzz.other.com"}, Upstreams: []string{"keel-z-1:80"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var cfg wildcardCfg
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	srv := cfg.Apps.HTTP.Servers["aven"]
	var hosts []string
	for _, r := range srv.Routes {
		if len(r.Match) == 0 {
			continue // 마지막 404 catch-all
		}
		hosts = append(hosts, r.Match[0]["host"][0])
	}
	// 구체적인 호스트가 와일드카드보다 먼저
	if strings.Join(hosts, ",") != "api.example.com,zzz.other.com,*.example.com" {
		t.Errorf("route order: %v", hosts)
	}
	if skip, _ := srv.AutomaticHTTPS["skip_certificates"].([]any); len(skip) != 1 || skip[0] != "*.example.com" {
		t.Errorf("skip_certificates: %v", srv.AutomaticHTTPS)
	}
	auto := cfg.Apps.TLS.Automation
	if auto.OnDemand == nil || auto.OnDemand.Permission["module"] != "http" || auto.OnDemand.Permission["endpoint"] != "http://"+TLSAskListen+TLSAskPath {
		t.Fatalf("on_demand: %+v", auto.OnDemand)
	}
	var od, acme bool
	for _, p := range auto.Policies {
		if p.OnDemand {
			od = len(p.Subjects) == 1 && p.Subjects[0] == "*.example.com"
		} else if strings.Contains(strings.Join(p.Subjects, ","), "api.example.com") {
			acme = !strings.Contains(strings.Join(p.Subjects, ","), "*")
		}
	}
	if !od || !acme {
		t.Errorf("policies: %+v", auto.Policies)
	}
	ask, ok := cfg.Apps.HTTP.Servers["aven-tls-ask"]
	if !ok || ask.Listen[0] != TLSAskListen || !strings.Contains(string(b), `"Host":["`+TLSAskHost+`"]`) || !strings.Contains(string(b), `"dial":"`+WakeDial+`"`) {
		t.Errorf("ask server: %+v", ask)
	}
}

// Caddy는 처음 맞는 automation policy를 쓴다: auto 와일드카드 아래의 internal 구체 호스트는
// 와일드카드 on-demand(ACME) policy보다 먼저 와야 한다(e2e에서 handshake가 ACME로 새던 버그).
func TestBuildPolicyOrderExactBeforeWildcard(t *testing.T) {
	c := New(Config{TLS: TLSAuto})
	b, err := c.Build([]Route{
		{Hosts: []string{"*.apps.example.com"}, Upstreams: []string{"keel-wild-1:80"}},
		{Hosts: []string{"special.apps.example.com"}, Upstreams: []string{"keel-sp-1:80"}, TLS: TLSInternal},
		{Hosts: []string{"*.int.other.com"}, Upstreams: []string{"keel-i-1:80"}, TLS: TLSInternal},
		{Hosts: []string{"x.int.other.com"}, Upstreams: []string{"keel-x-1:80"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var cfg wildcardCfg
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	// 각 이름에 대해 Caddy처럼 첫 매칭 policy를 고른다.
	first := func(name string) (issuer string, onDemand bool) {
		for _, p := range cfg.Apps.TLS.Automation.Policies {
			for _, s := range p.Subjects {
				if s == name || (IsWildcard(s) && WildcardFor(name) == s) {
					return p.Issuers[0]["module"].(string), p.OnDemand
				}
			}
		}
		return "", false
	}
	for name, want := range map[string]string{
		"special.apps.example.com": "internal", "foo.apps.example.com": "acme",
		"x.int.other.com": "acme", "y.int.other.com": "internal",
	} {
		if got, _ := first(name); got != want {
			t.Errorf("%s: first policy issuer %q, want %q (policies %+v)", name, got, want, cfg.Apps.TLS.Automation.Policies)
		}
	}
	if _, od := first("special.apps.example.com"); od {
		t.Error("special.apps.example.com must not hit the on-demand policy")
	}
}

func TestBuildWildcardInternalAndOff(t *testing.T) {
	c := New(Config{TLS: TLSAuto})
	b, _ := c.Build([]Route{
		{Hosts: []string{"*.int.example.com"}, Upstreams: []string{"keel-a-1:80"}, TLS: TLSInternal},
		{Hosts: []string{"*.plain.example.com"}, Upstreams: []string{"keel-b-1:80"}, TLS: TLSOff},
	})
	var cfg wildcardCfg
	json.Unmarshal(b, &cfg)
	if cfg.Apps.TLS.Automation.OnDemand != nil {
		t.Error("no auto-TLS wildcard: on_demand must not be set")
	}
	if _, ok := cfg.Apps.HTTP.Servers["aven-tls-ask"]; ok {
		t.Error("ask server without on-demand hosts")
	}
	pol := cfg.Apps.TLS.Automation.Policies
	if len(pol) != 1 || pol[0].Subjects[0] != "*.int.example.com" || pol[0].Issuers[0]["module"] != "internal" {
		t.Errorf("internal policy: %+v", pol)
	}
	plain := cfg.Apps.HTTP.Servers["aven-http"]
	if len(plain.Routes) != 1 || plain.Routes[0].Match[0]["host"][0] != "*.plain.example.com" {
		t.Errorf("plain wildcard route: %+v", plain)
	}
}

func TestHandleTLSAsk(t *testing.T) {
	allowed := func(h string) bool { return h == "a.example.com" }
	do := func(host, target string) (bool, int) {
		r := httptest.NewRequest("GET", target, nil)
		r.Host = host
		w := httptest.NewRecorder()
		handled := HandleTLSAsk(w, r, allowed)
		return handled, w.Code
	}
	if h, _ := do("a.example.com", TLSAskPath+"?domain=a.example.com"); h {
		t.Error("normal host must not be handled")
	}
	for target, want := range map[string]int{
		TLSAskPath + "?domain=a.example.com":  200,
		TLSAskPath + "?domain=A.Example.com.": 200,
		TLSAskPath + "?domain=b.example.com":  404,
		TLSAskPath + "?domain=*.example.com":  400,
		TLSAskPath + "?domain=a%20b":          400,
		TLSAskPath:                            400,
		"/other?domain=a.example.com":         404,
	} {
		if h, code := do(TLSAskHost, target); !h || code != want {
			t.Errorf("%s: handled=%v code=%d want %d", target, h, code, want)
		}
	}
}

func TestPathsOnlyHostAnswers404(t *testing.T) {
	b, err := New(Config{TLS: TLSOff}).Build([]Route{{Hosts: []string{"api.example.com"}, Path: "/api/v1/*", Upstreams: []string{"front-1:80"}, PathsOnly: true}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"status_code":404`) || strings.Contains(s, `"status_code":502`) {
		t.Fatalf("unmatched paths of a paths-only host must be 404: %s", s)
	}
	b, _ = New(Config{TLS: TLSOff}).Build([]Route{{Hosts: []string{"api.example.com"}, Path: "/api/*", Upstreams: []string{"keel-a-1:80"}}})
	if !strings.Contains(string(b), `"status_code":502`) {
		t.Fatal("a normal host without a live default service still answers 502")
	}
}

// 옛 agent는 라우트를 strict하게 디코드한다: 새 필드는 쓰지 않을 때 JSON에 나오면 안 된다.
func TestRouteJSONOmitsUnusedNewFields(t *testing.T) {
	b, _ := json.Marshal(Route{Hosts: []string{"a.example.com"}, Upstreams: []string{"keel-a-1:80"}})
	for _, f := range []string{"PathsOnly", "CheckToken", "Unverified"} {
		if strings.Contains(string(b), f) {
			t.Fatalf("unused %s must be omitted: %s", f, b)
		}
	}
}

func TestSetHeadersOverwriteOnUpstreamRequest(t *testing.T) {
	b, err := New(Config{TLS: TLSOff}).Build([]Route{{Hosts: []string{"aven.example.com"}, Upstreams: []string{"unix//run/aven/keel-server.sock"},
		SetHeaders: map[string]string{"X-Keel-Gateway": "s3cret"}}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"set":{"X-Keel-Gateway":["s3cret"]}`) || !strings.Contains(s, `"dial":"unix//run/aven/keel-server.sock"`) {
		t.Fatalf("header must be set (overwriting any client value) on the upstream request: %s", s)
	}
}

func TestMetricsRouteOnPrivateHost(t *testing.T) {
	b, err := New(Config{TLS: TLSAuto, MetricsHosts: []string{"100.64.0.3"}}).Build([]Route{{Hosts: []string{"a.example.com"}, Upstreams: []string{"keel-a-1:80"}}})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Apps struct {
			HTTP struct {
				Metrics map[string]any            `json:"metrics"`
				Servers map[string]map[string]any `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	json.Unmarshal(b, &cfg)
	if cfg.Apps.HTTP.Metrics["per_host"] != true {
		t.Fatalf("per-host metrics: %s", b)
	}
	plain, ok := cfg.Apps.HTTP.Servers["aven-http"]
	if !ok {
		t.Fatalf("a :80 server must exist for /metrics: %s", b)
	}
	s, _ := json.Marshal(plain)
	for _, want := range []string{`"handler":"metrics"`, `"host":["100.64.0.3"]`, `"path":["/metrics"]`, `"100.64.0.0/10"`} {
		if !strings.Contains(string(s), want) {
			t.Errorf("metrics route missing %s: %s", want, s)
		}
	}
	if strings.Contains(string(s), `"status_code":404`) {
		t.Error("the :80 server must not end in a 404 catch-all while TLS hosts need Caddy's http->https redirect")
	}
	b, _ = New(Config{TLS: TLSAuto}).Build(nil)
	if strings.Contains(string(b), `"handler":"metrics"`) {
		t.Error("no metrics route without a private address")
	}
}

// 설정을 다시 적용(배포마다)해도 WebSocket 같은 긴 연결이 바로 끊기지 않아야 한다. 원격 agent는 aven-server로 가는
// WebSocket 하나로 이미지를 받으므로, 끊기면 그 순간 원격 노드로 가던 배포가 "remote node is offline"으로 실패했다.
func TestReverseProxyKeepsStreamsAcrossReload(t *testing.T) {
	for _, r := range []Route{
		{Hosts: []string{"aven.example.com"}, Upstreams: []string{"unix//run/aven/keel-server.sock"}},
		{Hosts: []string{"lb.example.com"}, Upstreams: []string{"keel-web-1:8080", "keel-web-2:8080"}},
	} {
		b, err := New(Config{TLS: TLSOff}).Build([]Route{r})
		if err != nil {
			t.Fatal(err)
		}
		if want := `"stream_close_delay":"` + StreamCloseDelay + `"`; !strings.Contains(string(b), want) {
			t.Errorf("%s: reverse_proxy must keep upgraded streams open across reloads (%s): %s", r.Hosts[0], want, b)
		}
	}
	h := &hostCfg{}
	if h.edgeForward()["stream_close_delay"] != StreamCloseDelay {
		t.Error("edge forward must keep upgraded streams open across reloads")
	}
}

// hostHandlers는 서버 이름 srv에서 host 라우트의 handle 목록이다.
func hostHandlers(t *testing.T, m map[string]any, srv, host string) []any {
	t.Helper()
	s, ok := servers(m)[srv]
	if !ok {
		t.Fatalf("server %s missing", srv)
	}
	for _, r := range s["routes"].([]any) {
		rt := r.(map[string]any)
		ms, _ := rt["match"].([]any)
		if len(ms) == 0 {
			continue
		}
		hs, _ := ms[0].(map[string]any)["host"].([]any)
		if len(hs) == 1 && hs[0] == host {
			return rt["handle"].([]any)
		}
	}
	t.Fatalf("host %s not in server %s", host, srv)
	return nil
}

// isCheckRoute는 handler가 도메인 확인 subroute(token)인지다.
func isCheckRoute(h any, token string) bool {
	m, _ := h.(map[string]any)
	if m == nil || m["handler"] != "subroute" {
		return false
	}
	rs, _ := m["routes"].([]any)
	if len(rs) != 1 {
		return false
	}
	r := rs[0].(map[string]any)
	match, _ := json.Marshal(r["match"])
	handle := r["handle"].([]any)[0].(map[string]any)
	return string(match) == `[{"path":["`+DomainCheckPath+`","`+LegacyDomainCheckPath+`"]}]` && handle["handler"] == "static_response" &&
		handle["body"] == DomainCheckBody(token)+"\n"+LegacyDomainCheckBody(token) && handle["status_code"] == float64(200) && r["terminal"] == true
}

func TestDomainCheckRouteComesFirst(t *testing.T) {
	const tok = "0123456789abcdef0123456789abcdef"
	hash := "$2a$10$abcdefghijklmnopqrstuuOq1Q3b2Yc8b3o0m9i0l2b5n6x7z8a9e"
	routes := []Route{
		{Hosts: []string{"auth.example.com"}, Upstreams: []string{"keel-a-1:80"}, Auth: &BasicAuth{User: "u", Hash: hash}, TrustProto: true, CheckToken: tok},
		{Hosts: []string{"redir.example.com"}, Redirect: &Redirect{To: "example.com", Permanent: true}, CheckToken: tok},
		{Hosts: []string{"paths.example.com"}, Upstreams: []string{"compose-x-1:80"}, Path: "/api/v1/*", PathsOnly: true, CheckToken: tok},
		{Hosts: []string{"edge.example.com"}, Upstreams: []string{"keel-e-1:80"}, InstanceID: "ins_e", Edge: edgeOn(), CheckToken: tok},
		{Hosts: []string{"plain.example.com"}, Upstreams: []string{"keel-p-1:80"}, TLS: TLSOff, TrustProto: true, CheckToken: tok},
		{Hosts: []string{"notoken.example.com"}, Upstreams: []string{"keel-n-1:80"}},
	}
	c := New(Config{TLS: TLSAuto})
	c.SetEdge(&fakeEdge{})
	m, _ := buildCfg(t, c, routes)
	next := map[string]string{
		"auth.example.com":  "authentication",  // basic auth보다 먼저
		"redir.example.com": "static_response", // 리다이렉트보다 먼저
		"paths.example.com": "subroute",        // upstream_paths와 PathsOnly 404보다 먼저
		"edge.example.com":  "reverse_proxy",   // 엣지 엔진으로 보내기 전에
	}
	for host, want := range next {
		hs := hostHandlers(t, m, "aven", host)
		if len(hs) < 2 || !isCheckRoute(hs[0], tok) {
			t.Fatalf("%s: check route is not first: %v", host, hs)
		}
		if got := hs[1].(map[string]any)["handler"]; got != want {
			t.Errorf("%s: handler after check = %v, want %v", host, got, want)
		}
	}
	// 엣지: :443에서 엣지 엔진(unix edge socket)으로 넘기기 전에 답하고, origin 서버에는 넣지 않는다.
	eh := hostHandlers(t, m, "aven", "edge.example.com")
	if b, _ := json.Marshal(eh[1]); !strings.Contains(string(b), EdgeSocket) {
		t.Errorf("edge host should forward to the edge engine after the check: %s", b)
	}
	if oh := hostHandlers(t, m, EdgeOriginServer, "edge.example.com"); isCheckRoute(oh[0], tok) {
		t.Error("origin server must not carry the check route")
	}
	// PathsOnly: 확인 경로는 404 대신 토큰으로 답한다(확인 경로가 경로 규칙 subroute보다 앞).
	if b, _ := json.Marshal(hostHandlers(t, m, "aven", "paths.example.com")[1]); !strings.Contains(string(b), `"status_code":404`) {
		t.Errorf("PathsOnly 404 missing: %s", b)
	}
	// 평문(tls=off, 터널) 서버에서도 맨 앞, X-Forwarded-Proto 보관 handler보다 먼저.
	ph := hostHandlers(t, m, "aven-http", "plain.example.com")
	if !isCheckRoute(ph[0], tok) || ph[1].(map[string]any)["handler"] != "headers" {
		t.Fatalf("plain server: %v", ph)
	}
	// 토큰이 없는 호스트는 예전 그대로
	if nh := hostHandlers(t, m, "aven", "notoken.example.com"); len(nh) != 1 || nh[0].(map[string]any)["handler"] != "reverse_proxy" {
		t.Fatalf("no token: %v", nh)
	}

	// 전역 TLS가 off인 노드(평문 서버가 "aven")
	m, _ = buildCfg(t, New(Config{TLS: TLSOff}), []Route{{Hosts: []string{"a.example.com"}, Upstreams: []string{"keel-a-1:80"}, Auth: &BasicAuth{User: "u", Hash: hash}, CheckToken: tok}})
	if hs := hostHandlers(t, m, "aven", "a.example.com"); !isCheckRoute(hs[0], tok) || hs[1].(map[string]any)["handler"] != "authentication" {
		t.Fatalf("tls off node: %v", hs)
	}
}

func TestUnverifiedHostsUseOnDemandPolicy(t *testing.T) {
	routes := []Route{
		{Hosts: []string{"legacy.example.com"}, Upstreams: []string{"keel-a-1:80"}},
		{Hosts: []string{"new.example.com"}, Upstreams: []string{"keel-b-1:80"}, Unverified: true},
		// 같은 호스트의 다른 라우트 하나만 Unverified여도 호스트 전체가 보류된다.
		{Hosts: []string{"mixed.example.com"}, Upstreams: []string{"keel-c-1:80"}},
		{Hosts: []string{"mixed.example.com"}, Path: "/x", Upstreams: []string{"keel-c-1:80"}, Unverified: true},
		{Hosts: []string{"internal.example.com"}, Upstreams: []string{"keel-d-1:80"}, TLS: TLSInternal, Unverified: true},
		{Hosts: []string{"off.example.com"}, Upstreams: []string{"keel-e-1:80"}, TLS: TLSOff, Unverified: true},
	}
	m, _ := buildCfg(t, New(Config{TLS: TLSAuto}), routes)
	pols := m["apps"].(map[string]any)["tls"].(map[string]any)["automation"].(map[string]any)["policies"].([]any)
	got := map[string]string{}
	for _, p := range pols {
		pm := p.(map[string]any)
		kind := "managed"
		if pm["on_demand"] == true {
			kind = "on_demand"
		} else if b, _ := json.Marshal(pm["issuers"]); strings.Contains(string(b), "internal") {
			kind = "internal"
		}
		for _, s := range pm["subjects"].([]any) {
			got[s.(string)] = kind
		}
	}
	want := map[string]string{"legacy.example.com": "managed", "new.example.com": "on_demand", "mixed.example.com": "on_demand", "internal.example.com": "internal"}
	if len(got) != len(want) {
		t.Fatalf("policies: %v", got)
	}
	for h, k := range want {
		if got[h] != k {
			t.Errorf("%s: %s want %s", h, got[h], k)
		}
	}
	// ask 서버와 on_demand permission이 붙고, 보류 호스트는 미리 받지 않는다.
	auto := m["apps"].(map[string]any)["tls"].(map[string]any)["automation"].(map[string]any)
	if _, ok := auto["on_demand"]; !ok {
		t.Error("on_demand permission missing")
	}
	if _, ok := servers(m)["aven-tls-ask"]; !ok {
		t.Error("ask server missing")
	}
	skip, _ := json.Marshal(servers(m)["aven"]["automatic_https"])
	if !strings.Contains(string(skip), "new.example.com") || strings.Contains(string(skip), "legacy.example.com") {
		t.Errorf("skip_certificates: %s", skip)
	}
	// 보류 호스트가 없으면 예전 설정과 같다(ask 서버 없음).
	m, _ = buildCfg(t, New(Config{TLS: TLSAuto}), routes[:1])
	if _, ok := servers(m)["aven-tls-ask"]; ok {
		t.Error("ask server without on-demand hosts")
	}
}
