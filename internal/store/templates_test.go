package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestUserTemplatesCRUD(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tp := &UserTemplate{Key: "acme", Name: "Acme", Tags: []string{"a"}, Definition: []byte(`{"version":1}`), Visibility: TemplatePrivate, OwnerUserID: "usr_1"}
	if err := s.CreateTemplate(ctx, tp); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTemplate(ctx, &UserTemplate{Key: "acme", Name: "x", Definition: []byte(`{}`), Visibility: TemplatePrivate}); err != ErrConflict {
		t.Errorf("duplicate key: %v", err)
	}
	tp.Visibility, tp.Description = TemplateWorkspace, "desc"
	if err := s.UpdateTemplate(ctx, tp); err != nil {
		t.Fatal(err)
	}
	s.CountTemplateDeploy(ctx, "acme", false)
	s.CountTemplateDeploy(ctx, "postgres", true)
	s.CountTemplateDeploy(ctx, "postgres", true)
	got, err := s.GetTemplateByKey(ctx, "acme")
	if err != nil || got.Visibility != TemplateWorkspace || got.Description != "desc" || got.DeployCount != 1 || got.Tags[0] != "a" || string(got.Definition) != `{"version":1}` {
		t.Fatalf("get: %+v %v", got, err)
	}
	if c, _ := s.BuiltinTemplateDeploys(ctx); c["postgres"] != 2 {
		t.Errorf("builtin counts: %v", c)
	}
	if l, _ := s.ListTemplates(ctx); len(l) != 1 {
		t.Errorf("list: %d", len(l))
	}
	if err := s.DeleteTemplate(ctx, tp.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTemplateByKey(ctx, "acme"); err != ErrNotFound {
		t.Errorf("after delete: %v", err)
	}
}
