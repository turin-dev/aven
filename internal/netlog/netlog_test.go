package netlog

import (
	"context"
	"encoding/binary"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/node"
)

// ---------------- 테스트용 프레임 생성 ----------------

func ipv4Frame(src, dst string, proto uint8, sport, dport uint16, flags uint8, payload []byte) []byte {
	var l4 []byte
	switch proto {
	case protoTCP:
		l4 = make([]byte, 20)
		l4[12] = 5 << 4
		l4[13] = flags
	case protoUDP:
		l4 = make([]byte, 8)
		binary.BigEndian.PutUint16(l4[4:6], uint16(8+len(payload)))
	}
	binary.BigEndian.PutUint16(l4[0:2], sport)
	binary.BigEndian.PutUint16(l4[2:4], dport)
	l4 = append(l4, payload...)
	ip := make([]byte, 20)
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:4], uint16(20+len(l4)))
	ip[9] = proto
	s, d := netip.MustParseAddr(src).As4(), netip.MustParseAddr(dst).As4()
	copy(ip[12:16], s[:])
	copy(ip[16:20], d[:])
	eth := make([]byte, 14)
	binary.BigEndian.PutUint16(eth[12:14], 0x0800)
	return append(append(eth, ip...), l4...)
}

func ipv6Frame(src, dst string, sport, dport uint16, flags uint8) []byte {
	l4 := make([]byte, 20)
	binary.BigEndian.PutUint16(l4[0:2], sport)
	binary.BigEndian.PutUint16(l4[2:4], dport)
	l4[12], l4[13] = 5<<4, flags
	ip := make([]byte, 40)
	ip[0] = 0x60
	binary.BigEndian.PutUint16(ip[4:6], uint16(len(l4)))
	ip[6] = protoTCP
	s, d := netip.MustParseAddr(src).As16(), netip.MustParseAddr(dst).As16()
	copy(ip[8:24], s[:])
	copy(ip[24:40], d[:])
	eth := make([]byte, 14)
	binary.BigEndian.PutUint16(eth[12:14], 0x86dd)
	return append(append(eth, ip...), l4...)
}

func dnsName(name string) []byte {
	var b []byte
	for _, l := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		b = append(b, byte(len(l)))
		b = append(b, l...)
	}
	return append(b, 0)
}

func dnsQuery(id uint16, name string, qtype uint16) []byte {
	b := make([]byte, 12)
	binary.BigEndian.PutUint16(b[0:2], id)
	binary.BigEndian.PutUint16(b[2:4], 0x0100)
	binary.BigEndian.PutUint16(b[4:6], 1)
	b = append(b, dnsName(name)...)
	return binary.BigEndian.AppendUint16(binary.BigEndian.AppendUint16(b, qtype), 1)
}

// dnsResponse는 이름 압축 포인터(0xc00c)로 질문 이름을 가리키는 응답을 만든다.
func dnsResponse(id uint16, name string, qtype uint16, rcode int, cname string, addrs ...string) []byte {
	b := dnsQuery(id, name, qtype)
	binary.BigEndian.PutUint16(b[2:4], 0x8180|uint16(rcode))
	an := 0
	owner := []byte{0xc0, 0x0c}
	if cname != "" {
		target := dnsName(cname)
		b = append(b, owner...)
		b = binary.BigEndian.AppendUint16(b, 5)
		b = binary.BigEndian.AppendUint16(b, 1)
		b = binary.BigEndian.AppendUint32(b, 60)
		b = binary.BigEndian.AppendUint16(b, uint16(len(target)))
		b = append(b, target...)
		an++
	}
	for _, a := range addrs {
		ip := netip.MustParseAddr(a)
		typ, raw := uint16(1), ip.AsSlice()
		if ip.Is6() {
			typ = 28
		}
		b = append(b, owner...)
		b = binary.BigEndian.AppendUint16(b, typ)
		b = binary.BigEndian.AppendUint16(b, 1)
		b = binary.BigEndian.AppendUint32(b, 60)
		b = binary.BigEndian.AppendUint16(b, uint16(len(raw)))
		b = append(b, raw...)
		an++
	}
	binary.BigEndian.PutUint16(b[6:8], uint16(an))
	return b
}

func mustParse(t *testing.T, frame []byte) Packet {
	t.Helper()
	p, ok := ParseFrame(frame, len(frame))
	if !ok {
		t.Fatalf("frame did not parse")
	}
	return p
}

// ---------------- 파서 ----------------

func TestParseFrame(t *testing.T) {
	p := mustParse(t, ipv4Frame("172.18.0.2", "172.18.0.3", protoTCP, 40000, 5432, tcpSYN, []byte("hi")))
	if p.Src.String() != "172.18.0.2" || p.Dst.String() != "172.18.0.3" || p.SrcPort != 40000 || p.DstPort != 5432 ||
		p.Flags != tcpSYN || p.Length != 42 || string(p.Payload) != "hi" {
		t.Fatalf("ipv4 tcp: %+v", p)
	}
	frame := ipv4Frame("172.18.0.2", "1.1.1.1", protoUDP, 50000, 53, 0, []byte{1, 2, 3})
	p = mustParse(t, frame)
	if p.Proto != protoUDP || len(p.Payload) != 3 {
		t.Fatalf("ipv4 udp: %+v", p)
	}
	// 버퍼보다 큰 원래 프레임 길이는 바이트 수에 반영된다.
	if p, _ := ParseFrame(frame, 1500); p.Length != 1500-14 {
		t.Fatalf("wire length: %d", p.Length)
	}
	p = mustParse(t, ipv6Frame("fd00::2", "fd00::3", 41000, 80, tcpSYN|tcpACK))
	if !p.Src.Is6() || p.DstPort != 80 || p.Flags != tcpSYN|tcpACK || p.Length != 60 {
		t.Fatalf("ipv6: %+v", p)
	}
	// ARP, 짧은 프레임, 뒤쪽 IPv4 조각은 버린다.
	arp := make([]byte, 42)
	binary.BigEndian.PutUint16(arp[12:14], 0x0806)
	frag := ipv4Frame("10.0.0.1", "10.0.0.2", protoUDP, 1, 2, 0, nil)
	binary.BigEndian.PutUint16(frag[14+6:14+8], 10)
	for i, f := range [][]byte{arp, {1, 2, 3}, frag} {
		if _, ok := ParseFrame(f, len(f)); ok {
			t.Fatalf("case %d parsed", i)
		}
	}
}

func TestParseDNS(t *testing.T) {
	m, err := parseDNS(dnsResponse(7, "API.example.com", 1, 0, "edge.example.net", "203.0.113.10", "2001:db8::1"))
	if err != nil {
		t.Fatal(err)
	}
	if !m.Response || m.ID != 7 || m.QName != "api.example.com" || m.QType != 1 || m.RCode != 0 {
		t.Fatalf("header: %+v", m)
	}
	if strings.Join(m.Answers, ",") != "203.0.113.10,2001:db8::1" || strings.Join(m.CNAMEs, ",") != "edge.example.net" {
		t.Fatalf("answers: %+v", m)
	}
	q, err := parseDNS(dnsQuery(9, "db.production.keel.internal", 28))
	if err != nil || q.Response || q.QType != 28 {
		t.Fatalf("query: %+v %v", q, err)
	}
	// 포인터 고리, 잘린 메시지, 질문 여러 개는 거부한다.
	loop := dnsQuery(1, "a", 1)
	loop = append(loop[:12], 0xc0, 0x0c)
	trunc := dnsQuery(1, "example.com", 1)[:16]
	two := dnsQuery(1, "a.b", 1)
	binary.BigEndian.PutUint16(two[4:6], 2)
	for i, b := range [][]byte{loop, trunc, two, {1}} {
		if _, err := parseDNS(b); err == nil {
			t.Fatalf("case %d parsed", i)
		}
	}
	// 출력할 수 없는 바이트는 이스케이프된다.
	odd := dnsQuery(1, "x", 1)
	odd[13] = '\n'
	if m, err := parseDNS(odd); err != nil || m.QName != `\10` {
		t.Fatalf("escaped: %q %v", m.QName, err)
	}
}

func TestZone(t *testing.T) {
	for name, want := range map[string]string{
		"postgres": "internal", "postgres.production.keel.internal.": "internal", "metadata.google.internal": "internal",
		"printer.local": "internal", "api.github.com": "external", "example.com.": "external",
	} {
		if got := Zone(name); got != want {
			t.Fatalf("%s: %s", name, got)
		}
	}
}

// ---------------- tracker ----------------

type collected struct {
	mu     sync.Mutex
	events []Event
}

func (c *collected) add(e Event) { c.mu.Lock(); c.events = append(c.events, e); c.mu.Unlock() }
func (c *collected) all() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Event(nil), c.events...)
}

var testEndpoints = map[string]Endpoint{
	"172.18.0.2": {Kind: "service", ServiceID: "svc_api", Replica: "keel-api-1"},
	"172.18.0.3": {Kind: "service", ServiceID: "svc_db", Replica: "keel-db-1"},
	"172.18.0.4": {Kind: "mesh", ServiceName: "cache"},
	"172.18.0.5": {Kind: "edge_proxy"},
	"172.18.0.1": {Kind: "host"},
}

func newTracker() (*Tracker, *collected) {
	out := &collected{}
	return &Tracker{Env: "env_test", Emit: out.add, Classify: func(a netip.Addr) Endpoint {
		if ep, ok := testEndpoints[a.String()]; ok {
			return ep
		}
		return Endpoint{Kind: "internet"}
	}}, out
}

func feed(t *testing.T, tr *Tracker, now time.Time, frames ...[]byte) {
	t.Helper()
	for _, f := range frames {
		tr.Packet(mustParse(t, f), now)
	}
}

func TestTrackerTCPServiceToService(t *testing.T) {
	tr, out := newTracker()
	t0 := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	api, db := "172.18.0.2", "172.18.0.3"
	feed(t, tr, t0, ipv4Frame(api, db, protoTCP, 40000, 5432, tcpSYN, nil))
	feed(t, tr, t0.Add(2*time.Millisecond), ipv4Frame(db, api, protoTCP, 5432, 40000, tcpSYN|tcpACK, nil))
	feed(t, tr, t0.Add(3*time.Millisecond),
		ipv4Frame(api, db, protoTCP, 40000, 5432, tcpACK, make([]byte, 100)),
		ipv4Frame(db, api, protoTCP, 5432, 40000, tcpACK, make([]byte, 1000)),
		ipv4Frame(api, db, protoTCP, 40000, 5432, tcpFIN|tcpACK, nil))
	feed(t, tr, t0.Add(time.Second), ipv4Frame(db, api, protoTCP, 5432, 40000, tcpFIN|tcpACK, nil))
	// 마지막 ACK는 새 flow를 만들지 않는다.
	feed(t, tr, t0.Add(time.Second+time.Millisecond), ipv4Frame(api, db, protoTCP, 40000, 5432, tcpACK, nil))
	tr.Sweep(t0.Add(time.Minute))
	ev := out.all()
	if len(ev) != 1 || ev[0].Kind != "network" {
		t.Fatalf("events: %+v", ev)
	}
	f := ev[0].Flow
	if f.ServiceID != "svc_api" || f.Direction != "egress" || f.PeerKind != "service" || f.PeerServiceID != "svc_db" || f.Peer != "svc_db" ||
		f.SrcAddr != api || f.DstAddr != db || f.DstPort != 5432 || f.Status != "ok" || f.FlowState != "complete" || f.Protocol != "tcp" {
		t.Fatalf("flow: %+v", f)
	}
	if f.BytesSent != 40*3+100 || f.BytesReceived != 40*3+1000 || f.Packets != 6 || f.LatencyMS != 2 || f.DurationMS != 1000 {
		t.Fatalf("counters: sent=%d recv=%d pkts=%d latency=%v dur=%v", f.BytesSent, f.BytesReceived, f.Packets, f.LatencyMS, f.DurationMS)
	}
}

func TestTrackerIngressRefusedTimeout(t *testing.T) {
	tr, out := newTracker()
	t0 := time.Now()
	// edge proxy → api: ingress from api's point of view
	feed(t, tr, t0, ipv4Frame("172.18.0.5", "172.18.0.2", protoTCP, 50000, 8080, tcpSYN, nil),
		ipv4Frame("172.18.0.2", "172.18.0.5", protoTCP, 8080, 50000, tcpRST|tcpACK, nil))
	// api → internet SYN without answer
	feed(t, tr, t0, ipv4Frame("172.18.0.2", "93.184.216.34", protoTCP, 41000, 443, tcpSYN, nil))
	// host → nothing interesting (neither side a service) is ignored
	feed(t, tr, t0, ipv4Frame("172.18.0.1", "172.18.0.5", protoTCP, 41001, 80, tcpSYN, nil))
	tr.Sweep(t0.Add(11 * time.Second))
	ev := out.all()
	if len(ev) != 2 {
		t.Fatalf("events: %d", len(ev))
	}
	byStatus := map[string]*Flow{}
	for _, e := range ev {
		byStatus[e.Flow.Status] = e.Flow
	}
	r := byStatus["refused"]
	if r == nil || r.Direction != "ingress" || r.ServiceID != "svc_api" || r.PeerKind != "edge_proxy" || r.Peer != "edge_proxy" ||
		r.SrcAddr != "172.18.0.5" || r.DstPort != 8080 || r.BytesSent != 40 || r.BytesReceived != 40 {
		t.Fatalf("refused: %+v", r)
	}
	to := byStatus["timeout"]
	if to == nil || to.Direction != "egress" || to.PeerKind != "internet" || to.DstPort != 443 {
		t.Fatalf("timeout: %+v", to)
	}
}

func TestTrackerMeshPeerUDPAndActive(t *testing.T) {
	tr, out := newTracker()
	t0 := time.Now()
	// 연결 중간부터 본 TCP: 작은 포트(6379)가 응답자다.
	feed(t, tr, t0, ipv4Frame("172.18.0.4", "172.18.0.2", protoTCP, 6379, 45000, tcpACK, make([]byte, 10)))
	feed(t, tr, t0, ipv4Frame("172.18.0.2", "8.8.4.4", protoUDP, 40001, 123, 0, make([]byte, 48)))
	tr.Sweep(t0.Add(31 * time.Second)) // UDP 유휴 → complete
	tr.Sweep(t0.Add(6 * time.Minute))  // TCP는 아직 열림 → active
	ev := out.all()
	if len(ev) != 2 {
		t.Fatalf("events: %+v", ev)
	}
	u, m := ev[0].Flow, ev[1].Flow
	if u.Protocol != "udp" || u.Direction != "egress" || u.PeerKind != "internet" || u.FlowState != "complete" || u.BytesSent != 76 {
		t.Fatalf("udp: %+v", u)
	}
	if m.Direction != "egress" || m.PeerKind != "service" || m.Peer != "cache" || m.DstPort != 6379 || m.FlowState != "active" || m.BytesReceived != 50 {
		t.Fatalf("mesh: %+v", m)
	}
	// 새 패킷이 없으면 active를 반복하지 않는다.
	tr.Sweep(t0.Add(12 * time.Minute))
	if len(out.all()) != 2 {
		t.Fatal("repeated active snapshot")
	}
}

func TestTrackerDNS(t *testing.T) {
	tr, out := newTracker()
	t0 := time.Now()
	api, resolver := "172.18.0.2", "192.168.1.1"
	feed(t, tr, t0,
		ipv4Frame(api, resolver, protoUDP, 51000, 53, 0, dnsQuery(1, "api.example.com", 1)),
		ipv4Frame(api, resolver, protoUDP, 51001, 53, 0, dnsQuery(2, "db.prod.keel.internal", 1)),
		ipv4Frame(api, resolver, protoUDP, 51002, 53, 0, dnsQuery(3, "slow.example.com", 28)),
		// 서비스가 아닌 주소의 질의는 기록하지 않는다
		ipv4Frame("172.18.0.5", resolver, protoUDP, 51003, 53, 0, dnsQuery(4, "x.example.com", 1)))
	feed(t, tr, t0.Add(4*time.Millisecond),
		ipv4Frame(resolver, api, protoUDP, 53, 51000, 0, dnsResponse(1, "api.example.com", 1, 0, "edge.example.net", "203.0.113.10")),
		ipv4Frame(resolver, api, protoUDP, 53, 51001, 0, dnsResponse(2, "db.prod.keel.internal", 1, 3, "")),
		// ID가 맞지 않는 응답은 무시한다
		ipv4Frame(resolver, api, protoUDP, 53, 51002, 0, dnsResponse(99, "slow.example.com", 28, 0, "", "2001:db8::1")))
	tr.Sweep(t0.Add(6 * time.Second))
	ev := out.all()
	if len(ev) != 3 {
		t.Fatalf("events: %+v", ev)
	}
	ok, nx, to := ev[0].DNS, ev[1].DNS, ev[2].DNS
	if ok.QName != "api.example.com" || ok.QType != "A" || ok.RCode != "NOERROR" || ok.Status != "ok" || ok.Zone != "external" ||
		strings.Join(ok.Answers, ",") != "203.0.113.10" || strings.Join(ok.CNAMEChain, ",") != "edge.example.net" ||
		ok.ServiceID != "svc_api" || ok.Resolver != resolver || ok.DurationMS != 4 || ok.Replica != "keel-api-1" {
		t.Fatalf("ok: %+v", ok)
	}
	if nx.RCode != "NXDOMAIN" || nx.Status != "failed" || nx.Zone != "internal" || len(nx.Answers) != 0 {
		t.Fatalf("nxdomain: %+v", nx)
	}
	if to.RCode != "TIMEOUT" || to.Status != "failed" || to.QType != "AAAA" {
		t.Fatalf("timeout: %+v", to)
	}
	// DNS 패킷은 flow로 세지 않는다.
	for _, e := range ev {
		if e.Kind != "dns" {
			t.Fatalf("unexpected %s", e.Kind)
		}
	}
}

// ---------------- collector ----------------

type fakeDocker struct{}

func (fakeDocker) ManagedNetworks(context.Context) ([]string, error) {
	return []string{"aven", "keel-env_test"}, nil
}
func (fakeDocker) NetworkInfo(_ context.Context, name string) (*node.NetworkInfo, error) {
	if name != "keel-env_test" {
		return nil, errors.New("not managed")
	}
	return &node.NetworkInfo{Subnets: []netip.Prefix{netip.MustParsePrefix("172.18.0.0/16")}, Gateways: []netip.Addr{netip.MustParseAddr("172.18.0.1")}}, nil
}
func (fakeDocker) ListManaged(context.Context) (map[string]map[string]string, error) {
	return map[string]map[string]string{
		"keel-api-1":   {node.LabelEnv: "env_test", node.LabelRole: node.RoleApp, node.LabelService: "svc_api"},
		"keel-mesh-db": {node.LabelEnv: "env_test", node.LabelRole: node.RoleMeshProxy, labelMeshService: "db"},
		"keel-proxy":   {node.LabelRole: "proxy"},
		"keel-other":   {node.LabelEnv: "env_gone", node.LabelRole: node.RoleApp, node.LabelService: "svc_x"},
	}, nil
}
func (fakeDocker) InspectMany(_ context.Context, names []string) (map[string]*node.ContainerState, error) {
	all, _ := fakeDocker{}.ListManaged(context.Background())
	ips := map[string]string{"keel-api-1": "172.18.0.2", "keel-mesh-db": "172.18.0.4", "keel-proxy": "172.18.0.5", "keel-other": "10.9.0.2"}
	out := map[string]*node.ContainerState{}
	for _, n := range names {
		out[n] = &node.ContainerState{Name: n, Running: true, Labels: all[n], IPs: map[string][]string{"keel-env_test": {ips[n]}}}
	}
	return out, nil
}

type fakeCapture struct {
	frames chan []byte
	closed chan struct{}
	once   sync.Once
}

func (f *fakeCapture) Read(buf []byte) (int, int, error) {
	select {
	case b := <-f.frames:
		return copy(buf, b), len(b), nil
	case <-time.After(10 * time.Millisecond):
		return 0, 0, nil
	}
}
func (f *fakeCapture) Close() error { f.once.Do(func() { close(f.closed) }); return nil }

func TestCollectorEndToEnd(t *testing.T) {
	cp := &fakeCapture{frames: make(chan []byte, 16), closed: make(chan struct{})}
	var opened string
	c := &Collector{NodeID: "node_local", Docker: fakeDocker{},
		open: func(ifname string) (capture, error) { opened = ifname; return cp, nil },
		iface: func(gws []netip.Addr) (string, error) {
			if len(gws) != 1 || gws[0].String() != "172.18.0.1" {
				t.Errorf("gateways: %v", gws)
			}
			return "br-test", nil
		},
	}
	// Run은 CAP_NET_RAW 검사부터 하므로 테스트는 Run의 단계(refresh, syncCaptures)를 직접 돈다.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.refresh(ctx)
	if ep := c.Classify("env_test", netip.MustParseAddr("172.18.0.2")); !ep.IsService() || ep.ServiceID != "svc_api" || ep.Replica != "keel-api-1" {
		t.Fatalf("classify service: %+v", ep)
	}
	for addr, kind := range map[string]string{"172.18.0.4": "mesh", "172.18.0.5": "edge_proxy", "172.18.0.1": "host", "172.18.9.9": "private",
		"8.8.8.8": "internet", "10.1.2.3": "private", "100.100.1.1": "private"} {
		if ep := c.Classify("env_test", netip.MustParseAddr(addr)); ep.Kind != kind {
			t.Fatalf("%s: %+v", addr, ep)
		}
	}
	past, ch, unsub := c.Subscribe()
	defer unsub()
	if len(past) != 0 {
		t.Fatalf("past: %+v", past)
	}
	running := map[string]*runningCapture{}
	c.syncCaptures(ctx, running)
	if opened != "br-test" || len(running) != 1 {
		t.Fatalf("opened %q running %d", opened, len(running))
	}
	cp.frames <- ipv4Frame("172.18.0.2", "172.18.0.4", protoTCP, 40000, 6379, tcpSYN, nil)
	cp.frames <- ipv4Frame("172.18.0.4", "172.18.0.2", protoTCP, 6379, 40000, tcpRST|tcpACK, nil)
	var got []Event
	deadline := time.After(2 * time.Second)
	for len(got) < 2 {
		select {
		case e := <-ch:
			got = append(got, e)
		case <-deadline:
			t.Fatalf("events: %+v", got)
		}
	}
	if got[0].Kind != "status" || !got[0].Status.Active || strings.Join(got[0].Status.Networks, ",") != "env_test" {
		t.Fatalf("status: %+v", got[0].Status)
	}
	f := got[1].Flow
	if got[1].Kind != "network" || f.Status != "refused" || f.Peer != "db" || f.PeerKind != "service" || f.Environment != "env_test" ||
		!strings.HasPrefix(got[1].ID, "node_local-") {
		t.Fatalf("flow: %+v %+v", got[1], f)
	}
	// 새 구독자는 상태와 ring을 replay로 받는다.
	past2, _, unsub2 := c.Subscribe()
	unsub2()
	if len(past2) != 2 || past2[1].ID != got[1].ID {
		t.Fatalf("replay: %+v", past2)
	}
	// 네트워크가 사라지면 캡처를 닫는다.
	c.epMu.Lock()
	c.gateways = map[string][]netip.Addr{}
	c.epMu.Unlock()
	c.syncCaptures(ctx, running)
	select {
	case <-cp.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("capture not closed")
	}
}
