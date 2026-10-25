package api

import (
	"net/http"

	"github.com/turin-dev/aven/internal/store"
)

type workspaceResources struct {
	Workspace *store.Workspace     `json:"workspace"`
	Limits    store.ResourceLimits `json:"limits"`
	Allocated store.ResourceUsage  `json:"allocated"`
}

// Only instance operators see this inventory: names and allocated capacity,
// never project configuration, secrets or implicit workspace membership.
func (s *Server) listResourceLimits(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := []workspaceResources{}
	err := s.Store.Tx(ctx, func(tx *store.Store) error {
		workspaces, err := tx.ListWorkspaces(ctx)
		if err != nil {
			return err
		}
		for _, ws := range workspaces {
			l, err := tx.WorkspaceResourceLimits(ctx, ws.ID)
			if err != nil {
				return err
			}
			u, err := tx.WorkspaceResourceUsage(ctx, ws.ID)
			if err != nil {
				return err
			}
			out = append(out, workspaceResources{ws, l, u})
		}
		return nil
	})
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) getResourceLimits(w http.ResponseWriter, r *http.Request) {
	var out workspaceResources
	err := s.Store.Tx(r.Context(), func(tx *store.Store) (err error) {
		out.Workspace, err = tx.GetWorkspace(r.Context(), r.PathValue("wsid"))
		if err != nil {
			return err
		}
		out.Limits, err = tx.WorkspaceResourceLimits(r.Context(), out.Workspace.ID)
		if err != nil {
			return err
		}
		out.Allocated, err = tx.WorkspaceResourceUsage(r.Context(), out.Workspace.ID)
		return err
	})
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) putResourceLimits(w http.ResponseWriter, r *http.Request) {
	var l store.ResourceLimits
	if !decode(w, r, &l) {
		return
	}
	if err := l.Validate(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	wsID := r.PathValue("wsid")
	if err := s.Store.SetWorkspaceResourceLimits(r.Context(), wsID, l); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "workspace.resource_limits.updated", wsID, "작업 공간 자원 한도를 변경했습니다")
	s.getResourceLimits(w, r)
}
