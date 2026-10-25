package agent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// agent는 railpack 바이너리가 없으면 공식 릴리스를 내려받아 쓴다(--railpack auto, 기본값).
// 버전과 SHA-256은 코드에 고정돼 있어 다른 파일은 받아들이지 않는다. 이 값은 릴리스의 checksums.txt와 같다.
const (
	RailpackVersion = "v0.42.0"
	railpackBase    = "https://github.com/railwayapp/railpack/releases/download/"
)

var railpackSHA = map[string]struct{ asset, sha string }{
	"amd64": {"railpack-" + RailpackVersion + "-x86_64-unknown-linux-musl.tar.gz", "81bca91d0577c7650378762609116d12d8630e2cfc1779e41d3ac3964fc7194b"},
	"arm64": {"railpack-" + RailpackVersion + "-arm64-unknown-linux-musl.tar.gz", "63c0417d6c0a437162fa2c688ddf58aabf9e4433eeffb69d8c3fb498603e2c3b"},
}

// RailpackAuto는 Handler.Railpack에 넣는 특별한 값이다(자동 내려받기).
const RailpackAuto = "auto"

// DownloadRailpack은 tar.gz를 base URL에서 받아 SHA-256을 확인하고 dir/railpack-<version>에 설치한다.
func DownloadRailpack(ctx context.Context, client *http.Client, base, asset, wantSHA, dir, dest string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+RailpackVersion+"/"+asset, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download railpack: HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp(dir, "railpack-dl-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	sum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, sum), io.LimitReader(resp.Body, 200<<20)); err != nil {
		tmp.Close()
		return err
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != wantSHA {
		tmp.Close()
		return fmt.Errorf("railpack download checksum mismatch (got %s)", got[:12])
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	gz, err := gzip.NewReader(tmp)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("railpack binary not found in archive")
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || filepath.Base(h.Name) != "railpack" || strings.Contains(h.Name, "..") {
			continue
		}
		out, err := os.CreateTemp(dir, "railpack-bin-*")
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, io.LimitReader(tr, 200<<20)); err != nil {
			out.Close()
			os.Remove(out.Name())
			return err
		}
		if err := out.Chmod(0o755); err != nil {
			out.Close()
			os.Remove(out.Name())
			return err
		}
		out.Close()
		if err := os.Rename(out.Name(), dest); err != nil {
			os.Remove(out.Name())
			return err
		}
		return nil
	}
}

// railpackPath는 railpack 바이너리 경로를 돌려준다. auto면 처음 쓸 때 내려받아 캐시한다.
func (h *Handler) railpackPath(ctx context.Context, out func(string)) (string, error) {
	switch h.Railpack {
	case "", "off":
		return "", errors.New("railpack builds are disabled on this node (aven-agent --railpack off)")
	case RailpackAuto:
	default:
		return h.Railpack, nil
	}
	h.rpMu.Lock()
	defer h.rpMu.Unlock()
	dir := filepath.Join(h.DataDir, "bin")
	dest := filepath.Join(dir, "railpack-"+RailpackVersion)
	if st, err := os.Stat(dest); err == nil && st.Mode().IsRegular() {
		return dest, nil
	}
	a, ok := railpackSHA[runtime.GOARCH]
	if !ok {
		return "", fmt.Errorf("no railpack release for %s", runtime.GOARCH)
	}
	out("railpack " + RailpackVersion + "을(를) 이 노드에 내려받습니다 (" + a.asset + ")")
	dctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	base := railpackBase
	if h.railpackBase != "" {
		base = h.railpackBase
	}
	if err := DownloadRailpack(dctx, http.DefaultClient, base, a.asset, a.sha, dir, dest); err != nil {
		return "", err
	}
	return dest, nil
}
