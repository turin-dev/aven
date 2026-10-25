package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/turin-dev/aven/internal/cronx"
	"github.com/turin-dev/aven/internal/store"
)

func validMountPath(p string) bool {
	return strings.HasPrefix(p, "/") && path.Clean(p) == p && p != "/" && !strings.ContainsAny(p, ",:\n")
}

const multiNodeVolumeErr = "this service is placed on several nodes; volumes need a single node (reduce placement to one node first)"

func (s *Server) createVolume(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	env, p, ok := s.envAndProject(w, r)
	if !ok {
		return
	}
	var in struct {
		ServiceID   string  `json:"service_id"`
		MountPath   string  `json:"mount_path"`
		Name        *string `json:"name"`
		SizeLimitMB *int    `json:"size_limit_mb"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !validSizeLimit(in.SizeLimitMB) {
		writeErr(w, 400, sizeLimitErr)
		return
	}
	svc, err := s.Store.GetService(ctx, in.ServiceID)
	if err != nil || svc.ProjectID != p.ID {
		writeErr(w, 400, "service_id must be a service of this project")
		return
	}
	inst, err := s.Store.GetInstanceFor(ctx, svc.ID, env.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if !validMountPath(in.MountPath) {
		writeErr(w, 400, "mount_path must be an absolute, clean path (not /)")
		return
	}
	if inst.MultiNode() {
		writeErr(w, 409, multiNodeVolumeErr)
		return
	}
	existing, _ := s.Store.ListVolumesByInstance(ctx, inst.ID)
	for _, v := range existing {
		if v.MountPath == in.MountPath {
			writeErr(w, 409, "a volume is already mounted at "+in.MountPath)
			return
		}
	}
	name := volumeName(svc.Name, in.MountPath)
	if in.Name != nil && strings.TrimSpace(*in.Name) != "" {
		name = strings.TrimSpace(*in.Name)
		if !nameRe.MatchString(name) {
			writeErr(w, 400, "name must be lowercase letters, digits and '-' (max 40)")
			return
		}
	}
	v := &store.Volume{ProjectID: p.ID, EnvironmentID: env.ID, InstanceID: &inst.ID, Name: name, MountPath: in.MountPath, SizeLimitMB: in.SizeLimitMB}
	if err := s.Store.CreateVolume(ctx, v); err != nil {
		s.storeErr(w, err)
		return
	}
	s.Events.Emit(ctx, p.ID, &env.ID, &svc.ID, "volume.created", fmt.Sprintf("볼륨 %s를 %s 경로로 %s에 연결했습니다 (다음 배포부터 적용)", v.Name, v.MountPath, svc.Name))
	writeJSON(w, 201, v)
}

func (s *Server) loadVolume(w http.ResponseWriter, r *http.Request) (*store.Volume, bool) {
	v, err := s.Store.GetVolume(r.Context(), r.PathValue("vid"))
	if err != nil {
		s.storeErr(w, err)
		return nil, false
	}
	return v, true
}

func (s *Server) updateVolume(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	v, ok := s.loadVolume(w, r)
	if !ok {
		return
	}
	var raw map[string]json.RawMessage
	if !decode(w, r, &raw) {
		return
	}
	if b, ok := raw["name"]; ok {
		var n string
		if json.Unmarshal(b, &n) != nil || !nameRe.MatchString(n) {
			writeErr(w, 400, "name must be lowercase letters, digits and '-' (max 40)")
			return
		}
		v.Name = n
	}
	if b, ok := raw["mount_path"]; ok {
		var mp string
		if json.Unmarshal(b, &mp) != nil || !validMountPath(mp) {
			writeErr(w, 400, "mount_path must be an absolute, clean path (not /)")
			return
		}
		v.MountPath = mp
	}
	if b, ok := raw["service_instance_id"]; ok {
		var id *string
		if json.Unmarshal(b, &id) != nil {
			writeErr(w, 400, "service_instance_id must be a string or null")
			return
		}
		if id != nil {
			in, err := s.Store.GetInstance(ctx, *id)
			if err != nil || in.EnvironmentID != v.EnvironmentID {
				writeErr(w, 400, "service_instance_id must be an instance in the volume's environment")
				return
			}
			if in.MultiNode() {
				writeErr(w, 409, multiNodeVolumeErr)
				return
			}
		}
		v.InstanceID = id
	}
	limitChanged := false
	if b, ok := raw["size_limit_mb"]; ok {
		var lim *int
		if json.Unmarshal(b, &lim) != nil || !validSizeLimit(lim) {
			writeErr(w, 400, sizeLimitErr)
			return
		}
		limitChanged = !equalIntPtr(lim, v.SizeLimitMB)
		v.SizeLimitMB = lim
	}
	if err := s.Store.UpdateVolume(ctx, v); err != nil {
		s.storeErr(w, err)
		return
	}
	if limitChanged {
		// 상한이 바뀌면 다음 크기 측정 때 새 상한으로 다시 판정해 알린다.
		s.Store.SetVolumeLimitState(ctx, v.ID, store.VolumeLimitOK)
		v.LimitState = store.VolumeLimitOK
	}
	writeJSON(w, 200, s.fillVolumes([]*store.Volume{v})[0])
}

// MaxVolumeLimitMB는 size_limit_mb 상한(16 TiB)이다.
const MaxVolumeLimitMB = 16 * 1024 * 1024

const sizeLimitErr = "size_limit_mb must be 1..16777216 or null"

func validSizeLimit(n *int) bool { return n == nil || (*n >= 1 && *n <= MaxVolumeLimitMB) }

func equalIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func (s *Server) deleteVolume(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	v, ok := s.loadVolume(w, r)
	if !ok {
		return
	}
	if v.InstanceID != nil {
		writeErr(w, 409, "volume is attached to a service; detach it first (PATCH service_instance_id: null)")
		return
	}
	if err := s.Deploy.Docker.RemoveVolume(context.WithoutCancel(ctx), v.DockerName); err != nil {
		writeErr(w, 409, "remove docker volume: "+err.Error())
		return
	}
	bs, _ := s.Store.ListBackups(ctx, v.ID)
	for _, b := range bs {
		s.Deploy.DeleteBackup(ctx, b)
	}
	if err := s.Store.DeleteVolume(ctx, v.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) listBackups(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadVolume(w, r)
	if !ok {
		return
	}
	bs, err := s.Store.ListBackups(r.Context(), v.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, bs)
}

func (s *Server) createBackup(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadVolume(w, r)
	if !ok {
		return
	}
	b, err := s.Deploy.CreateBackup(context.WithoutCancel(r.Context()), v)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 202, b)
}

func (s *Server) backupSchedule(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadVolume(w, r)
	if !ok {
		return
	}
	var in struct {
		Cron *string `json:"cron"`
		Keep int     `json:"keep"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Cron != nil && strings.TrimSpace(*in.Cron) != "" {
		c := strings.TrimSpace(*in.Cron)
		if _, err := cronx.Parse(c); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		v.BackupCron = &c
	} else {
		v.BackupCron = nil
	}
	if in.Keep < 0 || in.Keep > 1000 {
		writeErr(w, 400, "keep must be 1..1000")
		return
	}
	if in.Keep > 0 {
		v.BackupKeep = in.Keep
	}
	if err := s.Store.UpdateVolume(r.Context(), v); err != nil {
		s.storeErr(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) restoreBackup(w http.ResponseWriter, r *http.Request) {
	b, err := s.Store.GetBackup(r.Context(), r.PathValue("bid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.Deploy.RestoreBackup(context.WithoutCancel(r.Context()), b); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	w.WriteHeader(202)
}

func (s *Server) deleteBackup(w http.ResponseWriter, r *http.Request) {
	b, err := s.Store.GetBackup(r.Context(), r.PathValue("bid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.Deploy.DeleteBackup(r.Context(), b); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	w.WriteHeader(204)
}

func (s *Server) getBackupSchedule(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadVolume(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, map[string]any{"cron": v.BackupCron, "keep": v.BackupKeep})
}

// listEnvVolumes는 환경의 모든 볼륨(분리된 것 포함)이다.
func (s *Server) listEnvVolumes(w http.ResponseWriter, r *http.Request) {
	env, _, ok := s.envAndProject(w, r)
	if !ok {
		return
	}
	vols, err := s.Store.ListVolumesByEnv(r.Context(), env.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, s.fillVolumes(vols))
}
