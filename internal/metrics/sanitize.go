package metrics

import (
	"math"

	"github.com/turin-dev/aven/internal/node"
)

// 원격 agent가 보낸 사용량은 믿지 않는다(옛 agent, 고장 난 docker stats). NaN은 SQLite에 NULL로 들어가려다
// NOT NULL 제약에 걸리고, collect는 첫 저장 오류에서 멈추므로 그 주기의 나머지 인스턴스 지표가 모두 빠진다.
// Inf는 JSON 응답을 만들 수 없게 한다. 음수는 의미가 없다.

// maxContainerCPU는 컨테이너 하나의 CPU 사용률 상한(%)이다. docker stats는 코어 수 × 100까지 낸다.
const maxContainerCPU = 100 * 1024

func finiteNonNeg(f, max float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0
	}
	return math.Min(f, max)
}

func nonNeg(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}

// SanitizeStats는 컨테이너 사용량 하나를 저장할 수 있는 값으로 고친다.
func SanitizeStats(s node.Stats) node.Stats {
	s.CPU = finiteNonNeg(s.CPU, maxContainerCPU)
	s.Mem, s.MemLimit, s.Rx, s.Tx, s.BlockRead = nonNeg(s.Mem), nonNeg(s.MemLimit), nonNeg(s.Rx), nonNeg(s.Tx), nonNeg(s.BlockRead)
	return s
}

// SanitizeNodeStat는 노드 사용량을 저장할 수 있는 값으로 고친다(CPU는 노드 전체 0..100%).
func SanitizeNodeStat(s NodeStat) NodeStat {
	s.CPUPercent = finiteNonNeg(s.CPUPercent, 100)
	if s.MemoryBytes > math.MaxInt64 {
		s.MemoryBytes = 0
	}
	if s.DiskFreeBytes > math.MaxInt64 {
		s.DiskFreeBytes = 0
	}
	return s
}
