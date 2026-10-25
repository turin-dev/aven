// 노드의 aven 프로세스(aven-server, aven-agent)가 환경 네트워크마다 docker bridge(br-…)에 AF_PACKET 소켓을 열어
// 패킷 헤더만 읽는다(promiscuous, 수동 관찰. 컨테이너 설정이나 이름 해석 경로는 바꾸지 않는다):
//
//   - DNS: 컨테이너가 보내는 UDP 53 질의와 응답을 짝지어 이름, 타입, rcode, 응답을 기록한다. docker 내장 DNS(127.0.0.11)는
//     컨테이너 네임스페이스 안에서 답하므로, 같은 네트워크의 alias(`<svc>`, `<svc>.<env>.keel.internal`)로 바로 풀린 조회는
//     bridge를 지나지 않아 보이지 않는다. 외부 이름과, 내장 DNS가 모르는 이름(예: 틀린 `*.keel.internal`)은 upstream으로
//     넘어가므로 기록된다.
//   - Flow: TCP/UDP 연결마다 방향, 상대(서비스/인터넷/edge proxy/호스트), 포트, 바이트, 패킷, 지속 시간, 연결 지연과 상태를 기록한다.
//
// 캡처에는 CAP_NET_RAW가 필요하다(보통 root). 권한이 없으면 수집기는 꺼진 채로 상태만 알린다.
package netlog

import "time"

// DNSQuery는 서비스 컨테이너가 보낸 DNS 질의 하나와 그 응답이다.
type DNSQuery struct {
	T           time.Time `json:"t"`
	Environment string    `json:"environment_id"`
	ServiceID   string    `json:"service_id"`
	Replica     string    `json:"replica,omitempty"`
	QName       string    `json:"qname"`
	QType       string    `json:"qtype"`
	// Zone은 internal(사설 네트워크 이름: *.keel.internal, *.internal, 점 없는 이름) 또는 external이다.
	Zone       string   `json:"zone"`
	RCode      string   `json:"rcode"`  // NOERROR, NXDOMAIN, SERVFAIL, REFUSED, TIMEOUT, ...
	Status     string   `json:"status"` // ok | failed
	Answers    []string `json:"answers"`
	CNAMEChain []string `json:"cname_chain,omitempty"`
	Resolver   string   `json:"resolver"`
	SrcAddr    string   `json:"src_addr"`
	DurationMS float64  `json:"duration_ms"`
}

// Flow는 연결 하나(또는 오래 가는 연결의 중간 스냅샷)다. 방향과 바이트는 ServiceID 쪽에서 본 값이다.
type Flow struct {
	T            time.Time `json:"t"` // capture end
	CaptureStart time.Time `json:"capture_start"`
	Environment  string    `json:"environment_id"`
	ServiceID    string    `json:"service_id"`
	Replica      string    `json:"replica,omitempty"`
	// FlowState: complete(끝난 연결), active(아직 열린 연결의 중간 기록)
	FlowState string `json:"flow_state"`
	Direction string `json:"direction"` // egress | ingress
	Protocol  string `json:"protocol"`  // tcp | udp
	SrcAddr   string `json:"src_addr"`
	SrcPort   int    `json:"src_port"`
	DstAddr   string `json:"dst_addr"`
	DstPort   int    `json:"dst_port"`
	// PeerKind: service | internet | edge_proxy | host | private | unknown
	PeerKind      string `json:"peer_kind"`
	PeerServiceID string `json:"peer_service_id,omitempty"`
	// Peer는 상대 서비스 이름(mesh forwarder면 다른 노드의 서비스 이름), 아니면 PeerKind와 같은 값이다.
	Peer          string  `json:"peer"`
	BytesSent     int64   `json:"bytes_sent"`
	BytesReceived int64   `json:"bytes_received"`
	Packets       int64   `json:"packets"`
	DurationMS    float64 `json:"duration_ms"`
	LatencyMS     float64 `json:"latency_ms,omitempty"` // TCP SYN → SYN-ACK
	// Status: ok | refused(RST로 거절) | timeout(SYN에 답 없음) | reset(연결 중 RST)
	Status string `json:"status"`
}

// Status는 노드 수집기의 상태다(권한이 없거나 꺼졌는지 알리려고).
type Status struct {
	Node     string   `json:"node"`
	Active   bool     `json:"active"`
	Error    string   `json:"error,omitempty"`
	Networks []string `json:"networks,omitempty"`
}

// Event는 노드에서 서버로 보내는 기록이다. ID는 노드 안에서 유일해서 재연결 replay의 중복을 거른다.
type Event struct {
	ID     string    `json:"id"`
	Kind   string    `json:"kind"` // dns | network | status
	DNS    *DNSQuery `json:"dns,omitempty"`
	Flow   *Flow     `json:"flow,omitempty"`
	Status *Status   `json:"status,omitempty"`
}
