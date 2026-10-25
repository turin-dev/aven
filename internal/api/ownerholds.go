package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/turin-dev/aven/internal/store"
)

// 서버 owner의 프로젝트·작업 공간 정지(store.OwnerHold). 정지하면 실행 중인 서비스를 모두 멈추고, 재개할 때까지 새 배포(웹훅 포함),
// 재시작, scale, cron 즉시 실행을 막는다(deploy.checkSuspended). 서버 owner만 걸고 풀 수 있다.
// 서비스 단위 정지(service)와 배포 차단(deploy)은 서버 관리자도 서비스 어드민 탭에서 걸고 푼다(service_admin.go).

// holdTarget은 정지 대상의 이름과 정지할 때 멈출 인스턴스다(배포 차단은 멈추지 않는다).
func (s *Server) holdTarget(ctx context.Context, scope, id string) (name string, insts []*store.Instance, err error) {
	switch scope {
	case store.HoldProject:
		p, err := s.Store.GetProject(ctx, id)
		if err != nil {
			return "", nil, err
		}
		insts, err := s.projectsInstances(ctx, []string{p.ID})
		return "프로젝트 " + p.Name, insts, err
	case store.HoldWorkspace:
		ws, err := s.Store.GetWorkspace(ctx, id)
		if err != nil {
			return "", nil, err
		}
		all, err := s.Store.ListProjects(ctx)
		if err != nil {
			return "", nil, err
		}
		var projects []string
		for _, p := range all {
			if p.WorkspaceID == ws.ID {
				projects = append(projects, p.ID)
			}
		}
		insts, err := s.projectsInstances(ctx, projects)
		return "작업 공간 " + ws.Name, insts, err
	case store.HoldService, store.HoldDeploy:
		svc, err := s.Store.GetService(ctx, id)
		if err != nil {
			return "", nil, err
		}
		if scope == store.HoldDeploy {
			return "서비스 " + svc.Name + "의 배포", nil, nil
		}
		insts, err := s.Store.ListInstancesByService(ctx, svc.ID)
		return "서비스 " + svc.Name, insts, err
	}
	return "", nil, errBadHoldScope
}

var errBadHoldScope = errors.New("scope는 project, workspace, service, deploy 중 하나입니다")

// heldRunning은 정지 때 멈출(실행 중이거나 곧 실행될) 인스턴스인지다(usage.running과 같은 기준).
func heldRunning(in *store.Instance) bool {
	switch in.Status {
	case store.InstOffline, store.InstIdle:
		return false
	case store.InstFailed:
		return in.CurrentDeploymentID != nil
	}
	return true
}

func (s *Server) projectsInstances(ctx context.Context, projects []string) ([]*store.Instance, error) {
	own := map[string]bool{}
	for _, pid := range projects {
		svcs, err := s.Store.ListServices(ctx, pid)
		if err != nil {
			return nil, err
		}
		for _, sv := range svcs {
			own[sv.ID] = true
		}
	}
	all, err := s.Store.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	var out []*store.Instance
	for _, in := range all {
		if own[in.ServiceID] {
			out = append(out, in)
		}
	}
	return out, nil
}

func (s *Server) listOwnerHolds(w http.ResponseWriter, r *http.Request) {
	hs, err := s.Store.ListOwnerHolds(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, hs)
}

func (s *Server) createOwnerHold(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Scope    string `json:"scope"`
		TargetID string `json:"target_id"`
		Reason   string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	s.placeHold(w, r, in.Scope, in.TargetID, in.Reason)
}

// placeHold는 정지(또는 배포 차단)를 걸고 대상의 실행 중인 인스턴스를 멈춘다.
func (s *Server) placeHold(w http.ResponseWriter, r *http.Request, scope, targetID, reason string) {
	ctx := context.WithoutCancel(r.Context())
	reason = strings.TrimSpace(reason)
	if len([]rune(reason)) > 200 {
		writeErr(w, 400, "사유는 200자까지입니다")
		return
	}
	name, insts, err := s.holdTarget(ctx, scope, targetID)
	if errors.Is(err, errBadHoldScope) {
		writeErr(w, 400, err.Error())
		return
	} else if err != nil {
		s.storeErr(w, err)
		return
	}
	if _, err := s.Store.GetOwnerHold(ctx, scope, targetID); err == nil {
		if scope == store.HoldDeploy {
			writeErr(w, 409, "이미 배포가 막혀 있습니다")
		} else {
			writeErr(w, 409, "이미 정지되어 있습니다")
		}
		return
	}
	// 기록을 먼저 남겨 멈추는 동안 들어오는 배포도 막는다.
	h := &store.OwnerHold{Scope: scope, TargetID: targetID, Reason: reason, CreatedBy: principalFrom(r).display()}
	if err := s.Store.SetOwnerHold(ctx, h); err != nil {
		s.storeErr(w, err)
		return
	}
	failed := map[string]string{}
	for _, inst := range insts {
		if !heldRunning(inst) {
			continue
		}
		if err := s.Deploy.Stop(ctx, inst); err != nil {
			failed[inst.ID] = err.Error()
			continue
		}
		h.StoppedInstances = append(h.StoppedInstances, inst.ID)
	}
	if err := s.Store.SetOwnerHold(ctx, h); err != nil {
		s.storeErr(w, err)
		return
	}
	kind, msg := "owner.hold_created", fmt.Sprintf("%s를 정지하고 서비스 %d개를 멈췄습니다", name, len(h.StoppedInstances))
	if scope == store.HoldDeploy {
		kind, msg = "service.deploy_blocked", name+"를 막았습니다"
	}
	if reason != "" {
		msg += " (사유: " + reason + ")"
	}
	s.audit(r, kind, targetID, msg)
	writeJSON(w, 200, map[string]any{"hold": h, "failed": failed})
}

func (s *Server) releaseOwnerHold(w http.ResponseWriter, r *http.Request) {
	s.liftHold(w, r, r.PathValue("scope"), r.PathValue("tid"))
}

// liftHold는 정지(또는 배포 차단)를 풀고 정지 때 멈춘 인스턴스를 다시 띄운다.
func (s *Server) liftHold(w http.ResponseWriter, r *http.Request, scope, id string) {
	ctx := context.WithoutCancel(r.Context())
	h, err := s.Store.GetOwnerHold(ctx, scope, id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	name, _, err := s.holdTarget(ctx, scope, id)
	if err != nil {
		name = scope + " " + id // 대상이 지워졌어도 기록은 풀 수 있다
	}
	if err := s.Store.DeleteOwnerHold(ctx, scope, id); err != nil {
		s.storeErr(w, err)
		return
	}
	restarted, failed := []string{}, map[string]string{}
	for _, iid := range h.StoppedInstances {
		inst, err := s.Store.GetInstance(ctx, iid)
		if err != nil || inst.Status != store.InstOffline {
			continue // 지워졌거나 이미 다시 떠 있다
		}
		if other, err := s.Store.InstanceOwnerHold(ctx, iid); err == nil && other != nil {
			continue // 프로젝트·작업 공간·서비스의 다른 정지가 남아 있다
		}
		if _, err := s.Deploy.StartAgain(ctx, inst); err != nil {
			failed[iid] = err.Error()
			continue
		}
		restarted = append(restarted, iid)
	}
	slices.Sort(restarted)
	if scope == store.HoldDeploy {
		s.audit(r, "service.deploy_unblocked", id, name+" 차단을 풀었습니다")
	} else {
		s.audit(r, "owner.hold_released", id, fmt.Sprintf("%s의 정지를 풀고 서비스 %d개를 다시 시작합니다", name, len(restarted)))
	}
	writeJSON(w, 200, map[string]any{"restarted": restarted, "failed": failed})
}
