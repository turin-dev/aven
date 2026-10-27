package proxy

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/turin-dev/aven/internal/compat"
	"github.com/turin-dev/aven/internal/edge"
)

// 엣지(CDN 캐시, WAF, 엣지 규칙) 경로:
//
//	client → keel-proxy :443 (TLS, basic auth) → unix EdgeSocket (aven-server/aven-agent의 edge.Engine)
//	       → unix EdgeOriginSocket (keel-proxy의 origin 서버: 원래 경로 규칙과 replica 분산) → 컨테이너
//
// 두 socket 모두 wake socket과 같은 공유 볼륨(WakeVolume)에 있어 밖에서는 닿지 않는다.
// 엣지 설정이 없는 호스트는 지금처럼 바로 컨테이너로 간다.
const (
	EdgeSocket       = WakeDir + "/edge.sock"
	EdgeOriginSocket = WakeDir + "/edge-origin.sock"
	EdgeOriginServer = "aven-edge-origin"
)

// EdgeSink는 Apply 때 호스트별 엣지 정책을 받는 쪽이다(edge.Engine).
type EdgeSink interface {
	Configure(map[string]edge.Policy)
}

// SetEdge는 엣지 엔진을 붙인다. 붙이지 않으면 Route.Edge는 무시된다.
func (c *Caddy) SetEdge(s EdgeSink) { c.edge = s }

// EdgePolicies는 라우트에서 호스트 → 엣지 정책 표를 만든다(Build와 같은 규칙: 호스트마다 처음 값이 있는 것).
func EdgePolicies(routes []Route) map[string]edge.Policy {
	out := map[string]edge.Policy{}
	for _, r := range routes {
		if r.Edge == nil || !r.Edge.Enabled() || r.Redirect != nil {
			continue
		}
		for _, h := range r.Hosts {
			if _, ok := out[h]; !ok {
				out[h] = edge.Policy{InstanceID: r.InstanceID, Settings: *r.Edge}
			}
		}
	}
	return out
}

// edgeForward는 요청을 엣지 엔진으로 보내는 handler다. 클라이언트 IP와 scheme은 Caddy가 덮어써서 위조할 수 없다.
func (h *hostCfg) edgeForward() map[string]any {
	proto := "{http.request.scheme}"
	if h.trustProto {
		proto = "{http.request.header.X-Forwarded-Proto}"
	}
	return map[string]any{
		"handler":            "reverse_proxy",
		"upstreams":          []map[string]any{{"dial": "unix/" + h.socket("edge.sock")}},
		"flush_interval":     -1,
		"stream_close_delay": StreamCloseDelay,
		"headers": map[string]any{"request": map[string]any{
			"set": map[string][]string{edge.HeaderClientIP: {"{http.request.remote.host}"}, edge.HeaderClientProto: {proto}},
		}},
	}
}

// originHeaders는 origin 서버가 컨테이너로 보낼 때 엔진이 넘긴 클라이언트 정보를 X-Forwarded-*로 되돌린다.
func originHeaders() map[string]any {
	return map[string]any{"request": map[string]any{
		"set": map[string][]string{
			"X-Forwarded-For":   {"{http.request.header." + edge.HeaderClientIP + "}"},
			"X-Forwarded-Proto": {"{http.request.header." + edge.HeaderClientProto + "}"},
		},
		"delete": []string{edge.HeaderClientIP, edge.HeaderClientProto},
	}}
}

func (h *hostCfg) socket(name string) string {
	dir := h.wakeDir
	if dir == "" {
		dir = WakeDir
	}
	return dir + "/" + name
}

func originServer(dir string, routes []map[string]any) map[string]any {
	if dir == "" {
		dir = WakeDir
	}
	routes = append(routes, map[string]any{"handle": []map[string]any{{"handler": "static_response", "status_code": 404, "body": "not found"}}, "terminal": true})
	// access log는 남기지 않는다(같은 요청이 :443 서버에서 이미 기록된다).
	return map[string]any{
		"listen":          []string{"unix/" + dir + "/edge-origin.sock"},
		"automatic_https": map[string]any{"disable": true},
		"routes":          routes,
	}
}

// origin socket 파일은 Caddy가 listen 전에 지우므로(남은 파일 정리) 따로 다루지 않는다.
// StartEdge는 공유 볼륨의 호스트 경로(hostDir)에 엣지 엔진 socket을 열고 Caddy에 붙인다.
// 캐시 크기는 KEEL_EDGE_CACHE_MB(기본 256), 항목 하나의 최대 크기는 KEEL_EDGE_CACHE_MAX_ITEM_MB(기본 32)다.
func (c *Caddy) StartEdge(ctx context.Context, hostDir string, log *slog.Logger) (*edge.Engine, error) {
	sock := filepath.Join(hostDir, filepath.Base(EdgeSocket))
	origin := filepath.Join(hostDir, filepath.Base(EdgeOriginSocket))
	os.Remove(sock)
	l, err := net.Listen("unix", sock)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	mb := func(suffix string, def int) int64 {
		if n, err := strconv.Atoi(compat.Env(suffix)); err == nil && n > 0 {
			return int64(n) << 20
		}
		return int64(def) << 20
	}
	e := edge.NewEngine(edge.UnixTransport(origin), mb("EDGE_CACHE_MB", 256), mb("EDGE_CACHE_MAX_ITEM_MB", 32))
	e.Log = log
	srv := &http.Server{Handler: e, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
	go srv.Serve(l)
	go func() { <-ctx.Done(); srv.Close() }()
	c.SetEdge(e)
	return e, nil
}
