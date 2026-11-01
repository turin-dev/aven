package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/events"
	"github.com/turin-dev/aven/internal/store"
)

func TestMCPModernDiscoverToolsAndBearerAuth(t *testing.T) {
	s := &Server{APIToken: "mcp-owner-token", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := s.Handler()

	withoutToken := req(h, http.MethodPost, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{}}`, nil)
	if withoutToken.Code != http.StatusUnauthorized {
		t.Fatalf("POST /mcp without a Bearer token: %d %s", withoutToken.Code, withoutToken.Body)
	}
	if got := req(h, http.MethodGet, "/mcp", "", withBearer("mcp-owner-token")).Code; got != http.StatusMethodNotAllowed {
		t.Fatalf("GET /mcp: %d, want 405", got)
	}

	discoverBody := `{"jsonrpc":"2.0","id":"d1","method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"}}}}`
	discover := mcpPost(h, discoverBody, "mcp-owner-token", mcpCurrentVersion, "server/discover", "")
	if discover.Code != http.StatusOK {
		t.Fatalf("discover: %d %s", discover.Code, discover.Body)
	}
	var response struct {
		ID     string `json:"id"`
		Result struct {
			Versions []string       `json:"supportedVersions"`
			Meta     map[string]any `json:"_meta"`
		} `json:"result"`
	}
	if err := json.Unmarshal(discover.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ID != "d1" || len(response.Result.Versions) == 0 || response.Result.Versions[0] != mcpCurrentVersion {
		t.Fatalf("unexpected discover response: %+v", response)
	}
	if response.Result.Meta[mcpMetaServer] == nil {
		t.Fatalf("discover response is missing server identity: %s", discover.Body)
	}

	listBody := `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`
	list := mcpPost(h, listBody, "mcp-owner-token", mcpCurrentVersion, "tools/list", "")
	if list.Code != http.StatusOK {
		t.Fatalf("tools/list: %d %s", list.Code, list.Body)
	}
	var listed struct {
		Result struct {
			Tools []mcpTool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"list_projects": false, "get_variables": false, "rollback": false, "create_service_from_template": false, "add_domain": false}
	for _, tool := range listed.Result.Tools {
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("tools/list is missing %q", name)
		}
	}
}

func mcpPost(h http.Handler, body, token, version, method, name string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	r.RemoteAddr = "198.18.12.34:4321"
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("MCP-Protocol-Version", version)
	r.Header.Set("Mcp-Method", method)
	if name != "" {
		r.Header.Set("Mcp-Name", name)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestMCPToolCallsReuseRBACAndRedactSealedVariables(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	user := testUser(t, st, "mcp-viewer@example.com", store.RoleViewer)
	project := &store.Project{Name: "mcp-visible"}
	env, err := st.CreateProject(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	other := &store.Project{Name: "mcp-hidden"}
	if _, err := st.CreateProject(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMember(ctx, project.ID, user.ID, store.RoleViewer); err != nil {
		t.Fatal(err)
	}
	service := &store.Service{ProjectID: project.ID, Name: "web", SourceType: "image"}
	if err := st.CreateService(ctx, service); err != nil {
		t.Fatal(err)
	}
	instance := &store.Instance{ServiceID: service.ID, EnvironmentID: env.ID, Variables: map[string]string{"PLAIN": "visible", "SECRET": "never-return"}}
	if err := st.CreateInstance(ctx, instance); err != nil {
		t.Fatal(err)
	}
	if err := st.SealVariables(ctx, instance.ID, instance.Variables, []string{"SECRET"}); err != nil {
		t.Fatal(err)
	}
	token := "keel_mcp_scoped_token"
	pid := project.ID
	apiToken := &store.APIToken{UserID: user.ID, Name: "mcp test", Prefix: token[:12], Scope: store.ScopeProject, ProjectID: &pid, Role: store.RoleViewer}
	if err := st.CreateAPIToken(ctx, apiToken, hashSecret(token)); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st, SessionKey: []byte("0123456789abcdef0123456789abcdef"), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := s.Handler()

	visible := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"inspect_project","arguments":{"project_id":"` + project.ID + `"}}}`
	got := req(h, http.MethodPost, "/mcp", visible, withBearer(token))
	if got.Code != http.StatusOK {
		t.Fatalf("inspect visible project: %d %s", got.Code, got.Body)
	}
	var success struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &success); err != nil || success.Result.IsError {
		t.Fatalf("visible project call failed: %s (%v)", got.Body, err)
	}

	hidden := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"inspect_project","arguments":{"project_id":"` + other.ID + `"}}}`
	got = req(h, http.MethodPost, "/mcp", hidden, withBearer(token))
	if got.Code != http.StatusOK {
		t.Fatalf("inspect hidden project transport: %d %s", got.Code, got.Body)
	}
	var denied struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &denied); err != nil {
		t.Fatal(err)
	}
	if !denied.Result.IsError || len(denied.Result.Content) == 0 || !strings.Contains(denied.Result.Content[0].Text, "404") {
		t.Fatalf("RBAC denial did not stay hidden as 404: %s", got.Body)
	}

	inspect := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"inspect_service","arguments":{"service_id":"` + service.ID + `","environment_id":"` + env.ID + `"}}}`
	got = req(h, http.MethodPost, "/mcp", inspect, withBearer(token))
	if got.Code != http.StatusOK {
		t.Fatalf("inspect service: %d %s", got.Code, got.Body)
	}
	var serviceResult struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &serviceResult); err != nil {
		t.Fatal(err)
	}
	if serviceResult.Result.IsError || len(serviceResult.Result.Content) == 0 {
		t.Fatalf("inspect service failed: %s", got.Body)
	}
	var view map[string]any
	if err := json.Unmarshal([]byte(serviceResult.Result.Content[0].Text), &view); err != nil {
		t.Fatal(err)
	}
	variables, _ := view["variables"].(map[string]any)
	if variables["SECRET"] != SealedValue || variables["PLAIN"] != MaskedValue {
		t.Fatalf("MCP leaked sealed or viewer-only variables: %#v", variables)
	}
	if strings.Contains(serviceResult.Result.Content[0].Text, "never-return") {
		t.Fatal("sealed variable value leaked from MCP")
	}
}

// mcpCall sends a legacy-style (header-less) tools/call and returns the tool result.
func mcpCall(t *testing.T, h http.Handler, auth func(*http.Request), tool string, args map[string]any) (bool, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
	got := req(h, http.MethodPost, "/mcp", string(body), auth)
	if got.Code != http.StatusOK {
		t.Fatalf("%s: HTTP %d %s", tool, got.Code, got.Body)
	}
	var res struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &res); err != nil || len(res.Result.Content) == 0 {
		t.Fatalf("%s: bad response %s (%v)", tool, got.Body, err)
	}
	return res.Result.IsError, res.Result.Content[0].Text
}

func TestMCPSetVariablesMergesAndRespectsRole(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	member := testUser(t, st, "mcp-member@example.com", store.RoleMember)
	viewer := testUser(t, st, "mcp-viewer2@example.com", store.RoleViewer)
	project := &store.Project{Name: "mcp-vars"}
	env, err := st.CreateProject(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMember(ctx, project.ID, member.ID, store.RoleMember); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMember(ctx, project.ID, viewer.ID, store.RoleViewer); err != nil {
		t.Fatal(err)
	}
	service := &store.Service{ProjectID: project.ID, Name: "api", SourceType: "image"}
	if err := st.CreateService(ctx, service); err != nil {
		t.Fatal(err)
	}
	instance := &store.Instance{ServiceID: service.ID, EnvironmentID: env.ID, Variables: map[string]string{"KEEP": "1", "DROP": "x", "SECRET": "s3cret"}}
	if err := st.CreateInstance(ctx, instance); err != nil {
		t.Fatal(err)
	}
	if err := st.SealVariables(ctx, instance.ID, instance.Variables, []string{"SECRET"}); err != nil {
		t.Fatal(err)
	}
	pid := project.ID
	tokens := map[string]*store.User{"keel_mcp_member_tok": member, "keel_mcp_viewer_tok": viewer}
	for tok, u := range tokens {
		role := store.RoleMember
		if u == viewer {
			role = store.RoleViewer
		}
		if err := st.CreateAPIToken(ctx, &store.APIToken{UserID: u.ID, Name: "mcp", Prefix: tok[:12], Scope: store.ScopeProject, ProjectID: &pid, Role: role}, hashSecret(tok)); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{Store: st, Events: &events.Bus{Store: st}, SessionKey: make([]byte, 32),
		Deploy: &deploy.Manager{Store: st, Docker: testDocker(), Cfg: deploy.Config{DataDir: t.TempDir()}},
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := s.Handler()
	args := map[string]any{"service_id": service.ID, "environment_id": env.ID, "variables": map[string]any{"NEW": "2", "DROP": nil}}

	if isErr, text := mcpCall(t, h, withBearer("keel_mcp_viewer_tok"), "set_variables", args); !isErr || !strings.Contains(text, "403") {
		t.Fatalf("viewer set_variables should be forbidden: %v %s", isErr, text)
	}
	isErr, text := mcpCall(t, h, withBearer("keel_mcp_member_tok"), "set_variables", args)
	if isErr {
		t.Fatalf("member set_variables: %s", text)
	}
	if strings.Contains(text, "s3cret") {
		t.Fatal("set_variables response leaked a sealed value")
	}
	fresh, err := st.GetInstance(ctx, instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Variables["KEEP"] != "1" || fresh.Variables["NEW"] != "2" || fresh.Variables["SECRET"] != "s3cret" {
		t.Fatalf("variables were not merged: %#v", fresh.Variables)
	}
	if _, ok := fresh.Variables["DROP"]; ok {
		t.Fatalf("null did not delete DROP: %#v", fresh.Variables)
	}
	if isErr, text := mcpCall(t, h, withBearer("keel_mcp_member_tok"), "whoami", nil); isErr || !strings.Contains(text, "mcp-member@example.com") {
		t.Fatalf("whoami: %v %s", isErr, text)
	}
}

func TestMCPInitializeNegotiationAndSessionCookie(t *testing.T) {
	st := testStore(t)
	owner := testUser(t, st, "mcp-owner@example.com", store.RoleOwner)
	s := &Server{Store: st, SessionKey: make([]byte, 32), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := s.Handler()
	cookie := testSessionCookie(t, s, owner)

	initialize := func(version string, auth func(*http.Request)) (int, string) {
		body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + version + `","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
		w := req(h, http.MethodPost, "/mcp", body, auth)
		var res struct {
			Result struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"result"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		return w.Code, res.Result.ProtocolVersion
	}
	// The `aven mcp` bridge forwards the CLI session cookie without browser headers.
	if code, v := initialize("2025-06-18", withCookie(cookie)); code != 200 || v != "2025-06-18" {
		t.Fatalf("initialize 2025-06-18: %d %q", code, v)
	}
	if code, v := initialize("2024-11-05", withCookie(cookie)); code != 200 || v != mcpLegacyVersion {
		t.Fatalf("initialize unknown version should fall back to %s: %d %q", mcpLegacyVersion, code, v)
	}
	evil := func(r *http.Request) {
		withCookie(cookie)(r)
		r.Header.Set("Origin", "https://evil.example")
	}
	if code, _ := initialize("2025-06-18", evil); code != http.StatusForbidden {
		t.Fatalf("cross-origin cookie MCP request: %d, want 403", code)
	}
	if isErr, text := mcpCall(t, h, withCookie(cookie), "list_projects", nil); isErr {
		t.Fatalf("list_projects via session: %s", text)
	}
}

func TestMCPToolSchemaAndArgumentValidation(t *testing.T) {
	st := testStore(t)
	owner := testUser(t, st, "mcp-validate@example.com", store.RoleOwner)
	s := &Server{Store: st, SessionKey: make([]byte, 32), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := s.Handler()
	auth := withCookie(testSessionCookie(t, s, owner))

	// 모든 도구는 제목, 설명, 닫힌 입력 스키마, 힌트를 갖는다. 상태를 바꾸는 도구는 readOnlyHint=false다.
	destructive := map[string]bool{"set_variables": true, "deploy": true, "redeploy": true, "restart_service": true, "stop_service": true, "rollback": true, "cancel_deployment": true}
	for _, tool := range mcpTools {
		if tool.Title == "" || tool.Description == "" || tool.InputSchema["additionalProperties"] != false || tool.Annotations["title"] != tool.Title {
			t.Errorf("%s: incomplete metadata %+v", tool.Name, tool)
		}
		if got := tool.Annotations["destructiveHint"] == true; got != destructive[tool.Name] {
			t.Errorf("%s: destructiveHint=%v", tool.Name, got)
		}
		props := tool.InputSchema["properties"].(map[string]any)
		req, _ := tool.InputSchema["required"].([]string)
		for _, k := range req {
			if _, ok := props[k]; !ok {
				t.Errorf("%s: required %q is not a property", tool.Name, k)
			}
		}
	}

	// 없는 도구는 JSON-RPC 오류다.
	body := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"drop_database","arguments":{}}}`
	w := req(h, http.MethodPost, "/mcp", body, auth)
	var rpc struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rpc); err != nil || rpc.Error == nil || rpc.Error.Code != -32602 || !strings.Contains(rpc.Error.Message, "drop_database") {
		t.Fatalf("unknown tool: %d %s", w.Code, w.Body)
	}

	for _, tc := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"inspect_project", map[string]any{"projectId": "prj_1"}, `unknown argument "projectId" for inspect_project; allowed: project_id`},
		{"whoami", map[string]any{"x": 1}, "whoami takes no arguments"},
		{"inspect_project", map[string]any{}, "project_id is required"},
		{"inspect_project", map[string]any{"project_id": nil}, "project_id is required"},
		{"inspect_project", map[string]any{"project_id": 5}, "project_id must be a string"},
		{"get_logs", map[string]any{"deployment_id": "dep_1", "type": "http"}, "type must be one of: runtime, build"},
		{"get_logs", map[string]any{"deployment_id": "dep_1", "tail": 5000}, "tail must be between 1 and 1000"},
		{"get_logs", map[string]any{"deployment_id": "dep_1", "tail": 1.5}, "tail must be an integer"},
		{"set_variables", map[string]any{"service_id": "s", "environment_id": "e", "variables": []string{"A"}}, "variables must be an object"},
		{"set_variables", map[string]any{"service_id": "s", "environment_id": "e", "variables": map[string]any{"A": "1"}, "deploy": "yes"}, "deploy must be true or false"},
		{"add_domain", map[string]any{"service_id": "s", "environment_id": "e", "target_port": 70000}, "target_port must be between 1 and 65535"},
	} {
		isErr, text := mcpCall(t, h, auth, tc.tool, tc.args)
		if !isErr || !strings.Contains(text, tc.want) {
			t.Errorf("%s %v: %v %q, want %q", tc.tool, tc.args, isErr, text, tc.want)
		}
	}

	// 오류에는 다음에 할 일을 알려 주는 힌트가 붙는다.
	isErr, text := mcpCall(t, h, auth, "inspect_project", map[string]any{"project_id": "prj_missing"})
	if !isErr || !strings.Contains(text, "404") || !strings.Contains(text, "list_projects") {
		t.Errorf("not found hint: %q", text)
	}
	// 선택 인자의 null은 생략과 같다.
	if isErr, text := mcpCall(t, h, auth, "list_deployments", map[string]any{"service_id": "svc_x", "environment_id": "env_x", "limit": nil}); !isErr || !strings.Contains(text, "404") {
		t.Errorf("null limit: %q", text)
	}
}

// get_logs는 tail을 자르기 전에 필터를 적용한다(예전에는 마지막 tail줄에서만 찾아 앞쪽 오류를 놓쳤다).
func TestMCPGetLogsFiltersBeforeTail(t *testing.T) {
	ctx := context.Background()
	f := newGQLFixture(t)
	in, _ := f.st.GetInstanceFor(ctx, f.svc.ID, f.env.ID)
	d := &store.Deployment{ServiceID: f.svc.ID, EnvironmentID: f.env.ID, InstanceID: in.ID, Trigger: "manual"}
	if err := f.st.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	writeCronLogs(t, f, d, "panic: boom", "ok 1", "ok 2", "ok 3")
	isErr, text := mcpCall(t, f.h, f.owner, "get_logs", map[string]any{"deployment_id": d.ID, "tail": 2, "filter": "panic"})
	if isErr || !strings.Contains(text, "panic: boom") || strings.Contains(text, "ok 3") {
		t.Fatalf("filtered logs: %v %s", isErr, text)
	}
	isErr, text = mcpCall(t, f.h, f.owner, "get_logs", map[string]any{"deployment_id": d.ID, "tail": 2})
	if isErr || strings.Contains(text, "panic") || !strings.Contains(text, "ok 3") {
		t.Fatalf("tail: %v %s", isErr, text)
	}
}
