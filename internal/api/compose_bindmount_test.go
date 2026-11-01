package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/turin-dev/aven/internal/store"
)

// bind mount는 호스트 파일을 컨테이너에 연다: allow_bind_mounts를 보내도 서버 관리자가 아니면 가져올 수 없다.
func TestComposeBindMountsRequireServerAdmin(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	s := &Server{Store: st, AdminPassword: "password123", SessionKey: make([]byte, 32), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := s.Handler()
	p := &store.Project{Name: "binds"}
	env, err := st.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	owner := testUser(t, st, "owner@example.com", store.RoleOwner)
	member := testUser(t, st, "member@example.com", store.RoleMember)
	if err := st.SetMember(ctx, p.ID, member.ID, store.RoleMember); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"source": "yaml", "allow_bind_mounts": true,
		"yaml": "services:\n  pwn:\n    image: alpine:3\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n"})
	path := "/api/projects/" + p.ID + "/environments/" + env.ID + "/compose/preview"
	codes := func(u *store.User) (bool, map[string]bool) {
		w := req(h, "POST", path, string(body), withCookie(testSessionCookie(t, s, u)))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", u.Email, w.Code, w.Body.String())
		}
		var res struct {
			OK   bool `json:"ok"`
			Plan struct {
				Errors []struct{ Code string } `json:"errors"`
			} `json:"plan"`
		}
		json.Unmarshal(w.Body.Bytes(), &res)
		out := map[string]bool{}
		for _, e := range res.Plan.Errors {
			out[e.Code] = true
		}
		return res.OK, out
	}
	if ok, errs := codes(member); ok || !errs["bind_mount_admin"] {
		t.Fatalf("member: ok=%v errors=%v", ok, errs)
	}
	if ok, errs := codes(owner); !ok || errs["bind_mount_admin"] {
		t.Fatalf("owner: ok=%v errors=%v", ok, errs)
	}
}
