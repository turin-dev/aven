package tracing

import (
	"time"
	"unicode/utf8"

	"github.com/turin-dev/aven/internal/store"
)

// 수신 한도. 요청 하나는 MaxRequestBytes와 MaxSpansPerRequest로, 저장량은 아래 창(RateWindow) 단위 한도로 막는다.
// replica ID는 앱이 resource 속성(keel.replica.id)으로 정하므로 값을 바꿔 가며 replica 한도를 피할 수 있다.
// 그래서 토큰이 정한 서비스(서비스 × 환경) 전체에도 한도를 두고, 서비스 한도를 먼저 본다(replica 창이 무한히 늘지 않게).
const (
	// ReplicaByteLimit은 replica 하나가 RateWindow 동안 저장할 수 있는 span 크기(추정) 합이다.
	ReplicaByteLimit = 16 << 20
	// ServiceSpanLimit / ServiceByteLimit은 서비스 하나의 모든 replica를 합친 한도다.
	ServiceSpanLimit = 20 * ReplicaSpanLimit
	ServiceByteLimit = 64 << 20
	// MaxClockSkew보다 미래에 시작한 span은 버린다(보관 기간 정리에 걸리지 않고 목록 맨 위에 남는다).
	MaxClockSkew = time.Hour

	maxKeyBytes   = 256 // 속성 이름
	maxIdentBytes = 256 // 서비스 이름, 배포/replica ID
)

// truncateUTF8은 s를 n바이트 이하로 자르되 UTF-8 문자 중간에서 자르지 않는다.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// spanBytes는 span을 저장할 때의 크기를 대략 셈한다(문자열 길이 합 + 값마다 조금).
func spanBytes(sp *store.TraceSpan) int {
	n := 256 + len(sp.Name) + len(sp.ServiceName) + len(sp.DeploymentID) + len(sp.ReplicaID)
	n += mapBytes(sp.Attributes) + mapBytes(sp.ResourceAttributes)
	for _, ev := range sp.Events {
		n += 32 + len(ev.Name) + mapBytes(ev.Attributes)
	}
	for _, ln := range sp.Links {
		n += 64 + mapBytes(ln.Attributes)
	}
	return n
}

func mapBytes(m map[string]any) int {
	n := 0
	for k, v := range m {
		n += len(k) + valueBytes(v)
	}
	return n
}

func valueBytes(v any) int {
	switch x := v.(type) {
	case string:
		return len(x) + 2
	case []any:
		n := 2
		for _, item := range x {
			n += valueBytes(item) + 1
		}
		return n
	case map[string]any:
		return mapBytes(x) + 2
	}
	return 8
}

type usage struct {
	start        time.Time
	spans, bytes int
}

func (u *usage) fits(now time.Time, size, spanLimit, byteLimit int) bool {
	if now.Sub(u.start) >= RateWindow {
		u.start, u.spans, u.bytes = now, 0, 0
	}
	return u.spans < spanLimit && u.bytes+size <= byteLimit
}

// dropFuture는 limit보다 늦게 시작한 span을 뺀다.
func dropFuture(spans []store.TraceSpan, limit time.Time) ([]store.TraceSpan, int64) {
	kept := spans[:0]
	var dropped int64
	for _, sp := range spans {
		if sp.StartTime.After(limit) {
			dropped++
			continue
		}
		kept = append(kept, sp)
	}
	return kept, dropped
}
