package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/turin-dev/aven/internal/compat"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/edge"
)

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// TestEdgeWithRealCaddy는 실제 Caddy 바이너리로 client → Caddy → 엣지 엔진 → Caddy origin 서버 → upstream 경로를 확인한다.
// KEEL_CADDY_BIN=/path/to/caddy 일 때만 돈다(docker 없이 호스트에서 실행한다).
func TestEdgeWithRealCaddy(t *testing.T) {
	bin := compat.Env("CADDY_BIN")
	if bin == "" {
		t.Skip("set KEEL_CADDY_BIN to run against a real Caddy")
	}
	dir, err := os.MkdirTemp("/tmp", "keel-edge-") // unix socket 경로 길이 제한 때문에 짧은 경로
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	var seen http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		if r.URL.Path == "/app.js" {
			w.Header().Set("Content-Type", "text/javascript")
		}
		fmt.Fprintf(w, "hello %s", r.URL.Path)
	}))
	defer up.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	httpPort, adminPort := freePort(t), freePort(t)
	c := New(Config{TLS: TLSOff, AdminPort: adminPort})
	if _, err := c.StartEdge(ctx, dir, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	s := edge.DefaultSettings()
	s.CDN.Enabled = true
	if err := json.Unmarshal([]byte(`{"version":1,"rules":[{"priority":1,"if":{"attr":"http.path","op":"matches","value":"/admin*"},"then":{"action":"block"}}]}`), &s.Rules); err != nil {
		t.Fatal(err)
	}
	routes := []Route{{Hosts: []string{"app.test"}, Upstreams: []string{strings.TrimPrefix(up.URL, "http://")}, InstanceID: "ins_1", Edge: &s}}
	c.edge.Configure(EdgePolicies(routes))
	b, err := c.Build(routes)
	if err != nil {
		t.Fatal(err)
	}
	cfg := strings.ReplaceAll(string(b), WakeDir+"/", dir+"/")
	cfg = strings.ReplaceAll(cfg, `"0.0.0.0:2019"`, fmt.Sprintf(`"127.0.0.1:%d"`, adminPort))
	cfg = strings.ReplaceAll(cfg, `":80"`, fmt.Sprintf(`"127.0.0.1:%d"`, httpPort))
	file := filepath.Join(dir, "caddy.json")
	os.WriteFile(file, []byte(cfg), 0o600)
	cmd := exec.CommandContext(ctx, bin, "run", "--config", file)
	cmd.Env = append(os.Environ(), "HOME="+dir, "XDG_DATA_HOME="+dir, "XDG_CONFIG_HOME="+dir)
	out := &strings.Builder{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); cmd.Wait() }()
	get := func(path string) *http.Response {
		t.Helper()
		var resp *http.Response
		for i := 0; i < 50; i++ {
			req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d%s", httpPort, path), nil)
			req.Host = "app.test"
			req.Header.Set(edge.HeaderClientIP, "6.6.6.6") // 위조 시도: Caddy가 덮어써야 한다
			if resp, err = http.DefaultClient.Do(req); err == nil {
				return resp
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("caddy not reachable: %v\n%s", err, out)
		return nil
	}
	r1 := get("/app.js")
	body, _ := io.ReadAll(r1.Body)
	r1.Body.Close()
	if r1.StatusCode != 200 || string(body) != "hello /app.js" || r1.Header.Get("X-Cache") != "MISS" {
		t.Fatalf("first: %d %q %v\n%s", r1.StatusCode, body, r1.Header, out)
	}
	if xff := seen.Get("X-Forwarded-For"); xff != "127.0.0.1" || seen.Get(edge.HeaderClientIP) != "" || seen.Get("X-Forwarded-Proto") != "http" {
		t.Fatalf("upstream headers: %v", seen)
	}
	r2 := get("/app.js")
	r2.Body.Close()
	if r2.Header.Get("X-Cache") != "HIT" {
		t.Fatalf("second: %v", r2.Header)
	}
	r3 := get("/admin/x")
	r3.Body.Close()
	if r3.StatusCode != 403 || r3.Header.Get("X-Keel-Edge") != "rule" {
		t.Fatalf("rule: %d %v", r3.StatusCode, r3.Header)
	}
}
