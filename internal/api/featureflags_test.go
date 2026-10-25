package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/store"
)

func TestFeatureFlagAPIAndProjectTokenEvaluation(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "flags.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	project := &store.Project{Name: "flags-api"}
	prod, err := st.CreateProject(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	stage := &store.Environment{ProjectID: project.ID, Name: "staging"}
	if err := st.CreateEnvironment(ctx, stage); err != nil {
		t.Fatal(err)
	}

	token := func(role, scope, environment string) string {
		t.Helper()
		secret := "keel_feature_" + role + scope + environment
		pt := &store.APIToken{UserID: store.BuiltinUserID, Name: "feature-test", Prefix: secret[:13], Scope: scope, ProjectID: &project.ID, Role: role}
		if environment != "" {
			pt.Scope = store.ScopeEnvironment
			pt.EnvironmentID = &environment
		}
		if err := st.CreateAPIToken(ctx, pt, hashSecret(secret)); err != nil {
			t.Fatal(err)
		}
		return secret
	}
	adminToken := token(store.RoleAdmin, store.ScopeProject, "")
	viewerToken := token(store.RoleViewer, store.ScopeProject, "")
	envToken := token(store.RoleMember, store.ScopeEnvironment, prod.ID)
	srv := &Server{Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := srv.Handler()
	withToken := func(value string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+value) }
	}
	ifNoneMatch := func(value, etag string) func(*http.Request) {
		return func(r *http.Request) { withToken(value)(r); r.Header.Set("If-None-Match", etag) }
	}
	base := "/api/projects/" + project.ID + "/flags"
	body := `{"name":"checkout.v2","type":"bool","environment_id":"` + prod.ID + `","default_value":false,"rules":[{"id":"enterprise","when":"plan == \"enterprise\"","value":true},{"id":"rollout","when":"bucket(key) < 1","value":true}]}`
	w := req(h, http.MethodPost, base, body, withToken(adminToken))
	if w.Code != http.StatusCreated {
		t.Fatalf("create flag: %d %s", w.Code, w.Body.String())
	}

	w = req(h, http.MethodGet, base+"?environment_id="+prod.ID, "", withToken(viewerToken))
	if w.Code != http.StatusOK {
		t.Fatalf("viewer list: %d %s", w.Code, w.Body.String())
	}
	var snapshot featureFlagSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil || len(snapshot.Flags) != 1 || len(snapshot.Flags[0].Rules) != 2 {
		t.Fatalf("snapshot: %+v (%v)", snapshot, err)
	}

	w = req(h, http.MethodPost, "/api/feature-flags/evaluate", `{"name":"checkout.v2","environment_id":"`+prod.ID+`","context":{"key":"user-1","plan":"free"}}`, withToken(viewerToken))
	if w.Code != http.StatusOK {
		t.Fatalf("evaluate project token: %d %s", w.Code, w.Body.String())
	}
	var evaluated struct {
		Value  bool   `json:"value"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &evaluated); err != nil || !evaluated.Value || evaluated.Reason != "SPLIT" {
		t.Fatalf("evaluation: %+v (%v)", evaluated, err)
	}

	w = req(h, http.MethodPost, base, `{"name":"blocked","type":"bool","environment_id":"`+prod.ID+`","default_value":false}`, withToken(viewerToken))
	if w.Code != http.StatusForbidden {
		t.Fatalf("viewer created flag: %d %s", w.Code, w.Body.String())
	}
	w = req(h, http.MethodGet, base+"?environment_id="+stage.ID, "", withToken(envToken))
	if w.Code != http.StatusNotFound {
		t.Fatalf("environment token read another environment: %d %s", w.Code, w.Body.String())
	}
	w = req(h, http.MethodGet, "/api/feature-flags", "", withToken(envToken))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"environment_id":"`+prod.ID+`"`) {
		t.Fatalf("environment token snapshot: %d %s", w.Code, w.Body.String())
	}
	w = req(h, http.MethodGet, "/api/feature-flags?environment_id="+stage.ID, "", withToken(envToken))
	if w.Code != http.StatusNotFound {
		t.Fatalf("environment token read another environment snapshot: %d %s", w.Code, w.Body.String())
	}

	// Polling with the ETag of an unchanged snapshot answers 304.
	w = req(h, http.MethodGet, "/api/feature-flags", "", withToken(viewerToken))
	etag := w.Header().Get("ETag")
	if w.Code != http.StatusOK || etag == "" {
		t.Fatalf("snapshot etag: %d %q", w.Code, etag)
	}
	w = req(h, http.MethodGet, "/api/feature-flags", "", ifNoneMatch(viewerToken, etag))
	if w.Code != http.StatusNotModified {
		t.Fatalf("unchanged snapshot: %d", w.Code)
	}

	// Matching rules that disagree fall back to the default.
	w = req(h, http.MethodPut, base+"/checkout.v2", `{"environment_id":"`+prod.ID+`","rules":[{"id":"a","when":"plan == \"pro\"","value":true},{"id":"b","when":"bucket(key) < 1","value":false}]}`, withToken(adminToken))
	if w.Code != http.StatusOK {
		t.Fatalf("update rules: %d %s", w.Code, w.Body.String())
	}
	w = req(h, http.MethodGet, "/api/feature-flags", "", ifNoneMatch(viewerToken, etag))
	if w.Code != http.StatusOK {
		t.Fatalf("changed snapshot should not be 304: %d", w.Code)
	}
	w = req(h, http.MethodPost, "/api/feature-flags/evaluate", `{"name":"checkout.v2","context":{"key":"u","plan":"pro"}}`, withToken(viewerToken))
	if err := json.Unmarshal(w.Body.Bytes(), &evaluated); err != nil || evaluated.Value || evaluated.Reason != "CONFLICT" {
		t.Fatalf("conflicting rules: %d %s", w.Code, w.Body.String())
	}

	// Validation: bad expression, wrong value type, type change without force.
	for _, bad := range []string{
		`{"environment_id":"` + prod.ID + `","rules":[{"id":"x","when":"plan = \"pro\"","value":true}]}`,
		`{"environment_id":"` + prod.ID + `","default_value":"yes"}`,
	} {
		if w = req(h, http.MethodPut, base+"/checkout.v2", bad, withToken(adminToken)); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid update %s: %d %s", bad, w.Code, w.Body.String())
		}
	}
	if w = req(h, http.MethodPut, base+"/checkout.v2", `{"environment_id":"`+prod.ID+`","type":"string","default_value":"x"}`, withToken(adminToken)); w.Code != http.StatusConflict {
		t.Fatalf("type change without force: %d %s", w.Code, w.Body.String())
	}
	if w = req(h, http.MethodDelete, base+"/checkout.v2", "", withToken(adminToken)); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if w = req(h, http.MethodPost, "/api/feature-flags/evaluate", `{"name":"checkout.v2"}`, withToken(viewerToken)); w.Code != http.StatusNotFound {
		t.Fatalf("evaluate deleted flag: %d", w.Code)
	}
}
