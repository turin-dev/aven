// Package agent는 원격 노드에서 도는 aven-agent의 요청 처리기다.
//
// 서버가 보낼 수 있는 작업은 agentprotocol의 Op* 목록(allowlist)뿐이고, 각 작업은 엄격하게 검사한다:
//   - 컨테이너는 keel.managed=true 라벨이 있는 것만 조회/중지/삭제한다(keel-proxy 제외).
//   - container.run 스펙은 알 수 없는 필드를 거부한다(privileged, cap_add, host network, bind mount, docker.sock 등은 스펙에 표현 자체가 불가).
//     이름·이미지·네트워크·볼륨·라벨·재시작 정책은 정해진 형식만 받는다. 포트 publish는 받지 않는다.
//   - 빌드는 이 노드에서 한다(source.fetch → image.build → source.cleanup). 저장소는 https/ssh만, file:// 금지.
//   - proxy.apply는 aven 컨테이너 이름:포트 upstream만 받는다(IP/unix socket 금지).
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/compat"
	"github.com/turin-dev/aven/internal/mesh"
	"github.com/turin-dev/aven/internal/netlog"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/sysstat"
)

// Docker는 agent가 쓰는 docker 기능이다(테스트에서 가짜로 바꾼다).
type Docker interface {
	Version(ctx context.Context) string
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
	InspectMany(ctx context.Context, names []string) (map[string]*node.ContainerState, error)
	ListManaged(ctx context.Context) (map[string]map[string]string, error)
	LogsSince(ctx context.Context, name, tail, since string, follow bool, out func(node.LogLine)) error
	Stats(ctx context.Context, names []string) ([]node.Stats, error)
	Build(ctx context.Context, contextDir, dockerfile, tag string, labels, buildArgs map[string]string, out func(string)) error
	RailpackBuild(ctx context.Context, railpack, frontend, srcDir, workDir, tag string, vars map[string]string, out func(string)) (*node.RailpackInfo, error)
	Events(ctx context.Context, out func(node.ContainerEvent)) error
	ExportVolume(ctx context.Context, volume, name string, w io.Writer) error
	ImportVolume(ctx context.Context, volume, name string, r io.Reader) error
	LoadImage(ctx context.Context, r io.Reader) error
	RunStream(ctx context.Context, s node.RunSpec, stdin io.Reader, stdout io.Writer, stderr func(string)) (int, error)
	NetworkInfo(ctx context.Context, name string) (*node.NetworkInfo, error)
}

// Proxy는 이 노드의 keel-proxy(Caddy)다.
type Proxy interface {
	Apply(ctx context.Context, routes []proxy.Route) error
	Connect(ctx context.Context, network string) error
	FollowAccessLogs(ctx context.Context, out func(proxy.AccessLog))
}

type Handler struct {
	Docker           Docker
	Proxy            Proxy
	WorkDir          string // 소스 clone/빌드 작업 디렉터리
	Railpack         string // railpack 바이너리(없으면 railpack 빌드 거부)
	RailpackFrontend string
	Version          string
	IPv4, IPv6       string
	// IPv6Iface는 도메인별 IPv6 주소를 붙일 인터페이스다(KEEL_IPV6_IFACE, 비우면 공인 IPv6/기본 경로의 인터페이스).
	IPv6Iface string
	Private   string
	DataDir   string // 디스크 여유 공간을 잴 경로
	TCPBind   string // TCP proxy publish 주소(비우면 모든 주소). agent 설정이며 서버가 정하지 못한다.
	// Mesh는 노드 간 사설 네트워킹 gateway다(nil이면 꺼짐). listen 주소(사설 주소:MeshPort)는 agent 설정이다.
	Mesh     *mesh.Gateway
	MeshPort int
	// ExternalUpstreams는 이 노드의 운영자가 허용한 aven 밖의 upstream(host:port)이다(--external-upstreams). 서버가 보낸
	// 라우트는 aven 컨테이너와 이 목록만 upstream으로 쓸 수 있다.
	ExternalUpstreams []string
	NodeID            string
	// NetLog는 DNS/flow 로그 수집기다(nil이면 꺼짐, KEEL_NETWORK_LOGS=off).
	NetLog *netlog.Collector

	rpMu         sync.Mutex
	railpackBase string // 테스트용; 비우면 GitHub 릴리스
	sampler      sysstat.Sampler
	up           uploads
	execs        execSessions
	link         upState

	hostsMu sync.Mutex
	hosts   map[string]bool // 마지막으로 적용한 proxy 라우트의 호스트 → on-demand TLS 허가 여부(Route.Unverified면 false)

	clone func(ctx context.Context, repo, branch, sha, dir, token string, out func(string)) (*node.Commit, error)
}

var (
	containerRe  = regexp.MustCompile(`^(?:aven|keel)-[a-z0-9][a-z0-9-]{0,120}$`)
	networkRe    = regexp.MustCompile(`^(?:aven|keel)-(env_[a-zA-Z0-9]{4,64}|sbx-[a-z0-9-]{4,64})$`)
	volumeRe     = regexp.MustCompile(`^(?:aven|keel)-vol-[a-zA-Z0-9]{4,64}$`)
	imageRe      = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:@+-]{0,254}$`)
	keelImageRe  = regexp.MustCompile(`^(?:aven|keel)/[a-z0-9-]{1,80}:[a-z0-9-]{1,80}$`)
	keelRepoRe   = regexp.MustCompile(`^(?:aven|keel)/[a-z0-9-]{1,80}$`)
	labelKeyRe   = regexp.MustCompile(`^(?:aven|keel)\.[a-z0-9._-]{1,40}$`)
	labelValRe   = regexp.MustCompile(`^[a-zA-Z0-9_.:/@+=-]{0,200}$`)
	hostLabelRe  = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.-]{0,251}[a-zA-Z0-9])?$`)
	envKeyRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,255}$`)
	restartRe    = regexp.MustCompile(`^(|no|always|unless-stopped|on-failure(:[0-9]{1,4})?)$`)
	mountPathRe  = regexp.MustCompile(`^/[a-zA-Z0-9._/@+-]{0,250}$`)
	workspaceRe  = regexp.MustCompile(`^[a-f0-9]{24}$`)
	upstreamRe   = regexp.MustCompile(`^(?:aven|keel)-[a-z0-9][a-z0-9-]{0,120}:[0-9]{1,5}$`)
	cpusRe       = regexp.MustCompile(`^[0-9]{1,3}(\.[0-9]{1,3})?$`)
	routePathRe  = regexp.MustCompile(`^/[A-Za-z0-9/._~*%:@!$&'()+,;=-]{0,255}$`)
	redirectToRe = regexp.MustCompile(`^(https?://)?[A-Za-z0-9.-]+(:[0-9]{1,5})?(/[A-Za-z0-9/._~%:@!$&'()+,;=?#-]*)?$`)
	authUserRe   = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)
	bcryptRe     = regexp.MustCompile(`^\$2[aby]\$[0-9]{2}\$[./A-Za-z0-9]{53}$`)
	domainRe     = regexp.MustCompile(`^(\*\.)?[a-z0-9]([a-z0-9-]{0,62})(\.[a-z0-9]([a-z0-9-]{0,62}))*$`)
	checkTokenRe = regexp.MustCompile(`^[0-9a-f]{32}$`) // Route.CheckToken(Store.DomainToken: HMAC 앞 32자 hex)
)

// 샌드박스: 전용 네트워크(keel-sbx-<id>)와 fork/checkpoint 이미지(keel/sandbox:*).
var (
	sandboxIDRe    = regexp.MustCompile(`^sbx_[a-z2-7]{13}$`)
	sandboxImageRe = regexp.MustCompile(`^(?:aven|keel)/sandbox:[a-z0-9-]{4,80}$`)
)

// 컨테이너 run에 허용하는 역할. tcpproxy(포트 publish)와 proxy는 받지 않는다.
var runRoles = map[string]bool{node.RoleApp: true, node.RoleSandbox: true}
var runOnceRoles = map[string]bool{node.RolePreDeploy: true, node.RoleCron: true}

// 서버가 다룰 수 있는 컨테이너 역할(keel-proxy는 agent만 관리한다).
var actionRoles = map[string]bool{node.RoleApp: true, node.RolePreDeploy: true, node.RoleCron: true, node.RoleHelper: true, node.RoleTCPProxy: true, node.RoleSandbox: true}

func decodeStrict(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid payload: %w", err)
	}
	if dec.More() {
		return errors.New("invalid payload: trailing data")
	}
	return nil
}

func checkLabels(labels map[string]string) error {
	if len(labels) > 32 {
		return errors.New("too many labels")
	}
	for k, v := range labels {
		if !labelKeyRe.MatchString(k) || !labelValRe.MatchString(v) {
			return fmt.Errorf("label %q is not allowed", k)
		}
	}
	return nil
}

// ValidateRunSpec은 서버가 보낸 컨테이너 스펙을 검사한다. roles는 허용하는 keel.role 값이다.
func ValidateRunSpec(s node.RunSpec, roles map[string]bool) error {
	if !containerRe.MatchString(s.Name) {
		return fmt.Errorf("container name %q is not allowed", s.Name)
	}
	if !imageRe.MatchString(s.Image) {
		return fmt.Errorf("image %q is not allowed", s.Image)
	}
	if s.Labels[node.LabelManaged] != "true" {
		return errors.New("container must be labeled keel.managed=true")
	}
	if !roles[s.Labels[node.LabelRole]] {
		return fmt.Errorf("container role %q is not allowed", s.Labels[node.LabelRole])
	}
	if err := checkLabels(s.Labels); err != nil {
		return err
	}
	if !networkRe.MatchString(s.Network) {
		return fmt.Errorf("network %q is not allowed (only aven environment networks)", s.Network)
	}
	for _, a := range s.Aliases {
		if !hostLabelRe.MatchString(a) {
			return fmt.Errorf("network alias %q is not allowed", a)
		}
	}
	if s.Hostname != "" && !hostLabelRe.MatchString(s.Hostname) {
		return errors.New("hostname is not allowed")
	}
	if !restartRe.MatchString(s.Restart) {
		return fmt.Errorf("restart policy %q is not allowed", s.Restart)
	}
	if s.MemoryMB < 0 || s.MemoryMB > 1<<22 {
		return errors.New("memory limit is not allowed")
	}
	if s.CPUs != "" && !cpusRe.MatchString(s.CPUs) {
		return fmt.Errorf("cpu limit %q is not allowed", s.CPUs)
	}
	if len(s.Publish) > 0 {
		return errors.New("publishing host ports is not allowed on remote nodes")
	}
	for _, m := range s.Mounts {
		if !volumeRe.MatchString(m.Volume) {
			return fmt.Errorf("volume %q is not allowed (only aven named volumes; no host paths)", m.Volume)
		}
		if !mountPathRe.MatchString(m.Path) || m.Path == "/" || strings.Contains(m.Path, "..") {
			return fmt.Errorf("mount path %q is not allowed", m.Path)
		}
	}
	for k, v := range s.Env {
		if !envKeyRe.MatchString(k) || strings.ContainsRune(v, 0) {
			return fmt.Errorf("environment variable %q is not allowed", k)
		}
	}
	if s.Entrypoint != nil && (strings.ContainsRune(*s.Entrypoint, 0) || len(*s.Entrypoint) > 4096) {
		return errors.New("entrypoint is not allowed")
	}
	for _, c := range s.Cmd {
		if strings.ContainsRune(c, 0) {
			return errors.New("command is not allowed")
		}
	}
	return nil
}

// managed는 컨테이너가 서버가 다뤄도 되는 aven 컨테이너인지 확인한다. 없으면 exists=false.
func (h *Handler) managed(ctx context.Context, name string) (exists bool, err error) {
	if !containerRe.MatchString(name) {
		return false, fmt.Errorf("container name %q is not allowed", name)
	}
	if strings.HasPrefix(name, "keel-") {
		compat.NoteLegacyResource("container-prefix", "container prefix keel-", "container prefix aven-")
	}
	st, err := h.Docker.InspectMany(ctx, []string{name})
	if err != nil {
		return false, err
	}
	c := st[name]
	if c == nil {
		return false, nil
	}
	if c.Labels[node.LabelManaged] != "true" || !actionRoles[c.Labels[node.LabelRole]] {
		return true, fmt.Errorf("container %s is not a keel-managed workload", name)
	}
	return true, nil
}

func checkImageRef(image string) error {
	if !imageRe.MatchString(image) {
		return fmt.Errorf("image %q is not allowed", image)
	}
	return nil
}

func checkRepo(repo string) error {
	if strings.HasPrefix(repo, "-") || strings.ContainsAny(repo, " \t\n\x00") {
		return errors.New("repository URL is not allowed")
	}
	for _, p := range []string{"https://", "http://", "ssh://", "git@"} {
		if strings.HasPrefix(repo, p) {
			return nil
		}
	}
	return errors.New("repository URL must be https, http or ssh (file:// is not allowed on agents)")
}

func safeJoin(root, rel string) (string, error) {
	p := filepath.Join(root, filepath.Clean("/"+rel))
	if p != root && !strings.HasPrefix(p, root+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid path: %s", rel)
	}
	return p, nil
}

func (h *Handler) workspace(id string) (string, error) {
	if !workspaceRe.MatchString(id) {
		return "", errors.New("invalid workspace")
	}
	return filepath.Join(h.WorkDir, id), nil
}

// Handle은 요청 하나를 처리한다. stream은 중간 데이터를 서버로 보낸다.
func (h *Handler) Handle(ctx context.Context, op string, payload []byte, stream func(any)) (any, error) {
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	switch op {
	case agentprotocol.OpPing:
		return h.ping(ctx), nil

	case agentprotocol.OpImagePull:
		var r agentprotocol.PullRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		if err := checkImageRef(r.Image); err != nil {
			return nil, err
		}
		return nil, h.Docker.Pull(ctx, r.Image, r.Auth, func(l string) { stream(agentprotocol.LineChunk{Line: l}) })

	case agentprotocol.OpImageExists, agentprotocol.OpImagePorts, agentprotocol.OpImageDigest:
		var r agentprotocol.ImageRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		if err := checkImageRef(r.Image); err != nil {
			return nil, err
		}
		if op == agentprotocol.OpImageExists {
			return h.Docker.ImageExists(ctx, r.Image), nil
		}
		if op == agentprotocol.OpImageDigest {
			return h.Docker.ImageDigest(ctx, r.Image)
		}
		return h.Docker.ExposedPorts(ctx, r.Image), nil

	case agentprotocol.OpImageList:
		var r agentprotocol.RepoRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		if !keelRepoRe.MatchString(r.Repo) {
			return nil, errors.New("only keel/ image repositories can be listed")
		}
		return h.Docker.ImagesByRepo(ctx, r.Repo), nil

	case agentprotocol.OpImageRemove:
		var r agentprotocol.ImageRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		if !keelImageRe.MatchString(r.Image) {
			return nil, errors.New("only keel-built images can be removed")
		}
		h.Docker.RemoveImage(ctx, r.Image)
		return nil, nil

	case agentprotocol.OpNetworkEnsure:
		var r agentprotocol.NetworkRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		if !networkRe.MatchString(r.Name) {
			return nil, fmt.Errorf("network %q is not allowed", r.Name)
		}
		if err := checkLabels(r.Labels); err != nil {
			return nil, err
		}
		slug := strings.ReplaceAll(r.Labels[node.LabelSandbox], "_", "-")
		if id := r.Labels[node.LabelSandbox]; id != "" || strings.HasPrefix(r.Name, "aven-sbx-") || strings.HasPrefix(r.Name, "keel-sbx-") {
			if strings.HasPrefix(r.Name, "keel-sbx-") {
				compat.NoteLegacyResource("sandbox-prefix", "sandbox network prefix keel-sbx-", "sandbox network prefix aven-sbx-")
			}
			if !sandboxIDRe.MatchString(id) || (r.Name != "aven-sbx-"+slug && r.Name != "keel-sbx-"+slug) || r.Labels[node.LabelProject] == "" || r.Labels[node.LabelEnv] == "" {
				return nil, errors.New("invalid sandbox network labels")
			}
		}
		if err := h.Docker.EnsureNamedNetwork(ctx, r.Name, r.Labels); err != nil {
			return nil, err
		}
		// 샌드박스 전용 네트워크에는 keel-proxy를 붙이지 않는다(밖에서 들어오는 경로 없음).
		if r.Labels[node.LabelSandbox] != "" {
			return nil, nil
		}
		return nil, h.Proxy.Connect(ctx, r.Name)

	case agentprotocol.OpNetworkRemove:
		var r agentprotocol.NetworkRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		if !networkRe.MatchString(r.Name) {
			return nil, fmt.Errorf("network %q is not allowed", r.Name)
		}
		return nil, h.Docker.RemoveNetwork(ctx, r.Name, proxy.ContainerName, proxy.LegacyContainerName)

	case agentprotocol.OpVolumeEnsure, agentprotocol.OpVolumeRemove:
		var r agentprotocol.VolumeRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		if !volumeRe.MatchString(r.Name) {
			return nil, fmt.Errorf("volume %q is not allowed", r.Name)
		}
		if op == agentprotocol.OpVolumeRemove {
			return nil, h.Docker.RemoveVolume(ctx, r.Name)
		}
		if err := checkLabels(r.Labels); err != nil {
			return nil, err
		}
		return nil, h.Docker.EnsureVolume(ctx, r.Name, r.Labels)

	case agentprotocol.OpContainerRun, agentprotocol.OpContainerRunFg:
		var r agentprotocol.RunRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		roles := runRoles
		if op == agentprotocol.OpContainerRunFg {
			roles = runOnceRoles
		}
		if err := ValidateRunSpec(r.Spec, roles); err != nil {
			return nil, err
		}
		// 같은 이름의 컨테이너가 이미 있으면 aven 것일 때만 바꾼다.
		if exists, err := h.managed(ctx, r.Spec.Name); err != nil {
			return nil, err
		} else if exists && op == agentprotocol.OpContainerRun {
			return nil, fmt.Errorf("container %s already exists", r.Spec.Name)
		} else if exists {
			h.Docker.Remove(ctx, r.Spec.Name, 0)
		}
		if op == agentprotocol.OpContainerRun {
			return nil, h.Docker.Run(ctx, r.Spec)
		}
		code, err := h.Docker.RunToCompletion(ctx, r.Spec, func(s, l string) { stream(agentprotocol.RunChunk{Stream: s, Line: l}) })
		if err != nil {
			return nil, err
		}
		return agentprotocol.RunResult{ExitCode: code}, nil

	case agentprotocol.OpContainerStart, agentprotocol.OpContainerStop, agentprotocol.OpContainerRestrt, agentprotocol.OpContainerRemove:
		var r agentprotocol.ContainerRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		if r.Grace < 0 || r.Grace > 300 {
			return nil, errors.New("grace must be 0..300")
		}
		exists, err := h.managed(ctx, r.Name)
		if err != nil {
			return nil, err
		}
		if !exists {
			if op == agentprotocol.OpContainerRemove {
				return nil, nil
			}
			return nil, node.ErrNoContainer
		}
		switch op {
		case agentprotocol.OpContainerStart:
			return nil, h.Docker.Start(ctx, r.Name)
		case agentprotocol.OpContainerStop:
			return nil, h.Docker.Stop(ctx, r.Name, r.Grace)
		case agentprotocol.OpContainerRestrt:
			return nil, h.Docker.Restart(ctx, r.Name, r.Grace)
		default:
			return nil, h.Docker.Remove(ctx, r.Name, r.Grace)
		}

	case agentprotocol.OpSandboxCommit:
		var r agentprotocol.SandboxCommitRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		if !containerRe.MatchString(r.Name) || !sandboxImageRe.MatchString(r.Image) {
			return nil, errors.New("invalid sandbox snapshot target")
		}
		exists, err := h.managed(ctx, r.Name)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, node.ErrNoContainer
		}
		states, err := h.Docker.InspectMany(ctx, []string{r.Name})
		if err != nil {
			return nil, err
		}
		st := states[r.Name]
		if st == nil || st.Labels[node.LabelRole] != node.RoleSandbox {
			return nil, errors.New("container is not a Aven sandbox")
		}
		committer, ok := h.Docker.(interface {
			CommitSandbox(context.Context, string, string) error
		})
		if !ok {
			return nil, errors.New("sandbox snapshots are not supported by this agent")
		}
		return nil, committer.CommitSandbox(ctx, r.Name, r.Image)

	case agentprotocol.OpContainerInspct:
		var r agentprotocol.NamesRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		names, err := checkNames(r.Names)
		if err != nil {
			return nil, err
		}
		states, err := h.Docker.InspectMany(ctx, names)
		if err != nil {
			return nil, err
		}
		for n, st := range states {
			if st.Labels[node.LabelManaged] != "true" {
				delete(states, n) // aven 컨테이너가 아니면 없는 것으로 본다
			}
		}
		return states, nil

	case agentprotocol.OpContainerList:
		return h.Docker.ListManaged(ctx)

	case agentprotocol.OpContainerLogs:
		var r agentprotocol.LogsRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		if r.Tail != "all" {
			if n, err := strconv.Atoi(r.Tail); err != nil || n < 0 || n > 100000 {
				return nil, errors.New("invalid tail")
			}
		}
		if r.Since != "" {
			if _, err := time.Parse(time.RFC3339Nano, r.Since); err != nil {
				return nil, errors.New("invalid since")
			}
		}
		exists, err := h.managed(ctx, r.Name)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, node.ErrNoContainer
		}
		return nil, h.Docker.LogsSince(ctx, r.Name, r.Tail, r.Since, r.Follow, func(l node.LogLine) { stream(l) })

	case agentprotocol.OpContainerStats:
		var r agentprotocol.NamesRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		names, err := checkNames(r.Names)
		if err != nil {
			return nil, err
		}
		managed, err := h.Docker.ListManaged(ctx)
		if err != nil {
			return nil, err
		}
		var ok []string
		for _, n := range names {
			if _, m := managed[n]; m {
				ok = append(ok, n)
			}
		}
		return h.Docker.Stats(ctx, ok)

	case agentprotocol.OpContainerProbe:
		var r agentprotocol.ProbeRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		if !networkRe.MatchString(r.Network) || r.Port < 1 || r.Port > 65535 || (r.Kind != "http" && r.Kind != "tcp") || len(r.Path) > 1024 {
			return nil, errors.New("invalid probe")
		}
		if !containerRe.MatchString(r.Name) {
			return nil, errors.New("invalid container")
		}
		states, err := h.Docker.InspectMany(ctx, []string{r.Name})
		if err != nil {
			return nil, err
		}
		st := states[r.Name]
		if st == nil || st.Labels[node.LabelManaged] != "true" || st.Labels[node.LabelRole] != node.RoleApp {
			return nil, node.ErrNoContainer
		}
		ok, detail := node.ProbeState(ctx, st, r.Network, r.Kind, r.Port, r.Path)
		return agentprotocol.ProbeResult{OK: ok, Detail: detail}, nil

	case agentprotocol.OpSourceFetch:
		var r agentprotocol.FetchRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		return h.fetch(ctx, r, func(l string) { stream(agentprotocol.LineChunk{Line: l}) })

	case agentprotocol.OpImageBuild:
		var r agentprotocol.BuildRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		return h.build(ctx, r, func(l string) { stream(agentprotocol.LineChunk{Line: l}) })

	case agentprotocol.OpSourceCleanup:
		var r agentprotocol.WorkspaceRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		dir, err := h.workspace(r.Workspace)
		if err != nil {
			return nil, err
		}
		return nil, os.RemoveAll(dir)

	case agentprotocol.OpProxyApply:
		var r agentprotocol.ProxyRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		if err := ValidateRoutesWith(r.Routes, h.ExternalUpstreams); err != nil {
			return nil, err
		}
		if err := h.Proxy.Apply(ctx, r.Routes); err != nil {
			return nil, err
		}
		h.setRouteHosts(r.Routes)
		return nil, nil

	case agentprotocol.OpTCPEnsure:
		var r agentprotocol.TCPProxyRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		return nil, h.tcpEnsure(ctx, r)

	case agentprotocol.OpTCPRemove:
		var r agentprotocol.TCPRemoveRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		return nil, h.tcpRemove(ctx, r)

	case agentprotocol.OpPortFree:
		var r agentprotocol.PortFreeRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		return h.portFree(r)

	case agentprotocol.OpAccessLogs:
		if err := decodeStrict(payload, &struct{}{}); err != nil {
			return nil, err
		}
		h.Proxy.FollowAccessLogs(ctx, func(l proxy.AccessLog) { stream(l) })
		return nil, ctx.Err()

	case agentprotocol.OpEvents:
		if err := decodeStrict(payload, &struct{}{}); err != nil {
			return nil, err
		}
		err := h.Docker.Events(ctx, func(ev node.ContainerEvent) {
			if ev.Labels[node.LabelManaged] != "true" || !containerRe.MatchString(ev.Name) {
				return
			}
			stream(agentprotocol.EventChunk{Action: ev.Action, Name: ev.Name, Labels: keelLabels(ev.Labels), Exit: ev.Exit})
		})
		if err == nil {
			err = errors.New("docker events ended")
		}
		return nil, err

	case agentprotocol.OpVolumeExport:
		var r agentprotocol.VolumeExportRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		if !volumeRe.MatchString(r.Name) {
			return nil, fmt.Errorf("volume %q is not allowed", r.Name)
		}
		id, err := agentprotocol.Secret()
		if err != nil {
			return nil, err
		}
		w := &chunkWriter{ctx: ctx, stream: stream}
		if err := h.Docker.ExportVolume(ctx, r.Name, "keel-export-"+id[:12], w); err != nil {
			return nil, err
		}
		return agentprotocol.VolumeExportResult{Bytes: w.n}, nil

	case agentprotocol.OpDBBackup:
		var r agentprotocol.DBToolRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		return h.dbBackup(ctx, r, stream)

	case agentprotocol.OpVolumeSizes:
		if err := decodeStrict(payload, &struct{}{}); err != nil {
			return nil, err
		}
		return h.volumeSizes(ctx)

	case agentprotocol.OpUploadBegin:
		var r agentprotocol.UploadBeginRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		return h.uploadBegin(r)

	case agentprotocol.OpUploadChunk:
		var r agentprotocol.UploadChunkRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		return nil, h.uploadChunk(r)

	case agentprotocol.OpUploadFinish:
		var r agentprotocol.UploadFinishRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		return h.uploadFinish(ctx, r)

	case agentprotocol.OpUploadAbort:
		var r agentprotocol.UploadAbortRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		if !uploadIDRe.MatchString(r.UploadID) {
			return nil, errors.New("invalid upload")
		}
		h.up.drop(r.UploadID)
		return nil, nil

	case agentprotocol.OpExecStart:
		var r agentprotocol.ExecStartRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		return h.execStart(ctx, r, stream)

	case agentprotocol.OpExecInput:
		var r agentprotocol.ExecInputRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		return nil, h.execInput(ctx, r)

	case agentprotocol.OpExecResize:
		var r agentprotocol.ExecResizeRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		return nil, h.execResize(ctx, r)

	case agentprotocol.OpExecClose:
		var r agentprotocol.ExecCloseRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		return nil, h.execClose(ctx, r)

	case agentprotocol.OpExecRun:
		var r agentprotocol.ExecRunRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		return h.execRun(ctx, r)

	case agentprotocol.OpNodeStats:
		if err := decodeStrict(payload, &struct{}{}); err != nil {
			return nil, err
		}
		dir := h.DataDir
		s := h.sampler.Sample(dir)
		return agentprotocol.NodeStats{CPUPercent: s.CPUPercent, MemoryBytes: s.MemoryBytes, DiskFreeBytes: s.DiskFreeBytes}, nil

	case agentprotocol.OpMeshEnsure:
		var r agentprotocol.MeshEnsureRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		return h.meshEnsure(ctx, r)

	case agentprotocol.OpPortsListening:
		return h.handlePortsListening(ctx, payload)

	case agentprotocol.OpEgressStatus, agentprotocol.OpEgressEnsure:
		return h.handleEgress(ctx, op, payload)

	case agentprotocol.OpIPv6Addrs, agentprotocol.OpIPv6Ensure:
		return h.handleIPv6(ctx, op, payload)

	case agentprotocol.OpNetLogFollow:
		if err := decodeStrict(payload, &struct{}{}); err != nil {
			return nil, err
		}
		if h.NetLog == nil {
			// 수집기가 꺼진 노드: 상태만 알리고 연결을 열어 둔다(서버가 5초마다 다시 시도하지 않게).
			stream(netlog.Event{Kind: "status", Status: &netlog.Status{Node: h.NodeID, Error: "network logs are disabled on this node (KEEL_NETWORK_LOGS=off)"}})
			<-ctx.Done()
			return nil, ctx.Err()
		}
		h.NetLog.Follow(ctx, func(e netlog.Event) { stream(e) })
		return nil, ctx.Err()
	}
	return nil, fmt.Errorf("operation %q is not allowed", op)
}

func checkNames(names []string) ([]string, error) {
	if len(names) > 500 {
		return nil, errors.New("too many containers")
	}
	for _, n := range names {
		if !containerRe.MatchString(n) {
			return nil, fmt.Errorf("container name %q is not allowed", n)
		}
	}
	return names, nil
}

// ValidateRoutes는 keel-proxy 라우트를 검사한다. upstream은 aven 컨테이너 이름:포트만 허용한다.
func ValidateRoutes(routes []proxy.Route) error { return ValidateRoutesWith(routes, nil) }

// ValidateRoutesWith는 ValidateRoutes에 노드가 허용한 외부 upstream(external)을 더한다.
func ValidateRoutesWith(routes []proxy.Route, external []string) error {
	if len(routes) > 5000 {
		return errors.New("too many routes")
	}
	for _, r := range routes {
		for _, h := range r.Hosts {
			if len(h) > 253 || !domainRe.MatchString(h) {
				return fmt.Errorf("route host %q is not allowed", h)
			}
		}
		for _, u := range r.Upstreams {
			if !upstreamRe.MatchString(u) && u != WakeUpstream && !slices.Contains(external, u) {
				return fmt.Errorf("route upstream %q is not allowed", u)
			}
		}
		if len(r.SetHeaders) > 0 {
			return errors.New("route header injection is only for aven-server's own route")
		}
		if r.Path != "" && !routePathRe.MatchString(r.Path) {
			return fmt.Errorf("route path %q is not allowed", r.Path)
		}
		switch r.TLS {
		case "", proxy.TLSAuto, proxy.TLSInternal, proxy.TLSOff:
		default:
			return fmt.Errorf("route tls mode %q is not allowed", r.TLS)
		}
		if r.Redirect != nil && !redirectToRe.MatchString(r.Redirect.To) {
			return fmt.Errorf("route redirect target %q is not allowed", r.Redirect.To)
		}
		if r.Auth != nil && (!authUserRe.MatchString(r.Auth.User) || !bcryptRe.MatchString(r.Auth.Hash)) {
			return errors.New("route basic auth is not allowed")
		}
		if r.Edge != nil {
			if err := r.Edge.Validate(); err != nil {
				return fmt.Errorf("route edge settings: %w", err)
			}
		}
		if r.CheckToken != "" && !checkTokenRe.MatchString(r.CheckToken) {
			return errors.New("route domain check token is not allowed")
		}
	}
	return nil
}

func (h *Handler) ping(ctx context.Context) agentprotocol.PingResult {
	r := agentprotocol.PingResult{AgentVersion: h.Version, DockerVersion: h.Docker.Version(ctx), CPUCores: runtime.NumCPU(),
		IPv4: h.IPv4, IPv6: h.IPv6, Private: h.Private, Railpack: h.Railpack != "" && h.Railpack != "off", ExternalUpstreams: h.ExternalUpstreams}
	if h.meshReady() {
		r.MeshPort = h.MeshPort
	}
	dir := h.DataDir
	if dir == "" {
		dir = "/"
	}
	var fs syscall.Statfs_t
	if syscall.Statfs(dir, &fs) == nil {
		r.DiskFreeBytes = fs.Bavail * uint64(fs.Bsize)
	}
	if mem, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(mem), "\n") {
			if f := strings.Fields(line); len(f) >= 2 && f[0] == "MemTotal:" {
				if kib, err := strconv.ParseUint(f[1], 10, 64); err == nil {
					r.MemoryBytes = kib * 1024
				}
			}
		}
	}
	return r
}

func (h *Handler) fetch(ctx context.Context, r agentprotocol.FetchRequest, out func(string)) (*agentprotocol.FetchResult, error) {
	if err := checkRepo(r.Repo); err != nil {
		return nil, err
	}
	if r.Branch == "" || strings.HasPrefix(r.Branch, "-") || strings.ContainsAny(r.Branch, " \t\n\x00") {
		return nil, errors.New("invalid branch")
	}
	if r.SHA != "" && !regexp.MustCompile(`^[a-f0-9]{7,64}$`).MatchString(r.SHA) {
		return nil, errors.New("invalid commit SHA")
	}
	id, err := agentprotocol.Secret()
	if err != nil {
		return nil, err
	}
	id = id[:24]
	dir := filepath.Join(h.WorkDir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	clone := h.clone
	if clone == nil {
		clone = node.Clone
	}
	var c *node.Commit
	knownHosts := ""
	if r.SSHKey != "" {
		if !node.IsSSHRepo(r.Repo) {
			os.RemoveAll(dir)
			return nil, errors.New("deploy key given for a non-SSH repository URL")
		}
		c, knownHosts, err = node.CloneSSH(ctx, r.Repo, r.Branch, r.SHA, filepath.Join(dir, "src"), r.SSHKey, r.KnownHosts, out)
	} else {
		c, err = clone(ctx, r.Repo, r.Branch, r.SHA, filepath.Join(dir, "src"), r.Token, out)
	}
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	res := &agentprotocol.FetchResult{Workspace: id, Commit: *c, KnownHosts: knownHosts}
	root, err := safeJoin(filepath.Join(dir, "src"), r.RootDir)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	res.ConfigName, res.Config = readConfig(root)
	return res, nil
}

func (h *Handler) build(ctx context.Context, r agentprotocol.BuildRequest, out func(string)) (*agentprotocol.BuildResult, error) {
	dir, err := h.workspace(r.Workspace)
	if err != nil {
		return nil, err
	}
	if !keelImageRe.MatchString(r.Image) {
		return nil, errors.New("build image tag must be keel/<service>:<deployment>")
	}
	if r.Labels[node.LabelManaged] != "true" {
		return nil, errors.New("build must be labeled keel.managed=true")
	}
	if err := checkLabels(r.Labels); err != nil {
		return nil, err
	}
	src := filepath.Join(dir, "src")
	root, err := safeJoin(src, r.RootDir)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("root_dir %s does not exist in the repository", r.RootDir)
	}
	dockerfile, err := safeJoin(root, r.DockerfilePath)
	if err != nil {
		return nil, err
	}
	builder := r.Builder
	if builder == "auto" {
		builder = "railpack"
		if _, err := os.Stat(dockerfile); err == nil {
			builder = "dockerfile"
		}
		out("builder=auto → " + builder)
	}
	switch builder {
	case "dockerfile":
		if _, err := os.Stat(dockerfile); err != nil {
			return nil, fmt.Errorf("Dockerfile not found: %s", r.DockerfilePath)
		}
		plain, secrets := node.SplitSecrets(r.Vars, r.Secrets)
		if bs, ok := h.Docker.(interface {
			BuildSecrets(ctx context.Context, contextDir, dockerfile, tag, target string, labels, buildArgs, secrets map[string]string, out func(string)) error
		}); ok {
			err = bs.BuildSecrets(ctx, root, dockerfile, r.Image, "", r.Labels, node.BuildArgs(dockerfile, plain), secrets, out)
		} else {
			err = h.Docker.Build(ctx, root, dockerfile, r.Image, r.Labels, node.BuildArgs(dockerfile, plain), out)
		}
	case "railpack":
		var rp string
		rp, err = h.railpackPath(ctx, out)
		if err != nil {
			return nil, err
		}
		_, err = h.Docker.RailpackBuild(ctx, rp, h.RailpackFrontend, root, dir, r.Image, r.Vars, out)
	default:
		return nil, fmt.Errorf("unknown builder %q", builder)
	}
	if err != nil {
		return nil, err
	}
	return &agentprotocol.BuildResult{Builder: builder}, nil
}

// ---------------- connection loop ----------------

// MaxConcurrent는 agent가 동시에 처리하는 요청 수다.
const MaxConcurrent = 32

// Serve는 연결 하나에서 요청을 받아 동시에 처리한다. 연결이 끊기거나 ctx가 끝나면 돌아온다.
// 연결이 끊기면 진행 중인 작업도 취소한다(컨테이너는 그대로 돈다).
func (h *Handler) Serve(ctx context.Context, read func(context.Context) ([]byte, error), write func(context.Context, agentprotocol.Message) error) error {
	ctx, cancelAll := context.WithCancel(ctx)
	defer cancelAll()
	link := &upLink{write: write}
	h.setLink(link)
	defer func() {
		h.setLink(nil)
		h.DropUploads()
	}()
	var mu sync.Mutex
	cancels := map[string]context.CancelFunc{}
	sem := make(chan struct{}, MaxConcurrent)
	var wg sync.WaitGroup
	// 진행 중인 요청을 먼저 취소하고 기다린다. defer는 나중에 등록한 것이 먼저 돌므로 위의 defer cancelAll()에 맡기면
	// 대기가 취소보다 먼저 돌아, 끝나지 않는 스트림(접근 로그·docker 이벤트, 25시간 제한)을 영원히 기다리고 재연결하지 못한다.
	defer func() {
		cancelAll()
		wg.Wait()
	}()
	for {
		b, err := read(ctx)
		if err != nil {
			return err
		}
		var req agentprotocol.Message
		if json.Unmarshal(b, &req) != nil {
			continue
		}
		if req.Reply {
			link.deliver(req)
			continue
		}
		if req.Operation == agentprotocol.OpCancel {
			var c agentprotocol.CancelRequest
			if json.Unmarshal(req.Payload, &c) == nil {
				mu.Lock()
				if f := cancels[c.ID]; f != nil {
					f()
				}
				mu.Unlock()
			}
			continue
		}
		if req.ID == "" {
			continue
		}
		select {
		case sem <- struct{}{}:
		default:
			write(ctx, agentprotocol.Message{ID: req.ID, Operation: req.Operation, Error: "agent is busy"})
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, opTimeout(req.Operation))
		mu.Lock()
		cancels[req.ID] = cancel
		mu.Unlock()
		wg.Add(1)
		go func(req agentprotocol.Message) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				mu.Lock()
				delete(cancels, req.ID)
				mu.Unlock()
				cancel()
			}()
			stream := func(v any) {
				p, err := json.Marshal(v)
				if err == nil {
					wctx, c := context.WithTimeout(rctx, 10*time.Second)
					write(wctx, agentprotocol.Message{ID: req.ID, Operation: req.Operation, Payload: p, Stream: true})
					c()
				}
			}
			result, err := h.Handle(rctx, req.Operation, req.Payload, stream)
			answer := agentprotocol.Message{ID: req.ID, Operation: req.Operation}
			if err != nil {
				answer.Error = err.Error()
			} else if result != nil {
				answer.Payload, _ = json.Marshal(result)
			}
			wctx, c := context.WithTimeout(ctx, 10*time.Second)
			write(wctx, answer)
			c()
		}(req)
	}
}

func opTimeout(op string) time.Duration {
	switch op {
	case agentprotocol.OpImageBuild:
		return 2 * time.Hour
	case agentprotocol.OpImagePull, agentprotocol.OpSourceFetch:
		return 1 * time.Hour
	case agentprotocol.OpContainerRunFg, agentprotocol.OpContainerLogs, agentprotocol.OpAccessLogs, agentprotocol.OpEvents, agentprotocol.OpNetLogFollow:
		return 25 * time.Hour
	case agentprotocol.OpVolumeExport, agentprotocol.OpUploadFinish, agentprotocol.OpDBBackup:
		return 6 * time.Hour
	case agentprotocol.OpExecStart:
		return 12 * time.Hour // 서버가 30분 유휴 제한을 따로 건다
	case agentprotocol.OpExecInput, agentprotocol.OpExecResize, agentprotocol.OpExecClose:
		return 30 * time.Second
	case agentprotocol.OpExecRun:
		return (agentprotocol.MaxExecRunTimeoutS + 10) * time.Second // 요청의 timeout_s가 먼저 걸린다
	default:
		return 5 * time.Minute
	}
}
