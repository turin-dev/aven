//go:build windows

package main

import (
	"os"
	"os/exec"
)

func configureDevProcess(*exec.Cmd) {}

func terminateDevProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Signal(os.Interrupt)
}

func killDevProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func devSignals() []os.Signal { return []os.Signal{os.Interrupt} }
