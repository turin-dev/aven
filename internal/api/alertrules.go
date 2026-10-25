package api

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/alerts"
	"github.com/turin-dev/aven/internal/store"
	"github.com/turin-dev/aven/internal/usage"
)

// ---- 지표 알림 규칙 ----

const maxAlertRules = 100

// optID는 PATCH에서 "필드 없음"과 null(지우기)을 구분한다.
type optID struct {
	Set bool
	V   *string
}

func (o *optID) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.V = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if s != "" {
		o.V = &s
	}
	return nil
}

type alertInput struct {
	Name          *string  `json:"name"`
	EnvironmentID optID    `json:"environment_id"`
	ServiceID     optID    `json:"service_id"`
	Metric        *string  `json:"metric"`
	Comparator    *string  `json:"comparator"`
	Threshold     *float64 `json:"threshold"`
	ForSeconds    *int     `json:"for_seconds"`
	Enabled       *bool    `json:"enabled"`
}

func (s *Server) applyAlert(r *http.Request, a *store.AlertRule, in *alertInput) string {
	ctx := r.Context()
	if in.Name != nil {
		a.Name = strings.TrimSpace(*in.Name)
	}
	if len(a.Name) > 100 || strings.ContainsAny(a.Name, "\r\n\t") {
		return "name is too long (max 100 chars)"
	}
	if in.Metric != nil {
		a.Metric = *in.Metric
	}
	if _, ok := alerts.Metrics[a.Metric]; !ok {
		return "metric must be one of cpu_percent, memory_percent, memory_bytes, disk_bytes, restarts, http_5xx_rate, http_p95_ms, volume_percent"
	}
	if in.Comparator != nil {
		a.Comparator = *in.Comparator
	}
	if a.Comparator == "" {
		a.Comparator = ">"
	}
	if a.Comparator != ">" && a.Comparator != "<" {
		return "comparator must be > or <"
	}
	if in.Threshold != nil {
		a.Threshold = *in.Threshold
	}
	if math.IsNaN(a.Threshold) || math.IsInf(a.Threshold, 0) || a.Threshold < 0 || a.Threshold > 1e15 {
		return "threshold must be a non-negative number"
	}
	if in.ForSeconds != nil {
		a.ForSeconds = *in.ForSeconds
	}
	if a.ForSeconds < 0 || a.ForSeconds > 86400 {
		return "for_seconds must be between 0 and 86400"
	}
	if in.EnvironmentID.Set {
		a.EnvironmentID = in.EnvironmentID.V
	}
	if in.ServiceID.Set {
		a.ServiceID = in.ServiceID.V
	}
	if a.EnvironmentID != nil {
		env, err := s.Store.GetEnvironment(ctx, *a.EnvironmentID)
		if err != nil || env.ProjectID != a.ProjectID {
			return "environment_id must be an environment of this project"
		}
	}
	if a.ServiceID != nil {
		svc, err := s.Store.GetService(ctx, *a.ServiceID)
		if err != nil || svc.ProjectID != a.ProjectID {
			return "service_id must be a service of this project"
		}
	}
	if in.Enabled != nil {
		a.Enabled = *in.Enabled
	}
	return ""
}

func (s *Server) listAlertRules(w http.ResponseWriter, r *http.Request) {
	rs, err := s.Store.ListAlertRules(r.Context(), r.PathValue("pid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, rs)
}

func (s *Server) createAlertRule(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	var in alertInput
	if !decode(w, r, &in) {
		return
	}
	existing, err := s.Store.ListAlertRules(r.Context(), pid)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if len(existing) >= maxAlertRules {
		writeErr(w, 400, "알림 규칙이 너무 많습니다")
		return
	}
	a := &store.AlertRule{ProjectID: pid, Enabled: true}
	if in.Threshold == nil {
		writeErr(w, 400, "threshold is required")
		return
	}
	if msg := s.applyAlert(r, a, &in); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	if err := s.Store.CreateAlertRule(r.Context(), a); err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 201, a)
}

func (s *Server) loadAlertRule(w http.ResponseWriter, r *http.Request) (*store.AlertRule, bool) {
	a, err := s.Store.GetAlertRule(r.Context(), r.PathValue("arid"))
	if err == nil && a.ProjectID != r.PathValue("pid") {
		err = store.ErrNotFound
	}
	if err != nil {
		s.storeErr(w, err)
		return nil, false
	}
	return a, true
}

func (s *Server) patchAlertRule(w http.ResponseWriter, r *http.Request) {
	a, ok := s.loadAlertRule(w, r)
	if !ok {
		return
	}
	var in alertInput
	if !decode(w, r, &in) {
		return
	}
	if msg := s.applyAlert(r, a, &in); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	if err := s.Store.UpdateAlertRule(r.Context(), a); err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, a)
}

func (s *Server) deleteAlertRule(w http.ResponseWriter, r *http.Request) {
	a, ok := s.loadAlertRule(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteAlertRule(r.Context(), a.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	w.WriteHeader(204)
}

// ---- 사용량 ----

// usageRange는 ?from=&to=(RFC3339 또는 YYYY-MM-DD, UTC)를 시간 경계로 읽는다. 기본은 이번 달 1일부터 지금까지.
func usageRange(r *http.Request) (time.Time, time.Time, string) {
	now := time.Now().UTC()
	from, to := usage.MonthStart(now), now
	parse := func(v string, end bool) (time.Time, bool) {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t.UTC(), true
		}
		if t, err := time.Parse("2006-01-02", v); err == nil {
			if end {
				t = t.Add(24 * time.Hour) // 날짜만 주면 그날 끝까지
			}
			return t, true
		}
		return time.Time{}, false
	}
	if v := r.URL.Query().Get("from"); v != "" {
		t, ok := parse(v, false)
		if !ok {
			return from, to, "from must be RFC3339 or YYYY-MM-DD"
		}
		from = t
	}
	if v := r.URL.Query().Get("to"); v != "" {
		t, ok := parse(v, true)
		if !ok {
			return from, to, "to must be RFC3339 or YYYY-MM-DD"
		}
		to = t
	}
	if !to.After(from) {
		return from, to, "to must be after from"
	}
	if to.Sub(from) > usage.Retention+48*time.Hour {
		return from, to, "range is too long (max 400 days)"
	}
	return from, to, ""
}

// hourBounds는 [from, to)를 집계 시간 경계로 바꾼다(from 시간은 포함, to가 걸친 시간도 포함).
func hourBounds(from, to time.Time) (int64, int64) {
	f := from.Unix() - from.Unix()%usage.Hour
	t := to.Unix()
	if t%usage.Hour != 0 {
		t = t - t%usage.Hour + usage.Hour
	}
	return f, t
}

func (s *Server) projectUsage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.Store.GetProject(ctx, r.PathValue("pid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	from, to, msg := usageRange(r)
	if msg != "" {
		writeErr(w, 400, msg)
		return
	}
	group := r.URL.Query().Get("group")
	if group == "" {
		group = "service"
	}
	if group != "service" && group != "environment" && group != "day" {
		writeErr(w, 400, "group must be service, environment or day")
		return
	}
	f, t := hourBounds(from, to)
	rows, err := s.Store.QueryUsage(ctx, p.ID, f, t)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	names := map[string]string{}
	if svcs, err := s.Store.ListServices(ctx, p.ID); err == nil {
		for _, sv := range svcs {
			names[sv.ID] = sv.Name
		}
	}
	if envs, err := s.Store.ListEnvironments(ctx, p.ID); err == nil {
		for _, e := range envs {
			names[e.ID] = e.Name
		}
	}
	key := func(u store.UsageRow) string {
		switch group {
		case "environment":
			return u.EnvironmentID
		case "day":
			return time.Unix(u.Hour, 0).UTC().Format("2006-01-02")
		}
		return u.ServiceID
	}
	total, groups, series := usage.Summarize(rows, key, s.Pricing)
	for i := range groups {
		groups[i].Name = names[groups[i].Key]
		if groups[i].Name == "" {
			groups[i].Name = groups[i].Key // 지워진 서비스/환경은 ID, day는 날짜
		}
	}
	lf := s.usageLimitFields(ctx, p.ID) // usagelimit.go
	writeJSON(w, 200, map[string]any{"project_id": p.ID, "from": time.Unix(f, 0).UTC(), "to": time.Unix(t, 0).UTC(), "group": group,
		"totals": total, "groups": groups, "series": series, "pricing": s.Pricing, "usage_alert": p.UsageAlert,
		"usage_limit": lf.UsageLimit, "usage_suspension": lf.UsageSuspension})
}

func (s *Server) workspaceUsage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	from, to, msg := usageRange(r)
	if msg != "" {
		writeErr(w, 400, msg)
		return
	}
	f, t := hourBounds(from, to)
	rows, err := s.Store.QueryUsage(ctx, "", f, t)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	ps, err := s.Store.ListProjects(ctx)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	names := map[string]string{}
	for _, p := range ps {
		names[p.ID] = p.Name
	}
	kept := rows[:0]
	for _, u := range rows {
		if _, ok := names[u.ProjectID]; ok { // 지워진 프로젝트의 집계는 뺀다
			kept = append(kept, u)
		}
	}
	total, groups, series := usage.Summarize(kept, func(u store.UsageRow) string { return u.ProjectID }, s.Pricing)
	for i := range groups {
		groups[i].Name = names[groups[i].Key]
	}
	writeJSON(w, 200, map[string]any{"from": time.Unix(f, 0).UTC(), "to": time.Unix(t, 0).UTC(), "group": "project",
		"totals": total, "projects": groups, "series": series, "pricing": s.Pricing})
}
