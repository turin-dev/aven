// Package tarx는 업로드된 tar.gz를 안전하게 푼다(경로 탈출/링크 차단). 서버와 agent가 같이 쓴다.
//
// 절대 경로, ".." 경로 요소, 심볼릭 링크, 하드 링크, 장치 파일, FIFO는 거부한다.
// 아카이브 항목의 쓰기는 os.Root로 dst 안에만 한정한다.
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

// SafeJoin은 root 안의 경로만 돌려준다.
// ".." 요소와 절대 경로는 거부한다. filepath.Clean("/"+rel)은 '..'를 지워서
// 링크 대상 검사가 항상 통과하므로 쓰지 않는다.
// 결과는 root이거나 root+구분자로 시작하는 경로다(root+"x" 같은 형제 디렉터리는 거부).
func SafeJoin(root, rel string) (string, error) {
	root = filepath.Clean(root)
	if rel == "" || strings.Contains(rel, "\x00") {
		return "", fmt.Errorf("invalid path: %q", rel)
	}
	// tar 절대 경로는 OS와 상관없이 '/'로 시작한다.
	if strings.HasPrefix(rel, "/") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("invalid path: %s", rel)
	}
	// Join은 '..'를 어휘적으로 해석한다. '/'를 앞에 붙여 Clean하면 '..'가 사라진다.
	p := filepath.Join(root, rel)
	if !insideRoot(root, p) {
		return "", fmt.Errorf("invalid path: %s", rel)
	}
	// root 안에 남는 "foo/../bar"도 거부한다.
	if hasDotDot(rel) {
		return "", fmt.Errorf("invalid path: %s", rel)
	}
	return p, nil
}

// insideRoot는 p가 root이거나 root 바로 아래인지 본다.
// 접두사만 비교하면 root+"x" 같은 형제 디렉터리가 통과한다.
func insideRoot(root, p string) bool {
	root = filepath.Clean(root)
	p = filepath.Clean(p)
	if p == root {
		return true
	}
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	sep := string(filepath.Separator)
	if rel == ".." || strings.HasPrefix(rel, ".."+sep) {
		return false
	}
	return strings.HasPrefix(p, root+sep)
}

func hasDotDot(name string) bool {
	for rest := name; rest != ""; {
		var part string
		part, rest, _ = strings.Cut(rest, "/")
		if part == ".." {
			return true
		}
	}
	return false
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

// Extract는 tar.gz 스트림을 dst에 푼다.
// 심볼릭 링크, 하드 링크, 장치 파일, FIFO는 거부한다.
// 정규 파일과 디렉터리만 os.Root를 통해 쓰므로, 이미 있는 링크를 따라 dst 밖으로 나가지 못한다.
func Extract(r io.Reader, dst string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()

	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	dst = filepath.Clean(dst)
	info, err := os.Lstat(dst)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("destination is a symlink")
	}
	if !info.IsDir() {
		return errors.New("destination is not a directory")
	}
	root, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer root.Close()

	tr := tar.NewReader(gz)
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if err := extractEntry(root, dst, h, tr, &total); err != nil {
			return err
		}
	}
}

func extractEntry(root *os.Root, dst string, h *tar.Header, tr *tar.Reader, total *int64) error {
	switch h.Typeflag {
	case tar.TypeSymlink:
		return fmt.Errorf("archive entry %q: symlinks are not allowed", h.Name)
	case tar.TypeLink:
		return fmt.Errorf("archive entry %q: hard links are not allowed", h.Name)
	case tar.TypeChar, tar.TypeBlock:
		return fmt.Errorf("archive entry %q: device files are not allowed", h.Name)
	case tar.TypeFifo:
		return fmt.Errorf("archive entry %q: fifo files are not allowed", h.Name)
	case tar.TypeDir, tar.TypeReg:
	default:
		return fmt.Errorf("archive entry %q: unsupported file type", h.Name)
	}

	joined, err := SafeJoin(dst, h.Name)
	if err != nil {
		return err
	}
	// SafeJoin already required joined to be dst or dst+separator+rest.
	rel := "."
	if joined != dst {
		rel = strings.TrimPrefix(joined, dst+string(filepath.Separator))
	}

	if h.Typeflag == tar.TypeDir {
		if rel == "." {
			return nil
		}
		return root.MkdirAll(rel, 0o755)
	}

	if h.Size > MaxBytes || *total > MaxBytes-h.Size {
		return errors.New("upload too large (2GiB)")
	}
	*total += h.Size
	if dir := filepath.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	mode := os.FileMode(0o644)
	if h.Mode&0o111 != 0 {
		mode = 0o755
	}
	out, err := root.OpenFile(rel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, tr)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
