package api

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/store"
	"github.com/turin-dev/aven/internal/tracing"
)

// maxScanSpans는 필터 검색 한 번에 훑는 span 수 상한이다.
const maxScanSpans = 200000

func (s *Server) getTracingSetting(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	setting, err := s.Store.GetTracingSetting(r.Context(), c.in.ID)
	if err != nil && err != store.ErrNotFound {
		s.storeErr(w, err)
		return
	}
	s.writeTracingSetting(w, r, c, setting)
}

func (s *Server) writeTracingSetting(w http.ResponseWriter, r *http.Request, c *instCtx, setting *store.TracingSetting) {
	if setting == nil {
		setting = &store.TracingSetting{}
	}
	setting.InstanceID, setting.ServiceID, setting.Service = c.in.ID, c.svc.ID, c.svc.Name
	setting.HasDomain = len(c.in.Domains) > 0
	edge, app, err := s.Store.LastTraceSpans(r.Context(), c.svc.ID, c.env.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	setting.LastEdgeAt, setting.LastAppAt = edge, app
	writeJSON(w, http.StatusOK, setting)
}

type tracingBody struct {
	Enabled *bool `json:"enabled"`
	// ServiceIDs는 환경 단위 PUT에서 바꿀 서비스다. 비우면 데이터베이스가 아닌 모든 서비스.
	ServiceIDs []string `json:"service_ids,omitempty"`
}

func decodeTracingBody(w http.ResponseWriter, r *http.Request) (*tracingBody, bool) {
	var body tracingBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "enabled must be a boolean")
		return nil, false
	}
	return &body, true
}

func (s *Server) putTracingSetting(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	if c.svc.SourceType == "database" {
		writeErr(w, http.StatusBadRequest, "tracing is not available for databases")
		return
	}
	body, ok := decodeTracingBody(w, r)
	if !ok {
		return
	}
	setting, err := s.Store.SetTracingSetting(r.Context(), c.in.ID, *body.Enabled)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.applyTracingRoutes(r)
	s.writeTracingSetting(w, r, c, setting)
}

// applyTracingRoutes는 edge가 바로 trace를 시작/중단하도록 proxy 라우트를 다시 만든다.
// 실패해도 설정은 저장됐고 다음 동기화에 반영되므로 경고만 남긴다.
func (s *Server) applyTracingRoutes(r *http.Request) {
	if s.Deploy == nil {
		return
	}
	if err := s.Deploy.SyncProxy(r.Context()); err != nil && s.Log != nil {
		s.Log.Warn("apply traced proxy routes", "err", err)
	}
}

func (s *Server) listTracingSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.Store.ListTracingSettings(r.Context(), r.PathValue("eid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

// putEnvTracing은 환경의 여러 서비스(기본: 전부)의 tracing을 한 번에 켜거나 끈다(`aven trace on --all`).
func (s *Server) putEnvTracing(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	eid := r.PathValue("eid")
	body, ok := decodeTracingBody(w, r)
	if !ok {
		return
	}
	settings, err := s.Store.ListTracingSettings(ctx, eid)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	want := map[string]bool{}
	for _, id := range body.ServiceIDs {
		want[id] = true
	}
	for _, st := range settings {
		if len(want) > 0 && !want[st.ServiceID] {
			continue
		}
		delete(want, st.ServiceID)
		if _, err := s.Store.SetTracingSetting(ctx, st.InstanceID, *body.Enabled); err != nil {
			s.storeErr(w, err)
			return
		}
	}
	if len(want) > 0 {
		writeErr(w, http.StatusNotFound, "service not found in this environment (or is a database)")
		return
	}
	s.applyTracingRoutes(r)
	s.listTracingSettings(w, r)
}

func (s *Server) listTraces(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	eid := r.PathValue("eid")
	qv := r.URL.Query()
	now := time.Now().UTC()
	from, err := traceTime(firstNonEmpty(qv.Get("since"), qv.Get("from")), now, now.Add(-time.Hour))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "since must be a duration (30m, 2h, 1d) or an RFC 3339 time")
		return
	}
	until, err := traceTime(firstNonEmpty(qv.Get("until"), qv.Get("to")), now, now)
	if err != nil || until.Before(from) {
		writeErr(w, http.StatusBadRequest, "until must be a duration or RFC 3339 time after since")
		return
	}
	limit := 100
	if raw := qv.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 500 {
			writeErr(w, http.StatusBadRequest, "limit must be between 1 and 500")
			return
		}
		limit = n
	}
	query := strings.TrimSpace(firstNonEmpty(qv.Get("filter"), qv.Get("q")))
	if b, _ := strconv.ParseBool(qv.Get("errors")); b {
		if query != "" {
			query = "(" + query + ") @status:error"
		} else {
			query = "@status:error"
		}
	}
	tq := store.TraceQuery{EnvironmentID: eid, ServiceID: qv.Get("service"), From: from, Until: until, Limit: limit}
	if query != "" {
		f, err := tracing.ParseFilter(query)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "filter: "+err.Error())
			return
		}
		seen := map[string]bool{}
		err = s.Store.ScanTraceSpans(ctx, eid, tq.ServiceID, from, until, maxScanSpans, func(sp *store.TraceSpan) bool {
			if !seen[sp.TraceID] && f.Match(sp) {
				seen[sp.TraceID] = true
				tq.TraceIDs = append(tq.TraceIDs, sp.TraceID)
			}
			return len(tq.TraceIDs) < limit
		})
		if err != nil {
			s.storeErr(w, err)
			return
		}
		if len(tq.TraceIDs) == 0 {
			writeJSON(w, http.StatusOK, []store.TraceSummary{})
			return
		}
		tq.ServiceID = "" // 이미 서비스로 걸렀다
	}
	items, err := s.Store.ListTraceSummaries(ctx, tq)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) getTrace(w http.ResponseWriter, r *http.Request) {
	traceID, ok := normalizeTraceID(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "trace ID must be 32 hexadecimal characters or a traceparent header")
		return
	}
	limit := 1000
	if raw := r.URL.Query().Get("max_spans"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 2000 {
			writeErr(w, http.StatusBadRequest, "max_spans must be between 1 and 2000")
			return
		}
		limit = n
	}
	spans, err := s.Store.GetTraceSpans(r.Context(), r.PathValue("eid"), traceID, limit)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if len(spans) == 0 {
		writeErr(w, http.StatusNotFound, "trace not found in this environment")
		return
	}
	writeJSON(w, http.StatusOK, spans)
}

// traceAttributeKeys는 기간 안 최근 span의 속성 키를 준다(필터 입력 자동 완성).
func (s *Server) traceAttributeKeys(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	from, err := traceTime(r.URL.Query().Get("since"), now, now.Add(-time.Hour))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "since must be a duration or RFC 3339 time")
		return
	}
	keys := map[string]bool{}
	err = s.Store.ScanTraceSpans(r.Context(), r.PathValue("eid"), "", from, now, 2000, func(sp *store.TraceSpan) bool {
		for k := range sp.Attributes {
			keys[k] = true
		}
		for k := range sp.ResourceAttributes {
			keys[k] = true
		}
		return len(keys) < 500
	})
	if err != nil {
		s.storeErr(w, err)
		return
	}
	out := make([]string, 0, len(keys))
	for k := range keys {
		out = append(out, k)
	}
	sort.Strings(out)
	writeJSON(w, http.StatusOK, out)
}

func firstNonEmpty(v ...string) string {
	for _, x := range v {
		if strings.TrimSpace(x) != "" {
			return strings.TrimSpace(x)
		}
	}
	return ""
}

// normalizeTraceID는 32자리 hex trace ID 또는 traceparent 헤더 전체를 받는다.
func normalizeTraceID(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if strings.Contains(value, "-") {
		parts := strings.Split(value, "-")
		if len(parts) != 4 || len(parts[0]) != 2 || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
			return "", false
		}
		value = parts[1]
	}
	value = strings.ToLower(value)
	if len(value) != 32 || value == strings.Repeat("0", 32) {
		return "", false
	}
	_, err := hex.DecodeString(value)
	return value, err == nil
}

// traceTime은 상대 시간(30m, 2h, 1d, 1w)이나 RFC 3339 시각을 해석한다. 비면 fallback.
func traceTime(value string, now, fallback time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	if n := len(value); n > 1 && (value[n-1] == 'd' || value[n-1] == 'w') {
		if k, err := strconv.ParseFloat(value[:n-1], 64); err == nil && k >= 0 {
			unit := 24 * time.Hour
			if value[n-1] == 'w' {
				unit *= 7
			}
			return now.Add(-time.Duration(k * float64(unit))), nil
		}
	}
	if d, err := time.ParseDuration(value); err == nil {
		return now.Add(-d), nil
	}
	return time.Parse(time.RFC3339Nano, value)
}
