package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestAccessControlsStore(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// 기본값: 모두 꺼짐
	p, err := s.AccessControlSettings(ctx)
	if err != nil || p.AccessGroupsEnabled || p.RestrictGeneratedDomains || p.RestrictTCPProxy || p.RestrictDeploymentSources || p.ApprovedGitHubOrgs == nil {
		t.Fatalf("defaults: %+v %v", p, err)
	}
	p.RestrictTCPProxy, p.ApprovedGitHubOrgs = true, []string{"acme"}
	if err := s.SetAccessControlSettings(ctx, p); err != nil {
		t.Fatal(err)
	}
	if p2, _ := s.AccessControlSettings(ctx); !p2.RestrictTCPProxy || len(p2.ApprovedGitHubOrgs) != 1 || p2.ApprovedGitHubOrgs[0] != "acme" {
		t.Errorf("round trip: %+v", p2)
	}

	pr := &Project{Name: "p"}
	env, err := s.CreateProject(ctx, pr)
	if err != nil {
		t.Fatal(err)
	}
	other := &Project{Name: "q"}
	if _, err := s.CreateProject(ctx, other); err != nil {
		t.Fatal(err)
	}
	u := &User{Email: "u@example.com", Role: RoleMember}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}

	if err := s.SetWorkspaceMember(ctx, DefaultWorkspaceID, u.ID, RoleMember); err != nil {
		t.Fatal(err)
	}

	// 환경 제한
	if env.Restricted {
		t.Error("new environment restricted")
	}
	if err := s.SetEnvironmentRestricted(ctx, env.ID, true); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.GetEnvironment(ctx, env.ID); !e.Restricted {
		t.Error("restricted not stored")
	}
	if envs, _ := s.ListEnvironments(ctx, pr.ID); len(envs) != 1 || !envs[0].Restricted {
		t.Errorf("list: %+v", envs)
	}
	e2 := &Environment{ProjectID: pr.ID, Name: "staging", Restricted: true}
	if err := s.CreateEnvironment(ctx, e2); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.GetEnvironment(ctx, e2.ID); !e.Restricted {
		t.Error("CreateEnvironment dropped restricted")
	}
	if err := s.SetEnvironmentRestricted(ctx, "env_missing", true); err != ErrNotFound {
		t.Errorf("missing env: %v", err)
	}

	// 그룹
	g := &AccessGroup{Name: "Platform", Role: RoleViewer, UserIDs: []string{u.ID, u.ID, ""}, ProjectIDs: []string{pr.ID}}
	if err := s.SaveAccessGroup(ctx, g); err != nil {
		t.Fatal(err)
	}
	if len(g.UserIDs) != 1 {
		t.Errorf("ids not deduplicated: %v", g.UserIDs)
	}
	if err := s.SaveAccessGroup(ctx, &AccessGroup{Name: "platform", Role: RoleAdmin}); err != ErrConflict {
		t.Errorf("duplicate name (case-insensitive): %v", err)
	}
	if err := s.SaveAccessGroup(ctx, &AccessGroup{Name: "bad", Role: RoleViewer, UserIDs: []string{"usr_missing"}}); err == nil {
		t.Error("unknown user accepted")
	}

	// 꺼져 있으면 그룹은 권한을 주지 않는다
	if role, _ := s.ProjectRole(ctx, pr.ID, u.ID); role != "" {
		t.Errorf("group applied while disabled: %q", role)
	}
	if role, _ := s.AccessGroupRole(ctx, pr.ID, u.ID); role != RoleViewer {
		t.Errorf("AccessGroupRole: %q", role)
	}
	p.AccessGroupsEnabled = true
	s.SetAccessControlSettings(ctx, p)
	if role, _ := s.ProjectRole(ctx, pr.ID, u.ID); role != RoleViewer {
		t.Errorf("group role: %q", role)
	}
	if role, _ := s.ProjectRole(ctx, other.ID, u.ID); role != "" {
		t.Errorf("group leaked to another project: %q", role)
	}
	// 직접 부여가 더 높으면 그쪽
	s.SetMember(ctx, pr.ID, u.ID, RoleMember)
	if role, _ := s.ProjectRole(ctx, pr.ID, u.ID); role != RoleMember {
		t.Errorf("direct member + group viewer: %q", role)
	}
	// 여러 그룹이면 가장 높은 역할
	admins := &AccessGroup{Name: "Admins", Role: RoleAdmin, UserIDs: []string{u.ID}}
	s.SaveAccessGroup(ctx, admins)
	if role, _ := s.ProjectRole(ctx, pr.ID, u.ID); role != RoleMember {
		t.Errorf("group without the project changed the role: %q", role)
	}
	if err := s.SetProjectAccessGroups(ctx, pr.ID, []string{g.ID, admins.ID}); err != nil {
		t.Fatal(err)
	}
	if role, _ := s.ProjectRole(ctx, pr.ID, u.ID); role != RoleAdmin {
		t.Errorf("highest group role: %q", role)
	}
	grants, err := s.ProjectGroupGrants(ctx, pr.ID)
	if err != nil || len(grants) != 2 || grants[0].GroupName != "Admins" || grants[0].UserID != u.ID {
		t.Errorf("grants: %+v %v", grants, err)
	}
	gs, _ := s.ListAccessGroups(ctx)
	if len(gs) != 2 || gs[0].Name != "Admins" || len(gs[0].ProjectIDs) != 1 || len(gs[1].UserIDs) != 1 {
		t.Errorf("list: %+v", gs)
	}
	// 그룹 삭제·사용자 삭제는 부여를 지운다
	if err := s.DeleteAccessGroup(ctx, admins.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAccessGroup(ctx, admins.ID); err != ErrNotFound {
		t.Errorf("double delete: %v", err)
	}
	s.RemoveMember(ctx, pr.ID, u.ID)
	if role, _ := s.ProjectRole(ctx, pr.ID, u.ID); role != RoleViewer {
		t.Errorf("after deleting admin group: %q", role)
	}
	if err := s.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAccessGroup(ctx, g.ID); len(got.UserIDs) != 0 {
		t.Errorf("deleted user still in group: %v", got.UserIDs)
	}
	if err := s.DeleteProject(ctx, pr.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAccessGroup(ctx, g.ID); len(got.ProjectIDs) != 0 {
		t.Errorf("deleted project still in group: %v", got.ProjectIDs)
	}
}
