package main

import (
	"testing"
	"time"
)

func TestBackoffGrowsWithJitterAndResetsAfterStableConnection(t *testing.T) {
	for _, tc := range []struct {
		name string
		rnd  float64
		want []time.Duration
	}{
		{"low", 0, []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 15 * time.Second}},
		{"high", 0.999999, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &backoff{rnd: func() float64 { return tc.rnd }}
			for i, want := range tc.want {
				got := b.next(time.Second).Round(time.Millisecond)
				if got != want.Round(time.Millisecond) {
					t.Fatalf("attempt %d: got %v, want %v", i, got, want)
				}
			}
			// 연결이 오래 유지됐으면 처음부터 다시 센다.
			if got, want := b.next(2*time.Minute).Round(time.Millisecond), tc.want[0].Round(time.Millisecond); got != want {
				t.Fatalf("after a stable connection: got %v, want %v", got, want)
			}
		})
	}
}

func TestBackoffSpreadsAgents(t *testing.T) {
	// 실제 난수로 같은 시도 횟수의 agent들이 같은 순간에 몰리지 않는지 본다.
	seen := map[time.Duration]bool{}
	for i := 0; i < 50; i++ {
		b := &backoff{}
		for j := 0; j < 3; j++ {
			b.next(0)
		}
		d := b.next(0)
		if d < 4*time.Second || d >= 8*time.Second {
			t.Fatalf("4th attempt waited %v, want within [4s, 8s)", d)
		}
		seen[d.Truncate(10*time.Millisecond)] = true
	}
	if len(seen) < 10 {
		t.Fatalf("only %d distinct delays across 50 agents; jitter is not spreading reconnects", len(seen))
	}
	// 시도 횟수가 아주 커도 넘치지 않고 상한을 지킨다.
	b := &backoff{attempt: 1000}
	if d := b.next(0); d < maxBackoff/2 || d >= maxBackoff {
		t.Fatalf("huge attempt count: %v", d)
	}
}
