// Package ghstatus는 배포 상태를 GitHub commit status로 올린다(POST /repos/{owner}/{repo}/statuses/{sha}).
//
// 배포 경로를 막지 않도록 Hook은 큐에 넣기만 하고, 한 worker가 순서대로(같은 배포의 pending → success가 뒤바뀌지 않게) 보낸다.
// 요청마다 제한 시간이 있고, 실패는 로그만 남긴다(배포에는 영향이 없다).
package ghstatus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

// DefaultAPIBase는 GitHub REST API 주소다(KEEL_GITHUB_API_URL로 바꿀 수 있다).
const DefaultAPIBase = "https://api.github.com"

type Poster struct {
	Store *store.Store
	Token string // KEEL_GITHUB_TOKEN. 비면 아무것도 보내지 않는다(TokenFunc가 있으면 그쪽을 쓴다)
	// TokenFunc가 있으면 배포마다 토큰을 묻는다(GitHub 연결은 사용자별이고 프로젝트마다 다르며 서버 실행 중에 바뀐다).
	TokenFunc func(context.Context, *store.Deployment) string
	APIBase   string // 비면 DefaultAPIBase
	PublicURL string // KEEL_PUBLIC_URL. 있으면 target_url로 배포 화면을 건다
	HTTP      *http.Client
	Log       *slog.Logger
	Timeout   time.Duration // 요청당 제한 시간(기본 10초)

	once sync.Once
	ch   chan store.Deployment
}

// Hook은 deploy.Manager.StatusHook에 넣는다. 큐가 가득 차면 버린다(배포를 막지 않는다).
func (p *Poster) token(ctx context.Context, d *store.Deployment) string {
	if p.TokenFunc != nil {
		return p.TokenFunc(ctx, d)
	}
	return p.Token
}

func (p *Poster) Hook(d store.Deployment) {
	if d.CommitSHA == nil || (p.TokenFunc == nil && p.Token == "") {
		return
	}
	p.once.Do(func() {
		p.ch = make(chan store.Deployment, 256)
		go p.run()
	})
	select {
	case p.ch <- d:
	default:
		p.logWarn("github status queue full, dropping", "deployment", d.ID)
	}
}

func (p *Poster) run() {
	for d := range p.ch {
		if err := p.Post(context.Background(), &d); err != nil {
			p.logWarn("github commit status", "deployment", d.ID, "err", err)
		}
	}
}

func (p *Poster) logWarn(msg string, args ...any) {
	if p.Log != nil {
		p.Log.Warn(msg, args...)
	}
}

// State는 배포 상태를 GitHub status state와 설명으로 바꾼다. ok=false면 올리지 않는다.
func State(d *store.Deployment) (state, desc string, ok bool) {
	switch d.Status {
	case store.DepWaiting:
		return "pending", "CI 통과를 기다리는 중", true
	case store.DepQueued:
		return "pending", "배포 대기 중", true
	case store.DepInitializing, store.DepBuilding, store.DepPreDeploy, store.DepDeploying, store.DepHealthchecking:
		return "pending", "배포 중", true
	case store.DepLive:
		return "success", "배포 완료", true
	case store.DepFailed, store.DepCrashed:
		return "failure", "배포 실패", true
	case store.DepCancelled:
		return "error", "배포 취소됨", true
	case store.DepRemoved:
		return "error", "새 배포로 대체됨", true
	case store.DepSkipped:
		// watch paths 때문에 건너뛴 배포는 pending을 올린 적이 없으니 그대로 둔다. CI 실패로 건너뛴 것만 닫는다.
		if d.Error != nil && *d.Error != "" {
			return "error", *d.Error + " — 배포하지 않음", true
		}
	}
	return "", "", false
}

var (
	shaRe  = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)
	partRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
)

// OwnerRepo는 github.com 저장소 URL에서 owner/repo를 꺼낸다(다른 호스트면 ok=false).
func OwnerRepo(repoURL string) (owner, repo string, ok bool) {
	u := strings.ToLower(strings.TrimSpace(repoURL))
	for _, p := range []string{"https://", "http://", "ssh://", "git://"} {
		u = strings.TrimPrefix(u, p)
	}
	if strings.HasPrefix(u, "git@") {
		u = strings.Replace(strings.TrimPrefix(u, "git@"), ":", "/", 1)
	}
	if at := strings.Index(u, "@"); at >= 0 && at < strings.Index(u+"/", "/") {
		u = u[at+1:]
	}
	u = strings.TrimSuffix(strings.TrimRight(u, "/"), ".git")
	rest, found := strings.CutPrefix(u, "github.com/")
	if !found {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || !partRe.MatchString(parts[0]) || !partRe.MatchString(parts[1]) || parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// Context는 status context다: keel/<service>-<env>.
func Context(service, env string) string { return "keel/" + service + "-" + env }

// Post는 배포 하나의 상태를 바로 올린다. 올릴 대상이 아니면(토큰 없음, github 저장소 아님, sha 없음) nil.
func (p *Poster) Post(ctx context.Context, d *store.Deployment) error {
	token := p.token(ctx, d)
	if token == "" || d.CommitSHA == nil || !shaRe.MatchString(*d.CommitSHA) || d.Spec.RepoURL == nil {
		return nil
	}
	owner, repo, ok := OwnerRepo(*d.Spec.RepoURL)
	if !ok {
		return nil
	}
	state, desc, ok := State(d)
	if !ok {
		return nil
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	svcName, envName, projectID := d.ServiceID, d.EnvironmentID, ""
	if p.Store != nil {
		if sv, err := p.Store.GetService(ctx, d.ServiceID); err == nil {
			svcName, projectID = sv.Name, sv.ProjectID
		}
		if e, err := p.Store.GetEnvironment(ctx, d.EnvironmentID); err == nil {
			envName = e.Name
		}
	}
	body := map[string]string{"state": state, "description": truncate(desc, 140), "context": Context(svcName, envName)}
	if p.PublicURL != "" {
		q := url.Values{}
		if projectID != "" {
			q.Set("project", projectID)
		}
		q.Set("env", envName)
		q.Set("svc", d.ServiceID)
		q.Set("dep", d.ID)
		body["target_url"] = strings.TrimRight(p.PublicURL, "/") + "/?" + q.Encode()
	}
	b, _ := json.Marshal(body)
	base := strings.TrimRight(p.APIBase, "/")
	if base == "" {
		base = DefaultAPIBase
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/statuses/%s", base, url.PathEscape(owner), url.PathEscape(repo), strings.ToLower(*d.CommitSHA))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	client := p.HTTP
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("github statuses: HTTP %d", resp.StatusCode)
	}
	return nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
