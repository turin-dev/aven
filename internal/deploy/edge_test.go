package deploy

import (
	"context"
	"testing"

	"github.com/turin-dev/aven/internal/proxy"
)

func TestSyncProxyAttachesEdgeAndPurgesOnDeploy(t *testing.T) {
	ctx := context.Background()
	f := newScaleFixture(t)
	var got []proxy.Route
	f.m.localProxy = func(_ context.Context, rs []proxy.Route) error { got = rs; return nil }
	d1 := f.live(t)

	// 엣지 설정이 없으면 라우트에 붙지 않는다.
	f.m.SyncProxy(ctx)
	if len(got) == 0 || got[0].Edge != nil {
		t.Fatalf("routes without edge: %+v", got)
	}
	settings, _ := f.st.GetEdgeSettings(ctx, f.in.ID)
	settings.CDN.Enabled = true
	if err := f.st.SaveEdgeSettings(ctx, f.in.ID, settings); err != nil {
		t.Fatal(err)
	}
	f.m.SyncProxy(ctx)
	if len(got) == 0 || got[0].Edge == nil || got[0].InstanceID != f.in.ID || !got[0].Edge.CDN.Enabled {
		t.Fatalf("edge not attached: %+v", got)
	}
	// 처음 본 배포는 기록만 한다(비울 캐시가 없다).
	s1, _ := f.st.GetEdgeSettings(ctx, f.in.ID)
	if s1.CDN.LastDeployPurged != d1.ID || s1.CDN.HTMLGeneration != 0 {
		t.Fatalf("first deploy: %+v", s1.CDN)
	}
	// 새 배포가 live가 되면 purge_on_deploy=html이 HTML 세대를 올린다. 같은 배포로 다시 sync해도 한 번뿐이다.
	d2 := f.live(t)
	f.m.SyncProxy(ctx)
	f.m.SyncProxy(ctx)
	s2, _ := f.st.GetEdgeSettings(ctx, f.in.ID)
	if s2.CDN.LastDeployPurged != d2.ID || s2.CDN.HTMLGeneration != 1 || s2.CDN.AllGeneration != 0 || s2.CDN.LastPurgedHTML == nil {
		t.Fatalf("html purge on deploy: %+v", s2.CDN)
	}
	if got[0].Edge.CDN.HTMLGeneration != 1 {
		t.Fatalf("route carries the old generation: %+v", got[0].Edge.CDN)
	}
	s2.CDN.PurgeOnDeploy = "all"
	f.st.SaveEdgeSettings(ctx, f.in.ID, s2)
	f.live(t)
	f.m.SyncProxy(ctx)
	s3, _ := f.st.GetEdgeSettings(ctx, f.in.ID)
	if s3.CDN.HTMLGeneration != 2 || s3.CDN.AllGeneration != 1 {
		t.Fatalf("purge all on deploy: %+v", s3.CDN)
	}
	s3.CDN.PurgeOnDeploy = "off"
	f.st.SaveEdgeSettings(ctx, f.in.ID, s3)
	f.live(t)
	f.m.SyncProxy(ctx)
	if s4, _ := f.st.GetEdgeSettings(ctx, f.in.ID); s4.CDN.HTMLGeneration != 2 || s4.CDN.AllGeneration != 1 {
		t.Fatalf("purge off: %+v", s4.CDN)
	}
}
