package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/turin-dev/aven/internal/watch"
)

// maxUpload matches the server's limit for `aven up` sources (MaxBytesReader in the upload handler).
const maxUpload = 512 << 20

// skipDirs are never uploaded.
var skipDirs = map[string]bool{".git": true, "node_modules": true, ".next": true, "__pycache__": true, ".venv": true}

// ignoreFiles hold extra upload exclusions (gitignore syntax) in the uploaded directory.
var ignoreFiles = []string{".avenignore", ".keelignore", ".railwayignore"}

type deployment struct {
	ID     string  `json:"id"`
	Status string  `json:"status"`
	Error  *string `json:"error"`
}

// uploadIgnore reads the exclusion patterns of dir: .gitignore (unless disabled) and the aven ignore files.
// It returns the patterns and the files they came from.
func uploadIgnore(dir string, gitignore bool) (patterns, sources []string) {
	files := ignoreFiles
	if gitignore {
		files = append([]string{".gitignore"}, files...)
	}
	for _, name := range files {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		sources = append(sources, name)
		for _, l := range strings.Split(string(b), "\n") {
			l = strings.TrimRight(l, "\r")
			if strings.TrimSpace(l) == "" || strings.HasPrefix(l, "#") {
				continue
			}
			patterns = append(patterns, strings.TrimSpace(l))
		}
	}
	return patterns, sources
}

// packStats describes what tarDir packed.
type packStats struct {
	Files, Ignored int
	Bytes          int64 // uncompressed file bytes
}

// tarDir writes dir as tar.gz to w, skipping skipDirs and files matched by patterns (gitignore syntax,
// relative to dir). Directories are pruned only when no "!" pattern could bring files back.
func tarDir(dir string, w io.Writer, patterns []string) (packStats, error) {
	var st packStats
	negation := false
	for _, p := range patterns {
		negation = negation || strings.HasPrefix(p, "!")
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() && skipDirs[d.Name()] {
			return filepath.SkipDir
		}
		if len(patterns) > 0 && watch.MatchFile(patterns, rel) {
			if !d.IsDir() {
				st.Ignored++
				return nil
			}
			if !negation {
				st.Ignored++
				return filepath.SkipDir
			}
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, _ = os.Readlink(p)
		} else if !info.Mode().IsRegular() && !info.IsDir() {
			return nil // sockets, devices, pipes
		}
		h, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		h.Name = rel
		h.Uname, h.Gname = "", "" // local account names mean nothing on the server
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			n, err := io.Copy(tw, f)
			f.Close()
			st.Files++
			st.Bytes += n
			return err
		}
		return nil
	})
	if err != nil {
		return st, err
	}
	if err := tw.Close(); err != nil {
		return st, err
	}
	return st, gz.Close()
}

func cmdUp(c *config, args []string) {
	fl := newFlagSet("up")
	detach := fl.Bool("d", false, "don't follow build logs")
	fl.BoolVar(detach, "detach", false, "don't follow build logs")
	noGitignore := fl.Bool("no-gitignore", false, "also upload files matched by .gitignore")
	asJSON := fl.Bool("json", false, "print the deployment as JSON (no log output)")
	tf := addTargetFlags(fl)
	fl.Parse(reorderFlags(args))
	if fl.NArg() > 1 {
		die("usage: aven up [dir] [-d] [--no-gitignore] [--json] [-s service] [-e env]")
	}
	dir, _ := os.Getwd()
	if fl.NArg() == 1 {
		dir, _ = filepath.Abs(fl.Arg(0))
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			die("%s is not a directory", fl.Arg(0))
		}
	}
	l := tf.service(c)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	patterns, sources := uploadIgnore(dir, !*noGitignore)
	tmp, err := os.CreateTemp("", "aven-up-*.tar.gz")
	if err != nil {
		die("pack: %v", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	st, err := tarDir(dir, tmp, patterns)
	if err != nil {
		os.Remove(tmp.Name())
		die("pack %s: %v", dir, err)
	}
	size, _ := tmp.Seek(0, io.SeekCurrent)
	if size > maxUpload {
		os.Remove(tmp.Name())
		die("the packed directory is %s, over the server limit of %s\nhint: exclude build output and data with a .avenignore file (gitignore syntax)", humanBytes(size), humanBytes(maxUpload))
	}
	if !*asJSON {
		note := ""
		if st.Ignored > 0 {
			note = fmt.Sprintf(", %d ignored by %s", st.Ignored, strings.Join(sources, ", "))
		}
		fmt.Fprintf(os.Stderr, "%s %d files (%s compressed%s) to %s/%s\n", errOut(stylePurple, "==> uploading"),
			st.Files, humanBytes(size), note, l.Environment, out(styleBold, l.Service))
	}
	d, err := uploadSource(ctx, c, l, tmp, size, !*asJSON && isTerminal(os.Stderr))
	os.Remove(tmp.Name())
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "\naven: upload cancelled; nothing was deployed")
			os.Exit(exitInterrupted)
		}
		die("upload failed: %v", err)
	}
	if *asJSON && *detach {
		printJSON(d)
		return
	}
	if !*asJSON {
		fmt.Println("deployment", d.ID) // stdout, as before: scripts read the id from here
		if base := strings.TrimRight(c.URL, "/"); base != "" {
			fmt.Fprintf(os.Stderr, "    %s\n", errOut(styleDim, dashboardURL(base, l)))
		}
	}
	if *detach {
		fmt.Fprintf(os.Stderr, "follow it with: aven logs --build --deployment %s\n", d.ID)
		return
	}
	var logs io.Writer = os.Stdout
	if *asJSON {
		logs = io.Discard
	}
	final, err := followBuild(ctx, c, d.ID, logs, os.Stderr)
	if ctx.Err() != nil {
		fmt.Fprintf(os.Stderr, "\naven: stopped following; the deployment continues on the server\n      follow again: aven logs --build --deployment %s\n", d.ID)
		os.Exit(exitInterrupted)
	}
	if err != nil {
		die("%v\nfollow again: aven logs --build --deployment %s", err, d.ID)
	}
	if final == "" {
		var cur deployment
		if c.call("GET", "/deployments/"+url.PathEscape(d.ID), nil, &cur) == nil {
			final = cur.Status
			d = cur
		}
	}
	if *asJSON {
		d.Status = final
		printJSON(d)
	} else {
		fmt.Println("status:", out(statusStyle(final), final))
	}
	if final != "live" {
		if !*asJSON {
			fmt.Fprintf(os.Stderr, "hint: see the build log with aven logs --build --deployment %s, runtime logs with aven logs --deployment %s\n", d.ID, d.ID)
		}
		os.Exit(exitError)
	}
}

// uploadSource posts the packed source as multipart with a known length, showing progress on a terminal.
func uploadSource(ctx context.Context, c *config, l *link, src *os.File, size int64, progress bool) (deployment, error) {
	var d deployment
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return d, err
	}
	var head bytes.Buffer
	mw := multipart.NewWriter(&head)
	if _, err := mw.CreateFormFile("source", "source.tar.gz"); err != nil {
		return d, err
	}
	headLen := head.Len()
	mw.Close()
	tail := append([]byte(nil), head.Bytes()[headLen:]...)
	head.Truncate(headLen)
	var sent atomic.Int64
	body := io.MultiReader(&head, &countingReader{r: src, n: &sent}, bytes.NewReader(tail))
	r, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.URL, "/")+"/api"+l.instPath()+"/upload", body)
	if err != nil {
		return d, err
	}
	r.ContentLength = int64(head.Len()) + size + int64(len(tail))
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("User-Agent", userAgent())
	c.authorize(r)
	stopProgress := func() {}
	if progress {
		stopProgress = showProgress(&sent, size)
	}
	resp, err := http.DefaultClient.Do(r)
	stopProgress()
	if err != nil {
		return d, netHint(c.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return d, responseError("POST", l.instPath()+"/upload", resp)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d); err != nil || d.ID == "" {
		return d, fmt.Errorf("unexpected upload response (%v)", err)
	}
	return d, nil
}

type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// showProgress redraws "uploading 42% (1.2 MiB / 3.0 MiB)" on stderr until the returned func is called.
func showProgress(sent *atomic.Int64, total int64) func() {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(150 * time.Millisecond)
		defer t.Stop()
		for {
			n := sent.Load()
			pct := 100
			if total > 0 {
				pct = int(n * 100 / total)
			}
			fmt.Fprintf(os.Stderr, "\r    %3d%% %s / %s ", pct, humanBytes(n), humanBytes(total))
			select {
			case <-done:
				fmt.Fprint(os.Stderr, "\r\x1b[K")
				return
			case <-t.C:
			}
		}
	}()
	return func() { close(done); <-finished }
}

func terminalStatus(s string) bool {
	switch s {
	case "live", "failed", "crashed", "cancelled", "removed", "skipped":
		return true
	}
	return false
}

// followBuild streams the build/deploy log of a deployment to logs until it finishes and returns the
// final status. Dropped connections are retried; the server replays the whole log on every connection,
// so lines that were already printed are skipped.
func followBuild(ctx context.Context, c *config, id string, logs, notes io.Writer) (string, error) {
	printed, final, status := 0, "", ""
	path := "/deployments/" + url.PathEscape(id) + "/build-logs"
	for attempt := 0; ; attempt++ {
		seen := 0
		stopped, err := c.stream(ctx, path, streamIdle, func(ev, data string) bool {
			switch ev {
			case "log":
				seen++
				if seen > printed {
					fmt.Fprintln(logs, data)
					printed = seen
				}
			case "status":
				if data != status && !terminalStatus(data) {
					fmt.Fprintf(notes, "%s %s\n", errOut(stylePurple, "==>"), errOut(statusStyle(data), data))
				}
				status = data
			case "done":
				final = data
				return false
			}
			return true
		})
		if stopped || ctx.Err() != nil {
			return final, nil
		}
		if !retryable(err) {
			return "", err
		}
		if seen > 0 {
			attempt = 0 // the connection worked for a while: start the backoff over
		}
		if err != nil && !errors.Is(err, errStreamIdle) {
			fmt.Fprintf(notes, "%s log stream interrupted (%v); reconnecting...\n", errOut(styleYellow, "aven:"), firstLine(err.Error()))
		}
		if attempt >= 8 {
			return "", fmt.Errorf("lost the log stream: %v", err)
		}
		if !sleepCtx(ctx, backoff(attempt)) {
			return final, nil
		}
	}
}
