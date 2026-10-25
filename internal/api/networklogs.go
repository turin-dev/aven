package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/store"
)

// 서비스 하나(.../instances/{eid}/dns-logs)나 환경 전체(/environments/{eid}/dns-logs?services=a,b)를 SSE로 흘려보낸다.

func (s *Server) serviceDNSLogs(w http.ResponseWriter, r *http.Request) {
	s.serviceNetLogs(w, r, "dns")
}
func (s *Server) serviceNetworkLogs(w http.ResponseWriter, r *http.Request) {
	s.serviceNetLogs(w, r, "network")
}
func (s *Server) envDNSLogs(w http.ResponseWriter, r *http.Request) { s.envNetLogs(w, r, "dns") }
func (s *Server) envNetworkLogs(w http.ResponseWriter, r *http.Request) {
	s.envNetLogs(w, r, "network")
}

func (s *Server) serviceNetLogs(w http.ResponseWriter, r *http.Request, kind string) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	s.streamNetworkLogs(w, r, c.env.ID, []string{c.svc.ID}, kind)
}

func (s *Server) envNetLogs(w http.ResponseWriter, r *http.Request, kind string) {
	env, _, ok := s.envAndProject(w, r)
	if !ok {
		return
	}
	var ids []string
	for _, id := range strings.Split(r.URL.Query().Get("services"), ",") {
		if id = strings.TrimSpace(id); id != "" && len(ids) < 100 {
			ids = append(ids, id)
		}
	}
	s.streamNetworkLogs(w, r, env.ID, ids, kind)
}

// parseLogTime은 since/until 값을 읽는다: 상대 시간(30s, 5m, 2h, 1d, 1w) 또는 RFC3339.
func parseLogTime(v string, now time.Time) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t, nil
	}
	if len(v) >= 2 {
		n, err := strconv.Atoi(v[:len(v)-1])
		unit := map[byte]time.Duration{'s': time.Second, 'm': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour, 'w': 7 * 24 * time.Hour}[v[len(v)-1]]
		if err == nil && n >= 0 && unit > 0 {
			return now.Add(-time.Duration(n) * unit), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time %q (use 30m, 2h, 1d or RFC3339)", v)
}

// streamNetworkLogs는 보관된 기록(tail, 기본 200, 최대 5000)을 시간 순서로 보낸 뒤, follow=0이 아니고 until이 없으면
// 새 기록을 이어서 보낸다. 이벤트: `capture`(노드 수집 상태, 처음 한 번), `log`(기록), `done`(follow하지 않을 때 끝).
func (s *Server) streamNetworkLogs(w http.ResponseWriter, r *http.Request, envID string, services []string, kind string) {
	qv := r.URL.Query()
	now := time.Now()
	since, err := parseLogTime(qv.Get("since"), now)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	until, err := parseLogTime(qv.Get("until"), now)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	tail, err := strconv.Atoi(qv.Get("tail"))
	if err != nil || tail <= 0 {
		tail = 200
	}
	follow := qv.Get("follow") != "0" && until.IsZero()
	viewer := ""
	if len(services) == 1 {
		viewer = services[0]
	}
	filter := deploy.ParseNetworkLogFilter(qv.Get("q"))
	q := store.NetworkLogQuery{EnvironmentID: envID, Kind: kind, Services: services, Since: since, Until: until, Limit: min(tail, 5000)}
	if !filter.Empty() {
		// 필터는 서버에서 적용하므로 맞는 기록이 tail개 모이도록 더 많이 읽는다.
		q.Limit = 5000
	}
	past, ch, cancel, err := s.Deploy.NetworkLogs.Subscribe(r.Context(), q, follow)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	defer cancel()
	e, ok := newSSE(w)
	if !ok {
		return
	}
	e.sendJSON("capture", captureSummary(s.Deploy.NetworkLogs))
	var matched []map[string]any
	sent := make(map[string]bool, len(past))
	for _, l := range past {
		sent[l.ID] = true
		if v := deploy.NetworkLogView(l, viewer); filter.Match(v) {
			matched = append(matched, v)
		}
	}
	if len(matched) > tail {
		matched = matched[len(matched)-tail:]
	}
	for _, v := range matched {
		e.sendJSON("log", v)
	}
	if !follow || ch == nil {
		e.sendJSON("done", map[string]any{"count": len(matched)})
		e.f.Flush()
		return
	}
	e.f.Flush()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			fmt.Fprint(w, ": ping\n\n")
			e.f.Flush()
		case l, ok := <-ch:
			if !ok {
				return // 너무 느린 구독자: 다시 연결하면 replay한다
			}
			if sent[l.ID] {
				continue
			}
			if v := deploy.NetworkLogView(l, viewer); filter.Match(v) {
				e.sendJSON("log", v)
				e.f.Flush()
			}
		}
	}
}

// captureSummary는 노드 이름이나 주소 없이 수집 여부만 알린다(뷰어도 볼 수 있는 정보만).
func captureSummary(h *deploy.NetworkLogHub) map[string]any {
	active, inactive := 0, 0
	var reason string
	for _, st := range h.CaptureStatus() {
		if st.Active {
			active++
		} else {
			inactive++
			if reason == "" {
				reason = st.Error
			}
		}
	}
	return map[string]any{"active_nodes": active, "inactive_nodes": inactive, "reason": reason}
}
