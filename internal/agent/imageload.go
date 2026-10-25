package agent

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// maxImageUpload는 다른 노드에서 빌드해 보내는 이미지 아카이브의 상한이다.
const maxImageUpload = 32 << 30

// archiveImages는 docker save 아카이브(tar 또는 gzip tar)에 이름이 붙은 이미지를 모두 돌려준다.
// manifest.json의 RepoTags(기존 docker 로더)와 index.json의 이미지 이름 annotation(containerd 이미지 저장소)을 둘 다 본다.
func archiveImages(r io.Reader) ([]string, error) {
	br := bufio.NewReader(r)
	var src io.Reader = br
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		src = zr
	}
	tr := tar.NewReader(src)
	var names []string
	sawManifest := false
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read image archive: %w", err)
		}
		switch strings.TrimPrefix(h.Name, "./") {
		case "manifest.json":
			sawManifest = true
			var m []struct {
				RepoTags []string `json:"RepoTags"`
			}
			if err := json.NewDecoder(io.LimitReader(tr, 4<<20)).Decode(&m); err != nil {
				return nil, fmt.Errorf("image archive manifest.json: %w", err)
			}
			for _, e := range m {
				names = append(names, e.RepoTags...)
			}
		case "index.json":
			var idx struct {
				Manifests []struct {
					Annotations map[string]string `json:"annotations"`
				} `json:"manifests"`
			}
			if err := json.NewDecoder(io.LimitReader(tr, 4<<20)).Decode(&idx); err != nil {
				return nil, fmt.Errorf("image archive index.json: %w", err)
			}
			for _, m := range idx.Manifests {
				if n := m.Annotations["io.containerd.image.name"]; n != "" {
					names = append(names, n)
				}
			}
		}
	}
	if !sawManifest {
		return nil, errors.New("image archive has no manifest.json (not a docker save archive)")
	}
	return names, nil
}

// checkArchive는 아카이브의 이미지 이름이 모두 want 하나뿐인지 본다(docker.io/ 접두어는 같은 이름으로 친다).
func checkArchive(names []string, want string) error {
	if len(names) == 0 {
		return errors.New("image archive names no image")
	}
	for _, n := range names {
		if strings.TrimPrefix(n, "docker.io/") != want {
			return fmt.Errorf("image archive contains %q; only %s may be loaded", n, want)
		}
	}
	return nil
}

// loadImage는 업로드된 아카이브(이름은 keelImageRe로만 받는다: postgres:16 같은 다른 이미지를 덮어쓸 수 없다)를 검사한 뒤 docker load로 읽고, 이미지가 생겼는지 확인한다.
func (h *Handler) loadImage(ctx context.Context, f *os.File, image string) error {
	names, err := archiveImages(f)
	if err != nil {
		return err
	}
	if err := checkArchive(names, image); err != nil {
		return err
	}
	// docker load는 레이어를 푸는 도중 다른 정리(image prune)와 겹치면 실패해도 0으로 끝날 수 있다. 결과를 확인하고 한 번 더 시도한다.
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if err := h.Docker.LoadImage(ctx, f); err != nil {
			return err
		}
		if h.Docker.ImageExists(ctx, image) {
			return nil
		}
	}
	return fmt.Errorf("docker load finished but %s is missing (a concurrent image prune on the node?)", image)
}
