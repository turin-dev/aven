package metrics

import (
	"testing"

	"github.com/turin-dev/aven/internal/store"
)

func TestDownsample(t *testing.T) {
	d := int64(100)
	rows := []store.MetricRow{
		{T: 1000, CPU: 10, Mem: 100, MemLimit: 1000, Rx: 5, Tx: 1, Replicas: 1},
		{T: 1010, CPU: 20, Mem: 300, MemLimit: 1000, Rx: 9, Tx: 2, Replicas: 2, Disk: &d},
		{T: 1290, CPU: 30, Mem: 200, MemLimit: 1000, Rx: 20, Tx: 3, Replicas: 2},
		{T: 1300, CPU: 40, Mem: 400, MemLimit: 2000, Rx: 30, Tx: 4, Replicas: 1},
	}
	out := Downsample(rows, 300)
	if len(out) != 2 {
		t.Fatalf("want 2 buckets, got %d: %+v", len(out), out)
	}
	b := out[0]
	if b.T != 900 || b.CPU != 15 || b.Mem != 200 || b.MemLimit != 1000 || b.Rx != 9 || b.Tx != 2 || b.Replicas != 2 || b.Resolution != 300 {
		t.Errorf("bucket 0: %+v", b)
	}
	if b.Disk == nil || *b.Disk != 100 {
		t.Errorf("disk should carry the last known value: %v", b.Disk)
	}
	if out[1].T != 1200 || out[1].CPU != 35 || out[1].Mem != 300 || out[1].MemLimit != 1500 || out[1].Rx != 30 {
		t.Errorf("bucket 1: %+v", out[1])
	}
	if got := Downsample(nil, 300); len(got) != 0 {
		t.Error("empty input")
	}
}

func TestRangeStep(t *testing.T) {
	for r, want := range map[string]int64{"1h": 10, "6h": 60, "24h": 300, "7d": 1800, "": 10} {
		_, step, ok := RangeStep(r)
		if !ok || step != want {
			t.Errorf("%s: %d %v", r, step, ok)
		}
	}
	if _, _, ok := RangeStep("1y"); ok {
		t.Error("1y should be invalid")
	}
}
