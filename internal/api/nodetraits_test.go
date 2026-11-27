package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

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

func TestNodeListPowerAndTraffic(t *testing.T) {
	f, h := authServer(t)
	_, ownerC := freshAccount(t, f, "power-owner@example.com", store.RoleOwner)
	ctx := context.Background()
	now := time.Now()
	// 최근 CPU 평균 30%
	f.st.InsertNodeMetrics(ctx, store.LocalNodeID, []store.NodeMetricRow{{T: now.Add(-time.Hour).Unix(), Resolution: 300, CPU: 20}, {T: now.Add(-50 * time.Minute).Unix(), Resolution: 300, CPU: 40}})
	f.st.AddNodeTraffic(ctx, store.LocalNodeID, 0, 0, now)
	f.st.AddNodeTraffic(ctx, store.LocalNodeID, 300_000_000, 200_000_000, now.Add(time.Second))
	w := req(h, "PATCH", "/api/nodes/node_local", `{"traits":{"site":"home","idle_watts":10,"max_watts":110,"power_price_per_kwh":200,"power_currency":"KRW","data_cap_gb":1,"cgnat":true}}`, ownerC)
	if w.Code != 200 {
		t.Fatalf("patch: %d %s", w.Code, w.Body)
	}
	w = req(h, "GET", "/api/nodes", "", ownerC)
	var ns []struct {
		PowerEstimate *store.PowerEstimate `json:"power_estimate"`
		Traffic       *nodeTrafficView     `json:"traffic"`
		PublicRank    int                  `json:"public_rank"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &ns); err != nil || len(ns) == 0 {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	pe := ns[0].PowerEstimate
	// 10 + 100*0.3 = 40W → 29.2kWh → 5840원
	if pe == nil || pe.AvgWatts != 40 || pe.KWhMonth != 29.2 || pe.CostMonth == nil || int(*pe.CostMonth+0.5) != 5840 || pe.Currency != "KRW" {
		t.Fatalf("power: %+v", pe)
	}
	tr := ns[0].Traffic
	if tr == nil || tr.TotalBytes != 500_000_000 || tr.CapBytes != 1_000_000_000 || tr.CapPercent == nil || *tr.CapPercent != 50 {
		t.Fatalf("traffic: %+v", tr)
	}
	if ns[0].PublicRank != 2 {
		t.Fatalf("rank: %d", ns[0].PublicRank)
	}
}
