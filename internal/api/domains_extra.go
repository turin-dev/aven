package api

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/store"
	"golang.org/x/crypto/bcrypt"
)

var (
	pathRuleRe   = regexp.MustCompile(`^/[A-Za-z0-9/._~*%:@!$&'()+,;=-]{0,255}$`)
	redirectToRe = regexp.MustCompile(`^(https?://)?[A-Za-z0-9.-]+(:[0-9]{1,5})?(/[A-Za-z0-9/._~%:@!$&'()+,;=?#-]*)?$`)
	authUserRe   = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)
)

// validateDomainExtras는 경로 규칙, TLS 모드, 리다이렉트, basic auth 입력을 검사하고 정규화한다.
func (s *Server) validateDomainExtras(ctx context.Context, in *store.Instance, d *store.Domain) error {
	switch d.TLS {
	case "default":
		d.TLS = ""
	case "", "auto", "internal", "off":
	default:
		return fmt.Errorf("tls must be one of auto, internal, off (got %q)", d.TLS)
	}
	if d.Redirect != nil {
		d.Redirect.To = strings.TrimSpace(d.Redirect.To)
		if !redirectToRe.MatchString(d.Redirect.To) || len(d.Redirect.To) > 300 {
			return fmt.Errorf("invalid redirect target: %q", d.Redirect.To)
		}
		to := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(d.Redirect.To, "https://"), "http://"))
		if h, _, ok := strings.Cut(to, "/"); ok {
			to = h
		}
		if to == d.Host {
			return errors.New("redirect target is the same host (redirect loop)")
		}
		if len(d.Paths) > 0 {
			return errors.New("a redirect domain cannot have path rules")
		}
	}
	if d.BasicAuth != nil && !authUserRe.MatchString(d.BasicAuth.Username) {
		return errors.New("basic_auth.username must be 1-64 chars of letters, digits, . _ @ -")
	}
	if d.Upstream != "" || len(d.UpstreamPaths) > 0 {
		if err := s.validateExternalUpstream(ctx, in, d); err != nil {
			return err
		}
	}
	if len(d.Paths) == 0 {
		return nil
	}
	if len(d.Paths) > 50 {
		return errors.New("too many path rules (max 50)")
	}
	names := map[string]*store.Instance{}
	if insts, err := s.Store.ListInstancesByEnv(ctx, in.EnvironmentID); err == nil {
		for _, o := range insts {
			if sv, err := s.Store.GetService(ctx, o.ServiceID); err == nil {
				names[sv.Name] = o
			}
		}
	}
	self, _ := s.Store.GetService(ctx, in.ServiceID)
	seen := map[string]bool{}
	for i := range d.Paths {
		pr := &d.Paths[i]
		pr.Path = strings.TrimSpace(pr.Path)
		if !pathRuleRe.MatchString(pr.Path) {
			return fmt.Errorf("invalid path %q (must start with /)", pr.Path)
		}
		if seen[pr.Path] {
			return fmt.Errorf("duplicate path rule %q", pr.Path)
		}
		seen[pr.Path] = true
		if pr.StripPrefix && !strings.HasSuffix(pr.Path, "/*") {
			return fmt.Errorf("strip_prefix needs a prefix path ending in /*: %q", pr.Path)
		}
		if pr.TargetPort != nil && (*pr.TargetPort < 1 || *pr.TargetPort > 65535) {
			return errors.New("path target_port must be 1..65535")
		}
		if pr.Service != "" && (self == nil || pr.Service != self.Name) {
			o := names[pr.Service]
			if o == nil {
				return fmt.Errorf("path %s: service %q not found in this environment", pr.Path, pr.Service)
			}
			if s.Deploy.HomeNode(ctx, o) != s.Deploy.HomeNode(ctx, in) {
				return fmt.Errorf("path %s: service %q runs on a different node", pr.Path, pr.Service)
			}
		}
	}
	return nil
}

// applyDomainAuth는 요청의 basic auth 비밀번호를 bcrypt로 해시해 설정 테이블에 저장하고, 도메인에는 has_password만 남긴다.
// 비밀번호 없이 같은 사용자 이름이 오면 기존 해시를 유지한다. 도메인에서 빠진 호스트의 해시는 지운다.
func (s *Server) applyDomainAuth(ctx context.Context, in *store.Instance, doms []store.Domain) error {
	old := map[string]*store.BasicAuth{}
	for _, d := range in.Domains {
		old[d.Host] = d.BasicAuth
	}
	keep := map[string]bool{}
	for i := range doms {
		d := &doms[i]
		if d.BasicAuth == nil {
			continue
		}
		ba := d.BasicAuth
		switch {
		case ba.Password != "":
			if len(ba.Password) > 72 {
				return errors.New("basic_auth.password is too long (max 72 bytes)")
			}
			h, err := bcrypt.GenerateFromPassword([]byte(ba.Password), bcrypt.DefaultCost)
			if err != nil {
				return err
			}
			if err := s.Store.SetSetting(ctx, store.DomainAuthKey(in.ID, d.Host), string(h)); err != nil {
				return err
			}
		case old[d.Host] != nil && old[d.Host].HasPassword:
		default:
			return fmt.Errorf("basic_auth.password is required for %s", d.Host)
		}
		ba.Password, ba.HasPassword = "", true
		keep[d.Host] = true
	}
	for host, ba := range old {
		if ba != nil && !keep[host] {
			s.Store.SetSetting(ctx, store.DomainAuthKey(in.ID, host), "")
		}
	}
	return nil
}

// validateExternalUpstream은 aven 밖의 upstream 도메인을 검사한다. 대상은 그 인스턴스가 도는 노드의 허용 목록
// (KEEL_EXTERNAL_UPSTREAMS)에 있어야 하고, 값을 바꾸는 사람은 작업 공간 admin(세션 또는 KEEL_API_TOKEN)이어야 한다.
func (s *Server) validateExternalUpstream(ctx context.Context, in *store.Instance, d *store.Domain) error {
	d.Upstream = strings.TrimSpace(d.Upstream)
	if d.Upstream == "" {
		return errors.New("upstream_paths needs upstream")
	}
	if !proxy.ValidExternalUpstream(d.Upstream) {
		return fmt.Errorf("invalid upstream %q: want host:port (a container name or private IP, not loopback)", d.Upstream)
	}
	if d.Redirect != nil || len(d.Paths) > 0 {
		return errors.New("an upstream domain cannot have a redirect or path rules (use upstream_paths)")
	}
	if len(d.UpstreamPaths) > 20 {
		return errors.New("too many upstream_paths (max 20)")
	}
	for i, p := range d.UpstreamPaths {
		p = "/" + strings.Trim(strings.TrimSpace(p), "/")
		if !upstreamPathRe.MatchString(p) {
			return fmt.Errorf("invalid upstream path %q", d.UpstreamPaths[i])
		}
		d.UpstreamPaths[i] = p
	}
	// 바뀌지 않았으면 권한·허용 목록을 다시 보지 않는다(다른 설정을 바꾸는 member를 막지 않게).
	for _, old := range in.Domains {
		if old.Host == d.Host && old.Upstream == d.Upstream && slices.Equal(old.UpstreamPaths, d.UpstreamPaths) {
			return nil
		}
	}
	if p, _ := ctx.Value(principalKey{}).(*principal); p != nil && (s.instanceLevel(p) < lvAdmin || p.Token != nil || p.OAuth != nil) {
		return errors.New("only an instance admin (session) can route a domain to an upstream outside aven")
	}
	allowed := s.Deploy.Cfg.ExternalUpstreams
	if node := store.NormNode(in.NodeID); !store.IsLocalNode(node) {
		n, err := s.Store.GetNode(ctx, node)
		if err != nil {
			return err
		}
		allowed = n.ExternalUpstreams
	}
	if !slices.Contains(allowed, d.Upstream) {
		return fmt.Errorf("upstream %s is not allowed on this node: add it to KEEL_EXTERNAL_UPSTREAMS on the node (allowed: %s)", d.Upstream, strings.Join(allowed, ", "))
	}
	return nil
}

var upstreamPathRe = regexp.MustCompile(`^/[A-Za-z0-9._~/-]{0,200}$`)
