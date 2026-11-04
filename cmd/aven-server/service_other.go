//go:build !windows

package main

import (
	"context"
	"io"
	"os"
)

func runWindowsService(func(context.Context) error) (bool, error) { return false, nil }
func loadServerEnv() error                                        { return nil }
func serverLogOutput() (io.Writer, func(), error)                 { return os.Stderr, func() {}, nil }
