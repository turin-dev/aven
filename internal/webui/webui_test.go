package webui

import (
	"io/fs"
	"testing"
)

// FS는 dist가 비어 있어도(make web 전) 쓸 수 있는 파일시스템이어야 한다.
func TestFS(t *testing.T) {
	f := FS()
	if f == nil {
		t.Fatal("nil FS")
	}
	if _, err := fs.ReadDir(f, "."); err != nil {
		t.Fatalf("read root: %v", err)
	}
	if _, err := fs.Stat(f, "../webui.go"); err == nil {
		t.Fatal("FS must not reach outside dist")
	}
}
