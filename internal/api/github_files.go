package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const githubFilesPerPage = 100
const githubMaxPullRequestFiles = 3000

// pullRequestChangedFiles gets the complete PR file list for focused previews.
// GitHub webhook payloads only include a changed-file count, not file names.
func (s *Server) pullRequestChangedFiles(ctx context.Context, projectID string, ev *prEvent) ([]string, error) {
	repo := ev.baseRepo().primary()
	_, ownerRepo, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("invalid GitHub repository %q", repo)
	}
	parts := strings.Split(ownerRepo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("invalid GitHub repository %q", repo)
	}

	apiBase := strings.TrimRight(s.GitHubAPIBase, "/")
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	token := ""
	if s.Deploy != nil {
		token = s.Deploy.ProjectGitHubToken(ctx, projectID)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	changed := map[string]bool{}
	for page := 1; page <= githubMaxPullRequestFiles/githubFilesPerPage; page++ {
		endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/files?per_page=%d&page=%d",
			apiBase, url.PathEscape(parts[0]), url.PathEscape(parts[1]), ev.Number, githubFilesPerPage, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list GitHub pull request files: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			return nil, fmt.Errorf("list GitHub pull request files: HTTP %d", resp.StatusCode)
		}
		var files []struct {
			Filename         string `json:"filename"`
			PreviousFilename string `json:"previous_filename"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&files)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decode GitHub pull request files: %w", err)
		}
		for _, f := range files {
			if f.Filename != "" {
				changed[f.Filename] = true
			}
			if f.PreviousFilename != "" {
				changed[f.PreviousFilename] = true
			}
		}
		if len(files) < githubFilesPerPage {
			break
		}
		if page == githubMaxPullRequestFiles/githubFilesPerPage && len(files) == githubFilesPerPage &&
			ev.PullRequest.ChangedFiles > len(changed) {
			return nil, fmt.Errorf("pull request has more than %d changed files", githubMaxPullRequestFiles)
		}
	}
	out := make([]string, 0, len(changed))
	for file := range changed {
		out = append(out, file)
	}
	sort.Strings(out)
	return out, nil
}
