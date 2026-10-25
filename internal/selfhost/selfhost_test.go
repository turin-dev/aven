package selfhost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func argAfter(args []string, flag string) []string {
	var out []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			out = append(out, args[i+1])
		}
	}
	return out
}

func has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func TestRunArgs(t *testing.T) {
	args := RunArgs("keel/self:abc", Paths{DataDir: "/data/aven", EnvFile: "/etc/aven/aven.env", DockerRoot: "/srv/docker"})
	if args[0] != "run" || args[len(args)-1] != "keel/self:abc" {
		t.Fatalf("args: %v", args)
	}
	if n := argAfter(args, "--name"); len(n) != 1 || n[0] != ContainerName {
		t.Fatalf("name: %v", n)
	}
	envs := argAfter(args, "-e")
	for _, want := range []string{EnvActive + "=1", EnvImage + "=keel/self:abc", "KEEL_DATA=/data/aven", "KEEL_RAILPACK_BIN=/usr/local/bin/railpack", "KEEL_AGENT_BIN_DIR=/usr/local/bin"} {
		if !has(envs, want) {
			t.Errorf("missing -e %s: %v", want, envs)
		}
	}
	vols := argAfter(args, "-v")
	for _, want := range []string{DockerSock + ":" + DockerSock, "/data/aven:/data/aven", "/etc/aven:/etc/aven", "/srv/docker/volumes:/srv/docker/volumes"} {
		if !has(vols, want) {
			t.Errorf("missing -v %s: %v", want, vols)
		}
	}
	if f := argAfter(args, "--env-file"); len(f) != 1 || f[0] != "/etc/aven/aven.env" {
		t.Errorf("env-file: %v", f)
	}
	// reconcile이 앱 컨테이너로 보지 않도록 managed 라벨을 덮어쓴다.
	if l := argAfter(args, "--label"); !has(l, "keel.managed=false") || !has(l, LabelSelf+"=server") {
		t.Errorf("labels: %v", l)
	}
	if h := argAfter(args, "--health-cmd"); len(h) != 1 || h[0] != "aven-server selfhost health" {
		t.Errorf("health: %v", h)
	}
	for _, want := range []string{"--network", "--pid", "--privileged"} {
		if !has(args, want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestRunArgsDefaults(t *testing.T) {
	args := RunArgs("img", Paths{})
	if f := argAfter(args, "--env-file"); len(f) != 1 || f[0] != DefaultEnvFile {
		t.Errorf("env-file: %v", f)
	}
	if v := argAfter(args, "-v"); !has(v, "/var/lib/keel:/var/lib/keel") || !has(v, "/var/lib/docker/volumes:/var/lib/docker/volumes") {
		t.Errorf("volumes: %v", v)
	}
}

func TestUpgraderArgs(t *testing.T) {
	args := UpgraderArgs(UpgradeOpts{Image: "keel/self:new", Deployment: "dep_1", Previous: "dep_0", Paths: Paths{DataDir: "/d", EnvFile: "/e/aven.env", DockerRoot: "/r"}})
	s := strings.Join(args, " ")
	for _, want := range []string{"--name " + UpgraderName, "keel/self:new aven-server selfhost upgrade", "--image keel/self:new", "--deployment dep_1", "--previous dep_0", "--data /d", "--env-file /e/aven.env", "--docker-root /r"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q: %s", want, s)
		}
	}
	if has(args, "--restart") {
		t.Errorf("upgrader is one-shot: %s", s)
	}
}

func TestResultRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if r, err := TakeResult(dir); r != nil || err != nil {
		t.Fatalf("empty: %v %v", r, err)
	}
	if err := WriteResult(dir, Result{Deployment: "dep_1", Previous: "dep_0", Error: "boom", Log: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	r, err := TakeResult(dir)
	if err != nil || r == nil || r.Deployment != "dep_1" || r.Previous != "dep_0" || r.Error != "boom" || len(r.Log) != 2 {
		t.Fatalf("got %+v %v", r, err)
	}
	if r, _ := TakeResult(dir); r != nil {
		t.Fatalf("result must be consumed once: %+v", r)
	}
}

func TestActiveAndImage(t *testing.T) {
	t.Setenv(EnvActive, "")
	t.Setenv(EnvImage, "x")
	if Active() || Image() != "" {
		t.Fatal("not in a self container")
	}
	t.Setenv(EnvActive, "1")
	if !Active() || Image() != "x" {
		t.Fatal("self container")
	}
}

func TestHealth(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	t.Setenv("KEEL_TLS_CERT", "")
	t.Setenv("KEEL_LISTEN", strings.TrimPrefix(srv.URL, "http://"))
	if err := Health(context.Background()); err != nil {
		t.Fatalf("healthy: %v", err)
	}
	status = http.StatusServiceUnavailable
	if err := Health(context.Background()); err == nil {
		t.Fatal("want error for 503")
	}
}

func TestCheckEnvFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "aven.env")
	os.WriteFile(p, []byte("# comment\nKEEL_ADMIN_PASSWORD=plain\nKEEL_PUBLIC_URL=https://a.example\nEMPTY=\nONE=\"\n"), 0o600)
	if err := CheckEnvFile(p); err != nil {
		t.Fatalf("plain values: %v", err)
	}
	os.WriteFile(p, []byte("A=\"x\"\nB='y'\nC=z\n"), 0o600)
	if err := CheckEnvFile(p); err == nil || !strings.Contains(err.Error(), "A, B") {
		t.Fatalf("quoted values: %v", err)
	}
	if err := CheckEnvFile(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing file")
	}
}

func TestUnitFromCgroup(t *testing.T) {
	for in, want := range map[string]string{
		"0::/system.slice/aven-server.service\n":                 "aven-server",
		"12:pids:/system.slice/keel-server.service\n0::/foo\n":   "keel-server",
		"0::/user.slice/user-1000.slice/session-3.scope\n":       "",
		"0::/system.slice/aven-server.service/aven-server.extra": "aven-server",
	} {
		if got := unitFromCgroup(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}
