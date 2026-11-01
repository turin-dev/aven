package deploy

import "sync"

// coalescer는 같은 백그라운드 작업(전체 mesh·egress 동기화)을 몰아서 한 번씩만 돌린다. 도는 중에 다시 요청되면
// 끝난 뒤 한 번 더 돌린다(그 사이의 변경도 반영된다). 배포가 몰려도 goroutine과 동기화가 요청 수만큼 쌓이지 않는다.
type coalescer struct {
	mu      sync.Mutex
	running bool
	pending bool
}

// Trigger는 f를 백그라운드에서 돌린다. 이미 돌고 있으면 끝난 뒤 한 번 더 돌리도록 표시만 한다.
func (c *coalescer) Trigger(f func()) {
	c.mu.Lock()
	if c.running {
		c.pending = true
		c.mu.Unlock()
		return
	}
	c.running = true
	c.mu.Unlock()
	go func() {
		for {
			f()
			c.mu.Lock()
			if !c.pending {
				c.running = false
				c.mu.Unlock()
				return
			}
			c.pending = false
			c.mu.Unlock()
		}
	}()
}
