package api

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/notify"
	"github.com/turin-dev/aven/internal/store"
	"github.com/turin-dev/aven/internal/usage"
)

func TestNotificationChannelsAPI(t *testing.T) {
	f, h := authServer(t)
	legacy := withBearer("legacy-token")
	got := make(chan *http.Request, 4)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		got <- r
	}))
	defer hook.Close()
	f.srv.Notifier = &notify.Notifier{Store: f.st, Guard: &notify.Guard{}, Backoff: []time.Duration{}}
	base := "/api/projects/" + f.p.ID + "/notification-channels"

	for _, body := range []string{
		`{"kind":"sms","name":"x","url":"https://hooks.example.com/a"}`,
		`{"kind":"webhook","name":"","url":"https://hooks.example.com/a"}`,
		`{"kind":"webhook","name":"x","url":"ftp://hooks.example.com/a"}`,
		`{"kind":"webhook","name":"x","url":"https://user:pw@hooks.example.com/a"}`,
		`{"kind":"webhook","name":"x","url":"http://127.0.0.1:8080/a"}`,
		`{"kind":"webhook","name":"x","url":"http://[::1]/a"}`,
		`{"kind":"webhook","name":"x","url":"http://169.254.169.254/latest"}`,
		`{"kind":"webhook","name":"x","url":"http://localhost/a"}`,
		`{"kind":"webhook","name":"x","url":"https://hooks.example.com/a","events":["deploy failed"]}`,
		`{"kind":"email","name":"x","recipients":[]}`,
		`{"kind":"email","name":"x","recipients":["not an email"]}`,
	} {
		if w := req(h, "POST", base, body, legacy); w.Code != 400 {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	w := req(h, "POST", base, `{"kind":"webhook","name":"ci","url":"https://hooks.example.com/abc?token=t0p","secret":"hmac-key","events":["deploy.*","alert.firing"]}`, legacy)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "hmac-key") {
		t.Error("secret echoed")
	}
	ch := decodeBody[struct {
		ID        string   `json:"id"`
		URL       string   `json:"url"`
		SecretSet bool     `json:"secret_set"`
		Events    []string `json:"events"`
		Enabled   bool     `json:"enabled"`
	}](t, w)
	if !ch.SecretSet || !ch.Enabled || ch.URL != "https://hooks.example.com/abc?token=t0p" || len(ch.Events) != 2 {
		t.Errorf("created: %+v", ch)
	}
	// viewer는 목록은 보지만 URL은 가려진다
	viewer := testUser(t, f.st, "nv@example.com", store.RoleViewer)
	f.st.SetMember(f.ctx, f.p.ID, viewer.ID, store.RoleViewer)
	vc := withCookie(testSessionCookie(t, f.srv, viewer))
	if body := req(h, "GET", base, "", vc).Body.String(); strings.Contains(body, "t0p") || !strings.Contains(body, MaskedValue) || strings.Contains(body, "hmac-key") {
		t.Errorf("viewer list leaks: %s", body)
	}
	if w := req(h, "PATCH", base+"/"+ch.ID, `{"enabled":false}`, vc); w.Code != 403 {
		t.Errorf("viewer patch: %d", w.Code)
	}
	// 기본 이벤트, email
	w = req(h, "POST", base, `{"kind":"email","name":"ops","recipients":["ops@example.com"," dev@example.com "]}`, legacy)
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"recipients":["ops@example.com","dev@example.com"]`) || !strings.Contains(w.Body.String(), `"deploy.failed"`) {
		t.Errorf("email channel: %d %s", w.Code, w.Body)
	}
	// 작업 공간 채널과 프로젝트 채널은 서로의 경로로 보이지 않는다
	w = req(h, "POST", "/api/notification-channels", `{"kind":"discord","name":"all","url":"https://discord.example/api/webhooks/1/x","events":["*"]}`, legacy)
	if w.Code != 201 {
		t.Fatalf("workspace channel: %d %s", w.Code, w.Body)
	}
	wsID := decodeBody[struct {
		ID string `json:"id"`
	}](t, w).ID
	if w := req(h, "PATCH", "/api/notification-channels/"+ch.ID, `{"name":"x"}`, legacy); w.Code != 404 {
		t.Errorf("project channel via workspace route: %d", w.Code)
	}
	if w := req(h, "DELETE", base+"/"+wsID, "", legacy); w.Code != 404 {
		t.Errorf("workspace channel via project route: %d", w.Code)
	}
	if list := decodeBody[[]map[string]any](t, req(h, "GET", "/api/notification-channels", "", legacy)); len(list) != 1 || list[0]["project_id"] != nil {
		t.Errorf("workspace list: %v", list)
	}
	// patch: kind는 못 바꾸고, secret ""는 서명 키를 지운다
	if w := req(h, "PATCH", base+"/"+ch.ID, `{"kind":"slack"}`, legacy); w.Code != 400 {
		t.Errorf("kind change: %d", w.Code)
	}
	w = req(h, "PATCH", base+"/"+ch.ID, `{"secret":"","events":["*"],"name":"ci2"}`, legacy)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"secret_set":false`) || !strings.Contains(w.Body.String(), `"name":"ci2"`) {
		t.Errorf("patch: %d %s", w.Code, w.Body)
	}
	// 시험 전송: 루프백 목적지는 SSRF 방지로 실패하고 전달 기록에 남는다
	c, _ := f.st.GetChannel(f.ctx, ch.ID)
	c.Config.URL, c.Config.Secret = hook.URL, "k2"
	f.st.UpdateChannel(f.ctx, c)
	tr := decodeBody[map[string]any](t, req(h, "POST", base+"/"+ch.ID+"/test", "", legacy))
	if tr["ok"] != false || !strings.Contains(fmt.Sprint(tr["error"]), "사설") || len(got) != 0 {
		t.Errorf("test to loopback: %v", tr)
	}
	f.srv.Notifier.Guard.AllowPrivate = true
	tr = decodeBody[map[string]any](t, req(h, "POST", base+"/"+ch.ID+"/test", "", legacy))
	if tr["ok"] != true || tr["attempts"] != float64(1) {
		t.Errorf("test: %v", tr)
	}
	r := <-got
	if r.Header.Get("X-Keel-Event") != "notification.test" || !strings.HasPrefix(r.Header.Get("X-Keel-Signature"), "sha256=") {
		t.Errorf("test request headers: %v", r.Header)
	}
	ds := decodeBody[[]store.Delivery](t, req(h, "GET", base+"/"+ch.ID+"/deliveries", "", legacy))
	if len(ds) != 2 || ds[0].Status != "ok" || ds[1].Status != "failed" {
		t.Errorf("deliveries: %+v", ds)
	}
	if w := req(h, "GET", base+"/"+ch.ID+"/deliveries", "", vc); w.Code != 403 {
		t.Errorf("viewer deliveries: %d", w.Code)
	}
	if w := req(h, "DELETE", base+"/"+ch.ID, "", legacy); w.Code != 204 {
		t.Errorf("delete: %d", w.Code)
	}
	if w := req(h, "GET", base+"/"+ch.ID+"/deliveries", "", legacy); w.Code != 404 {
		t.Errorf("deliveries after delete: %d", w.Code)
	}
}

func TestNotificationInboxAPI(t *testing.T) {
	f, h := authServer(t)
	u := testUser(t, f.st, "inbox@example.com", store.RoleMember)
	f.st.SetMember(f.ctx, f.p.ID, u.ID, store.RoleMember)
	c := withCookie(testSessionCookie(t, f.srv, u))
	prefs := decodeBody[struct {
		Events    []string `json:"events"`
		Available []string `json:"available"`
	}](t, req(h, "GET", "/api/auth/notification-preferences", "", c))
	if strings.Join(prefs.Events, ",") != strings.Join(store.DefaultNotificationEvents, ",") || len(prefs.Available) < 30 {
		t.Errorf("default prefs: %+v", prefs)
	}
	if w := req(h, "PUT", "/api/auth/notification-preferences", `{"events":["deploy failed"]}`, c); w.Code != 400 {
		t.Errorf("bad pattern: %d", w.Code)
	}
	if w := req(h, "PUT", "/api/auth/notification-preferences", `{"events":["deploy.*"]}`, c); w.Code != 200 || !strings.Contains(w.Body.String(), `"events":["deploy.*"]`) {
		t.Errorf("put prefs: %d %s", w.Code, w.Body)
	}
	n := &notify.Notifier{Store: f.st}
	for _, k := range []string{"deploy.failed", "deploy.live", "alert.firing"} {
		e := &store.Event{ProjectID: f.p.ID, Kind: k, Message: k + " 메시지"}
		f.st.CreateEvent(f.ctx, e)
		n.Fanout(f.ctx, e)
	}
	type inbox struct {
		Notifications []store.UserNotification `json:"notifications"`
		Unread        int                      `json:"unread"`
	}
	in := decodeBody[inbox](t, req(h, "GET", "/api/notifications?unread=1", "", c))
	if len(in.Notifications) != 2 || in.Unread != 2 || in.Notifications[0].Kind != "deploy.live" {
		t.Fatalf("inbox: %+v", in)
	}
	if w := req(h, "POST", "/api/notifications/read", `{}`, c); w.Code != 400 {
		t.Errorf("empty read: %d", w.Code)
	}
	w := req(h, "POST", "/api/notifications/read", `{"ids":["`+in.Notifications[0].ID+`"]}`, c)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"updated":1`) {
		t.Errorf("read one: %d %s", w.Code, w.Body)
	}
	if in := decodeBody[inbox](t, req(h, "GET", "/api/notifications", "", c)); len(in.Notifications) != 2 || in.Unread != 1 || in.Notifications[0].ReadAt == nil {
		t.Errorf("after read: %+v", in)
	}
	req(h, "POST", "/api/notifications/read", `{"all":true}`, c)
	if in := decodeBody[inbox](t, req(h, "GET", "/api/notifications?unread=1", "", c)); len(in.Notifications) != 0 || in.Unread != 0 {
		t.Errorf("after read all: %+v", in)
	}
	// 프로젝트에서 빠지면 그 프로젝트의 알림은 보이지 않는다
	f.st.RemoveMember(f.ctx, f.p.ID, u.ID)
	if in := decodeBody[inbox](t, req(h, "GET", "/api/notifications", "", c)); len(in.Notifications) != 0 {
		t.Errorf("notifications of a project no longer visible: %+v", in)
	}
}

func TestAlertRulesAPI(t *testing.T) {
	f, h := authServer(t)
	legacy := withBearer("legacy-token")
	base := "/api/projects/" + f.p.ID + "/alert-rules"
	other := &store.Project{Name: "other"}
	otherEnv, _ := f.st.CreateProject(f.ctx, other)
	otherSvc := &store.Service{ProjectID: other.ID, Name: "x", SourceType: "image"}
	f.st.CreateService(f.ctx, otherSvc)
	for _, body := range []string{
		`{"metric":"load","threshold":1}`,
		`{"metric":"cpu_percent"}`,
		`{"metric":"cpu_percent","threshold":1,"comparator":">="}`,
		`{"metric":"cpu_percent","threshold":-1}`,
		`{"metric":"cpu_percent","threshold":1,"for_seconds":999999}`,
		`{"metric":"cpu_percent","threshold":1,"service_id":"` + otherSvc.ID + `"}`,
		`{"metric":"cpu_percent","threshold":1,"environment_id":"` + otherEnv.ID + `"}`,
	} {
		if w := req(h, "POST", base, body, legacy); w.Code != 400 {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	w := req(h, "POST", base, `{"name":"CPU 높음","metric":"cpu_percent","comparator":">","threshold":80,"for_seconds":120,"service_id":"`+f.web.ID+`","environment_id":"`+f.prod.ID+`"}`, legacy)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	rule := decodeBody[store.AlertRule](t, w)
	if rule.LastState != "ok" || rule.ForSeconds != 120 || !rule.Enabled || *rule.ServiceID != f.web.ID {
		t.Errorf("rule: %+v", rule)
	}
	w = req(h, "PATCH", base+"/"+rule.ID, `{"threshold":90,"service_id":null,"enabled":false}`, legacy)
	if w.Code != 200 {
		t.Fatalf("patch: %d %s", w.Code, w.Body)
	}
	if r := decodeBody[store.AlertRule](t, w); r.Threshold != 90 || r.ServiceID != nil || r.Enabled || r.EnvironmentID == nil {
		t.Errorf("patched: %+v", r)
	}
	if rs := decodeBody[[]store.AlertRule](t, req(h, "GET", base, "", legacy)); len(rs) != 1 {
		t.Errorf("list: %+v", rs)
	}
	// 다른 프로젝트 경로로는 보이지 않는다
	if w := req(h, "DELETE", "/api/projects/"+other.ID+"/alert-rules/"+rule.ID, "", legacy); w.Code != 404 {
		t.Errorf("cross-project delete: %d", w.Code)
	}
	if w := req(h, "DELETE", base+"/"+rule.ID, "", legacy); w.Code != 204 {
		t.Errorf("delete: %d", w.Code)
	}
}

func TestUsageAPI(t *testing.T) {
	f, h := authServer(t)
	legacy := withBearer("legacy-token")
	day := time.Date(2025, 3, 10, 0, 0, 0, 0, time.UTC).Unix()
	rows := []store.UsageRow{
		{ProjectID: f.p.ID, EnvironmentID: f.prod.ID, ServiceID: f.web.ID, Hour: day, CPUCoreSeconds: 3600, MemGBSeconds: 7200, EgressBytes: 2e9, DiskGBHours: 1},
		{ProjectID: f.p.ID, EnvironmentID: f.prod.ID, ServiceID: f.web.ID, Hour: day + 3600, CPUCoreSeconds: 1800, MemGBSeconds: 3600},
		{ProjectID: f.p.ID, EnvironmentID: f.prod.ID, ServiceID: f.db.ID, Hour: day + 86400, CPUCoreSeconds: 3600},
		{ProjectID: f.p.ID, EnvironmentID: f.prod.ID, ServiceID: f.db.ID, Hour: day - 86400, CPUCoreSeconds: 99999}, // 범위 밖
	}
	if err := f.st.UpsertUsage(f.ctx, rows); err != nil {
		t.Fatal(err)
	}
	type resp struct {
		Totals usage.Totals        `json:"totals"`
		Groups []usage.Group       `json:"groups"`
		Series []usage.SeriesPoint `json:"series"`
	}
	base := "/api/projects/" + f.p.ID + "/usage?from=2025-03-10&to=2025-03-11"
	r := decodeBody[resp](t, req(h, "GET", base, "", legacy))
	if !near(r.Totals.CPUCoreHours, 2.5) || !near(r.Totals.MemoryGBHours, 3) || !near(r.Totals.EgressGB, 2) || r.Totals.Cost != nil {
		t.Errorf("totals: %+v", r.Totals)
	}
	if len(r.Groups) != 2 || len(r.Series) != 2 || r.Series[0].T != "2025-03-10" || !near(r.Series[0].CPUCoreHours, 1.5) {
		t.Errorf("groups/series: %+v %+v", r.Groups, r.Series)
	}
	names := map[string]bool{}
	for _, g := range r.Groups {
		names[g.Name] = true
	}
	if !names["web"] || !names[f.db.Name] || len(names) != 2 {
		t.Errorf("group names: %v", names)
	}
	f.srv.Pricing = &usage.Pricing{VCPUHour: 0.1}
	r = decodeBody[resp](t, req(h, "GET", base+"&group=environment", "", legacy))
	if len(r.Groups) != 1 || r.Groups[0].Name != "production" || r.Totals.Cost == nil || !near(r.Totals.Cost.CPU, 0.25) {
		t.Errorf("environment group with pricing: %+v %+v", r.Groups, r.Totals.Cost)
	}
	if r := decodeBody[resp](t, req(h, "GET", base+"&group=day", "", legacy)); len(r.Groups) != 2 || r.Groups[1].Key != "2025-03-11" {
		t.Errorf("day group: %+v", r.Groups)
	}
	for _, q := range []string{"?group=node", "?from=yesterday", "?from=2025-03-10&to=2025-03-01", "?from=2020-01-01&to=2025-01-01"} {
		if w := req(h, "GET", "/api/projects/"+f.p.ID+"/usage"+q, "", legacy); w.Code != 400 {
			t.Errorf("%s: %d", q, w.Code)
		}
	}
	ws := decodeBody[struct {
		Projects []usage.Group `json:"projects"`
	}](t, req(h, "GET", "/api/usage?from=2025-03-10T00:00:00Z&to=2025-03-12T00:00:00Z", "", legacy))
	if len(ws.Projects) != 1 || ws.Projects[0].Name != "acme" || !near(ws.Projects[0].CPUCoreHours, 2.5) {
		t.Errorf("workspace usage: %+v", ws.Projects)
	}
	// 프로젝트 설정의 월간 사용량 알림 기준
	if w := req(h, "PATCH", "/api/projects/"+f.p.ID, `{"usage_alert":{"cpu_core_hours":-1}}`, legacy); w.Code != 400 {
		t.Errorf("negative usage alert: %d", w.Code)
	}
	w := req(h, "PATCH", "/api/projects/"+f.p.ID, `{"usage_alert":{"cpu_core_hours":100,"egress_gb":50}}`, legacy)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"usage_alert":{"cpu_core_hours":100,"egress_gb":50}`) {
		t.Errorf("set usage alert: %d %s", w.Code, w.Body)
	}
	w = req(h, "PATCH", "/api/projects/"+f.p.ID, `{"usage_alert":null}`, legacy)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"usage_alert":null`) {
		t.Errorf("clear usage alert: %d %s", w.Code, w.Body)
	}
}

func near(a, b float64) bool { d := a - b; return d < 1e-6 && d > -1e-6 }

// ---- 초대 메일 ----

type smtpSink struct {
	mu   sync.Mutex
	data string
	rcpt []string
}

func startSMTPSink(t *testing.T) (string, *smtpSink) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	s := &smtpSink{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				say := func(v string) { io.WriteString(c, v+"\r\n") }
				say("220 sink")
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					line = strings.TrimRight(line, "\r\n")
					switch u := strings.ToUpper(line); {
					case strings.HasPrefix(u, "EHLO"):
						say("250 sink")
					case strings.HasPrefix(u, "RCPT TO:"):
						s.mu.Lock()
						s.rcpt = append(s.rcpt, line[8:])
						s.mu.Unlock()
						say("250 ok")
					case u == "DATA":
						say("354 go")
						var b strings.Builder
						for {
							l, err := r.ReadString('\n')
							if err != nil || l == ".\r\n" {
								break
							}
							b.WriteString(l)
						}
						s.mu.Lock()
						s.data = b.String()
						s.mu.Unlock()
						say("250 ok")
					case u == "QUIT":
						say("221 bye")
						return
					default:
						say("250 ok")
					}
				}
			}(c)
		}
	}()
	return ln.Addr().String(), s
}

func TestInviteEmailAndSMTPTest(t *testing.T) {
	f, h := authServer(t)
	legacy := withBearer("legacy-token")
	f.srv.PublicURL = "https://aven.example.com"
	// SMTP가 없으면 초대는 만들되 email_sent=false
	w := req(h, "POST", "/api/invites", `{"email":"first@example.com","send_email":true}`, legacy)
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"email_sent":false`) {
		t.Errorf("invite without smtp: %d %s", w.Code, w.Body)
	}
	if w := req(h, "POST", "/api/system/smtp/test", `{"to":"a@example.com"}`, legacy); w.Code != 503 {
		t.Errorf("smtp test without config: %d", w.Code)
	}
	addr, sink := startSMTPSink(t)
	host, port, _ := net.SplitHostPort(addr)
	env := map[string]string{"KEEL_SMTP_HOST": host, "KEEL_SMTP_PORT": port, "KEEL_SMTP_FROM": "aven <noreply@example.com>", "KEEL_SMTP_TLS": "none"}
	m, err := notify.MailerFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	f.srv.Notifier = &notify.Notifier{Store: f.st, Mailer: m}
	w = req(h, "POST", "/api/invites", `{"email":"New.User@Example.com","send_email":true}`, legacy)
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"email_sent":true`) {
		t.Fatalf("invite: %d %s", w.Code, w.Body)
	}
	tok := decodeBody[struct {
		Token string `json:"token"`
	}](t, w).Token
	sink.mu.Lock()
	data, rcpt := sink.data, sink.rcpt
	sink.mu.Unlock()
	msg, err := mail.ReadMessage(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(msg.Body)
	text, _ := base64.StdEncoding.DecodeString(strings.ReplaceAll(string(raw), "\r\n", ""))
	if len(rcpt) != 1 || rcpt[0] != "<new.user@example.com>" || !strings.Contains(string(text), "https://aven.example.com/invite/#token="+tok) {
		t.Errorf("invite mail: %v\n%s", rcpt, text)
	}
	if w := req(h, "POST", "/api/system/smtp/test", `{"to":"bad"}`, legacy); w.Code != 400 {
		t.Errorf("smtp test bad to: %d", w.Code)
	}
	if w := req(h, "POST", "/api/system/smtp/test", `{"to":"ops@example.com"}`, legacy); w.Code != 200 {
		t.Errorf("smtp test: %d %s", w.Code, w.Body)
	}
}
