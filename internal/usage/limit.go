package usage

// 프로젝트 월간 사용량 한도(hard limit). usage_alert(알림만)와 달리 action=stop이면 한도에 닿을 때
// 프로젝트의 실행 중인 서비스를 모두 멈추고(삭제 아님) 새 배포를 막는다(deploy.Manager.Enqueue가 409용 에러를 낸다).
//
// 한도는 매시 집계 뒤와 LimitInterval(5분)마다 검사한다. 이번 달 사용량 = 집계가 끝난 시간(usage_hourly, watermark 전)
// + watermark 이후 아직 집계하지 않은 부분 시간(지표 점에서 바로 계산)이라 초과량은 대략 5분치 사용량으로 묶인다.
//
// 정지는 다음 달(UTC) 1일이 되거나 관리자가 한도를 올리거나/지우거나 action을 notify로 바꾸면 풀린다(자동 해제).
// 풀려도 멈춘 서비스는 자동으로 다시 시작하지 않는다. POST /projects/{pid}/usage/resume이 정지를 지우고 멈췄던 서비스를 다시 배포한다.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

// LimitInterval은 집계 사이에 한도를 검사하는 주기다.
const LimitInterval = 5 * time.Minute

// InstanceController는 한도 정지/재개에 쓰는 인스턴스 작업이다(deploy.Manager).
type InstanceController interface {
	// Stop은 인스턴스를 멈춘다(배포·데이터는 남는다).
	Stop(ctx context.Context, in *store.Instance) error
	// StartAgain은 멈춘 인스턴스를 현재 배포(없으면 설정)로 다시 띄운다.
	StartAgain(ctx context.Context, in *store.Instance) (*store.Deployment, error)
}

var (
	// ErrNotSuspended는 resume할 정지 기록이 없을 때다.
	ErrNotSuspended = errors.New("사용량 한도로 정지된 프로젝트가 아닙니다")
	// ErrStillOverLimit은 한도를 넘은 채로 resume하려 할 때다.
	ErrStillOverLimit = errors.New("이번 달 사용량이 아직 한도를 넘었습니다. 한도를 올리거나 지운 뒤 다시 시도하세요")
)

// limitMu는 한도 검사·정지·재개를 한 번에 하나씩 한다(Roller 고루틴과 API 요청이 같은 프로젝트를 동시에 바꾸지 않게).
var limitMu sync.Mutex

// Exceeded는 한도에 닿은(사용량 ≥ 한도) 항목 이름이다(cpu_core_hours, memory_gb_hours, egress_gb 순서).
func Exceeded(l *store.UsageLimit, t Totals) []string {
	if l.Empty() {
		return nil
	}
	var out []string
	check := func(metric string, limit *float64, used float64) {
		if limit != nil && *limit > 0 && used >= *limit {
			out = append(out, metric)
		}
	}
	check("cpu_core_hours", l.CPUCoreHours, t.CPUCoreHours)
	check("memory_gb_hours", l.MemoryGBHours, t.MemoryGBHours)
	check("egress_gb", l.EgressGB, t.EgressGB)
	return out
}

func limitValue(l *store.UsageLimit, metric string) float64 {
	var p *float64
	switch metric {
	case "cpu_core_hours":
		p = l.CPUCoreHours
	case "memory_gb_hours":
		p = l.MemoryGBHours
	case "egress_gb":
		p = l.EgressGB
	}
	if p == nil {
		return 0
	}
	return *p
}

func usedValue(t Totals, metric string) float64 {
	switch metric {
	case "cpu_core_hours":
		return t.CPUCoreHours
	case "memory_gb_hours":
		return t.MemoryGBHours
	case "egress_gb":
		return t.EgressGB
	}
	return 0
}

// PartialTotals는 지표 점에서 [from, to) 사용량을 바로 계산한다(아직 시간별 집계가 없는 부분 시간용).
func PartialTotals(rows []store.MetricRow, from, to int64) Totals {
	var t Totals
	for _, u := range Aggregate(rows, from, to) {
		t.Add(*u)
	}
	return t
}

func (r *Roller) watermark(ctx context.Context) int64 {
	v, err := r.Store.GetSetting(ctx, watermarkKey)
	if err != nil {
		return 0
	}
	w, _ := strconv.ParseInt(v, 10, 64)
	return w
}

// MonthToDate는 프로젝트의 이번 달(UTC) 사용량이다: 집계가 끝난 시간(watermark 전) + 그 뒤 지금까지의 부분 시간(지표 점).
func (r *Roller) MonthToDate(ctx context.Context, projectID string, now time.Time) (Totals, error) {
	var t Totals
	ms, end := MonthStart(now).Unix(), now.Unix()
	w := r.watermark(ctx)
	w = min(max(w, ms), end)
	rows, err := r.Store.QueryUsage(ctx, projectID, ms, w)
	if err != nil {
		return t, err
	}
	for _, u := range rows {
		t.Add(u)
	}
	if w >= end {
		return t, nil
	}
	ins, err := r.projectInstances(ctx, projectID)
	if err != nil {
		return t, err
	}
	for _, in := range ins {
		mr, err := r.Store.QueryMetrics(ctx, in.ID, w-600) // 송신량 기준값
		if err != nil {
			return t, err
		}
		p := PartialTotals(mr, w, end+1)
		t.CPUCoreHours += p.CPUCoreHours
		t.MemoryGBHours += p.MemoryGBHours
		t.EgressGB += p.EgressGB
		t.DiskGBHours += p.DiskGBHours
	}
	return t, nil
}

func (r *Roller) projectInstances(ctx context.Context, projectID string) ([]*store.Instance, error) {
	svcs, err := r.Store.ListServices(ctx, projectID)
	if err != nil {
		return nil, err
	}
	own := map[string]bool{}
	for _, s := range svcs {
		own[s.ID] = true
	}
	all, err := r.Store.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	var out []*store.Instance
	for _, in := range all {
		if own[in.ServiceID] {
			out = append(out, in)
		}
	}
	return out, nil
}

// running은 한도 정지 때 멈출(실행 중이거나 곧 실행될) 인스턴스인지다.
func running(in *store.Instance) bool {
	switch in.Status {
	case store.InstOffline, store.InstIdle:
		return false
	case store.InstFailed:
		return in.CurrentDeploymentID != nil // 실패한 새 배포 뒤에도 이전 배포가 떠 있을 수 있다
	}
	return true
}

// CheckLimits는 한도가 있거나 정지 기록이 있는 모든 프로젝트를 검사한다.
func (r *Roller) CheckLimits(ctx context.Context, now time.Time) {
	pls, err := r.Store.ListUsageLimits(ctx)
	if err != nil {
		if r.Log != nil && ctx.Err() == nil {
			r.Log.Warn("usage limits", "err", err)
		}
		return
	}
	for _, pl := range pls {
		if err := r.CheckProject(ctx, pl.ProjectID, now); err != nil && r.Log != nil && ctx.Err() == nil {
			r.Log.Warn("usage limit", "project", pl.ProjectID, "err", err)
		}
	}
}

// CheckProject는 프로젝트 하나의 한도를 검사한다: 처음 닿은 항목마다 usage.limit_reached(달마다 한 번),
// action=stop이면 정지(실행 중인 서비스를 멈춤), 더는 넘지 않거나 달이 바뀌었으면 정지를 푼다(서비스는 다시 시작하지 않는다).
// 한도를 바꾼 직후(API)에도 부른다.
func (r *Roller) CheckProject(ctx context.Context, projectID string, now time.Time) error {
	limitMu.Lock()
	defer limitMu.Unlock()
	pl, err := r.Store.GetUsageLimit(ctx, projectID)
	if err != nil {
		return err
	}
	month := now.UTC().Format("2006-01")
	var t Totals
	var ex []string
	if !pl.Limit.Empty() {
		if t, err = r.MonthToDate(ctx, projectID, now); err != nil {
			return err
		}
		ex = Exceeded(pl.Limit, t)
	}
	stop := pl.Limit != nil && pl.Limit.Action == store.UsageLimitStop && len(ex) > 0
	sp := pl.Suspension
	// 정지 해제: 달이 바뀌었거나, 한도를 올리거나/지웠거나, notify로 바꿨다.
	if sp.ActiveAt(now) && !stop {
		lifted := now.UTC()
		sp.LiftedAt = &lifted
		if err := r.Store.SetUsageSuspension(ctx, projectID, sp); err != nil {
			return err
		}
		r.emit(ctx, projectID, "usage.unsuspended", fmt.Sprintf("사용량 한도 정지가 풀렸습니다. 멈춘 서비스 %d개는 자동으로 다시 시작하지 않습니다(사용량 페이지에서 다시 시작)", len(sp.StoppedInstances)))
	} else if sp != nil && sp.LiftedAt == nil && !sp.ActiveAt(now) {
		lifted := now.UTC()
		sp.LiftedAt = &lifted
		if err := r.Store.SetUsageSuspension(ctx, projectID, sp); err != nil {
			return err
		}
		r.emit(ctx, projectID, "usage.unsuspended", fmt.Sprintf("새 달이 시작되어 사용량 한도 정지가 풀렸습니다. 멈춘 서비스 %d개는 자동으로 다시 시작하지 않습니다(사용량 페이지에서 다시 시작)", len(sp.StoppedInstances)))
	}
	// 처음 닿은 항목
	reached := []string{}
	if pl.ReachedMonth == month {
		reached = append(reached, pl.Reached...)
	}
	var fresh []string
	for _, m := range ex {
		if !slices.Contains(reached, m) {
			fresh = append(fresh, m)
			reached = append(reached, m)
		}
	}
	if len(fresh) > 0 || (pl.ReachedMonth != month && pl.ReachedMonth != "") {
		if err := r.Store.SetUsageLimitReached(ctx, projectID, month, reached); err != nil {
			return err
		}
	}
	stopped := 0
	if stop {
		if stopped, err = r.suspend(ctx, projectID, pl, t, ex, now); err != nil {
			return err
		}
	}
	for _, m := range fresh {
		msg := fmt.Sprintf("%s 이번 달 %s 사용량이 한도에 도달했습니다 (%.2f / %.2f)", month, metricLabel[m], usedValue(t, m), limitValue(pl.Limit, m))
		if pl.Limit.Action == store.UsageLimitStop {
			msg += fmt.Sprintf(". 프로젝트를 정지하고 서비스 %d개를 멈췄습니다(다음 달까지 또는 한도를 올릴 때까지 새 배포 불가)", stopped)
		}
		r.emit(ctx, projectID, "usage.limit_reached", msg)
	}
	return nil
}

// suspend는 정지 기록을 남기고(먼저: 새 배포를 막는다) 실행 중인 인스턴스를 멈춘다. 이미 정지 중이면 그 사이 떠 버린
// 인스턴스(정지 직전에 큐에 들어간 배포 등)만 멈춘다. 멈춘 인스턴스 수를 돌려준다.
func (r *Roller) suspend(ctx context.Context, projectID string, pl *store.ProjectUsageLimit, t Totals, ex []string, now time.Time) (int, error) {
	sp := pl.Suspension
	if !sp.ActiveAt(now) {
		var reasons []string
		for _, m := range ex {
			reasons = append(reasons, fmt.Sprintf("%s %.2f / %.2f", metricLabel[m], usedValue(t, m), limitValue(pl.Limit, m)))
		}
		ms := MonthStart(now)
		next := &store.UsageSuspension{Month: ms.Format("2006-01"), Reason: "이번 달 사용량 한도에 도달했습니다: " + strings.Join(reasons, ", "),
			SuspendedAt: now.UTC(), Until: ms.AddDate(0, 1, 0)}
		if sp != nil {
			next.StoppedInstances = sp.StoppedInstances // 아직 다시 시작하지 않은 지난 정지의 서비스도 resume 대상으로 둔다
		}
		sp = next
		if err := r.Store.SetUsageSuspension(ctx, projectID, sp); err != nil {
			return 0, err
		}
	}
	ins, err := r.projectInstances(ctx, projectID)
	if err != nil {
		return 0, err
	}
	n := 0
	changed := false
	for _, in := range ins {
		if !running(in) {
			continue
		}
		if r.Instances != nil {
			if err := r.Instances.Stop(ctx, in); err != nil {
				if r.Log != nil {
					r.Log.Warn("usage limit stop", "instance", in.ID, "err", err)
				}
				continue
			}
		}
		n++
		if !slices.Contains(sp.StoppedInstances, in.ID) {
			sp.StoppedInstances = append(sp.StoppedInstances, in.ID)
			changed = true
		}
	}
	if changed {
		if err := r.Store.SetUsageSuspension(ctx, projectID, sp); err != nil {
			return n, err
		}
	}
	return n, nil
}

// ResumeResult는 Resume의 결과다.
type ResumeResult struct {
	Restarted []string          `json:"restarted"` // 다시 배포를 넣은 인스턴스
	Failed    map[string]string `json:"failed"`    // 인스턴스 → 실패 이유
}

// Resume은 정지 기록을 지우고 정지 때 멈춘 인스턴스(그 뒤 다른 방법으로 다시 띄우지 않은 것)를 다시 배포한다.
// 정지가 아직 유효한데 한도를 넘은 상태면 ErrStillOverLimit(한도를 먼저 올리거나 지워야 한다).
func (r *Roller) Resume(ctx context.Context, projectID string, now time.Time) (*ResumeResult, error) {
	limitMu.Lock()
	defer limitMu.Unlock()
	pl, err := r.Store.GetUsageLimit(ctx, projectID)
	if err != nil {
		return nil, err
	}
	sp := pl.Suspension
	if sp == nil {
		return nil, ErrNotSuspended
	}
	if pl.Limit != nil && pl.Limit.Action == store.UsageLimitStop {
		t, err := r.MonthToDate(ctx, projectID, now)
		if err != nil {
			return nil, err
		}
		if len(Exceeded(pl.Limit, t)) > 0 {
			return nil, ErrStillOverLimit
		}
	}
	if err := r.Store.SetUsageSuspension(ctx, projectID, nil); err != nil {
		return nil, err
	}
	res := &ResumeResult{Restarted: []string{}, Failed: map[string]string{}}
	for _, id := range sp.StoppedInstances {
		in, err := r.Store.GetInstance(ctx, id)
		if err != nil || in.Status != store.InstOffline {
			continue // 지워졌거나 이미 다시 떠 있다
		}
		if r.Instances == nil {
			res.Failed[id] = "배포 관리자가 없습니다"
			continue
		}
		if _, err := r.Instances.StartAgain(ctx, in); err != nil {
			res.Failed[id] = err.Error()
			continue
		}
		res.Restarted = append(res.Restarted, id)
	}
	msg := fmt.Sprintf("사용량 한도 정지를 해제하고 서비스 %d개를 다시 시작합니다", len(res.Restarted))
	if len(res.Failed) > 0 {
		msg += fmt.Sprintf(" (%d개 실패)", len(res.Failed))
	}
	r.emit(ctx, projectID, "usage.resumed", msg)
	return res, nil
}

func (r *Roller) emit(ctx context.Context, projectID, kind, msg string) {
	if r.Events != nil {
		r.Events.Emit(ctx, projectID, nil, nil, kind, msg)
	}
}
