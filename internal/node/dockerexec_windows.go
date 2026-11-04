//go:build windows

package node

import (
	"context"
	"errors"
	"net"
	"strings"

	winio "github.com/Microsoft/go-winio"
)

const defaultWindowsDockerPipe = `\\.\pipe\docker_engine`

func defaultDockerSocket() string { return defaultWindowsDockerPipe }

func parseDockerSocket(endpoint string) (string, error) {
	if p, ok := strings.CutPrefix(endpoint, "npipe://"); ok {
		p = strings.ReplaceAll(p, "/", `\`)
		p = strings.TrimLeft(p, `\`)
		lower := strings.ToLower(p)
		switch {
		case strings.HasPrefix(lower, `.\pipe\`):
			return `\\` + p, nil
		case strings.HasPrefix(lower, `pipe\`):
			return `\\.\` + p, nil
		}
	}
	if strings.HasPrefix(strings.ToLower(endpoint), `\\.\pipe\`) {
		return endpoint, nil
	}
	return "", errors.New("exec supports only a Windows named-pipe DOCKER_HOST (npipe://) on this platform")
}

func dialDockerSocket(ctx context.Context, pipe string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, pipe)
}
