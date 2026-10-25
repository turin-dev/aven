package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/catalog"
)

// 이름이 비밀 같지 않은 변수 값과 명령 안에 그대로 적힌 비밀도 내보내지 않는다.
func TestExportTemplateSecretLiterals(t *testing.T) {
	f, h := authServer(t)
	legacy := withBearer("legacy-token")
	web, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	cmd := "node server.js --stripe sk_live_51HxYzAbCdEfGhIj --port 3000"
	pre := "curl -H 'X-Hook: 9fK2mQx7Lp4Zr8Tn3Vb6Wc1Yd5' https://hooks.example.com"
	web.StartCommand, web.PreDeployCommand = &cmd, &pre
	web.Variables = map[string]string{
		"AUTH_HEADER": "Bearer ghp_aB3dE5gH7jK9mN1pQ3sT5vW7yZ",
		"CERT":        "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n-----END PRIVATE KEY-----",
		"GREETING":    "hello world",
	}
	f.st.UpdateInstanceConfig(f.ctx, web)
	leaks := []string{"sk_live_51HxYzAbCdEfGhIj", "9fK2mQx7Lp4Zr8Tn3Vb6Wc1Yd5", "ghp_aB3dE5gH7jK9mN1pQ3sT5vW7yZ", "MIIEvQIBADANBgkqhkiG9w0BAQEFAASC"}
	export := func(q string) (*catalog.Definition, []string) {
		w := req(h, "POST", "/api/projects/"+f.p.ID+"/environments/"+f.prod.ID+"/export-template"+q, "", legacy)
		if w.Code != 200 {
			t.Fatalf("export%s: %d %s", q, w.Code, w.Body)
		}
		for _, l := range leaks {
			if strings.Contains(w.Body.String(), l) {
				t.Errorf("export%s leaks %q", q, l)
			}
		}
		var out struct {
			Definition json.RawMessage `json:"definition"`
			Warnings   []string        `json:"warnings"`
		}
		json.Unmarshal(w.Body.Bytes(), &out)
		d, err := catalog.ParseDefinition(out.Definition)
		if err != nil {
			t.Fatalf("invalid definition: %v\n%s", err, out.Definition)
		}
		return d, out.Warnings
	}
	webSpec := func(d *catalog.Definition) catalog.ServiceSpec {
		for _, sv := range d.Services {
			if sv.Name == "web" {
				return sv
			}
		}
		t.Fatal("web not exported")
		return catalog.ServiceSpec{}
	}
	d, warns := export("")
	w := webSpec(d)
	if *w.Settings.StartCommand != "node server.js --stripe ${{ secret(32) }} --port 3000" ||
		*w.Settings.PreDeployCommand != "curl -H 'X-Hook: ${{ secret(32) }}' https://hooks.example.com" ||
		w.Variables["AUTH_HEADER"] != "Bearer ${{ secret(32) }}" || w.Variables["CERT"] != "${{ secret(32) }}" || w.Variables["GREETING"] != "hello world" {
		t.Errorf("generate: %+v %v", w.Settings, w.Variables)
	}
	joined := strings.Join(warns, "\n")
	for _, s := range []string{"시작 명령", "배포 전 명령", "변수 AUTH_HEADER", "변수 CERT", "sk_", "entropy", "-----BEGIN"} {
		if !strings.Contains(joined, s) {
			t.Errorf("warning for %q missing: %s", s, joined)
		}
	}

	d, _ = export("?secrets=inputs")
	w = webSpec(d)
	if !strings.HasPrefix(w.Variables["AUTH_HEADER"], "Bearer ${{ inputs.WEB_AUTH_HEADER") || !strings.Contains(*w.Settings.StartCommand, "${{ inputs.WEB_START_COMMAND") {
		t.Errorf("inputs: %+v %v", *w.Settings.StartCommand, w.Variables)
	}
	n := 0
	for _, in := range d.Inputs {
		if strings.HasPrefix(in.Key, "WEB_") && in.Required && in.Secret && in.Default == "" {
			n++
		}
	}
	if n < 4 {
		t.Errorf("secret inputs: %+v", d.Inputs)
	}
}
