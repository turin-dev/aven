package tarx

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type entry struct {
	name string
	typ  byte
	body string
	link string
	mode int64
	size int64 // 0이면 len(body)
}

func archive(t *testing.T, entries ...entry) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Linkname: e.link, Mode: e.mode}
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if e.typ == tar.TypeReg {
			h.Size = int64(len(e.body))
			if e.size != 0 {
				h.Size = e.size
			}
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg && e.size == 0 {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	// size를 부풀린 항목은 본문을 쓰지 않으므로 Close가 실패할 수 있다(헤더만 있으면 충분하다).
	_ = tw.Close()
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func reg(name, body string) entry { return entry{name: name, typ: tar.TypeReg, body: body} }
func dir(name string) entry       { return entry{name: name, typ: tar.TypeDir, mode: 0o755} }
func sym(name, link string) entry { return entry{name: name, typ: tar.TypeSymlink, link: link} }

func TestSafeJoin(t *testing.T) {
	root := "/srv/app"
	cases := []struct {
		rel, want string
	}{
		{"a/b", "/srv/app/a/b"},
		{"../../etc/passwd", "/srv/app/etc/passwd"},
		{"/etc/passwd", "/srv/app/etc/passwd"},
		{"./", "/srv/app"},
		{"", "/srv/app"},
		{"a/../../b", "/srv/app/b"},
	}
	for _, c := range cases {
		got, err := SafeJoin(root, c.rel)
		if err != nil || got != c.want {
			t.Errorf("SafeJoin(%q) = %q, %v; want %q", c.rel, got, err, c.want)
		}
	}
	if got, err := SafeJoin("/srv/app/", "x"); err != nil || got != "/srv/app/x" {
		t.Errorf("trailing slash root: %q %v", got, err)
	}
}

func TestWithin(t *testing.T) {
	cases := []struct {
		root, p string
		ok      bool
	}{
		{"/srv/a", "/srv/a", true},
		{"/srv/a", "/srv/a/b", true},
		{"/srv/a", "/srv/ab", false},
		{"/srv/a", "/srv", false},
		{"/", "/anything", true},
	}
	for _, c := range cases {
		if got := within(c.root, c.p); got != c.ok {
			t.Errorf("within(%q,%q) = %v", c.root, c.p, got)
		}
	}
}

func TestExtractBasic(t *testing.T) {
	dst := t.TempDir()
	err := Extract(archive(t,
		dir("./"),
		dir("src"),
		reg("src/main.go", "package main"),
		entry{name: "run.sh", typ: tar.TypeReg, body: "#!/bin/sh", mode: 0o4755},
		reg("deep/nested/file.txt", "x"),
		sym("link", "src/main.go"),
	), dst)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dst, "src/main.go"))
	if err != nil || string(b) != "package main" {
		t.Fatalf("main.go = %q %v", b, err)
	}
	fi, err := os.Stat(filepath.Join(dst, "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 || fi.Mode()&os.ModeSetuid != 0 {
		t.Errorf("run.sh mode = %v; want 0755 without setuid", fi.Mode())
	}
	fi, _ = os.Stat(filepath.Join(dst, "src/main.go"))
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("main.go mode = %v", fi.Mode())
	}
	if l, err := os.Readlink(filepath.Join(dst, "link")); err != nil || l != "src/main.go" {
		t.Errorf("link = %q %v", l, err)
	}
	if _, err := os.Stat(filepath.Join(dst, "deep/nested/file.txt")); err != nil {
		t.Error(err)
	}
}

func TestExtractTraversalIsConfined(t *testing.T) {
	parent := t.TempDir()
	dst := filepath.Join(parent, "out")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	err := Extract(archive(t,
		reg("../escape.txt", "x"),
		reg("/abs.txt", "y"),
		reg("a/../../../escape2.txt", "z"),
	), dst)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"escape.txt", "escape2.txt", "abs.txt"} {
		if _, err := os.Stat(filepath.Join(parent, name)); err == nil {
			t.Errorf("%s escaped dst", name)
		}
		if _, err := os.Stat(filepath.Join(dst, name)); err != nil {
			t.Errorf("%s not confined into dst: %v", name, err)
		}
	}
}

func TestExtractSymlinkEscapes(t *testing.T) {
	cases := []struct {
		name    string
		entries []entry
		wantErr bool
		gone    []string // dst 기준으로 남아 있으면 안 되는 링크
	}{
		{
			name:    "absolute link is skipped",
			entries: []entry{sym("etc", "/etc")},
			gone:    []string{"etc"},
		},
		{
			name:    "relative link out of dst is skipped",
			entries: []entry{sym("up", "../..")},
			gone:    []string{"up"},
		},
		{
			name:    "empty link target is skipped",
			entries: []entry{sym("empty", "")},
			gone:    []string{"empty"},
		},
		{
			name:    "write through a dir symlink is rejected",
			entries: []entry{sym("d", "."), reg("d/x", "x")},
			wantErr: true,
		},
		{
			name:    "chained links reaching outside are pruned",
			entries: []entry{dir("a"), sym("a/self", ".."), sym("b", "a/self/a/self/..")},
			gone:    []string{"b"},
		},
		{
			name:    "symlink loop is pruned",
			entries: []entry{sym("x", "y"), sym("y", "x")},
			gone:    []string{"x"}, // x를 지우면 y는 dst 안의 끊어진 링크가 되어 남아도 된다
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parent := t.TempDir()
			dst := filepath.Join(parent, "out")
			if err := os.Mkdir(dst, 0o755); err != nil {
				t.Fatal(err)
			}
			err := Extract(archive(t, c.entries...), dst)
			if c.wantErr {
				if err == nil {
					t.Fatal("want error")
				}
				if _, err := os.Stat(filepath.Join(parent, "x")); err == nil {
					t.Fatal("file written outside dst")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, g := range c.gone {
				if _, err := os.Lstat(filepath.Join(dst, g)); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("%s should not exist: %v", g, err)
				}
			}
		})
	}
}

// 바깥을 가리키는 링크를 파일 이름으로 두고 같은 이름의 일반 파일을 쓰면 링크 대상이 아니라 링크가 바뀌어야 한다.
func TestExtractRegularOverExistingSymlink(t *testing.T) {
	parent := t.TempDir()
	victim := filepath.Join(parent, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(parent, "out")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dst, "f")); err != nil {
		t.Fatal(err)
	}
	if err := Extract(archive(t, reg("f", "new")), dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatalf("victim overwritten: %q", b)
	}
	fi, err := os.Lstat(filepath.Join(dst, "f"))
	if err != nil || fi.Mode()&fs.ModeSymlink != 0 {
		t.Fatalf("f should be a regular file: %v %v", fi, err)
	}
}

func TestExtractSkipsSpecialEntries(t *testing.T) {
	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, "secret"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(parent, "out")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	err := Extract(archive(t,
		entry{name: "hard", typ: tar.TypeLink, link: "../secret"},
		entry{name: "fifo", typ: tar.TypeFifo},
		entry{name: "dev", typ: tar.TypeChar},
	), dst)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"hard", "fifo", "dev"} {
		if _, err := os.Lstat(filepath.Join(dst, n)); err == nil {
			t.Errorf("%s should be skipped", n)
		}
	}
}

func TestExtractSymlinkCannotReplaceDir(t *testing.T) {
	dst := t.TempDir()
	err := Extract(archive(t, dir("d"), reg("d/x", "x"), sym("d", ".")), dst)
	if err == nil || !strings.Contains(err.Error(), "replace a directory") {
		t.Fatalf("err = %v", err)
	}
}

func TestExtractTooLarge(t *testing.T) {
	err := Extract(archive(t, entry{name: "big", typ: tar.TypeReg, size: MaxBytes + 1}), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v", err)
	}
}

func TestExtractNotGzip(t *testing.T) {
	if err := Extract(strings.NewReader("plain"), t.TempDir()); err == nil {
		t.Fatal("want error for non-gzip input")
	}
}

func TestExtractFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "a.tgz")
	if err := os.WriteFile(path, archive(t, reg("hello", "hi")).Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(tmp, "out")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ExtractFile(path, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "hello")); string(b) != "hi" {
		t.Fatalf("hello = %q", b)
	}
	if err := ExtractFile(filepath.Join(tmp, "missing"), dst); err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestExtractRelativeDst(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)
	if err := os.Mkdir("out", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Extract(archive(t, reg("f", "x"), sym("l", "f")), "out"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "out", "l")); err != nil {
		t.Fatal(err)
	}
}
