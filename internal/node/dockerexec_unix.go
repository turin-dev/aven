//go:build !windows

package node

import (
	"context"
	"errors"
	"net"
	"strings"
)

func defaultDockerSocket() string { return "/var/run/docker.sock" }

func parseDockerSocket(endpoint string) (string, error) {
	if p, ok := strings.CutPrefix(endpoint, "unix://"); ok && p != "" {
		return p, nil
	}
	return "", errors.New("exec supports only a unix socket DOCKER_HOST on this platform")
}

func dialDockerSocket(ctx context.Context, socket string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", socket)
}
