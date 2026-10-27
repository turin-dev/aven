package api

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/store"
)

func TestPrivateDNSNameAPI(t *testing.T) {
	f := newGHFixture(t)
	pv := map[string]string{"sid": f.web.ID, "eid": f.prod.ID}
	avail := func(name string) (bool, string) {
		t.Helper()
		code, out := f.call(f.srv.privateDNSAvailable, "GET", "/x?name="+name, "", pv)
		if code != 200 {
			t.Fatalf("available %q: %d %v", name, code, out)
		}
		reason, _ := out["reason"].(string)
		return out["available"] == true, reason
	}

	// 사용 가능 확인: 자기 이름·빈 값은 언제나 된다. 다른 서비스 이름, 예약 이름, 형식이 틀린 이름은 안 된다.
	for _, name := range []string{"", "web", "frontend", "WEB-2"} {
		if ok, reason := avail(name); !ok {
			t.Errorf("%q: unavailable (%s)", name, reason)
		}
	}
	for name, want := range map[string]string{
		"db": "서비스", "other": "서비스", "shared": "예약", "localhost": "예약", "keel-proxy": "aven", "sbx-abc": "sbx-",
		"a_b": "소문자", "-x": "소문자", strings.Repeat("a", 41): "40자",
	} {
		ok, reason := avail(name)
		if ok || !strings.Contains(reason, want) {
			t.Errorf("%q: available=%v reason=%q (want %q)", name, ok, reason, want)
		}
	}

	// PATCH: 저장하고, 다음 배포 때 반영되는 대기 변경이다.
	if code, out := f.call(f.srv.patchInstance, "PATCH", "/x", `{"private_dns_name":"db"}`, pv); code != 400 || !strings.Contains(out["error"].(string), "private_dns_name") {
		t.Fatalf("collision with service name: %d %v", code, out)
	}
	if code, _ := f.call(f.srv.patchInstance, "PATCH", "/x", `{"private_dns_name":"bad_name"}`, pv); code != 400 {
		t.Fatalf("bad name: %d", code)
	}
	code, out := f.call(f.srv.patchInstance, "PATCH", "/x", `{"private_dns_name":" Frontend "}`, pv)
	if code != 200 || out["private_dns_name"] != "frontend" {
		t.Fatalf("PATCH: %d %v", code, out)
	}
	in, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	if pc := PendingChanges(in, &store.Deployment{ReqSpec: store.Spec{}, VarsRaw: in.Variables}); !slices.Contains(pc, "private_dns_name") {
		t.Errorf("private_dns_name not pending: %v", pc)
	}
	name, fqdn, aliases := privateDNS(in, f.web, f.prod)
	if name != "frontend" || fqdn != "frontend."+f.prod.Name+".keel.internal" ||
		!slices.Equal(aliases, []string{"web", "web." + f.prod.Name + ".keel.internal", "web." + f.prod.Name + ".aven.internal", "frontend", "frontend." + f.prod.Name + ".keel.internal", "frontend." + f.prod.Name + ".aven.internal"}) {
		t.Fatalf("privateDNS: %s %s %v", name, fqdn, aliases)
	}

	// 다른 서비스는 web이 쓰는 사설 이름을 쓸 수 없다(설정 값과 live 배포 값 모두).
	dbPV := map[string]string{"sid": f.db.ID, "eid": f.prod.ID}
	if code, out := f.call(f.srv.privateDNSAvailable, "GET", "/x?name=frontend", "", dbPV); code != 200 || out["available"] != false ||
		!strings.Contains(out["reason"].(string), "web") {
		t.Fatalf("taken by web: %d %v", code, out)
	}
	if code, _ := f.call(f.srv.patchInstance, "PATCH", "/x", `{"private_dns_name":"frontend"}`, dbPV); code != 400 {
		t.Fatalf("db took web's name: %d", code)
	}
	d := &store.Deployment{ServiceID: f.web.ID, EnvironmentID: f.prod.ID, InstanceID: in.ID, Trigger: "manual", Status: store.DepLive,
		Env: map[string]string{}, Spec: store.Spec{PrivateDNSName: "legacy"}}
	if err := f.st.CreateDeployment(f.ctx, d); err != nil {
		t.Fatal(err)
	}
	f.st.SetInstanceStatus(f.ctx, in.ID, store.InstOnline, &d.ID)
	if code, out := f.call(f.srv.privateDNSAvailable, "GET", "/x?name=legacy", "", dbPV); code != 200 || out["available"] != false {
		t.Fatalf("live name of web: %d %v", code, out)
	}

	// 서비스 이름과 같은 값은 기본값(빈 값)으로 저장한다.
	if code, out := f.call(f.srv.patchInstance, "PATCH", "/x", `{"private_dns_name":"web"}`, pv); code != 200 {
		t.Fatalf("reset: %d %v", code, out)
	} else if _, ok := out["private_dns_name"]; ok {
		t.Fatalf("reset kept value: %v", out["private_dns_name"])
	}

	// IaC도 같은 검사를 한다.
	dbIn, _ := f.st.GetInstanceFor(f.ctx, f.db.ID, f.prod.ID)
	if _, _, err := f.srv.iacSettings(f.ctx, f.db, dbIn, map[string]json.RawMessage{"private_dns_name": json.RawMessage(`"web"`)}); err == nil {
		t.Fatal("IaC accepted another service's name")
	}
	next, _, err := f.srv.iacSettings(f.ctx, f.db, dbIn, map[string]json.RawMessage{"private_dns_name": json.RawMessage(`"pg"`)})
	if err != nil || next.PrivateDNSName != "pg" {
		t.Fatalf("IaC: %v %q", err, next.PrivateDNSName)
	}
}
