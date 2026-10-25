package deploy

import (
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// applyComposeRun은 compose 스택에서 가져온 서비스의 추가 런타임 설정(entrypoint/command/user/별칭/라벨/bind mount)을 컨테이너 설정에 반영한다.
// start_command를 사용자가 따로 정했다면 그쪽이 우선한다.
func applyComposeRun(s *node.RunSpec, sp *store.Spec) {
	c := sp.Compose
	if c == nil {
		return
	}
	for _, a := range c.Aliases {
		s.Aliases = append(s.Aliases, a)
	}
	for k, v := range c.Labels {
		if _, taken := s.Labels[k]; !taken {
			s.Labels[k] = v
		}
	}
	s.User, s.WorkDir = c.User, c.WorkingDir
	for _, b := range c.BindMounts {
		s.Mounts = append(s.Mounts, node.Mount{Bind: b.Source, Path: b.Target, ReadOnly: b.ReadOnly})
	}
	if s.Entrypoint != nil { // start_command가 이미 정했다
		return
	}
	switch {
	case len(c.Entrypoint) > 0:
		ep := c.Entrypoint[0]
		s.Entrypoint = &ep
		s.Cmd = append(append([]string{}, c.Entrypoint[1:]...), c.Command...)
	case len(c.Command) > 0:
		s.Cmd = append([]string{}, c.Command...)
	}
}

// composeBuild는 compose build.args(Dockerfile ARG로 선언된 것만 쓰인다)를 빌드 변수에 덮어쓰고 build target을 돌려준다.
func composeBuild(sp *store.Spec, vars map[string]string) (map[string]string, string) {
	c := sp.Compose
	if c == nil {
		return vars, ""
	}
	out := make(map[string]string, len(vars)+len(c.BuildArgs))
	for k, v := range vars {
		out[k] = v
	}
	for k, v := range c.BuildArgs {
		out[k] = v
	}
	return out, c.Target
}
