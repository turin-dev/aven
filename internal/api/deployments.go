package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/guardrails"
	"github.com/turin-dev/aven/internal/store"
)

func (s *Server) deployInstance(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	var body struct {
		ForceBuild bool `json:"force_build"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := s.checkSource(c); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	d, err := s.Deploy.Enqueue(r.Context(), c.in, deploy.EnqueueOpts{Trigger: s.deployTrigger(r.Context(), c.in), ForceBuild: body.ForceBuild})
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.emitDeployRequested(r, c.p.ID, c.env, c.svc, d)
	writeJSON(w, 202, d)
}

// emitDeployRequested는 누가 배포를 요청했는지 남긴다(actor는 요청 context에서 붙는다).
func (s *Server) emitDeployRequested(r *http.Request, pid string, env *store.Environment, svc *store.Service, d *store.Deployment) {
	if s.Events == nil || principalFrom(r) == nil {
		return
	}
	s.Events.Emit(r.Context(), pid, &env.ID, &svc.ID, "deploy.requested", fmt.Sprintf("%s (%s): %s 배포를 요청했습니다 (%s)", svc.Name, env.Name, d.ID, d.Trigger))
}

// deployTrigger는 저장만 해 둔 변경이 변수뿐이면 "variables", 아니면 "manual"이다.
func (s *Server) deployTrigger(ctx context.Context, in *store.Instance) string {
	if in.CurrentDeploymentID == nil {
		return "manual"
	}
	cur, err := s.Store.GetDeployment(ctx, *in.CurrentDeploymentID)
	if err != nil {
		return "manual"
	}
	pc := PendingChanges(in, cur)
	if len(pc) == 0 {
		return "manual"
	}
	for _, k := range pc {
		if !strings.HasPrefix(k, "variables.") {
			return "manual"
		}
	}
	return "variables"
}

func (s *Server) checkSource(c *instCtx) error {
	return sourceErr(c.svc, c.in)
}

func (s *Server) loadDeployment(w http.ResponseWriter, r *http.Request) (*store.Deployment, bool) {
	d, err := s.Store.GetDeployment(r.Context(), r.PathValue("did"))
	if err != nil {
		s.storeErr(w, err)
		return nil, false
	}
	return d, true
}

func (s *Server) redeploy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	target, ok := s.loadDeployment(w, r)
	if !ok {
		return
	}
	if target.Image == nil {
		writeErr(w, 400, "this deployment has no image to redeploy")
		return
	}
	in, err := s.Store.GetInstance(ctx, target.InstanceID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	d, err := s.Deploy.Enqueue(ctx, in, deploy.EnqueueOpts{Trigger: "redeploy", Image: target.Image, Spec: &target.Spec, ReqSpec: &target.ReqSpec,
		Commit: deploy.CommitOf(target), Branch: target.Branch, BuilderUsed: target.BuilderUsed})
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if t, err := s.Deploy.Load(ctx, in.ID); err == nil {
		s.emitDeployRequested(r, t.Project.ID, t.Env, t.Service, d)
	}
	writeJSON(w, 202, d)
}

// rollback은 대상 배포의 이미지와 변수 스냅샷을 복원한다. 인스턴스의 변수도 스냅샷으로 되돌린다.
func (s *Server) rollback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	target, ok := s.loadDeployment(w, r)
	if !ok {
		return
	}
	if target.Image == nil || !target.Status.Succeeded() {
		writeErr(w, 400, "only deployments that went live can be rolled back to")
		return
	}
	t, err := s.Deploy.Load(ctx, target.InstanceID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	in := t.Instance
	in.Variables = copyMap(target.VarsRaw)
	if err := s.Store.UpdateInstanceConfig(ctx, in); err != nil {
		s.storeErr(w, err)
		return
	}
	d, err := s.Deploy.Enqueue(ctx, in, deploy.EnqueueOpts{Trigger: "rollback", Image: target.Image, Spec: &target.Spec, ReqSpec: &target.ReqSpec,
		VarsRaw: target.VarsRaw, Commit: deploy.CommitOf(target), Branch: target.Branch, BuilderUsed: target.BuilderUsed})
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.Events.Emit(ctx, t.Project.ID, &t.Env.ID, &t.Service.ID, "deploy.rollback",
		fmt.Sprintf("%s (%s): %s 배포로 롤백합니다 (변수 복원)", t.Service.Name, t.Env.Name, target.ID))
	writeJSON(w, 202, d)
}

func (s *Server) cancelDeployment(w http.ResponseWriter, r *http.Request) {
	if err := s.Deploy.Cancel(r.Context(), r.PathValue("did")); err != nil {
		s.storeErr(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) restartInstance(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	if s.blockSuspended(w, r, c.in) { // 사용량 한도 정지(usagelimit.go)
		return
	}
	if err := s.Deploy.Restart(context.WithoutCancel(r.Context()), c.in); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	s.Events.Emit(r.Context(), c.p.ID, &c.env.ID, &c.svc.ID, "service.restarted", fmt.Sprintf("%s (%s): 재시작했습니다", c.svc.Name, c.env.Name))
	w.WriteHeader(204)
}

// scaleInstance는 replica 수를 재배포 없이 바로 바꾼다(POST .../scale {replicas}). 새 replica가 healthcheck를 통과할 때까지
// 기다렸다가 응답한다. 자동 확장이 켜져 있으면 409(자동 확장이 정한다). 인스턴스의 replicas(placement면 노드별 수)도 바뀐다.
func (s *Server) scaleInstance(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	var body struct {
		Replicas *int `json:"replicas"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Replicas == nil || *body.Replicas < 1 || *body.Replicas > deploy.MaxReplicas {
		writeErr(w, 400, fmt.Sprintf("replicas must be 1..%d", deploy.MaxReplicas))
		return
	}
	if a := c.in.Autoscale; a != nil && a.Enabled {
		writeErr(w, 409, "자동 확장이 켜져 있어 replica 수를 직접 바꿀 수 없습니다(autoscale의 min/max를 바꾸세요)")
		return
	}
	if s.blockSuspended(w, r, c.in) {
		return
	}
	if n := len(c.in.Placement); n > 1 && *body.Replicas < n {
		writeErr(w, 400, fmt.Sprintf("placement의 노드마다 replica가 하나 이상 있어야 합니다(최소 %d)", n))
		return
	}
	ctx := context.WithoutCancel(r.Context())
	res, err := s.Deploy.ScaleManual(ctx, c.in, *body.Replicas)
	if errors.Is(err, deploy.ErrScaleUnavailable) || errors.Is(err, store.ErrResourceLimit) || errors.Is(err, deploy.ErrSelfService) {
		writeErr(w, 409, err.Error())
		return
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	fresh, err := s.Store.GetInstance(ctx, c.in.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	nodes := res.Nodes
	if nodes == nil {
		nodes = []store.Placement{}
	}
	writeJSON(w, 200, map[string]any{"from": res.From, "to": res.To, "nodes": nodes, "instance": s.viewInstance(r, c.svc, c.env, fresh)})
}

func (s *Server) stopInstance(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	if err := s.Deploy.Stop(context.WithoutCancel(r.Context()), c.in); errors.Is(err, deploy.ErrSelfService) {
		writeErr(w, 409, err.Error())
		return
	} else if err != nil {
		s.storeErr(w, err)
		return
	}
	s.Events.Emit(r.Context(), c.p.ID, &c.env.ID, &c.svc.ID, "service.stopped", fmt.Sprintf("%s (%s): 중지했습니다", c.svc.Name, c.env.Name))
	w.WriteHeader(204)
}

func (s *Server) listDeployments(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	deps, err := s.Store.ListDeployments(r.Context(), c.in.ID, limit)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.Deploy.FillReplicas(r.Context(), deps...)
	writeJSON(w, 200, deps)
}

func (s *Server) getDeployment(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDeployment(w, r)
	if !ok {
		return
	}
	s.Deploy.FillReplicas(r.Context(), d)
	writeJSON(w, 200, d)
}

func (s *Server) cronRuns(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDeployment(w, r)
	if !ok {
		return
	}
	runs, err := s.Store.ListCronRuns(r.Context(), d.ID, 100)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, runs)
}

func (s *Server) cronRunLogs(w http.ResponseWriter, r *http.Request) {
	run, err := s.Store.GetCronRun(r.Context(), r.PathValue("rid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	logs := s.Deploy.CronRunLogs(run.ID)
	if logs == nil {
		logs = []deploy.LogEntry{}
	}
	writeJSON(w, 200, logs)
}

func (s *Server) runCron(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	if c.in.Status != store.InstScheduled {
		writeErr(w, 409, "service is not a deployed cron job")
		return
	}
	if s.blockSuspended(w, r, c.in) {
		return
	}
	run, err := s.Deploy.RunCron(context.WithoutCancel(r.Context()), c.in)
	if errors.Is(err, deploy.ErrCronBusy) {
		writeErr(w, 409, err.Error())
		return
	}
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 202, run)
}

// upload는 `aven up`의 소스 tar.gz를 받아 배포한다.
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	// 배포 출처 guardrail: CLI 업로드는 출처를 확인할 수 없어 막는다(본문을 받기 전에. Enqueue도 같은 검사를 한다).
	if err := s.checkSourcePolicy(r.Context(), guardrails.Source{Type: c.svc.SourceType, Uploaded: true}); err != nil {
		s.policyErr(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 512<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, 400, "multipart body with a 'source' file is required")
		return
	}
	path := s.Deploy.UploadPath()
	saved := false
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			os.Remove(path)
			writeErr(w, 400, "read upload: "+err.Error())
			return
		}
		if part.FormName() != "source" {
			continue
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		_, err = io.Copy(f, part)
		f.Close()
		if err != nil {
			os.Remove(path)
			writeErr(w, 400, "read upload: "+err.Error())
			return
		}
		saved = true
	}
	if !saved {
		writeErr(w, 400, "missing 'source' file (tar.gz)")
		return
	}
	d, err := s.Deploy.Enqueue(r.Context(), c.in, deploy.EnqueueOpts{Trigger: "cli", Source: "upload:" + path})
	if err != nil {
		os.Remove(path)
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 202, d)
}

// ---- logs SSE ----

func (s *Server) buildLogs(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDeployment(w, r)
	if !ok {
		return
	}
	past, ch, cancel := s.Deploy.Logs.Subscribe(d.ID)
	defer cancel()
	e, ok := newSSE(w)
	if !ok {
		return
	}
	for _, l := range past {
		e.send("log", l)
	}
	e.send("status", string(d.Status))
	e.f.Flush()
	if ch == nil {
		if d.Status.Terminal() {
			e.send("done", string(d.Status))
			e.f.Flush()
			return
		}
		s.waitQueued(r.Context(), e, d.ID)
		return
	}
	s.pump(r.Context(), e, d.ID, ch)
}

func (s *Server) pump(ctx context.Context, e *sse, id string, ch chan logbookEvent) {
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-keepalive.C:
			fmt.Fprint(e.w, ": ping\n\n")
			e.f.Flush()
		case ev, ok := <-ch:
			if !ok {
				if cur, err := s.Store.GetDeployment(context.WithoutCancel(ctx), id); err == nil && cur.Status.Terminal() {
					e.send("done", string(cur.Status))
					e.f.Flush()
				}
				return
			}
			e.send(ev.Kind, ev.Data)
			e.f.Flush()
			if ev.Kind == "done" {
				return
			}
		}
	}
}

// waitQueued는 큐에서 대기 중인 배포가 시작되면 로그 구독으로 넘어간다.
func (s *Server) waitQueued(ctx context.Context, e *sse, id string) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		d, err := s.Store.GetDeployment(ctx, id)
		if err != nil {
			return
		}
		if d.Status.Terminal() {
			past, _, cancel := s.Deploy.Logs.Subscribe(id)
			cancel()
			for _, l := range past {
				e.send("log", l)
			}
			e.send("status", string(d.Status))
			e.send("done", string(d.Status))
			e.f.Flush()
			return
		}
		if d.Status == store.DepQueued || d.Status == store.DepWaiting {
			continue
		}
		past, ch, cancel := s.Deploy.Logs.Subscribe(id)
		for _, l := range past {
			e.send("log", l)
		}
		e.f.Flush()
		if ch == nil {
			cancel()
			continue
		}
		s.pump(ctx, e, id, ch)
		cancel()
		return
	}
}

func (s *Server) deployLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d, ok := s.loadDeployment(w, r)
	if !ok {
		return
	}
	tail, _ := strconv.Atoi(r.URL.Query().Get("tail"))
	if tail <= 0 || tail > 10000 {
		tail = 500
	}
	in, err := s.Store.GetInstance(ctx, d.InstanceID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	current := in.CurrentDeploymentID != nil && *in.CurrentDeploymentID == d.ID && in.Status != store.InstOffline
	follow := current || !d.Status.Terminal()
	e, ok := newSSE(w)
	if !ok {
		return
	}
	lines := make(chan deploy.LogEntry, 1024)
	go func() {
		defer close(lines)
		s.Deploy.StreamDeployLogs(ctx, d, tail, follow, func(l deploy.LogEntry) {
			select {
			case lines <- l:
			case <-ctx.Done():
			}
		})
	}()
	var filter func(deploy.LogEntry) bool
	q := deploy.ParseFilter(r.URL.Query().Get("q"))
	if errOnly := r.URL.Query().Get("level") == "error"; errOnly || r.URL.Query().Get("q") != "" {
		filter = func(l deploy.LogEntry) bool { return q.Match(l.Msg) && (!errOnly || deploy.IsErrorLine(l)) }
	}
	s.pumpLogs(ctx, e, lines, filter)
	if ctx.Err() == nil {
		e.send("done", "")
		e.f.Flush()
	}
}

// pumpLogs는 로그 채널을 SSE로 보낸다. filter가 있으면 맞는 줄만 보낸다.
func (s *Server) pumpLogs(ctx context.Context, e *sse, lines chan deploy.LogEntry, filter func(deploy.LogEntry) bool) {
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-keepalive.C:
			fmt.Fprint(e.w, ": ping\n\n")
			e.f.Flush()
		case l, ok := <-lines:
			if !ok {
				return
			}
			if filter == nil || filter(l) {
				e.sendJSON("log", l)
			}
			for n := 0; n < 500 && len(lines) > 0; n++ {
				l, ok := <-lines
				if !ok {
					break
				}
				if filter == nil || filter(l) {
					e.sendJSON("log", l)
				}
			}
			e.f.Flush()
		}
	}
}

var errNoSource = errors.New("소스를 먼저 설정하세요")

// sourceErr는 배포할 소스(repo_url 또는 image)가 없으면 에러를 돌려준다.
func sourceErr(svc *store.Service, in *store.Instance) error {
	if svc.SourceType == "function" { // 코드는 생성 때 기본값으로 채워진다(없으면 빌드가 알려 준다)
		return nil
	}
	if svc.SourceType == "repo" {
		if in.RepoURL == nil || *in.RepoURL == "" {
			return errNoSource
		}
		return nil
	}
	if in.Image == nil || *in.Image == "" {
		return errNoSource
	}
	return nil
}
