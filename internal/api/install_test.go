package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallScriptAndAgentDownloadsArePublic(t *testing.T) {
	dir := t.TempDir()
	bin := []byte("\x7fELF fake agent")
	if err := os.WriteFile(filepath.Join(dir, "aven-agent-linux-amd64"), bin, 0o755); err != nil {
		t.Fatal(err)
	}
	s := &Server{AdminPassword: "password123", SessionKey: make([]byte, 32), AgentBinDir: dir, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	get := func(path string) (int, string) {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	code, body := get("/install-agent.sh")
	if code != 200 || !strings.HasPrefix(body, "#!/bin/sh") || !strings.Contains(body, "/downloads/aven-agent-linux-$ARCH") {
		t.Fatalf("install script: %d %.80q", code, body)
	}
	code, body = get("/downloads/aven-agent-linux-amd64")
	if code != 200 || body != string(bin) {
		t.Fatalf("amd64 download: %d %q", code, body)
	}
	sum := sha256.Sum256(bin)
	code, body = get("/downloads/aven-agent-linux-amd64.sha256")
	if code != 200 || body != hex.EncodeToString(sum[:])+"  aven-agent-linux-amd64\n" {
		t.Fatalf("checksum: %d %q", code, body)
	}
	for _, p := range []string{"/downloads/aven-agent-linux-arm64", "/downloads/aven-agent-linux-386", "/downloads/..%2fkeel.db", "/downloads/aven-server"} {
		if code, _ := get(p); code != 404 {
			t.Errorf("%s: %d, want 404", p, code)
		}
	}
	// 관리 API는 여전히 인증이 필요하다.
	if code, _ := get("/api/nodes"); code != 401 {
		t.Errorf("GET /api/nodes without auth: %d", code)
	}
}

func TestCLIDownloadsArePublic(t *testing.T) {
	dir := t.TempDir()
	bin := []byte("\x7fELF fake cli")
	for _, n := range []string{"keel-linux-amd64", "keel-windows-amd64.exe", "keel-freebsd-amd64"} {
		if err := os.WriteFile(filepath.Join(dir, n), bin, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{AdminPassword: "password123", SessionKey: make([]byte, 32), AgentBinDir: dir, Version: "v9", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	get := func(path string) (int, string) {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	sum := sha256.Sum256(bin)
	hexSum := hex.EncodeToString(sum[:])
	for _, n := range []string{"keel-linux-amd64", "keel-windows-amd64.exe"} {
		if code, body := get("/downloads/" + n); code != 200 || body != string(bin) {
			t.Errorf("%s: %d %q", n, code, body)
		}
		if code, body := get("/downloads/" + n + ".sha256"); code != 200 || body != hexSum+"  "+n+"\n" {
			t.Errorf("%s.sha256: %d %q", n, code, body)
		}
	}
	for _, p := range []string{"/downloads/keel-darwin-arm64", "/downloads/keel-freebsd-amd64", "/downloads/keel-windows-amd64", "/downloads/aven"} {
		if code, _ := get(p); code != 404 {
			t.Errorf("%s: %d, want 404", p, code)
		}
	}
	code, body := get("/api/cli/downloads")
	var list struct {
		Version string `json:"version"`
		Files   []struct {
			Name, OS, Arch, URL, SHA256 string
			Size                        int64
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(body), &list); code != 200 || err != nil {
		t.Fatalf("list: %d %v %s", code, err, body)
	}
	if list.Version != "v9" || len(list.Files) != 2 || list.Files[0].Name != "aven-linux-amd64" || list.Files[0].OS != "linux" ||
		list.Files[0].URL != "/downloads/aven-linux-amd64" || list.Files[0].SHA256 != hexSum || list.Files[0].Size != int64(len(bin)) ||
		list.Files[1].Name != "aven-windows-amd64.exe" || list.Files[1].Arch != "amd64" {
		t.Fatalf("list = %+v", list)
	}
	code, body = get("/install.sh")
	if code != 200 || !strings.HasPrefix(body, "#!/bin/sh") || !strings.Contains(body, "/downloads/$NAME.sha256") {
		t.Fatalf("install.sh: %d %.80q", code, body)
	}
}

func TestInstallScriptIsValidPOSIXShell(t *testing.T) {
	for _, sh := range []string{"sh", "dash", "bash"} {
		p, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		for name, script := range map[string][]byte{"install-agent.sh": installAgentScript, "install-cli.sh": installCLIScriptBody} {
			cmd := exec.Command(p, "-n")
			cmd.Stdin = bytes.NewReader(script)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("%s -n %s: %v %s", sh, name, err, out)
			}
		}
	}
	if !strings.Contains(string(installCLIScriptBody), "--proto =https") {
		t.Error("install-cli.sh does not force https")
	}
	s := string(installAgentScript)
	// 토큰은 unit 파일이나 agent argv가 아니라 0600 env 파일로만 넘긴다.
	for _, want := range []string{"EnvironmentFile=/etc/keel-agent/agent.env", "ExecStart=/usr/local/bin/aven-agent\n", "chmod 600", "--proto =https"} {
		if !strings.Contains(s, want) {
			t.Errorf("install script lacks %q", want)
		}
	}
	if strings.Contains(s, "--join-token") {
		t.Error("install script passes the token on the agent command line")
	}
}

func TestJoinCommandAndPublicURLValidation(t *testing.T) {
	tok := strings.Repeat("a", 64)
	got := joinCommand("https://aven.example.com/", tok)
	want := "curl -fsSL https://aven.example.com/install-agent.sh | sudo sh -s -- --server https://aven.example.com --token '" + tok + "'"
	if got != want {
		t.Fatalf("joinCommand = %q", got)
	}
	for u, ok := range map[string]bool{
		"https://aven.example.com": true, "https://[2001:db8::1]:7443": true, "https://aven.example.com/sub/": true,
		"http://aven.example.com": false, "": false, "https://": false, "https://x.com;rm -rf /": false, "https://x.com'$(id)'": false,
	} {
		if validPublicURL(u) != ok {
			t.Errorf("validPublicURL(%q) = %v", u, !ok)
		}
	}
}

func TestTrustedListenerHonoursForwardedProtoOnly(t *testing.T) {
	s := &Server{Store: testStore(t), AdminPassword: "password123", SessionKey: make([]byte, 32), SecureCookie: false, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	login := func(h http.Handler, proto string) *http.Cookie {
		r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"password":"password123"}`))
		r.Header.Set("X-Forwarded-Proto", proto)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 204 {
			t.Fatalf("login: %d", w.Code)
		}
		return w.Result().Cookies()[0]
	}
	h := s.Handler()
	if c := login(h, "https"); c.Secure {
		t.Error("plain TCP listener trusted X-Forwarded-Proto for the cookie")
	}
	if c := login(ViaTrustedProxy(h), "https"); !c.Secure {
		t.Error("cookie over the trusted HTTPS gateway is not Secure")
	}
	if c := login(ViaTrustedProxy(h), "http"); c.Secure {
		t.Error("http via gateway marked Secure")
	}
	enroll := func(h http.Handler, proto string) int {
		r := httptest.NewRequest("POST", "/api/agent/enroll", strings.NewReader(`{"token":"x"}`))
		r.Header.Set("X-Forwarded-Proto", proto)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if c := enroll(h, "https"); c != 403 {
		t.Errorf("enroll over plain TCP with spoofed header: %d, want 403", c)
	}
	if c := enroll(ViaTrustedProxy(h), "http"); c != 403 {
		t.Errorf("enroll via gateway over http: %d, want 403", c)
	}
	if c := enroll(ViaTrustedProxy(h), "wss"); c != 401 {
		t.Errorf("via gateway with X-Forwarded-Proto wss (WebSocket upgrade through Traefik): %d, want 401", c)
	}
	if c := enroll(ViaTrustedProxy(h), "ws"); c != 403 {
		t.Errorf("via gateway with X-Forwarded-Proto ws: %d, want 403", c)
	}
	if c := enroll(ViaTrustedProxy(h), "https"); c != 401 {
		t.Errorf("enroll via gateway over https: %d, want 401 (bad token, TLS accepted)", c)
	}
}
