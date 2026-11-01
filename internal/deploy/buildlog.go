package deploy

import (
	"fmt"
	"sync"
	"unicode/utf8"
)

// 빌드·pre-deploy·cron 출력의 한도. 빌드 도구가 쏟아내는 출력(진행 막대, minified 번들, 무한 루프)이 배포 로그 파일과
// 메모리(logbook은 진행 중인 배포의 줄을 모두 들고 있다)를 끝없이 키우지 않게 한다. 실패 원인은 보통 끝에 있으므로
// 앞부분 head줄과 마지막 tail줄을 남기고 가운데를 줄인다.
const (
	// maxLogLine은 한 줄의 최대 바이트다(로그 파일을 다시 읽는 scanner는 1 MiB를 넘는 줄에서 멈춘다).
	maxLogLine = 4 << 10
	// buildLogHead/buildLogTail은 빌드 로그에 남기는 앞·뒤 줄 수다.
	buildLogHead = 10000
	buildLogTail = 2000
)

// truncateLine은 s를 maxLogLine바이트 이하로 자른다(UTF-8 문자 경계).
func truncateLine(s string) string {
	if len(s) <= maxLogLine {
		return s
	}
	cut := maxLogLine
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf(" …(%d bytes truncated)", len(s)-cut)
}

// lineLimiter는 항목을 out으로 넘기되 head개 뒤로는 마지막 tail개만 모아 두었다가 Flush에서 생략 표시(mark)와 함께 넘긴다.
// 여러 goroutine(stdout/stderr)에서 불러도 된다.
type lineLimiter[T any] struct {
	mu      sync.Mutex
	out     func(T)
	mark    func(omitted int) T
	head    int
	tail    int
	ring    []T // 꼬리 항목(가득 차면 next부터가 오래된 것)
	next    int
	seen    int
	omitted int
}

func newLineLimiter[T any](out func(T), mark func(int) T, head, tail int) *lineLimiter[T] {
	return &lineLimiter[T]{out: out, mark: mark, head: head, tail: tail}
}

// Add는 항목 하나를 기록한다.
func (l *lineLimiter[T]) Add(v T) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen++
	switch {
	case l.seen <= l.head:
		l.out(v)
	case l.tail <= 0:
		l.omitted++
	case len(l.ring) < l.tail:
		l.ring = append(l.ring, v)
	default:
		l.ring[l.next] = v
		l.next = (l.next + 1) % len(l.ring)
		l.omitted++
	}
}

// Flush는 모아 둔 마지막 항목을 넘긴다. 생략한 항목이 있으면 먼저 그 수를 알린다. 이후 항목도 계속 꼬리로 모은다.
func (l *lineLimiter[T]) Flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.omitted > 0 {
		l.out(l.mark(l.omitted))
	}
	for i := range l.ring {
		l.out(l.ring[(l.next+i)%len(l.ring)])
	}
	l.ring, l.next, l.omitted = l.ring[:0], 0, 0
}

func omittedMsg(n int) string {
	return fmt.Sprintf("… 출력이 너무 길어 %d줄을 생략했습니다 …", n)
}

// limitedLog는 빌드 출력용 줄 함수와, 단계가 끝나면 부를 flush를 돌려준다(긴 줄은 자른다).
func limitedLog(out func(string)) (func(string), func()) {
	l := newLineLimiter(out, omittedMsg, buildLogHead, buildLogTail)
	return func(s string) { l.Add(truncateLine(s)) }, l.Flush
}
