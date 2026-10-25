package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/proxy"
)

// fakeDocker는 필요한 메서드만 구현한다. 다른 메서드가 불리면 nil 인터페이스 호출로 panic → 테스트 실패.
type fakeDocker struct {
	Docker
	states    map[string]*node.ContainerState
	ran       []node.RunSpec
	removed   []string
	stopped   []string
	networks  []string
	committed []string
}

func (f *fakeDocker) InspectMany(_ context.Context, names []string) (map[string]*node.ContainerState, error) {
	out := map[string]*node.ContainerState{}
	for _, n := range names {
		if st := f.states[n]; st != nil {
			out[n] = st
		}
	}
	return out, nil
}
func (f *fakeDocker) Run(_ context.Context, s node.RunSpec) error {
	f.ran = append(f.ran, s)
	return nil
}
func (f *fakeDocker) EnsureNamedNetwork(_ context.Context, name string, _ map[string]string) error {
	f.networks = append(f.networks, name)
	return nil
}
func (f *fakeDocker) CommitSandbox(_ context.Context, name, image string) error {
	f.committed = append(f.committed, name+"="+image)
	return nil
}
func (f *fakeDocker) Remove(_ context.Context, n string, _ int) error {
	f.removed = append(f.removed, n)
	return nil
}
func (f *fakeDocker) Stop(_ context.Context, n string, _ int) error {
	f.stopped = append(f.stopped, n)
	return nil
}

type fakeProxy struct {
	routes    []proxy.Route
	connected []string
}

func (p *fakeProxy) Apply(_ context.Context, r []proxy.Route) error { p.routes = r; return nil }
func (p *fakeProxy) Connect(_ context.Context, network string) error {
	p.connected = append(p.connected, network)
	return nil
}
func (p *fakeProxy) FollowAccessLogs(ctx context.Context, _ func(proxy.AccessLog)) {
	<-ctx.Done()
}

func validSpec() node.RunSpec {
	return node.RunSpec{
		Name: "keel-web-abcdefgh-1", Image: "traefik/whoami:v1.10", Network: "keel-env_abcdefghijklm",
		Aliases: []string{"web", "web.production.keel.internal"}, Restart: "on-failure:5",
		Labels: map[string]string{node.LabelManaged: "true", node.LabelRole: node.RoleApp, node.LabelInstance: "ins_abc",
			node.LabelDeploy: "dep_abc", node.LabelReplica: "1"},
		Env:    map[string]string{"PORT": "80"},
		Mounts: []node.Mount{{Volume: "keel-vol-abcdefgh", Path: "/data"}},
	}
}

func handle(t *testing.T, h *Handler, op string, payload any) (any, error) {
	t.Helper()
	b, _ := json.Marshal(payload)
	return h.Handle(context.Background(), op, b, func(any) {})
}

func TestValidRunSpecAccepted(t *testing.T) {
	f := &fakeDocker{states: map[string]*node.ContainerState{}}
	h := &Handler{Docker: f, Proxy: &fakeProxy{}}
	if _, err := handle(t, h, agentprotocol.OpContainerRun, agentprotocol.RunRequest{Spec: validSpec()}); err != nil {
		t.Fatal(err)
	}
	if len(f.ran) != 1 {
		t.Fatal("container not started")
	}
}

func TestSandboxNetworkAndRunAreIsolated(t *testing.T) {
	const id = "sbx_abcdefghijkl2"
	name := "keel-sbx-sbx-abcdefghijkl2"
	labels := map[string]string{node.LabelSandbox: id, node.LabelProject: "prj_test", node.LabelEnv: "env_test"}
	f := &fakeDocker{states: map[string]*node.ContainerState{}}
	p := &fakeProxy{}
	h := &Handler{Docker: f, Proxy: p}
	if _, err := handle(t, h, agentprotocol.OpNetworkEnsure, agentprotocol.NetworkRequest{Name: name, Labels: labels}); err != nil {
		t.Fatal(err)
	}
	if len(f.networks) != 1 || len(p.connected) != 0 {
		t.Fatalf("network should not be attached to the Aven proxy: created=%v proxy=%v", f.networks, p.connected)
	}
	// 샌드박스 이름의 네트워크는 맞는 샌드박스 라벨이 있어야 한다.
	for _, bad := range []agentprotocol.NetworkRequest{
		{Name: name, Labels: map[string]string{node.LabelProject: "prj_test", node.LabelEnv: "env_test"}},
		{Name: name, Labels: map[string]string{node.LabelSandbox: "sbx_otherotherot", node.LabelProject: "prj_test", node.LabelEnv: "env_test"}},
	} {
		if _, err := handle(t, h, agentprotocol.OpNetworkEnsure, bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	spec := node.RunSpec{Name: name, Image: "ubuntu:24.04", Network: name, Restart: "unless-stopped",
		Cmd: []string{"sh", "-lc", "sleep infinity"}, MemoryMB: 512, CPUs: "1",
		Labels: map[string]string{node.LabelManaged: "true", node.LabelRole: node.RoleSandbox, node.LabelSandbox: id,
			node.LabelProject: "prj_test", node.LabelEnv: "env_test"}}
	if _, err := handle(t, h, agentprotocol.OpContainerRun, agentprotocol.RunRequest{Spec: spec}); err != nil {
		t.Fatal(err)
	}
	if len(f.ran) != 1 || f.ran[0].Labels[node.LabelRole] != node.RoleSandbox {
		t.Fatalf("sandbox container not run: %+v", f.ran)
	}
}

func TestSandboxCommitRequiresSandboxRole(t *testing.T) {
	name := "keel-sbx-sbx-abcdefghijkl2"
	f := &fakeDocker{states: map[string]*node.ContainerState{
		name: {Name: name, Running: true, Labels: map[string]string{node.LabelManaged: "true", node.LabelRole: node.RoleSandbox}},
	}}
	h := &Handler{Docker: f, Proxy: &fakeProxy{}}
	if _, err := handle(t, h, agentprotocol.OpSandboxCommit, agentprotocol.SandboxCommitRequest{Name: name, Image: "keel/sandbox:scp-test"}); err != nil {
		t.Fatal(err)
	}
	if len(f.committed) != 1 {
		t.Fatalf("commit calls: %v", f.committed)
	}
	f.states[name].Labels[node.LabelRole] = node.RoleApp
	if _, err := handle(t, h, agentprotocol.OpSandboxCommit, agentprotocol.SandboxCommitRequest{Name: name, Image: "keel/sandbox:scp-other"}); err == nil {
		t.Fatal("non-sandbox container was committed")
	}
}

func TestRunSpecRejections(t *testing.T) {
	cases := map[string]func(s *node.RunSpec){
		"unmanaged":       func(s *node.RunSpec) { delete(s.Labels, node.LabelManaged) },
		"proxy role":      func(s *node.RunSpec) { s.Labels[node.LabelRole] = "proxy" },
		"tcpproxy role":   func(s *node.RunSpec) { s.Labels[node.LabelRole] = node.RoleTCPProxy },
		"host network":    func(s *node.RunSpec) { s.Network = "host" },
		"container netns": func(s *node.RunSpec) { s.Network = "container:keel-proxy" },
		"bridge":          func(s *node.RunSpec) { s.Network = "bridge" },
		"docker socket": func(s *node.RunSpec) {
			s.Mounts = []node.Mount{{Volume: "/var/run/docker.sock", Path: "/var/run/docker.sock"}}
		},
		"host path": func(s *node.RunSpec) { s.Mounts = []node.Mount{{Volume: "/", Path: "/host"}} },
		"mount injection": func(s *node.RunSpec) {
			s.Mounts = []node.Mount{{Volume: "keel-vol-abcd,type=bind,source=/", Path: "/x"}}
		},
		"mount path comma": func(s *node.RunSpec) { s.Mounts[0].Path = "/data,readonly=false" },
		"publish":          func(s *node.RunSpec) { s.Publish = []string{"80:80"} },
		"image flag":       func(s *node.RunSpec) { s.Image = "--privileged" },
		"name":             func(s *node.RunSpec) { s.Name = "dokploy-traefik" },
		"label key":        func(s *node.RunSpec) { s.Labels["traefik.enable"] = "true" },
		"restart":          func(s *node.RunSpec) { s.Restart = "--privileged" },
		"env key":          func(s *node.RunSpec) { s.Env["A=B"] = "x" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := validSpec()
			mutate(&s)
			f := &fakeDocker{states: map[string]*node.ContainerState{}}
			h := &Handler{Docker: f, Proxy: &fakeProxy{}}
			if _, err := handle(t, h, agentprotocol.OpContainerRun, agentprotocol.RunRequest{Spec: s}); err == nil {
				t.Fatalf("spec accepted: %+v", s)
			}
			if len(f.ran) != 0 {
				t.Fatal("container started")
			}
		})
	}
}

func TestUnknownFieldsRejected(t *testing.T) {
	// privileged / cap_add / network_mode / binds 등은 스펙에 없다. 알 수 없는 필드는 거부한다.
	f := &fakeDocker{states: map[string]*node.ContainerState{}}
	h := &Handler{Docker: f, Proxy: &fakeProxy{}}
	spec, _ := json.Marshal(validSpec())
	for _, extra := range []string{`"Privileged":true`, `"CapAdd":["SYS_ADMIN"]`, `"NetworkMode":"host"`, `"Binds":["/var/run/docker.sock:/var/run/docker.sock"]`, `"PidMode":"host"`} {
		payload := `{"spec":` + strings.TrimSuffix(string(spec), "}") + `,` + extra + `}}`
		if _, err := h.Handle(context.Background(), agentprotocol.OpContainerRun, []byte(payload), func(any) {}); err == nil {
			t.Fatalf("accepted %s", extra)
		}
	}
	if len(f.ran) != 0 {
		t.Fatal("container started")
	}
}

func TestUnknownOperationRejected(t *testing.T) {
	h := &Handler{Docker: &fakeDocker{}, Proxy: &fakeProxy{}}
	for _, op := range []string{"exec", "docker", "shell", "container.exec", "system.prune", ""} {
		if _, err := h.Handle(context.Background(), op, []byte(`{}`), func(any) {}); err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("op %q: %v", op, err)
		}
	}
}

func TestContainerActionsOnlyOnManagedWorkloads(t *testing.T) {
	f := &fakeDocker{states: map[string]*node.ContainerState{
		"keel-web-abcdefgh-1": {Name: "keel-web-abcdefgh-1", Labels: map[string]string{node.LabelManaged: "true", node.LabelRole: node.RoleApp}},
		"keel-foreign":        {Name: "keel-foreign", Labels: map[string]string{}},
		"keel-proxy":          {Name: "keel-proxy", Labels: map[string]string{node.LabelManaged: "true", node.LabelRole: "proxy"}},
	}}
	h := &Handler{Docker: f, Proxy: &fakeProxy{}}
	if _, err := handle(t, h, agentprotocol.OpContainerStop, agentprotocol.ContainerRequest{Name: "keel-web-abcdefgh-1", Grace: 5}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"keel-foreign", "keel-proxy", "dokploy-postgres", "../x"} {
		for _, op := range []string{agentprotocol.OpContainerStop, agentprotocol.OpContainerRemove, agentprotocol.OpContainerStart, agentprotocol.OpContainerRestrt} {
			if _, err := handle(t, h, op, agentprotocol.ContainerRequest{Name: name}); err == nil {
				t.Fatalf("%s on %s allowed", op, name)
			}
		}
	}
	if len(f.removed) != 0 || len(f.stopped) != 1 {
		t.Fatalf("unexpected docker calls: removed=%v stopped=%v", f.removed, f.stopped)
	}
	// 이름이 같아도 aven 것이 아니면 run으로 덮어쓰지 않는다.
	s := validSpec()
	s.Name = "keel-foreign"
	if _, err := handle(t, h, agentprotocol.OpContainerRun, agentprotocol.RunRequest{Spec: s}); err == nil {
		t.Fatal("run replaced a foreign container")
	}
	// inspect는 aven 컨테이너가 아닌 것을 숨긴다.
	res, err := handle(t, h, agentprotocol.OpContainerInspct, agentprotocol.NamesRequest{Names: []string{"keel-foreign", "keel-web-abcdefgh-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if st := res.(map[string]*node.ContainerState); st["keel-foreign"] != nil || st["keel-web-abcdefgh-1"] == nil {
		t.Fatalf("inspect leaked: %v", st)
	}
}

func TestProxyRoutesValidated(t *testing.T) {
	p := &fakeProxy{}
	h := &Handler{Docker: &fakeDocker{}, Proxy: p}
	ok := []proxy.Route{{Hosts: []string{"app.example.com"}, Upstreams: []string{"keel-web-abcdefgh-1:80"}}}
	if _, err := handle(t, h, agentprotocol.OpProxyApply, agentprotocol.ProxyRequest{Routes: ok}); err != nil || len(p.routes) != 1 {
		t.Fatalf("valid routes: %v", err)
	}
	for _, up := range []string{"169.254.169.254:80", "[::1]:2019", "unix//var/run/docker.sock", "localhost:2019", "keel-web:80/x"} {
		bad := []proxy.Route{{Hosts: []string{"app.example.com"}, Upstreams: []string{up}}}
		if _, err := handle(t, h, agentprotocol.OpProxyApply, agentprotocol.ProxyRequest{Routes: bad}); err == nil {
			t.Fatalf("upstream %q accepted", up)
		}
	}
	bad := []proxy.Route{{Hosts: []string{"a b"}, Upstreams: []string{"keel-web-abcdefgh-1:80"}}}
	if _, err := handle(t, h, agentprotocol.OpProxyApply, agentprotocol.ProxyRequest{Routes: bad}); err == nil {
		t.Fatal("bad host accepted")
	}
}

func TestSourceAndImageRestrictions(t *testing.T) {
	h := &Handler{Docker: &fakeDocker{}, Proxy: &fakeProxy{}, WorkDir: t.TempDir()}
	for _, repo := range []string{"file:///etc", "/etc", "--upload-pack=sh", "ext::sh -c id"} {
		if _, err := handle(t, h, agentprotocol.OpSourceFetch, agentprotocol.FetchRequest{Repo: repo, Branch: "main"}); err == nil {
			t.Fatalf("repo %q accepted", repo)
		}
	}
	if _, err := handle(t, h, agentprotocol.OpSourceFetch, agentprotocol.FetchRequest{Repo: "https://github.com/a/b", Branch: "--orphan"}); err == nil {
		t.Fatal("branch flag accepted")
	}
	if _, err := handle(t, h, agentprotocol.OpImageRemove, agentprotocol.ImageRequest{Image: "postgres:16"}); err == nil {
		t.Fatal("removing non-aven image allowed")
	}
	if _, err := handle(t, h, agentprotocol.OpImageBuild, agentprotocol.BuildRequest{Workspace: "../../etc", Image: "keel/x:y",
		Labels: map[string]string{node.LabelManaged: "true"}}); err == nil {
		t.Fatal("workspace traversal accepted")
	}
	if _, err := handle(t, h, agentprotocol.OpNetworkEnsure, agentprotocol.NetworkRequest{Name: "host"}); err == nil {
		t.Fatal("network host accepted")
	}
	if _, err := handle(t, h, agentprotocol.OpVolumeRemove, agentprotocol.VolumeRequest{Name: "dokploy-postgres-data"}); err == nil {
		t.Fatal("foreign volume removal accepted")
	}
}

func TestFetchUsesNodeAndReturnsConfig(t *testing.T) {
	dir := t.TempDir()
	h := &Handler{Docker: &fakeDocker{}, Proxy: &fakeProxy{}, WorkDir: dir}
	h.clone = func(_ context.Context, repo, branch, sha, dst, token string, out func(string)) (*node.Commit, error) {
		if err := mkfile(dst+"/app/aven.json", `{"build":{"builder":"DOCKERFILE"}}`); err != nil {
			return nil, err
		}
		return &node.Commit{SHA: "abc", Message: "m", Author: "a"}, nil
	}
	res, err := handle(t, h, agentprotocol.OpSourceFetch, agentprotocol.FetchRequest{Repo: "https://example.com/r.git", Branch: "main", RootDir: "app"})
	if err != nil {
		t.Fatal(err)
	}
	fr := res.(*agentprotocol.FetchResult)
	if fr.ConfigName != "aven.json" || !strings.Contains(string(fr.Config), "DOCKERFILE") || fr.Commit.SHA != "abc" || len(fr.Workspace) != 24 {
		t.Fatalf("fetch result: %+v", fr)
	}
	if _, err := handle(t, h, agentprotocol.OpSourceCleanup, agentprotocol.WorkspaceRequest{Workspace: fr.Workspace}); err != nil {
		t.Fatal(err)
	}
}

func mkfile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
