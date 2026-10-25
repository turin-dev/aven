package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/deploy"
)

const (
	mcpCurrentVersion = "2026-07-28"
	mcpLegacyVersion  = "2025-11-25"
	mcpMaxBody        = 4 << 20
)

// mcpLegacyVersions are the handshake-based protocol versions still accepted alongside the stateless 2026-07-28 core.
var mcpLegacyVersions = map[string]bool{"2025-11-25": true, "2025-06-18": true, "2025-03-26": true}

const (
	mcpMetaVersion = "io.modelcontextprotocol/protocolVersion"
	mcpMetaCaps    = "io.modelcontextprotocol/clientCapabilities"
	mcpMetaServer  = "io.modelcontextprotocol/serverInfo"
)

type mcpMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

func mcpString(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func mcpObject(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func mcpReadTool(name, description string, properties map[string]any, required ...string) mcpTool {
	return mcpTool{Name: name, Description: description, InputSchema: mcpObject(properties, required...), Annotations: map[string]any{
		"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false,
	}}
}

// mcpWriteTool marks a state-changing tool. destructive=true makes clients that honor hints ask for confirmation
// (deploys, rollbacks, variable changes); additive tools (create service, add domain) are not destructive.
func mcpWriteTool(name, description string, destructive bool, properties map[string]any, required ...string) mcpTool {
	return mcpTool{Name: name, Description: description, InputSchema: mcpObject(properties, required...), Annotations: map[string]any{
		"readOnlyHint": false, "destructiveHint": destructive, "idempotentHint": false, "openWorldHint": false,
	}}
}

var mcpTools = []mcpTool{
	mcpReadTool("whoami", "Show the authenticated user, role, and API token scope.", map[string]any{}),
	mcpReadTool("list_projects", "List projects visible to the authenticated API token.", map[string]any{}),
	mcpReadTool("inspect_project", "Inspect a project, including its environments and services.", map[string]any{"project_id": mcpString("Project ID")}, "project_id"),
	mcpReadTool("list_services", "List services in a project.", map[string]any{"project_id": mcpString("Project ID")}, "project_id"),
	mcpReadTool("inspect_service", "Inspect a service instance and its configuration in one environment. Sealed variables are redacted.", map[string]any{
		"service_id": mcpString("Service ID"), "environment_id": mcpString("Environment ID"),
	}, "service_id", "environment_id"),
	mcpReadTool("list_environments", "List environments in a project.", map[string]any{"project_id": mcpString("Project ID")}, "project_id"),
	mcpReadTool("inspect_environment", "Inspect an environment and the services deployed in it. Sealed variables are redacted.", map[string]any{"environment_id": mcpString("Environment ID")}, "environment_id"),
	mcpReadTool("list_templates", "List templates available to the authenticated user.", map[string]any{}),
	mcpReadTool("list_deployments", "List recent deployments for a service in an environment.", map[string]any{
		"service_id": mcpString("Service ID"), "environment_id": mcpString("Environment ID"),
		"limit": map[string]any{"type": "integer", "description": "Maximum number of deployments (1–500, default 50)"},
	}, "service_id", "environment_id"),
	mcpReadTool("inspect_deployment", "Inspect a deployment and its current status.", map[string]any{"deployment_id": mcpString("Deployment ID")}, "deployment_id"),
	mcpReadTool("get_logs", "Read a bounded snapshot of build or runtime logs for a deployment.", map[string]any{
		"deployment_id": mcpString("Deployment ID"),
		"type":          map[string]any{"type": "string", "enum": []string{"runtime", "build"}, "description": "Log stream to read (default runtime)"},
		"tail":          map[string]any{"type": "integer", "description": "Maximum number of log lines (1–1000, default 200)"},
		"filter":        mcpString("Optional runtime log filter using the dashboard search syntax (for example: error -healthcheck)"),
	}, "deployment_id"),
	mcpReadTool("get_variables", "Read resolved service variables. Sealed values and values derived from sealed variables are redacted by the API.", map[string]any{
		"service_id": mcpString("Service ID"), "environment_id": mcpString("Environment ID"),
	}, "service_id", "environment_id"),
	mcpWriteTool("set_variables", "Upsert service variables (a null value deletes the variable). Other variables are kept; sealed values stay sealed. Changes are staged unless deploy is true.", true, map[string]any{
		"service_id": mcpString("Service ID"), "environment_id": mcpString("Environment ID"),
		"variables": map[string]any{"type": "object", "description": "Variable names mapped to string values, or null to delete", "additionalProperties": map[string]any{"type": []string{"string", "null"}}},
		"deploy":    map[string]any{"type": "boolean", "description": "Deploy the service after saving (default false)"},
	}, "service_id", "environment_id", "variables"),
	mcpWriteTool("deploy", "Deploy the service's current source and staged configuration in an environment.", true, map[string]any{
		"service_id": mcpString("Service ID"), "environment_id": mcpString("Environment ID"),
	}, "service_id", "environment_id"),
	mcpWriteTool("redeploy", "Redeploy an existing deployment image.", true, map[string]any{"deployment_id": mcpString("Deployment ID")}, "deployment_id"),
	mcpWriteTool("restart_service", "Restart a service in an environment.", true, map[string]any{
		"service_id": mcpString("Service ID"), "environment_id": mcpString("Environment ID"),
	}, "service_id", "environment_id"),
	mcpWriteTool("rollback", "Roll back to a successful deployment, including its variable snapshot.", true, map[string]any{"deployment_id": mcpString("Deployment ID")}, "deployment_id"),
	mcpWriteTool("create_service_from_image", "Create an image-based service in a project and environment.", false, map[string]any{
		"project_id": mcpString("Project ID"), "environment_id": mcpString("Environment ID"),
		"image": mcpString("Container image, for example nginx:alpine"),
		"name":  mcpString("Optional service name"),
	}, "project_id", "environment_id", "image"),
	mcpWriteTool("create_service_from_template", "Create and deploy services from a Aven template.", false, map[string]any{
		"project_id": mcpString("Project ID"), "environment_id": mcpString("Environment ID"),
		"template_key": mcpString("Template key"),
		"inputs":       map[string]any{"type": "object", "description": "Template input values", "additionalProperties": map[string]any{"type": "string"}},
	}, "project_id", "environment_id", "template_key"),
	mcpWriteTool("add_domain", "Add a generated or custom domain to a service instance.", false, map[string]any{
		"service_id": mcpString("Service ID"), "environment_id": mcpString("Environment ID"),
		"host":        mcpString("Custom hostname; omit or use an empty string to generate a Aven domain"),
		"target_port": map[string]any{"type": "integer", "description": "Optional container port to route to"},
	}, "service_id", "environment_id"),
}

func (s *Server) mcpHandler(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// Bearer API tokens (KEEL_API_TOKEN or keel_ tokens) are the normal credential. The `aven mcp` stdio bridge may
		// also forward the CLI's `aven login` session cookie; requireAuth rejects cross-origin cookie POSTs (CSRF).
		s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.serveMCP(w, r, mux)
		})).ServeHTTP(w, r)
	})
}

func (s *Server) serveMCP(w http.ResponseWriter, r *http.Request, mux *http.ServeMux) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	body, err := io.ReadAll(io.LimitReader(r.Body, mcpMaxBody+1))
	if err != nil || len(body) > mcpMaxBody {
		writeMCPError(w, nil, http.StatusRequestEntityTooLarge, -32700, "request body too large", nil)
		return
	}
	var msg mcpMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		writeMCPError(w, nil, http.StatusBadRequest, -32700, "parse error", nil)
		return
	}
	id := msg.ID
	if msg.JSONRPC != "2.0" || msg.Method == "" {
		writeMCPError(w, id, http.StatusBadRequest, -32600, "invalid request", nil)
		return
	}
	if len(id) != 0 && !validMCPID(id) {
		writeMCPError(w, json.RawMessage("null"), http.StatusBadRequest, -32600, "invalid request id", nil)
		return
	}

	params := map[string]json.RawMessage{}
	if len(msg.Params) > 0 {
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			writeMCPError(w, id, http.StatusBadRequest, -32602, "params must be an object", nil)
			return
		}
	}
	version, modern, protocolErr := mcpRequestProtocol(r, msg, params)
	if protocolErr != nil {
		code, message, data := protocolErr.code, protocolErr.message, protocolErr.data
		writeMCPError(w, id, http.StatusBadRequest, code, message, data)
		return
	}
	if len(id) == 0 {
		// Notifications never produce a JSON-RPC response. Tool calls require IDs, so they are not executed as notifications.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if version == mcpCurrentVersion && !modern {
		writeMCPError(w, id, http.StatusBadRequest, -32021, "modern MCP requests require clientCapabilities metadata", nil)
		return
	}

	var result any
	switch msg.Method {
	case "server/discover":
		if !modern {
			writeMCPError(w, id, http.StatusBadRequest, -32022, "server/discover requires protocol version 2026-07-28", map[string]any{"supported": []string{mcpCurrentVersion}})
			return
		}
		result = map[string]any{
			"resultType": "complete", "supportedVersions": []string{mcpCurrentVersion, mcpLegacyVersion},
			"capabilities": map[string]any{"tools": map[string]any{"listChanged": false}},
			"_meta":        map[string]any{mcpMetaServer: s.mcpServerInfo()},
			"instructions": "Manage Aven projects, services, environments, deployments, logs, variables, and domains. API token scopes and project RBAC apply to every tool call. Sealed variables are redacted.",
			"ttlMs":        300000, "cacheScope": "private",
		}
	case "initialize":
		// Legacy handshake: answer with the client's version when we speak it, otherwise our newest legacy version.
		negotiated := mcpLegacyVersion
		if requested, _ := mcpParamString(params, "protocolVersion", false); mcpLegacyVersions[requested] {
			negotiated = requested
		}
		result = map[string]any{
			"protocolVersion": negotiated,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      s.mcpServerInfo(),
			"instructions":    "Manage Aven infrastructure with the authenticated user's permissions. Sealed variables are redacted.",
		}
	case "ping":
		result = map[string]any{}
	case "tools/list":
		tools := make([]mcpTool, len(mcpTools))
		copy(tools, mcpTools)
		out := map[string]any{"tools": tools}
		if modern {
			out["resultType"] = "complete"
			out["ttlMs"] = 300000
			out["cacheScope"] = "private"
		}
		result = out
	case "tools/call":
		name, err := mcpParamString(params, "name", true)
		if err != nil {
			writeMCPError(w, id, http.StatusBadRequest, -32602, err.Error(), nil)
			return
		}
		if modern && r.Header.Get("Mcp-Name") != name {
			writeMCPError(w, id, http.StatusBadRequest, -32020, "Mcp-Name header does not match params.name", nil)
			return
		}
		var arguments map[string]json.RawMessage
		if raw := params["arguments"]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &arguments); err != nil {
				writeMCPError(w, id, http.StatusBadRequest, -32602, "arguments must be an object", nil)
				return
			}
		}
		if arguments == nil {
			arguments = map[string]json.RawMessage{}
		}
		value, err := s.runMCPTool(mux, r, name, arguments)
		if err != nil {
			result = mcpToolResult(err.Error(), true, modern)
		} else {
			result = mcpToolResult(mcpText(value), false, modern)
		}
	default:
		writeMCPError(w, id, http.StatusOK, -32601, "method not found: "+msg.Method, nil)
		return
	}
	if modern {
		if m, ok := result.(map[string]any); ok {
			m["_meta"] = map[string]any{mcpMetaServer: s.mcpServerInfo()}
		}
	}
	writeMCPResult(w, id, result)
}

type mcpProtocolError struct {
	code    int
	message string
	data    any
}

func mcpRequestProtocol(r *http.Request, msg mcpMessage, params map[string]json.RawMessage) (version string, modern bool, protocolErr *mcpProtocolError) {
	var meta map[string]json.RawMessage
	if raw := params["_meta"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &meta)
	}
	var bodyVersion string
	if raw := meta[mcpMetaVersion]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &bodyVersion)
	}
	headerVersion := r.Header.Get("MCP-Protocol-Version")
	if bodyVersion != "" && headerVersion != bodyVersion {
		return bodyVersion, false, &mcpProtocolError{code: -32020, message: "MCP-Protocol-Version header does not match request metadata"}
	}
	version = bodyVersion
	if version == "" {
		version = headerVersion
	}
	if version == "" {
		if msg.Method == "initialize" {
			return mcpLegacyVersion, false, nil
		}
		// Older Streamable HTTP clients did not send the version header.
		return mcpLegacyVersion, false, nil
	}
	if version != mcpCurrentVersion && !mcpLegacyVersions[version] {
		return version, false, &mcpProtocolError{code: -32022, message: "unsupported protocol version", data: map[string]any{"supported": []string{mcpCurrentVersion, mcpLegacyVersion}, "requested": version}}
	}
	if version != mcpCurrentVersion {
		return version, false, nil
	}
	if bodyVersion == "" || headerVersion == "" {
		return version, false, &mcpProtocolError{code: -32020, message: "modern MCP requests require matching protocol version metadata and header"}
	}
	if r.Header.Get("Mcp-Method") != msg.Method {
		return version, false, &mcpProtocolError{code: -32020, message: "Mcp-Method header does not match request method"}
	}
	if raw, ok := meta[mcpMetaCaps]; !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return version, false, &mcpProtocolError{code: -32021, message: "modern MCP requests require clientCapabilities metadata"}
	}
	if msg.Method == "tools/call" {
		name, _ := mcpParamString(params, "name", false)
		if name == "" || r.Header.Get("Mcp-Name") != name {
			return version, false, &mcpProtocolError{code: -32020, message: "Mcp-Name header does not match params.name"}
		}
	}
	return version, true, nil
}

func validMCPID(id json.RawMessage) bool {
	var v any
	dec := json.NewDecoder(bytes.NewReader(id))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		return false
	}
	switch v.(type) {
	case string, json.Number:
		return true
	default:
		return false
	}
}

func (s *Server) mcpServerInfo() map[string]string {
	version := s.Version
	if version == "" {
		version = "dev"
	}
	return map[string]string{"name": "Aven", "version": version}
}

func writeMCPResult(w http.ResponseWriter, id json.RawMessage, result any) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func writeMCPError(w http.ResponseWriter, id json.RawMessage, status, code int, message string, data any) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	if status != 0 {
		w.WriteHeader(status)
	}
	item := map[string]any{"code": code, "message": message}
	if data != nil {
		item["data"] = data
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": item})
}

func mcpToolResult(text string, isError, modern bool) map[string]any {
	result := map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": isError}
	if modern {
		result["resultType"] = "complete"
	}
	return result
}

func mcpText(value any) string {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(b)
}

func mcpParamString(params map[string]json.RawMessage, key string, required bool) (string, error) {
	raw := params[key]
	var value string
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", fmt.Errorf("%s must be a string", key)
		}
	}
	if required && strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}

func mcpArgString(args map[string]json.RawMessage, key string, required bool) (string, error) {
	return mcpParamString(args, key, required)
}

func (s *Server) mcpAPI(mux *http.ServeMux, parent *http.Request, method, path string, body any) (any, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	r := httptest.NewRequestWithContext(parent.Context(), method, path, reader)
	// Forward the caller's credentials and proxy headers (client IP for rate limits/audit) but not the MCP body headers.
	r.Header = parent.Header.Clone()
	for _, k := range []string{"Content-Type", "Content-Length", "Accept", "Accept-Encoding", "Mcp-Method", "Mcp-Name", "MCP-Protocol-Version", "Mcp-Session-Id"} {
		r.Header.Del(k)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	r.RemoteAddr = parent.RemoteAddr
	r.Host = parent.Host
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	resp := w.Result()
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, mcpMaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(b) > mcpMaxBody {
		return nil, errors.New("API response exceeded the MCP response limit")
	}
	if resp.StatusCode >= 400 {
		var apiErr map[string]json.RawMessage
		message := strings.TrimSpace(string(b))
		if json.Unmarshal(b, &apiErr) == nil && len(apiErr["error"]) > 0 {
			var detail string
			if json.Unmarshal(apiErr["error"], &detail) == nil {
				message = detail
			}
		}
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return nil, fmt.Errorf("Aven API (%d): %s", resp.StatusCode, message)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return map[string]any{"ok": true}, nil
	}
	var result any
	if err := json.Unmarshal(b, &result); err != nil {
		return nil, fmt.Errorf("invalid response from Aven API: %w", err)
	}
	return result, nil
}

func mcpField(value any, key string) (any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("unexpected response from Aven API")
	}
	field, ok := object[key]
	if !ok {
		return nil, fmt.Errorf("Aven API response did not contain %s", key)
	}
	return field, nil
}

func (s *Server) runMCPTool(mux *http.ServeMux, r *http.Request, name string, args map[string]json.RawMessage) (any, error) {
	get := func(key string, required bool) (string, error) { return mcpArgString(args, key, required) }
	api := func(method, path string, body any) (any, error) { return s.mcpAPI(mux, r, method, path, body) }
	serviceAndEnv := func() (string, string, error) {
		sid, err := get("service_id", true)
		if err != nil {
			return "", "", err
		}
		eid, err := get("environment_id", true)
		return sid, eid, err
	}
	switch name {
	case "whoami":
		return api(http.MethodGet, "/api/auth/me", nil)
	case "deploy":
		sid, eid, err := serviceAndEnv()
		if err != nil {
			return nil, err
		}
		return api(http.MethodPost, "/api/services/"+url.PathEscape(sid)+"/instances/"+url.PathEscape(eid)+"/deploy", map[string]any{})
	case "list_projects":
		return api(http.MethodGet, "/api/projects", nil)
	case "inspect_project", "list_services", "list_environments":
		pid, err := get("project_id", true)
		if err != nil {
			return nil, err
		}
		project, err := api(http.MethodGet, "/api/projects/"+url.PathEscape(pid), nil)
		if err != nil || name == "inspect_project" {
			return project, err
		}
		field := "services"
		if name == "list_environments" {
			field = "environments"
		}
		return mcpField(project, field)
	case "inspect_service", "get_variables":
		sid, eid, err := serviceAndEnv()
		if err != nil {
			return nil, err
		}
		endpoint := "/api/services/" + url.PathEscape(sid) + "/instances/" + url.PathEscape(eid)
		if name == "get_variables" {
			endpoint += "/variables/resolved"
		}
		return api(http.MethodGet, endpoint, nil)
	case "inspect_environment":
		eid, err := get("environment_id", true)
		if err != nil {
			return nil, err
		}
		return api(http.MethodGet, "/api/environments/"+url.PathEscape(eid)+"/canvas", nil)
	case "list_templates":
		return api(http.MethodGet, "/api/templates", nil)
	case "list_deployments":
		sid, eid, err := serviceAndEnv()
		if err != nil {
			return nil, err
		}
		limit := 50
		if raw := args["limit"]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &limit); err != nil {
				return nil, errors.New("limit must be an integer")
			}
		}
		if limit < 1 || limit > 500 {
			return nil, errors.New("limit must be between 1 and 500")
		}
		path := "/api/services/" + url.PathEscape(sid) + "/instances/" + url.PathEscape(eid) + "/deployments?limit=" + strconv.Itoa(limit)
		return api(http.MethodGet, path, nil)
	case "inspect_deployment", "redeploy", "rollback":
		did, err := get("deployment_id", true)
		if err != nil {
			return nil, err
		}
		path := "/api/deployments/" + url.PathEscape(did)
		if name == "inspect_deployment" {
			return api(http.MethodGet, path, nil)
		}
		action := "redeploy"
		if name == "rollback" {
			action = "rollback"
		}
		return api(http.MethodPost, path+"/"+action, map[string]any{})
	case "restart_service":
		sid, eid, err := serviceAndEnv()
		if err != nil {
			return nil, err
		}
		return api(http.MethodPost, "/api/services/"+url.PathEscape(sid)+"/instances/"+url.PathEscape(eid)+"/restart", map[string]any{})
	case "get_logs":
		return s.mcpLogs(mux, r, args)
	case "set_variables":
		sid, eid, err := serviceAndEnv()
		if err != nil {
			return nil, err
		}
		var variables map[string]*string
		if err := json.Unmarshal(args["variables"], &variables); err != nil || len(variables) == 0 {
			return nil, errors.New("variables must be a non-empty object of string (or null to delete) values")
		}
		deployNow := false
		if raw := args["deploy"]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &deployNow); err != nil {
				return nil, errors.New("deploy must be a boolean")
			}
		}
		// variables_patch merges into the existing set (PATCH semantics), unlike the raw editor which replaces it.
		endpoint := "/api/services/" + url.PathEscape(sid) + "/instances/" + url.PathEscape(eid)
		if deployNow {
			endpoint += "?deploy=1"
		}
		return api(http.MethodPatch, endpoint, map[string]any{"variables_patch": variables})
	case "create_service_from_image":
		pid, err := get("project_id", true)
		if err != nil {
			return nil, err
		}
		eid, err := get("environment_id", true)
		if err != nil {
			return nil, err
		}
		image, err := get("image", true)
		if err != nil {
			return nil, err
		}
		name, err := get("name", false)
		if err != nil {
			return nil, err
		}
		body := map[string]any{"source_type": "image", "image": image, "environment_id": eid}
		if name != "" {
			body["name"] = name
		}
		return api(http.MethodPost, "/api/projects/"+url.PathEscape(pid)+"/services", body)
	case "create_service_from_template":
		pid, err := get("project_id", true)
		if err != nil {
			return nil, err
		}
		eid, err := get("environment_id", true)
		if err != nil {
			return nil, err
		}
		key, err := get("template_key", true)
		if err != nil {
			return nil, err
		}
		inputs := map[string]string{}
		if raw := args["inputs"]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &inputs); err != nil || inputs == nil {
				return nil, errors.New("inputs must be an object of string values")
			}
		}
		return api(http.MethodPost, "/api/projects/"+url.PathEscape(pid)+"/templates/"+url.PathEscape(key), map[string]any{"environment_id": eid, "inputs": inputs})
	case "add_domain":
		sid, eid, err := serviceAndEnv()
		if err != nil {
			return nil, err
		}
		host, err := get("host", false)
		if err != nil {
			return nil, err
		}
		body := map[string]any{}
		if host != "" {
			body["host"] = host
		}
		if raw := args["target_port"]; len(raw) > 0 {
			var port int
			if err := json.Unmarshal(raw, &port); err != nil {
				return nil, errors.New("target_port must be an integer")
			}
			body["target_port"] = port
		}
		return api(http.MethodPost, "/api/services/"+url.PathEscape(sid)+"/instances/"+url.PathEscape(eid)+"/domains", body)
	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
}

func (s *Server) mcpLogs(mux *http.ServeMux, r *http.Request, args map[string]json.RawMessage) (any, error) {
	did, err := mcpArgString(args, "deployment_id", true)
	if err != nil {
		return nil, err
	}
	kind, err := mcpArgString(args, "type", false)
	if err != nil {
		return nil, err
	}
	if kind == "" {
		kind = "runtime"
	}
	if kind != "runtime" && kind != "build" {
		return nil, errors.New("type must be runtime or build")
	}
	tail := 200
	if raw := args["tail"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &tail); err != nil {
			return nil, errors.New("tail must be an integer")
		}
	}
	if tail < 1 || tail > 1000 {
		return nil, errors.New("tail must be between 1 and 1000")
	}
	// Authorize through the existing deployment endpoint before accessing either log store.
	if _, err := s.mcpAPI(mux, r, http.MethodGet, "/api/deployments/"+url.PathEscape(did), nil); err != nil {
		return nil, err
	}
	if s.Store == nil || s.Deploy == nil {
		return nil, errors.New("deployment logs are unavailable")
	}
	d, err := s.Store.GetDeployment(r.Context(), did)
	if err != nil {
		return nil, err
	}
	if kind == "build" {
		if s.Deploy.Logs == nil {
			return map[string]any{"deployment_id": did, "type": kind, "logs": []string{}}, nil
		}
		past, _, cancel := s.Deploy.Logs.Subscribe(did)
		cancel()
		if len(past) > tail {
			past = past[len(past)-tail:]
		}
		return map[string]any{"deployment_id": did, "type": kind, "logs": past}, nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	filter, err := mcpArgString(args, "filter", false)
	if err != nil {
		return nil, err
	}
	q := deploy.ParseFilter(filter)
	logs := make([]deploy.LogEntry, 0, tail)
	if err := s.Deploy.StreamDeployLogs(ctx, d, tail, false, func(line deploy.LogEntry) {
		if filter == "" || q.Match(line.Msg) {
			logs = append(logs, line)
		}
	}); err != nil {
		return nil, err
	}
	return map[string]any{"deployment_id": did, "type": kind, "logs": logs}, nil
}
