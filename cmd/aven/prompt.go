package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
)

// stdinReader is shared by every prompt: separate bufio.Readers on os.Stdin would each buffer ahead and
// lose input piped for the next prompt (password, then 2FA code).
var stdinReader = bufio.NewReader(os.Stdin)

func readLine() (string, error) {
	s, err := stdinReader.ReadString('\n')
	if err != nil && (s == "" || !errors.Is(err, io.EOF)) {
		return strings.TrimRight(s, "\r\n"), err
	}
	return strings.TrimRight(s, "\r\n"), nil
}

// interactive reports whether we can ask the user something.
func interactive() bool { return isTerminal(os.Stdin) && isTerminal(os.Stderr) }

// readSecret reads one line without echo on a terminal. Ctrl+C restores the terminal before exiting
// (the old stty-based prompt left echo off when interrupted, and did not hide input on Windows).
func readSecret() string {
	if isTerminal(os.Stdin) {
		if restore, err := disableEcho(os.Stdin); err == nil {
			sig := make(chan os.Signal, 1)
			signal.Notify(sig, os.Interrupt)
			done := make(chan struct{})
			go func() {
				select {
				case <-sig:
					restore()
					fmt.Fprintln(os.Stderr)
					os.Exit(exitInterrupted)
				case <-done:
				}
			}()
			defer func() {
				signal.Stop(sig)
				close(done)
				restore()
			}()
		}
	}
	s, _ := readLine()
	return s
}

// pick asks the user to choose one of options on a terminal (numbered list, or type a name).
// allowNone adds a "0) none" entry and returns -1 for it.
func pick(prompt string, options []string, allowNone bool) (int, error) {
	fmt.Fprintln(os.Stderr, prompt)
	if allowNone {
		fmt.Fprintf(os.Stderr, "  %s none\n", errOut(styleDim, " 0)"))
	}
	for i, o := range options {
		fmt.Fprintf(os.Stderr, "  %s %s\n", errOut(stylePurple, fmt.Sprintf("%2d)", i+1)), o)
	}
	for attempt := 0; attempt < 3; attempt++ {
		fmt.Fprint(os.Stderr, "choose a number or name: ")
		s, err := readLine()
		if err != nil {
			return 0, err
		}
		if i, ok := pickAnswer(strings.TrimSpace(s), options, allowNone); ok {
			return i, nil
		}
		fmt.Fprintln(os.Stderr, "not a valid choice")
	}
	return 0, errors.New("no valid choice")
}

func pickAnswer(s string, options []string, allowNone bool) (int, bool) {
	if s == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(s); err == nil {
		switch {
		case n == 0 && allowNone:
			return -1, true
		case n >= 1 && n <= len(options):
			return n - 1, true
		}
		return 0, false
	}
	for i, o := range options {
		if o == s {
			return i, true
		}
	}
	return 0, false
}
