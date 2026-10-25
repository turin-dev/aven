package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/store"
)

// newFunctionFixture는 scaleAPIFixture 위에 createService로 function 서비스를 만든다.
func newFunctionFixture(t *testing.T, body string) (*scaleAPIFixture, *httptest.ResponseRecorder) {
	t.Helper()
	f := newScaleAPIFixture(t)
	r := httptest.NewRequest("POST", "/x", bytes.NewBufferString(body))
	r.SetPathValue("pid", f.svc.ProjectID)
	w := httptest.NewRecorder()
	f.srv.createService(w, r)
	if w.Code != 201 {
		return f, w
	}
	var svc store.Service
	json.Unmarshal(w.Body.Bytes(), &svc)
	sv, err := f.st.GetService(context.Background(), svc.ID)
	if err != nil {
		t.Fatal(err)
	}
	in, err := f.st.GetInstanceFor(context.Background(), sv.ID, f.env.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.svc, f.in = sv, in
	return f, w
}

func (f *scaleAPIFixture) doQ(t *testing.T, h http.HandlerFunc, method, query, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, "/x"+query, bytes.NewBufferString(body))
	r.SetPathValue("sid", f.svc.ID)
	r.SetPathValue("eid", f.env.ID)
	w := httptest.NewRecorder()
	h(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func codeJSON(code string) string {
	b, _ := json.Marshal(map[string]string{"code": code})
	return string(b)
}

func TestCreateFunctionServiceDefaultsAndCustomCode(t *testing.T) {
	f, w := newFunctionFixture(t, `{"source_type":"function"}`)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if f.svc.SourceType != "function" || f.svc.Name != "function" {
		t.Fatalf("service %+v", f.svc)
	}
	w, v := f.do(t, f.srv.getFunction, "GET", "")
	if w.Code != 200 || v["code"] != deploy.DefaultFunctionCode || v["runtime"] != "bun" || v["deployed_sha256"] != nil {
		t.Fatalf("get: %d %v", w.Code, v)
	}
	if !strings.Contains(deploy.DefaultFunctionCode, "Bun.serve") {
		t.Fatal("default code is not a Bun.serve hello world")
	}

	f2, w := newFunctionFixture(t, `{"source_type":"function","name":"hello","code":"console.log('hi')"}`)
	if w.Code != 201 || f2.svc.Name != "hello" {
		t.Fatalf("create custom: %d %s", w.Code, w.Body.String())
	}
	if fc, err := f2.st.GetFunctionCode(context.Background(), f2.in.ID); err != nil || fc.Code != "console.log('hi')" {
		t.Fatalf("code %+v %v", fc, err)
	}
	// image 서비스에서는 function API를 쓸 수 없다.
	f3 := newScaleAPIFixture(t)
	if w, _ := f3.do(t, f3.srv.getFunction, "GET", ""); w.Code != 400 {
		t.Fatalf("image service: %d", w.Code)
	}
}

func TestFunctionSizeLimit(t *testing.T) {
	big := strings.Repeat("a", store.MaxFunctionCodeBytes+1)
	if _, w := newFunctionFixture(t, `{"source_type":"function","code":"`+big+`"}`); w.Code != 413 {
		t.Fatalf("create oversized: %d", w.Code)
	}
	f, _ := newFunctionFixture(t, `{"source_type":"function"}`)
	if w, _ := f.do(t, f.srv.putFunction, "PUT", codeJSON(big)); w.Code != 413 {
		t.Fatalf("put oversized: %d", w.Code)
	}
	if w, _ := f.do(t, f.srv.putFunction, "PUT", codeJSON(big[:store.MaxFunctionCodeBytes])); w.Code != 200 {
		t.Fatalf("put 1 MiB: %d %s", w.Code, w.Body.String()[:min(200, w.Body.Len())])
	}
	if _, err := f.st.SetFunctionCode(context.Background(), f.in.ID, big); err == nil {
		t.Fatal("store accepted oversized code")
	}
	for name, body := range map[string]string{"no code": `{}`, "runtime": `{"code":"x","runtime":"node"}`} {
		if w, _ := f.do(t, f.srv.putFunction, "PUT", body); w.Code != 400 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
}

func TestFunctionPendingAndDeploy(t *testing.T) {
	ctx := context.Background()
	f, _ := newFunctionFixture(t, `{"source_type":"function","code":"v1"}`)
	// 첫 배포(실행은 하지 않는다): 만들 때의 코드가 스냅샷으로 남는다.
	w, _ := f.doQ(t, f.srv.putFunction, "PUT", "?deploy=1", codeJSON("v1"))
	if w.Code != 200 {
		t.Fatalf("put+deploy: %d %s", w.Code, w.Body.String())
	}
	deps, err := f.st.ListDeployments(ctx, f.in.ID, 10)
	if err != nil || len(deps) != 1 || deps[0].Trigger != "manual" {
		t.Fatalf("deployments %v %v", deps, err)
	}
	d := deps[0]
	if sha := f.st.DeploymentFunctionSHA(ctx, d.ID); sha != store.FunctionSHA("v1") {
		t.Fatalf("snapshot sha %q", sha)
	}
	f.st.SetInstanceStatus(ctx, f.in.ID, store.InstOnline, &d.ID)

	pending := func() []any {
		t.Helper()
		w, v := f.do(t, f.srv.getInstance, "GET", "")
		if w.Code != 200 {
			t.Fatalf("get instance: %d", w.Code)
		}
		return v["pending_changes"].([]any)
	}
	if p := pending(); len(p) != 0 {
		t.Fatalf("pending after deploy: %v", p)
	}
	// 코드를 바꾸면 function.code가 pending이 되고, 되돌리면 사라진다.
	w, v := f.do(t, f.srv.putFunction, "PUT", codeJSON("v2"))
	if w.Code != 200 || v["code"] != "v2" || v["deployed_sha256"] != store.FunctionSHA("v1") || v["sha256"] != store.FunctionSHA("v2") {
		t.Fatalf("put v2: %d %v", w.Code, v)
	}
	if p := pending(); len(p) != 1 || p[0] != PendingFunctionCode {
		t.Fatalf("pending after edit: %v", p)
	}
	f.do(t, f.srv.putFunction, "PUT", codeJSON("v1"))
	if p := pending(); len(p) != 0 {
		t.Fatalf("pending after revert: %v", p)
	}
	// 변수와 함께 바뀌면 둘 다 정렬되어 나온다.
	f.do(t, f.srv.putFunction, "PUT", codeJSON("v3"))
	f.do(t, f.srv.patchInstance, "PATCH", `{"variables_patch":{"A":"1"}}`)
	if p := pending(); len(p) != 2 || p[0] != PendingFunctionCode || p[1] != "variables.A" {
		t.Fatalf("pending with vars: %v", p)
	}
	// 일반 배포 엔드포인트도 function 서비스를 소스 오류 없이 배포한다.
	if err := sourceErr(f.svc, f.in); err != nil {
		t.Fatalf("sourceErr: %v", err)
	}
	// 새 환경은 원본 환경의 코드를 이어받는다(원본이 없으면 기본 코드).
	env2 := &store.Environment{ProjectID: f.svc.ProjectID, Name: "staging"}
	if err := f.st.CreateEnvironment(ctx, env2); err != nil {
		t.Fatal(err)
	}
	in2 := &store.Instance{ServiceID: f.svc.ID, EnvironmentID: env2.ID}
	if err := f.st.CreateInstance(ctx, in2); err != nil {
		t.Fatal(err)
	}
	if err := f.srv.copyFunctionCode(ctx, f.svc, f.in, in2); err != nil {
		t.Fatal(err)
	}
	if fc, err := f.st.GetFunctionCode(ctx, in2.ID); err != nil || fc.Code != "v3" {
		t.Fatalf("copied code %+v %v", fc, err)
	}
	if err := f.srv.copyFunctionCode(ctx, f.svc, nil, in2); err != nil {
		t.Fatal(err)
	}
	if fc, _ := f.st.GetFunctionCode(ctx, in2.ID); fc.Code != deploy.DefaultFunctionCode {
		t.Fatalf("default code not set: %q", fc.Code)
	}
}

func TestFunctionRoutesRBAC(t *testing.T) {
	f := newRBACFixture(t)
	for _, m := range []string{"GET", "PUT"} {
		path := f.fill(m + " /api/services/{sid}/instances/{eid}/function")
		if got := req(f.h, m, path, "", withCookie(f.ownerC)).Code; got != probeStatus {
			t.Errorf("%s owner: %d", m, got)
		}
		if got := req(f.h, m, path, "", withCookie(f.viewerC)).Code; got != 403 {
			t.Errorf("%s viewer: %d, want 403", m, got)
		}
		if got := req(f.h, m, path, "", withCookie(f.outsideC)).Code; got != 404 && got != 403 {
			t.Errorf("%s non-member: %d", m, got)
		}
		if got := req(f.h, m, path, "", nil).Code; got != 401 {
			t.Errorf("%s anonymous: %d", m, got)
		}
	}
}
