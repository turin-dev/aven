package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/store"
)

func TestResourceLimitsSandboxAndScaleResponses(t *testing.T) {
	ctx := context.Background()
	t.Run("sandbox", func(t *testing.T) {
		f := newSbxFixture(t)
		cpu := 1.0
		if err := f.s.Store.SetWorkspaceResourceLimits(ctx, store.DefaultWorkspaceID, store.ResourceLimits{CPU: &cpu}); err != nil {
			t.Fatal(err)
		}
		path := "/api/environments/" + f.env.ID + "/sandboxes"
		f.do("POST", path, map[string]any{}, 201)
		f.do("POST", path, map[string]any{}, 409)
		if len(f.fake.specs) != 1 {
			t.Fatal("over-quota sandbox started")
		}
		rows, err := f.s.Store.ListSandboxes(ctx, f.env.ID)
		if err != nil || len(rows) != 1 {
			t.Fatalf("rejected sandbox persisted: %+v %v", rows, err)
		}
	})
	t.Run("scale", func(t *testing.T) {
		f := newScaleAPIFixture(t)
		cpu := 1.0
		f.in.CPULimit = &cpu
		if err := f.st.UpdateInstanceConfig(ctx, f.in); err != nil {
			t.Fatal(err)
		}
		d := &store.Deployment{ServiceID: f.svc.ID, EnvironmentID: f.env.ID, InstanceID: f.in.ID, Spec: f.in.Spec, Image: f.in.Image, Status: store.DepLive, Containers: []string{"quota-test-container"}}
		if err := f.st.CreateDeployment(ctx, d); err != nil {
			t.Fatal(err)
		}
		if err := f.st.SetInstanceStatus(ctx, f.in.ID, store.InstOnline, &d.ID); err != nil {
			t.Fatal(err)
		}
		if err := f.st.SetWorkspaceResourceLimits(ctx, store.DefaultWorkspaceID, store.ResourceLimits{CPU: &cpu}); err != nil {
			t.Fatal(err)
		}
		w, _ := f.do(t, f.srv.scaleInstance, "POST", `{"replicas":2}`)
		if w.Code != 409 || !strings.Contains(w.Body.String(), "자원 한도") {
			t.Fatalf("scale quota response: %d %s", w.Code, w.Body)
		}
	})
}

func TestResourceLimitsAuthorityAndAdmission(t *testing.T) {
	f, h := authServer(t)
	ctx := context.Background()
	_, operatorC := freshAccount(t, f, "resource-operator@example.com", store.RoleAdmin)
	owner, ownerC := freshAccount(t, f, "resource-owner@example.com", store.RoleViewer)
	_, otherC := freshAccount(t, f, "resource-other@example.com", store.RoleMember)
	ws, err := f.st.CreateWorkspace(ctx, "Limited", owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	global := "/api/instance/resource-limits"
	local := "/api/workspaces/" + ws.ID + "/resource-limits"
	for _, tc := range []struct {
		method, path, body string
		auth               func(*http.Request)
		want               int
	}{
		{"GET", global, "", operatorC, 200},
		{"GET", global, "", ownerC, 403},
		{"GET", local, "", ownerC, 200},
		{"GET", local, "", otherC, 404},
		{"GET", local, "", operatorC, 404},
		{"PUT", global + "/" + ws.ID, `{"cpu_cores":2}`, ownerC, 403},
		{"PUT", global + "/" + ws.ID, `{"cpu_cores":-1}`, operatorC, 400},
		{"PUT", global + "/missing", `{"cpu_cores":1}`, operatorC, 404},
		{"PUT", global + "/" + ws.ID, `{"cpu_cores":1,"memory_mb":512,"volume_mb":1024}`, operatorC, 200},
	} {
		w := req(h, tc.method, tc.path, tc.body, tc.auth)
		if w.Code != tc.want {
			t.Fatalf("%s %s: %d %s, want %d", tc.method, tc.path, w.Code, w.Body, tc.want)
		}
	}
	p := &store.Project{Name: "limited", WorkspaceID: ws.ID}
	env, err := f.st.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	w := req(h, "POST", "/api/projects/"+p.ID+"/services", `{"source_type":"image","image":"nginx:alpine","name":"one"}`, ownerC)
	if w.Code != 201 {
		t.Fatalf("create within quota: %d %s", w.Code, w.Body)
	}
	w = req(h, "POST", "/api/projects/"+p.ID+"/services", `{"source_type":"image","image":"nginx:alpine","name":"two"}`, ownerC)
	if w.Code != 409 {
		t.Fatalf("create over quota: %d %s", w.Code, w.Body)
	}
	services, err := f.st.ListServices(ctx, p.ID)
	if err != nil || len(services) != 1 {
		t.Fatalf("failed creation left a service: %+v %v", services, err)
	}
	ins, err := f.st.ListInstancesByEnv(ctx, env.ID)
	if err != nil || len(ins) != 1 {
		t.Fatalf("instances: %+v %v", ins, err)
	}
	path := "/api/services/" + services[0].ID + "/instances/" + env.ID
	for _, body := range []string{`{"cpu_limit":null}`, `{"memory_limit_mb":1024}`, `{"replicas":2}`} {
		if w := req(h, "PATCH", path, body, ownerC); w.Code != 409 {
			t.Fatalf("resource edit %s: %d %s", body, w.Code, w.Body)
		}
	}
	w = req(h, "POST", "/api/projects/"+p.ID+"/environments", fmt.Sprintf(`{"name":"copy","copy_from":%q}`, env.ID), ownerC)
	if w.Code != 409 {
		t.Fatalf("environment quota: %d %s", w.Code, w.Body)
	}
	envs, err := f.st.ListEnvironments(ctx, p.ID)
	if err != nil || len(envs) != 1 {
		t.Fatalf("failed copy left environment: %+v %v", envs, err)
	}
	state := decodeBody[workspaceResources](t, req(h, "GET", local, "", ownerC))
	if state.Allocated.CPU != 1 || state.Allocated.MemoryMB != 512 {
		t.Fatalf("allocated: %+v", state)
	}
}
