package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/netcheck"
	"github.com/turin-dev/aven/internal/store"
)

// dnsRecord는 도메인에 필요한 DNS 레코드 하나와, 마지막 확인에서 실제로 조회된 값이다.
type dnsRecord struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
	// Status: ok(값이 맞음) | proxied(프록시 뒤라 값은 다르지만 정상) | mismatch(다른 곳을 가리킴) | pending(아직 없음/미확인)
	Status string `json:"status,omitempty"`
	// Current는 마지막 확인에서 조회된 같은 종류의 값이다.
	Current []string `json:"current"`
	// Alternative면 권장 레코드(CNAME) 대신 쓸 수 있는 대안이다.
	Alternative bool   `json:"alternative,omitempty"`
	Note        string `json:"note,omitempty"`
}

type domainVerification struct {
	Method     string     `json:"method"` // legacy | dns | http | txt | ""(미검증)
	VerifiedAt *time.Time `json:"verified_at"`
	TXT        struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"txt"`
}

type domainCert struct {
	Status   string     `json:"status"` // valid | invalid
	NotAfter *time.Time `json:"not_after"`
}

type domainView struct {
	store.Domain
	DNS    []dnsRecord `json:"dns"`
	Status string      `json:"status"` // active | pending_dns | proxied | error
	// Proxy는 마지막 확인의 분류다: direct | cloudflare | tunnel | none | ""(미확인).
	Proxy         string              `json:"proxy"`
	EffectivePort int                 `json:"effective_port"`
	Verification  *domainVerification `json:"verification,omitempty"`
	Cert          *domainCert         `json:"cert,omitempty"`
	CheckedAt     *time.Time          `json:"checked_at,omitempty"`
	LastError     string              `json:"last_error,omitempty"`
	Warnings      []string            `json:"warnings,omitempty"`
	// IPv6는 이 도메인 전용 공개 IPv6 주소다(서비스 노드에 IPv6 풀이 있을 때만, 없으면 생략). 권장 AAAA 레코드가 이 주소를 가리킨다.
	IPv6 string `json:"ipv6,omitempty"`
}

func (s *Server) domainView(ctx context.Context, in *store.Instance, d store.Domain) domainView {
	v := domainView{Domain: d, DNS: []dnsRecord{}, Status: "active"}
	if in != nil && s.Deploy != nil {
		v.EffectivePort = s.Deploy.DomainPort(ctx, in, d)
	}
	st, _ := s.Store.GetDomainState(ctx, d.Host)
	vf := &domainVerification{}
	vf.TXT.Name, vf.TXT.Value = netcheck.TXTName(d.Host), netcheck.TXTValue(s.Store.DomainToken(d.Host))
	v.Verification = vf
	if st != nil {
		vf.Method, vf.VerifiedAt = st.VerifyMethod, st.VerifiedAt
		if !st.Verified() {
			vf.Method = ""
		}
		v.Proxy, v.CheckedAt, v.LastError = st.Proxy, st.CheckedAt, st.LastError
		v.IPv6 = st.IPv6
		if st.CertStatus != "" {
			v.Cert = &domainCert{Status: st.CertStatus, NotAfter: st.CertNotAfter}
		}
	}
	// placement로 여러 노드에서 서비스하면 노드마다 레코드를 둔다(DNS round-robin). 생성 도메인도 이때는 레코드를 보여 준다
	// (기본 도메인 와일드카드는 로컬 노드만 가리키므로 다른 노드로 나누려면 레코드가 필요하다).
	nodes := s.servingIPs(ctx, in)
	if d.Kind != "custom" && len(nodes) < 2 {
		return v
	}
	shared := nodeV6s(nodes)
	nodes = withDedicatedIPv6(nodes, v.IPv6, s.homeNodeID(ctx, in))
	v.DNS, v.Warnings = domainRecords(d.Host, nodes)
	v.Status = domainStatus(st)
	annotateRecords(v.DNS, st)
	noteSharedIPv6(v.DNS, st, v.IPv6, shared)
	return v
}

// homeNodeID는 인스턴스의 home 노드다(도메인 IPv6를 배정하는 노드).
func (s *Server) homeNodeID(ctx context.Context, in *store.Instance) string {
	if in == nil || s.Deploy == nil {
		return store.LocalNodeID
	}
	return store.NormNode(s.Deploy.HomeNode(ctx, in))
}

// nodeV6s는 노드들의 기본 공인 IPv6다.
func nodeV6s(nodes []nodeIP) []string {
	var out []string
	for _, n := range nodes {
		if n.v6 != "" {
			out = append(out, n.v6)
		}
	}
	return out
}

// withDedicatedIPv6는 home 노드의 IPv6를 도메인 전용 주소로 바꾼 노드 목록이다(dedicated가 비면 그대로).
// replica가 여러 노드에 있으면 다른 노드는 기본 주소를 그대로 두어 추가 AAAA 레코드가 된다.
func withDedicatedIPv6(nodes []nodeIP, dedicated, home string) []nodeIP {
	if dedicated == "" {
		return nodes
	}
	out := append([]nodeIP(nil), nodes...)
	for i := range out {
		if store.NormNode(out[i].id) == store.NormNode(home) {
			out[i].v6 = dedicated
			return out
		}
	}
	if len(out) == 1 {
		out[0].v6 = dedicated
		return out
	}
	return append([]nodeIP{{id: home, v6: dedicated, ingress: store.NodeIngress{IngressMode: store.IngressDirect}}}, out...)
}

// sharedIPv6Note는 AAAA가 아직 노드 공용 IPv6를 가리킬 때의 안내다.
const sharedIPv6Note = "지금은 노드 공용 IPv6를 가리킵니다(정상 동작). 전용 IPv6로 바꾸면 도메인별 주소를 씁니다."

// noteSharedIPv6는 전용 주소 AAAA 레코드가 노드 공용 주소를 가리키고 있으면 정상(ok)으로 보고 안내를 붙인다.
func noteSharedIPv6(recs []dnsRecord, st *store.DomainState, dedicated string, shared []string) {
	if dedicated == "" || st == nil || st.CheckedAt == nil {
		return
	}
	for i := range recs {
		r := &recs[i]
		if r.Type != "AAAA" || r.Value != dedicated || r.Status == "ok" || r.Status == "proxied" {
			continue
		}
		for _, c := range r.Current {
			for _, sh := range shared {
				if sameIP(c, sh) {
					r.Status, r.Note = "ok", sharedIPv6Note
				}
			}
		}
	}
}

func sameIP(a, b string) bool {
	x, err1 := netip.ParseAddr(strings.TrimSpace(a))
	y, err2 := netip.ParseAddr(strings.TrimSpace(b))
	return err1 == nil && err2 == nil && x.Unmap() == y.Unmap()
}

// domainStatus는 domain_states의 상태를 API 값으로 바꾼다(아직 확인 전이면 pending_dns).
func domainStatus(st *store.DomainState) string {
	if st == nil || st.DNSStatus == "" || st.DNSStatus == store.DNSPending {
		return store.DNSPendingDNS
	}
	return st.DNSStatus
}

// domainRecords는 서비스 노드들에 맞는 레코드다.
//   - 노드 하나에 ingress_hostname이 있으면 CNAME을 권장하고 AAAA/A는 대안으로 둔다.
//   - Cloudflare 터널 노드는 터널 CNAME을 안내한다(공인 주소 레코드는 없다).
//   - 여러 노드(round-robin)거나 ingress 이름이 없으면 AAAA, A 레코드다.
func domainRecords(host string, nodes []nodeIP) ([]dnsRecord, []string) {
	var warn []string
	var v6s, v4s []string
	for _, n := range nodes {
		if n.v6 != "" {
			v6s = append(v6s, n.v6)
		}
		if n.v4 != "" {
			v4s = append(v4s, n.v4)
		}
		if n.v6 != "" && n.v4 == "" && !n.ingress.Tunnel() {
			warn = append(warn, "이 도메인을 서비스하는 노드에 IPv4 주소가 없습니다. IPv4 클라이언트가 접속하려면 Cloudflare 프록시(주황 구름)를 켜야 합니다.")
		}
	}
	if len(nodes) == 1 {
		n := nodes[0]
		if n.ingress.Tunnel() {
			r := dnsRecord{Type: "CNAME", Name: host, Value: n.ingress.IngressHostname}
			if r.Value == "" {
				r.Value = "<tunnel-id>.cfargotunnel.com"
			}
			r.Note = "Cloudflare 터널 노드입니다. Cloudflare에서 이 호스트를 터널의 public hostname으로 추가하거나, 터널 주소로 프록시된(주황 구름) CNAME을 만드세요."
			return []dnsRecord{r}, warn
		}
		if n.ingress.IngressHostname != "" {
			r := dnsRecord{Type: "CNAME", Name: host, Value: n.ingress.IngressHostname}
			if isApex(host) {
				r.Note = "apex 도메인은 CNAME을 쓸 수 없는 DNS가 많습니다. CNAME flattening/ALIAS를 쓰거나 아래 AAAA/A 레코드를 쓰세요."
			}
			out := []dnsRecord{r}
			for _, a := range dnsRecords(host, v6s, v4s) {
				a.Alternative = true
				out = append(out, a)
			}
			return out, warn
		}
	}
	return dnsRecords(host, v6s, v4s), warn
}

// isApex는 호스트가 등록 도메인 자체(라벨 두 개, 예: example.com)로 보이는지다(공개 접미사 목록 없이 어림잡는다).
func isApex(host string) bool {
	return strings.Count(strings.TrimPrefix(host, "*."), ".") == 1 && !strings.HasPrefix(host, "*.")
}

// annotateRecords는 레코드마다 마지막 확인에서 조회된 값과 상태를 채운다.
func annotateRecords(recs []dnsRecord, st *store.DomainState) {
	for i := range recs {
		r := &recs[i]
		r.Current = []string{}
		if st == nil || st.CheckedAt == nil {
			r.Status = "pending"
			continue
		}
		for _, o := range st.Observed {
			if o.Type == r.Type {
				r.Current = append(r.Current, o.Value)
			}
		}
		match := false
		for _, c := range r.Current {
			if strings.EqualFold(strings.TrimSuffix(c, "."), strings.TrimSuffix(r.Value, ".")) {
				match = true
			}
		}
		switch {
		case match:
			r.Status = "ok"
		case st.Proxy == netcheck.ProxyCloudflare || st.Proxy == netcheck.ProxyTunnel:
			r.Status = "proxied"
		case st.DNSStatus == store.DNSActive && r.Alternative:
			r.Status = "ok" // 권장 CNAME으로 이미 연결됨(대안 레코드는 필요 없다)
		case len(r.Current) == 0:
			r.Status = "pending"
		default:
			r.Status = "mismatch"
		}
	}
}

// dnsRecords는 호스트의 AAAA 레코드들, A 레코드들이다(같은 값은 한 번만).
func dnsRecords(host string, v6s, v4s []string) []dnsRecord {
	out := []dnsRecord{}
	seen := map[string]bool{}
	for _, l := range []struct {
		typ string
		ips []string
	}{{"AAAA", v6s}, {"A", v4s}} {
		for _, ip := range l.ips {
			if ip == "" || seen[l.typ+ip] {
				continue
			}
			seen[l.typ+ip] = true
			out = append(out, dnsRecord{Type: l.typ, Name: host, Value: ip, Current: []string{}})
		}
	}
	return out
}

type nodeIP struct {
	id      string
	v6, v4  string
	ingress store.NodeIngress
}

// servingIPs는 인스턴스의 트래픽을 받는 노드들(placement면 여러 개)의 공인 주소와 ingress 설정이다.
func (s *Server) servingIPs(ctx context.Context, in *store.Instance) []nodeIP {
	if in == nil || s.Deploy == nil {
		v6, v4 := s.nodeIPs(ctx, in)
		return []nodeIP{{id: store.LocalNodeID, v6: v6, v4: v4, ingress: s.nodeIngress(ctx, store.LocalNodeID)}}
	}
	var out []nodeIP
	for _, id := range s.Deploy.ServingNodes(ctx, in) {
		v6, v4 := s.nodeAddr(ctx, id)
		out = append(out, nodeIP{id: id, v6: v6, v4: v4, ingress: s.nodeIngress(ctx, id)})
	}
	return out
}

// dnsProbeHost는 DNS 확인에 쓸 이름이다. 와일드카드는 * 레코드가 답하는 임의의 하위 이름으로 확인한다.
func dnsProbeHost(host string) string { return netcheck.ProbeHost(host) }

// nodeIPs는 인스턴스가 도는 노드의 공인 주소다(원격 노드는 agent가 보고한 주소).
func (s *Server) nodeIPs(ctx context.Context, in *store.Instance) (v6, v4 string) {
	cfg := s.Deploy.Cfg
	if in == nil {
		return cfg.PublicIPv6, cfg.PublicIPv4
	}
	return s.nodeAddr(ctx, s.Deploy.HomeNode(ctx, in))
}

// nodeAddr는 노드의 공인 주소다(로컬 노드는 서버 설정).
func (s *Server) nodeAddr(ctx context.Context, id string) (v6, v4 string) {
	cfg := s.Deploy.Cfg
	if store.IsLocalNode(id) {
		return cfg.PublicIPv6, cfg.PublicIPv4
	}
	n, err := s.Store.GetNode(ctx, id)
	if err != nil {
		return "", ""
	}
	if n.Address.IPv6 != nil {
		v6 = *n.Address.IPv6
	}
	if n.Address.IPv4 != nil {
		v4 = *n.Address.IPv4
	}
	return v6, v4
}

// nodeIngress는 노드의 ingress 설정이다. 로컬 노드는 KEEL_INGRESS_MODE, KEEL_INGRESS_HOSTNAME, KEEL_TCP_PROXY_DOMAIN이 있으면 그 값이 우선한다.
func (s *Server) nodeIngress(ctx context.Context, id string) store.NodeIngress {
	var ing store.NodeIngress
	if store.IsLocalNode(id) {
		if n, err := s.Store.LocalNode(ctx); err == nil {
			ing = n.NodeIngress
		}
		applyLocalIngressEnv(&ing, s.localTCPProxyDomain())
	} else if n, err := s.Store.GetNode(ctx, id); err == nil {
		ing = n.NodeIngress
	}
	if ing.IngressMode == "" {
		ing.IngressMode = store.IngressDirect
	}
	return ing
}

func (s *Server) localTCPProxyDomain() string {
	if s.Deploy != nil {
		return s.Deploy.Cfg.TCPProxyDomain
	}
	return ""
}

// applyLocalIngressEnv는 로컬 노드의 env 설정을 덮어쓴다(값이 있는 것만).
func applyLocalIngressEnv(ing *store.NodeIngress, tcpProxyDomain string) {
	if v := strings.TrimSpace(os.Getenv("KEEL_INGRESS_MODE")); v != "" {
		ing.IngressMode = v
	}
	if v := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(os.Getenv("KEEL_INGRESS_HOSTNAME")), ".")); v != "" {
		ing.IngressHostname = v
	}
	if v := strings.TrimSpace(tcpProxyDomain); v != "" {
		ing.TCPHostname = v
	}
}

// dnsCache는 Server.dns 필드(api.go)의 타입이다. 도메인 상태는 netcheck가 백그라운드로 확인해 domain_states에 쓰고,
// API는 그 결과를 읽는다. checker가 nil이면(테스트) 저장된 상태만 보여 준다.
type dnsCache struct {
	checker *netcheck.Checker
}

// SetNetCheck는 도메인 상태 확인기를 연결한다(cmd/aven-server).
func (s *Server) SetNetCheck(c *netcheck.Checker) { s.dns.checker = c }

// NetCheckTargets는 netcheck가 확인할 도메인과, 각 도메인이 가리켜야 할 노드 주소·ingress 이름이다.
// domainView가 DNS 레코드를 보여 주는 도메인(커스텀, 또는 여러 노드에서 서비스하는 생성 도메인)만 확인한다.
func (s *Server) NetCheckTargets(ctx context.Context) ([]netcheck.Target, error) {
	insts, err := s.Store.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	dedicated := map[string]string{}
	if states, err := s.Store.ListDomainStates(ctx); err == nil {
		for _, st := range states {
			if st.IPv6 != "" {
				dedicated[st.Host] = st.IPv6
			}
		}
	}
	var out []netcheck.Target
	for _, in := range insts {
		if len(in.Domains) == 0 {
			continue
		}
		nodes := s.servingIPs(ctx, in)
		for _, d := range in.Domains {
			if d.Kind != "custom" && len(nodes) < 2 {
				continue
			}
			t := netcheck.Target{Host: d.Host, InstanceID: in.ID, TLSOff: d.TLS == "off"}
			for _, n := range nodes {
				if n.v6 != "" {
					t.IPv6 = append(t.IPv6, n.v6)
				}
				if n.v4 != "" {
					t.IPv4 = append(t.IPv4, n.v4)
				}
				if n.ingress.IngressHostname != "" {
					t.Ingress = append(t.Ingress, n.ingress.IngressHostname)
				}
				if n.ingress.Tunnel() {
					t.Tunnel = true
				}
			}
			// 전용 IPv6도 이 노드를 가리키는 주소로 본다(노드 기본 주소를 가리키는 기존 DNS도 그대로 active다).
			// 인증서 확인은 앞의 주소부터 연결하므로 아직 붙지 않았을 수 있는 전용 주소는 뒤에 둔다.
			if a := dedicated[strings.ToLower(d.Host)]; a != "" {
				t.IPv6 = append(t.IPv6, a)
			}
			out = append(out, t)
		}
	}
	return out, nil
}

// kickDomainCheck는 새 도메인을 다음 루프에서 바로 확인하게 한다.
func (s *Server) kickDomainCheck(host string) {
	if s.dns.checker != nil {
		s.dns.checker.Kick(host)
	}
}

// domainWriteErr는 도메인 쓰기 오류를 응답한다: 호스트 중복은 409(한국어 메시지), 그 밖의 저장 오류는 storeErr.
func (s *Server) domainWriteErr(w http.ResponseWriter, err error) {
	var hc *store.HostClaimedError
	if errors.As(err, &hc) {
		writeErr(w, 409, hc.Error())
		return
	}
	s.storeErr(w, err)
}

// domainInputErr는 도메인 검증 오류를 응답한다: 호스트 중복은 409, 나머지는 400.
func domainInputErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrConflict) {
		writeErr(w, 409, err.Error())
		return
	}
	writeErr(w, 400, err.Error())
}

// verifyDomain: POST /api/services/{sid}/instances/{eid}/domains/{host}/verify — 도메인 상태를 지금 다시 확인한다.
func (s *Server) verifyDomain(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	host := strings.ToLower(r.PathValue("host"))
	for _, d := range c.in.Domains {
		if d.Host != host {
			continue
		}
		if s.dns.checker != nil {
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			s.dns.checker.CheckHost(cctx, host)
			cancel()
		}
		writeJSON(w, 200, s.domainView(ctx, c.in, d))
		return
	}
	writeErr(w, 404, "domain not found")
}

// generateDomain은 <service>-<env>.<base> (production은 <service>.<base>)를 만든다. 겹치면 숫자를 붙인다.
func (s *Server) generateDomain(ctx context.Context, svc, env string) (string, error) {
	base := s.Deploy.Cfg.BaseDomain
	if base == "" {
		return "", errors.New("KEEL_BASE_DOMAIN is not set")
	}
	label := svc
	if env != "production" {
		label = svc + "-" + env
	}
	used, err := s.usedHosts(ctx)
	if err != nil {
		return "", err
	}
	host := label + "." + base
	for i := 2; used[host] != ""; i++ {
		host = fmt.Sprintf("%s-%d.%s", label, i, base)
	}
	return host, nil
}

func (s *Server) usedHosts(ctx context.Context) (map[string]string, error) {
	insts, err := s.Store.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, in := range insts {
		for _, d := range in.Domains {
			out[d.Host] = in.ID
		}
	}
	return out, nil
}

// normalizeDomains는 PATCH로 받은 도메인 목록을 검증하고 kind를 채운다.
func (s *Server) normalizeDomains(ctx context.Context, in *store.Instance, doms []store.Domain) ([]store.Domain, error) {
	out, err := s.checkDomains(ctx, in, doms)
	if err != nil {
		return nil, err
	}
	if err := s.applyDomainAuth(ctx, in, out); err != nil {
		return nil, err
	}
	return out, nil
}

// checkDomains는 normalizeDomains에서 basic auth 저장(부수 효과)을 뺀 검증·정규화다(IaC plan이 쓴다).
func (s *Server) checkDomains(ctx context.Context, in *store.Instance, doms []store.Domain) ([]store.Domain, error) {
	used, err := s.usedHosts(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []store.Domain{}
	for _, d := range doms {
		d.Host = strings.ToLower(strings.TrimSpace(d.Host))
		if seen[d.Host] {
			continue
		}
		seen[d.Host] = true
		if !domainRe.MatchString(d.Host) {
			return nil, fmt.Errorf("invalid domain: %s", d.Host)
		}
		if rest, ok := strings.CutPrefix(d.Host, "*."); ok && !strings.Contains(rest, ".") {
			return nil, fmt.Errorf("invalid domain: %s (a wildcard needs at least two labels after *., e.g. *.example.com)", d.Host)
		}
		if owner := used[d.Host]; owner != "" && owner != in.ID {
			return nil, &store.HostClaimedError{Host: d.Host, InstanceID: owner}
		}
		if d.TargetPort != nil && (*d.TargetPort < 1 || *d.TargetPort > 65535) {
			return nil, errors.New("target_port must be 1..65535")
		}
		base := s.Deploy.Cfg.BaseDomain
		d.Kind = "custom"
		// 와일드카드(*.example.com)는 항상 custom이다: DNS에 * 레코드가 필요하고, 구체적인 호스트 라우트가 우선한다.
		if base != "" && strings.HasSuffix(d.Host, "."+base) && !strings.HasPrefix(d.Host, "*.") {
			d.Kind = "generated"
		}
		if err := s.validateDomainExtras(ctx, in, &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func (s *Server) addDomain(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	var body struct {
		Host *string `json:"host"`
		store.Domain
	}
	if !decode(w, r, &body) {
		return
	}
	d := body.Domain
	d.Kind = ""
	generated := body.Host == nil || strings.TrimSpace(*body.Host) == ""
	reqHost := ""
	if !generated {
		reqHost = *body.Host
	}
	if err := s.checkGeneratedDomainPolicy(ctx, r, reqHost, generated); err != nil {
		s.policyErr(w, err)
		return
	}
	if generated {
		host, err := s.generateDomain(ctx, c.svc.Name, c.env.Name)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		d.Host, d.Kind = host, "generated"
	} else {
		d.Host = *body.Host
	}
	// Cloudflare 터널로만 받는 노드: 터널(cloudflared)이 TLS를 끝내고 평문으로 보내므로 tls=off와 X-Forwarded-Proto 신뢰를
	// 명시적으로 넣는다(넣지 않으면 HTTPS 리다이렉트가 터널과 루프를 만든다). 사용자가 tls를 정했으면 그대로 둔다.
	if d.TLS == "" && s.Deploy != nil && s.nodeIngress(ctx, s.Deploy.HomeNode(ctx, c.in)).Tunnel() {
		d.TLS, d.TrustForwardedProto = "off", true
	}
	all := append(append([]store.Domain{}, c.in.Domains...), d)
	for _, e := range c.in.Domains {
		if strings.EqualFold(e.Host, strings.TrimSpace(d.Host)) {
			writeErr(w, 409, "이 서비스에 이미 추가된 도메인입니다")
			return
		}
	}
	doms, err := s.normalizeDomains(ctx, c.in, all)
	if err != nil {
		domainInputErr(w, err)
		return
	}
	c.in.Domains = doms
	if err := s.Store.UpdateInstanceConfig(ctx, c.in); err != nil {
		s.domainWriteErr(w, err)
		return
	}
	// 도메인별 IPv6: 노드에 풀이 있으면 바로 배정해 응답에 보여 준다(노드에 붙이는 것은 SyncProxy가 깨우는 백그라운드 루프가 한다).
	if err := s.Deploy.AssignDomainIPv6(ctx); err != nil {
		s.Log.Warn("domain ipv6 assign", "err", err)
	}
	if err := s.Deploy.SyncProxy(ctx); err != nil {
		s.Log.Warn("proxy sync", "err", err)
	}
	added := doms[len(doms)-1]
	s.kickDomainCheck(added.Host)
	s.Events.Emit(ctx, c.p.ID, &c.env.ID, &c.svc.ID, "domain.added", fmt.Sprintf("%s (%s): 도메인 %s 추가", c.svc.Name, c.env.Name, added.Host))
	writeJSON(w, 201, s.domainView(ctx, c.in, added))
}

// patchDomain은 도메인 하나의 설정(경로 규칙, TLS 모드, 리다이렉트, basic auth, target_port)을 바꾼다.
// 보낸 키만 바뀌고, null은 값을 지운다. basic_auth는 {username, password?}이고 비밀번호는 응답에 나오지 않는다.
func (s *Server) patchDomain(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	host := strings.ToLower(r.PathValue("host"))
	var patch map[string]json.RawMessage
	if !decode(w, r, &patch) {
		return
	}
	all := append([]store.Domain{}, c.in.Domains...)
	idx := -1
	for i, d := range all {
		if d.Host == host {
			idx = i
		}
	}
	if idx < 0 {
		writeErr(w, 404, "domain not found")
		return
	}
	cur := all[idx]
	newHost, err := s.renameTarget(cur, all, patch)
	if err != nil {
		domainInputErr(w, err)
		return
	}
	// 현재 값 위에 보낸 키만 덮는다(kind는 바꿀 수 없고, host는 생성 도메인만 renameTarget이 허락한 값으로 바꾼다).
	b, _ := json.Marshal(cur)
	var m map[string]json.RawMessage
	json.Unmarshal(b, &m)
	for k, v := range patch {
		switch k {
		case "target_port", "paths", "tls", "trust_forwarded_proto", "redirect", "basic_auth":
			m[k] = v
		}
	}
	b, _ = json.Marshal(m)
	var nd store.Domain
	if err := json.Unmarshal(b, &nd); err != nil {
		writeErr(w, 400, "invalid domain: "+err.Error())
		return
	}
	nd.Host, nd.Kind = cur.Host, cur.Kind
	if newHost != "" {
		nd.Host = newHost
	}
	all[idx] = nd
	var doms []store.Domain
	if newHost == "" {
		doms, err = s.normalizeDomains(ctx, c.in, all)
	} else {
		doms, err = s.normalizeRenamedDomains(ctx, c.in, all, host, newHost)
	}
	if err != nil {
		domainInputErr(w, err)
		return
	}
	c.in.Domains = doms
	if newHost == "" {
		err = s.Store.UpdateInstanceConfig(ctx, c.in)
	} else {
		// 스펙, basic auth 해시, domain_states claim을 한 트랜잭션으로 옮긴다.
		err = s.Store.RenameInstanceDomain(ctx, c.in, host, newHost)
	}
	if err != nil {
		s.domainWriteErr(w, err)
		return
	}
	if err := s.Deploy.SyncProxy(ctx); err != nil {
		s.Log.Warn("proxy sync", "err", err)
	}
	if newHost != "" {
		s.kickDomainCheck(newHost)
		s.Events.Emit(ctx, c.p.ID, &c.env.ID, &c.svc.ID, "domain.renamed", fmt.Sprintf("%s (%s): 도메인 %s → %s", c.svc.Name, c.env.Name, host, newHost))
		host = newHost
	}
	for _, d := range doms {
		if d.Host == host {
			writeJSON(w, 200, s.domainView(ctx, c.in, d))
			return
		}
	}
}

func (s *Server) deleteDomain(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	host := strings.ToLower(r.PathValue("host"))
	var keep []store.Domain
	found := false
	for _, d := range c.in.Domains {
		if d.Host == host {
			found = true
			continue
		}
		keep = append(keep, d)
	}
	if !found {
		writeErr(w, 404, "domain not found")
		return
	}
	c.in.Domains = keep
	s.Store.SetSetting(ctx, store.DomainAuthKey(c.in.ID, host), "")
	if c.in.Domains == nil {
		c.in.Domains = []store.Domain{}
	}
	if err := s.Store.UpdateInstanceConfig(ctx, c.in); err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.Deploy.SyncProxy(ctx); err != nil {
		s.Log.Warn("proxy sync", "err", err)
	}
	w.WriteHeader(204)
}

// generatedLabelRe는 생성 도메인의 첫 이름(<label>.<KEEL_BASE_DOMAIN>)이다.
var generatedLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// renameTarget은 PATCH 본문의 host가 현재 호스트와 다르면 새 호스트를 돌려준다(같거나 없으면 "").
// 이름은 생성 도메인만 바꿀 수 있고, 접미사(KEEL_BASE_DOMAIN)는 그대로, 첫 이름은 DNS label 하나여야 한다.
// basic_auth와 같이 바꾸는 것은 받지 않는다(해시를 옮기는 것과 새로 쓰는 것이 섞이지 않게).
func (s *Server) renameTarget(cur store.Domain, all []store.Domain, patch map[string]json.RawMessage) (string, error) {
	raw, ok := patch["host"]
	if !ok || string(raw) == "null" {
		return "", nil
	}
	var h string
	if err := json.Unmarshal(raw, &h); err != nil {
		return "", errors.New("host must be a string")
	}
	h = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), "."))
	if h == "" || h == cur.Host {
		return "", nil
	}
	if cur.Kind != "generated" {
		return "", errors.New("커스텀 도메인은 이름을 바꿀 수 없습니다. 새 도메인을 추가하고 이 도메인을 삭제하세요")
	}
	base := ""
	if s.Deploy != nil {
		base = s.Deploy.Cfg.BaseDomain
	}
	label, ok := strings.CutSuffix(h, "."+base)
	if base == "" || !ok || !strings.HasSuffix(cur.Host, "."+base) {
		return "", fmt.Errorf("생성 도메인은 .%s 접미사를 바꿀 수 없습니다", base)
	}
	if !generatedLabelRe.MatchString(label) {
		return "", fmt.Errorf("invalid domain label: %q (영문 소문자, 숫자, '-'만, 63자 이하)", label)
	}
	if _, ok := patch["basic_auth"]; ok {
		return "", errors.New("이름 변경과 basic_auth 변경은 따로 요청하세요")
	}
	for _, d := range all {
		if d.Host == h {
			return "", &store.HostClaimedError{Host: h}
		}
	}
	return h, nil
}

// normalizeRenamedDomains는 이름을 바꾼 도메인 목록을 검증한다. basic auth 해시는 RenameInstanceDomain이 트랜잭션 안에서
// 옮기므로, 여기서는 이전 호스트의 설정을 새 호스트 이름으로 보고 확인만 한다(설정을 쓰거나 지우지 않는다).
func (s *Server) normalizeRenamedDomains(ctx context.Context, in *store.Instance, doms []store.Domain, oldHost, newHost string) ([]store.Domain, error) {
	out, err := s.checkDomains(ctx, in, doms)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Host == newHost && out[i].Kind != "generated" {
			return nil, fmt.Errorf("생성 도메인은 .%s 접미사를 바꿀 수 없습니다", s.Deploy.Cfg.BaseDomain)
		}
	}
	renamed := *in
	renamed.Domains = make([]store.Domain, len(in.Domains))
	for i, d := range in.Domains {
		if d.Host == oldHost {
			d.Host = newHost
		}
		renamed.Domains[i] = d
	}
	if err := s.applyDomainAuth(ctx, &renamed, out); err != nil {
		return nil, err
	}
	return out, nil
}
