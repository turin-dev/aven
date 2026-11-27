package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestNodeTrafficAccumulates(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "traffic.db"))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 30, 23, 59, 0, 0, time.UTC)
	add := func(rx, tx uint64, at time.Time) {
		t.Helper()
		if err := s.AddNodeTraffic(ctx, LocalNodeID, rx, tx, at); err != nil {
			t.Fatal(err)
		}
	}
	if s.HasNodeTrafficCounter(ctx, LocalNodeID) {
		t.Fatal("counter before first sample")
	}
	add(1_000, 500, base)                                // 기준점만 잡는다
	add(1_600, 900, base.Add(10*time.Second))            // +600 / +400
	add(100, 50, base.Add(20*time.Second))               // 재부팅: 지금 값을 그대로 더한다
	add(1<<62, 1<<62, base.Add(30*time.Second))          // 말도 안 되는 증가는 버린다
	add((1<<62)+10, (1<<62)+5, base.Add(70*time.Second)) // 12월로 넘어감: +10 / +5
	nov, err := s.NodeTrafficFor(ctx, "", "2026-11")
	if err != nil {
		t.Fatal(err)
	}
	if nov.RxBytes != 700 || nov.TxBytes != 450 || nov.Total() != 1150 {
		t.Fatalf("november: %+v", nov)
	}
	dec, _ := s.NodeTrafficFor(ctx, LocalNodeID, "2026-12")
	if dec.RxBytes != 10 || dec.TxBytes != 5 {
		t.Fatalf("december: %+v", dec)
	}
	if !s.HasNodeTrafficCounter(ctx, LocalNodeID) {
		t.Fatal("no counter")
	}

	ok, err := s.MarkNodeTrafficAlerted(ctx, LocalNodeID, "2026-11", 80)
	if err != nil || !ok {
		t.Fatalf("mark 80: %v %v", ok, err)
	}
	if ok, _ := s.MarkNodeTrafficAlerted(ctx, LocalNodeID, "2026-11", 80); ok {
		t.Fatal("80 marked twice")
	}
	if ok, _ := s.MarkNodeTrafficAlerted(ctx, LocalNodeID, "2026-11", 100); !ok {
		t.Fatal("100 not marked")
	}
	// 알림이 먼저 기록된 달에도 트래픽은 계속 쌓인다.
	if ok, _ := s.MarkNodeTrafficAlerted(ctx, LocalNodeID, "2027-01", 80); !ok {
		t.Fatal("new month row")
	}
	jan := time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC)
	add((1<<62)+20, (1<<62)+5, jan)
	got, _ := s.NodeTrafficFor(ctx, LocalNodeID, "2027-01")
	if got.RxBytes != 10 || got.AlertedPct != 80 {
		t.Fatalf("january: %+v", got)
	}
}

func TestEstimatePower(t *testing.T) {
	price := 200.0
	e := EstimatePower(NodeTraits{IdleWatts: 10, MaxWatts: 60, PowerPricePerKWh: &price, PowerCurrency: "KRW"}, 20, 3600)
	// 10 + 50*0.2 = 20W → 20*730/1000 = 14.6kWh → 2920원
	if e == nil || e.Model != "linear" || e.AvgWatts != 20 || e.KWhMonth != 14.6 || e.CostMonth == nil || *e.CostMonth < 2919.99 || *e.CostMonth > 2920.01 {
		t.Fatalf("linear: %+v", e)
	}
	if e := EstimatePower(NodeTraits{MaxWatts: 100}, 50, 0); e == nil || e.Model != "flat" || e.AvgWatts != 100 || e.CostMonth != nil {
		t.Fatalf("flat: %+v", e)
	}
	if EstimatePower(NodeTraits{}, 50, 0) != nil {
		t.Fatal("no watts should not estimate")
	}
	bad := NodeTraits{IdleWatts: 90, MaxWatts: 30}
	if bad.Normalize() == nil {
		t.Fatal("idle > max accepted")
	}
}
