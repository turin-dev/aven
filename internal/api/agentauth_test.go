package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/turin-dev/aven/internal/agentpki"
	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/store"
)

type authEnv struct {
	t      *testing.T
	s      *Server
	st     *store.Store
	hub    *agentprotocol.Hub
	ts     *httptest.Server
	client *http.Client
	wsURL  string
}

func newAuthEnv(t *testing.T) *authEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ca, err := agentpki.NewCA()
	if err != nil {
		t.Fatal(err)
	}
	hub := &agentprotocol.Hub{}
	s := &Server{Store: st, Agents: hub, CA: ca, Deploy: &deploy.Manager{Store: st, Docker: testDocker(), Cfg: deploy.Config{DataDir: t.TempDir()}},
		APIToken: "admin-test-token", AdminPassword: "password123", SessionKey: make([]byte, 32), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ts := httptest.NewTLSServer(s.Handler())
	t.Cleanup(ts.Close)
	s.PublicURL = ts.URL
	return &authEnv{t: t, s: s, st: st, hub: hub, ts: ts, client: ts.Client(), wsURL: "wss" + strings.TrimPrefix(ts.URL, "https") + "/api/agent/connect"}
}

func (e *authEnv) post(path string, body any, admin bool) (int, map[string]any) {
	e.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", e.ts.URL+path, bytes.NewReader(b))
	if admin {
		req.Header.Set("Authorization", "Bearer admin-test-token")
	}
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (e *authEnv) joinToken(name string) string {
	e.t.Helper()
	code, out := e.post("/api/nodes/join-tokens", map[string]any{"name": name, "ttl_s": 600}, true)
	if code != 201 {
		e.t.Fatalf("join token HTTP %d", code)
	}
	return out["token"].(string)
}

type testAgent struct {
	nodeID string
	priv   ed25519.PrivateKey
	cert   string
}

func (e *authEnv) enroll(name string) *testAgent {
	e.t.Helper()
	priv, _, _ := agentpki.GenerateKey()
	code, out := e.post("/api/agent/enroll", map[string]any{"token": e.joinToken(name), "public_key": agentpki.EncodePublicKey(priv.Public().(ed25519.PublicKey))}, false)
	if code != 200 || out["certificate"] == nil || out["secret"] != nil {
		e.t.Fatalf("enroll: %d %v", code, out)
	}
	return &testAgent{nodeID: out["node_id"].(string), priv: priv, cert: out["certificate"].(string)}
}

// handshake는 agent 쪽 챌린지 응답이다. audience/nonce/서명키를 바꿔 공격을 흉내 낼 수 있다.
func (e *authEnv) connect(ctx context.Context, a *testAgent, cert, audience string, key ed25519.PrivateKey) (*websocket.Conn, error) {
	conn, _, err := websocket.Dial(ctx, e.wsURL, &websocket.DialOptions{HTTPClient: e.client})
	if err != nil {
		return nil, err
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	var m agentprotocol.Message
	var ch agentprotocol.AuthChallenge
	json.Unmarshal(data, &m)
	json.Unmarshal(m.Payload, &ch)
	if m.Operation != agentprotocol.OpAuthChallenge || ch.Nonce == "" {
		e.t.Fatalf("no challenge: %s", data)
	}
	p, _ := json.Marshal(agentprotocol.AuthResponse{Certificate: cert, Signature: agentpki.Sign(key, a.nodeID, audience, ch.Nonce)})
	out, _ := json.Marshal(agentprotocol.Message{Operation: agentprotocol.OpAuthResponse, Payload: p})
	if err := conn.Write(ctx, websocket.MessageText, out); err != nil {
		return nil, err
	}
	_, data, err = conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	json.Unmarshal(data, &m)
	if m.Operation != agentprotocol.OpAuthOK {
		conn.CloseNow()
		return nil, io.ErrUnexpectedEOF
	}
	return conn, nil
}

func (e *authEnv) host() string {
	return agentpki.NormalizeHost(strings.TrimPrefix(e.ts.URL, "https://"))
}

func waitOnline(t *testing.T, hub *agentprotocol.Hub, id string, want bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if hub.Online(id) == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("node online=%v, want %v", hub.Online(id), want)
}

func TestCertificateConnectionAndRevocation(t *testing.T) {
	e := newAuthEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a := e.enroll("cert-node")

	// 정상 연결 + 요청/응답
	conn, err := e.connect(ctx, a, a.cert, e.host(), a.priv)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	waitOnline(t, e.hub, a.nodeID, true)

	// 인증서를 훔쳐도(공개 정보) 개인키가 없으면 안 된다.
	stolenKey, _, _ := agentpki.GenerateKey()
	if c, err := e.connect(ctx, a, a.cert, e.host(), stolenKey); err == nil {
		c.CloseNow()
		t.Error("connection accepted with a signature from a different key")
	}
	// 다른 서버(audience)용 서명은 중계할 수 없다.
	if c, err := e.connect(ctx, a, a.cert, "evil.example.com", a.priv); err == nil {
		c.CloseNow()
		t.Error("signature for another audience accepted")
	}
	// 다른 CA가 발급한 인증서
	otherCA, _ := agentpki.NewCA()
	forged, _ := otherCA.Issue(a.nodeID, a.priv.Public().(ed25519.PublicKey))
	if c, err := e.connect(ctx, a, string(forged.CertPEM), e.host(), a.priv); err == nil {
		c.CloseNow()
		t.Error("certificate from a foreign CA accepted")
	}
	// CA가 서명했지만 발급 기록이 없는 인증서(서버가 DB를 잃었거나 CA 키 유출 후 위조)
	unknown, _ := e.s.CA.Issue(a.nodeID, a.priv.Public().(ed25519.PublicKey))
	if c, err := e.connect(ctx, a, string(unknown.CertPEM), e.host(), a.priv); err == nil {
		c.CloseNow()
		t.Error("certificate without an issuance record accepted")
	}
	// Bearer 헤더만으로는 인증서 노드에 접속할 수 없다.
	bad, resp, err := websocket.Dial(ctx, e.wsURL, &websocket.DialOptions{HTTPClient: e.client, HTTPHeader: http.Header{"Authorization": []string{"Bearer " + a.nodeID + ":" + strings.Repeat("a", 64)}}})
	if err == nil || resp == nil || resp.StatusCode != 401 {
		if bad != nil {
			bad.CloseNow()
		}
		t.Errorf("bearer header accepted for a certificate node: %v %v", resp, err)
	}

	// 폐기하면 연결이 끊기고 같은 인증서로 다시 접속할 수 없다.
	if err := e.s.revokeNodeAccess(ctx, a.nodeID); err != nil {
		t.Fatal(err)
	}
	waitOnline(t, e.hub, a.nodeID, false)
	if c, err := e.connect(ctx, a, a.cert, e.host(), a.priv); err == nil {
		c.CloseNow()
		t.Error("revoked certificate accepted")
	}
}

func TestNodeDeleteRevokesCertificate(t *testing.T) {
	e := newAuthEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a := e.enroll("to-delete")
	req, _ := http.NewRequest("DELETE", e.ts.URL+"/api/nodes/"+a.nodeID, nil)
	req.Header.Set("Authorization", "Bearer admin-test-token")
	resp, err := e.client.Do(req)
	if err != nil || resp.StatusCode != 204 {
		t.Fatalf("delete node: %v %v", resp, err)
	}
	resp.Body.Close()
	if c, err := e.connect(ctx, a, a.cert, e.host(), a.priv); err == nil {
		c.CloseNow()
		t.Error("deleted node's certificate accepted")
	}
}

func TestLegacyAgentUpgradesTransparently(t *testing.T) {
	e := newAuthEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// 옛 agent: public_key 없이 enroll → Bearer 비밀키
	code, out := e.post("/api/agent/enroll", map[string]any{"token": e.joinToken("legacy")}, false)
	if code != 200 || out["secret"] == nil || out["certificate"] != nil {
		t.Fatalf("legacy enroll: %d %v", code, out)
	}
	id, secret := out["node_id"].(string), out["secret"].(string)
	bearer := http.Header{"Authorization": []string{"Bearer " + id + ":" + secret}}
	conn, _, err := websocket.Dial(ctx, e.wsURL, &websocket.DialOptions{HTTPClient: e.client, HTTPHeader: bearer})
	if err != nil {
		t.Fatalf("legacy bearer connect: %v", err)
	}
	conn.CloseNow()
	waitOnline(t, e.hub, id, false)

	// 업그레이드: 잘못된 비밀키는 거부
	priv, _, _ := agentpki.GenerateKey()
	pk := agentpki.EncodePublicKey(priv.Public().(ed25519.PublicKey))
	if code, _ := e.post("/api/agent/upgrade", map[string]any{"node_id": id, "secret": strings.Repeat("0", 64), "public_key": pk}, false); code != 401 {
		t.Errorf("upgrade with a wrong secret: %d", code)
	}
	code, out = e.post("/api/agent/upgrade", map[string]any{"node_id": id, "secret": secret, "public_key": pk}, false)
	if code != 200 || out["certificate"] == nil || out["node_id"] != id {
		t.Fatalf("upgrade: %d %v", code, out)
	}
	a := &testAgent{nodeID: id, priv: priv, cert: out["certificate"].(string)}
	// 인증서로 첫 연결에 성공하기 전에는 비밀키도 아직 유효하다(응답을 못 받은 경우 대비).
	if c, _, err := websocket.Dial(ctx, e.wsURL, &websocket.DialOptions{HTTPClient: e.client, HTTPHeader: bearer}); err != nil {
		t.Errorf("bearer rejected before the first certificate connection: %v", err)
	} else {
		c.CloseNow()
		waitOnline(t, e.hub, id, false)
	}
	conn, err = e.connect(ctx, a, a.cert, e.host(), a.priv)
	if err != nil {
		t.Fatal(err)
	}
	waitOnline(t, e.hub, id, true)
	conn.CloseNow()
	waitOnline(t, e.hub, id, false)
	// 첫 인증서 연결 뒤에는 Bearer 비밀키가 폐기된다(다운그레이드 불가).
	bad, resp, err := websocket.Dial(ctx, e.wsURL, &websocket.DialOptions{HTTPClient: e.client, HTTPHeader: bearer})
	if err == nil || resp == nil || resp.StatusCode != 401 {
		if bad != nil {
			bad.CloseNow()
		}
		t.Errorf("legacy bearer still accepted after the certificate connection: %v %v", resp, err)
	}
	if code, _ := e.post("/api/agent/upgrade", map[string]any{"node_id": id, "secret": secret, "public_key": pk}, false); code != 401 {
		t.Errorf("retired secret can still upgrade: %d", code)
	}
}

func TestLegacyBearerDisabled(t *testing.T) {
	e := newAuthEnv(t)
	e.s.DisableLegacyAgents = true
	code, _ := e.post("/api/agent/enroll", map[string]any{"token": e.joinToken("x")}, false)
	if code != 400 {
		t.Errorf("legacy enroll with legacy disabled: %d", code)
	}
}

func TestCertRenewalPushedOverConnection(t *testing.T) {
	e := newAuthEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a := e.enroll("renew")
	// 만료 임박한 인증서를 직접 발급해 기록한다.
	old, err := e.s.CA.IssueFor(a.nodeID, a.priv.Public().(ed25519.PublicKey), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.RecordNodeCert(ctx, old.Serial, a.nodeID, old.NotAfter); err != nil {
		t.Fatal(err)
	}
	conn, err := e.connect(ctx, a, string(old.CertPEM), e.host(), a.priv)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	// 서버는 heartbeat 주기에 맞춰 갱신을 밀어 준다(테스트에서는 즉시 호출).
	waitOnline(t, e.hub, a.nodeID, true)
	id := &agentIdentity{nodeID: a.nodeID, notAfter: old.NotAfter, serial: old.Serial, pub: a.priv.Public().(ed25519.PublicKey)}
	sess := e.hub.SessionFor(a.nodeID)
	if sess == nil {
		t.Fatal("no session")
	}
	e.s.renewCert(ctx, sess, id)
	var m agentprotocol.Message
	var r agentprotocol.CertRenew
	for m.Operation != agentprotocol.OpCertRenew {
		_, data, err := conn.Read(ctx) // 연결 직후 서버가 보내는 ping 요청은 건너뛴다
		if err != nil {
			t.Fatal(err)
		}
		m = agentprotocol.Message{}
		json.Unmarshal(data, &m)
	}
	json.Unmarshal(m.Payload, &r)
	v, err := e.s.CA.Verify([]byte(r.Certificate), time.Now())
	if err != nil || v.NodeID != a.nodeID || agentpki.NeedsRenewal(v.NotAfter, time.Now()) {
		t.Fatalf("renewed certificate: %+v %v", v, err)
	}
	if !e.st.NodeCertActive(ctx, v.Serial, a.nodeID) {
		t.Error("renewed certificate not recorded")
	}
}
