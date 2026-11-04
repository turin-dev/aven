//go:build linux

package sysstat

import (
	"os"
	"syscall"
)

func hostCPU() (idle, total uint64, ok bool) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	return ParseCPU(string(b))
}

func hostMemory() (total, available uint64) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	return ParseMem(string(b))
}

func hostMemoryUsed() (total, used uint64) {
	total, available := hostMemory()
	if total >= available {
		used = total - available
	}
	return total, used
}

func DiskFreeBytes(path string) uint64 {
	if path == "" {
		path = "/"
	}
	var fs syscall.Statfs_t
	if syscall.Statfs(path, &fs) != nil {
		return 0
	}
	return fs.Bavail * uint64(fs.Bsize)
}
