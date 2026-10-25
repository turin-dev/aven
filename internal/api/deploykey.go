package api

import (
	"net/http"

	"github.com/turin-dev/aven/internal/node"
)

// getDeployKey는 서비스의 SSH deploy key 공개키를 돌려준다. 개인키는 API로 나가지 않는다.
func (s *Server) getDeployKey(w http.ResponseWriter, r *http.Request) {
	svc, err := s.Store.GetService(r.Context(), r.PathValue("sid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	k, err := s.Store.GetDeployKey(r.Context(), svc.ID)
	if err != nil {
		writeErr(w, 404, "no deploy key")
		return
	}
	writeJSON(w, 200, k)
}

// createDeployKey는 ed25519 deploy key를 만든다. 이미 있으면 409, ?rotate=1이면 새로 만든다(옛 키는 폐기).
func (s *Server) createDeployKey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	svc, err := s.Store.GetService(ctx, r.PathValue("sid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if svc.SourceType != "repo" {
		writeErr(w, 400, "deploy keys are only for repository services")
		return
	}
	if _, err := s.Store.GetDeployKey(ctx, svc.ID); err == nil && r.URL.Query().Get("rotate") != "1" {
		writeErr(w, 409, "deploy key already exists (use ?rotate=1 to replace it)")
		return
	}
	dk, err := node.GenerateDeployKey("keel-deploy-" + svc.Name)
	if err != nil {
		writeErr(w, 500, "generate key: "+err.Error())
		return
	}
	if err := s.Store.PutDeployKey(ctx, svc.ID, dk.PrivatePEM, dk.PublicKey, dk.Fingerprint); err != nil {
		s.storeErr(w, err)
		return
	}
	k, err := s.Store.GetDeployKey(ctx, svc.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.Events.Emit(ctx, svc.ProjectID, nil, &svc.ID, "deploykey.created", svc.Name+": SSH deploy key 생성 ("+k.Fingerprint+")")
	writeJSON(w, 201, k)
}

func (s *Server) deleteDeployKey(w http.ResponseWriter, r *http.Request) {
	svc, err := s.Store.GetService(r.Context(), r.PathValue("sid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if _, err := s.Store.GetDeployKey(r.Context(), svc.ID); err != nil {
		writeErr(w, 404, "no deploy key")
		return
	}
	if err := s.Store.DeleteDeployKey(r.Context(), svc.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	w.WriteHeader(204)
}
