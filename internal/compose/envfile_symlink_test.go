package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// An env_file that is a symlink out of the cloned repository must not be read.
func TestReadEnvFileRejectsSymlinkOutOfRepo(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.env")
	if err := os.WriteFile(outside, []byte("KEEL_ADMIN_PASSWORD=hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo, "app.env")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "ok.env"), []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := Input{RepoDir: repo, ComposeDir: repo}
	if c, ok := readEnvFile(in, filepath.Join(repo, "app.env")); ok {
		t.Errorf("symlinked env_file was read: %q", c)
	}
	if c, ok := readEnvFile(in, filepath.Join(repo, "ok.env")); !ok || c != "A=1\n" {
		t.Errorf("regular env_file: %q %v", c, ok)
	}
}
