package catalog

import (
	"errors"
	"strings"
	"testing"
)

const haDef = `{
  "version": 1, "key": "pg-ha-test", "name": "PG HA",
  "services": [
    {"name": "pg", "source": {"database": "postgres"}, "clusterRole": "root", "revertsToTemplateCode": "postgres",
     "haConversionConfig": {"replica": {"label": "Replicas", "nodeLabel": "Postgres", "options": [1, 2], "defaultValue": 2},
       "supportedImageMajorVersions": [16, 17], "supportedImages": ["postgres"]},
     "clusterWiring": {"roleVariable": "ROLE", "primaryHostVariable": "PRIMARY", "dataNodesVariable": "NODES", "dataNodeFormat": "{host}:5432"}},
    {"name": "pg-replica", "source": {"database": "postgres"}, "clusterRole": "replica",
     "variables": {"POSTGRES_PASSWORD": "${{pg.POSTGRES_PASSWORD}}"}},
    {"name": "pg-proxy", "source": {"image": "haproxy:3.0-alpine"}, "clusterRole": "edge"}
  ]
}`

func TestHACompanionDefinition(t *testing.T) {
	d, err := ParseDefinition([]byte(haDef))
	if err != nil {
		t.Fatal(err)
	}
	if !d.IsHACompanion() || d.HACompanionRoot().Name != "pg" || d.HAPrototype(RoleEdge).Name != "pg-proxy" || d.HAPrototype(RoleInternal) != nil {
		t.Errorf("accessors: %+v", d)
	}
	sel := d.HACompanionRoot().HAConversionConfig.Replica
	if sel.Default() != 2 || !sel.Allows(1) || sel.Allows(3) {
		t.Errorf("selector %+v", sel)
	}
	if (&HASelector{Options: []int{3, 5}}).Default() != 3 {
		t.Error("default falls back to the first option")
	}
	std := Templates["postgres"].Definition
	if std.IsHACompanion() || std.HATemplateCode() != "postgres-ha" {
		t.Error("standalone postgres template")
	}
}

func TestHACompanionValidation(t *testing.T) {
	cases := map[string][2]string{
		"two roots":             {`"clusterRole": "replica",`, `"clusterRole": "root",`},
		"unknown role":          {`"clusterRole": "edge"}`, `"clusterRole": "proxy"}`},
		"missing role":          {`, "clusterRole": "edge"}`, `}`},
		"bad default":           {`"defaultValue": 2`, `"defaultValue": 3`},
		"bad option":            {`"options": [1, 2]`, `"options": [0, 2]`},
		"no versions":           {`"supportedImageMajorVersions": [16, 17]`, `"supportedImageMajorVersions": []`},
		"tagged supportedImage": {`"supportedImages": ["postgres"]`, `"supportedImages": ["postgres:17"]`},
		"bad wiring variable":   {`"roleVariable": "ROLE"`, `"roleVariable": "role var"`},
		"reference in format":   {`"dataNodeFormat": "{host}:5432"`, `"dataNodeFormat": "${{pg.X}}"`},
		"replica other engine":  {`{"name": "pg-replica", "source": {"database": "postgres"}`, `{"name": "pg-replica", "source": {"database": "mysql"}`},
		"no revert target":      {`"revertsToTemplateCode": "postgres",`, ``},
		"config on replica":     {`"clusterRole": "replica",`, `"clusterRole": "replica", "haConversionConfig": {},`},
	}
	for name, c := range cases {
		bad := strings.Replace(haDef, c[0], c[1], 1)
		if bad == haDef {
			t.Fatalf("%s: replacement did not apply", name)
		}
		_, err := ParseDefinition([]byte(bad))
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: want validation error, got %v", name, err)
		}
	}
	// haTemplateCode는 단독 템플릿 키여야 한다
	if _, err := ParseDefinition([]byte(`{"version":1,"services":[{"name":"db","source":{"database":"redis"},"haTemplateCode":"Not A Key"}]}`)); err == nil {
		t.Error("bad haTemplateCode accepted")
	}
}
