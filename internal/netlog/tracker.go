package netlog

import (
	"net/netip"
	"time"
)

// Endpoint는 주소 하나가 환경 네트워크에서 무엇인지다.
type Endpoint struct {
	// Kind: service(이 노드의 서비스 컨테이너), mesh(다른 노드 서비스의 forwarder), edge_proxy(keel-proxy, TCP proxy),
	// host(bridge gateway = 이 노드), private(그 밖의 사설 주소), internet, unknown
	Kind        string
	ServiceID   string
	ServiceName string // mesh forwarder처럼 ID 대신 이름만 아는 경우
	Replica     string
}

// IsService는 기록의 주인이 될 수 있는 서비스 컨테이너인지다.
func (e Endpoint) IsService() bool { return e.Kind == "service" && e.ServiceID != "" }

const (
	maxFlows      = 1 << 16
	maxPendingDNS = 4096

	dnsTimeout   = 5 * time.Second
	synTimeout   = 10 * time.Second
	udpIdle      = 30 * time.Second
	tcpIdle      = 2 * time.Hour
	closedLinger = 10 * time.Second
	activeEvery  = 5 * time.Minute
)

type flowKey struct {
	proto uint8
	a, b  netip.AddrPort // a < b
}

func keyOf(p Packet) flowKey {
	s, d := netip.AddrPortFrom(p.Src, p.SrcPort), netip.AddrPortFrom(p.Dst, p.DstPort)
	if s.Compare(d) > 0 {
		s, d = d, s
	}
	return flowKey{proto: p.Proto, a: s, b: d}
}

type flowState struct {
	proto          uint8
	cli, srv       netip.AddrPort
	cliEP, srvEP   Endpoint
	ignore         bool
	start, last    time.Time
	lastReport     time.Time
	reportedPkts   int64
	synAt          time.Time
	established    bool
	latency        time.Duration
	c2s, s2c, pkts int64
	finC, finS     bool
	closedAt       time.Time
	status         string
}

type dnsKey struct {
	client netip.AddrPort
	server netip.Addr
	id     uint16
}

type dnsPending struct {
	at    time.Time
	ep    Endpoint
	qname string
	qtype uint16
}

// Tracker는 한 환경 네트워크의 패킷을 flow와 DNS 기록으로 묶는다. 한 goroutine에서만 쓴다.
type Tracker struct {
	Env      string
	Classify func(netip.Addr) Endpoint
	Emit     func(Event)

	flows map[flowKey]*flowState
	dns   map[dnsKey]*dnsPending
}

func (t *Tracker) init() {
	if t.flows == nil {
		t.flows = map[flowKey]*flowState{}
		t.dns = map[dnsKey]*dnsPending{}
	}
}

// Packet은 패킷 하나를 반영한다.
func (t *Tracker) Packet(p Packet, now time.Time) {
	t.init()
	if p.Proto == protoUDP && (p.DstPort == 53 || p.SrcPort == 53) {
		t.dnsPacket(p, now)
		return
	}
	if p.Proto == protoTCP && (p.DstPort == 53 || p.SrcPort == 53) {
		return // DNS over TCP는 DNS 로그 쪽 일이고 flow로 세면 잡음이다
	}
	k := keyOf(p)
	f := t.flows[k]
	if f != nil && !f.closedAt.IsZero() {
		// 닫힌 연결의 마지막 ACK는 무시하고, 같은 포트로 새 SYN이 오면 새 연결로 본다.
		if p.Proto == protoTCP && p.Flags&tcpSYN != 0 && p.Flags&tcpACK == 0 {
			delete(t.flows, k)
			f = nil
		} else {
			return
		}
	}
	if f == nil {
		if len(t.flows) >= maxFlows {
			return
		}
		f = t.newFlow(p, now)
		t.flows[k] = f
	}
	if f.ignore {
		f.last = now
		return
	}
	fromCli := p.Src == f.cli.Addr() && p.SrcPort == f.cli.Port()
	if fromCli {
		f.c2s += int64(p.Length)
	} else {
		f.s2c += int64(p.Length)
	}
	f.pkts++
	f.last = now
	if p.Proto != protoTCP {
		return
	}
	switch {
	case p.Flags&tcpRST != 0:
		if !f.established && !fromCli {
			f.status = "refused"
		} else if f.status == "" {
			f.status = "reset"
		}
		t.finish(f, now, "complete")
	case p.Flags&tcpSYN != 0 && p.Flags&tcpACK != 0 && !fromCli:
		if !f.established && !f.synAt.IsZero() {
			f.latency = now.Sub(f.synAt)
		}
		f.established = true
	case p.Flags&tcpSYN != 0 && fromCli:
		// 재전송된 SYN: 처음 시각을 지킨다
	default:
		if p.Flags&tcpACK != 0 && f.synAt.IsZero() {
			f.established = true
		}
	}
	if p.Flags&tcpFIN != 0 {
		if fromCli {
			f.finC = true
		} else {
			f.finS = true
		}
		if f.finC && f.finS {
			t.finish(f, now, "complete")
		}
	}
}

func (t *Tracker) newFlow(p Packet, now time.Time) *flowState {
	src, dst := netip.AddrPortFrom(p.Src, p.SrcPort), netip.AddrPortFrom(p.Dst, p.DstPort)
	f := &flowState{proto: p.Proto, start: now, last: now, lastReport: now}
	switch {
	case p.Proto == protoTCP && p.Flags&tcpSYN != 0 && p.Flags&tcpACK == 0:
		f.cli, f.srv, f.synAt = src, dst, now
	case p.Proto == protoTCP && p.Flags&tcpSYN != 0:
		f.cli, f.srv, f.established = dst, src, true // SYN을 놓쳤다
	case p.SrcPort < p.DstPort && p.SrcPort < 32768:
		// 연결 중간부터 봤다: 작은(서버 쪽으로 보이는) 포트를 응답자로 둔다
		f.cli, f.srv, f.established = dst, src, true
	default:
		f.cli, f.srv, f.established = src, dst, true
	}
	f.cliEP, f.srvEP = t.classify(f.cli.Addr()), t.classify(f.srv.Addr())
	f.ignore = !f.cliEP.IsService() && !f.srvEP.IsService()
	return f
}

func (t *Tracker) classify(a netip.Addr) Endpoint {
	if t.Classify == nil {
		return Endpoint{Kind: "unknown"}
	}
	return t.Classify(a)
}

func (t *Tracker) finish(f *flowState, now time.Time, state string) {
	if f.closedAt.IsZero() {
		f.closedAt = now
		if !f.ignore {
			t.emitFlow(f, state)
		}
	}
}

func (t *Tracker) emitFlow(f *flowState, state string) {
	if t.Emit == nil {
		return
	}
	status := f.status
	if status == "" {
		status = "ok"
	}
	out := &Flow{
		T: f.last.UTC(), CaptureStart: f.start.UTC(), Environment: t.Env, FlowState: state, Protocol: protoName(f.proto),
		SrcAddr: f.cli.Addr().Unmap().String(), SrcPort: int(f.cli.Port()), DstAddr: f.srv.Addr().Unmap().String(), DstPort: int(f.srv.Port()),
		Packets: f.pkts, DurationMS: ms(f.last.Sub(f.start)), Status: status,
	}
	if f.latency > 0 {
		out.LatencyMS = ms(f.latency)
	}
	self, peer := f.cliEP, f.srvEP
	out.Direction, out.BytesSent, out.BytesReceived = "egress", f.c2s, f.s2c
	if !self.IsService() {
		self, peer = f.srvEP, f.cliEP
		out.Direction, out.BytesSent, out.BytesReceived = "ingress", f.s2c, f.c2s
	}
	out.ServiceID, out.Replica = self.ServiceID, self.Replica
	out.PeerKind, out.PeerServiceID, out.Peer = peer.Kind, peer.ServiceID, peer.Kind
	if peer.Kind == "mesh" {
		out.PeerKind = "service"
	}
	if peer.ServiceName != "" {
		out.Peer = peer.ServiceName
	} else if peer.ServiceID != "" {
		out.Peer = peer.ServiceID
	}
	t.Emit(Event{Kind: "network", Flow: out})
}

func (t *Tracker) dnsPacket(p Packet, now time.Time) {
	m, err := parseDNS(p.Payload)
	if err != nil {
		return
	}
	if !m.Response && p.DstPort == 53 {
		k := dnsKey{client: netip.AddrPortFrom(p.Src, p.SrcPort), server: p.Dst, id: m.ID}
		if _, dup := t.dns[k]; dup || len(t.dns) >= maxPendingDNS {
			return
		}
		ep := t.classify(p.Src)
		if !ep.IsService() {
			return
		}
		t.dns[k] = &dnsPending{at: now, ep: ep, qname: m.QName, qtype: m.QType}
		return
	}
	if m.Response && p.SrcPort == 53 {
		k := dnsKey{client: netip.AddrPortFrom(p.Dst, p.DstPort), server: p.Src, id: m.ID}
		q := t.dns[k]
		if q == nil || q.qname != m.QName || q.qtype != m.QType {
			return
		}
		delete(t.dns, k)
		t.emitDNS(k, q, now, RCodeName(m.RCode), m.Answers, m.CNAMEs)
	}
}

func (t *Tracker) emitDNS(k dnsKey, q *dnsPending, now time.Time, rcode string, answers, cnames []string) {
	if t.Emit == nil {
		return
	}
	if answers == nil {
		answers = []string{}
	}
	status := "ok"
	if rcode != "NOERROR" {
		status = "failed"
	}
	t.Emit(Event{Kind: "dns", DNS: &DNSQuery{
		T: q.at.UTC(), Environment: t.Env, ServiceID: q.ep.ServiceID, Replica: q.ep.Replica,
		QName: q.qname, QType: QTypeName(q.qtype), Zone: Zone(q.qname), RCode: rcode, Status: status,
		Answers: answers, CNAMEChain: cnames, Resolver: k.server.Unmap().String(), SrcAddr: k.client.Addr().Unmap().String(),
		DurationMS: ms(now.Sub(q.at)),
	}})
}

// Sweep은 시간이 지난 질의와 연결을 정리한다(응답 없는 DNS, 답 없는 SYN, 유휴 UDP, 오래 가는 연결의 중간 기록).
func (t *Tracker) Sweep(now time.Time) {
	t.init()
	for k, q := range t.dns {
		if now.Sub(q.at) >= dnsTimeout {
			delete(t.dns, k)
			t.emitDNS(k, q, now, "TIMEOUT", nil, nil)
		}
	}
	for k, f := range t.flows {
		switch {
		case !f.closedAt.IsZero():
			if now.Sub(f.closedAt) >= closedLinger {
				delete(t.flows, k)
			}
		case f.ignore:
			if now.Sub(f.last) >= udpIdle {
				delete(t.flows, k)
			}
		case f.proto == protoTCP && !f.established && now.Sub(f.synAt) >= synTimeout:
			f.status = "timeout"
			t.finish(f, now, "complete")
		case f.proto == protoUDP && now.Sub(f.last) >= udpIdle,
			f.proto == protoTCP && now.Sub(f.last) >= tcpIdle:
			t.finish(f, f.last, "complete")
		case now.Sub(f.lastReport) >= activeEvery:
			if f.pkts != f.reportedPkts {
				t.emitFlow(f, "active")
				f.reportedPkts = f.pkts
			}
			f.lastReport = now
		}
	}
}

// Flush는 열린 연결을 모두 active로 내보낸다(수집기를 멈출 때).
func (t *Tracker) Flush(now time.Time) {
	t.init()
	for _, f := range t.flows {
		if f.closedAt.IsZero() && !f.ignore && f.pkts != f.reportedPkts {
			t.emitFlow(f, "active")
			f.reportedPkts = f.pkts
		}
	}
}

func protoName(p uint8) string {
	switch p {
	case protoTCP:
		return "tcp"
	case protoUDP:
		return "udp"
	}
	return "unknown"
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
