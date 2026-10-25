package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

// 도메인별 공개 IPv6(§7).
//
// 노드마다 IPv6 풀(예: 2001:db8:1:d::/80)을 정하면 aven은 공개 도메인마다 그 풀에서 /128 주소 하나를 배정하고,
// 이 파일의 IPv6Ensure가 그 주소들을 노드의 공개 인터페이스에 붙인다:
//
//	ip -6 addr replace <addr>/128 dev <iface> preferred_lft 0 valid_lft forever nodad
//
// preferred_lft 0(deprecated)이라 호스트가 밖으로 나가는 연결의 출발 주소로 쓰지 않는다(Mailu ::25와 같은 방식). keel-proxy는
// [::]:80/443에 publish되어 있으므로 새 주소로 들어온 요청도 SNI로 그대로 라우팅된다(Caddy나 컨테이너는 바꾸지 않는다).
//
// 안전 규칙:
//   - 풀 밖의 주소는 절대 건드리지 않는다.
//   - 풀 안에서도 aven이 붙인 모양(/128이고 deprecated)인 주소만 지운다. 다른 모양의 주소(예: 운영자가 직접 붙인 /64)는
//     Foreign으로 알리기만 한다.
//   - 노드의 기본 공인 IPv6(protect)를 담는 풀은 거부한다.

// IPv6PoolMinBits, IPv6PoolMaxBits는 허용하는 풀 크기다(/64 ~ /120).
const (
	IPv6PoolMinBits = 64
	IPv6PoolMaxBits = 120
)

// MaxIPv6Addresses는 한 번에 받는 주소 수 상한이다.
const MaxIPv6Addresses = 4096

var ifaceRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:@-]{0,14}$`)

// ValidIPv6Iface는 인터페이스 이름 형식이다(ip 인자로 그대로 들어간다).
func ValidIPv6Iface(s string) bool { return ifaceRe.MatchString(s) && !strings.HasPrefix(s, "-") }

var ulaPrefix = netip.MustParsePrefix("fc00::/7")

// ParseIPv6Pool은 풀 CIDR을 검증한다: IPv6, 전역 유니캐스트(ULA 제외), /64~/120, 네트워크 주소로 적은 값.
func ParseIPv6Pool(s string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(strings.TrimSpace(s))
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("IPv6 풀 %q는 CIDR이 아닙니다", s)
	}
	a := p.Addr()
	if !a.Is6() || a.Is4In6() || a.Zone() != "" {
		return netip.Prefix{}, fmt.Errorf("IPv6 풀 %s는 IPv6 CIDR이어야 합니다", p)
	}
	if !a.IsGlobalUnicast() || ulaPrefix.Contains(a) {
		return netip.Prefix{}, fmt.Errorf("IPv6 풀 %s는 공인(global unicast) 대역이어야 합니다", p)
	}
	if p.Bits() < IPv6PoolMinBits || p.Bits() > IPv6PoolMaxBits {
		return netip.Prefix{}, fmt.Errorf("IPv6 풀 %s의 크기는 /%d ~ /%d여야 합니다", p, IPv6PoolMinBits, IPv6PoolMaxBits)
	}
	if p.Masked() != p {
		return netip.Prefix{}, fmt.Errorf("IPv6 풀은 네트워크 주소로 적어야 합니다(%s)", p.Masked())
	}
	return p, nil
}

// ParseIPv6PoolAddresses는 풀과 주소 목록을 검증한다: 모든 주소가 풀 안의 IPv6여야 한다. 중복은 하나로 합치고 정렬한다.
func ParseIPv6PoolAddresses(pool string, addrs []string) (netip.Prefix, []netip.Addr, error) {
	p, err := ParseIPv6Pool(pool)
	if err != nil {
		return netip.Prefix{}, nil, err
	}
	if len(addrs) > MaxIPv6Addresses {
		return netip.Prefix{}, nil, errors.New("too many addresses")
	}
	seen := map[netip.Addr]bool{}
	var out []netip.Addr
	for _, s := range addrs {
		a, err := netip.ParseAddr(strings.TrimSpace(s))
		if err != nil || !a.Is6() || a.Is4In6() || a.Zone() != "" {
			return netip.Prefix{}, nil, fmt.Errorf("address %q is not an IPv6 address", s)
		}
		if !p.Contains(a) {
			return netip.Prefix{}, nil, fmt.Errorf("address %s is outside the pool %s", a, p)
		}
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Less(out[j]) })
	return p, out, nil
}

// IPRunner는 ip(iproute2) 명령이다(테스트에서 가짜로 바꾼다). Run은 stdout을 돌려준다.
type IPRunner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

type execIP struct{ bin string }

func (e execIP) Run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.bin, args...)
	cmd.WaitDelay = 2 * time.Second
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return string(out), fmt.Errorf("ip %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// SystemIPRunner는 이 호스트의 ip 명령이다. 설치되어 있지 않거나 root가 아니면(주소를 붙이려면 root가 필요하다, 단위 테스트 등) nil이다.
func SystemIPRunner() IPRunner {
	if os.Geteuid() != 0 {
		return nil
	}
	for _, p := range []string{"ip", "/usr/sbin/ip", "/sbin/ip", "/usr/bin/ip", "/bin/ip"} {
		if bin, err := exec.LookPath(p); err == nil {
			return execIP{bin: bin}
		}
	}
	return nil
}

// HostIPv6Addr는 호스트 인터페이스에 붙은 IPv6 주소 하나다.
type HostIPv6Addr struct {
	Iface      string `json:"iface"`
	Addr       string `json:"addr"`
	Bits       int    `json:"bits"`
	Scope      string `json:"scope"`
	Deprecated bool   `json:"deprecated"`
}

// Prefix는 주소/길이다.
func (h HostIPv6Addr) Prefix() (netip.Prefix, bool) {
	a, err := netip.ParseAddr(h.Addr)
	if err != nil {
		return netip.Prefix{}, false
	}
	p := netip.PrefixFrom(a, h.Bits)
	return p, p.IsValid()
}

type ipAddrJSON struct {
	IfName   string `json:"ifname"`
	AddrInfo []struct {
		Family       string `json:"family"`
		Local        string `json:"local"`
		PrefixLen    int    `json:"prefixlen"`
		Scope        string `json:"scope"`
		Deprecated   bool   `json:"deprecated"`
		PreferredLft *int64 `json:"preferred_life_time"`
	} `json:"addr_info"`
}

// ListHostIPv6는 `ip -j -6 addr show [dev iface]`로 읽은 전역(scope global) IPv6 주소다(링크 로컬은 뺀다).
func ListHostIPv6(ctx context.Context, r IPRunner, iface string) ([]HostIPv6Addr, error) {
	if r == nil {
		return nil, errors.New("ip command is not available")
	}
	args := []string{"-j", "-6", "addr", "show"}
	if iface != "" {
		if !ValidIPv6Iface(iface) {
			return nil, fmt.Errorf("interface %q is not allowed", iface)
		}
		args = append(args, "dev", iface)
	}
	out, err := r.Run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var links []ipAddrJSON
	if s := strings.TrimSpace(out); s != "" {
		if err := json.Unmarshal([]byte(s), &links); err != nil {
			return nil, fmt.Errorf("parse ip -j addr: %w", err)
		}
	}
	var res []HostIPv6Addr
	for _, l := range links {
		for _, ai := range l.AddrInfo {
			if ai.Family != "inet6" || ai.Scope == "link" || ai.Scope == "host" {
				continue
			}
			a, err := netip.ParseAddr(ai.Local)
			if err != nil || !a.Is6() || a.IsLinkLocalUnicast() || a.IsLoopback() {
				continue
			}
			dep := ai.Deprecated || (ai.PreferredLft != nil && *ai.PreferredLft == 0)
			res = append(res, HostIPv6Addr{Iface: l.IfName, Addr: a.String(), Bits: ai.PrefixLen, Scope: ai.Scope, Deprecated: dep})
		}
	}
	return res, nil
}

// DefaultIPv6Iface는 도메인 주소를 붙일 인터페이스다: 노드의 공인 IPv6(publicIPv6)를 가진 인터페이스, 없으면 IPv6 기본 경로의 인터페이스.
func DefaultIPv6Iface(ctx context.Context, r IPRunner, publicIPv6 string) (string, error) {
	if r == nil {
		return "", errors.New("ip command is not available")
	}
	if pub, err := netip.ParseAddr(strings.TrimSpace(publicIPv6)); err == nil {
		if addrs, err := ListHostIPv6(ctx, r, ""); err == nil {
			for _, h := range addrs {
				if a, err := netip.ParseAddr(h.Addr); err == nil && a == pub && ValidIPv6Iface(h.Iface) {
					return h.Iface, nil
				}
			}
		}
	}
	out, err := r.Run(ctx, "-j", "-6", "route", "show", "default")
	if err != nil {
		return "", err
	}
	var routes []struct {
		Dst string `json:"dst"`
		Dev string `json:"dev"`
	}
	if s := strings.TrimSpace(out); s != "" {
		if err := json.Unmarshal([]byte(s), &routes); err != nil {
			return "", fmt.Errorf("parse ip -j route: %w", err)
		}
	}
	for _, rt := range routes {
		if (rt.Dst == "default" || rt.Dst == "::/0") && ValidIPv6Iface(rt.Dev) {
			return rt.Dev, nil
		}
	}
	return "", errors.New("IPv6 기본 경로가 없어 주소를 붙일 인터페이스를 정하지 못했습니다(KEEL_IPV6_IFACE로 정하세요)")
}

// IPv6EnsureResult는 IPv6Ensure의 결과다.
type IPv6EnsureResult struct {
	// Supported가 false면 이 노드에서 주소를 붙일 수 없다(Reason). 붙일 주소가 없으면 오류 없이 이것만 알린다.
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
	Iface     string `json:"iface,omitempty"`
	Pool      string `json:"pool,omitempty"`
	Desired   int    `json:"desired"`
	Present   int    `json:"present"` // 반영 뒤 붙어 있는 원하는 주소 수
	Added     int    `json:"added"`
	Removed   int    `json:"removed"`
	// Foreign은 풀 안에 있지만 aven이 붙인 모양이 아니라서 건드리지 않은 주소다(주소/길이).
	Foreign []string `json:"foreign,omitempty"`
}

// ErrIPv6Unsupported는 붙일 주소가 있는데 이 노드에서 ip 명령을 쓸 수 없을 때다.
var ErrIPv6Unsupported = errors.New("이 노드에서는 도메인별 IPv6 주소를 붙일 수 없습니다")

// IPv6Ensure는 iface에 붙은 pool 안의 aven 주소(/128, deprecated)를 want에 맞춘다: 없는 주소는 붙이고, 원하지 않는 주소는 지운다.
// pool 밖의 주소와 aven 모양이 아닌 주소는 건드리지 않는다. 이미 맞으면 아무 명령도 실행하지 않는다.
// protect(노드의 기본 공인 IPv6 등)가 pool 안에 있으면 아무것도 하지 않고 오류다.
// r이 nil이면 want가 비었을 때 Supported=false로 끝나고, 아니면 ErrIPv6Unsupported다.
func IPv6Ensure(ctx context.Context, r IPRunner, iface string, pool netip.Prefix, want []netip.Addr, protect []netip.Addr) (*IPv6EnsureResult, error) {
	res := &IPv6EnsureResult{Iface: iface, Pool: pool.String(), Desired: len(want)}
	if !pool.IsValid() {
		return res, errors.New("invalid pool")
	}
	if _, err := ParseIPv6Pool(pool.String()); err != nil {
		return res, err
	}
	for _, p := range protect {
		if p.IsValid() && pool.Contains(p) {
			return res, fmt.Errorf("IPv6 풀 %s에 노드의 기본 주소 %s가 들어 있습니다", pool, p)
		}
	}
	wantSet := map[netip.Addr]bool{}
	for _, a := range want {
		if !pool.Contains(a) {
			return res, fmt.Errorf("address %s is outside the pool %s", a, pool)
		}
		wantSet[a] = true
	}
	res.Desired = len(wantSet)
	if r == nil {
		res.Reason = "이 노드에서 ip 명령을 쓸 수 없습니다(설치되어 있지 않거나 aven이 root로 돌지 않습니다)"
		if len(wantSet) > 0 {
			return res, fmt.Errorf("%w: %s", ErrIPv6Unsupported, res.Reason)
		}
		return res, nil
	}
	if !ValidIPv6Iface(iface) {
		res.Reason = fmt.Sprintf("interface %q is not allowed", iface)
		return res, errors.New(res.Reason)
	}
	res.Supported = true
	cur, err := ListHostIPv6(ctx, r, iface)
	if err != nil {
		return res, err
	}
	have := map[netip.Addr]bool{} // 풀 안의 aven 모양 주소
	for _, h := range cur {
		a, err := netip.ParseAddr(h.Addr)
		if err != nil || !pool.Contains(a) {
			continue // 풀 밖: 절대 건드리지 않는다
		}
		if h.Bits == 128 && h.Deprecated {
			have[a] = true
			continue
		}
		if h.Bits == 128 && wantSet[a] {
			// 원하는 주소인데 preferred가 살아 있다(운영자가 직접 붙인 것일 수 있다): 지우지 않고 replace로 deprecated로 맞춘다.
			continue
		}
		res.Foreign = append(res.Foreign, fmt.Sprintf("%s/%d", a, h.Bits))
	}
	sort.Strings(res.Foreign)

	var errs []error
	var del []netip.Addr
	for a := range have {
		if !wantSet[a] {
			del = append(del, a)
		}
	}
	sort.Slice(del, func(i, j int) bool { return del[i].Less(del[j]) })
	for _, a := range del {
		if _, err := r.Run(ctx, "-6", "addr", "del", a.String()+"/128", "dev", iface); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", a, err))
			continue
		}
		res.Removed++
		delete(have, a)
	}
	add := make([]netip.Addr, 0, len(wantSet))
	for a := range wantSet {
		if !have[a] {
			add = append(add, a)
		}
	}
	sort.Slice(add, func(i, j int) bool { return add[i].Less(add[j]) })
	for _, a := range add {
		if _, err := r.Run(ctx, IPv6AddArgs(a, iface)...); err != nil {
			errs = append(errs, fmt.Errorf("add %s: %w", a, err))
			continue
		}
		res.Added++
		have[a] = true
	}
	for a := range wantSet {
		if have[a] {
			res.Present++
		}
	}
	return res, errors.Join(errs...)
}

// IPv6AddArgs는 주소 하나를 붙이는 ip 인자다(deprecated, DAD 없이, 만료 없음).
func IPv6AddArgs(a netip.Addr, iface string) []string {
	return []string{"-6", "addr", "replace", a.String() + "/128", "dev", iface, "preferred_lft", "0", "valid_lft", "forever", "nodad"}
}

// CheckIPv6PoolOnHost는 pool을 이 호스트에 써도 되는지 본다: 호스트의 주소(host) 가운데 pool 안에 있는 것은 managed(aven이 배정한
// 주소)뿐이어야 하고, /64보다 작은 호스트 서브넷(예: wg0의 /112)과 겹치면 안 된다. protect(기본 공인 주소)를 담아도 안 된다.
func CheckIPv6PoolOnHost(pool netip.Prefix, host []HostIPv6Addr, managed map[netip.Addr]bool, protect []netip.Addr) error {
	for _, p := range protect {
		if p.IsValid() && pool.Contains(p) {
			return fmt.Errorf("IPv6 풀 %s에 노드의 기본 IPv6 주소 %s가 들어 있습니다", pool, p)
		}
	}
	for _, h := range host {
		hp, ok := h.Prefix()
		if !ok {
			continue
		}
		a := hp.Addr()
		if pool.Contains(a) {
			if h.Bits == 128 && managed[a] {
				continue
			}
			return fmt.Errorf("IPv6 풀 %s에 이 노드가 이미 쓰는 주소 %s/%d(%s)가 들어 있습니다", pool, a, h.Bits, h.Iface)
		}
		if h.Bits > 64 && hp.Masked().Overlaps(pool) {
			return fmt.Errorf("IPv6 풀 %s가 이 노드의 서브넷 %s(%s)와 겹칩니다", pool, hp.Masked(), h.Iface)
		}
	}
	return nil
}

func isRoot() bool { return os.Geteuid() == 0 }
