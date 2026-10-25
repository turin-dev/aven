package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/turin-dev/aven/internal/cronx"
	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/s3"
	"github.com/turin-dev/aven/internal/store"
)

var destNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,62}$`)

type destView struct {
	*store.BackupDestination
	HasSecret bool `json:"has_secret"`
}

func viewDest(d *store.BackupDestination) destView { return destView{d, d.SecretKey != ""} }

func (s *Server) listDestinations(w http.ResponseWriter, r *http.Request) {
	ds, err := s.Store.ListBackupDestinations(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	out := []destView{}
	for _, d := range ds {
		out = append(out, viewDest(d))
	}
	writeJSON(w, 200, out)
}

type destBody struct {
	Name      *string `json:"name"`
	Type      *string `json:"type"`
	Endpoint  *string `json:"endpoint"`
	Region    *string `json:"region"`
	Bucket    *string `json:"bucket"`
	Prefix    *string `json:"prefix"`
	AccessKey *string `json:"access_key"`
	SecretKey *string `json:"secret_key"`
}

func (b *destBody) apply(d *store.BackupDestination) {
	if b.Name != nil {
		d.Name = strings.TrimSpace(*b.Name)
	}
	if b.Type != nil {
		d.Type = *b.Type
	}
	if b.Endpoint != nil {
		d.Endpoint = strings.TrimRight(strings.TrimSpace(*b.Endpoint), "/")
	}
	if b.Region != nil {
		d.Region = strings.TrimSpace(*b.Region)
	}
	if b.Bucket != nil {
		d.Bucket = strings.TrimSpace(*b.Bucket)
	}
	if b.Prefix != nil {
		d.Prefix = strings.Trim(strings.TrimSpace(*b.Prefix), "/")
	}
	if b.AccessKey != nil {
		d.AccessKey = strings.TrimSpace(*b.AccessKey)
	}
	if b.SecretKey != nil && *b.SecretKey != "" { // 비어 있으면 기존 비밀을 유지한다
		d.SecretKey = *b.SecretKey
	}
}

func validDest(d *store.BackupDestination) error {
	if !destNameRe.MatchString(d.Name) {
		return errors.New("name must be 1-63 characters (letters, digits, space, . _ -)")
	}
	switch d.Type {
	case store.DestLocal:
		d.Endpoint, d.Region, d.Bucket, d.Prefix, d.AccessKey, d.SecretKey = "", "", "", "", "", ""
	case store.DestS3:
		u, err := url.Parse(d.Endpoint)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Path != "" && u.Path != "/" {
			return errors.New("endpoint must be http(s)://host[:port] without a path")
		}
		if d.Region == "" {
			d.Region = "us-east-1"
		}
		if d.Bucket == "" || d.AccessKey == "" || d.SecretKey == "" {
			return errors.New("bucket, access_key and secret_key are required")
		}
	default:
		return errors.New("type must be local or s3")
	}
	return nil
}

func (s *Server) createDestination(w http.ResponseWriter, r *http.Request) {
	var body destBody
	if !decode(w, r, &body) {
		return
	}
	d := &store.BackupDestination{}
	body.apply(d)
	if err := validDest(d); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.Store.CreateBackupDestination(r.Context(), d); err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 201, viewDest(d))
}

func (s *Server) patchDestination(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d, err := s.Store.GetBackupDestination(ctx, r.PathValue("id"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var body destBody
	if !decode(w, r, &body) {
		return
	}
	body.apply(d)
	if err := validDest(d); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.Store.UpdateBackupDestination(ctx, d); err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, viewDest(d))
}

func (s *Server) deleteDestination(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if _, err := s.Store.GetBackupDestination(ctx, id); err != nil {
		s.storeErr(w, err)
		return
	}
	if n, _ := s.Store.CountDBBackupsOnDestination(ctx, id); n > 0 {
		writeErr(w, 409, fmt.Sprintf("%d backups are stored on this destination; delete them first", n))
		return
	}
	if n, _ := s.Store.CountDBPITRObjectsOnDestination(ctx, id); n > 0 {
		writeErr(w, 409, fmt.Sprintf("%d point-in-time recovery files are stored on this destination; delete the archive first", n))
		return
	}
	if err := s.Store.DeleteBackupDestination(ctx, id); err != nil {
		s.storeErr(w, err)
		return
	}
	w.WriteHeader(204)
}

// testDestination은 목적지에 접근할 수 있는지(버킷 목록)를 확인한다.
func (s *Server) testDestination(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d, err := s.Store.GetBackupDestination(ctx, r.PathValue("id"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if d.Type == store.DestS3 {
		if _, err := deploy.S3Client(d).List(ctx, strings.Trim(d.Prefix, "/")+"/"); err != nil {
			var e *s3.Error
			msg := err.Error()
			if errors.As(err, &e) {
				msg = e.Error()
			}
			writeJSON(w, 200, map[string]any{"ok": false, "error": msg})
			return
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---- 인스턴스별 ----

func (s *Server) getDBBackupConfig(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	cfg, err := s.Store.GetDBBackupConfig(r.Context(), c.in.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, cfg)
}

func (s *Server) putDBBackupConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	if !s.isDB(c.svc) {
		writeErr(w, 400, "not a database service")
		return
	}
	cfg, err := s.Store.GetDBBackupConfig(ctx, c.in.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var body struct {
		Enabled       *bool   `json:"enabled"`
		Cron          *string `json:"cron"`
		Retention     *int    `json:"retention"`
		DestinationID *string `json:"destination_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Enabled != nil {
		cfg.Enabled = *body.Enabled
	}
	if body.Cron != nil {
		cfg.Cron = strings.TrimSpace(*body.Cron)
	}
	if body.Retention != nil {
		cfg.Retention = *body.Retention
	}
	if body.DestinationID != nil {
		cfg.DestinationID = *body.DestinationID
	}
	if _, err := cronx.Parse(cfg.Cron); err != nil {
		writeErr(w, 400, "invalid cron: "+err.Error())
		return
	}
	if cfg.Retention < 1 || cfg.Retention > 365 {
		writeErr(w, 400, "retention must be 1..365")
		return
	}
	if cfg.DestinationID != "" {
		if _, err := s.Store.GetBackupDestination(ctx, cfg.DestinationID); err != nil {
			writeErr(w, 400, "unknown destination_id")
			return
		}
	}
	if err := s.Store.SetDBBackupConfig(ctx, c.in.ID, cfg); err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, cfg)
}

func (s *Server) isDB(svc *store.Service) bool {
	return svc.SourceType == "database" && svc.DatabaseKind != nil && deploy.IsDBKind(*svc.DatabaseKind)
}

func (s *Server) listDBBackups(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	bs, err := s.Store.ListDBBackups(r.Context(), c.in.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, bs)
}

func (s *Server) createDBBackup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	var body struct {
		DestinationID *string `json:"destination_id"`
	}
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	dest := ""
	if body.DestinationID != nil {
		dest = *body.DestinationID
	} else if cfg, err := s.Store.GetDBBackupConfig(ctx, c.in.ID); err == nil {
		dest = cfg.DestinationID
	}
	if dest != "" {
		if _, err := s.Store.GetBackupDestination(ctx, dest); err != nil {
			writeErr(w, 400, "unknown destination_id")
			return
		}
	}
	b, err := s.Deploy.CreateDBBackup(ctx, c.in, "manual", dest)
	if err != nil {
		code := 400
		if errors.Is(err, deploy.ErrDBBusy) {
			code = 409
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, 202, b)
}

func (s *Server) loadDBBackup(w http.ResponseWriter, r *http.Request) (*store.DBBackup, bool) {
	b, err := s.Store.GetDBBackup(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeErr(w, err)
		return nil, false
	}
	return b, true
}

func (s *Server) getDBBackup(w http.ResponseWriter, r *http.Request) {
	if b, ok := s.loadDBBackup(w, r); ok {
		writeJSON(w, 200, b)
	}
}

func (s *Server) deleteDBBackup(w http.ResponseWriter, r *http.Request) {
	b, ok := s.loadDBBackup(w, r)
	if !ok {
		return
	}
	if b.Status == "running" {
		writeErr(w, 409, "backup is still running")
		return
	}
	if err := s.Deploy.DeleteDBBackup(r.Context(), b); err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	w.WriteHeader(204)
}

func (s *Server) downloadDBBackup(w http.ResponseWriter, r *http.Request) {
	b, ok := s.loadDBBackup(w, r)
	if !ok {
		return
	}
	if b.Status != "done" {
		writeErr(w, 409, "backup is not complete")
		return
	}
	rc, err := s.Deploy.OpenDBBackup(r.Context(), b)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+b.FileName+`"`)
	io.Copy(w, rc)
}

// restoreDBBackup은 백업을 같은 또는 다른 DB 서비스에 복원한다. 대상 데이터는 덮어써진다.
// body: {service_id?, environment_id?} (비우면 백업을 만든 인스턴스). 비동기(202)이고 GET .../restore로 상태를 본다.
func (s *Server) restoreDBBackup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	b, ok := s.loadDBBackup(w, r)
	if !ok {
		return
	}
	var body struct {
		ServiceID     string `json:"service_id"`
		EnvironmentID string `json:"environment_id"`
	}
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	if body.ServiceID == "" {
		body.ServiceID = b.ServiceID
	}
	if body.EnvironmentID == "" {
		body.EnvironmentID = b.EnvironmentID // 다른 서비스를 고르면 같은 환경에서 찾는다
	}
	target, err := s.Store.GetInstanceFor(ctx, body.ServiceID, body.EnvironmentID)
	if err != nil {
		writeErr(w, 404, "target database instance not found")
		return
	}
	if err := s.Deploy.RestoreDBBackup(ctx, b, target); err != nil {
		code := 400
		if errors.Is(err, deploy.ErrDBBusy) {
			code = 409
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, 202, deploy.LastRestore(b.ID))
}

func (s *Server) getDBRestore(w http.ResponseWriter, r *http.Request) {
	b, ok := s.loadDBBackup(w, r)
	if !ok {
		return
	}
	rs := deploy.LastRestore(b.ID)
	if rs == nil {
		writeErr(w, 404, "no restore has run for this backup since the server started")
		return
	}
	writeJSON(w, 200, rs)
}
