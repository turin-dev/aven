package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/store"
)

func TestComposeBindMountsNeedServerAdmin(t *testing.T) {
	f, h := authServer(t)
	alice := testUser(t, f.st, "alice@example.com", store.RoleMember)
	if err := f.st.SetMember(f.ctx, f.p.ID, alice.ID, store.RoleMember); err != nil {
		t.Fatal(err)
	}
	body, _ := jsonMarshal(map[string]any{"source": "yaml", "allow_bind_mounts": true,
		"yaml": "services:\n  pwn:\n    image: alpine:3\n    volumes:\n      - /:/host\n"})
	path := "/api/projects/" + f.p.ID + "/environments/" + f.prod.ID + "/compose/preview"
	w := req(h, "POST", path, body, withCookie(testSessionCookie(t, f.srv, alice)))
	if w.Code != 200 {
		t.Fatalf("member preview: %d %s", w.Code, w.Body)
	}
	if out := decodeBody[map[string]any](t, w); out["ok"] != false || !strings.Contains(w.Body.String(), "bind_mount_forbidden") {
		t.Errorf("member may deploy a host bind mount: %s", w.Body)
	}
	if w := req(h, "POST", "/api/projects/"+f.p.ID+"/environments/"+f.prod.ID+"/compose/apply", body, withCookie(testSessionCookie(t, f.srv, alice))); w.Code < 400 {
		t.Errorf("member apply with a host bind mount: %d %s", w.Code, w.Body)
	}
	// The server administrator keeps the documented opt-in.
	w = req(h, "POST", path, body, withBearer("legacy-token"))
	if w.Code != 200 || strings.Contains(w.Body.String(), "bind_mount_forbidden") {
		t.Errorf("admin preview: %d %s", w.Code, w.Body)
	}
}

func TestFileInRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "x")
	os.WriteFile(outside, []byte("x"), 0o600)
	os.WriteFile(filepath.Join(root, "in.yml"), []byte("x"), 0o600)
	os.Symlink(outside, filepath.Join(root, "out.yml"))
	os.Symlink("in.yml", filepath.Join(root, "alias.yml"))
	for name, want := range map[string]bool{"in.yml": true, "alias.yml": true, "out.yml": false, "missing.yml": false} {
		if got := fileInRoot(root, filepath.Join(root, name)); got != want {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
}
