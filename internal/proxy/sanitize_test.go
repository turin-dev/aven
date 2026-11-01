package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const validTestHash = "$2a$10$abcdefghijklmnopqrstuuOq1Q3b2Yc8b3o0m9i0l2b5n6x7z8a9e"

func TestCleanHost(t *testing.T) {
	for in, want := range map[string]string{
		"App.Example.COM":   "app.example.com",
		"a.example.com.":    "a.example.com",
		" a.example.com ":   "a.example.com",
		"*.preview.ex.com":  "*.preview.ex.com",
		"localhost":         "localhost",
		"100.64.0.3":        "100.64.0.3",
		"[2001:db8::1]":     "[2001:db8::1]",
		"2001:db8::1":       "2001:db8::1",
		"_acme.example.com": "_acme.example.com",
	} {
		if got, ok := cleanHost(in); !ok || got != want {
			t.Errorf("cleanHost(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "*", "*.com", "a.*.example.com", "{http.request.host}", "a.example.com{x}", "a b.com", "a..b.com",
		"a.example.com/x", "a.example.com:443", "a\r\nb.com", "ex\\ample.com", strings.Repeat("a", 64) + ".com", "fe80::1%eth0", "a\"b.com"} {
		if got, ok := cleanHost(bad); ok {
			t.Errorf("cleanHost(%q) accepted as %q", bad, got)
		}
	}
}

func TestRedirectLocation(t *testing.T) {
	for in, want := range map[string]string{
		"example.com":                     "https://example.com{http.request.uri}",
		"example.com/":                    "https://example.com{http.request.uri}",
		"https://example.com/x/":          "https://example.com/x{http.request.uri}",
		"http://example.com:8080":         "http://example.com:8080{http.request.uri}",
		"https://example.com/landing?a=1": "https://example.com/landing?a=1", // 쿼리가 있으면 정확한 주소로
		"https://example.com/#top":        "https://example.com/#top",
	} {
		if got, ok := redirectLocation(in); !ok || got != want {
			t.Errorf("redirectLocation(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "x.com{http.request.header.Authorization}", "https://x.com/{env.KEEL_SECRET}", "javascript:alert(1)",
		"ftp://x.com", "https://user:pw@x.com", "https://", "x.com\r\nSet-Cookie: a=b", "x.com/ a", "https://*.x.com", "https://x.com\\@evil.com"} {
		if got, ok := redirectLocation(bad); ok {
			t.Errorf("redirectLocation(%q) accepted as %q", bad, got)
		}
	}
}

func TestValidBcrypt(t *testing.T) {
	if !validBcrypt(validTestHash) || !validBcrypt("$2b$12$"+validTestHash[7:]) || !validBcrypt("$2y$04$"+validTestHash[7:]) {
		t.Fatal("valid bcrypt hash rejected")
	}
	for _, bad := range []string{"", "plaintext", "$2a$10$short", "$2x$10$" + validTestHash[7:], "$2a$03$" + validTestHash[7:], "$2a$1a$" + validTestHash[7:],
		"$2a$10$" + validTestHash[7:59] + "{", "$1$10$" + validTestHash[6:]} {
		if validBcrypt(bad) {
			t.Errorf("validBcrypt(%q) = true", bad)
		}
	}
}

// 사용자 값에 Caddy placeholder가 들어가도 설정에는 나타나지 않는다.
func TestBuildRejectsPlaceholderInjection(t *testing.T) {
	c := New(Config{TLS: TLSAuto})
	_, s := buildCfg(t, c, []Route{
		{Hosts: []string{"{http.request.host}"}, Upstreams: []string{"keel-a-1:80"}},
		{Hosts: []string{"ok.example.com"}, Upstreams: []string{"keel-ok-1:80"}},
		{Hosts: []string{"ok.example.com"}, Path: "/a{http.request.uri}/*", Upstreams: []string{"keel-evil-1:80"}},
		{Hosts: []string{"ok.example.com"}, Path: "/b/*", Upstreams: []string{"{http.request.header.X-Target}:80"}},
		{Hosts: []string{"r.example.com"}, Redirect: &Redirect{To: "evil.com/{http.request.header.Authorization}"}},
		{Hosts: []string{"r2.example.com"}, Redirect: &Redirect{To: "evil.com\r\nSet-Cookie: x=1"}},
	})
	for _, bad := range []string{"{http.request.host}", "{http.request.header.X-Target}", "{http.request.header.Authorization}", "keel-evil-1", "Set-Cookie", "r.example.com", "r2.example.com"} {
		if strings.Contains(s, bad) {
			t.Errorf("config contains %q: %s", bad, s)
		}
	}
	if !strings.Contains(s, `"dial":"keel-ok-1:80"`) {
		t.Errorf("valid route lost: %s", s)
	}
}

func TestBuildNormalizesHostCase(t *testing.T) {
	c := New(Config{TLS: TLSAuto})
	m, s := buildCfg(t, c, []Route{
		{Hosts: []string{"App.Example.com"}, Upstreams: []string{"keel-a-1:80"}},
		{Hosts: []string{"app.example.com."}, Path: "/api/*", Upstreams: []string{"keel-b-1:80"}},
	})
	if strings.Contains(s, "App.Example.com") || strings.Count(s, `"host":["app.example.com"]`) != 1 {
		t.Fatalf("hosts differing in case or trailing dot must merge into one route: %s", s)
	}
	_ = m
}

// 경로 규칙이 잘못돼도 같은 Route의 basic auth는 사라지지 않는다.
func TestInvalidPathKeepsHostAuth(t *testing.T) {
	c := New(Config{TLS: TLSAuto})
	_, s := buildCfg(t, c, []Route{
		{Hosts: []string{"p.example.com"}, Path: "/x{y}", Upstreams: []string{"keel-a-1:80"}, Auth: &BasicAuth{User: "u", Hash: validTestHash}},
		{Hosts: []string{"p.example.com"}, Upstreams: []string{"keel-b-1:80"}},
	})
	if !strings.Contains(s, `"http_basic"`) || strings.Contains(s, "keel-a-1") || !strings.Contains(s, "keel-b-1") {
		t.Fatalf("auth must survive an invalid path rule: %s", s)
	}
}

// basic auth를 요청했지만 해시가 bcrypt가 아니면 보호 없이 열지 않는다.
func TestBasicAuthFailsClosed(t *testing.T) {
	c := New(Config{TLS: TLSAuto})
	for _, a := range []*BasicAuth{{User: "u", Hash: ""}, {User: "u", Hash: "plaintext-password"}, {User: "", Hash: validTestHash}, {User: "a:b", Hash: validTestHash}} {
		_, s := buildCfg(t, c, []Route{{Hosts: []string{"p.example.com"}, Upstreams: []string{"keel-a-1:80"}, Auth: a}})
		if strings.Contains(s, "keel-a-1") || !strings.Contains(s, `"status_code":503`) {
			t.Errorf("auth %+v: host must not be served without auth: %s", a, s)
		}
		if strings.Contains(s, "plaintext-password") {
			t.Errorf("invalid hash leaked into config: %s", s)
		}
	}
}

// 같은 입력은 같은 JSON이 된다(Caddy는 같은 설정의 /load를 다시 적용하지 않는다).
func TestBuildDeterministic(t *testing.T) {
	c := New(Config{TLS: TLSAuto, MetricsHosts: []string{"100.64.0.3"}})
	c.SetEdge(&fakeEdge{})
	first, err := c.Build(withCheckTokens(mainRoutes()))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		again, err := c.Build(withCheckTokens(mainRoutes()))
		if err != nil {
			t.Fatal(err)
		}
		if string(again) != string(first) {
			t.Fatal("Build output differs between runs")
		}
	}
}

// 공개 서버는 Cloudflare와 설정한 앞단 proxy에서 온 X-Forwarded-For만 오른쪽부터(strict) 믿는다.
func TestClientIPTrustedProxies(t *testing.T) {
	c := New(Config{TLS: TLSAuto, TrustedProxies: []string{"172.18.0.0/16", "10.1.2.3", "::ffff:10.9.9.9", "not-an-ip"}})
	m, _ := buildCfg(t, c, []Route{
		{Hosts: []string{"a.example.com"}, Upstreams: []string{"keel-a-1:80"}},
		{Hosts: []string{"b.example.com"}, Upstreams: []string{"keel-b-1:80"}, TLS: TLSOff},
	})
	servers := m["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)
	for _, name := range []string{"aven", "aven-http"} {
		srv := servers[name].(map[string]any)
		tp, _ := srv["trusted_proxies"].(map[string]any)
		if tp == nil || tp["source"] != "static" || srv["trusted_proxies_strict"] != float64(1) {
			t.Fatalf("%s: trusted proxies not configured: %v", name, srv)
		}
		got := map[string]bool{}
		for _, r := range tp["ranges"].([]any) {
			got[r.(string)] = true
		}
		for _, want := range []string{"172.18.0.0/16", "10.1.2.3/32", "10.9.9.9/32", "173.245.48.0/20", "2606:4700::/32"} {
			if !got[want] {
				t.Errorf("%s: missing trusted range %s in %v", name, want, got)
			}
		}
		if got["not-an-ip"] {
			t.Errorf("invalid entry kept")
		}
		if h := srv["client_ip_headers"].([]any); len(h) != 1 || h[0] != "X-Forwarded-For" {
			t.Errorf("%s: client_ip_headers = %v (CF-Connecting-IP is spoofable through non-Cloudflare proxies)", name, h)
		}
	}
}

func TestTrustedProxiesFromEnv(t *testing.T) {
	t.Setenv(TrustedProxiesEnv, "192.168.5.0/24, 10.0.0.7")
	got := strings.Join(New(Config{}).trustedRanges(), ",")
	if !strings.Contains(got, "192.168.5.0/24") || !strings.Contains(got, "10.0.0.7/32") {
		t.Fatalf("env ranges not used: %s", got)
	}
	// 명시한 값(빈 목록 포함)이 있으면 환경 변수를 읽지 않는다.
	if got := strings.Join(New(Config{TrustedProxies: []string{}}).trustedRanges(), ","); strings.Contains(got, "192.168.5.0") {
		t.Fatalf("explicit config must win over env: %s", got)
	}
}

// 같은 설정은 다시 보내지 않고, 바뀌거나 실패한 뒤에는 보낸다.
func TestApplySkipsUnchangedConfig(t *testing.T) {
	var mu sync.Mutex
	loads, fail := 0, false
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		loads++
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	}))
	defer admin.Close()
	u, _ := url.Parse(admin.URL)
	port, _ := strconv.Atoi(u.Port())
	c := New(Config{TLS: TLSOff, AdminPort: port})
	a := []Route{{Hosts: []string{"a.example.com"}, Upstreams: []string{"keel-a-1:80"}}}
	b := []Route{{Hosts: []string{"b.example.com"}, Upstreams: []string{"keel-b-1:80"}}}
	count := func() int { mu.Lock(); defer mu.Unlock(); return loads }
	for i := 0; i < 3; i++ {
		if err := c.Apply(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	if count() != 1 {
		t.Fatalf("identical config sent %d times", count())
	}
	c.Apply(context.Background(), b)
	if count() != 2 {
		t.Fatalf("changed config not sent: %d", count())
	}
	mu.Lock()
	fail = true
	mu.Unlock()
	if err := c.Apply(context.Background(), a); err == nil {
		t.Fatal("error not reported")
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	c.Apply(context.Background(), a)
	if count() != 4 {
		t.Fatalf("config must be resent after a failed load: %d", count())
	}
	c.lastAppliedAt = time.Now().Add(-ReapplyEvery - time.Second)
	c.Apply(context.Background(), a)
	if count() != 5 {
		t.Fatalf("config must be resent after ReapplyEvery: %d", count())
	}
	// 동시에 불러도 안전하다(-race).
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				c.Apply(context.Background(), a)
			} else {
				c.Apply(context.Background(), b)
			}
		}(i)
	}
	wg.Wait()
}

// 구체 호스트와 와일드카드의 TLS 모드가 달라도, 구체 호스트의 요청이 와일드카드 서비스로 새지 않는다.
func TestExactHostNotShadowedByWildcardOnOtherScheme(t *testing.T) {
	c := New(Config{TLS: TLSAuto})
	m, _ := buildCfg(t, c, []Route{
		{Hosts: []string{"*.example.com"}, Upstreams: []string{"keel-wild-1:80"}},
		{Hosts: []string{"plain.example.com"}, Upstreams: []string{"keel-plain-1:80"}, TLS: TLSOff},
		{Hosts: []string{"*.off.example.com"}, Upstreams: []string{"keel-wildoff-1:80"}, TLS: TLSOff},
		{Hosts: []string{"secure.off.example.com"}, Upstreams: []string{"keel-secure-1:80"}},
	})
	servers := m["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)
	first := func(server, host string) map[string]any {
		for _, r := range servers[server].(map[string]any)["routes"].([]any) {
			rt := r.(map[string]any)
			for _, mt := range asSlice(rt["match"]) {
				for _, h := range asSlice(mt.(map[string]any)["host"]) {
					if h == host || (strings.HasPrefix(h.(string), "*.") && strings.HasSuffix(host, h.(string)[1:]) && !strings.Contains(strings.TrimSuffix(host, h.(string)[1:]), ".")) {
						return rt["handle"].([]any)[0].(map[string]any)
					}
				}
			}
		}
		return nil
	}
	if h := first("aven", "plain.example.com"); h == nil || h["status_code"] != float64(404) {
		t.Errorf("https plain.example.com must not reach the wildcard: %v", h)
	}
	if h := first("aven-http", "secure.off.example.com"); h == nil || h["status_code"] != float64(308) {
		t.Errorf("http secure.off.example.com must redirect to https, not reach the wildcard: %v", h)
	}
	if h := first("aven", "other.example.com"); h == nil || h["handler"] != "reverse_proxy" {
		t.Errorf("wildcard still serves other names: %v", h)
	}
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// 너무 긴 줄 하나 때문에 그 뒤 access log를 놓치지 않는다.
func TestEachLineSkipsOverlongLines(t *testing.T) {
	in := "first\n" + strings.Repeat("x", 300_000) + "\nsecond\r\n\nthird"
	var got []string
	eachLine(strings.NewReader(in), 100_000, func(l string) { got = append(got, l) })
	if strings.Join(got, "|") != "first|second||third" {
		t.Fatalf("got %q", got)
	}
}
