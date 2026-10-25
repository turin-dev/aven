package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"strings"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
)

// DB 논리 백업/복원(db.backup, upload kind db.restore). 서버가 정할 수 있는 것은 DBToolRequest의 필드뿐이다:
// 스크립트는 agentprotocol의 고정 표에서 고르고, 접속 주소는 agent가 DB 컨테이너를 조회해 정한다.
// 대상은 이 노드의 aven 앱 컨테이너(keel.role=app)여야 하고, helper 컨테이너는 그 컨테이너의 project/env/service 라벨과
// 환경 네트워크를 그대로 쓴다(다른 환경 네트워크에 helper를 붙이지 못한다).

var dbJobRe = regexp.MustCompile(`^[a-z0-9]{6,40}$`)

// maxDBCred는 사용자/DB 이름/비밀번호 하나의 최대 길이다.
const maxDBCred = 1024

// validateDBTool은 요청 형식을 검사한다(컨테이너 상태 확인은 dbToolSpec이 한다).
func validateDBTool(r agentprotocol.DBToolRequest, restore bool) error {
	if !agentprotocol.IsDBKind(r.Kind) {
		return fmt.Errorf("database kind %q is not supported", r.Kind)
	}
	if !containerRe.MatchString(r.Container) {
		return fmt.Errorf("container name %q is not allowed", r.Container)
	}
	if !networkRe.MatchString(r.Network) {
		return fmt.Errorf("network %q is not allowed (only aven environment networks)", r.Network)
	}
	if r.Port < 1 || r.Port > 65535 {
		return errors.New("port must be 1..65535")
	}
	if !imageRe.MatchString(r.Image) {
		return fmt.Errorf("image %q is not allowed", r.Image)
	}
	if !dbJobRe.MatchString(r.Job) {
		return errors.New("invalid job id")
	}
	for _, v := range []string{r.User, r.Database, r.Password} {
		if len(v) > maxDBCred || strings.ContainsRune(v, 0) {
			return errors.New("database credentials are not allowed")
		}
	}
	if r.Kind == "mongo" && r.User == "" {
		return errors.New("mongo user is required")
	}
	if r.Action != "" {
		return validatePITR(r, restore)
	}
	if len(r.Files) > 0 || r.TargetTime != "" || r.DataDir != "" || r.KeepArchive || r.BaseFile != "" || r.BasePosition != 0 {
		return errors.New("point-in-time recovery fields need an action")
	}
	if restore && r.Kind == "redis" {
		if !volumeRe.MatchString(r.Volume) {
			return fmt.Errorf("volume %q is not allowed", r.Volume)
		}
		if !mountPathRe.MatchString(r.MountPath) || r.MountPath == "/" || strings.Contains(r.MountPath, "..") {
			return fmt.Errorf("mount path %q is not allowed", r.MountPath)
		}
	} else if r.Volume != "" || r.MountPath != "" {
		return errors.New("volume is only for a redis restore")
	}
	return nil
}

// dbHost는 컨테이너의 네트워크 주소 중 하나다(IPv4 우선: DB는 0.0.0.0에서만 listen하는 경우가 많다).
func dbHost(ips []string) string {
	var v6 string
	for _, ip := range ips {
		a, err := netip.ParseAddr(ip)
		if err != nil {
			continue
		}
		if a.Unmap().Is4() {
			return a.Unmap().String()
		}
		if v6 == "" {
			v6 = a.String()
		}
	}
	return v6
}

// dbToolSpec은 요청을 검사하고 DB 컨테이너를 확인한 뒤 helper 컨테이너 설정을 만든다.
func (h *Handler) dbToolSpec(ctx context.Context, r agentprotocol.DBToolRequest, restore bool) (node.RunSpec, error) {
	if err := validateDBTool(r, restore); err != nil {
		return node.RunSpec{}, err
	}
	states, err := h.Docker.InspectMany(ctx, []string{r.Container})
	if err != nil {
		return node.RunSpec{}, err
	}
	st := states[r.Container]
	if st == nil {
		return node.RunSpec{}, fmt.Errorf("database container %s does not exist on this node", r.Container)
	}
	l := st.Labels
	if l[node.LabelManaged] != "true" || l[node.LabelRole] != node.RoleApp || l[node.LabelEnv] == "" || l[node.LabelService] == "" || l[node.LabelProject] == "" {
		return node.RunSpec{}, fmt.Errorf("container %s is not a aven database container", r.Container)
	}
	if "keel-"+l[node.LabelEnv] != r.Network {
		return node.RunSpec{}, fmt.Errorf("container %s is not in network %s", r.Container, r.Network)
	}
	if r.Action != "" {
		return h.pitrToolSpec(ctx, r, restore, st)
	}
	creds := agentprotocol.DBCreds{User: r.User, Database: r.Database, Password: r.Password}
	redis := restore && r.Kind == "redis"
	host := ""
	if redis {
		// RDB는 멈춘 DB의 데이터 볼륨에 직접 놓는다(서버가 먼저 서비스를 멈춘다).
		if st.Running {
			return node.RunSpec{}, errors.New("the database must be stopped before an RDB restore")
		}
		mounted := false
		for _, v := range st.Volumes {
			mounted = mounted || v == r.Volume
		}
		if !mounted {
			return node.RunSpec{}, fmt.Errorf("volume %s is not mounted by %s", r.Volume, r.Container)
		}
	} else {
		if !st.Running {
			return node.RunSpec{}, fmt.Errorf("database container %s is not running", r.Container)
		}
		if host = dbHost(st.IP(r.Network)); host == "" {
			return node.RunSpec{}, fmt.Errorf("database container %s has no address in %s", r.Container, r.Network)
		}
	}
	var tool *agentprotocol.DBTool
	prefix := "keel-dbb-"
	if restore {
		tool, err = agentprotocol.DBRestoreTool(r.Kind, host, r.Port, creds)
		prefix = "keel-dbr-"
	} else {
		tool, err = agentprotocol.DBDumpTool(r.Kind, host, r.Port, creds)
	}
	if err != nil {
		return node.RunSpec{}, err
	}
	sh := "/bin/sh"
	spec := node.RunSpec{
		Name: prefix + r.Job, Image: r.Image, Env: tool.Env, Network: r.Network,
		Labels: map[string]string{node.LabelManaged: "true", node.LabelRole: node.RoleHelper, node.LabelProject: l[node.LabelProject],
			node.LabelEnv: l[node.LabelEnv], node.LabelService: l[node.LabelService]},
		Entrypoint: &sh, Cmd: []string{"-c", tool.Script},
	}
	if redis {
		spec.Env["DATADIR"] = r.MountPath
		spec.Mounts = []node.Mount{{Volume: r.Volume, Path: r.MountPath}}
	}
	// 같은 검사를 한 번 더(라벨 값 형식 등).
	if err := ValidateRunSpec(spec, map[string]bool{node.RoleHelper: true}); err != nil {
		return node.RunSpec{}, err
	}
	return spec, nil
}

// dbBackup은 db.backup을 처리한다: 덤프 도구의 stdout을 DataChunk로 보낸다.
func (h *Handler) dbBackup(ctx context.Context, r agentprotocol.DBToolRequest, stream func(any)) (any, error) {
	spec, err := h.dbToolSpec(ctx, r, false)
	if err != nil {
		return nil, err
	}
	w := &chunkWriter{ctx: ctx, stream: stream}
	tail := &lineTail{}
	code, err := h.Docker.RunStream(ctx, spec, nil, w, tail.add)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("%s dump exited with code %d: %s", r.Kind, code, tail.String())
	}
	return agentprotocol.DBBackupResult{Bytes: w.n}, nil
}

// dbRestore는 업로드가 끝난 db.restore를 실행한다: 복원 도구의 stdin으로 업로드 데이터를 넣는다.
func (h *Handler) dbRestore(ctx context.Context, r agentprotocol.DBToolRequest, in io.Reader) error {
	spec, err := h.dbToolSpec(ctx, r, true)
	if err != nil {
		return err
	}
	tail := &lineTail{}
	code, err := h.Docker.RunStream(ctx, spec, in, nil, tail.add)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("%s restore exited with code %d: %s", r.Kind, code, tail.String())
	}
	return nil
}

// maxVolumeSizes는 volume.sizes 결과의 최대 항목 수다.
const maxVolumeSizes = 10000

// volumeSizes는 이 노드의 aven 볼륨 크기다(docker system df -v; 느리므로 서버가 5분마다만 부른다).
func (h *Handler) volumeSizes(ctx context.Context) (agentprotocol.VolumeSizesResult, error) {
	vs, ok := h.Docker.(interface {
		VolumeSizes(ctx context.Context) (map[string]int64, error)
	})
	if !ok {
		return agentprotocol.VolumeSizesResult{}, errors.New("volume sizes are not available on this node")
	}
	sizes, err := vs.VolumeSizes(ctx)
	if err != nil {
		return agentprotocol.VolumeSizesResult{}, err
	}
	out := map[string]int64{}
	for name, n := range sizes {
		if volumeRe.MatchString(name) && n >= 0 && len(out) < maxVolumeSizes {
			out[name] = n
		}
	}
	return agentprotocol.VolumeSizesResult{Sizes: out}, nil
}
