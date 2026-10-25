package deploy

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/turin-dev/aven/internal/store"
)

func TestParseFilter(t *testing.T) {
	f := ParseFilter(`error "connection refused" -healthcheck -"GET /favicon"`)
	if !reflect.DeepEqual(f.Include, []string{"error", "connection refused"}) || !reflect.DeepEqual(f.Exclude, []string{"healthcheck", "get /favicon"}) {
		t.Fatalf("%+v", f)
	}
	for msg, want := range map[string]bool{
		"ERROR: Connection refused by db":           true,
		"error: connection reset":                   false,
		"error connection refused in healthcheck":   false,
		"error connection refused GET /favicon.ico": false,
	} {
		if f.Match(msg) != want {
			t.Errorf("%q: want %v", msg, want)
		}
	}
	if !ParseFilter("").Match("anything") {
		t.Error("empty filter matches all")
	}
}

func TestIsErrorLine(t *testing.T) {
	if !IsErrorLine(LogEntry{Stream: "stderr", Msg: "hello"}) || !IsErrorLine(LogEntry{Stream: "stdout", Msg: "PANIC: x"}) || IsErrorLine(LogEntry{Stream: "stdout", Msg: "ok"}) {
		t.Error("IsErrorLine")
	}
}

func TestExtractTarGzRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, entries map[string]string) string {
		p := filepath.Join(dir, name)
		f, _ := os.Create(p)
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		for n, body := range entries {
			tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
			tw.Write([]byte(body))
		}
		tw.Close()
		gz.Close()
		f.Close()
		return p
	}
	dst := filepath.Join(dir, "out")
	if err := extractTarGz(mk("ok.tgz", map[string]string{"a/b.txt": "hi", "c.txt": "yo"}), dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "a/b.txt")); string(b) != "hi" {
		t.Error("content")
	}
	// ../ 는 dst 안으로 고정된다(safeJoin)
	dst2 := filepath.Join(dir, "out2")
	extractTarGz(mk("evil.tgz", map[string]string{"../../escape.txt": "x"}), dst2)
	if _, err := os.Stat(filepath.Join(dir, "escape.txt")); err == nil {
		t.Error("path traversal escaped")
	}
}

func TestBuildArgsAndRestart(t *testing.T) {
	df := filepath.Join(t.TempDir(), "Dockerfile")
	os.WriteFile(df, []byte("FROM x\nARG NODE_ENV=production\narg API_URL\nRUN echo\n"), 0o644)
	got := buildArgsFor(df, map[string]string{"NODE_ENV": "dev", "API_URL": "u", "SECRET": "s"})
	if !reflect.DeepEqual(got, map[string]string{"NODE_ENV": "dev", "API_URL": "u"}) {
		t.Errorf("build args: %v", got)
	}
	for pol, want := range map[string]string{"always": "unless-stopped", "never": "no", "on_failure": "on-failure:5"} {
		if r := restartFlag(&store.Spec{RestartPolicy: pol, RestartMaxRetries: 5}); r != want {
			t.Errorf("%s → %s", pol, r)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 999: "999 B", 1000: "1.0 kB", 3481017: "3.5 MB", 3300000: "3.3 MB", 2_500_000_000: "2.5 GB"} {
		if got := HumanBytes(n); got != want {
			t.Errorf("%d: %s want %s", n, got, want)
		}
	}
}
