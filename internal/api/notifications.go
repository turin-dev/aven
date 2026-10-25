package api

import (
	"context"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/notify"
	"github.com/turin-dev/aven/internal/store"
)

// ---- 알림 채널 ----

// defaultChannelEvents는 events를 주지 않은 새 채널이 받는 이벤트다.
var defaultChannelEvents = []string{"deploy.failed", "deploy.crashed", "alert.*", "volume.limit_exceeded", "usage.threshold_reached", "usage.limit_reached"}

const maxChannelsPerScope = 50

// channelView는 채널의 API 모양이다. 서명 키는 내보내지 않고(secret_set), URL은 member 미만에게 가린다.
type channelView struct {
	*store.NotificationChannel
	URL        *string  `json:"url"`
	SecretSet  bool     `json:"secret_set"`
	Recipients []string `json:"recipients"`
}

func viewChannel(c *store.NotificationChannel, secrets bool) channelView {
	v := channelView{NotificationChannel: c, SecretSet: c.Config.Secret != "", Recipients: c.Config.Recipients}
	if v.Recipients == nil {
		v.Recipients = []string{}
	}
	if c.Config.URL != "" {
		u := c.Config.URL
		if !secrets {
			u = MaskedValue
		}
		v.URL = &u
	}
	return v
}

type channelInput struct {
	Kind       *string   `json:"kind"`
	Name       *string   `json:"name"`
	URL        *string   `json:"url"`
	Secret     *string   `json:"secret"` // ""이면 지운다
	Recipients *[]string `json:"recipients"`
	Events     *[]string `json:"events"`
	Enabled    *bool     `json:"enabled"`
}

func validEventPatterns(ev []string) string {
	if len(ev) > 100 {
		return "events: 100개까지 지정할 수 있습니다"
	}
	for _, e := range ev {
		if !notify.ValidPattern(e) {
			return "invalid event pattern: " + e
		}
	}
	return ""
}

// applyChannel은 입력을 채널에 반영하고 검사한다(오류 메시지가 비면 통과).
func (s *Server) applyChannel(c *store.NotificationChannel, in *channelInput) string {
	if in.Kind != nil {
		if c.ID != "" && *in.Kind != c.Kind {
			return "kind는 바꿀 수 없습니다"
		}
		c.Kind = *in.Kind
	}
	if !store.ValidChannelKind(c.Kind) {
		return "kind must be webhook, slack, discord or email"
	}
	if in.Name != nil {
		c.Name = strings.TrimSpace(*in.Name)
	}
	if c.Name == "" || len(c.Name) > 100 || strings.ContainsAny(c.Name, "\r\n\t") {
		return "name is required (max 100 chars)"
	}
	if in.URL != nil {
		c.Config.URL = strings.TrimSpace(*in.URL)
	}
	if in.Secret != nil {
		if len(*in.Secret) > 256 {
			return "secret is too long (max 256)"
		}
		c.Config.Secret = *in.Secret
	}
	if in.Recipients != nil {
		c.Config.Recipients = nil
		for _, a := range *in.Recipients {
			a = strings.TrimSpace(a)
			if a == "" {
				continue
			}
			if !notify.ValidAddress(a) {
				return "invalid recipient: " + a
			}
			c.Config.Recipients = append(c.Config.Recipients, a)
		}
	}
	if c.Kind == store.ChannelEmail {
		c.Config.URL, c.Config.Secret = "", ""
		if len(c.Config.Recipients) == 0 || len(c.Config.Recipients) > 20 {
			return "email channels need 1-20 recipients"
		}
	} else {
		c.Config.Recipients = nil
		if !notify.ValidURL(c.Config.URL) || len(c.Config.URL) > 2000 {
			return "url must be an http(s) URL without credentials"
		}
		if msg := s.checkNotifyHost(c.Config.URL); msg != "" {
			return msg
		}
	}
	if in.Events != nil {
		c.Events = *in.Events
	}
	if c.Events == nil {
		c.Events = append([]string(nil), defaultChannelEvents...)
	}
	if msg := validEventPatterns(c.Events); msg != "" {
		return msg
	}
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
	return ""
}

// checkNotifyHost는 URL 호스트가 IP 주소일 때 바로 거른다(이름은 보낼 때마다 풀어서 검사한다).
func (s *Server) checkNotifyHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "invalid url"
	}
	if s.Notifier != nil && s.Notifier.Guard != nil && s.Notifier.Guard.AllowPrivate {
		return ""
	}
	h := u.Hostname()
	if a, err := netip.ParseAddr(h); err == nil && !notify.PublicAddr(a) {
		return notify.ErrBlockedAddress.Error()
	}
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return notify.ErrBlockedAddress.Error()
	}
	return ""
}

// loadChannel은 범위(pid, 작업 공간이면 "")에 속한 채널을 찾는다. 다른 범위의 채널은 없는 것처럼 404다.
func (s *Server) loadChannel(w http.ResponseWriter, r *http.Request, pid string) (*store.NotificationChannel, bool) {
	c, err := s.Store.GetChannel(r.Context(), r.PathValue("cid"))
	if err == nil && ((pid == "" && c.ProjectID != nil) || (pid != "" && (c.ProjectID == nil || *c.ProjectID != pid))) {
		err = store.ErrNotFound
	}
	if err != nil {
		s.storeErr(w, err)
		return nil, false
	}
	return c, true
}

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request, pid string, secrets bool) {
	cs, err := s.Store.ListChannels(r.Context(), pid)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	out := make([]channelView, 0, len(cs))
	for _, c := range cs {
		out = append(out, viewChannel(c, secrets))
	}
	writeJSON(w, 200, out)
}

func (s *Server) createChannel(w http.ResponseWriter, r *http.Request, pid string) {
	var in channelInput
	if !decode(w, r, &in) {
		return
	}
	existing, err := s.Store.ListChannels(r.Context(), pid)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if len(existing) >= maxChannelsPerScope {
		writeErr(w, 400, "알림 채널이 너무 많습니다")
		return
	}
	c := &store.NotificationChannel{Enabled: true}
	if pid != "" {
		c.ProjectID = &pid
	}
	if msg := s.applyChannel(c, &in); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	if err := s.Store.CreateChannel(r.Context(), c); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "notification_channel.created", c.ID, c.Kind+" 알림 채널 "+c.Name+" 생성")
	writeJSON(w, 201, viewChannel(c, true))
}

func (s *Server) patchChannel(w http.ResponseWriter, r *http.Request, pid string) {
	c, ok := s.loadChannel(w, r, pid)
	if !ok {
		return
	}
	var in channelInput
	if !decode(w, r, &in) {
		return
	}
	if msg := s.applyChannel(c, &in); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	if err := s.Store.UpdateChannel(r.Context(), c); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "notification_channel.updated", c.ID, "알림 채널 "+c.Name+" 변경")
	writeJSON(w, 200, viewChannel(c, true))
}

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request, pid string) {
	c, ok := s.loadChannel(w, r, pid)
	if !ok {
		return
	}
	if err := s.Store.DeleteChannel(r.Context(), c.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "notification_channel.deleted", c.ID, "알림 채널 "+c.Name+" 삭제")
	w.WriteHeader(204)
}

func (s *Server) testChannel(w http.ResponseWriter, r *http.Request, pid string) {
	c, ok := s.loadChannel(w, r, pid)
	if !ok {
		return
	}
	if s.Notifier == nil {
		writeErr(w, 503, "알림 전송이 설정되지 않았습니다")
		return
	}
	var p *store.Project
	if pid != "" {
		p, _ = s.Store.GetProject(r.Context(), pid)
	}
	actor := ""
	if pr := principalFrom(r); pr != nil {
		actor = pr.User.Display()
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 90*time.Second)
	defer cancel()
	res := s.Notifier.Test(ctx, c, p, actor)
	out := map[string]any{"ok": res.Err == nil, "attempts": res.Attempts, "status_code": res.StatusCode, "duration_ms": res.Duration.Milliseconds(), "error": nil}
	if res.Err != nil {
		out["error"] = res.Err.Error()
	}
	writeJSON(w, 200, out)
}

func (s *Server) channelDeliveries(w http.ResponseWriter, r *http.Request, pid string) {
	c, ok := s.loadChannel(w, r, pid)
	if !ok {
		return
	}
	ds, err := s.Store.ListDeliveries(r.Context(), c.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, ds)
}

func (s *Server) listProjectChannels(w http.ResponseWriter, r *http.Request) {
	s.listChannels(w, r, r.PathValue("pid"), canSeeSecrets(r))
}
func (s *Server) createProjectChannel(w http.ResponseWriter, r *http.Request) {
	s.createChannel(w, r, r.PathValue("pid"))
}
func (s *Server) patchProjectChannel(w http.ResponseWriter, r *http.Request) {
	s.patchChannel(w, r, r.PathValue("pid"))
}
func (s *Server) deleteProjectChannel(w http.ResponseWriter, r *http.Request) {
	s.deleteChannel(w, r, r.PathValue("pid"))
}
func (s *Server) testProjectChannel(w http.ResponseWriter, r *http.Request) {
	s.testChannel(w, r, r.PathValue("pid"))
}
func (s *Server) projectChannelDeliveries(w http.ResponseWriter, r *http.Request) {
	s.channelDeliveries(w, r, r.PathValue("pid"))
}
func (s *Server) listWorkspaceChannels(w http.ResponseWriter, r *http.Request) {
	s.listChannels(w, r, "", true)
}
func (s *Server) createWorkspaceChannel(w http.ResponseWriter, r *http.Request) {
	s.createChannel(w, r, "")
}
func (s *Server) patchWorkspaceChannel(w http.ResponseWriter, r *http.Request) {
	s.patchChannel(w, r, "")
}
func (s *Server) deleteWorkspaceChannel(w http.ResponseWriter, r *http.Request) {
	s.deleteChannel(w, r, "")
}
func (s *Server) testWorkspaceChannel(w http.ResponseWriter, r *http.Request) {
	s.testChannel(w, r, "")
}
func (s *Server) workspaceChannelDeliveries(w http.ResponseWriter, r *http.Request) {
	s.channelDeliveries(w, r, "")
}

// ---- 메일 ----

func (s *Server) mailer() *notify.Mailer {
	if s.Notifier == nil {
		return nil
	}
	return s.Notifier.Mailer
}

// testSMTP는 SMTP 설정을 시험한다(받는 사람을 주지 않으면 요청한 사용자의 이메일).
func (s *Server) testSMTP(w http.ResponseWriter, r *http.Request) {
	m := s.mailer()
	if m == nil {
		writeErr(w, 503, "SMTP가 설정되지 않았습니다 (KEEL_SMTP_HOST, KEEL_SMTP_FROM)")
		return
	}
	var body struct {
		To string `json:"to"`
	}
	if !decode(w, r, &body) {
		return
	}
	to := strings.TrimSpace(body.To)
	if to == "" {
		if p := principalFrom(r); p != nil && !p.User.Builtin {
			to = p.User.Email
		}
	}
	if !notify.ValidAddress(to) {
		writeErr(w, 400, "to: 올바른 이메일이 필요합니다")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 60*time.Second)
	defer cancel()
	err := m.Send(ctx, []string{to}, "[aven] SMTP 설정 테스트", "aven에서 보낸 SMTP 테스트 메일입니다.\n이 메일을 받았다면 메일 설정이 올바릅니다.\n")
	if err != nil {
		writeErr(w, 502, "메일을 보내지 못했습니다: "+err.Error())
		return
	}
	s.audit(r, "smtp.tested", "", to+"로 SMTP 테스트 메일 전송")
	writeJSON(w, 200, map[string]any{"ok": true, "to": to})
}

// ---- 사용자 알림함 ----

func (s *Server) getNotificationPrefs(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	ev, err := s.Store.UserNotificationEvents(r.Context(), p.User.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"events": ev, "defaults": store.DefaultNotificationEvents, "available": notify.KnownKinds})
}

func (s *Server) putNotificationPrefs(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	var body struct {
		Events *[]string `json:"events"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Events == nil {
		writeErr(w, 400, "events is required")
		return
	}
	if msg := validEventPatterns(*body.Events); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	if err := s.Store.SetUserNotificationEvents(r.Context(), p.User.ID, *body.Events); err != nil {
		s.storeErr(w, err)
		return
	}
	s.getNotificationPrefs(w, r)
}

// listNotifications는 내 알림이다. 지금은 볼 수 없는 프로젝트(멤버에서 빠짐, 범위 토큰 밖)의 알림은 거른다.
func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	limit := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= store.MaxUserNotifications {
		limit = v
	}
	unread := r.URL.Query().Get("unread")
	ns, count, err := s.Store.ListUserNotifications(r.Context(), p.User.ID, unread == "1" || unread == "true", limit)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	vis := map[string]bool{}
	out := []*store.UserNotification{}
	for _, n := range ns {
		ok, seen := vis[n.ProjectID]
		if !seen {
			ok = s.visibleLevel(r, n.ProjectID) > lvNone
			vis[n.ProjectID] = ok
		}
		if ok {
			out = append(out, n)
		}
	}
	writeJSON(w, 200, map[string]any{"notifications": out, "unread": count})
}

func (s *Server) readNotifications(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	var body struct {
		IDs []string `json:"ids"`
		All bool     `json:"all"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !body.All && len(body.IDs) == 0 {
		writeErr(w, 400, "ids or all is required")
		return
	}
	if len(body.IDs) > 500 {
		writeErr(w, 400, "too many ids")
		return
	}
	ids := body.IDs
	if body.All {
		ids = nil
	}
	n, err := s.Store.MarkNotificationsRead(r.Context(), p.User.ID, ids)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"updated": n})
}
