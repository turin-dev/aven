// Package sysstat은 노드(호스트) 전체의 CPU/메모리/디스크 사용량을 /proc에서 읽는다.
package sysstat

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// Sample은 한 시점의 노드 사용량이다.
type Sample struct {
	CPUPercent    float64 // 노드 전체 CPU 사용률(0..100). 이전 Sample 호출 이후 구간 기준
	MemoryBytes   uint64  // 사용 중인 메모리(MemTotal - MemAvailable)
	MemoryTotal   uint64
	DiskFreeBytes uint64
}

type Sampler struct {
	mu          sync.Mutex
	idle, total uint64
	init        bool
}

// ParseCPU는 /proc/stat의 첫 줄에서 (idle, total) jiffies를 읽는다.
func ParseCPU(stat string) (idle, total uint64, ok bool) {
	line := stat
	if i := strings.IndexByte(stat, '\n'); i >= 0 {
		line = stat[:i]
	}
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return 0, 0, false
	}
	var vals []uint64
	for _, s := range f[1:] {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		vals = append(vals, v)
	}
	for i, v := range vals {
		if i >= 8 { // guest 계열은 user/nice에 이미 포함돼 있다
			break
		}
		total += v
	}
	idle = vals[3]
	if len(vals) > 4 {
		idle += vals[4] // iowait
	}
	return idle, total, true
}

// ParseMem은 /proc/meminfo에서 (total, available) bytes를 읽는다.
func ParseMem(info string) (total, avail uint64) {
	for _, line := range strings.Split(info, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		kib, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			total = kib * 1024
		case "MemAvailable:":
			avail = kib * 1024
		}
	}
	return
}

// Sample은 CPU 사용률을 지난 호출 이후 구간으로 계산한다(첫 호출은 부팅 이후 평균).
func (s *Sampler) Sample(dataDir string) Sample {
	var r Sample
	s.mu.Lock()
	if b, err := os.ReadFile("/proc/stat"); err == nil {
		if idle, total, ok := ParseCPU(string(b)); ok {
			var prevIdle, prevTotal uint64
			if s.init {
				prevIdle, prevTotal = s.idle, s.total
			}
			if total > prevTotal {
				busy := float64((total - prevTotal) - (idle - prevIdle))
				r.CPUPercent = 100 * busy / float64(total-prevTotal)
				if r.CPUPercent < 0 {
					r.CPUPercent = 0
				}
				if r.CPUPercent > 100 {
					r.CPUPercent = 100
				}
			}
			s.idle, s.total, s.init = idle, total, true
		}
	}
	s.mu.Unlock()
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		total, avail := ParseMem(string(b))
		r.MemoryTotal = total
		if total >= avail {
			r.MemoryBytes = total - avail
		}
	}
	if dataDir == "" {
		dataDir = "/"
	}
	var fs syscall.Statfs_t
	if syscall.Statfs(dataDir, &fs) == nil {
		r.DiskFreeBytes = fs.Bavail * uint64(fs.Bsize)
	}
	return r
}
