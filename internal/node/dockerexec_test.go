//go:build !windows

package node

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeEngine은 exec에 필요한 Docker Engine API만 흉내 낸다(unix socket).
type fakeEngine struct {
	mu      sync.Mutex
	created map[string]any
	resized string
}

func (f *fakeEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == "GET" && r.URL.Path == "/containers/keel-web-a/json":
		json.NewEncoder(w).Encode(map[string]any{"Config": map[string]any{"Labels": map[string]string{LabelManaged: "true", LabelRole: RoleApp}}, "State": map[string]any{"Running": true}})
	case r.Method == "GET" && r.URL.Path == "/containers/keel-proxy/json":
		json.NewEncoder(w).Encode(map[string]any{"Config": map[string]any{"Labels": map[string]string{LabelManaged: "true", LabelRole: "proxy"}}, "State": map[string]any{"Running": true}})
	case r.Method == "POST" && r.URL.Path == "/containers/keel-web-a/exec":
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.created = body
		f.mu.Unlock()
		w.WriteHeader(201)
		w.Write([]byte(`{"Id":"e1"}`))
	case r.Method == "POST" && r.URL.Path == "/exec/e1/start":
		if r.Header.Get("Upgrade") != "tcp" {
			http.Error(w, "no upgrade", 400)
			return
		}
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		brw.WriteString("HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\nwelcome\r\n")
		brw.Flush()
		// 입력을 그대로 돌려주다가 "exit"이 오면 끝낸다
		rd := bufio.NewReader(brw)
		buf := make([]byte, 256)
		for {
			n, err := rd.Read(buf)
			if n > 0 {
				if strings.Contains(string(buf[:n]), "exit") {
					return
				}
				conn.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	case r.Method == "POST" && r.URL.Path == "/exec/e1/resize":
		f.mu.Lock()
		f.resized = r.URL.RawQuery
		f.mu.Unlock()
		w.WriteHeader(201)
	case r.Method == "GET" && r.URL.Path == "/exec/e1/json":
		w.Write([]byte(`{"Running":false,"ExitCode":4}`))
	default:
		http.Error(w, `{"message":"no such container"}`, 404)
	}
}

func TestEngineExec(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "docker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip("unix socket:", err)
	}
	fe := &fakeEngine{}
	srv := &http.Server{Handler: fe}
	go srv.Serve(ln)
	defer srv.Close()
	t.Setenv("DOCKER_HOST", "unix://"+sock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d := &Docker{}
	if _, err := d.Exec(ctx, "keel-proxy", []string{"sh"}, 80, 24); err == nil || !strings.Contains(err.Error(), "not a keel-managed workload") {
		t.Errorf("proxy container allowed: %v", err)
	}
	if _, err := d.Exec(ctx, "keel-missing", []string{"sh"}, 80, 24); err != ErrNoContainer {
		t.Errorf("missing: %v", err)
	}
	s, err := d.Exec(ctx, "keel-web-a", []string{"sh", "-lc", "exec sh"}, 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fe.mu.Lock()
	c := fe.created
	fe.mu.Unlock()
	if c["Tty"] != true || c["AttachStdin"] != true || len(c["Cmd"].([]any)) != 3 {
		t.Errorf("create body: %v", c)
	}
	if cs, _ := json.Marshal(c["ConsoleSize"]); string(cs) != "[30,100]" {
		t.Errorf("console size: %s", cs)
	}
	buf := make([]byte, 64)
	got := ""
	for !strings.Contains(got, "welcome") {
		n, err := s.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		got += string(buf[:n])
	}
	s.Write([]byte("hi"))
	got = ""
	for !strings.Contains(got, "hi") {
		n, err := s.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		got += string(buf[:n])
	}
	if err := s.Resize(ctx, 120, 40); err != nil {
		t.Fatal(err)
	}
	fe.mu.Lock()
	if fe.resized != "h=40&w=120" {
		t.Errorf("resize query: %s", fe.resized)
	}
	fe.mu.Unlock()
	s.Write([]byte("exit"))
	io.Copy(io.Discard, s)
	if code, err := s.Wait(ctx); err != nil || code != 4 {
		t.Errorf("wait: %d %v", code, err)
	}
}

func TestExecHelpers(t *testing.T) {
	if c, r := ClampTermSize(0, 0); c != 80 || r != 24 {
		t.Error(c, r)
	}
	if c, r := ClampTermSize(5000, 2); c != 1000 || r != 2 {
		t.Error(c, r)
	}
	for role, ok := range map[string]bool{RoleApp: true, RoleCron: true, RoleHelper: false, RoleTCPProxy: false, "proxy": false, "": false} {
		err := CheckExecTarget("keel-x", map[string]string{LabelManaged: "true", LabelRole: role})
		if (err == nil) != ok {
			t.Errorf("role %q: %v", role, err)
		}
	}
	if CheckExecTarget("keel-x", map[string]string{LabelRole: RoleApp}) == nil {
		t.Error("unmanaged allowed")
	}
	t.Setenv("DOCKER_HOST", "tcp://1.2.3.4:2375")
	if _, err := DockerSocket(); err == nil {
		t.Error("tcp DOCKER_HOST accepted")
	}
	t.Setenv("DOCKER_HOST", "")
	if s, _ := DockerSocket(); s != "/var/run/docker.sock" {
		t.Error(s)
	}
}
