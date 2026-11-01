package edge

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// http와 https 응답은 따로 저장한다(origin의 http→https 리다이렉트가 https 요청에 나가면 고리가 된다).
func TestCacheKeySeparatesScheme(t *testing.T) {
	o := newTestOrigin(t, cdnOn())
	o.do(t, "GET", "/app.js")
	if w := o.do(t, "GET", "/app.js", HeaderClientProto, "http"); w.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("http request served the https entry: %v", w.Header())
	}
	if w := o.do(t, "GET", "/app.js"); w.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("https entry lost: %v", w.Header())
	}
}

// 캐시 키에 없는 헤더(메서드·URL override)는 캐시할 수 있는 요청에서 origin으로 보내지 않는다.
func TestCacheableRequestDropsUnkeyedHeaders(t *testing.T) {
	o := newTestOrigin(t, cdnOn())
	o.do(t, "GET", "/app.js", "X-Http-Method-Override", "DELETE", "X-Original-Url", "/admin", "X-Forwarded-Scheme", "http", "X-Custom", "kept")
	got := o.last.Load().(http.Header)
	for _, k := range []string{"X-Http-Method-Override", "X-Original-Url", "X-Forwarded-Scheme"} {
		if got.Get(k) != "" {
			t.Errorf("%s reached the origin on a cacheable request", k)
		}
	}
	if got.Get("X-Custom") != "kept" {
		t.Error("ordinary headers must pass through")
	}
	// 캐시와 무관한 요청(POST)은 그대로 보낸다.
	o.do(t, "POST", "/app.js", "X-Http-Method-Override", "DELETE")
	if o.last.Load().(http.Header).Get("X-Http-Method-Override") != "DELETE" {
		t.Error("non-cacheable request headers must not be changed")
	}
}

// Vary가 없는 응답은 Vary 표에 남지 않는다(쿼리를 바꿔 가며 보내도 메모리가 자라지 않는다).
func TestVaryTableStaysBounded(t *testing.T) {
	o := newTestOrigin(t, cdnOn())
	for i := 0; i < 200; i++ {
		o.do(t, "GET", fmt.Sprintf("/app.js?r=%d", i))
	}
	o.e.cache.mu.Lock()
	n := len(o.e.cache.vary)
	o.e.cache.mu.Unlock()
	if n != 0 {
		t.Fatalf("vary table has %d entries for responses without Vary", n)
	}
	c := NewCache(1<<20, 1<<20)
	h := http.Header{"Vary": {"X-Lang"}}
	for i := 0; i < 5000; i++ {
		c.put(fmt.Sprintf("pk%d", i), http.Header{}, &entry{header: h.Clone(), body: make([]byte, 1000)})
	}
	if items, _ := c.Stats(); len(c.vary) > 4*items+1024 {
		t.Fatalf("vary table unbounded: %d names for %d items", len(c.vary), items)
	}
}

// 표가 가득 차도 요청마다 전체를 훑지 않는다.
func TestLimiterSweepThrottledWhenFull(t *testing.T) {
	var l limiter
	now := time.Unix(1_800_000_000, 0)
	rl := RateLimit{Requests: 10, WindowSeconds: 60}
	l.allow("seed", rl, now) // 첫 정리
	for i := 0; len(l.windows) < maxLimiterKeys; i++ {
		l.windows[fmt.Sprintf("k%d", i)] = &window{start: now, size: time.Minute}
	}
	l.allow("x", rl, now.Add(2*time.Second))
	swept := l.lastSweep
	l.allow("y", rl, now.Add(2*time.Second+time.Millisecond))
	if l.lastSweep != swept {
		t.Fatal("full limiter swept again within a second")
	}
	if ok, _ := l.allow("brand-new", rl, now.Add(3*time.Second)); !ok {
		t.Fatal("new clients must not be blocked when the table is full")
	}
	if _, ok := l.windows["brand-new"]; ok {
		t.Fatal("table grew past its limit")
	}
}

func TestRulePathNormalization(t *testing.T) {
	for in, want := range map[string]string{"": "/", "/": "/", "/admin": "/admin", "//admin": "/admin", "/./admin": "/admin",
		"/x/../admin": "/admin", "/admin/": "/admin/", "/../../admin": "/admin", "/a//b/": "/a/b/"} {
		if got := rulePath(in); got != want {
			t.Errorf("rulePath(%q) = %q, want %q", in, got, want)
		}
	}
}

// 경로를 꾸며도(//, /./, ..) 경로 차단 규칙을 피하지 못한다.
func TestBlockRuleNotBypassedByPathTricks(t *testing.T) {
	s := DefaultSettings()
	s.Rules = Ruleset{Version: 1, Rules: []Rule{{Priority: 1, If: &Expr{Attr: "http.path", Op: "eq", Value: Value{One: "/admin"}}, Then: Action{Action: ActBlock}}}}
	o := newTestOrigin(t, s)
	for _, target := range []string{"/admin", "//admin", "/./admin", "/x/../admin", "/%2e/admin"} {
		if w := o.do(t, "GET", target); w.Code != http.StatusForbidden {
			t.Errorf("%s: %d (block rule bypassed)", target, w.Code)
		}
	}
	if w := o.do(t, "GET", "/admin2"); w.Code != http.StatusOK {
		t.Errorf("unrelated path blocked: %d", w.Code)
	}
}

// 쿠키를 보낸 요청의 응답은 명시한 Cache-Control이 없으면 정적 자산(이미지, CSS, JS, 글꼴)만 저장한다.
func TestCookieRequestsOnlyCacheAssetsHeuristically(t *testing.T) {
	cdn := cdnOn().CDN
	pdf := http.Header{"Content-Type": {"application/pdf"}}
	if !decideFor(cdn, 200, pdf, nil, false).store {
		t.Fatal("anonymous PDF should be cached heuristically")
	}
	if decideFor(cdn, 200, pdf, nil, true).store {
		t.Fatal("PDF requested with a cookie must not be cached without Cache-Control")
	}
	if !decideFor(cdn, 200, http.Header{"Content-Type": {"application/pdf"}, "Cache-Control": {"public, max-age=60"}}, nil, true).store {
		t.Fatal("explicit max-age must still allow caching")
	}
	for _, ct := range []string{"image/png", "text/css", "text/javascript; charset=utf-8", "font/woff2"} {
		if !decideFor(cdn, 200, http.Header{"Content-Type": {ct}}, nil, true).store {
			t.Errorf("%s with a cookie should be cached", ct)
		}
	}
	force := cdn
	force.HTMLCaching = "force"
	if !decideFor(force, 200, http.Header{"Content-Type": {"text/html"}}, nil, true).store {
		t.Error("HTML force mode is an explicit choice and still caches")
	}
}
