package vars

import (
	"errors"
	"strings"
	"testing"
)

func TestResolve(t *testing.T) {
	s := &Scope{
		Shared: map[string]string{"REGION": "region-a", "LABEL": "${{ REGION }}-prod"},
		Services: map[string]map[string]string{
			"postgres": {
				"AVEN_PRIVATE_DOMAIN": "postgres.production.aven.internal",
				"POSTGRES_PASSWORD":   "s3cret",
				"PGUSER":              "postgres",
				"DATABASE_URL":        "postgresql://${{PGUSER}}:${{POSTGRES_PASSWORD}}@${{AVEN_PRIVATE_DOMAIN}}:5432/app",
			},
			"web": {
				"PORT":   "3000",
				"DB":     "${{ postgres.DATABASE_URL }}",
				"WHERE":  "${{shared.LABEL}}",
				"SELF":   "port=${{PORT}}",
				"NOPE":   "x${{ redis.REDIS_URL }}y",
				"PLAIN":  "no refs $HOME ${NOT_A_REF}",
				"NESTED": "${{SELF}}/${{DB}}",
			},
		},
	}
	res, err := Resolve(s, "web")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"DB":     "postgresql://postgres:s3cret@postgres.production.aven.internal:5432/app",
		"WHERE":  "region-a-prod",
		"SELF":   "port=3000",
		"NOPE":   "xy",
		"PLAIN":  "no refs $HOME ${NOT_A_REF}",
		"NESTED": "port=3000/postgresql://postgres:s3cret@postgres.production.aven.internal:5432/app",
		"PORT":   "3000",
	}
	for k, v := range want {
		if res.Vars[k] != v {
			t.Errorf("%s = %q, want %q", k, res.Vars[k], v)
		}
	}
	if len(res.Missing) != 1 || res.Missing[0] != "redis.REDIS_URL" {
		t.Errorf("missing = %v", res.Missing)
	}
}

func TestCycle(t *testing.T) {
	s := &Scope{Services: map[string]map[string]string{
		"a": {"X": "${{ b.Y }}", "OK": "fine"},
		"b": {"Y": "${{ Z }}", "Z": "${{ a.X }}"},
	}}
	_, err := Resolve(s, "a")
	var ce *CycleError
	if !errors.As(err, &ce) {
		t.Fatalf("want cycle error, got %v", err)
	}
	msg := ce.Error()
	for _, p := range []string{"a.X", "b.Y", "b.Z"} {
		if !strings.Contains(msg, p) {
			t.Errorf("cycle message %q missing %s", msg, p)
		}
	}
	// 자기 자신 참조
	s = &Scope{Services: map[string]map[string]string{"a": {"X": "${{X}}"}}}
	if _, err := Resolve(s, "a"); !errors.As(err, &ce) {
		t.Fatalf("self reference: want cycle, got %v", err)
	}
}

func TestRenameService(t *testing.T) {
	got := RenameService("${{postgres.DATABASE_URL}} ${{ postgres.PGHOST }} ${{PORT}} ${{other.X}}", "postgres", "postgres-2")
	want := "${{postgres-2.DATABASE_URL}} ${{postgres-2.PGHOST}} ${{PORT}} ${{other.X}}"
	if got != want {
		t.Errorf("got %q", got)
	}
}

// $${{ X }}는 글자 그대로의 ${{ X }}다: 해석하지 않고, 참조 목록·이름 바꾸기에서도 빠진다.
func TestEscapedReference(t *testing.T) {
	s := &Scope{Services: map[string]map[string]string{
		"web": {
			"PORT":     "3000",
			"TEMPLATE": "on: $${{ github.event }} port=${{PORT}}",
			"DOLLARS":  "$$${{PORT}}",
			"COPY":     "${{TEMPLATE}}",
			"MISSING":  "$${{ nope.X }}",
		},
	}}
	res, err := Resolve(s, "web")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"TEMPLATE": "on: ${{ github.event }} port=3000",
		"DOLLARS":  "$${{PORT}}",
		"COPY":     "on: ${{ github.event }} port=3000", // 참조한 값 안의 글자 ${{ }}는 다시 해석하지 않는다
		"MISSING":  "${{ nope.X }}",
	}
	for k, v := range want {
		if res.Vars[k] != v {
			t.Errorf("%s = %q, want %q", k, res.Vars[k], v)
		}
	}
	if len(res.Missing) != 0 {
		t.Errorf("escaped refs reported missing: %v", res.Missing)
	}
	if refs := References("$${{ db.URL }} ${{ cache.URL }}"); len(refs) != 1 || refs[0] != "cache.URL" {
		t.Errorf("references = %v", refs)
	}
	if got := RenameService("$${{db.URL}} ${{db.URL}}", "db", "pg"); got != "$${{db.URL}} ${{pg.URL}}" {
		t.Errorf("rename = %q", got)
	}
	// Escape한 값을 다시 해석하면 원래 값이 나온다.
	for _, v := range []string{"plain", "${{ X }}", "a $${{Y}} b", "$$${{Z}}"} {
		r, err := Resolve(&Scope{Services: map[string]map[string]string{"s": {"V": Escape(v)}}}, "s")
		if err != nil || r.Vars["V"] != v || len(r.Missing) != 0 {
			t.Errorf("round trip %q: %q %v %v", v, r.Vars["V"], r.Missing, err)
		}
	}
}
