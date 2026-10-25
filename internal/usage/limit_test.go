package usage

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/events"
	"github.com/turin-dev/aven/internal/store"
)

// fakeInstances는 Stop/StartAgain 호출을 기록한다(Stop은 상태를 offline으로 바꾼다).
type fakeInstances struct {
	st      *store.Store
	stopped []string
	started []string
}

func (f *fakeInstances) Stop(ctx context.Context, in *store.Instance) error {
	f.stopped = append(f.stopped, in.ID)
	return f.st.SetInstanceStatus(ctx, in.ID, store.InstOffline, nil)
}

func (f *fakeInstances) StartAgain(ctx context.Context, in *store.Instance) (*store.Deployment, error) {
	f.started = append(f.started, in.ID)
	return &store.Deployment{InstanceID: in.ID}, nil
}

type limitFixture struct {
	st         *store.Store
	r          *Roller
	fake       *fakeInstances
	p          *store.Project
	env        *store.Environment
	web, idle  *store.Instance
	now        time.Time
	watermark  int64
	monthStart int64
}

func newLimitFixture(t *testing.T) *limitFixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &limitFixture{st: st, fake: &fakeInstances{st: st}}
	f.p = &store.Project{Name: "acme"}
	if f.env, err = st.CreateProject(ctx, f.p); err != nil {
		t.Fatal(err)
	}
	mk := func(name, status string) *store.Instance {
		sv := &store.Service{ProjectID: f.p.ID, Name: name, SourceType: "image"}
		if err := st.CreateService(ctx, sv); err != nil {
			t.Fatal(err)
		}
		img := "nginx:1"
		in := &store.Instance{ServiceID: sv.ID, EnvironmentID: f.env.ID, NodeID: store.LocalNodeID}
		in.Image = &img
		if err := st.CreateInstance(ctx, in); err != nil {
			t.Fatal(err)
		}
		st.SetInstanceStatus(ctx, in.ID, status, nil)
		in, _ = st.GetInstance(ctx, in.ID)
		return in
	}
	f.web = mk("web", store.InstOnline)
	f.idle = mk("old", store.InstOffline)
	// 3월 15일 12:20 UTC. 12:00까지 집계됨(watermark), 12:00~12:20은 지표 점만 있다.
	f.now = time.Date(2025, 3, 15, 12, 20, 0, 0, time.UTC)
	f.watermark = time.Date(2025, 3, 15, 12, 0, 0, 0, time.UTC).Unix()
	f.monthStart = MonthStart(f.now).Unix()
	st.SetSetting(ctx, watermarkKey, strconv.FormatInt(f.watermark, 10))
	f.r = &Roller{Store: st, Events: &events.Bus{Store: st}, Instances: f.fake, Now: func() time.Time { return f.now }}
	return f
}

// rolled는 집계가 끝난 시간에 CPU 코어·시간을 넣는다.
func (f *limitFixture) rolled(t *testing.T, hour int64, coreHours float64) {
	t.Helper()
	if err := f.st.UpsertUsage(context.Background(), []store.UsageRow{{ProjectID: f.p.ID, EnvironmentID: f.env.ID, ServiceID: f.web.ServiceID,
		Hour: hour, CPUCoreSeconds: coreHours * 3600}}); err != nil {
		t.Fatal(err)
	}
}

// samples는 [from, to) 동안 10초마다 CPU cpu%(합계) 점을 넣는다.
func (f *limitFixture) samples(t *testing.T, from, to int64, cpu float64) {
	t.Helper()
	var rows []store.MetricRow
	for ts := from; ts < to; ts += 10 {
		rows = append(rows, store.MetricRow{T: ts, Resolution: 10, CPU: cpu, Mem: 0, MemLimit: 1, Replicas: 1})
	}
	if err := f.st.InsertMetrics(context.Background(), f.web.ID, rows); err != nil {
		t.Fatal(err)
	}
}

func (f *limitFixture) setLimit(t *testing.T, cpu float64, action string) {
	t.Helper()
	if err := f.st.SetUsageLimit(context.Background(), f.p.ID, &store.UsageLimit{CPUCoreHours: &cpu, Action: action}); err != nil {
		t.Fatal(err)
	}
}

func (f *limitFixture) events(kind string) int {
	evs, _ := f.st.ListEvents(context.Background(), f.p.ID, "", 100)
	n := 0
	for _, e := range evs {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func TestMonthToDateIncludesPartialHour(t *testing.T) {
	ctx := context.Background()
	f := newLimitFixture(t)
	f.rolled(t, f.watermark-Hour, 2)                 // 11시: 집계됨
	f.rolled(t, f.monthStart-Hour, 50)               // 지난달: 빼야 한다
	f.samples(t, f.watermark-Hour, f.watermark, 400) // 11시의 점은 집계가 이미 있으므로 다시 세지 않는다
	f.samples(t, f.watermark, f.now.Unix(), 150)     // 12:00~12:20, 1.5코어 → 0.5 코어·시간
	got, err := f.r.MonthToDate(ctx, f.p.ID, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if !near(got.CPUCoreHours, 2.5) {
		t.Fatalf("month to date = %v, want 2.5 (2 rolled + 0.5 partial)", got.CPUCoreHours)
	}
	// 집계가 아직 한 번도 돌지 않았으면(watermark 없음) 이번 달 전체를 점으로 계산한다.
	f.st.SetSetting(ctx, watermarkKey, "0")
	got, _ = f.r.MonthToDate(ctx, f.p.ID, f.now)
	if !near(got.CPUCoreHours, 4+0.5) {
		t.Fatalf("without watermark = %v, want 4.5 (samples only)", got.CPUCoreHours)
	}
}

func TestExceeded(t *testing.T) {
	v := func(x float64) *float64 { return &x }
	l := &store.UsageLimit{CPUCoreHours: v(10), EgressGB: v(5), Action: "stop"}
	if ex := Exceeded(l, Totals{CPUCoreHours: 9.99, EgressGB: 5}); len(ex) != 1 || ex[0] != "egress_gb" {
		t.Errorf("exceeded: %v", ex)
	}
	if ex := Exceeded(nil, Totals{CPUCoreHours: 1e9}); ex != nil {
		t.Errorf("nil limit: %v", ex)
	}
}

func TestLimitStopSuspendsAndResume(t *testing.T) {
	ctx := context.Background()
	f := newLimitFixture(t)
	f.rolled(t, f.watermark-Hour, 0.8)
	f.setLimit(t, 1, store.UsageLimitStop)
	// 집계된 0.8만으로는 아직 아니다.
	f.r.CheckLimits(ctx, f.now)
	if len(f.fake.stopped) != 0 || f.events("usage.limit_reached") != 0 {
		t.Fatalf("stopped too early: %v", f.fake.stopped)
	}
	// 부분 시간(12:00~12:20, 1코어 → 0.333)을 더하면 넘는다: 매시 집계를 기다리지 않고 5분 검사에서 멈춘다.
	f.samples(t, f.watermark, f.now.Unix(), 100)
	f.r.CheckLimits(ctx, f.now)
	if len(f.fake.stopped) != 1 || f.fake.stopped[0] != f.web.ID {
		t.Fatalf("stop calls: %v (offline instance must not be stopped)", f.fake.stopped)
	}
	pl, _ := f.st.GetUsageLimit(ctx, f.p.ID)
	if !pl.Suspension.ActiveAt(f.now) || pl.Suspension.Month != "2025-03" || len(pl.Suspension.StoppedInstances) != 1 ||
		!pl.Suspension.Until.Equal(time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC)) || pl.Suspension.Reason == "" {
		t.Fatalf("suspension: %+v", pl.Suspension)
	}
	if sp, _ := f.st.SuspensionForInstance(ctx, f.web.ID); !sp.ActiveAt(f.now) {
		t.Fatalf("suspension for instance: %+v", sp)
	}
	if f.events("usage.limit_reached") != 1 {
		t.Fatal("limit_reached event")
	}
	// 다시 검사해도 이벤트와 정지는 반복되지 않는다.
	f.r.CheckLimits(ctx, f.now.Add(5*time.Minute))
	if len(f.fake.stopped) != 1 || f.events("usage.limit_reached") != 1 {
		t.Fatalf("repeated: stops=%v events=%d", f.fake.stopped, f.events("usage.limit_reached"))
	}
	// 한도를 넘은 채로는 resume할 수 없다.
	if _, err := f.r.Resume(ctx, f.p.ID, f.now); !errors.Is(err, ErrStillOverLimit) {
		t.Fatalf("resume over limit: %v", err)
	}
	// 한도를 올리면 정지가 풀리지만 서비스는 자동으로 다시 시작하지 않는다.
	f.setLimit(t, 100, store.UsageLimitStop)
	if err := f.r.CheckProject(ctx, f.p.ID, f.now); err != nil {
		t.Fatal(err)
	}
	pl, _ = f.st.GetUsageLimit(ctx, f.p.ID)
	if pl.Suspension == nil || pl.Suspension.ActiveAt(f.now) || pl.Suspension.LiftedAt == nil || len(f.fake.started) != 0 {
		t.Fatalf("lift: %+v started=%v", pl.Suspension, f.fake.started)
	}
	if f.events("usage.unsuspended") != 1 {
		t.Fatal("unsuspended event")
	}
	// resume: 멈춘 서비스를 다시 시작하고 기록을 지운다.
	res, err := f.r.Resume(ctx, f.p.ID, f.now)
	if err != nil || len(res.Restarted) != 1 || res.Restarted[0] != f.web.ID || len(f.fake.started) != 1 {
		t.Fatalf("resume: %+v %v started=%v", res, err, f.fake.started)
	}
	if pl, _ = f.st.GetUsageLimit(ctx, f.p.ID); pl.Suspension != nil {
		t.Fatalf("suspension after resume: %+v", pl.Suspension)
	}
	if _, err := f.r.Resume(ctx, f.p.ID, f.now); !errors.Is(err, ErrNotSuspended) {
		t.Fatalf("second resume: %v", err)
	}
}

func TestLimitNotifyDoesNotStop(t *testing.T) {
	ctx := context.Background()
	f := newLimitFixture(t)
	f.rolled(t, f.watermark-Hour, 5)
	f.setLimit(t, 1, store.UsageLimitNotify)
	f.r.CheckLimits(ctx, f.now)
	f.r.CheckLimits(ctx, f.now)
	pl, _ := f.st.GetUsageLimit(ctx, f.p.ID)
	if len(f.fake.stopped) != 0 || pl.Suspension != nil || f.events("usage.limit_reached") != 1 {
		t.Fatalf("notify: stops=%v suspension=%+v events=%d", f.fake.stopped, pl.Suspension, f.events("usage.limit_reached"))
	}
}

func TestLimitSuspensionEndsWithMonth(t *testing.T) {
	ctx := context.Background()
	f := newLimitFixture(t)
	f.rolled(t, f.watermark-Hour, 5)
	f.setLimit(t, 1, store.UsageLimitStop)
	f.r.RollUp(ctx) // 매시 집계 뒤에도 검사한다(RollUp → CheckLimits)
	if len(f.fake.stopped) != 1 {
		t.Fatalf("rollup check: %v", f.fake.stopped)
	}
	// 다음 달: 사용량이 0부터 다시 시작하므로 정지가 풀린다(서비스는 그대로 멈춰 있다).
	next := time.Date(2025, 4, 1, 0, 10, 0, 0, time.UTC)
	f.r.CheckLimits(ctx, next)
	pl, _ := f.st.GetUsageLimit(ctx, f.p.ID)
	if pl.Suspension == nil || pl.Suspension.ActiveAt(next) || len(pl.Suspension.StoppedInstances) != 1 || len(f.fake.started) != 0 {
		t.Fatalf("month rollover: %+v", pl.Suspension)
	}
	if sp, _ := f.st.SuspensionForInstance(ctx, f.web.ID); sp.ActiveAt(next) {
		t.Fatal("deploys still blocked in the new month")
	}
}
