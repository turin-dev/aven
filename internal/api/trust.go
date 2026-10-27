package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/turin-dev/aven/internal/compat"
	"github.com/turin-dev/aven/internal/netcheck"
)

// GatewayHeader는 신뢰하는 gateway(Traefik middleware가 붙임)가 모든 요청에 넣는 비밀 헤더다.
// dokploy-network의 다른 컨테이너는 gateway에 직접 붙어 X-Forwarded-*를 위조할 수 있으므로,
// unix socket listener도 이 헤더가 맞을 때만 전달 헤더를 믿는다. 검사 후에는 지운다.
const (
	GatewayHeader       = "X-Aven-Gateway"
	LegacyGatewayHeader = "X-Keel-Gateway"
)

// 전달(proxy) 헤더. 신뢰할 수 없는 요청에서는 전부 지운다.
var forwardHeaders = []string{"X-Forwarded-Proto", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Port", "X-Forwarded-Server", "X-Real-Ip", "Forwarded", "Cf-Connecting-Ip", "True-Client-Ip"}

type trustedListenerKey struct{}

// ViaTrustedProxy는 신뢰하는 reverse proxy만 접근할 수 있는 listener에 붙이는 wrapper다(무조건 신뢰).
// gateway 비밀이 없는 구성(예: aven이 직접 TLS를 끝내는 proxy와 unix socket을 공유)에서만 쓴다. TCP listener에는 붙이지 않는다.
func ViaTrustedProxy(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del(GatewayHeader)
		r.Header.Del(LegacyGatewayHeader)
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), trustedListenerKey{}, true)))
	})
}

// ViaGateway는 unix socket listener용 wrapper다. secret이 있으면 X-Keel-Gateway가 일치하는 요청만 전달 헤더를 신뢰하고,
// 일치하지 않으면 전달 헤더를 모두 지워 TCP 직접 연결과 똑같이 취급한다. secret이 비어 있으면 ViaTrustedProxy와 같다.
func ViaGateway(secret string, h http.Handler) http.Handler {
	if secret == "" {
		return ViaTrustedProxy(h)
	}
	want := sha256.Sum256([]byte(secret))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next := sha256.Sum256([]byte(r.Header.Get(GatewayHeader)))
		prev := sha256.Sum256([]byte(r.Header.Get(LegacyGatewayHeader)))
		r.Header.Del(GatewayHeader)
		r.Header.Del(LegacyGatewayHeader)
		okNext := subtle.ConstantTimeCompare(next[:], want[:]) == 1
		okPrev := subtle.ConstantTimeCompare(prev[:], want[:]) == 1
		if !okNext && okPrev {
			compat.NoteLegacyResource("gateway-header", "header "+LegacyGatewayHeader, "header "+GatewayHeader)
		}
		if !okNext && !okPrev {
			for _, k := range forwardHeaders {
				r.Header.Del(k)
			}
			h.ServeHTTP(w, r)
			return
		}
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), trustedListenerKey{}, true)))
	})
}

func fromTrustedListener(r *http.Request) bool {
	v, _ := r.Context().Value(trustedListenerKey{}).(bool)
	return v
}

func peerAddr(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}

// fromTrustedProxy: 요청이 gateway 전용 listener나 KEEL_TRUSTED_PROXIES의 주소에서 왔는지.
func (s *Server) fromTrustedProxy(r *http.Request) bool {
	if fromTrustedListener(r) {
		return true
	}
	ip, ok := peerAddr(r)
	if !ok {
		return false
	}
	for _, p := range s.TrustedProxies {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// secureTransport는 요청이 TLS로 들어왔는지 판단한다. 직접 TLS listener(KEEL_TLS_CERT/KEEL_TLS_KEY)면 r.TLS가 있다.
// TLS를 끝내는 reverse proxy 뒤라면 gateway 전용 unix socket(KEEL_LISTEN_UNIX, 비밀 헤더 검증 포함)이나
// KEEL_TRUSTED_PROXIES의 주소에서 온 요청의 X-Forwarded-Proto: https만 믿는다(기본값은 아무 헤더도 믿지 않음).
func (s *Server) secureTransport(r *http.Request) bool {
	return r.TLS != nil || (s.fromTrustedProxy(r) && forwardedSecure(r))
}

// forwardedSecure: proxy가 붙인 X-Forwarded-Proto가 TLS를 뜻하는지. Traefik 등은 WebSocket upgrade에 "wss"를 쓴다.
func forwardedSecure(r *http.Request) bool {
	p := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")))
	return p == "https" || p == "wss"
}

const tlsRequired = "TLS required: serve aven-server over HTTPS (KEEL_TLS_CERT/KEEL_TLS_KEY) or set KEEL_TRUSTED_PROXIES for a TLS-terminating reverse proxy"

// Cloudflare가 공개한 origin 접속 대역(https://www.cloudflare.com/ips). 이 대역에서 온 연결의 CF-Connecting-IP만 믿는다.
// 목록은 도메인 상태 확인(netcheck)과 같이 쓴다.
var cloudflareNets = netcheck.CloudflareNets

func inPrefixes(ps []netip.Prefix, ip netip.Addr) bool {
	for _, p := range ps {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP는 rate limit과 로그에 쓰는 실제 클라이언트 주소다.
// 신뢰하지 않는 연결이면 TCP peer 주소만 쓴다(헤더는 무시). 신뢰하는 proxy 뒤라면
// X-Forwarded-For의 마지막 항목(그 proxy가 직접 본 peer)을 쓰고, 그것이 Cloudflare 대역이면 CF-Connecting-IP를 쓴다.
// 클라이언트가 스스로 넣은 헤더 값은 어떤 경우에도 Cloudflare를 거치지 않고서는 채택되지 않는다.
func (s *Server) clientIP(r *http.Request) netip.Addr {
	peer, ok := peerAddr(r)
	if !s.fromTrustedProxy(r) {
		return peer
	}
	var hop netip.Addr
	var hopOK bool
	if parts := strings.Split(r.Header.Get("X-Forwarded-For"), ","); len(parts) > 0 {
		if a, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1])); err == nil {
			hop, hopOK = a.Unmap(), true
		}
	}
	if hopOK && inPrefixes(cloudflareNets, hop) {
		if a, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("Cf-Connecting-Ip"))); err == nil {
			return a.Unmap()
		}
	}
	if hopOK {
		return hop
	}
	if ok {
		return peer
	}
	return netip.Addr{}
}

// limitKey는 rate limit 버킷 키다. IPv6는 /64 단위로 묶는다(한 /64 안에서 주소를 바꿔 우회하지 못하게).
func limitKey(ip netip.Addr) string {
	if !ip.IsValid() {
		return "unknown"
	}
	if ip.Is6() {
		p, _ := ip.Prefix(64)
		return p.String()
	}
	return ip.String()
}
