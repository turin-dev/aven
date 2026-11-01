package agentchat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompleteStreamsTextAndToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"Checking "}}]}`)
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"list_","arguments":"{\"project_id\":"}}]}}]}`)
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"projects","arguments":"\"prj_1\"}"}}]}}]}`)
		fmt.Fprintln(w, `data: [DONE]`)
	}))
	defer server.Close()

	var streamed strings.Builder
	got, err := (&Client{BaseURL: server.URL + "/v1", APIKey: "test-key", Model: "test-model"}).Complete(
		context.Background(), []Message{{Role: "user", Content: "check"}}, nil, func(s string) { streamed.WriteString(s) })
	if err != nil {
		t.Fatal(err)
	}
	if streamed.String() != "Checking " || got.Content != "Checking " {
		t.Fatalf("streamed=%q content=%q", streamed.String(), got.Content)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].ID != "call_1" || got.ToolCalls[0].Function.Name != "list_projects" || got.ToolCalls[0].Function.Arguments != `{"project_id":"prj_1"}` {
		t.Fatalf("tool call = %#v", got.ToolCalls)
	}
}

func TestCompleteReportsProviderErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, "slow down")
	}))
	defer server.Close()
	_, err := (&Client{BaseURL: server.URL, Model: "test"}).Complete(context.Background(), nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "slow down") {
		t.Fatalf("expected provider error, got %v", err)
	}
}

func TestCompleteAcceptsCompactDataAndFinishReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": keepalive\n\n")
		fmt.Fprint(w, "data:{\"choices\":[{\"delta\":{\"content\":\"안녕\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	}))
	defer server.Close()
	got, err := (&Client{BaseURL: server.URL + "/v1/chat/completions", Model: "m"}).Complete(context.Background(), nil, nil, nil)
	if err != nil || got.Content != "안녕" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestCompleteRejectsTruncatedStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"half\"}}]}\n\n")
	}))
	defer server.Close()
	if _, err := (&Client{BaseURL: server.URL, Model: "m"}).Complete(context.Background(), nil, nil, nil); err == nil {
		t.Fatal("expected an error for a stream without [DONE] or finish_reason")
	}
}

func streamServer(t *testing.T, lines ...string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range lines {
			fmt.Fprint(w, "data: "+l+"\n\n")
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestCompleteAssemblesToolCallsRobustly(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []string
		want  string // name(args) joined by ;
	}{
		{"index starts at 1", []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c1","type":"function","function":{"name":"whoami","arguments":"{}"}}]}}]}`,
			`[DONE]`,
		}, "whoami({})"},
		{"no index, one id per call", []string{
			`{"choices":[{"delta":{"tool_calls":[{"id":"a","function":{"name":"list_projects","arguments":"{"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"id":"b","function":{"name":"whoami","arguments":"{}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		}, "list_projects({});whoami({})"},
		{"index 0 reused with a new id", []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"list_projects","arguments":"{}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"b","function":{"name":"whoami","arguments":"{}"}}]}}]}`,
			`[DONE]`,
		}, "list_projects({});whoami({})"},
		{"two indexes interleaved", []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"get_","arguments":""}},{"index":1,"id":"b","function":{"name":"who","arguments":"{}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"name":"ami"}},{"index":0,"function":{"name":"logs","arguments":"{}"}}]}}]}`,
			`[DONE]`,
		}, "get_logs({});whoami({})"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := streamServer(t, tc.lines...)
			got, err := (&Client{BaseURL: server.URL, Model: "m"}).Complete(context.Background(), nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			parts := []string{}
			for _, c := range got.ToolCalls {
				parts = append(parts, c.Function.Name+"("+c.Function.Arguments+")")
			}
			if strings.Join(parts, ";") != tc.want {
				t.Fatalf("tool calls = %v, want %s", parts, tc.want)
			}
		})
	}
}

func TestCompleteProviderErrorFormats(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   []string
	}{
		{401, `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error"}}`, []string{"401", "Incorrect API key provided", "check the agent API key"}},
		{404, `{"error":"model not found"}`, []string{"model not found", "endpoint URL and model name"}},
		{500, `{"detail":"upstream exploded"}`, []string{"upstream exploded"}},
		{502, ``, []string{"502 Bad Gateway"}},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			fmt.Fprint(w, tc.body)
		}))
		_, err := (&Client{BaseURL: server.URL, Model: "m"}).Complete(context.Background(), nil, nil, nil)
		server.Close()
		for _, w := range tc.want {
			if err == nil || !strings.Contains(err.Error(), w) {
				t.Errorf("%d %s: %v, want %q", tc.status, tc.body, err, w)
			}
		}
	}
	// 스트림 중간의 오류: 객체와 문자열 모두
	for _, chunk := range []string{`{"error":{"message":"context length exceeded"}}`, `{"error":"context length exceeded"}`} {
		server := streamServer(t, chunk)
		if _, err := (&Client{BaseURL: server.URL, Model: "m"}).Complete(context.Background(), nil, nil, nil); err == nil || !strings.Contains(err.Error(), "context length exceeded") {
			t.Errorf("stream error %s: %v", chunk, err)
		}
	}
}

func TestCompleteAcceptsNonStreamingJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"whoami","arguments":"{}"}}]}}]}`)
	}))
	defer server.Close()
	got, err := (&Client{BaseURL: server.URL, Model: "m"}).Complete(context.Background(), nil, nil, nil)
	if err != nil || len(got.ToolCalls) != 1 || got.ToolCalls[0].Function.Name != "whoami" || got.Content != "" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestCompleteBoundsResponseSize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		line := "data: {\"choices\":[{\"delta\":{\"content\":\"" + strings.Repeat("x", 64<<10) + "\"}}]}\n\n"
		for i := 0; i < (MaxResponseBytes/len(line))+10; i++ {
			if _, err := fmt.Fprint(w, line); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	if _, err := (&Client{BaseURL: server.URL, Model: "m"}).Complete(context.Background(), nil, nil, nil); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("oversized stream: %v", err)
	}
}
