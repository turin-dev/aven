package alerts

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/events"
	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/store"
)

func TestStepStateMachine(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	state, since := store.AlertOK, (*time.Time)(nil)
	step := func(breach bool, at time.Duration, wantState, wantEvent string) {
		t.Helper()
		var ev string
		state, since, ev = Step(state, since, breach, t0.Add(at), 60)
		if state != wantState || ev != wantEvent {
			t.Fatalf("at %v breach=%v: state=%s event=%q, want %s %q", at, breach, state, ev, wantState, wantEvent)
		}
	}
	step(true, 0, store.AlertPending, "")
	step(true, 30*time.Second, store.AlertPending, "")
	step(false, 45*time.Second, store.AlertOK, "") // 짧은 튐은 무시
	step(true, 60*time.Second, store.AlertPending, "")
	if !since.Equal(t0.Add(60 * time.Second)) {
		t.Fatalf("pending since reset: %v", since)
	}
	step(true, 90*time.Second, store.AlertPending, "")
	step(true, 120*time.Second, store.AlertFiring, "alert.firing")
	step(true, 150*time.Second, store.AlertFiring, "") // 계속 넘어도 한 번만
	step(false, 180*time.Second, store.AlertOK, "alert.resolved")
	step(false, 210*time.Second, store.AlertOK, "")
	// for_seconds = 0이면 바로 firing
	if s, _, ev := Step(store.AlertOK, nil, true, t0, 0); s != store.AlertFiring || ev != "alert.firing" {
		t.Errorf("for=0: %s %s", s, ev)
	}
	if !Breach(">", 81, 80) || Breach(">", 80, 80) || !Breach("<", 1, 2) || Breach("<", 2, 2) {
		t.Error("Breach")
	}
}

func TestEvaluatorEmitsFiringAndResolved(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p := &store.Project{Name: "acme"}
	env, _ := st.CreateProject(ctx, p)
	svc := &store.Service{ProjectID: p.ID, Name: "web", SourceType: "image"}
	st.CreateService(ctx, svc)
	in := &store.Instance{ServiceID: svc.ID, EnvironmentID: env.ID}
	st.CreateInstance(ctx, in)
	rule := &store.AlertRule{ProjectID: p.ID, ServiceID: &svc.ID, Metric: "cpu_percent", Comparator: ">", Threshold: 80, ForSeconds: 60, Enabled: true}
	if err := st.CreateAlertRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	value, have := 0.0, true
	e := &Evaluator{Store: st, Events: &events.Bus{Store: st}, Now: func() time.Time { return now },
		Value: func(_ context.Context, metric string, got *store.Instance) (float64, bool) {
			if metric != "cpu_percent" || got.ID != in.ID {
				t.Errorf("unexpected value request %s %s", metric, got.ID)
			}
			return value, have
		}}
	tick := func(v float64, ok bool) *store.AlertRule {
		t.Helper()
		value, have = v, ok
		if err := e.EvaluateAll(ctx); err != nil {
			t.Fatal(err)
		}
		now = now.Add(Interval)
		r, _ := st.GetAlertRule(ctx, rule.ID)
		return r
	}
	kinds := func() []string {
		evs, _ := st.ListEvents(ctx, p.ID, "", 100)
		var out []string
		for i := len(evs) - 1; i >= 0; i-- {
			if evs[i].Kind == "alert.firing" || evs[i].Kind == "alert.resolved" {
				out = append(out, evs[i].Kind)
			}
		}
		return out
	}
	if r := tick(95, true); r.LastState != store.AlertPending || r.PendingSince == nil || *r.LastValue != 95 {
		t.Fatalf("after 1st breach: %+v", r)
	}
	tick(96, true) // 30초
	if r := tick(97, true); r.LastState != store.AlertFiring || r.LastFiredAt == nil {
		t.Fatalf("after 60s: %+v", r)
	}
	if r := tick(0, false); r.LastState != store.AlertFiring { // 데이터가 없으면 그대로
		t.Fatalf("no data: %+v", r)
	}
	tick(99, true)
	if r := tick(10, true); r.LastState != store.AlertOK {
		t.Fatalf("resolve: %+v", r)
	}
	if k := kinds(); len(k) != 2 || k[0] != "alert.firing" || k[1] != "alert.resolved" {
		t.Errorf("events: %v", k)
	}
	evs, _ := st.ListEvents(ctx, p.ID, "", 10)
	if evs[0].ServiceID == nil || *evs[0].ServiceID != svc.ID || evs[0].EnvironmentID == nil || *evs[0].EnvironmentID != env.ID {
		t.Errorf("event target: %+v", evs[0])
	}
	// 규칙을 바꾸면 상태가 처음으로 돌아간다
	tick(99, true)
	r, _ := st.GetAlertRule(ctx, rule.ID)
	r.Threshold = 50
	st.UpdateAlertRule(ctx, r)
	if r, _ = st.GetAlertRule(ctx, rule.ID); r.LastState != store.AlertOK || r.PendingSince != nil {
		t.Errorf("after update: %+v", r)
	}
}

func TestSourcesValues(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p := &store.Project{Name: "acme"}
	env, _ := st.CreateProject(ctx, p)
	svc := &store.Service{ProjectID: p.ID, Name: "web", SourceType: "image"}
	st.CreateService(ctx, svc)
	in := &store.Instance{ServiceID: svc.ID, EnvironmentID: env.ID}
	st.CreateInstance(ctx, in)
	now := time.Now()
	disk := int64(3 << 20)
	st.InsertMetrics(ctx, in.ID, []store.MetricRow{{T: now.Add(-5 * time.Minute).Unix(), CPU: 10, Mem: 1, MemLimit: 4, Resolution: 10},
		{T: now.Add(-10 * time.Second).Unix(), CPU: 150, Mem: 1 << 30, MemLimit: 4 << 30, Disk: &disk, Resolution: 10}})
	limit := 4
	vol := &store.Volume{ProjectID: p.ID, EnvironmentID: env.ID, InstanceID: &in.ID, Name: "data", MountPath: "/data", SizeLimitMB: &limit}
	if err := st.CreateVolume(ctx, vol); err != nil {
		t.Fatal(err)
	}
	vol, _ = st.GetVolume(ctx, vol.ID)
	ts := func(d time.Duration) string { return now.Add(d).UTC().Format(time.RFC3339Nano) }
	logs := []proxy.AccessLog{{T: ts(-10 * time.Minute), Status: 500, DurationMS: 9999}}
	for i := 1; i <= 20; i++ {
		st := 200
		if i <= 2 {
			st = 502
		}
		logs = append(logs, proxy.AccessLog{T: ts(-time.Duration(i) * time.Second), Status: st, DurationMS: float64(i * 10)})
	}
	src := &Sources{Store: st, Now: func() time.Time { return now },
		HTTP:       func(_ string, since time.Time) deploy.HTTPWindow { return deploy.SummarizeHTTP(logs, since) },
		Restarts:   func(context.Context, *store.Instance) (int, bool) { return 3, true },
		VolumeSize: func(name string) *int64 { v := int64(3 << 20); return &v }}
	for metric, want := range map[string]float64{"cpu_percent": 150, "memory_percent": 25, "memory_bytes": 1 << 30, "disk_bytes": float64(disk),
		"http_5xx_rate": 10, "http_p95_ms": 190, "restarts": 3, "volume_percent": 75} {
		got, ok := src.Value(ctx, metric, in)
		if !ok || got != want {
			t.Errorf("%s = %v (%v), want %v", metric, got, ok, want)
		}
	}
	// 오래된 점만 있으면 데이터 없음
	in2 := &store.Instance{ServiceID: svc.ID, EnvironmentID: env.ID + "x"}
	if _, ok := src.Value(ctx, "cpu_percent", in2); ok {
		t.Error("value for an instance without samples")
	}
}
