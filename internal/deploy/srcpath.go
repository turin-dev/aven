package deploy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/turin-dev/aven/internal/config"
	"github.com/turin-dev/aven/internal/logbook"
)

// 저장소(clone한 소스)에는 심볼릭 링크가 커밋돼 있을 수 있다. aven-server는 빌드 전에 소스 일부를 호스트에서 직접 읽는다
// (root_dir, config-as-code, Dockerfile, railpack prepare). 링크가 소스 밖(예: /etc/keel/aven.env, 데이터 디렉터리의 키)을
// 가리키면 배포 권한만 있는 사용자가 서버 파일을 빌드 로그나 이미지로 읽을 수 있으므로, 실제 경로가 소스 안에 있는지 확인한다.

// within은 p가 dir 자신이거나 그 아래인지다(둘 다 정리된 절대 경로).
func within(dir, p string) bool {
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator))
}

// resolveInside는 p(base 아래 경로)의 링크를 풀고 실제 경로가 base(역시 링크를 푼 것) 안인지 확인해 실제 경로를 돌려준다.
// p가 없으면(끊긴 링크 포함) 읽을 것도 없으므로 p를 그대로 돌려준다.
func resolveInside(base, p string) (string, error) {
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(p)
	if errors.Is(err, fs.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return "", err
	}
	if !within(realBase, real) {
		rel, _ := filepath.Rel(base, p)
		return "", fmt.Errorf("경로 %s가 심볼릭 링크로 저장소 밖을 가리킵니다", filepath.ToSlash(rel))
	}
	return real, nil
}

// sourceRoot는 소스 srcDir 안의 root_dir 경로다. 링크로 소스 밖을 가리키면 오류다.
func sourceRoot(srcDir, rootDir string) (string, error) {
	p, err := safeJoin(srcDir, rootDir)
	if err != nil {
		return "", err
	}
	return resolveInside(srcDir, p)
}

// sourceFile은 root 안의 상대 경로 rel(Dockerfile 등)이다. 링크를 따라가도 srcDir 안이어야 한다.
// root 밖이어도 저장소 안을 가리키는 링크(monorepo의 공유 Dockerfile)는 허용하므로 글자 검사만 root 기준으로 한다.
func sourceFile(srcDir, root, rel string) (string, error) {
	p, err := lexicalJoin(root, rel)
	if err != nil {
		return "", err
	}
	return resolveInside(srcDir, p)
}

// checkConfigLinks는 config-as-code 파일(railway.json 등)이 소스 밖을 가리키는 링크가 아닌지 본다(config.Find가 그대로 읽는다).
func checkConfigLinks(srcDir, root string) error {
	for _, name := range config.Files {
		p := filepath.Join(root, name)
		if fi, err := os.Lstat(p); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			continue
		}
		if _, err := resolveInside(srcDir, p); err != nil {
			return fmt.Errorf("config-as-code: %w", err)
		}
	}
	return nil
}

// stripEscapingLinks는 root 아래에서 srcDir 밖을 가리키는 심볼릭 링크를 지운다(railpack prepare가 호스트에서 소스 파일을 읽는다).
// 소스 안을 가리키는 링크(monorepo의 공유 패키지 등)와 끊긴 링크는 그대로 둔다. 지운 링크는 빌드 로그에 남긴다.
func stripEscapingLinks(srcDir, root string, b *logbook.Book) error {
	realBase, err := filepath.EvalSymlinks(srcDir)
	if err != nil {
		return err
	}
	var removed []string
	err = filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			return nil // 읽지 못하는 항목은 빌드도 읽지 못한다
		}
		if de.IsDir() && de.Name() == ".git" {
			return filepath.SkipDir
		}
		if de.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil || within(realBase, real) {
			return nil
		}
		if err := os.Remove(p); err != nil {
			return err
		}
		rel, _ := filepath.Rel(srcDir, p)
		removed = append(removed, filepath.ToSlash(rel))
		return nil
	})
	if len(removed) > 0 && b != nil {
		sort.Strings(removed)
		b.Printf("WARN: 저장소 밖을 가리키는 심볼릭 링크를 빌드에서 뺐습니다: %s", strings.Join(removed, ", "))
	}
	return err
}
