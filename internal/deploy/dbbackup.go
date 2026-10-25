package deploy

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/cronx"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/s3"
	"github.com/turin-dev/aven/internal/store"
)

// ---------------- 저장소(local / S3) ----------------

// BackupStorage는 백업 파일을 스트림으로 읽고 쓴다(전체를 메모리에 올리지 않는다).
type BackupStorage interface {
	Put(ctx context.Context, key string, r io.Reader) (int64, error)
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

type localStorage struct{ root string }

func (l *localStorage) file(key string) (string, error) {
	p := filepath.Join(l.root, filepath.FromSlash(path.Clean("/"+key)))
	if !strings.HasPrefix(p, l.root+string(filepath.Separator)) {
		return "", errors.New("invalid backup key")
	}
	return p, nil
}

func (l *localStorage) Put(ctx context.Context, key string, r io.Reader) (int64, error) {
	p, err := l.file(key)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return 0, err
	}
	tmp := p + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, r)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return n, err
	}
	return n, os.Rename(tmp, p)
}

func (l *localStorage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	p, err := l.file(key)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

func (l *localStorage) Delete(ctx context.Context, key string) error {
	p, err := l.file(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

type s3Storage struct {
	c      *s3.Client
	prefix string
}

func (s *s3Storage) k(key string) string {
	if s.prefix == "" {
		return key
	}
	return strings.Trim(s.prefix, "/") + "/" + key
}
func (s *s3Storage) Put(ctx context.Context, key string, r io.Reader) (int64, error) {
	return s.c.PutStream(ctx, s.k(key), r)
}
func (s *s3Storage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return s.c.Get(ctx, s.k(key))
}
func (s *s3Storage) Delete(ctx context.Context, key string) error { return s.c.Delete(ctx, s.k(key)) }

// S3Client는 목적지 설정으로 S3 클라이언트를 만든다.
func S3Client(d *store.BackupDestination) *s3.Client {
	return &s3.Client{Endpoint: d.Endpoint, Region: d.Region, Bucket: d.Bucket, AccessKey: d.AccessKey, SecretKey: d.SecretKey}
}

// BackupStorageFor는 목적지 ID(빈 값 = local)에 맞는 저장소를 돌려준다.
func (m *Manager) BackupStorageFor(ctx context.Context, destID string) (BackupStorage, error) {
	if destID == "" {
		return &localStorage{root: filepath.Join(m.Cfg.DataDir, "dbbackups")}, nil
	}
	d, err := m.Store.GetBackupDestination(ctx, destID)
	if err != nil {
		return nil, fmt.Errorf("backup destination %s: %w", destID, err)
	}
	switch d.Type {
	case store.DestLocal:
		return &localStorage{root: filepath.Join(m.Cfg.DataDir, "dbbackups")}, nil
	case store.DestS3:
		return &s3Storage{c: S3Client(d), prefix: d.Prefix}, nil
	}
	return nil, fmt.Errorf("unknown destination type %q", d.Type)
}

// ---------------- 실행 ----------------

var dbJobs = struct {
	sync.Mutex
	busy     map[string]bool
	restores map[string]*RestoreStatus
}{busy: map[string]bool{}, restores: map[string]*RestoreStatus{}}

// RestoreStatus는 마지막 복원 작업의 진행 상태다(메모리에만 둔다).
type RestoreStatus struct {
	Status     string     `json:"status"` // running | done | failed
	Error      string     `json:"error,omitempty"`
	TargetID   string     `json:"target_instance_id"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

func lockInstance(id string) bool {
	dbJobs.Lock()
	defer dbJobs.Unlock()
	if dbJobs.busy[id] {
		return false
	}
	dbJobs.busy[id] = true
	return true
}

func unlockInstance(id string) {
	dbJobs.Lock()
	delete(dbJobs.busy, id)
	dbJobs.Unlock()
}

// LastRestore는 백업의 마지막 복원 상태다(없으면 nil).
func LastRestore(backupID string) *RestoreStatus {
	dbJobs.Lock()
	defer dbJobs.Unlock()
	if r := dbJobs.restores[backupID]; r != nil {
		c := *r
		return &c
	}
	return nil
}

var ErrDBBusy = errors.New("another backup or restore of this database is in progress")

// dbTarget은 DB 서비스 인스턴스에서 백업/복원에 필요한 정보를 모은다.
type dbTarget struct {
	t     *Target
	kind  string
	image string
	vars  map[string]string
	host  string
	port  int
	cur   *store.Deployment
	node  string // 데이터베이스가 도는 노드
	drv   Driver
}

func (m *Manager) dbTargetFor(ctx context.Context, in *store.Instance) (*dbTarget, error) {
	t, err := m.Load(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	kind := ""
	if t.Service.DatabaseKind != nil {
		kind = *t.Service.DatabaseKind
	}
	if t.Service.SourceType != "database" || !IsDBKind(kind) {
		return nil, fmt.Errorf("service %s is not a database that supports logical backups (postgres, mysql, mariadb, redis, mongo)", t.Service.Name)
	}
	nodeID := m.homeNode(ctx, in)
	if !isLocal(nodeID) && !m.nodeOnline(nodeID) {
		return nil, fmt.Errorf("%w (node %s)", ErrNodeOffline, nodeID)
	}
	res, err := m.Resolve(ctx, in, nil)
	if err != nil {
		return nil, err
	}
	dt := &dbTarget{t: t, kind: kind, vars: res.Vars, host: PrivateDomain(t.Service.Name, t.Env.Name), node: nodeID, drv: m.driverForNode(nodeID)}
	if in.CurrentDeploymentID != nil {
		dt.cur, _ = m.Store.GetDeployment(ctx, *in.CurrentDeploymentID)
	}
	if dt.cur != nil && dt.cur.Image != nil {
		dt.image = *dt.cur.Image
	} else if in.Image != nil {
		dt.image = *in.Image
	}
	if dt.image == "" {
		return nil, errors.New("database has no image yet (deploy it first)")
	}
	dt.port = DBPort(kind, 0)
	if in.Port != nil && *in.Port > 0 {
		dt.port = *in.Port
	}
	return dt, nil
}

func (m *Manager) toolSpec(dt *dbTarget, name string, tool *dbTool) node.RunSpec {
	env := map[string]string{}
	for k, v := range tool.Env {
		env[k] = v
	}
	sh := "/bin/sh"
	return node.RunSpec{
		Name: name, Image: dt.image, Env: env, Network: EnvNetwork(dt.t.Env.ID),
		Labels: map[string]string{node.LabelManaged: "true", node.LabelProject: dt.t.Project.ID, node.LabelEnv: dt.t.Env.ID,
			node.LabelService: dt.t.Service.ID, node.LabelRole: node.RoleHelper},
		Entrypoint: &sh, Cmd: []string{"-c", tool.Script},
	}
}

type tailBuf struct {
	mu    sync.Mutex
	lines []string
}

func (t *tailBuf) add(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, s)
	if len(t.lines) > 8 {
		t.lines = t.lines[1:]
	}
}
func (t *tailBuf) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, " | ")
}

// CreateDBBackup은 DB 논리 백업을 시작한다(비동기). destID가 비면 local이다.
func (m *Manager) CreateDBBackup(ctx context.Context, in *store.Instance, trigger, destID string) (*store.DBBackup, error) {
	dt, err := m.dbTargetFor(ctx, in)
	if err != nil {
		return nil, err
	}
	if !liveStatus(in.Status) || in.Status == store.InstDeploying {
		return nil, fmt.Errorf("database is %s; it must be online to back up", in.Status)
	}
	tool, err := dumpTool(dt.kind, dt.host, dt.port, dt.vars)
	if err != nil {
		return nil, err
	}
	st, err := m.BackupStorageFor(ctx, destID)
	if err != nil {
		return nil, err
	}
	if !lockInstance(in.ID) {
		return nil, ErrDBBusy
	}
	b := &store.DBBackup{ServiceID: dt.t.Service.ID, EnvironmentID: dt.t.Env.ID, InstanceID: in.ID, Kind: dt.kind, Status: "running", Trigger: trigger, DestinationID: destID}
	if err := m.Store.CreateDBBackup(ctx, b); err != nil {
		unlockInstance(in.ID)
		return nil, err
	}
	b.FileName = fmt.Sprintf("%s-%s.%s", b.CreatedAt.Format("20060102T150405Z"), strings.TrimPrefix(b.ID, "dbb_"), tool.Ext)
	b.ObjectKey = fmt.Sprintf("%s-%s/%s", dt.t.Service.Name, dt.t.Env.Name, b.FileName)
	m.Store.ExecDBBackupKey(ctx, b)
	go func() {
		defer unlockInstance(in.ID)
		bg := context.Background()
		size, err := m.runDump(bg, dt, tool, st, b)
		now := time.Now().UTC()
		b.FinishedAt = &now
		b.SizeBytes = size
		if err != nil {
			msg := err.Error()
			b.Status, b.Error = "failed", &msg
			st.Delete(bg, b.ObjectKey)
			m.Store.UpdateDBBackup(bg, b)
			m.emitDB(bg, dt, "dbbackup.failed", fmt.Sprintf("%s: DB 백업 실패: %v", dt.t.Service.Name, err))
			return
		}
		b.Status = "done"
		m.Store.UpdateDBBackup(bg, b)
		m.emitDB(bg, dt, "dbbackup.done", fmt.Sprintf("%s: DB 백업 완료 (%s, %s)", dt.t.Service.Name, b.FileName, HumanBytes(size)))
		m.pruneDBBackups(bg, in.ID)
	}()
	return b, nil
}

func (m *Manager) emitDB(ctx context.Context, dt *dbTarget, kind, msg string) {
	if m.Events != nil {
		m.Events.Emit(ctx, dt.t.Project.ID, &dt.t.Env.ID, &dt.t.Service.ID, kind, msg)
	}
}

// runDump는 덤프 컨테이너의 stdout을 저장소로 바로 흘린다(파이프). 덤프가 실패하면 업로드도 실패로 끝난다.
func (m *Manager) runDump(ctx context.Context, dt *dbTarget, tool *dbTool, st BackupStorage, b *store.DBBackup) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Hour)
	defer cancel()
	pr, pw := io.Pipe()
	tail := &tailBuf{}
	go func() {
		var w io.Writer = pw
		var gz *gzip.Writer
		if tool.Gzip {
			gz = gzip.NewWriter(pw)
			w = gz
		}
		code, err := m.streamOut(ctx, dt, strings.TrimPrefix(b.ID, "dbb_"), tool, w, tail.add)
		if err == nil && code != 0 {
			err = fmt.Errorf("%s dump exited with code %d: %s", dt.kind, code, tail.String())
		}
		if err == nil && gz != nil {
			err = gz.Close()
		}
		pw.CloseWithError(err)
	}()
	size, err := st.Put(ctx, b.ObjectKey, pr)
	pr.CloseWithError(err) // Put이 먼저 실패하면 덤프 쪽 쓰기도 풀어 준다
	cancel()
	if err != nil {
		return size, err
	}
	if size == 0 {
		return 0, errors.New("dump is empty")
	}
	return size, nil
}

// pruneDBBackups는 성공한 백업을 최신 retention개만 남기고 지운다(저장소의 파일도 지운다). 실패 기록은 10개만 남긴다.
func (m *Manager) pruneDBBackups(ctx context.Context, instanceID string) {
	cfg, err := m.Store.GetDBBackupConfig(ctx, instanceID)
	if err != nil {
		return
	}
	bs, err := m.Store.ListDBBackups(ctx, instanceID)
	if err != nil {
		return
	}
	done, failed := 0, 0
	for _, b := range bs { // 최신순
		switch b.Status {
		case "done":
			done++
			if cfg.Retention > 0 && done > cfg.Retention {
				m.deleteDBBackupFiles(ctx, b)
				m.Store.DeleteDBBackup(ctx, b.ID)
			}
		case "failed":
			failed++
			if failed > 10 {
				m.Store.DeleteDBBackup(ctx, b.ID)
			}
		}
	}
}

func (m *Manager) deleteDBBackupFiles(ctx context.Context, b *store.DBBackup) {
	st, err := m.BackupStorageFor(ctx, b.DestinationID)
	if err != nil {
		m.Log.Warn("delete db backup: storage", "backup", b.ID, "err", err)
		return
	}
	if err := st.Delete(ctx, b.ObjectKey); err != nil {
		m.Log.Warn("delete db backup file", "backup", b.ID, "err", err)
	}
}

// DeleteDBBackup은 백업 기록과 저장소의 파일을 지운다.
func (m *Manager) DeleteDBBackup(ctx context.Context, b *store.DBBackup) error {
	st, err := m.BackupStorageFor(ctx, b.DestinationID)
	if err == nil {
		if err := st.Delete(ctx, b.ObjectKey); err != nil {
			return fmt.Errorf("delete backup file: %w", err)
		}
	}
	return m.Store.DeleteDBBackup(ctx, b.ID)
}

// OpenDBBackup은 백업 파일을 스트림으로 연다(다운로드용).
func (m *Manager) OpenDBBackup(ctx context.Context, b *store.DBBackup) (io.ReadCloser, error) {
	st, err := m.BackupStorageFor(ctx, b.DestinationID)
	if err != nil {
		return nil, err
	}
	return st.Get(ctx, b.ObjectKey)
}

// RestoreDBBackup은 백업을 target DB 서비스(같은 종류)에 복원한다(비동기). 대상의 기존 데이터는 덮어쓴다.
func (m *Manager) RestoreDBBackup(ctx context.Context, b *store.DBBackup, target *store.Instance) error {
	if b.Status != "done" {
		return errors.New("only a completed backup can be restored")
	}
	dt, err := m.dbTargetFor(ctx, target)
	if err != nil {
		return err
	}
	if !SameDBFamily(b.Kind, dt.kind) {
		return fmt.Errorf("backup is %s but the target is %s", b.Kind, dt.kind)
	}
	if dt.kind != "redis" && (!liveStatus(target.Status) || target.Status == store.InstDeploying) {
		return fmt.Errorf("target database is %s; it must be online to restore", target.Status)
	}
	tool, err := restoreTool(dt.kind, dt.host, dt.port, dt.vars)
	if err != nil {
		return err
	}
	var vol *store.Volume
	if tool.DataDir {
		vols, err := m.Store.ListVolumesByInstance(ctx, target.ID)
		if err != nil || len(vols) == 0 {
			return errors.New("target has no volume to restore the RDB into")
		}
		vol = vols[0]
		tool.Env["DATADIR"] = vol.MountPath
	}
	if _, err := m.BackupStorageFor(ctx, b.DestinationID); err != nil {
		return err
	}
	if !lockInstance(target.ID) {
		return ErrDBBusy
	}
	rs := &RestoreStatus{Status: "running", TargetID: target.ID, StartedAt: time.Now().UTC()}
	dbJobs.Lock()
	dbJobs.restores[b.ID] = rs
	dbJobs.Unlock()
	go func() {
		defer unlockInstance(target.ID)
		bg := context.Background()
		err := m.runRestore(bg, dt, tool, vol, b, target)
		now := time.Now().UTC()
		dbJobs.Lock()
		rs.FinishedAt = &now
		if err != nil {
			rs.Status, rs.Error = "failed", err.Error()
		} else {
			rs.Status = "done"
		}
		dbJobs.Unlock()
		if err != nil {
			m.emitDB(bg, dt, "dbbackup.restore_failed", fmt.Sprintf("%s: DB 복원 실패: %v", dt.t.Service.Name, err))
			return
		}
		m.emitDB(bg, dt, "dbbackup.restored", fmt.Sprintf("%s: 백업 %s로 DB를 복원했습니다", dt.t.Service.Name, b.FileName))
	}()
	return nil
}

func (m *Manager) runRestore(ctx context.Context, dt *dbTarget, tool *dbTool, vol *store.Volume, b *store.DBBackup, target *store.Instance) error {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Hour)
	defer cancel()
	src, err := m.OpenDBBackup(ctx, b)
	if err != nil {
		return fmt.Errorf("open backup: %w", err)
	}
	defer src.Close()
	var in io.Reader = src
	if tool.Gzip {
		gz, err := gzip.NewReader(src)
		if err != nil {
			return fmt.Errorf("backup is not gzip: %w", err)
		}
		defer gz.Close()
		in = gz
	}
	job := strings.TrimPrefix(b.ID, "dbb_")
	if vol != nil {
		// redis: 서비스를 멈추고 볼륨에 직접 쓴다. reconcile이 되살리지 않게 잠시 멈춰 둔다.
		m.mu.Lock()
		m.paused[target.ID] = true
		m.mu.Unlock()
		defer func() {
			m.mu.Lock()
			delete(m.paused, target.ID)
			m.mu.Unlock()
		}()
		if dt.cur != nil {
			for _, c := range dt.cur.Containers {
				m.markDrain(c)
				dt.drv.Stop(ctx, c, 30)
			}
			defer func() {
				for _, c := range dt.cur.Containers {
					dt.drv.Start(context.WithoutCancel(ctx), c)
					m.undrain(c)
				}
			}()
		}
	}
	tail := &tailBuf{}
	code, err := m.streamIn(ctx, dt, job, tool, vol, in, tail.add)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("%s restore exited with code %d: %s", dt.kind, code, tail.String())
	}
	return nil
}

// ---------------- 일정 ----------------

// dbBackupTick은 정기 DB 백업이 켜진 인스턴스 중 지금(분 단위)이 일정에 맞는 것을 백업한다.
func (m *Manager) dbBackupTick(ctx context.Context, now time.Time) {
	cfgs, err := m.Store.ListEnabledDBBackupConfigs(ctx)
	if err != nil {
		return
	}
	for id, c := range cfgs {
		s, err := cronx.Parse(c.Cron)
		if err != nil || !s.Match(now) {
			continue
		}
		in, err := m.Store.GetInstance(ctx, id)
		if err != nil {
			continue
		}
		if _, err := m.CreateDBBackup(ctx, in, "schedule", c.DestinationID); err != nil {
			m.Log.Warn("scheduled db backup", "instance", id, "err", err)
		}
	}
}

// streamOut은 덤프 도구를 dt의 노드에서 실행하고 stdout을 w로 흘린다. 원격 노드는 agent의 db.backup이
// 같은 스크립트 표로 helper 컨테이너를 만들고 chunk로 보낸다(서버는 스크립트를 보내지 않는다).
func (m *Manager) streamOut(ctx context.Context, dt *dbTarget, job string, tool *dbTool, w io.Writer, stderr func(string)) (int, error) {
	if rd, ok := dt.drv.(*remoteDriver); ok {
		req, err := dt.toolRequest(job, nil)
		if err != nil {
			return 0, err
		}
		return 0, rd.dbBackup(ctx, req, w)
	}
	return m.Docker.RunStream(ctx, m.toolSpec(dt, "keel-dbb-"+job, tool), nil, w, stderr)
}

// streamIn은 복원 도구를 dt의 노드에서 stdin=in으로 실행한다(원격 노드는 먼저 agent에 올린 뒤 agent가 실행한다).
// vol이 있으면(redis) 그 볼륨을 DATADIR에 마운트한다.
func (m *Manager) streamIn(ctx context.Context, dt *dbTarget, job string, tool *dbTool, vol *store.Volume, in io.Reader, stderr func(string)) (int, error) {
	if rd, ok := dt.drv.(*remoteDriver); ok {
		req, err := dt.toolRequest(job, vol)
		if err != nil {
			return 0, err
		}
		return 0, rd.dbRestore(ctx, req, in)
	}
	spec := m.toolSpec(dt, "keel-dbr-"+job, tool)
	if vol != nil {
		spec.Mounts = []node.Mount{{Volume: vol.DockerName, Path: vol.MountPath}}
	}
	return m.Docker.RunStream(ctx, spec, in, nil, stderr)
}
