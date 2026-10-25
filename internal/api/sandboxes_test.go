package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

type fakeSandboxes struct {
	mu        sync.Mutex
	specs     map[string]node.RunSpec // 살아 있는 컨테이너
	removed   []string
	commits   []string // container=image
	rmImages  []string
	execs     []node.ExecRunRequest
	execOut   *node.ExecOutput
	execErr   error
	startErr  error
	orphanFor string // SandboxContainers가 덧붙일 고아 샌드박스 id
}

func newFakeSandboxes() *fakeSandboxes { return &fakeSandboxes{specs: map[string]node.RunSpec{}} }

func (f *fakeSandboxes) StartSandbox(_ context.Context, _ *store.Environment, _ *store.Sandbox, spec node.RunSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return f.startErr
	}
	f.specs[spec.Name] = spec
	return nil
}

func (f *fakeSandboxes) InspectSandbox(_ context.Context, b *store.Sandbox) (*node.ContainerState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.specs[b.ContainerName]; !ok {
		return nil, node.ErrNoContainer
	}
	return &node.ContainerState{Name: b.ContainerName, Running: true}, nil
}

func (f *fakeSandboxes) RemoveSandbox(_ context.Context, b *store.Sandbox) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.specs, b.ContainerName)
	f.removed = append(f.removed, b.ContainerName)
	return nil
}

func (f *fakeSandboxes) CommitSandbox(_ context.Context, _, container, image string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commits = append(f.commits, container+"="+image)
	return nil
}

func (f *fakeSandboxes) RemoveSandboxImage(_ context.Context, _, image string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rmImages = append(f.rmImages, image)
	return nil
}

func (f *fakeSandboxes) SandboxContainers(_ context.Context, nodeID string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for name, spec := range f.specs {
		out[name] = spec.Labels[node.LabelSandbox]
	}
	if f.orphanFor != "" {
		out[sandboxContainerName(f.orphanFor)] = f.orphanFor
	}
	return out, nil
}

func (f *fakeSandboxes) ExecRun(_ context.Context, _, _ string, req node.ExecRunRequest, _ time.Duration) (*node.ExecOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execs = append(f.execs, req)
	if f.execErr != nil {
		return nil, f.execErr
	}
	if f.execOut != nil {
		return f.execOut, nil
	}
	return &node.ExecOutput{Stdout: []byte("ok\n")}, nil
}

type sbxFixture struct {
	t    *testing.T
	s    *Server
	h    http.Handler
	fake *fakeSandboxes
	env  *store.Environment
	auth func(*http.Request)
}

func newSbxFixture(t *testing.T) *sbxFixture {
	st := testStore(t)
	fake := newFakeSandboxes()
	s := &Server{Store: st, AdminPassword: "password123", SessionKey: make([]byte, 32), sbxImpl: fake,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	p := &store.Project{Name: "sbx"}
	env, err := st.CreateProject(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	owner := testUser(t, st, "owner@example.com", store.RoleOwner)
	return &sbxFixture{t: t, s: s, h: s.Handler(), fake: fake, env: env, auth: withCookie(testSessionCookie(t, s, owner))}
}

func (f *sbxFixture) do(method, path string, body any, want int) []byte {
	f.t.Helper()
	b := ""
	if body != nil {
		j, _ := json.Marshal(body)
		b = string(j)
	}
	w := req(f.h, method, path, b, f.auth)
	if w.Code != want {
		f.t.Fatalf("%s %s: %d %s (want %d)", method, path, w.Code, w.Body.String(), want)
	}
	return w.Body.Bytes()
}

func TestSandboxLifecycle(t *testing.T) {
	f := newSbxFixture(t)
	base := "/api/environments/" + f.env.ID + "/sandboxes"

	var b store.Sandbox
	json.Unmarshal(f.do("POST", base, map[string]any{"variables": map[string]string{"GH_TOKEN": "secret"}}, 201), &b)
	if b.Status != store.SandboxRunning || b.Image != defaultSandboxImage || b.IdleTimeoutMinutes != defaultSandboxIdle ||
		b.NetworkMode != store.SandboxIsolated || b.ExpiresAt == nil {
		t.Fatalf("created: %+v", b)
	}
	spec := f.fake.specs[b.ContainerName]
	if spec.Network != b.ContainerName || spec.Env["GH_TOKEN"] != "secret" || spec.Labels[node.LabelRole] != node.RoleSandbox ||
		spec.Entrypoint == nil || *spec.Entrypoint != "sh" || spec.MemoryMB != defaultSandboxMem {
		t.Fatalf("run spec: %+v", spec)
	}
	// 변수 값은 API에 다시 나오지 않는다.
	if body := string(f.do("GET", "/api/sandboxes/"+b.ID, nil, 200)); strings.Contains(body, "secret") {
		t.Fatalf("variables leaked: %s", body)
	}

	// private: 환경 네트워크와 alias.
	var p store.Sandbox
	json.Unmarshal(f.do("POST", base, map[string]any{"network": "private", "idle_timeout_minutes": 0, "image": "node:22"}, 201), &p)
	ps := f.fake.specs[p.ContainerName]
	if ps.Network != "keel-"+f.env.ID || len(ps.Aliases) != 1 || ps.Aliases[0] != sandboxSlug(p.ID) || p.ExpiresAt != nil {
		t.Fatalf("private sandbox: %+v %+v", p, ps)
	}

	// 잘못된 입력.
	for _, in := range []map[string]any{
		{"image": "keel/sandbox:scp-other"}, {"image": "-rm"}, {"memory_mb": 64}, {"cpu_cores": 100},
		{"network": "public"}, {"idle_timeout_minutes": 999999}, {"variables": map[string]string{"1BAD": "x"}},
		{"checkpoint": "missing"},
	} {
		f.do("POST", base, in, 400)
	}

	// exec: 한 줄 명령은 sh -lc, cwd/timeout은 래퍼 인자로 간다.
	var res sandboxExecResult
	json.Unmarshal(f.do("POST", "/api/sandboxes/"+b.ID+"/exec", map[string]any{"command": "echo hi | wc -c", "cwd": "/app", "env": map[string]string{"CI": "1"}, "timeout_s": 5}, 200), &res)
	if string(res.Stdout) != "ok\n" || res.TimedOut {
		t.Fatalf("exec result: %+v", res)
	}
	last := f.fake.execs[len(f.fake.execs)-1]
	if want := []string{"sh", "-c", sandboxExecWrapper, "keel-exec", "5", "/app", "sh", "-lc", "echo hi | wc -c"}; strings.Join(last.Cmd, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("exec argv: %q", last.Cmd)
	}
	if len(last.Env) != 1 || last.Env[0] != "CI=1" {
		t.Fatalf("exec env: %v", last.Env)
	}
	f.do("POST", "/api/sandboxes/"+b.ID+"/exec", map[string]any{"cmd": []string{"true"}, "cwd": "relative"}, 400)
	f.do("POST", "/api/sandboxes/"+b.ID+"/exec", map[string]any{"cmd": []string{"true"}, "timeout_s": 500}, 400)
	f.do("POST", "/api/sandboxes/"+b.ID+"/exec", map[string]any{}, 400)

	// 마감을 넘기면 timed_out.
	f.fake.execErr = context.DeadlineExceeded
	json.Unmarshal(f.do("POST", "/api/sandboxes/"+b.ID+"/exec", map[string]any{"cmd": []string{"sleep", "999"}, "timeout_s": 1}, 200), &res)
	if !res.TimedOut || res.ExitCode != -1 {
		t.Fatalf("timeout result: %+v", res)
	}
	f.fake.execErr = nil

	// 파일 읽기/쓰기/목록.
	f.do("PUT", "/api/sandboxes/"+b.ID+"/files", map[string]any{"path": "/app/a.txt", "content_base64": base64.StdEncoding.EncodeToString([]byte("hello")), "mode": 0o755}, 200)
	last = f.fake.execs[len(f.fake.execs)-1]
	if string(last.Stdin) != "hello" || last.Cmd[len(last.Cmd)-1] != "0755" || last.Cmd[len(last.Cmd)-2] != "/app/a.txt" {
		t.Fatalf("write argv: %q stdin %q", last.Cmd, last.Stdin)
	}
	f.do("PUT", "/api/sandboxes/"+b.ID+"/files", map[string]any{"path": "rel", "content_base64": ""}, 400)
	var file struct {
		ContentBase64 string `json:"content_base64"`
	}
	json.Unmarshal(f.do("GET", "/api/sandboxes/"+b.ID+"/files?path=/app/a.txt", nil, 200), &file)
	if file.ContentBase64 != base64.StdEncoding.EncodeToString([]byte("ok\n")) {
		t.Fatalf("read: %+v", file)
	}
	f.fake.execOut = &node.ExecOutput{ExitCode: 1, Stderr: []byte("cat: /nope: No such file or directory")}
	f.do("GET", "/api/sandboxes/"+b.ID+"/files?path=/nope", nil, 404)
	f.fake.execOut = &node.ExecOutput{Stdout: []byte("dir\t4096\t1700000000\tsrc\x00file\t5\t1700000001\ta\tb.txt\x00")}
	var list struct {
		Entries []sandboxFileEntry `json:"entries"`
	}
	json.Unmarshal(f.do("GET", "/api/sandboxes/"+b.ID+"/files/list?path=/app/", nil, 200), &list)
	if len(list.Entries) != 2 || list.Entries[0].Type != "dir" || list.Entries[1].Name != "a\tb.txt" || list.Entries[1].Path != "/app/a\tb.txt" || list.Entries[1].Size != 5 {
		t.Fatalf("list: %+v", list.Entries)
	}
	f.fake.execOut = nil

	// checkpoint: 같은 이름은 바꾸고 이전 이미지를 지운다.
	var cp store.SandboxCheckpoint
	json.Unmarshal(f.do("POST", "/api/sandboxes/"+b.ID+"/checkpoints", map[string]string{"name": "after-deps"}, 201), &cp)
	var cp2 store.SandboxCheckpoint
	json.Unmarshal(f.do("POST", "/api/sandboxes/"+b.ID+"/checkpoints", map[string]string{"name": "after-deps"}, 201), &cp2)
	if cp2.ID != cp.ID || len(f.fake.commits) != 2 || len(f.fake.rmImages) != 1 || !strings.HasSuffix(f.fake.commits[0], "="+f.fake.rmImages[0]) {
		t.Fatalf("checkpoint replace: %+v %v %v", cp2, f.fake.commits, f.fake.rmImages)
	}
	f.do("POST", "/api/sandboxes/"+b.ID+"/checkpoints", map[string]string{"name": "Bad Name"}, 400)

	// checkpoint에서 만들기(이름으로).
	var fromCP store.Sandbox
	json.Unmarshal(f.do("POST", base, map[string]any{"checkpoint": "after-deps"}, 201), &fromCP)
	if !strings.HasPrefix(fromCP.Image, "keel/sandbox:scp-") || fromCP.CheckpointName == nil || *fromCP.CheckpointName != "after-deps" {
		t.Fatalf("from checkpoint: %+v", fromCP)
	}
	f.do("PATCH", "/api/sandbox-checkpoints/"+cp.ID, map[string]string{"name": "node-base"}, 200)
	f.do("POST", base, map[string]any{"checkpoint": "after-deps"}, 400)

	// fork: 원본 파일시스템을 새 전용 이미지로, 삭제하면 이미지도 지운다.
	var fork store.Sandbox
	json.Unmarshal(f.do("POST", "/api/sandboxes/"+b.ID+"/fork", map[string]any{"memory_mb": 2048}, 201), &fork)
	if fork.ParentID == nil || *fork.ParentID != b.ID || fork.Image != sandboxSnapshotImage(fork.ID) || fork.MemoryMB != 2048 {
		t.Fatalf("fork: %+v", fork)
	}
	if spec := f.fake.specs[fork.ContainerName]; len(spec.Env) != 0 {
		t.Fatalf("fork inherited variables: %v", spec.Env)
	}
	f.do("DELETE", "/api/sandboxes/"+fork.ID, nil, 204)
	if f.fake.rmImages[len(f.fake.rmImages)-1] != fork.Image {
		t.Fatalf("fork image not removed: %v", f.fake.rmImages)
	}
	f.do("GET", "/api/sandboxes/"+fork.ID, nil, 404)

	var cps []store.SandboxCheckpoint
	json.Unmarshal(f.do("GET", "/api/environments/"+f.env.ID+"/sandbox-checkpoints", nil, 200), &cps)
	if len(cps) != 1 || cps[0].Name != "node-base" {
		t.Fatalf("checkpoints: %+v", cps)
	}
	f.do("DELETE", "/api/sandbox-checkpoints/"+cp.ID, nil, 204)

	var items []store.Sandbox
	json.Unmarshal(f.do("GET", base, nil, 200), &items)
	if len(items) != 3 {
		t.Fatalf("list: %d", len(items))
	}
	f.do("DELETE", "/api/sandboxes/"+b.ID, nil, 204)
	if _, ok := f.fake.specs[b.ContainerName]; ok {
		t.Fatal("container not removed")
	}
}

func TestSandboxStartFailureLeavesNoRow(t *testing.T) {
	f := newSbxFixture(t)
	f.fake.startErr = io.ErrUnexpectedEOF
	f.do("POST", "/api/environments/"+f.env.ID+"/sandboxes", map[string]any{}, 502)
	if items, _ := f.s.Store.ListSandboxes(context.Background(), f.env.ID); len(items) != 0 || len(f.fake.removed) != 1 {
		t.Fatalf("rows %v removed %v", items, f.fake.removed)
	}
}

func TestSandboxIdleExpiryAndSweep(t *testing.T) {
	f := newSbxFixture(t)
	ctx := context.Background()
	var idle, forever store.Sandbox
	json.Unmarshal(f.do("POST", "/api/environments/"+f.env.ID+"/sandboxes", map[string]any{"idle_timeout_minutes": 5}, 201), &idle)
	json.Unmarshal(f.do("POST", "/api/environments/"+f.env.ID+"/sandboxes", map[string]any{"idle_timeout_minutes": 0}, 201), &forever)

	// heartbeat는 유휴 타이머를 다시 시작한다.
	var hb store.Sandbox
	json.Unmarshal(f.do("POST", "/api/sandboxes/"+idle.ID+"/heartbeat", nil, 200), &hb)
	if !hb.ExpiresAt.After(*idle.ExpiresAt) && !hb.ExpiresAt.Equal(*idle.ExpiresAt) {
		t.Fatalf("heartbeat did not extend: %v -> %v", idle.ExpiresAt, hb.ExpiresAt)
	}

	f.s.expireSandboxes(ctx, time.Now().Add(4*time.Minute))
	if _, err := f.s.Store.GetSandbox(ctx, idle.ID); err != nil {
		t.Fatal("expired too early")
	}
	f.s.expireSandboxes(ctx, time.Now().Add(6*time.Minute))
	if _, err := f.s.Store.GetSandbox(ctx, idle.ID); err != store.ErrNotFound {
		t.Fatalf("idle sandbox not expired: %v", err)
	}
	if _, err := f.s.Store.GetSandbox(ctx, forever.ID); err != nil {
		t.Fatalf("idle 0 sandbox expired: %v", err)
	}

	// DB에 없는 샌드박스 컨테이너는 고아로 지운다.
	f.fake.orphanFor = "sbx_orphanorphan1"
	f.s.sweepSandboxes(ctx)
	if got := f.fake.removed[len(f.fake.removed)-1]; got != sandboxContainerName("sbx_orphanorphan1") {
		t.Fatalf("orphan not removed: %v", f.fake.removed)
	}
	if _, ok := f.fake.specs[forever.ContainerName]; !ok {
		t.Fatal("known sandbox removed by sweep")
	}

	// 환경을 지우면 샌드박스도 지운다.
	if err := f.s.cleanupSandboxEnvironment(ctx, f.env.ID); err != nil {
		t.Fatal(err)
	}
	if items, _ := f.s.Store.ListSandboxes(ctx, f.env.ID); len(items) != 0 {
		t.Fatalf("left: %v", items)
	}
}

func TestSandboxNames(t *testing.T) {
	if got := sandboxContainerName("sbx_abcdefghijkl2"); got != "keel-sbx-sbx-abcdefghijkl2" {
		t.Fatalf("container = %q", got)
	}
	if got := sandboxSnapshotImage("scp_abcdefghijkl2"); got != "keel/sandbox:scp-abcdefghijkl2" {
		t.Fatalf("image = %q", got)
	}
	for _, p := range []string{"/tmp/a", "/"} {
		if !validSandboxPath(p) {
			t.Errorf("rejected %q", p)
		}
	}
	for _, p := range []string{"", "rel", "/nul\x00"} {
		if validSandboxPath(p) {
			t.Errorf("accepted %q", p)
		}
	}
}
