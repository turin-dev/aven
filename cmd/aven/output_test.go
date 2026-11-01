package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestConfigSaveIsPrivateAndAtomic(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KEEL_CONFIG_DIR", filepath.Join(dir, "nested"))
	c := &config{URL: "https://k.example", Cookie: "secret-cookie", Links: map[string]*link{"/src": {ProjectID: "prj_1"}}}
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	// An older CLI may have left the file world-readable: the next save makes it private again.
	if err := os.Chmod(cfgPath(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(cfgPath())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", st.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(cfgPath()))
	if len(entries) != 1 {
		t.Errorf("temporary files left behind: %v", entries)
	}
	got, warn := readCfg(cfgPath(), "")
	if warn != "" || got.URL != c.URL || got.Cookie != c.Cookie || got.Links["/src"].ProjectID != "prj_1" {
		t.Errorf("round trip: %+v %q", got, warn)
	}
}

func TestReadCfg(t *testing.T) {
	dir := t.TempDir()
	cur, legacy := filepath.Join(dir, "aven.json"), filepath.Join(dir, "keel.json")
	// nothing: empty config, no warning
	if c, warn := readCfg(cur, legacy); warn != "" || c.URL != "" || c.Links == nil {
		t.Errorf("missing: %+v %q", c, warn)
	}
	// legacy keel config is read when the aven one does not exist
	os.WriteFile(legacy, []byte(`{"url":"https://old"}`), 0o600)
	if c, _ := readCfg(cur, legacy); c.URL != "https://old" {
		t.Errorf("legacy: %+v", c)
	}
	// a damaged file is reported, not silently treated as logged out
	os.WriteFile(cur, []byte(`{"url":`), 0o600)
	if c, warn := readCfg(cur, legacy); !strings.Contains(warn, "damaged") || c.URL != "" || c.Links == nil {
		t.Errorf("damaged: %+v %q", c, warn)
	}
}

func TestConcurrentSavesNeverCorrupt(t *testing.T) {
	t.Setenv("KEEL_CONFIG_DIR", t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := &config{URL: strings.Repeat("x", 1000*i), Links: map[string]*link{}}
			if err := c.save(); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	b, _ := os.ReadFile(cfgPath())
	var c config
	if err := json.Unmarshal(b, &c); err != nil {
		t.Errorf("config corrupted by concurrent saves: %v", err)
	}
}

// TestLoginChecksMFAOnTheRightPath is a regression test: the post-login check used /api/api/auth/me,
// so the "workspace requires 2FA" warning never appeared.
func TestLoginChecksMFAOnTheRightPath(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/api/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "keel_session", Value: "sess"})
			w.WriteHeader(204)
		case "/api/auth/me":
			json.NewEncoder(w).Encode(map[string]any{"user": "a", "mfa_setup_required": true})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	t.Setenv("KEEL_CONFIG_DIR", t.TempDir())
	t.Setenv("KEEL_PASSWORD", "pw")
	t.Setenv("KEEL_TOKEN", "")
	c := &config{Links: map[string]*link{}}
	cmdLogin(c, []string{srv.URL})
	if c.Cookie != "sess" {
		t.Errorf("cookie %q", c.Cookie)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(paths, ",") != "/api/auth/login,/api/auth/me" {
		t.Errorf("requests: %v", paths)
	}
}

func TestResponseError(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   []string
	}{
		{401, `{"error":"unauthorized"}`, []string{"unauthorized", "aven login"}},
		{403, `{"error":"mfa_setup_required"}`, []string{"two-factor"}},
		{403, `{"error":"forbidden"}`, []string{"GET /x: 403 forbidden", "hint: your role"}},
		{404, `{"error":"not found"}`, []string{"404 not found", "aven link"}},
		{502, `<html><body>Bad gateway</body></html>`, []string{"502 Bad Gateway", "unavailable"}},
		{500, "boom\nstack", []string{"500 boom"}},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		rec.WriteHeader(tc.status)
		rec.WriteString(tc.body)
		err := responseError("GET", "/x", rec.Result())
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%d: %q does not contain %q", tc.status, err, w)
			}
		}
		if strings.Contains(err.Error(), "stack") || strings.Contains(err.Error(), "<html>") {
			t.Errorf("%d: raw body leaked: %q", tc.status, err)
		}
	}
	var ae *apiError
	rec := httptest.NewRecorder()
	rec.WriteHeader(409)
	if !errors.As(responseError("POST", "/y", rec.Result()), &ae) || ae.Status != 409 {
		t.Error("409 should be an *apiError")
	}
}

func TestColorDecision(t *testing.T) {
	env := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}
	for _, tc := range []struct {
		name string
		env  func(string) string
		tty  bool
		want bool
	}{
		{"tty", env(), true, true},
		{"pipe", env(), false, false},
		{"NO_COLOR wins", env("NO_COLOR", "1", "FORCE_COLOR", "1"), true, false},
		{"dumb terminal", env("TERM", "dumb"), true, false},
		{"forced on a pipe", env("FORCE_COLOR", "1"), false, true},
		{"CLICOLOR_FORCE", env("CLICOLOR_FORCE", "1"), false, true},
		{"FORCE_COLOR=0", env("FORCE_COLOR", "0"), false, false},
	} {
		if got := colorDecision(tc.env, tc.tty); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
	if paint(false, styleRed, "x") != "x" || paint(true, styleRed, "x") != "\x1b[31mx\x1b[0m" {
		t.Error("paint")
	}
}

func TestExitCodeFor(t *testing.T) {
	for msg, want := range map[string]int{
		"usage: aven up":                exitUsage,
		"bad flag\nusage: aven link":    exitUsage,
		"GET /x: 404 not found":         exitError,
		"see usage: below is not usage": exitError,
	} {
		if got := exitCodeFor(msg); got != want {
			t.Errorf("%q: %d", msg, got)
		}
	}
}

func TestSuggest(t *testing.T) {
	cmds := []string{"login", "logs", "link", "status", "variables", "domain"}
	for in, want := range map[string]string{"lgos": "logs", "stauts": "status", "var": "variables", "dommain": "domain", "zzzzzz": ""} {
		if got := suggest(in, cmds); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

func TestKEELURLIsNotPersisted(t *testing.T) {
	t.Setenv("KEEL_CONFIG_DIR", t.TempDir())
	t.Setenv("KEEL_URL", "")
	if err := (&config{URL: "https://stored", Links: map[string]*link{}}).save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KEEL_URL", "https://override")
	c := loadCfg()
	if c.URL != "https://override" {
		t.Fatalf("override not applied: %s", c.URL)
	}
	c.Links["/x"] = &link{ProjectID: "prj_1"}
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KEEL_URL", "")
	if got := loadCfg(); got.URL != "https://stored" || got.Links["/x"] == nil {
		t.Errorf("stored URL %q, links %v", got.URL, got.Links)
	}
}

func TestLoginRejectsURLsWithCredentials(t *testing.T) {
	if os.Getenv("KEEL_LOGIN_HELPER") == "1" {
		cmdLogin(&config{Links: map[string]*link{}}, []string{os.Getenv("KEEL_LOGIN_URL")})
		return
	}
	for _, u := range []string{"https://user:pw@aven.example.com", "https://aven.example.com/?token=x", "https://", "ftp://x"} {
		exe, _ := os.Executable()
		cmd := exec.Command(exe, "-test.run=TestLoginRejectsURLsWithCredentials")
		cmd.Env = append(os.Environ(), "KEEL_LOGIN_HELPER=1", "KEEL_LOGIN_URL="+u, "KEEL_CONFIG_DIR="+t.TempDir())
		out, err := cmd.CombinedOutput()
		if err == nil || strings.Contains(string(out), "pw@") {
			t.Errorf("%s accepted or echoed: %v %s", u, err, out)
		}
	}
}
