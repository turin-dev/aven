package edge

import (
	"fmt"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testOrigin은 경로별 응답을 정하는 origin과, 그 origin으로 보내는 엔진이다.
type testOrigin struct {
	srv   *httptest.Server
	hits  atomic.Int64
	fail  atomic.Bool
	last  atomic.Value // *http.Request 헤더
	clock time.Time
	e     *Engine
}

type rewriteTransport struct{ target *url.URL }

func (t rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = t.target.Scheme, t.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

func newTestOrigin(t *testing.T, s Settings) *testOrigin {
	t.Helper()
	o := &testOrigin{clock: time.Unix(1_800_000_000, 0)}
	o.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := o.hits.Add(1)
		o.last.Store(r.Header.Clone())
		w.Header().Set("X-Origin-Path", r.URL.RequestURI())
		switch {
		case r.URL.Path == "/app.js":
			w.Header().Set("Content-Type", "text/javascript")
		case r.URL.Path == "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case r.URL.Path == "/page-cached":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "public, max-age=60, stale-while-revalidate=120")
			w.Header().Set("ETag", `"v1"`)
		case r.URL.Path == "/api":
			w.Header().Set("Content-Type", "application/json")
		case r.URL.Path == "/api-cached":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "s-maxage=30")
		case r.URL.Path == "/cookie.css":
			w.Header().Set("Content-Type", "text/css")
			w.Header().Set("Set-Cookie", "a=b")
		case r.URL.Path == "/private.css":
			w.Header().Set("Content-Type", "text/css")
			w.Header().Set("Cache-Control", "private")
		case r.URL.Path == "/flaky.css":
			w.Header().Set("Content-Type", "text/css")
			w.Header().Set("Cache-Control", "max-age=10, stale-if-error=600")
			if o.fail.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		}
		fmt.Fprintf(w, "body %d %s", n, r.Method)
	}))
	t.Cleanup(o.srv.Close)
	u, _ := url.Parse(o.srv.URL)
	o.e = NewEngine(rewriteTransport{u}, 1<<20, 64<<10)
	o.e.Now = func() time.Time { return o.clock }
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	o.e.Configure(map[string]Policy{"app.example.com": {InstanceID: "ins_1", Settings: s}})
	return o
}

func (o *testOrigin) do(t *testing.T, method, target string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://app.example.com"+target, nil)
	r.Header.Set(HeaderClientIP, "198.51.100.10")
	r.Header.Set(HeaderClientProto, "https")
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	o.e.ServeHTTP(w, r)
	time.Sleep(time.Millisecond) // 백그라운드 갱신이 있으면 기회를 준다
	return w
}

func cdnOn() Settings {
	s := DefaultSettings()
	s.CDN.Enabled = true
	return s
}

func TestCDNCacheStatuses(t *testing.T) {
	o := newTestOrigin(t, cdnOn())
	// 정적 자산: Cache-Control이 없어도 Default TTL로 캐시한다.
	if w := o.do(t, "GET", "/app.js"); w.Header().Get("X-Cache") != "MISS" || w.Body.String() != "body 1 GET" {
		t.Fatalf("first: %v %q", w.Header(), w.Body)
	}
	w := o.do(t, "GET", "/app.js")
	if w.Header().Get("X-Cache") != "HIT" || w.Body.String() != "body 1 GET" || w.Header().Get("Age") != "0" || o.hits.Load() != 1 {
		t.Fatalf("second: %v %q hits=%d", w.Header(), w.Body, o.hits.Load())
	}
	// HEAD도 같은 항목을 쓰고, 본문 없이 헤더만 준다.
	if w := o.do(t, "HEAD", "/app.js"); w.Header().Get("X-Cache") != "HIT" || w.Body.Len() != 0 {
		t.Fatalf("head: %v %q", w.Header(), w.Body)
	}
	// 쿼리 순서는 키에 영향이 없다.
	o.do(t, "GET", "/app.js?a=1&b=2")
	if w := o.do(t, "GET", "/app.js?b=2&a=1"); w.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("query order: %v", w.Header())
	}
	// 인코딩은 키에 들어간다.
	if w := o.do(t, "GET", "/app.js", "Accept-Encoding", "gzip, br"); w.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("encoding variant: %v", w.Header())
	}
	if got := o.last.Load().(http.Header).Get("Accept-Encoding"); got != "br" {
		t.Fatalf("origin accept-encoding %q", got)
	}
	// Default TTL(2시간)이 지나면 다시 받는다.
	o.clock = o.clock.Add(2*time.Hour + time.Second)
	if w := o.do(t, "GET", "/app.js"); w.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("expired: %v", w.Header())
	}
	// HTML(Auto): 헤더가 없으면 DYNAMIC, max-age가 있으면 캐시.
	if w := o.do(t, "GET", "/page"); w.Header().Get("X-Cache") != "DYNAMIC" {
		t.Fatalf("html auto: %v", w.Header())
	}
	o.do(t, "GET", "/page-cached")
	if w := o.do(t, "GET", "/page-cached"); w.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("html cached: %v", w.Header())
	}
	// JSON은 명시한 s-maxage가 있어야 캐시한다.
	if w := o.do(t, "GET", "/api"); w.Header().Get("X-Cache") != "DYNAMIC" {
		t.Fatalf("api: %v", w.Header())
	}
	o.do(t, "GET", "/api-cached")
	if w := o.do(t, "GET", "/api-cached"); w.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("api cached: %v", w.Header())
	}
	// Set-Cookie, private은 저장하지 않는다.
	for _, p := range []string{"/cookie.css", "/private.css"} {
		o.do(t, "GET", p)
		if w := o.do(t, "GET", p); w.Header().Get("X-Cache") != "DYNAMIC" {
			t.Fatalf("%s: %v", p, w.Header())
		}
	}
	// Authorization이 있거나 GET/HEAD가 아니면 캐시와 무관하다(x-cache 없음).
	if w := o.do(t, "GET", "/app.js", "Authorization", "Bearer x"); w.Header().Get("X-Cache") != "" {
		t.Fatalf("auth: %v", w.Header())
	}
	if w := o.do(t, "POST", "/app.js"); w.Header().Get("X-Cache") != "" || !strings.HasSuffix(w.Body.String(), "POST") {
		t.Fatalf("post: %v %q", w.Header(), w.Body)
	}
}

func TestCDNConditionalAndSWR(t *testing.T) {
	o := newTestOrigin(t, cdnOn())
	o.do(t, "GET", "/page-cached")
	if w := o.do(t, "GET", "/page-cached", "If-None-Match", `W/"v1", "v0"`); w.Code != http.StatusNotModified || w.Body.Len() != 0 || w.Header().Get("Etag") != `"v1"` {
		t.Fatalf("304: %d %v", w.Code, w.Header())
	}
	// max-age 60 + swr 120: 61초 뒤에는 STALE을 바로 주고 백그라운드에서 갱신한다.
	o.clock = o.clock.Add(61 * time.Second)
	w := o.do(t, "GET", "/page-cached")
	if w.Header().Get("X-Cache") != "STALE" || w.Body.String() != "body 1 GET" || w.Header().Get("Age") != "61" {
		t.Fatalf("stale: %v %q", w.Header(), w.Body)
	}
	deadline := time.Now().Add(2 * time.Second)
	for o.hits.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	if w := o.do(t, "GET", "/page-cached"); w.Header().Get("X-Cache") != "HIT" || w.Body.String() != "body 2 GET" {
		t.Fatalf("refreshed: %v %q", w.Header(), w.Body)
	}
}

func TestCDNStaleSWRDisabledAndStaleIfError(t *testing.T) {
	s := cdnOn()
	s.CDN.HonorSWR = false
	o := newTestOrigin(t, s)
	o.do(t, "GET", "/page-cached")
	o.clock = o.clock.Add(61 * time.Second)
	if w := o.do(t, "GET", "/page-cached"); w.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("swr off: %v", w.Header())
	}
	// stale-if-error는 설정과 무관하게 따른다.
	o.do(t, "GET", "/flaky.css")
	o.fail.Store(true)
	o.clock = o.clock.Add(11 * time.Second)
	if w := o.do(t, "GET", "/flaky.css"); w.Code != 200 || w.Header().Get("X-Cache") != "STALE" {
		t.Fatalf("stale-if-error: %d %v %q", w.Code, w.Header(), w.Body)
	}
}

func TestCDNPurgeGenerations(t *testing.T) {
	s := cdnOn()
	o := newTestOrigin(t, s)
	o.do(t, "GET", "/app.js")
	o.do(t, "GET", "/page-cached")
	if n, _ := o.e.CacheStats(); n != 2 {
		t.Fatalf("items %d", n)
	}
	// HTML만 비우면 정적 자산은 남는다.
	s.CDN.HTMLGeneration++
	o.e.Configure(map[string]Policy{"app.example.com": {InstanceID: "ins_1", Settings: s}})
	if n, _ := o.e.CacheStats(); n != 1 {
		t.Fatalf("after html purge %d", n)
	}
	if w := o.do(t, "GET", "/app.js"); w.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("asset should survive: %v", w.Header())
	}
	if w := o.do(t, "GET", "/page-cached"); w.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("html purged: %v", w.Header())
	}
	s.CDN.AllGeneration++
	o.e.Configure(map[string]Policy{"app.example.com": {InstanceID: "ins_1", Settings: s}})
	if n, _ := o.e.CacheStats(); n != 0 {
		t.Fatalf("after purge all %d", n)
	}
	// CDN을 끄면 캐시를 쓰지 않고 x-cache도 없다.
	s.CDN.Enabled = false
	s.WAF.ManagedRules = true
	o.e.Configure(map[string]Policy{"app.example.com": {InstanceID: "ins_1", Settings: s}})
	if w := o.do(t, "GET", "/app.js"); w.Header().Get("X-Cache") != "" {
		t.Fatalf("cdn off: %v", w.Header())
	}
}

func TestWAFIPListsAndRateLimit(t *testing.T) {
	s := DefaultSettings()
	s.WAF.DenyIPs = []string{"198.51.100.0/24"}
	s.WAF.AllowIPs = []string{"198.51.100.10"}
	s.WAF.RateLimit = &RateLimit{Requests: 2, WindowSeconds: 60}
	o := newTestOrigin(t, s)
	// allow 목록은 deny/요청 제한보다 먼저다.
	for i := 0; i < 4; i++ {
		if w := o.do(t, "GET", "/"); w.Code != 200 {
			t.Fatalf("allowlisted: %d", w.Code)
		}
	}
	if w := o.do(t, "GET", "/", HeaderClientIP, "198.51.100.11"); w.Code != 403 || w.Header().Get("X-Keel-Edge") != "waf-ip" {
		t.Fatalf("denied: %d %v", w.Code, w.Header())
	}
	for i := 0; i < 2; i++ {
		if w := o.do(t, "GET", "/", HeaderClientIP, "192.0.2.1"); w.Code != 200 {
			t.Fatalf("under limit: %d", w.Code)
		}
	}
	w := o.do(t, "GET", "/", HeaderClientIP, "192.0.2.1")
	if w.Code != 429 || w.Header().Get("Retry-After") != "61" {
		t.Fatalf("limited: %d %v", w.Code, w.Header())
	}
	// 다른 클라이언트는 따로 센다. IPv6는 /64 단위다.
	if w := o.do(t, "GET", "/", HeaderClientIP, "192.0.2.2"); w.Code != 200 {
		t.Fatalf("other client: %d", w.Code)
	}
	o.do(t, "GET", "/", HeaderClientIP, "2001:db8::1")
	o.do(t, "GET", "/", HeaderClientIP, "2001:db8::2")
	if w := o.do(t, "GET", "/", HeaderClientIP, "2001:db8::3"); w.Code != 429 {
		t.Fatalf("ipv6 /64: %d", w.Code)
	}
	o.clock = o.clock.Add(61 * time.Second)
	if w := o.do(t, "GET", "/", HeaderClientIP, "192.0.2.1"); w.Code != 200 {
		t.Fatalf("next window: %d", w.Code)
	}
}

func TestManagedRules(t *testing.T) {
	s := DefaultSettings()
	s.WAF.ManagedRules = true
	o := newTestOrigin(t, s)
	for _, target := range []string{"/../../etc/passwd", "/.env", "/x/.git/config", "/search?q=1%27%20OR%20%271%27=%271", "/?q=%3Cscript%3Ealert(1)%3C/script%3E", "/?x=$%7Bjndi:ldap://a%7D"} {
		if w := o.do(t, "GET", target); w.Code != 403 || w.Header().Get("X-Keel-Edge") != "waf-managed" {
			t.Errorf("%s: %d", target, w.Code)
		}
	}
	if w := o.do(t, "GET", "/", "User-Agent", "sqlmap/1.7"); w.Code != 403 {
		t.Errorf("scanner UA: %d", w.Code)
	}
	for _, target := range []string{"/", "/environment", "/blog/select-a-plan?from=home", "/docs/union-station"} {
		if w := o.do(t, "GET", target); w.Code != 200 {
			t.Errorf("false positive %s: %d", target, w.Code)
		}
	}
}

func solve(challenge string) string {
	for n := 0; ; n++ {
		nonce := strconv.FormatInt(int64(n), 36)
		h := fnv.New32a()
		h.Write([]byte(challenge + ":" + nonce))
		if h.Sum32()>>(32-challengeBits) == 0 {
			return nonce
		}
	}
}

func TestUnderAttackChallengeFlow(t *testing.T) {
	s := DefaultSettings()
	s.WAF.UnderAttack = &UnderAttack{Since: time.Unix(1_800_000_000, 0)}
	s.WAF.ClearanceSecret = "secret"
	o := newTestOrigin(t, s)
	ua := "Mozilla/5.0 Test"
	// API 호출(브라우저 탐색이 아님)은 바로 거절한다.
	if w := o.do(t, "GET", "/api", "Accept", "application/json"); w.Code != 403 || strings.Contains(w.Body.String(), "<html") {
		t.Fatalf("api: %d %q", w.Code, w.Body)
	}
	w := o.do(t, "GET", "/dash?tab=1", "Accept", "text/html,*/*", "User-Agent", ua)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "브라우저를 확인") || o.hits.Load() != 0 {
		t.Fatalf("challenge page: %d %q", w.Code, w.Body)
	}
	body := w.Body.String()
	i := strings.Index(body, `name="c" value="`) + len(`name="c" value="`)
	tok := body[i : i+strings.IndexByte(body[i:], '"')]
	// 틀린 답은 거절한다.
	post := func(nonce, ua string) *httptest.ResponseRecorder {
		form := url.Values{"c": {tok}, "n": {nonce}, "r": {"/dash?tab=1"}}
		r := httptest.NewRequest("POST", "http://app.example.com"+ChallengePath, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("User-Agent", ua)
		r.Header.Set(HeaderClientIP, "198.51.100.10")
		r.Header.Set(HeaderClientProto, "https")
		w := httptest.NewRecorder()
		o.e.ServeHTTP(w, r)
		return w
	}
	nonce := solve(tok)
	if w := post(nonce+"zz", ua); w.Code != 403 && powOK(tok, nonce+"zz") == false {
		t.Fatalf("wrong answer: %d", w.Code)
	}
	w = post(nonce, ua)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/dash?tab=1" {
		t.Fatalf("answer: %d %v", w.Code, w.Header())
	}
	cookie := w.Result().Cookies()[0]
	if cookie.Name != ClearanceCookie || !cookie.Secure || !cookie.HttpOnly {
		t.Fatalf("cookie %+v", cookie)
	}
	if w := o.do(t, "GET", "/dash", "Cookie", cookie.Name+"="+cookie.Value, "User-Agent", ua, "Accept", "text/html"); w.Code != 200 {
		t.Fatalf("cleared: %d", w.Code)
	}
	// 쿠키는 User-Agent에 묶인다.
	if w := o.do(t, "GET", "/dash", "Cookie", cookie.Name+"="+cookie.Value, "User-Agent", "curl/8", "Accept", "text/html"); w.Code != 403 {
		t.Fatalf("other UA: %d", w.Code)
	}
	// 끝나는 시각이 지나면 확인 없이 통과한다.
	until := o.clock.Add(time.Minute)
	s.WAF.UnderAttack.Until = &until
	o.e.Configure(map[string]Policy{"app.example.com": {InstanceID: "ins_1", Settings: s}})
	o.clock = o.clock.Add(2 * time.Minute)
	if w := o.do(t, "GET", "/api"); w.Code != 200 {
		t.Fatalf("expired: %d", w.Code)
	}
}

func TestEdgeRuleActions(t *testing.T) {
	s := cdnOn()
	s.Rules = parseRuleset(t, `{"version":1,"rules":[
	  {"priority":1,"if":{"attr":"http.path","op":"eq","value":"/old"},"then":{"action":"redirect","params":{"host":"new.example.com","status":308}}},
	  {"priority":2,"if":{"attr":"http.path","op":"eq","value":"/gone"},"then":{"action":"redirect","params":{"location":"https://example.org/x"}}},
	  {"priority":3,"if":{"attr":"http.path","op":"matches","value":"/internal*"},"then":{"action":"block","params":{"status":404,"body":"nope"}}},
	  {"priority":4,"if":{"attr":"http.header","key":"x-debug","op":"eq","value":"1"},"then":{"action":"allow"}},
	  {"priority":5,"if":{"attr":"http.method","op":"eq","value":"DELETE"},"then":{"action":"block"}},
	  {"priority":6,"if":{"attr":"http.path","op":"eq","value":"/legacy.js"},"then":{"action":"rewrite","params":{"path":"/app.js?v=2"}}},
	  {"priority":7,"if":{"attr":"http.path","op":"matches","value":"/app*"},"then":{"action":"set_header","params":{"name":"X-Frame-Options","value":"DENY"}}},
	  {"priority":8,"if":{"attr":"http.path","op":"matches","value":"/app*"},"then":{"action":"set_header","params":{"name":"X-From-Edge","value":"1","target":"request"}}},
	  {"priority":9,"if":{"attr":"http.path","op":"eq","value":"/api"},"then":{"action":"cache_override","params":{"ttl_seconds":30}}},
	  {"priority":10,"if":{"attr":"http.path","op":"eq","value":"/app.js"},"then":{"action":"cache_override","params":{"bypass":true}}},
	  {"priority":11,"enabled":false,"if":{"attr":"http.path","op":"eq","value":"/"},"then":{"action":"block"}}
	]}`)
	o := newTestOrigin(t, s)
	if w := o.do(t, "GET", "/old?a=1"); w.Code != 308 || w.Header().Get("Location") != "https://new.example.com/old?a=1" {
		t.Fatalf("host redirect: %d %v", w.Code, w.Header())
	}
	if w := o.do(t, "GET", "/gone"); w.Code != 302 || w.Header().Get("Location") != "https://example.org/x" {
		t.Fatalf("location redirect: %d %v", w.Code, w.Header())
	}
	if w := o.do(t, "GET", "/internal/x"); w.Code != 404 || w.Body.String() != "nope" {
		t.Fatalf("block: %d %q", w.Code, w.Body)
	}
	if w := o.do(t, "DELETE", "/x"); w.Code != 403 {
		t.Fatalf("method block: %d", w.Code)
	}
	if w := o.do(t, "DELETE", "/x", "X-Debug", "1"); w.Code != 200 {
		t.Fatalf("allow skips later rules: %d", w.Code)
	}
	if w := o.do(t, "GET", "/"); w.Code != 200 {
		t.Fatalf("disabled rule: %d", w.Code)
	}
	// rewrite는 origin 경로를 바꾸고 다음 규칙으로 계속 간다(/app* 헤더 규칙이 맞는다).
	w := o.do(t, "GET", "/legacy.js")
	if w.Header().Get("X-Origin-Path") != "/app.js?v=2" || w.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("rewrite: %v", w.Header())
	}
	if o.last.Load().(http.Header).Get("X-From-Edge") != "1" {
		t.Fatal("request header not set")
	}
	// bypass: 캐시를 쓰지 않는다.
	o.do(t, "GET", "/app.js")
	if w := o.do(t, "GET", "/app.js"); w.Header().Get("X-Cache") != "" || w.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("bypass: %v", w.Header())
	}
	// ttl override: 헤더가 없는 JSON도 캐시한다.
	o.do(t, "GET", "/api")
	if w := o.do(t, "GET", "/api"); w.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("override: %v", w.Header())
	}
	o.clock = o.clock.Add(31 * time.Second)
	if w := o.do(t, "GET", "/api"); w.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("override expiry: %v", w.Header())
	}
}

func TestChallengeRuleAndTrace(t *testing.T) {
	s := cdnOn()
	s.Rules = parseRuleset(t, `{"version":1,"rules":[{"priority":1,"if":{"attr":"http.path","op":"matches","value":"/login*"},"then":{"action":"challenge"}}]}`)
	o := newTestOrigin(t, s)
	if w := o.do(t, "GET", "/login", "Accept", "text/html"); w.Code != 403 || !strings.Contains(w.Body.String(), "<form") {
		t.Fatalf("challenge rule: %d", w.Code)
	}
	if w := o.do(t, "GET", "/"); w.Code != 200 {
		t.Fatalf("other path: %d", w.Code)
	}
	w := o.do(t, "GET", CDNTracePath+"?json")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"host":"app.example.com"`) || !strings.Contains(w.Body.String(), `"ip":"198.51.100.10"`) {
		t.Fatalf("trace: %d %q", w.Code, w.Body)
	}
}

func TestUnknownHostAndWildcardPolicy(t *testing.T) {
	o := newTestOrigin(t, DefaultSettings())
	s := DefaultSettings()
	s.WAF.DenyIPs = []string{"0.0.0.0/0"}
	o.e.Configure(map[string]Policy{"*.example.com": {InstanceID: "ins_2", Settings: s}})
	if w := o.do(t, "GET", "/"); w.Code != 403 {
		t.Fatalf("wildcard policy: %d", w.Code)
	}
	r := httptest.NewRequest("GET", "http://other.test/", nil)
	w := httptest.NewRecorder()
	o.e.ServeHTTP(w, r)
	if w.Code != 502 {
		t.Fatalf("unknown host: %d", w.Code)
	}
}

func TestDecideAndEncoding(t *testing.T) {
	cdn := DefaultSettings().CDN
	h := http.Header{"Content-Type": {"text/html"}}
	if decide(cdn, 200, h, nil).store {
		t.Fatal("auto html without headers")
	}
	cdn.HTMLCaching = "force"
	if d := decide(cdn, 200, h, nil); !d.store || d.ttl != 2*time.Hour {
		t.Fatalf("force html: %+v", d)
	}
	h.Set("Cache-Control", "max-age=5")
	if d := decide(cdn, 200, h, nil); d.ttl != 5*time.Second {
		t.Fatalf("force html max-age: %+v", d)
	}
	cdn.HTMLCaching = "never"
	if decide(cdn, 200, h, nil).store {
		t.Fatal("never html")
	}
	img := http.Header{"Content-Type": {"image/png"}, "Vary": {"Cookie"}}
	if decide(cdn, 200, img, nil).store {
		t.Fatal("vary cookie")
	}
	img.Set("Vary", "Accept")
	if !decide(cdn, 200, img, nil).store {
		t.Fatal("vary accept")
	}
	if decide(cdn, 500, img, nil).store || decide(cdn, 404, img, nil).store {
		t.Fatal("errors without explicit max-age")
	}
	if !decide(cdn, 301, http.Header{"Cache-Control": {"max-age=60"}, "Content-Length": {"10"}}, nil).store ||
		decide(cdn, 301, http.Header{"Cache-Control": {"max-age=60"}, "Content-Length": {"0"}}, nil).store {
		t.Fatal("redirect body rule")
	}
	if !IsStaticType("text/css; charset=utf-8") || IsStaticType("text/plain") || IsStaticType("application/json") {
		t.Fatal("static types")
	}
	for in, want := range map[string]string{"": "identity", "gzip, deflate, br": "br", "br;q=0, gzip": "gzip", "gzip;q=0.5": "gzip", "deflate": "identity"} {
		if got := encodingClass(in); got != want {
			t.Errorf("%q → %q", in, got)
		}
	}
}

func TestCacheEvictsLRU(t *testing.T) {
	c := NewCache(3000, 2000)
	for i := 0; i < 5; i++ {
		c.put(fmt.Sprintf("k%d", i), http.Header{}, &entry{body: make([]byte, 800), header: http.Header{}})
	}
	n, used := c.Stats()
	if n != 2 || used > 3000 || c.get("k4") == nil || c.get("k0") != nil {
		t.Fatalf("n=%d used=%d", n, used)
	}
	if c.put("big", http.Header{}, &entry{body: make([]byte, 2500), header: http.Header{}}) {
		t.Fatal("stored an item over the per-item limit")
	}
}
