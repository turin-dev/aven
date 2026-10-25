// 정책이 켜져 있으면 admin을 포함한 모든 사용자에게:
//   - 승인된 GitHub 조직(소유자 이름, 대소문자 무시)의 github.com 저장소만 배포할 수 있다
//   - 다른 저장소, Docker 이미지, 함수, CLI 업로드(aven up) 배포는 막는다(출처를 GitHub 조직으로 확인할 수 없다)
//   - 내장 데이터베이스(catalog.Databases, bucket)는 공식 이미지일 때만 허용한다
//   - 소스가 없는 서비스(빈 서비스, 소스 연결 해제)는 배포할 것이 없으므로 허용한다
//
// API(서비스 생성·소스 변경)와 deploy.Manager.Enqueue(모든 새 배포)가 같은 함수로 검사한다. 기존 배포에는 영향이 없다.
package guardrails

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/turin-dev/aven/internal/catalog"
	"github.com/turin-dev/aven/internal/store"
)

// Source는 배포하거나 연결하려는 소스다.
type Source struct {
	Type         string // repo | image | database | function
	DatabaseKind string
	RepoURL      *string
	Image        *string
	Uploaded     bool // CLI 업로드(aven up)
}

// SourceOf는 서비스와 인스턴스 설정의 소스다.
func SourceOf(svc *store.Service, sp *store.Spec) Source {
	src := Source{Type: svc.SourceType, RepoURL: sp.RepoURL, Image: sp.Image}
	if svc.DatabaseKind != nil {
		src.DatabaseKind = *svc.DatabaseKind
	}
	return src
}

func restricted(reason string) error {
	return fmt.Errorf("%w: %s", store.ErrDeploymentSourceRestricted, reason)
}

func empty(p *string) bool { return p == nil || strings.TrimSpace(*p) == "" }

// Check는 정책 p로 src를 판정한다. 막히면 store.ErrDeploymentSourceRestricted를 감싼 오류다.
func Check(p *store.AccessControlSettings, src Source) error {
	if p == nil || !p.RestrictDeploymentSources {
		return nil
	}
	if src.Uploaded {
		return restricted("CLI uploads cannot be verified against an approved GitHub organization")
	}
	switch src.Type {
	case "database":
		if empty(src.Image) || OfficialDatabaseImage(src.DatabaseKind, *src.Image) {
			return nil
		}
		return restricted("only the official image of a built-in database can be deployed")
	case "repo":
		if empty(src.RepoURL) {
			return nil
		}
		owner, ok := GitHubOwner(*src.RepoURL)
		if !ok {
			return restricted("only github.com repositories from an approved organization can be deployed")
		}
		for _, org := range p.ApprovedGitHubOrgs {
			if strings.EqualFold(owner, org) {
				return nil
			}
		}
		return restricted("repository owner " + owner + " is not an approved GitHub organization")
	case "image":
		if empty(src.Image) {
			return nil
		}
		return restricted("Docker image sources cannot be verified against an approved GitHub organization")
	case "function":
		return restricted("function services cannot be verified against an approved GitHub organization")
	}
	return restricted("unknown source type")
}

// CheckStore는 저장된 정책으로 src를 판정한다.
func CheckStore(ctx context.Context, st *store.Store, src Source) error {
	p, err := st.AccessControlSettings(ctx)
	if err != nil {
		return err
	}
	return Check(p, src)
}

// GitHubOwner는 github.com 저장소 URL(https://, ssh://, git@)의 소유자(조직/사용자)다.
func GitHubOwner(repo string) (string, bool) {
	repo = strings.TrimSpace(repo)
	var host, path string
	if rest, ok := strings.CutPrefix(repo, "git@"); ok && !strings.Contains(repo, "://") {
		h, p, ok := strings.Cut(rest, ":")
		if !ok {
			return "", false
		}
		host, path = h, p
	} else {
		u, err := url.Parse(repo)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "ssh" && u.Scheme != "git") {
			return "", false
		}
		host, path = u.Hostname(), u.Path
	}
	host = strings.ToLower(host)
	if host != "github.com" && host != "www.github.com" {
		return "", false
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || strings.TrimSuffix(parts[1], ".git") == "" {
		return "", false
	}
	return strings.ToLower(parts[0]), true
}

// imageRepo는 태그·digest를 뺀 이미지 이름이다(docker.io/library/ 접두사는 무시).
func imageRepo(image string) string {
	image = strings.ToLower(strings.TrimSpace(image))
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		image = image[:i]
	}
	for _, p := range []string{"docker.io/library/", "docker.io/", "index.docker.io/library/", "library/"} {
		image = strings.TrimPrefix(image, p)
	}
	return image
}

// OfficialDatabaseImage는 image가 내장 데이터베이스 kind의 공식 이미지(태그는 무관)인지다.
func OfficialDatabaseImage(kind, image string) bool {
	var official string
	if kind == catalog.BucketKind {
		official = catalog.BucketImage
	} else if db, ok := catalog.Databases[kind]; ok {
		official = db.Image
	} else {
		return false
	}
	return imageRepo(image) == imageRepo(official)
}
