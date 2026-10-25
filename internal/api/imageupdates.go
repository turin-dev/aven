package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/store"
)

// loadImageUpdateInstance는 image 서비스 인스턴스만 통과시킨다.
func (s *Server) loadImageUpdateInstance(w http.ResponseWriter, r *http.Request) (*instCtx, bool) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return nil, false
	}
	if c.svc.SourceType != "image" {
		writeErr(w, 400, "image updates are available for image services only")
		return nil, false
	}
	return c, true
}

func (s *Server) writeImageUpdates(w http.ResponseWriter, r *http.Request, instanceID string) {
	updates, err := s.Store.ListImageUpdates(r.Context(), instanceID, 50)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, updates)
}

func (s *Server) listImageUpdates(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadImageUpdateInstance(w, r)
	if !ok {
		return
	}
	s.writeImageUpdates(w, r, c.in.ID)
}

// checkImageUpdates는 캐시를 무시하고 레지스트리를 바로 확인한다("업데이트 확인").
func (s *Server) checkImageUpdates(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadImageUpdateInstance(w, r)
	if !ok {
		return
	}
	if c.in.Image == nil || *c.in.Image == "" {
		writeErr(w, 400, "set an image before checking for updates")
		return
	}
	if _, err := s.Deploy.CheckImageUpdate(r.Context(), c.in, time.Now().UTC()); err != nil {
		writeErr(w, http.StatusBadGateway, "registry check failed: "+err.Error())
		return
	}
	s.writeImageUpdates(w, r, c.in.ID)
}

func (s *Server) loadImageUpdate(w http.ResponseWriter, r *http.Request, c *instCtx) (*store.ImageUpdate, bool) {
	update, err := s.Store.GetImageUpdate(r.Context(), r.PathValue("uid"))
	if err != nil {
		s.storeErr(w, err)
		return nil, false
	}
	if update.InstanceID != c.in.ID {
		writeErr(w, http.StatusNotFound, "image update not found")
		return nil, false
	}
	return update, true
}

func (s *Server) applyImageUpdate(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadImageUpdateInstance(w, r)
	if !ok {
		return
	}
	update, ok := s.loadImageUpdate(w, r, c)
	if !ok {
		return
	}
	dep, err := s.Deploy.ApplyImageUpdate(r.Context(), c.in, update, false)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.storeErr(w, err)
			return
		}
		if errors.Is(err, deploy.ErrImageUpdateChanged) || errors.Is(err, deploy.ErrImageUpdateBusy) {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		writeErr(w, 400, err.Error())
		return
	}
	s.emitDeployRequested(r, c.p.ID, c.env, c.svc, dep)
	writeJSON(w, http.StatusAccepted, dep)
}

// skipImageUpdate는 이 버전을 자동 업데이트 대상에서 뺀다.
func (s *Server) skipImageUpdate(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadImageUpdateInstance(w, r)
	if !ok {
		return
	}
	update, ok := s.loadImageUpdate(w, r, c)
	if !ok {
		return
	}
	if err := s.Store.SkipImageUpdate(r.Context(), update.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusConflict, "only available or failed updates can be skipped")
			return
		}
		s.storeErr(w, err)
		return
	}
	s.writeImageUpdates(w, r, c.in.ID)
}
