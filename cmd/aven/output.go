package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
)

// Exit codes. Scripts can rely on them (docs/cli.md, "Exit codes").
const (
	exitOK          = 0
	exitError       = 1   // the command failed (API error, deployment failed, ...)
	exitUsage       = 2   // bad command line (unknown command or flag, missing argument)
	exitInterrupted = 130 // Ctrl+C (128 + SIGINT)
)

// ---- colors ----

// colorEnabled decides whether f gets ANSI colors: never with NO_COLOR (https://no-color.org) or TERM=dumb,
// always with FORCE_COLOR/CLICOLOR_FORCE (not "0"), otherwise only on a terminal.
func colorEnabled(f *os.File) bool {
	return colorDecision(os.Getenv, f != nil && isTerminal(f))
}

func colorDecision(getenv func(string) string, tty bool) bool {
	if getenv("NO_COLOR") != "" {
		return false
	}
	for _, k := range []string{"FORCE_COLOR", "CLICOLOR_FORCE"} {
		if v := getenv(k); v != "" && v != "0" && v != "false" {
			return true
		}
	}
	if getenv("TERM") == "dumb" {
		return false
	}
	return tty
}

var (
	colorOnce             sync.Once
	stdoutColor, errColor bool
)

func initColors() {
	colorOnce.Do(func() {
		// A forced color on a pipe needs no console mode; a console needs VT processing (Windows).
		stdoutColor = colorEnabled(os.Stdout) && (!isTerminal(os.Stdout) || enableANSI(os.Stdout))
		errColor = colorEnabled(os.Stderr) && (!isTerminal(os.Stderr) || enableANSI(os.Stderr))
	})
}

type style string

const (
	styleBold   style = "1"
	styleDim    style = "2"
	styleRed    style = "31"
	styleGreen  style = "32"
	styleYellow style = "33"
	styleBlue   style = "34"
	stylePurple style = "35"
	styleCyan   style = "36"
)

func paint(on bool, s style, text string) string {
	if !on || text == "" {
		return text
	}
	return "\x1b[" + string(s) + "m" + text + "\x1b[0m"
}

// out / errOut color text for stdout / stderr.
func out(s style, text string) string    { initColors(); return paint(stdoutColor, s, text) }
func errOut(s style, text string) string { initColors(); return paint(errColor, s, text) }

// statusStyle colors deployment, instance, domain and replica states consistently.
func statusStyle(status string) style {
	switch strings.ToLower(status) {
	case "live", "running", "active", "ok", "online", "healthy", "success", "verified", "enabled", "on":
		return styleGreen
	case "failed", "crashed", "error", "offline", "exited", "unhealthy", "mismatch", "refused", "timeout":
		return styleRed
	case "queued", "waiting", "initializing", "building", "pre_deploy", "deploying", "healthchecking",
		"starting", "restarting", "pending", "pending_dns", "sleeping", "proxied":
		return styleYellow
	}
	return styleDim
}

func outStatus(status string) string { return out(statusStyle(status), status) }

// pad right-pads a (possibly colored) text to width, measured on its plain form.
func pad(colored, plain string, width int) string {
	if n := width - len([]rune(plain)); n > 0 {
		return colored + strings.Repeat(" ", n)
	}
	return colored
}

// ---- messages ----

// warnf prints a non-fatal message to stderr.
func warnf(format string, a ...any) {
	fmt.Fprintln(os.Stderr, errOut(styleYellow, "aven:")+" "+fmt.Sprintf(format, a...))
}

// die prints the error to stderr and exits: 2 when the message is a usage text, 1 otherwise.
func die(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	fmt.Fprintln(os.Stderr, errOut(styleRed, "aven:")+" "+msg)
	os.Exit(exitCodeFor(msg))
}

// dieUsage reports a command line mistake (exit 2).
func dieUsage(format string, a ...any) {
	fmt.Fprintln(os.Stderr, errOut(styleRed, "aven:")+" "+fmt.Sprintf(format, a...))
	os.Exit(exitUsage)
}

func exitCodeFor(msg string) int {
	if strings.HasPrefix(msg, "usage:") || strings.Contains(msg, "\nusage:") {
		return exitUsage
	}
	return exitError
}

// ---- errors with next steps ----

// apiError is a non-2xx answer of the API.
type apiError struct {
	Method, Path string
	Status       int
	Msg          string
}

func (e *apiError) Error() string {
	s := fmt.Sprintf("%s %s: %d", e.Method, e.Path, e.Status)
	if e.Msg != "" {
		s += " " + e.Msg
	}
	if h := statusHint(e.Status, e.Msg); h != "" {
		s += "\n" + h
	}
	return s
}

// statusHint suggests what to do next for common API failures.
func statusHint(status int, msg string) string {
	switch status {
	case 403:
		if msg == "mfa_setup_required" {
			return ""
		}
		return "hint: your role cannot do this here; ask a project or workspace admin, or check the token scope (aven whoami)"
	case 404:
		return "hint: it may have been deleted or you may not have access; check the link with aven status, or relink with aven link"
	case 409:
		return ""
	case 413:
		return "hint: the request is too large for the server (or a proxy in front of it)"
	case 429:
		return "hint: rate limited; wait a moment and retry"
	case 502, 503, 504:
		return "hint: the server or its gateway is unavailable; retry in a moment"
	}
	return ""
}

// netHint wraps transport errors (server down, DNS, TLS) with the server URL and a next step.
func netHint(base string, err error) error {
	if err == nil {
		return nil
	}
	var ue *url.Error
	if errors.As(err, &ue) && ue.Timeout() {
		return fmt.Errorf("%s did not answer in time: %w\nhint: check the URL (aven whoami) and your network", base, err)
	}
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr):
		return fmt.Errorf("cannot resolve %s: %v\nhint: check the server URL (KEEL_URL or aven login <url>)", base, dnsErr)
	case errors.Is(err, syscall.ECONNREFUSED):
		return fmt.Errorf("cannot connect to %s: connection refused\nhint: is aven-server running and is the URL right? (aven login <url>)", base)
	case strings.Contains(err.Error(), "certificate"):
		return fmt.Errorf("TLS error talking to %s: %w\nhint: the server certificate is not trusted on this machine", base, err)
	}
	return err
}

// levenshtein is the edit distance used for "did you mean" suggestions.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// suggest returns the closest candidate to s (distance ≤ 2, or a prefix match), or "".
func suggest(s string, candidates []string) string {
	best, bestD := "", 3
	for _, c := range candidates {
		if len(s) >= 2 && strings.HasPrefix(c, s) {
			return c
		}
		if d := levenshtein(s, c); d < bestD {
			best, bestD = c, d
		}
	}
	return best
}
