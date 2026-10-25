// Package watch는 gitignore 스타일 watch_paths로 변경 파일을 판정한다.
//
//   - 앞의 "/"는 저장소 루트 기준(있어도 없어도 루트 기준)
//   - "/"가 없는 패턴(예: "*.md")은 모든 깊이의 파일 이름에 맞춘다
//   - "**"는 0개 이상의 디렉터리, "*"와 "?"는 한 경로 조각 안에서만
//   - "dir/" 또는 "dir"은 그 아래 모든 파일
//   - "!패턴"은 제외. 나중 패턴이 이긴다
package watch

import (
	"path"
	"strings"
)

// Match는 changed 중 하나라도 patterns에 걸리면 true다. patterns가 비면 항상 true.
func Match(patterns, changed []string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, f := range changed {
		if MatchFile(patterns, f) {
			return true
		}
	}
	return false
}

// MatchService reports whether PR changes affect a service. Configured watch
// paths are relative to the repository root; with no patterns, the root
// directory determines the service's source boundary.
func MatchService(patterns []string, rootDir string, changed []string) bool {
	if len(patterns) > 0 {
		return Match(patterns, changed)
	}
	root := strings.TrimPrefix(path.Clean("/"+strings.TrimSpace(rootDir)), "/")
	for _, file := range changed {
		file = strings.TrimPrefix(path.Clean("/"+file), "/")
		if root == "" || file == root || strings.HasPrefix(file, root+"/") {
			return true
		}
	}
	return false
}

func MatchFile(patterns []string, file string) bool {
	file = strings.TrimPrefix(path.Clean("/"+file), "/")
	matched := false
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		neg := strings.HasPrefix(p, "!")
		if neg {
			p = p[1:]
		}
		if matchPattern(p, file) {
			matched = !neg
		}
	}
	return matched
}

func matchPattern(p, file string) bool {
	dirOnly := strings.HasSuffix(p, "/")
	p = strings.Trim(p, "/")
	if p == "" {
		return false
	}
	if !strings.Contains(p, "/") && !dirOnly {
		// 이름 패턴: 어떤 조각이든 맞으면(디렉터리 이름이면 그 아래 전부)
		for _, seg := range strings.Split(file, "/") {
			if ok, _ := path.Match(p, seg); ok {
				return true
			}
		}
		return false
	}
	ps := strings.Split(p, "/")
	fs := strings.Split(file, "/")
	// 디렉터리 패턴이나 마지막이 **가 아닌 패턴도 그 아래 파일까지 포함한다.
	return segs(ps, fs, true)
}

// segs는 패턴 조각과 파일 조각을 맞춘다. prefixOK면 패턴이 파일의 상위 디렉터리에 맞아도 된다.
func segs(ps, fs []string, prefixOK bool) bool {
	for len(ps) > 0 {
		if ps[0] == "**" {
			rest := ps[1:]
			if len(rest) == 0 {
				return len(fs) > 0
			}
			for i := 0; i <= len(fs); i++ {
				if segs(rest, fs[i:], prefixOK) {
					return true
				}
			}
			return false
		}
		if len(fs) == 0 {
			return false
		}
		if ok, _ := path.Match(ps[0], fs[0]); !ok {
			return false
		}
		ps, fs = ps[1:], fs[1:]
	}
	return len(fs) == 0 || prefixOK
}
