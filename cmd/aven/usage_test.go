package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPrintUsage(t *testing.T) {
	var u usageResp
	raw := `{"from":"2025-03-01T00:00:00Z","to":"2025-03-15T00:00:00Z","group":"service",
		"totals":{"cpu_core_hours":3.5,"memory_gb_hours":10,"net_egress_gb":0.25,"disk_gb_hours":1,"estimated_cost":{"total":1.5}},
		"groups":[{"key":"svc_1","name":"web","cpu_core_hours":3,"memory_gb_hours":8,"net_egress_gb":0.2,"disk_gb_hours":1,"estimated_cost":{"total":1.2}},
		          {"key":"svc_gone","name":"","cpu_core_hours":0.5,"memory_gb_hours":2,"net_egress_gb":0.05,"disk_gb_hours":0,"estimated_cost":{"total":0.3}}]}`
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	printUsage(&b, &u)
	out := b.String()
	for _, want := range []string{"SERVICE", "EST. COST", "web", "svc_gone", "TOTAL", "3.50", "1.50", "0.250"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// 단가가 없으면 비용 열이 없다. 작업 공간 응답은 프로젝트별.
	u = usageResp{Projects: []usageGroup{{Key: "prj_1", Name: "acme"}}}
	b.Reset()
	printUsage(&b, &u)
	if out := b.String(); strings.Contains(out, "COST") || !strings.Contains(out, "PROJECT") || !strings.Contains(out, "acme") {
		t.Errorf("workspace table:\n%s", out)
	}
}
