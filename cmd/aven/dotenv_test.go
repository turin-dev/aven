package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseVarsArgsSealEnvImport(t *testing.T) {
	o, err := parseVarsArgs([]string{"--seal", "API_KEY", "--seal=DB_PASS", "--env", "--import", ".env", "--import=prod.env"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(o.seal, []string{"API_KEY", "DB_PASS"}) || !o.dotenv || !reflect.DeepEqual(o.imports, []string{".env", "prod.env"}) {
		t.Errorf("parsed: %+v", o)
	}
	for _, bad := range [][]string{{"--seal"}, {"--import"}} {
		if _, err := parseVarsArgs(bad); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

func TestReadDotenvFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	content := "# SECRET=<sealed>\nexport A=1\nB=\"x\\ny\"\nC='lit $x'\n"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := readDotenvFile(p)
	if err != nil || !reflect.DeepEqual(m, map[string]string{"A": "1", "B": "x\ny", "C": "lit $x"}) {
		t.Errorf("read: %v %v", m, err)
	}
	os.WriteFile(p, []byte("A=1\nBROKEN\n"), 0o600)
	if _, err := readDotenvFile(p); err == nil {
		t.Error("broken file accepted")
	}
	if _, err := readDotenvFile(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("missing file accepted")
	}
}

func TestVisibleResolvedDropsSealed(t *testing.T) {
	out, sealed := visibleResolved(map[string]resolvedValue{"A": {Value: "1"}, "S": {Value: "<sealed>", Sealed: true}})
	if !reflect.DeepEqual(out, map[string]string{"A": "1"}) || !reflect.DeepEqual(sealed, []string{"S"}) {
		t.Errorf("visible: %v %v", out, sealed)
	}
}
