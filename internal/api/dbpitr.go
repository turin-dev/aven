package api

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/catalog"
	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/store"
)

// 시점 복구(PITR): GET/PUT .../pitr, POST .../pitr/base, POST|GET .../pitr/restore, DELETE .../pitr/archive.

func (s *Server) loadPITR(w http.ResponseWriter, r *http.Request) (*instCtx, string, bool) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return nil, "", false
	}
	if !s.isDB(c.svc) {
		writeErr(w, 400, "not a database service")
		return nil, "", false
	}
	return c, *c.svc.DatabaseKind, true
}

func (s *Server) getPITR(w http.ResponseWriter, r *http.Request) {
	c, kind, ok := s.loadPITR(w, r)
	if !ok {
		return
	}
	st, err := s.Deploy.PITRStatusFor(r.Context(), c.in, kind)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, st)
}

func (s *Server) putPITR(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, kind, ok := s.loadPITR(w, r)
	if !ok {
		return
	}
	cfg, err := s.Store.GetDBPITRConfig(ctx, c.in.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var body struct {
		Enabled       *bool   `json:"enabled"`
		RetentionDays *int    `json:"retention_days"`
		DestinationID *string `json:"destination_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	enabled, days, dest := cfg.Enabled, cfg.RetentionDays, cfg.DestinationID
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	if body.RetentionDays != nil {
		days = *body.RetentionDays
	}
	if body.DestinationID != nil {
		dest = *body.DestinationID
	}
	if enabled {
		if !deploy.PITRSupported(kind) {
			writeErr(w, 400, deploy.ErrPITRNotSupported.Error())
			return
		}
	}
	if dest != "" {
		if _, err := s.Store.GetBackupDestination(ctx, dest); err != nil {
			writeErr(w, 400, "unknown destination_id")
			return
		}
	}
	out, err := s.Deploy.ConfigurePITR(ctx, c.in, enabled, days, dest)
	if err != nil {
		if out != nil { // 설정은 저장했지만 재시작이 실패했다
			writeJSON(w, 200, map[string]any{"config": out, "warning": err.Error()})
			return
		}
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"config": out})
}

func (s *Server) createPITRBase(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.loadPITR(w, r)
	if !ok {
		return
	}
	if err := s.Deploy.StartPITRBase(r.Context(), c.in); err != nil {
		code := 400
		if errors.Is(err, deploy.ErrPITRBusy) {
			code = 409
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, 202, map[string]string{"status": "running"})
}

func (s *Server) deletePITRArchive(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.loadPITR(w, r)
	if !ok {
		return
	}
	n, err := s.Deploy.DeletePITRArchive(r.Context(), c.in)
	if err != nil {
		code := 400
		if errors.Is(err, deploy.ErrPITRBusy) {
			code = 409
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, 200, map[string]int{"deleted": n})
}

// parsePITRTime은 RFC3339 또는 "YYYY-MM-DD HH:MM[:SS]"(UTC)를 읽는다. 초 단위로 자른다.
func parsePITRTime(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, v, time.UTC); err == nil {
			return t.UTC().Truncate(time.Second), nil
		}
	}
	return time.Time{}, errors.New("target_time must be RFC3339 (e.g. 2026-07-20T12:00:00Z)")
}

func restoredName(src string, at time.Time) string {
	suffix := "-restored-" + at.UTC().Format("20060102-1504")
	if room := 40 - len(suffix); len(src) > room {
		src = strings.TrimRight(src[:room], "-")
	}
	return src + suffix
}

func (s *Server) restorePITR(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, kind, ok := s.loadPITR(w, r)
	if !ok {
		return
	}
	var body struct {
		TargetTime     string `json:"target_time"`
		InPlace        bool   `json:"in_place"`
		NewServiceName string `json:"new_service_name"`
	}
	if !decode(w, r, &body) {
		return
	}
	at, err := parsePITRTime(body.TargetTime)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if at.After(time.Now()) {
		writeErr(w, 400, "target_time is in the future")
		return
	}
	// 최근 시각이면 원본의 밀린 로그를 먼저 보관해 본다.
	if err := s.Deploy.CheckPITRTarget(ctx, c.in, kind, at); err != nil && c.in.Status == store.InstOnline {
		if cfg, _ := s.Store.GetDBPITRConfig(ctx, c.in.ID); cfg != nil && cfg.Enabled {
			s.Deploy.SyncPITRNow(ctx, c.in)
		}
	}
	if err := s.Deploy.CheckPITRTarget(ctx, c.in, kind, at); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if deploy.PITRRestoreRunning(c.in.ID) {
		writeErr(w, 409, deploy.ErrPITRBusy.Error())
		return
	}
	target := c.in
	if !body.InPlace {
		name := strings.TrimSpace(body.NewServiceName)
		if name == "" {
			name = restoredName(c.svc.Name, at)
		}
		if !nameRe.MatchString(name) || name == "shared" {
			writeErr(w, 400, "new_service_name must be lowercase letters, digits and '-' (max 40)")
			return
		}
		if svcs, _ := s.Store.ListServices(ctx, c.p.ID); svcs != nil {
			for _, sv := range svcs {
				if sv.Name == name {
					writeErr(w, 409, "service already exists: "+name)
					return
				}
			}
		}
		target, err = s.createPITRFork(ctx, c, kind, name)
		if err != nil {
			s.storeErr(w, err)
			return
		}
	}
	st, err := s.Deploy.RestorePITR(ctx, c.in, target, kind, at, deploy.PITRRestoreOpts{InPlace: body.InPlace})
	if err != nil {
		code := 400
		if errors.Is(err, deploy.ErrPITRBusy) {
			code = 409
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, 202, st)
}

// createPITRFork는 복원 대상이 될 새 DB 서비스를 만들고(원본과 같은 이미지·변수) 이 환경에 배포를 시작한다.
func (s *Server) createPITRFork(ctx context.Context, c *instCtx, kind, name string) (*store.Instance, error) {
	def, ok := catalog.DatabaseService(kind)
	if !ok {
		return nil, fmt.Errorf("unknown database kind %s", kind)
	}
	def.Name = name
	svc, err := s.createServiceFromDef(ctx, c.p, def, nil, "main", nil)
	if err != nil {
		return nil, err
	}
	in, err := s.Store.GetInstanceFor(ctx, svc.ID, c.env.ID)
	if err != nil {
		return nil, err
	}
	// 복원한 데이터에는 원본의 사용자·비밀번호가 들어 있다: 원본 변수와 이미지(같은 버전)를 그대로 쓴다.
	in.Variables = maps.Clone(c.in.Variables)
	if in.Variables == nil {
		in.Variables = map[string]string{}
	}
	img := ""
	if c.in.CurrentDeploymentID != nil {
		if d, err := s.Store.GetDeployment(ctx, *c.in.CurrentDeploymentID); err == nil && d.Image != nil {
			img = *d.Image
		}
	}
	if img == "" && c.in.Image != nil {
		img = *c.in.Image
	}
	if img != "" {
		in.Image = &img
	}
	if err := s.Store.UpdateInstanceConfig(ctx, in); err != nil {
		return nil, err
	}
	if _, err := s.Deploy.Enqueue(ctx, in, deploy.EnqueueOpts{Trigger: "manual"}); err != nil {
		return nil, err
	}
	s.Events.Emit(ctx, c.p.ID, &c.env.ID, &svc.ID, "pitr.fork", fmt.Sprintf("%s: %s의 시점 복원 대상으로 만들었습니다", svc.Name, c.svc.Name))
	return in, nil
}

func (s *Server) getPITRRestore(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.loadPITR(w, r)
	if !ok {
		return
	}
	rs := deploy.LastPITRRestore(c.in.ID)
	if rs == nil {
		writeErr(w, 404, "no point-in-time restore has run for this database since the server started")
		return
	}
	writeJSON(w, 200, rs)
}
