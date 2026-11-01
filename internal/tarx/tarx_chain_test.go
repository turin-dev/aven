package tarx

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

type listEntry struct {
	name, link, body string
	dir              bool
}

func archiveList(t *testing.T, entries []listEntry) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o644}
		switch {
		case e.link != "":
			h.Typeflag, h.Linkname = tar.TypeSymlink, e.link
		case e.dir:
			h.Typeflag, h.Mode = tar.TypeDir, 0o755
		default:
			h.Typeflag, h.Size = tar.TypeReg, int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	gz.Close()
	return &buf
}

// Chained symlinks must not let an entry write outside dst.
func TestExtractSymlinkChainCannotEscape(t *testing.T) {
	for name, entries := range map[string][]listEntry{
		"dot then parent": {{name: "a", link: "."}, {name: "a/b", link: ".."}, {name: "a/b/evil", body: "x"}},
		"parent via link": {{name: "s", link: "."}, {name: "t", link: "s/.."}, {name: "t/evil", body: "x"}},
		"mkdir via link":  {{name: "a", link: "."}, {name: "a/b", link: ".."}, {name: "a/b/newdir/evil", body: "x"}},
		"dir via link":    {{name: "a", link: "."}, {name: "a/b", link: ".."}, {name: "a/b/evildir/", dir: true}},
	} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			dst := filepath.Join(parent, "dst")
			Extract(archiveList(t, entries), dst) // an error is fine; writing outside is not
			for _, p := range []string{"evil", "newdir", "evildir"} {
				if _, err := os.Lstat(filepath.Join(parent, p)); err == nil {
					t.Fatalf("archive wrote %s outside dst", p)
				}
			}
		})
	}
}

func TestExtractKeepsInnerSymlinksAndFiles(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "dst")
	err := Extract(archiveList(t, []listEntry{{name: "src/", dir: true}, {name: "src/main.go", body: "package main"},
		{name: "link", link: "src/main.go"}, {name: "sub/up", link: "../src"}}), dst)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "link")); err != nil || string(b) != "package main" {
		t.Errorf("inner symlink: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dst, "sub/up/main.go")); err != nil {
		t.Errorf("relative symlink to a sibling dir: %v", err)
	}
}
