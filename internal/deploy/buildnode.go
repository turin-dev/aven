package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/gzip"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/logbook"
	"github.com/turin-dev/aven/internal/store"
)

// buildsLocally는 원격 노드 배포의 빌드를 aven-server 노드에서 할지다(build_node=local인 저장소/업로드/function 소스).
func buildsLocally(t *Target, d *store.Deployment) bool {
	if isLocal(d.NodeID) || d.Spec.BuildNode != store.BuildNodeLocal {
		return false
	}
	switch t.Service.SourceType {
	case "repo", "function":
		return true
	}
	return strings.HasPrefix(d.Source, "upload:")
}

// imageSaver는 이미지를 docker save로 내보낼 수 있는 로컬 드라이버다(*node.Docker).
type imageSaver interface {
	SaveImage(ctx context.Context, image string, w io.Writer) error
}

type countingReader struct {
	r io.Reader
	n atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// shipImage는 로컬 노드에서 만든 image를 docker save | gzip으로 원격 노드 nodeID에 보내고 그 노드에서 docker load한다.
// 이미 있으면 아무것도 하지 않는다.
func (m *Manager) shipImage(ctx context.Context, b *logbook.Book, image, nodeID string) error {
	rd, ok := m.driverForNode(nodeID).(*remoteDriver)
	if !ok {
		return nil
	}
	if rd.ImageExists(ctx, image) {
		b.Printf("노드 %s: 이미지 %s가 이미 있습니다", nodeID, image)
		return nil
	}
	saver, ok := m.driverForNode(store.LocalNodeID).(imageSaver)
	if !ok {
		return errors.New("로컬 노드 드라이버가 이미지를 내보낼 수 없습니다")
	}
	b.Printf("빌드한 이미지 %s를 노드 %s로 보냅니다", image, nodeID)
	start := time.Now()
	pr, pw := io.Pipe()
	go func() {
		zw, _ := gzip.NewWriterLevel(pw, gzip.BestSpeed)
		err := saver.SaveImage(ctx, image, zw)
		if cerr := zw.Close(); err == nil {
			err = cerr
		}
		pw.CloseWithError(err)
	}()
	src := &countingReader{r: pr}
	err := stage(ctx, m.Timeouts.Build, "ship", func(ctx context.Context) error {
		return rd.upload(ctx, agentprotocol.UploadBeginRequest{Kind: agentprotocol.UploadImage, Image: image}, src, "", nil)
	})
	pr.CloseWithError(err)
	if err != nil {
		if strings.Contains(err.Error(), "unknown field") || strings.Contains(err.Error(), "not allowed") && strings.Contains(err.Error(), "upload kind") {
			return fmt.Errorf("노드 %s의 aven-agent가 이미지 전송(upload kind image)을 지원하지 않습니다. agent를 업그레이드하세요: %w", nodeID, err)
		}
		return fmt.Errorf("이미지를 노드 %s로 보내지 못했습니다: %w", nodeID, err)
	}
	b.Printf("노드 %s에 이미지를 올렸습니다 (%s 압축, %s)", nodeID, HumanBytes(src.n.Load()), time.Since(start).Round(time.Second))
	return nil
}

// placesOn은 배포가 nodeID 노드에 replica를 하나라도 띄우는지다.
func placesOn(d *store.Deployment, nodeID string) bool {
	for _, p := range planWeights(d) {
		if p.Replicas > 0 && nodeKey(p.NodeID) == nodeKey(nodeID) {
			return true
		}
	}
	return false
}
