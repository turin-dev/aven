package api

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// limiter는 키(IP, 계정)별 실패 횟수를 세고, 임계값을 넘으면 지수적으로 늘어나는 잠금(backoff)을 건다.
// 잠겨 있는 동안에는 비밀번호가 맞아도 검사하지 않고 거부한다(무차별 대입이 잠금을 우회하지 못하게).
type limiter struct {
	threshold int
	base, max time.Duration
	window    time.Duration // 마지막 실패로부터 이 시간이 지나면 횟수를 잊는다
	maxKeys   int
	now       func() time.Time

	mu sync.Mutex
	m  map[string]*limitEntry
}

type limitEntry struct {
	fails  int
	last   time.Time
	locked time.Time
}

func newLimiter(threshold int, base, max, window time.Duration) *limiter {
	return &limiter{threshold: threshold, base: base, max: max, window: window, maxKeys: 20000, now: time.Now, m: map[string]*limitEntry{}}
}

// wait는 키가 잠겨 있으면 남은 시간을 돌려준다(0이면 통과).
func (l *limiter) wait(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.m[key]; e != nil {
		if d := e.locked.Sub(l.now()); d > 0 {
			return d
		}
	}
	return 0
}

func (l *limiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e := l.m[key]
	if e == nil {
		if len(l.m) >= l.maxKeys {
			l.gc(now)
			if len(l.m) >= l.maxKeys { // 그래도 가득 차면 가장 오래된 항목을 버린다
				var oldest string
				var ot time.Time
				for k, v := range l.m {
					if oldest == "" || v.last.Before(ot) {
						oldest, ot = k, v.last
					}
				}
				delete(l.m, oldest)
			}
		}
		e = &limitEntry{}
		l.m[key] = e
	}
	if now.Sub(e.last) > l.window && !e.locked.After(now) {
		e.fails = 0
	}
	e.fails++
	e.last = now
	if e.fails >= l.threshold {
		d := l.base
		for i := l.threshold; i < e.fails && d < l.max; i++ {
			d *= 2
		}
		if d > l.max {
			d = l.max
		}
		e.locked = now.Add(d)
	}
}

func (l *limiter) reset(key string) {
	l.mu.Lock()
	delete(l.m, key)
	l.mu.Unlock()
}

func (l *limiter) gc(now time.Time) {
	for k, e := range l.m {
		if now.Sub(e.last) > l.window && !e.locked.After(now) {
			delete(l.m, k)
		}
	}
}

// limits는 서버의 rate limiter 모음이다(처음 쓸 때 만든다; 테스트가 Server를 리터럴로 만들어도 동작한다).
type limits struct {
	loginIP   *limiter // IP당: 5번 틀리면 30초, 이후 두 배씩 최대 15분
	loginAcct *limiter // 계정당(모든 IP 합산): 20번 틀리면 30초, 이후 두 배씩 최대 10분
	agentIP   *limiter // 잘못된 Bearer / join token / agent 인증 시도: IP당 10번 이후 잠금
}

func (s *Server) lim() *limits {
	s.limOnce.Do(func() {
		s.limits = &limits{
			loginIP:   newLimiter(5, 30*time.Second, 15*time.Minute, time.Hour),
			loginAcct: newLimiter(20, 30*time.Second, 10*time.Minute, time.Hour),
			agentIP:   newLimiter(10, 30*time.Second, 15*time.Minute, time.Hour),
		}
	})
	return s.limits
}

func tooMany(w http.ResponseWriter, wait time.Duration, msg string) {
	secs := int(wait/time.Second) + 1
	w.Header().Set("Retry-After", fmt.Sprint(secs))
	writeErr(w, http.StatusTooManyRequests, fmt.Sprintf("%s (%d초 후 다시 시도하세요)", msg, secs))
}
