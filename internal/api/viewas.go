package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

// 서버 owner의 "사용자로 보기": owner 세션의 요청을 정해진 시간 동안 대상 사용자의 권한으로 처리한다(권한 문제 진단용).
// 읽기만 된다: 쓰기 요청과 웹소켓(셸, exec)은 requireAuth가 막는다. 시작과 끝은 감사 기록에 남는다.

const (
	viewAsDefault = 30 * time.Minute
	viewAsMax     = 2 * time.Hour
)

// viewAsPrincipal은 owner 세션 p에 유효한 보기 대상이 있으면 그 사용자로 바꾼 주체다(없으면 nil).
// 기한이 지났거나 owner가 아니게 됐거나 대상이 보기 금지 대상이 되면 보기를 끝낸다.
func (s *Server) viewAsPrincipal(ctx context.Context, p *principal) *principal {
	uid, until, err := s.Store.SessionViewAs(ctx, p.SessionID)
	if err != nil {
		return nil
	}
	target, err := s.Store.GetUser(ctx, uid)
	if err != nil || time.Now().After(until) || !s.instanceOwner(p.User) || s.viewAsForbidden(target) != "" {
		s.Store.DeleteSessionViewAs(context.WithoutCancel(ctx), p.SessionID)
		return nil
	}
	return &principal{User: target, Via: viaSession, SessionID: p.SessionID, ViewAsBy: p.User, ViewAsUntil: until}
}

// viewAsForbidden은 대상으로 볼 수 없는 이유다(볼 수 있으면 ""). 다른 owner는 모든 권한이 있어 진단 의미가 없고 권한 혼동만 생긴다.
func (s *Server) viewAsForbidden(u *store.User) string {
	switch {
	case u.Disabled:
		return "비활성 계정은 볼 수 없습니다"
	case u.Builtin || s.instanceOwner(u):
		return "서버 owner 계정은 볼 수 없습니다"
	}
	return ""
}

// viewAsAllowed는 사용자로 보기 중에 허용되는 요청인지다: 읽기(웹소켓 제외)와 보기 끝내기, 로그아웃.
func viewAsAllowed(r *http.Request) bool {
	if r.Header.Get("Upgrade") != "" {
		return false
	}
	return safeMethod(r.Method)
}

func (s *Server) startViewAs(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	var in struct {
		UserID  string `json:"user_id"`
		Minutes int    `json:"minutes"`
	}
	if !decode(w, r, &in) {
		return
	}
	if p.SessionID == "" {
		writeErr(w, 403, "브라우저 세션에서만 쓸 수 있습니다")
		return
	}
	if in.UserID == p.User.ID {
		writeErr(w, 400, "자기 자신은 볼 수 없습니다")
		return
	}
	target, err := s.Store.GetUser(r.Context(), in.UserID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if why := s.viewAsForbidden(target); why != "" {
		writeErr(w, 400, why)
		return
	}
	d := viewAsDefault
	if in.Minutes > 0 {
		d = min(time.Duration(in.Minutes)*time.Minute, viewAsMax)
	}
	until := time.Now().Add(d).UTC()
	if err := s.Store.SetSessionViewAs(r.Context(), p.SessionID, target.ID, until); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "owner.view_as_started", target.ID, fmt.Sprintf("%s 사용자로 보기를 시작했습니다(%d분, 읽기 전용)", target.Display(), int(d.Minutes())))
	writeJSON(w, 200, map[string]any{"user_id": target.ID, "until": until})
}

// stopViewAs는 보기를 끝낸다. 보기 중에는 주체가 대상 사용자라 owner 전용 라우트를 쓸 수 없으므로 세션 쿠키로 직접 처리한다.
func (s *Server) stopViewAs(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		writeErr(w, 401, "unauthorized")
		return
	}
	p, _ := s.sessionPrincipal(r.Context(), c.Value)
	if p == nil {
		writeErr(w, 401, "unauthorized")
		return
	}
	if !sameOrigin(r) {
		writeErr(w, 403, "cross-origin request rejected")
		return
	}
	if p.ViewAsBy == nil {
		w.WriteHeader(204) // 이미 끝났다(기한 만료 등)
		return
	}
	if err := s.Store.DeleteSessionViewAs(r.Context(), p.SessionID); err != nil {
		s.storeErr(w, err)
		return
	}
	a := &store.AuditEntry{Kind: "owner.view_as_ended", Target: p.User.ID, ActorID: p.ViewAsBy.ID, Actor: p.ViewAsBy.Display(),
		Message: p.User.Display() + " 사용자로 보기를 끝냈습니다", IP: limitKey(s.clientIP(r))}
	if err := s.Store.CreateAudit(context.WithoutCancel(r.Context()), a); err != nil {
		s.Log.Warn("audit", "err", err)
	}
	w.WriteHeader(204)
}
