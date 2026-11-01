package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"
)

// logEntry is one runtime log line of the deploy-logs stream (deploy.LogEntry on the server).
type logEntry struct {
	T       string `json:"t"`
	Replica string `json:"replica"`
	Stream  string `json:"stream"`
	Msg     string `json:"msg"`
}

type logsOpts struct {
	build              bool
	lines              int
	depID, filter      string
	replica, stream    string
	errorsOnly, asJSON bool
}

func cmdLogs(c *config, args []string) {
	if kind := netLogKind(args); kind != "" {
		cmdNetLogs(c, kind, args) // --dns / --network: networklogs.go
		return
	}
	fl := newFlagSet("logs")
	var o logsOpts
	fl.BoolVar(&o.build, "b", false, "build logs of the latest deployment")
	fl.BoolVar(&o.build, "build", false, "build logs of the latest deployment")
	fl.IntVar(&o.lines, "n", 200, "lines of history")
	fl.IntVar(&o.lines, "lines", 200, "lines of history")
	fl.StringVar(&o.depID, "deployment", "", "deployment id (default: current / latest)")
	fl.StringVar(&o.filter, "filter", "", `filter: words, "phrase", -exclude`)
	fl.StringVar(&o.filter, "f", "", `filter: words, "phrase", -exclude`)
	fl.StringVar(&o.replica, "replica", "", "only this replica (name or prefix)")
	fl.StringVar(&o.replica, "r", "", "only this replica (name or prefix)")
	fl.StringVar(&o.stream, "stream", "", "only stdout or stderr")
	fl.BoolVar(&o.errorsOnly, "errors", false, "only error lines (stderr, or containing error/fatal/panic)")
	fl.BoolVar(&o.asJSON, "json", false, "print newline-delimited JSON")
	tf := addTargetFlags(fl)
	fl.Parse(args)
	if fl.NArg() > 0 {
		die("unexpected argument %q (filters go in --filter)\nusage: aven logs [--build] [--deployment id] [--filter q] [-n lines] [--errors] [--replica r] [--stream stdout|stderr] [--json]", fl.Arg(0))
	}
	if o.stream != "" && o.stream != "stdout" && o.stream != "stderr" {
		die("--stream must be stdout or stderr\nusage: aven logs --stream stdout|stderr")
	}
	if o.build && (o.replica != "" || o.stream != "" || o.errorsOnly) {
		die("--replica, --stream and --errors apply to runtime logs, not --build\nusage: aven logs --build [--deployment id] [--filter q]")
	}
	l := tf.service(c)
	base := l.instPath()
	// Ctrl+C ends the stream quietly (exit 130); a broken pipe (aven logs | head) ends it too.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if o.build {
		id := o.depID
		if id == "" {
			var deps []deployment
			if err := c.call("GET", base+"/deployments?limit=1", nil, &deps); err != nil {
				die("%v", err)
			}
			if len(deps) == 0 {
				die("no deployments yet (deploy with: aven up)")
			}
			id = deps[0].ID
		}
		match := buildFilter(o.filter)
		w := &filterWriter{w: os.Stdout, match: match, json: o.asJSON}
		if _, err := followBuild(ctx, c, id, w, os.Stderr); err != nil {
			die("%v", err)
		}
		exitIfInterrupted(ctx)
		return
	}
	id := o.depID
	if id == "" {
		var inst struct {
			CurrentDeploymentID *string `json:"current_deployment_id"`
		}
		if err := c.call("GET", base, nil, &inst); err != nil {
			die("%v", err)
		}
		if inst.CurrentDeploymentID == nil {
			die("%s has no live deployment\nhint: see the latest build with aven logs --build, or deploy with aven up", l.Service)
		}
		id = *inst.CurrentDeploymentID
	}
	q := url.Values{"tail": {fmt.Sprint(o.lines)}}
	if o.filter != "" {
		q.Set("q", o.filter)
	}
	if o.errorsOnly {
		q.Set("level", "error")
	}
	path := "/deployments/" + url.PathEscape(id) + "/deploy-logs?" + q.Encode()
	if err := followDeployLogs(ctx, c, path, o, os.Stdout, os.Stderr); err != nil {
		die("%v", err)
	}
	exitIfInterrupted(ctx)
}

func exitIfInterrupted(ctx context.Context) {
	if ctx.Err() != nil {
		os.Exit(exitInterrupted)
	}
}

// filterWriter prints build log lines that match (as JSON objects with --json).
type filterWriter struct {
	w     io.Writer
	match func(string) bool
	json  bool
}

func (f *filterWriter) Write(p []byte) (int, error) {
	line := strings.TrimSuffix(string(p), "\n")
	if !f.match(line) {
		return len(p), nil
	}
	if f.json {
		b, _ := json.Marshal(map[string]string{"msg": line})
		_, err := fmt.Fprintln(f.w, string(b))
		return len(p), err
	}
	_, err := fmt.Fprintln(f.w, line)
	return len(p), err
}

// logDedup remembers the newest printed timestamp so a reconnect, which replays the tail, does not
// print lines twice.
type logDedup struct {
	last time.Time
	at   map[string]bool // lines printed at exactly last
}

func (d *logDedup) fresh(e logEntry) bool {
	t, err := time.Parse(time.RFC3339Nano, e.T)
	if err != nil {
		return true
	}
	key := e.Replica + "\x00" + e.Stream + "\x00" + e.Msg
	switch {
	case t.Before(d.last):
		return false
	case t.Equal(d.last):
		if d.at[key] {
			return false
		}
	default:
		d.last, d.at = t, map[string]bool{}
	}
	d.at[key] = true
	return true
}

// followDeployLogs streams runtime logs, reconnecting (without duplicates) when the connection drops.
func followDeployLogs(ctx context.Context, c *config, path string, o logsOpts, w, notes io.Writer) error {
	var dedup logDedup
	colors := map[string]style{}
	for attempt := 0; ; attempt++ {
		got := 0
		var writeErr error
		stopped, err := c.stream(ctx, path, streamIdle, func(ev, data string) bool {
			if ev == "done" {
				return false
			}
			if ev != "log" {
				return true
			}
			got++
			var e logEntry
			if json.Unmarshal([]byte(data), &e) != nil || !dedup.fresh(e) || !keepLog(e, o) {
				return true
			}
			if o.asJSON {
				_, writeErr = fmt.Fprintln(w, data)
			} else {
				_, writeErr = fmt.Fprintln(w, formatLogLine(e, colors))
			}
			return writeErr == nil
		})
		if writeErr != nil || stopped || ctx.Err() != nil {
			return nil // done, interrupted, or stdout closed (aven logs | head)
		}
		if !retryable(err) {
			return err
		}
		if got > 0 {
			attempt = 0
		}
		if err != nil && !errors.Is(err, errStreamIdle) {
			fmt.Fprintf(notes, "%s log stream interrupted (%v); reconnecting...\n", errOut(styleYellow, "aven:"), firstLine(err.Error()))
		}
		if !sleepCtx(ctx, backoff(attempt)) {
			return nil
		}
	}
}

// keepLog applies the client-side filters (--replica, --stream).
func keepLog(e logEntry, o logsOpts) bool {
	if o.replica != "" && e.Replica != o.replica && !strings.HasPrefix(e.Replica, o.replica) &&
		!strings.HasSuffix(e.Replica, "-"+o.replica) {
		return false
	}
	return o.stream == "" || e.Stream == o.stream
}

var replicaColors = []style{styleCyan, stylePurple, styleBlue, styleGreen, styleYellow}

// formatLogLine is "15:04:05 replica message", with a stable color per replica on a terminal.
func formatLogLine(e logEntry, colors map[string]style) string {
	ts := e.T
	if t, err := time.Parse(time.RFC3339Nano, e.T); err == nil {
		ts = t.Local().Format("15:04:05")
	}
	st, ok := colors[e.Replica]
	if !ok {
		h := fnv.New32a()
		h.Write([]byte(e.Replica))
		st = replicaColors[h.Sum32()%uint32(len(replicaColors))]
		colors[e.Replica] = st
	}
	return out(styleDim, ts) + " " + out(st, e.Replica) + " " + e.Msg
}
