package tarx

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type archiveEntry struct {
	Name     string
	Body     string
	Mode     int64
	Typeflag byte
	Linkname string
	Devmajor int64
	Devminor int64
}

func archiveBytes(t *testing.T, entries []archiveEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.Typeflag
		if typ == 0 {
			typ = tar.TypeReg
		}
		hdr := &tar.Header{Name: e.Name, Typeflag: typ}
		if typ == tar.TypeXGlobalHeader {
			hdr.PAXRecords = map[string]string{"comment": "skip"}
		} else {
			mode := e.Mode
			if mode == 0 {
				mode = 0o644
			}
			hdr.Mode = mode
			hdr.Linkname = e.Linkname
			hdr.Devmajor = e.Devmajor
			hdr.Devminor = e.Devminor
			if typ == tar.TypeReg {
				hdr.Size = int64(len(e.Body))
			}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("header %s: %v", e.Name, err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.Body)); err != nil {
				t.Fatalf("write %s: %v", e.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSafeJoin(t *testing.T) {
	root := filepath.Join(t.TempDir(), "out")
	sibling := root + "x"

	if !strings.HasPrefix(sibling, root) || strings.HasPrefix(sibling, root+string(filepath.Separator)) {
		t.Fatalf("fixture %q does not share a raw prefix with %q", sibling, root)
	}

	ok := map[string]string{
		".":     root,
		"./":    root,
		"a":     filepath.Join(root, "a"),
		"a/b":   filepath.Join(root, "a/b"),
		"./a/b": filepath.Join(root, "a/b"),
		"a/b/":  filepath.Join(root, "a/b"),
		"a//b":  filepath.Join(root, "a/b"),
		"a/./b": filepath.Join(root, "a/b"),
	}
	for rel, want := range ok {
		got, err := SafeJoin(root, rel)
		if err != nil || got != want {
			t.Errorf("SafeJoin(%q) = %q, %v; want %q", rel, got, err, want)
		}
		if got != root && !strings.HasPrefix(got, root+string(filepath.Separator)) {
			t.Errorf("SafeJoin(%q) = %q is not inside root", rel, got)
		}
	}

	siblingRel := "../" + filepath.Base(sibling) + "/pwned.txt"
	bad := []string{
		siblingRel,
		"../escape.txt",
		"..",
		"foo/../../etc/passwd",
		"foo/../bar",
		"/etc/passwd",
		"/tmp/outx/pwned.txt",
		"",
		"a/\x00/b",
	}
	for _, rel := range bad {
		got, err := SafeJoin(root, rel)
		if err == nil {
			t.Errorf("SafeJoin(%q) = %q, want error", rel, got)
		}
	}

	if insideRoot(root, sibling) {
		t.Fatalf("sibling %q passed the prefix check", sibling)
	}
	if insideRoot(root, filepath.Join(sibling, "pwned.txt")) {
		t.Fatal("sibling file passed the prefix check")
	}
	if !insideRoot(root, root) || !insideRoot(root, filepath.Join(root, "a", "b")) {
		t.Fatal("path inside root was rejected")
	}
	if insideRoot(root, filepath.Dir(root)) {
		t.Fatal("parent of root was accepted")
	}
	if insideRoot("relative-root", filepath.Join(root, "a")) {
		t.Fatal("mixed relative and absolute paths were accepted")
	}
}

func TestExtract(t *testing.T) {
	type testCase struct {
		name      string
		entries   []archiveEntry
		prepare   func(t *testing.T, dst string) (forbidden []string)
		wantErr   string // empty: success; "*": any error; otherwise a substring
		wantFiles map[string]string
		wantMode  map[string]os.FileMode
	}

	cases := []testCase{
		{
			name: "nested files and modes",
			entries: []archiveEntry{
				// Global PAX metadata is not a file and must be skipped.
				{Name: "global", Typeflag: tar.TypeXGlobalHeader},
				{Name: ".", Typeflag: tar.TypeDir, Mode: 0o755},
				{Name: "nested", Typeflag: tar.TypeDir, Mode: 0o755},
				{Name: "nested/plain.txt", Body: "hello", Mode: 0o644},
				{Name: "nested/script.sh", Body: "#!/bin/sh\n", Mode: 0o755},
				{Name: "./top.txt", Body: "top", Mode: 0o644},
				{Name: "bin/run", Body: "run", Mode: 0o700},
			},
			wantFiles: map[string]string{
				"nested/plain.txt": "hello",
				"nested/script.sh": "#!/bin/sh\n",
				"top.txt":          "top",
				"bin/run":          "run",
			},
			wantMode: map[string]os.FileMode{
				"nested/plain.txt": 0o644,
				"nested/script.sh": 0o755,
				"bin/run":          0o755,
				"top.txt":          0o644,
			},
		},
		{
			name: "absolute path",
			entries: []archiveEntry{
				{Name: "/tmp/abs.txt", Body: "no"},
			},
			wantErr: "invalid path",
		},
		{
			name: "dotdot traversal",
			entries: []archiveEntry{
				{Name: "../../escape.txt", Body: "no"},
				{Name: "foo/../bar.txt", Body: "no"},
			},
			wantErr: "invalid path",
		},
		{
			name: "sibling prefix",
			prepare: func(t *testing.T, dst string) []string {
				sibling := dst + "x"
				if err := os.MkdirAll(sibling, 0o755); err != nil {
					t.Fatal(err)
				}
				return []string{filepath.Join(sibling, "pwned.txt")}
			},
			entries: []archiveEntry{
				{Name: "../PLACEHOLDER/pwned.txt", Body: "no"},
			},
			wantErr: "invalid path",
		},
		{
			name: "sibling prefix via link",
			prepare: func(t *testing.T, dst string) []string {
				sibling := dst + "x"
				if err := os.MkdirAll(sibling, 0o755); err != nil {
					t.Fatal(err)
				}
				return []string{filepath.Join(sibling, "pwned.txt")}
			},
			entries: []archiveEntry{
				{Name: "l", Typeflag: tar.TypeSymlink, Linkname: "../PLACEHOLDER"},
				{Name: "l/pwned.txt", Body: "no"},
			},
			wantErr: "symlink",
		},
		{
			name: "symlink chain",
			entries: []archiveEntry{
				{Name: "l1", Typeflag: tar.TypeSymlink, Linkname: "."},
				{Name: "l1/l2", Typeflag: tar.TypeSymlink, Linkname: ".."},
				{Name: "l2/l3", Typeflag: tar.TypeSymlink, Linkname: ".."},
				{Name: "l2/l3/l4", Typeflag: tar.TypeSymlink, Linkname: ".."},
				{Name: "l2/l3/l4/deep.txt", Body: "pwned"},
			},
			wantErr: "symlink",
		},
		{
			name: "hard link",
			entries: []archiveEntry{
				{Name: "a.txt", Body: "a"},
				{Name: "b.txt", Typeflag: tar.TypeLink, Linkname: "a.txt"},
			},
			wantErr: "hard link",
		},
		{
			name: "fifo",
			entries: []archiveEntry{
				{Name: "pipe", Typeflag: tar.TypeFifo},
			},
			wantErr: "fifo",
		},
		{
			name: "device",
			entries: []archiveEntry{
				{Name: "dev", Typeflag: tar.TypeChar, Devmajor: 1, Devminor: 3},
			},
			wantErr: "device",
		},
		{
			name: "block device",
			entries: []archiveEntry{
				{Name: "blk", Typeflag: tar.TypeBlock, Devmajor: 1, Devminor: 0},
			},
			wantErr: "device",
		},
		{
			name: "unsupported type",
			entries: []archiveEntry{
				{Name: "weird", Typeflag: tar.TypeCont},
			},
			wantErr: "unsupported",
		},
		{
			name: "pre-existing link",
			prepare: func(t *testing.T, dst string) []string {
				if err := os.MkdirAll(dst, 0o755); err != nil {
					t.Fatal(err)
				}
				outside := filepath.Join(filepath.Dir(dst), "outside")
				if err := os.MkdirAll(outside, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(dst, "jump")); err != nil {
					t.Fatal(err)
				}
				return []string{filepath.Join(outside, "evil.txt")}
			},
			entries: []archiveEntry{
				{Name: "jump/evil.txt", Body: "pwned"},
			},
			wantErr: "*",
		},
		{
			name: "destination is a symlink",
			prepare: func(t *testing.T, dst string) []string {
				real := filepath.Join(filepath.Dir(dst), "real")
				if err := os.MkdirAll(real, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(real, dst); err != nil {
					t.Fatal(err)
				}
				return []string{filepath.Join(real, "file.txt")}
			},
			entries: []archiveEntry{
				{Name: "file.txt", Body: "no"},
			},
			wantErr: "symlink",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			// Nest the destination so a failed confinement check stays inside TempDir.
			dst := filepath.Join(parent, "a", "b", "dst")
			var forbidden []string
			if tc.prepare != nil {
				forbidden = tc.prepare(t, dst)
			}
			entries := append([]archiveEntry(nil), tc.entries...)
			for i := range entries {
				base := filepath.Base(dst + "x")
				entries[i].Name = strings.ReplaceAll(entries[i].Name, "PLACEHOLDER", base)
				entries[i].Linkname = strings.ReplaceAll(entries[i].Linkname, "PLACEHOLDER", base)
			}
			err := Extract(bytes.NewReader(archiveBytes(t, entries)), dst)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Errorf("expected an error")
			} else if tc.wantErr != "*" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q, want substring %q", err, tc.wantErr)
			}
			for rel, body := range tc.wantFiles {
				got, rerr := os.ReadFile(filepath.Join(dst, rel))
				if rerr != nil {
					t.Fatalf("read %s: %v", rel, rerr)
				}
				if string(got) != body {
					t.Fatalf("%s = %q, want %q", rel, got, body)
				}
			}
			for rel, mode := range tc.wantMode {
				fi, rerr := os.Lstat(filepath.Join(dst, rel))
				if rerr != nil {
					t.Fatalf("lstat %s: %v", rel, rerr)
				}
				if fi.Mode().Perm() != mode {
					t.Fatalf("%s mode %o, want %o", rel, fi.Mode().Perm(), mode)
				}
				if fi.Mode()&os.ModeSetuid != 0 || fi.Mode()&os.ModeSetgid != 0 {
					t.Fatalf("%s kept privileged mode bits: %o", rel, fi.Mode())
				}
			}
			for _, path := range forbidden {
				if _, rerr := os.Lstat(path); rerr == nil {
					t.Fatalf("wrote outside destination: %s", path)
				}
			}
			if tc.name == "symlink chain" {
				escaped := filepath.Clean(filepath.Join(dst, "..", "..", "..", "deep.txt"))
				if _, rerr := os.Lstat(escaped); rerr == nil {
					t.Fatalf("symlink chain wrote %s", escaped)
				}
				// Links created while resolving the chain must not land above dst.
				for _, rel := range []string{"../l3", "../../l4"} {
					p := filepath.Clean(filepath.Join(dst, rel))
					if _, rerr := os.Lstat(p); rerr == nil {
						t.Fatalf("symlink chain created %s", p)
					}
				}
			}
			if tc.wantErr != "" {
				// A rejected archive must not leave the payload above dst.
				filepath.WalkDir(parent, func(path string, d os.DirEntry, werr error) error {
					if werr != nil || d.IsDir() {
						return nil
					}
					rel, rerr := filepath.Rel(dst, path)
					if rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
						if filepath.Base(path) == "pwned.txt" || filepath.Base(path) == "deep.txt" || filepath.Base(path) == "evil.txt" || filepath.Base(path) == "escape.txt" || filepath.Base(path) == "file.txt" {
							t.Errorf("payload escaped to %s", path)
						}
					}
					return nil
				})
			}
		})
	}
}

func TestExtractFileAndErrors(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "ok.tgz")
	if err := os.WriteFile(archive, archiveBytes(t, []archiveEntry{
		{Name: "a/b.txt", Body: "hi", Mode: 0o644},
	}), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out")
	if err := ExtractFile(archive, dst); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "a/b.txt")); err != nil || string(b) != "hi" {
		t.Fatalf("content %q, %v", b, err)
	}
	if err := ExtractFile(filepath.Join(dir, "missing.tgz"), dst); err == nil {
		t.Fatal("missing archive succeeded")
	}
	if err := Extract(strings.NewReader("not a tarball"), filepath.Join(dir, "bad")); err == nil {
		t.Fatal("invalid gzip succeeded")
	}
	var junk bytes.Buffer
	jgz := gzip.NewWriter(&junk)
	if _, err := jgz.Write([]byte("this is not a tar archive")); err != nil {
		t.Fatal(err)
	}
	if err := jgz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Extract(bytes.NewReader(junk.Bytes()), filepath.Join(dir, "junk")); err == nil {
		t.Fatal("non-tar gzip succeeded")
	}

	// Gzip wrapping a truncated tar entry.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "short.txt", Mode: 0o644, Size: 100, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("only-ten!")); err != nil {
		t.Fatal(err)
	}
	// Close gzip without finishing the tar so the entry is short.
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Extract(bytes.NewReader(buf.Bytes()), filepath.Join(dir, "short")); err == nil {
		t.Fatal("truncated entry succeeded")
	}

	parentFile := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(parentFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Extract(bytes.NewReader(archiveBytes(t, []archiveEntry{{Name: "a.txt", Body: "a"}})), filepath.Join(parentFile, "child")); err == nil {
		t.Fatal("destination under a file succeeded")
	}

	notDir := filepath.Join(dir, "file-dst")
	if err := os.WriteFile(notDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Extract(bytes.NewReader(archiveBytes(t, []archiveEntry{{Name: "a.txt", Body: "a"}})), notDir); err == nil {
		t.Fatal("file destination succeeded")
	}

	// A header that claims more than MaxBytes must be rejected before the body is written.
	var big bytes.Buffer
	bgz := gzip.NewWriter(&big)
	btw := tar.NewWriter(bgz)
	if err := btw.WriteHeader(&tar.Header{Name: "big.bin", Mode: 0o644, Size: MaxBytes + 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if err := bgz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Extract(bytes.NewReader(big.Bytes()), filepath.Join(dir, "big")); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversize archive: %v", err)
	}

	var empty bytes.Buffer
	egz := gzip.NewWriter(&empty)
	etw := tar.NewWriter(egz)
	if err := etw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := egz.Close(); err != nil {
		t.Fatal(err)
	}
	emptyDst := filepath.Join(dir, "empty")
	if err := Extract(bytes.NewReader(empty.Bytes()), emptyDst); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(emptyDst); err != nil || !fi.IsDir() {
		t.Fatalf("empty archive dst: %v %+v", err, fi)
	}

	// A regular member must not follow a pre-existing symlink to a file outside dst.
	linkDst := filepath.Join(dir, "linkdst")
	if err := os.MkdirAll(linkDst, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "outside.txt")
	if err := os.WriteFile(outside, []byte("safe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(linkDst, "evil.txt")); err != nil {
		t.Fatal(err)
	}
	err := Extract(bytes.NewReader(archiveBytes(t, []archiveEntry{{Name: "evil.txt", Body: "pwned"}})), linkDst)
	if err == nil {
		t.Fatal("write through a pre-existing symlink succeeded")
	}
	if b, rerr := os.ReadFile(outside); rerr != nil || string(b) != "safe" {
		t.Fatalf("outside file = %q, %v", b, rerr)
	}
}
