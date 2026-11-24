package api

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

func TestSessionExpiryIsCappedByMaxAge(t *testing.T) {
	s := &Server{}
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	// A fresh session slides by sessionTTL.
	if got := s.sessionExpiry(now, now); !got.Equal(now.Add(sessionTTL)) {
		t.Errorf("fresh: %v", got)
	}
	// 80 days in, the 90-day cap is nearer than now+30d.
	created := now.Add(-80 * 24 * time.Hour)
	if got := s.sessionExpiry(created, now); !got.Equal(created.Add(DefaultSessionMaxAge)) {
		t.Errorf("capped: %v, want %v", got, created.Add(DefaultSessionMaxAge))
	}
	// A negative max age removes the cap.
	s.SessionMaxAge = -1
	if got := s.sessionExpiry(created, now); !got.Equal(now.Add(sessionTTL)) {
		t.Errorf("uncapped: %v", got)
	}
}

func TestSessionPastMaxAgeIsRejectedAndDeleted(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	s := &Server{Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	u := testUser(t, st, "member@example.com", store.RoleMember)
	tok := testSessionCookie(t, s, u)

	s.SessionMaxAge = -1 // no cap: the session works
	if p, _ := s.sessionPrincipal(ctx, tok); p == nil {
		t.Fatal("session rejected without a cap")
	}
	s.SessionMaxAge = time.Nanosecond // older than the cap
	time.Sleep(time.Millisecond)
	if p, _ := s.sessionPrincipal(ctx, tok); p != nil {
		t.Fatal("session past its max age was accepted")
	}
	if _, err := st.GetSessionByHash(ctx, hashSecret(tok)); err == nil {
		t.Fatal("expired session was not deleted")
	}
}
