package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/proxy"
)

type extraDocker struct {
	fakeDocker
	exportData []byte
	imported   []byte
	importedTo string
	managed    map[string]map[string]string
	events     []node.ContainerEvent
}

func (f *extraDocker) ExportVolume(_ context.Context, vol, _ string, w io.Writer) error {
	_, err := w.Write(f.exportData)
	return err
}
func (f *extraDocker) ImportVolume(_ context.Context, vol, _ string, r io.Reader) error {
	b, err := io.ReadAll(r)
	f.imported, f.importedTo = b, vol
	return err
}
func (f *extraDocker) ListManaged(context.Context) (map[string]map[string]string, error) {
	return f.managed, nil
}
func (f *extraDocker) Events(ctx context.Context, out func(node.ContainerEvent)) error {
	for _, e := range f.events {
		out(e)
	}
	return errors.New("events closed")
}

func validTCP() agentprotocol.TCPProxyRequest {
	return agentprotocol.TCPProxyRequest{InstanceID: "ins_abcdefghijklm", ProjectID: "prj_abc", EnvID: "env_abc", Network: "keel-env_abcdefghijklm",
		Target: "web.production.keel.internal", PublicPort: 25000, ContainerPort: 5432}
}

func TestTCPProxyEnsureBuildsOwnSpec(t *testing.T) {
	f := &extraDocker{}
	h := &Handler{Docker: f, Proxy: &fakeProxy{}, TCPBind: "::"}
	if _, err := handle(t, h, agentprotocol.OpTCPEnsure, validTCP()); err != nil {
		t.Fatal(err)
	}
	if len(f.ran) != 1 {
		t.Fatalf("ran %d", len(f.ran))
	}
	s := f.ran[0]
	if s.Name != "keel-tcp-abcdefghijklm" || s.Image != node.TCPForwardImage || s.Labels[node.LabelRole] != node.RoleTCPProxy ||
		len(s.Publish) != 1 || s.Publish[0] != "[::]:25000:5432" {
		t.Fatalf("spec: %+v", s)
	}
}

func TestTCPProxyRejectedInputs(t *testing.T) {
	f := &extraDocker{}
	h := &Handler{Docker: f, Proxy: &fakeProxy{}}
	mut := map[string]func(*agentprotocol.TCPProxyRequest){
		"port 80":          func(r *agentprotocol.TCPProxyRequest) { r.PublicPort = 80 },
		"port 443":         func(r *agentprotocol.TCPProxyRequest) { r.PublicPort = 443 },
		"port 22":          func(r *agentprotocol.TCPProxyRequest) { r.PublicPort = 22 },
		"port 1023":        func(r *agentprotocol.TCPProxyRequest) { r.PublicPort = 1023 },
		"port 70000":       func(r *agentprotocol.TCPProxyRequest) { r.PublicPort = 70000 },
		"container port 0": func(r *agentprotocol.TCPProxyRequest) { r.ContainerPort = 0 },
		"bad instance":     func(r *agentprotocol.TCPProxyRequest) { r.InstanceID = "ins_../x" },
		"foreign network":  func(r *agentprotocol.TCPProxyRequest) { r.Network = "dokploy-network" },
		"external target":  func(r *agentprotocol.TCPProxyRequest) { r.Target = "evil.example.com" },
		"ip target":        func(r *agentprotocol.TCPProxyRequest) { r.Target = "169.254.169.254" },
		"suffix trick":     func(r *agentprotocol.TCPProxyRequest) { r.Target = "a.b.keel.internal.evil.com" },
		"bad project":      func(r *agentprotocol.TCPProxyRequest) { r.ProjectID = "p r" },
	}
	for name, m := range mut {
		r := validTCP()
		m(&r)
		if _, err := handle(t, h, agentprotocol.OpTCPEnsure, r); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if len(f.ran) != 0 {
		t.Fatal("container started for rejected input")
	}
	// 서버가 publish나 임의 스펙을 끼워 보낼 수 없다.
	if _, err := h.Handle(context.Background(), agentprotocol.OpTCPEnsure, []byte(`{"instance_id":"ins_abcdefghijklm","publish":["22:22"]}`), func(any) {}); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestTCPProxyNeverTouchesForeignContainers(t *testing.T) {
	f := &extraDocker{fakeDocker: fakeDocker{states: map[string]*node.ContainerState{
		"keel-tcp-abcdefghijklm": {Name: "keel-tcp-abcdefghijklm", Labels: map[string]string{}},
	}}}
	h := &Handler{Docker: f, Proxy: &fakeProxy{}}
	if _, err := handle(t, h, agentprotocol.OpTCPEnsure, validTCP()); err == nil {
		t.Fatal("ensure replaced a foreign container")
	}
	if _, err := handle(t, h, agentprotocol.OpTCPRemove, agentprotocol.TCPRemoveRequest{InstanceID: "ins_abcdefghijklm"}); err == nil {
		t.Fatal("remove deleted a foreign container")
	}
	if _, err := handle(t, h, agentprotocol.OpTCPRemove, agentprotocol.TCPRemoveRequest{InstanceID: "x; rm"}); err == nil {
		t.Fatal("bad id accepted")
	}
	if len(f.removed) != 0 || len(f.ran) != 0 {
		t.Fatalf("docker touched: %v %v", f.removed, f.ran)
	}
	// 같은 설정의 실행 중 포워더는 그대로 둔다.
	sig := "25000:5432:web.production.keel.internal"
	f.states["keel-tcp-abcdefghijklm"] = &node.ContainerState{Running: true, Labels: map[string]string{node.LabelManaged: "true", node.LabelRole: node.RoleTCPProxy, "keel.tcp": sig}}
	if _, err := handle(t, h, agentprotocol.OpTCPEnsure, validTCP()); err != nil || len(f.ran) != 0 || len(f.removed) != 0 {
		t.Fatalf("idempotent ensure: %v ran=%v", err, f.ran)
	}
}

func TestTCPContainersCanBeRemovedByServerAction(t *testing.T) {
	f := &extraDocker{fakeDocker: fakeDocker{states: map[string]*node.ContainerState{
		"keel-tcp-abcdefghijklm": {Labels: map[string]string{node.LabelManaged: "true", node.LabelRole: node.RoleTCPProxy}},
	}}}
	h := &Handler{Docker: f, Proxy: &fakeProxy{}}
	if _, err := handle(t, h, agentprotocol.OpContainerRemove, agentprotocol.ContainerRequest{Name: "keel-tcp-abcdefghijklm"}); err != nil || len(f.removed) != 1 {
		t.Fatalf("remove tcp proxy: %v", err)
	}
	// container.run은 여전히 tcpproxy 역할을 받지 않는다(포트 publish 경로는 tcpproxy.ensure뿐).
	s := validSpec()
	s.Labels[node.LabelRole] = node.RoleTCPProxy
	if _, err := handle(t, h, agentprotocol.OpContainerRun, agentprotocol.RunRequest{Spec: s}); err == nil {
		t.Fatal("run accepted role tcpproxy")
	}
}

func TestWakeUpstreamAllowedOnlyExactSocket(t *testing.T) {
	p := &fakeProxy{}
	h := &Handler{Docker: &fakeDocker{}, Proxy: p}
	good := []proxy.Route{{Hosts: []string{"app.example.com"}, Upstreams: []string{proxy.WakeDial}}}
	if _, err := handle(t, h, agentprotocol.OpProxyApply, agentprotocol.ProxyRequest{Routes: good}); err != nil {
		t.Fatal(err)
	}
	for _, up := range []string{"unix//run/keel/other.sock", "unix//run/keel/wake.sock/../x", "unix//var/run/docker.sock", "unix//run/keel/wake.sock "} {
		bad := []proxy.Route{{Hosts: []string{"app.example.com"}, Upstreams: []string{up}}}
		if _, err := handle(t, h, agentprotocol.OpProxyApply, agentprotocol.ProxyRequest{Routes: bad}); err == nil {
			t.Fatalf("upstream %q accepted", up)
		}
	}
}

func TestValidateWakeResult(t *testing.T) {
	ok := agentprotocol.WakeResult{Network: "keel-env_abcdefghijklm", Container: "keel-web-abcdefgh-1", Port: 80}
	if err := ValidateWakeResult(ok); err != nil {
		t.Fatal(err)
	}
	for _, r := range []agentprotocol.WakeResult{
		{Network: "bridge", Container: ok.Container, Port: 80},
		{Network: ok.Network, Container: "dokploy-postgres", Port: 80},
		{Network: ok.Network, Container: ok.Container, Port: 0},
		{Network: ok.Network, Container: ok.Container, Port: 70000},
	} {
		if ValidateWakeResult(r) == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
}

func TestWakeHandlerRejectsBadHostAndDisconnected(t *testing.T) {
	h := &Handler{Docker: &fakeDocker{}, Proxy: &fakeProxy{}}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "bad host!"
	h.WakeHandler().ServeHTTP(rec, r)
	if rec.Code != 400 {
		t.Fatalf("bad host: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/", nil)
	r.Host = "app.example.com"
	h.WakeHandler().ServeHTTP(rec, r)
	if rec.Code != 503 {
		t.Fatalf("not connected: %d", rec.Code)
	}
}

func TestAskServerRoundTripThroughServe(t *testing.T) {
	h := &Handler{Docker: &fakeDocker{}, Proxy: &fakeProxy{}}
	in := make(chan []byte, 4)
	out := make(chan agentprotocol.Message, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.Serve(ctx, func(c context.Context) ([]byte, error) {
			select {
			case b := <-in:
				return b, nil
			case <-c.Done():
				return nil, c.Err()
			}
		}, func(_ context.Context, m agentprotocol.Message) error { out <- m; return nil })
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for h.getLink() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	var res agentprotocol.WakeResult
	errc := make(chan error, 1)
	go func() {
		errc <- h.AskServer(context.Background(), agentprotocol.OpWake, agentprotocol.WakeRequest{Host: "a.example.com"}, &res)
	}()
	req := <-out
	if !req.Up || req.Operation != agentprotocol.OpWake {
		t.Fatalf("request: %+v", req)
	}
	// 서버의 답(Reply)은 요청으로 처리되지 않고 AskServer로 간다. 답 id가 틀리면 무시된다.
	in <- []byte(`{"id":"zzz","reply":true,"payload":{"container":"keel-x-abcdefgh-1"}}`)
	in <- []byte(`{"id":"` + req.ID + `","operation":"wake","reply":true,"payload":{"network":"keel-env_abcdefghijklm","container":"keel-web-abcdefgh-1","port":80}}`)
	if err := <-errc; err != nil || res.Container != "keel-web-abcdefgh-1" || res.Port != 80 {
		t.Fatalf("ask: %v %+v", err, res)
	}
	cancel()
	<-done
	if h.AskServer(context.Background(), agentprotocol.OpWake, struct{}{}, nil) == nil {
		t.Fatal("ask after disconnect succeeded")
	}
}

func TestVolumeExportChunksAndValidation(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 2*agentprotocol.MaxChunkBytes+123)
	f := &extraDocker{exportData: data}
	h := &Handler{Docker: f, Proxy: &fakeProxy{}}
	var chunks []agentprotocol.DataChunk
	res, err := h.Handle(context.Background(), agentprotocol.OpVolumeExport, []byte(`{"name":"keel-vol-abcdefgh"}`), func(v any) { chunks = append(chunks, v.(agentprotocol.DataChunk)) })
	if err != nil {
		t.Fatal(err)
	}
	var got []byte
	for i, c := range chunks {
		if c.Seq != i || len(c.Data) > agentprotocol.MaxChunkBytes {
			t.Fatalf("chunk %d: seq=%d len=%d", i, c.Seq, len(c.Data))
		}
		got = append(got, c.Data...)
	}
	if !bytes.Equal(got, data) || res.(agentprotocol.VolumeExportResult).Bytes != int64(len(data)) || len(chunks) != 3 {
		t.Fatalf("export: %d chunks, %d bytes", len(chunks), len(got))
	}
	for _, name := range []string{"dokploy-postgres-data", "keel-vol-", "../keel-vol-abcdefgh", "keel-proxy-wake", ""} {
		if _, err := handle(t, h, agentprotocol.OpVolumeExport, agentprotocol.VolumeExportRequest{Name: name}); err == nil {
			t.Fatalf("export of %q accepted", name)
		}
	}
}

func doUpload(t *testing.T, h *Handler, kind, vol string, data []byte) (any, error) {
	t.Helper()
	r, err := handle(t, h, agentprotocol.OpUploadBegin, agentprotocol.UploadBeginRequest{Kind: kind, Volume: vol})
	if err != nil {
		return nil, err
	}
	id := r.(*agentprotocol.UploadBeginResult).UploadID
	for seq, off := 0, 0; off < len(data); seq++ {
		n := len(data) - off
		if n > agentprotocol.MaxChunkBytes {
			n = agentprotocol.MaxChunkBytes
		}
		if _, err := handle(t, h, agentprotocol.OpUploadChunk, agentprotocol.UploadChunkRequest{UploadID: id, Seq: seq, Data: data[off : off+n]}); err != nil {
			return nil, err
		}
		off += n
	}
	return handle(t, h, agentprotocol.OpUploadFinish, agentprotocol.UploadFinishRequest{UploadID: id, Bytes: int64(len(data))})
}

func TestVolumeUploadImportsAfterFullReceipt(t *testing.T) {
	f := &extraDocker{}
	h := &Handler{Docker: f, Proxy: &fakeProxy{}, WorkDir: t.TempDir()}
	data := bytes.Repeat([]byte("v"), agentprotocol.MaxChunkBytes+5)
	if _, err := doUpload(t, h, agentprotocol.UploadVolume, "keel-vol-abcdefgh", data); err != nil {
		t.Fatal(err)
	}
	if f.importedTo != "keel-vol-abcdefgh" || !bytes.Equal(f.imported, data) {
		t.Fatalf("imported %q %d bytes", f.importedTo, len(f.imported))
	}
	if entries, _ := os.ReadDir(h.uploadDir()); len(entries) != 0 {
		t.Fatalf("upload temp files left: %v", entries)
	}
}

func TestUploadRejections(t *testing.T) {
	f := &extraDocker{}
	h := &Handler{Docker: f, Proxy: &fakeProxy{}, WorkDir: t.TempDir()}
	for _, r := range []agentprotocol.UploadBeginRequest{
		{Kind: "volume", Volume: "dokploy-pg"}, {Kind: "volume"}, {Kind: "shell"}, {Kind: "source", Volume: "keel-vol-abcdefgh"}, {Kind: ""},
	} {
		if _, err := handle(t, h, agentprotocol.OpUploadBegin, r); err == nil {
			t.Fatalf("begin %+v accepted", r)
		}
	}
	r, err := handle(t, h, agentprotocol.OpUploadBegin, agentprotocol.UploadBeginRequest{Kind: "volume", Volume: "keel-vol-abcdefgh"})
	if err != nil {
		t.Fatal(err)
	}
	id := r.(*agentprotocol.UploadBeginResult).UploadID
	bad := []agentprotocol.UploadChunkRequest{
		{UploadID: id, Seq: 1, Data: []byte("x")},                                 // 순서 어긋남
		{UploadID: id, Seq: 0, Data: nil},                                         // 빈 chunk
		{UploadID: id, Seq: 0, Data: make([]byte, agentprotocol.MaxChunkBytes+1)}, // 너무 큼
		{UploadID: "../../etc/passwd", Seq: 0, Data: []byte("x")},                 // 잘못된 id
		{UploadID: strings.Repeat("a", 24), Seq: 0, Data: []byte("x")},            // 모르는 업로드
	}
	for i, c := range bad {
		if _, err := handle(t, h, agentprotocol.OpUploadChunk, c); err == nil {
			t.Fatalf("chunk %d accepted", i)
		}
	}
	// 받은 양이 다르면 import하지 않는다.
	handle(t, h, agentprotocol.OpUploadChunk, agentprotocol.UploadChunkRequest{UploadID: id, Seq: 0, Data: []byte("abc")})
	if _, err := handle(t, h, agentprotocol.OpUploadFinish, agentprotocol.UploadFinishRequest{UploadID: id, Bytes: 999}); err == nil {
		t.Fatal("finish with wrong size accepted")
	}
	if f.importedTo != "" {
		t.Fatal("volume imported from an incomplete upload")
	}
	// 동시 업로드 수 제한과 abort.
	var ids []string
	for i := 0; i < maxUploads; i++ {
		r, err := handle(t, h, agentprotocol.OpUploadBegin, agentprotocol.UploadBeginRequest{Kind: "source"})
		if err != nil {
			t.Fatalf("begin %d: %v", i, err)
		}
		ids = append(ids, r.(*agentprotocol.UploadBeginResult).UploadID)
	}
	if _, err := handle(t, h, agentprotocol.OpUploadBegin, agentprotocol.UploadBeginRequest{Kind: "source"}); err == nil {
		t.Fatal("upload limit not enforced")
	}
	if _, err := handle(t, h, agentprotocol.OpUploadAbort, agentprotocol.UploadAbortRequest{UploadID: ids[0]}); err != nil {
		t.Fatal(err)
	}
	if _, err := handle(t, h, agentprotocol.OpUploadAbort, agentprotocol.UploadAbortRequest{UploadID: "../x"}); err == nil {
		t.Fatal("bad abort id accepted")
	}
	h.DropUploads()
	if entries, _ := os.ReadDir(h.uploadDir()); len(entries) != 0 {
		t.Fatalf("uploads left after DropUploads: %v", entries)
	}
}

func TestVolumeImportRefusedWhileInUse(t *testing.T) {
	f := &extraDocker{
		fakeDocker: fakeDocker{states: map[string]*node.ContainerState{
			"keel-db-abcdefgh-1": {Name: "keel-db-abcdefgh-1", Running: true, Volumes: []string{"keel-vol-abcdefgh"}, Labels: map[string]string{node.LabelManaged: "true"}},
		}},
		managed: map[string]map[string]string{"keel-db-abcdefgh-1": {node.LabelManaged: "true"}},
	}
	h := &Handler{Docker: f, Proxy: &fakeProxy{}, WorkDir: t.TempDir()}
	if _, err := doUpload(t, h, agentprotocol.UploadVolume, "keel-vol-abcdefgh", []byte("data")); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("import into volume in use: %v", err)
	}
	if f.importedTo != "" {
		t.Fatal("volume was wiped while in use")
	}
	f.states["keel-db-abcdefgh-1"].Running = false
	if _, err := doUpload(t, h, agentprotocol.UploadVolume, "keel-vol-abcdefgh", []byte("data")); err != nil {
		t.Fatalf("import into idle volume: %v", err)
	}
}

func makeTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestSourceUploadExtractsSafely(t *testing.T) {
	dir := t.TempDir()
	h := &Handler{Docker: &fakeDocker{}, Proxy: &fakeProxy{}, WorkDir: dir}
	res, err := doUpload(t, h, agentprotocol.UploadSource, "", makeTarGz(t, map[string]string{"Dockerfile": "FROM scratch", "aven.json": `{"build":{}}`}))
	if err != nil {
		t.Fatal(err)
	}
	fr := res.(*agentprotocol.FetchResult)
	if len(fr.Workspace) != 24 || fr.ConfigName != "aven.json" {
		t.Fatalf("result: %+v", fr)
	}
	if _, err := os.Stat(filepath.Join(dir, fr.Workspace, "src", "Dockerfile")); err != nil {
		t.Fatal(err)
	}
	// 경로 탈출 이름은 workspace 밖에 파일을 만들지 못한다(안쪽으로 정규화되거나 거부된다).
	evil, err := doUpload(t, h, agentprotocol.UploadSource, "", makeTarGz(t, map[string]string{"../../escape.txt": "x"}))
	if _, serr := os.Stat(filepath.Join(filepath.Dir(dir), "escape.txt")); serr == nil {
		t.Fatal("file escaped the workspace")
	}
	if _, serr := os.Stat(filepath.Join(dir, "escape.txt")); serr == nil {
		t.Fatal("file escaped src")
	}
	if err == nil {
		if _, serr := os.Stat(filepath.Join(dir, evil.(*agentprotocol.FetchResult).Workspace, "src", "escape.txt")); serr != nil {
			t.Fatalf("neutralized file missing: %v", serr)
		}
	}
	if _, err := doUpload(t, h, agentprotocol.UploadSource, "", []byte("not a tarball")); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestEventsOnlyManagedAndKeelLabels(t *testing.T) {
	f := &extraDocker{events: []node.ContainerEvent{
		{Action: "die", Name: "keel-web-abcdefgh-1", Exit: "1", Labels: map[string]string{node.LabelManaged: "true", node.LabelRole: node.RoleApp, "com.docker.compose.project": "secret", "image": "x"}},
		{Action: "die", Name: "dokploy-postgres", Labels: map[string]string{node.LabelManaged: "true"}},
		{Action: "die", Name: "keel-other", Labels: map[string]string{}},
	}}
	h := &Handler{Docker: f, Proxy: &fakeProxy{}}
	var got []agentprotocol.EventChunk
	_, err := h.Handle(context.Background(), agentprotocol.OpEvents, []byte(`{}`), func(v any) { got = append(got, v.(agentprotocol.EventChunk)) })
	if err == nil {
		t.Fatal("events should end with an error so the server resubscribes")
	}
	if len(got) != 1 || got[0].Name != "keel-web-abcdefgh-1" || got[0].Exit != "1" {
		t.Fatalf("events: %+v", got)
	}
	for k := range got[0].Labels {
		if !strings.HasPrefix(k, "keel.") {
			t.Fatalf("non-keel label leaked: %q", k)
		}
	}
	if _, err := h.Handle(context.Background(), agentprotocol.OpEvents, []byte(`{"filter":"x"}`), func(any) {}); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestAccessLogStreamsUntilCancelled(t *testing.T) {
	var mu sync.Mutex
	var got []proxy.AccessLog
	p := &streamProxy{logs: []proxy.AccessLog{{Host: "a.example.com", Status: 200}, {Host: "b.example.com", Status: 404}}}
	h := &Handler{Docker: &fakeDocker{}, Proxy: p}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := h.Handle(ctx, agentprotocol.OpAccessLogs, []byte(`{}`), func(v any) { mu.Lock(); got = append(got, v.(proxy.AccessLog)); mu.Unlock() })
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("handle returned %v", err)
	}
	if len(got) != 2 || got[1].Status != 404 {
		t.Fatalf("access logs: %+v", got)
	}
	if _, err := h.Handle(context.Background(), agentprotocol.OpAccessLogs, []byte(`{"path":"/etc/shadow"}`), func(any) {}); err == nil {
		t.Fatal("unknown field accepted")
	}
}

type streamProxy struct {
	fakeProxy
	logs []proxy.AccessLog
}

func (p *streamProxy) FollowAccessLogs(ctx context.Context, out func(proxy.AccessLog)) {
	for _, l := range p.logs {
		out(l)
	}
	<-ctx.Done()
}

func TestNodeStatsStrictPayload(t *testing.T) {
	h := &Handler{Docker: &fakeDocker{}, Proxy: &fakeProxy{}, DataDir: t.TempDir()}
	res, err := handle(t, h, agentprotocol.OpNodeStats, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if s := res.(agentprotocol.NodeStats); s.DiskFreeBytes == 0 || s.CPUPercent < 0 || s.CPUPercent > 100 {
		t.Fatalf("stats: %+v", s)
	}
	if _, err := h.Handle(context.Background(), agentprotocol.OpNodeStats, []byte(`{"path":"/"}`), func(any) {}); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestRailpackAutoDownloadVerifiesChecksum(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := "#!/bin/sh\necho ok\n"
	tw.WriteHeader(&tar.Header{Name: "railpack", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write([]byte(body))
	tw.Close()
	gz.Close()
	sum := sha256.Sum256(buf.Bytes())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(buf.Bytes()) }))
	defer srv.Close()
	dir := t.TempDir()
	dest := filepath.Join(dir, "rp")
	if err := DownloadRailpack(context.Background(), srv.Client(), srv.URL+"/", "x.tar.gz", strings.Repeat("0", 64), dir, dest); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("wrong checksum accepted: %v", err)
	}
	if _, err := os.Stat(dest); err == nil {
		t.Fatal("binary installed despite checksum mismatch")
	}
	if err := DownloadRailpack(context.Background(), srv.Client(), srv.URL+"/", "x.tar.gz", hex.EncodeToString(sum[:]), dir, dest); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(dest); err != nil || st.Mode()&0o111 == 0 {
		t.Fatalf("installed binary: %v %v", st, err)
	}
	// off면 railpack 빌드를 거부한다.
	h := &Handler{Docker: &fakeDocker{}, Proxy: &fakeProxy{}, Railpack: "off"}
	if _, err := h.railpackPath(context.Background(), func(string) {}); err == nil {
		t.Fatal("railpack off did not refuse")
	}
	// auto는 고정된 URL이 아닌 곳에서 받지 않는다: 테스트용 base를 지정한 Handler로 확인.
	h = &Handler{Docker: &fakeDocker{}, Proxy: &fakeProxy{}, Railpack: RailpackAuto, DataDir: t.TempDir(), railpackBase: srv.URL + "/"}
	if _, err := h.railpackPath(context.Background(), func(string) {}); err == nil {
		t.Fatal("auto download with a non-pinned checksum succeeded")
	}
}

func TestValidateRoutesNewFields(t *testing.T) {
	ok := proxy.Route{Hosts: []string{"a.example.com"}, Upstreams: []string{"keel-x-1:80"}, Path: "/api/*", TLS: proxy.TLSOff,
		Redirect: nil, Auth: &proxy.BasicAuth{User: "bob", Hash: "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ01234"}}
	if err := ValidateRoutes([]proxy.Route{ok}); err != nil {
		t.Fatalf("valid route rejected: %v", err)
	}
	bad := []proxy.Route{
		{Hosts: []string{"a.example.com"}, Upstreams: []string{"keel-x-1:80"}, Path: "/a{http.request.uri}"},
		{Hosts: []string{"a.example.com"}, Upstreams: []string{"keel-x-1:80"}, Path: "nolead"},
		{Hosts: []string{"a.example.com"}, Upstreams: []string{"keel-x-1:80"}, TLS: "weird"},
		{Hosts: []string{"a.example.com"}, Redirect: &proxy.Redirect{To: "x.com{http.request.host}"}},
		{Hosts: []string{"a.example.com"}, Redirect: &proxy.Redirect{To: "javascript:alert(1)"}},
		{Hosts: []string{"a.example.com"}, Upstreams: []string{"keel-x-1:80"}, Auth: &proxy.BasicAuth{User: "bob", Hash: "plaintext"}},
	}
	for i, r := range bad {
		if err := ValidateRoutes([]proxy.Route{r}); err == nil {
			t.Errorf("bad route %d accepted: %+v", i, r)
		}
	}
	if err := ValidateRunSpec(node.RunSpec{Name: "keel-x-1", Image: "nginx", Labels: map[string]string{node.LabelManaged: "true", node.LabelRole: node.RoleApp},
		Network: "keel-env_abcd1234", MemoryMB: 256, CPUs: "0.5"}, map[string]bool{node.RoleApp: true}); err != nil {
		t.Errorf("limits rejected: %v", err)
	}
	if err := ValidateRunSpec(node.RunSpec{Name: "keel-x-1", Image: "nginx", Labels: map[string]string{node.LabelManaged: "true", node.LabelRole: node.RoleApp},
		Network: "keel-env_abcd1234", CPUs: "1;rm"}, map[string]bool{node.RoleApp: true}); err == nil {
		t.Error("bad cpus accepted")
	}
}

func TestValidateRoutesExternalAllowlist(t *testing.T) {
	r := proxy.Route{Hosts: []string{"dash.example.com"}, Upstreams: []string{"grafana-1:3000"}}
	if ValidateRoutes([]proxy.Route{r}) == nil {
		t.Fatal("a non-aven upstream must be rejected without an allowlist")
	}
	if err := ValidateRoutesWith([]proxy.Route{r}, []string{"grafana-1:3000"}); err != nil {
		t.Fatalf("allowlisted upstream: %v", err)
	}
	if ValidateRoutesWith([]proxy.Route{r}, []string{"grafana-2:3000"}) == nil {
		t.Fatal("only the exact allowlisted host:port passes")
	}
}

func TestValidateRoutesRejectsHeaderInjection(t *testing.T) {
	r := proxy.Route{Hosts: []string{"a.example.com"}, Upstreams: []string{"keel-a-1:80"}, SetHeaders: map[string]string{"X-Keel-Gateway": "x"}}
	if ValidateRoutesWith([]proxy.Route{r}, nil) == nil {
		t.Fatal("remote nodes must not accept header injection")
	}
}
