package watch

import "testing"

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
