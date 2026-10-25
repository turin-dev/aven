package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/metrics"
	"github.com/turin-dev/aven/internal/store"
)

func (s *Server) httpLogs(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	past, ch, cancel := s.Deploy.HTTPLogs.Subscribe(c.in.ID)
	defer cancel()
	e, ok := newSSE(w)
	if !ok {
		return
	}
	for _, l := range past {
		e.sendJSON("log", l)
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
		case l := <-ch:
			e.sendJSON("log", l)
			e.f.Flush()
		}
	}
}

// envLogs는 환경의 모든(또는 선택한) 서비스 현재 배포 로그를 합쳐 흘려보낸다. q 필터는 서버에서 적용한다.
func (s *Server) envLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	env, _, ok := s.envAndProject(w, r)
	if !ok {
		return
	}
	want := map[string]bool{}
	if q := r.URL.Query().Get("services"); q != "" {
		for _, id := range strings.Split(q, ",") {
			want[strings.TrimSpace(id)] = true
		}
	}
	filter := deploy.ParseFilter(r.URL.Query().Get("q"))
	insts, err := s.Store.ListInstancesByEnv(ctx, env.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	e, ok := newSSE(w)
	if !ok {
		return
	}
	lines := make(chan deploy.LogEntry, 2048)
	var wg sync.WaitGroup
	for _, in := range insts {
		if len(want) > 0 && !want[in.ServiceID] {
			continue
		}
		if in.CurrentDeploymentID == nil || in.Status == store.InstOffline {
			continue
		}
		d, err := s.Store.GetDeployment(ctx, *in.CurrentDeploymentID)
		if err != nil {
			continue
		}
		wg.Add(1)
		go func(svcID string, d *store.Deployment) {
			defer wg.Done()
			s.Deploy.StreamDeployLogs(ctx, d, 200, true, func(l deploy.LogEntry) {
				l.ServiceID = svcID
				select {
				case lines <- l:
				case <-ctx.Done():
				}
			})
		}(in.ServiceID, d)
	}
	go func() { wg.Wait(); close(lines) }()
	errOnly := r.URL.Query().Get("level") == "error"
	s.pumpLogs(ctx, e, lines, func(l deploy.LogEntry) bool { return filter.Match(l.Msg) && (!errOnly || deploy.IsErrorLine(l)) })
}

func (s *Server) instanceMetrics(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	rng := r.URL.Query().Get("range")
	if _, _, ok := metrics.RangeStep(rng); !ok {
		writeErr(w, 400, "range must be 1h, 6h, 24h or 7d")
		return
	}
	pts, step, replicas, err := metrics.Query(r.Context(), s.Store, c.in.ID, rng)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if replicas == 0 {
		replicas = c.in.Replicas
	}
	writeJSON(w, 200, map[string]any{"points": pts, "step_s": step, "replicas": replicas})
}

func (s *Server) envMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	env, _, ok := s.envAndProject(w, r)
	if !ok {
		return
	}
	rng := r.URL.Query().Get("range")
	if _, _, ok := metrics.RangeStep(rng); !ok {
		writeErr(w, 400, "range must be 1h, 6h, 24h or 7d")
		return
	}
	insts, err := s.Store.ListInstancesByEnv(ctx, env.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	type svcPoints struct {
		ServiceID string          `json:"service_id"`
		Points    []metrics.Point `json:"points"`
	}
	out := []svcPoints{}
	for _, in := range insts {
		pts, _, _, err := metrics.Query(context.WithoutCancel(ctx), s.Store, in.ID, rng)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		out = append(out, svcPoints{ServiceID: in.ServiceID, Points: pts})
	}
	writeJSON(w, 200, map[string]any{"services": out})
}
