package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

// 수평 자동 확장(Kubernetes HPA 방식).
//
//	desired = ceil(current × max(metric/target))   (설정한 target들 중 가장 큰 비율)
//
// metric은 최근 2분 지표의 평균이다: CPU·메모리는 replica당 값(합계 ÷ replica 수), rps는 replica당 초당 요청 수.
// 비율이 1±10%(tolerance) 안이면 그대로 둔다. 결과는 [min, max]로 자르고, 올린 뒤 scale_up_cooldown_s,
// 마지막 변경 뒤 scale_down_cooldown_s 동안은 같은 방향으로 다시 바꾸지 않는다. 내릴 때는 scale_down_cooldown_s 동안의
// 추천 값 중 최대값까지만 내린다(stabilization window: 잠깐 줄어든 부하로 바로 내리지 않는다).

// AutoscaleInterval은 자동 확장 평가 주기다.
const AutoscaleInterval = 30 * time.Second

// AutoscaleWindow는 지표 평균 구간이다.
const AutoscaleWindow = 2 * time.Minute

// AutoscaleTolerance는 아무것도 하지 않는 비율 범위(1±tolerance)다.
const AutoscaleTolerance = 0.1

// HPAMetric은 지표 하나의 현재 값(replica당)과 목표 값이다.
type HPAMetric struct {
	Name            string
	Current, Target float64
}

// HPADesired는 현재 replica 수와 지표들로 원하는 replica 수를 계산한다(자르기 전). 가장 큰 비율과 그 지표 이름도 돌려준다.
// 목표가 0 이하인 지표는 건너뛴다. 쓸 지표가 없거나 비율이 tolerance 안이면 current다.
func HPADesired(current int, metrics []HPAMetric, tolerance float64) (int, float64, string) {
	if current < 1 {
		current = 1
	}
	ratio, name := -1.0, ""
	for _, mt := range metrics {
		if mt.Target <= 0 || math.IsNaN(mt.Current) || mt.Current < 0 {
			continue
		}
		if r := mt.Current / mt.Target; r > ratio {
			ratio, name = r, mt.Name
		}
	}
	if ratio < 0 {
		return current, 1, ""
	}
	if math.Abs(ratio-1) <= tolerance {
		return current, ratio, name
	}
	return int(math.Ceil(float64(current)*ratio - 1e-9)), ratio, name
}

// hpaRec은 시각 at의 추천 replica 수다(stabilization window용).
type hpaRec struct {
	at      time.Time
	desired int
}

// HPAState는 인스턴스 하나의 자동 확장 기록이다.
type HPAState struct {
	recs      []hpaRec
	LastScale time.Time
}

// HPADecision은 Decide의 결과다.
type HPADecision struct {
	Target int    // 바꿀 replica 수(Act가 false면 current)
	Act    bool   // 지금 바꾸는지
	Reason string // 사람이 읽는 이유(바꾸지 않을 때도)
}

// Decide는 자르기 전 추천 값 raw로 지금 바꿀 replica 수를 정한다. raw를 기록하고(stabilization) cooldown을 지킨다.
// min/max 밖에 있으면 cooldown과 상관없이 바로 범위 안으로 맞춘다.
func (s *HPAState) Decide(cfg *store.Autoscale, current, raw int, now time.Time) HPADecision {
	lo, hi := cfg.MinReplicas, cfg.MaxReplicas
	desired := clampInt(raw, lo, hi)
	down := time.Duration(cfg.ScaleDownCooldownS) * time.Second
	up := time.Duration(cfg.ScaleUpCooldownS) * time.Second
	s.recs = append(s.recs, hpaRec{at: now, desired: desired})
	keep := s.recs[:0]
	for _, r := range s.recs {
		if now.Sub(r.at) <= down {
			keep = append(keep, r)
		}
	}
	s.recs = keep
	switch {
	case current < lo:
		return HPADecision{Target: lo, Act: true, Reason: fmt.Sprintf("최소 replica %d보다 적습니다", lo)}
	case hi > 0 && current > hi:
		return HPADecision{Target: hi, Act: true, Reason: fmt.Sprintf("최대 replica %d보다 많습니다", hi)}
	case desired > current:
		if !s.LastScale.IsZero() && now.Sub(s.LastScale) < up {
			return HPADecision{Target: current, Reason: fmt.Sprintf("scale up cooldown (%s 남음)", (up - now.Sub(s.LastScale)).Round(time.Second))}
		}
		return HPADecision{Target: desired, Act: true}
	case desired < current:
		stab := desired
		for _, r := range s.recs {
			stab = max(stab, r.desired)
		}
		if stab >= current {
			return HPADecision{Target: current, Reason: "stabilization window: 최근 추천 값이 더 높아 줄이지 않습니다"}
		}
		if !s.LastScale.IsZero() && now.Sub(s.LastScale) < down {
			return HPADecision{Target: current, Reason: fmt.Sprintf("scale down cooldown (%s 남음)", (down - now.Sub(s.LastScale)).Round(time.Second))}
		}
		return HPADecision{Target: stab, Act: true}
	}
	return HPADecision{Target: current, Reason: "목표 범위 안입니다"}
}

// AutoscaleMetrics는 마지막 평가에 쓴 replica당 지표다(데이터가 없으면 nil).
type AutoscaleMetrics struct {
	CPU    *float64 `json:"cpu"`    // replica당 CPU 사용률(%). cpu_limit이 있으면 상한 대비
	Memory *float64 `json:"memory"` // replica당 메모리 사용률(상한 대비 %)
	RPS    *float64 `json:"rps"`    // replica당 초당 요청 수
}

// AutoscaleStatus는 인스턴스의 자동 확장 상태다(GET instance의 autoscale_status).
type AutoscaleStatus struct {
	Desired     *int             `json:"desired"`
	LastScaleAt *time.Time       `json:"last_scale_at"`
	LastReason  string           `json:"last_reason"` // 마지막으로 replica 수를 바꾼 이유
	Reason      string           `json:"reason"`      // 마지막 평가 결과(건너뛴 이유 포함)
	EvaluatedAt *time.Time       `json:"evaluated_at"`
	Metrics     AutoscaleMetrics `json:"metrics"`
}

type autoscaleState struct {
	mu   sync.Mutex
	inst map[string]*asEntry
}

type asEntry struct {
	hpa    HPAState
	status AutoscaleStatus
	cfgSig string // 설정이 바뀌면 기록을 지운다

	// 저장(autoscale_state): 마지막으로 쓴 내용의 서명과 시각. 서명이 바뀌거나 asPersistRefresh가 지나면 다시 쓴다.
	savedSig string
	savedAt  time.Time
	stored   bool // 저장된 행에서 읽었다
}

// asPersistRefresh는 내용(추천 값 목록, cooldown, 상태)이 그대로여도 다시 저장하는 주기다
// (추천 값 시각과 evaluated_at을 갱신한다: 재시작 뒤 stabilization window 오차가 이 값 이내다).
const asPersistRefresh = time.Minute

// persistSig는 저장 여부를 정하는 서명이다(시각만 바뀌는 값은 뺀다).
func (e *asEntry) persistSig() string {
	vals := make([]int, len(e.hpa.recs))
	for i, r := range e.hpa.recs {
		vals[i] = r.desired
	}
	d := -1
	if e.status.Desired != nil {
		d = *e.status.Desired
	}
	var last int64
	if e.status.LastScaleAt != nil {
		last = e.status.LastScaleAt.UnixNano()
	}
	return fmt.Sprintf("%s|%d|%v|%d|%d|%s|%s", e.cfgSig, e.hpa.LastScale.UnixNano(), vals, d, last, e.status.LastReason, e.status.Reason)
}

// loadASEntry는 저장된 자동 확장 기록을 읽는다(없거나 읽지 못하면 빈 기록).
func (m *Manager) loadASEntry(ctx context.Context, instanceID string) *asEntry {
	e := &asEntry{}
	if m.Store == nil {
		return e
	}
	row, err := m.Store.GetAutoscaleState(ctx, instanceID)
	if err != nil {
		return e
	}
	e.stored = true
	e.cfgSig = row.CfgSig
	if row.LastScale != nil {
		e.hpa.LastScale = *row.LastScale
	}
	for _, r := range row.Recs {
		e.hpa.recs = append(e.hpa.recs, hpaRec{at: r.At, desired: r.Desired})
	}
	if len(row.Status) > 0 {
		json.Unmarshal(row.Status, &e.status)
	}
	e.savedSig = e.persistSig()
	return e
}

// asEntry는 인스턴스의 자동 확장 기록이다. 메모리에 없으면 저장된 기록을 읽는다(lazy).
func (m *Manager) asEntry(ctx context.Context, instanceID string) *asEntry {
	m.autoscale.mu.Lock()
	e := m.autoscale.inst[instanceID]
	m.autoscale.mu.Unlock()
	if e != nil {
		return e
	}
	loaded := m.loadASEntry(ctx, instanceID)
	m.autoscale.mu.Lock()
	defer m.autoscale.mu.Unlock()
	if m.autoscale.inst == nil {
		m.autoscale.inst = map[string]*asEntry{}
	}
	if e := m.autoscale.inst[instanceID]; e != nil {
		return e
	}
	m.autoscale.inst[instanceID] = loaded
	return loaded
}

// persistAS는 기록이 바뀌었거나 asPersistRefresh가 지났으면 저장한다.
func (m *Manager) persistAS(ctx context.Context, instanceID string, e *asEntry, now time.Time) {
	if m.Store == nil {
		return
	}
	m.autoscale.mu.Lock()
	sig := e.persistSig()
	if sig == e.savedSig && now.Sub(e.savedAt) < asPersistRefresh {
		m.autoscale.mu.Unlock()
		return
	}
	row := &store.AutoscaleStateRow{InstanceID: instanceID, CfgSig: e.cfgSig}
	if !e.hpa.LastScale.IsZero() {
		t := e.hpa.LastScale
		row.LastScale = &t
	}
	for _, r := range e.hpa.recs {
		row.Recs = append(row.Recs, store.AutoscaleRec{At: r.at, Desired: r.desired})
	}
	row.Status, _ = json.Marshal(e.status)
	m.autoscale.mu.Unlock()
	if err := m.Store.PutAutoscaleState(ctx, row); err != nil {
		if m.Log != nil {
			m.Log.Warn("autoscale state", "instance", instanceID, "err", err)
		}
		return
	}
	m.autoscale.mu.Lock()
	e.savedSig, e.savedAt, e.stored = sig, now, true
	m.autoscale.mu.Unlock()
}

// AutoscaleStatusOf는 인스턴스의 자동 확장 상태다. 자동 확장이 꺼져 있으면 nil. 재시작 직후에는 저장된 상태를 보여 준다.
func (m *Manager) AutoscaleStatusOf(in *store.Instance) *AutoscaleStatus {
	if in.Autoscale == nil || !in.Autoscale.Enabled {
		return nil
	}
	m.autoscale.mu.Lock()
	e := m.autoscale.inst[in.ID]
	m.autoscale.mu.Unlock()
	if e == nil {
		e = m.asEntry(context.Background(), in.ID)
	}
	m.autoscale.mu.Lock()
	defer m.autoscale.mu.Unlock()
	if e.status.EvaluatedAt == nil && e.status.Reason == "" {
		return &AutoscaleStatus{Reason: "아직 평가하지 않았습니다"}
	}
	st := e.status
	return &st
}

// AutoscaleSkipReason은 자동 확장을 지금 하지 않는 이유다(없으면 ""): 배포 중, 잠듦/cron/중지, 볼륨이 붙은 서비스.
func AutoscaleSkipReason(in *store.Instance, hasVolumes, deploying bool) string {
	switch {
	case in.Autoscale == nil || !in.Autoscale.Enabled:
		return "자동 확장이 꺼져 있습니다"
	case hasVolumes:
		return "볼륨이 붙은 서비스는 자동 확장하지 않습니다(여러 replica가 같은 데이터를 씁니다)"
	case deploying:
		return "배포가 진행 중입니다"
	}
	return scaleBlocker(in)
}

// AutoscaleLoop은 AutoscaleInterval마다 자동 확장을 평가한다.
func (m *Manager) AutoscaleLoop(ctx context.Context) {
	t := time.NewTicker(AutoscaleInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		m.AutoscaleOnce(ctx, time.Now())
	}
}

// AutoscaleOnce는 자동 확장이 켜진 인스턴스를 한 번씩 평가하고 필요하면 replica 수를 바꾼다.
func (m *Manager) AutoscaleOnce(ctx context.Context, now time.Time) {
	insts, err := m.Store.ListInstances(ctx)
	if err != nil {
		return
	}
	live := map[string]bool{}
	for _, in := range insts {
		if in.Autoscale == nil || !in.Autoscale.Enabled {
			continue
		}
		live[in.ID] = true
		m.autoscaleInstance(ctx, in, now)
	}
	var gone []string
	m.autoscale.mu.Lock()
	for id, e := range m.autoscale.inst {
		if !live[id] {
			delete(m.autoscale.inst, id)
			if e.stored {
				gone = append(gone, id)
			}
		}
	}
	m.autoscale.mu.Unlock()
	// 자동 확장을 끈(또는 지운) 인스턴스의 저장된 기록도 지운다.
	for _, id := range gone {
		m.Store.DeleteAutoscaleState(ctx, id)
	}
}

func (m *Manager) autoscaleInstance(ctx context.Context, in *store.Instance, now time.Time) {
	e := m.asEntry(ctx, in.ID)
	defer m.persistAS(ctx, in.ID, e, now)
	cfg := *in.Autoscale
	cfg.Defaults()
	sig := fmt.Sprintf("%+v", cfg)
	setStatus := func(f func(st *AutoscaleStatus)) {
		m.autoscale.mu.Lock()
		if e.cfgSig != sig {
			e.cfgSig, e.hpa.recs = sig, nil
		}
		t := now.UTC()
		e.status.EvaluatedAt = &t
		f(&e.status)
		m.autoscale.mu.Unlock()
	}
	vols, err := m.Store.ListVolumesByInstance(ctx, in.ID)
	if err != nil {
		return
	}
	if reason := AutoscaleSkipReason(in, len(vols) > 0, m.deploymentInProgress(ctx, in.ID)); reason != "" {
		setStatus(func(st *AutoscaleStatus) { st.Reason, st.Desired = reason, nil })
		return
	}
	d, err := m.Store.GetDeployment(ctx, *in.CurrentDeploymentID)
	if err != nil || d.Status != store.DepLive {
		setStatus(func(st *AutoscaleStatus) { st.Reason = "현재 배포가 live가 아닙니다" })
		return
	}
	current := len(d.AllContainers())
	mt, metrics := m.autoscaleMetrics(ctx, in, d, current, &cfg, now)
	raw, ratio, which := HPADesired(current, metrics, AutoscaleTolerance)
	var dec HPADecision
	m.autoscale.mu.Lock()
	if e.cfgSig != sig {
		e.cfgSig, e.hpa.recs = sig, nil
	}
	if len(metrics) == 0 && current >= cfg.MinReplicas && current <= cfg.MaxReplicas {
		dec = HPADecision{Target: current, Reason: "지표가 아직 없습니다"}
	} else {
		dec = e.hpa.Decide(&cfg, current, raw, now)
	}
	m.autoscale.mu.Unlock()
	reason := dec.Reason
	if dec.Act {
		reason = scaleReason(current, dec.Target, which, ratio, metrics)
		if current < cfg.MinReplicas || current > cfg.MaxReplicas {
			reason = "자동 확장: " + dec.Reason
		}
	}
	desired := dec.Target
	setStatus(func(st *AutoscaleStatus) { st.Desired, st.Metrics, st.Reason = &desired, mt, reason })
	if !dec.Act || dec.Target == current {
		return
	}
	if _, err := m.ScaleReplicas(ctx, in, dec.Target, reason); err != nil {
		msg := err.Error()
		if !errors.Is(err, ErrScaleUnavailable) && m.Log != nil {
			m.Log.Warn("autoscale", "instance", in.ID, "err", err)
		}
		setStatus(func(st *AutoscaleStatus) { st.Reason = "replica 변경 실패: " + msg })
		return
	}
	setStatus(func(st *AutoscaleStatus) {
		t := now.UTC()
		st.LastScaleAt, st.LastReason, st.Reason = &t, reason, reason
	})
	m.autoscale.mu.Lock()
	e.hpa.LastScale = now
	m.autoscale.mu.Unlock()
}

func scaleReason(from, to int, which string, ratio float64, metrics []HPAMetric) string {
	dir := "확장"
	if to < from {
		dir = "축소"
	}
	var parts []string
	for _, mt := range metrics {
		parts = append(parts, fmt.Sprintf("%s %.1f/%g", mt.Name, mt.Current, mt.Target))
	}
	sort.Strings(parts)
	s := fmt.Sprintf("자동 %s: %s", dir, strings.Join(parts, ", "))
	if which != "" {
		s += fmt.Sprintf(" (%s 기준 ×%.2f)", which, ratio)
	}
	return s
}

// autoscaleMetrics는 최근 AutoscaleWindow 동안의 replica당 지표 평균이다. 설정된 target 중 데이터가 있는 것만 HPAMetric으로 돌려준다.
func (m *Manager) autoscaleMetrics(ctx context.Context, in *store.Instance, d *store.Deployment, current int, cfg *store.Autoscale, now time.Time) (AutoscaleMetrics, []HPAMetric) {
	var out AutoscaleMetrics
	rows, _ := m.Store.QueryMetrics(ctx, in.ID, now.Add(-AutoscaleWindow).Unix())
	cpu, mem := AverageReplicaUsage(rows, d.Spec.CPULimit)
	out.CPU, out.Memory = cpu, mem
	if m.HTTPLogs != nil && current > 0 {
		r := m.HTTPLogs.RequestRate(in.ID, AutoscaleWindow) / float64(current)
		out.RPS = &r
	}
	var ms []HPAMetric
	if cfg.TargetCPUPercent != nil && cpu != nil {
		ms = append(ms, HPAMetric{Name: "cpu", Current: *cpu, Target: *cfg.TargetCPUPercent})
	}
	if cfg.TargetMemoryPercent != nil && mem != nil {
		ms = append(ms, HPAMetric{Name: "memory", Current: *mem, Target: *cfg.TargetMemoryPercent})
	}
	if cfg.TargetRPSPerReplica != nil && out.RPS != nil {
		ms = append(ms, HPAMetric{Name: "rps", Current: *out.RPS, Target: *cfg.TargetRPSPerReplica})
	}
	return out, ms
}

// AverageReplicaUsage는 지표 점들의 replica당 CPU(%)와 메모리 사용률(%) 평균이다. 점의 CPU/메모리는 replica 합계다.
// cpuLimit(코어 수)이 있으면 CPU는 상한 대비 %다(0.5코어 상한에서 50% = 100%). 데이터가 없으면 nil.
func AverageReplicaUsage(rows []store.MetricRow, cpuLimit *float64) (cpu, mem *float64) {
	var cs, ms float64
	var cn, mn int
	for _, r := range rows {
		if r.Replicas <= 0 {
			continue
		}
		c := r.CPU / float64(r.Replicas)
		if cpuLimit != nil && *cpuLimit > 0 {
			c /= *cpuLimit
		}
		cs += c
		cn++
		if r.MemLimit > 0 {
			ms += float64(r.Mem) * 100 / float64(r.MemLimit)
			mn++
		}
	}
	if cn > 0 {
		v := cs / float64(cn)
		cpu = &v
	}
	if mn > 0 {
		v := ms / float64(mn)
		mem = &v
	}
	return cpu, mem
}
