package api

import (
	"context"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/cloudflare/cftest"
	"github.com/turin-dev/aven/internal/secret"
	"github.com/turin-dev/aven/internal/store"
)

func TestCloudflareSettingsAPI(t *testing.T) {
	f, h := authServer(t)
	cf := cftest.New("good-token-1234567890")
	defer cf.Close()
	f.mgr.Cfg.CloudflareAPI = cf.URL
	box, err := secret.NewBox([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	f.st.SetSecretBox(box)
	_, ownerC := freshAccount(t, f, "boss@example.com", store.RoleOwner)
	_, adminC := freshAccount(t, f, "operator@example.com", store.RoleAdmin)

	for _, rt := range []struct{ m, p string }{{"GET", "/api/owner/cloudflare"}, {"PUT", "/api/owner/cloudflare"}, {"POST", "/api/owner/cloudflare/test"}, {"POST", "/api/owner/cloudflare/sync"}, {"POST", "/api/owner/cloudflare/cleanup"}} {
		if w := req(h, rt.m, rt.p, `{}`, adminC); w.Code != 403 {
			t.Errorf("admin %s %s: %d", rt.m, rt.p, w.Code)
		}
	}
	v := decodeBody[map[string]any](t, req(h, "GET", "/api/owner/cloudflare", "", ownerC))
	if v["enabled"] != false || v["has_token"] != false || v["mode"] != "auto" || v["proxied"] != true {
		t.Fatalf("defaults %v", v)
	}
	if w := req(h, "PUT", "/api/owner/cloudflare", `{"enabled":true}`, ownerC); w.Code != 400 {
		t.Fatalf("enable without token: %d", w.Code)
	}
	if w := req(h, "PUT", "/api/owner/cloudflare", `{"token":"wrong-token-xxxxxxxx"}`, ownerC); w.Code != 400 || !strings.Contains(w.Body.String(), "rejected") {
		t.Fatalf("bad token: %d %s", w.Code, w.Body)
	}
	if w := req(h, "PUT", "/api/owner/cloudflare", `{"mode":"geo"}`, ownerC); w.Code != 400 {
		t.Fatalf("bad mode: %d", w.Code)
	}
	if w := req(h, "PUT", "/api/owner/cloudflare", `{"zones":["not a zone"]}`, ownerC); w.Code != 400 {
		t.Fatalf("bad zone: %d", w.Code)
	}
	w := req(h, "PUT", "/api/owner/cloudflare", `{"token":"good-token-1234567890","enabled":true,"zones":["Example.COM."]}`, ownerC)
	if w.Code != 200 || strings.Contains(w.Body.String(), "good-token") {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	v = decodeBody[map[string]any](t, w)
	if v["has_token"] != true || v["token_hint"] != "…7890" || v["enabled"] != true || v["zones"].([]any)[0] != "example.com" {
		t.Fatalf("saved %v", v)
	}
	// 토큰은 암호화되어 저장된다
	raw, _ := f.st.GetSetting(context.Background(), "cloudflare")
	if strings.Contains(raw, "good-token") {
		t.Fatalf("token stored in plain text: %s", raw)
	}
	// token 없이 PUT하면 저장된 토큰을 유지한다
	req(h, "PUT", "/api/owner/cloudflare", `{"proxied":false}`, ownerC)
	if cfg, _ := f.st.CloudflareSettings(context.Background()); cfg.Token != "good-token-1234567890" || cfg.Proxied {
		t.Fatalf("token kept: %+v", cfg)
	}
	res := decodeBody[map[string]any](t, req(h, "POST", "/api/owner/cloudflare/test", "", ownerC))
	if res["ok"] != true || res["load_balancing"] != "available" || len(res["zones"].([]any)) != 1 {
		t.Fatalf("test %v", res)
	}
	cf.NoLB = true
	res = decodeBody[map[string]any](t, req(h, "POST", "/api/owner/cloudflare/test", `{"token":"good-token-1234567890"}`, ownerC))
	if res["load_balancing"] != "unavailable" || !strings.Contains(res["load_balancing_error"].(string), "subscription") {
		t.Fatalf("test no LB %v", res)
	}
	if w := req(h, "POST", "/api/owner/cloudflare/cleanup", "", ownerC); w.Code != 409 {
		t.Fatalf("cleanup while enabled: %d", w.Code)
	}
	if w := req(h, "POST", "/api/owner/cloudflare/sync", "", ownerC); w.Code != 200 {
		t.Fatalf("sync: %d %s", w.Code, w.Body)
	}
	req(h, "PUT", "/api/owner/cloudflare", `{"enabled":false}`, ownerC)
	if w := req(h, "POST", "/api/owner/cloudflare/cleanup", "", ownerC); w.Code != 200 {
		t.Fatalf("cleanup: %d %s", w.Code, w.Body)
	}
}

func TestNodeGeoLabelValidated(t *testing.T) {
	f, h := authServer(t)
	_, ownerC := freshAccount(t, f, "boss@example.com", store.RoleOwner)
	if w := req(h, "PATCH", "/api/nodes/node_local", `{"labels":{"geo":"seoul"}}`, ownerC); w.Code != 400 {
		t.Fatalf("bad geo: %d %s", w.Code, w.Body)
	}
	if w := req(h, "PATCH", "/api/nodes/node_local", `{"labels":{"geo":"37.57,126.98"}}`, ownerC); w.Code != 200 {
		t.Fatalf("good geo: %d %s", w.Code, w.Body)
	}
}
