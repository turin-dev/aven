package proxy

import (
	"github.com/turin-dev/aven/internal/compat"
	"log/slog"
	"net/netip"

	"github.com/turin-dev/aven/internal/netcheck"
)

// 클라이언트 IP: keel-proxy 앞에 다른 proxy(Cloudflare 주황 구름, cloudflared 터널, 로드 밸런서)가 있으면 TCP peer는 그 proxy다.
// Caddy 서버의 trusted_proxies에 넣은 대역에서 온 연결만 X-Forwarded-For를 믿고, 오른쪽부터 읽어 믿지 않는 첫 주소를
// 클라이언트로 본다(trusted_proxies_strict). 그래서 클라이언트가 X-Forwarded-For 왼쪽에 넣은 가짜 주소는 쓰이지 않는다.
// 믿는 대역은 Cloudflare 공개 대역(Cloudflare가 실제 접속 주소를 X-Forwarded-For 끝에 붙인다)과
// AVEN_PROXY_TRUSTED_PROXIES(쉼표로 구분한 IP/CIDR, 예: cloudflared 컨테이너가 있는 docker 네트워크)다.
// 결과({http.request.client_ip})는 access log, 엣지 WAF(IP 규칙, 요청 제한)와 upstream의 X-Forwarded-For에 쓰인다.
// CF-Connecting-IP는 쓰지 않는다: 믿는 proxy가 Cloudflare가 아니면 클라이언트가 그 헤더를 마음대로 넣을 수 있다.

// TrustedProxiesEnv는 프록시 앞단 proxy 대역을 정하는 환경 변수다(Config.TrustedProxies가 비었을 때 읽는다).
// 예전 이름 KEEL_PROXY_TRUSTED_PROXIES도 읽는다.
const TrustedProxiesEnv = "AVEN_PROXY_TRUSTED_PROXIES"

// trustedRanges는 Caddy trusted_proxies에 넣을 대역(정규화한 CIDR)이다. 잘못된 항목은 경고하고 뺀다.
func (c *Caddy) trustedRanges() []string {
	list := c.cfg.TrustedProxies
	if list == nil {
		list = ParseList(compat.Env("PROXY_TRUSTED_PROXIES"))
	}
	seen := map[string]bool{}
	var out []string
	add := func(p netip.Prefix) {
		if s := p.Masked().String(); !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, p := range netcheck.CloudflareNets {
		add(p)
	}
	for _, raw := range list {
		if p, err := netip.ParsePrefix(raw); err == nil {
			add(p)
		} else if a, err := netip.ParseAddr(raw); err == nil && a.Zone() == "" {
			a = a.Unmap()
			add(netip.PrefixFrom(a, a.BitLen()))
		} else {
			slog.Warn("proxy: trusted proxy entry ignored (not an IP or CIDR)", "env", TrustedProxiesEnv, "value", truncateForLog(raw))
		}
	}
	return out
}

// applyClientIP는 공개 서버(:443, :80) 설정에 클라이언트 IP 해석을 넣는다.
func (c *Caddy) applyClientIP(server map[string]any, ranges []string) {
	server["trusted_proxies"] = map[string]any{"source": "static", "ranges": ranges}
	server["client_ip_headers"] = []string{"X-Forwarded-For"}
	server["trusted_proxies_strict"] = 1
}
