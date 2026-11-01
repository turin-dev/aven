package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
)

// eventsDocker의 Events는 ctx가 끝날 때까지 돌아오지 않는다(실제 docker events --follow처럼).
type eventsDocker struct {
	fakeDocker
	started chan struct{}
}

func (f *eventsDocker) Events(ctx context.Context, _ func(node.ContainerEvent)) error {
	close(f.started)
	<-ctx.Done()
	return ctx.Err()
}

// 연결이 끊기면 끝나지 않는 스트림 요청을 취소하고 바로 돌아와야 한다(그래야 agent가 다시 연결한다).
func TestServeReturnsWhenConnectionDropsDuringStream(t *testing.T) {
	f := &eventsDocker{started: make(chan struct{})}
	h := &Handler{Docker: f, Proxy: &fakeProxy{}}
	msg, _ := json.Marshal(agentprotocol.Message{ID: "req-1", Operation: agentprotocol.OpEvents, Payload: json.RawMessage(`{}`)})
	sent := false
	read := func(ctx context.Context) ([]byte, error) {
		if !sent {
			sent = true
			return msg, nil
		}
		<-f.started // 스트림이 시작된 뒤에 연결이 끊긴다
		return nil, errors.New("connection lost")
	}
	write := func(context.Context, agentprotocol.Message) error { return nil }
	done := make(chan error, 1)
	go func() { done <- h.Serve(context.Background(), read, write) }()
	select {
	case err := <-done:
		if err == nil || err.Error() != "connection lost" {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after the connection dropped (in-flight stream was not cancelled)")
	}
}

// 처리 중인 요청과 같은 id의 요청은 거부한다. 그래도 첫 요청은 cancel로 멈출 수 있어야 한다.
func TestServeRejectsDuplicateInFlightIDsAndKeepsCancel(t *testing.T) {
	f := &eventsDocker{started: make(chan struct{})}
	h := &Handler{Docker: f, Proxy: &fakeProxy{}}
	enc := func(m agentprotocol.Message) []byte { b, _ := json.Marshal(m); return b }
	cancelPayload, _ := json.Marshal(agentprotocol.CancelRequest{ID: "req-1"})
	in := make(chan []byte, 8)
	in <- enc(agentprotocol.Message{ID: "req-1", Operation: agentprotocol.OpEvents})
	read := func(ctx context.Context) ([]byte, error) {
		select {
		case b := <-in:
			return b, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	out := make(chan agentprotocol.Message, 8)
	write := func(_ context.Context, m agentprotocol.Message) error { out <- m; return nil }
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go h.Serve(ctx, read, write)
	<-f.started
	in <- enc(agentprotocol.Message{ID: "req-1", Operation: agentprotocol.OpPing})
	select {
	case m := <-out:
		if m.ID != "req-1" || m.Error != "duplicate request id" {
			t.Fatalf("duplicate request answered with %+v", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("duplicate request id was not refused")
	}
	in <- enc(agentprotocol.Message{Operation: agentprotocol.OpCancel, Payload: cancelPayload})
	select {
	case m := <-out:
		if m.ID != "req-1" || m.Error == "" {
			t.Fatalf("cancelled stream answered with %+v", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first request could not be cancelled")
	}
}
