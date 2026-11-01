package api

import (
	"context"
	"net/http"
	"time"
)

// guardStream은 오래 열려 있는 스트림(SSE 로그, 이벤트)의 context다. 요청 context가 끝나거나, 스트림을 연 자격이
// 사라지면(로그아웃, 세션 끊기, 토큰 삭제, 비활성화) 또는 프로젝트(eid가 있으면 그 환경)에 더 접근할 수 없게 되면
// (멤버에서 빠짐, 제한된 환경으로 바뀜) 취소된다. 권한은 스트림을 열 때 한 번만 검사되므로 이렇게 다시 확인한다.
// 알림함 스트림과 같은 주기(inboxRecheck)로, 세션을 지울 때 오는 kick에도 바로 검사한다.
func (s *Server) guardStream(r *http.Request, pid, eid string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(r.Context())
	p := principalFrom(r)
	if p == nil || s.Store == nil { // 핸들러를 직접 부르는 테스트
		return ctx, cancel
	}
	allowed := func() bool {
		// OAuth 토큰은 principalStillValid가 다루지 않는다. 권한(그랜트 scope 포함)만 다시 본다.
		if p.Via != viaOAuth && !s.principalStillValid(ctx, r, p) {
			return false
		}
		if pid == "" {
			return true
		}
		lv, err := s.projectLevel(ctx, p, pid, eid)
		return err == nil && lv > lvNone
	}
	go func() {
		hub := s.inboxHub()
		t := time.NewTicker(inboxRecheck)
		defer t.Stop()
		kicked := hub.Kicked()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-kicked:
				kicked = hub.Kicked()
			}
			if ctx.Err() == nil && !allowed() {
				cancel()
				return
			}
		}
	}()
	return ctx, cancel
}
