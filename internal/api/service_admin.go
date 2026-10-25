package api

import (
	"errors"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// 서비스 패널의 "어드민" 탭: 서버 관리자(users.role owner/admin)가 보는 서비스 운영 정보다.
// 프로젝트를 볼 수 있어야 하고(라우트 권한 R) 서버 관리자여야 한다. 워크스페이스 관리자는 볼 수 없다.

type adminContainer struct {
	Name         string              `json:"name"`
	Role         string              `json:"role"`
	DeploymentID string              `json:"deployment_id"`
	Replica      string              `json:"replica"`
	Status       string              `json:"status"`
	Running      bool                `json:"running"`
	ExitCode     int                 `json:"exit_code"`
	RestartCount int                 `json:"restart_count"`
	StartedAt    *time.Time          `json:"started_at"`
	IPs          map[string][]string `json:"ips"`
	Volumes      []string            `json:"volumes"`
}

type adminRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type serviceAdminView struct {
	Workspace       adminRef            `json:"workspace"`
	Project         adminRef            `json:"project"`
	Environment     adminRef            `json:"environment"`
	ServiceID       string              `json:"service_id"`
	InstanceID      string              `json:"instance_id"`
	CurrentDeployID *string             `json:"current_deployment_id"`
	Node            *store.Node         `json:"node"`
	Containers      []adminContainer    `json:"containers"`
	ContainersError string              `json:"containers_error,omitempty"`
	Holds           []*store.OwnerHold  `json:"holds"`
	Audit           []*store.AuditEntry `json:"audit"`
	// AuditHidden은 서버 owner가 서버 관리자에게 감사 기록 기능을 막아 둔 경우다.
	AuditHidden bool `json:"audit_hidden"`
	// CanControl은 이 서비스를 정지하거나 배포를 막을 수 있는지다(서버 owner가 service_controls를 막으면 false).
	CanControl bool `json:"can_control"`
}

func (s *Server) getServiceAdmin(w http.ResponseWriter, r *http.Request) {
	if !s.instanceAdminRequest(r) {
		writeErr(w, 403, "서버 관리자만 볼 수 있습니다")
		return
	}
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	v := serviceAdminView{
		Project:         adminRef{c.p.ID, c.p.Name},
		Environment:     adminRef{c.env.ID, c.env.Name},
		ServiceID:       c.svc.ID,
		InstanceID:      c.in.ID,
		CurrentDeployID: c.in.CurrentDeploymentID,
		Containers:      []adminContainer{},
		Holds:           []*store.OwnerHold{},
		Audit:           []*store.AuditEntry{},
	}
	if ws, err := s.Store.GetWorkspace(ctx, c.p.WorkspaceID); err == nil {
		v.Workspace = adminRef{ws.ID, ws.Name}
	} else {
		v.Workspace = adminRef{ID: c.p.WorkspaceID}
	}
	nodeID := c.in.NodeID
	if nodeID == "" {
		nodeID = store.LocalNodeID
	}
	if n, err := s.Store.GetNode(ctx, nodeID); err == nil {
		v.Node = n
	}
	for _, h := range [][2]string{{store.HoldService, c.svc.ID}, {store.HoldDeploy, c.svc.ID}, {store.HoldProject, c.p.ID}, {store.HoldWorkspace, c.p.WorkspaceID}} {
		if hold, err := s.Store.GetOwnerHold(ctx, h[0], h[1]); err == nil {
			v.Holds = append(v.Holds, hold)
		} else if !errors.Is(err, store.ErrNotFound) {
			s.storeErr(w, err)
			return
		}
	}
	v.CanControl = s.adminCapAllowed(r, "service_controls")
	if s.adminCapAllowed(r, "audit") {
		audit, err := s.Store.ListAuditForTargets(ctx, []string{c.svc.ID, c.in.ID, c.p.ID, c.env.ID}, 50)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		v.Audit = audit
	} else {
		v.AuditHidden = true
	}
	if s.Deploy == nil {
		v.ContainersError = "docker is not configured"
	} else if cs, err := s.Deploy.InstanceContainersOn(ctx, nodeID, c.in.ID); err != nil {
		v.ContainersError = err.Error()
	} else {
		for name, st := range cs {
			ac := adminContainer{
				Name: name, Role: st.Labels[node.LabelRole], DeploymentID: st.Labels[node.LabelDeploy], Replica: st.Labels[node.LabelReplica],
				Status: st.Status, Running: st.Running, ExitCode: st.ExitCode, RestartCount: st.RestartCount, IPs: st.IPs, Volumes: st.Volumes,
			}
			if !st.StartedAt.IsZero() {
				t := st.StartedAt
				ac.StartedAt = &t
			}
			if ac.Volumes == nil {
				ac.Volumes = []string{}
			}
			v.Containers = append(v.Containers, ac)
		}
		sort.Slice(v.Containers, func(i, j int) bool { return v.Containers[i].Name < v.Containers[j].Name })
	}
	writeJSON(w, 200, v)
}

// adminCapAllowed는 요청자가 서버 관리자이고 owner가 그 기능을 막지 않았는지다(주체가 없는 테스트 호출은 허용).
func (s *Server) adminCapAllowed(r *http.Request, capID string) bool {
	p := principalFrom(r)
	return p == nil || slices.Contains(s.allowedAdminCaps(r.Context(), p), capID)
}

// serviceHoldScope는 서비스 어드민 탭이 걸 수 있는 정지 종류다(service: 정지, deploy: 배포 차단).
func serviceHoldScope(scope string) bool {
	return scope == store.HoldService || scope == store.HoldDeploy
}

func (s *Server) serviceControlAllowed(w http.ResponseWriter, r *http.Request) bool {
	if !s.instanceAdminRequest(r) {
		writeErr(w, 403, "서버 관리자만 할 수 있습니다")
		return false
	}
	if !s.adminCapAllowed(r, "service_controls") {
		writeErr(w, 403, "서버 owner가 서버 관리자의 서비스 정지·배포 차단 기능을 막아 두었습니다")
		return false
	}
	return true
}

func (s *Server) createServiceHold(w http.ResponseWriter, r *http.Request) {
	if !s.serviceControlAllowed(w, r) {
		return
	}
	var in struct {
		Scope  string `json:"scope"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !serviceHoldScope(in.Scope) {
		writeErr(w, 400, "scope는 service 또는 deploy입니다")
		return
	}
	if s.Deploy != nil && s.Deploy.IsSelfService(r.PathValue("sid")) && in.Scope == store.HoldService {
		writeErr(w, 400, "aven-server 자신은 정지할 수 없습니다")
		return
	}
	s.placeHold(w, r, in.Scope, r.PathValue("sid"), in.Reason)
}

func (s *Server) deleteServiceHold(w http.ResponseWriter, r *http.Request) {
	if !s.serviceControlAllowed(w, r) {
		return
	}
	if !serviceHoldScope(r.PathValue("scope")) {
		writeErr(w, 400, "scope는 service 또는 deploy입니다")
		return
	}
	s.liftHold(w, r, r.PathValue("scope"), r.PathValue("sid"))
}
