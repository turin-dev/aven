package agentprotocol

// port.free: TCP 프록시 공개 포트를 배정하기 전에 원격 노드에서 그 포트를 bind할 수 있는지 확인한다(agent의 TCP bind 주소에서
// 잠깐 listen했다가 닫는다). 이 작업을 모르는 옛 agent는 거부한다(서버는 확인 없이 배정하고, 충돌은 publish 때 드러난다).
const OpPortFree = "port.free"

// PortFreeRequest는 port.free 요청이다(1024..65535).
type PortFreeRequest struct {
	Port int `json:"port"`
}

// PortFreeResult는 port.free의 결과다.
type PortFreeResult struct {
	Free bool `json:"free"`
}
