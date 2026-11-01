package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGETRoutesSweep calls every parameterless-after-substitution GET route as the server admin on a small
// fixture project. No route may panic, answer 5xx, or answer a bare JSON null (lists must be []).
func TestGETRoutesSweep(t *testing.T) {
	f, h := authServer(t)
	vols, err := f.st.ListVolumesByEnv(f.ctx, f.prod.ID)
	if err != nil || len(vols) == 0 {
		t.Fatalf("fixture volume: %v", err)
	}
	skip := []string{
		"stream", "/exec", "/shell", "http-logs", "build-logs", "deploy-logs", // SSE / WebSocket
		"/api/system",              // needs the proxy admin API
		"/api/owner/server-config", // reads /etc/keel
		"/api/github/oauth/start",  // needs an OAuth app
	}
	for _, sid := range []string{f.web.ID, f.db.ID} {
		repl := strings.NewReplacer("{pid}", f.p.ID, "{eid}", f.prod.ID, "{sid}", sid, "{vid}", vols[0].ID)
	routes:
		for _, rt := range f.srv.Routes() {
			method, pattern, _ := strings.Cut(rt.Pattern, " ")
			if method != "GET" {
				continue
			}
			for _, s := range skip {
				if strings.Contains(pattern, s) {
					continue routes
				}
			}
			p := repl.Replace(pattern)
			if strings.Contains(p, "{") {
				continue
			}
			var w *httptest.ResponseRecorder
			func() {
				defer func() {
					if e := recover(); e != nil {
						t.Errorf("GET %s panicked: %v", p, e)
					}
				}()
				w = req(h, "GET", p, "", withBearer("legacy-token"))
			}()
			if w == nil {
				continue
			}
			if body := strings.TrimSpace(w.Body.String()); w.Code >= 500 || body == "null" {
				t.Errorf("GET %s: %d %.300s", p, w.Code, body)
			}
		}
	}
}
