package mesh

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/agentpki"
	"github.com/turin-dev/aven/internal/node"
)

const (
	envA = "env_aaaaaaaaaaaaa"
	envB = "env_bbbbbbbbbbbbb"
	svcA = "svc_aaaaaaaaaaaaa"
	svcB = "svc_bbbbbbbbbbbbb"
	nodA = "nod_aaaaaaaaaaaaa"
	nodB = "nod_bbbbbbbbbbbbb"
)

type ident struct {
	id     string
	cert   *tls.Certificate
	serial string
}

func issue(t *testing.T, ca *agentpki.CA, id string) ident {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	is, err := ca.Issue(id, pub)
	if err != nil {
		t.Fatal(err)
	}
	c, err := agentpki.TLSCertificate(is.CertPEM, priv)
	if err != nil {
		t.Fatal(err)
	}
	return ident{id: id, cert: c, serial: is.Serial}
}

type staticResolver map[string][]netip.Addr

func (r staticResolver) Upstreams(_ context.Context, env, svc string) ([]netip.Addr, error) {
	return r[env+"/"+svc], nil
}

// start는 loopback에서 gateway를 띄운다(Validate는 사설 주소만 받으므로 테스트는 Apply를 바로 쓴다).
func start(t *testing.T, ca *agentpki.CA, id ident, res Resolver) *Gateway {
	t.Helper()
	g := &Gateway{NodeID: id.id, Roots: ca.Pool(), Cert: func() (*tls.Certificate, error) { return id.cert, nil }, Resolve: res, Timeout: 2 * time.Second}
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

func peerOf(g *Gateway, id ident) Peer {
	return Peer{NodeID: id.id, Address: g.Addr(), Serials: []string{id.serial}}
}

// echoServer는 받은 바이트를 그대로 돌려주는 가짜 upstream이다.
func echoServer(t *testing.T) netip.AddrPort {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	return netip.MustParseAddrPort(ln.Addr().String())
}

var loopNet = EnvNet{Subnets: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, Gateway: netip.MustParseAddr("127.0.0.1")}

// pair는 A(소비 노드)와 B(서비스가 도는 노드)를 서로 peer로 만들고 B가 envA/api를 export하게 한다.
func pair(t *testing.T, port int) (ga, gb *Gateway, a, b ident, ca *agentpki.CA) {
	ca, _ = agentpki.NewCA()
	a, b = issue(t, ca, nodA), issue(t, ca, nodB)
	up := netip.MustParseAddr("127.0.0.1")
	ga = start(t, ca, a, nil)
	gb = start(t, ca, b, staticResolver{envA + "/" + svcA: {up}, envB + "/" + svcB: {up}})
	if _, err := gb.Apply(Config{Peers: []Peer{peerOf(ga, a)}, Exports: []Export{
		{EnvID: envA, ServiceID: svcA, Service: "api", Ports: []int{port}, Peers: []string{nodA}},
		{EnvID: envB, ServiceID: svcB, Service: "db", Ports: []int{port}, Peers: []string{}}, // env B는 A에게 열려 있지 않다
	}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	return
}

func TestEndToEndForwarding(t *testing.T) {
	up := echoServer(t)
	ga, gb, _, b, _ := pair(t, int(up.Port()))
	binds, err := ga.Apply(Config{Peers: []Peer{peerOf(gb, b)}, Imports: []Import{{EnvID: envA, EnvName: "production", Service: "api", Ports: []int{int(up.Port())}, Node: nodB}}},
		map[string]EnvNet{envA: loopNet}, nil)
	if err != nil || len(binds) != 1 {
		t.Fatalf("apply: %v %v", binds, err)
	}
	c, err := net.Dial("tcp", binds[0].Local.String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	msg := strings.Repeat("keel-mesh ", 5000) // 여러 TLS 레코드
	go func() { c.Write([]byte(msg)); c.(*net.TCPConn).CloseWrite() }()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(c)
	if err != nil || string(got) != msg {
		t.Fatalf("echo: %d bytes, err %v", len(got), err)
	}
	// 상태 확인: A → B ping
	ga.ProbeAll(context.Background())
	st := ga.Status()
	if len(st.Peers) != 1 || st.Peers[0].Status != "ok" || st.Peers[0].RTTMs == nil || st.Peers[0].LastOKAt == nil {
		t.Fatalf("status: %+v", st)
	}
	// 같은 설정을 다시 적용하면 같은 로컬 포트를 쓴다.
	again, _ := ga.Apply(Config{Peers: []Peer{peerOf(gb, b)}, Imports: []Import{{EnvID: envA, EnvName: "production", Service: "api", Ports: []int{int(up.Port())}, Node: nodB}}},
		map[string]EnvNet{envA: loopNet}, nil)
	if len(again) != 1 || again[0].Local != binds[0].Local {
		t.Fatalf("rebind: %v vs %v", again, binds)
	}
	// import를 지우면 로컬 포트도 닫힌다.
	ga.Apply(Config{Peers: []Peer{peerOf(gb, b)}}, nil, nil)
	if c2, err := net.DialTimeout("tcp", binds[0].Local.String(), time.Second); err == nil {
		c2.Close()
		t.Fatal("local port still open after import removal")
	}
}

func rawDial(t *testing.T, g *Gateway, id ident, roots *Gateway, h any) (Reply, error) {
	t.Helper()
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{ALPN}, InsecureSkipVerify: true,
		Certificates: []tls.Certificate{*id.cert}}
	c, err := tls.Dial("tcp", g.Addr(), cfg)
	if err != nil {
		return Reply{}, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	var b []byte
	if s, ok := h.(string); ok {
		b = []byte(s)
	} else {
		b, _ = json.Marshal(h)
	}
	if _, err := c.Write(append(b, '\n')); err != nil {
		return Reply{}, err
	}
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		return Reply{}, err
	}
	var r Reply
	err = json.Unmarshal(line, &r)
	return r, err
}

func TestHandshakeRejectsOtherCAAndRevoked(t *testing.T) {
	up := echoServer(t)
	ga, gb, a, b, ca := pair(t, int(up.Port()))
	ga.Apply(Config{Peers: []Peer{peerOf(gb, b)}}, nil, nil)
	if _, err := ga.Ping(context.Background(), nodB); err != nil {
		t.Fatalf("valid ping: %v", err)
	}
	// 다른 CA가 서명한 같은 node ID 인증서
	other, _ := agentpki.NewCA()
	fake := issue(t, other, nodA)
	if r, err := rawDial(t, gb, fake, nil, Header{V: 1, Op: "ping"}); err == nil && r.OK {
		t.Fatal("certificate from another CA accepted")
	}
	// aven CA 인증서지만 peer 목록에 없는 노드
	stranger := issue(t, ca, "nod_ccccccccccccc")
	if r, err := rawDial(t, gb, stranger, nil, Header{V: 1, Op: "ping"}); err == nil && r.OK {
		t.Fatal("non-peer node accepted")
	}
	// 같은 노드의 새 인증서(serial이 목록에 없음 = 폐기/모르는 인증서)
	rotated := issue(t, ca, nodA)
	if r, err := rawDial(t, gb, rotated, nil, Header{V: 1, Op: "ping"}); err == nil && r.OK {
		t.Fatal("unknown serial accepted")
	}
	// 폐기: 서버가 A의 serial을 목록에서 빼면 A의 연결은 거부된다.
	gb.Apply(Config{Peers: []Peer{{NodeID: nodA, Address: ga.Addr(), Serials: []string{"abc"}}}}, nil, nil)
	if _, err := ga.Ping(context.Background(), nodB); err == nil {
		t.Fatal("revoked node could still ping")
	}
	if r, err := rawDial(t, gb, a, nil, Header{V: 1, Op: "ping"}); err == nil && r.OK {
		t.Fatal("revoked certificate accepted")
	}
	// 연결하는 쪽도 상대의 node ID를 확인한다: B의 주소에 C가 앉아 있으면 거부.
	c := issue(t, ca, "nod_ccccccccccccc")
	gc := start(t, ca, c, nil)
	gc.Apply(Config{Peers: []Peer{peerOf(ga, a)}}, nil, nil)
	ga.Apply(Config{Peers: []Peer{{NodeID: nodB, Address: gc.Addr(), Serials: []string{b.serial, c.serial}}}}, nil, nil)
	if _, err := ga.Ping(context.Background(), nodB); err == nil {
		t.Fatal("impersonating gateway accepted")
	}
	// 클라이언트 인증서 없는 연결
	conn, err := tls.Dial("tcp", gb.Addr(), &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true, NextProtos: []string{ALPN}})
	if err == nil {
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		conn.Write([]byte(`{"v":1,"op":"ping"}` + "\n"))
		if _, rerr := bufio.NewReader(conn).ReadBytes('\n'); rerr == nil {
			t.Fatal("connection without client certificate answered")
		}
		conn.Close()
	}
}

func TestHeaderValidationAndEnvIsolation(t *testing.T) {
	up := echoServer(t)
	_, gb, a, _, _ := pair(t, int(up.Port()))
	port := int(up.Port())
	cases := map[string]any{
		"unknown field":      `{"v":1,"op":"dial","env":"` + envA + `","service":"api","port":1,"host":"10.0.0.1"}`,
		"bad version":        Header{V: 2, Op: "ping"},
		"unknown op":         Header{V: 1, Op: "exec"},
		"ping with target":   Header{V: 1, Op: "ping", Env: envA},
		"bad env":            Header{V: 1, Op: "dial", Env: "../etc", Service: "api", Port: port},
		"bad service":        Header{V: 1, Op: "dial", Env: envA, Service: "API;rm", Port: port},
		"port out of range":  Header{V: 1, Op: "dial", Env: envA, Service: "api", Port: 70000},
		"not a private port": Header{V: 1, Op: "dial", Env: envA, Service: "api", Port: port + 1},
		"unknown service":    Header{V: 1, Op: "dial", Env: envA, Service: "db", Port: port},
		// env B의 서비스는 A에게 export되지 않았다(A는 env B의 노드가 아니다).
		"other environment": Header{V: 1, Op: "dial", Env: envB, Service: "db", Port: port},
		"too long":          `{"v":1,"op":"ping","x":"` + strings.Repeat("a", 2000) + `"}`,
		"trailing data":     `{"v":1,"op":"ping"}{"v":1}`,
	}
	for name, h := range cases {
		r, err := rawDial(t, gb, a, nil, h)
		if err == nil && r.OK {
			t.Errorf("%s: accepted", name)
		}
	}
	if r, err := rawDial(t, gb, a, nil, Header{V: 1, Op: "dial", Env: envA, Service: "api", Port: port}); err != nil || !r.OK || r.Node != nodB {
		t.Fatalf("valid dial: %+v %v", r, err)
	}
}

func TestLocalListenerRejectsOtherNetworks(t *testing.T) {
	up := echoServer(t)
	ga, gb, _, b, _ := pair(t, int(up.Port()))
	// env A의 네트워크 서브넷이 10.x라서 127.0.0.1에서 온 연결(다른 네트워크의 컨테이너 흉내)은 거부된다.
	other := EnvNet{Subnets: []netip.Prefix{netip.MustParsePrefix("10.200.0.0/16")}, Gateway: netip.MustParseAddr("127.0.0.1")}
	binds, err := ga.Apply(Config{Peers: []Peer{peerOf(gb, b)}, Imports: []Import{{EnvID: envA, EnvName: "production", Service: "api", Ports: []int{int(up.Port())}, Node: nodB}}},
		map[string]EnvNet{envA: other}, nil)
	if err != nil || len(binds) != 1 {
		t.Fatal(binds, err)
	}
	c, err := net.Dial("tcp", binds[0].Local.String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("hello"))
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if n, _ := c.Read(make([]byte, 16)); n != 0 {
		t.Fatal("connection from outside the environment network was forwarded")
	}
}

func TestValidate(t *testing.T) {
	ok := Config{
		Peers:   []Peer{{NodeID: nodB, Address: "100.64.1.2:7443", Serials: []string{"ab12"}}, {NodeID: "node_local", Address: "[fd7a:115c:a1e0::1]:7443", Serials: []string{"ff"}}},
		Exports: []Export{{EnvID: envA, ServiceID: svcA, Service: "api", Ports: []int{8080, 5432}, Peers: []string{nodB}}},
		Imports: []Import{{EnvID: envA, EnvName: "production", Service: "db", Ports: []int{5432}, Node: "node_local"}},
	}
	if err := Validate(ok, nodA); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(c *Config){
		"public peer address": func(c *Config) { c.Peers[0].Address = "8.8.8.8:7443" },
		"loopback peer":       func(c *Config) { c.Peers[0].Address = "127.0.0.1:7443" },
		"hostname peer":       func(c *Config) { c.Peers[0].Address = "evil.example:7443" },
		"low port":            func(c *Config) { c.Peers[0].Address = "10.0.0.2:22" },
		"self as peer":        func(c *Config) { c.Peers[0].NodeID = nodA },
		"no serials":          func(c *Config) { c.Peers[0].Serials = nil },
		"bad serial":          func(c *Config) { c.Peers[0].Serials = []string{"zz"} },
		"export unknown peer": func(c *Config) { c.Exports[0].Peers = []string{"nod_zzzzzzzzzzzzz"} },
		"export no peers":     func(c *Config) { c.Exports[0].Peers = nil },
		"export bad port":     func(c *Config) { c.Exports[0].Ports = []int{0} },
		"export dup port":     func(c *Config) { c.Exports[0].Ports = []int{80, 80} },
		"export no ports":     func(c *Config) { c.Exports[0].Ports = nil },
		"export bad env":      func(c *Config) { c.Exports[0].EnvID = "keel-env_x" },
		"import unknown node": func(c *Config) { c.Imports[0].Node = "nod_zzzzzzzzzzzzz" },
		"import bad service":  func(c *Config) { c.Imports[0].Service = "a b" },
		"import bad env name": func(c *Config) { c.Imports[0].EnvName = "prod;rm" },
		"duplicate import":    func(c *Config) { c.Imports = append(c.Imports, c.Imports[0]) },
	}
	for name, mut := range bad {
		c := ok
		c.Peers = append([]Peer(nil), ok.Peers...)
		c.Peers[0].Serials = append([]string(nil), ok.Peers[0].Serials...)
		c.Exports = append([]Export(nil), ok.Exports...)
		c.Imports = append([]Import(nil), ok.Imports...)
		mut(&c)
		if err := Validate(c, nodA); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestForwarderSpecAndSignature(t *testing.T) {
	im := Import{EnvID: envA, EnvName: "production", Service: "db", Ports: []int{5432, 6379}, Node: nodB}
	gw := netip.MustParseAddr("172.18.0.1")
	s := ForwarderSpec(im, map[int]netip.AddrPort{5432: netip.AddrPortFrom(gw, 40001), 6379: netip.AddrPortFrom(gw, 40002)})
	if len(s.Publish) != 0 || len(s.Mounts) != 0 || s.Image != node.TCPForwardImage || s.Network != "aven-"+envA {
		t.Fatalf("spec: %+v", s)
	}
	if s.Labels[node.LabelRole] != node.RoleMeshProxy || s.Aliases[0] != "db" || s.Aliases[1] != "db.production.keel.internal" || s.Aliases[2] != "db.production.aven.internal" {
		t.Fatalf("labels/aliases: %+v %v", s.Labels, s.Aliases)
	}
	if !strings.Contains(s.Cmd[1], "TCP-LISTEN:5432,fork,reuseaddr TCP4:172.18.0.1:40001") || !strings.HasSuffix(s.Cmd[1], "wait") {
		t.Fatalf("cmd: %v", s.Cmd)
	}
	local, a, ok := parseSignature(s.Labels[labelMesh])
	if !ok || a != gw || local[5432] != 40001 || local[6379] != 40002 {
		t.Fatalf("signature: %s", s.Labels[labelMesh])
	}
}

// fakeDocker는 Ensure가 쓰는 docker 기능만 흉내 낸다.
type fakeDocker struct {
	mu      sync.Mutex
	labels  map[string]map[string]string
	ran     []string
	removed []string
	nets    map[string]*node.NetworkInfo
}

func (f *fakeDocker) NetworkInfo(_ context.Context, name string) (*node.NetworkInfo, error) {
	if n := f.nets[name]; n != nil {
		return n, nil
	}
	return nil, errors.New("no such network")
}
func (f *fakeDocker) ListManaged(context.Context) (map[string]map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]map[string]string{}
	for k, v := range f.labels {
		out[k] = v
	}
	return out, nil
}
func (f *fakeDocker) InspectMany(_ context.Context, names []string) (map[string]*node.ContainerState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]*node.ContainerState{}
	for _, n := range names {
		if l, ok := f.labels[n]; ok {
			out[n] = &node.ContainerState{Name: n, Running: true, Labels: l}
		}
	}
	return out, nil
}
func (f *fakeDocker) Run(_ context.Context, s node.RunSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ran = append(f.ran, s.Name)
	f.labels[s.Name] = s.Labels
	return nil
}
func (f *fakeDocker) Remove(_ context.Context, name string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.labels[name]; ok {
		f.removed = append(f.removed, name)
		delete(f.labels, name)
	}
	return nil
}

func TestEnsureForwarders(t *testing.T) {
	up := echoServer(t)
	ga, gb, a, b, ca := pair(t, int(up.Port()))
	stale := ContainerName(envB, "old")
	f := &fakeDocker{labels: map[string]map[string]string{
		stale:      {node.LabelManaged: "true", node.LabelRole: node.RoleMeshProxy},
		"keel-app": {node.LabelManaged: "true", node.LabelRole: node.RoleApp},
	}, nets: map[string]*node.NetworkInfo{"keel-" + envA: {Subnets: loopNet.Subnets, Gateways: []netip.Addr{loopNet.Gateway}}}}
	cfg := Config{Peers: []Peer{peerOf(gb, b)}, Imports: []Import{{EnvID: envA, EnvName: "production", Service: "api", Ports: []int{int(up.Port())}, Node: nodB}}}
	if _, err := Ensure(context.Background(), ga, f, cfg); err != nil {
		t.Fatal(err)
	}
	name := ContainerName(envA, "api")
	if len(f.ran) != 1 || f.ran[0] != name || len(f.removed) != 1 || f.removed[0] != stale {
		t.Fatalf("ran=%v removed=%v", f.ran, f.removed)
	}
	if _, ok := f.labels["keel-app"]; !ok {
		t.Fatal("non-mesh container removed")
	}
	// 두 번째 적용은 아무것도 바꾸지 않는다(같은 로컬 포트, 같은 서명).
	if _, err := Ensure(context.Background(), ga, f, cfg); err != nil || len(f.ran) != 1 {
		t.Fatalf("not idempotent: %v ran=%v", err, f.ran)
	}
	// 재시작(새 gateway)해도 이전 서명의 로컬 포트를 다시 쓰므로 컨테이너를 다시 만들지 않는다.
	ga.Close()
	ga2 := start(t, ca, a, nil)
	if _, err := Ensure(context.Background(), ga2, f, cfg); err != nil || len(f.ran) != 1 {
		t.Fatalf("restart recreated forwarder: %v ran=%v", err, f.ran)
	}
	// import가 없어지면 forwarder를 지운다.
	if _, err := Ensure(context.Background(), ga2, f, Config{Peers: cfg.Peers}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.labels[name]; ok {
		t.Fatal("forwarder kept after import removal")
	}
}
