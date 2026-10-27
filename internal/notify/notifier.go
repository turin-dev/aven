package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/events"
	"github.com/turin-dev/aven/internal/store"
)

// DefaultBackoff는 재시도 간격이다(최대 3번 시도).
var DefaultBackoff = []time.Duration{2 * time.Second, 8 * time.Second}

// Notifier는 events.Notifier 구현이다.
type Notifier struct {
	Store  *store.Store
	Log    *slog.Logger
	Guard  *Guard
	Mailer *Mailer // nil이면 이메일 채널은 실패로 기록된다
	// PublicURL은 이메일에 넣을 aven 링크다(비우면 넣지 않는다).
	PublicURL string
	Backoff   []time.Duration // nil이면 DefaultBackoff
	Timeout   time.Duration   // 시도마다(0이면 10초)

	client *http.Client
	inbox  InboxHub // 알림함 실시간 스트림(inboxhub.go)
}

var _ events.Notifier = (*Notifier)(nil)

func (n *Notifier) httpClient() *http.Client {
	if n.client == nil {
		t := n.Timeout
		if t <= 0 {
			t = 10 * time.Second
		}
		n.client = n.Guard.Client(t)
	}
	return n.client
}

func (n *Notifier) backoff() []time.Duration {
	if n.Backoff != nil {
		return n.Backoff
	}
	return DefaultBackoff
}

func (n *Notifier) warn(msg string, args ...any) {
	if n.Log != nil {
		n.Log.Warn(msg, args...)
	}
}

// Dispatch는 이벤트 하나를 사용자 알림함, 예전 프로젝트 webhook, 맞는 채널로 보낸다.
func (n *Notifier) Dispatch(e *store.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	p, err := n.Store.GetProject(ctx, e.ProjectID)
	if err != nil {
		return
	}
	if err := n.Fanout(ctx, e); err != nil {
		n.warn("notify inbox", "err", err)
	}
	pl := n.PayloadFor(ctx, e, p)
	// 예전 프로젝트 webhook(notify_webhook_url + notify_events)은 암묵적인 채널이다(Discord/Slack 겸용 본문).
	if p.NotifyWebhookURL != nil && *p.NotifyWebhookURL != "" {
		if lk, ok := events.LegacyKind(e.Kind); ok && Match(p.NotifyEvents, lk) {
			res := PostJSON(ctx, n.httpClient(), *p.NotifyWebhookURL, events.Payload(p.Name, lk, e.Message), nil, n.backoff())
			if res.Err != nil {
				n.warn("notify webhook (project)", "project", p.ID, "err", res.Err)
			}
		}
	}
	chans, err := n.Store.ChannelsForProject(ctx, e.ProjectID)
	if err != nil {
		return
	}
	for _, ch := range chans {
		if !Match(ch.Events, e.Kind) {
			continue
		}
		n.Deliver(ctx, ch, pl)
	}
}

// PayloadFor는 이벤트의 webhook payload다(환경·서비스 이름을 채운다).
func (n *Notifier) PayloadFor(ctx context.Context, e *store.Event, p *store.Project) *Payload {
	pl := &Payload{ID: e.ID, Kind: e.Kind, Message: e.Message, Project: Ref{ID: p.ID, Name: p.Name}, DeploymentID: e.DeploymentID,
		CreatedAt: e.CreatedAt, Actor: e.Actor}
	if e.EnvironmentID != nil {
		ref := &Ref{ID: *e.EnvironmentID}
		if env, err := n.Store.GetEnvironment(ctx, *e.EnvironmentID); err == nil {
			ref.Name = env.Name
		}
		pl.Environment = ref
	}
	if e.ServiceID != nil {
		ref := &Ref{ID: *e.ServiceID}
		if e.ServiceName != nil {
			ref.Name = *e.ServiceName
		}
		pl.Service = ref
	}
	return pl
}

// Deliver는 채널로 보내고 전달 기록을 남긴다.
func (n *Notifier) Deliver(ctx context.Context, ch *store.NotificationChannel, pl *Payload) Result {
	var res Result
	switch ch.Kind {
	case store.ChannelEmail:
		start := time.Now()
		res.Attempts = 1
		if n.Mailer == nil {
			res.Err = errors.New("SMTP가 설정되지 않았습니다 (AVEN_SMTP_HOST)")
		} else {
			subject, body := EmailFor(pl, n.PublicURL)
			res.Err = n.Mailer.Send(ctx, ch.Config.Recipients, subject, body)
		}
		res.Duration = time.Since(start)
	default:
		body := Body(ch.Kind, pl)
		sig := ""
		if ch.Config.Secret != "" {
			sig = Sign(ch.Config.Secret, body)
		}
		headers := map[string]string{
			"X-Aven-Event": pl.Kind, "X-Aven-Delivery": pl.ID,
			"X-Keel-Event": pl.Kind, "X-Keel-Delivery": pl.ID,
		}
		if sig != "" {
			headers["X-Aven-Signature"] = sig
			headers["X-Keel-Signature"] = sig
		}
		res = PostJSON(ctx, n.httpClient(), ch.Config.URL, body, headers, n.backoff())
	}
	d := &store.Delivery{ChannelID: ch.ID, EventID: pl.ID, EventKind: pl.Kind, Status: "ok", Attempts: res.Attempts, StatusCode: res.StatusCode,
		DurationMS: res.Duration.Milliseconds()}
	if res.Err != nil {
		d.Status, d.Error = "failed", truncate(res.Err.Error(), 500)
		n.warn("notify channel", "channel", ch.ID, "kind", ch.Kind, "err", res.Err)
	}
	if err := n.Store.AddDelivery(context.WithoutCancel(ctx), d); err != nil {
		n.warn("notify delivery log", "err", err)
	}
	return res
}

// Test는 채널로 시험 이벤트를 보낸다(전달 기록에 남는다).
func (n *Notifier) Test(ctx context.Context, ch *store.NotificationChannel, p *store.Project, actor string) Result {
	pl := &Payload{ID: store.NewID("evt"), Kind: "notification.test", Message: "aven 알림 채널 " + ch.Name + " 테스트입니다", CreatedAt: time.Now().UTC()}
	if p != nil {
		pl.Project = Ref{ID: p.ID, Name: p.Name}
	} else {
		pl.Project = Ref{Name: "workspace"}
	}
	if actor != "" {
		pl.Actor = &actor
	}
	return n.Deliver(ctx, ch, pl)
}

// EmailFor는 이벤트 알림 메일의 제목과 본문이다.
func EmailFor(pl *Payload, publicURL string) (string, string) {
	subject := fmt.Sprintf("[aven] %s · %s", pl.Project.Name, pl.Kind)
	var b strings.Builder
	b.WriteString(pl.Message + "\n\n")
	b.WriteString("프로젝트: " + pl.Project.Name + "\n")
	if pl.Environment != nil {
		b.WriteString("환경: " + nonEmpty(pl.Environment.Name, pl.Environment.ID) + "\n")
	}
	if pl.Service != nil {
		b.WriteString("서비스: " + nonEmpty(pl.Service.Name, pl.Service.ID) + "\n")
	}
	if pl.DeploymentID != nil {
		b.WriteString("배포: " + *pl.DeploymentID + "\n")
	}
	b.WriteString("이벤트: " + pl.Kind + "\n")
	b.WriteString("시각: " + pl.CreatedAt.UTC().Format("2006-01-02 15:04:05 UTC") + "\n")
	if pl.Actor != nil {
		b.WriteString("실행한 사람: " + *pl.Actor + "\n")
	}
	if publicURL != "" && pl.Project.ID != "" {
		b.WriteString("\n" + strings.TrimRight(publicURL, "/") + "/?project=" + pl.Project.ID + "\n")
	}
	b.WriteString("\n-- \n이 메일은 aven 알림 채널 설정에 따라 자동으로 보냈습니다.\n")
	return subject, b.String()
}

// InviteEmail은 초대 메일의 제목과 본문이다.
func InviteEmail(inviter, link string, expires time.Time) (string, string) {
	subject := "[aven] " + inviter + "님이 aven 작업 공간에 초대했습니다"
	body := fmt.Sprintf("%s님이 aven 작업 공간에 초대했습니다.\n\n아래 링크에서 이름과 비밀번호를 정하면 가입이 끝납니다.\n%s\n\n이 링크는 %s까지 쓸 수 있고 한 번만 쓸 수 있습니다.\n초대받을 일이 없다면 이 메일을 무시하세요.\n",
		inviter, link, expires.UTC().Format("2006-01-02 15:04 UTC"))
	return subject, body
}

// HoldEmail은 서버 운영자가 정지(또는 배포 차단)를 걸거나 풀었을 때 작업 공간 관리자에게 보내는 메일의 제목과 본문이다.
// target은 "서비스 web의 배포"처럼 대상을 가리키는 말이고, deploy는 배포 차단(실행 중인 서비스는 그대로)인지다.
func HoldEmail(placed, deploy bool, target, reason, by, link string) (string, string) {
	var subject, body string
	switch {
	case placed && deploy:
		subject = "[aven] " + target + "가 막혔습니다"
		body = target + "가 서버 운영자에 의해 막혔습니다.\n실행 중인 서비스는 그대로 돌지만, 차단이 풀릴 때까지 새 배포(GitHub 푸시 포함)는 시작되지 않습니다.\n"
	case placed:
		subject = "[aven] " + target + "가 정지되었습니다"
		body = target + "가 서버 운영자에 의해 정지되었습니다.\n실행 중이던 서비스를 멈췄고, 정지가 풀릴 때까지 배포·재시작을 할 수 없습니다.\n"
	case deploy:
		subject = "[aven] " + target + " 차단이 풀렸습니다"
		body = target + " 차단이 풀렸습니다. 이제 다시 배포할 수 있습니다.\n"
	default:
		subject = "[aven] " + target + "의 정지가 풀렸습니다"
		body = target + "의 정지가 풀렸습니다. 정지 때 멈춘 서비스를 다시 시작합니다.\n"
	}
	body += "\n"
	if placed && reason != "" {
		body += "사유: " + reason + "\n"
	}
	if by != "" {
		body += "처리한 사람: " + by + "\n"
	}
	if link != "" {
		body += link + "\n"
	}
	body += "\n이 메일은 작업 공간 소유자와 관리자에게 보냅니다. 문의는 서버 운영자에게 하세요.\n"
	return subject, body
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func workspaceAdmin(role string) bool { return role == store.RoleOwner || role == store.RoleAdmin }

// Fanout은 이벤트를 받을 사용자(프로젝트가 보이고 그 kind를 구독한)의 알림함에 넣는다.
func (n *Notifier) Fanout(ctx context.Context, e *store.Event) error {
	if e.Kind == "notification.test" {
		return nil
	}
	prefs, err := n.Store.AllUserNotificationEvents(ctx)
	if err != nil {
		return err
	}
	users, err := n.Store.ListUsers(ctx)
	if err != nil {
		return err
	}
	var to []string
	for _, u := range users {
		ev, ok := prefs[u.ID]
		if !ok || u.Disabled || !Match(ev, e.Kind) {
			continue
		}
		if e.ProjectID == "" {
			if !workspaceAdmin(u.Role) {
				continue
			}
		} else {
			// 프로젝트 작업 공간의 역할 + 직접 부여 + 접근 그룹(기본 작업 공간). 남의 개인 작업 공간 알림은 받지 않는다.
			role, err := n.Store.EffectiveProjectRole(ctx, e.ProjectID, u)
			if err != nil || role == "" {
				continue
			}
		}
		to = append(to, u.ID)
	}
	if err := n.Store.AddUserNotifications(ctx, e, to); err != nil {
		return err
	}
	n.inbox.publishEvent(e, to)
	return nil
}
