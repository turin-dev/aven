package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/turin-dev/aven/internal/store"
)

func TestImageUpdateSettingsAndRBAC(t *testing.T) {
	f, h := authServer(t)
	ctx := context.Background()
	owner := withBearer("legacy-token")
	viewer := testUser(t, f.st, "v@example.com", store.RoleViewer)
	f.st.SetMember(ctx, f.p.ID, viewer.ID, store.RoleViewer)
	vc := withCookie(testSessionCookie(t, f.srv, viewer))

	repoInst := "/api/services/" + f.web.ID + "/instances/" + f.prod.ID
	if w := req(h, "PATCH", repoInst, `{"image_update_policy":"minor"}`, owner); w.Code != 400 {
		t.Fatalf("repo service accepted image auto updates: %d %s", w.Code, w.Body)
	}
	if w := req(h, "GET", repoInst+"/image-updates", "", owner); w.Code != 400 {
		t.Fatalf("repo service image updates: %d", w.Code)
	}

	svc := &store.Service{ProjectID: f.p.ID, Name: "img", SourceType: "image"}
	if err := f.st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	in := &store.Instance{ServiceID: svc.ID, EnvironmentID: f.prod.ID}
	image := "ghcr.io/acme/app:1.2.3"
	in.Image = &image
	if err := f.st.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	inst := "/api/services/" + svc.ID + "/instances/" + f.prod.ID
	for _, bad := range []string{
		`{"image_update_policy":"major"}`,
		`{"image_update_policy":"minor","image_update_window":"hourly"}`,
		`{"image_update_policy":"minor","image_update_window":"custom","image_update_days":[]}`,
		`{"image_update_policy":"minor","image_update_window":"custom","image_update_days":[1,7]}`,
		`{"image_update_policy":"minor","image_update_window":"custom","image_update_days":[1],"image_update_start_hour":6,"image_update_end_hour":2}`,
	} {
		if w := req(h, "PATCH", inst, bad, owner); w.Code != 400 {
			t.Errorf("%s: %d %s", bad, w.Code, w.Body)
		}
	}
	if w := req(h, "PATCH", inst, `{"image_update_policy":"minor"}`, vc); w.Code != 403 {
		t.Errorf("viewer changed image update settings: %d", w.Code)
	}
	w := req(h, "PATCH", inst, `{"image_update_policy":"patch","image_update_window":"custom","image_update_days":[0,6],"image_update_start_hour":1,"image_update_end_hour":5}`, owner)
	if w.Code != 200 {
		t.Fatalf("save settings: %d %s", w.Code, w.Body)
	}
	var got struct {
		Policy  string   `json:"image_update_policy"`
		Window  string   `json:"image_update_window"`
		Days    []int    `json:"image_update_days"`
		Start   int      `json:"image_update_start_hour"`
		End     int      `json:"image_update_end_hour"`
		Pending []string `json:"pending_changes"`
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.Policy != "patch" || got.Window != "custom" || len(got.Days) != 2 || got.Start != 1 || got.End != 5 {
		t.Fatalf("saved settings: %s", w.Body)
	}
	for _, p := range got.Pending {
		if len(p) > 13 && p[:13] == "image_update_" {
			t.Errorf("image update settings must not need a redeploy: %v", got.Pending)
		}
	}

	// 기록과 건너뛰기
	u, err := f.st.SaveAvailableImageUpdate(ctx, &store.ImageUpdate{InstanceID: in.ID, CurrentImage: image,
		TargetImage: "ghcr.io/acme/app:1.2.4", Digest: "sha256:p", DigestImage: "ghcr.io/acme/app@sha256:p"})
	if err != nil {
		t.Fatal(err)
	}
	if w := req(h, "GET", inst+"/image-updates", "", vc); w.Code != 200 {
		t.Fatalf("viewer list: %d", w.Code)
	}
	if w := req(h, "POST", inst+"/image-updates/"+u.ID+"/skip", "", vc); w.Code != 403 {
		t.Errorf("viewer skipped an update: %d", w.Code)
	}
	if w := req(h, "POST", inst+"/image-updates/"+u.ID+"/apply", "", vc); w.Code != 403 {
		t.Errorf("viewer applied an update: %d", w.Code)
	}
	w = req(h, "POST", inst+"/image-updates/"+u.ID+"/skip", "", owner)
	var list []store.ImageUpdate
	if json.Unmarshal(w.Body.Bytes(), &list); w.Code != 200 || len(list) != 1 || list[0].Status != "skipped" {
		t.Fatalf("skip: %d %s", w.Code, w.Body)
	}
	if w := req(h, "POST", inst+"/image-updates/"+u.ID+"/skip", "", owner); w.Code != 409 {
		t.Errorf("skip twice: %d", w.Code)
	}
	other := "/api/services/" + f.other.ID + "/instances/" + f.prod.ID
	if w := req(h, "POST", other+"/image-updates/"+u.ID+"/apply", "", owner); w.Code != 400 && w.Code != 404 {
		t.Errorf("update applied through another instance: %d", w.Code)
	}
}
