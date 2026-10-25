//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"
)

type devTestLogWriter chan string

func (w devTestLogWriter) Write(p []byte) (int, error) {
	w <- string(p)
	return len(p), nil
}

func TestDevShutdownChild(t *testing.T) {
	if os.Getenv("KEEL_DEV_SHUTDOWN_CHILD") != "1" {
		return
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM)
	fmt.Println("ready")
	<-stop
	fmt.Println("stopped")
	os.Exit(0)
}

func TestDevProcessesStopChildrenGracefully(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	logs := make(devTestLogWriter, 16)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	spec := devProcessSpec{
		service: "local",
		program: exe,
		args:    []string{"-test.run=^TestDevShutdownChild$"},
		env:     append(os.Environ(), "KEEL_DEV_SHUTDOWN_CHILD=1"),
	}
	go func() { done <- runDevProcesses(ctx, []devProcessSpec{spec}, "", "", logs, false, nil) }()

	var output strings.Builder
	ready := false
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for !ready {
		select {
		case line := <-logs:
			output.WriteString(line)
			ready = strings.Contains(output.String(), "local | ready")
		case <-timer.C:
			cancel()
			t.Fatal("child did not start")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runDevProcesses: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("graceful shutdown timed out")
	}
	for {
		select {
		case line := <-logs:
			output.WriteString(line)
		default:
			if !strings.Contains(output.String(), "local | stopped") {
				t.Fatalf("child was not stopped gracefully; logs: %q", output.String())
			}
			return
		}
	}
}
