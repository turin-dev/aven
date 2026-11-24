package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCheckEnv(t *testing.T) {
	for _, tc := range []struct {
		name   string
		env    map[string]string
		listen string
		want   string
	}{
		{"ok", map[string]string{"KEEL_ADMIN_PASSWORD": "long-enough"}, "[::1]:7070", ""},
		{"short password", map[string]string{"KEEL_ADMIN_PASSWORD": "short"}, "[::1]:7070", "KEEL_ADMIN_PASSWORD"},
		{"no port", map[string]string{"KEEL_ADMIN_PASSWORD": "long-enough"}, "localhost", "AVEN_LISTEN"},
		{"empty listen", map[string]string{"KEEL_ADMIN_PASSWORD": "long-enough"}, " ", "listen address is empty"},
		{"cert without key", map[string]string{"KEEL_ADMIN_PASSWORD": "long-enough", "KEEL_TLS_CERT": "/c.pem"}, ":7070", "set together"},
		{"bad proxies", map[string]string{"KEEL_ADMIN_PASSWORD": "long-enough", "KEEL_TRUSTED_PROXIES": "10.0.0.0/8,nope"}, ":7070", "KEEL_TRUSTED_PROXIES"},
		{"new name", map[string]string{"AVEN_ADMIN_PASSWORD": "long-enough"}, "[::1]:7070", ""},
		{"new name short", map[string]string{"AVEN_ADMIN_PASSWORD": "short"}, "[::1]:7070", "AVEN_ADMIN_PASSWORD"},
		{"tcp allowlist", map[string]string{"AVEN_ADMIN_PASSWORD": "long-enough", "AVEN_TCP_PORT_ALLOW_CIDRS": "10.0.0.0/8, 203.0.113.7"}, "[::1]:7070", ""},
		{"session max age days", map[string]string{"AVEN_ADMIN_PASSWORD": "long-enough", "AVEN_SESSION_MAX_AGE": "30d"}, "[::1]:7070", ""},
		{"session max age off", map[string]string{"AVEN_ADMIN_PASSWORD": "long-enough", "AVEN_SESSION_MAX_AGE": "off"}, "[::1]:7070", ""},
		{"bad session max age", map[string]string{"AVEN_ADMIN_PASSWORD": "long-enough", "AVEN_SESSION_MAX_AGE": "5m"}, "[::1]:7070", "SESSION_MAX_AGE"},
		{"bad tcp allowlist", map[string]string{"AVEN_ADMIN_PASSWORD": "long-enough", "AVEN_TCP_PORT_ALLOW_CIDRS": "10.0.0.0/8,nope"}, "[::1]:7070", "TCP_PORT_ALLOW_CIDRS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range []string{"AVEN_ADMIN_PASSWORD", "AVEN_TLS_CERT", "AVEN_TLS_KEY", "AVEN_TRUSTED_PROXIES", "KEEL_ADMIN_PASSWORD", "KEEL_TLS_CERT", "KEEL_TLS_KEY", "KEEL_TRUSTED_PROXIES", "AVEN_TCP_PORT_ALLOW_CIDRS", "KEEL_TCP_PORT_ALLOW_CIDRS", "AVEN_SESSION_MAX_AGE", "KEEL_SESSION_MAX_AGE"} {
				t.Setenv(k, tc.env[k])
			}
			err := checkEnv(tc.listen)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Errorf("err %v, want %q", err, tc.want)
			}
		})
	}
}

// TestShutdownServersDrainsThenEndsStreams: a short request finishes normally, a stream that only ends
// with its context is cancelled after the drain timeout, and shutdown does not hang.
func TestShutdownServersDrainsThenEndsStreams(t *testing.T) {
	oldDrain, oldStream := drainTimeout, streamTimeout
	drainTimeout, streamTimeout = 200*time.Millisecond, time.Second
	defer func() { drainTimeout, streamTimeout = oldDrain, oldStream }()

	reqCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	streamEnded := make(chan struct{})
	started := make(chan struct{}, 2)
	srv := &http.Server{
		BaseContext: func(net.Listener) context.Context { return reqCtx },
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started <- struct{}{}
			if r.URL.Path == "/slow" {
				time.Sleep(100 * time.Millisecond)
				fmt.Fprint(w, "done")
				return
			}
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			close(streamEnded)
		}),
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	base := "http://" + ln.Addr().String()
	streamResp, err := http.Get(base + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer streamResp.Body.Close()
	<-started
	var wg sync.WaitGroup
	var slowBody string
	wg.Go(func() {
		resp, err := http.Get(base + "/slow")
		if err != nil {
			t.Error(err)
			return
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		slowBody = string(b)
	})
	<-started
	begin := time.Now()
	shutdownServers(slog.New(slog.NewTextHandler(io.Discard, nil)), []*http.Server{srv}, cancelRequests)
	wg.Wait()
	select {
	case <-streamEnded:
	default:
		t.Error("the stream's request context was not cancelled")
	}
	if slowBody != "done" {
		t.Errorf("in-flight request was cut off: %q", slowBody)
	}
	if d := time.Since(begin); d > 2*time.Second {
		t.Errorf("shutdown took %v", d)
	}
	if _, err := http.Get(base + "/slow"); err == nil {
		t.Error("server still accepts connections after shutdown")
	}
}

func TestWaitTimeout(t *testing.T) {
	var wg sync.WaitGroup
	if !waitTimeout(&wg, time.Millisecond) {
		t.Error("empty group")
	}
	block := make(chan struct{})
	wg.Go(func() { <-block })
	if waitTimeout(&wg, 20*time.Millisecond) {
		t.Error("blocked group reported done")
	}
	close(block)
	if !waitTimeout(&wg, time.Second) {
		t.Error("finished group")
	}
}

func TestRequireHeaderSecret(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	cases := []struct {
		name, secret, sent string
		want               int
	}{
		{"matching secret", "s3cret", "s3cret", http.StatusNoContent},
		{"missing header", "s3cret", "", http.StatusForbidden},
		{"wrong secret", "s3cret", "s3cre", http.StatusForbidden},
		{"empty secret refuses all", "", "", http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := requireHeaderSecret("X-Test-Secret", c.secret, ok)
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if c.sent != "" {
				r.Header.Set("X-Test-Secret", c.sent)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != c.want {
				t.Fatalf("status = %d, want %d", w.Code, c.want)
			}
		})
	}
}

func TestWakeSecretIsNotForwarded(t *testing.T) {
	var forwarded string
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded = r.Header.Get(wakeSecretHeader) })
	h := requireHeaderSecret(wakeSecretHeader, "s3cret", stripHeader(wakeSecretHeader, app))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set(wakeSecretHeader, "s3cret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || forwarded != "" {
		t.Fatalf("status %d, app saw secret %q", w.Code, forwarded)
	}
}
