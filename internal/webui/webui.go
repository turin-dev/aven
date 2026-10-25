// Package webui는 빌드된 Next.js 정적 파일(web/out → dist)을 바이너리에 넣는다.
// `make web`이 dist를 채운다. 비어 있으면 안내 페이지만 나온다.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

func FS() fs.FS {
	sub, _ := fs.Sub(dist, "dist")
	return sub
}
