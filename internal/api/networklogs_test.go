package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/netlog"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

func TestParseLogTime(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"": {}, "30s": now.Add(-30 * time.Second), "5m": now.Add(-5 * time.Minute), "2h": now.Add(-2 * time.Hour),
		"1d": now.Add(-24 * time.Hour), "1w": now.Add(-7 * 24 * time.Hour), "2026-10-01T00:00:00Z": time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
	} {
		got, err := parseLogTime(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"5", "m", "-1h", "1y", "yesterday"} {
		if _, err := parseLogTime(bad, now); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// sseEvents는 SSE 응답을 (event, data) 쌍으로 나눈다.
func sseEvents(body string) [][2]string {
	var out [][2]string
	ev := ""
	for _, l := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(l, "event: "):
			ev = l[7:]
		case strings.HasPrefix(l, "data: "):
			out = append(out, [2]string{ev, l[6:]})
		}
	}
	return out
}

func TestNetworkLogEndpoints(t *testing.T) {
	f := newGHFixture(t)
	hub := deploy.NewNetworkLogHub(f.st, nil)
	f.mgr.NetworkLogs = hub
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.Run(ctx)
	t0 := time.Now().UTC().Add(-time.Minute)
	hub.Add("node_local", netlog.Event{ID: "d1", Kind: "dns", DNS: &netlog.DNSQuery{T: t0, Environment: f.prod.ID, ServiceID: f.web.ID,
		QName: "db.production.keel.internal", QType: "A", Zone: "internal", RCode: "NXDOMAIN", Status: "failed", Answers: []string{}}})
	hub.Add("node_local", netlog.Event{ID: "d2", Kind: "dns", DNS: &netlog.DNSQuery{T: t0.Add(time.Second), Environment: f.prod.ID, ServiceID: f.web.ID,
		QName: "api.github.com", QType: "AAAA", Zone: "external", RCode: "NOERROR", Status: "ok", Answers: []string{"2001:db8::1"}}})
	hub.Add("node_local", netlog.Event{ID: "f1", Kind: "network", Flow: &netlog.Flow{T: t0, Environment: f.prod.ID, ServiceID: f.web.ID,
		Direction: "egress", Protocol: "tcp", PeerKind: "service", PeerServiceID: f.db.ID, DstPort: 5432, Status: "ok"}})
	hub.Add("node_local", netlog.Event{Kind: "status", Status: &netlog.Status{Error: "packet capture needs CAP_NET_RAW"}})
	deadline := time.Now().Add(3 * time.Second)
	for {
		if l, _ := f.st.ListNetworkLogs(f.ctx, store.NetworkLogQuery{EnvironmentID: f.prod.ID}); len(l) == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("records not stored")
		}
		time.Sleep(50 * time.Millisecond)
	}

	get := func(h func(w *httptest.ResponseRecorder, target string, pv map[string]string), target string, pv map[string]string) (int, [][2]string) {
		w := httptest.NewRecorder()
		h(w, target, pv)
		return w.Code, sseEvents(w.Body.String())
	}
	call := func(name string) func(w *httptest.ResponseRecorder, target string, pv map[string]string) {
		return func(w *httptest.ResponseRecorder, target string, pv map[string]string) {
			r := httptest.NewRequest("GET", target, nil)
			for k, v := range pv {
				r.SetPathValue(k, v)
			}
			switch name {
			case "sdns":
				f.srv.serviceDNSLogs(w, r)
			case "snet":
				f.srv.serviceNetworkLogs(w, r)
			case "enet":
				f.srv.envNetworkLogs(w, r)
			}
		}
	}
	webPV := map[string]string{"sid": f.web.ID, "eid": f.prod.ID}

	code, ev := get(call("sdns"), "/x?follow=0", webPV)
	if code != 200 || len(ev) != 4 || ev[0][0] != "capture" || !strings.Contains(ev[0][1], `"inactive_nodes":1`) || !strings.Contains(ev[0][1], "CAP_NET_RAW") ||
		!strings.Contains(ev[1][1], "db.production.keel.internal") || !strings.Contains(ev[2][1], "api.github.com") || ev[3][0] != "done" {
		t.Fatalf("dns: %d %v", code, ev)
	}
	// 필터와 tail: 맞는 것만, 마지막 tail개.
	_, ev = get(call("sdns"), "/x?follow=0&q=%40status%3Afailed", webPV)
	if len(ev) != 3 || !strings.Contains(ev[1][1], "NXDOMAIN") {
		t.Fatalf("dns filter: %v", ev)
	}
	_, ev = get(call("sdns"), "/x?follow=0&tail=1", webPV)
	if len(ev) != 3 || !strings.Contains(ev[1][1], "api.github.com") {
		t.Fatalf("dns tail: %v", ev)
	}
	// until이 있으면 follow하지 않는다.
	_, ev = get(call("sdns"), "/x?until="+t0.Add(500*time.Millisecond).Format(time.RFC3339Nano), webPV)
	if len(ev) != 3 || !strings.Contains(ev[1][1], "NXDOMAIN") || ev[2][0] != "done" {
		t.Fatalf("dns until: %v", ev)
	}
	if code, _ := get(call("sdns"), "/x?since=yesterday", webPV); code != 400 {
		t.Fatalf("bad since: %d", code)
	}
	// db 서비스의 network 로그에 web → db flow가 ingress로 보인다.
	_, ev = get(call("snet"), "/x?follow=0", map[string]string{"sid": f.db.ID, "eid": f.prod.ID})
	if len(ev) != 3 || !strings.Contains(ev[1][1], `"direction":"ingress"`) || !strings.Contains(ev[1][1], `"peer":"web"`) {
		t.Fatalf("db network: %v", ev)
	}
	// 환경 전체(services 선택 없음)는 기록 그대로(egress)다.
	_, ev = get(call("enet"), "/x?follow=0&services="+f.other.ID, map[string]string{"eid": f.prod.ID})
	if len(ev) != 2 {
		t.Fatalf("env other: %v", ev)
	}
	_, ev = get(call("enet"), "/x?follow=0", map[string]string{"eid": f.prod.ID})
	if len(ev) != 3 || !strings.Contains(ev[1][1], `"direction":"egress"`) || !strings.Contains(ev[1][1], `"peer":"db"`) {
		t.Fatalf("env network: %v", ev)
	}

	// follow: 새 기록이 이어서 온다.
	rctx, rcancel := context.WithCancel(context.Background())
	pr := httptest.NewRequest("GET", "/x", nil).WithContext(rctx)
	pr.SetPathValue("sid", f.web.ID)
	pr.SetPathValue("eid", f.prod.ID)
	w := &syncRecorder{ResponseRecorder: httptest.NewRecorder()}
	done := make(chan struct{})
	go func() { f.srv.serviceDNSLogs(w, pr); close(done) }()
	time.Sleep(200 * time.Millisecond)
	hub.Add("node_local", netlog.Event{ID: "d3", Kind: "dns", DNS: &netlog.DNSQuery{T: time.Now(), Environment: f.prod.ID, ServiceID: f.web.ID,
		QName: "live.example.com", QType: "A", Zone: "external", RCode: "NOERROR", Status: "ok", Answers: []string{}}})
	deadline = time.Now().Add(3 * time.Second)
	for !strings.Contains(w.body(), "live.example.com") {
		if time.Now().After(deadline) {
			t.Fatalf("live record not streamed: %s", w.body())
		}
		time.Sleep(50 * time.Millisecond)
	}
	rcancel()
	<-done
}

type syncRecorder struct {
	mu sync.Mutex
	*httptest.ResponseRecorder
}

func (s *syncRecorder) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ResponseRecorder.Write(b)
}
func (s *syncRecorder) Flush() { s.mu.Lock(); s.ResponseRecorder.Flush(); s.mu.Unlock() }
func (s *syncRecorder) body() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ResponseRecorder.Body.String()
}

type diagRunner struct {
	mu   sync.Mutex
	cmds [][]string
	exit map[string]int
	out  map[string]string
}

func (d *diagRunner) ExecRun(_ context.Context, nodeID, container string, req node.ExecRunRequest, timeout time.Duration) (*node.ExecOutput, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cmds = append(d.cmds, req.Cmd)
	name := req.Cmd[3]
	return &node.ExecOutput{Stdout: []byte(d.out[name]), ExitCode: d.exit[name]}, nil
}

func TestNetworkDiagnostics(t *testing.T) {
	df := newDataFixture(t)
	run := &diagRunner{exit: map[string]int{"keel-dns": 0, "keel-tcp": 1}, out: map[string]string{"keel-dns": "10.0.0.5 STREAM db", "keel-tcp": ""}}
	df.srv.dataExec = run
	pv := pvFor(df, "postgres", nil)
	code, out := df.call(df.srv.networkDiagnostics, "POST", "/x", `{"host":"db.production.keel.internal","port":5432}`, pv)
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	dns, tcp := out["dns"].(map[string]any), out["tcp"].(map[string]any)
	if dns["ok"] != true || dns["output"] != "10.0.0.5 STREAM db" || tcp["ok"] != false || tcp["available"] != true || out["replica"] != "keel-postgres-1" {
		t.Fatalf("result: %v", out)
	}
	// 호스트와 포트는 셸 문자열이 아니라 위치 인자로만 넘어간다.
	if len(run.cmds) != 2 || run.cmds[0][0] != "sh" || run.cmds[0][4] != "db.production.keel.internal" || run.cmds[1][5] != "5432" ||
		strings.Contains(run.cmds[0][2], "db.production") {
		t.Fatalf("cmds: %q", run.cmds)
	}
	// 포트 0이면 DNS만 본다.
	run.cmds = nil
	_, out = df.call(df.srv.networkDiagnostics, "POST", "/x", `{"host":"10.0.0.5"}`, pv)
	if tcp := out["tcp"].(map[string]any); tcp["skipped"] != true || len(run.cmds) != 1 {
		t.Fatalf("dns only: %v %d", out, len(run.cmds))
	}
	for _, body := range []string{`{"host":"a;rm -rf /","port":1}`, `{"host":"-x","port":1}`, `{"host":"$(id)","port":1}`, `{"host":"","port":1}`,
		`{"host":"ok.example","port":70000}`} {
		if code, _ := df.call(df.srv.networkDiagnostics, "POST", "/x", body, pv); code != 400 {
			t.Errorf("%s: %d", body, code)
		}
	}
	if code, _ := df.call(df.srv.networkDiagnostics, "POST", "/x", `{"host":"a.b","port":1,"replica":"nope"}`, pv); code != 404 {
		t.Errorf("unknown replica: %d", code)
	}
}
