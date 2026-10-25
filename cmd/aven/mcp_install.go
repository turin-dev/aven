package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// mcpAgent describes where an MCP client keeps its project-level config and how a stdio server entry looks.
type mcpAgent struct {
	file  string // relative to the project directory
	key   string // top-level object holding the servers
	entry map[string]any
}

var mcpAgents = map[string]mcpAgent{
	"claude-code": {file: ".mcp.json", key: "mcpServers", entry: map[string]any{"command": "aven", "args": []string{"mcp"}}},
	"cursor":      {file: filepath.Join(".cursor", "mcp.json"), key: "mcpServers", entry: map[string]any{"command": "aven", "args": []string{"mcp"}}},
	"vscode":      {file: filepath.Join(".vscode", "mcp.json"), key: "servers", entry: map[string]any{"type": "stdio", "command": "aven", "args": []string{"mcp"}}},
}

func mcpAgentNames() string {
	names := make([]string, 0, len(mcpAgents))
	for n := range mcpAgents {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, "|")
}

func cmdMCPInstall(args []string) {
	var agents []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--agent" && i+1 < len(args):
			agents = append(agents, args[i+1])
			i++
		case strings.HasPrefix(a, "--agent="):
			agents = append(agents, strings.TrimPrefix(a, "--agent="))
		default:
			die("usage: aven mcp install [--agent %s]...", mcpAgentNames())
		}
	}
	dir, err := os.Getwd()
	if err != nil {
		die("%v", err)
	}
	written, err := installMCP(dir, agents)
	if err != nil {
		die("%v", err)
	}
	for _, f := range written {
		fmt.Fprintf(os.Stderr, "aven: wrote the aven MCP server to %s\n", f)
	}
	fmt.Fprintln(os.Stderr, "aven: the server runs `aven mcp`, which uses your aven login session (or KEEL_TOKEN). Restart the MCP client to load it.")
}

// installMCP merges a "aven" stdio server into each agent's project config, keeping other servers and settings.
// Without explicit agents it writes Claude Code's .mcp.json plus Cursor/VS Code configs whose directories already exist.
func installMCP(dir string, agents []string) ([]string, error) {
	if len(agents) == 0 {
		agents = []string{"claude-code"}
		for _, n := range []string{"cursor", "vscode"} {
			if st, err := os.Stat(filepath.Join(dir, filepath.Dir(mcpAgents[n].file))); err == nil && st.IsDir() {
				agents = append(agents, n)
			}
		}
	}
	var written []string
	for _, name := range agents {
		agent, ok := mcpAgents[name]
		if !ok {
			return written, fmt.Errorf("unknown agent %q (use %s)", name, mcpAgentNames())
		}
		path := filepath.Join(dir, agent.file)
		doc := map[string]any{}
		if b, err := os.ReadFile(path); err == nil {
			if len(bytes.TrimSpace(b)) > 0 {
				if err := json.Unmarshal(b, &doc); err != nil {
					return written, fmt.Errorf("%s is not valid JSON (fix or remove it first): %v", path, err)
				}
			}
		} else if !os.IsNotExist(err) {
			return written, err
		}
		servers, _ := doc[agent.key].(map[string]any)
		if servers == nil {
			if _, exists := doc[agent.key]; exists {
				return written, fmt.Errorf("%s: %q is not an object", path, agent.key)
			}
			servers = map[string]any{}
		}
		servers["aven"] = agent.entry
		doc[agent.key] = servers
		b, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return written, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return written, err
		}
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			return written, err
		}
		written = append(written, path)
	}
	return written, nil
}
