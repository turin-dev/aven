package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/turin-dev/aven/internal/imageupdate"
	"github.com/turin-dev/aven/internal/store"
)

const (
	ImageUpdateInterval = 5 * time.Minute
	ImageUpdateCache    = 30 * time.Minute
)

var (
	ErrImageUpdateChanged  = errors.New("image source changed since this update was detected")
	ErrImageUpdateBusy     = errors.New("image update is already being applied")
	ErrImageUpdateNotImage = errors.New("image auto updates are available for image services only")
)

// registryClient는 테스트에서 가짜 레지스트리로 바꿀 수 있다.
var newRegistryClient = func(auth *imageupdate.Auth) *imageupdate.Client { return imageupdate.NewClient(auth) }

// ImageUpdateLoop checks registries and applies discovered updates during their
// configured UTC windows. The registry cache limits checks to once per 30 minutes.
func (m *Manager) ImageUpdateLoop(ctx context.Context) {
	ticker := time.NewTicker(ImageUpdateInterval)
	defer ticker.Stop()
	for {
		m.imageUpdateTick(ctx, time.Now().UTC())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) imageUpdateTick(ctx context.Context, now time.Time) {
	instances, err := m.Store.ListInstances(ctx)
	if err != nil {
		m.warn("image update list instances", "err", err)
		return
	}
	for _, in := range instances {
		if ctx.Err() != nil {
			return
		}
		if in.CurrentDeploymentID == nil || in.Image == nil || *in.Image == "" || !imageupdate.Enabled(in.ImageUpdatePolicy) {
			continue
		}
		state, err := m.Store.GetImageUpdateState(ctx, in.ID)
		if err != nil || (state.LastCheckedAt != nil && now.Sub(*state.LastCheckedAt) < ImageUpdateCache) {
			continue
		}
		if _, err := m.CheckImageUpdate(ctx, in, now); err != nil && !errors.Is(err, ErrImageUpdateNotImage) {
			m.warn("image update registry check", "instance", in.ID, "image", *in.Image, "err", err)
		}
	}

	available, err := m.Store.ListAvailableImageUpdates(ctx)
	if err != nil {
		m.warn("image update list available", "err", err)
		return
	}
	for _, update := range available {
		in, err := m.Store.GetInstance(ctx, update.InstanceID)
		if err != nil {
			continue
		}
		// 감지한 뒤 사용자가 이미지를 바꿨으면 그 항목은 더 이상 의미가 없다.
		if in.Image == nil || *in.Image != update.CurrentImage {
			_ = m.Store.SupersedeImageUpdate(ctx, update.ID)
			continue
		}
		if !imageupdate.Enabled(in.ImageUpdatePolicy) || in.Status != store.InstOnline ||
			!imageupdate.InWindow(in.ImageUpdateWindow, in.ImageUpdateDays, in.ImageUpdateStartHour, in.ImageUpdateEndHour, now) {
			continue
		}
		if _, err := m.ApplyImageUpdate(ctx, in, update, true); err != nil {
			m.warn("automatic image update", "instance", in.ID, "err", err)
		}
	}
}

// CheckImageUpdate asks the registry for a newer version of the instance's image and
// records it as an available update. It returns nil when the image is up to date.
func (m *Manager) CheckImageUpdate(ctx context.Context, in *store.Instance, now time.Time) (*store.ImageUpdate, error) {
	t, err := m.Load(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if t.Service.SourceType != "image" || in.Image == nil || *in.Image == "" {
		return nil, ErrImageUpdateNotImage
	}
	image := *in.Image
	state, err := m.Store.GetImageUpdateState(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if state.LastDigest == "" {
		state.LastDigest = m.runningDigest(ctx, in)
	}
	var creds *imageupdate.Auth
	if a := m.registryAuth(ctx, t.Project.ID, image, nil); a != nil {
		creds = &imageupdate.Auth{Username: a.Username, Password: a.Password}
	}
	discovery, err := newRegistryClient(creds).Discover(ctx, image, in.ImageUpdatePolicy, state.LastDigest)
	checked := now
	state.LastCheckedAt = &checked
	if err != nil {
		_ = m.Store.SetImageUpdateState(ctx, state)
		return nil, err
	}
	// mutable 태그의 첫 확인이면 기준 digest를 남긴다. 이후에는 배포가 끝날 때 실제 digest로 바뀐다.
	if state.LastDigest == "" {
		state.LastDigest = discovery.CurrentDigest
	}
	if err := m.Store.SetImageUpdateState(ctx, state); err != nil {
		return nil, err
	}
	if discovery.Candidate == nil {
		return nil, nil
	}
	return m.Store.SaveAvailableImageUpdate(ctx, &store.ImageUpdate{InstanceID: in.ID, CurrentImage: image,
		TargetImage: discovery.Candidate.Image, Digest: discovery.Candidate.Digest, DigestImage: discovery.Candidate.DigestImage})
}

// runningDigest는 현재 배포가 실제로 쓰는 이미지의 manifest digest다(모르면 "").
func (m *Manager) runningDigest(ctx context.Context, in *store.Instance) string {
	if in.CurrentDeploymentID == nil {
		return ""
	}
	d, err := m.Store.GetDeployment(ctx, *in.CurrentDeploymentID)
	if err != nil || d.Image == nil || d.BuilderUsed == nil || *d.BuilderUsed != "image" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	digest, err := m.driverFor(d).ImageDigest(ctx, *d.Image)
	if err != nil {
		return ""
	}
	return digest
}

// recordDeployedDigest는 일반 배포(수동 redeploy 등)가 live가 됐을 때 실제 digest를 기준으로 남기고,
// 그 digest를 가리키던 업데이트 항목은 이미 반영된 것으로 정리한다.
func (m *Manager) recordDeployedDigest(ctx context.Context, d *store.Deployment, now time.Time) {
	if d.BuilderUsed == nil || *d.BuilderUsed != "image" || d.Image == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	digest, err := m.driverFor(d).ImageDigest(ctx, *d.Image)
	if err != nil || digest == "" {
		return
	}
	_ = m.Store.SetImageUpdateState(ctx, &store.ImageUpdateState{InstanceID: d.InstanceID, LastDigest: digest, LastCheckedAt: &now})
	_ = m.Store.SupersedeImageUpdatesWithDigest(ctx, d.InstanceID, digest)
}

// ApplyImageUpdate queues the selected update. Manual requests bypass the
// maintenance window; automatic requests are checked against the saved UTC window

func (m *Manager) ApplyImageUpdate(ctx context.Context, in *store.Instance, update *store.ImageUpdate, automatic bool) (*store.Deployment, error) {
	if in == nil || update == nil || in.ID != update.InstanceID || in.Image == nil || *in.Image != update.CurrentImage {
		return nil, ErrImageUpdateChanged
	}
	t, err := m.Load(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if t.Service.SourceType != "image" {
		return nil, ErrImageUpdateNotImage
	}
	if automatic {
		now := time.Now().UTC()
		if !imageupdate.Enabled(in.ImageUpdatePolicy) || !imageupdate.InWindow(in.ImageUpdateWindow, in.ImageUpdateDays, in.ImageUpdateStartHour, in.ImageUpdateEndHour, now) {
			return nil, errors.New("outside the configured image update window")
		}
	}
	claimed, err := m.Store.ClaimImageUpdate(ctx, update.ID, automatic)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, ErrImageUpdateBusy
	}
	if automatic {
		// 백업은 오래 걸릴 수 있으니 스케줄러를 막지 않는다. 백업이 실패하면 배포하지 않는다.
		go func() {
			ctx := context.WithoutCancel(ctx)
			if err := m.backupBeforeImageUpdate(ctx, in, update); err != nil {
				_ = m.Store.ResetImageUpdate(ctx, update.ID, "failed", "volume backup failed: "+err.Error())
				m.emit(ctx, t, "image.update_failed", fmt.Sprintf("%s (%s): 볼륨 백업 실패로 이미지 업데이트를 건너뜁니다: %v", t.Service.Name, t.Env.Name, err))
				return
			}
			if _, err := m.enqueueImageUpdate(ctx, in, update); err != nil {
				m.warn("automatic image update", "instance", in.ID, "err", err)
			}
		}()
		return nil, nil
	}
	return m.enqueueImageUpdate(ctx, in, update)
}

func (m *Manager) enqueueImageUpdate(ctx context.Context, in *store.Instance, update *store.ImageUpdate) (*store.Deployment, error) {
	cur, err := m.Store.GetInstance(ctx, in.ID)
	if err != nil {
		_ = m.Store.ResetImageUpdate(ctx, update.ID, "available", err.Error())
		return nil, err
	}
	if cur.Image == nil || *cur.Image != update.CurrentImage {
		_ = m.Store.ResetImageUpdate(ctx, update.ID, "superseded", ErrImageUpdateChanged.Error())
		return nil, ErrImageUpdateChanged
	}
	previous := *cur.Image
	cur.Image = &update.TargetImage
	if err := m.Store.UpdateInstanceConfig(ctx, cur); err != nil {
		_ = m.Store.ResetImageUpdate(ctx, update.ID, "available", err.Error())
		return nil, err
	}
	digestImage := update.DigestImage
	if digestImage == "" {
		digestImage = update.TargetImage
	}
	builder := "image"
	d, err := m.Enqueue(ctx, cur, EnqueueOpts{Trigger: "image_update", ImageUpdateID: update.ID,
		Image: &digestImage, Spec: &cur.Spec, ReqSpec: &cur.Spec, BuilderUsed: &builder})
	if err != nil {
		_ = m.Store.RestoreImageIf(context.WithoutCancel(ctx), cur.ID, update.TargetImage, previous)
		_ = m.Store.ResetImageUpdate(context.WithoutCancel(ctx), update.ID, "failed", err.Error())
		return nil, err
	}
	return d, nil
}

// backupBeforeImageUpdate는 인스턴스에 붙은 볼륨을 차례로 백업하고 끝날 때까지 기다린다.
func (m *Manager) backupBeforeImageUpdate(ctx context.Context, in *store.Instance, update *store.ImageUpdate) error {
	vols, err := m.Store.ListVolumesByInstance(ctx, in.ID)
	if err != nil {
		return err
	}
	for _, v := range vols {
		if _, err := m.volumeDriver(ctx, v); err != nil {
			return err
		}
		dir := filepath.Join(m.backupDir(), v.ID)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
		b := &store.Backup{VolumeID: v.ID, Status: "running"}
		if err := m.Store.CreateBackup(ctx, b); err != nil {
			return err
		}
		b.Path = filepath.Join(dir, b.ID+".tar.zst")
		m.Store.UpdateBackup(ctx, b)
		if err := m.writeBackup(ctx, v, b); err != nil {
			msg := err.Error()
			b.Status, b.Error = "failed", &msg
			os.Remove(b.Path)
			m.Store.UpdateBackup(ctx, b)
			m.emitVolume(ctx, v, "volume.backup.failed", fmt.Sprintf("볼륨 %s 백업 실패 (이미지 자동 업데이트 %s): %v", v.Name, update.TargetImage, err))
			return fmt.Errorf("volume %s: %w", v.Name, err)
		}
		b.Status = "done"
		m.Store.UpdateBackup(ctx, b)
		m.emitVolume(ctx, v, "backup.done", fmt.Sprintf("볼륨 %s 백업 완료 (이미지 자동 업데이트 %s, %s)", v.Name, update.TargetImage, HumanBytes(b.SizeBytes)))
		m.pruneBackups(ctx, v)
	}
	return nil
}

// finishImageUpdate는 배포가 끝날 때 불린다. 이미지 업데이트 배포면 기록을 마치고 알림을 보내며,
// 실패하면 (그 사이 사용자가 바꾸지 않았다면) 설정한 이미지를 이전 값으로 되돌린다.
func (m *Manager) finishImageUpdate(ctx context.Context, d *store.Deployment, s store.DeployStatus, errMsg string, now time.Time) {
	ctx = context.WithoutCancel(ctx)
	update, err := m.Store.CompleteImageUpdate(ctx, d.ID, imageUpdateStatus(s), errMsg)
	if err != nil {
		return
	}
	if update == nil {
		if s == store.DepLive {
			m.recordDeployedDigest(ctx, d, now)
		}
		return
	}
	t, err := m.Load(ctx, update.InstanceID)
	if s == store.DepLive {
		if err == nil {
			kind := "수동"
			if update.Automatic {
				kind = "자동"
			}
			m.emit(ctx, t, "image.updated", fmt.Sprintf("%s (%s): 이미지를 %s에서 %s로 업데이트했습니다 (%s)",
				t.Service.Name, t.Env.Name, update.CurrentImage, update.TargetImage, kind))
		}
		return
	}
	if update.CurrentImage != update.TargetImage {
		_ = m.Store.RestoreImageIf(ctx, update.InstanceID, update.TargetImage, update.CurrentImage)
	}
	if err == nil && update.Status == "failed" {
		m.emit(ctx, t, "image.update_failed", fmt.Sprintf("%s (%s): 이미지 %s 업데이트에 실패했습니다", t.Service.Name, t.Env.Name, update.TargetImage))
	}
}

func imageUpdateStatus(s store.DeployStatus) string {
	switch s {
	case store.DepLive:
		return "applied"
	case store.DepFailed, store.DepCrashed:
		return "failed"
	default:
		return "superseded"
	}
}

func (m *Manager) warn(msg string, args ...any) {
	if m.Log != nil {
		m.Log.Warn(msg, args...)
	}
}
