package agentprotocol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"
)

var ErrOffline = errors.New("remote node is offline")

// DefaultCallTimeout는 ctx에 deadline이 없을 때 Call 하나에 주는 최대 시간이다.
var DefaultCallTimeout = 2 * time.Minute

const (
	// MaxPending은 연결 하나에 동시에 걸 수 있는 요청 수다.
	MaxPending   = 256
	writeTimeout = 10 * time.Second
)

type pending struct {
	final  chan Message
	stream func(json.RawMessage)
}

type connection struct {
	peer    *Peer
	pending map[string]*pending
	done    chan struct{}
	once    sync.Once
	mu      sync.Mutex
}

func (c *connection) close() { c.once.Do(func() { close(c.done) }) }

// Session은 Register가 돌려주는 연결 하나다.
type Session struct {
	hub    *Hub
	nodeID string
	c      *connection
}

// Done은 이 연결이 hub에서 빠지면(끊김/새 연결로 교체) 닫힌다.
func (s *Session) Done() <-chan struct{} { return s.c.done }

// Send는 이 연결로 요청이 아닌 메시지를 하나 보낸다(예: cert.renew). 다른 쓰기와 직렬화된다.
func (s *Session) Send(ctx context.Context, msg Message) error {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return s.c.peer.Write(ctx, msg)
}

// Close는 이 연결을 hub에서 뺀다. 이미 새 연결로 바뀌었으면 새 연결은 건드리지 않는다.
func (s *Session) Close() {
	s.hub.mu.Lock()
	if s.hub.nodes[s.nodeID] == s.c {
		delete(s.hub.nodes, s.nodeID)
	}
	s.hub.mu.Unlock()
	s.c.close()
}

type Hub struct {
	// OnConnect는 노드가 (다시) 연결되면 별도 goroutine에서 불린다(reconcile 재개 등).
	OnConnect func(nodeID string)
	// OnRequest는 agent가 보낸 요청(Message.Up)이 오면 별도 goroutine에서 불린다. 답은 Reply로 보낸다.
	OnRequest func(nodeID string, msg Message)

	mu       sync.Mutex
	nodes    map[string]*connection
	statusMu map[string]*sync.Mutex
}

// Register는 노드의 새 연결을 등록한다. 같은 노드의 이전 연결이 남아 있으면(반쯤 끊긴 TCP 등) 새 연결이 대신한다.
func (h *Hub) Register(nodeID string, conn *websocket.Conn) (*Session, error) {
	h.mu.Lock()
	if h.nodes == nil {
		h.nodes = map[string]*connection{}
	}
	old := h.nodes[nodeID]
	c := &connection{peer: &Peer{Conn: conn}, pending: map[string]*pending{}, done: make(chan struct{})}
	h.nodes[nodeID] = c
	h.mu.Unlock()
	if old != nil {
		old.close()
		// close handshake는 최대 몇 초 걸리므로 새 연결 등록을 막지 않게 따로 한다.
		go old.peer.Conn.Close(websocket.StatusPolicyViolation, "superseded by a newer connection")
	}
	if h.OnConnect != nil {
		go h.OnConnect(nodeID)
	}
	return &Session{hub: h, nodeID: nodeID, c: c}, nil
}

// Disconnect는 노드의 연결을 끊는다(인증서 폐기/노드 삭제용). 연결이 없으면 아무것도 하지 않는다.
func (h *Hub) Disconnect(nodeID string) {
	h.mu.Lock()
	c := h.nodes[nodeID]
	delete(h.nodes, nodeID)
	h.mu.Unlock()
	if c != nil {
		c.close()
		go c.peer.Conn.Close(websocket.StatusPolicyViolation, "credentials revoked")
	}
}

// SessionFor는 노드의 현재 연결을 돌려준다(없으면 nil).
func (h *Hub) SessionFor(nodeID string) *Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c := h.nodes[nodeID]; c != nil {
		return &Session{hub: h, nodeID: nodeID, c: c}
	}
	return nil
}

func (h *Hub) Online(nodeID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.nodes[nodeID] != nil
}

// SetStatus는 노드 상태 기록을 노드별로 직렬화한다. write는 잠금 안에서 그 시점의 실제 연결 여부로 불린다.
// 그래서 늦게 끝난 이전 연결의 "offline" 기록이 새 연결의 "online"을 덮어쓰지 않는다(마지막 기록이 항상 최신 상태).
func (h *Hub) SetStatus(nodeID string, write func(online bool)) {
	h.mu.Lock()
	if h.statusMu == nil {
		h.statusMu = map[string]*sync.Mutex{}
	}
	m := h.statusMu[nodeID]
	if m == nil {
		m = &sync.Mutex{}
		h.statusMu[nodeID] = m
	}
	h.mu.Unlock()
	m.Lock()
	defer m.Unlock()
	write(h.Online(nodeID))
}

// Deliver는 agent가 보낸 응답을 기다리는 Call에 넘긴다. stream 메시지는 콜백을 바로 부른다.
func (h *Hub) Deliver(nodeID string, msg Message) {
	h.mu.Lock()
	c := h.nodes[nodeID]
	h.mu.Unlock()
	if c == nil {
		return
	}
	if msg.Up {
		if h.OnRequest != nil && msg.ID != "" {
			go h.OnRequest(nodeID, msg)
		}
		return
	}
	c.mu.Lock()
	p := c.pending[msg.ID]
	c.mu.Unlock()
	if p == nil {
		return
	}
	if msg.Stream {
		if p.stream != nil {
			p.stream(msg.Payload)
		}
		return
	}
	select {
	case p.final <- msg:
	default:
	}
}

// Reply는 agent가 보낸 요청(Up)에 답한다.
func (h *Hub) Reply(ctx context.Context, nodeID string, msg Message) error {
	h.mu.Lock()
	c := h.nodes[nodeID]
	h.mu.Unlock()
	if c == nil {
		return ErrOffline
	}
	msg.Reply, msg.Up = true, false
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return c.peer.Write(ctx, msg)
}

// Call은 요청을 보내고 응답을 기다린다. ctx에 deadline이 없으면 DefaultCallTimeout으로 제한한다.
// 연결이 끊기면 ErrOffline을 돌려준다.
func (h *Hub) Call(ctx context.Context, nodeID, operation string, request, result any) error {
	return h.CallStream(ctx, nodeID, operation, request, result, nil)
}

// CallStream은 Call과 같지만 최종 응답 전의 stream 메시지를 onChunk로 넘긴다.
// onChunk는 연결의 읽기 goroutine에서 불리므로 빨리 끝나야 한다.
func (h *Hub) CallStream(ctx context.Context, nodeID, operation string, request, result any, onChunk func(json.RawMessage)) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultCallTimeout)
		defer cancel()
	}
	h.mu.Lock()
	c := h.nodes[nodeID]
	h.mu.Unlock()
	if c == nil {
		return ErrOffline
	}
	id, err := Secret()
	if err != nil {
		return err
	}
	id = id[:24]
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	p := &pending{final: make(chan Message, 1), stream: onChunk}
	c.mu.Lock()
	if len(c.pending) >= MaxPending {
		c.mu.Unlock()
		return fmt.Errorf("too many in-flight requests to node %s", nodeID)
	}
	c.pending[id] = p
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
	err = c.peer.Write(writeCtx, Message{ID: id, Operation: operation, Payload: payload})
	cancel()
	if err != nil {
		select {
		case <-c.done:
			return ErrOffline
		default:
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %v", ErrOffline, err)
	}
	select {
	case <-ctx.Done():
		// agent 쪽 작업도 멈춘다(best effort).
		cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		b, _ := json.Marshal(CancelRequest{ID: id})
		c.peer.Write(cctx, Message{Operation: OpCancel, Payload: b})
		cancel()
		return ctx.Err()
	case <-c.done:
		return ErrOffline
	case response := <-p.final:
		if response.Error != "" {
			return &RemoteError{Msg: response.Error}
		}
		if result != nil && len(response.Payload) > 0 {
			return json.Unmarshal(response.Payload, result)
		}
		return nil
	}
}

// RemoteError는 agent가 돌려준 오류다.
type RemoteError struct{ Msg string }

func (e *RemoteError) Error() string { return e.Msg }
