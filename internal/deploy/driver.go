package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/store"
)

// Driver는 한 노드의 컨테이너 런타임이다. 로컬 노드는 node.Docker, 원격 노드는 agent 연결(remoteDriver)이다.
type Driver interface {
	Pull(ctx context.Context, image string, auth *node.RegistryAuth, out func(string)) error
	ImageExists(ctx context.Context, image string) bool
	ImageDigest(ctx context.Context, image string) (string, error)
	ExposedPorts(ctx context.Context, image string) []int
	ImagesByRepo(ctx context.Context, repo string) []string
	RemoveImage(ctx context.Context, image string)
	EnsureNamedNetwork(ctx context.Context, name string, labels map[string]string) error
	RemoveNetwork(ctx context.Context, name string, detach ...string) error
	EnsureVolume(ctx context.Context, name string, labels map[string]string) error
	RemoveVolume(ctx context.Context, name string) error
	Run(ctx context.Context, s node.RunSpec) error
	RunToCompletion(ctx context.Context, s node.RunSpec, out func(stream, line string)) (int, error)
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string, grace int) error
	Restart(ctx context.Context, name string, grace int) error
	Remove(ctx context.Context, name string, grace int) error
	Inspect(ctx context.Context, name string) (*node.ContainerState, error)
	InspectMany(ctx context.Context, names []string) (map[string]*node.ContainerState, error)
	ListManaged(ctx context.Context) (map[string]map[string]string, error)
	TailLogs(ctx context.Context, name string, n int) []string
	FollowLogs(ctx context.Context, name, tail string, follow bool, out func(node.LogLine)) error
	LogsSince(ctx context.Context, name, tail, since string, follow bool, out func(node.LogLine)) error
	Stats(ctx context.Context, names []string) ([]node.Stats, error)
	Probe(ctx context.Context, name, network, kind string, port int, path string) (bool, string)
	// ExportVolume은 볼륨을 tar 스트림으로 w에 쓴다. name은 helper 컨테이너 이름(원격은 agent가 정한다).
	ExportVolume(ctx context.Context, volume, name string, w io.Writer) error
	// ImportVolume은 볼륨을 비우고 r의 tar 스트림을 푼다.
	ImportVolume(ctx context.Context, volume, name string, r io.Reader) error
}

var _ Driver = (*node.Docker)(nil)

// AgentCaller는 원격 노드에 요청을 보내는 hub다(agentprotocol.Hub).
type AgentCaller interface {
	Online(nodeID string) bool
	CallStream(ctx context.Context, nodeID, operation string, request, result any, onChunk func(json.RawMessage)) error
	Reply(ctx context.Context, nodeID string, msg agentprotocol.Message) error
}

var _ AgentCaller = (*agentprotocol.Hub)(nil)

// ErrNodeUnavailable은 배포 대상 노드가 없거나 배포를 받지 않을 때다.
var ErrNodeUnavailable = errors.New("node is not available for deployments")

func isLocal(nodeID string) bool { return nodeID == "" || nodeID == store.LocalNodeID }

// driverForNode는 노드의 드라이버다.
func (m *Manager) driverForNode(nodeID string) Driver {
	if isLocal(nodeID) {
		if m.localDrv != nil {
			return m.localDrv
		}
		return m.Docker
	}
	return &remoteDriver{hub: m.Agents, node: nodeID}
}

// driverFor는 배포가 실행되는 노드의 드라이버다.
func (m *Manager) driverFor(d *store.Deployment) Driver { return m.driverForNode(d.NodeID) }

// nodeOnline은 원격 노드가 연결돼 있는지다(로컬은 항상 true).
func (m *Manager) nodeOnline(nodeID string) bool {
	if isLocal(nodeID) {
		return true
	}
	return m.Agents != nil && m.Agents.Online(nodeID)
}

// remoteDriver는 agent 연결로 원격 노드의 docker를 다룬다. agent는 allowlist에 있는 작업만 받는다.
type remoteDriver struct {
	hub  AgentCaller
	node string
}

func (r *remoteDriver) call(ctx context.Context, op string, req, res any) error {
	return r.stream(ctx, op, req, res, nil)
}

func (r *remoteDriver) stream(ctx context.Context, op string, req, res any, chunk func(json.RawMessage)) error {
	if r.hub == nil {
		return agentprotocol.ErrOffline
	}
	err := r.hub.CallStream(ctx, r.node, op, req, res, chunk)
	if errors.Is(err, agentprotocol.ErrOffline) {
		return fmt.Errorf("node %s: %w", r.node, err)
	}
	return err
}

func lines(out func(string)) func(json.RawMessage) {
	return func(b json.RawMessage) {
		var c agentprotocol.LineChunk
		if out != nil && json.Unmarshal(b, &c) == nil {
			out(c.Line)
		}
	}
}

// withLongDeadline은 deadline 없는 ctx에 긴 상한을 준다(hub 기본 2분 제한 대신).
func withLongDeadline(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d)
}

func (r *remoteDriver) Pull(ctx context.Context, image string, auth *node.RegistryAuth, out func(string)) error {
	ctx, cancel := withLongDeadline(ctx, time.Hour)
	defer cancel()
	return r.stream(ctx, agentprotocol.OpImagePull, agentprotocol.PullRequest{Image: image, Auth: auth}, nil, lines(out))
}

func (r *remoteDriver) ImageExists(ctx context.Context, image string) bool {
	var ok bool
	return r.call(ctx, agentprotocol.OpImageExists, agentprotocol.ImageRequest{Image: image}, &ok) == nil && ok
}

func (r *remoteDriver) ImageDigest(ctx context.Context, image string) (string, error) {
	var digest string
	err := r.call(ctx, agentprotocol.OpImageDigest, agentprotocol.ImageRequest{Image: image}, &digest)
	return digest, err
}

func (r *remoteDriver) ExposedPorts(ctx context.Context, image string) []int {
	var ports []int
	r.call(ctx, agentprotocol.OpImagePorts, agentprotocol.ImageRequest{Image: image}, &ports)
	return ports
}

func (r *remoteDriver) ImagesByRepo(ctx context.Context, repo string) []string {
	var out []string
	r.call(ctx, agentprotocol.OpImageList, agentprotocol.RepoRequest{Repo: repo}, &out)
	return out
}

func (r *remoteDriver) RemoveImage(ctx context.Context, image string) {
	r.call(ctx, agentprotocol.OpImageRemove, agentprotocol.ImageRequest{Image: image}, nil)
}

func (r *remoteDriver) EnsureNamedNetwork(ctx context.Context, name string, labels map[string]string) error {
	return r.call(ctx, agentprotocol.OpNetworkEnsure, agentprotocol.NetworkRequest{Name: name, Labels: labels}, nil)
}

func (r *remoteDriver) RemoveNetwork(ctx context.Context, name string, _ ...string) error {
	return r.call(ctx, agentprotocol.OpNetworkRemove, agentprotocol.NetworkRequest{Name: name}, nil)
}

func (r *remoteDriver) EnsureVolume(ctx context.Context, name string, labels map[string]string) error {
	return r.call(ctx, agentprotocol.OpVolumeEnsure, agentprotocol.VolumeRequest{Name: name, Labels: labels}, nil)
}

func (r *remoteDriver) RemoveVolume(ctx context.Context, name string) error {
	return r.call(ctx, agentprotocol.OpVolumeRemove, agentprotocol.VolumeRequest{Name: name}, nil)
}

func (r *remoteDriver) Run(ctx context.Context, s node.RunSpec) error {
	return r.call(ctx, agentprotocol.OpContainerRun, agentprotocol.RunRequest{Spec: s}, nil)
}

func (r *remoteDriver) RunToCompletion(ctx context.Context, s node.RunSpec, out func(stream, line string)) (int, error) {
	ctx, cancel := withLongDeadline(ctx, 24*time.Hour)
	defer cancel()
	var res agentprotocol.RunResult
	err := r.stream(ctx, agentprotocol.OpContainerRunFg, agentprotocol.RunRequest{Spec: s}, &res, func(b json.RawMessage) {
		var c agentprotocol.RunChunk
		if json.Unmarshal(b, &c) == nil {
			out(c.Stream, c.Line)
		}
	})
	if err != nil {
		return -1, err
	}
	return res.ExitCode, nil
}

func (r *remoteDriver) action(ctx context.Context, op, name string, grace int) error {
	// docker stop -t grace가 hub 기본 제한(2분)보다 길 수 있다(draining_seconds 최대 300초).
	ctx, cancel := withLongDeadline(ctx, time.Duration(grace)*time.Second+time.Minute)
	defer cancel()
	err := r.call(ctx, op, agentprotocol.ContainerRequest{Name: name, Grace: grace}, nil)
	var re *agentprotocol.RemoteError
	if errors.As(err, &re) && re.Msg == node.ErrNoContainer.Error() {
		return node.ErrNoContainer
	}
	return err
}

func (r *remoteDriver) Start(ctx context.Context, name string) error {
	return r.action(ctx, agentprotocol.OpContainerStart, name, 0)
}

func (r *remoteDriver) Stop(ctx context.Context, name string, grace int) error {
	return r.action(ctx, agentprotocol.OpContainerStop, name, grace)
}

func (r *remoteDriver) Restart(ctx context.Context, name string, grace int) error {
	return r.action(ctx, agentprotocol.OpContainerRestrt, name, grace)
}

func (r *remoteDriver) Remove(ctx context.Context, name string, grace int) error {
	return r.action(ctx, agentprotocol.OpContainerRemove, name, grace)
}

func (r *remoteDriver) Inspect(ctx context.Context, name string) (*node.ContainerState, error) {
	m, err := r.InspectMany(ctx, []string{name})
	if err != nil {
		return nil, err
	}
	if st := m[name]; st != nil {
		return st, nil
	}
	return nil, node.ErrNoContainer
}

func (r *remoteDriver) InspectMany(ctx context.Context, names []string) (map[string]*node.ContainerState, error) {
	out := map[string]*node.ContainerState{}
	if len(names) == 0 {
		return out, nil
	}
	err := r.call(ctx, agentprotocol.OpContainerInspct, agentprotocol.NamesRequest{Names: names}, &out)
	if out == nil {
		out = map[string]*node.ContainerState{}
	}
	return out, err
}

func (r *remoteDriver) ListManaged(ctx context.Context) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	err := r.call(ctx, agentprotocol.OpContainerList, struct{}{}, &out)
	return out, err
}

func (r *remoteDriver) TailLogs(ctx context.Context, name string, n int) []string {
	var out []string
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	r.LogsSince(cctx, name, fmt.Sprint(n), "", false, func(l node.LogLine) { out = append(out, l.Msg) })
	return out
}

func (r *remoteDriver) FollowLogs(ctx context.Context, name, tail string, follow bool, out func(node.LogLine)) error {
	return r.LogsSince(ctx, name, tail, "", follow, out)
}

func (r *remoteDriver) LogsSince(ctx context.Context, name, tail, since string, follow bool, out func(node.LogLine)) error {
	ctx, cancel := withLongDeadline(ctx, 24*time.Hour)
	defer cancel()
	return r.stream(ctx, agentprotocol.OpContainerLogs, agentprotocol.LogsRequest{Name: name, Tail: tail, Since: since, Follow: follow}, nil, func(b json.RawMessage) {
		var l node.LogLine
		if json.Unmarshal(b, &l) == nil {
			out(l)
		}
	})
}

func (r *remoteDriver) Stats(ctx context.Context, names []string) ([]node.Stats, error) {
	if len(names) == 0 {
		return nil, nil
	}
	var out []node.Stats
	err := r.call(ctx, agentprotocol.OpContainerStats, agentprotocol.NamesRequest{Names: names}, &out)
	return out, err
}

func (r *remoteDriver) Probe(ctx context.Context, name, network, kind string, port int, path string) (bool, string) {
	var res agentprotocol.ProbeResult
	if err := r.call(ctx, agentprotocol.OpContainerProbe, agentprotocol.ProbeRequest{Name: name, Network: network, Kind: kind, Port: port, Path: path}, &res); err != nil {
		return false, err.Error()
	}
	return res.OK, res.Detail
}

func (r *remoteDriver) applyProxy(ctx context.Context, routes []proxy.Route) error {
	if routes == nil {
		routes = []proxy.Route{}
	}
	return r.call(ctx, agentprotocol.OpProxyApply, agentprotocol.ProxyRequest{Routes: routes}, nil)
}

// fetchSource는 원격 노드에서 저장소를 clone한다(빌드는 그 노드에서 한다).
func (r *remoteDriver) fetchSource(ctx context.Context, req agentprotocol.FetchRequest, out func(string)) (*agentprotocol.FetchResult, error) {
	var res agentprotocol.FetchResult
	if err := r.stream(ctx, agentprotocol.OpSourceFetch, req, &res, lines(out)); err != nil {
		return nil, err
	}
	return &res, nil
}

func (r *remoteDriver) buildImage(ctx context.Context, req agentprotocol.BuildRequest, out func(string)) (string, error) {
	var res agentprotocol.BuildResult
	if err := r.stream(ctx, agentprotocol.OpImageBuild, req, &res, lines(out)); err != nil {
		return "", err
	}
	return res.Builder, nil
}

func (r *remoteDriver) cleanupSource(ctx context.Context, workspace string) {
	r.call(ctx, agentprotocol.OpSourceCleanup, agentprotocol.WorkspaceRequest{Workspace: workspace}, nil)
}

// ExportVolume은 agent가 보낸 tar 조각을 순서대로 w에 쓴다.
func (r *remoteDriver) ExportVolume(ctx context.Context, volume, _ string, w io.Writer) error {
	ctx, cancel := withLongDeadline(ctx, 6*time.Hour)
	defer cancel()
	var res agentprotocol.VolumeExportResult
	var total int64
	next := 0
	var werr error
	err := r.stream(ctx, agentprotocol.OpVolumeExport, agentprotocol.VolumeExportRequest{Name: volume}, &res, func(b json.RawMessage) {
		if werr != nil {
			return
		}
		var c agentprotocol.DataChunk
		if err := json.Unmarshal(b, &c); err != nil {
			werr = err
			return
		}
		if c.Seq != next {
			werr = fmt.Errorf("volume export: chunk %d out of order (want %d)", c.Seq, next)
			return
		}
		next++
		n, err := w.Write(c.Data)
		total += int64(n)
		werr = err
	})
	if werr != nil {
		return werr
	}
	if err != nil {
		return err
	}
	if res.Bytes != total {
		return fmt.Errorf("volume export incomplete: received %d bytes, expected %d", total, res.Bytes)
	}
	return nil
}

// upload는 r의 내용을 agent에 chunk로 올리고 upload.finish의 결과를 res에 담는다.
func (r *remoteDriver) upload(ctx context.Context, begin agentprotocol.UploadBeginRequest, src io.Reader, rootDir string, res any) error {
	var b agentprotocol.UploadBeginResult
	if err := r.call(ctx, agentprotocol.OpUploadBegin, begin, &b); err != nil {
		return err
	}
	abort := func() {
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		r.call(actx, agentprotocol.OpUploadAbort, agentprotocol.UploadAbortRequest{UploadID: b.UploadID}, nil)
		cancel()
	}
	buf := make([]byte, agentprotocol.MaxChunkBytes)
	var total int64
	for seq := 0; ; seq++ {
		n, rerr := io.ReadFull(src, buf)
		if n > 0 {
			if err := r.call(ctx, agentprotocol.OpUploadChunk, agentprotocol.UploadChunkRequest{UploadID: b.UploadID, Seq: seq, Data: buf[:n]}, nil); err != nil {
				abort()
				return err
			}
			total += int64(n)
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			abort()
			return rerr
		}
	}
	fctx, cancel := withLongDeadline(ctx, 6*time.Hour)
	defer cancel()
	if err := r.call(fctx, agentprotocol.OpUploadFinish, agentprotocol.UploadFinishRequest{UploadID: b.UploadID, Bytes: total, RootDir: rootDir}, res); err != nil {
		abort()
		return err
	}
	return nil
}

func (r *remoteDriver) ImportVolume(ctx context.Context, volume, _ string, src io.Reader) error {
	return r.upload(ctx, agentprotocol.UploadBeginRequest{Kind: agentprotocol.UploadVolume, Volume: volume}, src, "", nil)
}

// uploadSource는 CLI 업로드 tar.gz를 노드에 올려 작업 디렉터리로 푼다.
func (r *remoteDriver) uploadSource(ctx context.Context, src io.Reader, rootDir string) (*agentprotocol.FetchResult, error) {
	var res agentprotocol.FetchResult
	if err := r.upload(ctx, agentprotocol.UploadBeginRequest{Kind: agentprotocol.UploadSource}, src, rootDir, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (r *remoteDriver) tcpEnsure(ctx context.Context, req agentprotocol.TCPProxyRequest) error {
	return r.call(ctx, agentprotocol.OpTCPEnsure, req, nil)
}

func (r *remoteDriver) tcpRemove(ctx context.Context, instanceID string) error {
	return r.call(ctx, agentprotocol.OpTCPRemove, agentprotocol.TCPRemoveRequest{InstanceID: instanceID}, nil)
}

func (r *remoteDriver) nodeStats(ctx context.Context) (agentprotocol.NodeStats, error) {
	var s agentprotocol.NodeStats
	err := r.call(ctx, agentprotocol.OpNodeStats, struct{}{}, &s)
	return s, err
}

// ---- 포트 감지(ports.listening) ----

// ErrPortDetectionUnsupported는 노드가 listen 포트 감지를 못 할 때다(ports.listening을 모르는 옛 agent 등).
var ErrPortDetectionUnsupported = errors.New("이 노드의 aven-agent가 포트 감지(ports.listening)를 지원하지 않습니다. agent를 업그레이드하세요")

// portLister는 노드에서 인스턴스의 listen 포트를 읽는 드라이버다. node.Docker와 remoteDriver가 구현한다.
type portLister interface {
	InstanceListeningPorts(ctx context.Context, instanceID string) (*node.ListeningPorts, error)
}

var _ portLister = (*node.Docker)(nil)
var _ portLister = (*remoteDriver)(nil)

// InstanceListeningPorts는 agent의 ports.listening이다. 옛 agent가 작업을 모르면 ErrPortDetectionUnsupported다.
func (r *remoteDriver) InstanceListeningPorts(ctx context.Context, instanceID string) (*node.ListeningPorts, error) {
	var res agentprotocol.PortsListeningResult
	if err := r.call(ctx, agentprotocol.OpPortsListening, agentprotocol.PortsListeningRequest{InstanceID: instanceID}, &res); err != nil {
		if isUnknownOp(err, agentprotocol.OpPortsListening) {
			return nil, ErrPortDetectionUnsupported
		}
		return nil, err
	}
	return &res, nil
}

// isUnknownOp은 agent가 작업 자체를 모른다는 응답인지다(agent.Handle의 `operation "x" is not allowed`).
func isUnknownOp(err error, op string) bool {
	return err != nil && strings.Contains(err.Error(), `operation "`+op+`" is not allowed`)
}

// ListeningPorts는 노드(nodeID)에서 도는 인스턴스의 app 컨테이너가 listen 중인 TCP 포트다(replica 합집합).
// 로컬은 aven-server가 호스트의 /proc를 직접 읽고, 원격은 agent의 ports.listening이다.
func (m *Manager) ListeningPorts(ctx context.Context, nodeID, instanceID string) (*node.ListeningPorts, error) {
	if isLocal(nodeID) && m.localDrv == nil && m.Docker == nil {
		return nil, errors.New("docker is not configured")
	}
	if !m.nodeOnline(nodeID) {
		return nil, fmt.Errorf("node %s: %w", nodeID, agentprotocol.ErrOffline)
	}
	pl, ok := m.driverForNode(nodeID).(portLister)
	if !ok {
		return nil, ErrPortDetectionUnsupported
	}
	return pl.InstanceListeningPorts(ctx, instanceID)
}

// ImageExposedPortsOn은 노드에 있는 이미지가 EXPOSE한 TCP 포트다(없거나 모르면 nil).
func (m *Manager) ImageExposedPortsOn(ctx context.Context, nodeID, image string) []int {
	if image == "" || (isLocal(nodeID) && m.localDrv == nil && m.Docker == nil) || !m.nodeOnline(nodeID) {
		return nil
	}
	return m.driverForNode(nodeID).ExposedPorts(ctx, image)
}

// InspectOn은 노드의 컨테이너 상태다(읽기 전용).
func (m *Manager) InspectOn(ctx context.Context, nodeID, name string) (*node.ContainerState, error) {
	if isLocal(nodeID) && m.localDrv == nil && m.Docker == nil {
		return nil, errors.New("docker is not configured")
	}
	if !m.nodeOnline(nodeID) {
		return nil, fmt.Errorf("node %s: %w", nodeID, agentprotocol.ErrOffline)
	}
	return m.driverForNode(nodeID).Inspect(ctx, name)
}

// RoutePort는 도메인의 target_port가 없을 때 라우트가 보내는 포트다(syncProxy와 같은 routePort 우선순위).
func RoutePort(d *store.Deployment, instPort *int) int {
	return routePort(d, instPort, nil, targetIfPositive)
}

// InstanceContainersOn은 노드에서 인스턴스에 속한 aven 컨테이너(app/tcpproxy/cron, 멈춘 것 포함)의 상태다(읽기 전용).
func (m *Manager) InstanceContainersOn(ctx context.Context, nodeID, instanceID string) (map[string]*node.ContainerState, error) {
	if isLocal(nodeID) && m.localDrv == nil && m.Docker == nil {
		return nil, errors.New("docker is not configured")
	}
	if !m.nodeOnline(nodeID) {
		return nil, fmt.Errorf("node %s: %w", nodeID, agentprotocol.ErrOffline)
	}
	cs, err := m.driverForNode(nodeID).ListManaged(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for name, labels := range cs {
		if labels[node.LabelInstance] == instanceID {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return map[string]*node.ContainerState{}, nil
	}
	return m.driverForNode(nodeID).InspectMany(ctx, names)
}
