package node

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Probe는 컨테이너의 network 주소로 healthcheck를 한 번 한다(kind: http | tcp).
// 성공하면 ok=true와 설명을, 실패하면 ok=false와 마지막 오류를 돌려준다.
func (d *Docker) Probe(ctx context.Context, name, network, kind string, port int, path string) (bool, string) {
	st, err := d.Inspect(ctx, name)
	if err != nil {
		return false, err.Error()
	}
	return ProbeState(ctx, st, network, kind, port, path)
}

// ProbeState는 이미 조회한 컨테이너 상태로 Probe를 한다.
func ProbeState(ctx context.Context, st *ContainerState, network, kind string, port int, path string) (bool, string) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	client := &http.Client{
		Timeout:       3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	last := "no address on network " + network
	for _, ip := range st.IP(network) {
		addr := net.JoinHostPort(ip, fmt.Sprint(port))
		if kind == "tcp" {
			c, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", addr)
			if err != nil {
				last = err.Error()
				continue
			}
			c.Close()
			return true, "tcp " + addr
		}
		url := "http://" + addr + path
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err != nil {
			last = err.Error()
			continue
		}
		resp.Body.Close()
		if resp.StatusCode < 400 {
			return true, fmt.Sprintf("GET %s → %d", url, resp.StatusCode)
		}
		last = fmt.Sprintf("GET %s → %d", url, resp.StatusCode)
	}
	return false, last
}

// BuildArgs는 Dockerfile에 ARG로 선언된 변수만 build-arg로 넘긴다(불필요한 경고/캐시 무효화 방지).
func BuildArgs(dockerfile string, vars map[string]string) map[string]string {
	src, err := os.ReadFile(dockerfile)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(src), "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) >= 2 && strings.EqualFold(f[0], "ARG") {
			name, _, _ := strings.Cut(f[1], "=")
			if v, ok := vars[name]; ok {
				out[name] = v
			}
		}
	}
	return out
}

// SplitSecrets는 변수를 build secret(names에 든 것)과 나머지로 나눈다.
func SplitSecrets(vars map[string]string, names []string) (rest, secrets map[string]string) {
	rest, secrets = map[string]string{}, map[string]string{}
	for k, v := range vars {
		rest[k] = v
	}
	for _, n := range names {
		if v, ok := vars[n]; ok {
			secrets[n] = v
			delete(rest, n)
		}
	}
	return rest, secrets
}
