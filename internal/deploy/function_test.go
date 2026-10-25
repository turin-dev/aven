package deploy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/store"
)

// untar는 tar.gz의 파일 이름 → 내용이다.
func untar(t *testing.T, data []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = string(b)
	}
	return out
}

func TestFunctionContext(t *testing.T) {
	data, err := FunctionContext("console.log(1)")
	if err != nil {
		t.Fatal(err)
	}
	files := untar(t, data)
	if len(files) != 2 || files[FunctionEntry] != "console.log(1)" {
		t.Fatalf("files: %v", files)
	}
	df := files["Dockerfile"]
	for _, want := range []string{"FROM " + FunctionBunImage + "\n", "COPY index.ts ./index.ts", `CMD ["bun", "run", "index.ts"]`} {
		if !strings.Contains(df, want) {
			t.Errorf("Dockerfile missing %q:\n%s", want, df)
		}
	}
	if _, err := FunctionContext(strings.Repeat("x", store.MaxFunctionCodeBytes+1)); err == nil {
		t.Error("oversized code accepted")
	}
	if _, err := FunctionContext(strings.Repeat("x", store.MaxFunctionCodeBytes)); err != nil {
		t.Errorf("1 MiB code rejected: %v", err)
	}
}

// fnHub는 원격 노드의 업로드·빌드 작업을 흉내 내고 나머지는 nodesHub에 넘긴다.
type fnHub struct {
	*nodesHub
	mu       sync.Mutex
	uploaded bytes.Buffer
	builds   []agentprotocol.BuildRequest
}

func (h *fnHub) CallStream(ctx context.Context, nodeID, op string, req, res any, out func(json.RawMessage)) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch op {
	case agentprotocol.OpUploadBegin:
		h.uploaded.Reset()
		return setResult(res, agentprotocol.UploadBeginResult{UploadID: strings.Repeat("u", 24)})
	case agentprotocol.OpUploadChunk:
		h.uploaded.Write(req.(agentprotocol.UploadChunkRequest).Data)
		return nil
	case agentprotocol.OpUploadFinish:
		return setResult(res, agentprotocol.FetchResult{Workspace: strings.Repeat("w", 24)})
	case agentprotocol.OpSourceCleanup:
		return nil
	case agentprotocol.OpImageBuild:
		br := req.(agentprotocol.BuildRequest)
		h.builds = append(h.builds, br)
		h.nodes[nodeID].images[br.Image] = true
		return setResult(res, agentprotocol.BuildResult{Builder: "dockerfile"})
	}
	return h.nodesHub.CallStream(ctx, nodeID, op, req, res, out)
}

// function 서비스 배포: 생성 시 코드 스냅샷 → 빌드 컨텍스트(Dockerfile + index.ts)를 원격 노드로 보내 Dockerfile로 빌드한다.
func TestFunctionDeployBuildsSnapshotOnRemoteNode(t *testing.T) {
	f := newScaleFixture(t)
	ctx := context.Background()
	hub := &fnHub{nodesHub: f.m.Agents.(*nodesHub)}
	f.m.Agents = hub
	f.m.WorkDir = t.TempDir()
	// 서비스 종류는 생성 뒤 바꾸지 않으므로 테스트에서는 새로 만든다.
	svc := &store.Service{ProjectID: f.svc.ProjectID, Name: "fn", SourceType: "function"}
	if err := f.st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	in := &store.Instance{ServiceID: svc.ID, EnvironmentID: f.env.ID, NodeID: f.rid}
	in.Spec.Defaults()
	in.Builder, in.RootDir = "railpack", "/sub" // function 빌드는 이 설정을 쓰지 않는다
	in.HealthcheckTimeoutS = 2
	if err := f.st.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	f.svc, f.in = svc, in
	if _, err := f.st.SetFunctionCode(ctx, in.ID, "v1"); err != nil {
		t.Fatal(err)
	}
	inst, _ := f.st.GetInstance(ctx, in.ID)
	d, err := f.m.Enqueue(ctx, inst, EnqueueOpts{Trigger: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	// 실행 전에 코드를 바꿔도 배포는 만들 때의 코드를 쓴다.
	f.st.SetFunctionCode(ctx, in.ID, "v2")
	f.m.execute(d.ID)
	d, _ = f.st.GetDeployment(ctx, d.ID)
	if d.Status != store.DepLive {
		t.Fatalf("status %s err %v ops %v", d.Status, fnDeref(d.Error), f.log.list())
	}
	files := untar(t, hub.uploaded.Bytes())
	if files[FunctionEntry] != "v1" || !strings.Contains(files["Dockerfile"], FunctionBunImage) {
		t.Fatalf("uploaded context: %v", files)
	}
	if len(hub.builds) != 1 || hub.builds[0].Builder != "dockerfile" || hub.builds[0].RootDir != "/" || hub.builds[0].DockerfilePath != "Dockerfile" {
		t.Fatalf("build request: %+v", hub.builds)
	}
	if d.Image == nil || !strings.HasPrefix(*d.Image, "keel/") || d.Source != "" {
		t.Fatalf("image %v source %q", d.Image, d.Source)
	}
	if len(f.remote.names()) != 1 {
		t.Fatalf("remote containers: %v", f.remote.names())
	}
	if fc, err := f.st.DeploymentFunctionCode(ctx, d.ID); err != nil || fc.Code != "v1" || fc.SHA != store.FunctionSHA("v1") {
		t.Fatalf("snapshot %+v %v", fc, err)
	}
	// 임시 tarball은 남지 않는다.
	if ents, _ := os.ReadDir(f.m.WorkDir); len(ents) != 0 {
		t.Fatalf("work dir not cleaned: %v", ents)
	}
	// 같은 이미지로 다시 배포(rollback/redeploy)하면 그 이미지를 만든 배포의 코드 스냅샷을 이어받는다.
	inst, _ = f.st.GetInstance(ctx, in.ID)
	rb, err := f.m.Enqueue(ctx, inst, EnqueueOpts{Trigger: "rollback", Image: d.Image})
	if err != nil {
		t.Fatal(err)
	}
	if sha := f.st.DeploymentFunctionSHA(ctx, rb.ID); sha != store.FunctionSHA("v1") {
		t.Fatalf("rollback snapshot sha %s", sha)
	}
}

// 코드가 없는 function 배포는 빌드 전에 알기 쉬운 오류로 실패한다.
func TestFunctionDeployWithoutCode(t *testing.T) {
	f := newScaleFixture(t)
	ctx := context.Background()
	f.m.WorkDir = t.TempDir()
	svc := &store.Service{ProjectID: f.svc.ProjectID, Name: "fn", SourceType: "function"}
	if err := f.st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	in := &store.Instance{ServiceID: svc.ID, EnvironmentID: f.env.ID, NodeID: store.LocalNodeID}
	in.Spec.Defaults()
	if err := f.st.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	d, err := f.m.Enqueue(ctx, in, EnqueueOpts{Trigger: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	f.m.execute(d.ID)
	d, _ = f.st.GetDeployment(ctx, d.ID)
	if d.Status != store.DepFailed || d.Error == nil || !strings.Contains(*d.Error, "function 코드가 없습니다") {
		t.Fatalf("status %s err %v", d.Status, fnDeref(d.Error))
	}
}

func fnDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
