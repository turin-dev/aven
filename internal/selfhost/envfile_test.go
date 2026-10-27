package selfhost

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeEnv(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "aven.env")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEnvFileApplyKeepsCommentsAndOrder(t *testing.T) {
	p := writeEnv(t, "# server\nKEEL_DATA=/var/lib/keel\nKEEL_PUBLIC_URL=https://old.example\n\n# mail\nKEEL_SMTP_HOST=smtp.example\nKEEL_GITHUB_TOKEN=x\n")
	f, err := ReadEnvFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Apply(map[string]string{"KEEL_PUBLIC_URL": "https://new.example", "KEEL_BASE_DOMAIN": "apps.example", "MY_EXTRA": "1"}, []string{"KEEL_SMTP_HOST"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Write(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	want := "# server\nKEEL_DATA=/var/lib/keel\n\n# mail\nKEEL_GITHUB_TOKEN=x\nAVEN_BASE_DOMAIN=apps.example\nAVEN_PUBLIC_URL=https://new.example\nMY_EXTRA=1\n"
	if string(b) != want {
		t.Fatalf("file:\n%s\nwant:\n%s", b, want)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
}

func TestEnvFileRejects(t *testing.T) {
	p := writeEnv(t, "KEEL_ADMIN_PASSWORD=password123\n")
	for _, tc := range []struct {
		set   map[string]string
		unset []string
	}{
		{set: map[string]string{"KEEL_DATA": "/tmp"}},
		{set: map[string]string{"lower": "x"}},
		{set: map[string]string{"KEEL_X": "a\nb"}},
		{set: map[string]string{"KEEL_X": `"quoted"`}},
		{set: map[string]string{"KEEL_X": " padded"}},
		{set: map[string]string{"KEEL_ADMIN_PASSWORD": "short"}},
		{set: map[string]string{"KEEL_PUBLIC_URL": "example.com"}},
		{unset: []string{"KEEL_ADMIN_PASSWORD"}},
		{unset: []string{"KEEL_SELF_ENV_FILE"}},
	} {
		f, _ := ReadEnvFile(p)
		if err := f.Apply(tc.set, tc.unset); !errors.Is(err, ErrConfig) {
			t.Errorf("%v %v: %v", tc.set, tc.unset, err)
		}
	}
}

func TestEnvFileEntries(t *testing.T) {
	p := writeEnv(t, "KEEL_PUBLIC_URL=https://a.example\nKEEL_SMTP_PASSWORD=hunter22\nKEEL_DATA=/var/lib/keel\nCUSTOM_TOKEN=abc\nKEEL_BASE_DOMAIN=new.example\n")
	f, _ := ReadEnvFile(p)
	running := map[string]string{"KEEL_PUBLIC_URL": "https://a.example", "KEEL_SMTP_PASSWORD": "hunter22", "KEEL_DATA": "/other", "KEEL_BASE_DOMAIN": "old.example", "KEEL_MESH": "auto"}
	got := map[string]ConfigEntry{}
	for _, e := range f.Entries(func(k string) (string, bool) { v, ok := running[k]; return v, ok }) {
		got[e.Name] = e
	}
	if e := got["AVEN_PUBLIC_URL"]; !e.Set || e.Value != "https://a.example" || e.Pending || e.Group != "general" {
		t.Errorf("public url: %+v", e)
	}
	if e := got["AVEN_SMTP_PASSWORD"]; !e.Set || !e.Secret || e.Value != "" || e.Pending {
		t.Errorf("secret: %+v", e)
	}
	if e := got["AVEN_DATA"]; !e.Fixed || e.Pending {
		t.Errorf("fixed: %+v", e)
	}
	if e := got["AVEN_BASE_DOMAIN"]; !e.Pending {
		t.Errorf("changed value must be pending: %+v", e)
	}
	if e := got["AVEN_MESH"]; e.Set || !e.Pending {
		t.Errorf("removed from file must be pending: %+v", e)
	}
	if e := got["CUSTOM_TOKEN"]; e.Group != "other" || !e.Secret || e.Value != "" {
		t.Errorf("unknown key: %+v", e)
	}
	if e := got["KEEL_SMTP_HOST"]; e.Set || e.Pending {
		t.Errorf("unset catalog key: %+v", e)
	}
}

func TestGoodEnvRestore(t *testing.T) {
	data := t.TempDir()
	p := writeEnv(t, "KEEL_LISTEN=[::1]:7070\n")
	if ok, err := RestoreGoodEnv(data, p); ok || err != nil {
		t.Fatalf("no snapshot: %v %v", ok, err)
	}
	if err := SaveGoodEnv(data, p); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(p, []byte("KEEL_LISTEN=broken\n"), 0o600)
	if ok, err := RestoreGoodEnv(data, p); !ok || err != nil {
		t.Fatalf("restore: %v %v", ok, err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "[::1]:7070") {
		t.Fatalf("restored: %s", b)
	}
	if ok, _ := RestoreGoodEnv(data, p); ok {
		t.Fatal("restore again must be a no-op")
	}
}
