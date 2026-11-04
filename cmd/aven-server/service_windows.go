//go:build windows

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/turin-dev/aven/internal/compat"
	"github.com/turin-dev/aven/internal/selfhost"
	"golang.org/x/sys/windows/svc"
)

func loadServerEnv() error {
	path := compat.DefaultEnvFile()
	f, err := selfhost.ReadEnvFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for key, value := range f.Values() {
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return nil
}

func serverLogOutput() (io.Writer, func(), error) {
	isService, err := svc.IsWindowsService()
	if err != nil || !isService {
		return os.Stderr, func() {}, err
	}
	root := os.Getenv("ProgramData")
	if root == "" {
		root = `C:\ProgramData`
	}
	dir := filepath.Join(root, "Aven", "logs")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, func() {}, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "aven-server.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, func() {}, err
	}
	return f, func() { _ = f.Close() }, nil
}

type windowsService struct {
	run func(context.Context) error
}

func (s windowsService) Execute(_ []string, requests <-chan svc.ChangeRequest, statuses chan<- svc.Status) (bool, uint32) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.run(ctx) }()

	status := svc.Status{State: svc.StartPending, WaitHint: 30000}
	statuses <- status
	status = svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	statuses <- status
	for {
		select {
		case req := <-requests:
			switch req.Cmd {
			case svc.Interrogate:
				statuses <- status
			case svc.Stop, svc.Shutdown:
				statuses <- svc.Status{State: svc.StopPending, WaitHint: 30000}
				cancel()
				if err := <-done; err != nil {
					slog.Error("aven-server stopped with an error", "err", err)
					return true, 1
				}
				return false, 0
			}
		case err := <-done:
			if err != nil {
				slog.Error("aven-server stopped with an error", "err", err)
				return true, 1
			}
			return false, 0
		}
	}
}

func runWindowsService(run func(context.Context) error) (bool, error) {
	isService, err := svc.IsWindowsService()
	if err != nil || !isService {
		return false, err
	}
	_ = os.Setenv("INVOCATION_ID", "windows-service")
	return true, svc.Run("AvenServer", windowsService{run: run})
}
