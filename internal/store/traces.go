package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// migrateTracing는 인스턴스(서비스×환경)별 tracing 설정과 OTLP span 저장소를 추가한다.
func migrateTracing(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`CREATE TABLE tracing_settings (
			instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
			enabled INTEGER NOT NULL DEFAULT 0,
			token TEXT NOT NULL DEFAULT '',
			token_hash TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX tracing_settings_token ON tracing_settings(token_hash)`,
		`CREATE TABLE trace_spans (
			trace_id TEXT NOT NULL,
			span_id TEXT NOT NULL,
			parent_span_id TEXT NOT NULL DEFAULT '',
			project_id TEXT NOT NULL,
			environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
			service_id TEXT NOT NULL DEFAULT '',
			service_name TEXT NOT NULL DEFAULT '',
			deployment_id TEXT NOT NULL DEFAULT '',
			replica_id TEXT NOT NULL DEFAULT '',
			component TEXT NOT NULL DEFAULT 'service',
			name TEXT NOT NULL,
			kind TEXT NOT NULL DEFAULT 'internal',
			status TEXT NOT NULL DEFAULT 'unset',
			start_ns INTEGER NOT NULL,
			end_ns INTEGER NOT NULL,
			attributes TEXT NOT NULL DEFAULT '{}',
			resource_attributes TEXT NOT NULL DEFAULT '{}',
			events TEXT NOT NULL DEFAULT '[]',
			links TEXT NOT NULL DEFAULT '[]',
			PRIMARY KEY (environment_id, trace_id, span_id)
		)`,
		`CREATE INDEX trace_spans_env_time ON trace_spans(environment_id, start_ns DESC)`,
		`CREATE INDEX trace_spans_service ON trace_spans(service_id, environment_id, component, start_ns)`,
		`CREATE INDEX trace_spans_start ON trace_spans(start_ns)`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// TracingSetting은 서비스 하나의 환경별 tracing 스위치와 마지막 span 시각이다.
type TracingSetting struct {
	InstanceID string     `json:"instance_id"`
	ServiceID  string     `json:"service_id"`
	Service    string     `json:"service_name"`
	Enabled    bool       `json:"enabled"`
	HasDomain  bool       `json:"has_public_domain"`
	UpdatedAt  *time.Time `json:"updated_at"`
	LastEdgeAt *time.Time `json:"last_edge_span_at"`
	LastAppAt  *time.Time `json:"last_service_span_at"`
	Token      string     `json:"-"`
}

// TraceIdentity는 OTLP 자격 증명이 가리키는 서비스 배포 맥락이다.
type TraceIdentity struct {
	ProjectID     string
	EnvironmentID string
	ServiceID     string
	ServiceName   string
	DeploymentID  string
	ReplicaID     string
	Component     string
}

type TraceEvent struct {
	Name       string         `json:"name"`
	Time       time.Time      `json:"time"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

type TraceLink struct {
	TraceID    string         `json:"trace_id"`
	SpanID     string         `json:"span_id"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

type TraceSpan struct {
	TraceID            string         `json:"trace_id"`
	SpanID             string         `json:"span_id"`
	ParentSpanID       string         `json:"parent_span_id,omitempty"`
	ProjectID          string         `json:"project_id"`
	EnvironmentID      string         `json:"environment_id"`
	ServiceID          string         `json:"service_id,omitempty"`
	ServiceName        string         `json:"service_name,omitempty"`
	DeploymentID       string         `json:"deployment_id,omitempty"`
	ReplicaID          string         `json:"replica_id,omitempty"`
	Component          string         `json:"component"`
	Name               string         `json:"name"`
	Kind               string         `json:"kind"`
	Status             string         `json:"status"`
	StartTime          time.Time      `json:"start_time"`
	EndTime            time.Time      `json:"end_time"`
	DurationMS         float64        `json:"duration_ms"`
	Attributes         map[string]any `json:"attributes"`
	ResourceAttributes map[string]any `json:"resource_attributes"`
	Events             []TraceEvent   `json:"events"`
	Links              []TraceLink    `json:"links"`
}

type TraceSummary struct {
	TraceID     string    `json:"trace_id"`
	StartedAt   time.Time `json:"started_at"`
	DurationMS  float64   `json:"duration_ms"`
	RootName    string    `json:"root_name"`
	ServiceID   string    `json:"service_id,omitempty"`
	ServiceName string    `json:"service_name,omitempty"`
	SpanCount   int       `json:"span_count"`
	ErrorCount  int       `json:"error_count"`
}

// TraceQuery는 trace 목록 조건이다. ServiceID가 있으면 그 서비스의 span이 하나라도 있는 trace만,
// TraceIDs가 있으면 그 trace만 고른다.
type TraceQuery struct {
	EnvironmentID string
	ServiceID     string
	From, Until   time.Time
	Limit         int
	TraceIDs      []string
}

func traceTokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func (s *Store) GetTracingSetting(ctx context.Context, instanceID string) (*TracingSetting, error) {
	var st TracingSetting
	var enabled int
	var updated string
	var token string
	err := s.q.QueryRowContext(ctx, `SELECT instance_id, enabled, token, updated_at FROM tracing_settings WHERE instance_id=?`, instanceID).
		Scan(&st.InstanceID, &enabled, &token, &updated)
	if err != nil {
		return nil, notFound(err)
	}
	st.Enabled = enabled != 0
	u := parseTS(updated)
	st.UpdatedAt = &u
	if token != "" {
		st.Token, err = s.Unseal(token)
		if err != nil {
			return nil, err
		}
	}
	return &st, nil
}

// SetTracingSetting은 tracing을 켜거나 끈다. 수신기 토큰은 처음 켤 때 한 번 만들고 다시 켜도 그대로 둔다
// (이미 떠 있는 컨테이너의 OTEL_EXPORTER_OTLP_HEADERS가 계속 맞도록). 꺼져 있는 동안 그 토큰은 거절된다.
func (s *Store) SetTracingSetting(ctx context.Context, instanceID string, enabled bool) (*TracingSetting, error) {
	err := s.Tx(ctx, func(tx *Store) error {
		cur, err := tx.GetTracingSetting(ctx, instanceID)
		if err != nil && err != ErrNotFound {
			return err
		}
		token := ""
		if cur != nil {
			token = cur.Token
		}
		if token == "" {
			token = RandomSecret(48)
		}
		_, err = tx.q.ExecContext(ctx, `INSERT INTO tracing_settings (instance_id, enabled, token, token_hash, updated_at)
			VALUES (?,?,?,?,?) ON CONFLICT(instance_id) DO UPDATE SET enabled=excluded.enabled, token=excluded.token,
			token_hash=excluded.token_hash, updated_at=excluded.updated_at`,
			instanceID, enabled, tx.Seal(token), traceTokenHash(token), ts(time.Now()))
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetTracingSetting(ctx, instanceID)
}

// ListTracingSettings는 환경의 데이터베이스가 아닌 서비스마다 tracing 상태와 edge/앱이 마지막으로 보낸 span 시각을 준다.
func (s *Store) ListTracingSettings(ctx context.Context, environmentID string) ([]*TracingSetting, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT i.id, i.service_id, v.name, i.spec,
		COALESCE(t.enabled,0), COALESCE(t.updated_at,''),
		(SELECT MAX(start_ns) FROM trace_spans x WHERE x.service_id=i.service_id AND x.environment_id=i.environment_id AND x.component='edge'),
		(SELECT MAX(start_ns) FROM trace_spans x WHERE x.service_id=i.service_id AND x.environment_id=i.environment_id AND x.component='service')
		FROM instances i JOIN services v ON v.id=i.service_id
		LEFT JOIN tracing_settings t ON t.instance_id=i.id
		WHERE i.environment_id=? AND v.source_type<>'database' ORDER BY v.name`, environmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*TracingSetting{}
	for rows.Next() {
		var st TracingSetting
		var enabled int
		var updated, spec string
		var edgeNS, appNS sql.NullInt64
		if err := rows.Scan(&st.InstanceID, &st.ServiceID, &st.Service, &spec, &enabled, &updated, &edgeNS, &appNS); err != nil {
			return nil, err
		}
		st.Enabled = enabled != 0
		if updated != "" {
			u := parseTS(updated)
			st.UpdatedAt = &u
		}
		st.HasDomain = specHasDomain(spec)
		st.LastEdgeAt, st.LastAppAt = nullableNS(edgeNS), nullableNS(appNS)
		out = append(out, &st)
	}
	return out, rows.Err()
}

func specHasDomain(spec string) bool {
	var sp struct {
		Domains []json.RawMessage `json:"domains"`
	}
	return json.Unmarshal([]byte(spec), &sp) == nil && len(sp.Domains) > 0
}

// LastTraceSpans는 서비스의 환경에서 edge/앱이 마지막으로 보낸 span 시각이다.
func (s *Store) LastTraceSpans(ctx context.Context, serviceID, environmentID string) (edge, app *time.Time, err error) {
	var e, a sql.NullInt64
	err = s.q.QueryRowContext(ctx, `SELECT
		(SELECT MAX(start_ns) FROM trace_spans WHERE service_id=? AND environment_id=? AND component='edge'),
		(SELECT MAX(start_ns) FROM trace_spans WHERE service_id=? AND environment_id=? AND component='service')`,
		serviceID, environmentID, serviceID, environmentID).Scan(&e, &a)
	return nullableNS(e), nullableNS(a), err
}

func nullableNS(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(0, v.Int64).UTC()
	return &t
}

// LookupTraceToken은 켜져 있는 tracing 설정의 토큰으로 서비스 맥락을 찾는다.
func (s *Store) LookupTraceToken(ctx context.Context, token string) (*TraceIdentity, error) {
	id := TraceIdentity{Component: "service"}
	var deployment sql.NullString
	err := s.q.QueryRowContext(ctx, `SELECT e.project_id, i.environment_id, i.service_id, v.name, i.current_deployment_id
		FROM tracing_settings t JOIN instances i ON i.id=t.instance_id
		JOIN services v ON v.id=i.service_id JOIN environments e ON e.id=i.environment_id
		WHERE t.enabled=1 AND t.token_hash=?`, traceTokenHash(token)).
		Scan(&id.ProjectID, &id.EnvironmentID, &id.ServiceID, &id.ServiceName, &deployment)
	if err != nil {
		return nil, notFound(err)
	}
	id.DeploymentID = deployment.String
	return &id, nil
}

func (s *Store) InsertTraceSpans(ctx context.Context, spans []TraceSpan) error {
	return s.Tx(ctx, func(tx *Store) error {
		const insert = `INSERT INTO trace_spans (
				trace_id, span_id, parent_span_id, project_id, environment_id, service_id, service_name,
				deployment_id, replica_id, component, name, kind, status, start_ns, end_ns,
				attributes, resource_attributes, events, links
			) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(environment_id, trace_id, span_id) DO UPDATE SET
				parent_span_id=excluded.parent_span_id, project_id=excluded.project_id, service_id=excluded.service_id,
				service_name=excluded.service_name, deployment_id=excluded.deployment_id, replica_id=excluded.replica_id,
				component=excluded.component, name=excluded.name, kind=excluded.kind, status=excluded.status,
				start_ns=excluded.start_ns, end_ns=excluded.end_ns, attributes=excluded.attributes,
				resource_attributes=excluded.resource_attributes, events=excluded.events, links=excluded.links`
		for _, span := range spans {
			_, err := tx.q.ExecContext(ctx, insert,
				span.TraceID, span.SpanID, span.ParentSpanID, span.ProjectID, span.EnvironmentID, span.ServiceID, span.ServiceName,
				span.DeploymentID, span.ReplicaID, span.Component, span.Name, span.Kind, span.Status,
				span.StartTime.UnixNano(), span.EndTime.UnixNano(), jsonStr(orEmptyMap(span.Attributes)), jsonStr(orEmptyMap(span.ResourceAttributes)),
				jsonStr(orEmptySlice(span.Events)), jsonStr(orEmptySlice(span.Links)))
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func orEmptyMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func orEmptySlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// ListTraceSummaries는 조건에 맞는 trace를 시작 시각 내림차순으로 요약한다.
// trace의 시작/길이/span 수는 기간 밖 span까지 포함한 trace 전체 기준이다.
func (s *Store) ListTraceSummaries(ctx context.Context, q TraceQuery) ([]TraceSummary, error) {
	if q.Limit < 1 || q.Limit > 5000 {
		q.Limit = 100
	}
	var where []string
	args := []any{q.EnvironmentID}
	if len(q.TraceIDs) > 0 {
		where = append(where, `trace_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(q.TraceIDs)), ",")+`)`)
		for _, id := range q.TraceIDs {
			args = append(args, id)
		}
	} else {
		where = append(where, `start_ns>=? AND start_ns<=?`)
		args = append(args, q.From.UnixNano(), q.Until.UnixNano())
	}
	if q.ServiceID != "" {
		where = append(where, `service_id=?`)
		args = append(args, q.ServiceID)
	}
	args = append(args, q.EnvironmentID, q.Limit)
	rows, err := s.q.QueryContext(ctx, `WITH ids AS (
			SELECT DISTINCT trace_id FROM trace_spans WHERE environment_id=? AND `+strings.Join(where, " AND ")+`
		)
		SELECT s.trace_id, MIN(s.start_ns), MAX(s.end_ns), COUNT(*), SUM(s.status='error'),
			COALESCE((SELECT r.name || char(31) || r.service_id || char(31) || r.service_name FROM trace_spans r
				WHERE r.environment_id=s.environment_id AND r.trace_id=s.trace_id
				ORDER BY r.parent_span_id<>'', r.start_ns LIMIT 1), '')
		FROM trace_spans s JOIN ids USING (trace_id)
		WHERE s.environment_id=?
		GROUP BY s.trace_id ORDER BY MIN(s.start_ns) DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TraceSummary{}
	for rows.Next() {
		var item TraceSummary
		var startNS, endNS int64
		var root string
		if err := rows.Scan(&item.TraceID, &startNS, &endNS, &item.SpanCount, &item.ErrorCount, &root); err != nil {
			return nil, err
		}
		parts := strings.SplitN(root, "\x1f", 3)
		for len(parts) < 3 {
			parts = append(parts, "")
		}
		item.RootName, item.ServiceID, item.ServiceName = parts[0], parts[1], parts[2]
		item.StartedAt = time.Unix(0, startNS).UTC()
		item.DurationMS = float64(endNS-startNS) / float64(time.Millisecond)
		out = append(out, item)
	}
	return out, rows.Err()
}

const spanColumns = `trace_id, span_id, parent_span_id, project_id, environment_id, service_id,
	service_name, deployment_id, replica_id, component, name, kind, status, start_ns, end_ns,
	attributes, resource_attributes, events, links`

func scanSpan(rows *sql.Rows) (TraceSpan, error) {
	var span TraceSpan
	var startNS, endNS int64
	var attrs, resource, events, links string
	if err := rows.Scan(&span.TraceID, &span.SpanID, &span.ParentSpanID, &span.ProjectID, &span.EnvironmentID,
		&span.ServiceID, &span.ServiceName, &span.DeploymentID, &span.ReplicaID, &span.Component,
		&span.Name, &span.Kind, &span.Status, &startNS, &endNS, &attrs, &resource, &events, &links); err != nil {
		return span, err
	}
	span.StartTime, span.EndTime = time.Unix(0, startNS).UTC(), time.Unix(0, endNS).UTC()
	span.DurationMS = float64(endNS-startNS) / float64(time.Millisecond)
	span.Attributes, span.ResourceAttributes = map[string]any{}, map[string]any{}
	span.Events, span.Links = []TraceEvent{}, []TraceLink{}
	// 정수 속성이 float64로 바뀌지 않게 UseNumber로 읽고 int64/float64로 되돌린다.
	decodeAttrs(attrs, &span.Attributes)
	decodeAttrs(resource, &span.ResourceAttributes)
	json.Unmarshal([]byte(events), &span.Events)
	json.Unmarshal([]byte(links), &span.Links)
	return span, nil
}

func decodeAttrs(raw string, out *map[string]any) {
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	var m map[string]any
	if d.Decode(&m) != nil || m == nil {
		return
	}
	for k, v := range m {
		if n, ok := v.(json.Number); ok {
			if i, err := n.Int64(); err == nil {
				m[k] = i
			} else if f, err := n.Float64(); err == nil {
				m[k] = f
			}
		}
	}
	*out = m
}

// GetTraceSpans는 trace 하나의 span을 시작 순으로 최대 limit개(0이면 2000) 준다.
func (s *Store) GetTraceSpans(ctx context.Context, environmentID, traceID string, limit int) ([]TraceSpan, error) {
	if limit < 1 || limit > 2000 {
		limit = 2000
	}
	rows, err := s.q.QueryContext(ctx, `SELECT `+spanColumns+` FROM trace_spans
		WHERE environment_id=? AND trace_id=? ORDER BY start_ns, end_ns DESC LIMIT ?`, environmentID, traceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TraceSpan{}
	for rows.Next() {
		span, err := scanSpan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, span)
	}
	return out, rows.Err()
}

// ScanTraceSpans는 기간 안의 span을 최근 것부터 최대 max개 f에 넘긴다. f가 false를 내면 멈춘다.
func (s *Store) ScanTraceSpans(ctx context.Context, environmentID, serviceID string, from, until time.Time, max int, f func(*TraceSpan) bool) error {
	q := `SELECT ` + spanColumns + ` FROM trace_spans WHERE environment_id=? AND start_ns>=? AND start_ns<=?`
	args := []any{environmentID, from.UnixNano(), until.UnixNano()}
	if serviceID != "" {
		q += ` AND trace_id IN (SELECT trace_id FROM trace_spans WHERE environment_id=? AND service_id=? AND start_ns>=? AND start_ns<=?)`
		args = append(args, environmentID, serviceID, from.UnixNano(), until.UnixNano())
	}
	q += ` ORDER BY start_ns DESC LIMIT ?`
	args = append(args, max)
	rows, err := s.q.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		span, err := scanSpan(rows)
		if err != nil {
			return err
		}
		if !f(&span) {
			break
		}
	}
	return rows.Err()
}

// PruneTraceSpans는 before보다 먼저 시작한 span을 지운다.
func (s *Store) PruneTraceSpans(ctx context.Context, before time.Time) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM trace_spans WHERE start_ns < ?`, before.UnixNano())
	return err
}
