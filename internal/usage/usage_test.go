package usage

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/events"
	"github.com/turin-dev/aven/internal/store"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestAggregateMath(t *testing.T) {
	h := int64(1_700_000_000) - int64(1_700_000_000)%Hour
	disk := int64(2e9)
	var rows []store.MetricRow
	// 기준값(이전 시간): Tx 1000
	rows = append(rows, store.MetricRow{T: h - 10, CPU: 999, Mem: 1e9, Tx: 1000, Resolution: 10})
	// 한 시간 내내 10초마다 CPU 50%(0.5코어), 메모리 2GB, 디스크 2GB, Tx 매번 +100
	for i := int64(0); i < 360; i++ {
		rows = append(rows, store.MetricRow{T: h + i*10, CPU: 50, Mem: 2e9, Tx: 1000 + (i+1)*100, Disk: &disk, Resolution: 10})
	}
	// 다음 시간: 컨테이너 재시작으로 카운터가 줄었다(새 값 전체가 증가분)
	rows = append(rows, store.MetricRow{T: h + Hour, CPU: 100, Mem: 1e9, Tx: 500, Resolution: 300})
	agg := Aggregate(rows, h, h+2*Hour)
	if len(agg) != 2 {
		t.Fatalf("hours: %d", len(agg))
	}
	u := agg[h]
	if !near(u.CPUCoreSeconds, 0.5*3600) || !near(u.MemGBSeconds, 2*3600) || u.EgressBytes != 360*100 || !near(u.DiskGBHours, 2) {
		t.Errorf("hour 1: %+v", u)
	}
	u2 := agg[h+Hour]
	if !near(u2.CPUCoreSeconds, 300) || !near(u2.MemGBSeconds, 300) || u2.EgressBytes != 500 {
		t.Errorf("hour 2 (coarse sample, counter reset): %+v", u2)
	}
	// to 이후의 점은 무시
	if a := Aggregate(rows, h, h+Hour); len(a) != 1 {
		t.Errorf("to bound: %d", len(a))
	}
	var tot Totals
	tot.Add(*u)
	tot.Apply(&Pricing{VCPUHour: 0.02, GBRAMHour: 0.01, GBEgress: 0.1, GBDiskMonth: 0.15})
	if !near(tot.CPUCoreHours, 0.5) || !near(tot.MemoryGBHours, 2) || !near(tot.EgressGB, 36000/1e9) ||
		!near(tot.Cost.CPU, 0.01) || !near(tot.Cost.Memory, 0.02) || !near(tot.Cost.Disk, 2*0.15/730) ||
		!near(tot.Cost.Total, tot.Cost.CPU+tot.Cost.Memory+tot.Cost.Egress+tot.Cost.Disk) {
		t.Errorf("totals: %+v cost %+v", tot, tot.Cost)
	}
}

func TestPricingFromEnv(t *testing.T) {
	if p, err := PricingFromEnv(func(string) string { return "" }); p != nil || err != nil {
		t.Error("no env should mean no pricing")
	}
	p, err := PricingFromEnv(func(k string) string {
		if k == "KEEL_PRICE_VCPU_HOUR" {
			return "0.5"
		}
		return ""
	})
	if err != nil || p.VCPUHour != 0.5 || p.GBEgress != 0 {
		t.Errorf("%+v %v", p, err)
	}
	if _, err := PricingFromEnv(func(k string) string { return "-1" }); err == nil {
		t.Error("negative price accepted")
	}
}

func TestRollUpBackfillIsIdempotent(t *testing.T) {
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
	now := time.Now().Truncate(time.Hour).Add(10 * time.Minute)
	start := now.Truncate(time.Hour).Add(-5 * time.Hour)
	var rows []store.MetricRow
	for t := start; t.Before(now); t = t.Add(5 * time.Minute) {
		rows = append(rows, store.MetricRow{T: t.Unix(), CPU: 100, Mem: 1e9, Tx: t.Unix() - start.Unix(), Resolution: 300})
	}
	if err := st.InsertMetrics(ctx, in.ID, rows); err != nil {
		t.Fatal(err)
	}
	r := &Roller{Store: st, Now: func() time.Time { return now }}
	if err := r.RollUp(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot := func() []store.UsageRow {
		u, err := st.QueryUsage(ctx, p.ID, 0, math.MaxInt64)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	first := snapshot()
	if len(first) != 5 { // 끝난 5시간(현재 시간은 아직)
		t.Fatalf("rollup rows: %d", len(first))
	}
	for _, u := range first {
		if !near(u.CPUCoreSeconds, 3600) || !near(u.MemGBSeconds, 3600) || u.ServiceID != svc.ID || u.EnvironmentID != env.ID {
			t.Errorf("row: %+v", u)
		}
	}
	// 다시 돌려도(같은 시각) 바뀌지 않는다
	r.RollUp(ctx)
	// 워터마크를 지워 처음부터 다시 집계(백필)해도 결과가 같다
	st.SetSetting(ctx, watermarkKey, "0")
	r.RollUp(ctx)
	again := snapshot()
	if len(again) != len(first) {
		t.Fatalf("rows after re-roll: %d", len(again))
	}
	for i := range first {
		if first[i] != again[i] {
			t.Errorf("re-roll changed row %d: %+v → %+v", i, first[i], again[i])
		}
	}
	// 서버가 3시간 꺼져 있었다: 다음 실행이 놓친 시간을 채운다
	later := now.Add(3 * time.Hour)
	for t := now; t.Before(later); t = t.Add(5 * time.Minute) {
		st.InsertMetrics(ctx, in.ID, []store.MetricRow{{T: t.Unix(), CPU: 200, Mem: 1e9, Tx: t.Unix() - start.Unix(), Resolution: 300}})
	}
	r.Now = func() time.Time { return later }
	r.RollUp(ctx)
	if got := snapshot(); len(got) != 8 {
		t.Errorf("after backfill: %d rows", len(got))
	}
}

func TestUsageThresholdsOncePerMonth(t *testing.T) {
	lim := func(v float64) *float64 { return &v }
	limits := &store.UsageAlert{CPUCoreHours: lim(100), EgressGB: lim(10)}
	st := store.UsageAlertState{}
	cs, st := Crossings(limits, Totals{CPUCoreHours: 50}, st, "2025-01")
	if len(cs) != 0 {
		t.Errorf("50%%: %+v", cs)
	}
	cs, st = Crossings(limits, Totals{CPUCoreHours: 85}, st, "2025-01")
	if len(cs) != 1 || cs[0].Percent != 80 || cs[0].Metric != "cpu_core_hours" {
		t.Errorf("80%%: %+v", cs)
	}
	if cs, st = Crossings(limits, Totals{CPUCoreHours: 90}, st, "2025-01"); len(cs) != 0 {
		t.Errorf("80%% again: %+v", cs)
	}
	cs, st = Crossings(limits, Totals{CPUCoreHours: 120, EgressGB: 11}, st, "2025-01")
	if len(cs) != 2 || cs[0].Percent != 100 || cs[1].Metric != "egress_gb" || cs[1].Percent != 100 {
		t.Errorf("100%%: %+v", cs)
	}
	if cs, st = Crossings(limits, Totals{CPUCoreHours: 200, EgressGB: 20}, st, "2025-01"); len(cs) != 0 {
		t.Errorf("after 100%%: %+v", cs)
	}
	// 새 달에는 다시 알린다
	if cs, _ = Crossings(limits, Totals{CPUCoreHours: 90}, st, "2025-02"); len(cs) != 1 || cs[0].Percent != 80 {
		t.Errorf("new month: %+v", cs)
	}
}

func TestCheckThresholdsEmitsEvents(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p := &store.Project{Name: "acme"}
	env, _ := st.CreateProject(ctx, p)
	v := 1.0
	p.UsageAlert = &store.UsageAlert{CPUCoreHours: &v}
	if err := st.UpdateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2025, 3, 15, 12, 30, 0, 0, time.UTC)
	st.UpsertUsage(ctx, []store.UsageRow{{ProjectID: p.ID, EnvironmentID: env.ID, ServiceID: "svc_x", Hour: now.Unix() - now.Unix()%Hour - Hour, CPUCoreSeconds: 3600 * 0.9}})
	r := &Roller{Store: st, Events: &events.Bus{Store: st}}
	r.CheckThresholds(ctx, now)
	r.CheckThresholds(ctx, now)
	evs, _ := st.ListEvents(ctx, p.ID, "", 10)
	n := 0
	for _, e := range evs {
		if e.Kind == "usage.threshold_reached" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("threshold events: %d", n)
	}
	got, _ := st.GetProject(ctx, p.ID)
	if got.UsageAlert == nil || *got.UsageAlert.CPUCoreHours != 1 {
		t.Errorf("usage_alert round trip: %+v", got.UsageAlert)
	}
}

// 시간 경계를 넘는 점은 두 시간에 나눠 들어가고, 범위를 시간 단위로 나눠 집계해도 합이 같다.
func TestAggregateSplitsAcrossHourBoundary(t *testing.T) {
	h := int64(1_700_000_000) - int64(1_700_000_000)%Hour
	disk := int64(1e9)
	// 10초 점이 경계 5초 전에 찍혔다: 5초는 h, 5초는 h+Hour
	rows := []store.MetricRow{
		{T: h + Hour - 5, CPU: 100, Mem: 1e9, Disk: &disk, Resolution: 10},
		{T: h + Hour + 5, CPU: 100, Mem: 1e9, Disk: &disk, Resolution: 10},
	}
	all := Aggregate(rows, h, h+2*Hour)
	if !near(all[h].CPUCoreSeconds, 5) || !near(all[h+Hour].CPUCoreSeconds, 15) {
		t.Fatalf("split: h=%+v h+1=%+v", all[h], all[h+Hour])
	}
	if !near(all[h].DiskGBHours+all[h+Hour].DiskGBHours, 20.0/3600) {
		t.Errorf("disk: %v + %v", all[h].DiskGBHours, all[h+Hour].DiskGBHours)
	}
	// 시간별로 따로 집계(RollRange가 시간마다 하는 것과 같다)해도 같은 값
	first, second := Aggregate(rows, h, h+Hour), Aggregate(rows, h+Hour, h+2*Hour)
	if !near(first[h].CPUCoreSeconds, 5) || !near(second[h+Hour].CPUCoreSeconds, 15) {
		t.Fatalf("separate ranges: %+v %+v", first[h], second[h+Hour])
	}
}

// 지표 점을 고르게 찍었을 때 집계 합은 범위를 어떻게 나눠도 실제 시간과 같다(경계에서 빠지는 초가 없다).
func TestAggregateConservesTime(t *testing.T) {
	h := int64(1_700_000_000) - int64(1_700_000_000)%Hour
	var rows []store.MetricRow
	for ts := h + 7; ts < h+3*Hour; ts += 10 { // 시간 경계와 어긋난 10초 간격
		rows = append(rows, store.MetricRow{T: ts, CPU: 100, Resolution: 10})
	}
	total := func(parts ...[2]int64) float64 {
		var s float64
		for _, p := range parts {
			for _, u := range Aggregate(rows, p[0], p[1]) {
				s += u.CPUCoreSeconds
			}
		}
		return s
	}
	want := float64(3*Hour - 7)
	if got := total([2]int64{h, h + 3*Hour}); !near(got, want) {
		t.Errorf("one range: %v want %v", got, want)
	}
	if got := total([2]int64{h, h + Hour}, [2]int64{h + Hour, h + 2*Hour}, [2]int64{h + 2*Hour, h + 3*Hour}); !near(got, want) {
		t.Errorf("hourly ranges: %v want %v", got, want)
	}
	// 시간 중간에서 끊는 부분 시간(MonthToDate)도 같다
	if got := total([2]int64{h, h + Hour + 1234}, [2]int64{h + Hour + 1234, h + 3*Hour}); !near(got, want) {
		t.Errorf("partial ranges: %v want %v", got, want)
	}
}

// 달이 바뀌는 순간에 걸친 점은 지난달과 이번 달에 나뉜다(이번 달 합계에 지난달 사용량이 들어가지 않는다).
func TestPartialTotalsAtMonthStart(t *testing.T) {
	ms := MonthStart(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)).Unix()
	rows := []store.MetricRow{
		{T: ms - 300, CPU: 100, Resolution: 300}, // 전부 지난달
		{T: ms - 4, CPU: 100, Resolution: 10},    // 6초만 이번 달
		{T: ms + 6, CPU: 100, Resolution: 10},
	}
	got := PartialTotals(rows, ms, ms+Hour)
	if !near(got.CPUCoreHours*float64(Hour), 16) {
		t.Fatalf("month-to-date cpu seconds: %v", got.CPUCoreHours*float64(Hour))
	}
}
