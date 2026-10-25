package api

import (
	"strings"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/store"
)

func TestParsePITRTimeAndRestoredName(t *testing.T) {
	want := time.Date(2026, 7, 20, 12, 0, 5, 0, time.UTC)
	for _, v := range []string{"2026-07-20T12:00:05Z", "2026-07-20T21:00:05+09:00", "2026-07-20 12:00:05", "2026-07-20T12:00:05.900Z"} {
		got, err := parsePITRTime(v)
		if err != nil || !got.Equal(want) {
			t.Errorf("%q → %v %v", v, got, err)
		}
	}
	if _, err := parsePITRTime("yesterday"); err == nil {
		t.Error("bad time accepted")
	}
	at := time.Date(2026, 7, 20, 12, 34, 0, 0, time.UTC)
	if n := restoredName("postgres", at); n != "postgres-restored-20260720-1234" {
		t.Errorf("name %q", n)
	}
	n := restoredName(strings.Repeat("a", 39), at)
	if len(n) > 40 || !nameRe.MatchString(n) || !strings.HasSuffix(n, "-restored-20260720-1234") {
		t.Errorf("long name %q", n)
	}
}

func TestPITRAPI(t *testing.T) {
	f := newGHFixture(t)
	code, out := f.call(f.srv.createService, "POST", "/x", `{"source_type":"database","database_kind":"postgres","name":"pg"}`, map[string]string{"pid": f.p.ID})
	if code != 201 {
		t.Fatalf("create: %d %v", code, out)
	}
	sid := out["id"].(string)
	in, _ := f.st.GetInstanceFor(f.ctx, sid, f.prod.ID)
	img := "postgres:17.2-alpine"
	d := &store.Deployment{ServiceID: sid, EnvironmentID: f.prod.ID, InstanceID: in.ID, Status: store.DepLive, Trigger: "manual",
		NodeID: store.LocalNodeID, Containers: []string{"keel-pg-1"}, Image: &img}
	if err := f.st.CreateDeployment(f.ctx, d); err != nil {
		t.Fatal(err)
	}
	f.st.SetInstanceStatus(f.ctx, in.ID, store.InstOnline, &d.ID)
	in, _ = f.st.GetInstanceFor(f.ctx, sid, f.prod.ID)
	pv := map[string]string{"sid": sid, "eid": f.prod.ID}

	code, out = f.call(f.srv.getPITR, "GET", "/x", "", pv)
	if code != 200 || out["supported"] != true || out["kind"] != "postgres" || out["config"].(map[string]any)["enabled"] != false {
		t.Fatalf("status: %d %v", code, out)
	}
	if code, out = f.call(f.srv.putPITR, "PUT", "/x", `{"retention_days":0}`, pv); code != 400 {
		t.Errorf("retention 0: %d %v", code, out)
	}
	if code, out = f.call(f.srv.putPITR, "PUT", "/x", `{"destination_id":"bkd_nope"}`, pv); code != 400 {
		t.Errorf("unknown destination: %d %v", code, out)
	}
	if code, out = f.call(f.srv.putPITR, "PUT", "/x", `{"retention_days":14}`, pv); code != 200 {
		t.Errorf("retention 14 while off: %d %v", code, out)
	}
	if cfg, _ := f.st.GetDBPITRConfig(f.ctx, in.ID); cfg.RetentionDays != 14 || cfg.Enabled {
		t.Errorf("config: %+v", cfg)
	}

	// redis는 시점 복구를 지원하지 않는다.
	code, out = f.call(f.srv.createService, "POST", "/x", `{"source_type":"database","database_kind":"redis","name":"cache"}`, map[string]string{"pid": f.p.ID})
	if code != 201 {
		t.Fatal(code, out)
	}
	rpv := map[string]string{"sid": out["id"].(string), "eid": f.prod.ID}
	if code, out = f.call(f.srv.putPITR, "PUT", "/x", `{"enabled":true}`, rpv); code != 400 || !strings.Contains(out["error"].(string), "PostgreSQL and MySQL") {
		t.Errorf("redis enable: %d %v", code, out)
	}
	if code, out = f.call(f.srv.getPITR, "GET", "/x", "", rpv); code != 200 || out["supported"] != false {
		t.Errorf("redis status: %d %v", code, out)
	}

	// 아직 보관된 것이 없으면 복원할 수 없다.
	now := time.Now().UTC().Truncate(time.Second)
	body := `{"target_time":"` + now.Add(-30*time.Minute).Format(time.RFC3339) + `"}`
	if code, out = f.call(f.srv.restorePITR, "POST", "/x", body, pv); code != 400 {
		t.Errorf("restore without archive: %d %v", code, out)
	}
	for _, o := range []*store.DBPITRObject{
		{InstanceID: in.ID, Kind: "postgres", ObjectType: "base", FileName: "b.tar.gz", ObjectKey: "k/b", StartedAt: now.Add(-61 * time.Minute), SourceTime: now.Add(-60 * time.Minute), SizeBytes: 10},
		{InstanceID: in.ID, Kind: "postgres", ObjectType: "wal", FileName: "000000010000000000000002", ObjectKey: "k/w2", SourceTime: now.Add(-40 * time.Minute), SizeBytes: 5},
		{InstanceID: in.ID, Kind: "postgres", ObjectType: "wal", FileName: "000000010000000000000003", ObjectKey: "k/w3", SourceTime: now.Add(-10 * time.Minute), SizeBytes: 5},
	} {
		if err := f.st.UpsertDBPITRObject(f.ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	code, out = f.call(f.srv.getPITR, "GET", "/x", "", pv)
	win := out["window"].(map[string]any)
	if code != 200 || win["earliest"] == nil || win["latest"] == nil || win["bases"].(float64) != 1 || win["logs"].(float64) != 2 || win["bytes"].(float64) != 20 {
		t.Fatalf("window: %d %v", code, out)
	}
	for name, b := range map[string]string{
		"before earliest": `{"target_time":"` + now.Add(-2*time.Hour).Format(time.RFC3339) + `"}`,
		"after latest":    `{"target_time":"` + now.Add(-5*time.Minute).Format(time.RFC3339) + `"}`,
		"future":          `{"target_time":"` + now.Add(time.Hour).Format(time.RFC3339) + `"}`,
		"bad time":        `{"target_time":"noon"}`,
		"bad name":        `{"target_time":"` + now.Add(-30*time.Minute).Format(time.RFC3339) + `","new_service_name":"Bad Name"}`,
	} {
		if code, out = f.call(f.srv.restorePITR, "POST", "/x", b, pv); code != 400 {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	if code, out = f.call(f.srv.restorePITR, "POST", "/x", `{"target_time":"`+now.Add(-30*time.Minute).Format(time.RFC3339)+`","new_service_name":"cache"}`, pv); code != 409 {
		t.Errorf("existing name: %d %v", code, out)
	}

	// 새 서비스로 복원: 원본 변수·이미지를 쓰는 서비스를 만들고 배포를 시작한다(복원은 첫 배포가 끝나면 진행된다).
	at := now.Add(-30 * time.Minute)
	code, out = f.call(f.srv.restorePITR, "POST", "/x", `{"target_time":"`+at.Format(time.RFC3339)+`"}`, pv)
	if code != 202 || out["status"] != "running" || out["in_place"] != false {
		t.Fatalf("restore: %d %v", code, out)
	}
	fork, err := f.st.GetService(f.ctx, out["target_service_id"].(string))
	if err != nil || fork.Name != restoredName("pg", at) || fork.DatabaseKind == nil || *fork.DatabaseKind != "postgres" {
		t.Fatalf("fork: %+v %v", fork, err)
	}
	fin, _ := f.st.GetInstanceFor(f.ctx, fork.ID, f.prod.ID)
	if fin.Image == nil || *fin.Image != img || fin.Variables["POSTGRES_PASSWORD"] != in.Variables["POSTGRES_PASSWORD"] || fin.Variables["POSTGRES_PASSWORD"] == "" {
		t.Errorf("fork instance: image %v vars %v", fin.Image, fin.Variables)
	}
	if rs := deploy.LastPITRRestore(in.ID); rs == nil || rs.TargetInstance != fin.ID {
		t.Errorf("restore status: %+v", rs)
	}
	if code, out = f.call(f.srv.restorePITR, "POST", "/x", `{"target_time":"`+at.Format(time.RFC3339)+`"}`, pv); code != 409 {
		t.Errorf("second restore: %d %v", code, out)
	}
	if code, _ = f.call(f.srv.getPITRRestore, "GET", "/x", "", pv); code != 200 {
		t.Errorf("restore status: %d", code)
	}
}
