// Package sysstat은 노드(호스트) 전체의 CPU/메모리/디스크 사용량을 읽는다.
package sysstat

import (
	"strconv"
	"strings"
	"sync"
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
// MemAvailable이 없는 오래된 커널(3.14 미만)에서는 MemFree + Buffers + Cached로 어림한다(그대로 두면 사용률이 100%로 보인다).
func ParseMem(info string) (total, avail uint64) {
	var free, buffers, cached uint64
	haveAvail := false
	for _, line := range strings.Split(info, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		kib, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil || kib > 1<<50 { // 1 EiB 넘는 값은 깨진 입력이다(곱하다 넘치지 않게)
			continue
		}
		switch f[0] {
		case "MemTotal:":
			total = kib * 1024
		case "MemAvailable:":
			avail, haveAvail = kib*1024, true
		case "MemFree:":
			free = kib * 1024
		case "Buffers:":
			buffers = kib * 1024
		case "Cached:":
			cached = kib * 1024
		}
	}
	if !haveAvail {
		avail = free + buffers + cached
	}
	if avail > total {
		avail = total
	}
	return
}

// CPUPercent는 두 /proc/stat 표본 사이의 사용률(0..100)이다. 카운터가 줄면(iowait는 커널에서 줄 수 있다,
// 재부팅·컨테이너 이동) 0으로 본다. 부호 없는 뺄셈이 넘쳐 사용률이 100%로 튀지 않게 한다.
func CPUPercent(prevIdle, prevTotal, idle, total uint64) float64 {
	if total <= prevTotal {
		return 0
	}
	dTotal := total - prevTotal
	var dIdle uint64
	if idle > prevIdle {
		dIdle = idle - prevIdle
	}
	if dIdle >= dTotal {
		return 0
	}
	return 100 * float64(dTotal-dIdle) / float64(dTotal)
}

// Sample은 CPU 사용률을 지난 호출 이후 구간으로 계산한다(첫 호출은 부팅 이후 평균).
func (s *Sampler) Sample(dataDir string) Sample {
	var r Sample
	s.mu.Lock()
	if idle, total, ok := hostCPU(); ok {
		var prevIdle, prevTotal uint64
		if s.init {
			prevIdle, prevTotal = s.idle, s.total
		}
		if total > prevTotal {
			r.CPUPercent = CPUPercent(prevIdle, prevTotal, idle, total)
			s.idle, s.total, s.init = idle, total, true
		}
	}
	r.MemoryTotal, r.MemoryBytes = hostMemoryUsed()
	s.mu.Unlock()
	r.DiskFreeBytes = DiskFreeBytes(dataDir)
	return r
}

// MemoryTotalBytes returns the host's physical memory size when the platform exposes it.
func MemoryTotalBytes() uint64 {
	total, _ := hostMemory()
	return total
}
