package deploy

import (
	"fmt"
	"testing"

	"github.com/turin-dev/aven/internal/store"
)

// 아래 세 함수는 routePort를 도입하기 전의 포트 선택 코드를 그대로 옮긴 것이다(동작이 같은지 비교용).

// oldSyncProxyPort는 ops.go syncProxy의 예전 코드다.
func oldSyncProxyPort(d *store.Deployment, in *store.Instance, dm store.Domain) int {
	port := d.Spec.EffectivePort(d.Env)
	if in.Port != nil {
		port = *in.Port
	}
	p := port
	if dm.TargetPort != nil && *dm.TargetPort > 0 {
		p = *dm.TargetPort
	}
	return p
}

// oldPathRoutePort는 routes.go pathRoute의 예전 코드다.
func oldPathRoutePort(d *store.Deployment, target *store.Instance, pr store.PathRule) int {
	port := d.Spec.EffectivePort(d.Env)
	if target.Port != nil {
		port = *target.Port
	}
	if pr.TargetPort != nil && *pr.TargetPort > 0 {
		port = *pr.TargetPort
	}
	return port
}

// oldWakeTargetPort는 nodestreams.go targetPort의 예전 코드다(호스트가 맞는 도메인이 dm일 때. 없으면 dm=nil).
func oldWakeTargetPort(d *store.Deployment, in *store.Instance, dm *store.Domain) int {
	port := d.Spec.EffectivePort(d.Env)
	if in.Port != nil {
		port = *in.Port
	}
	if dm != nil && dm.TargetPort != nil {
		port = *dm.TargetPort
	}
	return port
}

func TestRoutePortMatchesOldSnippets(t *testing.T) {
	ip := func(v int) *int { return &v }
	specPorts := []*int{nil, ip(0), ip(3000)}
	envs := []map[string]string{nil, {"PORT": "5000"}, {"PORT": "bogus"}, {"PORT": "70000"}}
	instPorts := []*int{nil, ip(0), ip(-1), ip(9000)}
	targets := []*int{nil, ip(0), ip(-5), ip(7000)}
	n := 0
	for _, sp := range specPorts {
		for _, env := range envs {
			for _, inp := range instPorts {
				for _, tp := range targets {
					d := &store.Deployment{Spec: store.Spec{Port: sp}, Env: env}
					in := &store.Instance{}
					in.Port = inp
					name := fmt.Sprintf("spec=%v env=%v inst=%v target=%v", deref(sp), env, deref(inp), deref(tp))

					if got, want := routePort(d, in.Port, tp, targetIfPositive), oldSyncProxyPort(d, in, store.Domain{TargetPort: tp}); got != want {
						t.Errorf("syncProxy %s: routePort=%d old=%d", name, got, want)
					}
					if got, want := routePort(d, in.Port, tp, targetIfPositive), oldPathRoutePort(d, in, store.PathRule{TargetPort: tp}); got != want {
						t.Errorf("pathRoute %s: routePort=%d old=%d", name, got, want)
					}
					if got, want := routePort(d, in.Port, tp, targetIfSet), oldWakeTargetPort(d, in, &store.Domain{TargetPort: tp}); got != want {
						t.Errorf("targetPort (matched host) %s: routePort=%d old=%d", name, got, want)
					}
					if got, want := routePort(d, in.Port, nil, targetIfSet), oldWakeTargetPort(d, in, nil); got != want {
						t.Errorf("targetPort (no host) %s: routePort=%d old=%d", name, got, want)
					}
					n++
				}
			}
		}
	}
	if n == 0 {
		t.Fatal("no cases")
	}
	// 차이가 실제로 있는 경우를 고정한다: target_port=0은 syncProxy/pathRoute에서는 무시되고 wake에서는 그대로 쓰인다.
	d := &store.Deployment{Env: map[string]string{"PORT": "5000"}}
	if p := routePort(d, nil, ip(0), targetIfPositive); p != 5000 {
		t.Errorf("targetIfPositive with target 0 = %d, want 5000", p)
	}
	if p := routePort(d, nil, ip(0), targetIfSet); p != 0 {
		t.Errorf("targetIfSet with target 0 = %d, want 0", p)
	}
}

func deref(p *int) any {
	if p == nil {
		return "nil"
	}
	return *p
}
