package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseScaleArgs(t *testing.T) {
	o, err := parseScaleArgs([]string{"3", "-s", "web"})
	if err != nil || o.replicas == nil || *o.replicas != 3 || o.svc != "web" {
		t.Fatalf("%+v %v", o, err)
	}
	o, err = parseScaleArgs([]string{"-e", "staging", "4"})
	if err != nil || *o.replicas != 4 || o.env != "staging" {
		t.Fatalf("trailing n: %+v %v", o, err)
	}
	o, err = parseScaleArgs([]string{"--auto", "--min", "1", "--max", "5", "--cpu", "70"})
	if err != nil || !o.auto || o.min != 1 || o.max != 5 || o.cpu == nil || *o.cpu != 70 || o.memory != nil {
		t.Fatalf("auto: %+v %v", o, err)
	}
	b, _ := json.Marshal(o.autoscaleBody())
	if s := string(b); !strings.Contains(s, `"target_cpu_percent":70`) || !strings.Contains(s, `"target_memory_percent":null`) || strings.Contains(s, "cooldown") {
		t.Fatalf("body %s", s)
	}
	o, _ = parseScaleArgs([]string{"--auto", "--max", "3", "--rps", "20", "--down-cooldown", "120"})
	if o.min != 1 || o.autoscaleBody()["scale_down_cooldown_s"] != 120 {
		t.Fatalf("defaults: %+v", o)
	}
	if o, err := parseScaleArgs(nil); err != nil || o.replicas != nil || o.auto || o.off {
		t.Fatalf("show: %+v %v", o, err)
	}
	for _, bad := range [][]string{
		{"0"}, {"11"}, {"x"}, {"3", "4"},
		{"--auto", "--max", "5"},  // target 없음
		{"--auto", "--cpu", "70"}, // max 없음
		{"--auto", "--min", "4", "--max", "2", "--cpu", "50"},
		{"--cpu", "70"},                              // --auto 없이
		{"3", "--auto", "--max", "4", "--cpu", "50"}, // 두 모드
		{"--off", "--auto"},
		{"--auto", "--max", "4", "--cpu", "-5"},
	} {
		if _, err := parseScaleArgs(bad); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

func TestFormatScale(t *testing.T) {
	var in scaleInstance
	json.Unmarshal([]byte(`{"replicas":2,"current_replicas":4,"placement":[{"node_id":"node_local","replicas":1},{"node_id":"nod_b","replicas":1}],
		"autoscale":{"enabled":true,"min_replicas":2,"max_replicas":6,"target_cpu_percent":70},
		"autoscale_status":{"desired":4,"reason":"목표 범위 안입니다","last_reason":"자동 확장: cpu 140.0/70"}}`), &in)
	out := formatScale("web", "production", &in)
	for _, want := range []string{"4 replicas running (configured 2)", "node_local×1, nod_b×1", "2..6 replicas, target cpu 70%", "desired: 4", "last scale: 자동 확장"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(formatScale("w", "p", &scaleInstance{Replicas: 1}), "autoscale: off") {
		t.Error("off")
	}
}
