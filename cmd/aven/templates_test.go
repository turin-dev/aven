package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

func TestTemplateDeployFlags(t *testing.T) {
	fl := flag.NewFlagSet("t", flag.ContinueOnError)
	in := kvList{}
	fl.Var(in, "input", "")
	tf := addTargetFlags(fl)
	key := splitKey(fl, []string{"umami", "--input", "A=1", "--input=B=x=y", "-e", "staging"})
	if key != "umami" || in["A"] != "1" || in["B"] != "x=y" || *tf.env != "staging" {
		t.Errorf("key=%q inputs=%v env=%q", key, in, *tf.env)
	}
	if err := (kvList{}).Set("novalue"); err == nil {
		t.Error("KEY without = accepted")
	}
	fl2 := flag.NewFlagSet("t", flag.ContinueOnError)
	in2 := kvList{}
	fl2.Var(in2, "input", "")
	if k := splitKey(fl2, []string{"--input", "A=1", "ghost"}); k != "ghost" || in2["A"] != "1" {
		t.Errorf("flags first: %q %v", k, in2)
	}
}

func TestPublishBody(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "def.json")
	os.WriteFile(raw, []byte(`{"version":1,"services":[]}`), 0o600)
	b, err := publishBody(raw, "workspace", "")
	if err != nil || b["visibility"] != "workspace" || string(b["definition"].(json.RawMessage)) != `{"version":1,"services":[]}` {
		t.Errorf("raw definition: %v %v", b, err)
	}
	wrapped := filepath.Join(dir, "wrapped.json")
	os.WriteFile(wrapped, []byte(`{"definition":{"version":1},"warnings":["x"],"name":"N"}`), 0o600)
	b, err = publishBody(wrapped, "", "k")
	if err != nil || b["key"] != "k" || string(b["definition"].(json.RawMessage)) != `{"version":1}` || b["visibility"] != nil {
		t.Errorf("wrapped: %v %v", b, err)
	}
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`nope`), 0o600)
	if _, err := publishBody(bad, "", ""); err == nil {
		t.Error("invalid json accepted")
	}
}
