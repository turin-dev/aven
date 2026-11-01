package main

import (
	"math/rand/v2"
	"time"
)

// 재연결 대기: 지수 증가(minBackoff부터 두 배씩, maxBackoff까지)에 jitter를 섞는다. 서버가 재시작하면 모든 agent가 같은 순간
// 끊기므로, jitter가 없으면 1, 2, 4초... 같은 박자로 한꺼번에 다시 몰려와(thundering herd) 인증 한도와 handshake를 같이 친다.
// 연결이 stableAfter 넘게 유지됐으면 다음 실패는 처음부터 센다.
const (
	minBackoff  = time.Second
	maxBackoff  = 30 * time.Second
	stableAfter = time.Minute
)

type backoff struct {
	attempt int
	// rnd는 [0,1) 난수다(테스트용, nil이면 math/rand/v2).
	rnd func() float64
}

// next는 연결이 connected 동안 유지되다 끊긴 뒤 기다릴 시간이다. 값은 [d/2, d) 범위다(d는 지수 증가한 상한).
// 아래 절반을 남겨 두어 빠르게 실패하는 서버를 두드리지 않고, 위 절반은 고르게 퍼뜨린다("equal jitter").
func (b *backoff) next(connected time.Duration) time.Duration {
	if connected > stableAfter {
		b.attempt = 0
	}
	d := maxBackoff
	if b.attempt < 16 { // 1<<attempt가 넘치지 않게
		d = min(maxBackoff, minBackoff<<b.attempt)
	}
	b.attempt++
	r := b.rnd
	if r == nil {
		r = rand.Float64
	}
	return d/2 + time.Duration(r()*float64(d/2))
}
