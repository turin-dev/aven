package main

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func withRestartDelays(t *testing.T, lo, hi time.Duration) {
	t.Helper()
	oldLo, oldHi := loopRestartMin, loopRestartMax
	loopRestartMin, loopRestartMax = lo, hi
	t.Cleanup(func() { loopRestartMin, loopRestartMax = oldLo, oldHi })
}

func TestGoLoopRestartsAfterPanic(t *testing.T) {
	withRestartDelays(t, time.Millisecond, 4*time.Millisecond)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var runs atomic.Int32
	var bg sync.WaitGroup
	goLoop(&bg, context.Background(), log, "test", func() {
		if runs.Add(1) < 3 {
			panic("boom")
		}
	})
	bg.Wait()
	if got := runs.Load(); got != 3 {
		t.Fatalf("runs = %d, want 3 (two panics, then a normal return)", got)
	}
}

func TestGoLoopStopsWaitingOnCancel(t *testing.T) {
	withRestartDelays(t, time.Hour, time.Hour)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	var runs atomic.Int32
	var bg sync.WaitGroup
	goLoop(&bg, ctx, log, "test", func() {
		runs.Add(1)
		panic("boom")
	})
	time.Sleep(20 * time.Millisecond)
	cancel()
	if !waitTimeout(&bg, 5*time.Second) {
		t.Fatal("the task kept waiting for its restart after cancellation")
	}
	if got := runs.Load(); got != 1 {
		t.Fatalf("runs = %d, want 1", got)
	}
}
