package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/store"
)

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
	server := strings.ReplaceAll(strings.TrimRight(s.PublicURL, "/"), "'", "''")
	command := fmt.Sprintf("Invoke-WebRequest https://github.com/turin-dev/aven/releases/latest/download/aven-hyperv-host.ps1 -OutFile .\\aven-hyperv-host.ps1 -UseBasicParsing -ErrorAction Stop; powershell -ExecutionPolicy Bypass -File .\\aven-hyperv-host.ps1 -Server '%s' -NodeId '%s' -Token '%s' -VmName '%s'",
		server, nodeID, token, strings.ReplaceAll(body.VMName, "'", "''"))
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
	writeJSON(w, http.StatusAccepted, cmd)
}
