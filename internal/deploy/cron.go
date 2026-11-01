package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/cronx"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// CronLoop은 매 분 정각에 cron 인스턴스와 볼륨 백업 일정을 확인한다.
func (m *Manager) CronLoop(ctx context.Context) {
	for {
		now := time.Now().UTC()
		next := now.Truncate(time.Minute).Add(time.Minute)
		select {
		case <-ctx.Done():
			return
		case <-time.After(next.Sub(now)):
		}
		m.cronTick(ctx, next)
	}
}

func (m *Manager) cronTick(ctx context.Context, now time.Time) {
	// 사용자 cron 작업을 먼저 시작한다(정각에 맞아야 한다). 백업 일정 확인이 느려도 늦지 않게.
	m.cronInstancesTick(ctx, now)
	m.dbBackupTick(ctx, now)
	m.pitrTick(ctx, now)
	m.volumeBackupTick(ctx, now)
}

func (m *Manager) cronInstancesTick(ctx context.Context, now time.Time) {
	insts, err := m.Store.ListInstances(ctx)
	if err != nil {
		return
	}
	for _, in := range insts {
		if in.Status != store.InstScheduled || in.CurrentDeploymentID == nil {
			continue
		}
		d, err := m.Store.GetDeployment(ctx, *in.CurrentDeploymentID)
		if err != nil || d.Spec.CronSchedule == nil {
			continue
		}
		s, err := cronx.Parse(*d.Spec.CronSchedule)
		if err != nil || !s.Match(now) {
			continue
		}
		if _, err := m.RunCron(ctx, in); err != nil {
			m.log().Warn("cron run", "instance", in.ID, "err", err)
		}
	}
}

func (m *Manager) volumeBackupTick(ctx context.Context, now time.Time) {
	vols, err := m.Store.ListVolumes(ctx)
	if err != nil {
		return
	}
	for _, v := range vols {
		if v.BackupCron == nil {
			continue
		}
		if s, err := cronx.Parse(*v.BackupCron); err == nil && s.Match(now) {
			if _, err := m.CreateBackup(ctx, v); err != nil {
				m.log().Warn("scheduled backup", "volume", v.ID, "err", err)
			}
		}
	}
}

var ErrCronBusy = errors.New("a run of this cron job is still in progress")

// cronRunLabel은 cron 실행 컨테이너에 붙이는 실행 기록 ID 라벨이다.
const cronRunLabel = "keel.cronrun"

// cron 실행 로그에 남기는 앞·뒤 항목 수.
const (
	cronLogHead = 1000
	cronLogTail = 5000
)

// RunCron은 cron 인스턴스의 현재 배포 이미지를 `docker run --rm`처럼 한 번 실행한다(비동기).
func (m *Manager) RunCron(ctx context.Context, in *store.Instance) (*store.CronRun, error) {
	if in.CurrentDeploymentID == nil {
		return nil, errors.New("no live deployment to run")
	}
	d, err := m.Store.GetDeployment(ctx, *in.CurrentDeploymentID)
	if err != nil {
		return nil, err
	}
	if d.Image == nil {
		return nil, errors.New("deployment has no image")
	}
	t, err := m.Load(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.cronBusy[in.ID] {
		m.mu.Unlock()
		return nil, ErrCronBusy
	}
	m.cronBusy[in.ID] = true
	m.mu.Unlock()
	run := &store.CronRun{ID: store.NewID("run"), DeploymentID: d.ID, InstanceID: in.ID}
	run.Container = "keel-cron-" + t.Service.Name + "-" + strings.TrimPrefix(run.ID, "run_")[:8]
	if err := m.Store.CreateCronRun(ctx, run); err != nil {
		m.mu.Lock()
		delete(m.cronBusy, in.ID)
		m.mu.Unlock()
		return nil, err
	}
	m.emit(ctx, t, "cron.started", fmt.Sprintf("%s (%s): cron 실행 %s 시작", t.Service.Name, t.Env.Name, run.ID))
	go m.execCron(t, d, run)
	return run, nil
}

func (m *Manager) execCron(t *Target, d *store.Deployment, run *store.CronRun) {
	ctx := context.Background()
	defer func() {
		m.mu.Lock()
		delete(m.cronBusy, t.Instance.ID)
		m.mu.Unlock()
	}()
	// 출력은 앞 cronLogHead개와 마지막 cronLogTail개만 남긴다(24시간 도는 작업의 출력을 모두 메모리에 들고 있지 않게).
	var lines []LogEntry
	lim := newLineLimiter(func(e LogEntry) { lines = append(lines, e) }, func(n int) LogEntry {
		return LogEntry{T: time.Now().UTC().Format(time.RFC3339Nano), Replica: run.Container, Stream: "stderr", Msg: "aven: " + omittedMsg(n)}
	}, cronLogHead, cronLogTail)
	logf := func(stream, msg string) {
		lim.Add(LogEntry{T: time.Now().UTC().Format(time.RFC3339Nano), Replica: run.Container, Stream: stream, Msg: truncateLine(msg)})
	}
	code := -1
	err := m.ensureEnvNetwork(ctx, t.Env, d.NodeID)
	if err == nil {
		spec := m.runSpec(t, d, 0)
		spec.Name = run.Container
		spec.Labels[node.LabelRole] = node.RoleCron
		spec.Labels[cronRunLabel] = run.ID
		spec.Aliases = nil
		// 실행 시간 제한: 24시간
		rctx, cancel := context.WithTimeout(ctx, 24*time.Hour)
		code, err = m.driverFor(d).RunToCompletion(rctx, spec, logf)
		cancel()
	}
	lim.Flush()
	if err != nil {
		lines = append(lines, LogEntry{T: time.Now().UTC().Format(time.RFC3339Nano), Replica: run.Container, Stream: "stderr", Msg: "aven: " + err.Error()})
	}
	writeJSONL(m.cronLogPath(run.ID), lines)
	run.ExitCode = &code
	run.Status = "succeeded"
	if code != 0 || err != nil {
		run.Status = "failed"
	}
	m.Store.FinishCronRun(ctx, run)
	kind := "cron.succeeded"
	if run.Status == "failed" {
		kind = "cron.failed"
	}
	m.emit(ctx, t, kind, fmt.Sprintf("%s (%s): cron 실행 %s 종료 (exit %d)", t.Service.Name, t.Env.Name, run.ID, code))
}

// CronRunLogs는 실행 기록의 로그를 돌려준다.
func (m *Manager) CronRunLogs(runID string) []LogEntry { return readJSONL(m.cronLogPath(runID), 0) }
