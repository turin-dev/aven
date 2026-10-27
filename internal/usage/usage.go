// Package usage는 지표 점(internal/metrics, 7일 보관)을 시간별 사용량(usage_hourly, 400일 보관)으로 집계하고,
// 기간별 합계·비용 추정과 프로젝트 월간 사용량 알림을 계산한다.
package usage

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/turin-dev/aven/internal/compat"
	"github.com/turin-dev/aven/internal/events"
	"github.com/turin-dev/aven/internal/store"
)

const (
	Hour = int64(3600)
	// SampleRetention은 지표 점 보관 기간이다(metrics.Retention과 같다). 이보다 오래된 시간은 다시 집계할 수 없다.
	SampleRetention = 7 * 24 * time.Hour
	// Retention은 시간별 집계 보관 기간이다.
	Retention = 400 * 24 * time.Hour
	// GB는 사용량 단위(10^9 bytes)다.
	GB = 1e9
	// watermarkKey는 마지막으로 집계를 끝낸 시간(unix, 그 시간 전까지)을 저장하는 settings 키다.
	watermarkKey = "usage_rollup_until"
)

// Aggregate는 한 인스턴스의 점(시간순)을 [from, to) 안의 시간별 사용량으로 묶는다.
// 점 하나는 자기 해상도(res초) 동안의 평균으로 본다. 네트워크 송신량은 누적 카운터의 증가분이다
// (from 이전의 점은 기준값으로만 쓰고, 카운터가 줄면(컨테이너 재시작) 새 값 전체를 증가분으로 본다).
func Aggregate(rows []store.MetricRow, from, to int64) map[int64]*store.UsageRow {
	out := map[int64]*store.UsageRow{}
	var prevTx int64
	havePrev := false
	for _, r := range rows {
		if r.T >= to {
			break
		}
		var egress int64
		if havePrev {
			if r.Tx >= prevTx {
				egress = r.Tx - prevTx
			} else {
				egress = r.Tx
			}
		}
		prevTx, havePrev = r.Tx, true
		if r.T < from {
			continue
		}
		h := r.T - r.T%Hour
		u := out[h]
		if u == nil {
			u = &store.UsageRow{Hour: h}
			out[h] = u
		}
		w := float64(r.Resolution)
		if w <= 0 {
			w = 10
		}
		if end := float64(h + Hour - r.T); w > end {
			w = end // 시간 경계를 넘는 부분은 자른다
		}
		u.CPUCoreSeconds += r.CPU / 100 * w
		u.MemGBSeconds += float64(r.Mem) / GB * w
		u.EgressBytes += egress
		if r.Disk != nil {
			u.DiskGBHours += float64(*r.Disk) / GB * w / float64(Hour)
		}
	}
	return out
}

// Roller는 시간별 집계 작업이다.
type Roller struct {
	Store  *store.Store
	Events *events.Bus
	Log    *slog.Logger
	Now    func() time.Time
	// Instances는 사용량 한도(action=stop) 정지와 resume에 쓴다(nil이면 기록만 하고 인스턴스는 건드리지 않는다).
	Instances InstanceController
}

func (r *Roller) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Run은 시작할 때와 매시 5분쯤 집계한다(놓친 시간은 보관 중인 점으로 채운다). 그 사이에는 LimitInterval마다
// 부분 시간 사용량으로 한도를 검사한다.
func (r *Roller) Run(ctx context.Context) {
	var nextRoll time.Time
	for {
		now := r.now()
		if !now.Before(nextRoll) {
			if err := r.RollUp(ctx); err != nil && ctx.Err() == nil && r.Log != nil {
				r.Log.Warn("usage rollup", "err", err)
			}
			now = r.now()
			nextRoll = now.Truncate(time.Hour).Add(time.Hour + 5*time.Minute)
		} else {
			r.CheckLimits(ctx, now)
		}
		wait := min(LimitInterval, nextRoll.Sub(now))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// RollUp은 마지막 집계 이후 끝난 시간을 모두 집계한다(최대 SampleRetention 전부터). 같은 시간을 다시 집계해도 결과가 같다.
func (r *Roller) RollUp(ctx context.Context) error {
	now := r.now()
	to := now.Unix() - now.Unix()%Hour
	oldest := now.Add(-SampleRetention).Unix()
	oldest = oldest - oldest%Hour + Hour // 앞부분이 잘렸을 수 있는 시간은 건너뛴다
	from := oldest
	if v, err := r.Store.GetSetting(ctx, watermarkKey); err == nil {
		if w, err := strconv.ParseInt(v, 10, 64); err == nil && w > from {
			from = w
		}
	}
	if from < to {
		if err := r.RollRange(ctx, from, to); err != nil {
			return err
		}
		if err := r.Store.SetSetting(ctx, watermarkKey, strconv.FormatInt(to, 10)); err != nil {
			return err
		}
	}
	r.Store.PruneUsage(ctx, now.Add(-Retention).Unix())
	r.CheckThresholds(ctx, now)
	r.CheckLimits(ctx, now)
	return nil
}

// RollRange는 [from, to) 시간을 집계해 저장한다.
func (r *Roller) RollRange(ctx context.Context, from, to int64) error {
	ins, err := r.Store.ListInstances(ctx)
	if err != nil {
		return err
	}
	svcProject := map[string]string{}
	for _, in := range ins {
		pid, ok := svcProject[in.ServiceID]
		if !ok {
			svc, err := r.Store.GetService(ctx, in.ServiceID)
			if err != nil {
				continue
			}
			pid = svc.ProjectID
			svcProject[in.ServiceID] = pid
		}
		rows, err := r.Store.QueryMetrics(ctx, in.ID, from-600) // 송신량 기준값
		if err != nil {
			return err
		}
		agg := Aggregate(rows, from, to)
		if len(agg) == 0 {
			continue
		}
		out := make([]store.UsageRow, 0, len(agg))
		for _, u := range agg {
			u.ProjectID, u.EnvironmentID, u.ServiceID = pid, in.EnvironmentID, in.ServiceID
			out = append(out, *u)
		}
		if err := r.Store.UpsertUsage(ctx, out); err != nil {
			return err
		}
	}
	return nil
}

// Totals는 기간 합계다(사람이 읽는 단위).
type Totals struct {
	CPUCoreHours  float64 `json:"cpu_core_hours"`
	MemoryGBHours float64 `json:"memory_gb_hours"`
	EgressGB      float64 `json:"net_egress_gb"`
	DiskGBHours   float64 `json:"disk_gb_hours"`
	Cost          *Cost   `json:"estimated_cost,omitempty"`
}

func (t *Totals) Add(u store.UsageRow) {
	t.CPUCoreHours += u.CPUCoreSeconds / float64(Hour)
	t.MemoryGBHours += u.MemGBSeconds / float64(Hour)
	t.EgressGB += float64(u.EgressBytes) / GB
	t.DiskGBHours += u.DiskGBHours
}

// Pricing은 단가다(KEEL_PRICE_*). 설정한 항목이 하나도 없으면 nil로 둔다.
type Pricing struct {
	VCPUHour    float64 `json:"vcpu_hour"`
	GBRAMHour   float64 `json:"gb_ram_hour"`
	GBEgress    float64 `json:"gb_egress"`
	GBDiskMonth float64 `json:"gb_disk_month"`
}

// Cost는 단가로 계산한 추정 비용이다(통화 단위는 단가를 따른다).
type Cost struct {
	CPU    float64 `json:"cpu"`
	Memory float64 `json:"memory"`
	Egress float64 `json:"egress"`
	Disk   float64 `json:"disk"`
	Total  float64 `json:"total"`
}

// HoursPerMonth는 디스크 월 단가를 시간 단가로 바꾸는 값이다(365×24/12).
const HoursPerMonth = 730

// PricingFromEnv는 KEEL_PRICE_VCPU_HOUR, KEEL_PRICE_GB_RAM_HOUR, KEEL_PRICE_GB_EGRESS, KEEL_PRICE_GB_DISK_MONTH를 읽는다.
func PricingFromEnv(getenv func(string) string) (*Pricing, error) {
	var p Pricing
	set := false
	for _, f := range []struct {
		key string
		dst *float64
	}{{"PRICE_VCPU_HOUR", &p.VCPUHour}, {"PRICE_GB_RAM_HOUR", &p.GBRAMHour}, {"PRICE_GB_EGRESS", &p.GBEgress}, {"PRICE_GB_DISK_MONTH", &p.GBDiskMonth}} {
		v := compat.From(getenv, f.key)
		if v == "" {
			continue
		}
		x, err := strconv.ParseFloat(v, 64)
		if err != nil || x < 0 {
			return nil, fmt.Errorf("%s must be a non-negative number", compat.Describe(f.key))
		}
		*f.dst, set = x, true
	}
	if !set {
		return nil, nil
	}
	return &p, nil
}

// Apply는 단가가 있으면 합계에 추정 비용을 붙인다.
func (t *Totals) Apply(p *Pricing) {
	if p == nil {
		return
	}
	c := &Cost{CPU: t.CPUCoreHours * p.VCPUHour, Memory: t.MemoryGBHours * p.GBRAMHour, Egress: t.EgressGB * p.GBEgress, Disk: t.DiskGBHours * p.GBDiskMonth / HoursPerMonth}
	c.Total = c.CPU + c.Memory + c.Egress + c.Disk
	t.Cost = c
}

// Group은 묶음 하나의 합계다.
type Group struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Totals
}

// SeriesPoint는 하루치 합계다.
type SeriesPoint struct {
	T string `json:"t"` // YYYY-MM-DD (UTC)
	Totals
}

// Summarize는 집계 행을 합계, 묶음(keyFn), 일별 series로 만든다.
func Summarize(rows []store.UsageRow, keyFn func(store.UsageRow) string, p *Pricing) (Totals, []Group, []SeriesPoint) {
	var total Totals
	groups := map[string]*Group{}
	days := map[string]*SeriesPoint{}
	for _, u := range rows {
		total.Add(u)
		k := keyFn(u)
		g := groups[k]
		if g == nil {
			g = &Group{Key: k}
			groups[k] = g
		}
		g.Add(u)
		d := time.Unix(u.Hour, 0).UTC().Format("2006-01-02")
		sp := days[d]
		if sp == nil {
			sp = &SeriesPoint{T: d}
			days[d] = sp
		}
		sp.Add(u)
	}
	total.Apply(p)
	gs := make([]Group, 0, len(groups))
	for _, g := range groups {
		g.Apply(p)
		gs = append(gs, *g)
	}
	sort.Slice(gs, func(i, j int) bool { return gs[i].Key < gs[j].Key })
	ss := make([]SeriesPoint, 0, len(days))
	for _, s := range days {
		s.Apply(p)
		ss = append(ss, *s)
	}
	sort.Slice(ss, func(i, j int) bool { return ss[i].T < ss[j].T })
	return total, gs, ss
}

// Crossing은 이번에 새로 넘은 기준이다.
type Crossing struct {
	Metric  string
	Percent int // 80 | 100
	Used    float64
	Limit   float64
}

// Crossings는 월간 합계가 기준의 80%/100%를 새로 넘은 항목과 갱신된 상태다(달이 바뀌면 상태를 비운다).
func Crossings(limits *store.UsageAlert, t Totals, st store.UsageAlertState, month string) ([]Crossing, store.UsageAlertState) {
	if st.Month != month || st.Sent == nil {
		st = store.UsageAlertState{Month: month, Sent: map[string]int{}}
	}
	if limits.Empty() {
		return nil, st
	}
	var out []Crossing
	check := func(metric string, limit *float64, used float64) {
		if limit == nil || *limit <= 0 {
			return
		}
		pct := 0
		switch {
		case used >= *limit:
			pct = 100
		case used >= *limit*0.8:
			pct = 80
		}
		if pct > st.Sent[metric] {
			st.Sent[metric] = pct
			out = append(out, Crossing{Metric: metric, Percent: pct, Used: used, Limit: *limit})
		}
	}
	check("cpu_core_hours", limits.CPUCoreHours, t.CPUCoreHours)
	check("memory_gb_hours", limits.MemoryGBHours, t.MemoryGBHours)
	check("egress_gb", limits.EgressGB, t.EgressGB)
	return out, st
}

var metricLabel = map[string]string{"cpu_core_hours": "CPU(코어·시간)", "memory_gb_hours": "메모리(GB·시간)", "egress_gb": "송신 트래픽(GB)"}

// MonthStart는 t가 속한 UTC 달력 월의 시작이다.
func MonthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// CheckThresholds는 사용량 알림 기준이 있는 프로젝트의 이번 달 사용량을 보고 넘은 기준마다 이벤트를 한 번 낸다.
func (r *Roller) CheckThresholds(ctx context.Context, now time.Time) {
	ps, err := r.Store.ListProjects(ctx)
	if err != nil {
		return
	}
	ms := MonthStart(now)
	month := ms.Format("2006-01")
	for _, p := range ps {
		if p.UsageAlert.Empty() {
			continue
		}
		rows, err := r.Store.QueryUsage(ctx, p.ID, ms.Unix(), now.Unix()+Hour)
		if err != nil {
			continue
		}
		var t Totals
		for _, u := range rows {
			t.Add(u)
		}
		st, err := r.Store.GetUsageAlertState(ctx, p.ID)
		if err != nil {
			continue
		}
		cs, next := Crossings(p.UsageAlert, t, st, month)
		if len(cs) == 0 && next.Month == st.Month {
			continue
		}
		if err := r.Store.SetUsageAlertState(ctx, p.ID, next); err != nil {
			continue
		}
		for _, c := range cs {
			if r.Events == nil {
				break
			}
			msg := fmt.Sprintf("%s 이번 달 %s 사용량이 기준의 %d%%에 도달했습니다 (%.2f / %.2f)", month, metricLabel[c.Metric], c.Percent, c.Used, c.Limit)
			r.Events.Emit(ctx, p.ID, nil, nil, "usage.threshold_reached", msg)
		}
	}
}
