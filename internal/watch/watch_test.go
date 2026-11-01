package watch

import (
	"strings"
	"testing"
	"time"
)

func TestMatchFile(t *testing.T) {
	cases := []struct {
		patterns []string
		file     string
		want     bool
	}{
		{[]string{"src/**"}, "src/a.go", true},
		{[]string{"src/**"}, "src/deep/x/a.go", true},
		{[]string{"src/**"}, "docs/a.md", false},
		{[]string{"/backend/**"}, "backend/main.go", true},
		{[]string{"/backend/**"}, "frontend/main.go", false},
		{[]string{"*.md"}, "README.md", true},
		{[]string{"*.md"}, "docs/guide/intro.md", true},
		{[]string{"*.md"}, "main.go", false},
		{[]string{"backend"}, "backend/x/y.go", true},
		{[]string{"backend/"}, "backend/x/y.go", true},
		{[]string{"backend/"}, "other/backend.go", false},
		{[]string{"**/*.go"}, "a/b/c.go", true},
		{[]string{"**/*.go"}, "c.go", true},
		{[]string{"apps/web/**", "!apps/web/**/*.md"}, "apps/web/src/page.tsx", true},
		{[]string{"apps/web/**", "!apps/web/**/*.md"}, "apps/web/README.md", false},
		{[]string{"apps/*/package.json"}, "apps/api/package.json", true},
		{[]string{"apps/*/package.json"}, "apps/api/src/package.json", false},
		{[]string{"# comment", "", "Dockerfile"}, "Dockerfile", true},
		// 앞의 "/"는 루트에 고정한다.
		{[]string{"/Dockerfile"}, "Dockerfile", true},
		{[]string{"/Dockerfile"}, "apps/web/Dockerfile", false},
		{[]string{"/*.md"}, "README.md", true},
		{[]string{"/*.md"}, "docs/intro.md", false},
		{[]string{"Dockerfile"}, "apps/web/Dockerfile", true},
		{[]string{"/backend"}, "backend/main.go", true},
		{[]string{"/backend"}, "apps/backend/main.go", false},
		{[]string{"**", "!/README.md"}, "docs/README.md", true},
		{[]string{"**", "!/README.md"}, "README.md", false},
		// 빈 경로는 아무 패턴에도 맞지 않는다.
		{[]string{"*"}, "", false},
		{[]string{"**"}, "/", false},
	}
	for _, c := range cases {
		if got := MatchFile(c.patterns, c.file); got != c.want {
			t.Errorf("MatchFile(%v, %q) = %v, want %v", c.patterns, c.file, got, c.want)
		}
	}
}

func TestMatch(t *testing.T) {
	if !Match(nil, []string{"anything"}) {
		t.Error("empty patterns must always match")
	}
	if Match([]string{"src/**"}, []string{"README.md", "docs/x"}) {
		t.Error("no file matches")
	}
	if !Match([]string{"src/**"}, []string{"README.md", "src/x.go"}) {
		t.Error("one file matches")
	}
	if Match([]string{"src/**"}, nil) {
		t.Error("no changed files → no match")
	}
}

func TestMatchService(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		root     string
		changed  []string
		want     bool
	}{
		{"root directory", nil, "/apps/web", []string{"apps/api/main.go", "apps/web/page.tsx"}, true},
		{"outside root", nil, "/apps/web", []string{"apps/api/main.go"}, false},
		{"root directory defaults to repository", nil, "/", []string{"README.md"}, true},
		{"watch paths take precedence", []string{"packages/shared/**"}, "/apps/web", []string{"apps/web/page.tsx"}, false},
		{"watch paths are repository relative", []string{"packages/shared/**"}, "/apps/web", []string{"packages/shared/lib.ts"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchService(tc.patterns, tc.root, tc.changed); got != tc.want {
				t.Errorf("MatchService(%v, %q, %v) = %v, want %v", tc.patterns, tc.root, tc.changed, got, tc.want)
			}
		})
	}
}

// "**"가 많은 패턴도 깊은 경로에서 빨리 끝난다.
func TestMatchFileManyDoubleStars(t *testing.T) {
	p := strings.Repeat("**/", 12) + "nomatch"
	file := strings.Repeat("d/", 60) + "file.go"
	done := make(chan bool, 1)
	go func() { done <- MatchFile([]string{p}, file) }()
	select {
	case got := <-done:
		if got {
			t.Fatal("unexpected match")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pathological pattern did not finish")
	}
}
