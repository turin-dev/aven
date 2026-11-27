package api

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/store"
)

// hyperVHostScript is the host controller bundled with this server build.
// The repo-root aven-hyperv-host.ps1 is the release copy and must stay identical.
//
//go:embed aven-hyperv-host.ps1
var hyperVHostScript []byte

const hyperVHostScriptName = "aven-hyperv-host.ps1"

func hyperVHostScriptSHA256() string {
	sum := sha256.Sum256(hyperVHostScript)
	return hex.EncodeToString(sum[:])
}

func hyperVConnectCommand(publicURL, nodeID, token, vmName string) string {
	server := strings.ReplaceAll(strings.TrimRight(publicURL, "/"), "'", "''")
	vm := strings.ReplaceAll(vmName, "'", "''")
	return fmt.Sprintf("$p='.\\aven-hyperv-host.ps1'; Invoke-WebRequest '%s/downloads/aven-hyperv-host.ps1' -OutFile $p -UseBasicParsing -ErrorAction Stop; if ((Get-FileHash -Algorithm SHA256 -LiteralPath $p).Hash -ne '%s') { Remove-Item -Force -LiteralPath $p; throw 'aven-hyperv-host.ps1 failed its SHA-256 check' }; powershell -ExecutionPolicy Bypass -File $p -Server '%s' -NodeId '%s' -Token '%s' -VmName '%s'",
		server, strings.ToUpper(hyperVHostScriptSHA256()), server, nodeID, token, vm)
}

func (s *Server) serveHyperVHostScript(w http.ResponseWriter, r *http.Request, checksum bool) {
	sum := hyperVHostScriptSHA256()
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if s.Version != "" {
		w.Header().Set("X-Aven-Version", s.Version)
	}
	if checksum {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "%s  %s\n", sum, hyperVHostScriptName)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="aven-hyperv-host.ps1"`)
	http.ServeContent(w, r, hyperVHostScriptName, time.Time{}, bytes.NewReader(hyperVHostScript))
}

var hyperVNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type hyperVNodeView struct {
	VMName      string               `json:"vm_name"`
	HostName    string               `json:"host_name"`
	HostOnline  bool                 `json:"host_online"`
	PowerState  string               `json:"power_state"`
	LastSeenAt  *time.Time           `json:"last_seen_at"`
	LastCommand *store.HyperVCommand `json:"last_command,omitempty"`
}

func (s *Server) createHyperVPairToken(w http.ResponseWriter, r *http.Request) {
	if !validPublicURL(s.PublicURL) {
		writeErr(w, 503, "the main server must have an HTTPS public URL")
		return
	}
	nodeID := r.PathValue("nid")
	if nodeID == store.LocalNodeID {
		writeErr(w, 400, "the local node cannot be paired with a Hyper-V host")
		return
	}
	var body struct {
		VMName string `json:"vm_name"`
	}
	if !decode(w, r, &body) {
		return
	}
	body.VMName = strings.TrimSpace(body.VMName)
	if !hyperVNameRe.MatchString(body.VMName) {
		writeErr(w, 400, "vm_name must contain 1..64 letters, digits, dots, underscores or hyphens")
		return
	}
	token, err := agentprotocol.Secret()
	if err != nil {
		s.storeErr(w, err)
		return
	}
	expires := time.Now().UTC().Add(15 * time.Minute)
	if err := s.Store.CreateHyperVPairToken(r.Context(), nodeID, agentprotocol.Hash(token), body.VMName, expires); err != nil {
		s.storeErr(w, err)
		return
	}
	command := hyperVConnectCommand(s.PublicURL, nodeID, token, body.VMName)
	s.audit(r, "hyperv.pair_token", nodeID, "Hyper-V 호스트 연결 토큰을 발급했습니다")
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "expires_at": expires, "command": command})
}

func (s *Server) enrollHyperVHost(w http.ResponseWriter, r *http.Request) {
	if !s.secureTransport(r) {
		writeErr(w, 403, tlsRequired)
		return
	}
	key := limitKey(s.clientIP(r))
	if wait := s.lim().agentIP.wait(key); wait > 0 {
		tooMany(w, wait, "too many Hyper-V enrollment attempts")
		return
	}
	var body struct {
		Token    string `json:"token"`
		HostName string `json:"host_name"`
	}
	if !decode(w, r, &body) {
		return
	}
	if len(body.Token) != 64 {
		s.lim().agentIP.fail(key)
		writeErr(w, 401, "invalid or expired Hyper-V pairing token")
		return
	}
	body.HostName = strings.TrimSpace(body.HostName)
	if body.HostName == "" || len(body.HostName) > 255 || strings.ContainsAny(body.HostName, "\r\n\x00") {
		writeErr(w, 400, "invalid host_name")
		return
	}
	credential, err := agentprotocol.Secret()
	if err != nil {
		s.storeErr(w, err)
		return
	}
	nodeID, vmName, err := s.Store.ConsumeHyperVPairToken(r.Context(), agentprotocol.Hash(body.Token), agentprotocol.Hash(credential), body.HostName)
	if err != nil {
		s.lim().agentIP.fail(key)
		writeErr(w, 401, "invalid or expired Hyper-V pairing token")
		return
	}
	s.Log.Info("Hyper-V host paired", "node", nodeID, "host", body.HostName, "vm", vmName)
	writeJSON(w, http.StatusOK, map[string]string{"node_id": nodeID, "vm_name": vmName, "credential": credential})
}

func (s *Server) hyperVHostID(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !s.secureTransport(r) {
		writeErr(w, 403, tlsRequired)
		return "", false
	}
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) != 64 {
		writeErr(w, 401, "invalid Hyper-V host credential")
		return "", false
	}
	nodeID, err := s.Store.HyperVNodeIDForCredential(r.Context(), agentprotocol.Hash(parts[1]))
	if err != nil {
		writeErr(w, 401, "invalid Hyper-V host credential")
		return "", false
	}
	return nodeID, true
}

func (s *Server) hyperVHostStatus(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := s.hyperVHostID(w, r)
	if !ok {
		return
	}
	var body struct {
		PowerState string `json:"power_state"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := s.Store.SetHyperVHostStatus(r.Context(), nodeID, body.PowerState); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) nextHyperVCommand(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := s.hyperVHostID(w, r)
	if !ok {
		return
	}
	cmd, err := s.Store.ClaimHyperVCommand(r.Context(), nodeID)
	if err == store.ErrNotFound {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cmd)
}

func (s *Server) completeHyperVCommand(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := s.hyperVHostID(w, r)
	if !ok {
		return
	}
	var body struct {
		OK         bool   `json:"ok"`
		Error      string `json:"error"`
		PowerState string `json:"power_state"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.PowerState != "" && body.PowerState != "running" && body.PowerState != "off" && body.PowerState != "paused" && body.PowerState != "unknown" {
		writeErr(w, 400, "invalid Hyper-V power state")
		return
	}
	if err := s.Store.CompleteHyperVCommand(r.Context(), nodeID, r.PathValue("id"), body.OK, body.Error); err != nil {
		s.storeErr(w, err)
		return
	}
	if body.PowerState != "" {
		if err := s.Store.SetHyperVHostStatus(r.Context(), nodeID, body.PowerState); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) requestHyperVPower(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("nid")
	if nodeID == store.LocalNodeID {
		writeErr(w, 400, "the local node cannot be controlled through Hyper-V")
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Action != "start" && body.Action != "stop" {
		writeErr(w, 400, "action must be start or stop")
		return
	}
	hosts, err := s.Store.ListHyperVHosts(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	host, paired := hosts[nodeID]
	if !paired {
		writeErr(w, 409, "node has no paired Hyper-V host")
		return
	}
	if host.LastSeenAt == nil || time.Since(*host.LastSeenAt) > 45*time.Second {
		writeErr(w, 409, "Hyper-V host is offline")
		return
	}
	cmd, err := s.Store.EnqueueHyperVCommand(r.Context(), nodeID, body.Action)
	if err == store.ErrConflict {
		writeErr(w, 409, "a Hyper-V command is already pending")
		return
	}
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "hyperv.power", nodeID, "Hyper-V VM "+body.Action+" 명령을 큐에 넣었습니다")
	writeJSON(w, http.StatusAccepted, cmd)
}

func (s *Server) deleteHyperVHost(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("nid")
	if err := s.Store.DeleteHyperVHost(r.Context(), nodeID); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "hyperv.disconnected", nodeID, "Hyper-V 호스트 연결을 끊었습니다")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) hyperVNodeJSON(r *http.Request, host store.HyperVHost) *hyperVNodeView {
	online := host.LastSeenAt != nil && time.Since(*host.LastSeenAt) <= 45*time.Second
	view := &hyperVNodeView{
		VMName: host.VMName, HostName: host.HostName, HostOnline: online,
		PowerState: host.PowerState, LastSeenAt: host.LastSeenAt, LastCommand: host.LastCommand,
	}
	if s.instanceLevel(principalFrom(r)) >= lvAdmin {
		return view
	}
	view.HostName = ""
	if host.LastCommand != nil {
		cmd := *host.LastCommand
		cmd.Error = ""
		view.LastCommand = &cmd
	}
	return view
}
