package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/turin-dev/aven/internal/agentchat"
	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/secret"
	"github.com/turin-dev/aven/internal/store"
)

// fakeLLM은 OpenAI 호환 스트리밍 endpoint다. 요청마다 replies에서 순서대로 하나를 답한다.
type fakeLLM struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	replies  []map[string]any // 각 응답의 delta (content 또는 tool_calls)
	requests [][]agentchat.Message
}

func newFakeLLM(t *testing.T, replies ...map[string]any) *fakeLLM {
	f := &fakeLLM{t: t, replies: replies}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("model path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer provider-secret" {
			t.Errorf("model authorization = %q", got)
		}
		var body struct {
			Messages []agentchat.Message `json:"messages"`
			Tools    []agentchat.Tool    `json:"tools"`
			Stream   bool                `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode model request: %v", err)
		}
		if !body.Stream {
			t.Error("model request did not ask for streaming")
		}
		f.mu.Lock()
		n := len(f.requests)
		f.requests = append(f.requests, body.Messages)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n >= len(f.replies) {
			t.Errorf("unexpected model request #%d", n+1)
			writeAgentChunk(t, w, map[string]any{"content": "?"})
			return
		}
		writeAgentChunk(t, w, f.replies[n])
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLLM) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeLLM) request(i int) []agentchat.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[i]
}

func toolCallDelta(id, name, args string) map[string]any {
	return map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": id, "type": "function",
		"function": map[string]string{"name": name, "arguments": args}}}}
}

func writeAgentChunk(t *testing.T, w http.ResponseWriter, delta map[string]any) {
	t.Helper()
	// 내용은 두 조각으로 나눠 보내 스트리밍을 흉내 낸다.
	if c, ok := delta["content"].(string); ok && len(c) > 1 {
		half := len([]rune(c)) / 2
		for _, part := range []string{string([]rune(c)[:half]), string([]rune(c)[half:])} {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": part}}}})
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
	} else {
		chunk, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta}}})
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(w, "data: %s\n\n", chunk)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

type agentFixture struct {
	st      *store.Store
	h       http.Handler
	s       *Server
	project *store.Project
	env     *store.Environment
	svc     *store.Service
	admin   string // owner 세션 쿠키
}

func newAgentFixture(t *testing.T, llm *fakeLLM) *agentFixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, err := secret.LoadOrCreate(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSecretBox(box)
	ctx := context.Background()
	project := &store.Project{Name: "agent-test"}
	env, err := st.CreateProject(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	svc := &store.Service{ProjectID: project.ID, Name: "web", SourceType: "repo"}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateInstance(ctx, &store.Instance{ServiceID: svc.ID, EnvironmentID: env.ID, Variables: map[string]string{"EXISTING": "keep", "OLD": "x"}}); err != nil {
		t.Fatal(err)
	}
	if llm != nil {
		if err := st.SetAgentModelSettings(ctx, store.AgentModelSettings{Endpoint: llm.srv.URL + "/v1", Model: "fake-model", APIKey: "provider-secret"}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{Store: st, Deploy: &deploy.Manager{Store: st}, AdminPassword: "password123", SessionKey: make([]byte, 32),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	f := &agentFixture{st: st, s: s, h: s.Handler(), project: project, env: env, svc: svc}
	f.admin = testSessionCookie(t, s, &store.User{ID: store.BuiltinUserID, Email: store.BuiltinUserEmail, Name: "admin", Role: store.RoleOwner, Builtin: true})
	return f
}

type sseEvent struct {
	Name string
	Data map[string]any
}

func parseSSE(t *testing.T, body string) []sseEvent {
	t.Helper()
	var out []sseEvent
	name := ""
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			var data map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data); err != nil {
				t.Fatalf("bad SSE data %q: %v", line, err)
			}
			out = append(out, sseEvent{name, data})
		}
	}
	return out
}

func findEvent(events []sseEvent, name string) *sseEvent {
	for i := range events {
		if events[i].Name == name {
			return &events[i]
		}
	}
	return nil
}

func (f *agentFixture) chat(t *testing.T, cookie string, in agentChatRequest) (*httptest.ResponseRecorder, []sseEvent) {
	t.Helper()
	b, _ := json.Marshal(in)
	w := req(f.h, http.MethodPost, "/api/ai/chat", string(b), withCookie(cookie))
	if w.Code != 200 {
		return w, nil
	}
	return w, parseSSE(t, w.Body.String())
}

func (f *agentFixture) confirm(t *testing.T, cookie, thread, call string, approve bool) (*httptest.ResponseRecorder, []sseEvent) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"thread_id": thread, "call_id": call, "approve": approve})
	w := req(f.h, http.MethodPost, "/api/ai/confirm", string(b), withCookie(cookie))
	if w.Code != 200 {
		return w, nil
	}
	return w, parseSSE(t, w.Body.String())
}

func TestAgentActionWaitsForConfirmationAndStreamsReply(t *testing.T) {
	var f *agentFixture
	llm := newFakeLLM(t) // 응답은 fixture를 만든 뒤 채운다(ID가 필요하다)
	f = newAgentFixture(t, llm)
	llm.replies = []map[string]any{
		toolCallDelta("call_set_1", "set_variables", fmt.Sprintf(`{"service_id":%q,"environment_id":%q,"set":{"FEATURE_FLAG":"enabled"},"unset":["OLD"]}`, f.svc.ID, f.env.ID)),
		{"content": "변수를 업데이트했습니다."},
	}
	w, events := f.chat(t, f.admin, agentChatRequest{Prompt: "FEATURE_FLAG를 enabled로 설정해 줘", ProjectID: f.project.ID, EnvironmentID: f.env.ID, ServiceID: f.svc.ID})
	conf := findEvent(events, "confirmation_required")
	if w.Code != 200 || conf == nil {
		t.Fatalf("chat status=%d body=%s", w.Code, w.Body.String())
	}
	if summary, _ := conf.Data["summary"].(string); !strings.Contains(summary, "web (production)") || !strings.Contains(summary, "FEATURE_FLAG") || !strings.Contains(summary, "OLD") {
		t.Fatalf("confirmation summary = %q", summary)
	}
	// 시스템 프롬프트에 지금 보고 있는 서비스가 들어간다.
	if sys := llm.request(0)[0]; sys.Role != "system" || !strings.Contains(sys.Content, "Current service: web ("+f.svc.ID+")") {
		t.Fatalf("system prompt = %+v", sys)
	}
	unchanged, err := f.st.GetInstanceFor(context.Background(), f.svc.ID, f.env.ID)
	if err != nil || unchanged.Variables["FEATURE_FLAG"] != "" {
		t.Fatalf("action ran before confirmation: %+v, %v", unchanged.Variables, err)
	}
	threadID, _ := conf.Data["thread_id"].(string)
	callID, _ := conf.Data["call_id"].(string)
	if threadID == "" || callID != "call_set_1" {
		t.Fatalf("confirmation event: %+v", conf.Data)
	}
	// 대기 중인 작업이 있으면 새 메시지를 받지 않는다.
	if w, _ := f.chat(t, f.admin, agentChatRequest{ThreadID: threadID, Prompt: "또"}); w.Code != 409 {
		t.Fatalf("chat with pending action = %d", w.Code)
	}
	// 대화 기록에 대기 중인 작업이 보인다(새로고침 뒤에도 확인할 수 있게).
	view := decodeBody[agentThreadView](t, req(f.h, http.MethodGet, "/api/ai/threads/"+threadID, "", withCookie(f.admin)))
	if view.PendingAction == nil || view.PendingAction.CallID != "call_set_1" || view.PendingAction.Summary == "" {
		t.Fatalf("thread pending action = %+v", view.PendingAction)
	}

	w, events = f.confirm(t, f.admin, threadID, callID, true)
	if w.Code != 200 || findEvent(events, "done") == nil || !strings.Contains(w.Body.String(), "변수를 업데이트했습니다.") {
		t.Fatalf("confirm status=%d body=%s", w.Code, w.Body.String())
	}
	if tool := findEvent(events, "tool"); tool == nil || tool.Data["name"] != "set_variables" || tool.Data["is_error"] != false {
		t.Fatalf("tool event = %+v", tool)
	}
	updated, err := f.st.GetInstanceFor(context.Background(), f.svc.ID, f.env.ID)
	if err != nil || updated.Variables["FEATURE_FLAG"] != "enabled" || updated.Variables["EXISTING"] != "keep" || updated.Variables["OLD"] != "" {
		t.Fatalf("confirmed variables=%v, err=%v", updated.Variables, err)
	}
	if llm.count() != 2 {
		t.Fatalf("fake model requests = %d", llm.count())
	}
	foundResult := false
	for _, m := range llm.request(1) {
		if m.Role == "tool" && m.ToolCallID == "call_set_1" && strings.Contains(m.Content, `"status":"updated"`) {
			foundResult = true
		}
	}
	if !foundResult {
		t.Fatal("follow-up request did not contain the confirmed tool result")
	}
	// 같은 확인을 다시 보내도 두 번 실행되지 않는다.
	if w, _ := f.confirm(t, f.admin, threadID, callID, true); w.Code != 404 {
		t.Fatalf("second confirm = %d", w.Code)
	}
	settings := req(f.h, http.MethodGet, "/api/ai/settings", "", withCookie(f.admin))
	if settings.Code != 200 || strings.Contains(settings.Body.String(), "provider-secret") || !strings.Contains(settings.Body.String(), `"key_set":true`) {
		t.Fatalf("settings response exposed or omitted key state: %d %s", settings.Code, settings.Body.String())
	}
	view = decodeBody[agentThreadView](t, req(f.h, http.MethodGet, "/api/ai/threads/"+threadID, "", withCookie(f.admin)))
	if view.PendingAction != nil || len(view.Messages) != 3 || view.Messages[0].Role != "user" || view.Messages[1].Tool != "set_variables" || view.Messages[2].Content != "변수를 업데이트했습니다." {
		t.Fatalf("thread view = %+v", view)
	}
}

func TestAgentDeclineDoesNotRunAction(t *testing.T) {
	llm := newFakeLLM(t)
	f := newAgentFixture(t, llm)
	llm.replies = []map[string]any{
		toolCallDelta("call_r", "restart_service", fmt.Sprintf(`{"service_id":%q,"environment_id":%q}`, f.svc.ID, f.env.ID)),
		{"content": "재시작하지 않았습니다."},
	}
	_, events := f.chat(t, f.admin, agentChatRequest{Prompt: "재시작해"})
	conf := findEvent(events, "confirmation_required")
	if conf == nil {
		t.Fatalf("events = %+v", events)
	}
	w, events := f.confirm(t, f.admin, conf.Data["thread_id"].(string), "call_r", false)
	if w.Code != 200 || findEvent(events, "done") == nil {
		t.Fatalf("decline = %d %s", w.Code, w.Body.String())
	}
	if tool := findEvent(events, "tool"); tool == nil || tool.Data["declined"] != true {
		t.Fatalf("tool event = %+v", tool)
	}
	last := llm.request(1)
	if m := last[len(last)-1]; m.Role != "tool" || !strings.Contains(m.Content, "declined") {
		t.Fatalf("model did not learn about the decline: %+v", m)
	}
}

func TestAgentToolsRespectRBAC(t *testing.T) {
	llm := newFakeLLM(t)
	f := newAgentFixture(t, llm)
	ctx := context.Background()
	other := &store.Project{Name: "secret-project"}
	if _, err := f.st.CreateProject(ctx, other); err != nil {
		t.Fatal(err)
	}
	viewer := testUser(t, f.st, "viewer@example.com", store.RoleMember)
	if err := f.st.SetMember(ctx, f.project.ID, viewer.ID, store.RoleViewer); err != nil {
		t.Fatal(err)
	}
	cookie := testSessionCookie(t, f.s, viewer)
	llm.replies = []map[string]any{
		toolCallDelta("call_list", "list_projects", `{}`),
		toolCallDelta("call_other", "list_services", fmt.Sprintf(`{"project_id":%q}`, other.ID)),
		toolCallDelta("call_restart", "restart_service", fmt.Sprintf(`{"service_id":%q,"environment_id":%q}`, f.svc.ID, f.env.ID)),
		{"content": "권한이 없습니다."},
	}
	w, events := f.chat(t, cookie, agentChatRequest{Prompt: "다 보여주고 재시작해"})
	if w.Code != 200 || findEvent(events, "done") == nil {
		t.Fatalf("chat = %d %s", w.Code, w.Body.String())
	}
	// viewer에게는 확인을 묻지 않는다(권한이 없으니 모델에 바로 오류를 돌려준다).
	if findEvent(events, "confirmation_required") != nil {
		t.Fatal("viewer was offered a confirmation for an action they cannot run")
	}
	results := map[string]string{}
	for _, m := range llm.request(3) {
		if m.Role == "tool" {
			results[m.ToolCallID] = m.Content
		}
	}
	if !strings.Contains(results["call_list"], f.project.ID) || strings.Contains(results["call_list"], other.ID) || !strings.Contains(results["call_list"], f.env.ID) {
		t.Fatalf("list_projects = %s", results["call_list"])
	}
	if !strings.Contains(results["call_other"], "error") || strings.Contains(results["call_other"], "secret-project") {
		t.Fatalf("list_services on another project = %s", results["call_other"])
	}
	if !strings.Contains(results["call_restart"], "permission denied") {
		t.Fatalf("restart as viewer = %s", results["call_restart"])
	}
	// 다른 사용자의 대화는 보이지 않는다.
	threadID := findEvent(events, "thread").Data["thread_id"].(string)
	if w := req(f.h, http.MethodGet, "/api/ai/threads/"+threadID, "", withCookie(f.admin)); w.Code != 404 {
		t.Fatalf("owner read viewer's thread: %d", w.Code)
	}
	list := decodeBody[[]agentThreadItem](t, req(f.h, http.MethodGet, "/api/ai/threads", "", withCookie(cookie)))
	if len(list) != 1 || list[0].ID != threadID {
		t.Fatalf("viewer threads = %+v", list)
	}
	if w := req(f.h, http.MethodDelete, "/api/ai/threads/"+threadID, "", withCookie(cookie)); w.Code != 204 {
		t.Fatalf("delete = %d", w.Code)
	}
	// 볼 수 없는 프로젝트를 문맥으로 넘기면 404다.
	if w, _ := f.chat(t, cookie, agentChatRequest{Prompt: "hi", ProjectID: other.ID}); w.Code != 404 {
		t.Fatalf("chat with hidden project = %d", w.Code)
	}
	// 모델 설정은 작업 공간 admin만 바꾼다. 일반 사용자는 endpoint를 보지 못한다.
	if w := req(f.h, http.MethodPut, "/api/ai/settings", `{"endpoint":"https://x.example/v1","model":"m"}`, withCookie(cookie)); w.Code != 403 {
		t.Fatalf("viewer put settings = %d", w.Code)
	}
	if got := decodeBody[agentSettingsView](t, req(f.h, http.MethodGet, "/api/ai/settings", "", withCookie(cookie))); got.Endpoint != "" || !got.Configured || got.Model != "fake-model" {
		t.Fatalf("viewer settings = %+v", got)
	}
}

func TestAgentSettingsConnectionTest(t *testing.T) {
	llm := newFakeLLM(t, map[string]any{"content": "ok"})
	f := newAgentFixture(t, llm)
	w := req(f.h, http.MethodPost, "/api/ai/settings/test", "", withCookie(f.admin))
	if got := decodeBody[map[string]any](t, w); w.Code != 200 || got["ok"] != true || got["reply"] != "ok" {
		t.Fatalf("test = %d %v", w.Code, got)
	}
	got := decodeBody[agentSettingsView](t, req(f.h, http.MethodGet, "/api/ai/settings", "", withCookie(f.admin)))
	if got.Endpoint == "" || got.KeyHint != "cret" || !got.KeySet {
		t.Fatalf("admin settings = %+v", got)
	}
}

func TestAgentSettingsValidationAndUnconfigured(t *testing.T) {
	f := newAgentFixture(t, nil)
	if w, _ := f.chat(t, f.admin, agentChatRequest{Prompt: "hi"}); w.Code != 503 {
		t.Fatalf("chat without model = %d", w.Code)
	}
	for _, body := range []string{
		`{"endpoint":"ftp://x/v1","model":"m"}`,
		`{"endpoint":"https://user:pw@x/v1","model":"m"}`,
		`{"endpoint":"https://x/v1?key=1","model":"m"}`,
		`{"endpoint":"https://x/v1","model":""}`,
	} {
		if w := req(f.h, http.MethodPut, "/api/ai/settings", body, withCookie(f.admin)); w.Code != 400 {
			t.Errorf("PUT %s = %d", body, w.Code)
		}
	}
	w := req(f.h, http.MethodPut, "/api/ai/settings", `{"endpoint":"https://models.example/v1/","model":"m","api_key":"sk-1"}`, withCookie(f.admin))
	if got := decodeBody[agentSettingsView](t, w); w.Code != 200 || got.Endpoint != "https://models.example/v1" || !got.KeySet {
		t.Fatalf("PUT = %d %+v", w.Code, got)
	}
	// 키를 보내지 않으면 저장된 키를 유지한다.
	w = req(f.h, http.MethodPut, "/api/ai/settings", `{"endpoint":"https://models.example/v1","model":"m2"}`, withCookie(f.admin))
	if got := decodeBody[agentSettingsView](t, w); !got.KeySet || got.Model != "m2" {
		t.Fatalf("PUT keep key = %+v", got)
	}
	cfg, _ := f.st.AgentModelSettings(context.Background())
	if cfg.APIKey != "sk-1" {
		t.Fatalf("stored key = %q", cfg.APIKey)
	}
	w = req(f.h, http.MethodPut, "/api/ai/settings", `{"endpoint":"https://models.example/v1","model":"m2","clear_api_key":true}`, withCookie(f.admin))
	if got := decodeBody[agentSettingsView](t, w); got.KeySet {
		t.Fatalf("PUT clear key = %+v", got)
	}
}

func TestTrimAgentMessagesKeepsToolPairs(t *testing.T) {
	msgs := []agentchat.Message{{Role: "system", Content: "sys"}}
	for i := 0; i < 30; i++ {
		msgs = append(msgs,
			agentchat.Message{Role: "user", Content: fmt.Sprint(i)},
			agentchat.Message{Role: "assistant", ToolCalls: []agentchat.ToolCall{{ID: fmt.Sprint("c", i), Type: "function"}}},
			agentchat.Message{Role: "tool", ToolCallID: fmt.Sprint("c", i), Content: "{}"},
			agentchat.Message{Role: "assistant", Content: "ok"},
		)
	}
	got := trimAgentMessages(msgs)
	if len(got) > agentMaxMessages || got[0].Role != "system" || got[1].Role != "user" {
		t.Fatalf("trimmed len=%d first=%+v second=%+v", len(got), got[0], got[1])
	}
	if last := got[len(got)-1]; last.Content != "ok" {
		t.Fatalf("last message dropped: %+v", last)
	}
	if short := trimAgentMessages(msgs[:5]); len(short) != 5 {
		t.Fatalf("short history trimmed: %d", len(short))
	}
}
