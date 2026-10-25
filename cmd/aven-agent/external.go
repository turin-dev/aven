package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/turin-dev/aven/internal/proxy"
)

// externalUpstreams는 --external-upstreams를 검사한다(컨테이너 이름이나 사설 IP의 host:port만).
func externalUpstreams(v string) ([]string, error) {
	list := proxy.ParseList(v)
	for _, u := range list {
		if !proxy.ValidExternalUpstream(u) {
			return nil, fmt.Errorf("--external-upstreams: %q is not a container-name or private-IP host:port", u)
		}
	}
	return list, nil
}

// connectProxyNetworks는 keel-proxy를 외부 upstream이 사는 docker 네트워크에 붙인다(실패하면 경고만 한다).
func connectProxyNetworks(ctx context.Context, c *proxy.Caddy, networks []string, log *slog.Logger) {
	for _, n := range networks {
		if err := c.Connect(ctx, n); err != nil {
			log.Warn("keel-proxy could not join network", "network", n, "err", err)
		} else {
			log.Info("keel-proxy joined network", "network", n)
		}
	}
}
