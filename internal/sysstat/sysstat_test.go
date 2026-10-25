package sysstat

import "testing"

func TestParseCPU(t *testing.T) {
	idle, total, ok := ParseCPU("cpu  100 20 30 400 50 0 0 0 0 0\ncpu0 1 2 3 4 5\n")
	if !ok || idle != 450 || total != 600 {
		t.Fatalf("idle=%d total=%d ok=%v", idle, total, ok)
	}
	for _, bad := range []string{"", "cpu0 1 2 3 4", "cpu a b c d e", "intr 1 2 3 4 5"} {
		if _, _, ok := ParseCPU(bad); ok {
			t.Fatalf("%q parsed", bad)
		}
	}
}

func TestParseMem(t *testing.T) {
	total, avail := ParseMem("MemTotal:       2048 kB\nMemFree: 10 kB\nMemAvailable:   512 kB\n")
	if total != 2048*1024 || avail != 512*1024 {
		t.Fatalf("%d %d", total, avail)
	}
}

func TestSamplerReportsSaneValues(t *testing.T) {
	var s Sampler
	s.Sample(t.TempDir())
	r := s.Sample(t.TempDir())
	if r.CPUPercent < 0 || r.CPUPercent > 100 || r.MemoryTotal == 0 || r.MemoryBytes > r.MemoryTotal || r.DiskFreeBytes == 0 {
		t.Fatalf("%+v", r)
	}
}
