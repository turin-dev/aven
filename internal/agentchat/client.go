// Package agentchat implements the OpenAI-compatible streaming chat client used by Aven Agent.
package agentchat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Function struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type Client struct {
	BaseURL string
	APIKey  string
	Model   string
	HTTP    *http.Client
}

// Complete streams one chat completion and assembles any function calls.
func (c *Client) Complete(ctx context.Context, messages []Message, tools []Tool, delta func(string)) (Message, error) {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" || c.Model == "" {
		return Message{}, errors.New("agent model endpoint and model are required")
	}
	if !strings.HasSuffix(base, "/chat/completions") {
		base += "/chat/completions"
	}
	body := struct {
		Model             string    `json:"model"`
		Messages          []Message `json:"messages"`
		Tools             []Tool    `json:"tools,omitempty"`
		Stream            bool      `json:"stream"`
		ParallelToolCalls bool      `json:"parallel_tool_calls"`
	}{c.Model, messages, tools, true, false}
	b, err := json.Marshal(body)
	if err != nil {
		return Message{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base, bytes.NewReader(b))
	if err != nil {
		return Message{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 3 * time.Minute}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return Message{}, fmt.Errorf("agent model request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		return Message{}, providerError(resp.Status, resp.StatusCode, msg)
	}
	reply := io.LimitReader(resp.Body, MaxResponseBytes+1)
	// Some OpenAI-compatible servers ignore stream:true and answer with one JSON completion.
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(strings.ToLower(strings.TrimSpace(ct)), "application/json") {
		return readCompletion(reply)
	}
	return readStream(reply, delta)
}

// MaxResponseBytes bounds one model response (stream or JSON). A runaway or hostile endpoint cannot make the server
// buffer an unbounded reply.
const MaxResponseBytes = 8 << 20

type toolCallDelta struct {
	Index    *int   `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// toolCalls assembles streamed tool call fragments. Calls are kept in order of first appearance. A fragment belongs
// to the call with the same index; providers that omit index (or reuse index 0) start a new call whenever a new id
// appears, so two calls are never merged into one name like "list_projectsget_logs".
type toolCalls struct {
	order   []*ToolCall
	byIndex map[int]*ToolCall
	last    *ToolCall
}

func (t *toolCalls) add(d toolCallDelta) {
	var call *ToolCall
	switch {
	case d.Index != nil:
		call = t.byIndex[*d.Index]
		if call != nil && d.ID != "" && call.ID != "" && call.ID != d.ID {
			call = nil // the same index reused for a different call
		}
	case d.ID == "" && t.last != nil:
		call = t.last // continuation fragment without index or id
	}
	if call == nil && d.ID != "" {
		for _, c := range t.order {
			if c.ID == d.ID {
				call = c
			}
		}
	}
	if call == nil {
		call = &ToolCall{Type: "function"}
		t.order = append(t.order, call)
		if d.Index != nil {
			if t.byIndex == nil {
				t.byIndex = map[int]*ToolCall{}
			}
			t.byIndex[*d.Index] = call
		}
	}
	t.last = call
	if d.ID != "" {
		call.ID = d.ID
	}
	if d.Type != "" {
		call.Type = d.Type
	}
	call.Function.Name += d.Function.Name
	call.Function.Arguments += d.Function.Arguments
}

func (t *toolCalls) result() ([]ToolCall, error) {
	out := make([]ToolCall, 0, len(t.order))
	for _, c := range t.order {
		if c.ID == "" || c.Function.Name == "" {
			return nil, errors.New("agent model returned an incomplete tool call")
		}
		out = append(out, *c)
	}
	return out, nil
}

// streamError reads the error member of a chunk: {"error": {"message": ...}} or {"error": "..."}.
func streamError(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var obj struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Message != "" {
		return obj.Message
	}
	return strings.TrimSpace(string(raw))
}

// providerError formats a non-2xx response. OpenAI-style JSON errors are reduced to their message, and common
// statuses get a hint about what to fix in the agent settings.
func providerError(status string, code int, body []byte) error {
	msg := strings.TrimSpace(string(body))
	var wrapped struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Detail  string          `json:"detail"`
	}
	if json.Unmarshal(body, &wrapped) == nil {
		switch {
		case streamError(wrapped.Error) != "":
			msg = streamError(wrapped.Error)
		case wrapped.Message != "":
			msg = wrapped.Message
		case wrapped.Detail != "":
			msg = wrapped.Detail
		}
	}
	if len(msg) > 500 {
		msg = msg[:500] + "…"
	}
	hint := ""
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		hint = " (check the agent API key)"
	case http.StatusNotFound:
		hint = " (check the agent endpoint URL and model name)"
	case http.StatusTooManyRequests:
		hint = " (rate limited by the model provider; try again later)"
	}
	if msg == "" {
		return fmt.Errorf("agent model returned %s%s", status, hint)
	}
	return fmt.Errorf("agent model returned %s: %s%s", status, msg, hint)
}

func tooLarge() error {
	return fmt.Errorf("agent model response is larger than %d MiB", MaxResponseBytes>>20)
}

func readStream(body io.Reader, delta func(string)) (Message, error) {
	result := Message{Role: "assistant"}
	var content strings.Builder
	var calls toolCalls
	seenDone := false
	read := 0
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if read += len(line) + 1; read > MaxResponseBytes {
			return Message{}, tooLarge()
		}
		// SSE allows "data:" with or without a following space; other fields (event:, id:, comments) are ignored.
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			seenDone = true
			break
		}
		var chunk struct {
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Delta        struct {
					Content   string          `json:"content"`
					ToolCalls []toolCallDelta `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return Message{}, fmt.Errorf("invalid agent model stream: %w", err)
		}
		if msg := streamError(chunk.Error); msg != "" {
			return Message{}, errors.New("agent model error: " + msg)
		}
		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				// Some OpenAI-compatible servers close the stream without [DONE]; a finish_reason is enough.
				seenDone = true
			}
			if choice.Delta.Content != "" {
				content.WriteString(choice.Delta.Content)
				if delta != nil {
					delta(choice.Delta.Content)
				}
			}
			for _, tc := range choice.Delta.ToolCalls {
				calls.add(tc)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return Message{}, errors.New("agent model stream has a line longer than 1 MiB")
		}
		return Message{}, err
	}
	if !seenDone {
		return Message{}, errors.New("agent model stream ended before [DONE]")
	}
	return finish(result, content.String(), &calls)
}

// readCompletion reads a non-streaming chat completion.
func readCompletion(body io.Reader) (Message, error) {
	b, err := io.ReadAll(body)
	if err != nil {
		return Message{}, err
	}
	if len(b) > MaxResponseBytes {
		return Message{}, tooLarge()
	}
	var resp struct {
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			Message struct {
				Content   *string    `json:"content"`
				ToolCalls []ToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return Message{}, fmt.Errorf("invalid agent model response: %w", err)
	}
	if msg := streamError(resp.Error); msg != "" {
		return Message{}, errors.New("agent model error: " + msg)
	}
	if len(resp.Choices) == 0 {
		return Message{}, errors.New("agent model response has no choices")
	}
	m := resp.Choices[0].Message
	var calls toolCalls
	for i, tc := range m.ToolCalls {
		d := toolCallDelta{Index: &i, ID: tc.ID, Type: tc.Type}
		d.Function.Name, d.Function.Arguments = tc.Function.Name, tc.Function.Arguments
		calls.add(d)
	}
	content := ""
	if m.Content != nil {
		content = *m.Content
	}
	return finish(Message{Role: "assistant"}, content, &calls)
}

func finish(result Message, content string, calls *toolCalls) (Message, error) {
	result.Content = content
	tcs, err := calls.result()
	if err != nil {
		return Message{}, err
	}
	if len(tcs) > 0 {
		result.ToolCalls = tcs
		if strings.TrimSpace(result.Content) == "" {
			result.Content = ""
		}
	}
	return result, nil
}
