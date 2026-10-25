package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/turin-dev/aven/internal/store"
)

// ---- 캔버스 서비스 그룹 ----

type groupInput struct {
	Name       *string           `json:"name"`
	Color      *string           `json:"color"`
	ServiceIDs *[]string         `json:"service_ids"`
	Rect       *store.CanvasRect `json:"rect"`
}

// layoutErr는 검증 실패를 400으로, 나머지는 storeErr로 돌려준다.
func (s *Server) layoutErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrInvalidLayout) {
		writeErr(w, 400, err.Error())
		return
	}
	s.storeErr(w, err)
}

// projectServiceIDs는 프로젝트 서비스 ID 집합이다.
func (s *Server) projectServiceIDs(ctx context.Context, pid string) (map[string]bool, error) {
	svcs, err := s.Store.ListServices(ctx, pid)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(svcs))
	for _, sv := range svcs {
		out[sv.ID] = true
	}
	return out, nil
}

func (s *Server) applyGroup(ctx context.Context, env *store.Environment, g *store.CanvasGroup, in *groupInput) string {
	if in.Name != nil {
		g.Name = *in.Name
	}
	if in.Color != nil {
		g.Color = *in.Color
	}
	if in.Rect != nil {
		r := *in.Rect
		g.Rect = &r
	}
	if in.ServiceIDs != nil {
		ids, err := s.projectServiceIDs(ctx, env.ProjectID)
		if err != nil {
			return "서비스 목록을 읽지 못했습니다"
		}
		for _, id := range *in.ServiceIDs {
			if !ids[id] {
				return "service_ids must be services of this project"
			}
		}
		g.ServiceIDs = append([]string{}, *in.ServiceIDs...)
	}
	return ""
}

// liveGroups는 환경의 그룹에서 지워진 서비스를 뺀 것이다(캔버스 응답과 목록에 쓴다).
func (s *Server) liveGroups(ctx context.Context, env *store.Environment) ([]*store.CanvasGroup, error) {
	gs, err := s.Store.ListGroups(ctx, env.ID)
	if err != nil {
		return nil, err
	}
	ids, err := s.projectServiceIDs(ctx, env.ProjectID)
	if err != nil {
		return nil, err
	}
	for _, g := range gs {
		kept := make([]string, 0, len(g.ServiceIDs))
		for _, id := range g.ServiceIDs {
			if ids[id] {
				kept = append(kept, id)
			}
		}
		g.ServiceIDs = kept
	}
	return gs, nil
}

func (s *Server) listGroups(w http.ResponseWriter, r *http.Request) {
	env, _, ok := s.envAndProject(w, r)
	if !ok {
		return
	}
	gs, err := s.liveGroups(r.Context(), env)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, gs)
}

func (s *Server) createGroup(w http.ResponseWriter, r *http.Request) {
	env, _, ok := s.envAndProject(w, r)
	if !ok {
		return
	}
	var in groupInput
	if !decode(w, r, &in) {
		return
	}
	g := &store.CanvasGroup{EnvironmentID: env.ID, ServiceIDs: []string{}}
	if msg := s.applyGroup(r.Context(), env, g, &in); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	if err := s.Store.CreateGroup(r.Context(), g); err != nil {
		s.layoutErr(w, err)
		return
	}
	writeJSON(w, 201, g)
}

func (s *Server) loadGroup(w http.ResponseWriter, r *http.Request) (*store.Environment, *store.CanvasGroup, bool) {
	env, _, ok := s.envAndProject(w, r)
	if !ok {
		return nil, nil, false
	}
	g, err := s.Store.GetGroup(r.Context(), r.PathValue("gid"))
	if err == nil && g.EnvironmentID != env.ID {
		err = store.ErrNotFound
	}
	if err != nil {
		s.storeErr(w, err)
		return nil, nil, false
	}
	return env, g, true
}

func (s *Server) patchGroup(w http.ResponseWriter, r *http.Request) {
	env, g, ok := s.loadGroup(w, r)
	if !ok {
		return
	}
	var in groupInput
	if !decode(w, r, &in) {
		return
	}
	if msg := s.applyGroup(r.Context(), env, g, &in); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	if err := s.Store.UpdateGroup(r.Context(), g); err != nil {
		s.layoutErr(w, err)
		return
	}
	writeJSON(w, 200, g)
}

func (s *Server) deleteGroup(w http.ResponseWriter, r *http.Request) {
	_, g, ok := s.loadGroup(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteGroup(r.Context(), g.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	w.WriteHeader(204)
}

// ---- 관측 대시보드 ----

type dashboardInput struct {
	Name    *string         `json:"name"`
	Widgets *[]store.Widget `json:"widgets"`
}

// checkWidgetRefs는 위젯의 서비스/환경이 이 프로젝트 것인지 확인한다.
func (s *Server) checkWidgetRefs(ctx context.Context, pid string, ws []store.Widget) string {
	var svcs map[string]bool
	envs := map[string]bool{}
	for _, wd := range ws {
		if wd.ServiceID != nil && *wd.ServiceID != "" {
			if svcs == nil {
				var err error
				if svcs, err = s.projectServiceIDs(ctx, pid); err != nil {
					return "서비스 목록을 읽지 못했습니다"
				}
			}
			if !svcs[*wd.ServiceID] {
				return "widget service_id must be a service of this project"
			}
		}
		if wd.EnvironmentID != nil && *wd.EnvironmentID != "" {
			id := *wd.EnvironmentID
			if _, done := envs[id]; !done {
				env, err := s.Store.GetEnvironment(ctx, id)
				envs[id] = err == nil && env.ProjectID == pid
			}
			if !envs[id] {
				return "widget environment_id must be an environment of this project"
			}
		}
	}
	return ""
}

func (s *Server) applyDashboard(ctx context.Context, d *store.Dashboard, in *dashboardInput) string {
	if in.Name != nil {
		d.Name = *in.Name
	}
	if in.Widgets != nil {
		d.Widgets = append([]store.Widget{}, *in.Widgets...)
		if msg := s.checkWidgetRefs(ctx, d.ProjectID, d.Widgets); msg != "" {
			return msg
		}
	}
	return ""
}

func (s *Server) listDashboards(w http.ResponseWriter, r *http.Request) {
	ds, err := s.Store.ListDashboards(r.Context(), r.PathValue("pid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, ds)
}

func (s *Server) createDashboard(w http.ResponseWriter, r *http.Request) {
	var in dashboardInput
	if !decode(w, r, &in) {
		return
	}
	d := &store.Dashboard{ProjectID: r.PathValue("pid"), Widgets: []store.Widget{}}
	if msg := s.applyDashboard(r.Context(), d, &in); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	if err := s.Store.CreateDashboard(r.Context(), d); err != nil {
		s.layoutErr(w, err)
		return
	}
	writeJSON(w, 201, d)
}

func (s *Server) loadDashboard(w http.ResponseWriter, r *http.Request) (*store.Dashboard, bool) {
	d, err := s.Store.GetDashboard(r.Context(), r.PathValue("dbid"))
	if err == nil && d.ProjectID != r.PathValue("pid") {
		err = store.ErrNotFound
	}
	if err != nil {
		s.storeErr(w, err)
		return nil, false
	}
	return d, true
}

func (s *Server) getDashboard(w http.ResponseWriter, r *http.Request) {
	if d, ok := s.loadDashboard(w, r); ok {
		writeJSON(w, 200, d)
	}
}

func (s *Server) patchDashboard(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDashboard(w, r)
	if !ok {
		return
	}
	var in dashboardInput
	if !decode(w, r, &in) {
		return
	}
	if msg := s.applyDashboard(r.Context(), d, &in); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	if err := s.Store.UpdateDashboard(r.Context(), d); err != nil {
		s.layoutErr(w, err)
		return
	}
	writeJSON(w, 200, d)
}

func (s *Server) deleteDashboard(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDashboard(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteDashboard(r.Context(), d.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	w.WriteHeader(204)
}
