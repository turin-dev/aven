package api

import (
	"context"
	"crypto/hmac"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/notify"
)

// 알림함 스트림 주기(테스트에서 줄인다).
var (
	inboxHeartbeat = 25 * time.Second // ": ping" 주석
	inboxRecheck   = 10 * time.Second // 세션/토큰이 아직 유효한지
)

// fallbackInbox는 Notifier가 없을 때(테스트, 알림 미설정) 쓰는 빈 허브다.
var fallbackInbox notify.InboxHub

func (s *Server) inboxHub() *notify.InboxHub {
	if s.Notifier != nil {
		return s.Notifier.Inbox()
	}
	return &fallbackInbox
}

// kickInboxStreams는 세션을 지운 뒤 열린 알림 스트림이 자격을 곧바로 다시 검사하게 한다.
func (s *Server) kickInboxStreams() { s.inboxHub().Kick() }

// principalStillValid는 스트림을 연 자격이 아직 살아 있는지 본다(로그아웃, 세션 끊기, 비활성화, 토큰 삭제).
// 세션은 건드리지 않는다(열린 스트림만으로 세션 만료가 늦춰지지 않도록).
func (s *Server) principalStillValid(ctx context.Context, r *http.Request, p *principal) bool {
	if p == nil || s.Store == nil {
		return true
	}
	switch p.Via {
	case viaEnvToken:
		return true
	case viaToken:
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		np := s.bearerPrincipal(ctx, tok)
		return np != nil && np.Token != nil && np.Token.ID == p.Token.ID
	case viaSession:
		c, err := r.Cookie(cookieName)
		if err != nil || c.Value == "" || len(c.Value) > 200 {
			return false
		}
		se, err := s.Store.GetSessionByHash(ctx, hashSecret(c.Value))
		if err != nil || se.ID != p.SessionID {
			return false
		}
		u, err := s.Store.GetUser(ctx, se.UserID)
		if err != nil || u.Disabled {
			return false
		}
		if u.Builtin && (s.AdminPassword == "" || !hmac.Equal([]byte(se.AuthTag), []byte(s.adminTag()))) {
			return false
		}
		return true
	}
	return false
}

// streamNotifications는 내 알림함에 새로 들어온 항목을 SSE로 보낸다(event: notification).
// 25초마다 주석 heartbeat를 보내고, 로그아웃·세션 끊기·토큰 삭제 뒤에는 event: revoked를 보내고 닫는다.
func (s *Server) streamNotifications(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	if p == nil || p.User == nil {
		writeErr(w, 401, "unauthorized")
		return
	}
	hub := s.inboxHub()
	ch, cancel := hub.Subscribe(p.User.ID)
	defer cancel()
	e, ok := newSSE(w)
	if !ok {
		return
	}
	fmt.Fprint(w, "retry: 5000\n\n")
	e.f.Flush()
	heartbeat := time.NewTicker(inboxHeartbeat)
	defer heartbeat.Stop()
	recheck := time.NewTicker(inboxRecheck)
	defer recheck.Stop()
	kicked := hub.Kicked()
	revoked := func() bool {
		if s.principalStillValid(r.Context(), r, p) {
			return false
		}
		e.sendJSON("revoked", map[string]string{"reason": "세션이 끝났습니다"})
		e.f.Flush()
		return true
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			e.f.Flush()
		case <-recheck.C:
			if revoked() {
				return
			}
		case <-kicked:
			kicked = hub.Kicked()
			if revoked() {
				return
			}
		case it := <-ch:
			// 범위 토큰이나 멤버에서 빠진 프로젝트의 알림은 보내지 않는다(목록 API와 같은 규칙).
			if s.visibleLevel(r, it.ProjectID) <= lvNone {
				continue
			}
			e.sendJSON("notification", it)
			e.f.Flush()
		}
	}
}
