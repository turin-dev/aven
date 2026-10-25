package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/store"
)

func TestParseSharedVarsBody(t *testing.T) {
	for _, tc := range []struct {
		body   string
		values map[string]string
		sealed []string
		err    bool
	}{
		{`{"A":"1","values":"x"}`, map[string]string{"A": "1", "values": "x"}, nil, false},
		{`{"values":{"A":"1"},"sealed":["A"]}`, map[string]string{"A": "1"}, []string{"A"}, false},
		{`{"values":null}`, map[string]string{}, nil, false},
		{`{}`, map[string]string{}, nil, false},
		{`{"values":{"A":"1"},"extra":1}`, nil, nil, true},
		{`{"A":1}`, nil, nil, true},
		{`[]`, nil, nil, true},
	} {
		b, err := parseSharedVarsBody([]byte(tc.body))
		if tc.err {
			if err == nil {
				t.Errorf("%s: want error", tc.body)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(b.Values, tc.values) || !reflect.DeepEqual(b.Sealed, tc.sealed) {
			t.Errorf("%s: %+v %v", tc.body, b, err)
		}
	}
}

func TestRenderRawVars(t *testing.T) {
	m := map[string]string{"A": "x y", "S": "secret", "B": "${{ db.URL }}"}
	got := RenderRawVars("env", m, map[string]bool{"S": true})
	if strings.Contains(got, "secret") || !strings.Contains(got, "# S=<sealed>\n") || !strings.Contains(got, "A='x y'\n") {
		t.Errorf("env:\n%s", got)
	}
	back, err := ParseRawVars("env", got)
	if err != nil || len(back) != 2 || back["A"] != "x y" || back["B"] != "${{ db.URL }}" {
		t.Errorf("round trip: %v %v", back, err)
	}
	js := RenderRawVars("json", m, map[string]bool{"S": true})
	back, err = ParseRawVars("json", js)
	if err != nil || len(back) != 2 || strings.Contains(js, "secret") {
		t.Errorf("json: %s %v", js, err)
	}
	if _, err := ParseRawVars("json", `{"A":1}`); err == nil {
		t.Error("json numbers accepted")
	}
}

// TestSealedVariablesNeverLeak는 봉인한 값이 변수를 돌려주는 모든 API에서 가려지는지, 배포 해석에는 그대로 쓰이는지 본다.
func TestSealedVariablesNeverLeak(t *testing.T) {
	f, h := authServer(t)
	ctx := f.ctx
	const secret, sharedSecret, newSecret = "hunter2-sealed-xyz", "shared-sealed-abc", "rotated-sealed-789"
	web, _ := f.st.GetInstanceFor(ctx, f.web.ID, f.prod.ID)
	web.Variables = map[string]string{"SECRET": secret, "PLAIN": "visible-1", "SELFREF": "x-${{ SECRET }}"}
	if err := f.st.UpdateInstanceConfig(ctx, web); err != nil {
		t.Fatal(err)
	}
	oth, _ := f.st.GetInstanceFor(ctx, f.other.ID, f.prod.ID)
	oth.Variables = map[string]string{"REF": "pre-${{ web.SECRET }}", "SH": "${{ shared.TOKEN }}", "OK": "${{ web.PLAIN }}"}
	if err := f.st.UpdateInstanceConfig(ctx, oth); err != nil {
		t.Fatal(err)
	}
	viewer := testUser(t, f.st, "v@example.com", store.RoleViewer)
	member := testUser(t, f.st, "m@example.com", store.RoleMember)
	f.st.SetMember(ctx, f.p.ID, viewer.ID, store.RoleViewer)
	f.st.SetMember(ctx, f.p.ID, member.ID, store.RoleMember)
	vc, mc := withCookie(testSessionCookie(t, f.srv, viewer)), withCookie(testSessionCookie(t, f.srv, member))
	owner := withBearer("legacy-token")
	inst := "/api/services/" + f.web.ID + "/instances/" + f.prod.ID
	othInst := "/api/services/" + f.other.ID + "/instances/" + f.prod.ID
	envPath := "/api/environments/" + f.prod.ID

	mustCode := func(want int, method, path, body string, auth func(*http.Request)) string {
		t.Helper()
		w := req(h, method, path, body, auth)
		if w.Code != want {
			t.Fatalf("%s %s: %d (want %d) %s", method, path, w.Code, want, w.Body)
		}
		return w.Body.String()
	}

	// 봉인 요청: 없는 키는 거부, viewer는 거부
	mustCode(400, "PATCH", inst, `{"sealed_variables":["NOPE"]}`, mc)
	mustCode(400, "PATCH", inst, `{"sealed_variables":"SECRET"}`, mc)
	mustCode(403, "PATCH", inst, `{"sealed_variables":["SECRET"]}`, vc)
	body := mustCode(200, "PATCH", inst, `{"sealed_variables":["SECRET"]}`, mc)
	var view struct {
		Variables       map[string]string `json:"variables"`
		SealedVariables []string          `json:"sealed_variables"`
	}
	json.Unmarshal([]byte(body), &view)
	if view.Variables["SECRET"] != SealedValue || !reflect.DeepEqual(view.SealedVariables, []string{"SECRET"}) || view.Variables["PLAIN"] != "visible-1" {
		t.Fatalf("seal response: %s", body)
	}
	// 공유 변수: 새 형식으로 봉인, 예전 형식(문자열 맵)은 그대로 동작하고 SealedValue는 원래 값을 유지한다
	mustCode(400, "PUT", envPath+"/shared-variables", `{"values":{"A":"1"},"sealed":["B"]}`, mc)
	mustCode(204, "PUT", envPath+"/shared-variables", `{"values":{"TOKEN":"`+sharedSecret+`","REGION":"eu"},"sealed":["TOKEN"]}`, mc)
	mustCode(204, "PUT", envPath+"/shared-variables", `{"TOKEN":"<sealed>","REGION":"us"}`, mc)
	if sh, _ := f.st.GetSharedVars(ctx, f.prod.ID); sh["TOKEN"] != sharedSecret || sh["REGION"] != "us" {
		t.Fatalf("shared after legacy put: %v", sh)
	}
	// 복제 환경에는 봉인 변수가 따라가지 않는다
	body = mustCode(201, "POST", "/api/projects/"+f.p.ID+"/environments", `{"name":"staging","copy_from":"`+f.prod.ID+`"}`, owner)
	var stg struct{ ID string }
	json.Unmarshal([]byte(body), &stg)
	stgWeb, _ := f.st.GetInstanceFor(ctx, f.web.ID, stg.ID)
	if _, ok := stgWeb.Variables["SECRET"]; ok || stgWeb.Variables["PLAIN"] != "visible-1" {
		t.Errorf("duplicated env vars: %v", stgWeb.Variables)
	}
	if sh, _ := f.st.GetSharedVars(ctx, stg.ID); sh["TOKEN"] != "" || sh["REGION"] != "us" {
		t.Errorf("duplicated shared vars: %v", sh)
	}

	// 배포 때는 봉인 값이 그대로 해석된다
	res, err := f.mgr.Resolve(ctx, oth, nil)
	if err != nil || res.Vars["REF"] != "pre-"+secret || res.Vars["SH"] != sharedSecret {
		t.Fatalf("deploy-time resolve: %v %v", res, err)
	}

	leaks := []string{secret, sharedSecret, "pre-hunter2"}
	gets := []string{
		inst, othInst, inst + "/variables/resolved", othInst + "/variables/resolved",
		envPath + "/shared-variables", envPath + "/shared-variables?detail=1", envPath + "/canvas",
		inst + "/variables/raw?format=env", inst + "/variables/raw?format=json", othInst + "/variables/raw",
		"/api/environments/" + stg.ID + "/sync-preview?from=" + f.prod.ID,
		"/api/environments/" + f.prod.ID + "/sync-preview?from=" + stg.ID,
	}
	posts := []string{
		"/api/projects/" + f.p.ID + "/environments/" + f.prod.ID + "/export-template?secrets=generate",
		"/api/projects/" + f.p.ID + "/environments/" + f.prod.ID + "/export-template?secrets=inputs",
	}
	for _, who := range []struct {
		name string
		auth func(*http.Request)
	}{{"owner", owner}, {"member", mc}, {"viewer", vc}} {
		check := func(method, path string) {
			w := req(h, method, path, "", who.auth)
			if w.Code != 200 {
				if who.name == "viewer" && w.Code == 403 {
					return
				}
				t.Errorf("%s %s %s: %d %s", who.name, method, path, w.Code, w.Body)
				return
			}
			b := w.Body.String()
			for _, l := range leaks {
				if strings.Contains(b, l) {
					t.Errorf("%s %s %s leaked %q: %s", who.name, method, path, l, b)
				}
			}
		}
		for _, p := range gets {
			check("GET", p)
		}
		for _, p := range posts {
			check("POST", p)
		}
	}

	// 해석된 변수: 자기 봉인 값, 그것을 참조한 값, 다른 서비스의 참조, 공유 변수 참조 모두 sealed
	type rv struct {
		Value  string `json:"value"`
		Sealed bool   `json:"sealed"`
	}
	var rw, ro map[string]rv
	json.Unmarshal([]byte(mustCode(200, "GET", inst+"/variables/resolved", "", mc)), &rw)
	json.Unmarshal([]byte(mustCode(200, "GET", othInst+"/variables/resolved", "", mc)), &ro)
	if !rw["SECRET"].Sealed || rw["SECRET"].Value != SealedValue || !rw["SELFREF"].Sealed || rw["PLAIN"].Sealed || rw["PLAIN"].Value != "visible-1" {
		t.Errorf("web resolved: %+v", rw)
	}
	if !ro["REF"].Sealed || !ro["SH"].Sealed || ro["OK"].Sealed || ro["OK"].Value != "visible-1" {
		t.Errorf("other resolved: %+v", ro)
	}
	var sd sharedVarsView
	json.Unmarshal([]byte(mustCode(200, "GET", envPath+"/shared-variables?detail=1", "", mc)), &sd)
	if !reflect.DeepEqual(sd.Sealed, []string{"TOKEN"}) || sd.Values["TOKEN"] != SealedValue || sd.Values["REGION"] != "us" {
		t.Errorf("shared detail: %+v", sd)
	}
	var raw rawVars
	json.Unmarshal([]byte(mustCode(200, "GET", inst+"/variables/raw?format=env", "", mc)), &raw)
	if !strings.Contains(raw.Content, "# SECRET=<sealed>") || !strings.Contains(raw.Content, "PLAIN=visible-1") || !reflect.DeepEqual(raw.SealedVariables, []string{"SECRET"}) {
		t.Errorf("raw: %+v", raw)
	}
	mustCode(400, "GET", inst+"/variables/raw?format=yaml", "", mc)
	// 동기화 미리보기는 봉인 변수를 이름만 알린다
	var plan syncPlan
	json.Unmarshal([]byte(mustCode(200, "GET", "/api/environments/"+stg.ID+"/sync-preview?from="+f.prod.ID, "", mc)), &plan)
	if !reflect.DeepEqual(plan.SealedSharedVariables, []string{"TOKEN"}) {
		t.Errorf("sync plan shared sealed: %+v", plan.SealedSharedVariables)
	}
	for _, ss := range plan.Services {
		for _, c := range ss.Variables {
			if c.Key == "SECRET" {
				t.Errorf("sealed variable in sync changes: %+v", c)
			}
		}
	}

	// raw 편집기: viewer는 못 쓰고, 봉인 변수는 편집기에 없어도 유지된다
	mustCode(403, "GET", inst+"/variables/raw", "", vc)
	mustCode(403, "PUT", inst+"/variables/raw", `{"format":"env","content":"A=1"}`, vc)
	if b := mustCode(400, "PUT", inst+"/variables/raw", `{"format":"env","content":"A=1\nBROKEN\n"}`, mc); !strings.Contains(b, "line 2") {
		t.Errorf("parse error: %s", b)
	}
	mustCode(200, "PUT", inst+"/variables/raw", `{"format":"env","content":"# SECRET=<sealed>\nexport PLAIN=changed\nNEW=\"a b\\nc\"\n"}`, mc)
	cur, _ := f.st.GetInstance(ctx, web.ID)
	if !reflect.DeepEqual(cur.Variables, map[string]string{"SECRET": secret, "PLAIN": "changed", "NEW": "a b\nc"}) {
		t.Errorf("after raw put: %v", cur.Variables)
	}
	// 봉인 변수 값을 바꾸면(값 교체) 새 값도 봉인된다
	mustCode(200, "PUT", inst+"/variables/raw", `{"format":"json","content":"{\"SECRET\":\"`+newSecret+`\",\"PLAIN\":\"changed\"}"}`, mc)
	b := mustCode(200, "GET", inst, "", mc)
	if strings.Contains(b, newSecret) || !strings.Contains(b, `"sealed_variables":["SECRET"]`) {
		t.Errorf("rotated sealed value leaked or unsealed: %s", b)
	}
	// 가린 값을 그대로 보내면(variables 전체 교체) 원래 값이 유지된다
	mustCode(200, "PATCH", inst, `{"variables":{"SECRET":"<sealed>","PLAIN":"p2"}}`, mc)
	if cur, _ := f.st.GetInstance(ctx, web.ID); cur.Variables["SECRET"] != newSecret || cur.Variables["PLAIN"] != "p2" {
		t.Errorf("placeholder round trip: %v", cur.Variables)
	}
	// 예전 봉인 값이 롤백 등으로 돌아와도 봉인 상태다
	cur, _ = f.st.GetInstance(ctx, web.ID)
	cur.Variables["SECRET"] = secret
	f.st.UpdateInstanceConfig(ctx, cur)
	if b := mustCode(200, "GET", inst, "", mc); strings.Contains(b, secret) {
		t.Errorf("restored sealed value leaked: %s", b)
	}
	// 봉인은 풀 수 없다: 지우고 다른 값으로 다시 만들면 보통 변수다
	mustCode(200, "PATCH", inst, `{"variables_patch":{"SECRET":null}}`, mc)
	b = mustCode(200, "PATCH", inst, `{"variables_patch":{"SECRET":"plain-again"}}`, mc)
	if !strings.Contains(b, `"SECRET":"plain-again"`) || !strings.Contains(b, `"sealed_variables":[]`) {
		t.Errorf("recreated variable: %s", b)
	}
}

// TestSealedBucketCredentialsHidden는 bucket 정보·목록이 봉인된 자격 증명을 누구에게도 돌려주지 않는지 본다.
func TestSealedBucketCredentialsHidden(t *testing.T) {
	bf := newBucketFixture(t)
	keys := make([]string, 0, len(bf.in.Variables))
	for k := range bf.in.Variables {
		keys = append(keys, k)
	}
	if err := bf.st.SealVariables(bf.ctx, bf.in.ID, bf.in.Variables, keys); err != nil {
		t.Fatal(err)
	}
	secret := bf.dep.Env["SECRET_ACCESS_KEY"]
	if secret == "" {
		t.Fatal("fixture has no SECRET_ACCESS_KEY")
	}
	for _, lv := range []int{lvMember, lvViewer} {
		w := bf.do(bf.srv.bucketInfo, "GET", "/x", "", "", bf.pv(""), lv)
		if w.Code != 200 || strings.Contains(w.Body.String(), secret) {
			t.Errorf("bucket info (level %d): %d %s", lv, w.Code, w.Body)
		}
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		if lv == lvMember && out["secret_access_key"] != SealedValue {
			t.Errorf("member bucket info: %v", out)
		}
	}
	if w := bf.do(bf.srv.bucketObjects, "GET", "/x", "", "", bf.pv(""), lvMember); w.Code != 200 || strings.Contains(w.Body.String(), secret) {
		t.Errorf("bucket objects: %d %s", w.Code, w.Body)
	}
}
