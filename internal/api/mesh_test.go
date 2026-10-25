package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

func TestMeshAPI(t *testing.T) {
	f := newGHFixture(t)
	pv := map[string]string{"sid": f.web.ID, "eid": f.prod.ID}

	// private_ports: 검증, 저장, 바로 반영되므로 pending changes에 넣지 않는다.
	for _, bad := range []string{`{"private_ports":[0]}`, `{"private_ports":[70000]}`, `{"private_ports":[80,80]}`,
		`{"private_ports":[1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17]}`, `{"private_ports":"80"}`} {
		if code, _ := f.call(f.srv.patchInstance, "PATCH", "/x", bad, pv); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	code, out := f.call(f.srv.patchInstance, "PATCH", "/x", `{"private_ports":[8080,9090]}`, pv)
	if code != 200 {
		t.Fatalf("PATCH private_ports: %d %v", code, out)
	}
	if pp, _ := out["private_ports"].([]any); len(pp) != 2 || pp[0] != float64(8080) {
		t.Fatalf("private_ports: %v", out["private_ports"])
	}
	in, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	if pc := strings.Join(PendingChanges(in, &store.Deployment{ReqSpec: store.Spec{}, VarsRaw: in.Variables}), ","); strings.Contains(pc, "private_ports") {
		t.Errorf("private_ports listed as pending: %s", pc)
	}

	// 원격 노드(mesh 가능)에 db를 두고 web이 db를 참조한다. 로컬 노드는 사설 주소가 없어 mesh를 쓸 수 없다.
	f.st.CreateJoinToken(f.ctx, "h", "fra-1", time.Now().Add(time.Hour))
	n, err := f.st.ConsumeJoinToken(f.ctx, "h", "")
	if err != nil {
		t.Fatal(err)
	}
	f.st.RecordNodeCert(f.ctx, "abcd", n.ID, time.Now().Add(24*time.Hour))
	priv := "100.64.0.9"
	f.st.SetNodeInfo(f.ctx, n.ID, store.NodeAddress{Private: &priv}, store.NodeInfo{MeshPort: 7443})
	dbIn, _ := f.st.GetInstanceFor(f.ctx, f.db.ID, f.prod.ID)
	dbIn.NodeID = n.ID
	f.st.UpdateInstanceConfig(f.ctx, dbIn)
	in.Variables = map[string]string{"DATABASE_URL": "${{db.DATABASE_URL}}", "VIA_SHARED": "${{shared.DB}}", "PLAIN": "x"}
	f.st.UpdateInstanceConfig(f.ctx, in)
	f.st.SetSharedVars(f.ctx, f.prod.ID, map[string]string{"DB": "${{db.PGHOST}}"})

	r := httptest.NewRequest("GET", "/x", nil)
	r.SetPathValue("sid", f.web.ID)
	r.SetPathValue("eid", f.prod.ID)
	w := httptest.NewRecorder()
	f.srv.resolvedVars(w, r)
	if w.Code != 200 {
		t.Fatalf("resolved: %d %s", w.Code, w.Body.String())
	}
	var res map[string]map[string]any
	json.Unmarshal(w.Body.Bytes(), &res)
	for _, k := range []string{"DATABASE_URL", "VIA_SHARED"} {
		if res[k]["cross_node"] != true || res[k]["mesh"] != false {
			t.Errorf("%s: %v", k, res[k])
		}
	}
	if _, ok := res["PLAIN"]["cross_node"]; ok {
		t.Errorf("PLAIN: %v", res["PLAIN"])
	}

	// GET /api/nodes: 노드마다 mesh 상태. 로컬은 꺼져 있고 이유가 있다. 원격은 사설 주소와 mesh 포트가 있어 켜져 있다.
	lw := httptest.NewRecorder()
	f.srv.listNodes(lw, httptest.NewRequest(http.MethodGet, "/api/nodes", nil))
	var nodes []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Mesh struct {
			Enabled bool    `json:"enabled"`
			Listen  *string `json:"listen"`
			Peers   []any   `json:"peers"`
			Reason  string  `json:"reason"`
		} `json:"mesh"`
	}
	if err := json.Unmarshal(lw.Body.Bytes(), &nodes); err != nil || len(nodes) != 2 {
		t.Fatalf("nodes: %s %v", lw.Body.String(), err)
	}
	for _, nd := range nodes {
		switch nd.ID {
		case store.LocalNodeID:
			if nd.Mesh.Enabled || nd.Mesh.Reason == "" || nd.Mesh.Peers == nil || nd.Name == "" {
				t.Errorf("local mesh: %+v", nd)
			}
		case n.ID:
			if !nd.Mesh.Enabled || nd.Mesh.Listen == nil || *nd.Mesh.Listen != "100.64.0.9:7443" {
				t.Errorf("remote mesh: %+v", nd.Mesh)
			}
		}
	}
}
