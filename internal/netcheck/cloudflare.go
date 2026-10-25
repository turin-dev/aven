package netcheck

import "net/netip"

// CloudflareNets는 Cloudflare가 공개한 프록시(엣지) 대역이다(https://www.cloudflare.com/ips).
// 도메인이 이 대역으로 resolve되면 Cloudflare 프록시(주황 구름) 뒤에 있다고 본다. api의 CF-Connecting-IP 신뢰 판단도 이 목록을 쓴다.
var CloudflareNets = mustPrefixes(
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18", "108.162.192.0/18",
	"190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32")

func mustPrefixes(s ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(s))
	for i, v := range s {
		out[i] = netip.MustParsePrefix(v)
	}
	return out
}

// IsCloudflare는 주소가 Cloudflare 엣지 대역인지다.
func IsCloudflare(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, p := range CloudflareNets {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
