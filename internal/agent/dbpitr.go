package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
)

// 시점 복구(PITR) helper(agentprotocol/dbpitr.go의 고정 스크립트). validateDBTool이 공통 필드를 검사한 뒤 여기로 온다.

// validatePITR는 Action이 있는 요청의 PITR 전용 필드를 검사한다.
func validatePITR(r agentprotocol.DBToolRequest, restore bool) error {
	if !agentprotocol.PITRSupported(r.Kind) {
		return fmt.Errorf("point-in-time recovery is not supported for %q", r.Kind)
	}
	needVolume := false
	switch r.Action {
	case agentprotocol.PITRBase:
		if restore {
			return errors.New("pitr-base is a backup action")
		}
	case agentprotocol.PITRWAL:
		if restore || r.Kind != "postgres" {
			return errors.New("pitr-wal is a postgres backup action")
		}
		needVolume = true
	case agentprotocol.PITRBinlog:
		if restore || r.Kind != "mysql" {
			return errors.New("pitr-binlog is a mysql backup action")
		}
		if len(r.Files) == 0 || len(r.Files) > agentprotocol.PITRMaxFiles {
			return fmt.Errorf("pitr-binlog needs 1..%d files", agentprotocol.PITRMaxFiles)
		}
		for _, f := range r.Files {
			if !agentprotocol.PITRFileRe.MatchString(f) {
				return fmt.Errorf("binlog name %q is not allowed", f)
			}
		}
	case agentprotocol.PITRRestore:
		if !restore {
			return errors.New("pitr-restore is a restore action")
		}
		if _, err := time.Parse(time.RFC3339, r.TargetTime); err != nil {
			return errors.New("pitr-restore needs an RFC3339 target_time")
		}
		if r.Kind == "postgres" {
			needVolume = true
			if !agentprotocol.PITRDataDirOK(r.MountPath, r.DataDir) {
				return errors.New("data_dir must be a subdirectory of the volume mount path")
			}
		} else if !agentprotocol.PITRFileRe.MatchString(r.BaseFile) || r.BasePosition == 0 {
			return errors.New("pitr-restore needs base_file and base_position")
		}
	default:
		return fmt.Errorf("database action %q is not allowed", r.Action)
	}
	if needVolume {
		if !volumeRe.MatchString(r.Volume) {
			return fmt.Errorf("volume %q is not allowed", r.Volume)
		}
		if !agentprotocol.ValidPITRPath(r.MountPath) || !mountPathRe.MatchString(r.MountPath) {
			return fmt.Errorf("mount path %q is not allowed", r.MountPath)
		}
	} else if r.Volume != "" || r.MountPath != "" {
		return errors.New("this action does not take a volume")
	}
	if r.Action != agentprotocol.PITRBinlog && len(r.Files) > 0 {
		return errors.New("files are only for pitr-binlog")
	}
	return nil
}

// pitrTool은 요청에 맞는 고정 스크립트를 고른다(host는 DB 컨테이너 주소, 볼륨 작업에서는 비어 있을 수 있다).
func pitrTool(r agentprotocol.DBToolRequest, host string) (*agentprotocol.DBTool, error) {
	creds := agentprotocol.DBCreds{User: r.User, Database: r.Database, Password: r.Password}
	switch r.Action {
	case agentprotocol.PITRBase:
		return agentprotocol.DBPITRBaseTool(r.Kind, host, r.Port, creds)
	case agentprotocol.PITRWAL:
		return agentprotocol.DBPITRWALTool(r.MountPath)
	case agentprotocol.PITRBinlog:
		return agentprotocol.DBPITRBinlogTool(host, r.Port, creds, r.Files)
	case agentprotocol.PITRRestore:
		t, _ := time.Parse(time.RFC3339, r.TargetTime)
		return agentprotocol.DBPITRRestoreTool(r.Kind, host, r.Port, creds, agentprotocol.PITRRestoreOpts{
			Target: t, Mount: r.MountPath, DataDir: r.DataDir, KeepArchive: r.KeepArchive, BaseFile: r.BaseFile, BasePosition: r.BasePosition})
	}
	return nil, fmt.Errorf("database action %q is not allowed", r.Action)
}

// pitrToolSpec은 PITR helper 컨테이너 설정을 만든다. postgres 복원은 멈춘 DB의 볼륨에 쓰고, 나머지는 실행 중인 DB가 필요하다.
// pitr-wal은 데이터 볼륨을 읽기 전용으로 붙인다.
func (h *Handler) pitrToolSpec(ctx context.Context, r agentprotocol.DBToolRequest, restore bool, st *node.ContainerState) (node.RunSpec, error) {
	stopped := r.Action == agentprotocol.PITRRestore && r.Kind == "postgres"
	host := ""
	if stopped && st.Running {
		return node.RunSpec{}, errors.New("the database must be stopped before a point-in-time restore")
	}
	if !stopped {
		if !st.Running {
			return node.RunSpec{}, fmt.Errorf("database container %s is not running", r.Container)
		}
		if host = dbHost(st.IP(r.Network)); host == "" {
			return node.RunSpec{}, fmt.Errorf("database container %s has no address in %s", r.Container, r.Network)
		}
	}
	if r.Volume != "" {
		mounted := false
		for _, v := range st.Volumes {
			mounted = mounted || v == r.Volume
		}
		if !mounted {
			return node.RunSpec{}, fmt.Errorf("volume %s is not mounted by %s", r.Volume, r.Container)
		}
	}
	tool, err := pitrTool(r, host)
	if err != nil {
		return node.RunSpec{}, err
	}
	prefix := "keel-dbb-"
	if restore {
		prefix = "keel-dbr-"
	}
	l := st.Labels
	sh := "/bin/sh"
	spec := node.RunSpec{
		Name: prefix + r.Job, Image: r.Image, Env: tool.Env, Network: r.Network,
		Labels: map[string]string{node.LabelManaged: "true", node.LabelRole: node.RoleHelper, node.LabelProject: l[node.LabelProject],
			node.LabelEnv: l[node.LabelEnv], node.LabelService: l[node.LabelService]},
		Entrypoint: &sh, Cmd: []string{"-c", tool.Script},
	}
	if r.Volume != "" {
		spec.Env["DATADIR"] = r.MountPath
		spec.Mounts = []node.Mount{{Volume: r.Volume, Path: r.MountPath, ReadOnly: r.Action == agentprotocol.PITRWAL}}
	}
	if err := ValidateRunSpec(spec, map[string]bool{node.RoleHelper: true}); err != nil {
		return node.RunSpec{}, err
	}
	return spec, nil
}
