package api

import (
	"context"
	"encoding/base64"
	"io"
	"mime"
	"net"
	"net/mail"
	"slices"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/notify"
	"github.com/turin-dev/aven/internal/store"
)

func TestServiceAdminTabNeedsServerAdmin(t *testing.T) {
	f, h := authServer(t)
	ctx := context.Background()
	_, bossC := freshAccount(t, f, "svc-admin-boss@example.com", store.RoleOwner)
	_, operatorC := freshAccount(t, f, "svc-admin-operator@example.com", store.RoleAdmin)
	tenant, tenantC := freshAccount(t, f, "svc-admin-tenant@example.com", store.RoleViewer)
	ws, err := f.st.CreateWorkspace(ctx, "Tenant", tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	p := &store.Project{Name: "tenant-app", WorkspaceID: ws.ID}
	env, err := f.st.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	svc := &store.Service{ProjectID: p.ID, Name: "web", SourceType: "image"}
	if err := f.st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	in := &store.Instance{ServiceID: svc.ID, EnvironmentID: env.ID, Status: store.InstOnline}
	if err := f.st.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := f.st.CreateAudit(ctx, &store.AuditEntry{Kind: "project.test", Target: p.ID, Message: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.CreateAudit(ctx, &store.AuditEntry{Kind: "other.test", Target: "elsewhere"}); err != nil {
		t.Fatal(err)
	}
	path := "/api/services/" + svc.ID + "/instances/" + env.ID + "/admin"
	// 작업 공간 owner라도 서버 관리자가 아니면 볼 수 없다.
	if w := req(h, "GET", path, "", tenantC); w.Code != 403 {
		t.Fatalf("workspace owner: %d %s", w.Code, w.Body)
	}
	// 프로젝트 멤버십이 없는 서버 admin은 프로젝트 자체가 보이지 않는다.
	if w := req(h, "GET", path, "", operatorC); w.Code != 404 && w.Code != 403 {
		t.Fatalf("server admin without membership: %d %s", w.Code, w.Body)
	}
	w := req(h, "GET", path, "", bossC)
	if w.Code != 200 {
		t.Fatalf("server owner: %d %s", w.Code, w.Body)
	}
	v := decodeBody[serviceAdminView](t, w)
	if v.Workspace.ID != ws.ID || v.Workspace.Name != "Tenant" || v.Project.ID != p.ID || v.InstanceID != in.ID || v.ServiceID != svc.ID {
		t.Fatalf("ids: %+v", v)
	}
	if len(v.Audit) != 1 || v.Audit[0].Kind != "project.test" || v.AuditHidden {
		t.Fatalf("audit: %+v", v.Audit)
	}
	if v.Containers == nil || v.Holds == nil {
		t.Fatalf("nil lists: %+v", v)
	}
}

func TestServiceAdminHoldsBlockDeploys(t *testing.T) {
	f, h := authServer(t)
	ctx := context.Background()
	_, bossC := freshAccount(t, f, "svc-hold-boss@example.com", store.RoleOwner)
	operator, operatorC := freshAccount(t, f, "svc-hold-operator@example.com", store.RoleAdmin)
	wsAdmin, wsAdminC := freshAccount(t, f, "svc-hold-wsadmin@example.com", store.RoleMember)
	proj, _ := f.st.GetProject(ctx, f.p.ID)
	for uid, role := range map[string]string{operator.ID: store.RoleAdmin, wsAdmin.ID: store.RoleAdmin} {
		if err := f.st.SetWorkspaceMember(ctx, proj.WorkspaceID, uid, role); err != nil {
			t.Fatal(err)
		}
	}
	base := "/api/services/" + f.web.ID
	deployPath := base + "/instances/" + f.prod.ID + "/deploy"
	block := `{"scope":"deploy","reason":"incident"}`
	// 작업 공간 admin은 서버 관리자가 아니다.
	if w := req(h, "POST", base+"/admin/holds", block, wsAdminC); w.Code != 403 {
		t.Fatalf("workspace admin: %d %s", w.Code, w.Body)
	}
	if w := req(h, "POST", base+"/admin/holds", `{"scope":"project"}`, operatorC); w.Code != 400 {
		t.Fatalf("project scope from service route: %d", w.Code)
	}
	if w := req(h, "POST", base+"/admin/holds", block, operatorC); w.Code != 200 {
		t.Fatalf("block: %d %s", w.Code, w.Body)
	}
	if w := req(h, "POST", base+"/admin/holds", block, operatorC); w.Code != 409 {
		t.Fatalf("double block: %d", w.Code)
	}
	// 서버 owner도 막힌 동안에는 배포할 수 없다. 멤버에게도 차단이 보인다.
	if w := req(h, "POST", deployPath, "", bossC); w.Code != 409 || !strings.Contains(w.Body.String(), "incident") {
		t.Fatalf("deploy while blocked: %d %s", w.Code, w.Body)
	}
	inst := decodeBody[map[string]any](t, req(h, "GET", base+"/instances/"+f.prod.ID, "", wsAdminC))
	if hold, _ := inst["admin_hold"].(map[string]any); hold == nil || hold["scope"] != "deploy" {
		t.Fatalf("admin_hold: %v", inst["admin_hold"])
	}
	view := decodeBody[serviceAdminView](t, req(h, "GET", base+"/instances/"+f.prod.ID+"/admin", "", operatorC))
	if !view.CanControl || len(view.Holds) != 1 || view.Holds[0].Scope != store.HoldDeploy {
		t.Fatalf("admin view: %+v", view)
	}
	if w := req(h, "DELETE", base+"/admin/holds/deploy", "", wsAdminC); w.Code != 403 {
		t.Fatalf("workspace admin unblock: %d", w.Code)
	}
	if w := req(h, "DELETE", base+"/admin/holds/deploy", "", operatorC); w.Code != 200 {
		t.Fatalf("unblock: %d %s", w.Code, w.Body)
	}
	if w := req(h, "POST", deployPath, "", bossC); w.Code != 202 {
		t.Fatalf("deploy after unblock: %d %s", w.Code, w.Body)
	}
	// 서비스 정지도 배포를 막는다.
	if w := req(h, "POST", base+"/admin/holds", `{"scope":"service"}`, operatorC); w.Code != 200 {
		t.Fatalf("service hold: %d %s", w.Code, w.Body)
	}
	if w := req(h, "POST", deployPath, "", bossC); w.Code != 409 {
		t.Fatalf("deploy while service held: %d %s", w.Code, w.Body)
	}
	if w := req(h, "DELETE", base+"/admin/holds/service", "", operatorC); w.Code != 200 {
		t.Fatalf("service release: %d %s", w.Code, w.Body)
	}
	// owner가 기능을 막으면 서버 관리자는 쓸 수 없다(owner는 쓸 수 있다).
	if w := req(h, "PUT", "/api/owner/admin-caps", `{"disabled":["service_controls"]}`, bossC); w.Code != 200 {
		t.Fatalf("set caps: %d %s", w.Code, w.Body)
	}
	if w := req(h, "POST", base+"/admin/holds", block, operatorC); w.Code != 403 {
		t.Fatalf("withheld: %d", w.Code)
	}
	if w := req(h, "POST", base+"/admin/holds", block, bossC); w.Code != 200 {
		t.Fatalf("owner block: %d %s", w.Code, w.Body)
	}
	if len(auditKinds(t, f.st, "service.deploy_blocked")) != 2 || len(auditKinds(t, f.st, "service.deploy_unblocked")) != 1 {
		t.Fatal("not audited")
	}
	// 서비스를 지우면 기록도 지워진다.
	if err := f.st.DeleteService(ctx, f.web.ID); err != nil {
		t.Fatal(err)
	}
	if hs, _ := f.st.ListOwnerHolds(ctx); len(hs) != 0 {
		t.Fatalf("orphan holds: %+v", hs)
	}
}

func TestDeployBlockMailsWorkspaceAdmins(t *testing.T) {
	f, h := authServer(t)
	ctx := context.Background()
	_, bossC := freshAccount(t, f, "block-mail-boss@example.com", store.RoleOwner)
	wsAdmin, _ := freshAccount(t, f, "block-mail-admin@example.com", store.RoleMember)
	member, _ := freshAccount(t, f, "block-mail-member@example.com", store.RoleMember)
	proj, _ := f.st.GetProject(ctx, f.p.ID)
	for uid, role := range map[string]string{wsAdmin.ID: store.RoleAdmin, member.ID: store.RoleMember} {
		if err := f.st.SetWorkspaceMember(ctx, proj.WorkspaceID, uid, role); err != nil {
			t.Fatal(err)
		}
	}
	base := "/api/services/" + f.web.ID
	// SMTP가 없어도 차단은 된다.
	if w := req(h, "POST", base+"/admin/holds", `{"scope":"deploy"}`, bossC); w.Code != 200 || !strings.Contains(w.Body.String(), `"emailed":0`) {
		t.Fatalf("block without smtp: %d %s", w.Code, w.Body)
	}
	if w := req(h, "DELETE", base+"/admin/holds/deploy", "", bossC); w.Code != 200 {
		t.Fatalf("unblock: %d %s", w.Code, w.Body)
	}
	addr, sink := startSMTPSink(t)
	host, port, _ := net.SplitHostPort(addr)
	env := map[string]string{"KEEL_SMTP_HOST": host, "KEEL_SMTP_PORT": port, "KEEL_SMTP_FROM": "aven <noreply@example.com>", "KEEL_SMTP_TLS": "none"}
	m, err := notify.MailerFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	f.srv.Notifier = &notify.Notifier{Store: f.st, Mailer: m}
	f.srv.PublicURL = "https://aven.example.com"
	w := req(h, "POST", base+"/admin/holds", `{"scope":"deploy","reason":"결제 미납"}`, bossC)
	if w.Code != 200 {
		t.Fatalf("block: %d %s", w.Code, w.Body)
	}
	sink.mu.Lock()
	data, rcpt := sink.data, slices.Clone(sink.rcpt)
	sink.mu.Unlock()
	// 작업 공간 owner·admin만 받는다(일반 멤버는 받지 않는다).
	if slices.Contains(rcpt, "<"+member.Email+">") || !slices.Contains(rcpt, "<"+wsAdmin.Email+">") {
		t.Fatalf("recipients: %v", rcpt)
	}
	if n := decodeBody[struct {
		Emailed int `json:"emailed"`
	}](t, w).Emailed; n != len(rcpt) {
		t.Fatalf("emailed %d, rcpt %v", n, rcpt)
	}
	msg, err := mail.ReadMessage(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	subject, _ := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	raw, _ := io.ReadAll(msg.Body)
	text, _ := base64.StdEncoding.DecodeString(strings.ReplaceAll(string(raw), "\r\n", ""))
	if !strings.Contains(subject, "서비스 web의 배포가 막혔습니다") {
		t.Errorf("subject: %q", subject)
	}
	for _, want := range []string{"사유: 결제 미납", "https://aven.example.com/?project=" + f.p.ID} {
		if !strings.Contains(string(text), want) {
			t.Errorf("body missing %q:\n%s", want, text)
		}
	}
	if w := req(h, "DELETE", base+"/admin/holds/deploy", "", bossC); w.Code != 200 || strings.Contains(w.Body.String(), `"emailed":0`) {
		t.Fatalf("unblock: %d %s", w.Code, w.Body)
	}
	if len(auditKinds(t, f.st, "hold.emailed")) != 2 {
		t.Fatal("mail not audited")
	}
}
