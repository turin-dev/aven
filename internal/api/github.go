package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/guardrails"
	"github.com/turin-dev/aven/internal/store"
	"github.com/turin-dev/aven/internal/vars"
	"github.com/turin-dev/aven/internal/watch"
)

// VerifySignature는 GitHub X-Hub-Signature-256 헤더를 검증한다.
func VerifySignature(secret string, body []byte, header string) bool {
	sig, ok := strings.CutPrefix(header, "sha256=")
	if !ok || secret == "" {
		return false
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// NormalizeRepo는 저장소 URL을 비교용으로 정규화한다(https/ssh/git@, .git 유무 무시).
func NormalizeRepo(u string) string {
	u = strings.ToLower(strings.TrimSpace(u))
	for _, p := range []string{"https://", "http://", "ssh://", "git://", "file://"} {
		u = strings.TrimPrefix(u, p)
	}
	if strings.HasPrefix(u, "git@") {
		u = strings.Replace(strings.TrimPrefix(u, "git@"), ":", "/", 1)
	}
	if at := strings.Index(u, "@"); at >= 0 && at < strings.Index(u+"/", "/") {
		u = u[at+1:] // user:token@host
	}
	u = strings.TrimSuffix(strings.TrimRight(u, "/"), ".git")
	return u
}

// ghRepo는 webhook payload의 저장소 정보다.
type ghRepo struct {
	FullName string `json:"full_name"`
	CloneURL string `json:"clone_url"`
	SSHURL   string `json:"ssh_url"`
	HTMLURL  string `json:"html_url"`
	GitURL   string `json:"git_url"`
}

// keys는 저장소를 가리키는 정규화된 주소들이다.
func (g *ghRepo) keys() map[string]bool {
	repos := map[string]bool{}
	if g == nil {
		return repos
	}
	for _, u := range []string{g.CloneURL, g.SSHURL, g.HTMLURL, g.GitURL} {
		if u != "" {
			repos[NormalizeRepo(u)] = true
		}
	}
	if g.FullName != "" {
		repos["github.com/"+strings.ToLower(g.FullName)] = true
	}
	return repos
}

// primary는 저장소의 대표 주소다(미리보기 환경에 기록해 다음 PR 이벤트에서 찾는다).
func (g *ghRepo) primary() string {
	if g == nil {
		return ""
	}
	if g.FullName != "" {
		return "github.com/" + strings.ToLower(g.FullName)
	}
	for _, u := range []string{g.HTMLURL, g.CloneURL, g.SSHURL, g.GitURL} {
		if u != "" {
			return NormalizeRepo(u)
		}
	}
	return ""
}

type pushEvent struct {
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Deleted    bool   `json:"deleted"`
	Repository ghRepo `json:"repository"`
	HeadCommit *struct {
		ID      string `json:"id"`
		Message string `json:"message"`
		Author  struct {
			Name string `json:"name"`
		} `json:"author"`
	} `json:"head_commit"`
	Commits []struct {
		Added    []string `json:"added"`
		Removed  []string `json:"removed"`
		Modified []string `json:"modified"`
	} `json:"commits"`
}

type deployResult struct {
	ID     string             `json:"id"`
	Status store.DeployStatus `json:"status"`
}

func (s *Server) githubWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 25<<20))
	if err != nil {
		writeErr(w, 400, "read body")
		return
	}
	if s.GitHubSecret == "" {
		writeErr(w, 403, "KEEL_GITHUB_WEBHOOK_SECRET is not configured")
		return
	}
	if !VerifySignature(s.GitHubSecret, body, r.Header.Get("X-Hub-Signature-256")) {
		writeErr(w, 401, "invalid signature")
		return
	}
	// GitHub는 10초 뒤 연결을 끊는다. 환경 생성/배포 등록이 중간에 끊기지 않도록 요청 취소와 분리한다.
	ctx := context.WithoutCancel(r.Context())
	ev := r.Header.Get("X-GitHub-Event")
	if m := s.maintenance(ctx); m.Enabled && m.PauseWebhooks && ev != "ping" {
		// 점검 모드에서 owner가 웹훅 배포를 멈췄다. GitHub는 다시 보내지 않으므로 끝나면 수동으로 배포한다.
		s.Log.Info("github webhook skipped: maintenance", "event", ev)
		writeJSON(w, 202, map[string]any{"skipped": "maintenance"})
		return
	}
	switch ev {
	case "ping":
		writeJSON(w, 200, map[string]any{"ok": true})
	case "push":
		s.githubPush(ctx, w, body)
	case "pull_request":
		s.githubPullRequest(ctx, w, body)
	case "check_suite", "workflow_run":
		s.githubCI(ctx, w, ev, body)
	default:
		writeJSON(w, 202, map[string]any{"ignored": ev})
	}
}

// githubPush는 push된 브랜치를 쓰는 auto_deploy 인스턴스를 배포한다.
// PR 미리보기 환경의 인스턴스는 건너뛴다: PR head 브랜치로의 push는 pull_request(synchronize) 이벤트로 배포된다(이중 배포 방지).
func (s *Server) githubPush(ctx context.Context, w http.ResponseWriter, body []byte) {
	var ev pushEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	branch, isBranch := strings.CutPrefix(ev.Ref, "refs/heads/")
	if !isBranch || ev.Deleted {
		writeJSON(w, 202, map[string]any{"deployments": []string{}})
		return
	}
	repos := ev.Repository.keys()
	changedSet := map[string]bool{}
	for _, c := range ev.Commits {
		for _, l := range [][]string{c.Added, c.Removed, c.Modified} {
			for _, f := range l {
				changedSet[f] = true
			}
		}
	}
	changed := make([]string, 0, len(changedSet))
	for f := range changedSet {
		changed = append(changed, f)
	}
	sort.Strings(changed)

	insts, err := s.Store.ListInstances(ctx)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	preview, err := s.previewEnvIDs(ctx)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	out := []deployResult{}
	for _, in := range insts {
		if !in.AutoDeploy || in.RepoURL == nil || in.Branch != branch || !repos[NormalizeRepo(*in.RepoURL)] {
			continue
		}
		if in.Status == store.InstOffline || preview[in.EnvironmentID] {
			continue
		}
		o := deploy.EnqueueOpts{Trigger: "github", ChangedFiles: changed}
		o.Commit = &nodeCommit{SHA: ev.After}
		if ev.HeadCommit != nil {
			o.Commit.Message, o.Commit.Author = firstLine(ev.HeadCommit.Message), ev.HeadCommit.Author.Name
			if o.Commit.SHA == "" {
				o.Commit.SHA = ev.HeadCommit.ID
			}
		}
		switch {
		case !watch.Match(in.WatchPaths, changed):
			o.Status = store.DepSkipped
		case in.WaitForCI && o.Commit.SHA != "":
			o.Status = store.DepWaiting
		}
		d, err := s.Deploy.Enqueue(ctx, in, o)
		if err != nil {
			s.Log.Warn("github deploy", "instance", in.ID, "err", err)
			continue
		}
		out = append(out, deployResult{ID: d.ID, Status: d.Status})
	}
	writeJSON(w, 202, map[string]any{"deployments": out})
}

func (s *Server) previewEnvIDs(ctx context.Context) (map[string]bool, error) {
	envs, err := s.Store.ListEnvironments(ctx, "")
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, e := range envs {
		if e.Kind == store.EnvPreview {
			out[e.ID] = true
		}
	}
	return out, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ---- pull_request: PR 미리보기 환경 ----

type prEvent struct {
	Action       string `json:"action"`
	Number       int    `json:"number"`
	fileList     []string
	fileListOK   bool
	fileListRead bool
	PullRequest  struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		Title   string `json:"title"`
		User    struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"user"`
		Head struct {
			Ref  string  `json:"ref"`
			SHA  string  `json:"sha"`
			Repo *ghRepo `json:"repo"`
		} `json:"head"`
		Base struct {
			Ref  string  `json:"ref"`
			Repo *ghRepo `json:"repo"`
		} `json:"base"`
		ChangedFiles int `json:"changed_files"`
	} `json:"pull_request"`
	Repository ghRepo `json:"repository"`
}

var fullSHARe = regexp.MustCompile(`^[0-9a-f]{40}$`)

func validBranch(b string) bool { return branchRe.MatchString(b) && !strings.HasPrefix(b, "-") }

func (ev *prEvent) baseRepo() *ghRepo {
	if ev.PullRequest.Base.Repo != nil {
		return ev.PullRequest.Base.Repo
	}
	return &ev.Repository
}

// fork는 PR head가 다른 저장소(fork)에서 왔는지다. head 저장소가 지워졌으면 fork로 본다.
func (ev *prEvent) fork() bool {
	h := ev.PullRequest.Head.Repo
	return h == nil || h.primary() == "" || h.primary() != ev.baseRepo().primary()
}

func (ev *prEvent) bot() bool {
	login := strings.ToLower(ev.PullRequest.User.Login)
	return strings.EqualFold(ev.PullRequest.User.Type, "bot") || strings.HasSuffix(login, "[bot]") || login == "dependabot" || login == "renovate"
}

// targetBranch는 미리보기 환경의 repo 인스턴스가 쓸 브랜치다. 같은 저장소면 PR head 브랜치,
// fork면 head 브랜치가 base 저장소에 없으므로 base 브랜치를 clone한 뒤 head SHA(refs/pull/N/head로 받을 수 있다)를 받는다.
func (ev *prEvent) targetBranch() string {
	if ev.fork() {
		return ev.PullRequest.Base.Ref
	}
	return ev.PullRequest.Head.Ref
}

type previewResult struct {
	ProjectID     string         `json:"project_id"`
	EnvironmentID string         `json:"environment_id,omitempty"`
	Action        string         `json:"action"` // created | deployed | deleted | kept | skipped
	Reason        string         `json:"reason,omitempty"`
	Deployments   []deployResult `json:"deployments"`
}

func (s *Server) githubPullRequest(ctx context.Context, w http.ResponseWriter, body []byte) {
	var ev prEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if ev.Number == 0 {
		ev.Number = ev.PullRequest.Number
	}
	pr := &ev.PullRequest
	switch ev.Action {
	case "opened", "reopened", "synchronize", "closed":
	default:
		writeJSON(w, 202, map[string]any{"ignored": "pull_request." + ev.Action})
		return
	}
	if ev.Number <= 0 || ev.baseRepo().primary() == "" || !validBranch(pr.Base.Ref) || (ev.Action != "closed" && (!fullSHARe.MatchString(pr.Head.SHA) || !validBranch(pr.Head.Ref))) {
		writeErr(w, 400, "invalid pull_request payload")
		return
	}
	var out []previewResult
	var err error
	switch ev.Action {
	case "opened", "reopened":
		out, err = s.prOpened(ctx, &ev)
	case "synchronize":
		out, err = s.prSynchronized(ctx, &ev)
	case "closed":
		out, err = s.prClosed(ctx, &ev)
	}
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if out == nil {
		out = []previewResult{}
	}
	writeJSON(w, 202, map[string]any{"environments": out})
}

// findPreview는 프로젝트에서 PR에 묶인 미리보기 환경을 찾는다.
func (s *Server) findPreview(ctx context.Context, projectID string, ev *prEvent) (*store.Environment, error) {
	envs, err := s.Store.ListPreviewEnvironments(ctx, ev.baseRepo().primary(), ev.Number)
	if err != nil {
		return nil, err
	}
	for _, e := range envs {
		if e.ProjectID == projectID {
			return e, nil
		}
	}
	return nil, nil
}

// prOpened는 PR 환경이 켜진 프로젝트마다, base 환경에 PR 저장소·base 브랜치를 쓰는 인스턴스가 있으면 pr-<번호> 환경을 만들어 배포한다.
func (s *Server) prOpened(ctx context.Context, ev *prEvent) ([]previewResult, error) {
	projects, err := s.Store.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	repos := ev.baseRepo().keys()
	out := []previewResult{}
	for _, p := range projects {
		if !p.PREnvironments.Enabled {
			continue
		}
		res := previewResult{ProjectID: p.ID, Deployments: []deployResult{}}
		// 배포 출처 guardrail: 승인되지 않은 조직의 PR은 미리보기 환경을 만들지 않는다(배포 자체는 Enqueue도 막는다).
		if repoURL := ev.baseRepo().primary(); s.checkSourcePolicy(ctx, guardrails.Source{Type: "repo", RepoURL: &repoURL}) != nil {
			res.Action, res.Reason = "skipped", "repository is blocked by the workspace deployment source policy"
			out = append(out, res)
			continue
		}
		baseID := p.DefaultEnvironmentID
		if p.PREnvironments.BaseEnvironmentID != nil {
			baseID = *p.PREnvironments.BaseEnvironmentID
		}
		base, err := s.Store.GetEnvironment(ctx, baseID)
		if err != nil || base.ProjectID != p.ID || base.Kind != store.EnvStandard {
			continue
		}
		insts, err := s.Store.ListInstancesByEnv(ctx, base.ID)
		if err != nil {
			return nil, err
		}
		matched := false
		for _, in := range insts {
			if in.RepoURL != nil && repos[NormalizeRepo(*in.RepoURL)] && in.Branch == ev.PullRequest.Base.Ref {
				matched = true
			}
		}
		if !matched {
			continue
		}
		if ev.bot() && !p.PRFromBots {
			res.Action, res.Reason = "skipped", "bot PR (pr_from_bots is off)"
			out = append(out, res)
			continue
		}
		if ev.fork() && !p.PRFromForks {
			res.Action, res.Reason = "skipped", "fork PR (pr_from_forks is off)"
			out = append(out, res)
			continue
		}
		existing, err := s.findPreview(ctx, p.ID, ev)
		if err != nil {
			return nil, err
		}
		if existing != nil { // 재전송된 opened 또는 auto_delete가 꺼진 채 닫혔다가 다시 열린 PR
			res.EnvironmentID, res.Action = existing.ID, "deployed"
			files, focused := s.previewFiles(ctx, p.ID, ev, p.PREnvironments.Focused)
			res.Deployments = s.deployPreview(ctx, p, existing, ev, false, focused, files)
			out = append(out, res)
			continue
		}
		num, url, branch, repo := ev.Number, ev.PullRequest.HTMLURL, ev.PullRequest.Head.Ref, ev.baseRepo().primary()
		env := &store.Environment{ProjectID: p.ID, Name: fmt.Sprintf("pr-%d", ev.Number), Kind: store.EnvPreview,
			PRNumber: &num, PRURL: strPtrOrNil(url), PRBranch: &branch, PRRepo: &repo}
		domains := p.PREnvironments.Domain
		if err := s.forkEnvironment(ctx, p, env, base, forkOpts{PreviewDomains: &domains}); err != nil {
			s.Log.Warn("preview environment", "project", p.ID, "pr", ev.Number, "err", err)
			res.Action, res.Reason = "skipped", "create environment failed"
			if err == store.ErrConflict {
				res.Reason = "environment " + env.Name + " already exists"
			}
			out = append(out, res)
			continue
		}
		res.EnvironmentID, res.Action = env.ID, "created"
		files, focused := s.previewFiles(ctx, p.ID, ev, p.PREnvironments.Focused)
		res.Deployments = s.deployPreview(ctx, p, env, ev, true, focused, files)
		s.Events.Emit(ctx, p.ID, &env.ID, nil, "environment.preview_created",
			fmt.Sprintf("PR #%d 미리보기 환경 %s 생성 (%s에서 복사, 브랜치 %s)", ev.Number, env.Name, base.Name, branch))
		out = append(out, res)
	}
	return out, nil
}

func (s *Server) previewFiles(ctx context.Context, projectID string, ev *prEvent, focused bool) ([]string, bool) {
	if !focused {
		return nil, false
	}
	if ev.fileListRead {
		return ev.fileList, ev.fileListOK
	}
	ev.fileListRead = true
	files, err := s.pullRequestChangedFiles(ctx, projectID, ev)
	if err != nil {
		s.Log.Warn("focused PR files", "repo", ev.baseRepo().primary(), "pr", ev.Number, "err", err)
		return nil, false // If GitHub cannot provide a complete list, deploy the full preview.
	}
	ev.fileList, ev.fileListOK = files, true
	return files, true
}

// prSynchronized는 PR에 새 커밋이 올라오면 미리보기 환경의 변경된 서비스와 의존 서비스를 배포한다.
func (s *Server) prSynchronized(ctx context.Context, ev *prEvent) ([]previewResult, error) {
	envs, err := s.Store.ListPreviewEnvironments(ctx, ev.baseRepo().primary(), ev.Number)
	if err != nil {
		return nil, err
	}
	out := []previewResult{}
	for _, env := range envs {
		p, err := s.Store.GetProject(ctx, env.ProjectID)
		if err != nil {
			continue
		}
		res := previewResult{ProjectID: p.ID, EnvironmentID: env.ID, Deployments: []deployResult{}}
		if !p.PREnvironments.Enabled {
			res.Action, res.Reason = "skipped", "pr_environments disabled"
		} else if ev.bot() && !p.PRFromBots {
			res.Action, res.Reason = "skipped", "bot PR (pr_from_bots is off)"
		} else if ev.fork() && !p.PRFromForks {
			res.Action, res.Reason = "skipped", "fork PR (pr_from_forks is off)"
		} else {
			res.Action = "deployed"
			files, focused := s.previewFiles(ctx, p.ID, ev, p.PREnvironments.Focused)
			res.Deployments = s.deployPreview(ctx, p, env, ev, false, focused, files)
		}
		out = append(out, res)
	}
	return out, nil
}

// prClosed는 PR이 닫히면(병합 포함) auto_delete인 프로젝트의 미리보기 환경을 지운다(컨테이너·볼륨 포함).
func (s *Server) prClosed(ctx context.Context, ev *prEvent) ([]previewResult, error) {
	envs, err := s.Store.ListPreviewEnvironments(ctx, ev.baseRepo().primary(), ev.Number)
	if err != nil {
		return nil, err
	}
	out := []previewResult{}
	for _, env := range envs {
		p, err := s.Store.GetProject(ctx, env.ProjectID)
		if err != nil {
			continue
		}
		res := previewResult{ProjectID: p.ID, EnvironmentID: env.ID, Deployments: []deployResult{}}
		if !p.PREnvironments.AutoDelete {
			res.Action = "kept"
			out = append(out, res)
			continue
		}
		if err := s.deleteEnvResources(ctx, env); err != nil {
			s.Log.Warn("delete preview environment", "env", env.ID, "err", err)
			res.Action, res.Reason = "skipped", "delete failed"
			out = append(out, res)
			continue
		}
		if p.DefaultEnvironmentID == env.ID { // 기본 환경으로 지정돼 있었으면 base 환경으로 되돌린다
			if p.PREnvironments.BaseEnvironmentID != nil {
				p.DefaultEnvironmentID = *p.PREnvironments.BaseEnvironmentID
			} else if all, err := s.Store.ListEnvironments(ctx, p.ID); err == nil && len(all) > 0 {
				p.DefaultEnvironmentID = all[0].ID
			}
			s.Store.UpdateProject(ctx, p)
		}
		res.Action = "deleted"
		s.Events.Emit(ctx, p.ID, nil, nil, "environment.preview_deleted", fmt.Sprintf("PR #%d가 닫혀 미리보기 환경 %s를 삭제했습니다", ev.Number, env.Name))
		out = append(out, res)
	}
	return out, nil
}

// deployPreview는 미리보기 환경을 배포한다. PR 저장소를 쓰는 repo 인스턴스는 PR 브랜치로 바꾸고 head SHA를 그대로 배포한다
// (wait_for_ci면 CI를 기다린다). 일반 환경은 initial 때 전체 서비스, focused 환경은 변경 서비스와 그 의존 서비스만 배포한다.
func (s *Server) deployPreview(ctx context.Context, p *store.Project, env *store.Environment, ev *prEvent, initial, focused bool, changed []string) []deployResult {
	out := []deployResult{}
	repos := ev.baseRepo().keys()
	head := ev.PullRequest.Head
	if env.BaseEnvironmentID == nil {
		baseID := p.DefaultEnvironmentID
		if p.PREnvironments.BaseEnvironmentID != nil {
			baseID = *p.PREnvironments.BaseEnvironmentID
		}
		if baseID != env.ID {
			if err := s.Store.SetEnvironmentBase(ctx, env.ID, &baseID); err == nil {
				env.BaseEnvironmentID = &baseID
			}
		}
	}
	if url, branch := strPtrOrNil(ev.PullRequest.HTMLURL), head.Ref; url != nil {
		s.Store.SetEnvironmentPR(ctx, env.ID, url, &branch)
	}
	insts, err := s.Store.ListInstancesByEnv(ctx, env.ID)
	if err != nil {
		s.Log.Warn("preview deploy", "env", env.ID, "err", err)
		return out
	}
	svcs, err := s.Store.ListServices(ctx, p.ID)
	if err != nil {
		s.Log.Warn("preview deploy services", "env", env.ID, "err", err)
		return out
	}
	serviceByID := make(map[string]*store.Service, len(svcs))
	serviceIDByName := make(map[string]string, len(svcs))
	for _, svc := range svcs {
		serviceByID[svc.ID] = svc
		serviceIDByName[svc.Name] = svc.ID
	}
	target := ev.targetBranch()
	selected := map[string]bool{}
	direct := map[string]bool{} // focused: services whose own files changed
	prRepoByID := map[string]bool{}
	for _, in := range insts {
		svc := serviceByID[in.ServiceID]
		if svc == nil {
			continue
		}
		prRepo := svc.SourceType == "repo" && in.RepoURL != nil && repos[NormalizeRepo(*in.RepoURL)] &&
			(in.Branch == target || (initial && in.Branch == ev.PullRequest.Base.Ref))
		prRepoByID[in.ID] = prRepo
		if focused {
			if prRepo && watch.MatchService(in.WatchPaths, in.RootDir, changed) {
				selected[in.ServiceID], direct[in.ServiceID] = true, true
			}
		} else if initial || prRepo {
			selected[in.ServiceID] = true
		}
	}
	if focused {
		shared, err := s.Store.GetSharedVars(ctx, env.ID)
		if err != nil {
			s.Log.Warn("preview dependencies", "env", env.ID, "err", err)
			return out
		}
		dependencies := previewServiceDependencies(insts, serviceIDByName, shared)
		queue := make([]string, 0, len(selected))
		for id := range selected {
			queue = append(queue, id)
		}
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			for dependency := range dependencies[id] {
				if !selected[dependency] {
					selected[dependency] = true
					queue = append(queue, dependency)
				}
			}
		}
	}
	// Record skipped services before dispatching selected services so variable
	// resolution can use the base environment for untouched service references.
	if focused && initial {
		for _, in := range insts {
			if selected[in.ServiceID] {
				continue
			}
			// 건너뛴 PR 저장소 서비스도 PR 브랜치를 가리키게 해 둔다. 이후 커밋이 이 서비스를 건드리거나
			// 캔버스에서 직접 배포하면 PR 코드가 배포된다.
			if prRepoByID[in.ID] && in.Branch != target {
				in.Branch = target
				if err := s.Store.UpdateInstanceConfig(ctx, in); err != nil {
					s.Log.Warn("preview branch", "instance", in.ID, "err", err)
				}
			}
			d, err := s.Deploy.Enqueue(ctx, in, deploy.EnqueueOpts{Trigger: "github", Status: store.DepSkipped})
			if err != nil {
				s.Log.Warn("skip focused preview service", "instance", in.ID, "err", err)
				continue
			}
			out = append(out, deployResult{ID: d.ID, Status: d.Status})
		}
	}
	for _, in := range insts {
		if !selected[in.ServiceID] {
			continue
		}
		svc := serviceByID[in.ServiceID]
		if svc == nil {
			continue
		}
		prRepo := prRepoByID[in.ID]
		if !initial && in.Status == store.InstOffline {
			continue
		}
		// 이후 커밋에서 의존 서비스는 아직 미리보기에 배포된 적이 없을 때(처음에 건너뜀)만 배포한다.
		if focused && !initial && !direct[in.ServiceID] && in.CurrentDeploymentID != nil {
			continue
		}
		o := deploy.EnqueueOpts{Trigger: "github"}
		if prRepo {
			if in.Branch != target {
				in.Branch = target
				if err := s.Store.UpdateInstanceConfig(ctx, in); err != nil {
					s.Log.Warn("preview branch", "instance", in.ID, "err", err)
					continue
				}
			}
			o.Commit = &nodeCommit{SHA: head.SHA, Message: firstLine(fmt.Sprintf("PR #%d: %s", ev.Number, ev.PullRequest.Title)), Author: ev.PullRequest.User.Login}
			if in.WaitForCI {
				o.Status = store.DepWaiting
			}
		}
		if sourceErr(svc, in) != nil {
			continue
		}
		d, err := s.Deploy.Enqueue(ctx, in, o)
		if err != nil {
			s.Log.Warn("preview deploy", "instance", in.ID, "err", err)
			continue
		}
		out = append(out, deployResult{ID: d.ID, Status: d.Status})
	}
	return out
}

func previewServiceDependencies(insts []*store.Instance, serviceIDByName map[string]string, shared map[string]string) map[string]map[string]bool {
	byID := map[string]string{}
	for name, id := range serviceIDByName {
		byID[id] = name
	}
	dependencies := map[string]map[string]bool{}
	for _, in := range insts {
		serviceName := byID[in.ServiceID]
		if serviceName == "" {
			continue
		}
		if dependencies[in.ServiceID] == nil {
			dependencies[in.ServiceID] = map[string]bool{}
		}
		sharedSeen := map[string]bool{}
		var addRefs func(string)
		addRefs = func(value string) {
			for _, ref := range vars.References(value) {
				scope, name, qualified := strings.Cut(ref, ".")
				if !qualified {
					continue
				}
				if scope == "shared" {
					if sharedSeen[name] {
						continue
					}
					sharedSeen[name] = true
					addRefs(shared[name])
					continue
				}
				dependencyID := serviceIDByName[scope]
				if dependencyID != "" && dependencyID != in.ServiceID && scope != serviceName {
					dependencies[in.ServiceID][dependencyID] = true
				}
			}
		}
		for _, value := range in.Variables {
			addRefs(value)
		}
	}
	return dependencies
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ---- check_suite / workflow_run: wait_for_ci ----

type ciEvent struct {
	Action     string `json:"action"`
	CheckSuite *struct {
		HeadSHA    string  `json:"head_sha"`
		Conclusion *string `json:"conclusion"`
	} `json:"check_suite"`
	WorkflowRun *struct {
		HeadSHA    string  `json:"head_sha"`
		Conclusion *string `json:"conclusion"`
	} `json:"workflow_run"`
	Repository ghRepo `json:"repository"`
}

// ciOutcome은 GitHub conclusion을 pass/fail로 나눈다. 그 밖(stale, null 등)은 판단하지 않는다("").
func ciOutcome(conclusion string) string {
	switch conclusion {
	case "success", "neutral", "skipped":
		return "pass"
	case "failure", "timed_out", "cancelled", "action_required", "startup_failure":
		return "fail"
	}
	return ""
}

// githubCI는 커밋의 CI가 끝났을 때 그 SHA를 기다리던(waiting) 배포를 큐에 넣거나(성공) 건너뛴다(실패).
func (s *Server) githubCI(ctx context.Context, w http.ResponseWriter, kind string, body []byte) {
	var ev ciEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	var sha string
	var conclusion *string
	switch {
	case kind == "check_suite" && ev.CheckSuite != nil:
		sha, conclusion = ev.CheckSuite.HeadSHA, ev.CheckSuite.Conclusion
	case kind == "workflow_run" && ev.WorkflowRun != nil:
		sha, conclusion = ev.WorkflowRun.HeadSHA, ev.WorkflowRun.Conclusion
	}
	outcome := ""
	if conclusion != nil {
		outcome = ciOutcome(*conclusion)
	}
	if ev.Action != "completed" || outcome == "" || !fullSHARe.MatchString(sha) {
		writeJSON(w, 202, map[string]any{"ignored": kind + "." + ev.Action})
		return
	}
	deps, err := s.Store.ListWaitingBySHA(ctx, sha)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	repos := ev.Repository.keys()
	released, skipped := []string{}, []string{}
	for _, d := range deps {
		if d.Spec.RepoURL == nil || !repos[NormalizeRepo(*d.Spec.RepoURL)] {
			continue // 같은 SHA라도 다른 저장소의 배포는 건드리지 않는다
		}
		if outcome == "pass" {
			if ok, err := s.Deploy.ReleaseWaiting(ctx, d.ID); err != nil {
				s.Log.Warn("release waiting deployment", "deployment", d.ID, "err", err)
			} else if ok {
				released = append(released, d.ID)
			}
			continue
		}
		if ok, err := s.Deploy.SkipWaiting(ctx, d.ID, "CI 실패"); err != nil {
			s.Log.Warn("skip waiting deployment", "deployment", d.ID, "err", err)
		} else if ok {
			skipped = append(skipped, d.ID)
		}
	}
	writeJSON(w, 202, map[string]any{"released": released, "skipped": skipped})
}
