package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/graphql-go/graphql"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/events"
	"github.com/turin-dev/aven/internal/logbook"
	"github.com/turin-dev/aven/internal/store"
)

type gqlResult struct {
	Data   map[string]any `json:"data"`
	Errors []struct {
		Message    string         `json:"message"`
		Extensions map[string]any `json:"extensions"`
	} `json:"errors"`
}

func gql(t *testing.T, h http.Handler, auth func(*http.Request), query string, vars map[string]any) gqlResult {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	w := req(h, http.MethodPost, "/graphql/v2", string(body), auth)
	if w.Code != http.StatusOK {
		t.Fatalf("GraphQL HTTP %d: %s", w.Code, w.Body.String())
	}
	var out gqlResult
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return out
}

// mustGQL은 오류 없는 응답의 data다.
func mustGQL(t *testing.T, h http.Handler, auth func(*http.Request), query string, vars map[string]any) map[string]any {
	t.Helper()
	out := gql(t, h, auth, query, vars)
	if len(out.Errors) > 0 {
		t.Fatalf("GraphQL errors for %s: %+v", query, out.Errors)
	}
	return out.Data
}

// dig는 a.b.0.c 경로의 값을 꺼낸다.
func dig(v any, path string) any {
	for _, k := range strings.Split(path, ".") {
		switch c := v.(type) {
		case map[string]any:
			v = c[k]
		case []any:
			i := 0
			for _, ch := range k {
				i = i*10 + int(ch-'0')
			}
			if i >= len(c) {
				return nil
			}
			v = c[i]
		default:
			return nil
		}
	}
	return v
}

type gqlFixture struct {
	mgr   *deploy.Manager
	st    *store.Store
	h     http.Handler
	owner func(*http.Request)
	p     *store.Project
	env   *store.Environment
	svc   *store.Service
}

func newGQLFixture(t *testing.T) *gqlFixture {
	t.Helper()
	ctx := context.Background()
	st := testStore(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := &deploy.Manager{Store: st, Docker: testDocker(), Cfg: deploy.Config{DataDir: t.TempDir(), BaseDomain: "aven.example.com"},
		Log: log, Dispatch: func(string, string) {}}
	s := &Server{Store: st, Deploy: mgr, Events: &events.Bus{Store: st}, AdminPassword: "password123", SessionKey: make([]byte, 32), Log: log,
		envOps: &fakeEnvOps{st: st}}
	p := &store.Project{Name: "graphql-project"}
	env, err := st.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	svc := &store.Service{ProjectID: p.ID, Name: "web", SourceType: "image"}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	img := "nginx:1"
	in := &store.Instance{ServiceID: svc.ID, EnvironmentID: env.ID}
	in.Image = &img
	in.Variables = map[string]string{"PORT": "8080", "URL": "http://${{RAILWAY_PRIVATE_DOMAIN}}"}
	in.Domains = []store.Domain{{Host: "web.aven.example.com", Kind: "generated"}, {Host: "api.example.org", Kind: "custom"}}
	if err := st.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	owner := testUser(t, st, "graphql-owner@example.com", store.RoleOwner)
	cookie := testSessionCookie(t, s, owner)
	return &gqlFixture{mgr: mgr, st: st, h: s.Handler(), owner: withCookie(cookie), p: p, env: env, svc: svc}
}

func TestGraphQLIntrospectionAndQueries(t *testing.T) {
	f := newGQLFixture(t)
	data := mustGQL(t, f.h, f.owner, `{ __schema { queryType { name } mutationType { name } types { name } } }`, nil)
	if dig(data, "__schema.queryType.name") != "Query" || dig(data, "__schema.mutationType.name") != "Mutation" {
		t.Fatalf("introspection: %v", data)
	}

	data = mustGQL(t, f.h, f.owner, `query project($id: String!) {
		project(id: $id) { id name
			environments { edges { node { id name isEphemeral } } }
			services(first: 1) { totalCount pageInfo { hasNextPage endCursor } edges { node { id name projectId } } } } }`,
		map[string]any{"id": f.p.ID})
	if dig(data, "project.name") != f.p.Name || dig(data, "project.environments.edges.0.node.id") != f.env.ID ||
		dig(data, "project.environments.edges.0.node.isEphemeral") != false {
		t.Fatalf("project: %v", data)
	}
	if dig(data, "project.services.edges.0.node.id") != f.svc.ID || dig(data, "project.services.totalCount") != float64(1) {
		t.Fatalf("services: %v", data)
	}

	data = mustGQL(t, f.h, f.owner, `{ projects { edges { node { id services { totalCount } } } } me { email } }`, nil)
	found := false
	for _, e := range dig(data, "projects.edges").([]any) {
		if dig(e, "node.id") == f.p.ID {
			found = dig(e, "node.services.totalCount") == float64(1)
		}
	}
	if !found || dig(data, "me.email") != "graphql-owner@example.com" {
		t.Fatalf("projects/me: %v", data)
	}

	data = mustGQL(t, f.h, f.owner, `query($sid: String!, $eid: String!) {
		service(id: $sid) { name project { id } serviceInstances { edges { node { environmentId } } } }
		environment(id: $eid) { name serviceInstances { totalCount } }
		serviceInstance(serviceId: $sid, environmentId: $eid) { source { image } numReplicas restartPolicyType
			domains { serviceDomains { domain suffix } customDomains { id domain status { verified dnsRecords { status } } } } }
		variables(projectId: "x", environmentId: $eid, serviceId: $sid, unrendered: true)
		domains(projectId: "x", environmentId: $eid, serviceId: $sid) { customDomains { id domain } }
	}`, map[string]any{"sid": f.svc.ID, "eid": f.env.ID})
	if dig(data, "service.name") != "web" || dig(data, "service.project.id") != f.p.ID ||
		dig(data, "service.serviceInstances.edges.0.node.environmentId") != f.env.ID {
		t.Fatalf("service: %v", data)
	}
	if dig(data, "environment.serviceInstances.totalCount") != float64(1) {
		t.Fatalf("environment: %v", data)
	}
	if dig(data, "serviceInstance.source.image") != "nginx:1" || dig(data, "serviceInstance.restartPolicyType") != "ON_FAILURE" ||
		dig(data, "serviceInstance.domains.serviceDomains.0.suffix") != "aven.example.com" ||
		dig(data, "serviceInstance.domains.customDomains.0.domain") != "api.example.org" {
		t.Fatalf("serviceInstance: %v", data)
	}
	if dig(data, "variables.URL") != "http://${{RAILWAY_PRIVATE_DOMAIN}}" || dig(data, "variables.PORT") != "8080" {
		t.Fatalf("variables keep user keys and raw references: %v", data["variables"])
	}
	id, _ := dig(data, "domains.customDomains.0.id").(string)
	sid, eid, host, err := parseDomainID(id)
	if err != nil || sid != f.svc.ID || eid != f.env.ID || host != "api.example.org" {
		t.Fatalf("domain id %q: %s %s %s %v", id, sid, eid, host, err)
	}
	data = mustGQL(t, f.h, f.owner, `query($id: String!) { customDomain(id: $id, projectId: "x") { domain } }`, map[string]any{"id": id})
	if dig(data, "customDomain.domain") != "api.example.org" {
		t.Fatalf("customDomain: %v", data)
	}

	// 오류는 REST 상태를 extensions.code로 알려 준다.
	out := gql(t, f.h, f.owner, `{ project(id: "prj_missing") { id } }`, nil)
	if len(out.Errors) != 1 || out.Errors[0].Extensions["code"] != "NOT_FOUND" {
		t.Fatalf("missing project: %+v", out)
	}
}

func TestGraphQLMutations(t *testing.T) {
	ctx := context.Background()
	f := newGQLFixture(t)
	vars := map[string]any{"pid": f.p.ID, "sid": f.svc.ID, "eid": f.env.ID}

	// 서비스 변수: upsert(참조 그대로), 여러 개, 지우기
	mustGQL(t, f.h, f.owner, `mutation($pid: String!, $sid: String!, $eid: String!) {
		a: variableUpsert(input: {projectId: $pid, environmentId: $eid, serviceId: $sid, name: "API_KEY", value: "k1", skipDeploys: true})
		b: variableCollectionUpsert(input: {projectId: $pid, environmentId: $eid, serviceId: $sid, variables: {NODE_ENV: "production", DB: "${{Postgres.DATABASE_URL}}"}, skipDeploys: true})
		c: variableDelete(input: {projectId: $pid, environmentId: $eid, serviceId: $sid, name: "PORT"})
	}`, vars)
	in, err := f.st.GetInstanceFor(ctx, f.svc.ID, f.env.ID)
	if err != nil {
		t.Fatal(err)
	}
	if in.Variables["API_KEY"] != "k1" || in.Variables["NODE_ENV"] != "production" || in.Variables["DB"] != "${{Postgres.DATABASE_URL}}" {
		t.Fatalf("variables after upsert: %v", in.Variables)
	}
	if _, ok := in.Variables["PORT"]; ok {
		t.Fatalf("variableDelete kept PORT: %v", in.Variables)
	}

	// replace는 넘긴 변수만 남긴다
	mustGQL(t, f.h, f.owner, `mutation($sid: String!, $eid: String!) {
		variableCollectionUpsert(input: {environmentId: $eid, serviceId: $sid, variables: {ONLY: "1"}, replace: true, skipDeploys: true}) }`, vars)
	if in, _ = f.st.GetInstanceFor(ctx, f.svc.ID, f.env.ID); len(in.Variables) != 1 || in.Variables["ONLY"] != "1" {
		t.Fatalf("replace: %v", in.Variables)
	}

	// 공유 변수(serviceId 없음): 합치기, 지우기, 조회
	mustGQL(t, f.h, f.owner, `mutation($eid: String!) {
		a: variableCollectionUpsert(input: {environmentId: $eid, variables: {SHARED_A: "1", SHARED_B: "2"}})
		b: variableUpsert(input: {environmentId: $eid, name: "SHARED_C", value: "3"})
		c: variableDelete(input: {environmentId: $eid, name: "SHARED_A"})
	}`, vars)
	data := mustGQL(t, f.h, f.owner, `query($eid: String!) { variables(projectId: "x", environmentId: $eid) }`, vars)
	shared, _ := data["variables"].(map[string]any)
	if len(shared) != 2 || shared["SHARED_B"] != "2" || shared["SHARED_C"] != "3" {
		t.Fatalf("shared variables: %v", data)
	}

	data = mustGQL(t, f.h, f.owner, `mutation($sid: String!, $eid: String!) {
		serviceInstanceUpdate(serviceId: $sid, environmentId: $eid, input: {startCommand: "npm start", numReplicas: 2,
			restartPolicyType: ALWAYS, rootDirectory: "app", healthcheckPath: "/health", source: {image: "nginx:2"}}) }`, vars)
	if data["serviceInstanceUpdate"] != true {
		t.Fatalf("serviceInstanceUpdate: %v", data)
	}
	in, _ = f.st.GetInstanceFor(ctx, f.svc.ID, f.env.ID)
	if in.StartCommand == nil || *in.StartCommand != "npm start" || in.Replicas != 2 || in.RestartPolicy != "always" ||
		in.RootDir != "/app" || in.HealthcheckPath != "/health" || in.Image == nil || *in.Image != "nginx:2" {
		t.Fatalf("instance spec: %+v", in.Spec)
	}

	// 프로젝트 / 환경 / 서비스 / 볼륨
	data = mustGQL(t, f.h, f.owner, `mutation($pid: String!, $eid: String!) {
		projectUpdate(id: $pid, input: {description: "via graphql"}) { description }
		environmentCreate(input: {projectId: $pid, name: "staging", sourceEnvironmentId: $eid}) { id name }
		serviceCreate(input: {projectId: $pid, name: "redis", source: {image: "redis:7-alpine"}, variables: {MAXMEMORY: "64mb"}}) { id name sourceType }
	}`, vars)
	if dig(data, "projectUpdate.description") != "via graphql" || dig(data, "environmentCreate.name") != "staging" ||
		dig(data, "serviceCreate.name") != "redis" {
		t.Fatalf("create mutations: %v", data)
	}
	stagingID, _ := dig(data, "environmentCreate.id").(string)
	redisID, _ := dig(data, "serviceCreate.id").(string)
	if rin, err := f.st.GetInstanceFor(ctx, redisID, f.env.ID); err != nil || rin.Variables["MAXMEMORY"] != "64mb" {
		t.Fatalf("serviceCreate variables: %v %v", rin, err)
	}
	data = mustGQL(t, f.h, f.owner, `mutation($pid: String!, $sid: String!, $env: String!) {
		environmentRename(id: $env, input: {name: "qa"})
		serviceUpdate(id: $sid, input: {name: "cache"}) { name }
		volumeCreate(input: {projectId: $pid, serviceId: $sid, mountPath: "/data"}) { id mountPath environmentId }
	}`, map[string]any{"pid": f.p.ID, "sid": redisID, "env": stagingID})
	if data["environmentRename"] != true || dig(data, "serviceUpdate.name") != "cache" || dig(data, "volumeCreate.mountPath") != "/data" ||
		dig(data, "volumeCreate.environmentId") != f.env.ID {
		t.Fatalf("update mutations: %v", data)
	}
	volID, _ := dig(data, "volumeCreate.id").(string)
	data = mustGQL(t, f.h, f.owner, `query($pid: String!) { project(id: $pid) { volumes { edges { node { id } } } } environments(projectId: $pid) { edges { node { name } } } }`, vars)
	if dig(data, "project.volumes.edges.0.node.id") != volID || dig(data, "environments.edges.1.node.name") != "qa" {
		t.Fatalf("project volumes / environments: %v", data)
	}
	data = mustGQL(t, f.h, f.owner, `mutation($vid: String!) { volumeInstanceUpdate(volumeId: $vid, input: {serviceId: ""}) }`, map[string]any{"vid": volID})
	if v, err := f.st.GetVolume(ctx, volID); err != nil || v.InstanceID != nil {
		t.Fatalf("volume not detached: %+v %v", v, err)
	}
	// 볼륨/서비스 삭제는 docker가 필요해 여기서는 환경 삭제만 본다.
	data = mustGQL(t, f.h, f.owner, `mutation($env: String!) { environmentDelete(id: $env) }`, map[string]any{"env": stagingID})
	if data["environmentDelete"] != true {
		t.Fatalf("environmentDelete: %v", data)
	}
	if _, err := f.st.GetEnvironment(ctx, stagingID); err == nil {
		t.Fatal("environmentDelete kept the environment")
	}

	// 잘못된 입력은 REST 오류 메시지와 BAD_USER_INPUT
	out := gql(t, f.h, f.owner, `mutation($sid: String!, $eid: String!) {
		variableUpsert(input: {environmentId: $eid, serviceId: $sid, name: "1BAD", value: "x", skipDeploys: true}) }`, vars)
	if len(out.Errors) != 1 || out.Errors[0].Extensions["code"] != "BAD_USER_INPUT" {
		t.Fatalf("invalid variable name: %+v", out)
	}
}

func TestGraphQLRequiresAuthAndEnforcesRBAC(t *testing.T) {
	ctx := context.Background()
	f := newGQLFixture(t)
	if w := req(f.h, http.MethodPost, "/graphql/v2", `{"query":"{ projects { totalCount } }"}`, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GraphQL status %d: %s", w.Code, w.Body.String())
	}
	if w := req(f.h, http.MethodGet, "/graphql/v2", "", nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Aven GraphQL") {
		t.Fatalf("playground page: %d", w.Code)
	}

	viewer := testUser(t, f.st, "graphql-viewer@example.com", store.RoleViewer)
	if err := f.st.SetMember(ctx, f.p.ID, viewer.ID, store.RoleViewer); err != nil {
		t.Fatal(err)
	}
	outsider := testUser(t, f.st, "graphql-outsider@example.com", store.RoleViewer)
	tok := &store.APIToken{UserID: viewer.ID, Name: "ci", Prefix: "keel_gq", Scope: store.ScopeWorkspace, Role: store.RoleViewer}
	if err := f.st.CreateAPIToken(ctx, tok, hashSecret("keel_gqlviewer")); err != nil {
		t.Fatal(err)
	}
	asViewer := withBearer("keel_gqlviewer")
	vars := map[string]any{"sid": f.svc.ID, "eid": f.env.ID}

	// viewer는 읽을 수 있지만 바꿀 수 없다
	data := mustGQL(t, f.h, asViewer, `query($sid: String!, $eid: String!) { serviceInstance(serviceId: $sid, environmentId: $eid) { id } }`, vars)
	if dig(data, "serviceInstance.id") == nil {
		t.Fatalf("viewer read: %v", data)
	}
	out := gql(t, f.h, asViewer, `mutation($sid: String!, $eid: String!) {
		serviceInstanceUpdate(serviceId: $sid, environmentId: $eid, input: {startCommand: "echo denied"}) }`, vars)
	if len(out.Errors) != 1 || out.Errors[0].Extensions["code"] != "FORBIDDEN" {
		t.Fatalf("viewer mutation: %+v", out)
	}
	if in, _ := f.st.GetInstanceFor(ctx, f.svc.ID, f.env.ID); in.StartCommand != nil {
		t.Fatalf("viewer mutation changed instance: %q", *in.StartCommand)
	}

	// 멤버가 아닌 사용자에게는 프로젝트가 보이지 않는다(목록, 단건, 저장소 조회를 거치는 service 모두)
	outsiderAuth := withCookie(testSessionCookie(t, &Server{Store: f.st}, outsider))
	data = mustGQL(t, f.h, outsiderAuth, `{ projects { totalCount } }`, nil)
	if dig(data, "projects.totalCount") != float64(0) {
		t.Fatalf("outsider projects: %v", data)
	}
	for _, q := range []string{`query($sid: String!) { service(id: $sid) { name } }`, `query($eid: String!) { environment(id: $eid) { name } }`} {
		if out := gql(t, f.h, outsiderAuth, q, vars); len(out.Errors) != 1 || dig(out.Data, "service.name") != nil {
			t.Fatalf("outsider %s: %+v", q, out)
		}
	}
}

func TestGraphQLRESTCallLimit(t *testing.T) {
	f := newGQLFixture(t)
	// 별칭마다 내부 REST 호출이 둘(인스턴스, 최근 배포)이라 필드 수 상한 안에서 호출 상한을 넘는다.
	var b strings.Builder
	b.WriteString("{")
	for i := 0; i < maxGraphQLRESTCalls/2+5; i++ {
		b.WriteString(" s" + itoa(i) + `: serviceInstance(serviceId: "` + f.svc.ID + `", environmentId: "` + f.env.ID + `") { latestDeployment { id } }`)
	}
	b.WriteString("}")
	out := gql(t, f.h, f.owner, b.String(), nil)
	if len(out.Errors) == 0 || out.Errors[0].Extensions["code"] != "RATE_LIMITED" {
		t.Fatalf("expected call limit error, got %d errors", len(out.Errors))
	}
}

// gqlRaw는 상태 코드와 함께 응답을 돌려준다(400 응답을 보는 테스트용).
func gqlRaw(t *testing.T, h http.Handler, auth func(*http.Request), body string) (int, gqlResult, map[string]any) {
	t.Helper()
	w := req(h, http.MethodPost, "/graphql/v2", body, auth)
	var out gqlResult
	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	json.Unmarshal(w.Body.Bytes(), &raw)
	return w.Code, out, raw
}

func TestGraphQLRequestErrorsUseRailwayCodes(t *testing.T) {
	f := newGQLFixture(t)
	q := func(query string, vars map[string]any, op string) string {
		b, _ := json.Marshal(map[string]any{"query": query, "variables": vars, "operationName": op})
		return string(b)
	}
	deep := "{ project(id: \"x\") " + strings.Repeat("{ services { edges { node { project ", 4) + "{ id }" + strings.Repeat(" } } } }", 4) + " }"
	wide := strings.Builder{}
	wide.WriteString("{ me {")
	for i := 0; i < maxGraphQLFields+1; i++ {
		wide.WriteString(" e" + itoa(i) + ": email")
	}
	wide.WriteString(" } }")
	// fragment를 겹겹이 펼치면 필드 수가 2^20이 된다. 계산은 fragment마다 한 번이어야 한다.
	bomb := strings.Builder{}
	bomb.WriteString("{ me { ...F0 } }\n fragment F20 on User { id }\n")
	for i := 19; i >= 0; i-- {
		bomb.WriteString("fragment F" + itoa(i) + " on User { ...F" + itoa(i+1) + " ...F" + itoa(i+1) + " }\n")
	}
	for _, tc := range []struct {
		name, body, code, msg string
		status                int
	}{
		{"not json", `[{"query":"{ me { id } }"}]`, "BAD_USER_INPUT", "invalid GraphQL request body", 400},
		{"empty query", `{"query":"  "}`, "BAD_USER_INPUT", "query is required", 400},
		{"syntax", q("{ me { id ", nil, ""), "GRAPHQL_PARSE_FAILED", "Syntax Error", 400},
		{"unknown field", q("{ nope }", nil, ""), "GRAPHQL_VALIDATION_FAILED", `Cannot query field "nope" on type "Query"`, 400},
		{"missing argument", q("{ project { id } }", nil, ""), "GRAPHQL_VALIDATION_FAILED", `argument "id"`, 400},
		{"bad variable", q("query($n: Int!) { projects(first: $n) { totalCount } }", map[string]any{"n": "many"}, ""), "BAD_USER_INPUT", `Variable "$n"`, 400},
		{"missing variable", q("query($id: String!) { project(id: $id) { id } }", nil, ""), "BAD_USER_INPUT", `Variable "$id"`, 400},
		{"unknown operation", q("query A { me { id } } query B { me { id } }", nil, "C"), "BAD_USER_INPUT", `Unknown operation named "C"`, 400},
		{"too deep", q(deep, nil, ""), "GRAPHQL_VALIDATION_FAILED", "levels deep", 400},
		{"too many fields", q(wide.String(), nil, ""), "GRAPHQL_VALIDATION_FAILED", "more than 1000 fields", 400},
		{"fragment bomb", q(bomb.String(), nil, ""), "GRAPHQL_VALIDATION_FAILED", "more than 1000 fields", 400},
		{"query too large", q("{ me { id } }"+strings.Repeat(" ", maxGraphQLQueryBytes), nil, ""), "GRAPHQL_VALIDATION_FAILED", "larger than", 400},
		{"resolver error", q(`{ project(id: "prj_missing") { id } }`, nil, ""), "NOT_FOUND", "not found", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, out, raw := gqlRaw(t, f.h, f.owner, tc.body)
			if status != tc.status || len(out.Errors) == 0 || out.Errors[0].Extensions["code"] != tc.code || !strings.Contains(out.Errors[0].Message, tc.msg) {
				t.Fatalf("status %d, errors %+v; want %d %s %q", status, out.Errors, tc.status, tc.code, tc.msg)
			}
			if _, ok := raw["data"]; !ok {
				t.Fatalf("response must always carry data (null on request errors): %v", raw)
			}
		})
	}
	if w := req(f.h, http.MethodPost, "/graphql/v2", `{"query":"`+strings.Repeat("a", maxGraphQLRequestBytes)+`"}`, f.owner); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body: %d", w.Code)
	}
}

// 표준 introspection 쿼리(GraphiQL·codegen이 보내는 것)는 깊이·크기 제한에 걸리지 않아야 한다.
func TestGraphQLIntrospectionQueryFitsLimits(t *testing.T) {
	f := newGQLFixture(t)
	const introspection = `query IntrospectionQuery { __schema { queryType { name } mutationType { name } subscriptionType { name }
		types { ...FullType } directives { name description locations args { ...InputValue } } } }
	fragment FullType on __Type { kind name description
		fields(includeDeprecated: true) { name description args { ...InputValue } type { ...TypeRef } isDeprecated deprecationReason }
		inputFields { ...InputValue } interfaces { ...TypeRef }
		enumValues(includeDeprecated: true) { name description isDeprecated deprecationReason } possibleTypes { ...TypeRef } }
	fragment InputValue on __InputValue { name description type { ...TypeRef } defaultValue }
	fragment TypeRef on __Type { kind name ofType { kind name ofType { kind name ofType { kind name ofType { kind name
		ofType { kind name ofType { kind name ofType { kind name } } } } } } } }`
	data := mustGQL(t, f.h, f.owner, introspection, nil)
	if dig(data, "__schema.queryType.name") != "Query" {
		t.Fatalf("introspection: %v", data)
	}
}

func httptestRequest() *http.Request {
	return httptest.NewRequest(http.MethodPost, "/graphql/v2", nil)
}

func graphqlDo(schema graphql.Schema, q string, ctx context.Context) *graphql.Result {
	return graphql.Do(graphql.Params{Schema: schema, RequestString: q, Context: ctx})
}

func TestNormalizeRESTJSONKeepsVariableKeys(t *testing.T) {
	in := map[string]any{"service_id": "s", "variables": map[string]any{"MY_VAR": "1"}, "domains": []any{map[string]any{"target_port": 80.0}}}
	out := normalizeRESTJSON(in, false).(map[string]any)
	if out["serviceId"] != "s" || dig(out, "variables.MY_VAR") != "1" || dig(out, "domains.0.targetPort") != 80.0 {
		t.Fatalf("normalize: %v", out)
	}
}

// TestGraphQLRESTMapping은 docker가 필요한 mutation(도메인, 배포 조작)이 어떤 REST 호출로 바뀌는지 가짜 REST mux로 본다.
func TestGraphQLRESTMapping(t *testing.T) {
	type call struct{ method, path, body string }
	var calls []call
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, call{r.Method, r.URL.RequestURI(), string(b)})
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/domains"):
			writeJSON(w, 201, map[string]any{"host": "x.example.net", "kind": "custom", "target_port": 3000, "status": "pending_dns",
				"dns":          []any{map[string]any{"type": "A", "name": "x.example.net", "value": "203.0.113.1"}},
				"verification": map[string]any{"txt": map[string]any{"name": "_aven.x.example.net", "value": "aven-verify=abc"}}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/deployments/"):
			writeJSON(w, 200, map[string]any{"id": "dep_1", "service_id": "svc_1", "environment_id": "env_1", "status": "live"})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/deployments"):
			writeJSON(w, 200, []any{map[string]any{"id": "dep_9", "status": "healthchecking"}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/redeploy"):
			writeJSON(w, 202, map[string]any{"id": "dep_10", "status": "queued"})
		default:
			w.WriteHeader(204)
		}
	})
	schema, err := newGraphQLSchema()
	if err != nil {
		t.Fatal(err)
	}
	run := func(q string) map[string]any {
		t.Helper()
		calls = nil
		r := httptestRequest()
		exec := &graphQLExecution{rest: mux, request: r}
		res := graphqlDo(schema, q, context.WithValue(r.Context(), graphQLContextKey{}, exec))
		if len(res.Errors) > 0 {
			t.Fatalf("%s: %v", q, res.Errors)
		}
		return res.Data.(map[string]any)
	}

	data := run(`mutation { customDomainCreate(input: {serviceId: "svc_1", environmentId: "env_1", domain: "x.example.net", targetPort: 3000}) {
		id domain targetPort status { verified verificationToken verificationDnsHost dnsRecords { recordType requiredValue status } } } }`)
	if len(calls) != 1 || calls[0].path != "/api/services/svc_1/instances/env_1/domains" || !strings.Contains(calls[0].body, `"host":"x.example.net"`) ||
		!strings.Contains(calls[0].body, `"target_port":3000`) {
		t.Fatalf("customDomainCreate calls: %+v", calls)
	}
	if dig(data, "customDomainCreate.status.dnsRecords.0.status") != "PENDING" || dig(data, "customDomainCreate.status.dnsRecords.0.recordType") != "A" ||
		dig(data, "customDomainCreate.status.verified") != false || dig(data, "customDomainCreate.status.verificationToken") != "aven-verify=abc" ||
		dig(data, "customDomainCreate.status.verificationDnsHost") != "_aven.x.example.net" {
		t.Fatalf("customDomainCreate: %v", data)
	}
	id := dig(data, "customDomainCreate.id").(string)

	run(`mutation { customDomainUpdate(id: "` + id + `", environmentId: "env_1", targetPort: 8080) }`)
	if len(calls) != 1 || calls[0].method != http.MethodPatch || calls[0].path != "/api/services/svc_1/instances/env_1/domains/x.example.net" ||
		calls[0].body != `{"target_port":8080}` {
		t.Fatalf("customDomainUpdate calls: %+v", calls)
	}
	run(`mutation { customDomainDelete(id: "` + id + `") }`)
	if len(calls) != 1 || calls[0].method != http.MethodDelete || calls[0].path != "/api/services/svc_1/instances/env_1/domains/x.example.net" {
		t.Fatalf("customDomainDelete calls: %+v", calls)
	}
	run(`mutation { serviceDomainCreate(input: {serviceId: "svc_1", environmentId: "env_1"}) { id } }`)
	if len(calls) != 1 || calls[0].body != `{}` {
		t.Fatalf("serviceDomainCreate must not send a host: %+v", calls)
	}

	run(`mutation { environmentTriggersDeploy(input: {environmentId: "env_1", projectId: "prj_1", serviceId: "svc_1"}) }`)
	if len(calls) != 1 || calls[0].method != http.MethodPost || calls[0].path != "/api/services/svc_1/instances/env_1/deploy" {
		t.Fatalf("environmentTriggersDeploy calls: %+v", calls)
	}
	run(`mutation { deploymentRestart(id: "dep_1") }`)
	if len(calls) != 2 || calls[1].path != "/api/services/svc_1/instances/env_1/restart" {
		t.Fatalf("deploymentRestart calls: %+v", calls)
	}
	data = run(`mutation { serviceInstanceRedeploy(serviceId: "svc_1", environmentId: "env_1") }`)
	if len(calls) != 2 || calls[0].path != "/api/services/svc_1/instances/env_1/deployments?limit=1" || calls[1].path != "/api/deployments/dep_9/redeploy" {
		t.Fatalf("serviceInstanceRedeploy calls: %+v", calls)
	}
	data = run(`{ deployments(input: {serviceId: "svc_1", environmentId: "env_1"}, first: 5) { edges { node { id status keelStatus } } } }`)
	if dig(data, "deployments.edges.0.node.status") != "DEPLOYING" || dig(data, "deployments.edges.0.node.keelStatus") != "healthchecking" {
		t.Fatalf("deployment status mapping: %v", data)
	}
	run(`mutation { serviceInstanceUpdate(serviceId: "svc_1", environmentId: "env_1", input: {source: {repo: "acme/api"}, watchPatterns: ["src/**"]}) }`)
	if len(calls) != 1 || !strings.Contains(calls[0].body, `"repo_url":"https://github.com/acme/api"`) || !strings.Contains(calls[0].body, `"watch_paths":["src/**"]`) {
		t.Fatalf("serviceInstanceUpdate body: %+v", calls)
	}
}

func TestDomainIDRoundTrip(t *testing.T) {
	id := domainID("svc_a", "env_b", "app.example.com")
	sid, eid, host, err := parseDomainID(id)
	if err != nil || sid != "svc_a" || eid != "env_b" || host != "app.example.com" {
		t.Fatalf("round trip: %q %q %q %v", sid, eid, host, err)
	}
	for _, bad := range []string{"", "dom_", "dom_!!", "svc_a", domainID("", "env", "h")} {
		if _, _, _, err := parseDomainID(bad); err == nil {
			t.Errorf("parseDomainID(%q) accepted", bad)
		}
	}
}

func TestGraphQLDeploymentsRailwayFields(t *testing.T) {
	ctx := context.Background()
	f := newGQLFixture(t)
	in, err := f.st.GetInstanceFor(ctx, f.svc.ID, f.env.ID)
	if err != nil {
		t.Fatal(err)
	}
	img := "nginx:1"
	mk := func(status store.DeployStatus, image *string) *store.Deployment {
		d := &store.Deployment{ServiceID: f.svc.ID, EnvironmentID: f.env.ID, InstanceID: in.ID, Trigger: "manual", Image: image}
		if err := f.st.CreateDeployment(ctx, d); err != nil {
			t.Fatal(err)
		}
		if err := f.st.SetDeploymentStatus(ctx, d.ID, status); err != nil {
			t.Fatal(err)
		}
		return d
	}
	live := mk(store.DepLive, &img)
	cancelled := mk(store.DepCancelled, nil)
	failed := mk(store.DepFailed, &img)
	vars := map[string]any{"sid": f.svc.ID, "eid": f.env.ID}

	data := mustGQL(t, f.h, f.owner, `query($sid: String!, $eid: String!) {
		deployments(input: {serviceId: $sid, environmentId: $eid}) { edges { node { id status canRedeploy canRollback url staticUrl projectId
			service { name } environment { id } } } }
		serviceInstance(serviceId: $sid, environmentId: $eid) { serviceName region }
	}`, vars)
	byID := map[string]any{}
	for _, e := range dig(data, "deployments.edges").([]any) {
		byID[dig(e, "node.id").(string)] = dig(e, "node")
	}
	if n := byID[cancelled.ID]; dig(n, "status") != "REMOVED" || dig(n, "canRedeploy") != false || dig(n, "canRollback") != false {
		t.Errorf("cancelled: %v", n)
	}
	if n := byID[live.ID]; dig(n, "status") != "SUCCESS" || dig(n, "canRollback") != true || dig(n, "staticUrl") != "web.aven.example.com" ||
		dig(n, "url") != "https://web.aven.example.com" || dig(n, "projectId") != f.p.ID || dig(n, "service.name") != "web" ||
		dig(n, "environment.id") != f.env.ID {
		t.Errorf("live: %v", n)
	}
	if n := byID[failed.ID]; dig(n, "canRedeploy") != true || dig(n, "canRollback") != false {
		t.Errorf("failed: %v", n)
	}
	if dig(data, "serviceInstance.serviceName") != "web" || dig(data, "serviceInstance.region") != nil {
		t.Errorf("serviceInstance: %v", data["serviceInstance"])
	}

	count := func(status string) []string {
		t.Helper()
		data := mustGQL(t, f.h, f.owner, `query($sid: String!, $eid: String!) { deployments(input: {serviceId: $sid, environmentId: $eid, status: `+status+`}) { edges { node { status } } } }`, vars)
		out := []string{}
		for _, e := range dig(data, "deployments.edges").([]any) {
			out = append(out, dig(e, "node.status").(string))
		}
		return out
	}
	if got := strings.Join(count(`{successfulOnly: true}`), ","); got != "SUCCESS" {
		t.Errorf("successfulOnly: %s", got)
	}
	if got := count(`{in: ["FAILED", "REMOVED"]}`); len(got) != 2 {
		t.Errorf("in: %v", got)
	}
	if got := strings.Join(count(`{notIn: ["FAILED", "REMOVED"]}`), ","); got != "SUCCESS" {
		t.Errorf("notIn: %v", got)
	}
}

func TestGraphQLDeploymentLogs(t *testing.T) {
	ctx := context.Background()
	f := newGQLFixture(t)
	dir := t.TempDir()
	reg, err := logbook.NewRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	f.mgr.Logs = reg
	in, _ := f.st.GetInstanceFor(ctx, f.svc.ID, f.env.ID)
	d := &store.Deployment{ServiceID: f.svc.ID, EnvironmentID: f.env.ID, InstanceID: in.ID, Trigger: "manual"}
	if err := f.st.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	book, err := reg.Open(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(time.Second).Format("15:04:05")
	book.Line(at + " step 1/2 : FROM node")
	book.Line(at + " error: missing script")
	book.Line("plain line")

	writeCronLogs(t, f, d, "boot ok", "GET /health 200", "error: db down", "GET /health 200", "done")

	vars := map[string]any{"id": d.ID}
	data := mustGQL(t, f.h, f.owner, `query($id: String!) {
		build: buildLogs(deploymentId: $id) { timestamp message severity }
		errs: buildLogs(deploymentId: $id, filter: "err") { message }
		runtime: deploymentLogs(deploymentId: $id, limit: 2) { message }
		filtered: deploymentLogs(deploymentId: $id, filter: "-health", limit: 2) { message severity attributes { key value } }
		ranged: deploymentLogs(deploymentId: $id, startDate: "2026-01-02T03:04:01Z", endDate: "2026-01-02T03:04:02Z") { message }
	}`, vars)
	if dig(data, "build.0.message") != "step 1/2 : FROM node" || !strings.HasSuffix(dig(data, "build.0.timestamp").(string), "T"+at+"Z") ||
		dig(data, "build.1.severity") != "error" || dig(data, "build.2.message") != "plain line" {
		t.Errorf("buildLogs: %v", data["build"])
	}
	if l := data["errs"].([]any); len(l) != 1 || dig(l, "0.message") != "error: missing script" {
		t.Errorf("buildLogs filter: %v", l)
	}
	if l := data["runtime"].([]any); len(l) != 2 || dig(l, "1.message") != "done" {
		t.Errorf("deploymentLogs limit keeps the newest lines: %v", l)
	}
	// 필터는 tail을 자르기 전에 적용한다: 마지막 두 줄 중 하나만 맞지만 결과는 두 줄이다.
	if l := data["filtered"].([]any); len(l) != 2 || dig(l, "0.message") != "error: db down" || dig(l, "0.severity") != "error" ||
		dig(l, "0.attributes.0.key") != "replica" {
		t.Errorf("deploymentLogs filter: %v", l)
	}
	if l := data["ranged"].([]any); len(l) != 2 || dig(l, "0.message") != "GET /health 200" || dig(l, "1.message") != "error: db down" {
		t.Errorf("deploymentLogs date range: %v", l)
	}

	out := gql(t, f.h, f.owner, `query($id: String!) { deploymentLogs(deploymentId: $id, limit: 0) { message } }`, vars)
	if len(out.Errors) != 1 || out.Errors[0].Extensions["code"] != "BAD_USER_INPUT" {
		t.Errorf("limit 0: %+v", out.Errors)
	}
	out = gql(t, f.h, f.owner, `query($id: String!) { deploymentLogs(deploymentId: $id, startDate: "yesterday") { message } }`, vars)
	if len(out.Errors) != 1 || out.Errors[0].Extensions["code"] != "BAD_USER_INPUT" {
		t.Errorf("bad startDate: %+v", out.Errors)
	}
	// 프로젝트 멤버가 아니면 로그도 없다.
	outsider := testUser(t, f.st, "graphql-logs-outsider@example.com", store.RoleViewer)
	out = gql(t, f.h, withCookie(testSessionCookie(t, &Server{Store: f.st}, outsider)), `query($id: String!) { buildLogs(deploymentId: $id) { message } }`, vars)
	if len(out.Errors) != 1 || out.Data["buildLogs"] != nil {
		t.Errorf("outsider logs: %+v", out)
	}
}

func TestGraphQLRailwayInputsAndSafety(t *testing.T) {
	ctx := context.Background()
	f := newGQLFixture(t)
	vars := map[string]any{"sid": f.svc.ID, "eid": f.env.ID}
	// serviceDelete(environmentId)는 모든 환경에서 지우지 않고 거부한다.
	out := gql(t, f.h, f.owner, `mutation($sid: String!, $eid: String!) { serviceDelete(id: $sid, environmentId: $eid) }`, vars)
	if len(out.Errors) != 1 || out.Errors[0].Extensions["code"] != "BAD_USER_INPUT" {
		t.Fatalf("serviceDelete with environmentId: %+v", out)
	}
	if _, err := f.st.GetService(ctx, f.svc.ID); err != nil {
		t.Fatal("serviceDelete with environmentId deleted the service")
	}
	for _, q := range []string{
		`mutation($sid: String!, $eid: String!) { serviceInstanceDeployV2(serviceId: $sid, environmentId: $eid, commitSha: "abc123") }`,
		`mutation($sid: String!, $eid: String!) { serviceInstanceUpdate(serviceId: $sid, environmentId: $eid, input: {buildCommand: "make"}) }`,
	} {
		if out := gql(t, f.h, f.owner, q, vars); len(out.Errors) != 1 || out.Errors[0].Extensions["code"] != "BAD_USER_INPUT" {
			t.Errorf("%s: %+v", q, out)
		}
	}
	// Railway 전용 입력은 받는다(region, isPublic, stageInitialChanges).
	data := mustGQL(t, f.h, f.owner, `mutation($sid: String!, $eid: String!) {
		serviceInstanceUpdate(serviceId: $sid, environmentId: $eid, input: {region: "us-west1", numReplicas: 2}) }`, vars)
	if data["serviceInstanceUpdate"] != true {
		t.Fatalf("region: %v", data)
	}
	data = mustGQL(t, f.h, f.owner, `mutation {
		projectCreate(input: {name: "railway-style", isPublic: false, prDeploys: true, defaultEnvironmentName: "prod"}) {
			id name environments { edges { node { name } } } } }`, nil)
	if dig(data, "projectCreate.name") != "railway-style" || dig(data, "projectCreate.environments.edges.0.node.name") != "prod" {
		t.Fatalf("projectCreate: %v", data)
	}
	pid := dig(data, "projectCreate.id").(string)
	if p, err := f.st.GetProject(ctx, pid); err != nil || !p.PREnvironments.Enabled {
		t.Fatalf("prDeploys not applied: %+v %v", p, err)
	}
	data = mustGQL(t, f.h, f.owner, `mutation($pid: String!) { projectUpdate(id: $pid, input: {prDeploys: false, isPublic: true}) { id } }`, map[string]any{"pid": pid})
	if p, _ := f.st.GetProject(ctx, pid); p.PREnvironments.Enabled {
		t.Fatalf("projectUpdate prDeploys: %v", data)
	}
	data = mustGQL(t, f.h, f.owner, `{ projects { edges { node { updatedAt } } } }`, nil)
	if dig(data, "projects.edges.0.node.updatedAt") != nil {
		t.Errorf("updatedAt: %v", data)
	}
}

// writeCronLogs는 컨테이너 없는 배포의 런타임 로그(cron 실행 기록)를 만든다. 시각은 2026-01-02T03:04:0i다.
func writeCronLogs(t *testing.T, f *gqlFixture, d *store.Deployment, msgs ...string) {
	t.Helper()
	ctx := context.Background()
	run := &store.CronRun{DeploymentID: d.ID, InstanceID: d.InstanceID, StartedAt: time.Now(), Status: "succeeded"}
	if err := f.st.CreateCronRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(f.mgr.Cfg.DataDir, "logs", "cron")
	os.MkdirAll(logDir, 0o755)
	var jsonl strings.Builder
	for i, msg := range msgs {
		b, _ := json.Marshal(deploy.LogEntry{T: time.Date(2026, 1, 2, 3, 4, i, 0, time.UTC).Format(time.RFC3339Nano), Replica: "r1", Stream: "stdout", Msg: msg})
		jsonl.Write(append(b, '\n'))
	}
	if err := os.WriteFile(filepath.Join(logDir, run.ID+".jsonl"), []byte(jsonl.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}
