package deploy

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

//
//   - Postgres: 켜면 DB 컨테이너 안에서 ALTER SYSTEM으로 archive_mode=on, archive_command(데이터 볼륨의
//     keel-pitr/wal로 복사), archive_timeout=60을 설정하고, pg_hba.conf에 replication 줄을 더한 뒤(기준 백업용) 필요하면 재시작한다.
//     매 분 활동이 있었으면 작은 트랜잭션(커밋 시각 표시)과 pg_switch_wal()로 세그먼트를 닫고, helper가 볼륨의 WAL을 읽어
//     서버가 gzip으로 저장소에 올린 뒤 볼륨에서 지운다. 기준 백업은 하루에 한 번 pg_basebackup.
//   - MySQL: 바이너리 로그(8.x 기본값)를 쓴다. 매 분 활성 binlog가 커졌으면 FLUSH BINARY LOGS로 닫고, 닫힌 binlog를
//     mysqlbinlog --read-from-remote-server --raw로 받아 올린 뒤 PURGE BINARY LOGS로 지운다. 기준 백업은 하루에 한 번
//     mysqldump --source-data=2(binlog 위치 포함).
//   - 복원: 목표 시각 이전의 가장 새 기준 백업과 그 뒤 로그를 대상 DB에 넣는다. Postgres는 멈춘 대상 볼륨에 기준 백업을 풀고
//     recovery_target_time으로 재생한 뒤 promote, MySQL은 덤프를 넣고 mysqlbinlog --stop-datetime으로 재생한다.

const (
	pitrBaseEvery   = 24 * time.Hour
	pitrBaseBackoff = 10 * time.Minute
	// pitrLogSlack: postgres 복원은 목표 시각 뒤의 커밋 레코드가 있어야 멈춘다. 목표 뒤 이만큼 보관된 WAL까지 함께 넣는다.
	pitrLogSlack = 2 * time.Minute
)

var (
	ErrPITRNotSupported = errors.New("point-in-time recovery is only available for PostgreSQL and MySQL databases")
	ErrPITRBusy         = errors.New("a point-in-time recovery job of this database is in progress")
)

var pitrJobs = struct {
	sync.Mutex
	cfg      sync.Mutex // 설정 read-modify-write
	busy     map[string]string
	restores map[string]*PITRRestoreStatus
}{busy: map[string]string{}, restores: map[string]*PITRRestoreStatus{}}

// PITRSupported는 시점 복구를 지원하는 DB 종류인지 알려준다(postgres, mysql).
func PITRSupported(kind string) bool { return agentprotocol.PITRSupported(kind) }

// pitrLock은 인스턴스의 PITR 작업(base / sync / restore) 하나를 잡는다.
func pitrLock(id, what string) bool {
	pitrJobs.Lock()
	defer pitrJobs.Unlock()
	if pitrJobs.busy[id+"/"+what] != "" {
		return false
	}
	pitrJobs.busy[id+"/"+what] = what
	return true
}

func pitrUnlock(id, what string) {
	pitrJobs.Lock()
	delete(pitrJobs.busy, id+"/"+what)
	pitrJobs.Unlock()
}

// PITRRestoreRunning은 원본 인스턴스의 시점 복원이 진행 중인지 알려준다.
func PITRRestoreRunning(id string) bool {
	pitrJobs.Lock()
	defer pitrJobs.Unlock()
	return pitrJobs.busy[id+"/restore"] != ""
}

func pitrRunning(id string) []string {
	pitrJobs.Lock()
	defer pitrJobs.Unlock()
	var out []string
	for k, v := range pitrJobs.busy {
		if strings.HasPrefix(k, id+"/") {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// PITRRestoreStatus는 마지막 시점 복원 작업이다(메모리에만 둔다, 원본 인스턴스 ID별).
type PITRRestoreStatus struct {
	Status          string     `json:"status"` // running | done | failed
	Step            string     `json:"step,omitempty"`
	Error           string     `json:"error,omitempty"`
	TargetTime      time.Time  `json:"target_time"`
	TargetServiceID string     `json:"target_service_id"`
	TargetInstance  string     `json:"target_instance_id"`
	InPlace         bool       `json:"in_place"`
	StartedAt       time.Time  `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at"`
}

// LastPITRRestore는 원본 인스턴스의 마지막 시점 복원 상태다(없으면 nil).
func LastPITRRestore(sourceInstanceID string) *PITRRestoreStatus {
	pitrJobs.Lock()
	defer pitrJobs.Unlock()
	if r := pitrJobs.restores[sourceInstanceID]; r != nil {
		c := *r
		return &c
	}
	return nil
}

func setPITRRestore(id string, f func(r *PITRRestoreStatus)) {
	pitrJobs.Lock()
	defer pitrJobs.Unlock()
	if r := pitrJobs.restores[id]; r != nil {
		f(r)
	}
}

// ---------------- 상태 ----------------

// PITRWindow는 복원할 수 있는 시각 범위다.
type PITRWindow struct {
	Earliest *time.Time `json:"earliest"`
	Latest   *time.Time `json:"latest"`
	Bases    int        `json:"bases"`
	Logs     int        `json:"logs"`
	Bytes    int64      `json:"bytes"`
}

// PITRStatus는 GET .../pitr 응답이다.
type PITRStatus struct {
	Supported bool                `json:"supported"`
	Reason    string              `json:"reason,omitempty"`
	Kind      string              `json:"kind"`
	Config    *store.DBPITRConfig `json:"config"`
	Window    PITRWindow          `json:"window"`
	Running   []string            `json:"running"`
	Restore   *PITRRestoreStatus  `json:"restore"`
}

// pitrWindow는 보관 파일로 복원 가능 범위를 계산한다. 기준 백업이 있는 epoch의 로그만 센다.
func pitrWindow(objs []*store.DBPITRObject) PITRWindow {
	w := PITRWindow{}
	baseStart := map[int]time.Time{} // epoch → 가장 이른 기준 백업 완료 시각
	for _, o := range objs {
		w.Bytes += o.SizeBytes
		if o.ObjectType != "base" {
			w.Logs++
			continue
		}
		w.Bases++
		if t, ok := baseStart[o.Epoch]; !ok || o.SourceTime.Before(t) {
			baseStart[o.Epoch] = o.SourceTime
		}
		if w.Earliest == nil || o.SourceTime.Before(*w.Earliest) {
			t := o.SourceTime
			w.Earliest = &t
		}
	}
	for _, o := range objs {
		b, ok := baseStart[o.Epoch]
		if o.ObjectType == "base" || !ok || o.SourceTime.Before(b) {
			continue
		}
		if w.Latest == nil || o.SourceTime.After(*w.Latest) {
			t := o.SourceTime
			w.Latest = &t
		}
	}
	if w.Latest == nil {
		w.Earliest = nil // 기준 백업 뒤 로그가 아직 없으면 복원할 수 없다
	}
	return w
}

// PITRStatusFor는 인스턴스의 PITR 설정, 복원 범위, 진행 중 작업이다.
func (m *Manager) PITRStatusFor(ctx context.Context, in *store.Instance, kind string) (*PITRStatus, error) {
	cfg, err := m.Store.GetDBPITRConfig(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	objs, err := m.Store.ListDBPITRObjects(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	st := &PITRStatus{Supported: agentprotocol.PITRSupported(kind), Kind: kind, Config: cfg, Window: pitrWindow(objs),
		Running: pitrRunning(in.ID), Restore: LastPITRRestore(in.ID)}
	if st.Running == nil {
		st.Running = []string{}
	}
	if !st.Supported {
		st.Reason = ErrPITRNotSupported.Error()
	}
	return st, nil
}

// ---------------- 계획(순수 함수) ----------------

type pitrPlan struct {
	Base *store.DBPITRObject
	Logs []*store.DBPITRObject
}

var binlogSeqRe = regexp.MustCompile(`^(.*)\.(\d+)$`)

// binlogSeq는 binlog 이름의 (기본 이름, 번호)다.
func binlogSeq(name string) (string, int64, bool) {
	m := binlogSeqRe.FindStringSubmatch(name)
	if m == nil {
		return "", 0, false
	}
	n, err := strconv.ParseInt(m[2], 10, 64)
	return m[1], n, err == nil
}

// binlogLess는 binlog 이름을 번호 순으로 비교한다.
func binlogLess(a, b string) bool {
	ab, an, aok := binlogSeq(a)
	bb, bn, bok := binlogSeq(b)
	if aok && bok && ab == bb {
		return an < bn
	}
	return a < b
}

// planPITR은 at 시점으로 복원하는 데 필요한 기준 백업과 로그를 고른다.
func planPITR(kind string, objs []*store.DBPITRObject, at time.Time) (*pitrPlan, error) {
	w := pitrWindow(objs)
	if w.Earliest == nil || w.Latest == nil {
		return nil, errors.New("아직 복원할 수 있는 시점이 없습니다(첫 기준 백업과 로그 보관을 기다리세요)")
	}
	if at.Before(*w.Earliest) {
		return nil, fmt.Errorf("가장 이른 복원 시점은 %s입니다", w.Earliest.UTC().Format(time.RFC3339))
	}
	if at.After(*w.Latest) {
		return nil, fmt.Errorf("가장 최근 복원 시점은 %s입니다", w.Latest.UTC().Format(time.RFC3339))
	}
	var base *store.DBPITRObject
	for _, o := range objs {
		if o.ObjectType == "base" && !o.SourceTime.After(at) && (base == nil || o.SourceTime.After(base.SourceTime)) {
			base = o
		}
	}
	if base == nil {
		return nil, fmt.Errorf("%s 이전의 기준 백업이 없습니다", at.UTC().Format(time.RFC3339))
	}
	var logs []*store.DBPITRObject
	for _, o := range objs {
		if o.ObjectType == "base" || o.Epoch != base.Epoch {
			continue
		}
		if kind == "mysql" {
			if binlogLess(o.FileName, base.BaseFile) {
				continue
			}
		} else if o.SourceTime.Before(base.StartedAt) {
			continue
		}
		logs = append(logs, o)
	}
	if kind == "mysql" {
		sort.Slice(logs, func(i, j int) bool { return binlogLess(logs[i].FileName, logs[j].FileName) })
	} else {
		sort.Slice(logs, func(i, j int) bool { return logs[i].FileName < logs[j].FileName })
	}
	// 목표 시각 뒤에 보관된 로그가 있어야 그 시각까지의 변경이 모두 들어 있다.
	covered := false
	for _, o := range logs {
		covered = covered || !o.SourceTime.Before(at)
	}
	if !covered {
		return nil, fmt.Errorf("%s 시점의 로그가 아직 보관되지 않았습니다", at.UTC().Format(time.RFC3339))
	}
	p := &pitrPlan{Base: base}
	if kind == "mysql" {
		if len(logs) == 0 || logs[0].FileName != base.BaseFile {
			return nil, fmt.Errorf("보관소에 binlog %s가 없습니다(기준 백업 %s에 필요)", base.BaseFile, base.FileName)
		}
		for i, o := range logs {
			if i > 0 {
				pb, pn, _ := binlogSeq(logs[i-1].FileName)
				cb, cn, ok := binlogSeq(o.FileName)
				if !ok || cb != pb || cn != pn+1 {
					return nil, fmt.Errorf("보관소에서 %s와 %s 사이의 binlog가 빠져 있습니다", logs[i-1].FileName, o.FileName)
				}
			}
			p.Logs = append(p.Logs, o)
			if !o.SourceTime.Before(at) {
				break // 목표 시각을 덮는 첫 binlog까지
			}
		}
		return p, nil
	}
	limit := at.Add(pitrLogSlack)
	var last *store.DBPITRObject // 목표 시각 뒤에 보관된 첫 세그먼트(커밋 레코드가 있어야 recovery가 멈춘다)
	for _, o := range logs {
		if !o.SourceTime.After(limit) || strings.HasSuffix(o.FileName, ".history") {
			p.Logs = append(p.Logs, o)
		} else if last == nil || o.SourceTime.Before(last.SourceTime) {
			last = o
		}
	}
	if last != nil {
		p.Logs = append(p.Logs, last)
		sort.Slice(p.Logs, func(i, j int) bool { return p.Logs[i].FileName < p.Logs[j].FileName })
	}
	return p, nil
}

// planPITRPrune은 cutoff 이후 아무 시점으로나 복원하는 데 필요 없는 파일을 고른다.
func planPITRPrune(kind string, objs []*store.DBPITRObject, cutoff time.Time) []*store.DBPITRObject {
	var keep *store.DBPITRObject // cutoff 이전의 가장 새 기준 백업(이것부터는 남긴다)
	for _, o := range objs {
		if o.ObjectType == "base" && !o.SourceTime.After(cutoff) && (keep == nil || o.SourceTime.After(keep.SourceTime)) {
			keep = o
		}
	}
	if keep == nil {
		return nil
	}
	var out []*store.DBPITRObject
	for _, o := range objs {
		if o.ID == keep.ID {
			continue
		}
		switch {
		case o.ObjectType == "base":
			if o.SourceTime.Before(keep.SourceTime) {
				out = append(out, o)
			}
		case o.Epoch < keep.Epoch:
			out = append(out, o)
		case o.Epoch == keep.Epoch:
			if kind == "mysql" && binlogLess(o.FileName, keep.BaseFile) || kind != "mysql" && o.SourceTime.Before(keep.StartedAt) {
				out = append(out, o)
			}
		}
	}
	return out
}

// ---------------- 대상 ----------------

type pitrTarget struct {
	*dbTarget
	in        *store.Instance
	vol       *store.Volume // postgres: PGDATA가 들어 있는 데이터 볼륨
	dataDir   string        // postgres PGDATA
	container string
}

func (m *Manager) pitrTargetFor(ctx context.Context, in *store.Instance) (*pitrTarget, error) {
	dt, err := m.dbTargetFor(ctx, in)
	if err != nil {
		return nil, err
	}
	if !agentprotocol.PITRSupported(dt.kind) {
		return nil, ErrPITRNotSupported
	}
	pt := &pitrTarget{dbTarget: dt, in: in, container: dbContainer(dt.cur, dt.node)}
	if dt.kind == "postgres" {
		pt.dataDir = path.Clean(first(dt.vars, "PGDATA"))
		if pt.dataDir == "." {
			pt.dataDir = "/var/lib/postgresql/data"
		}
		vols, err := m.Store.ListVolumesByInstance(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		for _, v := range vols {
			if agentprotocol.PITRDataDirOK(v.MountPath, pt.dataDir) && (pt.vol == nil || len(v.MountPath) > len(pt.vol.MountPath)) {
				pt.vol = v
			}
		}
		if pt.vol == nil {
			return nil, fmt.Errorf("PGDATA(%s)가 볼륨의 하위 디렉터리여야 합니다(예: 볼륨 /var/lib/postgresql/data, PGDATA=/var/lib/postgresql/data/pgdata)", pt.dataDir)
		}
	}
	return pt, nil
}

func (pt *pitrTarget) archiveDir() string {
	return pt.vol.MountPath + "/" + agentprotocol.PITRArchiveDir
}

// exec는 DB 컨테이너 안에서 셸 스크립트를 실행하고 stdout을 돌려준다.
func (m *Manager) pitrExec(ctx context.Context, pt *pitrTarget, script string, env map[string]string) (string, error) {
	if pt.container == "" {
		return "", errors.New("database has no running container (deploy it first)")
	}
	cr := dbCreds(pt.kind, pt.vars)
	var e []string
	switch pt.kind {
	case "postgres":
		e = []string{"PGHOST=127.0.0.1", fmt.Sprintf("PGPORT=%d", pt.port), "PGUSER=" + cr.User, "PGDATABASE=postgres", "PGPASSWORD=" + cr.Password,
			"PGCONNECT_TIMEOUT=10", "PSQLRC=/dev/null"}
	case "mysql":
		e = []string{"DBUSER=" + cr.User, fmt.Sprintf("DBPORT=%d", pt.port), "MYSQL_PWD=" + cr.Password}
	}
	for k, v := range env {
		e = append(e, k+"="+v)
	}
	sort.Strings(e)
	out, err := m.ExecRun(ctx, pt.node, pt.container, node.ExecRunRequest{Cmd: []string{"sh", "-c", script}, Env: e, MaxOutput: 1 << 20}, 90*time.Second)
	if err != nil {
		return "", err
	}
	if out.ExitCode != 0 {
		msg := strings.TrimSpace(string(out.Stderr))
		if msg == "" {
			msg = strings.TrimSpace(string(out.Stdout))
		}
		if len(msg) > 500 {
			msg = msg[:500] + "…"
		}
		return "", fmt.Errorf("exit %d: %s", out.ExitCode, msg)
	}
	return string(out.Stdout), nil
}

// pgSetupScript는 archive 설정과 replication 접속 허용을 넣는다(여러 번 해도 같다). 출력: 현재 archive_mode.
const pgSetupScript = `set -e
mkdir -p "$PITR_DIR"
chown postgres "$PITR_DIR" "$(dirname "$PITR_DIR")" 2>/dev/null || true
H="$PGDATA/pg_hba.conf"
if [ -f "$H" ] && ! grep -q 'keel-pitr' "$H"; then
  m=$(grep -E '^[[:space:]]*host[[:space:]]+all[[:space:]]+all[[:space:]]+all[[:space:]]' "$H" | tail -n 1 | sed -E 's/.*[[:space:]]([a-z0-9-]+)[[:space:]]*$/\1/')
  [ -n "$m" ] || m=scram-sha-256
  printf '\n# keel-pitr: base backups over the environment network\nhost replication all all %s\n' "$m" >> "$H"
fi
psql -X -q -v ON_ERROR_STOP=1 <<SQL
ALTER SYSTEM SET wal_level = 'replica';
ALTER SYSTEM SET archive_mode = 'on';
ALTER SYSTEM SET archive_timeout = '60';
ALTER SYSTEM SET archive_command = 'mkdir -p $PITR_DIR && cp %p $PITR_DIR/%f.tmp && mv -f $PITR_DIR/%f.tmp $PITR_DIR/%f';
SELECT pg_reload_conf();
SQL
psql -X -tA -c 'SHOW archive_mode'`

// pgTeardownScript는 archive를 끈다(archive_mode는 다음 재시작에 꺼지고, 그때까지 archive_command는 아무것도 하지 않는다).
const pgTeardownScript = `set -e
psql -X -q -v ON_ERROR_STOP=1 <<SQL
ALTER SYSTEM RESET archive_mode;
ALTER SYSTEM RESET archive_timeout;
ALTER SYSTEM SET archive_command = '/bin/true';
SELECT pg_reload_conf();
SQL
rm -rf "$PITR_DIR"`

// pgTickScript는 활동이 있었으면 커밋 하나와 WAL 전환을 한다. 출력: "<lsn> <대기 파일 수> <archive_mode> <archiver 실패 여부>".
const pgTickScript = `set -e
q() { psql -X -q -tA -v ON_ERROR_STOP=1 -c "$1"; }
cur=$(q 'SELECT pg_current_wal_lsn()')
if [ "$cur" != "$PITR_LSN" ]; then
  q 'SELECT txid_current()' >/dev/null
  q 'SELECT pg_switch_wal()' >/dev/null
  sleep 2
  cur=$(q 'SELECT pg_current_wal_lsn()')
fi
n=$(ls -1 "$PITR_DIR" 2>/dev/null | grep -cE '^[0-9A-F]{8}([0-9A-F]{16}|\.history)' || true)
st=$(q "SELECT current_setting('archive_mode') || ' ' || COALESCE((last_failed_time > COALESCE(last_archived_time, 'epoch'::timestamptz))::text, 'false') FROM pg_stat_archiver")
echo "$cur $n $st"`

// mysqlTickScript는 활성 binlog가 바뀌었으면 닫고 binlog 목록을 출력한다(Log_name File_size ...).
const mysqlTickScript = `set -e
MYSQL=$(command -v mysql || command -v mariadb) || { echo "mysql client not found in image" >&2; exit 127; }
q() { "$MYSQL" --protocol=TCP -h 127.0.0.1 -P "$DBPORT" -u "$DBUSER" -N -B -e "$1"; }
[ "$(q 'SELECT @@log_bin')" = 1 ] || { echo "binary logging is off (log_bin=0); remove --skip-log-bin / --disable-log-bin" >&2; exit 3; }
set -- $(q 'SHOW BINARY LOGS' | tail -n 1)
if [ -n "$PITR_ROTATE" ] && [ "$1 $2" != "$PITR_ACTIVE" ]; then q 'FLUSH BINARY LOGS'; fi
q 'SHOW BINARY LOGS'`

type binlogInfo struct {
	Name string
	Size int64
}

// parseBinlogList는 SHOW BINARY LOGS(-N -B) 출력이다.
func parseBinlogList(out string) []binlogInfo {
	var res []binlogInfo
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 2 || !agentprotocol.PITRFileRe.MatchString(f[0]) {
			continue
		}
		n, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			continue
		}
		res = append(res, binlogInfo{f[0], n})
	}
	return res
}

// ---------------- 켜기 / 끄기 ----------------

// updatePITRConfig는 설정을 다시 읽어 f로 바꾸고 저장한다(주기 작업과 API가 같이 바꾼다).
func (m *Manager) updatePITRConfig(ctx context.Context, id string, f func(c *store.DBPITRConfig)) (*store.DBPITRConfig, error) {
	pitrJobs.cfg.Lock()
	defer pitrJobs.cfg.Unlock()
	c, err := m.Store.GetDBPITRConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	f(c)
	return c, m.Store.SetDBPITRConfig(ctx, id, c)
}

// ConfigurePITR은 시점 복구를 켜거나(설정 바꾸기 포함) 끈다. Postgres를 처음 켤 때 archive_mode를 바꾸려고 재시작할 수 있다.
func (m *Manager) ConfigurePITR(ctx context.Context, in *store.Instance, enabled bool, retentionDays int, destID string) (*store.DBPITRConfig, error) {
	if retentionDays < 1 || retentionDays > 90 {
		return nil, errors.New("retention_days must be 1..90")
	}
	if _, err := m.BackupStorageFor(ctx, destID); err != nil {
		return nil, err
	}
	cur, err := m.Store.GetDBPITRConfig(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if !enabled {
		if cur.Enabled {
			if pt, err := m.pitrTargetFor(ctx, in); err == nil && pt.kind == "postgres" && pt.container != "" {
				if _, err := m.pitrExec(ctx, pt, pgTeardownScript, map[string]string{"PITR_DIR": pt.archiveDir()}); err != nil {
					m.Log.Warn("pitr teardown", "instance", in.ID, "err", err)
				}
			}
		}
		return m.updatePITRConfig(ctx, in.ID, func(c *store.DBPITRConfig) {
			c.Enabled, c.RetentionDays, c.DestinationID = false, retentionDays, destID
		})
	}
	pt, err := m.pitrTargetFor(ctx, in)
	if err != nil {
		return nil, err
	}
	if !liveStatus(in.Status) || in.Status == store.InstDeploying {
		return nil, fmt.Errorf("database is %s; it must be online to enable point-in-time recovery", in.Status)
	}
	restart := false
	switch pt.kind {
	case "postgres":
		out, err := m.pitrExec(ctx, pt, pgSetupScript, map[string]string{"PITR_DIR": pt.archiveDir()})
		if err != nil {
			return nil, fmt.Errorf("WAL 보관 설정 실패: %w", err)
		}
		restart = strings.TrimSpace(out) != "on"
	case "mysql":
		if _, err := m.pitrExec(ctx, pt, mysqlTickScript, nil); err != nil {
			return nil, fmt.Errorf("binlog 확인 실패: %w", err)
		}
	}
	now := time.Now().UTC()
	c, err := m.updatePITRConfig(ctx, in.ID, func(c *store.DBPITRConfig) {
		if !c.Enabled {
			c.EnabledAt, c.LastBaseAt, c.LastBaseTry, c.LastError, c.PGLastLSN, c.MySQLActive = &now, nil, nil, "", "", ""
		}
		if c.DestinationID != destID {
			c.LastBaseAt = nil // 새 저장소에도 기준 백업이 있어야 한다
		}
		c.Enabled, c.RetentionDays, c.DestinationID = true, retentionDays, destID
	})
	if err != nil {
		return nil, err
	}
	if restart {
		m.emitDB(ctx, pt.dbTarget, "pitr.restart", fmt.Sprintf("%s: WAL 보관(archive_mode)을 켜려고 데이터베이스를 재시작합니다", pt.t.Service.Name))
		if err := m.Restart(ctx, in); err != nil {
			return c, fmt.Errorf("재시작 실패(archive_mode는 다음 재시작에 켜집니다): %w", err)
		}
	}
	if !cur.Enabled {
		m.emitDB(ctx, pt.dbTarget, "pitr.enabled", fmt.Sprintf("%s: 시점 복구를 켰습니다(보관 %d일)", pt.t.Service.Name, retentionDays))
	}
	return c, nil
}

// ---------------- 주기 작업 ----------------

// pitrTick은 매 분 켜진 인스턴스의 로그를 보관하고, 하루가 지났으면 기준 백업을 만든다.
func (m *Manager) pitrTick(ctx context.Context, now time.Time) {
	cfgs, err := m.Store.ListEnabledDBPITRConfigs(ctx)
	if err != nil {
		return
	}
	for id := range cfgs {
		go func(id string) {
			if err := m.pitrSyncInstance(context.Background(), id, now); err != nil && !errors.Is(err, ErrPITRBusy) {
				m.Log.Warn("pitr sync", "instance", id, "err", err)
			}
		}(id)
	}
}

// pitrSyncInstance는 인스턴스 하나의 로그 보관, 기준 백업 시작, 보관 기간 정리를 한다.
func (m *Manager) pitrSyncInstance(ctx context.Context, id string, now time.Time) error {
	if !pitrLock(id, "sync") {
		return ErrPITRBusy
	}
	defer pitrUnlock(id, "sync")
	in, err := m.Store.GetInstance(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return m.Store.DeleteDBPITRConfig(ctx, id) // 서비스를 지웠다
	}
	if err != nil {
		return err
	}
	cfg, err := m.Store.GetDBPITRConfig(ctx, id)
	if err != nil || !cfg.Enabled {
		return err
	}
	if in.Status != store.InstOnline {
		return nil // 멈췄거나 배포 중: 다음 주기에
	}
	pt, err := m.pitrTargetFor(ctx, in)
	if err != nil {
		m.updatePITRConfig(ctx, id, func(c *store.DBPITRConfig) { c.LastError = err.Error() })
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	switch pt.kind {
	case "postgres":
		err = m.pitrSyncPostgres(ctx, pt, cfg)
	case "mysql":
		err = m.pitrSyncMySQL(ctx, pt, cfg)
	}
	syncAt := time.Now().UTC()
	m.updatePITRConfig(ctx, id, func(c *store.DBPITRConfig) {
		if err != nil {
			c.LastError = err.Error()
		} else {
			c.LastSyncAt = &syncAt
			if c.LastError != "" && !strings.HasPrefix(c.LastError, "기준 백업") {
				c.LastError = ""
			}
		}
	})
	if err != nil {
		return err
	}
	cfg, _ = m.Store.GetDBPITRConfig(ctx, id)
	if cfg != nil && cfg.Enabled && (cfg.LastBaseAt == nil || now.Sub(*cfg.LastBaseAt) >= pitrBaseEvery) &&
		(cfg.LastBaseTry == nil || now.Sub(*cfg.LastBaseTry) >= pitrBaseBackoff) {
		if err := m.StartPITRBase(context.Background(), in); err != nil && !errors.Is(err, ErrPITRBusy) {
			m.Log.Warn("pitr base", "instance", id, "err", err)
		}
	}
	m.prunePITR(ctx, id, pt.kind, cfg)
	return nil
}

// pitrKey는 저장소 안 경로다.
func pitrKey(pt *pitrTarget, epoch int, typ, name string) string {
	return fmt.Sprintf("pitr/%s-%s-%s/e%d/%s/%s", pt.t.Service.Name, pt.t.Env.Name, strings.TrimPrefix(pt.in.ID, "ins_"), epoch, typ, name)
}

// pitrJob은 helper 컨테이너 이름 접미사다.
func pitrJob() string { return strings.ToLower(strings.TrimPrefix(store.NewID("p"), "p_")) }

// pitrRequest는 helper에 보낼 요청(원격 노드)이다.
func (pt *pitrTarget) request(action, job string) (agentprotocol.DBToolRequest, error) {
	req, err := pt.toolRequest(job, nil)
	if err != nil {
		return req, err
	}
	req.Action = action
	return req, nil
}

// pitrRunOut은 helper 도구를 실행해 stdout을 w로 흘린다(로컬은 같은 스크립트 표로 직접, 원격은 agent의 db.backup).
func (m *Manager) pitrRunOut(ctx context.Context, pt *pitrTarget, req agentprotocol.DBToolRequest, tool *dbTool, w io.Writer) error {
	tail := &tailBuf{}
	if rd, ok := pt.drv.(*remoteDriver); ok {
		return rd.dbBackup(ctx, req, w)
	}
	spec := m.toolSpec(pt.dbTarget, "keel-dbb-"+req.Job, tool)
	if req.Volume != "" {
		spec.Env["DATADIR"] = req.MountPath
		spec.Mounts = []node.Mount{{Volume: req.Volume, Path: req.MountPath, ReadOnly: req.Action == agentprotocol.PITRWAL}}
	}
	code, err := m.Docker.RunStream(ctx, spec, nil, w, tail.add)
	if err == nil && code != 0 {
		err = fmt.Errorf("%s exited with code %d: %s", req.Action, code, tail.String())
	}
	return err
}

// pitrRunIn은 복원 helper를 stdin=in으로 실행한다.
func (m *Manager) pitrRunIn(ctx context.Context, pt *pitrTarget, req agentprotocol.DBToolRequest, tool *dbTool, in io.Reader) error {
	if rd, ok := pt.drv.(*remoteDriver); ok {
		return rd.dbRestore(ctx, req, in)
	}
	tail := &tailBuf{}
	spec := m.toolSpec(pt.dbTarget, "keel-dbr-"+req.Job, tool)
	if req.Volume != "" {
		spec.Env["DATADIR"] = req.MountPath
		spec.Mounts = []node.Mount{{Volume: req.Volume, Path: req.MountPath}}
	}
	code, err := m.Docker.RunStream(ctx, spec, in, nil, tail.add)
	if err == nil && code != 0 {
		err = fmt.Errorf("restore exited with code %d: %s", code, tail.String())
	}
	return err
}

// pitrCollect는 helper가 내보낸 프레임을 gzip으로 저장소에 올리고 기록한다. 올린 파일 이름을 돌려준다.
func (m *Manager) pitrCollect(ctx context.Context, pt *pitrTarget, cfg *store.DBPITRConfig, typ string, req agentprotocol.DBToolRequest, tool *dbTool) ([]string, error) {
	st, err := m.BackupStorageFor(ctx, cfg.DestinationID)
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := m.pitrRunOut(ctx, pt, req, tool, pw)
		pw.CloseWithError(err)
		done <- err
	}()
	var names []string
	err = agentprotocol.ReadPITRFrames(pr, func(fr agentprotocol.PITRFrame, body io.Reader) error {
		key := pitrKey(pt, cfg.Epoch, typ, fr.Name+".gz")
		gr, gw := io.Pipe()
		go func() {
			gz := gzip.NewWriter(gw)
			n, err := io.Copy(gz, body)
			if err == nil && n != fr.Size {
				err = fmt.Errorf("%s is truncated (%d of %d bytes)", fr.Name, n, fr.Size) // 저장소 Put도 실패한다(반쪽 파일을 기록하지 않는다)
			}
			if err == nil {
				err = gz.Close()
			}
			gw.CloseWithError(err)
		}()
		n, err := st.Put(ctx, key, gr)
		gr.CloseWithError(err)
		if err != nil {
			return fmt.Errorf("upload %s: %w", fr.Name, err)
		}
		now := time.Now().UTC()
		if err := m.Store.UpsertDBPITRObject(ctx, &store.DBPITRObject{InstanceID: pt.in.ID, Kind: pt.kind, ObjectType: typ, Epoch: cfg.Epoch,
			FileName: fr.Name, DestinationID: cfg.DestinationID, ObjectKey: key, SizeBytes: n, SourceTime: now}); err != nil {
			return err
		}
		names = append(names, fr.Name)
		return nil
	})
	pr.CloseWithError(errors.New("stopped reading"))
	herr := <-done
	if err == nil && herr != nil {
		err = herr
	}
	return names, err
}

func (m *Manager) pitrSyncPostgres(ctx context.Context, pt *pitrTarget, cfg *store.DBPITRConfig) error {
	out, err := m.pitrExec(ctx, pt, pgTickScript, map[string]string{"PITR_DIR": pt.archiveDir(), "PITR_LSN": cfg.PGLastLSN})
	if err != nil {
		return fmt.Errorf("WAL 전환 실패: %w", err)
	}
	f := strings.Fields(out)
	if len(f) < 4 {
		return fmt.Errorf("unexpected WAL status %q", strings.TrimSpace(out))
	}
	m.updatePITRConfig(ctx, pt.in.ID, func(c *store.DBPITRConfig) { c.PGLastLSN = f[0] })
	if f[2] != "on" {
		return errors.New("archive_mode가 아직 꺼져 있습니다. 데이터베이스를 재시작하세요")
	}
	for i := 0; i < 8; i++ { // 밀린 파일이 많으면 helper를 몇 번 더 돌린다
		if n, _ := strconv.Atoi(f[1]); n == 0 {
			break
		}
		req, err := pt.request(agentprotocol.PITRWAL, pitrJob())
		if err != nil {
			return err
		}
		req.Volume, req.MountPath = pt.vol.DockerName, pt.vol.MountPath
		tool, err := agentprotocol.DBPITRWALTool(pt.vol.MountPath)
		if err != nil {
			return err
		}
		names, err := m.pitrCollect(ctx, pt, cfg, "wal", req, tool)
		if len(names) > 0 {
			// 올린 파일만 볼륨에서 지운다(이름은 PGWALFileRe로 다시 확인한다).
			var rm []string
			for _, n := range names {
				if agentprotocol.PGWALFileRe.MatchString(n) {
					rm = append(rm, n)
				}
			}
			if _, derr := m.pitrExec(ctx, pt, `cd "$PITR_DIR" && rm -f -- $PITR_FILES`, map[string]string{"PITR_DIR": pt.archiveDir(), "PITR_FILES": strings.Join(rm, " ")}); derr != nil && err == nil {
				err = fmt.Errorf("보관한 WAL 정리 실패: %w", derr)
			}
		}
		if err != nil {
			return err
		}
		if len(names) < agentprotocol.PITRMaxFiles {
			break
		}
	}
	if f[3] == "true" {
		return errors.New("archive_command가 실패하고 있습니다(데이터 볼륨의 keel-pitr 디렉터리 권한/공간을 확인하세요)")
	}
	return nil
}

func (m *Manager) pitrSyncMySQL(ctx context.Context, pt *pitrTarget, cfg *store.DBPITRConfig) error {
	objs, err := m.Store.ListDBPITRObjects(ctx, pt.in.ID)
	if err != nil {
		return err
	}
	var base *store.DBPITRObject // 이 epoch의 가장 이른 기준 백업
	archived := map[string]bool{}
	for _, o := range objs {
		if o.Epoch != cfg.Epoch {
			continue
		}
		if o.ObjectType == "base" && (base == nil || binlogLess(o.BaseFile, base.BaseFile)) {
			base = o
		} else if o.ObjectType == "binlog" {
			archived[o.FileName] = true
		}
	}
	env := map[string]string{"PITR_ACTIVE": cfg.MySQLActive}
	if base != nil {
		env["PITR_ROTATE"] = "1" // 기준 백업 전에는 닫을 필요가 없다
	}
	out, err := m.pitrExec(ctx, pt, mysqlTickScript, env)
	if err != nil {
		return fmt.Errorf("binlog 확인 실패: %w", err)
	}
	logs := parseBinlogList(out)
	if len(logs) == 0 {
		return errors.New("binlog가 없습니다")
	}
	active := logs[len(logs)-1]
	m.updatePITRConfig(ctx, pt.in.ID, func(c *store.DBPITRConfig) { c.MySQLActive = fmt.Sprintf("%s %d", active.Name, active.Size) })
	if base == nil {
		return nil
	}
	var todo []string
	purgeTo := active.Name // 이 앞의 binlog는 보관했거나 기준 백업 이전이다
	for _, l := range logs[:len(logs)-1] {
		if binlogLess(l.Name, base.BaseFile) || archived[l.Name] {
			continue
		}
		if len(todo) < agentprotocol.PITRMaxFiles {
			todo = append(todo, l.Name)
		} else if binlogLess(l.Name, purgeTo) {
			purgeTo = l.Name
		}
	}
	if len(todo) > 0 {
		req, err := pt.request(agentprotocol.PITRBinlog, pitrJob())
		if err != nil {
			return err
		}
		req.Files = todo
		cr := dbCreds(pt.kind, pt.vars)
		tool, err := agentprotocol.DBPITRBinlogTool(pt.host, pt.port, cr, todo)
		if err != nil {
			return err
		}
		names, err := m.pitrCollect(ctx, pt, cfg, "binlog", req, tool)
		got := map[string]bool{}
		for _, n := range names {
			got[n] = true
		}
		for _, n := range todo {
			if !got[n] && binlogLess(n, purgeTo) {
				purgeTo = n
			}
		}
		if err != nil {
			return err
		}
	}
	// 보관을 확인한 binlog만 지운다(PURGE BINARY LOGS TO는 그 앞의 파일을 지운다).
	if purgeTo != logs[0].Name {
		if _, err := m.pitrExec(ctx, pt, `MYSQL=$(command -v mysql || command -v mariadb); "$MYSQL" --protocol=TCP -h 127.0.0.1 -P "$DBPORT" -u "$DBUSER" -e "PURGE BINARY LOGS TO '$PITR_TO'"`,
			map[string]string{"PITR_TO": purgeTo}); err != nil {
			return fmt.Errorf("보관한 binlog 정리 실패: %w", err)
		}
	}
	return nil
}

// headSniffer는 스트림 앞부분(최대 1MiB)을 모은다(mysqldump의 binlog 위치 주석).
type headSniffer struct {
	w    io.Writer
	head []byte
}

func (h *headSniffer) Write(p []byte) (int, error) {
	if room := 1<<20 - len(h.head); room > 0 {
		h.head = append(h.head, p[:min(room, len(p))]...)
	}
	return h.w.Write(p)
}

// StartPITRBase는 기준 백업을 시작한다(비동기).
func (m *Manager) StartPITRBase(ctx context.Context, in *store.Instance) error {
	cfg, err := m.Store.GetDBPITRConfig(ctx, in.ID)
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		return errors.New("point-in-time recovery is off")
	}
	pt, err := m.pitrTargetFor(ctx, in)
	if err != nil {
		return err
	}
	if in.Status != store.InstOnline {
		return fmt.Errorf("database is %s; it must be online", in.Status)
	}
	if !pitrLock(in.ID, "base") {
		return ErrPITRBusy
	}
	now := time.Now().UTC()
	m.updatePITRConfig(ctx, in.ID, func(c *store.DBPITRConfig) { c.LastBaseTry = &now })
	go func() {
		defer pitrUnlock(in.ID, "base")
		bg, cancel := context.WithTimeout(context.Background(), 12*time.Hour)
		defer cancel()
		o, err := m.runPITRBase(bg, pt, cfg)
		if err != nil {
			m.updatePITRConfig(bg, in.ID, func(c *store.DBPITRConfig) { c.LastError = "기준 백업 실패: " + err.Error() })
			m.emitDB(bg, pt.dbTarget, "pitr.base_failed", fmt.Sprintf("%s: 시점 복구 기준 백업 실패: %v", pt.t.Service.Name, err))
			return
		}
		m.updatePITRConfig(bg, in.ID, func(c *store.DBPITRConfig) {
			t := o.SourceTime
			c.LastBaseAt = &t
			if strings.HasPrefix(c.LastError, "기준 백업") {
				c.LastError = ""
			}
		})
		m.emitDB(bg, pt.dbTarget, "pitr.base_done", fmt.Sprintf("%s: 시점 복구 기준 백업 완료 (%s)", pt.t.Service.Name, HumanBytes(o.SizeBytes)))
	}()
	return nil
}

func (m *Manager) runPITRBase(ctx context.Context, pt *pitrTarget, cfg *store.DBPITRConfig) (*store.DBPITRObject, error) {
	if pt.kind == "postgres" {
		// pg_hba replication 줄과 archive 설정이 그대로인지 다시 확인한다(사용자가 바꿨을 수 있다).
		if _, err := m.pitrExec(ctx, pt, pgSetupScript, map[string]string{"PITR_DIR": pt.archiveDir()}); err != nil {
			return nil, err
		}
	}
	st, err := m.BackupStorageFor(ctx, cfg.DestinationID)
	if err != nil {
		return nil, err
	}
	cr := dbCreds(pt.kind, pt.vars)
	tool, err := agentprotocol.DBPITRBaseTool(pt.kind, pt.host, pt.port, cr)
	if err != nil {
		return nil, err
	}
	req, err := pt.request(agentprotocol.PITRBase, pitrJob())
	if err != nil {
		return nil, err
	}
	started := time.Now().UTC()
	name := started.Format("20060102T150405Z") + "." + tool.Ext
	key := pitrKey(pt, cfg.Epoch, "base", name)
	pr, pw := io.Pipe()
	sniff := &headSniffer{}
	go func() {
		var w io.Writer = pw
		var gz *gzip.Writer
		if tool.Gzip {
			gz = gzip.NewWriter(pw)
			w = gz
		}
		sniff.w = w
		err := m.pitrRunOut(ctx, pt, req, tool, sniff)
		if err == nil && gz != nil {
			err = gz.Close()
		}
		pw.CloseWithError(err)
	}()
	size, err := st.Put(ctx, key, pr)
	pr.CloseWithError(err)
	if err == nil && size == 0 {
		err = errors.New("base backup is empty")
	}
	o := &store.DBPITRObject{InstanceID: pt.in.ID, Kind: pt.kind, ObjectType: "base", Epoch: cfg.Epoch, FileName: name,
		DestinationID: cfg.DestinationID, ObjectKey: key, SizeBytes: size, StartedAt: started, SourceTime: time.Now().UTC()}
	if err == nil && pt.kind == "mysql" {
		f, pos, ok := agentprotocol.ParseBinlogPosition(sniff.head)
		if !ok {
			err = errors.New("binlog position is missing from the dump (is binary logging on?)")
		}
		o.BaseFile, o.BasePosition = f, pos
	}
	if err != nil {
		st.Delete(context.WithoutCancel(ctx), key)
		return nil, err
	}
	return o, m.Store.UpsertDBPITRObject(ctx, o)
}

// prunePITR은 보관 기간보다 오래된(복원에 더는 필요 없는) 파일을 지운다.
func (m *Manager) prunePITR(ctx context.Context, id, kind string, cfg *store.DBPITRConfig) {
	if cfg == nil {
		return
	}
	objs, err := m.Store.ListDBPITRObjects(ctx, id)
	if err != nil {
		return
	}
	cutoff := time.Now().UTC().Add(-time.Duration(cfg.RetentionDays) * 24 * time.Hour)
	for _, o := range planPITRPrune(kind, objs, cutoff) {
		m.deletePITRObject(ctx, o)
	}
}

func (m *Manager) deletePITRObject(ctx context.Context, o *store.DBPITRObject) error {
	st, err := m.BackupStorageFor(ctx, o.DestinationID)
	if err != nil {
		return err
	}
	if err := st.Delete(ctx, o.ObjectKey); err != nil {
		m.Log.Warn("pitr delete", "object", o.ID, "err", err)
		return err
	}
	return m.Store.DeleteDBPITRObject(ctx, o.ID)
}

// DeletePITRArchive는 인스턴스의 보관 파일을 모두 지운다(시점 복구가 꺼져 있어야 한다).
func (m *Manager) DeletePITRArchive(ctx context.Context, in *store.Instance) (int, error) {
	cfg, err := m.Store.GetDBPITRConfig(ctx, in.ID)
	if err != nil {
		return 0, err
	}
	if cfg.Enabled {
		return 0, errors.New("disable point-in-time recovery first")
	}
	if len(pitrRunning(in.ID)) > 0 {
		return 0, ErrPITRBusy
	}
	objs, err := m.Store.ListDBPITRObjects(ctx, in.ID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, o := range objs {
		if err := m.deletePITRObject(ctx, o); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ---------------- 복원 ----------------

// PITRRestoreOpts는 복원 방식이다.
type PITRRestoreOpts struct {
	InPlace bool // 원본 서비스에 덮어쓴다(아니면 target은 새로 만든 서비스)
	// keepArchive: 같은 서비스에 복원하고 시점 복구가 켜져 있으면 복원한 DB가 계속 WAL을 보관한다.
	keepArchive bool
}

// SyncPITRNow는 원본이 실행 중이면 지금까지의 로그를 바로 보관한다(최근 시각으로 복원하기 전).
func (m *Manager) SyncPITRNow(ctx context.Context, in *store.Instance) error {
	for i := 0; i < 60; i++ {
		err := m.pitrSyncInstance(ctx, in.ID, time.Now().UTC())
		if !errors.Is(err, ErrPITRBusy) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return ErrPITRBusy
}

// CheckPITRTarget은 at으로 복원할 수 있는지 미리 확인한다.
func (m *Manager) CheckPITRTarget(ctx context.Context, src *store.Instance, kind string, at time.Time) error {
	objs, err := m.Store.ListDBPITRObjects(ctx, src.ID)
	if err != nil {
		return err
	}
	_, err = planPITR(kind, objs, at)
	return err
}

// RestorePITR은 src의 보관소로 target을 at 시점의 데이터로 만든다(비동기). target은 새로 만든 서비스면 첫 배포가 끝나기를 기다린다.
func (m *Manager) RestorePITR(ctx context.Context, src, target *store.Instance, kind string, at time.Time, o PITRRestoreOpts) (*PITRRestoreStatus, error) {
	if !agentprotocol.PITRSupported(kind) {
		return nil, ErrPITRNotSupported
	}
	if err := m.CheckPITRTarget(ctx, src, kind, at); err != nil {
		return nil, err
	}
	if !pitrLock(src.ID, "restore") {
		return nil, ErrPITRBusy
	}
	rs := &PITRRestoreStatus{Status: "running", Step: "준비", TargetTime: at.UTC(), TargetServiceID: target.ServiceID, TargetInstance: target.ID,
		InPlace: o.InPlace, StartedAt: time.Now().UTC()}
	pitrJobs.Lock()
	pitrJobs.restores[src.ID] = rs
	pitrJobs.Unlock()
	go func() {
		defer pitrUnlock(src.ID, "restore")
		bg, cancel := context.WithTimeout(context.Background(), 12*time.Hour)
		defer cancel()
		err := m.runPITRRestore(bg, src, target, kind, at, o)
		now := time.Now().UTC()
		setPITRRestore(src.ID, func(r *PITRRestoreStatus) {
			r.FinishedAt = &now
			if err != nil {
				r.Status, r.Error = "failed", err.Error()
			} else {
				r.Status, r.Step = "done", "완료"
			}
		})
		if dt, derr := m.dbTargetFor(bg, target); derr == nil {
			if err != nil {
				m.emitDB(bg, dt, "pitr.restore_failed", fmt.Sprintf("%s: 시점 복원 실패: %v", dt.t.Service.Name, err))
			} else {
				m.emitDB(bg, dt, "pitr.restored", fmt.Sprintf("%s: %s 시점으로 복원했습니다", dt.t.Service.Name, at.UTC().Format(time.RFC3339)))
			}
		}
	}()
	c := *rs
	return &c, nil
}

func (m *Manager) runPITRRestore(ctx context.Context, src, target *store.Instance, kind string, at time.Time, o PITRRestoreOpts) error {
	step := func(s string) { setPITRRestore(src.ID, func(r *PITRRestoreStatus) { r.Step = s }) }
	if cur, err := m.Store.GetInstance(ctx, src.ID); err == nil && cur.Status == store.InstOnline {
		step("최근 로그 보관")
		if err := m.SyncPITRNow(ctx, cur); err != nil {
			m.Log.Warn("pitr restore: sync source", "instance", src.ID, "err", err)
		}
	}
	objs, err := m.Store.ListDBPITRObjects(ctx, src.ID)
	if err != nil {
		return err
	}
	plan, err := planPITR(kind, objs, at)
	if err != nil {
		return err
	}
	if !o.InPlace {
		step("새 서비스 배포 대기")
		if err := m.waitOnline(ctx, target.ID, 20*time.Minute); err != nil {
			return err
		}
	}
	in, err := m.Store.GetInstance(ctx, target.ID)
	if err != nil {
		return err
	}
	pt, err := m.pitrTargetFor(ctx, in)
	if err != nil {
		return err
	}
	if pt.kind != kind {
		return fmt.Errorf("archive is %s but the target is %s", kind, pt.kind)
	}
	if !lockInstance(target.ID) {
		return ErrDBBusy
	}
	defer unlockInstance(target.ID)
	if o.InPlace {
		if cfg, err := m.Store.GetDBPITRConfig(ctx, src.ID); err == nil {
			o.keepArchive = cfg.Enabled
		}
		// 복원한 DB의 로그를 복원 전 로그와 섞지 않는다. 새 epoch에서 기준 백업부터 다시 만든다.
		m.updatePITRConfig(ctx, src.ID, func(c *store.DBPITRConfig) {
			c.Epoch++
			c.LastBaseAt, c.LastBaseTry, c.PGLastLSN, c.MySQLActive = nil, nil, "", ""
		})
		if !pitrLock(src.ID, "sync") { // 복원하는 동안 보관 작업을 멈춘다
			time.Sleep(5 * time.Second)
			if !pitrLock(src.ID, "sync") {
				return ErrPITRBusy
			}
		}
		defer pitrUnlock(src.ID, "sync")
	}
	step("데이터 복원")
	switch kind {
	case "postgres":
		return m.restorePITRPostgres(ctx, pt, plan, at, o, step)
	default:
		return m.restorePITRMySQL(ctx, pt, plan, at)
	}
}

// waitOnline은 인스턴스가 online이 될 때까지 기다린다(새 서비스의 첫 배포).
func (m *Manager) waitOnline(ctx context.Context, id string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		in, err := m.Store.GetInstance(ctx, id)
		if err != nil {
			return err
		}
		if in.Status == store.InstOnline && in.CurrentDeploymentID != nil {
			return nil
		}
		if in.Status == store.InstFailed || in.Status == store.InstCrashed {
			return fmt.Errorf("target service deploy is %s", in.Status)
		}
		if time.Now().After(deadline) {
			return errors.New("target service did not come online in time")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// pitrOpener는 보관 파일을 연다.
type pitrOpener func(ctx context.Context, o *store.DBPITRObject) (io.ReadCloser, error)

func (m *Manager) openPITR(ctx context.Context, o *store.DBPITRObject) (io.ReadCloser, error) {
	st, err := m.BackupStorageFor(ctx, o.DestinationID)
	if err != nil {
		return nil, err
	}
	return st.Get(ctx, o.ObjectKey)
}

// writePGRestoreTar는 postgres 복원 helper의 stdin(tar: base.tar.gz, wal/<name>.gz)을 쓴다.
func writePGRestoreTar(ctx context.Context, w io.Writer, plan *pitrPlan, open pitrOpener) error {
	tw := tar.NewWriter(w)
	add := func(name string, o *store.DBPITRObject) error {
		rc, err := open(ctx, o)
		if err != nil {
			return fmt.Errorf("open %s: %w", o.FileName, err)
		}
		defer rc.Close()
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: o.SizeBytes, ModTime: o.SourceTime, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		if _, err := io.Copy(tw, rc); err != nil {
			return fmt.Errorf("copy %s: %w", o.FileName, err)
		}
		return nil
	}
	if err := add("base.tar.gz", plan.Base); err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: "wal/", Mode: 0o700, Typeflag: tar.TypeDir, ModTime: plan.Base.SourceTime}); err != nil {
		return err
	}
	for _, o := range plan.Logs {
		if err := add("wal/"+o.FileName+".gz", o); err != nil {
			return err
		}
	}
	return tw.Close()
}

// writeMySQLRestoreFrames는 mysql 복원 helper의 stdin(프레임: base.sql.gz, <binlog>.gz...)을 쓴다.
func writeMySQLRestoreFrames(ctx context.Context, w io.Writer, plan *pitrPlan, open pitrOpener) error {
	bw := bufio.NewWriterSize(w, 256<<10)
	add := func(name string, o *store.DBPITRObject) error {
		rc, err := open(ctx, o)
		if err != nil {
			return fmt.Errorf("open %s: %w", o.FileName, err)
		}
		defer rc.Close()
		return agentprotocol.WritePITRFrame(bw, name, o.SizeBytes, rc)
	}
	if err := add("base.sql.gz", plan.Base); err != nil {
		return err
	}
	for _, o := range plan.Logs {
		if err := add(o.FileName+".gz", o); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// pipeTo는 write가 쓰는 스트림을 run의 입력으로 넘긴다.
func pipeTo(ctx context.Context, write func(io.Writer) error, run func(io.Reader) error) error {
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(write(pw)) }()
	err := run(pr)
	pr.CloseWithError(errors.New("restore stopped"))
	return err
}

func (m *Manager) restorePITRPostgres(ctx context.Context, pt *pitrTarget, plan *pitrPlan, at time.Time, o PITRRestoreOpts, step func(string)) error {
	cr := dbCreds(pt.kind, pt.vars)
	ro := agentprotocol.PITRRestoreOpts{Target: at, Mount: pt.vol.MountPath, DataDir: pt.dataDir, KeepArchive: o.keepArchive}
	tool, err := agentprotocol.DBPITRRestoreTool("postgres", "", pt.port, cr, ro)
	if err != nil {
		return err
	}
	req, err := pt.request(agentprotocol.PITRRestore, pitrJob())
	if err != nil {
		return err
	}
	req.Volume, req.MountPath, req.DataDir, req.KeepArchive, req.TargetTime = pt.vol.DockerName, pt.vol.MountPath, pt.dataDir, o.keepArchive, at.UTC().Format(time.RFC3339)
	// 볼륨에 직접 쓰므로 DB를 멈춘다. reconcile이 되살리지 않게 잠시 멈춰 둔다.
	m.mu.Lock()
	m.paused[pt.in.ID] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.paused, pt.in.ID)
		m.mu.Unlock()
	}()
	var containers []string
	if pt.cur != nil {
		containers = pt.cur.Containers
	}
	for _, c := range containers {
		m.markDrain(c)
		if err := pt.drv.Stop(ctx, c, 30); err != nil {
			m.Log.Warn("pitr restore: stop", "container", c, "err", err)
		}
	}
	err = pipeTo(ctx, func(w io.Writer) error { return writePGRestoreTar(ctx, w, plan, m.openPITR) },
		func(r io.Reader) error { return m.pitrRunIn(ctx, pt, req, tool, r) })
	for _, c := range containers {
		pt.drv.Start(context.WithoutCancel(ctx), c)
		m.undrain(c)
	}
	if err != nil {
		return err
	}
	// 재시작한 postgres가 WAL을 목표 시각까지 재생하고 promote할 때까지 기다린다.
	step("WAL 재생")
	deadline := time.Now().Add(6 * time.Hour)
	var last error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
		out, err := m.pitrExec(ctx, pt, `psql -X -tA -c 'SELECT pg_is_in_recovery()'`, nil)
		if err == nil && strings.TrimSpace(out) == "f" {
			return nil
		}
		last = err
	}
	if last != nil {
		return fmt.Errorf("recovery did not finish: %w", last)
	}
	return errors.New("recovery did not finish in time")
}

func (m *Manager) restorePITRMySQL(ctx context.Context, pt *pitrTarget, plan *pitrPlan, at time.Time) error {
	if pt.in.Status != store.InstOnline {
		return fmt.Errorf("target database is %s; it must be online", pt.in.Status)
	}
	cr := dbCreds(pt.kind, pt.vars)
	ro := agentprotocol.PITRRestoreOpts{Target: at, BaseFile: plan.Base.BaseFile, BasePosition: plan.Base.BasePosition}
	tool, err := agentprotocol.DBPITRRestoreTool("mysql", pt.host, pt.port, cr, ro)
	if err != nil {
		return err
	}
	req, err := pt.request(agentprotocol.PITRRestore, pitrJob())
	if err != nil {
		return err
	}
	req.TargetTime, req.BaseFile, req.BasePosition = at.UTC().Format(time.RFC3339), plan.Base.BaseFile, plan.Base.BasePosition
	return pipeTo(ctx, func(w io.Writer) error { return writeMySQLRestoreFrames(ctx, w, plan, m.openPITR) },
		func(r io.Reader) error { return m.pitrRunIn(ctx, pt, req, tool, r) })
}
