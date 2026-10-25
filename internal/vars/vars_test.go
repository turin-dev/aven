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
				"KEEL_PRIVATE_DOMAIN": "postgres.production.keel.internal",
				"POSTGRES_PASSWORD":   "s3cret",
				"PGUSER":              "postgres",
				"DATABASE_URL":        "postgresql://${{PGUSER}}:${{POSTGRES_PASSWORD}}@${{KEEL_PRIVATE_DOMAIN}}:5432/app",
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
		"DB":     "postgresql://postgres:s3cret@postgres.production.keel.internal:5432/app",
		"WHERE":  "region-a-prod",
		"SELF":   "port=3000",
		"NOPE":   "xy",
		"PLAIN":  "no refs $HOME ${NOT_A_REF}",
		"NESTED": "port=3000/postgresql://postgres:s3cret@postgres.production.keel.internal:5432/app",
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
