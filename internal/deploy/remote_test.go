package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/logbook"
	"github.com/turin-dev/aven/internal/store"
)

// fakeHub는 agent 쪽 동작을 흉내 내는 AgentCaller다.
type fakeHub struct {
	mu       sync.Mutex
	calls    []string
	export   []byte
	uploaded map[string]*bytes.Buffer
	nextID   int
	failOp   string
	finish   any
}

func (f *fakeHub) Online(string) bool { return true }
func (f *fakeHub) Reply(context.Context, string, agentprotocol.Message) error {
	return nil
}
func (f *fakeHub) CallStream(_ context.Context, _ string, op string, req, res any, onChunk func(json.RawMessage)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, op)
	if op == f.failOp {
		return &agentprotocol.RemoteError{Msg: "boom"}
	}
	b, _ := json.Marshal(req)
	switch op {
	case agentprotocol.OpVolumeExport:
		seq := 0
		for off := 0; off < len(f.export); off += agentprotocol.MaxChunkBytes {
			end := off + agentprotocol.MaxChunkBytes
			if end > len(f.export) {
				end = len(f.export)
			}
			c, _ := json.Marshal(agentprotocol.DataChunk{Seq: seq, Data: f.export[off:end]})
			onChunk(c)
			seq++
		}
		return setResult(res, agentprotocol.VolumeExportResult{Bytes: int64(len(f.export))})
	case agentprotocol.OpUploadBegin:
		f.nextID++
		if f.uploaded == nil {
			f.uploaded = map[string]*bytes.Buffer{}
		}
		id := strings.Repeat("a", 23) + string(rune('0'+f.nextID))
		f.uploaded[id] = &bytes.Buffer{}
		return setResult(res, agentprotocol.UploadBeginResult{UploadID: id})
	case agentprotocol.OpUploadChunk:
		var c agentprotocol.UploadChunkRequest
		json.Unmarshal(b, &c)
		f.uploaded[c.UploadID].Write(c.Data)
		return nil
	case agentprotocol.OpUploadFinish:
		if f.finish != nil {
			return setResult(res, f.finish)
		}
	}
	return nil
}

func setResult(res, v any) error {
	b, _ := json.Marshal(v)
	return json.Unmarshal(b, res)
}

func TestRemoteExportReassemblesChunksInOrder(t *testing.T) {
	data := bytes.Repeat([]byte("abc"), agentprotocol.MaxChunkBytes) // 3 MiB → 3 chunks
	h := &fakeHub{export: data}
	d := &remoteDriver{hub: h, node: "nod_a"}
	var out bytes.Buffer
	if err := d.ExportVolume(context.Background(), "keel-vol-abcdefgh", "ignored", &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), data) {
		t.Fatalf("exported %d bytes, want %d", out.Len(), len(data))
	}
}

func TestRemoteImportUploadsInChunksWithinLimit(t *testing.T) {
	data := bytes.Repeat([]byte("z"), 2*agentprotocol.MaxChunkBytes+17)
	h := &fakeHub{}
	d := &remoteDriver{hub: h, node: "nod_a"}
	if err := d.ImportVolume(context.Background(), "keel-vol-abcdefgh", "x", bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	var got []byte
	for _, b := range h.uploaded {
		got = b.Bytes()
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("uploaded %d bytes, want %d", len(got), len(data))
	}
	chunks := 0
	for _, op := range h.calls {
		if op == agentprotocol.OpUploadChunk {
			chunks++
		}
	}
	if chunks != 3 || h.calls[0] != agentprotocol.OpUploadBegin || h.calls[len(h.calls)-1] != agentprotocol.OpUploadFinish {
		t.Fatalf("calls: %v", h.calls)
	}
	// 한 메시지는 MaxMessageBytes 안에 들어간다(base64 + JSON 오버헤드 포함).
	msg, _ := json.Marshal(agentprotocol.Message{ID: strings.Repeat("a", 24), Operation: agentprotocol.OpUploadChunk,
		Payload: mustJSON(agentprotocol.UploadChunkRequest{UploadID: strings.Repeat("a", 24), Seq: 1, Data: bytes.Repeat([]byte{0xff}, agentprotocol.MaxChunkBytes)})})
	if len(msg) > agentprotocol.MaxMessageBytes {
		t.Fatalf("chunk message %d bytes exceeds MaxMessageBytes", len(msg))
	}
}

func mustJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func TestRemoteImportAbortsWhenFinishFails(t *testing.T) {
	h := &fakeHub{failOp: agentprotocol.OpUploadFinish}
	d := &remoteDriver{hub: h, node: "nod_a"}
	if err := d.ImportVolume(context.Background(), "keel-vol-abcdefgh", "x", strings.NewReader("data")); err == nil {
		t.Fatal("import succeeded")
	}
	if h.calls[len(h.calls)-1] != agentprotocol.OpUploadAbort {
		t.Fatalf("upload was not aborted: %v", h.calls)
	}
}

// memDriver는 볼륨 복사 테스트용 Driver다.
type memDriver struct {
	Driver
	name       string
	vols       map[string][]byte
	ensured    []string
	removed    []string
	failImport bool
	failExport bool
}

func (m *memDriver) EnsureVolume(_ context.Context, n string, _ map[string]string) error {
	if m.vols == nil {
		m.vols = map[string][]byte{}
	}
	if _, ok := m.vols[n]; !ok {
		m.vols[n] = nil
	}
	m.ensured = append(m.ensured, n)
	return nil
}
func (m *memDriver) RemoveVolume(_ context.Context, n string) error {
	delete(m.vols, n)
	m.removed = append(m.removed, n)
	return nil
}
func (m *memDriver) ExportVolume(_ context.Context, v, _ string, w io.Writer) error {
	if m.failExport {
		w.Write([]byte("part"))
		return errors.New("export boom")
	}
	_, err := w.Write(m.vols[v])
	return err
}
func (m *memDriver) ImportVolume(_ context.Context, v, _ string, r io.Reader) error {
	b, _ := io.ReadAll(r)
	if m.failImport {
		return errors.New("import boom")
	}
	m.vols[v] = b
	return nil
}

func testBook(t *testing.T) *logbook.Book {
	t.Helper()
	reg, err := logbook.NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := reg.Open("dep_test")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCopyVolumesLeavesSourceUntouchedOnFailure(t *testing.T) {
	v := &store.Volume{ID: "vol_1", Name: "data", DockerName: "keel-vol-abcdefgh"}
	m := &Manager{}
	old, d := &store.Deployment{NodeID: "nod_a"}, &store.Deployment{ID: "dep_abcdefgh", NodeID: "nod_b"}
	for name, mk := range map[string]func() (*memDriver, *memDriver){
		"import fails": func() (*memDriver, *memDriver) {
			return &memDriver{vols: map[string][]byte{v.DockerName: []byte("precious")}}, &memDriver{failImport: true}
		},
		"export fails": func() (*memDriver, *memDriver) {
			return &memDriver{vols: map[string][]byte{v.DockerName: []byte("precious")}, failExport: true}, &memDriver{}
		},
	} {
		from, to := mk()
		to.vols = map[string][]byte{v.DockerName: nil}
		moved, err := m.copyVolumes(context.Background(), testBook(t), []*store.Volume{v}, from, to, old, d)
		if err == nil || moved != nil {
			t.Fatalf("%s: copy succeeded: %v", name, err)
		}
		if string(from.vols[v.DockerName]) != "precious" || len(from.removed) != 0 {
			t.Fatalf("%s: source volume was touched: %v removed=%v", name, from.vols, from.removed)
		}
		if len(to.removed) != 1 {
			t.Fatalf("%s: partial copy not cleaned up on the new node: %v", name, to.removed)
		}
	}
}

func TestCopyVolumesCopiesDataAndReportsOldLocation(t *testing.T) {
	v := &store.Volume{ID: "vol_1", Name: "data", DockerName: "keel-vol-abcdefgh"}
	from := &memDriver{vols: map[string][]byte{v.DockerName: bytes.Repeat([]byte("d"), 3<<20)}}
	to := &memDriver{vols: map[string][]byte{v.DockerName: nil}}
	m := &Manager{}
	moved, err := m.copyVolumes(context.Background(), testBook(t), []*store.Volume{v}, from, to, &store.Deployment{NodeID: "nod_a"}, &store.Deployment{ID: "dep_abcdefgh", NodeID: "nod_b"})
	if err != nil || len(moved) != 1 || moved[0].drv != Driver(from) || moved[0].name != v.DockerName || moved[0].node != "nod_a" {
		t.Fatalf("moved=%+v err=%v", moved, err)
	}
	if !bytes.Equal(to.vols[v.DockerName], from.vols[v.DockerName]) {
		t.Fatal("data differs after copy")
	}
	if len(from.removed) != 0 {
		t.Fatal("source removed before cutover")
	}
}

func TestUploadSourceReturnsWorkspace(t *testing.T) {
	h := &fakeHub{finish: agentprotocol.FetchResult{Workspace: strings.Repeat("b", 24), ConfigName: "aven.json"}}
	d := &remoteDriver{hub: h, node: "nod_a"}
	res, err := d.uploadSource(context.Background(), strings.NewReader("tarball"), "app")
	if err != nil || res.Workspace != strings.Repeat("b", 24) || res.ConfigName != "aven.json" {
		t.Fatalf("%+v %v", res, err)
	}
	for _, b := range h.uploaded {
		if b.String() != "tarball" {
			t.Fatalf("uploaded %q", b.String())
		}
	}
}
