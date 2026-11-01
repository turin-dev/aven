// Package tarx는 업로드된 tar.gz를 안전하게 푼다(경로 탈출/링크 차단). 서버와 agent가 같이 쓴다.
package tarx

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// MaxBytes는 풀었을 때의 최대 총 크기다.
const MaxBytes = 2 << 30

// MaxEntries는 아카이브 하나에 허용하는 최대 항목 수다(빈 파일 수백만 개로 inode를 고갈시키는 것을 막는다).
const MaxEntries = 1 << 20

// maxLinkHops는 심볼릭 링크를 따라갈 때의 최대 단계 수다(커널의 ELOOP 한도와 같다).
const maxLinkHops = 40

// SafeJoin은 root 밖으로 나가는 경로를 거부한다. ../ 는 root 안으로 고정된다.
func SafeJoin(root, rel string) (string, error) {
	root = filepath.Clean(root)
	p := filepath.Join(root, filepath.Clean("/"+rel))
	if !within(root, p) {
		return "", fmt.Errorf("invalid path: %s", rel)
	}
	return p, nil
}

// within은 p가 root 자신이거나 그 아래인지 본다(둘 다 Clean된 경로). "/srv/a"와 "/srv/ab"를 구분한다.
func within(root, p string) bool {
	if p == root {
		return true
	}
	if root == string(filepath.Separator) {
		return strings.HasPrefix(p, root)
	}
	return strings.HasPrefix(p, root+string(filepath.Separator))
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
//   - 항목 경로의 ../ 와 절대 경로는 dst 안으로 고정된다.
//   - 이미 풀린 심볼릭 링크를 거쳐 쓰는 항목은 거부한다(링크로 dst 밖에 쓰는 공격).
//   - 하드 링크, 장치 파일, FIFO는 건너뛴다. 파일 권한은 0644/0755로만 둔다(setuid 등 제거).
//   - 다 푼 뒤 실제 파일시스템 기준으로 dst 밖을 가리키는 심볼릭 링크는 지운다.
func Extract(r io.Reader, dst string) error {
	dst, err := filepath.Abs(dst)
	if err != nil {
		return err
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	var total int64
	var entries int
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if entries++; entries > MaxEntries {
			return fmt.Errorf("upload has too many entries (max %d)", MaxEntries)
		}
		p, err := SafeJoin(dst, h.Name)
		if err != nil {
			return err
		}
		if p == dst {
			continue // "./" 같은 루트 항목
		}
		if err := noLinkInPath(dst, p); err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if h.Size < 0 {
				return fmt.Errorf("invalid size for %s", h.Name)
			}
			total += h.Size
			if total > MaxBytes {
				return errors.New("upload too large (2GiB)")
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := removeLink(p); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if h.Mode&0o111 != 0 {
				mode = 0o755
			}
			out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			// 저장소 안을 가리키는 상대 링크만 허용(사슬로 밖에 닿는 경우는 끝에서 다시 걸러낸다)
			if h.Linkname == "" || filepath.IsAbs(h.Linkname) {
				continue
			}
			if !within(dst, filepath.Join(filepath.Dir(p), h.Linkname)) {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if fi, err := os.Lstat(p); err == nil {
				if fi.IsDir() {
					return fmt.Errorf("symlink %s would replace a directory", h.Name)
				}
				if err := os.Remove(p); err != nil {
					return err
				}
			}
			if err := os.Symlink(h.Linkname, p); err != nil {
				return err
			}
		}
	}
	return pruneLinks(dst)
}

// noLinkInPath는 dst와 p 사이(p 자신은 빼고)의 이미 있는 경로 요소가 심볼릭 링크가 아닌지 확인한다.
func noLinkInPath(dst, p string) error {
	rel, err := filepath.Rel(dst, filepath.Dir(p))
	if err != nil || rel == "." {
		return err
	}
	cur := dst
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil // 이 아래는 아직 없으니 MkdirAll이 진짜 디렉터리로 만든다
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("invalid path: %s goes through a symlink", p[len(dst)+1:])
		}
	}
	return nil
}

// removeLink는 p가 심볼릭 링크면 지운다. O_TRUNC로 열 때 링크 대상이 덮어써지지 않게 한다.
func removeLink(p string) error {
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return os.Remove(p)
	}
	return nil
}

// pruneLinks는 실제 파일시스템을 따라 풀었을 때 dst 밖으로 나가거나 순환하는 링크를 지운다.
// 링크 하나하나는 dst 안을 가리켜도 a -> "." 와 b -> "a/../x" 처럼 이어지면 밖에 닿을 수 있다.
func pruneLinks(dst string) error {
	root, err := filepath.EvalSymlinks(dst)
	if err != nil {
		return err
	}
	for {
		removed := false
		err := filepath.WalkDir(dst, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type()&fs.ModeSymlink == 0 {
				return nil
			}
			hops := 0
			real, err := resolve(filepath.Join(root, path[len(dst):]), &hops)
			if err == nil && within(root, real) {
				return nil
			}
			removed = true
			return os.Remove(path)
		})
		if err != nil || !removed {
			return err
		}
		// 링크를 지우면 다른 링크의 해석이 바뀔 수 있으니 바뀌지 않을 때까지 반복한다
	}
}

// resolve는 커널처럼 경로 요소를 하나씩 따라가며 링크를 푼다. 아직 없는 꼬리 부분은 글자 그대로 붙인다.
// p는 링크를 거치지 않은 절대 경로여야 한다.
func resolve(p string, hops *int) (string, error) {
	return walk(string(filepath.Separator), p, hops)
}

func walk(cur, rest string, hops *int) (string, error) {
	missing := false
	for _, part := range strings.Split(rest, string(filepath.Separator)) {
		switch part {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, part)
		if missing {
			cur = next
			continue
		}
		fi, err := os.Lstat(next)
		if errors.Is(err, fs.ErrNotExist) {
			missing = true
			cur = next
			continue
		}
		if err != nil {
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			cur = next
			continue
		}
		if *hops++; *hops > maxLinkHops {
			return "", errors.New("too many levels of symbolic links")
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", err
		}
		base := cur
		if filepath.IsAbs(target) {
			base = string(filepath.Separator)
		}
		if cur, err = walk(base, target, hops); err != nil {
			return "", err
		}
	}
	return cur, nil
}
