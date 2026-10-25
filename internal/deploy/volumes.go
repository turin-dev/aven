package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// volumeDriver는 볼륨이 있는 노드의 드라이버다(원격 노드면 agent 연결). 노드가 꺼져 있으면 ErrNodeOffline.
func (m *Manager) volumeDriver(ctx context.Context, v *store.Volume) (Driver, error) {
	n := m.volumeNode(ctx, v)
	if !m.nodeOnline(n) {
		return nil, fmt.Errorf("%w (node %s)", ErrNodeOffline, n)
	}
	return m.driverForNode(n), nil
}

func (m *Manager) volumeTarget(ctx context.Context, v *store.Volume) *Target {
	if v.InstanceID == nil {
		return nil
	}
	t, err := m.Load(ctx, *v.InstanceID)
	if err != nil {
		return nil
	}
	return t
}

func (m *Manager) emitVolume(ctx context.Context, v *store.Volume, kind, msg string) {
	if m.Events == nil {
		return
	}
	var svc *string
	if t := m.volumeTarget(ctx, v); t != nil {
		svc = &t.Service.ID
	}
	m.Events.Emit(ctx, v.ProjectID, &v.EnvironmentID, svc, kind, msg)
}

// CreateBackup은 볼륨을 tar.zst로 백업한다(비동기). 짧게 사는 helper 컨테이너가 tar 스트림을 내고 aven이 zstd로 압축한다.
func (m *Manager) CreateBackup(ctx context.Context, v *store.Volume) (*store.Backup, error) {
	if _, err := m.volumeDriver(ctx, v); err != nil {
		return nil, err
	}
	dir := filepath.Join(m.backupDir(), v.ID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	b := &store.Backup{VolumeID: v.ID, Status: "running"}
	if err := m.Store.CreateBackup(ctx, b); err != nil {
		return nil, err
	}
	b.Path = filepath.Join(dir, b.ID+".tar.zst")
	m.Store.UpdateBackup(ctx, b)
	go func() {
		ctx := context.Background()
		err := m.writeBackup(ctx, v, b)
		if err != nil {
			msg := err.Error()
			b.Status, b.Error = "failed", &msg
			os.Remove(b.Path)
			m.Store.UpdateBackup(ctx, b)
			m.emitVolume(ctx, v, "volume.backup.failed", fmt.Sprintf("볼륨 %s 백업 실패: %v", v.Name, err))
			return
		}
		b.Status = "done"
		m.Store.UpdateBackup(ctx, b)
		m.emitVolume(ctx, v, "backup.done", fmt.Sprintf("볼륨 %s 백업 완료 (%s)", v.Name, HumanBytes(b.SizeBytes)))
		m.pruneBackups(ctx, v)
	}()
	return b, nil
}

// CreateBackupSync creates a completed volume backup before returning. Offline database upgrades use it
// so the snapshot is consistent and the upgrade cannot begin before the backup is durable.
func (m *Manager) CreateBackupSync(ctx context.Context, v *store.Volume) (*store.Backup, error) {
	if _, err := m.volumeDriver(ctx, v); err != nil {
		return nil, err
	}
	dir := filepath.Join(m.backupDir(), v.ID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	b := &store.Backup{VolumeID: v.ID, Status: "running"}
	if err := m.Store.CreateBackup(ctx, b); err != nil {
		return nil, err
	}
	b.Path = filepath.Join(dir, b.ID+".tar.zst")
	if err := m.Store.UpdateBackup(ctx, b); err != nil {
		return b, err
	}
	if err := m.writeBackup(ctx, v, b); err != nil {
		msg := err.Error()
		b.Status, b.Error = "failed", &msg
		_ = m.Store.UpdateBackup(context.WithoutCancel(ctx), b)
		os.Remove(b.Path)
		return b, err
	}
	b.Status = "done"
	if err := m.Store.UpdateBackup(ctx, b); err != nil {
		return b, err
	}
	m.emitVolume(ctx, v, "backup.done", fmt.Sprintf("볼륨 %s 백업 완료 (%s)", v.Name, HumanBytes(b.SizeBytes)))
	return b, nil
}

func (m *Manager) writeBackup(ctx context.Context, v *store.Volume, b *store.Backup) error {
	drv, err := m.volumeDriver(ctx, v)
	if err != nil {
		return err
	}
	if err := drv.EnsureVolume(ctx, v.DockerName, m.volumeLabels(v)); err != nil {
		return err
	}
	f, err := os.OpenFile(b.Path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	enc, err := zstd.NewWriter(f, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return err
	}
	name := "keel-backup-" + strings.TrimPrefix(b.ID, "bak_")
	if err := drv.ExportVolume(ctx, v.DockerName, name, enc); err != nil {
		enc.Close()
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		return err
	}
	b.SizeBytes = st.Size()
	return f.Sync()
}

func (m *Manager) pruneBackups(ctx context.Context, v *store.Volume) {
	cur, err := m.Store.GetVolume(ctx, v.ID)
	if err != nil {
		return
	}
	bs, err := m.Store.ListBackups(ctx, v.ID)
	if err != nil {
		return
	}
	n := 0
	for _, b := range bs { // 최신순
		if b.Status != "done" {
			continue
		}
		n++
		if n > cur.BackupKeep {
			os.Remove(b.Path)
			m.Store.DeleteBackup(ctx, b.ID)
		}
	}
}

func (m *Manager) DeleteBackup(ctx context.Context, b *store.Backup) error {
	if b.Status == "running" {
		return errors.New("backup is still running")
	}
	os.Remove(b.Path)
	return m.Store.DeleteBackup(ctx, b.ID)
}

// RestoreBackup은 서비스를 멈추고 볼륨을 백업 내용으로 바꾼 뒤 다시 배포한다(비동기).
func (m *Manager) RestoreBackup(ctx context.Context, b *store.Backup) error {
	if b.Status != "done" {
		return errors.New("only finished backups can be restored")
	}
	v, err := m.Store.GetVolume(ctx, b.VolumeID)
	if err != nil {
		return err
	}
	if _, err := m.volumeDriver(ctx, v); err != nil {
		return err
	}
	go func() {
		ctx := context.Background()
		if err := m.restore(ctx, v, b); err != nil {
			m.Log.Error("restore", "backup", b.ID, "err", err)
			m.emitVolume(ctx, v, "backup.restore_failed", fmt.Sprintf("볼륨 %s를 백업 %s로 복원하지 못했습니다: %v", v.Name, b.ID, err))
		}
	}()
	return nil
}

// RestoreBackupSync restores a completed snapshot before returning.
func (m *Manager) RestoreBackupSync(ctx context.Context, b *store.Backup) error {
	if b.Status != "done" {
		return errors.New("only finished backups can be restored")
	}
	v, err := m.Store.GetVolume(ctx, b.VolumeID)
	if err != nil {
		return err
	}
	if _, err := m.volumeDriver(ctx, v); err != nil {
		return err
	}
	return m.restore(ctx, v, b)
}

func (m *Manager) restore(ctx context.Context, v *store.Volume, b *store.Backup) error {
	drv, err := m.volumeDriver(ctx, v)
	if err != nil {
		return err
	}
	t := m.volumeTarget(ctx, v)
	var cur *store.Deployment
	if t != nil {
		m.mu.Lock()
		m.paused[t.Instance.ID] = true
		m.mu.Unlock()
		defer func() {
			m.mu.Lock()
			delete(m.paused, t.Instance.ID)
			m.mu.Unlock()
		}()
		if t.Instance.CurrentDeploymentID != nil {
			cur, _ = m.Store.GetDeployment(ctx, *t.Instance.CurrentDeploymentID)
		}
		if cur != nil {
			for _, c := range cur.Containers {
				m.markDrain(c)
				drv.Stop(ctx, c, 30)
			}
			defer func() {
				for _, c := range cur.Containers {
					m.undrain(c)
				}
			}()
		}
	}
	f, err := os.Open(b.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	dec, err := zstd.NewReader(f)
	if err != nil {
		return err
	}
	defer dec.Close()
	pr, pw := io.Pipe()
	go func() { _, err := io.Copy(pw, dec); pw.CloseWithError(err) }()
	name := "keel-restore-" + strings.TrimPrefix(b.ID, "bak_")
	ierr := drv.ImportVolume(ctx, v.DockerName, name, pr)
	pr.Close()
	if cur != nil && t != nil {
		if ierr != nil {
			for _, c := range cur.Containers {
				drv.Start(ctx, c)
			}
			return ierr
		}
		in, err := m.Store.GetInstance(ctx, t.Instance.ID)
		if err == nil && in.Status != store.InstOffline {
			// 같은 이미지·현재 변수로 다시 배포한다(볼륨 서비스는 recreate 전략).
			bu := cur.BuilderUsed
			if _, err := m.Enqueue(ctx, in, EnqueueOpts{Trigger: "redeploy", Image: cur.Image, Spec: &cur.Spec, ReqSpec: &cur.ReqSpec,
				Commit: commitOf(cur), Branch: cur.Branch, BuilderUsed: bu}); err != nil {
				return err
			}
		}
	}
	if ierr != nil {
		return ierr
	}
	m.emitVolume(ctx, v, "backup.restored", fmt.Sprintf("볼륨 %s를 백업 %s로 복원했습니다", v.Name, b.ID))
	return nil
}

func commitOf(d *store.Deployment) *nodeCommit {
	c := &nodeCommit{}
	if d.CommitSHA != nil {
		c.SHA = *d.CommitSHA
	}
	if d.CommitMessage != nil {
		c.Message = *d.CommitMessage
	}
	if d.CommitAuthor != nil {
		c.Author = *d.CommitAuthor
	}
	return c
}

type nodeCommit = node.Commit

// CommitOf는 배포의 커밋 정보를 rollback/redeploy용으로 돌려준다.
func CommitOf(d *store.Deployment) *node.Commit { return commitOf(d) }

// HumanBytes는 크기를 사람이 읽는 형식(10진, 예: "3.3 MB")으로 바꾼다.
func HumanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}
