package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/store"
)

func TestCanvasGroupsAPI(t *testing.T) {
	f, h := authServer(t)
	legacy := withBearer("legacy-token")
	base := "/api/environments/" + f.prod.ID + "/groups"

	for _, body := range []string{
		`{"name":""}`,
		`{"name":"x","color":"chartreuse"}`,
		`{"name":"x","service_ids":["svc_nope"]}`,
		`{"name":"x","rect":{"x":0,"y":0,"w":-5,"h":1}}`,
		`{"name":`,
	} {
		if w := req(h, "POST", base, body, legacy); w.Code != 400 {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	w := req(h, "POST", base, fmt.Sprintf(`{"name":"backend","service_ids":[%q,%q],"rect":{"x":10,"y":20,"w":300,"h":200}}`, f.web.ID, f.db.ID), legacy)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	g := decodeBody[store.CanvasGroup](t, w)
	if g.ID == "" || g.Color != "gray" || len(g.ServiceIDs) != 2 || g.Rect == nil || g.Rect.W != 300 {
		t.Errorf("created: %+v", g)
	}
	// 다른 그룹으로 옮기면 원래 그룹에서 빠진다.
	w = req(h, "POST", base, fmt.Sprintf(`{"name":"data","color":"blue","service_ids":[%q]}`, f.db.ID), legacy)
	if w.Code != 201 {
		t.Fatalf("create 2: %d %s", w.Code, w.Body)
	}
	g2 := decodeBody[store.CanvasGroup](t, w)
	w = req(h, "PATCH", base+"/"+g.ID, `{"name":"api","color":"#112233"}`, legacy)
	if w.Code != 200 {
		t.Fatalf("patch: %d %s", w.Code, w.Body)
	}
	pg := decodeBody[store.CanvasGroup](t, w)
	if pg.Name != "api" || pg.Color != "#112233" || len(pg.ServiceIDs) != 1 || pg.ServiceIDs[0] != f.web.ID || pg.Rect == nil {
		t.Errorf("patched: %+v", pg)
	}
	if w := req(h, "PATCH", base+"/"+g.ID, `{"color":"nope"}`, legacy); w.Code != 400 {
		t.Errorf("bad patch: %d", w.Code)
	}

	// 캔버스 응답에 그룹이 들어 있다.
	canvas := decodeBody[struct {
		Groups []store.CanvasGroup `json:"groups"`
	}](t, req(h, "GET", "/api/environments/"+f.prod.ID+"/canvas", "", legacy))
	if len(canvas.Groups) != 2 || canvas.Groups[0].ID != g.ID || canvas.Groups[1].ServiceIDs[0] != f.db.ID {
		t.Errorf("canvas groups: %+v", canvas.Groups)
	}

	// 다른 환경 경로로는 보이지 않는다.
	staging := &store.Environment{ProjectID: f.p.ID, Name: "staging"}
	if err := f.st.CreateEnvironment(f.ctx, staging); err != nil {
		t.Fatal(err)
	}
	other := "/api/environments/" + staging.ID + "/groups/" + g.ID
	if w := req(h, "PATCH", other, `{"name":"x"}`, legacy); w.Code != 404 {
		t.Errorf("cross-env patch: %d", w.Code)
	}
	if w := req(h, "DELETE", other, "", legacy); w.Code != 404 {
		t.Errorf("cross-env delete: %d", w.Code)
	}
	if l := decodeBody[[]store.CanvasGroup](t, req(h, "GET", "/api/environments/"+staging.ID+"/groups", "", legacy)); len(l) != 0 {
		t.Errorf("staging list: %v", l)
	}

	// RBAC: viewer는 읽기만, member는 쓰기, 비멤버는 없는 것처럼.
	viewer := testUser(t, f.st, "gv@example.com", store.RoleViewer)
	member := testUser(t, f.st, "gm@example.com", store.RoleMember)
	outsider := testUser(t, f.st, "go@example.com", store.RoleMember)
	f.st.SetMember(f.ctx, f.p.ID, viewer.ID, store.RoleViewer)
	f.st.SetMember(f.ctx, f.p.ID, member.ID, store.RoleMember)
	vc, mc, oc := withCookie(testSessionCookie(t, f.srv, viewer)), withCookie(testSessionCookie(t, f.srv, member)), withCookie(testSessionCookie(t, f.srv, outsider))
	for _, c := range []struct {
		who          string
		auth         func(*http.Request)
		get, post    int
		patch, del   int
		canvasGroups bool
	}{
		{"viewer", vc, 200, 403, 403, 403, true},
		{"outsider", oc, 404, 404, 404, 404, false},
	} {
		if got := req(h, "GET", base, "", c.auth).Code; got != c.get {
			t.Errorf("%s GET: %d", c.who, got)
		}
		if got := req(h, "POST", base, `{"name":"v"}`, c.auth).Code; got != c.post {
			t.Errorf("%s POST: %d", c.who, got)
		}
		if got := req(h, "PATCH", base+"/"+g.ID, `{"name":"v"}`, c.auth).Code; got != c.patch {
			t.Errorf("%s PATCH: %d", c.who, got)
		}
		if got := req(h, "DELETE", base+"/"+g.ID, "", c.auth).Code; got != c.del {
			t.Errorf("%s DELETE: %d", c.who, got)
		}
		if body := req(h, "GET", "/api/environments/"+f.prod.ID+"/canvas", "", c.auth).Body.String(); strings.Contains(body, g.ID) != c.canvasGroups {
			t.Errorf("%s canvas groups visible=%v", c.who, !c.canvasGroups)
		}
	}
	w = req(h, "POST", base, `{"name":"m"}`, mc)
	if w.Code != 201 {
		t.Fatalf("member create: %d %s", w.Code, w.Body)
	}
	mg := decodeBody[store.CanvasGroup](t, w)
	if w := req(h, "PATCH", base+"/"+mg.ID, fmt.Sprintf(`{"service_ids":[%q]}`, f.other.ID), mc); w.Code != 200 {
		t.Errorf("member patch: %d %s", w.Code, w.Body)
	}
	if w := req(h, "DELETE", base+"/"+mg.ID, "", mc); w.Code != 204 {
		t.Errorf("member delete: %d", w.Code)
	}

	// 지운 서비스는 그룹 응답에서 빠진다.
	if err := f.st.DeleteService(f.ctx, f.db.ID); err != nil {
		t.Fatal(err)
	}
	for _, gg := range decodeBody[[]store.CanvasGroup](t, req(h, "GET", base, "", legacy)) {
		if gg.ID == g2.ID && len(gg.ServiceIDs) != 0 {
			t.Errorf("deleted service still listed: %v", gg.ServiceIDs)
		}
	}
	if w := req(h, "DELETE", base+"/"+g.ID, "", legacy); w.Code != 204 {
		t.Errorf("delete: %d", w.Code)
	}
	if w := req(h, "DELETE", base+"/"+g.ID, "", legacy); w.Code != 404 {
		t.Errorf("delete twice: %d", w.Code)
	}
}

func TestDashboardsAPI(t *testing.T) {
	f, h := authServer(t)
	legacy := withBearer("legacy-token")
	base := "/api/projects/" + f.p.ID + "/dashboards"
	env := f.prod.ID

	other := &store.Project{Name: "elsewhere"}
	otherEnv, err := f.st.CreateProject(f.ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	foreign := &store.Service{ProjectID: other.ID, Name: "foreign", SourceType: "image"}
	if err := f.st.CreateService(f.ctx, foreign); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"name":""}`,
		`{"name":"x","widgets":[{"type":"pie"}]}`,
		`{"name":"x","widgets":[{"type":"metric","metric":"cpu_percent"}]}`,
		fmt.Sprintf(`{"name":"x","widgets":[{"type":"metric","metric":"load","environment_id":%q}]}`, env),
		fmt.Sprintf(`{"name":"x","widgets":[{"type":"metric","metric":"cpu_percent","environment_id":%q}]}`, otherEnv.ID),
		fmt.Sprintf(`{"name":"x","widgets":[{"type":"http","environment_id":%q,"service_id":%q}]}`, env, foreign.ID),
		fmt.Sprintf(`{"name":"x","widgets":[{"type":"logs","environment_id":%q,"w":20}]}`, env),
	} {
		if w := req(h, "POST", base, body, legacy); w.Code != 400 {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	body := fmt.Sprintf(`{"name":"Ops","widgets":[
		{"type":"metric","title":"CPU","metric":"cpu_percent","environment_id":%[1]q,"range":"24h","w":12,"h":4},
		{"type":"http","environment_id":%[1]q,"service_id":%[2]q},
		{"type":"logs","environment_id":%[1]q,"service_id":%[2]q},
		{"type":"events"}]}`, env, f.web.ID)
	w := req(h, "POST", base, body, legacy)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	d := decodeBody[store.Dashboard](t, w)
	if d.ID == "" || len(d.Widgets) != 4 || d.Widgets[0].Range != "24h" || d.Widgets[1].Range != "6h" || d.Widgets[1].W != 6 || d.Widgets[3].ID == "" {
		t.Errorf("created: %+v", d)
	}
	if w := req(h, "GET", base+"/"+d.ID, "", legacy); w.Code != 200 || !strings.Contains(w.Body.String(), `"title":"CPU"`) {
		t.Errorf("get: %d %s", w.Code, w.Body)
	}
	// 위젯 순서 바꾸기(PATCH widgets 전체), 이름만 바꾸면 위젯은 그대로.
	ws := d.Widgets
	ws[0], ws[3] = ws[3], ws[0]
	w = req(h, "PATCH", base+"/"+d.ID, layoutJSON(t, map[string]any{"widgets": ws}), legacy)
	if w.Code != 200 {
		t.Fatalf("patch widgets: %d %s", w.Code, w.Body)
	}
	if pd := decodeBody[store.Dashboard](t, w); pd.Widgets[0].Type != "events" || pd.Widgets[0].ID != d.Widgets[0].ID {
		t.Errorf("reordered: %+v", pd.Widgets)
	}
	w = req(h, "PATCH", base+"/"+d.ID, `{"name":"Renamed"}`, legacy)
	if pd := decodeBody[store.Dashboard](t, w); w.Code != 200 || pd.Name != "Renamed" || len(pd.Widgets) != 4 {
		t.Errorf("rename: %d %+v", w.Code, pd)
	}
	if w := req(h, "PATCH", base+"/"+d.ID, `{"widgets":[{"type":"bogus"}]}`, legacy); w.Code != 400 {
		t.Errorf("bad patch: %d", w.Code)
	}
	// 다른 프로젝트 경로로는 보이지 않는다.
	if w := req(h, "GET", "/api/projects/"+other.ID+"/dashboards/"+d.ID, "", legacy); w.Code != 404 {
		t.Errorf("cross-project get: %d", w.Code)
	}
	if w := req(h, "DELETE", "/api/projects/"+other.ID+"/dashboards/"+d.ID, "", legacy); w.Code != 404 {
		t.Errorf("cross-project delete: %d", w.Code)
	}

	viewer := testUser(t, f.st, "dv@example.com", store.RoleViewer)
	member := testUser(t, f.st, "dm@example.com", store.RoleMember)
	outsider := testUser(t, f.st, "do@example.com", store.RoleMember)
	f.st.SetMember(f.ctx, f.p.ID, viewer.ID, store.RoleViewer)
	f.st.SetMember(f.ctx, f.p.ID, member.ID, store.RoleMember)
	vc, mc, oc := withCookie(testSessionCookie(t, f.srv, viewer)), withCookie(testSessionCookie(t, f.srv, member)), withCookie(testSessionCookie(t, f.srv, outsider))
	for _, c := range []struct {
		who                       string
		auth                      func(*http.Request)
		list, get, post, put, del int
	}{
		{"viewer", vc, 200, 200, 403, 403, 403},
		{"outsider", oc, 404, 404, 404, 404, 404},
	} {
		got := []int{
			req(h, "GET", base, "", c.auth).Code,
			req(h, "GET", base+"/"+d.ID, "", c.auth).Code,
			req(h, "POST", base, `{"name":"v"}`, c.auth).Code,
			req(h, "PATCH", base+"/"+d.ID, `{"name":"v"}`, c.auth).Code,
			req(h, "DELETE", base+"/"+d.ID, "", c.auth).Code,
		}
		want := []int{c.list, c.get, c.post, c.put, c.del}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: %v, want %v", c.who, got, want)
		}
	}
	w = req(h, "POST", base, `{"name":"mine"}`, mc)
	if w.Code != 201 {
		t.Fatalf("member create: %d %s", w.Code, w.Body)
	}
	md := decodeBody[store.Dashboard](t, w)
	if w := req(h, "PATCH", base+"/"+md.ID, `{"name":"mine2"}`, mc); w.Code != 200 {
		t.Errorf("member patch: %d", w.Code)
	}
	if w := req(h, "DELETE", base+"/"+md.ID, "", mc); w.Code != 204 {
		t.Errorf("member delete: %d", w.Code)
	}
	if l := decodeBody[[]store.Dashboard](t, req(h, "GET", base, "", legacy)); len(l) != 1 || l[0].ID != d.ID {
		t.Errorf("list: %+v", l)
	}
	if w := req(h, "DELETE", base+"/"+d.ID, "", legacy); w.Code != 204 {
		t.Errorf("delete: %d", w.Code)
	}
}

func layoutJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
