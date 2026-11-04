//go:build !windows

package api

import (
	"os"
	"syscall"
)

func restartCurrentProcess() {
	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
}
