package deploy

import (
	"context"
	"time"

	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/store"
)

// attachEdge는 인스턴스의 엣지 설정(CDN/WAF/엣지 규칙)을 호스트 라우트에 붙인다. 켠 기능이 없으면 붙이지 않는다.
func (m *Manager) attachEdge(ctx context.Context, in *store.Instance, rt *proxy.Route) {
	settings, err := m.Store.GetEdgeSettings(ctx, in.ID)
	if err != nil {
		m.Log.Warn("edge settings", "instance", in.ID, "err", err)
		return
	}
	if settings.Enabled() {
		rt.InstanceID, rt.Edge = in.ID, &settings
	}
}

// edgePurgeOnDeploy는 CDN 캐시를 쓰는 인스턴스의 현재 배포가 새로 live가 됐으면 purge_on_deploy대로 캐시 세대를 올린다

func (m *Manager) edgePurgeOnDeploy(ctx context.Context, insts []*store.Instance) {
	all, err := m.Store.ListEdgeSettings(ctx)
	if err != nil {
		m.Log.Warn("edge settings", "err", err)
		return
	}
	for _, in := range insts {
		settings, ok := all[in.ID]
		if !ok || !settings.CDN.Enabled || in.CurrentDeploymentID == nil || settings.CDN.LastDeployPurged == *in.CurrentDeploymentID {
			continue
		}
		d, err := m.Store.GetDeployment(ctx, *in.CurrentDeploymentID)
		if err != nil || d.Status != store.DepLive {
			continue
		}
		first := settings.CDN.LastDeployPurged == ""
		settings.CDN.LastDeployPurged = d.ID
		// 처음 기록할 때(캐시를 막 켰을 때)는 비울 것이 없다.
		if !first && settings.CDN.PurgeOnDeploy != "off" {
			now := time.Now().UTC()
			settings.CDN.HTMLGeneration++
			settings.CDN.LastPurgedHTML = &now
			if settings.CDN.PurgeOnDeploy == "all" {
				settings.CDN.AllGeneration++
				settings.CDN.LastPurgedAll = &now
			}
		}
		if err := m.Store.SaveEdgeSettings(ctx, in.ID, settings); err != nil {
			m.Log.Warn("edge purge on deploy", "instance", in.ID, "err", err)
		}
	}
}
