package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/turin-dev/aven/internal/agentpki"
	"github.com/turin-dev/aven/internal/agentprotocol"
)

// Agent 인증 요약 (자세한 내용은 docs/api-v1.md):
//   enroll   : join token + agent가 만든 공개키 → aven CA가 서명한 인증서(node ID 바인딩, 30일)
//   upgrade  : 옛 Bearer 비밀키 + 새 공개키 → 인증서 (비밀키는 인증서로 첫 연결에 성공하면 폐기)
//   connect  : WebSocket 안에서 서버 nonce에 개인키로 서명 → 키 소유 증명. 연결 중 만료 10일 전부터 새 인증서를 밀어준다.
//   revoke   : 노드 삭제 시 인증서를 모두 폐기하고 연결을 끊는다.

const (
	handshakeTimeout   = 10 * time.Second
	maxHandshakes      = 64 // 인증 전 WebSocket 동시 수
	certCheckInterval  = time.Hour
	legacyAgentWarning = "agent uses the legacy bearer secret; update aven-agent so it upgrades to a certificate"
)

var handshakeSlots = make(chan struct{}, maxHandshakes)

func (s *Server) agentCA(w http.ResponseWriter) bool {
	if s.CA == nil {
		writeErr(w, 503, "agent CA unavailable")
		return false
	}
	return true
}

type enrollResponse struct {
	NodeID      string    `json:"node_id"`
	Certificate string    `json:"certificate"`
	CA          string    `json:"ca"`
	ExpiresAt   time.Time `json:"expires_at"`
	Secret      string    `json:"secret,omitempty"` // 공개키 없이 enroll한 옛 agent에게만
}

func (s *Server) enrollAgent(w http.ResponseWriter, r *http.Request) {
	if !s.secureTransport(r) {
		writeErr(w, 403, tlsRequired)
		return
	}
	key := limitKey(s.clientIP(r))
	if wait := s.lim().agentIP.wait(key); wait > 0 {
		tooMany(w, wait, "인증 시도가 너무 많습니다")
		return
	}
	var body struct {
		Token     string `json:"token"`
		Name      string `json:"name"`
		PublicKey string `json:"public_key"`
	}
	if !decode(w, r, &body) {
		return
	}
	if len(body.Token) != 64 {
		s.lim().agentIP.fail(key)
		writeErr(w, 401, "invalid join token")
		return
	}
	if body.PublicKey == "" { // 옛 agent: Bearer 비밀키 방식
		if s.DisableLegacyAgents {
			writeErr(w, 400, "public_key required (legacy bearer enrollment is disabled)")
			return
		}
		secret, err := agentprotocol.Secret()
		if err != nil {
			s.storeErr(w, err)
			return
		}
		n, err := s.Store.ConsumeJoinToken(r.Context(), agentprotocol.Hash(body.Token), agentprotocol.Hash(secret))
		if err != nil {
			s.lim().agentIP.fail(key)
			writeErr(w, 401, "invalid or expired join token")
			return
		}
		writeJSON(w, 200, map[string]string{"node_id": n.ID, "secret": secret})
		return
	}
	if !s.agentCA(w) {
		return
	}
	pub, err := agentpki.DecodePublicKey(body.PublicKey)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	var issued *agentpki.Issued
	n, err := s.Store.ConsumeJoinTokenForCert(r.Context(), agentprotocol.Hash(body.Token), func(id string) (string, time.Time, error) {
		var err error
		issued, err = s.CA.Issue(id, pub)
		if err != nil {
			return "", time.Time{}, err
		}
		return issued.Serial, issued.NotAfter, nil
	})
	if err != nil {
		s.lim().agentIP.fail(key)
		writeErr(w, 401, "invalid or expired join token")
		return
	}
	s.Log.Info("agent enrolled", "node", n.ID, "cert_serial", issued.Serial)
	writeJSON(w, 200, enrollResponse{NodeID: n.ID, Certificate: string(issued.CertPEM), CA: string(s.CA.PEM), ExpiresAt: issued.NotAfter})
}

// upgradeAgent: 옛 Bearer 비밀키를 가진 agent가 새 공개키를 내고 인증서를 받는다. 비밀키는 이 시점에는 아직 유효하며,
// 인증서로 첫 연결에 성공해야 폐기된다(응답을 못 받아도 노드가 잠기지 않게).
func (s *Server) upgradeAgent(w http.ResponseWriter, r *http.Request) {
	if !s.secureTransport(r) {
		writeErr(w, 403, tlsRequired)
		return
	}
	key := limitKey(s.clientIP(r))
	if wait := s.lim().agentIP.wait(key); wait > 0 {
		tooMany(w, wait, "인증 시도가 너무 많습니다")
		return
	}
	var body struct {
		NodeID    string `json:"node_id"`
		Secret    string `json:"secret"`
		PublicKey string `json:"public_key"`
	}
	if !decode(w, r, &body) {
		return
	}
	if len(body.Secret) != 64 || body.NodeID == "" || s.Store.VerifyNodeCredential(r.Context(), body.NodeID, agentprotocol.Hash(body.Secret)) != nil {
		s.lim().agentIP.fail(key)
		writeErr(w, 401, "invalid node credentials")
		return
	}
	if !s.agentCA(w) {
		return
	}
	pub, err := agentpki.DecodePublicKey(body.PublicKey)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	issued, err := s.CA.Issue(body.NodeID, pub)
	if err == nil {
		err = s.Store.RecordNodeCert(r.Context(), issued.Serial, body.NodeID, issued.NotAfter)
	}
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.Log.Info("agent upgraded to certificate", "node", body.NodeID, "cert_serial", issued.Serial)
	writeJSON(w, 200, enrollResponse{NodeID: body.NodeID, Certificate: string(issued.CertPEM), CA: string(s.CA.PEM), ExpiresAt: issued.NotAfter})
}

func wsSend(ctx context.Context, conn *websocket.Conn, op string, payload any) error {
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		raw = b
	}
	data, _ := json.Marshal(agentprotocol.Message{Operation: op, Payload: raw})
	return conn.Write(ctx, websocket.MessageText, data)
}

type agentIdentity struct {
	nodeID   string
	notAfter time.Time // 인증서 방식일 때만
	serial   string
	pub      ed25519.PublicKey
	legacy   bool
}

// authenticateCert는 WebSocket 안에서 nonce 챌린지로 키 소유를 증명받는다.
func (s *Server) authenticateCert(ctx context.Context, conn *websocket.Conn, host string) (*agentIdentity, string) {
	ctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	nonce, err := agentpki.NewNonce()
	if err != nil {
		return nil, "internal"
	}
	if err := wsSend(ctx, conn, agentprotocol.OpAuthChallenge, agentprotocol.AuthChallenge{Nonce: nonce}); err != nil {
		return nil, "challenge write"
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		return nil, "no response"
	}
	var msg agentprotocol.Message
	var resp agentprotocol.AuthResponse
	if json.Unmarshal(data, &msg) != nil || msg.Operation != agentprotocol.OpAuthResponse || json.Unmarshal(msg.Payload, &resp) != nil {
		return nil, "malformed response"
	}
	v, err := s.CA.Verify([]byte(resp.Certificate), time.Now())
	if err != nil {
		return nil, "certificate rejected"
	}
	if !agentpki.VerifySignature(v.PublicKey, v.NodeID, host, nonce, resp.Signature) {
		return nil, "bad signature"
	}
	if !s.Store.NodeCertActive(ctx, v.Serial, v.NodeID) {
		// 키 소유까지 증명한 폐기/삭제된 노드: 설정 오류이지 공격이 아니므로 같은 IP의 다른 노드를 잠그지 않게 실패 횟수에 넣지 않는다.
		return nil, "revoked: certificate revoked or unknown"
	}
	return &agentIdentity{nodeID: v.NodeID, notAfter: v.NotAfter, serial: v.Serial, pub: v.PublicKey}, ""
}

// renewCert는 인증서가 만료에 가까우면 같은 공개키로 새로 발급해 연결을 통해 보낸다.
func (s *Server) renewCert(ctx context.Context, sess *agentprotocol.Session, id *agentIdentity) {
	issued, err := s.CA.Issue(id.nodeID, id.pub)
	if err == nil {
		err = s.Store.RecordNodeCert(ctx, issued.Serial, id.nodeID, issued.NotAfter)
	}
	if err == nil {
		var payload json.RawMessage
		payload, err = json.Marshal(agentprotocol.CertRenew{Certificate: string(issued.CertPEM)})
		if err == nil {
			err = sess.Send(ctx, agentprotocol.Message{Operation: agentprotocol.OpCertRenew, Payload: payload})
		}
	}
	if err != nil {
		s.Log.Warn("agent certificate renewal", "node", id.nodeID, "err", err)
		return
	}
	id.notAfter, id.serial = issued.NotAfter, issued.Serial
	s.Log.Info("agent certificate renewed", "node", id.nodeID, "cert_serial", issued.Serial, "expires", issued.NotAfter.Format(time.RFC3339))
}

func (s *Server) connectAgent(w http.ResponseWriter, r *http.Request) {
	if !s.secureTransport(r) {
		writeErr(w, 403, tlsRequired)
		return
	}
	if s.Agents == nil {
		writeErr(w, 503, "agent hub unavailable")
		return
	}
	key := limitKey(s.clientIP(r))
	if wait := s.lim().agentIP.wait(key); wait > 0 {
		tooMany(w, wait, "인증 시도가 너무 많습니다")
		return
	}
	var id *agentIdentity
	if auth := r.Header.Get("Authorization"); auth != "" {
		// 옛 방식: 업그레이드하기 전의 agent에게만 허용한다(인증서로 한 번이라도 연결한 노드는 비밀키가 폐기되어 거부).
		nodeID, secret, ok := strings.Cut(strings.TrimPrefix(auth, "Bearer "), ":")
		if s.DisableLegacyAgents || !strings.HasPrefix(auth, "Bearer ") || !ok || len(secret) != 64 || nodeID == "" ||
			s.Store.VerifyNodeCredential(r.Context(), nodeID, agentprotocol.Hash(secret)) != nil {
			s.lim().agentIP.fail(key)
			writeErr(w, 401, "invalid node credentials")
			return
		}
		id = &agentIdentity{nodeID: nodeID, legacy: true}
	} else if s.CA == nil {
		writeErr(w, 503, "agent CA unavailable")
		return
	}
	select {
	case handshakeSlots <- struct{}{}:
	default:
		writeErr(w, 503, "too many pending agent connections")
		return
	}
	released := false
	release := func() {
		if !released {
			released = true
			<-handshakeSlots
		}
	}
	defer release()
	host := agentpki.NormalizeHost(r.Host)
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{r.Host}})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(agentprotocol.MaxAuthMessageBytes)
	if id == nil {
		var why string
		id, why = s.authenticateCert(r.Context(), conn, host)
		if id == nil {
			if !strings.HasPrefix(why, "revoked:") {
				s.lim().agentIP.fail(key)
			}
			s.Log.Warn("agent authentication failed", "reason", why, "ip", key)
			conn.Close(websocket.StatusPolicyViolation, "authentication failed")
			return
		}
		wctx, cancel := context.WithTimeout(r.Context(), handshakeTimeout)
		err := wsSend(wctx, conn, agentprotocol.OpAuthOK, nil)
		cancel()
		if err != nil {
			return
		}
	}
	nodeID := id.nodeID
	conn.SetReadLimit(agentprotocol.MaxMessageBytes)
	sess, err := s.Agents.Register(nodeID, conn)
	if err != nil {
		conn.Close(websocket.StatusPolicyViolation, "registration failed")
		return
	}
	release()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	s.lim().agentIP.reset(key)
	s.recordNodeStatus(nodeID)
	defer func() {
		sess.Close()
		s.recordNodeStatus(nodeID)
		s.Log.Info("agent disconnected", "node", nodeID)
	}()
	if id.legacy {
		s.Log.Warn(legacyAgentWarning, "node", nodeID)
	} else {
		if err := s.Store.RetireLegacyCredential(ctx, nodeID); err != nil {
			s.Log.Warn("retire legacy credential", "node", nodeID, "err", err)
		}
	}
	s.Log.Info("agent connected", "node", nodeID, "auth", map[bool]string{true: "bearer", false: "certificate"}[id.legacy])
	go s.refreshNodeInfo(ctx, nodeID)
	go func() {
		t := time.NewTicker(agentHeartbeat)
		defer t.Stop()
		lastCert := time.Time{}
		for {
			select {
			case <-ctx.Done():
				return
			case <-sess.Done():
				conn.CloseNow()
				return
			case <-t.C:
				pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Ping(pctx)
				pcancel()
				if err != nil && ctx.Err() == nil {
					s.Log.Warn("agent heartbeat failed", "node", nodeID, "err", err)
					conn.CloseNow()
					return
				}
				if !id.legacy && time.Since(lastCert) >= certCheckInterval {
					lastCert = time.Now()
					if !time.Now().Before(id.notAfter) || !s.Store.NodeCertActive(ctx, id.serial, nodeID) {
						s.Log.Warn("agent certificate expired or revoked while connected; closing", "node", nodeID)
						conn.Close(websocket.StatusPolicyViolation, "certificate expired or revoked")
						return
					}
					if agentpki.NeedsRenewal(id.notAfter, time.Now()) {
						s.renewCert(ctx, sess, id)
					}
				}
			}
		}
	}()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var msg agentprotocol.Message
		if json.Unmarshal(data, &msg) == nil {
			s.Agents.Deliver(nodeID, msg)
		}
	}
}

// revokeNodeAccess는 노드의 인증서와 옛 비밀키를 폐기하고 열려 있는 연결을 끊는다(노드 삭제용).
func (s *Server) revokeNodeAccess(ctx context.Context, nodeID string) error {
	if err := s.Store.RevokeNodeCerts(ctx, nodeID); err != nil {
		return err
	}
	if s.Agents != nil {
		s.Agents.Disconnect(nodeID)
	}
	return nil
}
