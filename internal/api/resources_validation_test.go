package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/store"
)

func TestPatchInstanceHealthcheckPathValidation(t *testing.T) {
	f, h := authServer(t)
	path := "/api/services/" + f.web.ID + "/instances/" + f.prod.ID
	for _, tc := range []struct {
		path string
		code int
	}{
		{"/healthz", 200},
		{"/health?full=1", 200},
		{"/%zz", 400},
		{"/a b", 400},
		{"/a\nb", 400},
		{"health", 400},
		{"/" + strings.Repeat("a", 1100), 400},
	} {
		body, _ := jsonMarshal(map[string]any{"healthcheck_path": tc.path})
		if w := req(h, "PATCH", path, body, withBearer("legacy-token")); w.Code != tc.code {
			t.Errorf("%q: %d %s, want %d", tc.path, w.Code, w.Body, tc.code)
		}
	}
}

func TestPatchInstanceVariablesPatchOnEmptyVariables(t *testing.T) {
	f, h := authServer(t)
	path := "/api/services/" + f.web.ID + "/instances/" + f.prod.ID
	if w := req(h, "PATCH", path, `{"variables":null}`, withBearer("legacy-token")); w.Code != 200 {
		t.Fatalf("clear variables: %d %s", w.Code, w.Body)
	}
	w := req(h, "PATCH", path, `{"variables_patch":{"A":"1"}}`, withBearer("legacy-token"))
	if w.Code != 200 {
		t.Fatalf("variables_patch: %d %s", w.Code, w.Body)
	}
	in, err := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	if err != nil || in.Variables["A"] != "1" {
		t.Fatalf("variables: %v %v", in.Variables, err)
	}
}

func TestVolumeMountPathConflicts(t *testing.T) {
	f, h := authServer(t)
	auth := withBearer("legacy-token")
	create := func(mount string) *httptest.ResponseRecorder {
		body, _ := jsonMarshal(map[string]any{"service_id": f.web.ID, "mount_path": mount})
		return req(h, "POST", "/api/environments/"+f.prod.ID+"/volumes", body, auth)
	}
	if w := create("/data"); w.Code != 201 {
		t.Fatalf("create /data: %d %s", w.Code, w.Body)
	}
	if w := create("/data"); w.Code != 409 {
		t.Errorf("duplicate mount path: %d %s", w.Code, w.Body)
	}
	for _, bad := range []string{"/da\tta", "/data\x00", "/a,b", "relative", "/"} {
		if w := create(bad); w.Code != 400 {
			t.Errorf("mount %q: %d %s", bad, w.Code, w.Body)
		}
	}
	w := create("/cache")
	if w.Code != 201 {
		t.Fatalf("create /cache: %d %s", w.Code, w.Body)
	}
	cache := decodeBody[map[string]any](t, w)["id"].(string)
	// Moving /cache onto /data of the same instance must be refused.
	if w := req(h, "PATCH", "/api/volumes/"+cache, `{"mount_path":"/data"}`, auth); w.Code != 409 {
		t.Errorf("rename onto taken mount path: %d %s", w.Code, w.Body)
	}
	if w := req(h, "PATCH", "/api/volumes/"+cache, `{"mount_path":"/cache2"}`, auth); w.Code != 200 {
		t.Errorf("rename to free mount path: %d %s", w.Code, w.Body)
	}
	// A detached volume at /data cannot be attached to web, which already has a volume at /data.
	if w := req(h, "PATCH", "/api/volumes/"+cache, `{"service_instance_id":null,"mount_path":"/data"}`, auth); w.Code != 200 {
		t.Fatalf("detach: %d %s", w.Code, w.Body)
	}
	webIn, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	if w := req(h, "PATCH", "/api/volumes/"+cache, `{"service_instance_id":"`+webIn.ID+`"}`, auth); w.Code != 409 {
		t.Errorf("attach onto taken mount path: %d %s", w.Code, w.Body)
	}
}

func TestDomainsCannotClaimReservedHosts(t *testing.T) {
	f, h := authServer(t)
	fixtureProxy(t, f)
	f.srv.PublicURL = "https://Aven.Example.org"
	f.mgr.Cfg.SelfHost = "dash.example.org"
	auth := withBearer("legacy-token")
	base := "/api/services/" + f.web.ID + "/instances/" + f.prod.ID
	for _, tc := range []struct {
		host string
		code int
	}{
		{"aven.example.org", 400},
		{"AVEN.example.org", 400},
		{"dash.example.org", 400},
		{strings.Repeat(strings.Repeat("a", 63)+".", 4) + "com", 400}, // 259 characters
		{"shop.example.org", 201},
		{"*.shop.example.org", 201},
	} {
		w := req(h, "POST", base+"/domains", `{"host":"`+tc.host+`"}`, auth)
		if w.Code != tc.code {
			t.Errorf("add %s: %d %s, want %d", tc.host, w.Code, w.Body, tc.code)
		}
	}
	// The whole-list PATCH path enforces the same rule.
	if w := req(h, "PATCH", base, `{"domains":[{"host":"aven.example.org"}]}`, auth); w.Code != 400 {
		t.Errorf("PATCH domains with the public host: %d %s", w.Code, w.Body)
	}
	// A reserved host saved before this check does not block edits of the other domains.
	in, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	in.Domains = append(in.Domains, store.Domain{Host: "aven.example.org", Kind: "custom"})
	if err := f.st.UpdateInstanceConfig(f.ctx, in); err != nil {
		t.Fatal(err)
	}
	if w := req(h, "PATCH", base+"/domains/shop.example.org", `{"tls":"internal"}`, auth); w.Code != 200 {
		t.Errorf("edit another domain next to a legacy reserved host: %d %s", w.Code, w.Body)
	}
}

func TestDomainsPerInstanceLimit(t *testing.T) {
	f, h := authServer(t)
	fixtureProxy(t, f)
	doms := make([]map[string]string, 0, maxDomainsPerInstance+1)
	for i := 0; i <= maxDomainsPerInstance; i++ {
		doms = append(doms, map[string]string{"host": "h" + itoa(i) + ".example.org"})
	}
	body, _ := jsonMarshal(map[string]any{"domains": doms})
	if w := req(h, "PATCH", "/api/services/"+f.web.ID+"/instances/"+f.prod.ID, body, withBearer("legacy-token")); w.Code != 400 {
		t.Errorf("too many domains: %d %.200s", w.Code, w.Body)
	}
}

func TestEnvTracingUnknownServiceChangesNothing(t *testing.T) {
	f, h := authServer(t)
	fixtureProxy(t, f)
	auth := withBearer("legacy-token")
	path := "/api/environments/" + f.prod.ID + "/tracing"
	body := `{"enabled":true,"service_ids":["` + f.web.ID + `","svc_missing"]}`
	if w := req(h, "PUT", path, body, auth); w.Code != 404 {
		t.Fatalf("unknown service: %d %s", w.Code, w.Body)
	}
	in, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	if st, err := f.st.GetTracingSetting(f.ctx, in.ID); err == nil && st.Enabled {
		t.Errorf("web tracing was enabled by a rejected request")
	}
	if w := req(h, "PUT", path, `{"enabled":true,"service_ids":["`+f.web.ID+`"]}`, auth); w.Code != 200 {
		t.Fatalf("known service: %d %s", w.Code, w.Body)
	}
	if st, err := f.st.GetTracingSetting(f.ctx, in.ID); err != nil || !st.Enabled {
		t.Errorf("web tracing: %+v %v", st, err)
	}
}

func TestRollbackKeepsVariablesWhenEnqueueFails(t *testing.T) {
	f, h := authServer(t)
	in, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	img := "ghcr.io/acme/web@sha256:" + strings.Repeat("a", 64)
	dep := &store.Deployment{ServiceID: f.web.ID, EnvironmentID: f.prod.ID, InstanceID: in.ID, Trigger: "manual",
		Status: store.DepLive, Image: &img, VarsRaw: map[string]string{"A": "old"}, Spec: in.Spec, ReqSpec: in.Spec}
	if err := f.st.CreateDeployment(f.ctx, dep); err != nil {
		t.Fatal(err)
	}
	in.Variables = map[string]string{"A": "new"}
	in.NodeID = "node_gone" // Enqueue fails: the node does not exist
	if err := f.st.UpdateInstanceConfig(f.ctx, in); err != nil {
		t.Fatal(err)
	}
	if w := req(h, "POST", "/api/deployments/"+dep.ID+"/rollback", "", withBearer("legacy-token")); w.Code < 400 {
		t.Fatalf("rollback to an unavailable node: %d %s", w.Code, w.Body)
	}
	got, _ := f.st.GetInstance(f.ctx, in.ID)
	if got.Variables["A"] != "new" {
		t.Errorf("variables after a failed rollback: %v", got.Variables)
	}
}
