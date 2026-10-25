package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestFeatureFlagsHaveEnvironmentSpecificValues(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "flags.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	project := &Project{Name: "flags-project"}
	production, err := s.CreateProject(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	staging := &Environment{ProjectID: project.ID, Name: "staging"}
	if err := s.CreateEnvironment(ctx, staging); err != nil {
		t.Fatal(err)
	}
	rules := []FeatureFlagRule{{ID: "enterprise", When: `plan == "enterprise"`, Value: json.RawMessage("true")}}
	flag := &FeatureFlag{ProjectID: project.ID, Name: "checkout.v2", Type: FeatureFlagBool, EnvironmentID: production.ID, DefaultValue: json.RawMessage("false"), Rules: rules}
	if err := s.CreateFeatureFlag(ctx, flag); err != nil {
		t.Fatal(err)
	}

	prod, err := s.GetFeatureFlag(ctx, project.ID, flag.Name, production.ID)
	if err != nil || !prod.Configured || len(prod.Rules) != 1 || string(prod.DefaultValue) != "false" {
		t.Fatalf("production flag: %+v, %v", prod, err)
	}
	stage, err := s.GetFeatureFlag(ctx, project.ID, flag.Name, staging.ID)
	if err != nil || !stage.Configured || len(stage.Rules) != 0 || string(stage.DefaultValue) != "false" {
		t.Fatalf("staging seed: %+v, %v", stage, err)
	}
	if err := s.UpdateFeatureFlag(ctx, project.ID, flag.Name, staging.ID, FeatureFlagBool, false, json.RawMessage("true"), nil); err != nil {
		t.Fatal(err)
	}
	prod, _ = s.GetFeatureFlag(ctx, project.ID, flag.Name, production.ID)
	stage, _ = s.GetFeatureFlag(ctx, project.ID, flag.Name, staging.ID)
	if string(prod.DefaultValue) != "false" || len(prod.Rules) != 1 || string(stage.DefaultValue) != "true" {
		t.Fatalf("environment values crossed: production=%+v staging=%+v", prod, stage)
	}

	newEnv := &Environment{ProjectID: project.ID, Name: "preview"}
	if err := s.CreateEnvironment(ctx, newEnv); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateFeatureFlag(ctx, project.ID, flag.Name, production.ID, FeatureFlagBool, false, json.RawMessage("true"), rules); err != nil {
		t.Fatal(err)
	}
	preview, err := s.GetFeatureFlag(ctx, project.ID, flag.Name, newEnv.ID)
	if err != nil || preview.Configured || string(preview.DefaultValue) != "true" || len(preview.Rules) != 0 {
		t.Fatalf("new environment fallback: %+v, %v", preview, err)
	}
}

func TestFeatureFlagTypeChangeResetsOtherEnvironmentRules(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "flags.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := &Project{Name: "type-project"}
	env, err := s.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	stage := &Environment{ProjectID: p.ID, Name: "staging"}
	if err := s.CreateEnvironment(ctx, stage); err != nil {
		t.Fatal(err)
	}
	f := &FeatureFlag{ProjectID: p.ID, Name: "theme", Type: FeatureFlagBool, EnvironmentID: env.ID, DefaultValue: json.RawMessage("true"), Rules: []FeatureFlagRule{{ID: "all", When: "true", Value: json.RawMessage("false")}}}
	if err := s.CreateFeatureFlag(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateFeatureFlag(ctx, p.ID, f.Name, env.ID, FeatureFlagString, false, json.RawMessage(`"blue"`), nil); err != ErrConflict {
		t.Fatalf("type change without force: %v", err)
	}
	if err := s.UpdateFeatureFlag(ctx, p.ID, f.Name, env.ID, FeatureFlagString, true, json.RawMessage(`"blue"`), nil); err != nil {
		t.Fatal(err)
	}
	prod, _ := s.GetFeatureFlag(ctx, p.ID, f.Name, env.ID)
	other, _ := s.GetFeatureFlag(ctx, p.ID, f.Name, stage.ID)
	if prod.Type != FeatureFlagString || len(prod.Rules) != 0 || string(prod.DefaultValue) != `"blue"` ||
		other.Type != FeatureFlagString || len(other.Rules) != 0 || string(other.DefaultValue) != `""` {
		t.Fatalf("forced type change: prod=%+v other=%+v", prod, other)
	}
}
