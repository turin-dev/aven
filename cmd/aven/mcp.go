package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/turin-dev/aven/internal/compat"
	"io"
	"net/http"
	"os"
	"strings"
)

const mcpProxyMaxMessage = 4 << 20

type mcpProxyMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func cmdMCP(c *config, args []string) {
	if len(args) > 0 && args[0] == "install" {
		cmdMCPInstall(args[1:])
		return
	}
	if len(args) > 0 {
		if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
			fmt.Println("usage: aven mcp                 serve the hosted Aven MCP endpoint over stdio (uses AVEN_TOKEN or the aven login session; KEEL_TOKEN still works)\n       aven mcp install [--agent claude-code|cursor|vscode]...   write MCP client config in the current directory")
			return
		}
		usage()
	}
	auth := mcpAuth{token: compat.Env("TOKEN"), cookie: c.Cookie}
	if auth.token == "" && auth.cookie == "" {
		die("aven mcp needs credentials: run aven login <url>, or set AVEN_TOKEN to an API token (aven tokens create --name mcp --project <project> --role member; KEEL_TOKEN still works)")
	}
	if err := runMCPProxy(c, auth, os.Stdin, os.Stdout, os.Stderr, http.DefaultClient); err != nil {
		die("mcp: %v", err)
	}
}

// mcpAuth is the credential the bridge attaches to each request: an API token (Bearer) wins over the login session cookie.
type mcpAuth struct {
	token, cookie string
}

func (a mcpAuth) apply(r *http.Request) {
	if a.token != "" {
		r.Header.Set("Authorization", "Bearer "+a.token)
	} else if a.cookie != "" {
		r.AddCookie(&http.Cookie{Name: "aven_session", Value: a.cookie})
	}
}

// runMCPProxy forwards newline-delimited MCP messages to the authenticated hosted endpoint.
// It writes protocol responses only to stdout; diagnostics go to stderr.
func runMCPProxy(c *config, auth mcpAuth, in io.Reader, out, errOut io.Writer, client *http.Client) error {
	if c.URL == "" {
		return errors.New("not logged in (run: aven login <url>)")
	}
	if auth.token == "" && auth.cookie == "" {
		return errors.New("no credentials (run: aven login <url> or set AVEN_TOKEN)")
	}
	if client == nil {
		client = http.DefaultClient
	}
	endpoint := strings.TrimRight(c.URL, "/") + "/mcp"
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), mcpProxyMaxMessage+1)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if len(line) == 0 {
			continue
		}
		if len(line) > mcpProxyMaxMessage {
			fmt.Fprintln(errOut, "aven: mcp message exceeds 4 MiB")
			continue
		}
		var message mcpProxyMessage
		if err := json.Unmarshal(line, &message); err != nil {
			writeProxyError(out, nil, -32700, "parse error")
			continue
		}
		req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(line))
		if err != nil {
			return err
		}
		auth.apply(req)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		setMCPProxyHeaders(req, message)
		resp, err := client.Do(req)
		if err != nil {
			fmt.Fprintf(errOut, "aven: mcp request failed: %v\n", err)
			writeProxyError(out, message.ID, -32000, err.Error())
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, mcpProxyMaxMessage+1))
		resp.Body.Close()
		if readErr != nil {
			return readErr
		}
		if len(body) > mcpProxyMaxMessage {
			writeProxyError(out, message.ID, -32000, "MCP response exceeds 4 MiB")
			continue
		}
		if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent {
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			if isMCPResponse(body) {
				fmt.Fprintln(out, string(body))
				continue
			}
			var apiErr struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(body, &apiErr)
			if apiErr.Error == "" {
				apiErr.Error = strings.TrimSpace(string(body))
			}
			if apiErr.Error == "" {
				apiErr.Error = http.StatusText(resp.StatusCode)
			}
			if resp.StatusCode == http.StatusUnauthorized {
				apiErr.Error += " (run: aven login <url>, or check AVEN_TOKEN)"
			}
			fmt.Fprintf(errOut, "aven: mcp endpoint returned HTTP %d: %s\n", resp.StatusCode, apiErr.Error)
			writeProxyError(out, message.ID, -32000, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, apiErr.Error))
			continue
		}
		if len(bytes.TrimSpace(body)) > 0 {
			fmt.Fprintln(out, string(body))
		}
	}
	return scanner.Err()
}

func setMCPProxyHeaders(req *http.Request, message mcpProxyMessage) {
	req.Header.Set("Mcp-Method", message.Method)
	var params map[string]json.RawMessage
	_ = json.Unmarshal(message.Params, &params)
	if message.Method == "tools/call" {
		var name string
		if json.Unmarshal(params["name"], &name) == nil && name != "" {
			req.Header.Set("Mcp-Name", name)
		}
	}
	var meta map[string]json.RawMessage
	_ = json.Unmarshal(params["_meta"], &meta)
	var version string
	if json.Unmarshal(meta["io.modelcontextprotocol/protocolVersion"], &version) == nil && version != "" {
		req.Header.Set("MCP-Protocol-Version", version)
		return
	}
	// Older initialize clients carry their version at the top level of params.
	if message.Method == "initialize" {
		var initialize struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(message.Params, &initialize)
		if initialize.ProtocolVersion != "" {
			req.Header.Set("MCP-Protocol-Version", initialize.ProtocolVersion)
		}
	}
}

func isMCPResponse(body []byte) bool {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil {
		return false
	}
	return len(envelope["jsonrpc"]) > 0 && (len(envelope["result"]) > 0 || len(envelope["error"]) > 0)
}

func writeProxyError(out io.Writer, id json.RawMessage, code int, message string) {
	if len(id) == 0 {
		return
	}
	_ = json.NewEncoder(out).Encode(map[string]any{
		"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message},
	})
}
