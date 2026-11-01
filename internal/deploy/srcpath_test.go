package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 저장소에 커밋된 심볼릭 링크로 빌드가 소스 밖(서버 파일)을 읽지 않는다.
func TestSourcePathsRejectEscapingLinks(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "aven.env")
	os.WriteFile(secret, []byte("KEEL_ADMIN_PASSWORD=hunter2\n"), 0o600)
	src := filepath.Join(t.TempDir(), "src")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(src, "apps", "web"), 0o755))
	must(os.MkdirAll(filepath.Join(src, "shared"), 0o755))
	must(os.WriteFile(filepath.Join(src, "shared", "Dockerfile"), []byte("FROM scratch\n"), 0o644))
	must(os.Symlink(outside, filepath.Join(src, "escape")))                                         // 디렉터리 링크 → 밖
	must(os.Symlink(secret, filepath.Join(src, "apps", "web", "Dockerfile")))                       // 파일 링크 → 밖
	must(os.Symlink("../../shared/Dockerfile", filepath.Join(src, "apps", "web", "Dockerfile.ok"))) // 저장소 안
	must(os.Symlink(secret, filepath.Join(src, "apps", "web", "railway.json")))                     // config-as-code → 밖
	must(os.Symlink("/nonexistent/aven", filepath.Join(src, "apps", "web", "dangling")))

	cases := []struct {
		name    string
		root    string
		file    string
		wantErr bool
	}{
		{"plain root", "/apps/web", "", false},
		{"root through outside link", "/escape", "", true},
		{"root traversal", "/../..", "", false}, // safeJoin가 저장소 루트로 자른다
		{"dockerfile link outside", "/apps/web", "Dockerfile", true},
		{"dockerfile link inside", "/apps/web", "Dockerfile.ok", false},
		{"missing dockerfile", "/apps/web", "nope/Dockerfile", false},
		{"dangling link", "/apps/web", "dangling", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, err := sourceRoot(src, c.root)
			if err == nil && c.file != "" {
				_, err = sourceFile(src, root, c.file)
			}
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), outside) {
				t.Fatalf("error leaks the link target: %v", err)
			}
		})
	}

	root, _ := sourceRoot(src, "/apps/web")
	if err := checkConfigLinks(src, root); err == nil {
		t.Fatal("config-as-code link outside the repository was accepted")
	}
	if err := checkConfigLinks(src, filepath.Join(src, "shared")); err != nil {
		t.Fatalf("no config file: %v", err)
	}

	// railpack: 밖을 가리키는 링크만 지운다.
	if err := stripEscapingLinks(src, src, nil); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"escape", "apps/web/Dockerfile", "apps/web/railway.json"} {
		if _, err := os.Lstat(filepath.Join(src, p)); !os.IsNotExist(err) {
			t.Errorf("%s still present (%v)", p, err)
		}
	}
	for _, p := range []string{"apps/web/Dockerfile.ok", "apps/web/dangling", "shared/Dockerfile"} {
		if _, err := os.Lstat(filepath.Join(src, p)); err != nil {
			t.Errorf("%s removed: %v", p, err)
		}
	}
	if _, err := os.Stat(secret); err != nil {
		t.Fatalf("link target was touched: %v", err)
	}
}
