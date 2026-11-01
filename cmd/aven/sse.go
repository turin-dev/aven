package main

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

// errStreamIdle means the server sent nothing (not even a keepalive) for too long; the caller reconnects.
var errStreamIdle = errors.New("no data from the server")

// streamIdle is how long a log stream may stay silent. The server sends a keepalive every 15 s while it
// streams; a queued deployment can be silent longer, which only causes a quiet reconnect.
var streamIdle = 60 * time.Second

// sse reads an SSE stream and passes (event, data) to f until f returns false or the stream ends.
func (c *config) sse(path string, f func(event, data string) bool) error {
	_, err := c.stream(context.Background(), path, 0, f)
	return err
}

// stream reads one SSE connection. stopped is true when f returned false. With idle > 0 the connection
// is dropped with errStreamIdle when no line arrives for that long.
func (c *config) stream(ctx context.Context, path string, idle time.Duration, f func(event, data string) bool) (stopped bool, err error) {
	if c.URL == "" {
		return false, errors.New("not logged in (run: aven login <url>)")
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	r, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(c.URL, "/")+"/api"+path, nil)
	if err != nil {
		return false, err
	}
	r.Header.Set("Accept", "text/event-stream")
	r.Header.Set("User-Agent", userAgent())
	c.authorize(r)
	var timer *time.Timer
	if idle > 0 {
		timer = time.AfterFunc(idle, func() { cancel(errStreamIdle) })
		defer timer.Stop()
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		if errors.Is(context.Cause(ctx), errStreamIdle) {
			return false, errStreamIdle
		}
		return false, netHint(c.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return false, responseError("GET", path, resp)
	}
	stopped, err = parseSSE(resp.Body, func() {
		if timer != nil {
			timer.Reset(idle)
		}
	}, f)
	if cause := context.Cause(ctx); errors.Is(cause, errStreamIdle) {
		return stopped, errStreamIdle
	}
	return stopped, err
}

// parseSSE implements the text/event-stream framing the server uses: "event:" and "data:" lines, a blank
// line ends an event, ":" lines are keepalives. activity is called for every line read.
func parseSSE(body interface{ Read([]byte) (int, error) }, activity func(), f func(event, data string) bool) (bool, error) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	event, data, has := "", []string(nil), false
	dispatch := func() bool {
		if !has {
			return true
		}
		ok := f(event, strings.Join(data, "\n"))
		event, data, has = "", nil, false
		return ok
	}
	for sc.Scan() {
		activity()
		l := sc.Text()
		switch {
		case l == "":
			if !dispatch() {
				return true, nil
			}
		case strings.HasPrefix(l, ":"):
		case strings.HasPrefix(l, "event:"):
			event = strings.TrimPrefix(strings.TrimPrefix(l, "event:"), " ")
		case strings.HasPrefix(l, "data:"):
			data, has = append(data, strings.TrimPrefix(strings.TrimPrefix(l, "data:"), " ")), true
		}
	}
	if !dispatch() {
		return true, nil
	}
	return false, sc.Err()
}

// retryBase is the first reconnect delay (tests shorten it).
var retryBase = time.Second

// backoff is the wait before reconnect attempt n (1, 2, 4, ... capped at 15 s).
func backoff(n int) time.Duration {
	d := retryBase << min(n, 4)
	return min(d, 15*retryBase)
}

// sleepCtx waits d or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// retryable tells whether a stream error is worth reconnecting for (network trouble, idle, 5xx),
// as opposed to auth or not-found errors that will not go away.
func retryable(err error) bool {
	if err == nil {
		return true // the server closed the stream without "done" (restart, proxy timeout)
	}
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Status >= 500 || ae.Status == 429
	}
	return !strings.Contains(err.Error(), "unauthorized") && !strings.Contains(err.Error(), "two-factor")
}
