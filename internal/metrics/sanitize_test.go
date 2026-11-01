package metrics

import (
	"math"
	"testing"

	"github.com/turin-dev/aven/internal/node"
)

func TestSanitizeStats(t *testing.T) {
	for _, c := range []struct {
		in   float64
		want float64
	}{{math.NaN(), 0}, {math.Inf(1), 0}, {math.Inf(-1), 0}, {-5, 0}, {250, 250}, {1e12, maxContainerCPU}} {
		if got := SanitizeStats(node.Stats{CPU: c.in}).CPU; got != c.want {
			t.Errorf("CPU %v → %v, want %v", c.in, got, c.want)
		}
	}
	s := SanitizeStats(node.Stats{Mem: -1, MemLimit: -2, Rx: -3, Tx: 4})
	if s.Mem != 0 || s.MemLimit != 0 || s.Rx != 0 || s.Tx != 4 {
		t.Errorf("negative counters: %+v", s)
	}
}

func TestSanitizeNodeStat(t *testing.T) {
	s := SanitizeNodeStat(NodeStat{CPUPercent: math.NaN(), MemoryBytes: math.MaxUint64, DiskFreeBytes: 10})
	if s.CPUPercent != 0 || s.MemoryBytes != 0 || s.DiskFreeBytes != 10 {
		t.Errorf("%+v", s)
	}
	if got := SanitizeNodeStat(NodeStat{CPUPercent: 180}).CPUPercent; got != 100 {
		t.Errorf("node CPU clamp: %v", got)
	}
}
