// Package tarx는 업로드된 tar.gz를 안전하게 푼다(경로 탈출/링크 차단). 서버와 agent가 같이 쓴다.
package tarx

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// MaxBytes는 풀었을 때의 최대 총 크기다.
const MaxBytes = 2 << 30

// SafeJoin은 root 밖으로 나가는 경로를 거부한다.
func SafeJoin(root, rel string) (string, error) {
	p := filepath.Join(root, filepath.Clean("/"+rel))
	if p != root && !strings.HasPrefix(p, root+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid path: %s", rel)
	}
	return p, nil
}

// ExtractFile은 tar.gz 파일을 dst에 푼다.
func ExtractFile(path, dst string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return Extract(f, dst)
}

// Extract는 tar.gz 스트림을 dst에 푼다. 경로 탈출과 절대/바깥 링크는 막는다.
func Extract(r io.Reader, dst string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		p, err := SafeJoin(dst, h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += h.Size
			if total > MaxBytes {
				return errors.New("upload too large (2GiB)")
			}
			os.MkdirAll(filepath.Dir(p), 0o755)
			mode := os.FileMode(0o644)
			if h.Mode&0o111 != 0 {
				mode = 0o755
			}
			out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			out.Close()
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			// 저장소 안을 가리키는 상대 링크만 허용
			if filepath.IsAbs(h.Linkname) {
				continue
			}
			if _, err := SafeJoin(filepath.Dir(p), h.Linkname); err != nil {
				continue
			}
			target := filepath.Join(filepath.Dir(p), h.Linkname)
			if !strings.HasPrefix(target, dst) {
				continue
			}
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.Symlink(h.Linkname, p)
		}
	}
}
