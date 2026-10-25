package api

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/store"
)

func TestEdgeSettingsAPI(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	s := &Server{Store: st, SessionKey: make([]byte, 32), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := s.Handler()
	p := &store.Project{Name: "edge"}
	env, err := st.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	svc := &store.Service{ProjectID: p.ID, Name: "web", SourceType: "image"}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	in := &store.Instance{ServiceID: svc.ID, EnvironmentID: env.ID}
	if err := st.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	owner := testUser(t, st, "owner@example.com", store.RoleOwner)
	c := withCookie(testSessionCookie(t, s, owner))
	base := "/api/services/" + svc.ID + "/instances/" + env.ID + "/edge"
	call := func(method, path, body string, want int) map[string]any {
		t.Helper()
		w := req(h, method, base+path, body, c)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "clearance_secret") {
			t.Fatalf("secret leaked: %s", w.Body)
		}
		return decodeBody[map[string]any](t, w)
	}

	v := call("GET", "", "", 200)
	if v["available"] != false || v["cdn"].(map[string]any)["honor_swr"] != true {
		t.Fatalf("defaults: %v", v)
	}

	call("PATCH", "", `{"cdn":{"enabled":true}}`, 400)
	call("POST", "/waf/under-attack", `{}`, 400)
	in.Domains = []store.Domain{{Host: "app.example.com"}}
	if err := st.UpdateInstanceConfig(ctx, in); err != nil {
		t.Fatal(err)
	}

	v = call("PATCH", "", `{"cdn":{"enabled":true,"default_ttl_seconds":14400,"html_generation":99},"waf":{"deny_ips":["203.0.113.7","203.0.113.0/24"],"rate_limit":{"requests":100,"window_seconds":60}}}`, 200)
	cdn := v["cdn"].(map[string]any)
	if cdn["enabled"] != true || cdn["default_ttl_seconds"] != 14400.0 || cdn["html_generation"] != 0.0 || cdn["html_caching"] != "auto" {
		t.Fatalf("cdn: %v", cdn)
	}
	// 키 일부만 바꾸면 나머지는 그대로다.
	v = call("PATCH", "", `{"cdn":{"honor_swr":false}}`, 200)
	if cdn := v["cdn"].(map[string]any); cdn["enabled"] != true || cdn["honor_swr"] != false || cdn["default_ttl_seconds"] != 14400.0 {
		t.Fatalf("partial: %v", cdn)
	}
	call("PATCH", "", `{"cdn":{"default_ttl_seconds":61}}`, 400)
	call("PATCH", "", `{"waf":{"allow_ips":["nope"]}}`, 400)

	v = call("POST", "/cdn/purge", `{"scope":"html"}`, 200)
	if cdn := v["cdn"].(map[string]any); cdn["html_generation"] != 1.0 || cdn["all_generation"] != 0.0 || cdn["last_purged_html"] == nil {
		t.Fatalf("purge html: %v", cdn)
	}
	v = call("POST", "/cdn/purge", `{"scope":"all"}`, 200)
	if cdn := v["cdn"].(map[string]any); cdn["all_generation"] != 1.0 || cdn["last_purged_all"] == nil {
		t.Fatalf("purge all: %v", cdn)
	}
	call("POST", "/cdn/purge", `{"scope":"page"}`, 400)

	v = call("POST", "/waf/under-attack", `{"duration":"1h"}`, 200)
	if v["under_attack_active"] != true || v["waf"].(map[string]any)["under_attack"].(map[string]any)["until"] == nil {
		t.Fatalf("under attack: %v", v)
	}
	call("POST", "/waf/under-attack", `{"duration":"2h"}`, 400)
	v = call("POST", "/waf/under-attack", `{"duration":"forever"}`, 200)
	if v["waf"].(map[string]any)["under_attack"].(map[string]any)["until"] != nil {
		t.Fatalf("forever: %v", v)
	}
	if v := call("DELETE", "/waf/under-attack", "", 200); v["under_attack_active"] != false {
		t.Fatalf("disable: %v", v)
	}
	saved, _ := st.GetEdgeSettings(ctx, in.ID)
	if saved.WAF.ClearanceSecret == "" {
		t.Fatal("clearance secret not generated")
	}

	v = call("PUT", "/rules", `{"version":1,"rules":[
	  {"description":"b","priority":20,"if":{"attr":"http.path","op":"matches","value":"/admin*"},"then":{"action":"block"}},
	  {"description":"a","priority":10,"if":{"attr":"ipv4.src","op":"in","value":["203.0.113.0/24"]},"then":{"action":"allow"}}]}`, 200)
	rules := v["rules"].(map[string]any)["rules"].([]any)
	if len(rules) != 2 || rules[0].(map[string]any)["description"] != "a" || !strings.HasPrefix(rules[0].(map[string]any)["id"].(string), "rul_") {
		t.Fatalf("rules: %v", rules)
	}
	w := req(h, "PUT", base+"/rules", `{"version":1,"rules":[{"priority":1,"if":{"attr":"http.host","op":"in","value":"a.com"},"then":{"action":"allow"}}]}`, c)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "rules[0].if.value") {
		t.Fatalf("diagnostic: %d %s", w.Code, w.Body)
	}
	// 검사에 실패하면 저장한 규칙은 그대로다.
	if v := call("GET", "", "", 200); len(v["rules"].(map[string]any)["rules"].([]any)) != 2 {
		t.Fatalf("rules changed after a failed save: %v", v["rules"])
	}
	call("PATCH", "", `{"cdn":{"enabled":false}}`, 200)
	call("PUT", "/rules", `{"version":1,"rules":[{"priority":1,"if":{"attr":"http.path","op":"eq","value":"/x"},"then":{"action":"cache_override","params":{"ttl_seconds":60}}}]}`, 400)
	call("PUT", "/rules", `{"version":1,"rules":[]}`, 200)
}
