package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPProxyForwardsModernProtocolHeaders(t *testing.T) {
	const body = `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"list_projects","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			t.Errorf("path %q, want /mcp", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer keel_test_token" {
			t.Errorf("authorization %q", got)
		}
		if got := r.Header.Get("MCP-Protocol-Version"); got != "2026-07-28" {
			t.Errorf("protocol version %q", got)
		}
		if got := r.Header.Get("Mcp-Method"); got != "tools/call" {
			t.Errorf("method header %q", got)
		}
		if got := r.Header.Get("Mcp-Name"); got != "list_projects" {
			t.Errorf("name header %q", got)
		}
		got, err := io.ReadAll(r.Body)
		if err != nil || string(got) != body {
			t.Errorf("request body %q, err %v", got, err)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"jsonrpc":"2.0","id":7,"result":{"resultType":"complete"}}`)
	}))
	defer srv.Close()

	var stdout, stderr strings.Builder
	err := runMCPProxy(&config{URL: srv.URL}, mcpAuth{token: "keel_test_token", cookie: "ignored"}, strings.NewReader(body+"\n"), &stdout, &stderr, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.Unmarshal([]byte(stdout.String()), &response); err != nil {
		t.Fatalf("stdout is not a JSON-RPC response: %q (%v)", stdout.String(), err)
	}
	if response["id"] != float64(7) || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestMCPProxyUsesLoginSessionAndSkipsNotifications(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected Authorization header without KEEL_TOKEN")
		}
		if c, err := r.Cookie("aven_session"); err != nil || c.Value != "sess" {
			t.Errorf("session cookie not forwarded: %v", err)
		}
		if r.Header.Get("Mcp-Method") == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if got := r.Header.Get("MCP-Protocol-Version"); got != "2025-06-18" {
			t.Errorf("initialize version header %q", got)
		}
		io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18"}}`)
	}))
	defer srv.Close()
	in := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"
	var stdout, stderr strings.Builder
	if err := runMCPProxy(&config{URL: srv.URL}, mcpAuth{cookie: "sess"}, strings.NewReader(in), &stdout, &stderr, srv.Client()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || strings.Count(stdout.String(), "\n") != 1 || stderr.Len() != 0 {
		t.Fatalf("calls=%d stdout=%q stderr=%q", calls, stdout.String(), stderr.String())
	}
}

func TestMCPInstallMergesExistingConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{"mcpServers":{"other":{"command":"x"}},"extra":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	written, err := installMCP(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 2 {
		t.Fatalf("written %v, want .mcp.json and .cursor/mcp.json", written)
	}
	var doc struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
		Extra bool `json:"extra"`
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if !doc.Extra || doc.MCPServers["other"].Command != "x" || doc.MCPServers["aven"].Command != "aven" || strings.Join(doc.MCPServers["aven"].Args, " ") != "mcp" {
		t.Fatalf("merged config: %s", b)
	}
	if _, err := installMCP(dir, []string{"nope"}); err == nil {
		t.Fatal("unknown agent accepted")
	}
}
