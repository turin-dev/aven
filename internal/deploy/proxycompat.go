package deploy

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/store"
)

// 도메인 확인(P3) 라우트 필드: Route.CheckToken(/.well-known/keel/domain-check 응답)과 Route.Unverified(확인 전 신규 도메인의
// ACME 보류). 옛 agent는 proxy.apply에서 모르는 필드를 거부한다(`json: unknown field "CheckToken"`). 그런 노드에는 두 필드를 빼고
// 다시 보내고, 그 노드를 noDomainCheckRetry 동안 기억해 처음부터 빼고 보낸다(mesh의 aliases fallback과 같은 방식).
// 빼고 보낸 노드는 HTTP 확인 응답이 없으므로 DNS/TXT로만 확인되고, 신규 도메인도 예전처럼 바로 관리 ACME 정책에 들어간다.

// noDomainCheckRetry는 도메인 확인 필드를 거부한 agent에 다시 보내 보기까지의 시간이다(그 사이 agent가 업그레이드될 수 있다).
const noDomainCheckRetry = 10 * time.Minute

type proxyCompat struct {
	mu      sync.Mutex
	noCheck map[string]time.Time // nodeID → 도메인 확인 필드를 거부한 시각
	noRelay map[string]time.Time // nodeID → ACMERelay 필드를 거부한 시각
}

// isACMERelayFieldRefused는 옛 agent가 ACMERelay 필드를 몰라 거부한 오류인지다. 그런 노드는 자기 ACME 확인에만 답한다(예전 동작).
func isACMERelayFieldRefused(err error) bool {
	return err != nil && strings.Contains(err.Error(), `unknown field "ACMERelay"`)
}

func withoutACMERelay(routes []proxy.Route) []proxy.Route {
	out := make([]proxy.Route, len(routes))
	for i, r := range routes {
		r.ACMERelay = nil
		out[i] = r
	}
	return out
}

// recent는 m[id]가 noDomainCheckRetry 안에 기록됐는지다(지났으면 지운다). pc.mu를 잡고 부른다.
func recent(m map[string]time.Time, id string) bool {
	at, ok := m[id]
	if ok && time.Since(at) >= noDomainCheckRetry {
		delete(m, id)
		return false
	}
	return ok
}

// isDomainCheckFieldRefused는 옛 agent가 도메인 확인 필드를 몰라 거부한 오류인지다.
func isDomainCheckFieldRefused(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, `unknown field "CheckToken"`) || strings.Contains(s, `unknown field "Unverified"`)
}

// withoutDomainCheck는 라우트에서 도메인 확인 필드를 뺀 복사본이다.
func withoutDomainCheck(routes []proxy.Route) []proxy.Route {
	out := make([]proxy.Route, len(routes))
	for i, r := range routes {
		r.CheckToken, r.Unverified = "", false
		out[i] = r
	}
	return out
}

// applyRemoteProxy는 원격 노드에 라우트를 보낸다. 옛 agent가 모르는 필드(도메인 확인, ACME relay)를 거부하면 그 필드를 빼고 다시 보낸다.
func (m *Manager) applyRemoteProxy(ctx context.Context, nodeID string, routes []proxy.Route) error {
	pc := &m.proxyCompat
	drv := &remoteDriver{hub: m.Agents, node: nodeID}
	for attempt := 0; ; attempt++ {
		pc.mu.Lock()
		skipCheck, skipRelay := recent(pc.noCheck, nodeID), recent(pc.noRelay, nodeID)
		pc.mu.Unlock()
		send := routes
		if skipCheck {
			send = withoutDomainCheck(send)
		}
		if skipRelay {
			send = withoutACMERelay(send)
		}
		err := drv.applyProxy(ctx, send)
		check, relay := isDomainCheckFieldRefused(err), isACMERelayFieldRefused(err)
		if (!check || skipCheck) && (!relay || skipRelay) || attempt >= 2 {
			return err
		}
		pc.mu.Lock()
		if check {
			if pc.noCheck == nil {
				pc.noCheck = map[string]time.Time{}
			}
			pc.noCheck[nodeID] = time.Now()
		}
		if relay {
			if pc.noRelay == nil {
				pc.noRelay = map[string]time.Time{}
			}
			pc.noRelay[nodeID] = time.Now()
		}
		pc.mu.Unlock()
		if m.Log != nil {
			if check {
				m.log().Info("node agent does not support domain check routes; sending routes without them (DNS/TXT verification only)", "node", nodeID)
			}
			if relay {
				m.log().Info("node agent does not support ACME challenge relay; upgrade it so hosts served by several nodes get certificates on every node", "node", nodeID)
			}
		}
	}
}

// DomainCheckSupported는 원격 노드가 도메인 확인 라우트를 받는지다(마지막으로 거부한 뒤 noDomainCheckRetry 안이면 false).
func (m *Manager) DomainCheckSupported(nodeID string) bool {
	if isLocal(nodeID) {
		return true
	}
	pc := &m.proxyCompat
	pc.mu.Lock()
	defer pc.mu.Unlock()
	at, ok := pc.noCheck[nodeKey(nodeID)]
	return !ok || time.Since(at) >= noDomainCheckRetry
}

// domainTLSOK는 도메인이 on-demand 인증서를 받아도 되는지다: legacy·확인됐거나 DNS가 이 노드를 가리킨다(netcheck 결과).
func domainTLSOK(st *store.DomainState) bool {
	return st.Verified() || st.DNSStatus == store.DNSActive
}

// decorateDomainRoutes는 서비스 도메인 라우트에 도메인 확인 토큰을 채우고, 확인 전 신규 커스텀 도메인은 Unverified로 표시한다.
// kinds는 호스트 → 도메인 종류(spec의 kind)다. aven-server 자신의 라우트처럼 kinds에 없는 호스트는 건드리지 않는다.
// 생성 도메인(KEEL_BASE_DOMAIN 아래)은 운영자 소유라 ACME 보류 대상이 아니다. domain_states를 못 읽으면 아무것도 보류하지 않는다
// (예전 동작). claim 행이 없는 호스트(마이그레이션 이전 경합 등)도 보류하지 않는다.
func (m *Manager) decorateDomainRoutes(ctx context.Context, routes map[string][]proxy.Route, kinds map[string]string) {
	states := map[string]*store.DomainState{}
	if list, err := m.Store.ListDomainStates(ctx); err == nil {
		for _, st := range list {
			states[st.Host] = st
		}
	} else if m.Log != nil {
		m.log().Warn("domain states unavailable; not holding ACME for new domains", "err", err)
	}
	tokens := map[string]string{}
	for nk, rs := range routes {
		for i := range rs {
			r := &rs[i]
			for _, h := range r.Hosts {
				kind, ok := kinds[h]
				if !ok {
					continue
				}
				tok, ok := tokens[h]
				if !ok {
					tok = m.Store.DomainToken(h)
					tokens[h] = tok
				}
				if r.CheckToken == "" {
					r.CheckToken = tok
				}
				if st := states[strings.ToLower(h)]; kind != "generated" && st != nil && !domainTLSOK(st) {
					r.Unverified = true
				}
			}
		}
		routes[nk] = rs
	}
}
