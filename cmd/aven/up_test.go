package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func init() { retryBase = 10 * time.Millisecond }

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func tarNames(t *testing.T, b []byte) []string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			names = append(names, h.Name)
		}
		if h.Uname != "" || h.Gname != "" {
			t.Errorf("%s: local user names leaked into the archive", h.Name)
		}
	}
	sort.Strings(names)
	return names
}

func TestTarDirRespectsIgnoreFiles(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"main.go":             "package main",
		".env":                "SECRET=1",
		"dist/app.js":         "built",
		"node_modules/x/i.js": "dep",
		"a.log":               "log",
		"keep.log":            "kept by negation",
		"secret.txt":          "s",
		"src/deep/b.log":      "log",
		".gitignore":          "# comment\n.env\ndist/\n*.log\n!keep.log\n",
		".avenignore":         "secret.txt\n",
	})
	patterns, sources := uploadIgnore(dir, true)
	if strings.Join(sources, ",") != ".gitignore,.avenignore" {
		t.Errorf("sources: %v", sources)
	}
	var buf bytes.Buffer
	st, err := tarDir(dir, &buf, patterns)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(tarNames(t, buf.Bytes()), " ")
	if got != ".avenignore .gitignore keep.log main.go" {
		t.Errorf("packed: %s", got)
	}
	if st.Files != 4 || st.Ignored != 5 {
		t.Errorf("stats: %+v", st)
	}

	// --no-gitignore: only the aven ignore files apply (node_modules is always skipped)
	patterns, _ = uploadIgnore(dir, false)
	buf.Reset()
	if _, err := tarDir(dir, &buf, patterns); err != nil {
		t.Fatal(err)
	}
	got = strings.Join(tarNames(t, buf.Bytes()), " ")
	if got != ".avenignore .env .gitignore a.log dist/app.js keep.log main.go src/deep/b.log" {
		t.Errorf("no-gitignore: %s", got)
	}
}

func TestTarDirPrunesIgnoredDirs(t *testing.T) {
	dir := writeTree(t, map[string]string{"app.py": "x", "data/big.bin": "x", "data/sub/more.bin": "x"})
	var buf bytes.Buffer
	st, err := tarDir(dir, &buf, []string{"data"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tarNames(t, buf.Bytes()), " "); got != "app.py" || st.Ignored != 1 {
		t.Errorf("packed %q, stats %+v", got, st)
	}
}

func TestParseSSE(t *testing.T) {
	in := ": ping\n\nevent: log\ndata: one\n\nevent: log\ndata: a\ndata: b\n\ndata: plain\n\nevent: done\ndata: live\n"
	var got []string
	lines := 0
	stopped, err := parseSSE(strings.NewReader(in), func() { lines++ }, func(ev, data string) bool {
		got = append(got, ev+"="+data)
		return ev != "done"
	})
	if err != nil || !stopped {
		t.Errorf("stopped %v err %v", stopped, err)
	}
	if strings.Join(got, "|") != "log=one|log=a\nb|=plain|done=live" {
		t.Errorf("events: %q", got)
	}
	if lines < 10 {
		t.Errorf("activity not reported per line: %d", lines)
	}
}

// buildLogServer serves build logs like the server: the whole history on every connection. The first
// connection drops after two lines.
func buildLogServer(t *testing.T, lines []string, final string) (*httptest.Server, *atomic.Int32) {
	var conns atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := conns.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for i, l := range lines {
			if n == 1 && i == 2 {
				fl.Flush()
				panic(http.ErrAbortHandler) // connection reset mid-stream
			}
			fmt.Fprintf(w, "event: log\ndata: %s\n\n", l)
		}
		fmt.Fprintf(w, "event: status\ndata: %s\n\nevent: done\ndata: %s\n\n", final, final)
	}))
	return srv, &conns
}

func TestFollowBuildReconnectsWithoutDuplicates(t *testing.T) {
	plainOutput(t)
	srv, conns := buildLogServer(t, []string{"step 1", "step 2", "step 3", "step 4"}, "live")
	defer srv.Close()
	t.Setenv("KEEL_TOKEN", "tok")
	c := &config{URL: srv.URL}
	var logs, notes bytes.Buffer
	final, err := followBuild(context.Background(), c, "dep_1", &logs, &notes)
	if err != nil || final != "live" {
		t.Fatalf("final %q err %v", final, err)
	}
	if logs.String() != "step 1\nstep 2\nstep 3\nstep 4\n" {
		t.Errorf("logs:\n%s", logs.String())
	}
	if conns.Load() != 2 || !strings.Contains(notes.String(), "reconnecting") {
		t.Errorf("conns %d notes %q", conns.Load(), notes.String())
	}
}

func TestFollowBuildStopsOnAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	c := &config{URL: srv.URL}
	_, err := followBuild(context.Background(), c, "dep_1", io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "aven login") {
		t.Errorf("err %v", err)
	}
}

func TestFollowBuildIdleReconnect(t *testing.T) {
	old := streamIdle
	streamIdle = 100 * time.Millisecond
	defer func() { streamIdle = old }()
	var conns atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: log\ndata: hello\n\n")
		w.(http.Flusher).Flush()
		if conns.Add(1) == 1 {
			<-r.Context().Done() // silent: no keepalive
			return
		}
		fmt.Fprint(w, "event: done\ndata: failed\n\n")
	}))
	defer srv.Close()
	c := &config{URL: srv.URL}
	var logs, notes bytes.Buffer
	final, err := followBuild(context.Background(), c, "dep_1", &logs, &notes)
	if err != nil || final != "failed" || logs.String() != "hello\n" {
		t.Errorf("final %q err %v logs %q", final, err, logs.String())
	}
	if notes.Len() != 0 {
		t.Errorf("an idle reconnect should be quiet: %q", notes.String())
	}
}

func TestFollowDeployLogsDedupAndFilters(t *testing.T) {
	plainOutput(t)
	entries := []logEntry{
		{T: "2026-01-02T10:00:00Z", Replica: "web-1", Stream: "stdout", Msg: "a"},
		{T: "2026-01-02T10:00:01Z", Replica: "web-2", Stream: "stderr", Msg: "b"},
		{T: "2026-01-02T10:00:01Z", Replica: "web-1", Stream: "stdout", Msg: "c"},
		{T: "2026-01-02T10:00:02Z", Replica: "web-1", Stream: "stderr", Msg: "d"},
	}
	var conns atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := conns.Add(1)
		if r.URL.Query().Get("tail") != "50" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		upto := len(entries)
		if n == 1 {
			upto = 3
		}
		for _, e := range entries[:upto] {
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "event: log\ndata: %s\n\n", b)
		}
		if n == 1 {
			return // closed without done: reconnect, the tail is replayed
		}
		fmt.Fprint(w, "event: done\ndata: \n\n")
	}))
	defer srv.Close()
	c := &config{URL: srv.URL}
	var out bytes.Buffer
	if err := followDeployLogs(context.Background(), c, "/deployments/dep_1/deploy-logs?tail=50", logsOpts{}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 || !strings.HasSuffix(lines[0], "web-1 a") || !strings.HasSuffix(lines[3], "web-1 d") {
		t.Errorf("deduped output:\n%s", out.String())
	}
	conns.Store(1) // every connection now ends with done
	out.Reset()
	if err := followDeployLogs(context.Background(), c, "/deployments/dep_1/deploy-logs?tail=50", logsOpts{replica: "1", stream: "stderr", asJSON: true}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != `{"t":"2026-01-02T10:00:02Z","replica":"web-1","stream":"stderr","msg":"d"}` {
		t.Errorf("filtered json:\n%s", out.String())
	}
}

func TestKeepLog(t *testing.T) {
	e := logEntry{Replica: "keel-web-2", Stream: "stdout"}
	for _, tc := range []struct {
		o    logsOpts
		want bool
	}{
		{logsOpts{}, true},
		{logsOpts{replica: "keel-web-2"}, true},
		{logsOpts{replica: "keel-web"}, true},
		{logsOpts{replica: "2"}, true},
		{logsOpts{replica: "3"}, false},
		{logsOpts{stream: "stderr"}, false},
		{logsOpts{stream: "stdout"}, true},
	} {
		if got := keepLog(e, tc.o); got != tc.want {
			t.Errorf("%+v: %v", tc.o, got)
		}
	}
}

func TestUploadSourceSendsMultipartWithLength(t *testing.T) {
	src := writeTree(t, map[string]string{"index.js": "console.log(1)"})
	var buf bytes.Buffer
	if _, err := tarDir(src, &buf, nil); err != nil {
		t.Fatal(err)
	}
	f, _ := os.CreateTemp(t.TempDir(), "src")
	f.Write(buf.Bytes())
	defer f.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/services/svc_1/instances/env_1/upload" || r.ContentLength <= int64(buf.Len()) || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("request %s len %d", r.URL.Path, r.ContentLength)
		}
		file, _, err := r.FormFile("source")
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		got, _ := io.ReadAll(file)
		if !bytes.Equal(got, buf.Bytes()) {
			t.Error("upload body differs from the archive")
		}
		w.WriteHeader(202)
		w.Write([]byte(`{"id":"dep_9","status":"queued"}`))
	}))
	defer srv.Close()
	t.Setenv("KEEL_TOKEN", "tok")
	c := &config{URL: srv.URL}
	d, err := uploadSource(context.Background(), c, &link{ServiceID: "svc_1", EnvironmentID: "env_1"}, f, int64(buf.Len()), false)
	if err != nil || d.ID != "dep_9" {
		t.Errorf("deployment %+v err %v", d, err)
	}
}

func TestUploadSourceReportsServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(403)
		w.Write([]byte(`{"error":"uploads are not allowed in this workspace"}`))
	}))
	defer srv.Close()
	f, _ := os.CreateTemp(t.TempDir(), "src")
	f.WriteString("x")
	defer f.Close()
	_, err := uploadSource(context.Background(), &config{URL: srv.URL}, &link{ServiceID: "s", EnvironmentID: "e"}, f, 1, false)
	if err == nil || !strings.Contains(err.Error(), "uploads are not allowed") {
		t.Errorf("err %v", err)
	}
}

func TestBackoff(t *testing.T) {
	old := retryBase
	retryBase = time.Second
	defer func() { retryBase = old }()
	if backoff(0) != time.Second || backoff(2) != 4*time.Second || backoff(10) != 15*time.Second {
		t.Errorf("%v %v %v", backoff(0), backoff(2), backoff(10))
	}
}

func TestCmdUpEndToEnd(t *testing.T) {
	plainOutput(t)
	dir := writeTree(t, map[string]string{"server.js": "require('http')", ".env": "SECRET=1", ".gitignore": ".env\n"})
	var uploaded []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/services/svc_1/instances/env_1/upload":
			file, _, err := r.FormFile("source")
			if err != nil {
				w.WriteHeader(400)
				return
			}
			b, _ := io.ReadAll(file)
			uploaded = tarNames(t, b)
			w.WriteHeader(202)
			w.Write([]byte(`{"id":"dep_1","status":"queued"}`))
		case "/api/deployments/dep_1/build-logs":
			fmt.Fprint(w, "event: log\ndata: building\n\nevent: status\ndata: building\n\nevent: status\ndata: live\n\nevent: done\ndata: live\n\n")
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	t.Setenv("KEEL_TOKEN", "tok")
	t.Chdir(dir)
	c := &config{URL: srv.URL, Links: map[string]*link{dir: {ProjectID: "prj_1", EnvironmentID: "env_1", ServiceID: "svc_1", Service: "web", Environment: "production"}}}
	cmdUp(c, nil) // exits the process unless the deployment ends live
	if strings.Join(uploaded, " ") != ".gitignore server.js" {
		t.Errorf("uploaded %v (.env must stay local)", uploaded)
	}
}
