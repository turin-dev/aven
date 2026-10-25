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
	"time"

	"github.com/coder/websocket"
	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/store"
)

func TestAgentJoinAndAuthenticatedOutboundConnection(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	hub := &agentprotocol.Hub{}
	s := &Server{Store: st, Agents: hub, Deploy: &deploy.Manager{Store: st, Docker: testDocker(), Cfg: deploy.Config{DataDir: t.TempDir()}}, APIToken: "admin-test-token", AdminPassword: "password123", SessionKey: make([]byte, 32), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	tls := httptest.NewTLSServer(s.Handler())
	defer tls.Close()
	s.PublicURL = tls.URL
	client := tls.Client()
	adminReq, _ := http.NewRequest("POST", tls.URL+"/api/nodes/join-tokens", strings.NewReader(`{"name":"test-node","ttl_s":600}`))
	adminReq.Header.Set("Authorization", "Bearer admin-test-token")
	resp, err := client.Do(adminReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("join HTTP %d", resp.StatusCode)
	}
	var join struct {
		Token   string `json:"token"`
		Command string `json:"command"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&join); err != nil {
		t.Fatal(err)
	}
	wantCmd := "curl -fsSL " + tls.URL + "/install-agent.sh | sudo sh -s -- --server " + tls.URL + " --token '" + join.Token + "'"
	if len(join.Token) != 64 || join.Command != wantCmd {
		t.Fatalf("join command = %q", join.Command)
	}
	enroll := func(token string) *http.Response {
		body, _ := json.Marshal(map[string]string{"token": token})
		resp, err := client.Post(tls.URL+"/api/agent/enroll", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	enrollment := enroll(join.Token)
	if enrollment.StatusCode != 200 {
		t.Fatalf("enroll HTTP %d", enrollment.StatusCode)
	}
	var cred struct {
		NodeID string `json:"node_id"`
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(enrollment.Body).Decode(&cred); err != nil {
		t.Fatal(err)
	}
	enrollment.Body.Close()
	if cred.NodeID == "" || cred.Secret == "" {
		t.Fatalf("empty credentials: %+v", cred)
	}
	again := enroll(join.Token)
	if again.StatusCode != 401 {
		t.Errorf("reused token HTTP %d", again.StatusCode)
	}
	again.Body.Close()
	wsURL := "wss" + strings.TrimPrefix(tls.URL, "https") + "/api/agent/connect"
	bad, badResp, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{HTTPClient: client, HTTPHeader: http.Header{"Authorization": []string{"Bearer " + cred.NodeID + ":wrong"}}})
	if bad != nil {
		bad.CloseNow()
	}
	if err == nil || badResp == nil || badResp.StatusCode != 401 {
		t.Fatalf("bad credentials accepted: %v, %v", badResp, err)
	}
	badResp.Body.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: client, HTTPHeader: http.Header{"Authorization": []string{"Bearer " + cred.NodeID + ":" + cred.Secret}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	// 가짜 agent: 서버는 연결 직후 스스로도 요청을 보낸다(노드 정보 ping, 인증서 갱신 등). 테스트의 ping이 첫 메시지라고
	// 가정하지 않고 모든 요청에 답한다.
	response := make(chan error, 1)
	go func() {
		peer := &agentprotocol.Peer{Conn: conn}
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				response <- err
				return
			}
			var msg agentprotocol.Message
			if err := json.Unmarshal(data, &msg); err != nil || msg.ID == "" || msg.Reply {
				continue
			}
			payload := json.RawMessage(`{}`)
			if msg.Operation == agentprotocol.OpPing {
				payload = json.RawMessage(`{"docker_version":"fake"}`)
			}
			if err := peer.Write(ctx, agentprotocol.Message{ID: msg.ID, Operation: msg.Operation, Payload: payload}); err != nil {
				response <- err
				return
			}
		}
	}()
	var result struct {
		DockerVersion string `json:"docker_version"`
	}
	waitOnline(t, hub, cred.NodeID, true) // Dial이 돌아온 직후에는 hub 등록 전일 수 있다
	if err := hub.Call(ctx, cred.NodeID, "ping", map[string]string{}, &result); err != nil {
		t.Fatal(err)
	}
	if result.DockerVersion != "fake" {
		t.Fatalf("response: %+v", result)
	}
	conn.Close(websocket.StatusNormalClosure, "")
	<-response
}
