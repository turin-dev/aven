//go:build windows

package api

import "os"

func restartCurrentProcess() {
	// A nonzero service exit activates the Windows Service Control Manager recovery action.
	os.Exit(1)
}
