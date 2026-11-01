package api

import (
	"testing"

	"github.com/turin-dev/aven/internal/catalog"
	"github.com/turin-dev/aven/internal/store"
)

// A project member must not restore a backup over a database of a project they cannot access.
func TestRestoreDBBackupChecksTargetEnvironment(t *testing.T) {
	f, h := authServer(t)
	ctx := f.ctx
	dbIn, _ := f.st.GetInstanceFor(ctx, f.db.ID, f.prod.ID)
	bak := &store.DBBackup{ServiceID: f.db.ID, EnvironmentID: f.prod.ID, InstanceID: dbIn.ID, Kind: "postgres", Status: "done", Trigger: "manual"}
	if err := f.st.CreateDBBackup(ctx, bak); err != nil {
		t.Fatal(err)
	}
	// Project B with its own postgres, invisible to alice.
	other := &store.Project{Name: "other"}
	otherProd, err := f.st.CreateProject(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	otherDB := &store.Service{ProjectID: other.ID, Name: "db", SourceType: "database"}
	if err := f.st.CreateService(ctx, otherDB); err != nil {
		t.Fatal(err)
	}
	in := &store.Instance{ServiceID: otherDB.ID, EnvironmentID: otherProd.ID}
	def, _ := catalog.DatabaseService("postgres")
	def.ApplyToInstance(in)
	if err := f.st.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	// A restricted environment in alice's own project.
	staging := &store.Environment{ProjectID: f.p.ID, Name: "staging", Kind: store.EnvStandard}
	if err := f.st.CreateEnvironment(ctx, staging); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetEnvironmentRestricted(ctx, staging.ID, true); err != nil {
		t.Fatal(err)
	}
	stagingDB := &store.Instance{ServiceID: f.db.ID, EnvironmentID: staging.ID}
	def.ApplyToInstance(stagingDB)
	if err := f.st.CreateInstance(ctx, stagingDB); err != nil {
		t.Fatal(err)
	}

	alice := testUser(t, f.st, "alice@example.com", store.RoleMember)
	if err := f.st.SetMember(ctx, f.p.ID, alice.ID, store.RoleMember); err != nil {
		t.Fatal(err)
	}
	auth := withCookie(testSessionCookie(t, f.srv, alice))
	path := "/api/db-backups/" + bak.ID + "/restore"

	if w := req(h, "POST", path, `{"service_id":"`+otherDB.ID+`","environment_id":"`+otherProd.ID+`"}`, auth); w.Code != 404 {
		t.Errorf("restore into another project: %d %s", w.Code, w.Body)
	}
	if w := req(h, "POST", path, `{"environment_id":"`+staging.ID+`"}`, auth); w.Code != 403 {
		t.Errorf("restore into a restricted environment: %d %s", w.Code, w.Body)
	}
	// The own environment passes the permission check (the restore itself fails later in this fixture).
	if w := req(h, "POST", path, `{}`, auth); w.Code == 403 || w.Code == 404 {
		t.Errorf("restore into the backup's own instance: %d %s", w.Code, w.Body)
	}
}
