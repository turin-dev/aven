package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/secret"
)

func TestUsersSessionsInvitesTokens(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	box, _ := secret.LoadOrCreate(filepath.Join(t.TempDir(), "m.key"))
	s.SetSecretBox(box)

	// 기본 owner 계정은 마이그레이션이 만든다.
	adm, err := s.GetUser(ctx, BuiltinUserID)
	if err != nil || !adm.Builtin || adm.Role != RoleOwner || adm.Email != BuiltinUserEmail || adm.PasswordHash != "" {
		t.Fatalf("builtin: %+v %v", adm, err)
	}
	if err := s.DeleteUser(ctx, BuiltinUserID); err != ErrNotFound {
		t.Errorf("builtin deleted: %v", err)
	}
	u := &User{Email: " Bob@Example.COM ", Name: "Bob", Role: RoleMember, PasswordHash: "h"}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(ctx, &User{Email: "bob@example.com", Role: RoleMember}); err != ErrConflict {
		t.Errorf("duplicate email: %v", err)
	}
	if got, err := s.GetUserByEmail(ctx, "BOB@example.com"); err != nil || got.ID != u.ID {
		t.Fatalf("by email: %v", err)
	}

	// TOTP 비밀은 암호화해서 저장한다.
	if err := s.SetUserTOTP(ctx, u.ID, "JBSWY3DPEHPK3PXP", true); err != nil {
		t.Fatal(err)
	}
	var raw string
	s.q.QueryRowContext(ctx, `SELECT totp_secret FROM users WHERE id=?`, u.ID).Scan(&raw)
	if strings.Contains(raw, "JBSWY3DP") || !strings.HasPrefix(raw, secret.Prefix) {
		t.Errorf("totp secret stored in plain text: %q", raw)
	}
	got, _ := s.GetUser(ctx, u.ID)
	if got.TOTPSecret != "JBSWY3DPEHPK3PXP" || !got.TOTPEnabled {
		t.Errorf("totp roundtrip: %+v", got)
	}
	if ok, _ := s.UseTOTPStep(ctx, u.ID, 100); !ok {
		t.Error("first step rejected")
	}
	for _, step := range []int64{100, 99} {
		if ok, _ := s.UseTOTPStep(ctx, u.ID, step); ok {
			t.Errorf("step %d replayed", step)
		}
	}
	s.SetRecoveryCodes(ctx, u.ID, []string{"a", "b"})
	if ok, _ := s.UseRecoveryCode(ctx, u.ID, "a"); !ok {
		t.Error("recovery code rejected")
	}
	if ok, _ := s.UseRecoveryCode(ctx, u.ID, "a"); ok {
		t.Error("recovery code reused")
	}
	s.SetUserTOTP(ctx, u.ID, "", false)
	if n, _ := s.CountRecoveryCodes(ctx, u.ID); n != 0 {
		t.Errorf("recovery codes kept after disabling 2fa: %d", n)
	}

	// 세션: 만료되면 찾을 수 없고, 사용자별 삭제는 하나를 남길 수 있다.
	keep := &Session{UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour)}
	other := &Session{UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour)}
	old := &Session{UserID: u.ID, ExpiresAt: time.Now().Add(-time.Minute)}
	for i, se := range []*Session{keep, other, old} {
		if err := s.CreateSession(ctx, se, string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.GetSessionByHash(ctx, "c"); err != ErrNotFound {
		t.Errorf("expired session found: %v", err)
	}
	if ses, _ := s.ListSessions(ctx, u.ID); len(ses) != 2 {
		t.Errorf("sessions: %d", len(ses))
	}
	s.DeleteUserSessions(ctx, u.ID, keep.ID)
	if _, err := s.GetSessionByHash(ctx, "b"); err != ErrNotFound {
		t.Error("other session survived")
	}
	if _, err := s.GetSessionByHash(ctx, "a"); err != nil {
		t.Error("kept session deleted")
	}

	// 초대: 이메일 충돌이면 초대가 남고, 성공하면 한 번만 쓸 수 있다.
	p := &Project{Name: "p"}
	if _, err := s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	pr := RoleViewer
	inv := &Invite{Email: "bob@example.com", Role: RoleMember, ProjectID: &p.ID, ProjectRole: &pr, ExpiresAt: time.Now().Add(time.Hour)}
	s.CreateInvite(ctx, inv, "inv1")
	if _, err := s.AcceptInvite(ctx, "inv1", &User{Name: "x"}); err != ErrConflict {
		t.Errorf("conflicting invite: %v", err)
	}
	if ins, _ := s.ListInvites(ctx); len(ins) != 1 {
		t.Error("invite lost on conflict")
	}
	inv2 := &Invite{Email: "carol@example.com", Role: RoleViewer, ProjectID: &p.ID, ProjectRole: &pr, ExpiresAt: time.Now().Add(time.Hour)}
	s.CreateInvite(ctx, inv2, "inv2")
	carol := &User{Name: "Carol", PasswordHash: "h"}
	if _, err := s.AcceptInvite(ctx, "inv2", carol); err != nil || carol.Role != RoleViewer || carol.Email != "carol@example.com" {
		t.Fatalf("accept: %+v %v", carol, err)
	}
	if role, _ := s.MemberRole(ctx, p.ID, carol.ID); role != RoleViewer {
		t.Errorf("membership: %q", role)
	}
	if _, err := s.AcceptInvite(ctx, "inv2", &User{Name: "again"}); err != ErrNotFound {
		t.Errorf("invite reused: %v", err)
	}

	// 토큰: 만료된 토큰은 찾을 수 없고, 사용자를 지우면 토큰·세션·멤버십도 지워진다.
	exp := time.Now().Add(-time.Second)
	s.CreateAPIToken(ctx, &APIToken{UserID: carol.ID, Name: "old", Prefix: "keel_o", Scope: ScopeWorkspace, Role: RoleViewer, ExpiresAt: &exp}, "t-old")
	if _, err := s.GetAPITokenByHash(ctx, "t-old"); err != ErrNotFound {
		t.Errorf("expired token found: %v", err)
	}
	s.CreateAPIToken(ctx, &APIToken{UserID: carol.ID, Name: "ok", Prefix: "keel_k", Scope: ScopeProject, ProjectID: &p.ID, Role: RoleViewer}, "t-ok")
	if tk, err := s.GetAPITokenByHash(ctx, "t-ok"); err != nil || tk.ProjectID == nil || *tk.ProjectID != p.ID {
		t.Fatalf("token: %+v %v", tk, err)
	}
	if err := s.DeleteUser(ctx, carol.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAPITokenByHash(ctx, "t-ok"); err != ErrNotFound {
		t.Error("token survived user deletion")
	}
	if ms, _ := s.ListMembers(ctx, p.ID); len(ms) != 0 {
		t.Errorf("membership survived user deletion: %+v", ms)
	}
	if n, _ := s.CountActiveOwners(ctx); n != 1 {
		t.Errorf("owners: %d", n)
	}
}
