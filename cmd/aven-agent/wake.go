package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/turin-dev/aven/internal/agent"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/proxy"
)

// startWake는 이 노드의 keel-proxy와 같이 쓰는 볼륨의 unix socket에서 wake 요청을 받는다.
// 잠든 서비스의 라우트가 이 socket을 가리키며, 외부에는 열리지 않는다. 실패해도 agent는 계속 돈다(잠든 서비스만 못 깨운다).
func startWake(ctx context.Context, h *agent.Handler, docker *node.Docker, log *slog.Logger) {
	mp, err := docker.VolumeMountpoint(ctx, proxy.WakeVolume)
	if err != nil {
		log.Warn("wake handler disabled (sleeping services cannot wake on this node)", "err", err)
		return
	}
	sock := filepath.Join(mp, "wake.sock")
	os.Remove(sock)
	l, err := net.Listen("unix", sock)
	if err != nil {
		log.Warn("wake handler disabled", "err", err)
		return
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		l.Close()
		log.Warn("wake handler disabled", "err", err)
		return
	}
	srv := &http.Server{Handler: h.WakeHandler(), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(l)
	go func() { <-ctx.Done(); srv.Close() }()
	log.Info("wake handler listening", "socket", sock)
}
