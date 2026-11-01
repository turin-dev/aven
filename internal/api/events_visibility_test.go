package api

import (
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/store"
)

func TestProjectEventsHideRestrictedEnvironments(t *testing.T) {
	f, h := authServer(t)
	ctx := f.ctx
	staging := &store.Environment{ProjectID: f.p.ID, Name: "staging", Kind: store.EnvStandard}
	if err := f.st.CreateEnvironment(ctx, staging); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetEnvironmentRestricted(ctx, staging.ID, true); err != nil {
		t.Fatal(err)
	}
	f.srv.Events.Emit(ctx, f.p.ID, &f.prod.ID, nil, "test.prod", "visible prod event")
	f.srv.Events.Emit(ctx, f.p.ID, &staging.ID, nil, "test.staging", "secret staging event")
	f.srv.Events.Emit(ctx, f.p.ID, nil, nil, "test.project", "project event")

	alice := testUser(t, f.st, "alice@example.com", store.RoleMember)
	if err := f.st.SetMember(ctx, f.p.ID, alice.ID, store.RoleMember); err != nil {
		t.Fatal(err)
	}
	w := req(h, "GET", "/api/projects/"+f.p.ID+"/events?limit=100", "", withCookie(testSessionCookie(t, f.srv, alice)))
	if w.Code != 200 {
		t.Fatalf("events: %d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if strings.Contains(body, "secret staging event") || !strings.Contains(body, "visible prod event") || !strings.Contains(body, "project event") {
		t.Errorf("member events: %s", body)
	}
	// The server admin (project admin level) still sees everything.
	if body := req(h, "GET", "/api/projects/"+f.p.ID+"/events?limit=100", "", withBearer("legacy-token")).Body.String(); !strings.Contains(body, "secret staging event") {
		t.Errorf("admin events: %s", body)
	}
}
