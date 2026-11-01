package events

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

func TestPayloadHasDiscordAndSlackKeys(t *testing.T) {
	var m map[string]string
	if err := json.Unmarshal(Payload("example.com", "deploy.failed", "web 배포 실패"), &m); err != nil {
		t.Fatal(err)
	}
	if m["content"] == "" || m["content"] != m["text"] {
		t.Errorf("payload: %v", m)
	}
}

func newBus(t *testing.T) (*Bus, *store.Project, *store.Environment) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	p := &store.Project{Name: "acme"}
	env, err := st.CreateProject(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return &Bus{Store: st}, p, env
}

func TestEmitFanoutFiltersByProject(t *testing.T) {
	ctx := context.Background()
	b, p, env := newBus(t)
	other := &store.Project{Name: "other"}
	b.Store.CreateProject(ctx, other)
	svc := &store.Service{ProjectID: p.ID, Name: "web", SourceType: "image"}
	b.Store.CreateService(ctx, svc)

	mine, cancelMine := b.Subscribe(p.ID)
	defer cancelMine()
	theirs, cancelTheirs := b.Subscribe(other.ID)
	defer cancelTheirs()

	ctx = WithDeployment(WithActor(ctx, "alice"), "dep_1")
	b.Emit(ctx, p.ID, &env.ID, &svc.ID, "deploy.live", "ok")
	select {
	case e := <-mine:
		if e.Kind != "deploy.live" || e.ServiceName == nil || *e.ServiceName != "web" ||
			e.Actor == nil || *e.Actor != "alice" || e.DeploymentID == nil || *e.DeploymentID != "dep_1" {
			t.Fatalf("event: %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no event")
	}
	select {
	case e := <-theirs:
		t.Fatalf("other project got %+v", e)
	default:
	}
	evs, _ := b.Store.ListEvents(ctx, p.ID, "", 10)
	if len(evs) != 1 {
		t.Fatalf("stored: %d", len(evs))
	}
}

func TestCancelIsIdempotentAndCloses(t *testing.T) {
	b, p, _ := newBus(t)
	ch, cancel := b.Subscribe(p.ID)
	cancel()
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("channel not closed")
	}
	b.mu.Lock()
	n := len(b.subs)
	b.mu.Unlock()
	if n != 0 {
		t.Fatalf("leaked subscribers: %d", n)
	}
	// 취소한 뒤의 Emit은 닫힌 채널에 쓰지 않는다(panic 없음)
	b.Emit(context.Background(), p.ID, nil, nil, "k", "m")
}

func TestSlowSubscriberDoesNotBlockEmit(t *testing.T) {
	b, p, _ := newBus(t)
	_, cancel := b.Subscribe(p.ID) // 읽지 않는다
	defer cancel()
	done := make(chan struct{})
	go func() {
		for i := 0; i < subscriberBuffer*3; i++ {
			b.Emit(context.Background(), p.ID, nil, nil, "k", "m")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Emit blocked on a slow subscriber")
	}
}

type countingNotifier struct {
	cur, peak, total atomic.Int32
	release          chan struct{}
}

func (n *countingNotifier) Dispatch(*store.Event) {
	c := n.cur.Add(1)
	for {
		p := n.peak.Load()
		if c <= p || n.peak.CompareAndSwap(p, c) {
			break
		}
	}
	<-n.release
	n.cur.Add(-1)
	n.total.Add(1)
}

func TestDispatchConcurrencyBounded(t *testing.T) {
	b, p, _ := newBus(t)
	n := &countingNotifier{release: make(chan struct{})}
	b.Notifier = n
	const events = MaxConcurrentDispatch * 3
	for i := 0; i < events; i++ {
		b.Emit(context.Background(), p.ID, nil, nil, "k", "m")
	}
	time.Sleep(100 * time.Millisecond)
	if peak := n.peak.Load(); peak > MaxConcurrentDispatch {
		t.Fatalf("peak concurrent dispatch %d > %d", peak, MaxConcurrentDispatch)
	}
	close(n.release)
	b.Wait()
	if got := n.total.Load(); got != events {
		t.Fatalf("dispatched %d of %d", got, events)
	}
}
