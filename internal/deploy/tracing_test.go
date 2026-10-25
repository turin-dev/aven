package deploy

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/store"
)

func TestRuntimeTraceEnv(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p := &store.Project{Name: "acme"}
	env, err := st.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	sv := &store.Service{ProjectID: p.ID, Name: "web api", SourceType: "image"}
	if err := st.CreateService(ctx, sv); err != nil {
		t.Fatal(err)
	}
	in := &store.Instance{ServiceID: sv.ID, EnvironmentID: env.ID}
	if err := st.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	m := &Manager{Store: st}
	tg := &Target{Project: p, Env: env, Service: sv, Instance: in}
	sha := "abc123"
	img := "nginx"
	d := &store.Deployment{ID: "dep_1", CommitSHA: &sha, Image: &img, Env: map[string]string{"PORT": "8080", "OTEL_RESOURCE_ATTRIBUTES": "team=core,keel.service.id=spoofed"}}

	// 꺼져 있으면 그대로
	got, traced := m.runtimeTraceEnv(ctx, tg, d, "keel-web-1")
	if traced || len(got) != 2 || got["OTEL_EXPORTER_OTLP_ENDPOINT"] != "" {
		t.Fatalf("disabled: %v %v", traced, got)
	}
	setting, _ := st.SetTracingSetting(ctx, in.ID, true)
	got, traced = m.runtimeTraceEnv(ctx, tg, d, "keel-web-1")
	if !traced || got["OTEL_EXPORTER_OTLP_ENDPOINT"] != DefaultOTLPEndpoint || got["OTEL_EXPORTER_OTLP_PROTOCOL"] != "http/protobuf" ||
		got["OTEL_EXPORTER_OTLP_HEADERS"] != "X-Keel-Trace-Token="+setting.Token || got["OTEL_SERVICE_NAME"] != "web api" ||
		got["OTEL_SERVICE_VERSION"] != "abc123" || got["OTEL_METRICS_EXPORTER"] != "none" || got["PORT"] != "8080" {
		t.Fatalf("enabled: %v", got)
	}
	ra := got["OTEL_RESOURCE_ATTRIBUTES"]
	for _, want := range []string{"team=core", "keel.service.id=" + sv.ID, "keel.replica.id=keel-web-1", "keel.deployment.id=dep_1", "keel.service.name=web%20api"} {
		if !strings.Contains(ra, want) {
			t.Errorf("resource attributes missing %s: %s", want, ra)
		}
	}
	if d.Env["OTEL_SERVICE_NAME"] != "" {
		t.Error("deployment env snapshot was modified")
	}
	spec := m.runSpecNamed(tg, d, "keel-web-1", 0)
	if len(spec.ExtraHosts) != 1 || spec.Env["OTEL_SERVICE_NAME"] != "web api" {
		t.Errorf("run spec: hosts=%v", spec.ExtraHosts)
	}

	// 사용자가 endpoint를 정했으면 아무것도 더하지 않는다
	d.Env["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"] = "http://collector:4318/v1/traces"
	if got, traced = m.runtimeTraceEnv(ctx, tg, d, "keel-web-1"); traced || got["OTEL_SERVICE_NAME"] != "" {
		t.Errorf("user endpoint: %v", got)
	}
	if spec := m.runSpecNamed(tg, d, "keel-web-1", 0); len(spec.ExtraHosts) != 0 {
		t.Errorf("extra hosts without tracing: %v", spec.ExtraHosts)
	}

	// edge 라우트 속성
	in.CurrentDeploymentID = &d.ID
	on, attrs := m.traceRoute(ctx, in, in.CurrentDeploymentID)
	if !on || attrs["keel.environment.id"] != env.ID || attrs["keel.project.id"] != p.ID || attrs["keel.service.id"] != sv.ID ||
		attrs["keel.deployment.id"] != "dep_1" || attrs["keel.component"] != "edge" {
		t.Errorf("trace route: %v %v", on, attrs)
	}
	st.SetTracingSetting(ctx, in.ID, false)
	if on, _ := m.traceRoute(ctx, in, nil); on {
		t.Error("route traced while disabled")
	}
}
