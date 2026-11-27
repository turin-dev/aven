package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/turin-dev/aven/internal/store"
)

func TestPatchNodeTraits(t *testing.T) {
	f, h := authServer(t)
	_, ownerC := freshAccount(t, f, "traits-owner@example.com", store.RoleOwner)
	_, viewerC := freshAccount(t, f, "traits-viewer@example.com", store.RoleViewer)

	if w := req(h, "PATCH", "/api/nodes/node_local", `{"traits":{"site":"moon"}}`, ownerC); w.Code != 400 {
		t.Fatalf("bad site: %d %s", w.Code, w.Body)
	}
	if w := req(h, "PATCH", "/api/nodes/node_local", `{"traits":{"power_price_per_kwh":0.3}}`, ownerC); w.Code != 400 {
		t.Fatalf("price without currency: %d %s", w.Code, w.Body)
	}
	if w := req(h, "PATCH", "/api/nodes/node_local", `{"traits":{"site":"datacenter"}}`, viewerC); w.Code != 403 {
		t.Fatalf("viewer patch: %d %s", w.Code, w.Body)
	}
	w := req(h, "PATCH", "/api/nodes/node_local", `{"traits":{"site":"Home","power_price_per_kwh":180,"power_currency":"krw","connection":"fiber","cgnat":true,"note":"거실"}}`, ownerC)
	if w.Code != 200 {
		t.Fatalf("patch: %d %s", w.Code, w.Body)
	}
	var n store.Node
	json.Unmarshal(w.Body.Bytes(), &n)
	if n.Traits.Site != "home" || n.Traits.PowerCurrency != "KRW" || !n.Traits.CGNAT {
		t.Fatalf("patched traits: %+v", n.Traits)
	}
	// 다른 필드만 바꾸면 특성은 그대로다.
	req(h, "PATCH", "/api/nodes/node_local", `{"schedulable":true}`, ownerC)

	list := func(c func(*http.Request)) store.NodeTraits {
		t.Helper()
		w := req(h, "GET", "/api/nodes", "", c)
		var ns []store.Node
		if err := json.Unmarshal(w.Body.Bytes(), &ns); err != nil || len(ns) == 0 {
			t.Fatalf("list: %d %s", w.Code, w.Body)
		}
		return ns[0].Traits
	}
	if got := list(ownerC); got.Site != "home" || got.Note != "거실" {
		t.Fatalf("owner sees: %+v", got)
	}
	if got := list(viewerC); got.Site != "home" || got.Note != "" {
		t.Fatalf("viewer sees: %+v", got)
	}
}
