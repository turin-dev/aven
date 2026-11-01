package deploy

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 도는 중에 여러 번 요청해도 끝난 뒤 한 번만 더 돌고, 동시에 둘이 돌지 않는다.
func TestCoalescer(t *testing.T) {
	var c coalescer
	var runs, active, maxActive atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2) // 첫 실행 + 몰아서 한 번 더
	f := func() {
		n := active.Add(1)
		if n > maxActive.Load() {
			maxActive.Store(n)
		}
		if runs.Add(1) == 1 {
			<-release
		}
		active.Add(-1)
		wg.Done()
	}
	c.Trigger(f)
	for i := 0; i < 50; i++ {
		c.Trigger(f)
	}
	close(release)
	wg.Wait()
	time.Sleep(50 * time.Millisecond)
	if runs.Load() != 2 || maxActive.Load() != 1 {
		t.Fatalf("runs=%d maxActive=%d", runs.Load(), maxActive.Load())
	}
	// 끝난 뒤에는 다시 바로 돈다.
	done := make(chan struct{})
	c.Trigger(func() { close(done) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("trigger after idle did not run")
	}
}
