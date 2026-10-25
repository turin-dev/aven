package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/turin-dev/aven/internal/catalog"
	"github.com/turin-dev/aven/internal/selfhost"
	"github.com/turin-dev/aven/internal/store"
)

// Aven on Aven: 서버 owner가 aven-server 자신을 캔버스의 서비스로 다룬다(docs/selfhost.md).
// self 서비스는 Aven 저장소를 Dockerfile로 빌드하는 repo 서비스이고, 그 프로젝트는 서버 owner만 볼 수 있다.

const (
	// settingSelfProjectOffered는 첫 실행 때 Aven 프로젝트를 만들어 줬는지(또는 기존 설치라 건너뛰었는지) 기록한다.
	settingSelfProjectOffered = "self_project_offered"
	defaultSelfRepo           = "https://github.com/turin-dev/aven"
	selfProjectName           = "aven"
	selfServiceName           = "aven-server"
	selfWorkspaceName         = "Aven"
)

// selfInfo는 self 서비스 인스턴스에 붙이는 지금 도는 aven-server의 정보다.
type selfInfo struct {
	// Mode는 aven-server가 도는 방식이다: container(self 컨테이너) | systemd(다음 배포가 컨테이너로 옮긴다) | process.
	Mode    string `json:"mode"`
	Unit    string `json:"unit,omitempty"`  // systemd 유닛
	Image   string `json:"image,omitempty"` // container면 지금 이미지
	Version string `json:"version"`
	// UpdateAvailable/UpdateBehind는 마지막 업데이트 확인 결과다(selfupdate.go). 캔버스 카드가 쓴다.
	UpdateAvailable bool `json:"update_available"`
	UpdateBehind    int  `json:"update_behind"`
}

func (s *Server) selfInfo() *selfInfo {
	v := &selfInfo{Mode: "process", Version: s.Version}
	switch {
	case selfhost.Active():
		v.Mode, v.Image = "container", selfhost.Image()
	default:
		if u := selfhost.SystemdUnit(); u != "" {
			v.Mode, v.Unit = "systemd", u
		}
	}
	return v
}

func (s *Server) selfServiceID() string {
	if s.Deploy == nil {
		return ""
	}
	return s.Deploy.SelfServiceID()
}

// isSelfProject는 pid가 self 서비스를 담은 프로젝트인지다.
func (s *Server) isSelfProject(ctx context.Context, pid string) bool {
	id := s.selfServiceID()
	if id == "" || s.Store == nil {
		return false
	}
	svc, err := s.Store.GetService(ctx, id)
	return err == nil && svc.ProjectID == pid
}

type selfServiceView struct {
	Enabled       bool        `json:"enabled"`
	FromEnv       bool        `json:"from_env"`
	ServiceID     string      `json:"service_id,omitempty"`
	ServiceName   string      `json:"service_name,omitempty"`
	ProjectID     string      `json:"project_id,omitempty"`
	WorkspaceID   string      `json:"workspace_id,omitempty"`
	EnvironmentID string      `json:"environment_id,omitempty"`
	RepoURL       string      `json:"repo_url,omitempty"`
	Branch        string      `json:"branch,omitempty"`
	Server        *selfInfo   `json:"server"`
	Update        *selfUpdate `json:"update,omitempty"`
}

func (s *Server) selfServiceView(ctx context.Context) *selfServiceView {
	v := &selfServiceView{Server: s.selfInfo()}
	if s.Deploy != nil {
		v.FromEnv = s.Deploy.SelfServiceFromEnv()
	}
	id := s.selfServiceID()
	if id == "" {
		return v
	}
	svc, err := s.Store.GetService(ctx, id)
	if err != nil {
		return v // 지정된 서비스가 지워졌다
	}
	v.Enabled, v.ServiceID, v.ServiceName, v.ProjectID = true, svc.ID, svc.Name, svc.ProjectID
	v.Update = s.selfUpdateView(ctx)
	v.Server.UpdateAvailable, v.Server.UpdateBehind = v.Update.Available, v.Update.Behind
	if p, err := s.Store.GetProject(ctx, svc.ProjectID); err == nil {
		v.EnvironmentID, v.WorkspaceID = p.DefaultEnvironmentID, p.WorkspaceID
		if in, err := s.Store.GetInstanceFor(ctx, svc.ID, p.DefaultEnvironmentID); err == nil {
			if in.RepoURL != nil {
				v.RepoURL = *in.RepoURL
			}
			v.Branch = in.Branch
		}
	}
	return v
}

func (s *Server) getSelfService(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.selfServiceView(r.Context()))
}

// createSelfService는 Aven 프로젝트와 aven-server 서비스를 만들고 self 서비스로 지정한다(POST /api/owner/self-service).
// 배포는 하지 않는다. 캔버스에서 처음 배포하면 systemd로 도는 서버가 aven-server 컨테이너로 옮겨진다.
func (s *Server) createSelfService(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RepoURL string `json:"repo_url"`
		Branch  string `json:"branch"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	if s.Deploy.SelfServiceFromEnv() {
		writeErr(w, 409, "self 서비스가 KEEL_SELF_SERVICE로 정해져 있습니다")
		return
	}
	if v := s.selfServiceView(ctx); v.Enabled {
		writeErr(w, 409, "Aven이 이미 캔버스에 있습니다: "+v.ServiceName)
		return
	}
	in.RepoURL, in.Branch = strings.TrimSpace(in.RepoURL), strings.TrimSpace(in.Branch)
	if in.RepoURL != "" && !s.validRepoURL(in.RepoURL) {
		writeErr(w, 400, "repo_url must be an https://, ssh:// or git@ URL")
		return
	}
	if in.Branch != "" && (!branchRe.MatchString(in.Branch) || strings.HasPrefix(in.Branch, "-")) {
		writeErr(w, 400, "invalid branch")
		return
	}
	p := principalFrom(r)
	if p == nil || p.User == nil {
		writeErr(w, 403, "server owner session required")
		return
	}
	if _, err := s.ensureSelfProject(ctx, p.User.ID, in.RepoURL, in.Branch); err != nil {
		s.storeErr(w, err)
		return
	}
	v := s.selfServiceView(ctx)
	s.audit(r, "owner.self_service", v.ServiceID, "Aven(aven-server)을 캔버스에서 관리합니다: "+v.RepoURL)
	writeJSON(w, 201, v)
}

// deleteSelfService는 self 서비스 지정을 해제한다. 프로젝트와 서비스는 남는다(평범한 서비스가 된다).
func (s *Server) deleteSelfService(w http.ResponseWriter, r *http.Request) {
	if err := s.Deploy.SetSelfService(r.Context(), ""); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	s.audit(r, "owner.self_service", "", "Aven 관리를 해제했습니다")
	writeJSON(w, 200, s.selfServiceView(r.Context()))
}

// ensureSelfProject는 전용 작업 공간 "Aven"에 aven 프로젝트와 aven-server 서비스(로컬 노드, replica 1, Dockerfile 빌드)를
// 만들고 self 서비스로 지정한다. 기본 작업 공간의 자원 한도나 멤버와 섞이지 않게 작업 공간을 따로 둔다(owner는 ownerID).
// self 서비스는 컨테이너를 띄우지 않으므로 작업 공간 자원 한도를 두지 않는다. 중간에 실패하면 만든 것을 지운다.
func (s *Server) ensureSelfProject(ctx context.Context, ownerID, repo, branch string) (svc *store.Service, err error) {
	if repo == "" {
		repo = env("KEEL_SELF_REPO", defaultSelfRepo)
	}
	if branch == "" {
		branch = env("KEEL_SELF_BRANCH", "main")
	}
	ws, err := s.Store.CreateWorkspace(ctx, selfWorkspaceName, ownerID)
	if err != nil {
		return nil, err
	}
	p := &store.Project{Name: selfProjectName, WorkspaceID: ws.ID, Description: "aven-server 자신. 배포하면 이 서버가 새 버전으로 바뀝니다."}
	defer func() {
		if err == nil {
			return
		}
		cctx := context.WithoutCancel(ctx)
		if p.ID != "" {
			if derr := s.Store.DeleteProject(cctx, p.ID); derr != nil {
				s.Log.Warn("self project cleanup", "project", p.ID, "err", derr)
			}
		}
		if derr := s.Store.DeleteWorkspace(cctx, ws.ID); derr != nil {
			s.Log.Warn("self workspace cleanup", "workspace", ws.ID, "err", derr)
		}
	}()
	envr, err := s.Store.CreateProject(ctx, p)
	if err != nil {
		return nil, err
	}
	// 저장소가 비공개(포크)여도 clone할 수 있게 만든 owner의 GitHub 연결을 프로젝트에 붙인다(대시보드에서 프로젝트를 만들 때와 같다).
	if s.Deploy.UserGitHubConnection(ctx, ownerID) != nil {
		if err = s.Store.SetProjectGitHubUser(ctx, p.ID, &ownerID); err != nil {
			return nil, err
		}
	}
	svc, err = s.createServiceFromDef(ctx, p, catalog.ServiceDef{Name: selfServiceName, Icon: "github", SourceType: "repo"}, &repo, branch, nil)
	if err != nil {
		return nil, err
	}
	in, err := s.Store.GetInstanceFor(ctx, svc.ID, envr.ID)
	if err != nil {
		return nil, err
	}
	in.NodeID = store.LocalNodeID
	in.Placement = nil
	in.Replicas = 1
	in.Builder = "dockerfile"
	in.DockerfilePath = "Dockerfile"
	in.AutoDeploy = false // 업그레이드는 owner가 배포를 눌러서 한다(push마다 이 서버를 바꾸지 않는다)
	if err = s.Store.UpdateInstanceConfig(ctx, in); err != nil {
		return nil, err
	}
	if err = s.Deploy.SetSelfService(ctx, svc.ID); err != nil {
		return nil, err
	}
	return svc, nil
}

// EnsureSelfProjectOnFirstRun은 새로 설치한 서버(서비스가 하나도 없음)에 Aven 프로젝트를 한 번 만들어 둔다.
// 기존 설치는 건너뛰고(어드민 › Owner · 초기 설정에서 추가한다), KEEL_SELF_PROJECT=off면 만들지 않는다.
func (s *Server) EnsureSelfProjectOnFirstRun(ctx context.Context) error {
	if s.Deploy == nil || s.Deploy.SelfServiceFromEnv() || strings.EqualFold(os.Getenv("KEEL_SELF_PROJECT"), "off") {
		return nil
	}
	if _, err := s.Store.GetSetting(ctx, settingSelfProjectOffered); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	insts, err := s.Store.ListInstances(ctx)
	if err != nil {
		return err
	}
	if len(insts) == 0 && s.selfServiceID() == "" {
		admin, err := s.Store.GetUserByEmail(ctx, store.BuiltinUserEmail)
		if err != nil {
			return err
		}
		svc, err := s.ensureSelfProject(ctx, admin.ID, "", "")
		if err != nil {
			return err
		}
		s.Log.Info("created the aven project for managing this server on the canvas", "service", svc.ID)
	}
	return s.Store.SetSetting(ctx, settingSelfProjectOffered, "1")
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
