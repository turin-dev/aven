package main

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
)

// Restart delays for a background loop that panicked: the first restart waits loopRestartMin, each
// further one doubles up to loopRestartMax, so a loop that panics on every tick cannot spin.
var (
	loopRestartMin = 30 * time.Second
	loopRestartMax = 5 * time.Minute
)

// goLoop runs fn as a tracked background task. A panic in fn is logged with its stack and fn is started
// again after a delay, so one faulty loop (an image-update check, a metrics collector) cannot take down
// the API, the proxy sync and every other loop with it. fn returning normally ends the task.
func goLoop(bg *sync.WaitGroup, ctx context.Context, log *slog.Logger, name string, fn func()) {
	bg.Go(func() { runRecovering(ctx, log, name, fn) })
}

func runRecovering(ctx context.Context, log *slog.Logger, name string, fn func()) {
	delay := loopRestartMin
	for {
		if !runOnce(log, name, fn) || ctx.Err() != nil {
			return
		}
		log.Error("background task restarts after a panic", "task", name, "in", delay)
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		delay = min(delay*2, loopRestartMax)
	}
}

// runOnce calls fn and reports whether it panicked.
func runOnce(log *slog.Logger, name string, fn func()) (panicked bool) {
	defer func() {
		if v := recover(); v != nil {
			panicked = true
			log.Error("background task panicked", "task", name, "panic", v, "stack", string(debug.Stack()))
		}
	}()
	fn()
	return false
}
