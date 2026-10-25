package api

// Public GraphQL API (Railway의 backboard /graphql/v2와 같은 모양). 스키마는 graphql_schema.go에 있다.
//
// resolver는 저장소를 직접 만지지 않고 같은 프로세스 안에서 REST v1 라우트(mux)를 호출한다. 그래서 인증(세션/토큰),
// 권한(authz), 비밀 값 가리기, 이벤트 기록이 REST와 똑같이 적용된다. 응답의 snake_case 키는 camelCase로 바꾼다.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/graphql-go/graphql"
	"github.com/graphql-go/graphql/language/ast"

	"github.com/turin-dev/aven/internal/store"
)

const (
	maxGraphQLRequestBytes = 1 << 20
	// maxGraphQLRESTCalls는 GraphQL 요청 하나가 부를 수 있는 내부 REST 호출 수다(깊게 중첩된 쿼리로 서버를 붙잡지 못하게).
	maxGraphQLRESTCalls = 500
)

type graphQLRequest struct {
	Query         string         `json:"query"`
	OperationName string         `json:"operationName"`
	Variables     map[string]any `json:"variables"`
}

type graphQLContextKey struct{}

// graphQLExecution은 GraphQL 요청 하나의 상태다. 내부 REST 호출은 원래 요청의 인증 헤더/쿠키를 그대로 쓴다.
type graphQLExecution struct {
	store   *store.Store
	rest    http.Handler
	request *http.Request
	calls   atomic.Int32
}

// graphQLError는 REST 응답 코드를 GraphQL 오류의 extensions.code로 알려 준다(gqlerrors.ExtendedError).
type graphQLError struct {
	message string
	status  int
}

func (e *graphQLError) Error() string { return e.message }

func (e *graphQLError) Extensions() map[string]any {
	code := "INTERNAL_SERVER_ERROR"
	switch e.status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		code = "BAD_USER_INPUT"
	case http.StatusUnauthorized:
		code = "UNAUTHENTICATED"
	case http.StatusForbidden:
		code = "FORBIDDEN"
	case http.StatusNotFound:
		code = "NOT_FOUND"
	case http.StatusConflict:
		code = "CONFLICT"
	case http.StatusTooManyRequests:
		code = "RATE_LIMITED"
	}
	return map[string]any{"code": code, "status": e.status}
}

func gqlErr(status int, format string, args ...any) error {
	return &graphQLError{message: fmt.Sprintf(format, args...), status: status}
}

var (
	graphQLSchemaOnce sync.Once
	graphQLSchemaVal  graphql.Schema
	graphQLSchemaErr  error
)

// graphQLHandler는 POST /graphql/v2다. rest는 REST 라우트가 등록된 mux다.
func (s *Server) graphQLHandler(rest http.Handler) http.Handler {
	// 스키마는 Server와 무관하므로 한 번만 만든다(테스트에서 Handler()를 여러 번 불러도 같은 스키마).
	graphQLSchemaOnce.Do(func() { graphQLSchemaVal, graphQLSchemaErr = newGraphQLSchema() })
	schema, schemaErr := graphQLSchemaVal, graphQLSchemaErr
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if schemaErr != nil {
			writeGraphQLError(w, http.StatusInternalServerError, "GraphQL schema unavailable: "+schemaErr.Error())
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxGraphQLRequestBytes)
		var in graphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeGraphQLError(w, http.StatusBadRequest, "invalid GraphQL request: "+err.Error())
			return
		}
		if strings.TrimSpace(in.Query) == "" {
			writeGraphQLError(w, http.StatusBadRequest, "query is required")
			return
		}
		exec := &graphQLExecution{store: s.Store, rest: rest, request: r}
		result := graphql.Do(graphql.Params{
			Schema:         schema,
			RequestString:  in.Query,
			OperationName:  in.OperationName,
			VariableValues: in.Variables,
			Context:        context.WithValue(r.Context(), graphQLContextKey{}, exec),
		})
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(result)
	})
	return s.requireAuth(h)
}

func writeGraphQLError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"data":   nil,
		"errors": []map[string]string{{"message": message}},
	})
}

// call은 REST 라우트를 같은 프로세스에서 부른다. 2xx가 아니면 REST 오류 메시지를 GraphQL 오류로 돌려준다.
func (e *graphQLExecution) call(ctx context.Context, method, path string, body any) (any, int, error) {
	if e.calls.Add(1) > maxGraphQLRESTCalls {
		return nil, 0, gqlErr(http.StatusTooManyRequests, "query is too complex (more than %d internal lookups)", maxGraphQLRESTCalls)
	}
	var content []byte
	if body != nil {
		var err error
		if content, err = json.Marshal(body); err != nil {
			return nil, 0, err
		}
	}
	u, err := url.Parse(path)
	if err != nil {
		return nil, 0, err
	}
	r := e.request.Clone(ctx)
	r.Method, r.URL, r.RequestURI = method, u, path
	r.GetBody = nil
	r.ContentLength = int64(len(content))
	r.Header.Del("Content-Length")
	if len(content) == 0 {
		r.Body = http.NoBody
		r.Header.Del("Content-Type")
	} else {
		r.Body = io.NopCloser(bytes.NewReader(content))
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	e.rest.ServeHTTP(w, r)
	data := w.Body.Bytes()
	if w.Code >= 300 {
		var apiErr struct {
			Error string `json:"error"`
		}
		msg := ""
		if json.Unmarshal(data, &apiErr) == nil {
			msg = apiErr.Error
		}
		if msg == "" {
			msg = strings.TrimSpace(string(data))
		}
		if msg == "" {
			msg = http.StatusText(w.Code)
		}
		return nil, w.Code, &graphQLError{message: msg, status: w.Code}
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, w.Code, nil
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, w.Code, fmt.Errorf("decode REST response: %w", err)
	}
	return normalizeRESTJSON(v, false), w.Code, nil
}

// rest는 resolver에서 쓰는 call 단축형이다.
func rest(p graphql.ResolveParams, method, path string, body any) (any, error) {
	exec, ok := p.Context.Value(graphQLContextKey{}).(*graphQLExecution)
	if !ok {
		return nil, errors.New("GraphQL request context missing")
	}
	v, _, err := exec.call(p.Context, method, path, body)
	return v, err
}

// restOK는 결과 본문 없이 성공 여부만 돌려준다(Boolean mutation).
func restOK(p graphql.ResolveParams, method, path string, body any) (any, error) {
	if _, err := rest(p, method, path, body); err != nil {
		return nil, err
	}
	return true, nil
}

// normalizeRESTJSON은 REST 응답의 키를 camelCase로 바꾼다. 변수 맵(variables/values)의 키는 사용자 데이터라 그대로 둔다.
func normalizeRESTJSON(value any, preserveKeys bool) any {
	switch v := value.(type) {
	case []any:
		for i := range v {
			v[i] = normalizeRESTJSON(v[i], false)
		}
		return v
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			name := key
			if !preserveKeys {
				name = snakeToCamel(key)
			}
			out[name] = normalizeRESTJSON(child, !preserveKeys && (key == "variables" || key == "values"))
		}
		return out
	default:
		return value
	}
}

func snakeToCamel(s string) string {
	if !strings.ContainsRune(s, '_') {
		return s
	}
	parts := strings.Split(s, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// ---- 값 도우미 ----

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asList(v any) []any {
	l, _ := v.([]any)
	if l == nil {
		return []any{}
	}
	return l
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func esc(v any) string { return url.PathEscape(asString(v)) }

// instancePath는 /api/services/{sid}/instances/{eid} 경로다.
func instancePath(sid, eid any) string {
	return "/api/services/" + esc(sid) + "/instances/" + esc(eid)
}

// ---- 도메인 id ----

// (서비스, 환경, 호스트)를 담은 불투명한 id를 만든다.

func domainID(sid, eid, host string) string {
	return "dom_" + base64.RawURLEncoding.EncodeToString([]byte(sid+"\n"+eid+"\n"+host))
}

func parseDomainID(id string) (sid, eid, host string, err error) {
	raw, ok := strings.CutPrefix(id, "dom_")
	b, decErr := base64.RawURLEncoding.DecodeString(raw)
	parts := strings.Split(string(b), "\n")
	if !ok || decErr != nil || len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", gqlErr(http.StatusBadRequest, "invalid domain id")
	}
	return parts[0], parts[1], parts[2], nil
}

// withDomainOwner는 도메인 맵에 소속 서비스/환경을 붙인다(id 계산용).
func withDomainOwner(d any, sid, eid string) map[string]any {
	m := asMap(d)
	if m == nil {
		return nil
	}
	m["serviceId"], m["environmentId"] = sid, eid
	return m
}

func splitDomains(instance map[string]any) map[string]any {
	sid, eid := asString(instance["serviceId"]), asString(instance["environmentId"])
	service, custom := []any{}, []any{}
	for _, d := range asList(instance["domains"]) {
		m := withDomainOwner(d, sid, eid)
		if asString(m["kind"]) == "custom" {
			custom = append(custom, m)
		} else {
			service = append(service, m)
		}
	}
	return map[string]any{"serviceDomains": service, "customDomains": custom}
}

// ---- 상태 값 변환 ----

func railwayDeploymentStatus(s string) string {
	switch s {
	case "live":
		return "SUCCESS"
	case "pre_deploy", "deploying", "healthchecking":
		return "DEPLOYING"
	case "":
		return ""
	}
	return strings.ToUpper(s)
}

func railwayDNSStatus(s string) string {
	switch s {
	case "active":
		return "VALID"
	case "proxied":
		return "VALID" // Cloudflare·터널 뒤: 레코드 값은 다르지만 정상 연결
	case "pending_dns", "":
		return "PENDING"
	}
	return "INVALID"
}

func railwayRecordStatus(s string) string {
	switch s {
	case "ok", "proxied":
		return "VALID"
	case "pending":
		return "PENDING"
	}
	return "INVALID"
}

// ---- JSON scalar ----

func jsonLiteral(value ast.Value) any {
	switch v := value.(type) {
	case *ast.ObjectValue:
		out := map[string]any{}
		for _, f := range v.Fields {
			out[f.Name.Value] = jsonLiteral(f.Value)
		}
		return out
	case *ast.ListValue:
		out := make([]any, len(v.Values))
		for i := range v.Values {
			out[i] = jsonLiteral(v.Values[i])
		}
		return out
	case *ast.IntValue:
		n, _ := strconv.ParseInt(v.Value, 10, 64)
		return n
	case *ast.FloatValue:
		f, _ := strconv.ParseFloat(v.Value, 64)
		return f
	case *ast.BooleanValue:
		return v.Value
	case *ast.StringValue:
		return v.Value
	case *ast.EnumValue:
		return v.Value
	}
	return nil
}

func newJSONScalar() *graphql.Scalar {
	return graphql.NewScalar(graphql.ScalarConfig{
		Name:         "JSON",
		Description:  "임의의 JSON 값입니다.",
		Serialize:    func(v any) any { return v },
		ParseValue:   func(v any) any { return v },
		ParseLiteral: jsonLiteral,
	})
}

// ---- connection(Relay 스타일 페이지) ----

func connectionArgs(extra graphql.FieldConfigArgument) graphql.FieldConfigArgument {
	args := graphql.FieldConfigArgument{
		"first": &graphql.ArgumentConfig{Type: graphql.Int},
		"after": &graphql.ArgumentConfig{Type: graphql.String},
	}
	for k, v := range extra {
		args[k] = v
	}
	return args
}

func paginate(items []any, args map[string]any) (map[string]any, error) {
	start := 0
	if after := asString(args["after"]); after != "" {
		b, err := base64.RawURLEncoding.DecodeString(after)
		i, convErr := strconv.Atoi(string(b))
		if err != nil || convErr != nil || i < 0 || i >= len(items) {
			return nil, gqlErr(http.StatusBadRequest, "invalid cursor")
		}
		start = i + 1
	}
	first := 100
	if n, ok := args["first"].(int); ok {
		if n < 0 {
			return nil, gqlErr(http.StatusBadRequest, "first must be zero or greater")
		}
		first = min(n, 500)
	}
	end := min(start+first, len(items))
	edges := make([]any, 0, end-start)
	var endCursor any
	for i := start; i < end; i++ {
		c := base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(i)))
		endCursor = c
		edges = append(edges, map[string]any{"cursor": c, "node": items[i]})
	}
	return map[string]any{
		"edges":      edges,
		"totalCount": len(items),
		"pageInfo":   map[string]any{"hasNextPage": end < len(items), "hasPreviousPage": start > 0, "endCursor": endCursor},
	}, nil
}
