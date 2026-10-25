package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/turin-dev/aven/internal/store"
	"github.com/turin-dev/aven/internal/usage"
)

// 프로젝트 월간 사용량 한도(hard limit): PATCH /projects/{pid}의 usage_limit, GET /projects/{pid}/usage/limit,
// POST /projects/{pid}/usage/resume. 검사·정지·재개 규칙은 internal/usage/limit.go.

// usageRoller는 한도 검사·재개에 쓰는 Roller다(서버의 집계 고루틴과 잠금을 공유한다).
func (s *Server) usageRoller() *usage.Roller {
	r := &usage.Roller{Store: s.Store, Events: s.Events, Log: s.Log}
	if s.Deploy != nil {
		r.Instances = s.Deploy
	}
	return r
}

// parseUsageLimit은 PATCH의 usage_limit 값이다. null 또는 한도가 없는 {}면 nil(끈다). action 기본값은 stop.
func parseUsageLimit(raw json.RawMessage) (*store.UsageLimit, error) {
	var ul *store.UsageLimit
	if err := json.Unmarshal(raw, &ul); err != nil {
		return nil, errors.New("invalid usage_limit: " + err.Error())
	}
	if ul == nil {
		return nil, nil
	}
	for _, v := range []*float64{ul.CPUCoreHours, ul.MemoryGBHours, ul.EgressGB} {
		if v != nil && (*v <= 0 || *v > 1e9) {
			return nil, errors.New("usage_limit values must be positive numbers")
		}
	}
	switch ul.Action {
	case "":
		ul.Action = store.UsageLimitStop
	case store.UsageLimitStop, store.UsageLimitNotify:
	default:
		return nil, errors.New(`usage_limit.action must be "stop" or "notify"`)
	}
	if ul.Empty() {
		return nil, nil
	}
	return ul, nil
}

// suspensionView는 정지 기록과 지금 유효한지(active)다. active가 false면 정지가 풀렸지만 멈춘 서비스를 아직 다시 시작하지 않은 상태다.
type suspensionView struct {
	*store.UsageSuspension
	Active bool `json:"active"`
}

type usageLimitFields struct {
	UsageLimit      *store.UsageLimit `json:"usage_limit"`
	UsageSuspension *suspensionView   `json:"usage_suspension"`
}

func (s *Server) usageLimitFields(ctx context.Context, projectID string) usageLimitFields {
	var f usageLimitFields
	pl, err := s.Store.GetUsageLimit(ctx, projectID)
	if err != nil {
		return f
	}
	f.UsageLimit = pl.Limit
	if pl.Suspension != nil {
		f.UsageSuspension = &suspensionView{pl.Suspension, pl.Suspension.ActiveAt(time.Now())}
	}
	return f
}

// applyUsageLimit은 한도를 저장하고 바로 다시 검사한다(올리거나 지우면 정지가 풀리고, 이미 넘은 값으로 낮추면 바로 정지한다).
func (s *Server) applyUsageLimit(ctx context.Context, p *store.Project, ul *store.UsageLimit) error {
	if err := s.Store.SetUsageLimit(ctx, p.ID, ul); err != nil {
		return err
	}
	return s.usageRoller().CheckProject(context.WithoutCancel(ctx), p.ID, time.Now())
}

// getUsageLimit은 GET /projects/{pid}/usage/limit: 한도, 정지 기록, 이번 달(부분 시간 포함) 사용량과 넘은 항목.
func (s *Server) getUsageLimit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.Store.GetProject(ctx, r.PathValue("pid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	now := time.Now()
	f := s.usageLimitFields(ctx, p.ID)
	t, err := s.usageRoller().MonthToDate(ctx, p.ID, now)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	t.Apply(s.Pricing)
	ex := usage.Exceeded(f.UsageLimit, t)
	if ex == nil {
		ex = []string{}
	}
	writeJSON(w, 200, map[string]any{"project_id": p.ID, "month": usage.MonthStart(now).Format("2006-01"), "usage_limit": f.UsageLimit,
		"usage_suspension": f.UsageSuspension, "month_to_date": t, "exceeded": ex})
}

// resumeUsage는 POST /projects/{pid}/usage/resume(프로젝트 admin): 정지를 지우고 정지 때 멈춘 서비스를 다시 배포한다.
// 정지가 유효한데 아직 한도를 넘었으면 409(한도를 먼저 올리거나 지운다).
func (s *Server) resumeUsage(w http.ResponseWriter, r *http.Request) {
	ctx := context.WithoutCancel(r.Context())
	p, err := s.Store.GetProject(ctx, r.PathValue("pid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	res, err := s.usageRoller().Resume(ctx, p.ID, time.Now())
	switch {
	case errors.Is(err, usage.ErrNotSuspended), errors.Is(err, usage.ErrStillOverLimit):
		writeErr(w, 409, err.Error())
		return
	case err != nil:
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

// blockSuspended는 프로젝트가 사용량 한도로 정지 중이면 409를 쓰고 true다(재시작, 수동 scale, cron 즉시 실행처럼
// Enqueue를 거치지 않고 컨테이너를 띄우는 작업용. 새 배포는 Enqueue가 막는다).
func (s *Server) blockSuspended(w http.ResponseWriter, r *http.Request, in *store.Instance) bool {
	if s.Deploy == nil {
		return false
	}
	if err := s.Deploy.SuspendedErr(r.Context(), in); err != nil {
		writeErr(w, 409, err.Error())
		return true
	}
	return false
}
