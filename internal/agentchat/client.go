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
		return Message{}, fmt.Errorf("agent model returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}

	result := Message{Role: "assistant"}
	calls := map[int]*ToolCall{}
	seenDone := false
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
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
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Delta        struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return Message{}, fmt.Errorf("invalid agent model stream: %w", err)
		}
		if chunk.Error != nil {
			return Message{}, errors.New(chunk.Error.Message)
		}
		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				// Some OpenAI-compatible servers close the stream without [DONE]; a finish_reason is enough.
				seenDone = true
			}
			if choice.Delta.Content != "" {
				result.Content += choice.Delta.Content
				if delta != nil {
					delta(choice.Delta.Content)
				}
			}
			for _, tc := range choice.Delta.ToolCalls {
				call := calls[tc.Index]
				if call == nil {
					call = &ToolCall{Type: "function"}
					calls[tc.Index] = call
				}
				if tc.ID != "" {
					call.ID = tc.ID
				}
				if tc.Type != "" {
					call.Type = tc.Type
				}
				call.Function.Name += tc.Function.Name
				call.Function.Arguments += tc.Function.Arguments
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Message{}, err
	}
	if !seenDone {
		return Message{}, errors.New("agent model stream ended before [DONE]")
	}
	for i := 0; ; i++ {
		call, ok := calls[i]
		if !ok {
			break
		}
		if call.ID == "" || call.Function.Name == "" {
			return Message{}, errors.New("agent model returned an incomplete tool call")
		}
		result.ToolCalls = append(result.ToolCalls, *call)
	}
	if len(result.ToolCalls) > 0 && strings.TrimSpace(result.Content) == "" {
		result.Content = ""
	}
	return result, nil
}
