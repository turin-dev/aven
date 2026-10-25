package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/turin-dev/aven/internal/edge"
)

func TestEdgeSettingsPersistence(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "edge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	projects, err := s.ListProjects(ctx)
	if err != nil || len(projects) != 1 {
		t.Fatalf("projects: %v %v", projects, err)
	}
	service := &Service{ProjectID: projects[0].ID, Name: "web", SourceType: "image"}
	if err := s.CreateService(ctx, service); err != nil {
		t.Fatal(err)
	}
	in := &Instance{ServiceID: service.ID, EnvironmentID: projects[0].DefaultEnvironmentID}
	if err := s.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	defaults, err := s.GetEdgeSettings(ctx, in.ID)
	if err != nil || defaults.CDN.DefaultTTLSeconds != 7200 || defaults.CDN.Enabled || !defaults.CDN.HonorSWR || defaults.CDN.PurgeOnDeploy != "html" {
		t.Fatalf("defaults: %+v %v", defaults, err)
	}
	settings := edge.DefaultSettings()
	settings.CDN.Enabled = true
	settings.CDN.HonorSWR = false
	settings.WAF.DenyIPs = []string{"203.0.113.0/24"}
	settings.Rules.Rules = []edge.Rule{{ID: "rul_1", Priority: 10, If: &edge.Expr{Attr: "http.path", Op: "eq", Value: edge.Value{One: "/old"}},
		Then: edge.Action{Action: edge.ActBlock}}}
	if err := s.SaveEdgeSettings(ctx, in.ID, settings); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetEdgeSettings(ctx, in.ID)
	if err != nil || !got.CDN.Enabled || got.CDN.HonorSWR || got.WAF.DenyIPs[0] != "203.0.113.0/24" || len(got.Rules.Rules) != 1 || got.Rules.Rules[0].If.Value.One != "/old" {
		t.Fatalf("saved settings: %+v %v", got, err)
	}
	all, err := s.ListEdgeSettings(ctx)
	if err != nil || len(all) != 1 || !all[in.ID].CDN.Enabled {
		t.Fatalf("list: %+v %v", all, err)
	}
	if err := s.DeleteService(ctx, service.ID); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.ListEdgeSettings(ctx); len(all) != 0 {
		t.Fatalf("settings should be deleted with the instance: %+v", all)
	}
}
