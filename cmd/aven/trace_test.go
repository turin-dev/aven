package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestParseTraceArgs(t *testing.T) {
	o, err := parseTraceArgs([]string{"list", "--since", "30m", "--errors", "-a", "--filter", "@http.route:/x @duration:>500", "-n", "20"})
	if err != nil || o.sub != "list" || o.since != "30m" || !o.errors || !o.all || o.filter != "@http.route:/x @duration:>500" || o.limit != 20 {
		t.Fatalf("list: %+v %v", o, err)
	}
	o, err = parseTraceArgs([]string{"show", "4bf92f3577b34da6a3ce929d0e0e4736", "--json", "-s", "api"})
	if err != nil || o.sub != "get" || o.pos[0] != "4bf92f3577b34da6a3ce929d0e0e4736" || !o.jsonOut || o.svc != "api" {
		t.Fatalf("get: %+v %v", o, err)
	}
	for args, sub := range map[string]string{"enable": "on", "disable": "off", "on": "on", "ls": "list", "": "status"} {
		var a []string
		if args != "" {
			a = []string{args}
		}
		if o, err := parseTraceArgs(a); err != nil || o.sub != sub {
			t.Errorf("%q: %+v %v", args, o, err)
		}
	}
	for _, bad := range [][]string{
		{"nope"}, {"get"}, {"on", "--all", "-s", "api"}, {"status", "-p", "prj_1"}, {"list", "-n", "0"}, {"get", "x", "--max-spans", "5000"},
	} {
		if _, err := parseTraceArgs(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestPrintTraceTree(t *testing.T) {
	at := time.Unix(1700000000, 0)
	spans := []traceSpan{
		{TraceID: "t", SpanID: "a", Name: "GET /", Component: "edge", ServiceName: "api", Status: "unset", StartTime: at, DurationMS: 120},
		{TraceID: "t", SpanID: "c", ParentSpanID: "b", Name: "SELECT", Component: "service", ServiceName: "api", Status: "error", StartTime: at.Add(3 * time.Millisecond), DurationMS: 4.5},
		{TraceID: "t", SpanID: "b", ParentSpanID: "a", Name: "handler", Component: "service", ServiceName: "api", StartTime: at.Add(time.Millisecond), DurationMS: 100},
		{TraceID: "t", SpanID: "d", ParentSpanID: "a", Name: "render", Component: "service", StartTime: at.Add(110 * time.Millisecond), DurationMS: 8},
		{TraceID: "t", SpanID: "e", ParentSpanID: "missing", Name: "orphan", Component: "service", StartTime: at.Add(time.Second), DurationMS: 1500},
	}
	var b bytes.Buffer
	printTraceTree(&b, spans)
	want := `trace t · 5 spans
GET /  [edge · api]  120ms
├─ handler  [service · api]  100ms
│  └─ SELECT  [service · api]  4.50ms  ERROR
└─ render  [service]  8.00ms
orphan  [service]  1.50s
`
	if b.String() != want {
		t.Errorf("tree:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestPrintTraceStatus(t *testing.T) {
	now := time.Now().Add(-90 * time.Second)
	var b bytes.Buffer
	printTraceStatus(&b, []traceSetting{
		{Service: "api", Enabled: true, HasDomain: true, LastEdgeAt: &now},
		{Service: "worker", Enabled: false},
	})
	out := b.String()
	if !strings.Contains(out, "api") || !strings.Contains(out, "1m ago") || !strings.Contains(out, "no public domain") || !strings.Contains(out, "never") {
		t.Errorf("status:\n%s", out)
	}
}
