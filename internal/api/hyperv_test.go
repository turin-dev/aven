package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/store"
)

func TestHyperVHostPairingAndPowerCommands(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hyperv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Server{Store: st, Agents: &agentprotocol.Hub{}, Deploy: &deploy.Manager{Store: st, Docker: testDocker(), Cfg: deploy.Config{DataDir: t.TempDir()}},
		APIToken: "admin-test-token", AdminPassword: "password123", SessionKey: make([]byte, 32), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv := httptest.NewTLSServer(s.Handler())
	defer srv.Close()
	s.PublicURL = srv.URL
	client := srv.Client()

	call := func(method, path, bearer string, body any) (int, []byte) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rd)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, out
	}
	admin := func(method, path string, body any) (int, []byte) { return call(method, path, "admin-test-token", body) }

	// A remote Linux region node joins the normal way.
	code, out := admin("POST", "/api/nodes/join-tokens", map[string]any{"name": "win-region"})
	if code != 201 {
		t.Fatalf("join token: %d %s", code, out)
	}
	var join struct{ Token string }
	_ = json.Unmarshal(out, &join)
	code, out = call("POST", "/api/agent/enroll", "", map[string]string{"token": join.Token})
	if code != 200 {
		t.Fatalf("agent enroll: %d %s", code, out)
	}
	var agent struct {
		NodeID string `json:"node_id"`
	}
	_ = json.Unmarshal(out, &agent)
	nid := agent.NodeID

	// Pairing token validation.
	if code, _ := admin("POST", "/api/nodes/"+store.LocalNodeID+"/hyperv/pair-token", map[string]string{"vm_name": "AvenRegion"}); code != 400 {
		t.Fatalf("local node pair: %d", code)
	}
	if code, _ := admin("POST", "/api/nodes/"+nid+"/hyperv/pair-token", map[string]string{"vm_name": "bad name'"}); code != 400 {
		t.Fatalf("bad vm name: %d", code)
	}
	if code, _ := admin("POST", "/api/nodes/nod_missing/hyperv/pair-token", map[string]string{"vm_name": "AvenRegion"}); code != 404 {
		t.Fatalf("missing node: %d", code)
	}
	if code, _ := admin("POST", "/api/nodes/"+nid+"/hyperv/power", map[string]string{"action": "start"}); code != 409 {
		t.Fatalf("power before pairing: %d", code)
	}

	pair := func() string {
		t.Helper()
		code, out := admin("POST", "/api/nodes/"+nid+"/hyperv/pair-token", map[string]string{"vm_name": "AvenRegion"})
		if code != 201 {
			t.Fatalf("pair token: %d %s", code, out)
		}
		var tok struct{ Token, Command string }
		_ = json.Unmarshal(out, &tok)
		for _, want := range []string{"aven-hyperv-host.ps1", "-UseBasicParsing", "-Server '" + srv.URL + "'", "-NodeId '" + nid + "'", "-Token '" + tok.Token + "'", "-VmName 'AvenRegion'"} {
			if !strings.Contains(tok.Command, want) {
				t.Fatalf("pair command lacks %q: %s", want, tok.Command)
			}
		}
		return tok.Token
	}
	enroll := func(token string) (int, string) {
		t.Helper()
		code, out := call("POST", "/api/hyperv/enroll", "", map[string]string{"token": token, "host_name": "WIN-HV01"})
		var res struct {
			NodeID     string `json:"node_id"`
			VMName     string `json:"vm_name"`
			Credential string `json:"credential"`
		}
		_ = json.Unmarshal(out, &res)
		if code == 200 && (res.NodeID != nid || len(res.Credential) != 64) {
			t.Fatalf("enroll response: %s", out)
		}
		return code, res.Credential
	}

	token := pair()
	code, cred := enroll(token)
	if code != 200 {
		t.Fatalf("hyperv enroll: %d", code)
	}
	if code, _ := enroll(token); code != 401 {
		t.Fatalf("pairing token reused: %d", code)
	}
	if code, _ := call("POST", "/api/hyperv/status", strings.Repeat("0", 64), map[string]string{"power_state": "running"}); code != 401 {
		t.Fatalf("wrong credential: %d", code)
	}
	// Plain HTTP is refused before the credential is even looked at.
	plain := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/hyperv/status", strings.NewReader(`{"power_state":"running"}`))
	r.Header.Set("Authorization", "Bearer "+cred)
	s.Handler().ServeHTTP(plain, r)
	if plain.Code != 403 {
		t.Fatalf("plain HTTP status: %d", plain.Code)
	}

	// Paired, but no heartbeat yet: the host counts as offline.
	if code, _ := admin("POST", "/api/nodes/"+nid+"/hyperv/power", map[string]string{"action": "stop"}); code != 409 {
		t.Fatalf("power with offline host: %d", code)
	}
	if code, _ := call("POST", "/api/hyperv/status", cred, map[string]string{"power_state": "bogus"}); code != 400 {
		t.Fatalf("bogus power state: %d", code)
	}
	if code, out := call("POST", "/api/hyperv/status", cred, map[string]string{"power_state": "running"}); code != 204 {
		t.Fatalf("status: %d %s", code, out)
	}
	if code, _ := call("GET", "/api/hyperv/commands/next", cred, nil); code != 204 {
		t.Fatalf("empty queue: %d", code)
	}
	if code, _ := admin("POST", "/api/nodes/"+nid+"/hyperv/power", map[string]string{"action": "reboot"}); code != 400 {
		t.Fatalf("bad action: %d", code)
	}
	code, out = admin("POST", "/api/nodes/"+nid+"/hyperv/power", map[string]string{"action": "stop"})
	if code != 202 {
		t.Fatalf("power stop: %d %s", code, out)
	}
	var queued store.HyperVCommand
	_ = json.Unmarshal(out, &queued)
	if code, _ := admin("POST", "/api/nodes/"+nid+"/hyperv/power", map[string]string{"action": "start"}); code != 409 {
		t.Fatalf("second command while one is pending: %d", code)
	}

	code, out = call("GET", "/api/hyperv/commands/next", cred, nil)
	var claimed store.HyperVCommand
	if err := json.Unmarshal(out, &claimed); code != 200 || err != nil || claimed.ID != queued.ID || claimed.Action != "stop" || claimed.Status != "running" {
		t.Fatalf("claim: %d %s", code, out)
	}
	if code, _ := call("GET", "/api/hyperv/commands/next", cred, nil); code != 204 {
		t.Fatalf("running command handed out twice: %d", code)
	}
	if code, out := call("POST", "/api/hyperv/commands/"+claimed.ID+"/result", cred, map[string]any{"ok": true, "power_state": "off"}); code != 204 {
		t.Fatalf("result: %d %s", code, out)
	}
	if code, _ := call("POST", "/api/hyperv/commands/"+claimed.ID+"/result", cred, map[string]any{"ok": true}); code != 404 {
		t.Fatalf("result reported twice: %d", code)
	}

	view := func() *hyperVNodeView {
		t.Helper()
		code, out := admin("GET", "/api/nodes", nil)
		if code != 200 {
			t.Fatalf("nodes: %d %s", code, out)
		}
		var nodes []struct {
			ID     string          `json:"id"`
			HyperV *hyperVNodeView `json:"hyperv"`
		}
		_ = json.Unmarshal(out, &nodes)
		for _, n := range nodes {
			if n.ID == nid {
				return n.HyperV
			}
		}
		t.Fatalf("node %s not listed", nid)
		return nil
	}
	hv := view()
	if hv == nil || !hv.HostOnline || hv.PowerState != "off" || hv.HostName != "WIN-HV01" || hv.VMName != "AvenRegion" ||
		hv.LastCommand == nil || hv.LastCommand.Status != "done" {
		t.Fatalf("node view: %+v", hv)
	}

	// Pairing again replaces the credential and fails whatever the old host had in flight.
	if code, _ := admin("POST", "/api/nodes/"+nid+"/hyperv/power", map[string]string{"action": "start"}); code != 202 {
		t.Fatalf("power start: %d", code)
	}
	code, newCred := enroll(pair())
	if code != 200 || newCred == cred {
		t.Fatalf("re-pair: %d", code)
	}
	if code, _ := call("POST", "/api/hyperv/status", cred, map[string]string{"power_state": "running"}); code != 401 {
		t.Fatalf("old credential after re-pair: %d", code)
	}
	if hv := view(); hv == nil || hv.HostOnline || hv.LastCommand == nil || hv.LastCommand.Status != "failed" {
		t.Fatalf("after re-pair: %+v", hv)
	}

	// Deleting the node drops its Hyper-V host and credential.
	if err := st.DeleteNode(context.Background(), nid); err != nil {
		t.Fatal(err)
	}
	if code, _ := call("POST", "/api/hyperv/status", newCred, map[string]string{"power_state": "running"}); code != 401 {
		t.Fatalf("credential after node delete: %d", code)
	}
	if hosts, err := st.ListHyperVHosts(context.Background()); err != nil || len(hosts) != 0 {
		t.Fatalf("hosts after node delete: %v %v", hosts, err)
	}
}
