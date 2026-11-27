package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNodeTraitsNormalize(t *testing.T) {
	price := 0.31
	ok := NodeTraits{Site: " Home ", Connection: "FIBER", PowerPricePerKWh: &price, PowerCurrency: "eur", DownlinkMbps: 1000, UplinkMbps: 100}
	if err := ok.Normalize(); err != nil {
		t.Fatal(err)
	}
	if ok.Site != "home" || ok.Connection != "fiber" || ok.PowerCurrency != "EUR" {
		t.Fatalf("normalized: %+v", ok)
	}
	neg := -1.0
	for name, bad := range map[string]NodeTraits{
		"site":           {Site: "moon"},
		"connection":     {Connection: "carrier-pigeon"},
		"price no unit":  {PowerPricePerKWh: &price},
		"price negative": {PowerPricePerKWh: &neg, PowerCurrency: "KRW"},
		"currency":       {PowerCurrency: "₩"},
		"mbps":           {UplinkMbps: -5},
		"note":           {Note: strings.Repeat("가", 201)},
	} {
		if err := bad.Normalize(); err == nil {
			t.Errorf("%s: accepted %+v", name, bad)
		}
	}
}

// 온보딩에서 적은 특성은 가입 토큰에 담겼다가 가입한 노드로 옮겨진다(두 가입 경로 모두).
func TestJoinTokenCarriesTraits(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "traits.db"))
	if err != nil {
		t.Fatal(err)
	}
	price := 180.0
	want := NodeTraits{Site: "home", PowerPricePerKWh: &price, PowerCurrency: "KRW", Connection: "fiber", UplinkMbps: 500, DynamicIP: true, Note: "거실 미니PC"}
	if err := s.CreateJoinToken(ctx, "h1", "home-1", time.Now().Add(time.Hour), want); err != nil {
		t.Fatal(err)
	}
	n, err := s.ConsumeJoinToken(ctx, "h1", "c")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetNode(ctx, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Traits.Site != "home" || got.Traits.PowerPricePerKWh == nil || *got.Traits.PowerPricePerKWh != 180 || !got.Traits.DynamicIP || got.Traits.Note != "거실 미니PC" {
		t.Fatalf("traits after join: %+v", got.Traits)
	}

	if err := s.CreateJoinToken(ctx, "h2", "dc-1", time.Now().Add(time.Hour), NodeTraits{Site: "datacenter"}); err != nil {
		t.Fatal(err)
	}
	n2, err := s.ConsumeJoinTokenForCert(ctx, "h2", func(string) (string, time.Time, error) { return "serial-1", time.Now().Add(time.Hour), nil })
	if err != nil {
		t.Fatal(err)
	}
	if n2.Traits.Site != "datacenter" {
		t.Fatalf("cert join traits: %+v", n2.Traits)
	}

	// 로컬 노드는 온보딩을 거치지 않으므로 나중에 적는다.
	if err := s.SetNodeTraits(ctx, LocalNodeID, NodeTraits{Site: "datacenter", Connection: "datacenter"}); err != nil {
		t.Fatal(err)
	}
	local, err := s.LocalNode(ctx)
	if err != nil || local.Traits.Connection != "datacenter" {
		t.Fatalf("local traits: %+v, %v", local, err)
	}
	all, err := s.ListNodes(ctx)
	if err != nil || len(all) != 3 {
		t.Fatalf("list: %d, %v", len(all), err)
	}
}
