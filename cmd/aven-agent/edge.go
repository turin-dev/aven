package main

import (
	"context"
	"log/slog"

	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/proxy"
)

// startEdge는 이 노드의 엣지 엔진(CDN 캐시, WAF, 엣지 규칙)을 keel-proxy와 같이 쓰는 볼륨의 unix socket에 띄운다.
// 실패하면 엣지 설정 없이 지금처럼 라우팅한다.
func startEdge(ctx context.Context, caddy *proxy.Caddy, docker *node.Docker, log *slog.Logger) {
	mp, err := docker.VolumeMountpoint(ctx, caddy.Resolve(ctx).WakeVolume)
	if err == nil {
		_, err = caddy.StartEdge(ctx, mp, log)
	}
	if err != nil {
		log.Warn("edge engine disabled (CDN caching, WAF and edge rules are not applied on this node)", "err", err)
		return
	}
	log.Info("edge engine listening", "socket", proxy.EdgeSocket)
}
