//go:build windows

package main

import (
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func isTerminal(f *os.File) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) == nil
}

// makeRaw는 콘솔 입력을 raw + VT 입력으로, 출력은 VT 처리(ANSI)로 바꾼다. 돌려준 함수로 되돌린다.
func makeRaw(f *os.File) (func(), error) {
	in := windows.Handle(f.Fd())
	var oldIn uint32
	if err := windows.GetConsoleMode(in, &oldIn); err != nil {
		return nil, err
	}
	raw := oldIn &^ (windows.ENABLE_ECHO_INPUT | windows.ENABLE_PROCESSED_INPUT | windows.ENABLE_LINE_INPUT)
	raw |= windows.ENABLE_VIRTUAL_TERMINAL_INPUT
	if err := windows.SetConsoleMode(in, raw); err != nil {
		return nil, err
	}
	out := windows.Handle(os.Stdout.Fd())
	var oldOut uint32
	outOK := windows.GetConsoleMode(out, &oldOut) == nil
	if outOK {
		windows.SetConsoleMode(out, oldOut|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING|windows.DISABLE_NEWLINE_AUTO_RETURN)
	}
	return func() {
		windows.SetConsoleMode(in, oldIn)
		if outOK {
			windows.SetConsoleMode(out, oldOut)
		}
	}, nil
}

func termSize(f *os.File) (cols, rows int, ok bool) {
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(windows.Handle(f.Fd()), &info); err != nil {
		return 0, 0, false
	}
	return int(info.Window.Right-info.Window.Left) + 1, int(info.Window.Bottom-info.Window.Top) + 1, true
}

// watchResize는 Windows에 SIGWINCH가 없어서 500ms마다 크기를 본다.
func watchResize(f func()) func() {
	done := make(chan struct{})
	go func() {
		lw, lh, _ := termSize(os.Stdout)
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if w, h, ok := termSize(os.Stdout); ok && (w != lw || h != lh) {
					lw, lh = w, h
					f()
				}
			}
		}
	}()
	return func() { close(done) }
}
