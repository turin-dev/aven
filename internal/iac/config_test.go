package iac

import (
	"strings"
	"testing"
)

func TestDecodeJSONReportsPaths(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`[]`, "definition: must be an object"},
		{`{"version":1,"services":[],"servics":[]}`, "definition.servics: unknown field (allowed: version, project_name, partial, services)"},
		{`{"version":"1","services":[]}`, "version: must be an integer"},
		{`{"version":2,"services":[]}`, "version: must be 1 (got 2)"},
		{`{"version":1,"services":{}}`, "services: must be a list"},
		{`{"version":1,"services":[{"name":"web","setings":{}}]}`, "services[0].setings: unknown field (allowed: name, source_type, database_kind, settings, variables)"},
		{`{"version":1,"services":[{"name":"a"},{"name":"b","settings":[]}]}`, "services[1].settings: must be an object"},
		{`{"version":1,"services":[{"name":"a","variables":{"OK":"1","PORT":8080}}]}`, `services[0].variables.PORT: variable values must be strings or {"preserve":true}`},
		{`{"version":1,"services":[{"name":"a"},{"name":""}]}`, "services[1].name: is required"},
		{`{"version":1,"services":[{"name":"a"},{"name":"b"},{"name":"a"}]}`, `services[2].name: service "a" is already declared at services[0]`},
		{`{"version":1,"services":[{"name":5}]}`, "services[0].name: must be a string"},
		{`{"version":1,"partial":"bad name","services":[]}`, "partial: must be 1-64"},
		{`{"version":1,`, "invalid JSON"},
	} {
		_, err := DecodeJSON([]byte(tc.in))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", tc.in, err, tc.want)
		}
	}
	d, err := DecodeJSON([]byte(`{"version":1,"project_name":"acme","services":[{"name":"web","source_type":"image","settings":{"replicas":2},"variables":{"A":"x","B":{"preserve":true}}},{"name":"db","variables":null}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if d.ProjectName != "acme" || string(d.Services[0].Settings["replicas"]) != "2" || !(*d.Services[0].Variables)["B"].Preserve ||
		d.Services[1].Variables != nil || d.Services[1].Settings == nil {
		t.Fatalf("decoded: %+v", d)
	}
	// YAML goes through the same strict decoder.
	if _, err := Decode([]byte("version: 1\nservices:\n  - name: web\n    setings: {}\n"), "yaml"); err == nil || !strings.Contains(err.Error(), "services[0].setings") {
		t.Fatalf("yaml unknown field: %v", err)
	}
}
