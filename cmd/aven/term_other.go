//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly && !windows

package main

import (
	"errors"
	"os"
)

// 이 플랫폼에서는 raw 모드를 지원하지 않는다(줄 단위 모드로 동작).
func isTerminal(*os.File) bool           { return false }
func makeRaw(*os.File) (func(), error)   { return nil, errors.New("not supported on this platform") }
func termSize(*os.File) (int, int, bool) { return 0, 0, false }
func watchResize(func()) func()          { return func() {} }
