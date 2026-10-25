package deploy

import (
	"context"
	"testing"

	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/store"
)

func TestSyncProxyExternalUpstreams(t *testing.T) {
	ctx := context.Background()
	f := newScaleFixture(t)
	var got []proxy.Route
	f.m.localProxy = func(_ context.Context, rs []proxy.Route) error { got = rs; return nil }
	f.m.Cfg.ExternalUpstreams = []string{"grafana-1:3000", "mailu-front-1:80"}
	// 배포 없이도(서비스가 offline이어도) 라우팅한다.
	in, _ := f.st.GetInstance(ctx, f.in.ID)
	in.Domains = []store.Domain{
		{Host: "dash.example.com", Kind: "custom", Upstream: "grafana-1:3000"},
		{Host: "mailu-api.example.com", Kind: "custom", Upstream: "mailu-front-1:80", UpstreamPaths: []string{"/api/v1"}},
		{Host: "evil.example.com", Kind: "custom", Upstream: "keel-proxy-admin:2019"}, // 허용 목록 밖
	}
	if err := f.st.UpdateInstanceConfig(ctx, in); err != nil {
		t.Fatal(err)
	}
	f.m.SyncProxy(ctx)
	by := map[string][]proxy.Route{}
	for _, r := range got {
		by[r.Hosts[0]] = append(by[r.Hosts[0]], r)
	}
	if rs := by["dash.example.com"]; len(rs) != 1 || rs[0].Upstreams[0] != "grafana-1:3000" || rs[0].Path != "" {
		t.Fatalf("whole-host upstream: %+v", rs)
	}
	if rs := by["mailu-api.example.com"]; len(rs) != 1 || rs[0].Path != "/api/v1/*" || rs[0].Upstreams[0] != "mailu-front-1:80" || !rs[0].PathsOnly {
		t.Fatalf("path-limited upstream: %+v", rs)
	}
	if rs := by["evil.example.com"]; len(rs) != 0 {
		t.Fatalf("an upstream the node did not allow must not be routed: %+v", rs)
	}
}

func TestSyncProxySelfRoute(t *testing.T) {
	ctx := context.Background()
	f := newScaleFixture(t)
	var got []proxy.Route
	f.m.localProxy = func(_ context.Context, rs []proxy.Route) error { got = rs; return nil }
	f.m.Cfg.SelfHost, f.m.Cfg.SelfUpstream = "aven.example.com", "unix//run/keel/keel-server.sock"
	f.m.Cfg.SelfHeaders = map[string]string{"X-Keel-Gateway": "s3cret"}
	f.m.SyncProxy(ctx)
	var self *proxy.Route
	for i := range got {
		if got[i].Hosts[0] == "aven.example.com" {
			self = &got[i]
		}
	}
	if self == nil || self.Upstreams[0] != "unix//run/keel/keel-server.sock" || self.SetHeaders["X-Keel-Gateway"] != "s3cret" {
		t.Fatalf("self route: %+v", self)
	}
	// 서비스 도메인이 같은 호스트를 쓰면 aven 자신은 라우팅하지 않는다(서비스를 덮어쓰지 않는다).
	in, _ := f.st.GetInstance(ctx, f.in.ID)
	in.Domains = append(in.Domains, store.Domain{Host: "aven.example.com", Kind: "custom", Upstream: "x-1:80"})
	f.st.UpdateInstanceConfig(ctx, in)
	f.m.SyncProxy(ctx)
	for _, r := range got {
		if r.Hosts[0] == "aven.example.com" && len(r.SetHeaders) > 0 {
			t.Fatal("self route must not shadow a service domain")
		}
	}
}
