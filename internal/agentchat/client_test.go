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
