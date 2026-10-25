package deploy

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/netlog"
	"github.com/turin-dev/aven/internal/store"
)

func networkLogFixture(t *testing.T) (*store.Store, *store.Environment, *store.Service, *store.Service) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	p := &store.Project{Name: "net"}
	env, err := st.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	api := &store.Service{ProjectID: p.ID, Name: "api", SourceType: "image"}
	db := &store.Service{ProjectID: p.ID, Name: "postgres", SourceType: "image"}
	for _, s := range []*store.Service{api, db} {
		if err := st.CreateService(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	return st, env, api, db
}

func TestNetworkLogHubStoresAndStreams(t *testing.T) {
	st, env, api, db := networkLogFixture(t)
	h := NewNetworkLogHub(st, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.Run(ctx)

	_, live, unsub, err := h.Subscribe(ctx, store.NetworkLogQuery{EnvironmentID: env.ID, Kind: "network", Services: []string{db.ID}}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	t0 := time.Now().UTC()
	// mesh forwarder처럼 상대 이름만 아는 flow: 서버가 ID를 채운다.
	flow := netlog.Event{ID: "n-1", Kind: "network", Flow: &netlog.Flow{T: t0, Environment: env.ID, ServiceID: api.ID, Direction: "egress",
		Protocol: "tcp", PeerKind: "service", Peer: "postgres", DstPort: 5432, BytesSent: 10, BytesReceived: 99, Status: "ok", Replica: "keel-api-1"}}
	h.Add("node_local", flow)
	h.Add("nod_b", flow) // 같은 ID(replay)는 한 번만 저장된다
	h.Add("node_local", netlog.Event{ID: "d-1", Kind: "dns", DNS: &netlog.DNSQuery{T: t0, Environment: env.ID, ServiceID: api.ID,
		QName: "api.example.com", QType: "A", Zone: "external", RCode: "NOERROR", Status: "ok", Answers: []string{"203.0.113.10"}}})
	// 프로젝트에 없는 서비스의 기록은 버린다.
	h.Add("nod_b", netlog.Event{ID: "x-1", Kind: "dns", DNS: &netlog.DNSQuery{T: t0, Environment: env.ID, ServiceID: "svc_unknown", QName: "x"}})
	h.Add("nod_b", netlog.Event{Kind: "status", Status: &netlog.Status{Active: false, Error: "packet capture needs CAP_NET_RAW"}})

	select {
	case l := <-live:
		if l.ID != "n-1" || l.ServiceID != api.ID || l.RelatedServiceID != db.ID {
			t.Fatalf("live: %+v", l)
		}
		// db 쪽에서 보면 ingress이고 바이트와 상대가 뒤집힌다.
		v := NetworkLogView(l, db.ID)
		if v["service_id"] != db.ID || v["service"] != "postgres" || v["direction"] != "ingress" || v["peer"] != "api" ||
			v["peer_service_id"] != api.ID || v["bytes_sent"] != float64(99) || v["bytes_received"] != float64(10) || v["replica"] != nil {
			t.Fatalf("flipped view: %+v", v)
		}
		if v := NetworkLogView(l, api.ID); v["direction"] != "egress" || v["peer"] != "postgres" || v["service"] != "api" {
			t.Fatalf("own view: %+v", v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no live record")
	}
	select {
	case l := <-live:
		t.Fatalf("unexpected second record (dedupe or kind filter): %+v", l)
	case <-time.After(700 * time.Millisecond):
	}
	past, _, done, err := h.Subscribe(ctx, store.NetworkLogQuery{EnvironmentID: env.ID}, false)
	done()
	if err != nil || len(past) != 2 || past[0].RecordedAt.After(past[1].RecordedAt) {
		t.Fatalf("history: %d %v", len(past), err)
	}
	if v := NetworkLogView(past[0], ""); v["kind"] == "dns" && v["service"] != "api" {
		t.Fatalf("dns name: %+v", v)
	}
	if st := h.CaptureStatus(); len(st) != 1 || st[0].Node != "nod_b" || st[0].Active {
		t.Fatalf("status: %+v", st)
	}
}

func TestNetworkLogFilter(t *testing.T) {
	dns := map[string]any{"kind": "dns", "qname": "api.example.com", "qtype": "AAAA", "rcode": "NXDOMAIN", "zone": "external",
		"status": "failed", "answers": []any{}, "service": "api"}
	flow := map[string]any{"kind": "network", "protocol": "tcp", "direction": "egress", "peer": "postgres", "peer_service_id": "svc_db",
		"peer_kind": "service", "src_addr": "172.18.0.2", "src_port": float64(40000), "dst_addr": "172.18.0.3", "dst_port": float64(5432),
		"bytes_sent": float64(2048), "status": "refused"}
	cases := []struct {
		q    string
		rec  map[string]any
		want bool
	}{
		{"", dns, true},
		{"@domain:example.com", dns, true},
		{"@domain:api.example.com", dns, true},
		{"@domain:ample.com", dns, false},
		{"@qtype:aaaa @rcode:NXDOMAIN", dns, true},
		{"@qtype:A", dns, false}, // 정확히 같아야 한다(AAAA는 A가 아니다)
		{"-@zone:internal", dns, true},
		{"@zone:internal", dns, false},
		{"api.example", dns, true},
		{"-api", dns, false},
		{"@port:5432", flow, true},
		{"@port:40000", flow, true},
		{"@port:>=6000", flow, true},
		{"@port:1..1000", flow, false},
		{"@peer:svc_db @direction:egress", flow, true},
		{"@peer:POSTGRES", flow, true},
		{"@host:172.18.0.3", flow, true},
		{"@src:172.18.0.3", flow, false},
		{"@dropped:true", flow, true},
		{"@dropped:false", flow, false},
		{"@bytes_sent:>1000", flow, true},
		{"@peer_kind:internet", flow, false},
		{`"@peer:post gres"`, flow, false},
		{"@nosuchfield:x", flow, false},
	}
	for _, c := range cases {
		if got := ParseNetworkLogFilter(c.q).Match(c.rec); got != c.want {
			t.Errorf("%q on %s: %v want %v", c.q, c.rec["kind"], got, c.want)
		}
	}
}
