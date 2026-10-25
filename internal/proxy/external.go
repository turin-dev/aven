package proxy

import (
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

var upstreamHostRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ValidExternalUpstream은 aven 밖의 upstream(host:port)으로 쓸 수 있는 값인지다. host는 컨테이너 이름(docker DNS)이거나
// 사설 IP여야 한다. localhost/루프백/링크 로컬/미지정 주소는 받지 않는다: keel-proxy 컨테이너 자신(관리 API 등)을 가리키게 된다.
func ValidExternalUpstream(s string) bool {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return false
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.IsPrivate() || netip.MustParsePrefix("100.64.0.0/10").Contains(ip)
	}
	h := strings.ToLower(host)
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || h == ContainerName {
		return false
	}
	return upstreamHostRe.MatchString(host)
}

// ParseList는 쉼표로 구분한 설정 값을 나눈다(빈 항목은 버린다).
func ParseList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
