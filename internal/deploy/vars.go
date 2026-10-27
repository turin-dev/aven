package deploy

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/turin-dev/aven/internal/compat"
	"github.com/turin-dev/aven/internal/store"
	"github.com/turin-dev/aven/internal/vars"
)

// Target은 배포 대상 인스턴스와 그 맥락이다.
type Target struct {
	Project  *store.Project
	Env      *store.Environment
	Service  *store.Service
	Instance *store.Instance
}

func (m *Manager) Load(ctx context.Context, instanceID string) (*Target, error) {
	in, err := m.Store.GetInstance(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	svc, err := m.Store.GetService(ctx, in.ServiceID)
	if err != nil {
		return nil, err
	}
	env, err := m.Store.GetEnvironment(ctx, in.EnvironmentID)
	if err != nil {
		return nil, err
	}
	p, err := m.Store.GetProject(ctx, svc.ProjectID)
	if err != nil {
		return nil, err
	}
	return &Target{Project: p, Env: env, Service: svc, Instance: in}, nil
}

// PrivateDomain은 환경 네트워크 안의 서비스 주소다.
func PrivateDomain(service, env string) string { return compat.PrivateHost(service, env) }

// LegacyPrivateDomain은 예전 설치가 쓰던 .keel.internal 주소다.
func LegacyPrivateDomain(service, env string) string {
	return compat.LegacyPrivateHost(service, env)
}

// PrivateDNSName은 사설 네트워크의 대표 이름이다: 설정한 private_dns_name, 없으면 서비스 이름.
func PrivateDNSName(service, dns string) string {
	if dns != "" {
		return dns
	}
	return service
}

// PrivateAliases는 환경 네트워크에서 서비스 컨테이너에 붙는 이름이다.
// <svc>.<env>.aven.internal과 예전 <svc>.<env>.keel.internal을 함께 붙여 기존 호스트 이름이 계속 해석되게 한다.
func PrivateAliases(service, env, dns string) []string {
	out := []string{service, PrivateDomain(service, env), LegacyPrivateDomain(service, env)}
	if dns != "" && dns != service {
		out = append(out, dns, PrivateDomain(dns, env), LegacyPrivateDomain(dns, env))
	}
	return out
}

// EnvNetwork는 환경별 docker 네트워크 이름이다. 예전 keel-<id> 네트워크가 있으면
// Docker.ResolveNetwork가 그 이름을 그대로 쓴다.
func EnvNetwork(envID string) string { return compat.Prefix + "-" + envID }

// LegacyEnvNetwork는 예전 설치의 환경 네트워크 이름이다.
func LegacyEnvNetwork(envID string) string { return compat.LegacyPrefix + "-" + envID }

// TCPProxyHostFor는 인스턴스의 TCP proxy 호스트다(TCPProxyEndpoint, tcpproxy.go). 원격 노드에서 도는 인스턴스는 그 노드의
// tcp_hostname, 없으면 공인 주소(IPv6 우선)다(KEEL_TCP_PROXY_DOMAIN은 로컬 노드를 가리키는 이름이므로 쓰지 않는다).
// 공인 주소가 없는 노드(터널 모드 등)는 "".
func (m *Manager) TCPProxyHostFor(ctx context.Context, in *store.Instance) string {
	h, _ := m.TCPProxyEndpoint(ctx, in)
	return h
}

// TCPProxyHost는 TCP proxy로 외부에서 접속할 호스트 이름이다.
func (m *Manager) TCPProxyHost() string {
	switch {
	case m.Cfg.TCPProxyDomain != "":
		return m.Cfg.TCPProxyDomain
	case m.Cfg.BaseDomain != "":
		return m.Cfg.BaseDomain
	case strings.Contains(m.Cfg.NodeHostname, "."):
		return m.Cfg.NodeHostname
	case m.Cfg.PublicIPv6 != "":
		return m.Cfg.PublicIPv6
	default:
		return m.Cfg.PublicIPv4
	}
}

// injected는 자동 주입 변수다. 사용자 변수가 같은 이름이면 사용자 값이 이긴다.
func (m *Manager) injected(p *store.Project, env *store.Environment, svc *store.Service, sp *store.Spec, dep *store.Deployment, volumes []*store.Volume, tcpHost string) map[string]string {
	v := map[string]string{
		"AVEN_SERVICE_NAME":     svc.Name,
		"AVEN_SERVICE_ID":       svc.ID,
		"AVEN_ENVIRONMENT_NAME": env.Name,
		"AVEN_ENVIRONMENT_ID":   env.ID,
		"AVEN_PROJECT_NAME":     p.Name,
		"AVEN_PROJECT_ID":       p.ID,
		"AVEN_PRIVATE_DOMAIN":   PrivateDomain(PrivateDNSName(svc.Name, sp.PrivateDNSName), env.Name),
		"PORT":                  "8080",
	}
	if sp.Port != nil && *sp.Port > 0 {
		v["PORT"] = strconv.Itoa(*sp.Port)
	}
	if len(sp.Domains) > 0 {
		v["AVEN_PUBLIC_DOMAIN"] = sp.Domains[0].Host
	}
	if sp.TCPProxy != nil && sp.TCPProxy.PublicPort > 0 {
		v["AVEN_TCP_PROXY_DOMAIN"] = tcpHost
		v["AVEN_TCP_PROXY_PORT"] = strconv.Itoa(sp.TCPProxy.PublicPort)
	}
	if len(volumes) > 0 {
		v["AVEN_VOLUME_MOUNT_PATH"] = volumes[0].MountPath
	}
	if dep != nil {
		v["AVEN_DEPLOYMENT_ID"] = dep.ID
		if dep.CommitSHA != nil {
			v["AVEN_GIT_COMMIT_SHA"] = *dep.CommitSHA
		}
		if dep.Branch != nil {
			v["AVEN_GIT_BRANCH"] = *dep.Branch
		}
	}
	return v
}

// Override는 해석할 때 한 인스턴스의 사용자 변수/설정/배포를 바꿔 끼운다(진행 중인 배포의 스냅샷).
type Override struct {
	InstanceID string
	Vars       map[string]string
	Spec       *store.Spec
	Deployment *store.Deployment
}

// Scope는 환경 전체의 변수 범위를 만든다. names는 instanceID → 서비스 이름.
func (m *Manager) Scope(ctx context.Context, envID string, ov *Override) (*vars.Scope, map[string]string, error) {
	env, err := m.Store.GetEnvironment(ctx, envID)
	if err != nil {
		return nil, nil, err
	}
	p, err := m.Store.GetProject(ctx, env.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	shared, err := m.Store.GetSharedVars(ctx, envID)
	if err != nil {
		return nil, nil, err
	}
	insts, err := m.Store.ListInstancesByEnv(ctx, envID)
	if err != nil {
		return nil, nil, err
	}
	svcs, err := m.Store.ListServices(ctx, env.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	byID := map[string]*store.Service{}
	for _, s := range svcs {
		byID[s.ID] = s
	}
	vols, _ := m.Store.ListVolumesByEnv(ctx, envID)
	// 집중형 PR 환경에서 건너뛴 서비스는 base 환경에서 실행 중인 서비스를 그대로 쓰므로,
	// 그 서비스를 가리키는 참조는 base 환경에서 완전히 해석한 값으로 바꾼다.
	var baseScope *vars.Scope
	baseLoaded := false
	loadBase := func() *vars.Scope {
		if !baseLoaded {
			baseLoaded = true
			if base, err := m.Store.GetEnvironment(ctx, *env.BaseEnvironmentID); err == nil &&
				base.ProjectID == env.ProjectID && base.Kind != store.EnvPreview {
				baseScope, _, _ = m.Scope(ctx, base.ID, nil)
			}
		}
		return baseScope
	}
	scope := &vars.Scope{Shared: shared, Services: map[string]map[string]string{}}
	names := map[string]string{}
	for _, in := range insts {
		svc := byID[in.ServiceID]
		if svc == nil {
			continue
		}
		names[in.ID] = svc.Name
		sp, user := &in.Spec, in.Variables
		var dep *store.Deployment
		if ov != nil && ov.InstanceID == in.ID {
			if ov.Spec != nil {
				sp = ov.Spec
			}
			if ov.Vars != nil {
				user = ov.Vars
			}
			dep = ov.Deployment
		} else if in.CurrentDeploymentID != nil {
			dep, _ = m.Store.GetDeployment(ctx, *in.CurrentDeploymentID)
		}
		var myVols []*store.Volume
		for _, v := range vols {
			if v.InstanceID != nil && *v.InstanceID == in.ID {
				myVols = append(myVols, v)
			}
		}
		// 도메인/TCP proxy는 배포 스냅샷이 아니라 현재 인스턴스 설정을 쓴다.
		spCopy := *sp
		spCopy.Domains, spCopy.TCPProxy = in.Domains, in.TCPProxy
		// port를 정하지 않은 서비스는 live 배포가 실제로 쓴 포트(EXPOSE 감지 등)를 PORT로 보여준다.
		if spCopy.Port == nil && dep != nil && dep.Spec.Port != nil && (ov == nil || ov.InstanceID != in.ID) {
			spCopy.Port = dep.Spec.Port
		}
		// 사설 이름은 다음 배포 때 alias가 붙으므로, live 배포가 있으면 그 배포가 실제로 붙인 이름을 쓴다
		// (다른 서비스의 ${{svc.KEEL_PRIVATE_DOMAIN}}이 아직 없는 이름을 가리키지 않게). 진행 중인 배포(ov)는 자기 스냅샷을 쓴다.
		if dep != nil && (ov == nil || ov.InstanceID != in.ID) {
			spCopy.PrivateDNSName = dep.Spec.PrivateDNSName
		}
		merged := m.injected(p, env, svc, &spCopy, dep, myVols, m.TCPProxyHostFor(ctx, in))
		for k, v := range user {
			merged[k] = v
		}
		// 사용자가 KEEL_*를 직접 넣었으면 그 값을 두고, 아니면 AVEN_*를 KEEL_*로도 복사한다.
		compat.InjectedAlias(merged)
		if env.Kind == store.EnvPreview && env.BaseEnvironmentID != nil && (ov == nil || ov.InstanceID != in.ID) {
			if latest, err := m.Store.LatestDeployment(ctx, in.ID); err == nil && latest.Trigger == "github" &&
				latest.Status == store.DepSkipped && latest.Error == nil {
				if bs := loadBase(); bs != nil && bs.Services[svc.Name] != nil {
					if res, err := vars.Resolve(bs, svc.Name); err == nil {
						merged = res.Vars
					}
				}
			}
		}
		scope.Services[svc.Name] = merged
	}
	return scope, names, nil
}

// Resolve는 인스턴스의 최종 환경변수를 계산한다. ov가 있으면 그 스냅샷을 쓴다.
func (m *Manager) Resolve(ctx context.Context, in *store.Instance, ov *Override) (*vars.Result, error) {
	scope, names, err := m.Scope(ctx, in.EnvironmentID, ov)
	if err != nil {
		return nil, err
	}
	name, ok := names[in.ID]
	if !ok {
		return nil, fmt.Errorf("instance %s not found in its environment", in.ID)
	}
	return vars.Resolve(scope, name)
}
