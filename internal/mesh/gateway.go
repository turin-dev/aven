package mesh

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/agentpki"
)

// Resolver는 이 노드에서 도는 (환경, 서비스) 컨테이너의 주소를 찾는다.
type Resolver interface {
	Upstreams(ctx context.Context, envID, serviceID string) ([]netip.Addr, error)
}

// EnvNet은 환경 네트워크의 서브넷과 호스트 쪽 gateway 주소다.
// Gateway6은 네트워크에 IPv6도 있을 때의 IPv6 gateway다(그러면 forwarder가 IPv6에서도 listen한다).
type EnvNet struct {
	Subnets  []netip.Prefix
	Gateway  netip.Addr
	Gateway6 netip.Addr
}

// Binding은 import 하나의 포트가 묶인 로컬 주소다(forwarder 컨테이너가 넘길 곳). Local6은 IPv6 로컬 주소(없으면 0 값)다.
type Binding struct {
	EnvID   string
	Service string
	Port    int
	Local   netip.AddrPort
	Local6  netip.AddrPort
}

// 기본 한도. 인증 전 연결(TLS handshake + 헤더)은 노드 전체에서 동시에 DefaultMaxHandshakes개, 출발 IP마다
// 초당 DefaultHandshakeRate개(순간 DefaultHandshakeBurst개)까지 받는다(mesh peer의 사설 주소는 IP 한도에서 뺀다).
// 인증된 peer 하나는 동시에 DefaultMaxTunnelsPerPeer개까지 터널을 연다.
const (
	DefaultMaxHandshakes     = 64
	DefaultHandshakeRate     = 20
	DefaultHandshakeBurst    = 40
	DefaultMaxTunnelsPerPeer = 512
	maxRateEntries           = 4096
)

// ErrRefused는 상대 gateway가 헤더를 거부했을 때다(상대에는 닿았다: failover 상태를 바꾸지 않는다).
var ErrRefused = errors.New("mesh: refused")

// Status는 GET /api/nodes의 mesh 상태다.
type Status struct {
	Enabled bool         `json:"enabled"`
	Listen  *string      `json:"listen"`
	Peers   []PeerStatus `json:"peers"`
	Error   string       `json:"error,omitempty"`
	Reason  string       `json:"reason,omitempty"` // enabled=false인 이유
}

type PeerStatus struct {
	NodeID   string     `json:"node_id"`
	Status   string     `json:"status"` // ok | unreachable | pending
	LastOKAt *time.Time `json:"last_ok_at"`
	RTTMs    *float64   `json:"rtt_ms"`
	Error    string     `json:"error,omitempty"`
}

// Gateway는 한 노드의 mesh gateway다.
type Gateway struct {
	NodeID string
	// Listen은 mesh listen 주소(사설 주소:포트)다. 노드 설정이며 서버가 정하지 못한다.
	Listen string
	// Proxy가 있으면 peer에 이 HTTP 프록시(host:port)의 CONNECT로 나간다(Tailscale userspace-networking 노드).
	Proxy string
	// Roots는 aven CA다. Cert는 이 노드의 현재 인증서(갱신되면 새 것)를 돌려준다.
	Roots    *x509.CertPool
	Cert     func() (*tls.Certificate, error)
	Resolve  Resolver
	Log      *slog.Logger
	Interval time.Duration // 상태 확인 주기(0이면 30초)
	Timeout  time.Duration // 연결/헤더 제한(0이면 5초)
	// Now는 인증서 검증 시각이다(테스트용).
	Now func() time.Time
	// 한도(0이면 기본값). HandshakeRate는 출발 IP마다 초당 새 연결 수, HandshakeBurst는 순간 허용량이다.
	MaxHandshakes     int
	HandshakeRate     float64
	HandshakeBurst    int
	MaxTunnelsPerPeer int

	mu      sync.RWMutex
	peers   map[string]Peer
	peerIPs map[netip.Addr]bool // peer 주소의 IP(출발 IP 한도에서 뺀다)
	serials map[string]map[string]bool
	exports map[string]Export
	locals  map[string]*localListener // env/service/port, IPv6는 env/service/port/6
	status  map[string]*PeerStatus
	ln      net.Listener
	lnErr   string
	closed  bool
	wake    chan struct{}
	hsSem   chan struct{} // 인증 전 연결 슬롯

	limMu   sync.Mutex
	ipLim   map[netip.Addr]*bucket
	tunMu   sync.Mutex
	tunnels map[string]int // peer → 열린 터널 수
}

// bucket은 출발 IP 하나의 token bucket이다.
type bucket struct {
	tokens float64
	last   time.Time
}

func (g *Gateway) maxHandshakes() int {
	if g.MaxHandshakes > 0 {
		return g.MaxHandshakes
	}
	return DefaultMaxHandshakes
}

func (g *Gateway) rate() (float64, float64) {
	r, b := g.HandshakeRate, float64(g.HandshakeBurst)
	if r <= 0 {
		r = DefaultHandshakeRate
	}
	if b <= 0 {
		b = DefaultHandshakeBurst
	}
	return r, b
}

func (g *Gateway) maxTunnels() int {
	if g.MaxTunnelsPerPeer > 0 {
		return g.MaxTunnelsPerPeer
	}
	return DefaultMaxTunnelsPerPeer
}

// allowIP는 출발 IP a의 새 연결을 받아도 되는지다(token bucket). 기록이 너무 많으면 쉬는 IP를 먼저 지우고,
// 그래도 가득하면 새 IP를 받지 않는다(메모리 상한).
func (g *Gateway) allowIP(a netip.Addr, now time.Time) bool {
	rate, burst := g.rate()
	g.limMu.Lock()
	defer g.limMu.Unlock()
	if g.ipLim == nil {
		g.ipLim = map[netip.Addr]*bucket{}
	}
	b := g.ipLim[a]
	if b == nil {
		if len(g.ipLim) >= maxRateEntries {
			for k, o := range g.ipLim {
				if o.tokens+now.Sub(o.last).Seconds()*rate >= burst {
					delete(g.ipLim, k)
				}
			}
			if len(g.ipLim) >= maxRateEntries {
				return false
			}
		}
		b = &bucket{tokens: burst, last: now}
		g.ipLim[a] = b
	}
	b.tokens = min(burst, b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// admit은 새 연결을 받을지 정한다: 출발 IP 한도(peer 주소는 제외)와 인증 전 연결 슬롯. 받으면 슬롯을 돌려줄 함수를 준다.
func (g *Gateway) admit(remote net.Addr) (func(), bool) {
	ap, err := netip.ParseAddrPort(remote.String())
	if err != nil {
		return nil, false
	}
	a := ap.Addr().Unmap()
	g.mu.RLock()
	known, sem := g.peerIPs[a], g.hsSem
	g.mu.RUnlock()
	if sem == nil {
		return nil, false
	}
	if !known && !g.allowIP(a, time.Now()) {
		return nil, false
	}
	select {
	case sem <- struct{}{}:
		return sync.OnceFunc(func() { <-sem }), true
	default:
		return nil, false
	}
}

// acquireTunnel은 peer의 터널 수가 한도 안이면 하나 늘린다.
func (g *Gateway) acquireTunnel(peer string) bool {
	g.tunMu.Lock()
	defer g.tunMu.Unlock()
	if g.tunnels == nil {
		g.tunnels = map[string]int{}
	}
	if g.tunnels[peer] >= g.maxTunnels() {
		return false
	}
	g.tunnels[peer]++
	return true
}

func (g *Gateway) releaseTunnel(peer string) {
	g.tunMu.Lock()
	defer g.tunMu.Unlock()
	if g.tunnels[peer]--; g.tunnels[peer] <= 0 {
		delete(g.tunnels, peer)
	}
}

// Tunnels는 peer가 지금 이 노드에 열어 둔 터널 수다.
func (g *Gateway) Tunnels(peer string) int {
	g.tunMu.Lock()
	defer g.tunMu.Unlock()
	return g.tunnels[peer]
}

func (g *Gateway) log() *slog.Logger {
	if g.Log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return g.Log
}

func (g *Gateway) timeout() time.Duration {
	if g.Timeout > 0 {
		return g.Timeout
	}
	return 5 * time.Second
}

func (g *Gateway) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *Gateway) init() {
	if g.peers == nil {
		g.peers, g.serials, g.exports = map[string]Peer{}, map[string]map[string]bool{}, map[string]Export{}
		g.locals, g.status = map[string]*localListener{}, map[string]*PeerStatus{}
		g.wake = make(chan struct{}, 1)
	}
	if g.hsSem == nil {
		g.hsSem = make(chan struct{}, g.maxHandshakes())
	}
}

// Run은 mesh listener를 열고(실패하면 10초마다 다시 시도: 사설 네트워크가 늦게 올라오는 경우) 상태 확인을 돈다. ctx가 끝나면 닫는다.
func (g *Gateway) Run(ctx context.Context) {
	g.mu.Lock()
	g.init()
	g.mu.Unlock()
	go g.probeLoop(ctx)
	for ctx.Err() == nil {
		ln, err := net.Listen("tcp", g.Listen)
		if err != nil {
			g.mu.Lock()
			g.lnErr = "listen " + g.Listen + ": " + err.Error()
			g.mu.Unlock()
			g.log().Warn("mesh gateway listen failed; retrying", "addr", g.Listen, "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Second):
			}
			continue
		}
		g.mu.Lock()
		g.ln, g.lnErr = ln, ""
		g.mu.Unlock()
		g.log().Info("mesh gateway listening", "addr", ln.Addr().String())
		go func() { <-ctx.Done(); ln.Close() }()
		g.serve(ctx, ln)
	}
	g.Close()
}

// Addr는 실제 listen 주소다(아직 열지 못했으면 "").
func (g *Gateway) Addr() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.ln == nil {
		return ""
	}
	return g.ln.Addr().String()
}

// Close는 listener와 로컬 포트를 모두 닫는다.
func (g *Gateway) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	if g.ln != nil {
		g.ln.Close()
	}
	for k, l := range g.locals {
		l.ln.Close()
		delete(g.locals, k)
	}
}

func (g *Gateway) serve(ctx context.Context, ln net.Listener) {
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{ALPN, LegacyALPN},
		ClientAuth: tls.RequireAnyClientCert,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return g.Cert()
		},
		VerifyConnection: func(cs tls.ConnectionState) error {
			_, err := g.verifyPeer(cs.PeerCertificates, "")
			return err
		},
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		// TLS handshake 전에 거른다: 출발 IP 한도와 인증 전 연결 수 상한(handshake를 붙잡아 두는 연결로 막히지 않게).
		release, ok := g.admit(c.RemoteAddr())
		if !ok {
			g.log().Debug("mesh connection rejected (handshake limit)", "remote", c.RemoteAddr().String())
			c.Close()
			continue
		}
		go g.handle(ctx, tls.Server(c, tlsCfg), release)
	}
}

// verifyPeer는 상대 인증서를 확인한다: aven CA 서명·유효기간·노드 신원, 서버가 알려 준 노드이고 그 serial이 유효한지.
// want가 있으면 그 노드여야 한다(연결하는 쪽이 상대를 확인할 때).
func (g *Gateway) verifyPeer(certs []*x509.Certificate, want string) (string, error) {
	if len(certs) == 0 {
		return "", errors.New("mesh: no peer certificate")
	}
	v, err := agentpki.VerifyNodeCert(g.Roots, certs[0], g.now())
	if err != nil {
		return "", fmt.Errorf("mesh: %w", err)
	}
	if want != "" && v.NodeID != want {
		return "", fmt.Errorf("mesh: peer is %s, expected %s", v.NodeID, want)
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	if _, ok := g.peers[v.NodeID]; !ok {
		return "", fmt.Errorf("mesh: node %s is not a mesh peer", v.NodeID)
	}
	if !g.serials[v.NodeID][strings.ToLower(v.Serial)] {
		return "", fmt.Errorf("mesh: certificate of node %s is revoked or unknown", v.NodeID)
	}
	return v.NodeID, nil
}

func writeReply(c net.Conn, r Reply) error {
	b, _ := json.Marshal(r)
	_, err := c.Write(append(b, '\n'))
	return err
}

// readLine은 \n까지 최대 MaxHeaderBytes를 읽는다(그 뒤의 바이트는 br에 남는다).
func readLine(br *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		b, err := br.ReadByte()
		if err != nil {
			return nil, err
		}
		if b == '\n' {
			return buf, nil
		}
		buf = append(buf, b)
		if len(buf) > MaxHeaderBytes {
			return nil, errors.New("header too long")
		}
	}
}

// DecodeHeader는 헤더 한 줄을 엄격하게 읽는다(알 수 없는 필드 거부).
func DecodeHeader(line []byte) (Header, error) {
	var h Header
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&h); err != nil {
		return h, errors.New("invalid header")
	}
	if dec.More() {
		return h, errors.New("invalid header")
	}
	return h, ValidateHeader(h)
}

func (g *Gateway) handle(ctx context.Context, c *tls.Conn, release func()) {
	defer c.Close()
	defer release()
	c.SetDeadline(time.Now().Add(g.timeout()))
	if err := c.HandshakeContext(ctx); err != nil {
		g.log().Debug("mesh handshake rejected", "remote", c.RemoteAddr().String(), "err", err)
		return
	}
	cs := c.ConnectionState()
	noteALPN(cs.NegotiatedProtocol)
	peer, err := g.verifyPeer(cs.PeerCertificates, "")
	if err != nil {
		return
	}
	br := bufio.NewReaderSize(c, 4096)
	line, err := readLine(br)
	if err != nil {
		writeReply(c, Reply{Error: "invalid header"})
		return
	}
	h, err := DecodeHeader(line)
	if err != nil {
		writeReply(c, Reply{Error: err.Error()})
		return
	}
	release() // 인증과 헤더 확인이 끝났다: 인증 전 슬롯을 돌려준다
	if h.Op == "ping" {
		writeReply(c, Reply{OK: true, Node: g.NodeID})
		return
	}
	if !g.acquireTunnel(peer) {
		g.log().Warn("mesh: too many tunnels from peer", "peer", peer, "limit", g.maxTunnels())
		writeReply(c, Reply{Error: "too many tunnels from this node"})
		return
	}
	defer g.releaseTunnel(peer)
	up, err := g.dialExport(ctx, peer, h)
	if err != nil {
		g.log().Debug("mesh dial rejected", "peer", peer, "env", h.Env, "service", h.Service, "port", h.Port, "err", err)
		writeReply(c, Reply{Error: err.Error()})
		return
	}
	defer up.Close()
	if err := writeReply(c, Reply{OK: true, Node: g.NodeID}); err != nil {
		return
	}
	c.SetDeadline(time.Time{})
	pipe(c, &bufConn{Conn: c, r: br}, up)
}

// dialExport는 peer가 요청한 서비스가 이 노드에서 export되고 peer가 쓸 수 있는지 확인하고 컨테이너에 연결한다.
func (g *Gateway) dialExport(ctx context.Context, peer string, h Header) (net.Conn, error) {
	g.mu.RLock()
	e, ok := g.exports[h.Env+"/"+h.Service]
	g.mu.RUnlock()
	if !ok {
		return nil, errors.New("service is not exported by this node")
	}
	allowed := false
	for _, p := range e.Peers {
		allowed = allowed || p == peer
	}
	if !allowed {
		return nil, errors.New("node is not part of this environment")
	}
	portOK := false
	for _, p := range e.Ports {
		portOK = portOK || p == h.Port
	}
	if !portOK {
		return nil, errors.New("port is not a private port of this service")
	}
	if g.Resolve == nil {
		return nil, errors.New("no resolver")
	}
	rctx, cancel := context.WithTimeout(ctx, g.timeout())
	defer cancel()
	addrs, err := g.Resolve.Upstreams(rctx, e.EnvID, e.ServiceID)
	if err != nil {
		return nil, fmt.Errorf("lookup: %w", err)
	}
	if len(addrs) == 0 {
		return nil, errors.New("service has no running containers on this node")
	}
	rand.Shuffle(len(addrs), func(i, j int) { addrs[i], addrs[j] = addrs[j], addrs[i] })
	var lastErr error
	d := net.Dialer{Timeout: g.timeout()}
	for _, a := range addrs {
		up, err := d.DialContext(rctx, "tcp", netip.AddrPortFrom(a, uint16(h.Port)).String())
		if err == nil {
			return up, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("upstream: %w", lastErr)
}

// clientConfig는 node(상대)에 연결할 때의 TLS 설정이다. 시스템 CA 대신 aven CA와 노드 ID로 직접 확인한다.
func (g *Gateway) clientConfig(node string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{ALPN, LegacyALPN},
		ServerName: "keel-mesh",
		// 시스템 기본 검증(호스트 이름)은 쓰지 않고 VerifyConnection에서 aven CA + node ID + serial로 확인한다.
		InsecureSkipVerify: true,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return g.Cert()
		},
		VerifyConnection: func(cs tls.ConnectionState) error {
			_, err := g.verifyPeer(cs.PeerCertificates, node)
			return err
		},
	}
}

// dialPeer는 상대 노드 gateway에 mTLS로 연결하고 헤더를 보낸 뒤 답을 읽는다. ok면 그대로 쓸 수 있는 연결을 돌려준다.
func (g *Gateway) dialPeer(ctx context.Context, node string, h Header) (net.Conn, error) {
	g.mu.RLock()
	p, ok := g.peers[node]
	g.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("node %s is not a mesh peer", node)
	}
	dctx, cancel := context.WithTimeout(ctx, g.timeout())
	defer cancel()
	var c *tls.Conn
	if g.Proxy != "" {
		tcp, err := dialConnect(dctx, &net.Dialer{Timeout: g.timeout()}, g.Proxy, p.Address)
		if err != nil {
			return nil, err
		}
		c = tls.Client(tcp, g.clientConfig(node))
		if err := c.HandshakeContext(dctx); err != nil {
			c.Close()
			return nil, err
		}
		noteALPN(c.ConnectionState().NegotiatedProtocol)
	} else {
		d := tls.Dialer{NetDialer: &net.Dialer{Timeout: g.timeout()}, Config: g.clientConfig(node)}
		raw, err := d.DialContext(dctx, "tcp", p.Address)
		if err != nil {
			return nil, err
		}
		c = raw.(*tls.Conn)
		noteALPN(c.ConnectionState().NegotiatedProtocol)
	}
	c.SetDeadline(time.Now().Add(g.timeout()))
	b, _ := json.Marshal(h)
	if _, err := c.Write(append(b, '\n')); err != nil {
		c.Close()
		return nil, err
	}
	br := bufio.NewReaderSize(c, 4096)
	line, err := readLine(br)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("no reply: %w", err)
	}
	var r Reply
	if json.Unmarshal(line, &r) != nil {
		c.Close()
		return nil, errors.New("bad reply")
	}
	if !r.OK {
		c.Close()
		return nil, fmt.Errorf("%w: %s", ErrRefused, r.Error)
	}
	c.SetDeadline(time.Time{})
	return &bufConn{Conn: c, r: br}, nil
}

// Ping은 상대 gateway에 mTLS ping을 보내고 왕복 시간을 잰다.
func (g *Gateway) Ping(ctx context.Context, node string) (time.Duration, error) {
	start := time.Now()
	c, err := g.dialPeer(ctx, node, Header{V: 1, Op: "ping"})
	if err != nil {
		return 0, err
	}
	c.Close()
	return time.Since(start), nil
}

// Apply는 서버가 보낸 설정을 반영한다. nets는 import가 있는 환경의 네트워크 정보, prefer는 이전에 쓰던 로컬 포트
// (env/service/port → 로컬 포트)다. 네트워크 정보가 없는 환경의 import는 건너뛴다. import마다 묶인 로컬 주소를 돌려준다.
func (g *Gateway) Apply(c Config, nets map[string]EnvNet, prefer map[string]int) ([]Binding, error) {
	c.Normalize()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.init()
	if g.closed {
		return nil, errors.New("mesh gateway is closed")
	}
	g.peers, g.serials, g.peerIPs = map[string]Peer{}, map[string]map[string]bool{}, map[netip.Addr]bool{}
	for _, p := range c.Peers {
		g.peers[p.NodeID] = p
		if ap, err := netip.ParseAddrPort(p.Address); err == nil {
			g.peerIPs[ap.Addr().Unmap()] = true
		}
		s := map[string]bool{}
		for _, x := range p.Serials {
			s[strings.ToLower(x)] = true
		}
		g.serials[p.NodeID] = s
		if g.status[p.NodeID] == nil {
			g.status[p.NodeID] = &PeerStatus{NodeID: p.NodeID, Status: "pending"}
		}
	}
	for id := range g.status {
		if _, ok := g.peers[id]; !ok {
			delete(g.status, id)
		}
	}
	g.exports = map[string]Export{}
	for _, e := range c.Exports {
		g.exports[e.key()] = e
	}
	var out []Binding
	var errs []error
	want := map[string]bool{}
	for _, im := range c.Imports {
		n, ok := nets[im.EnvID]
		if !ok || !n.Gateway.IsValid() || len(n.Subnets) == 0 {
			continue
		}
		for _, port := range im.Ports {
			k := fmt.Sprintf("%s/%s/%d", im.EnvID, im.Service, port)
			want[k] = true
			l, err := g.ensureLocal(k, n.Gateway, prefer[k])
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", k, err))
				continue
			}
			l.set(im, port, n.Subnets)
			b := Binding{EnvID: im.EnvID, Service: im.Service, Port: port, Local: l.addr}
			// 네트워크에 IPv6도 있으면 IPv6 gateway에도 로컬 포트를 연다(컨테이너가 AAAA로 접속하는 경우).
			if n.Gateway6.IsValid() && n.Gateway6 != n.Gateway {
				k6 := k + "/6"
				want[k6] = true
				l6, err := g.ensureLocal(k6, n.Gateway6, prefer[k6])
				if err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", k6, err))
				} else {
					l6.set(im, port, n.Subnets)
					b.Local6 = l6.addr
				}
			}
			out = append(out, b)
		}
	}
	for k, l := range g.locals {
		if !want[k] {
			l.ln.Close()
			delete(g.locals, k)
		}
	}
	select {
	case g.wake <- struct{}{}:
	default:
	}
	return out, errors.Join(errs...)
}

// ensureLocal은 키 k의 로컬 listener를 gw에 맞춘다(g.mu를 쥔 채로 부른다).
func (g *Gateway) ensureLocal(k string, gw netip.Addr, prefer int) (*localListener, error) {
	l := g.locals[k]
	if l != nil && l.addr.Addr() != gw {
		l.ln.Close()
		delete(g.locals, k)
		l = nil
	}
	if l != nil {
		return l, nil
	}
	l, err := g.listenLocal(gw, prefer)
	if err != nil {
		return nil, err
	}
	g.locals[k] = l
	go g.serveLocal(l)
	return l, nil
}

func (g *Gateway) listenLocal(gw netip.Addr, prefer int) (*localListener, error) {
	var ln net.Listener
	var err error
	if prefer > 0 {
		ln, err = net.Listen("tcp", netip.AddrPortFrom(gw, uint16(prefer)).String())
	}
	if ln == nil {
		ln, err = net.Listen("tcp", netip.AddrPortFrom(gw, 0).String())
	}
	if err != nil {
		return nil, err
	}
	ap, _ := netip.ParseAddrPort(ln.Addr().String())
	return &localListener{ln: ln, addr: netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())}, nil
}

// localListener는 import 포트 하나다: 환경 네트워크의 gateway IP에서 forwarder 컨테이너의 연결을 받는다.
type localListener struct {
	ln   net.Listener
	addr netip.AddrPort

	mu      sync.RWMutex
	im      Import
	port    int
	subnets []netip.Prefix
}

func (l *localListener) set(im Import, port int, subnets []netip.Prefix) {
	l.mu.Lock()
	l.im, l.port, l.subnets = im, port, append([]netip.Prefix(nil), subnets...)
	l.mu.Unlock()
}

// allowed는 연결의 출발 주소가 이 import의 환경 네트워크 서브넷인지다(다른 환경의 컨테이너는 거부).
func (l *localListener) allowed(remote net.Addr) bool {
	ap, err := netip.ParseAddrPort(remote.String())
	if err != nil {
		return false
	}
	a := ap.Addr().Unmap()
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, p := range l.subnets {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func (g *Gateway) serveLocal(l *localListener) {
	for {
		c, err := l.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		go g.forward(l, c)
	}
}

func (g *Gateway) forward(l *localListener, c net.Conn) {
	defer c.Close()
	if !l.allowed(c.RemoteAddr()) {
		g.log().Warn("mesh: rejected connection from outside the environment network", "remote", c.RemoteAddr().String(), "local", l.addr.String())
		return
	}
	l.mu.RLock()
	im, port := l.im, l.port
	l.mu.RUnlock()
	h := Header{V: 1, Op: "dial", Env: im.EnvID, Service: im.Service, Port: port}
	var up net.Conn
	var errs []string
	for _, n := range g.Order(im.Candidates()) {
		u, err := g.dialPeer(context.Background(), n, h)
		if err == nil {
			up = u
			break
		}
		errs = append(errs, n+": "+err.Error())
		if !errors.Is(err, ErrRefused) {
			g.markUnreachable(n, err) // 다음 연결은 다른 노드부터 시도한다(상태 확인이 다시 ok로 바꾼다)
		}
	}
	if up == nil {
		g.log().Warn("mesh: forward failed", "env", im.EnvID, "service", im.Service, "port", port, "nodes", im.Candidates(), "err", strings.Join(errs, "; "))
		return
	}
	defer up.Close()
	pipe(c, c, up)
}

// Order는 failover 후보를 시도할 순서로 늘어놓는다: 상태가 ok인 노드(RTT 낮은 순), 아직 확인하지 않은 노드, 그리고
// 닿지 않는 노드는 다른 후보가 하나도 없을 때만 넣는다. 같은 조건이면 원래 순서(주 노드 먼저)를 지킨다.
func (g *Gateway) Order(nodes []string) []string {
	type cand struct {
		id   string
		rank int
		rtt  float64
	}
	g.mu.RLock()
	cs := make([]cand, 0, len(nodes))
	for _, id := range nodes {
		c := cand{id: id, rank: 1}
		if st := g.status[id]; st != nil {
			switch st.Status {
			case "ok":
				c.rank = 0
				if st.RTTMs != nil {
					c.rtt = *st.RTTMs
				}
			case "unreachable":
				c.rank = 2
			}
		}
		cs = append(cs, c)
	}
	g.mu.RUnlock()
	sort.SliceStable(cs, func(a, b int) bool {
		if cs[a].rank != cs[b].rank {
			return cs[a].rank < cs[b].rank
		}
		return cs[a].rank == 0 && cs[a].rtt < cs[b].rtt
	})
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		if c.rank == 2 && len(out) > 0 {
			break
		}
		out = append(out, c.id)
	}
	return out
}

// markUnreachable은 연결에 실패한 peer를 바로 unreachable로 표시한다.
func (g *Gateway) markUnreachable(node string, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if st := g.status[node]; st != nil {
		st.Status, st.Error = "unreachable", err.Error()
	}
}

// bufConn은 헤더를 읽느라 bufio에 들어간 바이트를 먼저 읽게 한다.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

func (b *bufConn) CloseWrite() error {
	if cw, ok := b.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

// pipe는 양쪽으로 바이트를 넘긴다. 한쪽이 끝나면 다른 쪽에 write 반쪽 닫기를 보낸다.
// a는 닫기 대상, ar은 a에서 읽을 reader(bufConn일 수 있다).
func pipe(a net.Conn, ar io.Reader, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst net.Conn, src io.Reader) {
		io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
		done <- struct{}{}
	}
	go cp(b, ar)
	go cp(a, b)
	<-done
	<-done
}

// probeLoop는 Interval마다 모든 peer에 mTLS ping을 보낸다. 설정이 바뀌면 바로 한 번 더 한다.
func (g *Gateway) probeLoop(ctx context.Context) {
	every := g.Interval
	if every <= 0 {
		every = 30 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		g.ProbeAll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-g.wake:
		}
	}
}

// ProbeAll은 모든 peer를 한 번씩 확인한다.
func (g *Gateway) ProbeAll(ctx context.Context) {
	g.mu.RLock()
	ids := make([]string, 0, len(g.peers))
	for id := range g.peers {
		ids = append(ids, id)
	}
	g.mu.RUnlock()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			rtt, err := g.Ping(ctx, id)
			g.mu.Lock()
			defer g.mu.Unlock()
			st := g.status[id]
			if st == nil {
				return
			}
			if err != nil {
				st.Status, st.Error = "unreachable", err.Error()
				return
			}
			now := time.Now().UTC()
			ms := float64(rtt.Microseconds()) / 1000
			st.Status, st.Error, st.LastOKAt, st.RTTMs = "ok", "", &now, &ms
		}(id)
	}
	wg.Wait()
}

// Status는 현재 상태다.
func (g *Gateway) Status() Status {
	g.mu.RLock()
	defer g.mu.RUnlock()
	st := Status{Enabled: true, Peers: []PeerStatus{}, Error: g.lnErr}
	if g.ln != nil {
		a := g.ln.Addr().String()
		st.Listen = &a
	} else if g.Listen != "" {
		a := g.Listen
		st.Listen = &a
	}
	for _, p := range g.status {
		cp := *p
		st.Peers = append(st.Peers, cp)
	}
	sortPeers(st.Peers)
	return st
}

func sortPeers(ps []PeerStatus) {
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0 && ps[j].NodeID < ps[j-1].NodeID; j-- {
			ps[j], ps[j-1] = ps[j-1], ps[j]
		}
	}
}
