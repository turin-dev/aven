//go:build windows

package sysstat

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32HostStats             = windows.NewLazySystemDLL("kernel32.dll")
	getSystemTimesHostStats       = kernel32HostStats.NewProc("GetSystemTimes")
	globalMemoryStatusExHostStats = kernel32HostStats.NewProc("GlobalMemoryStatusEx")
)

type windowsFiletime struct {
	LowDateTime  uint32
	HighDateTime uint32
}

func filetimeValue(f windowsFiletime) uint64 {
	return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime)
}

func hostCPU() (idle, total uint64, ok bool) {
	var idleTime, kernelTime, userTime windowsFiletime
	r, _, _ := getSystemTimesHostStats.Call(
		uintptr(unsafe.Pointer(&idleTime)),
		uintptr(unsafe.Pointer(&kernelTime)),
		uintptr(unsafe.Pointer(&userTime)),
	)
	if r == 0 {
		return 0, 0, false
	}
	idle = filetimeValue(idleTime)
	total = filetimeValue(kernelTime) + filetimeValue(userTime)
	return idle, total, true
}

type windowsMemoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func hostMemory() (total, available uint64) {
	var status windowsMemoryStatusEx
	status.Length = uint32(unsafe.Sizeof(status))
	r, _, _ := globalMemoryStatusExHostStats.Call(uintptr(unsafe.Pointer(&status)))
	if r == 0 {
		return 0, 0
	}
	return status.TotalPhys, status.AvailPhys
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
		path = "."
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	var available, total, free uint64
	if windows.GetDiskFreeSpaceEx(name, &available, &total, &free) != nil {
		return 0
	}
	return available
}
