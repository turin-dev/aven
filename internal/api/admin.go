package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

type instanceNodeSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Local       bool   `json:"is_local"`
	Schedulable bool   `json:"schedulable"`
}

type instanceOverview struct {
	*store.InstanceInventory
	Nodes       []instanceNodeSummary `json:"nodes"`
	GeneratedAt time.Time             `json:"generated_at"`
}

func (s *Server) getInstanceOverview(w http.ResponseWriter, r *http.Request) {
	var out instanceOverview
	out.Nodes = []instanceNodeSummary{}
	err := s.Store.Tx(r.Context(), func(tx *store.Store) (err error) {
		out.InstanceInventory, err = tx.InstanceInventory(r.Context())
		if err != nil {
			return err
		}
		nodes, err := tx.ListNodes(r.Context())
		if err != nil {
			return err
		}
		for _, n := range nodes {
			status := "online"
			if !n.IsLocal {
				status = s.nodeStatus(n)
			}
			out.Nodes = append(out.Nodes, instanceNodeSummary{n.ID, n.Name, status, n.IsLocal, n.Schedulable})
		}
		return nil
	})
	if err != nil {
		s.storeErr(w, err)
		return
	}
	out.GeneratedAt = time.Now().UTC()
	writeJSON(w, 200, out)
}

// deleteInstanceWorkspace는 서버 관리자가 작업 공간을 프로젝트째 지운다. 서버 관리자는 작업 공간 멤버십이 없어도 되며,
// 실수 방지를 위해 본문의 confirm이 작업 공간 이름과 같아야 한다. 기본 작업 공간은 지울 수 없다.
func (s *Server) deleteInstanceWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, err := s.Store.GetWorkspace(r.Context(), r.PathValue("wsid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var in struct {
		Confirm string `json:"confirm"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Confirm != ws.Name {
		writeErr(w, 400, "확인용 작업 공간 이름이 일치하지 않습니다")
		return
	}
	if ws.Default {
		writeErr(w, 400, "기본 작업 공간은 지울 수 없습니다")
		return
	}
	ctx := context.WithoutCancel(r.Context())
	projects, err := s.Store.ListProjects(ctx)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	deleted := 0
	for _, p := range projects {
		if p.WorkspaceID != ws.ID {
			continue
		}
		if err := s.destroyProject(ctx, p); err != nil {
			s.storeErr(w, err)
			return
		}
		deleted++
		s.audit(r, "project.deleted", p.ID, fmt.Sprintf("서버 관리자가 작업 공간 %s를 지우면서 프로젝트 %s를 지웠습니다", ws.Name, p.Name))
	}
	if err := s.Store.DeleteWorkspace(ctx, ws.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "workspace.deleted", ws.ID, fmt.Sprintf("서버 관리자가 작업 공간 %s를 지웠습니다(프로젝트 %d개 포함)", ws.Name, deleted))
	w.WriteHeader(204)
}
