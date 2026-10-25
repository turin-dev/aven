package notify

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// ErrBlockedAddress는 SSRF 방지로 막은 목적지다.
var ErrBlockedAddress = errors.New("사설·루프백·링크 로컬 주소로는 알림을 보낼 수 없습니다 (KEEL_NOTIFY_ALLOW_PRIVATE=1로 허용)")

// blockedPrefixes는 IsPrivate/IsLoopback 등으로 잡히지 않는 특수 대역이다.
var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8",       // "this network"
		"100.64.0.0/10",   // CGNAT
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // TEST-NET-1
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // TEST-NET-2
		"203.0.113.0/24",  // TEST-NET-3
		"240.0.0.0/4",     // reserved + broadcast
		"64:ff9b::/96",    // NAT64(내부 IPv4로 이어질 수 있다)
		"64:ff9b:1::/48",
		"2002::/16", // 6to4
		"fec0::/10", // site-local(폐기)
		"100::/64",  // discard
		"2001:db8::/32",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// PublicAddr는 공인 unicast 주소인지 본다(IPv4-mapped IPv6는 IPv4로 본다).
func PublicAddr(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// Guard는 알림 HTTP 요청의 목적지를 검사한다. 보낼 때 이름을 풀어 모든 주소를 검사하고,
// 실제로 연결하는 소켓 주소도 dialer Control에서 다시 검사한다(DNS rebinding 방지).
type Guard struct {
	// AllowPrivate면 사설/루프백 목적지도 허용한다(KEEL_NOTIFY_ALLOW_PRIVATE=1).
	AllowPrivate bool
	// Lookup은 이름 풀이다(nil이면 시스템 resolver). 테스트에서 바꾼다.
	Lookup func(ctx context.Context, host string) ([]netip.Addr, error)
}

func (g *Guard) allowed(a netip.Addr) bool { return g == nil || g.AllowPrivate || PublicAddr(a) }

func (g *Guard) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if g != nil && g.Lookup != nil {
		return g.Lookup(ctx, host)
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// control은 connect 직전 실제 목적지 주소를 검사한다.
func (g *Guard) control(network, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("unexpected dial address %q", address)
	}
	if !g.allowed(ap.Addr()) {
		return ErrBlockedAddress
	}
	return nil
}

// Resolve는 host의 주소를 모두 풀고 하나라도 막힌 주소면 거부한다.
func (g *Guard) Resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else {
		if addrs, err = g.lookup(ctx, host); err != nil {
			return nil, err
		}
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("%s: no addresses", host)
	}
	for _, a := range addrs {
		if !g.allowed(a) {
			return nil, ErrBlockedAddress
		}
	}
	return addrs, nil
}

// DialContext는 이름을 풀어 검사한 주소로만 연결한다.
func (g *Guard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	addrs, err := g.Resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	d := &net.Dialer{Timeout: 10 * time.Second, Control: g.control}
	var last error
	for _, a := range addrs {
		c, err := d.DialContext(ctx, network, net.JoinHostPort(a.Unmap().String(), port))
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

// Client는 Guard를 거치는 HTTP 클라이언트다. 환경 proxy를 쓰지 않고 redirect를 따라가지 않는다.
func (g *Guard) Client(timeout time.Duration) *http.Client {
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           g.DialContext,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{Transport: tr, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
