package deploy

import (
	"context"
	"strings"

	"github.com/turin-dev/aven/internal/store"
)

// GitHub 연결은 사용자별이다(store.github_connections). 사용자는 대시보드에서 자기 GitHub 계정을 연결하고, 저장소 목록·웹훅 등록은
// 요청한 사용자의 토큰으로 한다. 배포 중의 클론·커밋 상태·PR 파일 조회는 프로젝트에 지정된 사용자(projects.github_user_id,
// 기본은 프로젝트를 만든 사람)의 토큰을 쓴다. 다른 사람의 연결로 넘어가지 않으며, 없으면 KEEL_GITHUB_TOKEN(운영자가 정한 대체값)만 쓴다.

// UserGitHubConnection은 사용자의 GitHub 연결이다. 연결되지 않았으면 nil이다.
func (m *Manager) UserGitHubConnection(ctx context.Context, userID string) *store.GitHubConnection {
	if m.Store == nil || userID == "" {
		return nil
	}
	c, err := m.Store.GetGitHubConnection(ctx, userID)
	if err != nil {
		return nil
	}
	return c
}

// UserGitHubToken은 사용자가 연결한 GitHub 토큰(평문)이다. 없거나 복호화하지 못하면 "".
func (m *Manager) UserGitHubToken(ctx context.Context, userID string) string {
	c := m.UserGitHubConnection(ctx, userID)
	if c == nil {
		return ""
	}
	tok, err := m.Store.Unseal(c.Token)
	if err != nil {
		if m.Log != nil {
			m.Log.Warn("github connection token cannot be decrypted (master.key changed?)", "user", userID, "err", err)
		}
		return ""
	}
	return tok
}

// EnvGitHubToken은 KEEL_GITHUB_TOKEN이다(사용자 연결이 없는 프로젝트의 대체값).
func (m *Manager) EnvGitHubToken() string { return m.Cfg.GitHubToken }

// ProjectGitHubToken은 프로젝트의 GitHub 저장소 접근에 쓸 토큰이다: 프로젝트에 지정된 사용자의 연결, 없으면 KEEL_GITHUB_TOKEN.
func (m *Manager) ProjectGitHubToken(ctx context.Context, projectID string) string {
	if m.Store != nil && projectID != "" {
		if uid, err := m.Store.ProjectGitHubUser(ctx, projectID); err == nil && uid != "" {
			if t := m.UserGitHubToken(ctx, uid); t != "" {
				return t
			}
		}
	}
	return m.Cfg.GitHubToken
}

// ServiceGitHubToken은 서비스가 속한 프로젝트의 토큰이다(ProjectGitHubToken).
func (m *Manager) ServiceGitHubToken(ctx context.Context, serviceID string) string {
	if m.Store != nil && serviceID != "" {
		if sv, err := m.Store.GetService(ctx, serviceID); err == nil {
			return m.ProjectGitHubToken(ctx, sv.ProjectID)
		}
	}
	return m.Cfg.GitHubToken
}

// DeploymentGitHubToken은 배포의 서비스 프로젝트 토큰이다(ghstatus.Poster.TokenFunc).
func (m *Manager) DeploymentGitHubToken(ctx context.Context, d *store.Deployment) string {
	return m.ServiceGitHubToken(ctx, d.ServiceID)
}

// githubTokenFor는 GitHub 저장소일 때만 서비스 프로젝트의 토큰을 돌려준다(다른 호스트나 원격 노드로 새지 않게).
func (m *Manager) githubTokenFor(ctx context.Context, serviceID, repo string) string {
	if strings.HasPrefix(repo, "https://github.com/") {
		return m.ServiceGitHubToken(ctx, serviceID)
	}
	return ""
}
