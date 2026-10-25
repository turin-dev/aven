package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseAgentFlags(t *testing.T) {
	o, err := parseAgentFlags([]string{"-p", "show logs", "--json", "-s", "web", "-e", "staging"})
	if err != nil || o.prompt != "show logs" || !o.json || o.svc != "web" || o.env != "staging" {
		t.Fatalf("%+v %v", o, err)
	}
	o, err = parseAgentFlags([]string{"-y", "what", "is", "failing?"})
	if err != nil || o.prompt != "what is failing?" || !o.yes {
		t.Fatalf("positional prompt: %+v %v", o, err)
	}
	if o, err := parseAgentFlags([]string{"--thread-id", "agt_1"}); err != nil || o.threadID != "agt_1" || o.prompt != "" {
		t.Fatalf("thread: %+v %v", o, err)
	}
	if o, err := parseAgentFlags([]string{"--list", "--json"}); err != nil || !o.list {
		t.Fatalf("list: %+v %v", o, err)
	}
	for _, bad := range [][]string{{"--list", "-p", "x"}, {"--json"}, {"-p", "x", "extra"}, {"--nope"}} {
		if _, err := parseAgentFlags(bad); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

// fake aven-server: 첫 chat은 확인을 요청하고, confirm은 작업 결과와 답을 스트리밍한다.
func TestAgentSessionStreamsAndConfirms(t *testing.T) {
	var confirmed map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(ev string, v any) {
			b, _ := json.Marshal(v)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev, b)
		}
		switch r.URL.Path {
		case "/api/ai/chat":
			if body["prompt"] != "restart web" || body["service_id"] != "svc_1" {
				t.Errorf("chat body = %v", body)
			}
			send("thread", map[string]string{"thread_id": "agt_1"})
			send("tool", map[string]any{"name": "list_services", "arguments": map[string]any{}, "is_error": false})
			send("token", map[string]string{"text": "재시작할게요."})
			send("confirmation_required", map[string]any{"thread_id": "agt_1", "call_id": "call_1", "name": "restart_service", "summary": "web (production) 재시작"})
		case "/api/ai/confirm":
			confirmed = body
			send("thread", map[string]string{"thread_id": "agt_1"})
			send("tool", map[string]any{"name": "restart_service", "result": map[string]string{"status": "restarted"}, "is_error": false})
			send("token", map[string]string{"text": "재시작"})
			send("token", map[string]string{"text": "했습니다."})
			send("done", map[string]string{"thread_id": "agt_1", "response": "재시작했습니다."})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	t.Setenv("KEEL_TOKEN", "tok")
	var out strings.Builder
	s := &agentSession{c: &config{URL: srv.URL}, ctx: map[string]string{"service_id": "svc_1"}, yes: true, in: bufio.NewReader(strings.NewReader("")), out: &out}
	resp, err := s.send("restart web")
	if err != nil || resp != "재시작했습니다." || s.threadID != "agt_1" {
		t.Fatalf("resp=%q thread=%q err=%v", resp, s.threadID, err)
	}
	if confirmed["approve"] != true || confirmed["call_id"] != "call_1" || confirmed["thread_id"] != "agt_1" {
		t.Fatalf("confirm body = %v", confirmed)
	}
	if len(s.tools) != 2 || s.tools[1].ToolName != "restart_service" || s.tools[1].Result == nil {
		t.Fatalf("tools = %+v", s.tools)
	}
	for _, want := range []string{"재시작할게요.", "확인 필요: web (production) 재시작", "↳ restart_service", "재시작했습니다."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}
