package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestDBPITRConfigAndObjectsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "keel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.GetDBPITRConfig(ctx, "ins_example")
	if err != nil || got.RetentionDays != 7 || got.Enabled {
		t.Fatalf("default config: %+v, %v", got, err)
	}
	baseAt := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	cfg := &DBPITRConfig{Enabled: true, RetentionDays: 14, DestinationID: "bkd_dest", Epoch: 2, PGLastLSN: "0/3000000", LastBaseAt: &baseAt}
	if err := s.SetDBPITRConfig(ctx, "ins_example", cfg); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetDBPITRConfig(ctx, "ins_example")
	if err != nil || !got.Enabled || got.RetentionDays != 14 || got.DestinationID != "bkd_dest" || got.Epoch != 2 || got.PGLastLSN != "0/3000000" || got.LastBaseAt == nil || !got.LastBaseAt.Equal(baseAt) {
		t.Fatalf("config round trip: %+v, %v", got, err)
	}

	if en, err := s.ListEnabledDBPITRConfigs(ctx); err != nil || en["ins_example"] == nil {
		t.Fatalf("enabled configs: %+v, %v", en, err)
	}
	obj := &DBPITRObject{InstanceID: "ins_example", Kind: "mysql", ObjectType: "base", Epoch: 2, FileName: "base.sql.gz", DestinationID: "bkd_dest", ObjectKey: "pitr/base.sql.gz", SizeBytes: 123, SourceTime: baseAt, BaseFile: "mysql-bin.000004", BasePosition: 9876}
	if err := s.UpsertDBPITRObject(ctx, obj); err != nil {
		t.Fatal(err)
	}
	obj.ObjectKey, obj.SizeBytes = "pitr/base-reuploaded.sql.gz", 456
	if err := s.UpsertDBPITRObject(ctx, obj); err != nil {
		t.Fatal(err)
	}
	objects, err := s.ListDBPITRObjects(ctx, "ins_example")
	if err != nil || len(objects) != 1 || objects[0].ObjectKey != obj.ObjectKey || objects[0].SizeBytes != 456 || objects[0].BaseFile != obj.BaseFile || objects[0].BasePosition != obj.BasePosition {
		t.Fatalf("object upsert: %+v, %v", objects, err)
	}
	if n, err := s.CountDBPITRObjectsOnDestination(ctx, "bkd_dest"); err != nil || n != 1 {
		t.Fatalf("count on destination: %d, %v", n, err)
	}
	if !objects[0].StartedAt.Equal(baseAt) || objects[0].Epoch != 2 {
		t.Fatalf("started_at/epoch: %+v", objects[0])
	}
	if err := s.DeleteDBPITRObject(ctx, objects[0].ID); err != nil {
		t.Fatal(err)
	}
	if objects, err = s.ListDBPITRObjects(ctx, "ins_example"); err != nil || len(objects) != 0 {
		t.Fatalf("objects after delete: %+v, %v", objects, err)
	}
}
