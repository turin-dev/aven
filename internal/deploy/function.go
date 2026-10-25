package deploy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/logbook"
	"github.com/turin-dev/aven/internal/store"
)

// FunctionBunImage는 function 서비스를 돌리는 Bun 이미지다(마이너 버전 고정).
const FunctionBunImage = "oven/bun:1.2-alpine"

// FunctionEntry는 function 코드가 들어가는 파일 이름이다(TypeScript/JavaScript 모두 bun이 바로 실행한다).
const FunctionEntry = "index.ts"

// DefaultFunctionCode는 새 function 서비스의 기본 코드다(PORT에서 HTTP로 응답한다).
const DefaultFunctionCode = `// Bun으로 실행되는 function입니다. PORT 환경 변수에서 HTTP 요청을 받습니다.
// cron_schedule을 정하면 일정마다 이 파일을 실행하고 끝나기를 기다리는 cron job이 됩니다.
const port = Number(process.env.PORT ?? 8080);

Bun.serve({
  port,
  fetch(req) {
    const url = new URL(req.url);
    return Response.json({ message: "Hello from aven function", path: url.pathname });
  },
});

console.log(` + "`listening on :${port}`" + `);
`

// FunctionDockerfile은 function 빌드 컨텍스트의 Dockerfile이다.
func FunctionDockerfile() string {
	return "FROM " + FunctionBunImage + "\n" +
		"WORKDIR /app\n" +
		"COPY " + FunctionEntry + " ./" + FunctionEntry + "\n" +
		"ENV NODE_ENV=production\n" +
		"EXPOSE 8080\n" +
		`CMD ["bun", "run", "` + FunctionEntry + `"]` + "\n"
}

// FunctionContext는 function 코드로 빌드 컨텍스트(tar.gz: Dockerfile + index.ts)를 만든다.
func FunctionContext(code string) ([]byte, error) {
	if len(code) > store.MaxFunctionCodeBytes {
		return nil, errors.New("function code is larger than 1 MiB")
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	now := time.Now()
	for _, f := range []struct{ name, body string }{{"Dockerfile", FunctionDockerfile()}, {FunctionEntry, code}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body)), ModTime: now, Typeflag: tar.TypeReg}); err != nil {
			return nil, err
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// functionUpload는 배포의 코드 스냅샷으로 빌드 컨텍스트 tar.gz를 WorkDir에 만들고 그 경로를 돌려준다.
// 업로드 배포와 같은 경로(로컬 풀기/원격 노드 전송/placement 노드 빌드)로 빌드되도록 builder를 Dockerfile로 고정한다.
func (m *Manager) functionUpload(ctx context.Context, d *store.Deployment, b *logbook.Book) (string, error) {
	fc, err := m.Store.DeploymentFunctionCode(ctx, d.ID)
	if errors.Is(err, store.ErrNotFound) {
		// 스냅샷 없이 만들어진 배포(이전 버전 등)는 지금 작업본을 기록해 쓴다.
		if err := m.Store.SnapshotFunctionCode(ctx, &store.Deployment{ID: d.ID, InstanceID: d.InstanceID}); err != nil {
			return "", err
		}
		fc, err = m.Store.DeploymentFunctionCode(ctx, d.ID)
	}
	if errors.Is(err, store.ErrNotFound) {
		return "", errors.New("function 코드가 없습니다. Code 탭에서 코드를 저장하세요")
	}
	if err != nil {
		return "", err
	}
	data, err := FunctionContext(fc.Code)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(m.WorkDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(m.WorkDir, "fn-"+strings.ReplaceAll(d.ID, "_", "-")+fmt.Sprintf("-%d.tar.gz", time.Now().UnixNano()))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	applyFunctionBuild(&d.Spec)
	if b != nil {
		b.Printf("function 코드(%d bytes, sha256 %s)로 %s 이미지를 빌드합니다", len(fc.Code), short(fc.SHA), FunctionBunImage)
	}
	return path, nil
}

// applyFunctionBuild는 function 빌드 컨텍스트에 맞게 빌드 설정을 고정한다(사용자 설정의 root_dir/builder는 쓰지 않는다).
func applyFunctionBuild(sp *store.Spec) {
	sp.RootDir, sp.Builder, sp.DockerfilePath = "/", "dockerfile", "Dockerfile"
	sp.BuildCommand = nil
}
