//go:build !linux && !windows

package sysstat

func hostCPU() (idle, total uint64, ok bool) { return 0, 0, false }

func hostMemory() (total, available uint64) { return 0, 0 }

func hostMemoryUsed() (total, used uint64) { return 0, 0 }

func DiskFreeBytes(string) uint64 { return 0 }
