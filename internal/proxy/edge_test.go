package proxy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/edge"
)

type fakeEdge struct{ got map[string]edge.Policy }

func (f *fakeEdge) Configure(p map[string]edge.Policy) { f.got = p }

type builtServers struct {
	Apps struct {
		HTTP struct {
			Servers map[string]struct {
				Listen []string         `json:"listen"`
				Routes []map[string]any `json:"routes"`
				Logs   any              `json:"logs"`
			} `json:"servers"`
		} `json:"http"`
	} `json:"apps"`
}

func edgeOn() *edge.Settings {
	s := edge.DefaultSettings()
	s.CDN.Enabled = true
	return &s
}

func TestBuildRoutesEdgeHostsThroughEngine(t *testing.T) {
	c := New(Config{TLS: TLSAuto})
	routes := []Route{
		{Hosts: []string{"cdn.example.com"}, Upstreams: []string{"keel-web-1:8080", "keel-web-2:8080"}, InstanceID: "ins_1", Edge: edgeOn(), TrustProto: true,
			Auth: &BasicAuth{User: "u", Hash: "$2a$10$abcdefghijklmnopqrstuuabcdefghijklmnopqrstuvwxyzABCDE"}},
		{Hosts: []string{"cdn.example.com"}, Path: "/api/*", Upstreams: []string{"keel-api-1:3000"}, InstanceID: "ins_1", Edge: edgeOn()},
		{Hosts: []string{"plain.example.com"}, Upstreams: []string{"keel-plain-1:80"}},
		{Hosts: []string{"old.example.com"}, Redirect: &Redirect{To: "cdn.example.com"}, InstanceID: "ins_1", Edge: edgeOn()},
		{Hosts: []string{"off.example.com"}, Upstreams: []string{"keel-off-1:80"}, InstanceID: "ins_3", Edge: func() *edge.Settings { s := edge.DefaultSettings(); return &s }()},
	}
	// 엔진이 없으면 엣지 설정을 무시한다(지금과 같은 설정).
	plain, _ := c.Build(routes)
	if strings.Contains(string(plain), "edge.sock") || strings.Contains(string(plain), EdgeOriginServer) {
		t.Fatalf("edge config without an engine: %s", plain)
	}
	sink := &fakeEdge{}
	c.SetEdge(sink)
	b, err := c.Build(routes)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	var cfg builtServers
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	origin, ok := cfg.Apps.HTTP.Servers[EdgeOriginServer]
	if !ok || origin.Listen[0] != "unix/"+EdgeOriginSocket || origin.Logs != nil {
		t.Fatalf("origin server: %+v", origin)
	}
	ob, _ := json.Marshal(origin.Routes)
	os := string(ob)
	// origin 서버는 cdn 호스트의 경로 규칙과 replica를 그대로 갖고, basic auth는 다시 하지 않는다.
	for _, want := range []string{`"host":["cdn.example.com"]`, `"dial":"keel-web-1:8080"`, `"dial":"keel-api-1:3000"`, `"round_robin"`, `"X-Forwarded-For":["{http.request.header.X-Aven-Client-Ip}"]`, `"delete":["X-Aven-Client-Ip","X-Aven-Client-Proto"]`} {
		if !strings.Contains(os, want) {
			t.Errorf("origin missing %s: %s", want, os)
		}
	}
	for _, bad := range []string{"plain.example.com", "old.example.com", "off.example.com", "authentication"} {
		if strings.Contains(os, bad) {
			t.Errorf("origin should not contain %s: %s", bad, os)
		}
	}
	main := cfg.Apps.HTTP.Servers["aven"]
	mb, _ := json.Marshal(main.Routes)
	ms := string(mb)
	for _, want := range []string{`"dial":"unix//run/aven/edge.sock"`, `"X-Aven-Client-Ip":["{http.request.remote.host}"]`, `"X-Aven-Client-Proto":["{http.request.header.X-Forwarded-Proto}"]`, `"authentication"`, `"dial":"keel-plain-1:80"`, `"dial":"keel-off-1:80"`} {
		if !strings.Contains(ms, want) {
			t.Errorf("main missing %s", want)
		}
	}
	// cdn 호스트의 컨테이너는 main 서버에 직접 나오지 않는다(엔진을 거친다).
	if strings.Contains(ms, "keel-web-1:8080") || strings.Contains(ms, "keel-api-1:3000") {
		t.Errorf("edge host upstream leaked into the main server: %s", ms)
	}
	if !strings.Contains(s, `"Location":["https://cdn.example.com{http.request.uri}"]`) {
		t.Error("redirect domain should stay a plain redirect")
	}
	pol := EdgePolicies(routes)
	if len(pol) != 1 || pol["cdn.example.com"].InstanceID != "ins_1" || !pol["cdn.example.com"].Settings.CDN.Enabled {
		t.Fatalf("policies: %+v", pol)
	}
}

func TestApplyConfiguresEngineFirst(t *testing.T) {
	c := New(Config{TLS: TLSOff, AdminPort: 1}) // 닿지 않는 admin: Apply는 실패하지만 엔진은 먼저 설정된다
	sink := &fakeEdge{}
	c.SetEdge(sink)
	c.Apply(context.Background(), []Route{{Hosts: []string{"a.example.com"}, Upstreams: []string{"keel-a-1:80"}, InstanceID: "ins_a", Edge: edgeOn()}})
	if sink.got["a.example.com"].InstanceID != "ins_a" {
		t.Fatalf("engine not configured: %+v", sink.got)
	}
}
