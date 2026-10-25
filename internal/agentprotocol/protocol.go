// Package agentprotocol은 서버와 원격 agent의 요청/응답 메시지 형식이다.
//
// 연결: agent가 wss://<server>/api/agent/connect 로 outbound 연결한다(TLS 필수). 인증은 연결 직후 WebSocket 안에서 한다:
// 서버가 auth.challenge{nonce}를 보내면 agent가 aven CA가 발급한 인증서와 nonce 서명을 auth.response로 보내고,
// 서버가 auth.ok를 돌려주면 그 뒤부터 아래 요청/응답이 오간다(자세한 내용은 internal/agentpki와 docs/api-v1.md).
// 옛 방식(Authorization: Bearer <node>:<secret>)은 인증서로 업그레이드하기 전의 agent에만 허용한다.
// 메시지: 서버가 {id, operation, payload}를 보내면 agent가 같은 id로 {payload} 또는 {error}를 한 번 돌려준다.
// 오래 걸리는 작업(pull/build/logs)은 최종 응답 전에 stream=true 메시지를 여러 번 보낼 수 있다.
// 서버는 {operation:"cancel", payload:{id}}로 진행 중인 요청을 취소한다.
package agentprotocol

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"

	"github.com/coder/websocket"
	"github.com/turin-dev/aven/internal/mesh"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/proxy"
)

const MaxMessageBytes = 4 << 20

// 연결 인증 단계의 메시지(ID 없음). 인증이 끝나기 전에는 이 메시지만 오간다.
const (
	OpAuthChallenge = "auth.challenge" // 서버 → agent  AuthChallenge
	OpAuthResponse  = "auth.response"  // agent → 서버  AuthResponse
	OpAuthOK        = "auth.ok"        // 서버 → agent
	OpCertRenew     = "cert.renew"     // 서버 → agent (연결 중 아무 때나)  CertRenew
	// MaxAuthMessageBytes는 인증 전 메시지 크기 상한이다.
	MaxAuthMessageBytes = 16 << 10
)

type AuthChallenge struct {
	Nonce string `json:"nonce"`
}

type AuthResponse struct {
	Certificate string `json:"certificate"` // PEM
	Signature   string `json:"signature"`   // hex(ed25519(transcript))
}

type CertRenew struct {
	Certificate string `json:"certificate"` // PEM, 같은 공개키로 새로 발급
}

type Message struct {
	ID        string          `json:"id,omitempty"`
	Operation string          `json:"operation"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Error     string          `json:"error,omitempty"`
	Stream    bool            `json:"stream,omitempty"` // 최종 응답이 아닌 중간 데이터
	// Up은 agent가 서버에 보내는 요청이다(예: wake). 서버는 같은 id로 Reply=true 메시지를 돌려준다.
	Up    bool `json:"up,omitempty"`
	Reply bool `json:"reply,omitempty"`
}

// agent가 받아들이는 작업 전체 목록(allowlist). 여기에 없는 작업은 agent가 거부한다.
const (
	OpPing            = "ping"
	OpCancel          = "cancel"
	OpImagePull       = "image.pull"
	OpImageExists     = "image.exists"
	OpImagePorts      = "image.exposed_ports"
	OpImageDigest     = "image.digest"
	OpImageList       = "image.list_repo"
	OpImageRemove     = "image.remove"
	OpNetworkEnsure   = "network.ensure"
	OpNetworkRemove   = "network.remove"
	OpVolumeEnsure    = "volume.ensure"
	OpVolumeRemove    = "volume.remove"
	OpContainerRun    = "container.run"
	OpContainerRunFg  = "container.run_to_completion"
	OpContainerStart  = "container.start"
	OpContainerStop   = "container.stop"
	OpContainerRestrt = "container.restart"
	OpContainerRemove = "container.remove"
	OpContainerInspct = "container.inspect"
	OpContainerList   = "container.list_managed"
	OpContainerLogs   = "container.logs"
	OpContainerStats  = "container.stats"
	OpContainerProbe  = "container.probe"
	OpSandboxCommit   = "sandbox.commit"
	OpSourceFetch     = "source.fetch"
	OpSourceCleanup   = "source.cleanup"
	OpImageBuild      = "image.build"
	OpProxyApply      = "proxy.apply"
	OpTCPEnsure       = "tcpproxy.ensure"
	OpTCPRemove       = "tcpproxy.remove"
	OpAccessLogs      = "proxy.accesslog_follow" // 오래 걸리는 stream: keel-proxy access log
	OpEvents          = "events.follow"          // 오래 걸리는 stream: aven 컨테이너 die/start 이벤트
	OpVolumeExport    = "volume.export"          // stream: 볼륨 tar 스트림을 chunk로
	// db.backup(stream): 이 노드의 aven DB 컨테이너를 덤프해 chunk로 보낸다(DBToolRequest, 스크립트는 agent에 고정; dbtool.go).
	// 복원은 upload.begin{kind:"db.restore"}. 이 작업을 모르는 옛 agent는 거부한다(서버는 agent 업그레이드가 필요하다고 알린다).
	OpDBBackup = "db.backup"
	// volume.sizes: 이 노드의 aven 볼륨 이름 → 바이트 크기(docker system df). 서버가 5분마다 부른다.
	OpVolumeSizes  = "volume.sizes"
	OpUploadBegin  = "upload.begin" // 서버 → agent로 큰 데이터(볼륨/소스)를 chunk로 올리기
	OpUploadChunk  = "upload.chunk"
	OpUploadFinish = "upload.finish"
	OpUploadAbort  = "upload.abort"
	OpNodeStats    = "node.stats"
	// exec: 컨테이너 TTY 셸. exec.start는 세션이 끝날 때까지 출력을 DataChunk stream으로 보내고 ExecResult로 끝난다.
	OpExecStart  = "exec.start"
	OpExecInput  = "exec.input"
	OpExecResize = "exec.resize"
	OpExecClose  = "exec.close"
	// exec.run: TTY 없는 일회성 exec(DB 데이터 브라우저). 출력을 모아 ExecRunResult 하나로 돌려준다.
	// 이 작업을 모르는 옛 agent는 거부한다(서버는 agent 업그레이드가 필요하다고 알린다).
	OpExecRun = "exec.run"
	// mesh.ensure: 노드 간 사설 네트워킹의 원하는 상태(peer, export, import)를 반영하고 mesh 상태를 돌려준다.
	// listen 주소는 agent 설정(--private-address, --mesh-port)이며 서버가 정하지 못한다. forwarder 컨테이너는 agent가 직접 만든다.
	OpMeshEnsure = "mesh.ensure"
	// netlog.follow: 오래 걸리는 stream. 노드 수집기의 DNS 조회와 연결(flow) 기록(netlog.Event)을 보낸다.
	OpNetLogFollow = "netlog.follow"
	// ports.listening: 인스턴스의 app 컨테이너가 listen 중인 TCP 포트(호스트에서 /proc/<pid>/net/tcp{,6}를 읽는다, exec 없음).
	// 이 작업을 모르는 옛 agent는 거부한다(서버는 감지 결과 없이 "unavailable"로 보여 준다).
	OpPortsListening = "ports.listening"
	// egress.status: 환경 네트워크 컨테이너의 IPv6 egress 상태(읽기 전용, 경로/NAT66 규칙을 읽는다)와 차단 가능 여부.
	// egress.ensure: outbound_ipv6=false인 인스턴스 컨테이너의 IPv6 egress를 DOCKER-USER ip6tables 규칙(keel:egress6:<id>)으로 막고
	// 오래된 aven 규칙을 지운다. 이 작업을 모르는 옛 agent는 거부한다(서버는 상태를 "모름"으로 보이고 차단 토글을 거부한다).
	OpEgressStatus = "egress.status"
	OpEgressEnsure = "egress.ensure"
	// ipv6.addrs: 노드 인터페이스의 전역 IPv6 주소(읽기 전용, ip -j -6 addr). 서버가 IPv6 풀을 정하기 전에 겹치는 주소가 없는지 본다.
	// ipv6.ensure: 도메인별 공개 IPv6 주소(/128, deprecated)를 풀 안에서 원하는 목록에 맞춘다. 풀 밖의 주소는 건드리지 않는다.
	// 이 작업을 모르는 옛 agent는 거부한다(서버는 그 노드를 "unsupported"로 보이고 건너뛴다).
	OpIPv6Addrs  = "ipv6.addrs"
	OpIPv6Ensure = "ipv6.ensure"
)

// MeshEnsureRequest는 mesh.ensure 요청이다(mesh.Validate로 엄격하게 검사한다).
type MeshEnsureRequest = mesh.Config

// MeshStatus는 mesh.ensure의 결과다.
type MeshStatus = mesh.Status

// ExecRunRequest는 exec.run 요청이다. Env는 KEY=VALUE 목록(비밀번호 등, argv에 넣지 않는다).
// MaxOutput은 stdout 상한(node.MaxExecRunOutput 이하), TimeoutS는 실행 시간 제한(1..120초)이다.
type ExecRunRequest struct {
	Name      string   `json:"name"`
	Cmd       []string `json:"cmd"`
	Env       []string `json:"env,omitempty"`
	Stdin     []byte   `json:"stdin,omitempty"`
	MaxOutput int      `json:"max_output,omitempty"`
	TimeoutS  int      `json:"timeout_s,omitempty"`
}

// ExecRunResult는 node.ExecOutput과 같은 모양이다.
type ExecRunResult = node.ExecOutput

// MaxExecRunTimeoutS는 exec.run 실행 시간 제한의 최댓값이다.
const MaxExecRunTimeoutS = 120

// ExecStartRequest의 Session은 서버가 정한 세션 id다(input/resize/close가 이것으로 세션을 가리킨다).
type ExecStartRequest struct {
	Session string   `json:"session"`
	Name    string   `json:"name"`
	Cmd     []string `json:"cmd"`
	Cols    int      `json:"cols"`
	Rows    int      `json:"rows"`
}

type ExecResult struct {
	ExitCode int `json:"exit_code"`
}

type ExecInputRequest struct {
	Session string `json:"session"`
	Data    []byte `json:"data"`
}

type ExecResizeRequest struct {
	Session string `json:"session"`
	Cols    int    `json:"cols"`
	Rows    int    `json:"rows"`
}

type ExecCloseRequest struct {
	Session string `json:"session"`
}

// MaxExecInputBytes는 exec.input 하나의 최대 크기다.
const MaxExecInputBytes = 64 << 10

// agent → 서버 요청(Message.Up)
const OpWake = "wake"

type PingResult struct {
	AgentVersion  string `json:"agent_version"`
	DockerVersion string `json:"docker_version"`
	CPUCores      int    `json:"cpu_cores"`
	MemoryBytes   uint64 `json:"memory_bytes"`
	DiskFreeBytes uint64 `json:"disk_free_bytes"`
	IPv4          string `json:"ipv4,omitempty"`
	IPv6          string `json:"ipv6,omitempty"`
	Private       string `json:"private,omitempty"`
	Railpack      bool   `json:"railpack"`
	// MeshPort는 mesh gateway가 켜져 있으면 그 포트다(사설 주소에서 listen). 0이면 mesh를 쓸 수 없다(옛 agent 포함).
	MeshPort int `json:"mesh_port,omitempty"`
	// ExternalUpstreams는 이 노드의 keel-proxy가 보낼 수 있는 aven 밖의 upstream이다(agent의 KEEL_EXTERNAL_UPSTREAMS).
	ExternalUpstreams []string `json:"external_upstreams,omitempty"`
}

type CancelRequest struct {
	ID string `json:"id"`
}

type PullRequest struct {
	Image string             `json:"image"`
	Auth  *node.RegistryAuth `json:"auth,omitempty"`
}

type ImageRequest struct {
	Image string `json:"image"`
}

type RepoRequest struct {
	Repo string `json:"repo"`
}

type NetworkRequest struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
}

type VolumeRequest struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
}

type RunRequest struct {
	Spec node.RunSpec `json:"spec"`
}

type RunResult struct {
	ExitCode int `json:"exit_code"`
}

// RunChunk는 run_to_completion의 로그 한 줄이다.
type RunChunk struct {
	Stream string `json:"stream"`
	Line   string `json:"line"`
}

type ContainerRequest struct {
	Name  string `json:"name"`
	Grace int    `json:"grace,omitempty"`
}

type SandboxCommitRequest struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

type NamesRequest struct {
	Names []string `json:"names"`
}

type LogsRequest struct {
	Name   string `json:"name"`
	Tail   string `json:"tail"`
	Since  string `json:"since,omitempty"`
	Follow bool   `json:"follow,omitempty"`
}

// ProbeRequest는 agent가 자기 노드의 aven 컨테이너에 healthcheck를 한 번 하게 한다.
// 주소는 agent가 컨테이너를 조회해 정한다(서버가 임의 주소를 찌르게 하지 않는다).
type ProbeRequest struct {
	Name    string `json:"name"`
	Network string `json:"network"`
	Kind    string `json:"kind"` // http | tcp
	Port    int    `json:"port"`
	Path    string `json:"path,omitempty"`
}

type ProbeResult struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type FetchRequest struct {
	Repo    string `json:"repo"`
	Branch  string `json:"branch"`
	SHA     string `json:"sha,omitempty"`
	Token   string `json:"token,omitempty"`
	RootDir string `json:"root_dir"`
	// SSHKey는 deploy key 개인키(OpenSSH PEM)다. agent는 0600 임시 파일로만 쓰고 로그에 남기지 않는다.
	SSHKey     string `json:"ssh_key,omitempty"`
	KnownHosts string `json:"known_hosts,omitempty"` // 서버가 기록해 둔 호스트 키(내장 github 키는 agent가 더한다)
}

type FetchResult struct {
	Workspace  string      `json:"workspace"`
	Commit     node.Commit `json:"commit"`
	ConfigName string      `json:"config_name,omitempty"`
	Config     []byte      `json:"config,omitempty"`
	KnownHosts string      `json:"known_hosts,omitempty"` // SSH clone 뒤의 known_hosts 전체(새로 기록된 키 포함)
}

type BuildRequest struct {
	Workspace        string            `json:"workspace"`
	RootDir          string            `json:"root_dir"`
	Builder          string            `json:"builder"` // auto | dockerfile | railpack
	DockerfilePath   string            `json:"dockerfile_path"`
	Image            string            `json:"image"`
	Labels           map[string]string `json:"labels"`
	Vars             map[string]string `json:"vars,omitempty"`
	Secrets          []string          `json:"secrets,omitempty"` // Vars 중 BuildKit secret으로만 넘길 이름
	RailpackFrontend string            `json:"railpack_frontend,omitempty"`
}

type BuildResult struct {
	Builder string `json:"builder"`
}

type WorkspaceRequest struct {
	Workspace string `json:"workspace"`
}

type ProxyRequest struct {
	Routes []proxy.Route `json:"routes"`
}

// LineChunk는 pull/build 출력 한 줄이다.
type LineChunk struct {
	Line string `json:"line"`
}

type Peer struct {
	Conn *websocket.Conn
	Mu   sync.Mutex
}

func (p *Peer) Write(ctx context.Context, msg Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	p.Mu.Lock()
	defer p.Mu.Unlock()
	return p.Conn.Write(ctx, websocket.MessageText, data)
}

func Secret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func Hash(s string) string {
	digest := sha256.Sum256([]byte(s))
	return hex.EncodeToString(digest[:])
}

type TCPProxyRequest struct {
	InstanceID    string `json:"instance_id"`
	ProjectID     string `json:"project_id"`
	EnvID         string `json:"env_id"`
	Network       string `json:"network"`
	Target        string `json:"target"` // 환경 네트워크 안의 서비스 alias(<service>.<env>.keel.internal)
	PublicPort    int    `json:"public_port"`
	ContainerPort int    `json:"container_port"`
}

type TCPRemoveRequest struct {
	InstanceID string `json:"instance_id"`
}

// EventChunk는 events.follow의 이벤트 하나다. Labels는 aven.* 라벨만 담는다.
type EventChunk struct {
	Action string            `json:"action"`
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
	Exit   string            `json:"exit"`
}

// DataChunk는 큰 데이터의 한 조각이다. 조각 하나는 MaxChunkBytes 이하(base64 전 기준)다.
type DataChunk struct {
	Seq  int    `json:"seq"`
	Data []byte `json:"data"`
}

// MaxChunkBytes는 chunk 하나의 원본 크기다. base64(×4/3)와 JSON 오버헤드를 더해도 MaxMessageBytes 안에 들어간다.
const MaxChunkBytes = 1 << 20

type VolumeExportRequest struct {
	Name string `json:"name"`
}

type VolumeExportResult struct {
	Bytes int64 `json:"bytes"`
}

const (
	UploadVolume = "volume"
	UploadSource = "source"
	// UploadImage는 다른 노드에서 빌드한 aven 이미지(docker save, gzip 가능)다. 이 작업을 모르는 옛 agent는 거부한다.
	UploadImage = "image"
)

type UploadBeginRequest struct {
	Kind   string `json:"kind"`             // volume | source | db.restore | image
	Volume string `json:"volume,omitempty"` // kind=volume: 채울 볼륨 이름
	// Image는 kind=image: 아카이브에 들어 있어야 하는 유일한 이미지 이름(keel/<service>:<tag>)이다.
	Image string `json:"image,omitempty"`
	// DB는 kind=db.restore: 업로드가 끝나면 이 DB 컨테이너에 복원 도구를 stdin=업로드 데이터로 실행한다.
	DB *DBToolRequest `json:"db,omitempty"`
}

// VolumeSizesResult는 volume.sizes의 결과다(aven 볼륨 이름 → 바이트).
type VolumeSizesResult struct {
	Sizes map[string]int64 `json:"sizes"`
}

type UploadBeginResult struct {
	UploadID string `json:"upload_id"`
}

type UploadChunkRequest struct {
	UploadID string `json:"upload_id"`
	Seq      int    `json:"seq"`
	Data     []byte `json:"data"`
}

type UploadFinishRequest struct {
	UploadID string `json:"upload_id"`
	Bytes    int64  `json:"bytes"` // 서버가 보낸 총 바이트(검증용)
	RootDir  string `json:"root_dir,omitempty"`
}

type UploadAbortRequest struct {
	UploadID string `json:"upload_id"`
}

// NodeStats는 node.stats의 결과다. CPUPercent는 노드 전체 사용률(0..100), MemoryBytes는 사용 중인 메모리다.
type NodeStats struct {
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryBytes   uint64  `json:"memory_bytes"`
	DiskFreeBytes uint64  `json:"disk_free_bytes"`
}

// WakeRequest는 agent가 서버에 "이 host의 잠든 서비스를 깨워 달라"고 한다.
type WakeRequest struct {
	Host string `json:"host"`
}

// WakeResult는 깨어난 서비스로 요청을 넘길 컨테이너다.
type WakeResult struct {
	Network   string `json:"network"`
	Container string `json:"container"`
	Port      int    `json:"port"`
}

// PortsListeningRequest는 ports.listening 요청이다. agent는 이 노드에서 keel.instance 라벨이 InstanceID인 app 컨테이너만 읽는다.
type PortsListeningRequest struct {
	InstanceID string `json:"instance_id"`
}

// PortsListeningResult는 ports.listening의 결과다(replica 전체의 합집합).
type PortsListeningResult = node.ListeningPorts

// EgressStatusRequest는 egress.status 요청이다(환경 ID만, 네트워크와 컨테이너는 agent가 고른다).
type EgressStatusRequest struct {
	EnvID string `json:"env_id"`
}

// EgressStatusResult는 egress.status의 결과다.
type EgressStatusResult = node.EgressStatus

// EgressEnsureRequest는 egress.ensure 요청이다. Blocked는 아웃바운드 IPv6를 막을 instance ID 전체다(노드에 없는 인스턴스는 무시된다).
type EgressEnsureRequest struct {
	Blocked []string `json:"blocked"`
}

// EgressEnsureResult는 egress.ensure의 결과다.
type EgressEnsureResult = node.EgressEnsureResult

// IPv6AddrsResult는 ipv6.addrs의 결과다(요청 본문은 빈 객체).
type IPv6AddrsResult struct {
	Addrs []node.HostIPv6Addr `json:"addrs"`
}

// IPv6EnsureRequest는 ipv6.ensure 요청이다. Addresses는 풀 안에서 이 노드에 붙어 있어야 할 /128 주소 전체다(agent가 다시 검사한다).
// Iface가 비면 agent가 정한다(KEEL_IPV6_IFACE, 아니면 공인 IPv6를 가진 인터페이스나 기본 경로의 인터페이스).
type IPv6EnsureRequest struct {
	Iface     string   `json:"iface,omitempty"`
	Pool      string   `json:"pool"`
	Addresses []string `json:"addresses"`
}

// IPv6EnsureResult는 ipv6.ensure의 결과다.
type IPv6EnsureResult = node.IPv6EnsureResult
