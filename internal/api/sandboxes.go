package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// ---- sandboxes ----
//

// 파일시스템은 docker commit으로 fork(새 샌드박스)하거나 이름 붙은 checkpoint(노드 로컬 이미지)로 남긴다.
// 유휴 제한(idle_timeout_minutes)이 지나도록 활동(exec, 파일, 셸, heartbeat)이 없으면 SandboxLoop가 지운다.

const (
	defaultSandboxImage   = "ubuntu:24.04"
	defaultSandboxIdle    = 30       // 분
	maxSandboxIdle        = 7 * 1440 // 분(0 = 유휴 삭제 안 함)
	defaultSandboxMem     = 1024
	minSandboxMem         = 128
	maxSandboxMem         = 16384
	defaultSandboxCPU     = 1.0
	maxSandboxCPU         = 16.0
	maxSandboxesPerEnv    = 50
	maxCheckpointsPerEnv  = 50
	maxSandboxFile        = node.MaxExecRunStdin
	maxSandboxVars        = 64
	maxSandboxVarBytes    = 64 << 10
	maxSandboxExecTimeout = agentprotocol.MaxExecRunTimeoutS - 10 // 안쪽 timeout(1)이 먼저 끝나게 남긴다
	sandboxCreatingGrace  = 10 * time.Minute
)

var (
	sandboxImageRe      = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:@+-]{0,254}$`)
	sandboxCheckpointRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,62}[a-z0-9])?$`)
	sandboxVarKeyRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
)

// sandboxKeepAlive는 컨테이너의 PID 1이다. 이미지 ENTRYPOINT와 상관없이 sh로 기다리고 TERM에 바로 끝난다.
const sandboxKeepAlive = `trap 'exit 0' TERM INT; while :; do sleep 3600 & wait $!; done`

// sandboxExecWrapper는 $1=timeout초(0이면 없음), $2=작업 디렉터리(비면 그대로), 나머지=argv로 실행한다.
// timeout(1)이 있으면 마감에 TERM(5초 뒤 KILL)을 보내 명령이 남아 돌지 않게 한다(종료 코드 124).
const sandboxExecWrapper = `t="$1"; d="$2"; shift 2
if [ -n "$d" ]; then cd -- "$d" || exit 1; fi
if [ "$t" -gt 0 ] 2>/dev/null && command -v timeout >/dev/null 2>&1; then exec timeout -k 5 "$t" "$@"; fi
exec "$@"`

const sandboxListScript = `cd -- "$1" || exit 1
for f in * .[!.]* ..?*; do
  [ -e "$f" ] || [ -L "$f" ] || continue
  if [ -L "$f" ]; then t=symlink; elif [ -d "$f" ]; then t=dir; else t=file; fi
  set -- $(stat -c '%s %Y' -- "$f" 2>/dev/null || echo "0 0")
  printf '%s\t%s\t%s\t%s\0' "$t" "$1" "$2" "$f"
done`

// sandboxBackend는 샌드박스 컨테이너 작업이다(deploy.Manager, 테스트에서는 가짜).
type sandboxBackend interface {
	StartSandbox(ctx context.Context, env *store.Environment, b *store.Sandbox, spec node.RunSpec) error
	InspectSandbox(ctx context.Context, b *store.Sandbox) (*node.ContainerState, error)
	RemoveSandbox(ctx context.Context, b *store.Sandbox) error
	CommitSandbox(ctx context.Context, nodeID, container, image string) error
	RemoveSandboxImage(ctx context.Context, nodeID, image string) error
	SandboxContainers(ctx context.Context, nodeID string) (map[string]string, error)
	ExecRun(ctx context.Context, nodeID, container string, req node.ExecRunRequest, timeout time.Duration) (*node.ExecOutput, error)
}

func (s *Server) sandboxOps() sandboxBackend {
	if s.sbxImpl != nil {
		return s.sbxImpl
	}
	if s.Deploy == nil {
		return nil
	}
	return s.Deploy
}

func sandboxSlug(id string) string { return strings.ReplaceAll(id, "_", "-") }

// sandboxContainerName은 컨테이너와 isolated 네트워크 이름이다(agent가 같은 규칙을 검사한다).
func sandboxContainerName(id string) string { return "keel-sbx-" + sandboxSlug(id) }

// sandboxSnapshotImage는 fork/checkpoint가 남기는 노드 로컬 이미지 이름이다.
func sandboxSnapshotImage(id string) string { return "keel/sandbox:" + sandboxSlug(id) }

func validSandboxPath(p string) bool {
	return p != "" && len(p) <= 4096 && strings.HasPrefix(p, "/") && !strings.ContainsRune(p, 0)
}

type sandboxOptions struct {
	IdleTimeoutMinutes *int              `json:"idle_timeout_minutes"`
	MemoryMB           int               `json:"memory_mb"`
	CPUCores           float64           `json:"cpu_cores"`
	Network            string            `json:"network"`
	Variables          map[string]string `json:"variables"`
}

func (o *sandboxOptions) normalize() (idle int, err error) {
	idle = defaultSandboxIdle
	if o.IdleTimeoutMinutes != nil {
		idle = max(*o.IdleTimeoutMinutes, 0) // 0 이하 = 유휴 삭제 안 함
	}
	if idle > maxSandboxIdle {
		return 0, fmt.Errorf("idle_timeout_minutes must be between 0 and %d", maxSandboxIdle)
	}
	if o.MemoryMB == 0 {
		o.MemoryMB = defaultSandboxMem
	}
	if o.MemoryMB < minSandboxMem || o.MemoryMB > maxSandboxMem {
		return 0, fmt.Errorf("memory_mb must be between %d and %d", minSandboxMem, maxSandboxMem)
	}
	if o.CPUCores == 0 {
		o.CPUCores = defaultSandboxCPU
	}
	if math.IsNaN(o.CPUCores) || o.CPUCores < 0.1 || o.CPUCores > maxSandboxCPU {
		return 0, fmt.Errorf("cpu_cores must be between 0.1 and %g", maxSandboxCPU)
	}
	o.CPUCores = math.Round(o.CPUCores*100) / 100
	switch strings.ToLower(o.Network) {
	case "", store.SandboxIsolated:
		o.Network = store.SandboxIsolated
	case store.SandboxPrivate:
		o.Network = store.SandboxPrivate
	default:
		return 0, errors.New(`network must be "isolated" or "private"`)
	}
	if err := checkSandboxEnv(o.Variables, maxSandboxVars); err != nil {
		return 0, fmt.Errorf("variables: %w", err)
	}
	return idle, nil
}

func checkSandboxEnv(vars map[string]string, limit int) error {
	if len(vars) > limit {
		return fmt.Errorf("at most %d entries", limit)
	}
	total := 0
	for k, v := range vars {
		if !sandboxVarKeyRe.MatchString(k) || strings.ContainsRune(v, 0) {
			return fmt.Errorf("%q is not a valid variable", k)
		}
		total += len(k) + len(v)
	}
	if total > maxSandboxVarBytes {
		return errors.New("too large")
	}
	return nil
}

// sandboxNode는 샌드박스를 띄울 노드다. 비면 로컬 노드.
func (s *Server) sandboxNode(ctx context.Context, id string) (string, error) {
	if store.IsLocalNode(id) {
		n, err := s.Store.LocalNode(ctx)
		if err != nil {
			return "", err
		}
		if !n.Schedulable {
			return "", deploy.ErrNodeUnavailable
		}
		return store.LocalNodeID, nil
	}
	n, err := s.Store.GetNode(ctx, id)
	if err != nil {
		return "", err
	}
	if !n.Schedulable || n.Draining || s.Agents == nil || !s.Agents.Online(n.ID) {
		return "", deploy.ErrNodeUnavailable
	}
	return n.ID, nil
}

func (s *Server) sandboxErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, deploy.ErrSandboxNodeOffline), errors.Is(err, deploy.ErrNodeUnavailable):
		writeErr(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrResourceLimit), errors.Is(err, context.Canceled):
		s.storeErr(w, err)
	default:
		s.Log.Warn("sandbox", "err", err)
		writeErr(w, http.StatusBadGateway, err.Error())
	}
}

func (s *Server) sandboxEvent(ctx context.Context, b *store.Sandbox, kind, msg string) {
	if s.Events != nil {
		s.Events.Emit(context.WithoutCancel(ctx), b.ProjectID, &b.EnvironmentID, nil, kind, msg)
	}
}

// startSandbox는 행을 creating으로 넣고 컨테이너를 띄운 뒤 running으로 바꾼다.
// 행을 먼저 넣으므로 고아 정리(SandboxLoop)가 막 뜬 컨테이너를 지우지 않는다.
func (s *Server) startSandbox(ctx context.Context, env *store.Environment, b *store.Sandbox, vars map[string]string) error {
	ops := s.sandboxOps()
	b.ProjectID, b.EnvironmentID = env.ProjectID, env.ID
	b.ContainerName = sandboxContainerName(b.ID)
	b.NetworkName = b.ContainerName
	if b.NetworkMode == store.SandboxPrivate {
		b.NetworkName = deploy.EnvNetwork(env.ID)
	}
	if err := s.Store.CreateSandbox(ctx, b); err != nil {
		return err
	}
	slug := sandboxSlug(b.ID)
	entry := "sh"
	spec := node.RunSpec{
		Name: b.ContainerName, Image: b.Image, Network: b.NetworkName, Restart: "unless-stopped", Hostname: slug,
		Entrypoint: &entry, Cmd: []string{"-c", sandboxKeepAlive}, MemoryMB: b.MemoryMB,
		CPUs: strconv.FormatFloat(b.CPUCores, 'f', -1, 64), Env: vars,
		Labels: map[string]string{
			node.LabelManaged: "true", node.LabelRole: node.RoleSandbox, node.LabelSandbox: b.ID,
			node.LabelProject: b.ProjectID, node.LabelEnv: b.EnvironmentID,
		},
	}
	if b.NetworkMode == store.SandboxPrivate {
		spec.Aliases = []string{slug} // 같은 환경의 서비스가 sbx-<id>로 찾는다
	}
	if err := ops.StartSandbox(ctx, env, b, spec); err != nil {
		cctx := context.WithoutCancel(ctx)
		_ = ops.RemoveSandbox(cctx, b)
		_ = s.Store.DeleteSandbox(cctx, b.ID)
		return err
	}
	if err := s.Store.MarkSandboxRunning(ctx, b.ID); err != nil {
		return err
	}
	b.Status = store.SandboxRunning
	return nil
}

// loadSandbox는 경로의 샌드박스를 읽는다. running이어야 하면 creating이나 만료된 것은 409다.
func (s *Server) loadSandbox(w http.ResponseWriter, r *http.Request, running bool) (*store.Sandbox, bool) {
	if s.sandboxOps() == nil {
		writeErr(w, http.StatusServiceUnavailable, "container runtime is not configured")
		return nil, false
	}
	b, err := s.Store.GetSandbox(r.Context(), r.PathValue("sbid"))
	if err != nil {
		s.storeErr(w, err)
		return nil, false
	}
	if running && b.Status != store.SandboxRunning {
		writeErr(w, http.StatusConflict, "sandbox is not running")
		return nil, false
	}
	if running && b.ExpiresAt != nil && !b.ExpiresAt.After(time.Now()) {
		writeErr(w, http.StatusConflict, "sandbox idle timeout has expired")
		return nil, false
	}
	return b, true
}

func (s *Server) touchSandbox(ctx context.Context, b *store.Sandbox) {
	now := time.Now().UTC()
	if err := s.Store.TouchSandbox(context.WithoutCancel(ctx), b.ID, now); err == nil {
		b.LastActivityAt = now
		if b.IdleTimeoutMinutes > 0 {
			t := now.Add(time.Duration(b.IdleTimeoutMinutes) * time.Minute)
			b.ExpiresAt = &t
		}
	}
}

func (s *Server) listSandboxes(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListSandboxes(r.Context(), r.PathValue("eid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createSandbox(w http.ResponseWriter, r *http.Request) {
	if s.sandboxOps() == nil {
		writeErr(w, http.StatusServiceUnavailable, "container runtime is not configured")
		return
	}
	env, err := s.Store.GetEnvironment(r.Context(), r.PathValue("eid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var in struct {
		sandboxOptions
		Image      string `json:"image"`
		NodeID     string `json:"node_id"`
		Checkpoint string `json:"checkpoint"` // 이름 또는 id
	}
	if !decode(w, r, &in) {
		return
	}
	idle, err := in.normalize()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	b := &store.Sandbox{ID: store.NewID("sbx"), NetworkMode: in.Network, MemoryMB: in.MemoryMB, CPUCores: in.CPUCores,
		IdleTimeoutMinutes: idle, Image: strings.TrimSpace(in.Image)}
	if in.Checkpoint != "" {
		if b.Image != "" {
			writeErr(w, http.StatusBadRequest, "image and checkpoint cannot be combined")
			return
		}
		cp, err := s.Store.FindSandboxCheckpoint(r.Context(), env.ID, in.Checkpoint)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "checkpoint not found in this environment")
			return
		}
		if in.NodeID != "" && store.NormNode(in.NodeID) != store.NormNode(cp.NodeID) {
			writeErr(w, http.StatusBadRequest, "a checkpoint can only be restored on the node where it was captured")
			return
		}
		in.NodeID, b.Image, b.CheckpointName = cp.NodeID, cp.Image, &cp.Name
	} else {
		if b.Image == "" {
			b.Image = defaultSandboxImage
		}
		// keel/* 는 서버가 만든 이미지(다른 환경의 checkpoint 포함)라 직접 고를 수 없다.
		if !sandboxImageRe.MatchString(b.Image) || strings.HasPrefix(b.Image, "keel/") {
			writeErr(w, http.StatusBadRequest, "invalid container image")
			return
		}
	}
	if b.NodeID, err = s.sandboxNode(r.Context(), in.NodeID); err != nil {
		s.sandboxErr(w, err)
		return
	}
	if !s.sandboxCapacity(w, r.Context(), env.ID) {
		return
	}
	if err := s.startSandbox(r.Context(), env, b, in.Variables); err != nil {
		s.sandboxErr(w, err)
		return
	}
	s.sandboxEvent(r.Context(), b, "sandbox.create", fmt.Sprintf("샌드박스 %s를 만들었습니다 (%s)", b.ID, b.Image))
	writeJSON(w, http.StatusCreated, b)
}

func (s *Server) sandboxCapacity(w http.ResponseWriter, ctx context.Context, envID string) bool {
	items, err := s.Store.ListSandboxes(ctx, envID)
	if err != nil {
		s.storeErr(w, err)
		return false
	}
	if len(items) >= maxSandboxesPerEnv {
		writeErr(w, http.StatusConflict, fmt.Sprintf("an environment can run at most %d sandboxes", maxSandboxesPerEnv))
		return false
	}
	return true
}

func (s *Server) showSandbox(w http.ResponseWriter, r *http.Request) {
	b, err := s.Store.GetSandbox(r.Context(), r.PathValue("sbid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// removeSandbox는 컨테이너(와 fork가 만든 전용 이미지)를 지우고 행을 지운다.
func (s *Server) removeSandbox(ctx context.Context, b *store.Sandbox) error {
	ops := s.sandboxOps()
	if err := ops.RemoveSandbox(ctx, b); errors.Is(err, deploy.ErrSandboxNodeOffline) {
		// 노드가 다시 연결되면 sweepSandboxes가 DB에 없는 컨테이너를 지운다.
		s.Log.Warn("sandbox node offline; container will be removed on reconnect", "sandbox", b.ID, "node", b.NodeID)
	} else if err != nil {
		return err
	} else if b.Image == sandboxSnapshotImage(b.ID) {
		_ = ops.RemoveSandboxImage(ctx, b.NodeID, b.Image)
	}
	if err := s.Store.DeleteSandbox(ctx, b.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return nil
}

func (s *Server) destroySandbox(w http.ResponseWriter, r *http.Request) {
	b, ok := s.loadSandbox(w, r, false)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), time.Minute)
	defer cancel()
	if err := s.removeSandbox(ctx, b); err != nil {
		s.sandboxErr(w, err)
		return
	}
	s.sandboxEvent(ctx, b, "sandbox.destroy", fmt.Sprintf("샌드박스 %s를 삭제했습니다", b.ID))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) sandboxHeartbeat(w http.ResponseWriter, r *http.Request) {
	b, ok := s.loadSandbox(w, r, true)
	if !ok {
		return
	}
	s.touchSandbox(r.Context(), b)
	writeJSON(w, http.StatusOK, b)
}

// requireRunning은 컨테이너가 실제로 돌고 있는지 확인한다(commit 전).
func (s *Server) requireRunning(w http.ResponseWriter, ctx context.Context, b *store.Sandbox) bool {
	st, err := s.sandboxOps().InspectSandbox(ctx, b)
	if err != nil && !errors.Is(err, node.ErrNoContainer) {
		s.sandboxErr(w, err)
		return false
	}
	if st == nil || !st.Running {
		writeErr(w, http.StatusConflict, "sandbox container is not running")
		return false
	}
	return true
}

func (s *Server) forkSandbox(w http.ResponseWriter, r *http.Request) {
	src, ok := s.loadSandbox(w, r, true)
	if !ok {
		return
	}
	var in sandboxOptions
	if !decode(w, r, &in) {
		return
	}
	idle, err := in.normalize()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	env, err := s.Store.GetEnvironment(r.Context(), src.EnvironmentID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if !s.sandboxCapacity(w, r.Context(), env.ID) || !s.requireRunning(w, r.Context(), src) {
		return
	}
	ops := s.sandboxOps()
	parent := src.ID
	b := &store.Sandbox{ID: store.NewID("sbx"), NodeID: src.NodeID, NetworkMode: in.Network, MemoryMB: in.MemoryMB,
		CPUCores: in.CPUCores, IdleTimeoutMinutes: idle, ParentID: &parent}
	// fork의 이미지는 새 샌드박스 전용이라 삭제할 때 같이 지운다(removeSandbox).
	b.Image = sandboxSnapshotImage(b.ID)
	if err := ops.CommitSandbox(r.Context(), src.NodeID, src.ContainerName, b.Image); err != nil {
		s.sandboxErr(w, err)
		return
	}
	s.touchSandbox(r.Context(), src)
	if err := s.startSandbox(r.Context(), env, b, in.Variables); err != nil {
		_ = ops.RemoveSandboxImage(context.WithoutCancel(r.Context()), b.NodeID, b.Image)
		s.sandboxErr(w, err)
		return
	}
	s.sandboxEvent(r.Context(), b, "sandbox.fork", fmt.Sprintf("샌드박스 %s를 %s에서 fork했습니다", b.ID, src.ID))
	writeJSON(w, http.StatusCreated, b)
}

func (s *Server) createSandboxCheckpoint(w http.ResponseWriter, r *http.Request) {
	b, ok := s.loadSandbox(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !sandboxCheckpointRe.MatchString(in.Name) {
		writeErr(w, http.StatusBadRequest, "name must be 1-64 lowercase letters, digits, '.', '_' or '-'")
		return
	}
	list, err := s.Store.ListSandboxCheckpoints(r.Context(), b.EnvironmentID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	replacing := false
	for _, c := range list {
		replacing = replacing || c.Name == in.Name
	}
	if !replacing && len(list) >= maxCheckpointsPerEnv {
		writeErr(w, http.StatusConflict, fmt.Sprintf("an environment can hold at most %d checkpoints", maxCheckpointsPerEnv))
		return
	}
	if !s.requireRunning(w, r.Context(), b) {
		return
	}
	ops := s.sandboxOps()
	cp := &store.SandboxCheckpoint{ProjectID: b.ProjectID, EnvironmentID: b.EnvironmentID, NodeID: b.NodeID, Name: in.Name}
	// 같은 이름을 다시 캡처해도 이미지 이름은 매번 새로 만든다(이전 것을 쓰는 샌드박스가 있어도 덮어쓰지 않게).
	cp.Image = sandboxSnapshotImage(store.NewID("scp"))
	if err := ops.CommitSandbox(r.Context(), b.NodeID, b.ContainerName, cp.Image); err != nil {
		s.sandboxErr(w, err)
		return
	}
	s.touchSandbox(r.Context(), b)
	oldNode, oldImage, err := s.Store.SaveSandboxCheckpoint(r.Context(), cp)
	if err != nil {
		_ = ops.RemoveSandboxImage(context.WithoutCancel(r.Context()), cp.NodeID, cp.Image)
		s.storeErr(w, err)
		return
	}
	if oldImage != "" {
		_ = ops.RemoveSandboxImage(context.WithoutCancel(r.Context()), oldNode, oldImage)
	}
	s.sandboxEvent(r.Context(), b, "sandbox.checkpoint", fmt.Sprintf("샌드박스 %s의 checkpoint %s를 저장했습니다", b.ID, cp.Name))
	writeJSON(w, http.StatusCreated, cp)
}

func (s *Server) listSandboxCheckpoints(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListSandboxCheckpoints(r.Context(), r.PathValue("eid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) renameSandboxCheckpoint(w http.ResponseWriter, r *http.Request) {
	cp, err := s.Store.GetSandboxCheckpoint(r.Context(), r.PathValue("cpid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !sandboxCheckpointRe.MatchString(in.Name) {
		writeErr(w, http.StatusBadRequest, "name must be 1-64 lowercase letters, digits, '.', '_' or '-'")
		return
	}
	if err := s.Store.RenameSandboxCheckpoint(r.Context(), cp.ID, in.Name); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeErr(w, http.StatusConflict, "a checkpoint with that name already exists")
			return
		}
		s.storeErr(w, err)
		return
	}
	cp.Name = in.Name
	writeJSON(w, http.StatusOK, cp)
}

func (s *Server) deleteSandboxCheckpoint(w http.ResponseWriter, r *http.Request) {
	cp, err := s.Store.GetSandboxCheckpoint(r.Context(), r.PathValue("cpid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if ops := s.sandboxOps(); ops != nil {
		if err := ops.RemoveSandboxImage(r.Context(), cp.NodeID, cp.Image); err != nil {
			s.sandboxErr(w, err)
			return
		}
	}
	if err := s.Store.DeleteSandboxCheckpoint(r.Context(), cp.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// sandboxExecResult는 exec 응답이다. stdout/stderr는 JSON에서 base64다.
type sandboxExecResult struct {
	Stdout    []byte `json:"stdout"`
	Stderr    []byte `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
	Truncated bool   `json:"truncated"`
	TimedOut  bool   `json:"timed_out"`
}

// sandboxRun은 샌드박스에서 argv를 실행한다. 실행 전후로 활동 시각을 갱신한다(긴 명령 중에 유휴 삭제되지 않게).
func (s *Server) sandboxRun(ctx context.Context, b *store.Sandbox, argv []string, cwd string, env []string, stdin []byte, maxOut, timeoutS int) (*sandboxExecResult, error) {
	s.touchSandbox(ctx, b)
	cmd := append([]string{"sh", "-c", sandboxExecWrapper, "keel-exec", strconv.Itoa(timeoutS), cwd}, argv...)
	started := time.Now()
	out, err := s.sandboxOps().ExecRun(ctx, b.NodeID, b.ContainerName,
		node.ExecRunRequest{Cmd: cmd, Env: env, Stdin: stdin, MaxOutput: maxOut}, time.Duration(timeoutS+10)*time.Second)
	defer s.touchSandbox(ctx, b)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "timed out") {
			return &sandboxExecResult{ExitCode: -1, TimedOut: true}, nil
		}
		return nil, err
	}
	res := &sandboxExecResult{Stdout: out.Stdout, Stderr: out.Stderr, ExitCode: out.ExitCode, Truncated: out.Truncated}
	if res.ExitCode == 124 && timeoutS > 0 && time.Since(started) >= time.Duration(timeoutS)*time.Second-time.Second {
		res.TimedOut, res.ExitCode = true, -1
	}
	return res, nil
}

func (s *Server) sandboxExec(w http.ResponseWriter, r *http.Request) {
	b, ok := s.loadSandbox(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Cmd       []string          `json:"cmd"`
		Command   string            `json:"command"` // sh -lc로 실행할 한 줄(cmd 대신)
		Cwd       string            `json:"cwd"`
		Env       map[string]string `json:"env"`
		Stdin     []byte            `json:"stdin"`
		TimeoutS  int               `json:"timeout_s"`
		MaxOutput int               `json:"max_output"`
	}
	if !decode(w, r, &in) {
		return
	}
	argv := in.Cmd
	if in.Command != "" {
		if len(argv) > 0 {
			writeErr(w, http.StatusBadRequest, "cmd and command cannot be combined")
			return
		}
		argv = []string{"sh", "-lc", in.Command}
	}
	if in.TimeoutS == 0 {
		in.TimeoutS = maxSandboxExecTimeout
	}
	bad := len(argv) == 0 || len(argv) > 256 || in.TimeoutS < 1 || in.TimeoutS > maxSandboxExecTimeout ||
		in.MaxOutput < 0 || in.MaxOutput > node.MaxExecRunOutput || len(in.Stdin) > node.MaxExecRunStdin ||
		(in.Cwd != "" && !validSandboxPath(in.Cwd))
	for _, a := range argv {
		bad = bad || strings.ContainsRune(a, 0)
	}
	if bad {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("invalid command or limits (timeout_s 1..%d, stdin up to %d bytes, cwd absolute)", maxSandboxExecTimeout, node.MaxExecRunStdin))
		return
	}
	var env []string
	for k, v := range in.Env {
		env = append(env, k+"="+v)
	}
	if err := checkSandboxEnv(in.Env, 32); err != nil {
		writeErr(w, http.StatusBadRequest, "env: "+err.Error())
		return
	}
	if err := node.ValidateExecEnv(env); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := s.sandboxRun(r.Context(), b, argv, in.Cwd, env, in.Stdin, in.MaxOutput, in.TimeoutS)
	if err != nil {
		s.sandboxErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// fileErr는 파일 명령의 실패를 HTTP 오류로 바꾼다.
func fileErr(w http.ResponseWriter, res *sandboxExecResult) {
	msg := strings.TrimSpace(string(res.Stderr))
	if strings.Contains(msg, "No such file") || strings.Contains(msg, "can't cd") || strings.Contains(msg, "can't open") {
		writeErr(w, http.StatusNotFound, msg)
		return
	}
	if msg == "" {
		msg = fmt.Sprintf("command exited with %d", res.ExitCode)
	}
	writeErr(w, http.StatusUnprocessableEntity, msg)
}

func (s *Server) readSandboxFile(w http.ResponseWriter, r *http.Request) {
	b, ok := s.loadSandbox(w, r, true)
	if !ok {
		return
	}
	p := r.URL.Query().Get("path")
	if !validSandboxPath(p) {
		writeErr(w, http.StatusBadRequest, "path must be absolute")
		return
	}
	res, err := s.sandboxRun(r.Context(), b, []string{"cat", "--", p}, "", nil, nil, node.MaxExecRunOutput, 30)
	if err != nil {
		s.sandboxErr(w, err)
		return
	}
	if res.ExitCode != 0 {
		fileErr(w, res)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "content_base64": base64.StdEncoding.EncodeToString(res.Stdout),
		"size": len(res.Stdout), "truncated": res.Truncated})
}

func (s *Server) writeSandboxFile(w http.ResponseWriter, r *http.Request) {
	b, ok := s.loadSandbox(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Path          string `json:"path"`
		ContentBase64 string `json:"content_base64"`
		Mode          int    `json:"mode"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !validSandboxPath(in.Path) || strings.HasSuffix(in.Path, "/") || in.Mode < 0 || in.Mode > 0o7777 {
		writeErr(w, http.StatusBadRequest, "invalid file path or mode")
		return
	}
	content, err := base64.StdEncoding.DecodeString(in.ContentBase64)
	if err != nil || len(content) > maxSandboxFile {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("content_base64 must be base64 of at most %d bytes", maxSandboxFile))
		return
	}
	if in.Mode == 0 {
		in.Mode = 0o644
	}
	argv := []string{"sh", "-c", `mkdir -p -- "$(dirname -- "$1")" && cat > "$1" && chmod "$2" "$1"`, "keel-write", in.Path, fmt.Sprintf("%04o", in.Mode)}
	res, err := s.sandboxRun(r.Context(), b, argv, "", nil, content, 1024, 60)
	if err != nil {
		s.sandboxErr(w, err)
		return
	}
	if res.ExitCode != 0 {
		fileErr(w, res)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": in.Path, "size": len(content)})
}

type sandboxFileEntry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Type    string    `json:"type"` // file | dir | symlink
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// parseSandboxList는 sandboxListScript 출력(type\tsize\tmtime\tname\0)을 읽는다.
func parseSandboxList(dir string, out []byte) []sandboxFileEntry {
	entries := []sandboxFileEntry{}
	for _, rec := range bytes.Split(out, []byte{0}) {
		parts := strings.SplitN(string(rec), "\t", 4)
		if len(parts) != 4 || parts[3] == "" {
			continue
		}
		size, _ := strconv.ParseInt(parts[1], 10, 64)
		mt, _ := strconv.ParseInt(parts[2], 10, 64)
		entries = append(entries, sandboxFileEntry{Name: parts[3], Path: path.Join(dir, parts[3]), Type: parts[0],
			Size: size, ModTime: time.Unix(mt, 0).UTC()})
	}
	return entries
}

func (s *Server) listSandboxFiles(w http.ResponseWriter, r *http.Request) {
	b, ok := s.loadSandbox(w, r, true)
	if !ok {
		return
	}
	dir := r.URL.Query().Get("path")
	if dir == "" {
		dir = "/"
	}
	if !validSandboxPath(dir) {
		writeErr(w, http.StatusBadRequest, "path must be absolute")
		return
	}
	dir = path.Clean(dir)
	res, err := s.sandboxRun(r.Context(), b, []string{"sh", "-c", sandboxListScript, "keel-ls", dir}, "", nil, nil, node.MaxExecRunOutput, 30)
	if err != nil {
		s.sandboxErr(w, err)
		return
	}
	if res.ExitCode != 0 {
		fileErr(w, res)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": dir, "entries": parseSandboxList(dir, res.Stdout), "truncated": res.Truncated})
}

// sandboxShell은 샌드박스의 대화형 셸 WebSocket이다(프로토콜은 서비스 셸과 같다). 연결되어 있는 동안 유휴 타이머를 미룬다.
func (s *Server) sandboxShell(w http.ResponseWriter, r *http.Request) {
	if !wsSameOrigin(r) {
		writeErr(w, 403, "cross-origin request rejected")
		return
	}
	b, ok := s.loadSandbox(w, r, true)
	if !ok {
		return
	}
	q := r.URL.Query()
	cmd, err := ExecCommand(q.Get("cmd"))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	cols, rows := node.ClampTermSize(atoiDefault(q.Get("cols"), 80), atoiDefault(q.Get("rows"), 24))
	key, who := s.execUser(r)
	release, ok := s.execs.acquire(key, maxExecPerUser)
	if !ok {
		writeErr(w, 429, fmt.Sprintf("동시에 열 수 있는 셸은 %d개까지입니다", maxExecPerUser))
		return
	}
	defer release()
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(execMaxFrame)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	sess, err := s.execBackend().Exec(ctx, b.NodeID, b.ContainerName, cmd, cols, rows)
	if err != nil {
		conn.Write(ctx, websocket.MessageText, execErrorFrame("셸을 열지 못했습니다: "+err.Error()))
		conn.Close(websocket.StatusInternalError, "exec failed")
		return
	}
	defer sess.Close()
	s.sandboxEvent(ctx, b, "sandbox.shell", fmt.Sprintf("%s이 샌드박스 %s에서 셸을 열었습니다", who, b.ID))
	s.touchSandbox(ctx, b)
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.touchSandbox(ctx, b)
			}
		}
	}()
	idle := s.execIdle
	if idle <= 0 {
		idle = execIdleLimit
	}
	execPump(ctx, conn, sess, idle)
	s.touchSandbox(ctx, b)
}

// SandboxLoop는 유휴 제한이 지난 샌드박스를 지우고, 5분마다 DB에 없는 샌드박스 컨테이너(고아)와
// 서버가 만들다 멈춘 creating 행을 정리한다. ctx가 끝날 때까지 돈다.
func (s *Server) SandboxLoop(ctx context.Context) {
	if s.sandboxOps() == nil {
		return
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for i := 0; ; i++ {
		s.expireSandboxes(ctx, time.Now().UTC())
		if i%5 == 0 {
			s.sweepSandboxes(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) expireSandboxes(ctx context.Context, now time.Time) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	items, err := s.Store.ListExpiredSandboxes(cctx, now)
	if err != nil {
		s.Log.Warn("list expired sandboxes", "err", err)
		return
	}
	for _, b := range items {
		if err := s.removeSandbox(cctx, b); err != nil {
			s.Log.Warn("expire sandbox", "sandbox", b.ID, "err", err) // 노드가 꺼져 있으면 다음에 다시
			continue
		}
		s.sandboxEvent(cctx, b, "sandbox.expire", fmt.Sprintf("샌드박스 %s가 %d분 동안 쓰이지 않아 삭제되었습니다", b.ID, b.IdleTimeoutMinutes))
	}
}

func (s *Server) sweepSandboxes(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ops := s.sandboxOps()
	// 컨테이너 목록을 DB보다 먼저 읽는다: 그 사이에 만들어진 샌드박스는 행이 먼저 들어가므로 고아로 보이지 않는다.
	nodes := []string{store.LocalNodeID}
	if ns, err := s.Store.ListNodes(cctx); err == nil {
		for _, n := range ns {
			if !store.IsLocalNode(n.ID) && s.Agents != nil && s.Agents.Online(n.ID) {
				nodes = append(nodes, n.ID)
			}
		}
	}
	containers := map[string]map[string]string{}
	for _, nid := range nodes {
		if cs, err := ops.SandboxContainers(cctx, nid); err == nil {
			containers[nid] = cs
		}
	}
	all, err := s.Store.ListAllSandboxes(cctx)
	if err != nil {
		return
	}
	known := map[string]bool{}
	for _, b := range all {
		known[b.ID] = true
		if b.Status == store.SandboxCreating && time.Since(b.CreatedAt) > sandboxCreatingGrace {
			if err := s.removeSandbox(cctx, b); err == nil {
				s.Log.Info("removed stale creating sandbox", "sandbox", b.ID)
			}
		}
	}
	for nid, cs := range containers {
		for name, id := range cs {
			if known[id] {
				continue
			}
			orphan := &store.Sandbox{ID: id, NodeID: nid, ContainerName: name, NetworkName: name, NetworkMode: store.SandboxIsolated}
			s.Log.Info("removing orphan sandbox container", "container", name, "node", nid)
			_ = ops.RemoveSandbox(cctx, orphan)
		}
	}
}

// cleanupSandboxEnvironment는 환경(또는 프로젝트) 삭제 전에 그 환경의 샌드박스와 checkpoint 이미지를 지운다.
func (s *Server) cleanupSandboxEnvironment(ctx context.Context, environmentID string) error {
	if s.sandboxOps() == nil {
		return nil
	}
	bxs, err := s.Store.ListSandboxes(ctx, environmentID)
	if err != nil {
		return err
	}
	for _, b := range bxs {
		if err := s.removeSandbox(ctx, b); err != nil {
			return fmt.Errorf("sandbox %s: %w", b.ID, err)
		}
	}
	cps, err := s.Store.ListSandboxCheckpoints(ctx, environmentID)
	if err != nil {
		return err
	}
	for _, cp := range cps {
		_ = s.sandboxOps().RemoveSandboxImage(ctx, cp.NodeID, cp.Image) // 노드가 꺼져 있어도 삭제는 막지 않는다
		if err := s.Store.DeleteSandboxCheckpoint(ctx, cp.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	return nil
}
