package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/secret"
)

func TestPasskeysChallengesAndMFAReset(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	box, _ := secret.LoadOrCreate(filepath.Join(t.TempDir(), "m.key"))
	s.SetSecretBox(box)
	u := &User{Email: "pk@example.com", Name: "pk", Role: RoleMember, PasswordHash: "h"}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}

	// 작업 공간 2FA 요구는 기본값이 꺼짐이다.
	if on, err := s.Workspace2FAEnforced(ctx); err != nil || on {
		t.Fatalf("default enforcement: %v %v", on, err)
	}
	s.SetWorkspace2FAEnforced(ctx, true)
	if on, _ := s.Workspace2FAEnforced(ctx); !on {
		t.Error("enforcement not stored")
	}

	// passkey credential은 암호화해서 저장하고, (rp_id, credential_id)는 유일하다.
	p := &Passkey{UserID: u.ID, RPID: "aven.example.com", Name: "laptop", CredentialID: "cred1", CredentialJSON: `{"publicKey":"SECRETKEY"}`}
	if err := s.CreatePasskey(ctx, p); err != nil {
		t.Fatal(err)
	}
	var raw string
	s.q.QueryRowContext(ctx, `SELECT credential_data FROM passkeys WHERE id=?`, p.ID).Scan(&raw)
	if strings.Contains(raw, "SECRETKEY") || !strings.HasPrefix(raw, secret.Prefix) {
		t.Errorf("credential stored in plain text: %q", raw)
	}
	if err := s.CreatePasskey(ctx, &Passkey{UserID: u.ID, RPID: "aven.example.com", Name: "dup", CredentialID: "cred1", CredentialJSON: "{}"}); err != ErrConflict {
		t.Errorf("duplicate credential: %v", err)
	}
	// 같은 credential ID라도 다른 RP(호스트)면 별개다.
	if err := s.CreatePasskey(ctx, &Passkey{UserID: u.ID, RPID: "other.example.com", Name: "o", CredentialID: "cred1", CredentialJSON: "{}"}); err != nil {
		t.Errorf("same id on another rp: %v", err)
	}
	if recs, _ := s.PasskeyRecords(ctx, u.ID, "aven.example.com"); len(recs) != 1 || recs[0].CredentialJSON != `{"publicKey":"SECRETKEY"}` {
		t.Errorf("records: %+v", recs)
	}
	if got, err := s.PasskeyByCredentialID(ctx, "aven.example.com", "cred1"); err != nil || got.ID != p.ID {
		t.Errorf("by credential id: %v", err)
	}
	if counts, _ := s.PasskeyCounts(ctx); counts[u.ID] != 2 {
		t.Errorf("counts: %v", counts)
	}
	if err := s.UpdatePasskeyCredential(ctx, "someone-else", p.ID, "{}"); err != ErrNotFound {
		t.Errorf("update another user's passkey: %v", err)
	}
	if err := s.UpdatePasskeyCredential(ctx, u.ID, p.ID, `{"n":1}`); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.ListPasskeys(ctx, u.ID); len(l) != 2 || l[0].LastUsedAt == nil || l[0].CredentialJSON != "" {
		t.Errorf("list: %+v", l[0])
	}
	if err := s.DeletePasskey(ctx, "someone-else", p.ID); err != ErrNotFound {
		t.Errorf("delete another user's passkey: %v", err)
	}

	// challenge는 한 번만, 같은 목적과 세션으로만, 만료 전에만 쓸 수 있다.
	mk := func(hash, purpose, session string, exp time.Duration) {
		t.Helper()
		if err := s.CreateWebAuthnChallenge(ctx, hash, &WebAuthnChallenge{UserID: u.ID, SessionID: session, Purpose: purpose, Origin: "https://aven.example.com", RPID: "aven.example.com", SessionData: "{}", ExpiresAt: time.Now().Add(exp)}); err != nil {
			t.Fatal(err)
		}
	}
	mk("h1", "register", "ses1", time.Minute)
	if _, err := s.ConsumeWebAuthnChallenge(ctx, "h1", "register", "ses2"); err != ErrNotFound {
		t.Errorf("other session: %v", err)
	}
	if _, err := s.ConsumeWebAuthnChallenge(ctx, "h1", "login_factor", "ses1"); err != ErrNotFound {
		t.Errorf("other purpose: %v", err)
	}
	if _, err := s.ConsumeWebAuthnChallenge(ctx, "h1", "register", "ses1"); err != nil {
		t.Errorf("owner after a mismatched attempt: %v", err)
	}
	mk("h3", "register", "ses1", time.Minute)
	c, err := s.ConsumeWebAuthnChallenge(ctx, "h3", "register", "ses1")
	if err != nil || c.UserID != u.ID || c.RPID != "aven.example.com" {
		t.Fatalf("consume: %+v %v", c, err)
	}
	if _, err := s.ConsumeWebAuthnChallenge(ctx, "h3", "register", "ses1"); err != ErrNotFound {
		t.Errorf("replay: %v", err)
	}
	mk("h4", "register", "ses1", -time.Second)
	if _, err := s.ConsumeWebAuthnChallenge(ctx, "h4", "register", "ses1"); err != ErrNotFound {
		t.Errorf("expired: %v", err)
	}

	// TOTP만 끄면 복구 코드는 남고, 전체 초기화는 모두 지운다.
	s.SetUserTOTP(ctx, u.ID, "JBSWY3DPEHPK3PXP", true)
	s.SetRecoveryCodes(ctx, u.ID, []string{"a", "b"})
	if err := s.ClearUserTOTP(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetUser(ctx, u.ID); got.TOTPEnabled || got.TOTPSecret != "" {
		t.Errorf("totp after clear: %+v", got)
	}
	if n, _ := s.CountRecoveryCodes(ctx, u.ID); n != 2 {
		t.Errorf("recovery codes after ClearUserTOTP: %d", n)
	}
	mk("h5", "register", "ses1", time.Minute)
	if err := s.ResetUserMFA(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountPasskeys(ctx, u.ID); n != 0 {
		t.Errorf("passkeys after reset: %d", n)
	}
	if n, _ := s.CountRecoveryCodes(ctx, u.ID); n != 0 {
		t.Errorf("recovery codes after reset: %d", n)
	}
	if _, err := s.ConsumeWebAuthnChallenge(ctx, "h5", "register", "ses1"); err != ErrNotFound {
		t.Errorf("challenge after reset: %v", err)
	}
	if err := s.ResetUserMFA(ctx, "missing"); err != ErrNotFound {
		t.Errorf("reset unknown user: %v", err)
	}
}
