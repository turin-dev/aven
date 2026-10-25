package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestBuildTracedRoute(t *testing.T) {
	c := New(Config{TLS: TLSOff})
	attrs := map[string]string{"keel.environment.id": "env_1", "keel.component": "edge"}
	b, err := c.Build([]Route{
		{Hosts: []string{"traced.example.com"}, Upstreams: []string{"keel-a-1:80"}, Trace: true, TraceAttributes: attrs},
		{Hosts: []string{"plain.example.com"}, Upstreams: []string{"keel-b-1:80"}},
		{Hosts: []string{"paths.example.com"}, Upstreams: []string{"keel-c-1:80"}},
		{Hosts: []string{"paths.example.com"}, Path: "/api/*", Upstreams: []string{"keel-d-1:80"}, Trace: true, TraceAttributes: attrs},
	})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Routes []struct {
						Match  []map[string]any `json:"match"`
						Handle []map[string]any `json:"handle"`
					} `json:"routes"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	byHost := map[string]string{}
	for _, r := range cfg.Apps.HTTP.Servers["keel"].Routes {
		h, _ := json.Marshal(r.Handle)
		m, _ := json.Marshal(r.Match)
		for _, host := range []string{"traced", "plain", "paths"} {
			if strings.Contains(string(m), host+".example.com") {
				byHost[host] = string(h)
			}
		}
	}
	tr := byHost["traced"]
	if i, j := strings.Index(tr, `"handler":"tracing"`), strings.Index(tr, `"handler":"reverse_proxy"`); i < 0 || j < i {
		t.Errorf("tracing handler must come before reverse_proxy: %s", tr)
	}
	if !strings.Contains(tr, `"keel.environment.id":"env_1"`) || !strings.Contains(tr, TraceIDHeader) {
		t.Errorf("span attributes / trace id header: %s", tr)
	}
	if strings.Contains(byHost["plain"], "tracing") {
		t.Errorf("untraced route has tracing: %s", byHost["plain"])
	}
	// 경로 규칙 단위로 켠다: /api/*만 trace
	if p := byHost["paths"]; strings.Count(p, `"handler":"tracing"`) != 1 {
		t.Errorf("path rule tracing: %s", p)
	}
}

func TestTraceEnv(t *testing.T) {
	if env := New(Config{}).traceEnv(); env != nil {
		t.Errorf("no tracing config: %v", env)
	}
	env := New(Config{OTLPEndpoint: "http://host.docker.internal:4318", TraceToken: "tok"}).traceEnv()
	if len(env) != 3 || env[2] != "OTEL_EXPORTER_OTLP_HEADERS=X-Keel-Trace-Token=tok" {
		t.Errorf("trace env: %v", env)
	}
}

func TestParseAccessLogTraceID(t *testing.T) {
	line := `{"level":"info","ts":1700000000.5,"logger":"http.log.access.keel","msg":"handled request","request":{"remote_ip":"1.2.3.4","method":"GET","host":"a.example.com","uri":"/x","headers":{}},"duration":0.01,"size":5,"status":200,"traceID":"4bf92f3577b34da6a3ce929d0e0e4736"}`
	l, ok := ParseAccessLog(line)
	if !ok || l.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace id: %+v %v", l, ok)
	}
}

// 오래된 Caddy가 tracing handler를 거절하면 tracing 없이 다시 넣어 라우팅은 유지한다.
func TestApplyFallsBackWithoutTracing(t *testing.T) {
	var bodies []string
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if strings.Contains(string(b), `"handler":"tracing"`) {
			http.Error(w, `loading module: unknown field "span_attributes"`, http.StatusBadRequest)
		}
	}))
	defer admin.Close()
	u, _ := url.Parse(admin.URL)
	port, _ := strconv.Atoi(u.Port())
	c := New(Config{TLS: TLSOff, AdminPort: port})
	err := c.Apply(context.Background(), []Route{{Hosts: []string{"a.example.com"}, Upstreams: []string{"keel-a-1:80"}, Trace: true}})
	if err != nil || len(bodies) != 2 || strings.Contains(bodies[1], "tracing") || !strings.Contains(bodies[1], "keel-a-1:80") {
		t.Errorf("fallback: %v %d", err, len(bodies))
	}
}
