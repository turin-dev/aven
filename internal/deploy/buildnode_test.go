package deploy

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/klauspost/compress/gzip"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/store"
)

type saveFake struct {
	Driver
	data  []byte
	saved []string
}

func (s *saveFake) SaveImage(_ context.Context, image string, w io.Writer) error {
	s.saved = append(s.saved, image)
	_, err := w.Write(s.data)
	return err
}

func TestShipImageStreamsGzipSave(t *testing.T) {
	data := bytes.Repeat([]byte("layer"), agentprotocol.MaxChunkBytes/2) // 여러 chunk
	local := &saveFake{data: data}
	h := &fakeHub{}
	m := &Manager{Agents: h, localDrv: local, Timeouts: DefaultTimeouts}
	if err := m.shipImage(context.Background(), testBook(t), "keel/svc-a:dep-a", "nod_remote"); err != nil {
		t.Fatal(err)
	}
	if len(local.saved) != 1 || local.saved[0] != "keel/svc-a:dep-a" {
		t.Fatalf("saved %v", local.saved)
	}
	var up []byte
	for _, b := range h.uploaded {
		up = b.Bytes()
	}
	zr, err := gzip.NewReader(bytes.NewReader(up))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if !bytes.Equal(got, data) {
		t.Fatalf("uploaded archive does not gunzip to the docker save output (%d vs %d bytes)", len(got), len(data))
	}
	if h.calls[len(h.calls)-1] != agentprotocol.OpUploadFinish {
		t.Fatalf("calls: %v", h.calls)
	}
	// 실패하면 업로드를 중단한다
	h2 := &fakeHub{failOp: agentprotocol.OpUploadFinish}
	m.Agents = h2
	if err := m.shipImage(context.Background(), testBook(t), "keel/svc-a:dep-a", "nod_remote"); err == nil {
		t.Fatal("finish failure must be reported")
	}
	if h2.calls[len(h2.calls)-1] != agentprotocol.OpUploadAbort {
		t.Fatalf("upload should be aborted: %v", h2.calls)
	}
}

func TestBuildsLocally(t *testing.T) {
	repo := &Target{Service: &store.Service{SourceType: "repo"}}
	img := &Target{Service: &store.Service{SourceType: "image"}}
	dep := func(node, bn, src string) *store.Deployment {
		d := &store.Deployment{NodeID: node, Source: src}
		d.Spec.BuildNode = bn
		return d
	}
	cases := []struct {
		t    *Target
		d    *store.Deployment
		want bool
	}{
		{repo, dep("nod_remote", "local", ""), true},
		{repo, dep("nod_remote", "", ""), false},
		{repo, dep(store.LocalNodeID, "local", ""), false},
		{img, dep("nod_remote", "local", ""), false},             // pull은 대상 노드에서
		{img, dep("nod_remote", "local", "upload:/x.tgz"), true}, // aven up 소스는 빌드한다
	}
	for i, c := range cases {
		if got := buildsLocally(c.t, c.d); got != c.want {
			t.Errorf("case %d: got %v", i, got)
		}
	}
}
