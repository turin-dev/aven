package agentprotocol

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// pair는 hub에 등록된 서버 쪽 연결과 agent 쪽 연결을 만든다. 서버 쪽 읽기 루프는 Deliver를 부른다.
type pair struct {
	agent *websocket.Conn
	sess  *Session
	done  chan struct{}
}

func connectPair(t *testing.T, h *Hub, nodeID string) *pair {
	t.Helper()
	ready := make(chan *Session, 1)
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer close(done)
		defer c.CloseNow()
		sess, _ := h.Register(nodeID, c)
		defer sess.Close()
		ready <- sess
		for {
			_, b, err := c.Read(context.Background())
			if err != nil {
				return
			}
			var m Message
			if json.Unmarshal(b, &m) == nil {
				h.Deliver(nodeID, m)
			}
		}
	}))
	t.Cleanup(srv.Close)
	a, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.CloseNow() })
	return &pair{agent: a, sess: <-ready, done: done}
}

func readMsg(t *testing.T, c *websocket.Conn) Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var m Message
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func writeMsg(t *testing.T, c *websocket.Conn, m Message) {
	t.Helper()
	b, _ := json.Marshal(m)
	if err := c.Write(context.Background(), websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

func TestCallRoundTripAndStream(t *testing.T) {
	h := &Hub{}
	p := connectPair(t, h, "nod_a")
	go func() {
		ctx := context.Background()
		_, b, err := p.agent.Read(ctx)
		if err != nil {
			return
		}
		var m Message
		json.Unmarshal(b, &m)
		for _, l := range []string{"one", "two"} {
			pl, _ := json.Marshal(LineChunk{Line: l})
			out, _ := json.Marshal(Message{ID: m.ID, Operation: m.Operation, Payload: pl, Stream: true})
			p.agent.Write(ctx, websocket.MessageText, out)
		}
		out, _ := json.Marshal(Message{ID: m.ID, Operation: m.Operation, Payload: json.RawMessage(`{"ok":true,"detail":"fine"}`)})
		p.agent.Write(ctx, websocket.MessageText, out)
	}()
	var lines []string
	var res ProbeResult
	err := h.CallStream(context.Background(), "nod_a", OpContainerProbe, ProbeRequest{Name: "keel-x"}, &res, func(b json.RawMessage) {
		var c LineChunk
		json.Unmarshal(b, &c)
		lines = append(lines, c.Line)
	})
	if err != nil || !res.OK || res.Detail != "fine" || strings.Join(lines, ",") != "one,two" {
		t.Fatalf("call: err=%v res=%+v lines=%v", err, res, lines)
	}
}

func TestCallReturnsErrOfflineWhenConnectionCloses(t *testing.T) {
	h := &Hub{}
	p := connectPair(t, h, "nod_a")
	errc := make(chan error, 1)
	go func() { errc <- h.Call(context.Background(), "nod_a", OpPing, struct{}{}, nil) }()
	readMsg(t, p.agent) // 요청은 도착했지만 응답하지 않고 끊는다
	p.agent.CloseNow()
	select {
	case err := <-errc:
		if !errors.Is(err, ErrOffline) {
			t.Fatalf("want ErrOffline, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Call did not return after the connection closed")
	}
	if err := h.Call(context.Background(), "nod_a", OpPing, struct{}{}, nil); !errors.Is(err, ErrOffline) {
		t.Fatalf("call to disconnected node: %v", err)
	}
}

func TestCallIsBoundedAndSendsCancel(t *testing.T) {
	h := &Hub{}
	p := connectPair(t, h, "nod_a")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- h.Call(ctx, "nod_a", OpContainerLogs, LogsRequest{Name: "keel-x", Follow: true}, nil) }()
	req := readMsg(t, p.agent)
	select {
	case err := <-errc:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("want deadline exceeded, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Call not bounded by ctx")
	}
	c := readMsg(t, p.agent)
	var cr CancelRequest
	json.Unmarshal(c.Payload, &cr)
	if c.Operation != OpCancel || cr.ID != req.ID {
		t.Fatalf("expected cancel for %s, got %+v", req.ID, c)
	}
}

func TestCallWithoutDeadlineGetsDefault(t *testing.T) {
	// ctx에 deadline이 없어도 Call은 DefaultCallTimeout 뒤에 끝난다(무한 대기 금지).
	old := DefaultCallTimeout
	DefaultCallTimeout = 150 * time.Millisecond
	defer func() { DefaultCallTimeout = old }()
	h := &Hub{}
	p := connectPair(t, h, "nod_a")
	go p.agent.Read(context.Background()) // 요청은 받지만 응답하지 않는다
	err := h.Call(context.Background(), "nod_a", OpPing, struct{}{}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
}

func TestNewerConnectionSupersedesAndStatusIsNotOverwritten(t *testing.T) {
	h := &Hub{}
	var mu sync.Mutex
	var writes []bool
	record := func() {
		h.SetStatus("nod_a", func(online bool) { mu.Lock(); writes = append(writes, online); mu.Unlock() })
	}
	old := connectPair(t, h, "nod_a")
	record()
	fresh := connectPair(t, h, "nod_a") // 이전 연결을 대신한다
	record()
	select {
	case <-old.sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("old session not closed on supersede")
	}
	// 이전 연결의 정리(늦게 끝남): Close는 새 연결을 지우지 않고, 상태 기록은 online이어야 한다.
	old.sess.Close()
	record()
	if !h.Online("nod_a") {
		t.Fatal("new connection was removed by old session cleanup")
	}
	mu.Lock()
	last := writes[len(writes)-1]
	mu.Unlock()
	if !last {
		t.Fatalf("stale disconnect overwrote online status: %v", writes)
	}
	fresh.agent.CloseNow()
	select {
	case <-fresh.done:
	case <-time.After(5 * time.Second):
		t.Fatal("fresh handler did not exit")
	}
	record()
	mu.Lock()
	last = writes[len(writes)-1]
	mu.Unlock()
	if last || h.Online("nod_a") {
		t.Fatalf("expected offline after last connection closed: %v", writes)
	}
}

func TestPendingLimit(t *testing.T) {
	h := &Hub{}
	connectPair(t, h, "nod_a")
	h.mu.Lock()
	c := h.nodes["nod_a"]
	h.mu.Unlock()
	c.mu.Lock()
	for i := 0; i < MaxPending; i++ {
		c.pending[string(rune('a'+i%26))+strings.Repeat("x", i)] = &pending{final: make(chan Message, 1)}
	}
	c.mu.Unlock()
	err := h.Call(context.Background(), "nod_a", OpPing, struct{}{}, nil)
	if err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("expected in-flight limit error, got %v", err)
	}
}

func TestAgentRequestsAreRoutedToOnRequestAndRepliedTo(t *testing.T) {
	h := &Hub{}
	got := make(chan Message, 1)
	h.OnRequest = func(nodeID string, m Message) {
		got <- m
		h.Reply(context.Background(), nodeID, Message{ID: m.ID, Operation: m.Operation, Payload: json.RawMessage(`{"ok":true}`)})
	}
	p := connectPair(t, h, "nod_a")
	// agent가 보낸 Up 메시지는 pending 응답으로 오인되지 않고 OnRequest로 간다.
	b, _ := json.Marshal(Message{ID: "req1", Operation: OpWake, Payload: json.RawMessage(`{"host":"a.example.com"}`), Up: true})
	if err := p.agent.Write(context.Background(), websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		if m.Operation != OpWake || m.ID != "req1" {
			t.Fatalf("request: %+v", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OnRequest not called")
	}
	reply := readMsg(t, p.agent)
	if reply.ID != "req1" || !reply.Reply || reply.Up || string(reply.Payload) != `{"ok":true}` {
		t.Fatalf("reply: %+v", reply)
	}
	// 연결이 없는 노드에는 답할 수 없다.
	if err := h.Reply(context.Background(), "nod_none", Message{ID: "x"}); !errors.Is(err, ErrOffline) {
		t.Fatalf("reply to offline node: %v", err)
	}
	// OnRequest가 없으면 Up 메시지는 조용히 버려진다.
	(&Hub{}).Deliver("nod_a", Message{ID: "x", Up: true})
}
