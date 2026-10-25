package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"testing"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/store"
)

func TestNodesAPIOnlyLocalAndRejectRemote(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := &Server{Store: st, Deploy: &deploy.Manager{Store: st, Docker: testDocker(), Cfg: deploy.Config{DataDir: t.TempDir()}}, AdminPassword: "password123", SessionKey: make([]byte, 32), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// Handler 단위 테스트. 인증은 기존 API 테스트에서 별도로 검증한다.
	list := httptest.NewRecorder()
	srv.listNodes(list, httptest.NewRequest(http.MethodGet, "/api/nodes", nil))
	if list.Code != 200 {
		t.Fatalf("GET nodes: %d %s", list.Code, list.Body.String())
	}
	var nodes []store.Node
	if err := json.Unmarshal(list.Body.Bytes(), &nodes); err != nil || len(nodes) != 1 || nodes[0].ID != store.LocalNodeID {
		t.Fatalf("nodes: %+v %v", nodes, err)
	}
	patch := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPatch, "/api/nodes/node_local", bytes.NewBufferString(`{"name":"local-test","schedulable":false}`))
	r.SetPathValue("nid", store.LocalNodeID)
	srv.patchNode(patch, r)
	if patch.Code != 200 {
		t.Fatalf("PATCH node: %d %s", patch.Code, patch.Body.String())
	}
	loaded, err := st.LocalNode(context.Background())
	if err != nil || loaded.Schedulable || loaded.Name != "local-test" {
		t.Fatalf("persisted node: %+v %v", loaded, err)
	}
	// 노드 단위 metrics: 로컬 노드는 빈 점 목록, 잘못된 range는 400, 없는 노드는 404.
	for _, tc := range []struct {
		nid, q string
		code   int
	}{{store.LocalNodeID, "?range=1h", 200}, {store.LocalNodeID, "?range=2y", 400}, {"nod_missing", "", 404}} {
		req := httptest.NewRequest(http.MethodGet, "/api/nodes/"+tc.nid+"/metrics"+tc.q, nil)
		req.SetPathValue("nid", tc.nid)
		result := httptest.NewRecorder()
		srv.nodeMetrics(result, req)
		if result.Code != tc.code {
			t.Fatalf("node metrics %s%s: %d %s", tc.nid, tc.q, result.Code, result.Body.String())
		}
	}
	// 로컬 노드는 drain할 수 없다.
	dr := httptest.NewRequest(http.MethodPost, "/api/nodes/node_local/drain", nil)
	dr.SetPathValue("nid", store.LocalNodeID)
	drain := httptest.NewRecorder()
	srv.drainNode(drain, dr)
	if drain.Code != 400 {
		t.Fatalf("drain local: %d", drain.Code)
	}
}

func TestSecureTransportTrustsOnlyConfiguredProxies(t *testing.T) {
	s := &Server{}
	req := func(remote, proto string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/agent/enroll", nil)
		r.RemoteAddr = remote
		if proto != "" {
			r.Header.Set("X-Forwarded-Proto", proto)
		}
		return r
	}
	// 기본값: X-Forwarded-Proto를 믿지 않는다.
	if s.secureTransport(req("[::1]:1234", "https")) {
		t.Fatal("trusted X-Forwarded-Proto without KEEL_TRUSTED_PROXIES")
	}
	s.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("fd00::/8"), netip.MustParsePrefix("127.0.0.1/32")}
	if !s.secureTransport(req("[fd00::5]:1234", "https")) || !s.secureTransport(req("127.0.0.1:9", "https")) {
		t.Fatal("trusted proxy with https not accepted")
	}
	if s.secureTransport(req("[fd00::5]:1234", "http")) || s.secureTransport(req("[fd00::5]:1234", "")) {
		t.Fatal("trusted proxy without https accepted")
	}
	if s.secureTransport(req("[2001:db8::1]:1234", "https")) || s.secureTransport(req("10.0.0.1:1", "https")) {
		t.Fatal("untrusted client could spoof X-Forwarded-Proto")
	}
}
