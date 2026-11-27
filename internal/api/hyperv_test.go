package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
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
	memberUser := testUser(t, st, "member@example.com", store.RoleMember)
	memberCookie := testSessionCookie(t, s, memberUser)
	member := func(method, path string, body any) (int, []byte) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rd)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: memberCookie})
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
	if code, _ := member("POST", "/api/nodes/"+nid+"/hyperv/pair-token", map[string]string{"vm_name": "AvenRegion"}); code != 403 {
		t.Fatalf("member pair token: %d", code)
	}
	if code, _ := member("POST", "/api/nodes/"+nid+"/hyperv/power", map[string]string{"action": "start"}); code != 403 {
		t.Fatalf("member power: %d", code)
	}
	if code, _ := member("DELETE", "/api/nodes/"+nid+"/hyperv", nil); code != 403 {
		t.Fatalf("member disconnect: %d", code)
	}

	pair := func() string {
		t.Helper()
		code, out := admin("POST", "/api/nodes/"+nid+"/hyperv/pair-token", map[string]string{"vm_name": "AvenRegion"})
		if code != 201 {
			t.Fatalf("pair token: %d %s", code, out)
		}
		var tok struct{ Token, Command string }
		_ = json.Unmarshal(out, &tok)
		for _, want := range []string{"aven-hyperv-host.ps1", "-UseBasicParsing", "-Server '" + srv.URL + "'", "-NodeId '" + nid + "'", "-Token '" + tok.Token + "'", "-VmName 'AvenRegion'", "Get-FileHash", srv.URL + "/downloads/aven-hyperv-host.ps1"} {
			if !strings.Contains(tok.Command, want) {
				t.Fatalf("pair command lacks %q: %s", want, tok.Command)
			}
		}
		if strings.Contains(tok.Command, "github.com") {
			t.Fatalf("pair command still downloads from GitHub: %s", tok.Command)
		}
		hashAt := strings.Index(tok.Command, "Get-FileHash")
		fileAt := strings.Index(tok.Command, " -File ")
		if hashAt < 0 || fileAt < 0 || hashAt > fileAt {
			t.Fatalf("hash check must run before the script: %s", tok.Command)
		}
		resp, err := client.Get(srv.URL + "/downloads/aven-hyperv-host.ps1")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !bytes.Contains(body, []byte("Unregister-ScheduledTask")) || !bytes.Contains(body, []byte("[switch]$Uninstall")) {
			t.Fatalf("host script download: %d %q", resp.StatusCode, body[:min(80, len(body))])
		}
		sum := sha256.Sum256(body)
		wantHash := strings.ToUpper(hex.EncodeToString(sum[:]))
		if !strings.Contains(tok.Command, wantHash) {
			t.Fatalf("pair command hash %s not in %s", wantHash, tok.Command)
		}
		resp, err = client.Get(srv.URL + "/downloads/aven-hyperv-host.ps1.sha256")
		if err != nil {
			t.Fatal(err)
		}
		sumBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(strings.ToLower(string(sumBody)), strings.ToLower(wantHash)) {
			t.Fatalf("script checksum: %d %s", resp.StatusCode, sumBody)
		}
		if !auditHas(t, st, "hyperv.pair_token", nid, tok.Token) {
			t.Fatal("pair token was not audited")
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
	if !auditHas(t, st, "hyperv.power", nid, "") {
		t.Fatal("power command was not audited")
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

	// A failed command's host name and error stay visible to an instance admin and are hidden from a member.
	code, out = admin("POST", "/api/nodes/"+nid+"/hyperv/power", map[string]string{"action": "start"})
	if code != 202 {
		t.Fatalf("power start for failure: %d %s", code, out)
	}
	var failing store.HyperVCommand
	code, out = call("GET", "/api/hyperv/commands/next", cred, nil)
	if err := json.Unmarshal(out, &failing); code != 200 || err != nil || failing.Action != "start" {
		t.Fatalf("claim start: %d %s", code, out)
	}
	const secretErr = `C:\ProgramData\Aven\secret-host`
	if code, out := call("POST", "/api/hyperv/commands/"+failing.ID+"/result", cred, map[string]any{"ok": false, "error": secretErr, "power_state": "running"}); code != 204 {
		t.Fatalf("failed result: %d %s", code, out)
	}
	adminView := view()
	if adminView == nil || adminView.HostName != "WIN-HV01" || adminView.LastCommand == nil || adminView.LastCommand.Error != secretErr {
		t.Fatalf("admin still sees host details: %+v", adminView)
	}
	code, out = member("GET", "/api/nodes", nil)
	if code != 200 {
		t.Fatalf("member nodes: %d %s", code, out)
	}
	if bytes.Contains(out, []byte("WIN-HV01")) || bytes.Contains(out, []byte(secretErr)) {
		t.Fatalf("member nodes leaked Hyper-V host details: %s", out)
	}
	var memberNodes []struct {
		ID     string          `json:"id"`
		HyperV *hyperVNodeView `json:"hyperv"`
	}
	_ = json.Unmarshal(out, &memberNodes)
	var memberHV *hyperVNodeView
	for _, n := range memberNodes {
		if n.ID == nid {
			memberHV = n.HyperV
		}
	}
	if memberHV == nil || memberHV.VMName != "AvenRegion" || memberHV.HostName != "" || memberHV.LastCommand == nil || memberHV.LastCommand.Error != "" || memberHV.LastCommand.Status != "failed" {
		t.Fatalf("member hyperv view: %+v", memberHV)
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

	// Disconnect revokes the host credential. A member cannot do it.
	if code, _ := member("DELETE", "/api/nodes/"+nid+"/hyperv", nil); code != 403 {
		t.Fatalf("member disconnect while paired: %d", code)
	}
	if code, out := admin("DELETE", "/api/nodes/"+nid+"/hyperv", nil); code != 204 {
		t.Fatalf("disconnect: %d %s", code, out)
	}
	if !auditHas(t, st, "hyperv.disconnected", nid, "") {
		t.Fatal("disconnect was not audited")
	}
	if code, _ := call("POST", "/api/hyperv/status", newCred, map[string]string{"power_state": "running"}); code != 401 {
		t.Fatalf("credential after disconnect: %d", code)
	}
	if code, _ := admin("DELETE", "/api/nodes/"+nid+"/hyperv", nil); code != 404 {
		t.Fatalf("disconnect twice: %d", code)
	}
	if hv := view(); hv != nil {
		t.Fatalf("hyperv block after disconnect: %+v", hv)
	}

	// Deleting the node drops any remaining Hyper-V rows.
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

func auditHas(t *testing.T, st *store.Store, kind, target, forbidden string) bool {
	t.Helper()
	entries, err := st.ListAudit(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if forbidden != "" && strings.Contains(e.Message, forbidden) {
			t.Fatalf("audit %s leaked a secret: %s", e.Kind, e.Message)
		}
		if e.Kind == kind && e.Target == target {
			found = true
		}
	}
	return found
}

func TestHyperVScriptFilesAreHardened(t *testing.T) {
	host, err := os.ReadFile(filepath.Join("..", "..", "aven-hyperv-host.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	region, err := os.ReadFile(filepath.Join("..", "..", "install-region-vm.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range append(append([]byte(nil), host...), region...) {
		if b >= 128 {
			t.Fatal("Hyper-V PowerShell scripts must stay ASCII so Windows PowerShell 5.1 reads them without a BOM")
		}
	}
	hostText := string(host)
	if !strings.Contains(hostText, "[switch]$Uninstall") || !strings.Contains(hostText, "Unregister-ScheduledTask") {
		t.Fatal("host script is missing uninstall")
	}
	if !strings.Contains(hostText, "/setowner") {
		t.Fatal("host script does not set the ProgramData owner")
	}
	if enrollAt, setAt := strings.Index(hostText, "/api/hyperv/enroll"), strings.Index(hostText, "Set-VM "); enrollAt < 0 || setAt < enrollAt {
		t.Fatal("host script changes VM autostart before enroll succeeds")
	}
	if !strings.Contains(hostText, "401") {
		t.Fatal("host script does not stop after rejected credentials")
	}
	regionText := string(region)
	if !strings.Contains(regionText, "[switch]$UseExisting") || !strings.Contains(regionText, "already exists") {
		t.Fatal("region installer still reuses an existing VM without an explicit switch")
	}
	if !strings.Contains(regionText, "Remove-VM") {
		t.Fatal("region installer does not remove a VM it failed to finish creating")
	}
	if newAt, setAt := strings.Index(regionText, "New-VM "), strings.Index(regionText, "Set-VM "); newAt < 0 || setAt < newAt {
		t.Fatal("region installer changes autostart before the VM exists")
	}
	if !bytes.Equal(host, hyperVHostScript) {
		t.Fatal("embedded host script does not match aven-hyperv-host.ps1")
	}
}
