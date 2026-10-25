// Package netcheck는 공개 도메인의 DNS·소유 확인·인증서 상태를 백그라운드로 확인해 domain_states에 기록한다.
//
// 확인 한 번:
//  1. A, AAAA, CNAME을 조회한다(와일드카드 *.x는 keel-dns-check.x로 조회).
//  2. direct(노드 주소나 ingress 호스트를 가리킴), cloudflare(CF 엣지 대역), tunnel(터널 노드이거나 cfargotunnel CNAME), none으로 나눈다.
//  3. cloudflare/tunnel이면 https://<host>/.well-known/keel/domain-check 응답에서 keel-domain-check=<token>을 찾는다.
//  4. _keel-verify.<host> TXT에서 keel-verify=<token>을 찾는다.
//
// 상태: direct → active, cloudflare/tunnel → proxied, none → pending_dns, 조회 실패 → error.
// 검증(verified_at)은 direct, HTTP 응답, TXT 중 하나가 맞으면 처음 한 번 기록한다(legacy 호스트는 이미 검증됨).
package netcheck

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

// 분류(domain_states.proxy).
const (
	ProxyDirect     = "direct"
	ProxyCloudflare = "cloudflare"
	ProxyTunnel     = "tunnel"
	ProxyNone       = "none"
)

// CheckPath는 프록시가 확인 토큰을 돌려주는 경로다(P3에서 keel-proxy가 응답한다).
const CheckPath = "/.well-known/keel/domain-check"

// Resolver는 DNS 조회다(*net.Resolver가 만족한다). 테스트에서 바꿔 끼운다.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
	LookupCNAME(ctx context.Context, host string) (string, error)
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// Target은 확인할 도메인 하나와, 그 도메인이 가리켜야 할 곳이다.
type Target struct {
	Host       string
	InstanceID string
	// IPv6, IPv4는 이 도메인을 서비스하는 노드들의 공인 주소다.
	IPv6, IPv4 []string
	// Ingress는 CNAME이 가리켜도 되는 이름(노드 ingress_hostname)이다.
	Ingress []string
	// Tunnel이면 서비스 노드가 Cloudflare 터널로만 받는다(CF 대역으로 resolve되면 tunnel로 분류).
	Tunnel bool
	// TLSOff면 인증서를 확인하지 않는다(tls=off 도메인).
	TLSOff bool
}

// Result는 확인 한 번의 결과다.
type Result struct {
	Status       string             `json:"status"`
	Proxy        string             `json:"proxy"`
	Observed     []store.ObservedRR `json:"observed"`
	VerifyMethod string             `json:"verify_method,omitempty"` // 이번 확인에서 맞은 방법(없으면 "")
	CertStatus   string             `json:"cert_status,omitempty"`
	CertNotAfter *time.Time         `json:"cert_not_after,omitempty"`
	Error        string             `json:"error,omitempty"`
}

// Checker는 도메인 상태 확인 루프다.
type Checker struct {
	Store *store.Store
	// Targets는 지금 확인할 도메인 목록이다(api가 노드 주소·ingress를 채워 준다).
	Targets func(ctx context.Context) ([]Target, error)
	// Token은 호스트의 소유 확인 토큰이다(nil이면 Store.DomainToken).
	Token func(host string) string
	// Resolver가 nil이면 net.DefaultResolver.
	Resolver Resolver
	// HTTP가 nil이면 Cloudflare 대역에만 연결하는 기본 클라이언트(IPv6 먼저)를 쓴다.
	HTTP *http.Client
	// CertDial이 있으면 인증서 확인에 쓴다(nil이고 CheckCerts면 노드 주소로 직접 TLS 연결).
	CertDial   func(ctx context.Context, network, addr string) (net.Conn, error)
	CheckCerts bool
	Log        *slog.Logger
	// Interval은 기본 확인 간격(0이면 60초), MaxInterval은 검증된 도메인의 최대 간격(0이면 10분).
	Interval, MaxInterval time.Duration
	// Workers는 동시에 확인하는 도메인 수(0이면 4).
	Workers int
	// OnVerified가 있으면 확인 전이던 도메인이 이번 확인으로 검증됐을 때 부른다(서버가 라우트를 다시 보내 ACME 보류를 푼다).
	OnVerified func(host string)

	mu    sync.Mutex
	sched map[string]schedule
	kick  chan struct{}
	once  sync.Once
}

type schedule struct {
	next     time.Time
	interval time.Duration
}

func (c *Checker) init() {
	c.once.Do(func() {
		c.sched = map[string]schedule{}
		c.kick = make(chan struct{}, 1)
	})
}

func (c *Checker) interval() time.Duration {
	if c.Interval > 0 {
		return c.Interval
	}
	return time.Minute
}

func (c *Checker) maxInterval() time.Duration {
	if c.MaxInterval > 0 {
		return c.MaxInterval
	}
	return 10 * time.Minute
}

func (c *Checker) resolver() Resolver {
	if c.Resolver != nil {
		return c.Resolver
	}
	return net.DefaultResolver
}

func (c *Checker) token(host string) string {
	if c.Token != nil {
		return c.Token(host)
	}
	if c.Store != nil {
		return c.Store.DomainToken(host)
	}
	return ""
}

// ProbeHost는 DNS 확인에 쓸 이름이다. 와일드카드는 * 레코드가 답하는 임의의 하위 이름으로 확인한다.
func ProbeHost(host string) string {
	if rest, ok := strings.CutPrefix(host, "*."); ok {
		return "keel-dns-check." + rest
	}
	return host
}

// TXTName은 소유 확인 TXT 레코드 이름이다(와일드카드는 *.를 뗀 이름).
func TXTName(host string) string { return "_keel-verify." + strings.TrimPrefix(host, "*.") }

// TXTValue는 TXT 레코드 값이다.
func TXTValue(token string) string { return "keel-verify=" + token }

// CheckBody는 /.well-known/keel/domain-check 응답 본문이다.
func CheckBody(token string) string { return "keel-domain-check=" + token }

func normName(s string) string { return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), ".")) }

// Check는 도메인 하나를 확인한다(저장하지 않는다).
func (c *Checker) Check(ctx context.Context, t Target) Result {
	res := Result{Proxy: ProxyNone, Status: store.DNSPendingDNS, Observed: []store.ObservedRR{}}
	host := normName(t.Host)
	probe := ProbeHost(host)
	r := c.resolver()
	tok := c.token(host)

	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	cname, _ := r.LookupCNAME(lctx, probe)
	cancel()
	cname = normName(cname)
	if cname == probe {
		cname = ""
	}
	if cname != "" {
		res.Observed = append(res.Observed, store.ObservedRR{Type: "CNAME", Value: cname})
	}
	lctx, cancel = context.WithTimeout(ctx, 5*time.Second)
	addrs, err := r.LookupIPAddr(lctx, probe)
	cancel()
	var ips []netip.Addr
	for _, a := range addrs {
		ip, ok := netip.AddrFromSlice(a.IP)
		if !ok {
			continue
		}
		ip = ip.Unmap()
		ips = append(ips, ip)
		typ := "A"
		if ip.Is6() {
			typ = "AAAA"
		}
		res.Observed = append(res.Observed, store.ObservedRR{Type: typ, Value: ip.String()})
	}
	if err != nil && !isNotFound(err) {
		res.Status, res.Error = store.DNSError, err.Error()
	}

	want := map[netip.Addr]bool{}
	for _, s := range append(append([]string(nil), t.IPv6...), t.IPv4...) {
		if ip, err := netip.ParseAddr(strings.TrimSpace(s)); err == nil {
			want[ip.Unmap()] = true
		}
	}
	direct := false
	for _, ip := range ips {
		if want[ip] {
			direct = true
		}
	}
	if cname != "" {
		for _, ing := range t.Ingress {
			if ing = normName(ing); ing != "" && (cname == ing || strings.HasSuffix(cname, "."+ing)) {
				direct = true
			}
		}
	}
	cf := false
	for _, ip := range ips {
		if IsCloudflare(ip) {
			cf = true
		}
	}
	tunnelCNAME := strings.HasSuffix(cname, ".cfargotunnel.com")
	switch {
	case direct:
		res.Proxy, res.Status, res.Error = ProxyDirect, store.DNSActive, ""
		res.VerifyMethod = store.VerifyDNS
	case tunnelCNAME || (cf && t.Tunnel):
		res.Proxy, res.Status, res.Error = ProxyTunnel, store.DNSProxied, ""
	case cf:
		res.Proxy, res.Status, res.Error = ProxyCloudflare, store.DNSProxied, ""
	}

	if res.VerifyMethod == "" && tok != "" {
		// 프록시 뒤라 DNS로는 이 노드인지 알 수 없다: 프록시가 돌려주는 토큰으로 확인한다(와일드카드는 이름이 정해지지 않아 건너뛴다).
		if (res.Proxy == ProxyCloudflare || res.Proxy == ProxyTunnel) && !strings.HasPrefix(host, "*.") {
			if ok, err := c.probe(ctx, host, tok); ok {
				res.VerifyMethod = store.VerifyHTTP
			} else if err != nil && c.Log != nil {
				c.Log.Debug("domain check probe", "host", host, "err", err)
			}
		}
	}
	if tok != "" {
		lctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		txts, _ := r.LookupTXT(lctx, TXTName(host))
		cancel()
		for _, v := range txts {
			res.Observed = append(res.Observed, store.ObservedRR{Type: "TXT", Value: v})
			if res.VerifyMethod == "" && strings.TrimSpace(v) == TXTValue(tok) {
				res.VerifyMethod = store.VerifyTXT
			}
		}
	}
	if c.CheckCerts && res.Proxy == ProxyDirect && !t.TLSOff && !strings.HasPrefix(host, "*.") {
		res.CertStatus, res.CertNotAfter = c.cert(ctx, host, t)
	}
	return res
}

func isNotFound(err error) bool {
	var de *net.DNSError
	return errors.As(err, &de) && de.IsNotFound
}

// probe는 https://<host>/.well-known/keel/domain-check 응답에 토큰이 있는지 본다.
func (c *Checker) probe(ctx context.Context, host, tok string) (bool, error) {
	cl := c.HTTP
	if cl == nil {
		cl = c.defaultClient()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "https://"+host+CheckPath, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", "keel-netcheck")
	resp, err := cl.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode != 200 {
		return false, fmt.Errorf("status %d", resp.StatusCode)
	}
	return strings.Contains(string(b), CheckBody(tok)), nil
}

// defaultClient는 Cloudflare 엣지 주소로만 연결하는 HTTP 클라이언트다(사용자가 정한 호스트로 요청하므로 사설 주소 등으로는
// 나가지 않게 막는다). IPv6 주소를 먼저 시도하고, 실패하면 IPv4로 넘어간다(IPv6만의 문제로 실패하지 않는다).
func (c *Checker) defaultClient() *http.Client {
	r := c.resolver()
	tr := &http.Transport{
		Proxy:               nil,
		TLSHandshakeTimeout: 5 * time.Second,
		DisableKeepAlives:   true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			addrs, err := r.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			var ips []netip.Addr
			for _, a := range addrs {
				if ip, ok := netip.AddrFromSlice(a.IP); ok && IsCloudflare(ip) {
					ips = append(ips, ip.Unmap())
				}
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("%s does not resolve to a Cloudflare edge address", host)
			}
			return dialPreferV6(ctx, nil, ips, port)
		},
	}
	return &http.Client{Transport: tr, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// dialPreferV6는 IPv6 주소부터 차례로 연결한다.
func dialPreferV6(ctx context.Context, dial func(ctx context.Context, network, addr string) (net.Conn, error), ips []netip.Addr, port string) (net.Conn, error) {
	sort.SliceStable(ips, func(i, j int) bool { return ips[i].Is6() && !ips[j].Is6() })
	if dial == nil {
		d := &net.Dialer{Timeout: 3 * time.Second}
		dial = d.DialContext
	}
	var last error
	for _, ip := range ips {
		network := "tcp4"
		if ip.Is6() {
			network = "tcp6"
		}
		conn, err := dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	if last == nil {
		last = errors.New("no address")
	}
	return nil, last
}

// cert는 노드 주소로 직접 TLS 연결해 그 호스트의 인증서를 본다: valid(검증 통과) | invalid(검증 실패) | ""(연결 못 함).
func (c *Checker) cert(ctx context.Context, host string, t Target) (string, *time.Time) {
	var ips []netip.Addr
	for _, s := range append(append([]string(nil), t.IPv6...), t.IPv4...) {
		if ip, err := netip.ParseAddr(strings.TrimSpace(s)); err == nil {
			ips = append(ips, ip.Unmap())
		}
	}
	if len(ips) == 0 {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	raw, err := dialPreferV6(ctx, c.CertDial, ips, "443")
	if err != nil {
		return "", nil
	}
	defer raw.Close()
	raw.SetDeadline(time.Now().Add(5 * time.Second))
	conn := tls.Client(raw, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err := conn.HandshakeContext(ctx); err != nil {
		// 인증서가 아직 없거나(발급 전) 신뢰할 수 없다.
		return "invalid", nil
	}
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "invalid", nil
	}
	na := certs[0].NotAfter.UTC()
	return "valid", &na
}

// CheckAndRecord는 도메인 하나를 확인하고 결과를 저장한다. 다음 확인 시각도 정한다.
func (c *Checker) CheckAndRecord(ctx context.Context, t Target) Result {
	c.init()
	res := c.Check(ctx, t)
	now := time.Now().UTC()
	wasVerified := true
	if c.Store != nil && c.OnVerified != nil && res.VerifyMethod != "" {
		if st, err := c.Store.GetDomainState(ctx, t.Host); err == nil && st.InstanceID == t.InstanceID {
			wasVerified = st.Verified()
		}
	}
	if c.Store != nil {
		err := c.Store.RecordDomainCheck(ctx, t.Host, t.InstanceID, store.DomainCheck{DNSStatus: res.Status, Proxy: res.Proxy, Observed: res.Observed,
			VerifyMethod: res.VerifyMethod, CertStatus: res.CertStatus, CertNotAfter: res.CertNotAfter, LastError: res.Error, CheckedAt: now})
		if err != nil && c.Log != nil {
			c.Log.Warn("domain check record", "host", t.Host, "err", err)
		}
	}
	if !wasVerified {
		c.OnVerified(t.Host)
	}
	verified := res.VerifyMethod != ""
	if !verified && c.Store != nil {
		if st, err := c.Store.GetDomainState(ctx, t.Host); err == nil {
			verified = st.Verified()
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.sched[normName(t.Host)]
	if verified && (res.Status == store.DNSActive || res.Status == store.DNSProxied) {
		// 검증되고 정상인 도메인은 간격을 두 배씩 늘린다(최대 MaxInterval).
		if s.interval < c.interval() {
			s.interval = c.interval()
		} else {
			s.interval *= 2
		}
		if s.interval > c.maxInterval() {
			s.interval = c.maxInterval()
		}
	} else {
		s.interval = c.interval()
	}
	s.next = now.Add(s.interval)
	c.sched[normName(t.Host)] = s
	return res
}

// CheckHost는 호스트 하나를 지금 확인한다(POST …/domains/{host}/verify). 대상이 아니면 ok=false.
func (c *Checker) CheckHost(ctx context.Context, host string) (Result, bool) {
	if c == nil || c.Targets == nil {
		return Result{}, false
	}
	ts, err := c.Targets(ctx)
	if err != nil {
		return Result{}, false
	}
	host = normName(host)
	for _, t := range ts {
		if normName(t.Host) == host {
			return c.CheckAndRecord(ctx, t), true
		}
	}
	return Result{}, false
}

// Kick은 호스트를 다음 루프에서 바로 확인하게 한다(도메인 추가 직후).
func (c *Checker) Kick(host string) {
	if c == nil {
		return
	}
	c.init()
	c.mu.Lock()
	s := c.sched[normName(host)]
	s.next, s.interval = time.Time{}, 0
	c.sched[normName(host)] = s
	c.mu.Unlock()
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// RunOnce는 확인할 때가 된 도메인을 모두 확인한다.
func (c *Checker) RunOnce(ctx context.Context) {
	c.init()
	if c.Targets == nil {
		return
	}
	ts, err := c.Targets(ctx)
	if err != nil {
		if c.Log != nil {
			c.Log.Warn("domain check targets", "err", err)
		}
		return
	}
	now := time.Now()
	live := map[string]bool{}
	var due []Target
	c.mu.Lock()
	for _, t := range ts {
		h := normName(t.Host)
		live[h] = true
		if !now.Before(c.sched[h].next) {
			due = append(due, t)
		}
	}
	for h := range c.sched {
		if !live[h] {
			delete(c.sched, h)
		}
	}
	c.mu.Unlock()
	workers := c.Workers
	if workers <= 0 {
		workers = 4
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, t := range due {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(t Target) {
			defer func() { <-sem; wg.Done() }()
			c.CheckAndRecord(ctx, t)
		}(t)
	}
	wg.Wait()
}

// Run은 ctx가 끝날 때까지 확인 루프를 돈다(15초마다 때가 된 도메인을 확인, Kick이면 바로).
func (c *Checker) Run(ctx context.Context) {
	c.init()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		c.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-c.kick:
		}
	}
}
