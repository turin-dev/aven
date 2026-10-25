package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/store"
)

// private_dns_name을 정하면 환경 네트워크에서 <dns>, <dns>.<env>.keel.internal로도 풀린다(다음 배포부터).
// 서비스 이름 alias(<svc>, <svc>.<env>.keel.internal)는 하위 호환을 위해 항상 남는다.

// reservedPrivateNames는 사설 DNS 이름으로 쓸 수 없는 이름이다(변수 네임스페이스, 호스트·도커 이름).
var reservedPrivateNames = map[string]bool{
	"shared": true, "localhost": true, "local": true, "keel": true, "aven": true, "host": true, "gateway": true,
	"internal": true, "docker": true, "broadcasthost": true,
}

// reservedPrivatePrefixes는 aven이 환경 네트워크에서 쓰는 컨테이너 이름(keel-<svc>-…, keel-mesh-…, keel-tcp-…)과
// 샌드박스 alias(sbx-<id>)의 접두사다.
var reservedPrivatePrefixes = []string{"keel-", "sbx-"}

// privateDNS는 인스턴스의 사설 네트워크 이름이다: 대표 이름(private_dns_name, 없으면 서비스 이름), 그 FQDN,
// 그리고 환경 네트워크에서 풀리는 모든 이름(서비스 이름 alias 두 개 + 사설 DNS 이름 alias 두 개).
// 설정 값(in.Spec) 기준이다. 바꾼 이름은 다음 배포부터 붙는다(그 전에는 pending_changes에 private_dns_name이 보인다).
func privateDNS(in *store.Instance, svc *store.Service, env *store.Environment) (name, fqdn string, aliases []string) {
	name = deploy.PrivateDNSName(svc.Name, in.Spec.PrivateDNSName)
	return name, deploy.PrivateDomain(name, env.Name), deploy.PrivateAliases(svc.Name, env.Name, in.Spec.PrivateDNSName)
}

// normalizePrivateDNSName은 입력을 정리한다: 공백 제거, 소문자. 서비스 이름과 같으면 비운다(기본값과 같다).
func normalizePrivateDNSName(svc *store.Service, name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == svc.Name {
		return ""
	}
	return name
}

// privateDNSUnavailable은 svc가 환경 envID에서 사설 DNS 이름 name을 쓸 수 없는 이유다(쓸 수 있으면 "").
// name은 normalizePrivateDNSName으로 정리한 값이다. 비어 있거나 서비스 자기 이름이면 언제나 쓸 수 있다.
// 같은 환경의 다른 서비스 이름, 그 서비스의 private_dns_name(설정 값과 live 배포 값), compose alias와 겹치면 안 된다.
func (s *Server) privateDNSUnavailable(ctx context.Context, envID string, svc *store.Service, name string) (string, error) {
	if name == "" || name == svc.Name {
		return "", nil
	}
	if !nameRe.MatchString(name) {
		return "이름은 소문자, 숫자, '-'만 쓸 수 있고 '-'로 시작하거나 끝날 수 없으며 40자 이하여야 합니다", nil
	}
	if reservedPrivateNames[name] {
		return "예약된 이름이라 쓸 수 없습니다", nil
	}
	for _, p := range reservedPrivatePrefixes {
		if strings.HasPrefix(name, p) {
			return "'" + p + "'로 시작하는 이름은 aven이 쓰므로 쓸 수 없습니다", nil
		}
	}
	insts, err := s.Store.ListInstancesByEnv(ctx, envID)
	if err != nil {
		return "", err
	}
	names := map[string]string{}
	if env, err := s.Store.GetEnvironment(ctx, envID); err == nil {
		if svcs, err := s.Store.ListServices(ctx, env.ProjectID); err == nil {
			for _, o := range svcs {
				names[o.ID] = o.Name
			}
		}
	}
	for _, o := range insts {
		if o.ServiceID == svc.ID {
			continue
		}
		other := names[o.ServiceID]
		if other == "" {
			continue
		}
		taken := []string{other, o.Spec.PrivateDNSName}
		if o.Spec.Compose != nil {
			taken = append(taken, o.Spec.Compose.Aliases...)
		}
		if o.CurrentDeploymentID != nil {
			if d, err := s.Store.GetDeployment(ctx, *o.CurrentDeploymentID); err == nil {
				taken = append(taken, d.Spec.PrivateDNSName)
			}
		}
		for _, t := range taken {
			if t == name {
				if t == other {
					return "같은 환경에 이 이름의 서비스가 있습니다", nil
				}
				return "같은 환경의 서비스 " + other + "이(가) 이미 쓰는 이름입니다", nil
			}
		}
	}
	return "", nil
}

// privateDNSAvailable: GET /api/services/{sid}/instances/{eid}/private-dns/available?name=
// 응답은 {available, reason?}이다. reason은 쓸 수 없는 이유(한국어)다.
func (s *Server) privateDNSAvailable(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	name := normalizePrivateDNSName(c.svc, r.URL.Query().Get("name"))
	reason, err := s.privateDNSUnavailable(r.Context(), c.env.ID, c.svc, name)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	out := map[string]any{"available": reason == ""}
	if reason != "" {
		out["reason"] = reason
	}
	writeJSON(w, 200, out)
}

// envPrivateDNSAvailable: GET /api/environments/{eid}/private-dns/available?name=
// 서비스를 정하지 않고 환경 전체에서 이름이 비었는지 본다(GraphQL privateNetworkEndpointNameAvailable에 serviceId가 없을 때).
// 어떤 서비스도 빼지 않으므로 서비스 자기 이름도 "쓰는 중"으로 나온다. 응답은 인스턴스 경로와 같은 {available, reason?}이다.
func (s *Server) envPrivateDNSAvailable(w http.ResponseWriter, r *http.Request) {
	env, _, ok := s.envAndProject(w, r)
	if !ok {
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("name")))
	var reason string
	if name == "" {
		reason = "이름을 입력하세요"
	} else {
		var err error
		// ID와 이름이 빈 서비스: 모든 서비스와 비교한다.
		if reason, err = s.privateDNSUnavailable(r.Context(), env.ID, &store.Service{}, name); err != nil {
			s.storeErr(w, err)
			return
		}
	}
	out := map[string]any{"available": reason == ""}
	if reason != "" {
		out["reason"] = reason
	}
	writeJSON(w, 200, out)
}
