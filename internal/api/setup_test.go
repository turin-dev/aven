package api

import (
	"context"
	"testing"

	"github.com/turin-dev/aven/internal/store"
)

func TestSetupChecklist(t *testing.T) {
	f, h := authServer(t)
	_, ownerC := freshAccount(t, f, "boss@example.com", store.RoleOwner)
	_, adminC := freshAccount(t, f, "operator@example.com", store.RoleAdmin)
	if w := req(h, "GET", "/api/owner/setup", "", adminC); w.Code != 403 {
		t.Fatalf("instance admin: %d", w.Code)
	}
	if w := req(h, "PUT", "/api/owner/setup", `{"dismissed":true}`, adminC); w.Code != 403 {
		t.Fatalf("instance admin dismiss: %d", w.Code)
	}

	type item struct {
		ID       string `json:"id"`
		Done     bool   `json:"done"`
		Optional bool   `json:"optional"`
	}
	type resp struct {
		Items        []item `json:"items"`
		Required     int    `json:"required"`
		RequiredDone int    `json:"required_done"`
		Complete     bool   `json:"complete"`
		Dismissed    bool   `json:"dismissed"`
	}
	get := func() (resp, map[string]item) {
		w := req(h, "GET", "/api/owner/setup", "", ownerC)
		if w.Code != 200 {
			t.Fatalf("get setup: %d %s", w.Code, w.Body)
		}
		r := decodeBody[resp](t, w)
		m := map[string]item{}
		for _, it := range r.Items {
			m[it.ID] = it
		}
		return r, m
	}
	r, m := get()
	if r.Complete || r.Dismissed || r.Required == 0 {
		t.Fatalf("fresh setup: %+v", r)
	}
	// freshAccount가 만든 owner는 기본 계정이 아닌 개인 owner 계정이다. 2단계 인증과 공개 URL은 아직 없다.
	if !m["owner_account"].Done || m["mfa"].Done || m["public_url"].Done {
		t.Errorf("items: %+v", m)
	}
	// 첫 실행 때 생기는 빈 default 프로젝트만으로는 "첫 프로젝트 배포"가 끝나지 않는다.
	if insts, _ := f.st.ListInstances(context.Background()); m["first_project"].Done != (len(insts) > 0) {
		t.Errorf("first_project=%v with %d instances", m["first_project"].Done, len(insts))
	}
	if !m["github"].Optional || m["first_project"].Optional {
		t.Errorf("optional flags: %+v", m)
	}

	if w := req(h, "PUT", "/api/owner/setup", `{"dismissed":true}`, ownerC); w.Code != 200 {
		t.Fatalf("dismiss: %d %s", w.Code, w.Body)
	}
	if r, _ = get(); !r.Dismissed {
		t.Errorf("not dismissed after PUT")
	}
	if w := req(h, "PUT", "/api/owner/setup", `{"dismissed":false}`, ownerC); w.Code != 200 {
		t.Fatalf("undismiss: %d", w.Code)
	}
	if r, _ = get(); r.Dismissed {
		t.Errorf("still dismissed")
	}
}
