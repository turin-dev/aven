package api

import (
	"context"
	"github.com/turin-dev/aven/internal/store"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func secServer(t *testing.T) *Server {
	return &Server{Store: testStore(t), AdminPassword: "password123", APIToken: "tok-tok-tok", SessionKey: make([]byte, 32), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func withTrusted(r *http.Request) context.Context {
	return context.WithValue(r.Context(), trustedListenerKey{}, true)
}

func TestGatewaySecretTrustLogic(t *testing.T) {
	s := secServer(t)
	var gotIP string
	var gotSecure bool
	var gotHdr http.Header
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIP, gotSecure, gotHdr = limitKey(s.clientIP(r)), s.secureTransport(r), r.Header.Clone()
	})
	gw := ViaGateway("s3cret-s3cret-s3cret-s3cret-s3cret", inner)
	do := func(h http.Handler, remote string, hdr map[string]string) {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	// unix socket의 RemoteAddr는 주소가 아니다 → peer 없음
	good := map[string]string{GatewayHeader: "s3cret-s3cret-s3cret-s3cret-s3cret", "X-Forwarded-Proto": "https", "X-Forwarded-For": "172.70.1.1", "Cf-Connecting-Ip": "203.0.113.9"}
	do(gw, "@", good)
	if !gotSecure || gotIP != "203.0.113.9" {
		t.Errorf("via Traefik with the secret: secure=%v ip=%s", gotSecure, gotIP)
	}
	if gotHdr.Get(GatewayHeader) != "" {
		t.Error("gateway secret header not stripped")
	}
	// 다른 컨테이너가 gateway에 직접 붙어 위조: 비밀 없음
	forged := map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-For": "172.70.1.1", "Cf-Connecting-Ip": "203.0.113.9"}
	do(gw, "@", forged)
	if gotSecure || gotIP != "unknown" {
		t.Errorf("forged headers without the secret trusted: secure=%v ip=%s", gotSecure, gotIP)
	}
	if gotHdr.Get("X-Forwarded-Proto") != "" || gotHdr.Get("Cf-Connecting-Ip") != "" || gotHdr.Get("X-Forwarded-For") != "" {
		t.Error("forwarded headers not stripped from an untrusted request")
	}
	// 틀린 비밀 / 길이가 다른 비밀
	for _, v := range []string{"wrong", "s3cret-s3cret-s3cret-s3cret-s3cre", "s3cret-s3cret-s3cret-s3cret-s3cret-x", ""} {
		f := map[string]string{GatewayHeader: v, "X-Forwarded-Proto": "https", "Cf-Connecting-Ip": "203.0.113.9"}
		do(gw, "@", f)
		if gotSecure || gotIP == "203.0.113.9" {
			t.Errorf("secret %q accepted", v)
		}
	}
	// TCP listener(Tailscale 등)는 헤더를 전혀 믿지 않는다: 위조된 CF-Connecting-IP/XFF 무시
	do(inner, "100.64.0.7:5555", map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-For": "1.2.3.4", "Cf-Connecting-Ip": "1.2.3.4", GatewayHeader: "x"})
	if gotSecure || gotIP != "100.64.0.7" {
		t.Errorf("plain TCP listener trusted headers: secure=%v ip=%s", gotSecure, gotIP)
	}
}

func TestClientIPSelection(t *testing.T) {
	s := secServer(t)
	s.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	ip := func(remote string, h map[string]string, trusted bool) string {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		for k, v := range h {
			r.Header.Set(k, v)
		}
		if trusted {
			r = r.WithContext(withTrusted(r))
		}
		return limitKey(s.clientIP(r))
	}
	// Cloudflare 대역에서 온 연결(XFF의 마지막 hop)만 CF-Connecting-IP를 신뢰
	if got := ip("@", map[string]string{"X-Forwarded-For": "172.70.1.1", "Cf-Connecting-Ip": "198.51.100.4"}, true); got != "198.51.100.4" {
		t.Errorf("cloudflare hop: %s", got)
	}
	// Cloudflare를 거치지 않고 origin에 직접 와서 CF-Connecting-IP를 위조: 무시하고 Traefik이 본 peer를 쓴다
	if got := ip("@", map[string]string{"X-Forwarded-For": "198.51.100.77", "Cf-Connecting-Ip": "1.1.1.1"}, true); got != "198.51.100.77" {
		t.Errorf("forged CF-Connecting-IP outside Cloudflare: %s", got)
	}
	// 클라이언트가 XFF 앞쪽에 가짜 값을 넣어도 마지막 hop만 본다
	if got := ip("@", map[string]string{"X-Forwarded-For": "9.9.9.9, 198.51.100.77"}, true); got != "198.51.100.77" {
		t.Errorf("spoofed XFF prefix: %s", got)
	}
	// IPv6 Cloudflare 대역 + IPv6 클라이언트는 /64로 묶는다
	if got := ip("@", map[string]string{"X-Forwarded-For": "2606:4700::1", "Cf-Connecting-Ip": "2001:db8:6b:1943:aaaa::1"}, true); got != "2001:db8:6b:1943::/64" {
		t.Errorf("ipv6 /64 bucket: %s", got)
	}
	// KEEL_TRUSTED_PROXIES의 주소에서 온 TCP 연결
	if got := ip("10.1.2.3:4000", map[string]string{"X-Forwarded-For": "198.51.100.5"}, false); got != "198.51.100.5" {
		t.Errorf("trusted TCP proxy: %s", got)
	}
	if got := ip("192.0.2.9:4000", map[string]string{"X-Forwarded-For": "198.51.100.5"}, false); got != "192.0.2.9" {
		t.Errorf("untrusted TCP peer: %s", got)
	}
}

func loginReq(h http.Handler, remote, pw string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"password":"`+pw+`"}`))
	r.RemoteAddr = remote
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestLoginRateLimitPerIPAndAccount(t *testing.T) {
	s := secServer(t)
	h := s.Handler()
	// 같은 IP에서 5번 틀리면 잠기고, 맞는 비밀번호도 잠긴 동안은 거부한다.
	for i := 0; i < 5; i++ {
		if w := loginReq(h, "198.51.100.1:1", "wrong-pass", nil); w.Code != 401 && w.Code != 429 {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	w := loginReq(h, "198.51.100.1:1", "password123", nil)
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("correct password while locked: %d retry-after=%q", w.Code, w.Header().Get("Retry-After"))
	}
	// 다른 IP는 영향 없음
	if w := loginReq(h, "198.51.100.2:1", "password123", nil); w.Code != 204 {
		t.Errorf("other IP blocked: %d", w.Code)
	}
	// IP를 계속 바꾸는 공격은 계정 단위 한도(20회)에서 막힌다.
	s2 := secServer(t)
	h2 := s2.Handler()
	var locked bool
	for i := 0; i < 40; i++ {
		w := loginReq(h2, "203.0.113."+strconv.Itoa(i+1)+":1", "wrong-pass", nil)
		if w.Code == 429 {
			locked = true
			break
		}
	}
	if !locked {
		t.Error("account never locked while rotating IPs")
	}
	if w := loginReq(h2, "203.0.113.200:1", "password123", nil); w.Code != 429 {
		t.Errorf("account lock does not apply to a new IP: %d", w.Code)
	}
}

func TestLimiterBackoffGrowsAndExpires(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := newLimiter(3, 10*time.Second, time.Minute, time.Hour)
	l.now = func() time.Time { return now }
	for i := 0; i < 2; i++ {
		l.fail("k")
	}
	if l.wait("k") != 0 {
		t.Fatal("locked before the threshold")
	}
	l.fail("k")
	if w := l.wait("k"); w != 10*time.Second {
		t.Fatalf("first lock = %v", w)
	}
	now = now.Add(11 * time.Second)
	l.fail("k")
	if w := l.wait("k"); w != 20*time.Second {
		t.Fatalf("second lock = %v", w)
	}
	for i := 0; i < 10; i++ {
		now = now.Add(2 * time.Minute)
		l.fail("k")
	}
	if w := l.wait("k"); w != time.Minute {
		t.Fatalf("lock not capped: %v", w)
	}
	l.reset("k")
	if l.wait("k") != 0 {
		t.Fatal("reset did not unlock")
	}
	// 오래 지나면 실패 횟수를 잊는다.
	l.fail("j")
	l.fail("j")
	now = now.Add(2 * time.Hour)
	l.fail("j")
	if l.wait("j") != 0 {
		t.Error("stale failures counted")
	}
}

func TestBearerBruteForceLocksIP(t *testing.T) {
	s := secServer(t)
	h := s.Handler()
	call := func(tok string) int {
		r := httptest.NewRequest("GET", "/api/auth/me", nil)
		r.RemoteAddr = "198.51.100.9:1"
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if call("tok-tok-tok") != 200 {
		t.Fatal("valid token rejected")
	}
	for i := 0; i < 10; i++ {
		call("nope")
	}
	if c := call("tok-tok-tok"); c != 429 {
		t.Errorf("valid token while IP locked: %d", c)
	}
}

func TestCSRFAndCookieAttributes(t *testing.T) {
	s := secServer(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s.Store = st
	h := s.Handler()
	w := loginReq(ViaTrustedProxy(h), "@", "password123", map[string]string{"X-Forwarded-Proto": "https"})
	c := w.Result().Cookies()[0]
	if w.Code != 204 || c.SameSite != http.SameSiteStrictMode || !c.HttpOnly || !c.Secure {
		t.Fatalf("cookie: %d samesite=%v httponly=%v secure=%v", w.Code, c.SameSite, c.HttpOnly, c.Secure)
	}
	call := func(method string, hdr map[string]string) int {
		r := httptest.NewRequest(method, "/api/projects/nope", nil)
		r.Host = "aven.example.com"
		r.RemoteAddr = "198.51.100.1:1"
		r.AddCookie(c)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	// 상태 변경 + 쿠키: 같은 origin / 헤더 없는 CLI만 통과, 나머지는 403
	for name, tc := range map[string]struct {
		hdr  map[string]string
		want bool
	}{
		"same-origin fetch":   {map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "https://aven.example.com"}, true},
		"origin matches host": {map[string]string{"Origin": "https://aven.example.com"}, true},
		"no browser headers":  {nil, true},
		"cross-site":          {map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, false},
		"same-site sibling":   {map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "https://other.example.com"}, false},
		"evil origin":         {map[string]string{"Origin": "https://evil.example"}, false},
		"null origin":         {map[string]string{"Origin": "null"}, false},
		"prefix trick":        {map[string]string{"Origin": "https://aven.example.com.evil.example"}, false},
	} {
		got := call("DELETE", tc.hdr)
		if (got != 403) != tc.want {
			t.Errorf("DELETE %s: %d", name, got)
		}
	}
	if got := call("GET", map[string]string{"Sec-Fetch-Site": "cross-site"}); got == 403 {
		t.Error("safe GET blocked")
	}
	// login/logout CSRF
	if w := loginReq(h, "198.51.100.3:1", "password123", map[string]string{"Origin": "https://evil.example"}); w.Code != 403 {
		t.Errorf("cross-origin login: %d", w.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	s := secServer(t)
	h := s.Handler()
	get := func(proto string) http.Header {
		r := httptest.NewRequest("GET", "/api/auth/me", nil)
		r.RemoteAddr = "198.51.100.1:1"
		if proto != "" {
			r = r.WithContext(withTrusted(r))
			r.Header.Set("X-Forwarded-Proto", proto)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Header()
	}
	hd := get("")
	for k, want := range map[string]string{"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Referrer-Policy": "same-origin"} {
		if hd.Get(k) != want {
			t.Errorf("%s = %q", k, hd.Get(k))
		}
	}
	if !strings.Contains(hd.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Error("no frame-ancestors")
	}
	if hd.Get("Strict-Transport-Security") != "" {
		t.Error("HSTS over plain HTTP")
	}
	if get("https").Get("Strict-Transport-Security") == "" {
		t.Error("no HSTS over HTTPS")
	}
}
