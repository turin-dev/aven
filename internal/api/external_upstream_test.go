package api

import (
	"context"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/store"
)

func TestValidateExternalUpstream(t *testing.T) {
	f := newScaleAPIFixture(t)
	f.srv.Deploy.Cfg.ExternalUpstreams = []string{"grafana-1:3000"}
	as := func(role string, token bool) context.Context {
		p := &principal{User: &store.User{ID: "u", Role: role}}
		if token {
			p.Token = &store.APIToken{Name: "ci"}
		}
		return context.WithValue(context.Background(), principalKey{}, p)
	}
	dom := func(up string, paths ...string) *store.Domain {
		return &store.Domain{Host: "dash.example.com", Upstream: up, UpstreamPaths: paths}
	}
	if err := f.srv.validateExternalUpstream(as(store.RoleOwner, false), f.in, dom("grafana-1:3000", "api/v1/")); err != nil {
		t.Fatalf("owner, allowlisted: %v", err)
	}
	if d := dom("grafana-1:3000", "api/v1/"); f.srv.validateExternalUpstream(as(store.RoleAdmin, false), f.in, d) != nil || d.UpstreamPaths[0] != "/api/v1" {
		t.Fatalf("paths are normalized: %v", d.UpstreamPaths)
	}
	for name, c := range map[string]struct {
		ctx context.Context
		d   *store.Domain
		msg string
	}{
		"member":          {as(store.RoleMember, false), dom("grafana-1:3000"), "instance admin"},
		"scoped token":    {as(store.RoleOwner, true), dom("grafana-1:3000"), "instance admin"},
		"not allowlisted": {as(store.RoleOwner, false), dom("other-1:3000"), "not allowed on this node"},
		"loopback":        {as(store.RoleOwner, false), dom("127.0.0.1:2019"), "invalid upstream"},
		"paths only":      {as(store.RoleOwner, false), dom("", "/x"), "needs upstream"},
		"bad path":        {as(store.RoleOwner, false), dom("grafana-1:3000", "/a b"), "invalid upstream path"},
	} {
		if err := f.srv.validateExternalUpstream(c.ctx, f.in, c.d); err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s: %v (want %q)", name, err, c.msg)
		}
	}
	// 이미 저장된 값과 같으면 member가 다른 설정을 바꿔도 막지 않는다.
	f.in.Domains = []store.Domain{*dom("grafana-1:3000")}
	if err := f.srv.validateExternalUpstream(as(store.RoleMember, false), f.in, dom("grafana-1:3000")); err != nil {
		t.Fatalf("unchanged upstream: %v", err)
	}
	// 원격 노드는 agent가 보고한 허용 목록을 따른다.
	f.in.Domains, f.in.NodeID = nil, f.rid
	if err := f.srv.validateExternalUpstream(as(store.RoleOwner, false), f.in, dom("grafana-1:3000")); err == nil {
		t.Fatal("remote node without an allowlist must reject")
	}
	f.st.SetNodeInfo(context.Background(), f.rid, store.NodeAddress{}, store.NodeInfo{ExternalUpstreams: []string{"grafana-1:3000"}})
	if err := f.srv.validateExternalUpstream(as(store.RoleOwner, false), f.in, dom("grafana-1:3000")); err != nil {
		t.Fatalf("remote allowlist: %v", err)
	}
}
