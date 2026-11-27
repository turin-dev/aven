package proxy

import (
	"net/netip"
	"regexp"
)

// ACME HTTP-01 relay. 같은 호스트를 여러 노드가 서비스하면(replica를 여러 노드에 둠, DNS round-robin 또는 Cloudflare 로드밸런서) Let's Encrypt의
// 확인 요청이 인증서를 요청한 노드가 아닌 다른 노드에 닿는다. 노드마다 Caddy 저장소가 따로라서 그 노드는 답을 모른다.
//
// Caddy는 자기가 시작한 challenge 요청은 라우트보다 먼저 답하고(tls app의 HandleHTTPChallenge), 모르는 토큰은 라우트로 넘긴다.
// 그래서 :80 서버 맨 앞에 이 호스트의 /.well-known/acme-challenge/ 요청을 노드 고리(ring)의 다음 노드로 넘기는 라우트를 둔다.
// 지나간 노드 ID를 ACMERelayHeader(",a,b,")에 쌓고, 다음 노드나 자기 자신이 이미 있으면 더 넘기지 않는다(404). 그래서 요청은
// 노드마다 한 번만 지나가고, 노드들의 고리 정보가 잠시 어긋나도 돌지 않는다. 요청한 노드에 닿으면 그 노드의 Caddy가 답한다.
//
// TLS-ALPN-01은 다른 노드로 넘길 수 없으므로 relay가 있는 호스트의 ACME 정책에서는 끈다(HTTP-01만 쓴다).

// ACMEChallengePath는 HTTP-01 확인 경로다.
const ACMEChallengePath = "/.well-known/acme-challenge/*"

// ACMERelayHeader는 relay가 지나간 노드 목록(",<id>,<id>,")이다.
const ACMERelayHeader = "X-Aven-Acme-Via"

// MaxACMERelayAddrs는 다음 노드 주소의 최대 개수(IPv4, IPv6)다.
const MaxACMERelayAddrs = 2

// ACMERelay는 이 노드에서 이 호스트의 모르는 ACME 확인 요청을 넘길 다음 노드다.
type ACMERelay struct {
	Self string // 이 노드 ID
	Next string // 고리의 다음 노드 ID
	// NextAddrs는 다음 노드의 공인 주소:80이다(앞의 것부터 시도한다).
	NextAddrs []string
}

var relayNodeRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

// ValidACMERelay는 relay 설정이 안전한지다: 노드 ID 형식, 서로 다른 노드, 공인 주소의 80번 포트만.
// (서버가 보낸 값으로 agent가 사설망이나 다른 포트에 요청을 보내지 않게 한다. 넘기는 것은 확인 경로의 GET뿐이다.)
func ValidACMERelay(r *ACMERelay) bool {
	if r == nil || !relayNodeRe.MatchString(r.Self) || !relayNodeRe.MatchString(r.Next) || r.Self == r.Next {
		return false
	}
	if len(r.NextAddrs) == 0 || len(r.NextAddrs) > MaxACMERelayAddrs {
		return false
	}
	for _, a := range r.NextAddrs {
		ap, err := netip.ParseAddrPort(a)
		if err != nil || ap.Port() != 80 {
			return false
		}
		ip := ap.Addr()
		if ip.Zone() != "" || ip.Is4In6() || !ip.IsGlobalUnicast() || ip.IsPrivate() {
			return false
		}
	}
	return true
}

// acmeRelayRoutes는 :80 서버 맨 앞에 둘 이 호스트의 relay 라우트다.
func acmeRelayRoutes(host string, r *ACMERelay) []map[string]any {
	base := func(extra map[string]any) []map[string]any {
		m := map[string]any{"host": []string{host}, "path": []string{ACMEChallengePath}, "method": []string{"GET"}}
		for k, v := range extra {
			m[k] = v
		}
		return []map[string]any{m}
	}
	relay := func(via string) []map[string]any {
		return []map[string]any{{
			"handler":   "reverse_proxy",
			"upstreams": upstreamList(r.NextAddrs),
			"headers":   map[string]any{"request": map[string]any{"set": map[string][]string{ACMERelayHeader: {via}}}},
			"transport": map[string]any{"protocol": "http", "dial_timeout": "3s", "response_header_timeout": "20s"},
			"load_balancing": map[string]any{
				"selection_policy": map[string]any{"policy": "first"},
				"try_duration":     "4s",
			},
		}}
	}
	notFound := []map[string]any{{"handler": "static_response", "status_code": 404, "body": "not found"}}
	return []map[string]any{
		// 처음 받은 노드: 지나간 노드 목록을 시작한다.
		{"match": base(map[string]any{"header": map[string]any{ACMERelayHeader: nil}}), "handle": relay("," + r.Self + ","), "terminal": true},
		// 넘겨받은 요청: 다음 노드도 나도 아직 지나가지 않았으면 이어서 넘긴다.
		{"match": base(map[string]any{"not": []map[string]any{
			{"header": map[string]any{ACMERelayHeader: []string{"*," + r.Next + ",*"}}},
			{"header": map[string]any{ACMERelayHeader: []string{"*," + r.Self + ",*"}}},
		}}), "handle": relay("{http.request.header." + ACMERelayHeader + "}" + r.Self + ","), "terminal": true},
		// 한 바퀴 돌았다: 아무 노드도 이 토큰을 모른다.
		{"match": base(nil), "handle": notFound, "terminal": true},
	}
}
