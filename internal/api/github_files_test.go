package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/deploy"
)

func TestPullRequestChangedFilesPaginatesAndIncludesRenamedPaths(t *testing.T) {
	var requests int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer read-token" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/repos/acme/app/pulls/12/files" || r.URL.Query().Get("per_page") != "100" {
			t.Errorf("request URL = %s", r.URL.String())
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page == 1 {
			files := make([]map[string]string, 100)
			for i := range files {
				files[i] = map[string]string{"filename": fmt.Sprintf("file-%03d.go", i)}
			}
			json.NewEncoder(w).Encode(files)
			return
		}
		json.NewEncoder(w).Encode([]map[string]string{{"filename": "new/name.go", "previous_filename": "old/name.go"}})
	}))
	defer api.Close()

	s := &Server{GitHubAPIBase: api.URL, Deploy: &deploy.Manager{Cfg: deploy.Config{GitHubToken: "read-token"}}}
	ev := &prEvent{Number: 12}
	ev.PullRequest.Base.Repo = &ghRepo{FullName: "acme/app"}
	files, err := s.pullRequestChangedFiles(t.Context(), "", ev)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(files) != 102 {
		t.Fatalf("requests=%d files=%d", requests, len(files))
	}
	joined := strings.Join(files, "\n")
	for _, want := range []string{"file-000.go", "file-099.go", "new/name.go", "old/name.go"} {
		if !strings.Contains(joined, want) {
			t.Errorf("changed files do not include %q", want)
		}
	}
}
