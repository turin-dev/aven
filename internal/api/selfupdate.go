package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

// 업데이트 확인: aven-server(self 서비스) 저장소 브랜치의 최신 커밋을 지금 배포된 커밋과 주기적으로 비교한다.
// 새 커밋이 있으면 캔버스·패널에 알리고 활동 기록에 한 번 남긴다. 적용은 owner가 배포를 눌러서 한다.

const settingSelfUpdate = "self_update"

// selfUpdateInterval은 확인 주기다(KEEL_SELF_UPDATE_INTERVAL, 기본 30분, off면 끈다).
func selfUpdateInterval() time.Duration {
	v := strings.TrimSpace(os.Getenv("KEEL_SELF_UPDATE_INTERVAL"))
	if strings.EqualFold(v, "off") {
		return 0
	}
	if d, err := time.ParseDuration(v); err == nil && d >= time.Minute {
		return d
	}
	return 30 * time.Minute
}

type selfUpdateCommit struct {
	SHA     string    `json:"sha"`
	Message string    `json:"message"` // 첫 줄
	Author  string    `json:"author"`
	At      time.Time `json:"at"`
}

// selfUpdate는 마지막 확인 결과다(settings.self_update). Available은 읽을 때 지금 배포와 다시 비교한다.
type selfUpdate struct {
	CheckedAt  time.Time          `json:"checked_at"`
	Repo       string             `json:"repo"`
	Branch     string             `json:"branch"`
	Latest     *selfUpdateCommit  `json:"latest,omitempty"`
	CurrentSHA string             `json:"current_sha,omitempty"` // 확인할 때 배포돼 있던 커밋
	Behind     int                `json:"behind"`                // 그 커밋보다 앞선 커밋 수
	Commits    []selfUpdateCommit `json:"commits,omitempty"`     // 새 커밋(최신이 먼저, 최대 20개)
	Error      string             `json:"error,omitempty"`
	Notified   string             `json:"notified,omitempty"` // 활동 기록에 알린 최신 커밋
	Available  bool               `json:"available"`
	Interval   string             `json:"interval"`
}

var githubRepoRe = regexp.MustCompile(`^(?:https://github\.com/|git@github\.com:|ssh://git@github\.com/)([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?/?$`)

// selfTarget은 self 서비스와 그 기본 환경 인스턴스, 지금 배포된 커밋이다.
func (s *Server) selfTarget(ctx context.Context) (*store.Service, *store.Instance, string, error) {
	id := s.selfServiceID()
	if id == "" {
		return nil, nil, "", errors.New("self service is not set")
	}
	svc, err := s.Store.GetService(ctx, id)
	if err != nil {
		return nil, nil, "", err
	}
	p, err := s.Store.GetProject(ctx, svc.ProjectID)
	if err != nil {
		return nil, nil, "", err
	}
	in, err := s.Store.GetInstanceFor(ctx, svc.ID, p.DefaultEnvironmentID)
	if err != nil {
		return nil, nil, "", err
	}
	cur := ""
	if in.CurrentDeploymentID != nil {
		if d, err := s.Store.GetDeployment(ctx, *in.CurrentDeploymentID); err == nil && d.CommitSHA != nil {
			cur = *d.CommitSHA
		}
	}
	return svc, in, cur, nil
}

func (s *Server) loadSelfUpdate(ctx context.Context) *selfUpdate {
	u := &selfUpdate{}
	if v, err := s.Store.GetSetting(ctx, settingSelfUpdate); err == nil {
		json.Unmarshal([]byte(v), u)
	}
	u.Interval = "off"
	if d := selfUpdateInterval(); d > 0 {
		u.Interval = d.String()
	}
	return u
}

// selfUpdateView는 저장된 결과를 지금 배포와 비교해 돌려준다(업데이트한 직후 바로 "최신"이 되게).
func (s *Server) selfUpdateView(ctx context.Context) *selfUpdate {
	u := s.loadSelfUpdate(ctx)
	_, _, cur, err := s.selfTarget(ctx)
	if err != nil || u.Latest == nil {
		u.Available = false
		return u
	}
	switch {
	case cur == u.Latest.SHA:
		u.Available, u.Behind, u.Commits = false, 0, nil
	case cur != u.CurrentSHA:
		// 확인한 뒤 다른 커밋을 배포했다: 다음 확인 전까지는 최신 커밋과 다르다는 것만 안다.
		u.Available, u.Behind, u.Commits = true, 0, nil
	default:
		u.Available = u.Behind > 0 || cur == ""
	}
	return u
}

// checkSelfUpdate는 GitHub에서 최신 커밋을 가져와 저장한다. 새 버전을 처음 보면 활동 기록에 남긴다.
func (s *Server) checkSelfUpdate(ctx context.Context) (*selfUpdate, error) {
	svc, in, cur, err := s.selfTarget(ctx)
	if err != nil {
		return nil, err
	}
	prev := s.loadSelfUpdate(ctx)
	u := &selfUpdate{CheckedAt: time.Now().UTC(), Branch: in.Branch, CurrentSHA: cur, Notified: prev.Notified}
	if in.RepoURL != nil {
		u.Repo = *in.RepoURL
	}
	if err := s.fetchSelfUpdate(ctx, svc.ProjectID, u); err != nil {
		u.Error = err.Error()
	}
	b, _ := json.Marshal(u)
	if err := s.Store.SetSetting(ctx, settingSelfUpdate, string(b)); err != nil {
		return nil, err
	}
	v := s.selfUpdateView(ctx)
	if v.Available && v.Latest != nil && v.Latest.SHA != prev.Notified && s.Events != nil {
		msg := fmt.Sprintf("aven-server 새 버전: %s (%s)", v.Latest.Message, short7(v.Latest.SHA))
		if v.Behind > 0 {
			msg = fmt.Sprintf("aven-server 새 버전: 커밋 %d개 · %s (%s)", v.Behind, v.Latest.Message, short7(v.Latest.SHA))
		}
		envID := in.EnvironmentID
		s.Events.Emit(ctx, svc.ProjectID, &envID, &svc.ID, "aven.update_available", msg)
		u.Notified = v.Latest.SHA
		b, _ := json.Marshal(u)
		s.Store.SetSetting(ctx, settingSelfUpdate, string(b))
		v.Notified = u.Notified
	}
	return v, nil
}

func (s *Server) fetchSelfUpdate(ctx context.Context, projectID string, u *selfUpdate) error {
	m := githubRepoRe.FindStringSubmatch(strings.TrimSpace(u.Repo))
	if m == nil {
		return errors.New("GitHub 저장소만 업데이트를 확인할 수 있습니다")
	}
	owner, repo := m[1], m[2]
	branch := u.Branch
	if branch == "" {
		branch = "main"
	}
	token := ""
	if s.Deploy != nil {
		token = s.Deploy.ProjectGitHubToken(ctx, projectID)
	}
	var head struct {
		SHA    string     `json:"sha"`
		Commit ghCommitV2 `json:"commit"`
	}
	if err := s.githubJSON(ctx, token, fmt.Sprintf("/repos/%s/%s/commits/%s", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(branch)), &head); err != nil {
		return err
	}
	u.Latest = &selfUpdateCommit{SHA: head.SHA, Message: strings.TrimSpace(firstLine(strings.TrimSpace(head.Commit.Message))), Author: head.Commit.Author.Name, At: head.Commit.Author.Date}
	if u.CurrentSHA == "" || u.CurrentSHA == head.SHA {
		return nil
	}
	var cmp struct {
		Status  string `json:"status"`
		AheadBy int    `json:"ahead_by"`
		Commits []struct {
			SHA    string     `json:"sha"`
			Commit ghCommitV2 `json:"commit"`
		} `json:"commits"`
	}
	if err := s.githubJSON(ctx, token, fmt.Sprintf("/repos/%s/%s/compare/%s...%s", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(u.CurrentSHA), url.PathEscape(head.SHA)), &cmp); err != nil {
		return err
	}
	u.Behind = cmp.AheadBy
	// compare는 오래된 커밋부터 준다. 최신이 먼저 오게 뒤집고 20개만 남긴다.
	for i := len(cmp.Commits) - 1; i >= 0 && len(u.Commits) < 20; i-- {
		c := cmp.Commits[i]
		u.Commits = append(u.Commits, selfUpdateCommit{SHA: c.SHA, Message: strings.TrimSpace(firstLine(strings.TrimSpace(c.Commit.Message))), Author: c.Commit.Author.Name, At: c.Commit.Author.Date})
	}
	return nil
}

type ghCommitV2 struct {
	Message string `json:"message"`
	Author  struct {
		Name string    `json:"name"`
		Date time.Time `json:"date"`
	} `json:"author"`
}

func (s *Server) githubJSON(ctx context.Context, token, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.githubAPI()+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		if resp.StatusCode == http.StatusNotFound {
			return fmt.Errorf("GitHub: 저장소나 브랜치를 찾을 수 없습니다(비공개 저장소면 프로젝트에 GitHub 연결이 필요합니다)")
		}
		return fmt.Errorf("GitHub: HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

func short7(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// SelfUpdateLoop은 self 서비스가 있으면 주기적으로 업데이트를 확인한다(시작 1분 뒤 처음).
func (s *Server) SelfUpdateLoop(ctx context.Context) {
	every := selfUpdateInterval()
	if every == 0 {
		return
	}
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if s.selfServiceID() != "" {
			if _, err := s.checkSelfUpdate(ctx); err != nil && ctx.Err() == nil {
				s.Log.Warn("self update check", "err", err)
			}
		}
		timer.Reset(every)
	}
}

// checkSelfUpdateNow는 POST /api/owner/self-service/check다.
func (s *Server) checkSelfUpdateNow(w http.ResponseWriter, r *http.Request) {
	if _, err := s.checkSelfUpdate(r.Context()); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, s.selfServiceView(r.Context()))
}
