package mesh

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// dialConnect는 HTTP 프록시(host:port)에 CONNECT로 addr까지 TCP 터널을 연다. Tailscale userspace-networking처럼
// 커널이 사설망 경로를 모르고 tailscaled의 outbound 프록시로만 나갈 수 있는 노드용이다.
// 터널 안에서 mTLS를 하므로 프록시는 내용을 볼 수 없다.
func dialConnect(ctx context.Context, d *net.Dialer, proxy, addr string) (net.Conn, error) {
	c, err := d.DialContext(ctx, "tcp", proxy)
	if err != nil {
		return nil, fmt.Errorf("mesh proxy %s: %w", proxy, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	}
	if _, err := fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", addr, addr); err != nil {
		c.Close()
		return nil, err
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("mesh proxy %s: %w", proxy, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.Close()
		return nil, fmt.Errorf("mesh proxy %s: CONNECT %s: %s", proxy, addr, strings.TrimSpace(resp.Status))
	}
	if br.Buffered() > 0 {
		// 프록시가 200 뒤에 데이터를 미리 보내지 않는다(서버가 먼저 말하지 않는 TLS). 보냈다면 잘못된 프록시다.
		c.Close()
		return nil, fmt.Errorf("mesh proxy %s: unexpected data after CONNECT", proxy)
	}
	c.SetDeadline(time.Time{})
	return c, nil
}
