package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/agentprotocol"
)

// imageArchive는 docker save 모양의 최소 아카이브다(manifest.json + 선택적으로 index.json).
func imageArchive(t *testing.T, gz bool, repoTags []string, indexName string) []byte {
	t.Helper()
	var buf bytes.Buffer
	var w io.Writer = &buf
	var zw *gzip.Writer
	if gz {
		zw = gzip.NewWriter(&buf)
		w = zw
	}
	tw := tar.NewWriter(w)
	add := func(name, body string) {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))})
		io.WriteString(tw, body)
	}
	add("blobs/sha256/aaaa", "layer")
	if repoTags != nil {
		tags := `"` + strings.Join(repoTags, `","`) + `"`
		add("manifest.json", `[{"Config":"blobs/sha256/aaaa","RepoTags":[`+tags+`],"Layers":[]}]`)
	}
	if indexName != "" {
		add("index.json", `{"schemaVersion":2,"manifests":[{"annotations":{"io.containerd.image.name":"`+indexName+`","org.opencontainers.image.ref.name":"dep-abc"}}]}`)
	}
	tw.Close()
	if zw != nil {
		zw.Close()
	}
	return buf.Bytes()
}

func TestArchiveImages(t *testing.T) {
	const img = "keel/svc-abc:dep-abc"
	for _, gz := range []bool{false, true} {
		names, err := archiveImages(bytes.NewReader(imageArchive(t, gz, []string{img}, "docker.io/"+img)))
		if err != nil || checkArchive(names, img) != nil {
			t.Errorf("gzip=%v: %v %v", gz, names, err)
		}
	}
	// 다른 이미지를 몰래 실어 보내면 거부한다(manifest.json이든 index.json이든)
	names, _ := archiveImages(bytes.NewReader(imageArchive(t, true, []string{img, "postgres:16"}, "")))
	if err := checkArchive(names, img); err == nil || !strings.Contains(err.Error(), "postgres:16") {
		t.Errorf("extra RepoTag must be rejected: %v", err)
	}
	names, _ = archiveImages(bytes.NewReader(imageArchive(t, false, []string{img}, "docker.io/library/postgres:16")))
	if checkArchive(names, img) == nil {
		t.Error("a different index.json image name must be rejected")
	}
	if _, err := archiveImages(bytes.NewReader(imageArchive(t, false, nil, img))); err == nil {
		t.Error("an archive without manifest.json must be rejected")
	}
	if checkArchive(nil, img) == nil {
		t.Error("an archive without names must be rejected")
	}
}

type loadDocker struct {
	fakeDocker
	loaded []byte
	images map[string]bool
}

func (f *loadDocker) LoadImage(_ context.Context, r io.Reader) error {
	b, err := io.ReadAll(r)
	f.loaded = b
	f.images["keel/svc-abc:dep-abc"] = true
	return err
}
func (f *loadDocker) ImageExists(_ context.Context, image string) bool { return f.images[image] }

func TestUploadImage(t *testing.T) {
	const img = "keel/svc-abc:dep-abc"
	f := &loadDocker{images: map[string]bool{}}
	h := &Handler{Docker: f, Proxy: &fakeProxy{}, WorkDir: t.TempDir()}
	for _, bad := range []agentprotocol.UploadBeginRequest{
		{Kind: agentprotocol.UploadImage, Image: "postgres:16"},
		{Kind: agentprotocol.UploadImage, Image: ""},
		{Kind: agentprotocol.UploadImage, Image: img, Volume: "keel-vol-abcd"},
		{Kind: agentprotocol.UploadSource, Image: img},
	} {
		if _, err := handle(t, h, agentprotocol.OpUploadBegin, bad); err == nil {
			t.Errorf("upload.begin %+v should be rejected", bad)
		}
	}
	data := imageArchive(t, true, []string{img}, "")
	r, err := handle(t, h, agentprotocol.OpUploadBegin, agentprotocol.UploadBeginRequest{Kind: agentprotocol.UploadImage, Image: img})
	if err != nil {
		t.Fatal(err)
	}
	id := r.(*agentprotocol.UploadBeginResult).UploadID
	if _, err := handle(t, h, agentprotocol.OpUploadChunk, agentprotocol.UploadChunkRequest{UploadID: id, Data: data}); err != nil {
		t.Fatal(err)
	}
	if _, err := handle(t, h, agentprotocol.OpUploadFinish, agentprotocol.UploadFinishRequest{UploadID: id, Bytes: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f.loaded, data) {
		t.Error("docker load should get the whole archive from the start")
	}
	// 이름이 다른 이미지가 든 아카이브는 load하지 않는다
	f.loaded = nil
	evil := imageArchive(t, false, []string{"postgres:16"}, "")
	r, _ = handle(t, h, agentprotocol.OpUploadBegin, agentprotocol.UploadBeginRequest{Kind: agentprotocol.UploadImage, Image: img})
	id = r.(*agentprotocol.UploadBeginResult).UploadID
	handle(t, h, agentprotocol.OpUploadChunk, agentprotocol.UploadChunkRequest{UploadID: id, Data: evil})
	if _, err := handle(t, h, agentprotocol.OpUploadFinish, agentprotocol.UploadFinishRequest{UploadID: id, Bytes: int64(len(evil))}); err == nil || f.loaded != nil {
		t.Errorf("mismatched archive must not be loaded: %v", err)
	}
}
