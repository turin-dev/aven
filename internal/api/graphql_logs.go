package api

// 배포 로그 스냅샷: GraphQL buildLogs/deploymentLogs와 MCP get_logs가 함께 쓴다.
// REST의 로그 라우트는 SSE 스트림이라 in-process 호출로 감쌀 수 없다. 그래서 먼저 REST GET /api/deployments/{did}로
// 권한을 확인하고(로그 라우트와 같은 읽기 권한) 그다음에 로그 저장소를 직접 읽는다.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/graphql-go/graphql"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/store"
)

const (
	defaultLogLimit = 500
	maxLogLimit     = 5000
	// maxLogScan은 필터나 시간 범위가 있을 때 거슬러 읽는 줄 수다(REST deploy-logs의 tail 상한과 같다).
	maxLogScan = 10000
)

// logQuery는 로그 스냅샷 조건이다. 0인 시각은 제한 없음이다.
type logQuery struct {
	Limit        int
	Filter       string
	Since, Until time.Time
}

func (q logQuery) bounded() bool { return q.Filter != "" || !q.Since.IsZero() || !q.Until.IsZero() }

func (q logQuery) inRange(ts string) bool {
	if q.Since.IsZero() && q.Until.IsZero() {
		return true
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return false
	}
	return (q.Since.IsZero() || !t.Before(q.Since)) && (q.Until.IsZero() || !t.After(q.Until))
}

// keepLast는 마지막 n개만 남긴다.
func keepLast[T any](items []T, n int) []T {
	if n > 0 && len(items) > n {
		return items[len(items)-n:]
	}
	return items
}

// runtimeLogSnapshot은 배포의 런타임 로그 중 조건에 맞는 마지막 Limit줄이다. 필터는 tail을 자른 뒤가 아니라
// 넉넉히 읽은 줄에 적용한다(그렇지 않으면 맞는 줄이 앞쪽에 있을 때 아무것도 돌려주지 못한다).
func (s *Server) runtimeLogSnapshot(ctx context.Context, d *store.Deployment, q logQuery) ([]deploy.LogEntry, error) {
	if s.Deploy == nil {
		return nil, gqlErr(http.StatusServiceUnavailable, "deployment logs are unavailable")
	}
	scan := q.Limit
	if q.bounded() {
		scan = maxLogScan
	}
	f := deploy.ParseFilter(q.Filter)
	out := make([]deploy.LogEntry, 0, min(q.Limit, 1024))
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	err := s.Deploy.StreamDeployLogs(ctx, d, scan, false, func(e deploy.LogEntry) {
		if (q.Filter == "" || f.Match(e.Msg)) && q.inRange(e.T) {
			out = append(out, e)
		}
	})
	if err != nil {
		return nil, err
	}
	return keepLast(out, q.Limit), nil
}

// buildLogSnapshot은 빌드 로그 줄(원문) 중 조건에 맞는 마지막 Limit줄이다(시각은 buildLogTime).
func (s *Server) buildLogSnapshot(d *store.Deployment, q logQuery) []string {
	if s.Deploy == nil || s.Deploy.Logs == nil {
		return []string{}
	}
	past, _, cancel := s.Deploy.Logs.Subscribe(d.ID)
	cancel()
	if !q.bounded() {
		return keepLast(past, q.Limit)
	}
	f := deploy.ParseFilter(q.Filter)
	out := []string{}
	for _, line := range past {
		ts, msg := buildLogTime(d, line)
		if (q.Filter == "" || f.Match(msg)) && q.inRange(ts) {
			out = append(out, line)
		}
	}
	return keepLast(out, q.Limit)
}

// buildLogTime은 빌드 로그 한 줄을 (RFC 3339 시각, 메시지)로 나눈다. 줄은 배포가 만들어진 뒤에 쓰이므로 시각 접두사는
// 배포 생성 날짜(UTC) 기준이고, 생성 시각보다 이르면 다음 날이다. 접두사가 없으면 배포 생성 시각이다.
func buildLogTime(d *store.Deployment, line string) (string, string) {
	base := d.CreatedAt.UTC()
	if len(line) >= 9 && line[8] == ' ' {
		if t, err := time.Parse("15:04:05", line[:8]); err == nil {
			at := time.Date(base.Year(), base.Month(), base.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
			if at.Before(base.Truncate(time.Second)) {
				at = at.AddDate(0, 0, 1) // 자정을 넘긴 빌드
			}
			return at.Format(time.RFC3339), line[9:]
		}
	}
	return base.Format(time.RFC3339), line
}

func logSeverity(e deploy.LogEntry) string {
	if deploy.IsErrorLine(e) {
		return "error"
	}
	return "info"
}

// ---- GraphQL ----

func newLogType() *graphql.Object {
	attr := graphql.NewObject(graphql.ObjectConfig{Name: "LogAttribute", Fields: graphql.Fields{
		"key":   field(reqStr),
		"value": field(reqStr),
	}})
	return graphql.NewObject(graphql.ObjectConfig{Name: "Log", Fields: graphql.Fields{
		"timestamp":  field(reqStr),
		"message":    field(reqStr),
		"severity":   field(optStr),
		"attributes": field(nn(list(attr))),
	}})
}

func logArgs() graphql.FieldConfigArgument {
	return args("deploymentId", reqStr, "limit", graphql.Int, "filter", optStr, "startDate", optStr, "endDate", optStr)
}

// parseLogArgs는 limit(기본 500, 최대 5000), filter, startDate/endDate(RFC 3339)를 읽는다.
func parseLogArgs(a map[string]any) (logQuery, error) {
	q := logQuery{Limit: defaultLogLimit, Filter: strings.TrimSpace(asString(a["filter"]))}
	if n, ok := a["limit"].(int); ok {
		if n < 1 || n > maxLogLimit {
			return q, gqlErr(http.StatusBadRequest, "limit must be between 1 and %d", maxLogLimit)
		}
		q.Limit = n
	}
	for key, dst := range map[string]*time.Time{"startDate": &q.Since, "endDate": &q.Until} {
		v := asString(a[key])
		if v == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return q, gqlErr(http.StatusBadRequest, "%s must be an RFC 3339 date-time (for example 2026-01-02T15:04:05Z)", key)
		}
		*dst = t
	}
	if !q.Since.IsZero() && !q.Until.IsZero() && q.Until.Before(q.Since) {
		return q, gqlErr(http.StatusBadRequest, "endDate is before startDate")
	}
	return q, nil
}

// resolveLogs는 buildLogs/deploymentLogs resolver다.
func resolveLogs(build bool) graphql.FieldResolveFn {
	return func(p graphql.ResolveParams) (any, error) {
		q, err := parseLogArgs(p.Args)
		if err != nil {
			return nil, err
		}
		did := asString(p.Args["deploymentId"])
		// 권한 확인: 로그 라우트와 같은 읽기 권한의 REST 단건 조회.
		if _, err := rest(p, http.MethodGet, "/api/deployments/"+esc(did), nil); err != nil {
			return nil, err
		}
		exec, _ := p.Context.Value(graphQLContextKey{}).(*graphQLExecution)
		if exec == nil || exec.srv == nil || exec.srv.Store == nil {
			return nil, gqlErr(http.StatusServiceUnavailable, "deployment logs are unavailable")
		}
		d, err := exec.srv.Store.GetDeployment(p.Context, did)
		if err != nil {
			return nil, gqlErr(http.StatusNotFound, "not found")
		}
		out := []any{}
		if build {
			for _, line := range exec.srv.buildLogSnapshot(d, q) {
				ts, msg := buildLogTime(d, line)
				out = append(out, map[string]any{"timestamp": ts, "message": msg,
					"severity": logSeverity(deploy.LogEntry{Msg: msg}), "attributes": []any{}})
			}
			return out, nil
		}
		lines, err := exec.srv.runtimeLogSnapshot(p.Context, d, q)
		if err != nil {
			return nil, err
		}
		for _, e := range lines {
			attrs := []any{}
			if e.Replica != "" {
				attrs = append(attrs, map[string]any{"key": "replica", "value": e.Replica})
			}
			if e.Stream != "" {
				attrs = append(attrs, map[string]any{"key": "stream", "value": e.Stream})
			}
			out = append(out, map[string]any{"timestamp": e.T, "message": e.Msg, "severity": logSeverity(e), "attributes": attrs})
		}
		return out, nil
	}
}
