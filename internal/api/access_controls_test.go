package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/store"
)

// ---- Enterprise access controls: 접근 그룹, Environment RBAC, guardrail ----

type acFixture struct {
	*ghFixture
	h                                        http.Handler
	owner, wsAdmin, projAdmin, member, other *store.User
	ownerC, wsAdminC, projAdminC, memberC    string
	otherC                                   string
}

func newACFixture(t *testing.T) *acFixture {
	t.Helper()
	gf, h := authServer(t)
	f := &acFixture{ghFixture: gf, h: h}
	ctx := context.Background()
	// 도메인을 바꾸면 proxy를 동기화한다: 가짜 Caddy admin API로 받는다.
	caddy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(caddy.Close)
	port, _ := strconv.Atoi(caddy.URL[strings.LastIndex(caddy.URL, ":")+1:])
	f.mgr.Proxy, f.mgr.HTTPLogs = proxy.New(proxy.Config{AdminPort: port}), deploy.NewHTTPLogHub(10)
	f.owner = testUser(t, f.st, "owner@example.com", store.RoleOwner)
	f.wsAdmin = testUser(t, f.st, "wsadmin@example.com", store.RoleAdmin)
	f.projAdmin = testUser(t, f.st, "padmin@example.com", store.RoleMember)
	f.member = testUser(t, f.st, "member@example.com", store.RoleMember)
	f.other = testUser(t, f.st, "other@example.com", store.RoleMember) // 프로젝트 비멤버
	if err := f.st.SetMember(ctx, f.p.ID, f.projAdmin.ID, store.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetMember(ctx, f.p.ID, f.member.ID, store.RoleMember); err != nil {
		t.Fatal(err)
	}
	f.ownerC, f.wsAdminC = testSessionCookie(t, f.srv, f.owner), testSessionCookie(t, f.srv, f.wsAdmin)
	f.projAdminC, f.memberC, f.otherC = testSessionCookie(t, f.srv, f.projAdmin), testSessionCookie(t, f.srv, f.member), testSessionCookie(t, f.srv, f.other)
	return f
}

func (f *acFixture) do(method, path, body, cookie string) (int, string) {
	w := req(f.h, method, path, body, withCookie(cookie))
	return w.Code, w.Body.String()
}

func (f *acFixture) expect(t *testing.T, want int, method, path, body, cookie string) string {
	t.Helper()
	code, out := f.do(method, path, body, cookie)
	if code != want {
		t.Errorf("%s %s: %d %s, want %d", method, path, code, out, want)
	}
	return out
}

func (f *acFixture) token(t *testing.T, u *store.User, scope string, eid *string, role, secret string) {
	t.Helper()
	tok := &store.APIToken{UserID: u.ID, Name: secret, Prefix: secret[:8], Scope: scope, Role: role}
	if scope != store.ScopeWorkspace {
		tok.ProjectID = &f.p.ID
	}
	tok.EnvironmentID = eid
	if err := f.st.CreateAPIToken(context.Background(), tok, hashSecret(secret)); err != nil {
		t.Fatal(err)
	}
}

func TestAccessGroupsGrantProjectAccess(t *testing.T) {
	f := newACFixture(t)
	pp := "/api/projects/" + f.p.ID
	canvas := "/api/environments/" + f.prod.ID + "/canvas"

	// 작업 공간 admin만 그룹·정책을 관리한다(그룹 admin이나 API 토큰도 안 된다).
	for _, c := range []string{f.memberC, f.projAdminC, f.otherC} {
		f.expect(t, 403, "GET", "/api/access-groups", "", c)
		f.expect(t, 403, "POST", "/api/access-groups", `{"name":"x","role":"viewer"}`, c)
		f.expect(t, 403, "GET", "/api/access-controls", "", c)
		f.expect(t, 403, "PATCH", "/api/access-controls", `{"access_groups_enabled":true}`, c)
		f.expect(t, 403, "PUT", pp+"/access-groups", `{"group_ids":[]}`, c)
	}
	f.token(t, f.owner, store.ScopeWorkspace, nil, store.RoleAdmin, "keel_owneradmintoken")
	if w := req(f.h, "PATCH", "/api/access-controls", `{"access_groups_enabled":true}`, withBearer("keel_owneradmintoken")); w.Code != 403 {
		t.Errorf("API token changed access controls: %d", w.Code)
	}
	if w := req(f.h, "POST", "/api/access-groups", `{"name":"x","role":"viewer"}`, withBearer("keel_owneradmintoken")); w.Code != 403 {
		t.Errorf("API token created a group: %d", w.Code)
	}

	// 검증
	f.expect(t, 400, "POST", "/api/access-groups", `{"name":"","role":"viewer"}`, f.ownerC)
	f.expect(t, 400, "POST", "/api/access-groups", `{"name":"g","role":"owner"}`, f.ownerC)
	f.expect(t, 400, "POST", "/api/access-groups", `{"name":"g","role":"viewer","user_ids":["usr_nope"]}`, f.ownerC)
	f.expect(t, 400, "POST", "/api/access-groups", `{"name":"g","role":"viewer","project_ids":["prj_nope"]}`, f.ownerC)

	// 그룹은 꺼진 상태에서 만들 수 있고, 켜기 전에는 접근을 바꾸지 않는다.
	out := f.expect(t, 201, "POST", "/api/access-groups", fmt.Sprintf(`{"name":"Readers","role":"viewer","user_ids":[%q],"project_ids":[%q]}`, f.other.ID, f.p.ID), f.wsAdminC)
	var g store.AccessGroup
	decodeInto(t, out, &g)
	f.expect(t, 409, "POST", "/api/access-groups", `{"name":"readers","role":"admin"}`, f.ownerC)
	f.expect(t, 404, "GET", pp, "", f.otherC)
	f.expect(t, 404, "GET", canvas, "", f.otherC)

	f.expect(t, 200, "PATCH", "/api/access-controls", `{"access_groups_enabled":true}`, f.ownerC)
	f.expect(t, 200, "GET", pp, "", f.otherC)
	f.expect(t, 200, "GET", canvas, "", f.otherC)
	if ps := decodeBody[[]store.Project](t, req(f.h, "GET", "/api/projects", "", withCookie(f.otherC))); len(ps) != 1 || ps[0].ID != f.p.ID {
		t.Errorf("project list via group: %+v", ps)
	}
	me := decodeBody[struct {
		Projects map[string]string `json:"projects"`
	}](t, req(f.h, "GET", "/api/auth/me", "", withCookie(f.otherC)))
	if me.Projects[f.p.ID] != "viewer" {
		t.Errorf("me.projects: %v", me.Projects)
	}
	// viewer 그룹은 viewer 권한만
	f.expect(t, 403, "POST", pp+"/services", `{"source_type":"image"}`, f.otherC)
	f.expect(t, 403, "GET", "/api/services/"+f.web.ID+"/instances/"+f.prod.ID+"/variables/raw", "", f.otherC)

	// 직접 부여와 그룹이 겹치면 높은 역할(그룹이 직접 부여를 낮추지 않는다).
	f.expect(t, 200, "PUT", "/api/access-groups/"+g.ID, fmt.Sprintf(`{"name":"Readers","role":"viewer","user_ids":[%q,%q],"project_ids":[%q]}`, f.other.ID, f.projAdmin.ID, f.p.ID), f.ownerC)
	f.expect(t, 200, "PATCH", pp, `{"description":"direct admin in a viewer group"}`, f.projAdminC)

	// 그룹 admin 역할은 그 프로젝트의 admin이지만 작업 공간 admin은 아니다.
	f.expect(t, 200, "PUT", "/api/access-groups/"+g.ID, fmt.Sprintf(`{"name":"Readers","role":"admin","user_ids":[%q],"project_ids":[%q]}`, f.other.ID, f.p.ID), f.ownerC)
	f.expect(t, 200, "PATCH", pp, `{"description":"via group admin"}`, f.otherC)
	f.expect(t, 403, "GET", "/api/access-groups", "", f.otherC)
	f.expect(t, 403, "GET", "/api/audit", "", f.otherC)

	// 프로젝트 화면: 그룹과 그룹으로 접근하는 사람
	pg := decodeBody[struct {
		Enabled bool                 `json:"enabled"`
		Groups  []projectAccessGroup `json:"groups"`
		Grants  []store.GroupGrant   `json:"group_grants"`
	}](t, req(f.h, "GET", pp+"/access-groups", "", withCookie(f.memberC)))
	if !pg.Enabled || len(pg.Groups) != 1 || pg.Groups[0].Name != "Readers" || len(pg.Grants) != 1 || pg.Grants[0].UserID != f.other.ID || pg.Grants[0].Role != "admin" {
		t.Errorf("project access groups: %+v", pg)
	}
	f.expect(t, 404, "GET", pp+"/access-groups", "", testSessionCookie(t, f.srv, testUser(t, f.st, "nobody@example.com", store.RoleMember)))

	// 프로젝트를 그룹에서 빼면(프로젝트 설정) 접근이 사라진다.
	f.expect(t, 400, "PUT", pp+"/access-groups", `{"group_ids":["ag_missing"]}`, f.ownerC)
	f.expect(t, 200, "PUT", pp+"/access-groups", `{"group_ids":[]}`, f.ownerC)
	f.expect(t, 404, "GET", pp, "", f.otherC)
	f.expect(t, 200, "PUT", pp+"/access-groups", fmt.Sprintf(`{"group_ids":[%q]}`, g.ID), f.ownerC)
	f.expect(t, 200, "GET", pp, "", f.otherC)

	// 끄면 그룹 설정은 남고 접근은 직접 부여로 돌아간다.
	f.expect(t, 200, "PATCH", "/api/access-controls", `{"access_groups_enabled":false}`, f.ownerC)
	f.expect(t, 404, "GET", pp, "", f.otherC)
	if gs := decodeBody[[]store.AccessGroup](t, req(f.h, "GET", "/api/access-groups", "", withCookie(f.ownerC))); len(gs) != 1 || len(gs[0].UserIDs) != 1 {
		t.Errorf("groups after disabling: %+v", gs)
	}
	f.expect(t, 200, "PATCH", "/api/access-controls", `{"access_groups_enabled":true}`, f.ownerC)
	f.expect(t, 200, "GET", pp, "", f.otherC)

	// 삭제하면 그룹이 준 접근이 사라진다. 직접 부여는 그대로.
	f.expect(t, 204, "DELETE", "/api/access-groups/"+g.ID, "", f.ownerC)
	f.expect(t, 404, "DELETE", "/api/access-groups/"+g.ID, "", f.ownerC)
	f.expect(t, 404, "GET", pp, "", f.otherC)
	f.expect(t, 200, "GET", pp, "", f.memberC)

	// 감사 기록
	audit := f.expect(t, 200, "GET", "/api/audit", "", f.ownerC)
	for _, kind := range []string{"access_group.created", "access_group.updated", "access_group.members_changed", "access_group.projects_changed", "access_group.deleted", "access_groups.enforcement"} {
		if !strings.Contains(audit, kind) {
			t.Errorf("audit log has no %s", kind)
		}
	}
}

func decodeInto(t *testing.T, body string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
}

func TestEnvironmentRBAC(t *testing.T) {
	f := newACFixture(t)
	ctx := context.Background()
	pp := "/api/projects/" + f.p.ID
	prod := f.prod.ID
	canvas := "/api/environments/" + prod + "/canvas"
	inst := "/api/services/" + f.web.ID + "/instances/" + prod

	// member는 제한을 켤 수 없다(아무것도 바뀌지 않는다).
	f.expect(t, 403, "PATCH", "/api/environments/"+prod, `{"name":"renamed","restricted":true}`, f.memberC)
	if e, _ := f.st.GetEnvironment(ctx, prod); e.Restricted || e.Name != "production" {
		t.Fatalf("member partially updated the environment: %+v", e)
	}
	f.expect(t, 200, "PATCH", "/api/environments/"+prod, `{"restricted":true}`, f.projAdminC)

	// 프로젝트 member: 환경이 있다는 것은 보이지만 리소스는 403.
	detail := decodeBody[struct {
		Environments []store.Environment `json:"environments"`
	}](t, req(f.h, "GET", pp, "", withCookie(f.memberC)))
	if len(detail.Environments) != 1 || !detail.Environments[0].Restricted {
		t.Errorf("project detail for member: %+v", detail.Environments)
	}
	for _, rt := range [][2]string{
		{"GET", canvas}, {"GET", "/api/environments/" + prod + "/logs"}, {"GET", "/api/environments/" + prod + "/metrics"},
		{"GET", "/api/environments/" + prod + "/shared-variables"}, {"GET", inst}, {"GET", inst + "/variables/resolved"},
		{"GET", inst + "/variables/raw"}, {"PATCH", inst}, {"POST", inst + "/deploy"}, {"POST", inst + "/restart"},
		{"GET", inst + "/deployments"}, {"PATCH", "/api/environments/" + prod},
	} {
		code, body := f.do(rt[0], rt[1], `{}`, f.memberC)
		if code != 403 || !strings.Contains(body, "제한된 환경") {
			t.Errorf("member %s %s on restricted env: %d %s", rt[0], rt[1], code, body)
		}
	}
	// 비멤버에게는 여전히 없는 것처럼(404).
	f.expect(t, 404, "GET", canvas, "", f.otherC)
	// 배포 기록 id로 우회할 수 없다.
	webIn, _ := f.st.GetInstanceFor(ctx, f.web.ID, prod)
	dep := &store.Deployment{ServiceID: f.web.ID, EnvironmentID: prod, InstanceID: webIn.ID, Trigger: "manual"}
	if err := f.st.CreateDeployment(ctx, dep); err != nil {
		t.Fatal(err)
	}
	f.expect(t, 403, "GET", "/api/deployments/"+dep.ID+"/deploy-logs", "", f.memberC)

	// 프로젝트 admin, 작업 공간 admin/owner는 접근한다.
	for _, c := range []string{f.projAdminC, f.wsAdminC, f.ownerC} {
		f.expect(t, 200, "GET", canvas, "", c)
	}

	// 다른(제한 없는) 환경은 member가 평소처럼 쓴다. 제한된 환경에서 복사·동기화해 비밀을 빼낼 수 없다.
	stg := decodeBody[store.Environment](t, req(f.h, "POST", pp+"/environments", `{"name":"staging"}`, withCookie(f.memberC)))
	if stg.ID == "" || stg.Restricted {
		t.Fatalf("staging: %+v", stg)
	}
	f.expect(t, 200, "GET", "/api/environments/"+stg.ID+"/canvas", "", f.memberC)
	f.expect(t, 200, "PATCH", "/api/environments/"+stg.ID, `{"name":"stage"}`, f.memberC)
	f.expect(t, 403, "PATCH", "/api/environments/"+stg.ID, `{"restricted":true}`, f.memberC)
	f.expect(t, 403, "POST", pp+"/environments", fmt.Sprintf(`{"name":"leak","copy_from":%q}`, prod), f.memberC)
	f.expect(t, 403, "GET", "/api/environments/"+stg.ID+"/sync-preview?from="+prod, "", f.memberC)
	f.expect(t, 403, "POST", "/api/environments/"+stg.ID+"/sync", fmt.Sprintf(`{"from":%q}`, prod), f.memberC)
	f.expect(t, 403, "POST", pp+"/services", fmt.Sprintf(`{"source_type":"database","database_kind":"redis","environment_id":%q}`, prod), f.memberC)
	f.expect(t, 200, "GET", "/api/environments/"+stg.ID+"/sync-preview?from="+prod, "", f.projAdminC)

	// admin이 제한된 환경을 복사하면 새 환경도 제한된다.
	fork := decodeBody[store.Environment](t, req(f.h, "POST", pp+"/environments", fmt.Sprintf(`{"name":"prod-copy","copy_from":%q}`, prod), withCookie(f.projAdminC)))
	if !fork.Restricted {
		t.Errorf("fork of a restricted env is not restricted: %+v", fork)
	}

	// 토큰: 권한은 만든 사용자 기준. admin의 environment 토큰(역할 member)은 제한된 환경에서 쓸 수 있고, member의 토큰은 못 쓴다.
	f.token(t, f.projAdmin, store.ScopeEnvironment, &prod, store.RoleMember, "keel_adminenvtoken")
	f.token(t, f.member, store.ScopeEnvironment, &prod, store.RoleMember, "keel_memberenvtoken")
	f.token(t, f.member, store.ScopeProject, nil, store.RoleMember, "keel_memberprojtoken")
	if w := req(f.h, "GET", canvas, "", withBearer("keel_adminenvtoken")); w.Code != 200 {
		t.Errorf("project admin's env token on restricted env: %d %s", w.Code, w.Body)
	}
	if w := req(f.h, "PATCH", "/api/environments/"+prod, `{"restricted":false}`, withBearer("keel_adminenvtoken")); w.Code != 403 {
		t.Errorf("env token (member role) toggled restriction: %d", w.Code)
	}
	for _, tok := range []string{"keel_memberenvtoken", "keel_memberprojtoken"} {
		if w := req(f.h, "GET", canvas, "", withBearer(tok)); w.Code != 403 {
			t.Errorf("member's %s on restricted env: %d", tok, w.Code)
		}
	}

	// 접근 그룹의 admin 역할은 제한된 환경에 접근한다(그룹이 켜져 있을 때만).
	g := &store.AccessGroup{Name: "Ops", Role: store.RoleAdmin, UserIDs: []string{f.other.ID}, ProjectIDs: []string{f.p.ID}}
	if err := f.st.SaveAccessGroup(ctx, g); err != nil {
		t.Fatal(err)
	}
	f.expect(t, 404, "GET", canvas, "", f.otherC)
	f.expect(t, 200, "PATCH", "/api/access-controls", `{"access_groups_enabled":true}`, f.ownerC)
	f.expect(t, 200, "GET", canvas, "", f.otherC)

	// git push 자동 배포는 제한된 환경에도 그대로 된다.
	sha := strings.Repeat("a", 40)
	f.hook("push", pushPayload("main", sha, "acme/app"))
	if d, _ := f.st.LatestDeployment(ctx, webIn.ID); d == nil || d.CommitSHA == nil || *d.CommitSHA != sha {
		t.Errorf("push to a restricted environment was not deployed: %+v", d)
	}

	// 풀면 member가 다시 접근한다.
	f.expect(t, 200, "PATCH", "/api/environments/"+prod, `{"restricted":false}`, f.projAdminC)
	f.expect(t, 200, "GET", canvas, "", f.memberC)
	if !strings.Contains(f.expect(t, 200, "GET", "/api/audit", "", f.ownerC), "environment.restricted") {
		t.Error("restriction change not audited")
	}
}

func TestGuardrailSettingsValidation(t *testing.T) {
	f := newACFixture(t)
	f.expect(t, 400, "PATCH", "/api/access-controls", `{"restrict_deployment_sources":true}`, f.ownerC)
	f.expect(t, 400, "PATCH", "/api/access-controls", `{"approved_github_orgs":["-bad-"]}`, f.ownerC)
	f.expect(t, 400, "PATCH", "/api/access-controls", `{"approved_github_orgs":["a/b"]}`, f.ownerC)
	out := f.expect(t, 200, "PATCH", "/api/access-controls", `{"restrict_deployment_sources":true,"approved_github_orgs":["Acme"," acme ","platform"]}`, f.wsAdminC)
	var p store.AccessControlSettings
	decodeInto(t, out, &p)
	if !p.RestrictDeploymentSources || strings.Join(p.ApprovedGitHubOrgs, ",") != "acme,platform" {
		t.Errorf("settings: %+v", p)
	}
	// 켜져 있는 동안 마지막 조직은 지울 수 없다.
	f.expect(t, 400, "PATCH", "/api/access-controls", `{"approved_github_orgs":[]}`, f.ownerC)
	f.expect(t, 200, "PATCH", "/api/access-controls", `{"restrict_deployment_sources":false,"approved_github_orgs":[]}`, f.ownerC)

	// 정책 요약은 모든 사용자가 읽는다(조직 목록은 admin만).
	f.expect(t, 200, "PATCH", "/api/access-controls", `{"restrict_tcp_proxy":true}`, f.ownerC)
	sum := f.expect(t, 200, "GET", "/api/access-controls/policies", "", f.memberC)
	if !strings.Contains(sum, `"restrict_tcp_proxy":true`) || !strings.Contains(sum, `"workspace_admin":false`) || strings.Contains(sum, "approved_github_orgs") {
		t.Errorf("policy summary: %s", sum)
	}
	audit := f.expect(t, 200, "GET", "/api/audit", "", f.ownerC)
	for _, kind := range []string{"guardrail.deployment_sources", "guardrail.approved_sources", "guardrail.tcp_proxy"} {
		if !strings.Contains(audit, kind) {
			t.Errorf("audit log has no %s", kind)
		}
	}
}

func TestGuardrailPublicNetworking(t *testing.T) {
	f := newACFixture(t)
	ctx := context.Background()
	pp := "/api/projects/" + f.p.ID
	inst := "/api/services/" + f.web.ID + "/instances/" + f.prod.ID
	dbInst := "/api/services/" + f.db.ID + "/instances/" + f.prod.ID

	// 꺼져 있으면 member도 만든다.
	f.expect(t, 201, "POST", inst+"/domains", `{}`, f.memberC)
	f.expect(t, 200, "PATCH", "/api/access-controls", `{"restrict_generated_domains":true,"restrict_tcp_proxy":true}`, f.ownerC)

	// 프로젝트 admin(그룹 admin 포함)이라도 작업 공간 admin이 아니면 막는다. custom domain은 된다.
	for _, c := range []string{f.memberC, f.projAdminC} {
		f.expect(t, 403, "POST", inst+"/domains", `{}`, c)
		f.expect(t, 403, "POST", inst+"/domains", `{"host":"sneaky.example.com"}`, c)
		f.expect(t, 403, "POST", dbInst+"/tcp-proxy", `{"container_port":5432}`, c)
	}
	f.expect(t, 201, "POST", inst+"/domains", `{"host":"shop.acme.dev"}`, f.memberC)
	in, _ := f.st.GetInstanceFor(ctx, f.web.ID, f.prod.ID)
	doms, _ := jsonMarshal(append(in.Domains, store.Domain{Host: "new-web.example.com", Kind: "generated"}))
	f.expect(t, 403, "PATCH", inst, `{"domains":`+doms+`}`, f.memberC)
	// 기존 도메인을 유지하는 PATCH는 된다.
	keep, _ := jsonMarshal(in.Domains)
	f.expect(t, 200, "PATCH", inst, `{"domains":`+keep+`}`, f.memberC)
	// API 토큰(프로젝트 범위)은 작업 공간 admin이 아니다.
	f.token(t, f.owner, store.ScopeProject, nil, store.RoleAdmin, "keel_ownerprojtoken")
	if w := req(f.h, "POST", inst+"/domains", `{}`, withBearer("keel_ownerprojtoken")); w.Code != 403 {
		t.Errorf("project token generated a domain: %d", w.Code)
	}

	// 작업 공간 admin은 계속 만든다.
	f.expect(t, 201, "POST", inst+"/domains", `{}`, f.wsAdminC)
	if code, body := f.do("POST", dbInst+"/tcp-proxy", `{"container_port":5432}`, f.ownerC); code == 403 {
		t.Errorf("owner TCP proxy: %d %s", code, body)
	}

	// 자동 생성은 모두에게 멈춘다: 새 환경은 생성 도메인을 복사하지 않고, 템플릿 서비스는 도메인·TCP 프록시가 없다.
	stg := decodeBody[store.Environment](t, req(f.h, "POST", pp+"/environments", fmt.Sprintf(`{"name":"staging","copy_from":%q}`, f.prod.ID), withCookie(f.ownerC)))
	si, err := f.st.GetInstanceFor(ctx, f.web.ID, stg.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range si.Domains {
		if d.Kind == "generated" {
			t.Errorf("new environment got a generated domain: %+v", d)
		}
	}
	if di, _ := f.st.GetInstanceFor(ctx, f.db.ID, stg.ID); di.TCPProxy != nil {
		t.Errorf("new environment got a TCP proxy: %+v", di.TCPProxy)
	}
	out := f.expect(t, 201, "POST", pp+"/services", `{"source_type":"database","database_kind":"postgres"}`, f.ownerC)
	var svc store.Service
	decodeInto(t, out, &svc)
	if pi, _ := f.st.GetInstanceFor(ctx, svc.ID, f.prod.ID); pi == nil || pi.TCPProxy != nil || len(pi.Domains) != 0 {
		t.Errorf("new database got public networking: %+v", pi)
	}
}

func TestGuardrailDeploymentSources(t *testing.T) {
	f := newACFixture(t)
	ctx := context.Background()
	pp := "/api/projects/" + f.p.ID
	f.expect(t, 200, "PATCH", "/api/access-controls", `{"restrict_deployment_sources":true,"approved_github_orgs":["acme"]}`, f.ownerC)

	// admin을 포함해 모두에게 적용된다.
	for _, c := range []string{f.ownerC, f.memberC} {
		f.expect(t, 403, "POST", pp+"/services", `{"source_type":"image","image":"nginx:alpine"}`, c)
		f.expect(t, 403, "POST", pp+"/services", `{"source_type":"repo","repo_url":"https://github.com/someone/app"}`, c)
		f.expect(t, 403, "POST", pp+"/services", `{"source_type":"function"}`, c)
	}
	f.expect(t, 201, "POST", pp+"/services", `{"source_type":"repo","repo_url":"https://github.com/acme/new-app"}`, f.memberC)
	f.expect(t, 201, "POST", pp+"/services", `{"source_type":"database","database_kind":"redis"}`, f.memberC)
	f.expect(t, 201, "POST", pp+"/services", `{"source_type":"image"}`, f.memberC) // 소스 없는 빈 서비스

	// 소스 변경: 승인되지 않은 저장소·DB 이미지 교체는 막고, 연결 해제는 허용한다.
	inst := "/api/services/" + f.web.ID + "/instances/" + f.prod.ID
	f.expect(t, 403, "PATCH", inst, `{"repo_url":"https://github.com/evil/app"}`, f.ownerC)
	f.expect(t, 200, "PATCH", inst, `{"repo_url":"https://github.com/acme/renamed"}`, f.memberC)
	var pg store.Service
	decodeInto(t, f.expect(t, 201, "POST", pp+"/services", `{"source_type":"database","database_kind":"postgres"}`, f.memberC), &pg)
	dbInst := "/api/services/" + pg.ID + "/instances/" + f.prod.ID
	f.expect(t, 403, "PATCH", dbInst, `{"image":"attacker/miner:latest"}`, f.ownerC)
	f.expect(t, 200, "PATCH", dbInst, `{"image":"postgres:16-alpine"}`, f.ownerC)

	// 새 배포: 이미지 서비스와 CLI 업로드는 막고(deploy.Manager.Enqueue), 승인된 저장소는 된다.
	img := "nginx:alpine"
	isvc := &store.Service{ProjectID: f.p.ID, Name: "legacy", SourceType: "image"}
	if err := f.st.CreateService(ctx, isvc); err != nil {
		t.Fatal(err)
	}
	ii := &store.Instance{ServiceID: isvc.ID, EnvironmentID: f.prod.ID}
	ii.Image = &img
	if err := f.st.CreateInstance(ctx, ii); err != nil {
		t.Fatal(err)
	}
	f.expect(t, 403, "POST", "/api/services/"+isvc.ID+"/instances/"+f.prod.ID+"/deploy", "", f.ownerC)
	f.expect(t, 403, "POST", inst+"/upload", "", f.ownerC)
	f.expect(t, 202, "POST", inst+"/deploy", "", f.memberC)

	// 연결 해제는 항상 된다.
	f.expect(t, 200, "PATCH", inst, `{"repo_url":null}`, f.memberC)

	// 정책을 끄면 다시 된다(기존 배포에는 영향 없음).
	f.expect(t, 200, "PATCH", "/api/access-controls", `{"restrict_deployment_sources":false}`, f.ownerC)
	f.expect(t, 202, "POST", "/api/services/"+isvc.ID+"/instances/"+f.prod.ID+"/deploy", "", f.ownerC)
}

func jsonMarshal(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// 제한된 환경의 리소스를 가리키는 모든 라우트는 프로젝트 admin이 아니면 403이고, 프로젝트 수준 라우트는 그대로다.
func TestRestrictedEnvironmentRouteMatrix(t *testing.T) {
	f := newRBACFixture(t)
	ctx := context.Background()
	if err := f.st.SetEnvironmentRestricted(ctx, f.prod.ID, true); err != nil {
		t.Fatal(err)
	}
	padmin := testUser(t, f.st, "padmin@example.com", store.RoleMember)
	if err := f.st.SetMember(ctx, f.p.ID, padmin.ID, store.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	// 접근 그룹 admin도 프로젝트 admin이다.
	gadmin := testUser(t, f.st, "gadmin@example.com", store.RoleMember)
	f.st.SaveAccessGroup(ctx, &store.AccessGroup{Name: "ops", Role: store.RoleAdmin, UserIDs: []string{gadmin.ID}, ProjectIDs: []string{f.p.ID}})
	f.st.SetAccessControlSettings(ctx, &store.AccessControlSettings{AccessGroupsEnabled: true})
	padminC, gadminC := testSessionCookie(t, f.s, padmin), testSessionCookie(t, f.s, gadmin)
	envScoped := func(pattern string) bool {
		for _, v := range []string{"{eid}", "{did}", "{rid}", "{bid}", "{vid}", "{sbid}", "/db-backups/{id}"} {
			if strings.Contains(pattern, v) {
				return true
			}
		}
		return false
	}
	n := 0
	for _, rt := range f.s.Routes() {
		if rt.Access.Kind != accProject {
			continue
		}
		method, _, _ := strings.Cut(rt.Pattern, " ")
		path := f.fill(rt.Pattern)
		viewer := req(f.h, method, path, "", withCookie(f.viewerC)).Code
		if envScoped(rt.Pattern) {
			n++
			if viewer != 403 {
				t.Errorf("%s: project viewer on restricted env got %d, want 403", rt.Pattern, viewer)
			}
			if got := req(f.h, method, path, "", withCookie(f.outsideC)).Code; got != 404 {
				t.Errorf("%s: non-member on restricted env got %d, want 404", rt.Pattern, got)
			}
			for _, c := range []string{padminC, gadminC, f.ownerC} {
				if got := req(f.h, method, path, "", withCookie(c)).Code; got != probeStatus {
					t.Errorf("%s: project admin on restricted env got %d", rt.Pattern, got)
				}
			}
		} else if method == "GET" && rt.Access.Level <= lvViewer && viewer != probeStatus {
			t.Errorf("%s: project-level route blocked for viewer by env restriction: %d", rt.Pattern, viewer)
		}
	}
	if n < 60 {
		t.Fatalf("only %d environment-scoped routes checked", n)
	}
}
