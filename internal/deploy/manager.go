// Package deploy는 배포 상태 머신, 인스턴스별 직렬 큐, reconcile 루프를 담당한다.
//
// 배포 단계: queued → initializing → building → pre_deploy → deploying → healthchecking → live.
// 단계마다 타임아웃이 있고, 컷오버 전에는 언제든 취소할 수 있다. 실패한 배포는 트래픽을 받지 않는다.
package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/agentpki"
	"github.com/turin-dev/aven/internal/config"
	"github.com/turin-dev/aven/internal/cronx"
	"github.com/turin-dev/aven/internal/events"
	"github.com/turin-dev/aven/internal/guardrails"
	"github.com/turin-dev/aven/internal/logbook"
	"github.com/turin-dev/aven/internal/mesh"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/store"
)

type Timeouts struct {
	Clone     time.Duration
	Build     time.Duration
	PreDeploy time.Duration
	Start     time.Duration
}

var DefaultTimeouts = Timeouts{
	Clone:     5 * time.Minute,
	Build:     30 * time.Minute,
	PreDeploy: 10 * time.Minute,
	Start:     2 * time.Minute,
}

type Config struct {
	DataDir        string
	BaseDomain     string
	PublicIPv4     string
	PublicIPv6     string
	TCPProxyDomain string // KEEL_TCP_PROXY_DOMAIN. 비우면 base domain → 노드 hostname → IPv6 → IPv4
	NodeHostname   string
	ProxyBind      string // TCP proxy publish 주소. 비우면 IPv4+IPv6 모두
	GitHubToken    string
	// GitHubClientID/GitHubClientSecret는 대시보드 GitHub 연결의 OAuth App이다. 만료되는 사용자 토큰을 refresh token으로 갱신할 때 쓴다.
	GitHubClientID     string
	GitHubClientSecret string
	GitHubOAuthBase    string // 기본 https://github.com
	// ExternalUpstreams는 로컬 keel-proxy가 보낼 수 있는 aven 밖의 upstream(host:port)이다(KEEL_EXTERNAL_UPSTREAMS).
	ExternalUpstreams []string
	// ProxyNetworks는 로컬 keel-proxy를 더 붙일 docker 네트워크다(KEEL_PROXY_NETWORKS, 외부 upstream이 사는 네트워크).
	ProxyNetworks []string
	// SelfHost/SelfUpstream/SelfHeaders: aven-server 자신(KEEL_PUBLIC_URL의 호스트)으로 가는 로컬 proxy 라우트.
	// keel-proxy와 공유하는 볼륨 안 unix socket으로 보내고, 게이트웨이 비밀 헤더를 덮어써서 전달 헤더를 믿게 한다.
	SelfHost         string
	SelfUpstream     string
	SelfHeaders      map[string]string
	Railpack         string // railpack 바이너리 경로
	RailpackFrontend string // BuildKit frontend 이미지
	WakeAddr         string // keel-proxy가 잠든 서비스 요청을 보낼 wake handler 주소(host:port)
	TCPPortMin       int
	TCPPortMax       int
	// PrivateAddress는 로컬 노드의 사설 주소(KEEL_PRIVATE_ADDRESS, Tailscale/WireGuard)다. mesh gateway가 여기서만 listen한다.
	PrivateAddress string
	Mesh           string // KEEL_MESH: on | off | auto(기본: 사설 IP가 있으면 켠다)
	MeshPort       int    // KEEL_MESH_PORT(기본 7443)
	// IPv6Pool은 로컬 노드의 도메인별 공개 IPv6 풀(KEEL_IPV6_POOL, CIDR)이다. 있으면 저장된 노드 값보다 우선한다.
	IPv6Pool string
	// IPv6Iface는 도메인 주소를 붙일 로컬 인터페이스(KEEL_IPV6_IFACE)다. 비우면 PublicIPv6를 가진 인터페이스나 IPv6 기본 경로의 인터페이스.
	IPv6Iface    string
	OTLPEndpoint string // KEEL_OTLP_ENDPOINT: 컨테이너가 span을 보낼 OTLP/HTTP 주소(기본 DefaultOTLPEndpoint)
	// SelfServiceID는 aven-server 자신을 빌드·배포하는 서비스(KEEL_SELF_SERVICE)다. 비우면 self 배포를 쓰지 않는다(docs/selfhost.md).
	SelfServiceID string
	// SelfEnvFile은 aven-server 컨테이너에 넘길 env 파일(KEEL_SELF_ENV_FILE, 기본 selfhost.DefaultEnvFile)이다.
	SelfEnvFile string
}

type Manager struct {
	Store    *store.Store
	Docker   *node.Docker // 로컬 노드
	Proxy    *proxy.Caddy // 로컬 노드의 keel-proxy
	Agents   AgentCaller  // 원격 노드 연결(nil이면 원격 노드 없음)
	Logs     *logbook.Registry
	Events   *events.Bus
	Cfg      Config
	Timeouts Timeouts
	Log      *slog.Logger
	HTTPLogs *HTTPLogHub
	// ghRefresh는 GitHub 토큰 갱신을 한 번에 하나씩 한다(refresh token은 한 번만 쓸 수 있다).
	ghRefresh sync.Mutex
	// NetworkLogs는 노드 수집기의 DNS/flow 기록을 보관하고 로그 화면과 CLI에 보낸다.
	NetworkLogs *NetworkLogHub

	WorkDir string

	// StatusHook은 배포 상태가 바뀔 때(생성, 시작, 끝) 배포 스냅샷으로 불린다(GitHub commit status 등).
	// 배포 경로를 막지 않도록 빨리 돌아와야 한다(느린 작업은 hook이 비동기로 처리한다).
	StatusHook func(d store.Deployment)
	// EnqueueHook은 배포를 큐에 넣을 때(건너뛴 배포 제외) 요청 ctx와 함께 불린다(API의 감사 기록 등). 빨리 돌아와야 한다.
	EnqueueHook func(ctx context.Context, d *store.Deployment)
	// Dispatch가 있으면 큐에 넣을 배포를 worker 대신 이 함수로 넘긴다(테스트용).
	Dispatch func(instanceID, depID string)

	// MeshCA는 로컬 노드의 mesh 인증서를 발급하고 상대 노드 인증서를 확인하는 aven CA다. LocalMesh는 로컬 mesh gateway다(nil이면 꺼짐).
	// IPRunner는 로컬 노드에 도메인별 IPv6 주소를 붙이는 ip 명령이다(cmd/aven-server가 node.SystemIPRunner()로 채운다).
	// nil이면 로컬 노드에서는 주소를 붙이지 않는다(테스트가 실수로 호스트 주소를 바꾸지 않게 기본은 nil).
	IPRunner node.IPRunner

	MeshCA      *agentpki.CA
	LocalMesh   *mesh.Gateway
	meshSt      meshState
	proxyCompat proxyCompat // 원격 노드: 도메인 확인 필드(CheckToken/Unverified)를 모르는 옛 agent(proxycompat.go)

	mu        sync.Mutex
	queues    map[string]chan string             // instanceID → 대기 중인 deployment ID
	cancels   map[string]context.CancelCauseFunc // deploymentID → 취소
	drain     map[string]bool                    // 컷오버 후 종료 대기 중이거나 aven이 직접 멈추는 컨테이너
	paused    map[string]bool                    // instanceID → reconcile 잠시 중지(백업 복원 등)
	cronBusy  map[string]bool                    // instanceID → cron 실행 중
	awake     map[string]time.Time               // instanceID → 마지막으로 깨어난(배포/wake/서버 시작) 시각
	waking    map[string]chan struct{}           // instanceID → 진행 중인 wake
	sleeping  map[string]chan struct{}           // instanceID → 진행 중인 sleep
	proxyMu   sync.Mutex
	networkMu sync.Mutex

	streamCtx context.Context               // 노드 스트림(access log, 이벤트)의 부모 ctx
	streams   map[string]context.CancelFunc // nodeID → 그 노드의 스트림 취소

	scaleMu    sync.Mutex
	scaleLocks map[string]*sync.Mutex // instanceID → replica 수 변경 직렬화
	autoscale  autoscaleState
	egressSt   egressState // 아웃바운드 IPv6 상태 캐시와 차단 규칙 동기화(egress.go)
	ipv6St     ipv6State   // 도메인별 공개 IPv6 반영 상태(ipv6addr.go)

	// 테스트용: localDrv가 있으면 로컬 노드 드라이버 대신 쓰고, localProxy가 있으면 로컬 keel-proxy 대신 라우트를 넘긴다.
	localDrv   Driver
	localProxy func(ctx context.Context, routes []proxy.Route) error
	selfDrv    selfDocker // 테스트용: upgrader 컨테이너를 다루는 docker 명령
	selfID     string     // 대시보드에서 정한 self 서비스(settings.self_service). mu로 보호한다
}

var (
	errCancelled = errors.New("cancelled by user")
	errSkipped   = errors.New("skipped")
)

func (m *Manager) Init(ctx context.Context) error {
	m.queues = map[string]chan string{}
	m.cancels = map[string]context.CancelCauseFunc{}
	m.drain = map[string]bool{}
	m.paused = map[string]bool{}
	m.cronBusy = map[string]bool{}
	m.awake = map[string]time.Time{}
	m.waking = map[string]chan struct{}{}
	m.sleeping = map[string]chan struct{}{}
	m.streamCtx = ctx
	m.streams = map[string]context.CancelFunc{}
	if m.Timeouts == (Timeouts{}) {
		m.Timeouts = DefaultTimeouts
	}
	if m.Cfg.TCPPortMin == 0 {
		m.Cfg.TCPPortMin, m.Cfg.TCPPortMax = 20000, 29999
	}
	if m.HTTPLogs == nil {
		m.HTTPLogs = NewHTTPLogHub(500)
	}
	if m.NetworkLogs == nil {
		m.NetworkLogs = NewNetworkLogHub(m.Store, m.Log)
		go m.NetworkLogs.Run(ctx)
	}
	if m.WorkDir == "" {
		m.WorkDir = filepath.Join(m.Cfg.DataDir, "work")
	}
	for _, d := range []string{m.WorkDir, m.backupDir(), m.cronLogDir(), m.runtimeLogDir(), m.uploadDir()} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return err
		}
	}
	m.loadSelfService(ctx)
	// self 배포의 교체 결과를 먼저 반영한다(새 aven-server로 바뀌었으면 그 배포는 실패가 아니라 live다).
	m.finishSelfDeploy(ctx)
	// 재시작 전에 진행 중이던 배포는 실패 처리한다. 이전 live 컨테이너는 손대지 않았으므로 그대로 서비스된다.
	unfinished, err := m.Store.ListUnfinished(ctx)
	if err != nil {
		return err
	}
	for _, d := range unfinished {
		m.finish(ctx, d, store.DepFailed, "server restarted during deployment")
		for _, g := range d.Groups() {
			for _, c := range g.Containers {
				if !m.isCurrent(ctx, d) && m.nodeOnline(g.NodeID) {
					m.driverForNode(g.NodeID).Remove(ctx, c, 5)
				}
			}
		}
	}
	insts, err := m.Store.ListInstances(ctx)
	if err != nil {
		return err
	}
	for _, in := range insts {
		if in.Status == store.InstDeploying {
			st := store.InstFailed
			if in.CurrentDeploymentID != nil {
				st = store.InstOnline
				if in.CronSchedule != nil {
					st = store.InstScheduled
				}
			}
			m.Store.SetInstanceStatus(ctx, in.ID, st, nil)
		}
	}
	m.Store.FailRunningBackups(ctx)
	m.Store.FailRunningDBBackups(ctx)
	runs, _ := m.Store.ListRunningCron(ctx)
	for _, r := range runs {
		r.Status = "failed"
		m.Store.FinishCronRun(ctx, r)
		m.Docker.Remove(ctx, r.Container, 1)
	}
	return nil
}

func (m *Manager) isCurrent(ctx context.Context, d *store.Deployment) bool {
	in, err := m.Store.GetInstance(ctx, d.InstanceID)
	return err == nil && in.CurrentDeploymentID != nil && *in.CurrentDeploymentID == d.ID
}

// EnqueueOpts는 배포를 만드는 방법이다.
type EnqueueOpts struct {
	Trigger      string
	Image        *string           // 있으면 빌드를 건너뛴다(redeploy/rollback)
	ForceBuild   bool              // true면 skipped builds(보존된 이미지 재사용)를 쓰지 않고 새로 빌드한다
	Spec         *store.Spec       // nil이면 인스턴스 현재 설정
	ReqSpec      *store.Spec       // pending_changes 비교 기준. nil이면 Spec
	VarsRaw      map[string]string // nil이면 인스턴스 현재 변수
	Source       string            // "upload:<path>"
	Commit       *node.Commit      // rollback/redeploy 때 원래 커밋 정보
	Branch       *string
	BuilderUsed  *string
	ChangedFiles []string
	Status       store.DeployStatus // 비우면 queued. skipped면 큐에 넣지 않는다
	Error        string

	// ImageUpdateID가 있으면 이 배포를 이미지 업데이트 기록(image_updates)에 연결한다.
	ImageUpdateID string
}

// Enqueue는 새 배포를 만들고 인스턴스 큐에 넣는다.
func (m *Manager) Enqueue(ctx context.Context, in *store.Instance, o EnqueueOpts) (*store.Deployment, error) {
	if err := m.checkDeploymentSource(ctx, in, o); err != nil {
		return nil, err
	}
	if o.Status != store.DepSkipped {
		if err := m.checkSuspended(ctx, in); err != nil { // 사용량 한도 정지(usagelimit.go)
			return nil, err
		}
		if err := m.checkDeployBlocked(ctx, in); err != nil {
			return nil, err
		}
	}
	var n *store.Node
	var err error
	if isLocal(in.NodeID) {
		n, err = m.Store.LocalNode(ctx)
	} else {
		n, err = m.Store.GetNode(ctx, in.NodeID)
	}
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("%w: node %s does not exist", ErrNodeUnavailable, in.NodeID)
	}
	if err != nil {
		return nil, err
	}
	if !n.Schedulable {
		return nil, fmt.Errorf("%w: node %s is not schedulable", ErrNodeUnavailable, n.Name)
	}
	sp := in.Spec
	if o.Spec != nil {
		sp = *o.Spec
	}
	for _, p := range sp.Placement {
		if nodeKey(p.NodeID) == nodeKey(in.NodeID) {
			continue
		}
		var pn *store.Node
		if isLocal(p.NodeID) {
			pn, err = m.Store.LocalNode(ctx)
		} else {
			pn, err = m.Store.GetNode(ctx, p.NodeID)
		}
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("%w: placement node %s does not exist", ErrNodeUnavailable, p.NodeID)
		}
		if err != nil {
			return nil, err
		}
		if !pn.Schedulable {
			return nil, fmt.Errorf("%w: placement node %s is not schedulable", ErrNodeUnavailable, pn.Name)
		}
	}
	vr := in.Variables
	if o.VarsRaw != nil {
		vr = o.VarsRaw
	}
	d := &store.Deployment{ServiceID: in.ServiceID, EnvironmentID: in.EnvironmentID, InstanceID: in.ID, Trigger: o.Trigger,
		Image: o.Image, Spec: sp, ReqSpec: sp, VarsRaw: vr, Source: o.Source, ChangedFiles: o.ChangedFiles, Status: o.Status, BuilderUsed: o.BuilderUsed, NodeID: in.NodeID,
		ForceBuild: o.ForceBuild}
	if o.Commit != nil {
		d.CommitSHA, d.CommitMessage, d.CommitAuthor = strPtr(o.Commit.SHA), strPtr(o.Commit.Message), strPtr(o.Commit.Author)
	}
	d.Branch = o.Branch
	if d.Branch == nil && sp.RepoURL != nil {
		b := sp.Branch
		d.Branch = &b
	}
	if o.ReqSpec != nil {
		d.ReqSpec = *o.ReqSpec
	}
	if o.Error != "" {
		d.Error = &o.Error
	}
	if o.Status == store.DepSkipped {
		now := time.Now().UTC()
		d.FinishedAt = &now
	}
	if err := m.Store.CreateDeployment(ctx, d); err != nil {
		return nil, err
	}
	if o.ImageUpdateID != "" {
		if err := m.Store.SetImageUpdateDeployment(ctx, o.ImageUpdateID, d.ID); err != nil {
			m.finish(ctx, d, store.DepFailed, err.Error())
			return nil, err
		}
	}
	// function 서비스는 배포를 만들 때의 코드를 스냅샷으로 남긴다(pending_changes와 재빌드 기준). 다른 서비스는 복사할 행이 없다.
	if err := m.Store.SnapshotFunctionCode(ctx, d); err != nil && m.Log != nil {
		m.Log.Warn("function snapshot", "deployment", d.ID, "err", err)
	}
	if d.Status == store.DepSkipped {
		if t, err := m.Load(ctx, in.ID); err == nil {
			message := fmt.Sprintf("%s: push가 watch paths에 맞지 않아 배포를 건너뜁니다", t.Service.Name)
			if t.Env.Kind == store.EnvPreview {
				message = fmt.Sprintf("%s: PR 변경 파일이 서비스 경로에 맞지 않아 base 환경을 사용합니다", t.Service.Name)
			}
			m.emit(ctx, t, "deploy.skipped", message)
		}
		return d, nil
	}
	if m.EnqueueHook != nil {
		m.EnqueueHook(ctx, d)
	}
	// 새 배포가 생기면 같은 인스턴스에서 CI를 기다리던 이전 배포는 의미가 없다(나중에 풀리면 오래된 코드를 배포하게 된다).
	m.supersedeWaiting(ctx, in.ID, d.ID)
	m.notifyStatus(d)
	if d.Status == store.DepWaiting {
		if t, err := m.Load(ctx, in.ID); err == nil {
			m.emit(ctx, t, "deploy.waiting", fmt.Sprintf("%s (%s): CI 결과를 기다립니다%s", t.Service.Name, t.Env.Name, commitSuffix(d)))
		}
		return d, nil
	}
	m.dispatch(in.ID, d.ID)
	return d, nil
}

// checkDeploymentSource는 "승인된 출처에서만 배포" guardrail이다(새 배포마다, admin 포함). 건너뛴 배포 기록은 검사하지 않는다.
func (m *Manager) checkDeploymentSource(ctx context.Context, in *store.Instance, o EnqueueOpts) error {
	if o.Status == store.DepSkipped {
		return nil
	}
	p, err := m.Store.AccessControlSettings(ctx)
	if err != nil || !p.RestrictDeploymentSources {
		return err
	}
	svc, err := m.Store.GetService(ctx, in.ServiceID)
	if err != nil {
		return err
	}
	src := guardrails.SourceOf(svc, &in.Spec)
	src.Uploaded = strings.HasPrefix(o.Source, "upload:")
	return guardrails.Check(p, src)
}

// dispatch는 배포를 인스턴스 큐에 넣는다.
func (m *Manager) dispatch(instanceID, depID string) {
	if m.Dispatch != nil {
		m.Dispatch(instanceID, depID)
		return
	}
	m.mu.Lock()
	q, ok := m.queues[instanceID]
	if !ok {
		q = make(chan string, 64)
		m.queues[instanceID] = q
		go m.worker(q)
	}
	m.mu.Unlock()
	q <- depID
}

// supersedeWaiting은 인스턴스의 CI 대기 배포 중 keep이 아닌 것을 removed로 끝낸다.
func (m *Manager) supersedeWaiting(ctx context.Context, instanceID, keep string) {
	ws, err := m.Store.ListWaitingByInstance(ctx, instanceID)
	if err != nil {
		return
	}
	msg := "superseded by a newer deployment"
	for _, w := range ws {
		if w.ID == keep {
			continue
		}
		if ok, _ := m.Store.TransitionDeployment(ctx, w.ID, store.DepWaiting, store.DepRemoved, &msg); ok {
			w.Status, w.Error = store.DepRemoved, &msg
			m.notifyStatus(w)
		}
	}
}

// ReleaseWaiting은 CI가 통과한 waiting 배포를 큐에 넣는다. 이미 처리된 배포면 false.
func (m *Manager) ReleaseWaiting(ctx context.Context, depID string) (bool, error) {
	ok, err := m.Store.TransitionDeployment(ctx, depID, store.DepWaiting, store.DepQueued, nil)
	if err != nil || !ok {
		return false, err
	}
	d, err := m.Store.GetDeployment(ctx, depID)
	if err != nil {
		return false, err
	}
	m.notifyStatus(d)
	m.dispatch(d.InstanceID, d.ID)
	return true, nil
}

// SkipWaiting은 CI가 실패한 waiting 배포를 skipped로 끝낸다. 이미 처리된 배포면 false.
func (m *Manager) SkipWaiting(ctx context.Context, depID, reason string) (bool, error) {
	ok, err := m.Store.TransitionDeployment(ctx, depID, store.DepWaiting, store.DepSkipped, &reason)
	if err != nil || !ok {
		return false, err
	}
	d, err := m.Store.GetDeployment(ctx, depID)
	if err != nil {
		return false, err
	}
	m.notifyStatus(d)
	if t, err := m.Load(ctx, d.InstanceID); err == nil {
		m.emit(ctx, t, "deploy.skipped", fmt.Sprintf("%s (%s): %s — 배포하지 않습니다%s", t.Service.Name, t.Env.Name, reason, commitSuffix(d)))
	}
	return true, nil
}

// notifyStatus는 StatusHook에 배포 스냅샷을 넘긴다.
func (m *Manager) notifyStatus(d *store.Deployment) {
	if m.StatusHook == nil || d == nil {
		return
	}
	m.StatusHook(*d)
}

// setCommit은 clone한 커밋을 배포에 기록한다. 배포에 SHA가 없었으면(수동 배포 등) 이제야 commit status를 올릴 수 있으므로
// 진행 중 상태를 한 번 알린다(initializing 때는 SHA가 없어 건너뛰었으니, 안 하면 GitHub에 끝 상태만 올라간다).
func (m *Manager) setCommit(ctx context.Context, d *store.Deployment, c *node.Commit) {
	hadSHA := d.CommitSHA != nil
	d.CommitSHA, d.CommitMessage, d.CommitAuthor = strPtr(c.SHA), strPtr(c.Message), strPtr(c.Author)
	br := d.Spec.Branch
	d.Branch = &br
	m.Store.UpdateDeployment(context.WithoutCancel(ctx), d)
	if !hadSHA && d.CommitSHA != nil {
		m.notifyStatus(d)
	}
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// worker는 인스턴스마다 하나씩 돌며 배포를 순서대로 처리한다.
// 큐에 더 새로운 배포가 쌓여 있으면 오래된 것은 removed로 건너뛴다.
func (m *Manager) worker(q chan string) {
	for id := range q {
	drain:
		for {
			select {
			case newer := <-q:
				if d, err := m.Store.GetDeployment(context.Background(), id); err == nil && d.Status == store.DepQueued {
					m.finish(context.Background(), d, store.DepRemoved, "superseded by a newer deployment")
				}
				id = newer
			default:
				break drain
			}
		}
		m.execute(id)
	}
}

func (m *Manager) Cancel(ctx context.Context, depID string) error {
	m.mu.Lock()
	cancel := m.cancels[depID]
	m.mu.Unlock()
	if cancel != nil {
		cancel(errCancelled)
		return nil
	}
	d, err := m.Store.GetDeployment(ctx, depID)
	if err != nil {
		return err
	}
	if d.Status == store.DepQueued {
		m.finish(ctx, d, store.DepCancelled, "")
	}
	if d.Status == store.DepWaiting {
		if ok, _ := m.Store.TransitionDeployment(ctx, d.ID, store.DepWaiting, store.DepCancelled, nil); ok {
			d.Status = store.DepCancelled
			m.notifyStatus(d)
		}
	}
	return nil
}

func (m *Manager) setStatus(ctx context.Context, d *store.Deployment, b *logbook.Book, s store.DeployStatus) {
	d.Status = s
	m.Store.UpdateDeployment(context.WithoutCancel(ctx), d)
	if b != nil {
		b.Status(string(s))
		b.Printf("==> %s", s)
	}
	if s == store.DepInitializing { // 진행 중 단계는 처음 한 번만 알린다
		m.notifyStatus(d)
	}
}

func (m *Manager) finish(ctx context.Context, d *store.Deployment, s store.DeployStatus, errMsg string) {
	now := time.Now().UTC()
	d.Status, d.FinishedAt = s, &now
	if errMsg != "" {
		d.Error = &errMsg
	}
	m.Store.UpdateDeployment(context.WithoutCancel(ctx), d)
	m.finishImageUpdate(ctx, d, s, errMsg, now)
	m.notifyStatus(d)
}

func (m *Manager) emit(ctx context.Context, t *Target, kind, msg string) {
	if m.Events == nil {
		return
	}
	m.Events.Emit(ctx, t.Project.ID, &t.Env.ID, &t.Service.ID, kind, msg)
}

func containerName(svc string, depID string, i int) string {
	return fmt.Sprintf("aven-%s-%s-%d", svc, strings.TrimPrefix(depID, "dep_")[:8], i+1)
}

func (m *Manager) execute(depID string) {
	base := context.Background()
	d, err := m.Store.GetDeployment(base, depID)
	if err != nil || d.Status != store.DepQueued {
		return
	}
	t, err := m.Load(base, d.InstanceID)
	if err != nil {
		m.finish(base, d, store.DepFailed, "service not found")
		return
	}
	book, err := m.Logs.Open(d.ID)
	if err != nil {
		m.finish(base, d, store.DepFailed, "open log: "+err.Error())
		return
	}
	ctx, cancel := context.WithCancelCause(base)
	m.mu.Lock()
	m.cancels[d.ID] = cancel
	m.mu.Unlock()
	defer func() {
		cancel(nil)
		m.mu.Lock()
		delete(m.cancels, d.ID)
		m.mu.Unlock()
		book.Close(string(d.Status))
		m.Logs.Release(d.ID)
	}()

	now := time.Now().UTC()
	d.StartedAt = &now
	prevStatus := t.Instance.Status
	m.Store.SetInstanceStatus(base, t.Instance.ID, store.InstDeploying, nil)
	m.emit(events.WithDeployment(base, d.ID), t, "deploy.started", fmt.Sprintf("%s: 배포 시작 (%s)", t.Service.Name, d.Trigger))

	var stopped stoppedContainers // recreate 전략에서 먼저 멈춘 이전 컨테이너
	err = m.run(ctx, t, d, book, &stopped)
	if err == nil {
		m.finish(base, d, store.DepLive, "")
		m.syncMeshAsync() // 새 서비스나 노드 이동을 다른 노드의 mesh에 반영

		m.syncEgressAsync()     // 새 컨테이너 주소에 아웃바운드 IPv6 차단 규칙 반영
		m.syncDomainIPv6Async() // 노드를 옮긴 서비스의 도메인 IPv6를 새 노드 풀에서 다시 배정
		return
	}
	final, msg := store.DepFailed, err.Error()
	switch {
	case errors.Is(err, errSkipped):
		book.Printf("push가 watch patterns에 맞지 않아 배포를 건너뜁니다")
		final, msg = store.DepSkipped, ""
	case errors.Is(err, errMoved):
		final, msg = store.DepRemoved, "deploy.region: redeploying on another node"
	case errors.Is(context.Cause(ctx), errCancelled):
		book.Printf("사용자가 배포를 취소했습니다")
		final, msg = store.DepCancelled, ""
	default:
		book.Printf("ERROR: %v", err)
	}
	// 실패하거나 취소되면 새 컨테이너만 지운다. 라우트는 아직 이전 컨테이너를 가리킨다.
	m.removeContainers(base, d, d.AllContainers(), 2)
	for _, c := range stopped.names {
		book.Printf("이전 컨테이너 %s를 다시 시작합니다", c)
		stopped.drv.Start(base, c)
		m.undrain(c)
	}
	// 인스턴스 상태를 먼저 되돌린 뒤 배포를 끝낸다(배포가 끝났다고 보이는 시점에 인스턴스도 정리돼 있게).
	// 그사이 Stop/Delete가 됐을 수 있으니 최신 상태를 다시 읽는다.
	cur, cerr := m.Store.GetInstance(base, t.Instance.ID)
	switch {
	case cerr != nil || cur.Status == store.InstOffline:
	case cur.CurrentDeploymentID != nil && (prevStatus == store.InstOnline || prevStatus == store.InstScheduled || prevStatus == store.InstCrashed || prevStatus == store.InstDeploying || prevStatus == store.InstSleeping):
		book.Printf("이전 배포 %s가 계속 서비스 중입니다", *cur.CurrentDeploymentID)
		st := prevStatus
		if st == store.InstDeploying {
			st = store.InstOnline
		}
		m.Store.SetInstanceStatus(base, cur.ID, st, nil)
	case prevStatus == store.InstOffline:
		m.Store.SetInstanceStatus(base, cur.ID, store.InstOffline, nil)
	case final == store.DepSkipped || final == store.DepCancelled || final == store.DepRemoved:
		st := prevStatus
		if st == store.InstDeploying || st == "" {
			st = store.InstIdle
		}
		m.Store.SetInstanceStatus(base, cur.ID, st, nil)
	default:
		m.Store.SetInstanceStatus(base, cur.ID, store.InstFailed, nil)
	}
	m.finish(base, d, final, msg)
	if final == store.DepFailed {
		m.emit(events.WithDeployment(base, d.ID), t, "deploy.failed", fmt.Sprintf("%s (%s) 배포 실패: %v", t.Service.Name, t.Env.Name, err))
	}
}

func stage(parent context.Context, timeout time.Duration, name string, f func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeoutCause(parent, timeout, fmt.Errorf("%s timed out after %s", name, timeout))
	defer cancel()
	err := f(ctx)
	if err != nil && ctx.Err() != nil && parent.Err() == nil {
		if c := context.Cause(ctx); c != nil && !errors.Is(err, c) {
			return fmt.Errorf("%w: %v", c, err)
		}
		return context.Cause(ctx)
	}
	return err
}

func (m *Manager) imageRepo(svcID string) string {
	return "keel/" + strings.ReplaceAll(svcID, "_", "-")
}

// stoppedContainers는 recreate 전략에서 먼저 멈춘 이전 배포의 컨테이너와 그 노드 드라이버다.
type stoppedContainers struct {
	drv   Driver
	names []string
}

func (m *Manager) run(ctx context.Context, t *Target, d *store.Deployment, b *logbook.Book, stopped *stoppedContainers) error {
	b.Printf("배포 %s 시작 (service=%s, environment=%s, trigger=%s)", d.ID, t.Service.Name, t.Env.Name, d.Trigger)
	m.setStatus(ctx, d, b, store.DepInitializing)
	self := m.isSelf(t)
	if self {
		if err := m.checkSelfPlacement(d); err != nil {
			return err
		}
	}
	save := func() { m.Store.UpdateDeployment(context.WithoutCancel(ctx), d) }
	drv := m.driverFor(d)
	remote := !isLocal(d.NodeID)
	if remote {
		n, err := m.Store.GetNode(ctx, d.NodeID)
		if err != nil {
			return fmt.Errorf("node %s: %w", d.NodeID, err)
		}
		if !m.nodeOnline(d.NodeID) {
			return fmt.Errorf("node %s is offline", n.Name)
		}
		b.Printf("원격 노드 %s(%s)에 배포합니다", n.Name, n.ID)
	}
	// function 소스는 배포 스냅샷 코드로 빌드 컨텍스트(tar.gz)를 만들어 업로드 빌드 경로를 그대로 쓴다(DB의 source는 그대로 "").
	if d.Image == nil && t.Service.SourceType == "function" && d.Source == "" {
		p, err := m.functionUpload(ctx, d, b)
		if err != nil {
			return err
		}
		d.Source = "upload:" + p
	}
	// 업로드 소스는 배포가 끝날 때 지운다(placement의 다른 노드에도 같은 tarball을 보내야 할 수 있다).
	var upload string
	if d.Image == nil && strings.HasPrefix(d.Source, "upload:") {
		upload = strings.TrimPrefix(d.Source, "upload:")
		defer os.Remove(upload)
	}

	// build_node=local: 원격 노드 배포도 여기서 빌드하고 이미지를 대상 노드로 보낸다.
	buildHere := buildsLocally(t, d)
	if buildHere {
		b.Printf("build_node=local: 이 노드(aven-server)에서 빌드하고 이미지를 노드 %s로 보냅니다", d.NodeID)
	}
	shipped := ""
	if d.Image == nil && remote && !buildHere && strings.HasPrefix(d.Source, "upload:") {
		if err := m.remoteUploadBuild(ctx, t, d, b, drv.(*remoteDriver), strings.TrimPrefix(d.Source, "upload:")); err != nil {
			return err
		}
	} else if d.Image == nil && remote && !buildHere && t.Service.SourceType == "repo" {
		if err := m.remoteBuild(ctx, t, d, b, drv.(*remoteDriver)); err != nil {
			return err
		}
	} else if d.Image == nil {
		dir := filepath.Join(m.WorkDir, d.ID)
		defer os.RemoveAll(dir)
		srcDir := ""
		switch {
		case strings.HasPrefix(d.Source, "upload:"):
			path := strings.TrimPrefix(d.Source, "upload:")
			b.Printf("업로드된 소스를 풉니다")
			if err := extractTarGz(path, filepath.Join(dir, "src")); err != nil {
				return fmt.Errorf("extract upload: %w", err)
			}
			srcDir = filepath.Join(dir, "src")
		case t.Service.SourceType == "repo":
			if d.Spec.RepoURL == nil || *d.Spec.RepoURL == "" {
				return errors.New("repo_url이 설정되지 않았습니다")
			}
			b.Printf("git clone %s (%s)", *d.Spec.RepoURL, d.Spec.Branch)
			var c *node.Commit
			err := stage(ctx, m.Timeouts.Clone, "clone", func(ctx context.Context) (err error) {
				sha := ""
				if d.Trigger == "github" && d.CommitSHA != nil {
					sha = *d.CommitSHA
				}
				c, err = m.cloneRepo(ctx, t.Service.ID, *d.Spec.RepoURL, d.Spec.Branch, sha, filepath.Join(dir, "src"), b)
				return err
			})
			if err != nil {
				return err
			}
			m.setCommit(ctx, d, c)
			b.Printf("commit %s %q by %s", short(c.SHA), c.Message, c.Author)
			srcDir = filepath.Join(dir, "src")
		default: // image | database
			if d.Spec.Image == nil || *d.Spec.Image == "" {
				return errors.New("image가 설정되지 않았습니다")
			}
			img := *d.Spec.Image
			m.setStatus(ctx, d, b, store.DepBuilding)
			b.Printf("docker pull %s", img)
			if err := stage(ctx, m.Timeouts.Build, "pull", func(ctx context.Context) error {
				return drv.Pull(ctx, img, m.registryAuth(ctx, t.Project.ID, img, b), b.Line)
			}); err != nil {
				if !drv.ImageExists(ctx, img) {
					return err
				}
				b.Printf("WARN: pull 실패, 로컬 이미지를 씁니다: %v", err)
			}
			d.Image = &img
			bu := "image"
			d.BuilderUsed = &bu
			save()
		}
		if srcDir != "" {
			if err := m.build(ctx, t, d, b, srcDir, dir); err != nil {
				return err
			}
			if buildHere {
				if err := m.shipImage(ctx, b, *d.Image, d.NodeID); err != nil {
					return err
				}
				shipped = *d.Image
			}
		}
	} else if !drv.ImageExists(ctx, *d.Image) {
		if buildHere && m.driverForNode(store.LocalNodeID).ImageExists(ctx, *d.Image) {
			// 재배포/롤백: 로컬에 남아 있는 빌드 이미지를 다시 보낸다.
			if err := m.shipImage(ctx, b, *d.Image, d.NodeID); err != nil {
				return err
			}
		} else if d.BuilderUsed != nil && *d.BuilderUsed == "image" {
			b.Printf("docker pull %s", *d.Image)
			if err := drv.Pull(ctx, *d.Image, m.registryAuth(ctx, t.Project.ID, *d.Image, b), b.Line); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("이미지 %s가 더 이상 존재하지 않습니다", *d.Image)
		}
	} else {
		b.Printf("기존 이미지 %s를 사용합니다", *d.Image)
	}
	if len(extraPlacementNodes(d)) > 0 {
		if err := m.prepareExtraImages(ctx, t, d, b, upload); err != nil {
			return err
		}
	}
	// 로컬에서 빌드해 보낸 이미지는 로컬에서 지운다(로컬 노드에 replica가 없으면). 디스크는 빌드 노드에만 남는다.
	if shipped != "" && !placesOn(d, store.LocalNodeID) {
		m.driverForNode(store.LocalNodeID).RemoveImage(context.WithoutCancel(ctx), shipped)
		b.Printf("로컬 빌드 이미지 %s를 지웠습니다(노드 %s에 있습니다)", shipped, d.NodeID)
	}
	if self {
		// 컨테이너를 띄우는 대신 keel-selfupgrade가 aven-server 컨테이너를 교체한다. 성공하면 여기로 돌아오지 않는다.
		return m.selfDeploy(ctx, t, d, b)
	}

	// port를 정하지 않았고 PORT 변수도 없으면 이미지가 EXPOSE한 포트를 쓴다.
	if d.Spec.Port == nil {
		if _, ok := d.VarsRaw["PORT"]; !ok {
			if ports := drv.ExposedPorts(ctx, *d.Image); len(ports) > 0 {
				p := ports[0]
				d.Spec.Port = &p
				b.Printf("port가 없어 이미지의 EXPOSE 포트 %d를 씁니다", p)
				save()
			}
		}
	}

	// 변수 해석(스냅샷 + 이 배포의 주입 변수)
	res, err := m.Resolve(ctx, t.Instance, &Override{InstanceID: t.Instance.ID, Vars: d.VarsRaw, Spec: &d.Spec, Deployment: d})
	if err != nil {
		return err
	}
	if len(res.Missing) > 0 {
		b.Printf("WARN: 찾을 수 없는 변수 참조(빈 값으로 둡니다): %s", strings.Join(res.Missing, ", "))
	}
	m.logCrossNode(ctx, t, d, b)
	d.Env = res.Vars
	save()

	if d.Spec.CronSchedule != nil && *d.Spec.CronSchedule != "" {
		if _, err := cronx.Parse(*d.Spec.CronSchedule); err != nil {
			return err
		}
	}
	if d.Spec.RequiredMountPath != "" {
		vols, err := m.Store.ListVolumesByInstance(ctx, t.Instance.ID)
		if err != nil {
			return err
		}
		if err := checkRequiredMount(d.Spec.RequiredMountPath, vols); err != nil {
			return err
		}
		b.Printf("deploy.requiredMountPath: %s에 볼륨이 연결되어 있습니다", d.Spec.RequiredMountPath)
	}

	plan := m.deployPlan(ctx, t.Instance, d)
	if err := m.Store.PrepareDeploymentResources(ctx, d, store.PlacementTotal(plan)); err != nil {
		return err
	}
	if d.Spec.PreDeployCommand != nil && strings.TrimSpace(*d.Spec.PreDeployCommand) != "" {
		m.setStatus(ctx, d, b, store.DepPreDeploy)
		if err := m.ensureEnvNetwork(ctx, t.Env, d.NodeID); err != nil {
			return err
		}
		cmd := *d.Spec.PreDeployCommand
		b.Printf("pre-deploy: %s", cmd)
		err := stage(ctx, m.Timeouts.PreDeploy, "pre-deploy", func(ctx context.Context) error {
			spec := m.runSpec(t, d, 0)
			spec.Name = "keel-predeploy-" + strings.TrimPrefix(d.ID, "dep_")[:8]
			spec.Labels[node.LabelRole] = node.RolePreDeploy
			spec.Aliases = nil
			spec.Restart = ""
			spec.Mounts = nil
			sh := "/bin/sh"
			spec.Entrypoint, spec.Cmd = &sh, []string{"-c", cmd}
			drv.Remove(ctx, spec.Name, 0)
			code, err := drv.RunToCompletion(ctx, spec, func(stream, l string) { b.Line("  | " + l) })
			if err != nil {
				return err
			}
			if code != 0 {
				return fmt.Errorf("pre-deploy command exited with code %d", code)
			}
			return nil
		})
		if err != nil {
			return err
		}
		b.Printf("pre-deploy 완료")
	}

	if d.Spec.CronSchedule != nil && *d.Spec.CronSchedule != "" {
		return m.cutoverCron(ctx, t, d, b)
	}

	m.setStatus(ctx, d, b, store.DepDeploying)
	if err := m.ensureEnvNetwork(ctx, t.Env, d.NodeID); err != nil {
		return err
	}
	vols, err := m.Store.ListVolumesByInstance(ctx, t.Instance.ID)
	if err != nil {
		return err
	}
	if len(extraPlacementNodes(d)) > 0 && len(vols) > 0 {
		return errors.New("볼륨이 붙은 서비스는 여러 노드에 나눠 배치할 수 없습니다(여러 프로세스가 같은 데이터를 씁니다). placement를 하나로 줄이거나 볼륨을 떼세요")
	}
	for _, p := range plan[1:] {
		if err := m.ensureEnvNetwork(ctx, t.Env, p.NodeID); err != nil {
			return err
		}
	}
	for _, v := range vols {
		if err := drv.EnsureVolume(ctx, v.DockerName, m.volumeLabels(v)); err != nil {
			return err
		}
	}
	// 볼륨이 붙은 서비스는 같은 데이터 디렉터리를 두 프로세스가 열지 않도록 이전 컨테이너를 먼저 멈춘다(recreate).
	var moved []movedVolume
	if len(vols) > 0 {
		if cur, err := m.Store.GetInstance(ctx, t.Instance.ID); err == nil && cur.CurrentDeploymentID != nil && *cur.CurrentDeploymentID != d.ID {
			if old, err := m.Store.GetDeployment(ctx, *cur.CurrentDeploymentID); err == nil {
				stopped.drv = m.driverFor(old)
				crossNode := nodeKey(old.NodeID) != nodeKey(d.NodeID)
				if crossNode && !m.nodeOnline(old.NodeID) {
					return fmt.Errorf("노드를 옮기려면 이전 노드 %s에서 볼륨 데이터를 복사해야 하는데 연결이 끊겨 있습니다", old.NodeID)
				}
				for _, c := range old.Containers {
					b.Printf("볼륨을 쓰는 서비스라 이전 컨테이너 %s를 먼저 멈춥니다", c)
					m.markDrain(c)
					err := stopped.drv.Stop(context.WithoutCancel(ctx), c, d.Spec.DrainGrace(30))
					switch {
					case err == nil:
						stopped.names = append(stopped.names, c)
					case crossNode && errors.Is(err, node.ErrNoContainer):
						m.undrain(c) // 이미 없다: 쓰는 프로세스도 없다
					case crossNode:
						m.undrain(c)
						return fmt.Errorf("이전 컨테이너 %s를 멈추지 못해 볼륨을 복사하지 않습니다 (이전 노드는 그대로 유지됩니다): %w", c, err)
					default:
						m.undrain(c)
					}
				}
				if crossNode {
					var err error
					if moved, err = m.copyVolumes(ctx, b, vols, stopped.drv, drv, old, d); err != nil {
						return err
					}
				}
			}
		}
	}
	b.Printf("==> starting")
	assignContainers(t.Service.Name, d, plan)
	total := len(d.AllContainers())
	if len(plan) > 1 {
		b.Printf("placement: %s", describePlan(plan))
	}
	save()
	if err := stage(ctx, m.Timeouts.Start, "start", func(ctx context.Context) error {
		for _, g := range d.Groups() {
			gd := m.driverForNode(g.NodeID)
			for _, name := range g.Containers {
				gd.Remove(ctx, name, 0)
				i := replicaIndex(name)
				if err := gd.Run(ctx, m.runSpecNamed(t, d, name, i)); err != nil {
					return err
				}
				if len(plan) > 1 {
					b.Printf("컨테이너 %s 시작 (replica %d/%d, node %s)", name, i+1, total, nodeKey(g.NodeID))
				} else {
					b.Printf("컨테이너 %s 시작 (replica %d/%d)", name, i+1, total)
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}

	m.setStatus(ctx, d, b, store.DepHealthchecking)
	hcTimeout := time.Duration(d.Spec.HealthcheckTimeoutS) * time.Second
	if err := stage(ctx, hcTimeout, "healthcheck", func(ctx context.Context) error {
		return m.healthcheckAll(ctx, t, d, b)
	}); err != nil {
		return err
	}
	if err := m.cutover(ctx, t, d, b, stopped.names); err != nil {
		return err
	}
	// 데이터는 새 노드로 옮겨졌고 서비스가 거기서 live가 됐다. 이전 노드의 볼륨은 더 쓰이지 않는다.
	for _, mv := range moved {
		if err := mv.drv.RemoveVolume(context.WithoutCancel(ctx), mv.name); err != nil {
			b.Printf("WARN: 이전 노드 %s의 볼륨 %s를 지우지 못했습니다: %v", mv.node, mv.name, err)
		} else {
			b.Printf("이전 노드 %s의 볼륨 %s를 지웠습니다 (데이터는 새 노드에 있습니다)", mv.node, mv.name)
		}
	}
	return nil
}

// movedVolume은 다른 노드로 복사가 끝난 볼륨의 이전 위치다.
type movedVolume struct {
	drv  Driver
	node string
	name string
}

// copyVolumes는 볼륨 데이터를 이전 노드에서 새 노드로 복사한다(쓰던 컨테이너는 이미 멈춰 있어야 한다).
// 이전 노드의 볼륨은 건드리지 않는다. 실패하면 새 노드에 만든 불완전한 복사본만 지운다.
func (m *Manager) copyVolumes(ctx context.Context, b *logbook.Book, vols []*store.Volume, from, to Driver, old, d *store.Deployment) ([]movedVolume, error) {
	var done []movedVolume
	for i, v := range vols {
		b.Printf("볼륨 %s 데이터를 노드 %s → %s로 복사합니다", v.Name, nodeKey(old.NodeID), nodeKey(d.NodeID))
		if err := from.EnsureVolume(ctx, v.DockerName, m.volumeLabels(v)); err != nil {
			return nil, fmt.Errorf("이전 노드의 볼륨 %s: %w", v.Name, err)
		}
		pr, pw := io.Pipe()
		errc := make(chan error, 1)
		tag := strings.TrimPrefix(d.ID, "dep_")
		go func() {
			err := from.ExportVolume(ctx, v.DockerName, fmt.Sprintf("aven-move-%s-%d", tag, i), pw)
			pw.CloseWithError(err)
			errc <- err
		}()
		ierr := to.ImportVolume(ctx, v.DockerName, fmt.Sprintf("aven-moveimp-%s-%d", tag, i), pr)
		pr.CloseWithError(ierr)
		eerr := <-errc
		if err := errors.Join(eerr, ierr); err != nil {
			to.RemoveVolume(context.WithoutCancel(ctx), v.DockerName)
			return nil, fmt.Errorf("볼륨 %s 복사 실패 (이전 노드의 데이터는 그대로입니다): %w", v.Name, err)
		}
		done = append(done, movedVolume{drv: from, node: nodeKey(old.NodeID), name: v.DockerName})
	}
	return done, nil
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// build는 소스 디렉터리에서 config-as-code를 읽고 Dockerfile 또는 railpack으로 이미지를 만든다.
func (m *Manager) build(ctx context.Context, t *Target, d *store.Deployment, b *logbook.Book, srcDir, workDir string) error {
	root, err := safeJoin(srcDir, d.Spec.RootDir)
	if err != nil {
		return err
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return fmt.Errorf("root_dir %s가 저장소에 없습니다", d.Spec.RootDir)
	}
	name, cfg, err := config.Find(root)
	if err != nil {
		return fmt.Errorf("config-as-code: %w", err)
	}
	if err := m.applyConfig(ctx, t, d, b, name, cfg); err != nil {
		return err
	}
	m.setStatus(ctx, d, b, store.DepBuilding)
	key := m.sourceBuildKey(ctx, t, d, root)
	if m.reuseBuild(ctx, t, d, b, key) {
		m.Store.UpdateDeployment(context.WithoutCancel(ctx), d)
		return nil
	}
	image := m.imageRepo(t.Service.ID) + ":" + strings.ReplaceAll(d.ID, "_", "-")
	builder, err := m.buildLocalImage(ctx, t, d, b, root, workDir, d.Spec.Builder, image)
	if err != nil {
		return err
	}
	d.BuilderUsed = &builder
	d.Image = &image
	d.BuildCacheKey = key
	m.Store.UpdateDeployment(context.WithoutCancel(ctx), d)
	return nil
}

// buildLocalImage는 로컬 노드에서 root(config-as-code가 이미 반영된 소스)를 builder로 빌드해 image를 만든다.
// d는 읽기만 한다(placement의 다른 노드 빌드와 동시에 불릴 수 있다). 실제로 쓴 builder를 돌려준다.
func (m *Manager) buildLocalImage(ctx context.Context, t *Target, d *store.Deployment, b *logbook.Book, root, workDir, builder, image string) (string, error) {
	// 빌드에도 변수를 넘긴다(Dockerfile ARG / railpack secret).
	res, err := m.Resolve(ctx, t.Instance, &Override{InstanceID: t.Instance.ID, Vars: d.VarsRaw, Spec: &d.Spec, Deployment: d})
	if err != nil {
		return "", err
	}
	buildVars, buildTarget := composeBuild(&d.Spec, res.Vars)
	dockerfile, err := safeJoin(root, d.Spec.DockerfilePath)
	if err != nil {
		return "", err
	}
	if builder == "auto" || builder == "" {
		builder = "railpack"
		if _, err := os.Stat(dockerfile); err == nil {
			builder = "dockerfile"
		}
		b.Printf("builder=auto → %s", builder)
	}
	labels := map[string]string{node.LabelManaged: "true", node.LabelService: t.Service.ID, node.LabelDeploy: d.ID}
	switch builder {
	case "dockerfile":
		if _, err := os.Stat(dockerfile); err != nil {
			return "", fmt.Errorf("Dockerfile을 찾을 수 없습니다: %s", d.Spec.DockerfilePath)
		}
		if d.Spec.BuildCommand != nil && *d.Spec.BuildCommand != "" {
			b.Printf("build.buildCommand는 Dockerfile 빌드에서 쓰지 않습니다(빌드 단계는 Dockerfile의 RUN으로 정하세요)")
		}
		err = stage(ctx, m.Timeouts.Build, "build", func(ctx context.Context) error {
			plain, secrets := node.SplitSecrets(buildVars, d.Spec.BuildSecrets)
			if len(secrets) > 0 {
				b.Printf("build secrets: %s (BuildKit --secret)", strings.Join(sortedNames(secrets), ", "))
			}
			return m.Docker.BuildSecrets(ctx, root, dockerfile, image, buildTarget, labels, buildArgsFor(dockerfile, plain), secrets, b.Line)
		})
	case "railpack":
		if m.Cfg.Railpack == "" {
			return "", errors.New("railpack을 찾을 수 없습니다 (KEEL_RAILPACK_BIN)")
		}
		b.Printf("railpack prepare + buildx (frontend %s)", m.Cfg.RailpackFrontend)
		rpVars := withBuildCommand(&d.Spec, buildVars, b)
		err = stage(ctx, m.Timeouts.Build, "build", func(ctx context.Context) error {
			_, err := m.Docker.RailpackBuild(ctx, m.Cfg.Railpack, m.Cfg.RailpackFrontend, root, workDir, image, rpVars, b.Line)
			return err
		})
	default:
		return "", fmt.Errorf("unknown builder %q", builder)
	}
	if err != nil {
		return "", err
	}
	return builder, nil
}

// checkRequiredMount는 deploy.requiredMountPath에 볼륨이 마운트되어 있는지 확인한다.
func checkRequiredMount(p string, vols []*store.Volume) error {
	for _, v := range vols {
		if v.MountPath == p { // 둘 다 정규화된 절대 경로다
			return nil
		}
	}
	return fmt.Errorf("deploy.requiredMountPath: %s 경로에 마운트된 볼륨이 없습니다. 이 경로로 볼륨을 연결한 뒤 다시 배포하세요", p)
}

// RailpackBuildCmdVar는 railpack이 빌드 명령을 덮어쓰는 설정 변수다.
const RailpackBuildCmdVar = "RAILPACK_BUILD_CMD"

// withBuildCommand는 config-as-code의 build.buildCommand를 RAILPACK_BUILD_CMD로 넘긴다(railpack prepare --env).
// 사용자가 그 변수를 직접 정했으면 변수 값을 쓴다. vars는 바꾸지 않는다.
func withBuildCommand(sp *store.Spec, vars map[string]string, b *logbook.Book) map[string]string {
	if sp.BuildCommand == nil || *sp.BuildCommand == "" {
		return vars
	}
	if _, ok := vars[RailpackBuildCmdVar]; ok {
		if b != nil {
			b.Printf("WARN: 변수 %s가 있어 build.buildCommand 대신 그 값을 씁니다", RailpackBuildCmdVar)
		}
		return vars
	}
	out := make(map[string]string, len(vars)+1)
	for k, v := range vars {
		out[k] = v
	}
	out[RailpackBuildCmdVar] = *sp.BuildCommand
	if b != nil {
		b.Printf("build.buildCommand: %s (railpack 빌드에 %s로 넘깁니다)", *sp.BuildCommand, RailpackBuildCmdVar)
	}
	return out
}

// buildArgsFor는 Dockerfile에 ARG로 선언된 변수만 build-arg로 넘긴다.
func buildArgsFor(dockerfile string, vars map[string]string) map[string]string {
	return node.BuildArgs(dockerfile, vars)
}

func restartFlag(sp *store.Spec) string {
	switch sp.RestartPolicy {
	case "always":
		return "unless-stopped"
	case "never":
		return "no"
	default:
		return fmt.Sprintf("on-failure:%d", sp.RestartMaxRetries)
	}
}

func (m *Manager) volumeLabels(v *store.Volume) map[string]string {
	l := map[string]string{node.LabelProject: v.ProjectID, node.LabelEnv: v.EnvironmentID, node.LabelVolumeID: v.ID}
	return l
}

// runSpec은 배포 d의 i번째 replica 컨테이너 설정이다.
func (m *Manager) runSpec(t *Target, d *store.Deployment, i int) node.RunSpec {
	name := containerName(t.Service.Name, d.ID, i)
	if i < len(d.Containers) {
		name = d.Containers[i]
	}
	return m.runSpecNamed(t, d, name, i)
}

// runSpecNamed는 배포 d의 replica 컨테이너 name(replica 번호 i, 0부터)의 설정이다. 노드와 상관없이 같다.
func (m *Manager) runSpecNamed(t *Target, d *store.Deployment, name string, i int) node.RunSpec {
	env, traced := m.runtimeTraceEnv(context.Background(), t, d, name)
	s := node.RunSpec{
		Name: name, Image: *d.Image, Env: env, Network: EnvNetwork(t.Env.ID),
		Aliases: PrivateAliases(t.Service.Name, t.Env.Name, d.Spec.PrivateDNSName),
		Restart: restartFlag(&d.Spec),
		Labels: map[string]string{
			node.LabelManaged: "true", node.LabelProject: t.Project.ID, node.LabelEnv: t.Env.ID,
			node.LabelService: t.Service.ID, node.LabelInstance: t.Instance.ID, node.LabelDeploy: d.ID,
			node.LabelReplica: fmt.Sprint(i + 1), node.LabelRole: node.RoleApp,
		},
	}
	if vols, err := m.Store.ListVolumesByInstance(context.Background(), t.Instance.ID); err == nil {
		for _, v := range vols {
			s.Mounts = append(s.Mounts, node.Mount{Volume: v.DockerName, Path: v.MountPath})
		}
	}
	if d.Spec.StartCommand != nil && strings.TrimSpace(*d.Spec.StartCommand) != "" {
		sh := "/bin/sh"
		s.Entrypoint, s.Cmd = &sh, []string{"-c", *d.Spec.StartCommand}
	}
	if d.Spec.MemoryLimitMB != nil {
		s.MemoryMB = *d.Spec.MemoryLimitMB
	}
	if d.Spec.CPULimit != nil && *d.Spec.CPULimit > 0 {
		s.CPUs = strconv.FormatFloat(*d.Spec.CPULimit, 'f', -1, 64)
	}
	if traced && strings.Contains(env["OTEL_EXPORTER_OTLP_ENDPOINT"], "host.docker.internal") {
		s.ExtraHosts = []string{"host.docker.internal:host-gateway"} // 호스트의 OTLP 수신기
	}
	applyComposeRun(&s, &d.Spec)
	return s
}

func safeJoin(root, rel string) (string, error) {
	p := filepath.Join(root, filepath.Clean("/"+rel))
	if p != root && !strings.HasPrefix(p, root+string(filepath.Separator)) {
		return "", fmt.Errorf("잘못된 경로: %s", rel)
	}
	return p, nil
}

// maxHealthRestarts는 healthcheck 중 허용하는 재시작 횟수다.
const maxHealthRestarts = 2

// healthcheckAll은 모든 replica가 건강해질 때까지 기다린다(한 세트로 판정).
// placement로 여러 노드에 나눴으면 모든 노드의 replica가 건강해야 한다(하나라도 실패하면 배포 전체가 실패한다).
func (m *Manager) healthcheckAll(ctx context.Context, t *Target, d *store.Deployment, b *logbook.Book) error {
	for _, g := range d.Groups() {
		if len(g.Containers) == 0 {
			continue
		}
		if err := m.healthcheckNames(ctx, t, d, g.NodeID, g.Containers, b); err != nil {
			if len(d.Extra) > 0 {
				return fmt.Errorf("node %s: %w", nodeKey(g.NodeID), err)
			}
			return err
		}
	}
	return nil
}

// healthcheckNames는 노드 nodeID의 컨테이너 names가 모두 건강해질 때까지 기다린다.
func (m *Manager) healthcheckNames(ctx context.Context, t *Target, d *store.Deployment, nodeID string, names []string, b *logbook.Book) error {
	drv := m.driverForNode(nodeID)
	port := d.Spec.EffectivePort(d.Env)
	ok := make([]bool, len(names))
	kind := "http"
	if d.Spec.HealthcheckType == "tcp" {
		kind = "tcp"
	}
	network := EnvNetwork(t.Env.ID)
	var last string
	noneSince := time.Now()
	restarts := map[string]int{}
	for attempt := 1; ; attempt++ {
		states, err := drv.InspectMany(ctx, names)
		if err != nil {
			return err
		}
		all := true
		for i, name := range names {
			st := states[name]
			if st == nil {
				return fmt.Errorf("컨테이너 %s가 사라졌습니다", name)
			}
			// 의존 서비스(DB 등)가 아직 안 떠서 한두 번 재시작하는 건 봐준다. 재시작이 계속되거나 완전히 멈추면 실패.
			if st.RestartCount > restarts[name] {
				b.Printf("컨테이너 %s가 재시작했습니다 (%d/%d)", name, st.RestartCount, maxHealthRestarts)
				restarts[name] = st.RestartCount
			}
			if (!st.Running && st.Status != "restarting") || st.RestartCount > maxHealthRestarts {
				b.Printf("컨테이너 %s 로그 (마지막 30줄):", name)
				for _, l := range drv.TailLogs(context.WithoutCancel(ctx), name, 30) {
					b.Line("  | " + l)
				}
				return fmt.Errorf("컨테이너 %s가 시작 직후 종료되었습니다 (status=%s, exit=%d, restarts=%d)", name, st.Status, st.ExitCode, st.RestartCount)
			}
			if ok[i] {
				continue
			}
			switch {
			case d.Spec.HealthcheckType == "none":
				ok[i] = time.Since(noneSince) > 3*time.Second
			case isLocal(nodeID) && m.localDrv == nil:
				var detail string
				if ok[i], detail = node.ProbeState(ctx, st, network, kind, port, d.Spec.HealthcheckPath); ok[i] {
					b.Printf("healthcheck OK: %s (%s)", detail, name)
				} else {
					last = detail
				}
			default:
				// 원격 노드는 agent가 자기 노드에서 컨테이너 주소로 확인한다.
				var detail string
				if ok[i], detail = drv.Probe(ctx, name, network, kind, port, d.Spec.HealthcheckPath); ok[i] {
					b.Printf("healthcheck OK: %s (%s, node %s)", detail, name, nodeKey(nodeID))
				} else {
					last = detail
				}
			}
			if !ok[i] {
				all = false
			}
		}
		if all {
			if d.Spec.HealthcheckType == "none" {
				b.Printf("healthcheck 없음: 컨테이너가 실행 중입니다")
			}
			return nil
		}
		if attempt%5 == 1 && last != "" {
			b.Printf("healthcheck 대기 중... (%s)", last)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("마지막 응답: %s", last)
		case <-time.After(time.Second):
		}
	}
}

func (m *Manager) markDrain(c string) {
	m.mu.Lock()
	m.drain[c] = true
	m.mu.Unlock()
}

func (m *Manager) undrain(c string) {
	m.mu.Lock()
	delete(m.drain, c)
	m.mu.Unlock()
}

func (m *Manager) draining(c string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.drain[c]
}

// cutover는 새 배포를 live로 만들고 프록시를 전환한 뒤 이전 컨테이너를 drain 후 지운다. 이 시점부터 취소하지 않는다.
func (m *Manager) cutover(ctx context.Context, t *Target, d *store.Deployment, b *logbook.Book, stopped []string) error {
	ctx = context.WithoutCancel(ctx)
	cur, err := m.Store.GetInstance(ctx, t.Instance.ID)
	if err != nil {
		return err
	}
	if cur.Status == store.InstOffline {
		return errors.New("배포 중에 서비스가 중지되었습니다")
	}
	prev := cur.CurrentDeploymentID
	m.markAwake(t.Instance.ID)
	m.Store.SetInstanceStatus(ctx, t.Instance.ID, store.InstOnline, &d.ID)
	d.Status = store.DepLive
	m.Store.UpdateDeployment(ctx, d)
	b.Status(string(store.DepLive))
	perr := m.syncProxy(ctx)
	proxyOK := true
	for _, g := range d.Groups() {
		if err := perr[nodeKey(g.NodeID)]; err != nil {
			proxyOK = false
			b.Printf("WARN: 노드 %s 프록시 반영 실패(재시도 예정): %v", nodeKey(g.NodeID), err)
		}
	}
	if proxyOK {
		b.Printf("프록시 전환 완료")
	}
	if err := m.syncTCPProxy(ctx, t); err != nil {
		b.Printf("WARN: TCP proxy: %v", err)
	}
	hosts := []string{}
	for _, h := range cur.Domains {
		hosts = append(hosts, h.Host)
	}
	b.Printf("배포 완료 %s", strings.Join(hosts, ", "))
	m.emit(events.WithDeployment(ctx, d.ID), t, "deploy.live", fmt.Sprintf("%s (%s) 배포 완료%s", t.Service.Name, t.Env.Name, commitSuffix(d)))
	if prev != nil && *prev != d.ID {
		m.retire(ctx, *prev, b, stopped, retireOptsFor(&d.Spec))
	}
	m.pruneImages(ctx, t.Service.ID)
	return nil
}

// retireOpts는 이전 배포를 내리는 방법이다(새 배포의 설정을 따른다).
type retireOpts struct {
	overlap time.Duration // live 전환 후 이전 컨테이너를 남겨 두는 시간(최소 3초 drain)
	grace   int           // docker stop -t (SIGTERM → SIGKILL)
}

func retireOptsFor(sp *store.Spec) retireOpts {
	return retireOpts{overlap: time.Duration(sp.OverlapSeconds) * time.Second, grace: sp.DrainGrace(10)}
}

// overlapWait는 이전 컨테이너를 지우기 전에 기다릴 시간이다. 이미 멈춘 컨테이너(recreate)는 기다리지 않는다.
func (o retireOpts) overlapWait(alreadyStopped bool) time.Duration {
	if alreadyStopped {
		return 0
	}
	if o.overlap < 3*time.Second {
		return 3 * time.Second // 진행 중인 요청이 끝나도록 잠시 둔다
	}
	return o.overlap
}

func commitSuffix(d *store.Deployment) string {
	if d.CommitSHA != nil {
		s := " @ " + short(*d.CommitSHA)
		if d.CommitMessage != nil {
			s += " " + *d.CommitMessage
		}
		return s
	}
	if d.Image != nil {
		return " (" + *d.Image + ")"
	}
	return ""
}

// retire는 이전 배포를 removed로 바꾸고 컨테이너를 drain 후 지운다.
// o.overlap 동안 이전 컨테이너를 남겨 둔 뒤(라우트는 이미 새 배포를 가리킨다) o.grace초의 SIGTERM 유예로 멈춘다.
func (m *Manager) retire(ctx context.Context, depID string, b *logbook.Book, alreadyStopped []string, o retireOpts) {
	old, err := m.Store.GetDeployment(ctx, depID)
	if err != nil {
		return
	}
	if old.Status == store.DepLive || old.Status == store.DepCrashed {
		old.Status = store.DepRemoved
		m.Store.UpdateDeployment(ctx, old)
	}
	// 다른 노드로 옮겨졌다면 이전 노드의 TCP 포워더(공개 포트를 잡고 있다)도 지운다.
	if in, err := m.Store.GetInstance(ctx, old.InstanceID); err == nil && in.TCPProxy != nil && nodeKey(old.NodeID) != m.homeNode(ctx, in) && m.nodeOnline(old.NodeID) {
		m.driverFor(old).Remove(ctx, tcpName(in.ID), 1)
	}
	all := old.AllContainers()
	if len(all) == 0 {
		return
	}
	wait := o.overlapWait(len(alreadyStopped) > 0)
	if b != nil {
		if wait > 3*time.Second {
			b.Printf("overlap: 이전 컨테이너 %s를 %s 동안 남겨 둔 뒤 종료합니다", strings.Join(all, ", "), wait)
		} else {
			b.Printf("이전 컨테이너 %s drain 후 종료", strings.Join(all, ", "))
		}
	}
	for _, c := range all {
		m.markDrain(c)
	}
	if wait > 0 {
		time.Sleep(wait)
	}
	if b != nil && o.grace != 10 {
		b.Printf("draining: SIGTERM 후 최대 %d초 기다립니다", o.grace)
	}
	m.removeContainers(ctx, old, all, o.grace)
	for _, c := range all {
		m.undrain(c)
	}
}

// removeContainers는 런타임 로그를 보관한 뒤 컨테이너를 지운다. 컨테이너마다 그것이 도는 노드에서 지운다(꺼진 노드는 건너뛴다:
// 다시 연결되면 reconcile이 고아로 지운다).
func (m *Manager) removeContainers(ctx context.Context, d *store.Deployment, names []string, grace int) {
	if len(names) == 0 {
		return
	}
	m.saveRuntimeLogs(ctx, d, names)
	for _, c := range names {
		n := d.NodeOf(c)
		if !m.nodeOnline(n) {
			continue
		}
		m.markDrain(c)
		m.driverForNode(n).Remove(ctx, c, grace)
		m.undrain(c)
	}
}

// cutoverCron은 cron 서비스의 배포를 live로 만든다. 컨테이너는 띄우지 않고 스케줄러가 실행한다.
func (m *Manager) cutoverCron(ctx context.Context, t *Target, d *store.Deployment, b *logbook.Book) error {
	ctx = context.WithoutCancel(ctx)
	cur, err := m.Store.GetInstance(ctx, t.Instance.ID)
	if err != nil {
		return err
	}
	prev := cur.CurrentDeploymentID
	m.Store.SetInstanceStatus(ctx, t.Instance.ID, store.InstScheduled, &d.ID)
	d.Status = store.DepLive
	d.Containers = nil
	m.Store.UpdateDeployment(ctx, d)
	b.Status(string(store.DepLive))
	b.Printf("cron 작업으로 등록했습니다: %s (UTC)", *d.Spec.CronSchedule)
	m.SyncProxy(ctx)
	m.syncTCPProxy(ctx, t)
	m.emit(events.WithDeployment(ctx, d.ID), t, "deploy.live", fmt.Sprintf("%s (%s) cron 일정 등록: %s (UTC)", t.Service.Name, t.Env.Name, *d.Spec.CronSchedule))
	if prev != nil && *prev != d.ID {
		m.retire(ctx, *prev, b, nil, retireOptsFor(&d.Spec))
	}
	m.pruneImages(ctx, t.Service.ID)
	return nil
}

// pruneImages는 서비스의 빌드 이미지 중 각 인스턴스의 최근 성공 배포 5개와 진행 중 배포 것만 남긴다.
func (m *Manager) pruneImages(ctx context.Context, serviceID string) {
	insts, err := m.Store.ListInstancesByService(ctx, serviceID)
	if err != nil {
		return
	}
	keep := map[string]bool{}
	for _, in := range insts {
		deps, err := m.Store.ListDeployments(ctx, in.ID, 100)
		if err != nil {
			return
		}
		n := 0
		for _, d := range deps {
			if d.Image == nil {
				continue
			}
			if !d.Status.Terminal() || (d.Status.Succeeded() && n < 5) {
				keep[*d.Image] = true
				if d.Status.Succeeded() {
					n++
				}
			}
		}
	}
	for _, drv := range m.onlineDrivers(ctx) {
		for _, img := range drv.ImagesByRepo(ctx, m.imageRepo(serviceID)) {
			if !keep[img] {
				drv.RemoveImage(ctx, img)
			}
		}
	}
}

// registryAuth는 프로젝트에 등록된 private registry 중 이미지 호스트에 맞는 인증 정보를 고른다.
func (m *Manager) registryAuth(ctx context.Context, projectID, image string, b *logbook.Book) *node.RegistryAuth {
	regs, err := m.Store.GetRegistries(ctx, projectID)
	if err != nil || len(regs) == 0 {
		return nil
	}
	auths := make([]node.RegistryAuth, 0, len(regs))
	for _, r := range regs {
		auths = append(auths, node.RegistryAuth{Host: r.Host, Username: r.Username, Password: r.Password})
	}
	a := node.MatchAuth(image, auths)
	if a != nil && b != nil {
		b.Printf("registry %s 인증 정보를 사용합니다 (user %s)", node.ImageHost(image), a.Username)
	}
	return a
}

func sortedNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
