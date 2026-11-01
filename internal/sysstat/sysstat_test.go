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

func TestCPUPercent(t *testing.T) {
	cases := []struct {
		name                             string
		prevIdle, prevTotal, idle, total uint64
		want                             float64
	}{
		{"half busy", 100, 200, 150, 300, 50},
		{"idle", 100, 200, 200, 300, 0},
		{"fully busy", 100, 200, 100, 300, 100},
		// iowait가 줄어 idle+iowait가 이전보다 작아졌다: 예전 코드는 uint64 뺄셈이 넘쳐 100%를 냈다.
		{"idle counter went backwards", 500, 1000, 490, 1100, 100},
		{"idle grew more than total", 100, 200, 400, 300, 0},
		{"total went backwards", 100, 1000, 50, 500, 0},
		{"no time passed", 100, 200, 100, 200, 0},
	}
	for _, c := range cases {
		if got := CPUPercent(c.prevIdle, c.prevTotal, c.idle, c.total); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestParseMemWithoutMemAvailable(t *testing.T) {
	total, avail := ParseMem("MemTotal: 1000 kB\nMemFree: 100 kB\nBuffers: 50 kB\nCached: 250 kB\n")
	if total != 1000*1024 || avail != 400*1024 {
		t.Fatalf("fallback: %d %d", total, avail)
	}
	if _, avail := ParseMem("MemTotal: 10 kB\nMemAvailable: 20 kB\n"); avail != 10*1024 {
		t.Fatalf("available must not exceed total: %d", avail)
	}
	if total, _ := ParseMem("MemTotal: 99999999999999999999 kB\nMemTotal: x kB\n"); total != 0 {
		t.Fatalf("garbage parsed: %d", total)
	}
}
