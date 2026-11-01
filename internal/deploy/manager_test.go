package deploy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafeJoin(t *testing.T) {
	root := "/work/dep_x"
	for in, want := range map[string]string{".": root, "Dockerfile": root + "/Dockerfile", "a/../b": root + "/b", "../../etc/passwd": root + "/etc/passwd"} {
		got, err := safeJoin(root, in)
		if err != nil || got != want {
			t.Errorf("safeJoin(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestSafeJoinRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "Dockerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Symlink(outside, filepath.Join(root, "ctx"))                                // root_dir -> host dir
	os.Symlink(filepath.Join(outside, "Dockerfile"), filepath.Join(root, "Dfile")) // dockerfile_path -> host file
	os.Mkdir(filepath.Join(root, "app"), 0o755)
	os.Symlink("app", filepath.Join(root, "inner")) // a symlink inside the repo is fine
	for rel, ok := range map[string]bool{"ctx": false, "ctx/Dockerfile": false, "ctx/missing/x": false, "Dfile": false,
		"app": true, "inner": true, "app/Dockerfile": true, "new/dir": true} {
		_, err := safeJoin(root, rel)
		if (err == nil) != ok {
			t.Errorf("safeJoin(%q): err=%v, want ok=%v", rel, err, ok)
		}
	}
}
