package mesh

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/agentpki"
)

// startWith는 start와 같지만 한도를 먼저 정한다(init 전에).
func startWith(t *testing.T, ca *agentpki.CA, id ident, res Resolver, tune func(*Gateway)) *Gateway {
	t.Helper()
	g := &Gateway{NodeID: id.id, Roots: ca.Pool(), Cert: func() (*tls.Certificate, error) { return id.cert, nil }, Resolve: res, Timeout: 2 * time.Second}
	tune(g)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	g.mu.Lock()
	g.init()
	g.ln = ln
	g.mu.Unlock()
	go g.serve(ctx, ln)
	t.Cleanup(func() { cancel(); g.Close() })
	return g
}

// admitted는 TCP 연결 하나가 gateway에 받아들여졌는지(handshake를 기다리는지) 거부됐는지(바로 닫힘) 본다.
func admitted(t *testing.T, addr string) (net.Conn, bool) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	_, err = c.Read(make([]byte, 1))
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		c.SetReadDeadline(time.Time{})
		return c, true
	}
	c.Close()
	return nil, false
}

func TestHandshakeConcurrencyCap(t *testing.T) {
	ca, _ := agentpki.NewCA()
	g := startWith(t, ca, issue(t, ca, nodB), nil, func(g *Gateway) { g.MaxHandshakes = 2; g.HandshakeBurst = 100 })
	var held []net.Conn
	for i := 0; i < 2; i++ {
		c, ok := admitted(t, g.Addr())
		if !ok {
			t.Fatalf("connection %d rejected under the cap", i)
		}
		held = append(held, c)
	}
	if c, ok := admitted(t, g.Addr()); ok {
		c.Close()
		t.Fatal("third unauthenticated connection admitted over the cap")
	}
	for _, c := range held {
		c.Close()
	}
	// 끊긴 연결의 슬롯은 돌아온다.
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, ok := admitted(t, g.Addr())
		if ok {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slots were not released")
		}
	}
}

func TestPerIPRateLimitExemptsPeers(t *testing.T) {
	ca, _ := agentpki.NewCA()
	g := startWith(t, ca, issue(t, ca, nodB), nil, func(g *Gateway) { g.HandshakeRate = 0.001; g.HandshakeBurst = 2 })
	for i := 0; i < 2; i++ {
		c, ok := admitted(t, g.Addr())
		if !ok {
			t.Fatalf("connection %d rejected within the burst", i)
		}
		c.Close()
	}
	if c, ok := admitted(t, g.Addr()); ok {
		c.Close()
		t.Fatal("connection over the per-IP rate admitted")
	}
	// mesh peer의 주소(여기서는 127.0.0.1)는 IP 한도에서 빠진다.
	g.Apply(Config{Peers: []Peer{{NodeID: nodA, Address: "127.0.0.1:7443", Serials: []string{"ab"}}}}, nil, nil)
	if c, ok := admitted(t, g.Addr()); !ok {
		t.Fatal("peer address rate limited")
	} else {
		c.Close()
	}
	// 상한: 기록이 가득하면 새 IP는 받지 않는다(쉬는 IP는 먼저 지운다).
	now := time.Now()
	g.limMu.Lock()
	for i := 0; i < maxRateEntries; i++ {
		a := netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)})
		g.ipLim[a] = &bucket{tokens: 0, last: now}
	}
	g.limMu.Unlock()
	if g.allowIP(netip.MustParseAddr("192.168.1.1"), now) {
		t.Fatal("new IP admitted with a full rate table")
	}
}

// dialTunnel은 a의 인증서로 g에 dial 헤더를 보내고 답과 연결을 돌려준다.
func dialTunnel(t *testing.T, g *Gateway, id ident, h Header) (Reply, net.Conn) {
	t.Helper()
	c, err := tls.Dial("tcp", g.Addr(), &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{ALPN}, InsecureSkipVerify: true, Certificates: []tls.Certificate{*id.cert}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(h)
	c.Write(append(b, '\n'))
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		c.Close()
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Time{})
	var r Reply
	json.Unmarshal(line, &r)
	return r, c
}

func TestTunnelCapPerPeer(t *testing.T) {
	up := echoServer(t)
	_, gb, a, _, _ := pair(t, int(up.Port()))
	gb.MaxTunnelsPerPeer = 1
	h := Header{V: 1, Op: "dial", Env: envA, Service: "api", Port: int(up.Port())}
	r1, c1 := dialTunnel(t, gb, a, h)
	if !r1.OK {
		t.Fatalf("first tunnel: %+v", r1)
	}
	r2, c2 := dialTunnel(t, gb, a, h)
	c2.Close()
	if r2.OK || !strings.Contains(r2.Error, "too many tunnels") {
		t.Fatalf("second tunnel over the cap: %+v", r2)
	}
	// ping은 터널이 아니다
	if r, err := rawDial(t, gb, a, nil, Header{V: 1, Op: "ping"}); err != nil || !r.OK {
		t.Fatalf("ping under tunnel cap: %+v %v", r, err)
	}
	c1.Close()
	deadline := time.Now().Add(3 * time.Second)
	for gb.Tunnels(nodA) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("tunnel count not released")
		}
		time.Sleep(20 * time.Millisecond)
	}
	r3, c3 := dialTunnel(t, gb, a, h)
	c3.Close()
	if !r3.OK {
		t.Fatalf("tunnel after release: %+v", r3)
	}
}

func TestImportFailoverSkipsUnreachable(t *testing.T) {
	up := echoServer(t)
	port := int(up.Port())
	ga, gb, a, b, ca := pair(t, port)
	c := issue(t, ca, "nod_ccccccccccccc")
	gc := start(t, ca, c, staticResolver{envA + "/" + svcA: {netip.MustParseAddr("127.0.0.1")}})
	gc.Apply(Config{Peers: []Peer{peerOf(ga, a)}, Exports: []Export{{EnvID: envA, ServiceID: svcA, Service: "api", Ports: []int{port}, Peers: []string{nodA}}}}, nil, nil)
	im := Import{EnvID: envA, EnvName: "production", Service: "api", Ports: []int{port}, Node: nodB, Nodes: []string{nodB, c.id}}
	cfg := Config{Peers: []Peer{peerOf(gb, b), peerOf(gc, c)}, Imports: []Import{im}}
	binds, err := ga.Apply(cfg, map[string]EnvNet{envA: loopNet}, nil)
	if err != nil || len(binds) != 1 {
		t.Fatal(binds, err)
	}
	echo := func() error {
		conn, err := net.Dial("tcp", binds[0].Local.String())
		if err != nil {
			return err
		}
		defer conn.Close()
		conn.Write([]byte("hi"))
		conn.(*net.TCPConn).CloseWrite()
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		got, err := io.ReadAll(conn)
		if err != nil || string(got) != "hi" {
			return errors.New("echo failed: " + string(got))
		}
		return nil
	}
	if err := echo(); err != nil {
		t.Fatal(err)
	}
	// 주 노드 B가 죽으면 C로 넘어간다. B는 바로 unreachable로 표시되어 다음 연결은 C부터 시도한다.
	gb.Close()
	if err := echo(); err != nil {
		t.Fatalf("failover: %v", err)
	}
	if o := ga.Order(im.Candidates()); len(o) != 1 || o[0] != c.id {
		t.Fatalf("order after failure: %v", o)
	}
	if err := echo(); err != nil {
		t.Fatalf("second connection: %v", err)
	}
}

func TestOrderPrefersLowRTT(t *testing.T) {
	g := &Gateway{}
	g.init()
	ms := func(v float64) *float64 { return &v }
	g.status = map[string]*PeerStatus{
		"nod_a": {Status: "ok", RTTMs: ms(30)}, "nod_b": {Status: "ok", RTTMs: ms(5)},
		"nod_c": {Status: "pending"}, "nod_d": {Status: "unreachable"},
	}
	if o := g.Order([]string{"nod_d", "nod_c", "nod_a", "nod_b"}); strings.Join(o, ",") != "nod_b,nod_a,nod_c" {
		t.Fatalf("order: %v", o)
	}
	// 모두 닿지 않으면 그래도 시도한다(상태가 낡았을 수 있다).
	if o := g.Order([]string{"nod_d"}); len(o) != 1 {
		t.Fatalf("all unreachable: %v", o)
	}
}

func TestIPv6LocalListener(t *testing.T) {
	if ln, err := net.Listen("tcp", "[::1]:0"); err != nil {
		t.Skip("no IPv6 loopback")
	} else {
		ln.Close()
	}
	up := echoServer(t)
	ga, gb, _, b, _ := pair(t, int(up.Port()))
	dual := EnvNet{Subnets: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")},
		Gateway: netip.MustParseAddr("127.0.0.1"), Gateway6: netip.MustParseAddr("::1")}
	binds, err := ga.Apply(Config{Peers: []Peer{peerOf(gb, b)}, Imports: []Import{{EnvID: envA, EnvName: "production", Service: "api", Ports: []int{int(up.Port())}, Node: nodB}}},
		map[string]EnvNet{envA: dual}, nil)
	if err != nil || len(binds) != 1 || !binds[0].Local6.IsValid() || !binds[0].Local6.Addr().Is6() {
		t.Fatalf("binds: %+v %v", binds, err)
	}
	c, err := net.Dial("tcp", binds[0].Local6.String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("v6"))
	c.(*net.TCPConn).CloseWrite()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if got, _ := io.ReadAll(c); string(got) != "v6" {
		t.Fatalf("echo over IPv6 listener: %q", got)
	}
	// IPv6가 없어지면 그 listener도 닫는다.
	ga.Apply(Config{Peers: []Peer{peerOf(gb, b)}, Imports: []Import{{EnvID: envA, EnvName: "production", Service: "api", Ports: []int{int(up.Port())}, Node: nodB}}},
		map[string]EnvNet{envA: loopNet}, nil)
	if c2, err := net.DialTimeout("tcp", binds[0].Local6.String(), time.Second); err == nil {
		c2.Close()
		t.Fatal("IPv6 listener still open")
	}
}

func TestForwarderSpecDualStack(t *testing.T) {
	im := Import{EnvID: envA, EnvName: "production", Service: "db", Ports: []int{5432}, Node: nodB}
	gw, gw6 := netip.MustParseAddr("172.18.0.1"), netip.MustParseAddr("fd00:18::1")
	s := ForwarderSpecDual(im, map[int]netip.AddrPort{5432: netip.AddrPortFrom(gw, 40001)}, map[int]netip.AddrPort{5432: netip.AddrPortFrom(gw6, 40002)})
	if !strings.Contains(s.Cmd[1], "TCP-LISTEN:5432,fork,reuseaddr TCP4:172.18.0.1:40001") ||
		!strings.Contains(s.Cmd[1], "TCP6-LISTEN:5432,fork,reuseaddr,ipv6only=1 TCP6:[fd00:18::1]:40002") {
		t.Fatalf("cmd: %v", s.Cmd)
	}
	sig := s.Labels[labelMesh]
	l4, a4, ok4 := parseSignature(sig)
	l6, a6, ok6 := parseSignature6(sig)
	if !ok4 || !ok6 || a4 != gw || a6 != gw6 || l4[5432] != 40001 || l6[5432] != 40002 {
		t.Fatalf("signature %q", sig)
	}
	if _, _, ok := parseSignature6(ForwarderSpec(im, map[int]netip.AddrPort{5432: netip.AddrPortFrom(gw, 1)}).Labels[labelMesh]); ok {
		t.Fatal("IPv4-only signature has an IPv6 part")
	}
}

func TestValidateImportNodes(t *testing.T) {
	base := Config{
		Peers:   []Peer{{NodeID: nodB, Address: "100.64.1.2:7443", Serials: []string{"ab"}}, {NodeID: "nod_ccccccccccccc", Address: "100.64.1.3:7443", Serials: []string{"cd"}}},
		Imports: []Import{{EnvID: envA, EnvName: "production", Service: "db", Ports: []int{5432}, Node: nodB, Nodes: []string{nodB, "nod_ccccccccccccc"}}},
	}
	if err := Validate(base, nodA); err != nil {
		t.Fatal(err)
	}
	for name, nodes := range map[string][]string{
		"unknown node":      {nodB, "nod_zzzzzzzzzzzzz"},
		"duplicate":         {nodB, nodB},
		"primary not in it": {"nod_ccccccccccccc"},
	} {
		c := base
		c.Imports = []Import{base.Imports[0]}
		c.Imports[0].Nodes = nodes
		if err := Validate(c, nodA); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// 옛 agent가 엄격하게 읽는 모양: nodes가 비면 JSON에 나오지 않는다.
	b, _ := json.Marshal(Import{EnvID: envA, Service: "db", Node: nodB})
	if strings.Contains(string(b), "nodes") {
		t.Fatalf("empty nodes serialized: %s", b)
	}
}
