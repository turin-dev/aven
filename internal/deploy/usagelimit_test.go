package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

func TestEnqueueBlockedWhileSuspended(t *testing.T) {
	ctx := context.Background()
	f := newScaleFixture(t)
	now := time.Now().UTC()
	sp := &store.UsageSuspension{Month: now.Format("2006-01"), Reason: "CPU 한도", SuspendedAt: now, Until: now.Add(time.Hour)}
	if err := f.st.SetUsageSuspension(ctx, f.svc.ProjectID, sp); err != nil {
		t.Fatal(err)
	}
	_, err := f.m.Enqueue(ctx, f.in, EnqueueOpts{Trigger: "manual"})
	if !errors.Is(err, ErrProjectSuspended) || !strings.Contains(err.Error(), "사용량 한도") {
		t.Fatalf("enqueue while suspended: %v", err)
	}
	// 건너뛴 배포 기록(skipped)은 막지 않는다.
	if _, err := f.m.Enqueue(ctx, f.in, EnqueueOpts{Trigger: "manual", Status: store.DepSkipped}); err != nil {
		t.Fatalf("skipped record: %v", err)
	}
	// 풀린 정지(지난달이거나 lifted)는 막지 않는다.
	lifted := now
	sp.LiftedAt = &lifted
	f.st.SetUsageSuspension(ctx, f.svc.ProjectID, sp)
	if _, err := f.m.Enqueue(ctx, f.in, EnqueueOpts{Trigger: "manual"}); err != nil {
		t.Fatalf("enqueue after lift: %v", err)
	}
	sp.LiftedAt, sp.Month = nil, "2000-01"
	f.st.SetUsageSuspension(ctx, f.svc.ProjectID, sp)
	if _, err := f.m.Enqueue(ctx, f.in, EnqueueOpts{Trigger: "manual"}); err != nil {
		t.Fatalf("enqueue with old month: %v", err)
	}
}

func TestStartAgainRedeploysCurrentImage(t *testing.T) {
	ctx := context.Background()
	f := newScaleFixture(t)
	d := f.live(t)
	f.st.SetInstanceStatus(ctx, f.in.ID, store.InstOffline, &d.ID)
	in, _ := f.st.GetInstance(ctx, f.in.ID)
	nd, err := f.m.StartAgain(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if nd.Trigger != "redeploy" || nd.Image == nil || *nd.Image != *d.Image {
		t.Fatalf("start again: %+v", nd)
	}
}

// 자동 확장 기록(cooldown, stabilization 추천 값, 표시 상태)은 Manager를 새로 만들어도(재시작) 남는다.
func TestAutoscaleStatePersistsAcrossManagers(t *testing.T) {
	ctx := context.Background()
	f := newScaleFixture(t)
	f.live(t)
	cpu := 50.0
	in, _ := f.st.GetInstance(ctx, f.in.ID)
	in.Autoscale = &store.Autoscale{Enabled: true, MinReplicas: 2, MaxReplicas: 6, TargetCPUPercent: &cpu}
	f.st.UpdateInstanceConfig(ctx, in)
	now := time.Now()
	for i := 0; i < 6; i++ {
		f.st.InsertMetrics(ctx, in.ID, []store.MetricRow{{T: now.Add(-time.Duration(i*10) * time.Second).Unix(), Resolution: 10, CPU: 200, Mem: 100, MemLimit: 1000, Replicas: 2}})
	}
	f.m.AutoscaleOnce(ctx, now)
	in, _ = f.st.GetInstance(ctx, in.ID)
	if n := f.m.CurrentReplicas(ctx, in); n != 4 {
		t.Fatalf("scale up: %d", n)
	}
	row, err := f.st.GetAutoscaleState(ctx, in.ID)
	if err != nil || row.LastScale == nil || len(row.Recs) == 0 {
		t.Fatalf("persisted row: %+v %v", row, err)
	}

	// 재시작: 새 Manager(같은 DB). 상태는 평가 전에도 저장된 값으로 보인다.
	m2 := &Manager{Store: f.st, Agents: f.m.Agents, Logs: f.m.Logs, Log: f.m.Log, Cfg: f.m.Cfg, localDrv: f.m.localDrv,
		localProxy: f.m.localProxy, Dispatch: func(string, string) {}}
	st := m2.AutoscaleStatusOf(in)
	if st == nil || st.LastScaleAt == nil || st.Desired == nil || *st.Desired != 4 || !strings.Contains(st.LastReason, "cpu") {
		t.Fatalf("status after restart: %+v", st)
	}
	// cooldown도 남는다: 부하가 더 올라도 바로 다시 늘리지 않는다.
	for i := 0; i < 6; i++ {
		f.st.InsertMetrics(ctx, in.ID, []store.MetricRow{{T: now.Add(time.Duration(i+1) * time.Second).Unix(), Resolution: 10, CPU: 800, Mem: 100, MemLimit: 1000, Replicas: 4}})
	}
	m2.AutoscaleOnce(ctx, now.Add(10*time.Second))
	if n := m2.CurrentReplicas(ctx, in); n != 4 {
		t.Fatalf("cooldown lost across restart: %d", n)
	}
	if st := m2.AutoscaleStatusOf(in); st == nil || !strings.Contains(st.Reason, "cooldown") {
		t.Fatalf("reason after restart: %+v", st)
	}

	// stabilization window도 남는다: 4를 추천한 기록이 있으므로 새 Manager는 부하가 줄어도 바로 2로 내리지 않는다.
	e := m2.loadASEntry(ctx, in.ID)
	if len(e.hpa.recs) == 0 || e.hpa.LastScale.IsZero() {
		t.Fatalf("loaded entry: %+v", e.hpa)
	}
	cfg := *in.Autoscale
	cfg.Defaults()
	dec := e.hpa.Decide(&cfg, 4, 2, now.Add(time.Duration(cfg.ScaleDownCooldownS)*time.Second/2))
	if dec.Act {
		t.Fatalf("stabilization lost across restart: %+v", dec)
	}

	// 자동 확장을 끄면 저장된 기록을 지운다.
	in.Autoscale.Enabled = false
	f.st.UpdateInstanceConfig(ctx, in)
	m2.AutoscaleOnce(ctx, now.Add(20*time.Second))
	if _, err := f.st.GetAutoscaleState(ctx, in.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("state after disabling: %v", err)
	}
}
