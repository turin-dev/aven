// Package alerts는 지표 알림 규칙을 30초마다 평가해 alert.firing / alert.resolved 이벤트를 낸다.
package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/events"
	"github.com/turin-dev/aven/internal/store"
)

// Interval은 평가 주기다.
const Interval = 30 * time.Second

// Metrics는 규칙에 쓸 수 있는 지표다.
var Metrics = map[string]string{
	"cpu_percent":    "CPU 사용률(%)",
	"memory_percent": "메모리 사용률(상한 대비 %)",
	"memory_bytes":   "메모리(bytes)",
	"disk_bytes":     "볼륨 사용량(bytes)",
	"restarts":       "현재 배포의 컨테이너 재시작 횟수",
	"http_5xx_rate":  "HTTP 5xx 비율(%)",
	"http_p95_ms":    "HTTP 응답 시간 p95(ms)",
	"volume_percent": "볼륨 사용률(상한 대비 %)",
}

// HTTPWindow는 HTTP 지표의 구간(이 시간 동안의 access log로 계산)이다.
const HTTPWindow = 5 * time.Minute

// sampleMaxAge보다 오래된 지표 점은 데이터가 없는 것으로 본다.
const sampleMaxAge = 2 * time.Minute

// Step은 규칙 상태 머신이다. breach는 이번 평가에서 기준을 넘었는지다. 반환값 event는 ""|"alert.firing"|"alert.resolved".
//
//	ok      + breach  → pending(since=now), for_seconds가 0이면 바로 firing
//	pending + breach  → for_seconds가 지나면 firing
//	pending + !breach → ok
//	firing  + !breach → ok(resolved)
func Step(state string, since *time.Time, breach bool, now time.Time, forSeconds int) (string, *time.Time, string) {
	hold := time.Duration(forSeconds) * time.Second
	switch {
	case breach && state == store.AlertFiring:
		return state, since, ""
	case breach:
		if state != store.AlertPending || since == nil {
			t := now
			since = &t
		}
		if now.Sub(*since) >= hold {
			return store.AlertFiring, since, "alert.firing"
		}
		return store.AlertPending, since, ""
	case state == store.AlertFiring:
		return store.AlertOK, nil, "alert.resolved"
	}
	return store.AlertOK, nil, ""
}

// Breach는 값이 기준을 넘었는지다.
func Breach(cmp string, value, threshold float64) bool {
	if cmp == "<" {
		return value < threshold
	}
	return value > threshold
}

// Sources는 인스턴스 지표를 주는 곳이다(테스트에서 바꾼다).
type Sources struct {
	Store *store.Store
	// HTTP는 인스턴스의 최근 access log 요약이다(nil이면 HTTP 지표는 데이터 없음).
	HTTP func(instanceID string, since time.Time) deploy.HTTPWindow
	// Restarts는 인스턴스 현재 배포의 재시작 횟수 합이다(nil이거나 false면 데이터 없음).
	Restarts func(ctx context.Context, in *store.Instance) (int, bool)
	// VolumeSize는 마지막으로 잰 docker 볼륨 크기다.
	VolumeSize func(dockerName string) *int64
	Now        func() time.Time
}

func (s *Sources) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Value는 인스턴스의 지표 값이다(ok=false면 데이터 없음).
func (s *Sources) Value(ctx context.Context, metric string, in *store.Instance) (float64, bool) {
	now := s.now()
	switch metric {
	case "cpu_percent", "memory_percent", "memory_bytes", "disk_bytes":
		rows, err := s.Store.QueryMetrics(ctx, in.ID, now.Add(-sampleMaxAge).Unix())
		if err != nil || len(rows) == 0 {
			return 0, false
		}
		r := rows[len(rows)-1]
		switch metric {
		case "cpu_percent":
			return r.CPU, true
		case "memory_percent":
			if r.MemLimit <= 0 {
				return 0, false
			}
			return float64(r.Mem) * 100 / float64(r.MemLimit), true
		case "memory_bytes":
			return float64(r.Mem), true
		default:
			if r.Disk == nil {
				return 0, false
			}
			return float64(*r.Disk), true
		}
	case "http_5xx_rate", "http_p95_ms":
		if s.HTTP == nil {
			return 0, false
		}
		w := s.HTTP(in.ID, now.Add(-HTTPWindow))
		if w.Requests == 0 {
			return 0, false
		}
		if metric == "http_p95_ms" {
			return w.P95MS, true
		}
		return float64(w.Errors5xx) * 100 / float64(w.Requests), true
	case "restarts":
		if s.Restarts == nil {
			return 0, false
		}
		n, ok := s.Restarts(ctx, in)
		return float64(n), ok
	case "volume_percent":
		if s.VolumeSize == nil {
			return 0, false
		}
		vols, err := s.Store.ListVolumesByInstance(ctx, in.ID)
		if err != nil {
			return 0, false
		}
		best, ok := 0.0, false
		for _, v := range vols {
			if v.SizeLimitMB == nil || *v.SizeLimitMB <= 0 {
				continue
			}
			sz := s.VolumeSize(v.DockerName)
			if sz == nil {
				continue
			}
			pct := float64(*sz) * 100 / (float64(*v.SizeLimitMB) * 1024 * 1024)
			if !ok || pct > best {
				best, ok = pct, true
			}
		}
		return best, ok
	}
	return 0, false
}

// Evaluator는 규칙을 평가하고 상태를 저장하며 이벤트를 낸다.
type Evaluator struct {
	Store  *store.Store
	Events *events.Bus
	Log    *slog.Logger
	// Value는 인스턴스의 지표 값이다(보통 Sources.Value).
	Value func(ctx context.Context, metric string, in *store.Instance) (float64, bool)
	Now   func() time.Time
}

func (e *Evaluator) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Run은 ctx가 끝날 때까지 Interval마다 평가한다.
func (e *Evaluator) Run(ctx context.Context) {
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		if err := e.EvaluateAll(ctx); err != nil && ctx.Err() == nil && e.Log != nil {
			e.Log.Warn("alert rules", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// EvaluateAll은 켜진 규칙을 모두 한 번 평가한다.
func (e *Evaluator) EvaluateAll(ctx context.Context) error {
	rules, err := e.Store.ListAlertRules(ctx, "")
	if err != nil {
		return err
	}
	for _, r := range rules {
		e.Evaluate(ctx, r)
	}
	return nil
}

// targets는 규칙이 보는 인스턴스다: 서비스+환경이면 하나, 서비스만이면 그 서비스의 모든 환경, 환경만이면 그 환경의 모든 서비스, 둘 다 없으면 프로젝트 전체.
func (e *Evaluator) targets(ctx context.Context, r *store.AlertRule) []*store.Instance {
	var ins []*store.Instance
	switch {
	case r.ServiceID != nil:
		ins, _ = e.Store.ListInstancesByService(ctx, *r.ServiceID)
	case r.EnvironmentID != nil:
		ins, _ = e.Store.ListInstancesByEnv(ctx, *r.EnvironmentID)
	default:
		envs, _ := e.Store.ListEnvironments(ctx, r.ProjectID)
		for _, env := range envs {
			l, _ := e.Store.ListInstancesByEnv(ctx, env.ID)
			ins = append(ins, l...)
		}
	}
	out := ins[:0]
	for _, in := range ins {
		if r.EnvironmentID != nil && in.EnvironmentID != *r.EnvironmentID {
			continue
		}
		if in.Status == store.InstOffline {
			continue
		}
		out = append(out, in)
	}
	return out
}

// Evaluate는 규칙 하나를 평가한다. 여러 인스턴스를 보면 가장 나쁜 값(>면 최대, <면 최소)을 쓴다.
// 데이터가 없으면 상태를 바꾸지 않는다(firing이 데이터 공백으로 풀리지 않게).
func (e *Evaluator) Evaluate(ctx context.Context, r *store.AlertRule) {
	now := e.now()
	var worst *store.Instance
	val, have := 0.0, false
	for _, in := range e.targets(ctx, r) {
		v, ok := e.Value(ctx, r.Metric, in)
		if !ok {
			continue
		}
		if !have || (r.Comparator == "<" && v < val) || (r.Comparator != "<" && v > val) {
			val, have, worst = v, true, in
		}
	}
	t := now.UTC()
	r.LastEvaluatedAt = &t
	if !have {
		e.Store.SetAlertState(ctx, r)
		return
	}
	r.LastValue = &val
	state, since, ev := Step(r.LastState, r.PendingSince, Breach(r.Comparator, val, r.Threshold), now, r.ForSeconds)
	r.LastState, r.PendingSince = state, since
	if ev == "alert.firing" {
		r.LastFiredAt = &t
	}
	if err := e.Store.SetAlertState(ctx, r); err != nil {
		return
	}
	if ev == "" || e.Events == nil {
		return
	}
	env, svc := r.EnvironmentID, r.ServiceID
	if worst != nil {
		env, svc = &worst.EnvironmentID, &worst.ServiceID
	}
	e.Events.Emit(ctx, r.ProjectID, env, svc, ev, Message(r, ev, val, e.serviceLabel(ctx, worst)))
}

func (e *Evaluator) serviceLabel(ctx context.Context, in *store.Instance) string {
	if in == nil {
		return ""
	}
	name := in.ServiceID
	if svc, err := e.Store.GetService(ctx, in.ServiceID); err == nil {
		name = svc.Name
	}
	if env, err := e.Store.GetEnvironment(ctx, in.EnvironmentID); err == nil {
		name += " (" + env.Name + ")"
	}
	return name
}

// Describe는 규칙 조건의 표시다(예: "cpu_percent > 80 (60초 동안)").
func Describe(r *store.AlertRule) string {
	s := r.Metric + " " + r.Comparator + " " + strconv.FormatFloat(r.Threshold, 'f', -1, 64)
	if r.ForSeconds > 0 {
		s += fmt.Sprintf(" (%d초 동안)", r.ForSeconds)
	}
	return s
}

// Message는 이벤트 메시지다.
func Message(r *store.AlertRule, ev string, val float64, target string) string {
	name := r.Name
	if name == "" {
		name = Describe(r)
	}
	where := ""
	if target != "" {
		where = " — " + target
	}
	v := strconv.FormatFloat(val, 'f', 2, 64)
	if ev == "alert.resolved" {
		return fmt.Sprintf("알림 해제: %s%s (현재 %s, 기준 %s)", name, where, v, Describe(r))
	}
	return fmt.Sprintf("알림 발생: %s%s (현재 %s, 기준 %s)", name, where, v, Describe(r))
}
