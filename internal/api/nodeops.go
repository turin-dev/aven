package api

import (
	"net/http"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/metrics"
	"github.com/turin-dev/aven/internal/store"
)

// nodeStatus는 API에 보여줄 노드 상태다: 연결돼 있으면 online(drain 중이면 draining), 아니면 offline.
func (s *Server) nodeStatus(n *store.Node) string {
	if s.Agents == nil || !s.Agents.Online(n.ID) {
		return "offline"
	}
	if n.Draining {
		return "draining"
	}
	return "online"
}

// nodeMetrics: GET /api/nodes/{nid}/metrics?range=1h|6h|24h|7d → {points:[{t,cpu_percent,memory_bytes,disk_free_bytes}], step_s}
// 로컬 노드는 서버가, 원격 노드는 agent가 10초마다 보고한 값을 인스턴스 metrics와 같은 보관 정책으로 저장한다.
func (s *Server) nodeMetrics(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("nid")
	if _, err := s.Store.GetNode(r.Context(), id); err != nil {
		s.storeErr(w, err)
		return
	}
	rng := r.URL.Query().Get("range")
	if _, _, ok := metrics.RangeStep(rng); !ok {
		writeErr(w, 400, "range must be 1h, 6h, 24h or 7d")
		return
	}
	pts, step, err := metrics.QueryNode(r.Context(), s.Store, id, rng)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"points": pts, "step_s": step})
}

// drainNode: POST /api/nodes/{nid}/drain {target_node_id?} → 202 {node, moved, skipped}
func (s *Server) drainNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("nid")
	if id == store.LocalNodeID {
		writeErr(w, 400, "local node cannot be drained")
		return
	}
	if _, err := s.Store.GetNode(r.Context(), id); err != nil {
		s.storeErr(w, err)
		return
	}
	var body struct {
		TargetNodeID string `json:"target_node_id"`
	}
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	moved, skipped, err := s.Deploy.DrainNode(r.Context(), id, body.TargetNodeID)
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	n, err := s.Store.GetNode(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	n.Status = s.nodeStatus(n)
	if moved == nil {
		moved = []deploy.DrainMove{}
	}
	if skipped == nil {
		skipped = []deploy.DrainSkip{}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"node": n, "moved": moved, "skipped": skipped})
}
