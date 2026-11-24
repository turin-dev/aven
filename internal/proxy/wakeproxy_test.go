package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"
)

// The Rewrite-based proxy must hand the app the same request the Director-based one did.
func TestWakeReverseProxyMatchesDirectorBehavior(t *testing.T) {
	var got []*http.Request
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Clone(r.Context()))
	}))
	defer app.Close()
	target, _ := url.Parse(app.URL)

	// The previous implementation, kept here as the reference the new one must match.
	legacy := httputil.NewSingleHostReverseProxy(target)
	//lint:ignore SA1019 reference implementation for the comparison
	orig := legacy.Director
	//lint:ignore SA1019 reference implementation for the comparison
	legacy.Director = func(req *http.Request) { orig(req); req.Host = "shop.example.com" }

	for _, h := range []http.Handler{legacy, WakeReverseProxy(target)} {
		r := httptest.NewRequest(http.MethodPost, "http://shop.example.com/cart/add?id=7", nil)
		r.RemoteAddr = "172.18.0.5:41000"
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-Host", "shop.example.com")
		r.Header.Set("Connection", "X-Secret")
		r.Header.Set("X-Secret", "hop-by-hop")
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	if len(got) != 2 {
		t.Fatalf("app saw %d requests", len(got))
	}
	old, now := got[0], got[1]
	for _, c := range []struct{ name, old, now string }{
		{"host", old.Host, now.Host},
		{"path", old.URL.RequestURI(), now.URL.RequestURI()},
		{"x-forwarded-for", old.Header.Get("X-Forwarded-For"), now.Header.Get("X-Forwarded-For")},
		{"x-forwarded-proto", old.Header.Get("X-Forwarded-Proto"), now.Header.Get("X-Forwarded-Proto")},
		{"x-forwarded-host", old.Header.Get("X-Forwarded-Host"), now.Header.Get("X-Forwarded-Host")},
		{"hop-by-hop", old.Header.Get("X-Secret"), now.Header.Get("X-Secret")},
	} {
		if c.old != c.now {
			t.Errorf("%s: Director gave %q, Rewrite gives %q", c.name, c.old, c.now)
		}
	}
	if now.Header.Get("X-Forwarded-For") != "203.0.113.9, 172.18.0.5" || now.Host != "shop.example.com" || now.Header.Get("X-Forwarded-Proto") != "https" {
		t.Errorf("unexpected request: host %q xff %q proto %q", now.Host, now.Header.Get("X-Forwarded-For"), now.Header.Get("X-Forwarded-Proto"))
	}
}
