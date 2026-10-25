package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/dotenv"

	"github.com/turin-dev/aven/internal/compose"
	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/guardrails"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// ---- request / source ----

type composeDomainReq struct {
	Host       string `json:"host"`        // 비우면 generated 도메인을 만든다
	TargetPort int    `json:"target_port"` // 0이면 서비스 포트
}

// composeReq는 preview와 apply가 같이 쓰는 요청이다.
type composeReq struct {
	Source          string            `json:"source"` // yaml | repo
	YAML            string            `json:"yaml"`
	RepoURL         string            `json:"repo_url"`
	Branch          string            `json:"branch"`
	Path            string            `json:"path"` // 저장소 안의 compose 파일 경로
	Name            string            `json:"name"` // 스택 이름(비우면 compose의 name: 또는 "stack")
	Env             string            `json:"env"`  // .env 내용: ${VAR} 보간과 env_file: .env에 쓴다
	EnvFiles        map[string]string `json:"env_files"`
	AllowBindMounts bool              `json:"allow_bind_mounts"`
	NodeID          string            `json:"node_id"`
	StackID         string            `json:"stack_id"`     // 이미 만든 스택을 다시 가져올 때
	ReuseSource     bool              `json:"reuse_source"` // 비어 있는 입력을 저장된 원본에서 채운다

	// apply 전용
	Domains  map[string][]composeDomainReq `json:"domains"`   // compose 서비스 이름 → 공개할 도메인
	TCPProxy map[string]int                `json:"tcp_proxy"` // compose 서비스 이름 → 컨테이너 포트
	Deploy   *bool                         `json:"deploy"`    // 기본 true
	Prune    bool                          `json:"prune"`     // compose에서 사라진 서비스를 삭제한다
}

// savedSource는 stack_envs.source에 저장하는 원본이다.
type savedSource struct {
	Source          string            `json:"source"`
	YAML            string            `json:"yaml,omitempty"`
	RepoURL         string            `json:"repo_url,omitempty"`
	Branch          string            `json:"branch,omitempty"`
	Path            string            `json:"path,omitempty"`
	Commit          string            `json:"commit,omitempty"`
	Env             string            `json:"env,omitempty"`
	EnvFiles        map[string]string `json:"env_files,omitempty"`
	AllowBindMounts bool              `json:"allow_bind_mounts,omitempty"`
	NodeID          string            `json:"node_id,omitempty"`
}

type composeLoaded struct {
	req      *composeReq
	plan     *compose.Plan
	stack    *store.Stack // 이미 있으면
	stackSvc map[string]*store.Service
	commit   string
	nodeID   string
	source   savedSource
	cleanup  func()
}

type composeFail struct {
	code int
	msg  string
}

func (e *composeFail) Error() string { return e.msg }

func failf(code int, f string, a ...any) *composeFail {
	return &composeFail{code, fmt.Sprintf(f, a...)}
}

func parseDotenv(s string) (map[string]string, error) {
	if strings.TrimSpace(s) == "" {
		return map[string]string{}, nil
	}
	return dotenv.UnmarshalWithLookup(s, nil)
}

var composeFileNames = []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"}

// loadCompose는 요청을 읽어 계획(Plan)을 만든다. 호출한 쪽이 cleanup을 불러야 한다.
func (s *Server) loadCompose(ctx context.Context, p *store.Project, env *store.Environment, req *composeReq) (*composeLoaded, *composeFail) {
	l := &composeLoaded{req: req, cleanup: func() {}}
	// 저장된 원본으로 빈 입력 채우기
	if req.StackID != "" {
		st, err := s.Store.GetStack(ctx, req.StackID)
		if err != nil || st.ProjectID != p.ID {
			return nil, failf(404, "stack not found")
		}
		if req.ReuseSource {
			if se, err := s.Store.GetStackEnv(ctx, st.ID, env.ID); err == nil {
				var sv savedSource
				if json.Unmarshal(se.Source, &sv) == nil {
					if req.Source == "" {
						req.Source = sv.Source
					}
					if req.YAML == "" {
						req.YAML = sv.YAML
					}
					if req.RepoURL == "" {
						req.RepoURL = sv.RepoURL
					}
					if req.Branch == "" {
						req.Branch = sv.Branch
					}
					if req.Path == "" {
						req.Path = sv.Path
					}
					if req.Env == "" && req.EnvFiles == nil {
						req.Env, req.EnvFiles = sv.Env, sv.EnvFiles
					}
					if !req.AllowBindMounts {
						req.AllowBindMounts = sv.AllowBindMounts
					}
					if req.NodeID == "" {
						req.NodeID = sv.NodeID
					}
				}
			}
		}
		if req.Name == "" {
			req.Name = st.Name
		}
	}
	if req.Name != "" {
		req.Name = strings.TrimSpace(req.Name)
		if !compose.ValidStack(req.Name) {
			return nil, failf(400, "name must be lowercase letters, digits and '-' (max 40)")
		}
	}
	in := compose.Input{Stack: req.Name, AllowBindMounts: req.AllowBindMounts, EnvFiles: map[string]string{}}
	for k, v := range req.EnvFiles {
		in.EnvFiles[k] = v
	}
	src := savedSource{Source: req.Source, Env: req.Env, EnvFiles: req.EnvFiles, AllowBindMounts: req.AllowBindMounts, NodeID: req.NodeID}
	switch req.Source {
	case "yaml", "":
		if strings.TrimSpace(req.YAML) == "" {
			return nil, failf(400, "yaml is required for source=yaml")
		}
		if len(req.YAML) > 1<<20 {
			return nil, failf(400, "compose file is too large")
		}
		in.YAML = req.YAML
		src.Source, src.YAML = "yaml", req.YAML
	case "repo":
		repo := strings.TrimSpace(req.RepoURL)
		if !s.validRepoURL(repo) {
			return nil, failf(400, "repo_url must be an https://, ssh:// or git@ URL")
		}
		branch := strings.TrimSpace(req.Branch)
		if branch == "" {
			branch = "main"
		}
		if !branchRe.MatchString(branch) || strings.HasPrefix(branch, "-") {
			return nil, failf(400, "invalid branch")
		}
		dir, err := os.MkdirTemp("", "keel-compose-")
		if err != nil {
			return nil, failf(500, "internal error")
		}
		l.cleanup = func() { os.RemoveAll(dir) }
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		c, err := node.Clone(cctx, repo, branch, "", filepath.Join(dir, "src"), s.Deploy.ProjectGitHubToken(cctx, p.ID), func(string) {})
		if err != nil {
			l.cleanup()
			return nil, failf(400, "git clone failed: %v", err)
		}
		root := filepath.Join(dir, "src")
		rel := strings.TrimSpace(req.Path)
		file := ""
		if rel == "" {
			for _, n := range composeFileNames {
				if _, err := os.Stat(filepath.Join(root, n)); err == nil {
					rel, file = n, filepath.Join(root, n)
					break
				}
			}
			if file == "" {
				l.cleanup()
				return nil, failf(400, "no compose file found in the repository root (tried %s); set path", strings.Join(composeFileNames, ", "))
			}
		} else {
			clean := filepath.Clean("/" + rel)
			file = filepath.Join(root, clean)
			rel = strings.TrimPrefix(clean, "/")
		}
		b, err := os.ReadFile(file)
		if err != nil {
			l.cleanup()
			return nil, failf(400, "cannot read %s in the repository", rel)
		}
		if len(b) > 1<<20 {
			l.cleanup()
			return nil, failf(400, "compose file is too large")
		}
		in.YAML, in.Filename = string(b), filepath.Base(file)
		in.RepoURL, in.Branch, in.RepoDir, in.ComposeDir = repo, branch, root, filepath.Dir(file)
		// 저장소의 .env는 기본값으로 쓰고, 요청의 env가 덮는다.
		in.Vars = map[string]string{}
		if eb, err := os.ReadFile(filepath.Join(in.ComposeDir, ".env")); err == nil {
			if m, err := parseDotenv(string(eb)); err == nil {
				for k, v := range m {
					in.Vars[k] = v
				}
			}
		}
		l.commit = c.SHA
		src.Source, src.RepoURL, src.Branch, src.Path, src.Commit = "repo", repo, branch, rel, c.SHA
	default:
		return nil, failf(400, "source must be yaml or repo")
	}
	vars, err := parseDotenv(req.Env)
	if err != nil {
		l.cleanup()
		return nil, failf(400, "env: %v", err)
	}
	if in.Vars == nil {
		in.Vars = map[string]string{}
	}
	for k, v := range vars {
		in.Vars[k] = v
	}
	if _, ok := in.EnvFiles[".env"]; !ok && strings.TrimSpace(req.Env) != "" {
		in.EnvFiles[".env"] = req.Env
	}
	plan, err := compose.Parse(ctx, in)
	if err != nil {
		l.cleanup()
		return nil, failf(400, "%v", err)
	}
	// 배포 출처 guardrail: 이미지 서비스와 승인되지 않은 저장소 빌드는 가져올 수 없다(access_controls.go).
	for _, sp := range plan.Services {
		gs := guardrails.Source{Type: sp.SourceType}
		if sp.SourceType == "repo" {
			gs.RepoURL = &in.RepoURL
		} else {
			gs.Image = &sp.Image
		}
		if err := s.checkSourcePolicy(ctx, gs); err != nil {
			l.cleanup()
			if errors.Is(err, store.ErrDeploymentSourceRestricted) {
				return nil, failf(403, "%s: %v", sp.ComposeName, err)
			}
			return nil, failf(500, "%v", err)
		}
	}
	if plan.Stack == "" || !compose.ValidStack(plan.Stack) {
		plan.Stack = "stack"
	}
	if len(plan.Services) == 0 {
		l.cleanup()
		return nil, failf(400, "the compose file defines no services")
	}
	l.plan, l.source = plan, src

	// 스택/이름
	l.stackSvc = map[string]*store.Service{}
	if req.StackID != "" {
		l.stack, _ = s.Store.GetStack(ctx, req.StackID)
		if l.stack != nil && req.Name == "" {
			plan.Stack = l.stack.Name
		}
	}
	if l.stack == nil {
		if st, err := s.Store.GetStackByName(ctx, p.ID, plan.Stack); err == nil {
			l.stack = st
		}
	}
	taken := map[string]bool{}
	svcs, err := s.Store.ListServices(ctx, p.ID)
	if err != nil {
		l.cleanup()
		return nil, failf(500, "internal error")
	}
	existing := map[string]string{}
	for _, sv := range svcs {
		if l.stack != nil && sv.StackID != nil && *sv.StackID == l.stack.ID && sv.ComposeName != nil {
			l.stackSvc[*sv.ComposeName] = sv
			existing[*sv.ComposeName] = sv.Name
			continue
		}
		taken[sv.Name] = true
	}
	plan.AssignNames(existing, taken)

	// 노드
	l.nodeID = store.LocalNodeID
	if req.NodeID != "" && !store.IsLocalNode(req.NodeID) {
		n, err := s.Store.GetNode(ctx, req.NodeID)
		if err != nil {
			l.cleanup()
			return nil, failf(400, "node %s does not exist", req.NodeID)
		}
		if !n.Schedulable {
			l.cleanup()
			return nil, failf(400, "node %s is not schedulable", n.Name)
		}
		l.nodeID = n.ID
	} else if req.NodeID == "" {
		// 다시 가져오는 경우 기존 노드를 유지한다.
		for _, sv := range l.stackSvc {
			if in, err := s.Store.GetInstanceFor(ctx, sv.ID, env.ID); err == nil {
				l.nodeID = in.NodeID
				break
			}
		}
	}
	remote := !store.IsLocalNode(l.nodeID)
	for _, sp := range plan.Services {
		if remote {
			if len(sp.BindMounts) > 0 {
				plan.AddIssue(compose.Issue{Severity: "error", Service: sp.ComposeName, Code: "bind_mount_remote", Message: "bind mounts only work on the local node; choose the local node or use named volumes"})
			}
			var keep []string
			for _, a := range sp.Aliases {
				if strings.Contains(a, "_") {
					plan.AddIssue(compose.Issue{Severity: "warning", Service: sp.ComposeName, Code: "alias_remote", Message: fmt.Sprintf("DNS alias %q contains '_' and is not allowed on remote nodes; it is dropped", a)})
					continue
				}
				keep = append(keep, a)
			}
			sp.Aliases = keep
			sp.Spec.Compose.Aliases = keep
		}
	}
	// 같은 환경의 다른 서비스와 이름/별칭이 겹치면 DNS가 섞인다.
	s.checkDNSConflicts(ctx, env, l, svcs)
	return l, nil
}

func (s *Server) checkDNSConflicts(ctx context.Context, env *store.Environment, l *composeLoaded, svcs []*store.Service) {
	byID := map[string]*store.Service{}
	for _, sv := range svcs {
		byID[sv.ID] = sv
	}
	insts, err := s.Store.ListInstancesByEnv(ctx, env.ID)
	if err != nil {
		return
	}
	reach := map[string]string{} // 이름 → 서비스
	for _, in := range insts {
		sv := byID[in.ServiceID]
		if sv == nil {
			continue
		}
		if l.stack != nil && sv.StackID != nil && *sv.StackID == l.stack.ID {
			continue
		}
		reach[sv.Name] = sv.Name
		if in.Compose != nil {
			for _, a := range in.Compose.Aliases {
				reach[a] = sv.Name
			}
		}
	}
	for _, sp := range l.plan.Services {
		for _, n := range append([]string{sp.Name}, sp.Aliases...) {
			if owner, ok := reach[n]; ok {
				l.plan.AddIssue(compose.Issue{Severity: "error", Service: sp.ComposeName, Code: "dns_conflict",
					Message: fmt.Sprintf("the name %q is already resolvable in this environment (service %s); two services would answer to the same DNS name. Import this stack into a separate environment", n, owner)})
			}
		}
	}
}

// ---- diff ----

type serviceDiff struct {
	Name        string   `json:"name"`
	ComposeName string   `json:"compose_name"`
	Action      string   `json:"action"` // create | update | unchanged | remove | keep
	Changes     []string `json:"changes"`
}

func shortVal(v *string) string {
	if v == nil {
		return "-"
	}
	return *v
}

func (s *Server) diffPlan(ctx context.Context, env *store.Environment, l *composeLoaded) []serviceDiff {
	out := []serviceDiff{}
	for _, sp := range l.plan.Services {
		sv := l.stackSvc[sp.ComposeName]
		d := serviceDiff{Name: sp.Name, ComposeName: sp.ComposeName, Changes: []string{}}
		if sv == nil {
			d.Action = "create"
			out = append(out, d)
			continue
		}
		in, err := s.Store.GetInstanceFor(ctx, sv.ID, env.ID)
		if err != nil {
			d.Action = "create"
			out = append(out, d)
			continue
		}
		sim := *in
		sim.Spec = in.Spec
		sim.Variables = copyMap(in.Variables)
		sp.ApplyTo(&sim)
		d.Changes = append(d.Changes, specChanges(in, &sim)...)
		d.Changes = append(d.Changes, varChanges(in.Variables, sim.Variables)...)
		d.Changes = append(d.Changes, s.volumeChanges(ctx, in, sp)...)
		if len(d.Changes) == 0 {
			d.Action = "unchanged"
		} else {
			d.Action = "update"
		}
		out = append(out, d)
	}
	inPlan := map[string]bool{}
	for _, sp := range l.plan.Services {
		inPlan[sp.ComposeName] = true
	}
	var gone []string
	for n := range l.stackSvc {
		if !inPlan[n] {
			gone = append(gone, n)
		}
	}
	sort.Strings(gone)
	for _, n := range gone {
		a := "keep"
		if l.req.Prune {
			a = "remove"
		}
		out = append(out, serviceDiff{Name: l.stackSvc[n].Name, ComposeName: n, Action: a, Changes: []string{"no longer in the compose file"}})
	}
	return out
}

func specChanges(a, b *store.Instance) []string {
	var am, bm map[string]json.RawMessage
	ab, _ := json.Marshal(a.Spec)
	bb, _ := json.Marshal(b.Spec)
	json.Unmarshal(ab, &am)
	json.Unmarshal(bb, &bm)
	keys := map[string]bool{}
	for k := range am {
		keys[k] = true
	}
	for k := range bm {
		keys[k] = true
	}
	var ks []string
	for k := range keys {
		if k == "domains" || k == "tcp_proxy" {
			continue
		}
		ks = append(ks, k)
	}
	sort.Strings(ks)
	var out []string
	for _, k := range ks {
		if string(am[k]) == string(bm[k]) {
			continue
		}
		if k == "compose" {
			out = append(out, "runtime settings (command/entrypoint/aliases/depends_on/labels)")
			continue
		}
		out = append(out, fmt.Sprintf("%s: %s → %s", k, trimJSON(am[k]), trimJSON(bm[k])))
	}
	return out
}

func trimJSON(r json.RawMessage) string {
	if len(r) == 0 {
		return "-"
	}
	s := string(r)
	if len(s) > 80 {
		s = s[:77] + "..."
	}
	return s
}

func varChanges(old, nw map[string]string) []string {
	var out []string
	keys := map[string]bool{}
	for k := range old {
		keys[k] = true
	}
	for k := range nw {
		keys[k] = true
	}
	var ks []string
	for k := range keys {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	for _, k := range ks {
		o, okO := old[k]
		n, okN := nw[k]
		switch {
		case !okO && okN:
			out = append(out, "variable added: "+k)
		case okO && !okN:
			out = append(out, "variable removed: "+k)
		case o != n:
			out = append(out, "variable changed: "+k)
		}
	}
	return out
}

func (s *Server) volumeChanges(ctx context.Context, in *store.Instance, sp *compose.ServicePlan) []string {
	vols, _ := s.Store.ListVolumesByInstance(ctx, in.ID)
	have := map[string]*store.Volume{}
	for _, v := range vols {
		have[v.MountPath] = v
	}
	var out []string
	want := map[string]bool{}
	for _, v := range sp.Volumes {
		want[v.MountPath] = true
		if have[v.MountPath] == nil {
			out = append(out, fmt.Sprintf("volume added: %s at %s", v.Name, v.MountPath))
		}
	}
	for _, v := range vols {
		if !want[v.MountPath] {
			out = append(out, fmt.Sprintf("volume %s at %s is no longer in the compose file (kept, data is not deleted)", v.Name, v.MountPath))
		}
	}
	return out
}

// ---- handlers ----

func (s *Server) composeEnv(w http.ResponseWriter, r *http.Request) (*store.Environment, *store.Project, *composeReq, bool) {
	ctx := r.Context()
	p, err := s.Store.GetProject(ctx, r.PathValue("pid"))
	if err != nil {
		s.storeErr(w, err)
		return nil, nil, nil, false
	}
	env, err := s.Store.GetEnvironment(ctx, r.PathValue("eid"))
	if err != nil || env.ProjectID != p.ID {
		writeErr(w, 404, "environment not found")
		return nil, nil, nil, false
	}
	var req composeReq
	if !decode(w, r, &req) {
		return nil, nil, nil, false
	}
	return env, p, &req, true
}

func (s *Server) composePreview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	env, p, req, ok := s.composeEnv(w, r)
	if !ok {
		return
	}
	l, fail := s.loadCompose(ctx, p, env, req)
	if fail != nil {
		writeErr(w, fail.code, fail.msg)
		return
	}
	defer l.cleanup()
	var stack any
	if l.stack != nil {
		stack = l.stack
	}
	writeJSON(w, 200, map[string]any{
		"plan":    l.plan,
		"stack":   stack,
		"exists":  l.stack != nil,
		"diff":    s.diffPlan(ctx, env, l),
		"node_id": l.nodeID,
		"source":  map[string]any{"kind": l.source.Source, "repo_url": l.source.RepoURL, "branch": l.source.Branch, "path": l.source.Path, "commit": l.source.Commit},
		"ok":      l.plan.OK(),
	})
}

// stackLayout은 새 서비스의 캔버스 위치를 정한다: 기존 카드 아래에 의존 단계별로 열을 만든다.
func stackLayout(plan *compose.Plan, existing []*store.Instance) map[string]store.Canvas {
	y0 := 0.0
	for _, in := range existing {
		if in.Canvas != nil && in.Canvas.Y+300 > y0 {
			y0 = in.Canvas.Y + 300
		}
	}
	if y0 > 0 {
		y0 += 60
	}
	out := map[string]store.Canvas{}
	for col, lv := range plan.Levels {
		for row, n := range lv {
			out[n] = store.Canvas{X: float64(col * 432), Y: y0 + float64(row*288)}
		}
	}
	return out
}

func (s *Server) composeApply(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	env, p, req, ok := s.composeEnv(w, r)
	if !ok {
		return
	}
	l, fail := s.loadCompose(ctx, p, env, req)
	if fail != nil {
		writeErr(w, fail.code, fail.msg)
		return
	}
	defer l.cleanup()
	plan := l.plan
	// 공개 요청 검증
	for name, doms := range req.Domains {
		sp := plan.Service(name)
		if sp == nil {
			writeErr(w, 400, "domains: unknown compose service "+name)
			return
		}
		for _, d := range doms {
			if d.TargetPort < 0 || d.TargetPort > 65535 {
				writeErr(w, 400, "domains: invalid target_port")
				return
			}
		}
	}
	for name, port := range req.TCPProxy {
		if plan.Service(name) == nil || port < 1 || port > 65535 {
			writeErr(w, 400, "tcp_proxy: unknown service or invalid port for "+name)
			return
		}
	}
	// 공개 네트워킹 guardrail: 작업 공간 admin이 아니면 생성 도메인과 TCP 프록시를 요청할 수 없다(custom domain은 된다).
	for _, doms := range req.Domains {
		for _, d := range doms {
			if err := s.checkGeneratedDomainPolicy(ctx, r, d.Host, strings.TrimSpace(d.Host) == ""); err != nil {
				s.policyErr(w, err)
				return
			}
		}
	}
	if len(req.TCPProxy) > 0 {
		if err := s.checkTCPProxyPolicy(ctx, r); err != nil {
			s.policyErr(w, err)
			return
		}
	}
	if !plan.OK() {
		writeJSON(w, 422, map[string]any{"error": fmt.Sprintf("the compose file cannot be imported: %d error(s)", len(plan.Errors)), "plan": plan})
		return
	}
	diff := s.diffPlan(ctx, env, l)

	envs, err := s.Store.ListEnvironments(ctx, p.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var existingInst []*store.Instance
	if all, err := s.Store.ListInstancesByEnv(ctx, env.ID); err == nil {
		existingInst = all
	}
	layout := stackLayout(plan, existingInst)

	stack := l.stack
	created := map[string]bool{}
	svcByCompose := map[string]*store.Service{}
	err = s.Store.Tx(ctx, func(tx *store.Store) error {
		if stack == nil {
			stack = &store.Stack{ProjectID: p.ID, Name: plan.Stack}
			if err := tx.CreateStack(ctx, stack); err != nil {
				return err
			}
		}
		for _, sp := range plan.Services {
			svc := l.stackSvc[sp.ComposeName]
			if svc == nil {
				stackID, cname := stack.ID, sp.ComposeName
				svc = &store.Service{ProjectID: p.ID, Name: sp.Name, SourceType: sp.SourceType, StackID: &stackID, ComposeName: &cname}
				icon := sp.Icon
				svc.Icon = &icon
				if err := tx.CreateService(ctx, svc); err != nil {
					return err
				}
				created[sp.ComposeName] = true
			}
			svcByCompose[sp.ComposeName] = svc
			for _, e := range envs {
				in, err := tx.GetInstanceFor(ctx, svc.ID, e.ID)
				switch {
				case errors.Is(err, store.ErrNotFound):
					in = &store.Instance{ServiceID: svc.ID, EnvironmentID: e.ID, NodeID: store.LocalNodeID}
					in.Spec.Defaults()
					if e.ID == env.ID {
						in.NodeID = l.nodeID
					}
					sp.ApplyTo(in)
					if c, ok := layout[sp.ComposeName]; ok {
						cc := c
						in.Canvas = &cc
					}
					if err := tx.CreateInstance(ctx, in); err != nil {
						return err
					}
				case err != nil:
					return err
				case e.ID == env.ID:
					sp.ApplyTo(in)
					in.NodeID = l.nodeID
					if err := tx.UpdateInstanceConfig(ctx, in); err != nil {
						return err
					}
				default:
					continue
				}
				if err := syncComposeVolumes(ctx, tx, p, e, in, sp); err != nil {
					return err
				}
			}
		}
		src, _ := json.Marshal(l.source)
		return tx.PutStackEnv(ctx, &store.StackEnv{StackID: stack.ID, EnvironmentID: env.ID, Source: src})
	})
	if err != nil {
		s.storeErr(w, err)
		return
	}

	var notes []string
	// 공개 도메인 / TCP proxy: 요청한 것만 켠다(기본은 비공개).
	for _, sp := range plan.Services {
		svc := svcByCompose[sp.ComposeName]
		in, err := s.Store.GetInstanceFor(ctx, svc.ID, env.ID)
		if err != nil {
			continue
		}
		changed := false
		for _, dr := range req.Domains[sp.ComposeName] {
			host := strings.ToLower(strings.TrimSpace(dr.Host))
			var d store.Domain
			if host == "" {
				host, err = s.generateDomain(ctx, svc.Name, env.Name)
				if err != nil {
					notes = append(notes, fmt.Sprintf("%s: %v", sp.ComposeName, err))
					continue
				}
				d = store.Domain{Host: host, Kind: "generated"}
			} else {
				d = store.Domain{Host: host}
			}
			if dr.TargetPort > 0 {
				tp := dr.TargetPort
				d.TargetPort = &tp
			}
			all := append([]store.Domain{}, in.Domains...)
			dup := false
			for _, e := range all {
				if e.Host == d.Host {
					dup = true
				}
			}
			if dup {
				continue
			}
			doms, err := s.normalizeDomains(ctx, in, append(all, d))
			if err != nil {
				notes = append(notes, fmt.Sprintf("%s: domain %s: %v", sp.ComposeName, d.Host, err))
				continue
			}
			in.Domains = doms
			changed = true
		}
		if port, ok := req.TCPProxy[sp.ComposeName]; ok {
			if in.TCPProxy != nil {
				in.TCPProxy.ContainerPort = port
			} else if pub, err := s.Deploy.AllocateTCPPortFor(ctx, s.Deploy.HomeNode(ctx, in)); err == nil {
				in.TCPProxy = &store.TCPProxy{ContainerPort: port, PublicPort: pub}
			} else {
				notes = append(notes, fmt.Sprintf("%s: %v", sp.ComposeName, err))
			}
			changed = true
		}
		if changed {
			if err := s.Store.UpdateInstanceConfig(ctx, in); err != nil {
				s.Log.Warn("compose apply: domains", "err", err)
				continue
			}
			if in.TCPProxy != nil {
				s.Deploy.SyncTCP(ctx, in.ID)
			}
		}
	}
	if err := s.Deploy.SyncProxy(ctx); err != nil {
		s.Log.Warn("proxy sync", "err", err)
	}

	// compose에서 사라진 서비스
	if req.Prune {
		in := map[string]bool{}
		for _, sp := range plan.Services {
			in[sp.ComposeName] = true
		}
		for n, sv := range l.stackSvc {
			if !in[n] {
				if err := s.Deploy.DeleteService(context.WithoutCancel(ctx), sv); err != nil {
					notes = append(notes, fmt.Sprintf("remove %s: %v", sv.Name, err))
				}
			}
		}
	}
	msg := fmt.Sprintf("스택 %s: compose 가져오기 (%d개 서비스)", plan.Stack, len(plan.Services))
	s.Events.Emit(ctx, p.ID, &env.ID, nil, "stack.applied", msg)

	// 배포(의존 순서)
	deployResp := map[string]any{"started": false}
	if req.Deploy == nil || *req.Deploy {
		members, err := s.stackMembers(ctx, stack, env)
		if err == nil {
			lv, started, derr := s.Deploy.DeployStack(stack.ID, env.ID, members, "manual")
			switch {
			case derr != nil:
				notes = append(notes, derr.Error())
			case !started:
				notes = append(notes, "a deploy of this stack is already running")
			default:
				deployResp = map[string]any{"started": true, "levels": lv}
			}
		}
	}
	createdNames := []string{}
	for n := range created {
		createdNames = append(createdNames, n)
	}
	sort.Strings(createdNames)
	writeJSON(w, 200, map[string]any{
		"stack":   stack,
		"plan":    plan,
		"diff":    diff,
		"created": createdNames,
		"deploy":  deployResp,
		"notes":   notes,
	})
}

// syncComposeVolumes는 계획한 볼륨을 인스턴스에 맞춘다. compose에서 사라진 볼륨은 데이터 보호를 위해 지우지 않는다.
func syncComposeVolumes(ctx context.Context, tx *store.Store, p *store.Project, env *store.Environment, in *store.Instance, sp *compose.ServicePlan) error {
	have, err := tx.ListVolumesByInstance(ctx, in.ID)
	if err != nil {
		return err
	}
	byPath := map[string]*store.Volume{}
	byName := map[string]*store.Volume{}
	for _, v := range have {
		byPath[v.MountPath] = v
		byName[v.Name] = v
	}
	for _, want := range sp.Volumes {
		if byPath[want.MountPath] != nil {
			continue
		}
		if v := byName[want.Name]; v != nil { // 같은 이름, 경로만 바뀜
			v.MountPath = want.MountPath
			if err := tx.UpdateVolume(ctx, v); err != nil {
				return err
			}
			continue
		}
		v := &store.Volume{ProjectID: p.ID, EnvironmentID: env.ID, InstanceID: &in.ID, Name: want.Name, MountPath: want.MountPath}
		if err := tx.CreateVolume(ctx, v); err != nil {
			return err
		}
	}
	return nil
}

// stackMembers는 환경에서 스택 서비스들의 인스턴스와 의존 정보를 모은다.
func (s *Server) stackMembers(ctx context.Context, stack *store.Stack, env *store.Environment) ([]deploy.StackMember, error) {
	svcs, err := s.Store.ListServicesByStack(ctx, stack.ID)
	if err != nil {
		return nil, err
	}
	var out []deploy.StackMember
	for _, sv := range svcs {
		in, err := s.Store.GetInstanceFor(ctx, sv.ID, env.ID)
		if err != nil {
			continue
		}
		m := deploy.StackMember{Name: sv.Name, Instance: in}
		if sv.ComposeName != nil {
			m.Name = *sv.ComposeName
		}
		if in.Compose != nil {
			m.Deps = in.Compose.DependsOn
		}
		out = append(out, m)
	}
	return out, nil
}

// ---- stacks ----

type stackView struct {
	*store.Stack
	ServiceIDs []string   `json:"service_ids"`
	Levels     [][]string `json:"levels"`   // 배포 순서(aven 서비스 이름)
	Imported   bool       `json:"imported"` // 이 환경에서 가져왔는지
	Deploying  bool       `json:"deploying"`
	Source     any        `json:"source"`
	AppliedAt  *time.Time `json:"applied_at"`
}

func maskSource(raw json.RawMessage) any {
	var sv savedSource
	if json.Unmarshal(raw, &sv) != nil {
		return nil
	}
	envKeys := []string{}
	if m, err := parseDotenv(sv.Env); err == nil {
		for k := range m {
			envKeys = append(envKeys, k)
		}
	}
	sort.Strings(envKeys)
	return map[string]any{
		"kind": sv.Source, "repo_url": sv.RepoURL, "branch": sv.Branch, "path": sv.Path, "commit": sv.Commit,
		"has_yaml": sv.YAML != "", "env_keys": envKeys, "allow_bind_mounts": sv.AllowBindMounts, "node_id": sv.NodeID,
	}
}

func (s *Server) buildStackView(ctx context.Context, st *store.Stack, env *store.Environment) *stackView {
	v := &stackView{Stack: st, ServiceIDs: []string{}, Levels: [][]string{}, Deploying: deploy.StackDeploying(st.ID, env.ID)}
	svcs, _ := s.Store.ListServicesByStack(ctx, st.ID)
	var members []deploy.StackMember
	byCompose := map[string]string{}
	for _, sv := range svcs {
		v.ServiceIDs = append(v.ServiceIDs, sv.ID)
		in, err := s.Store.GetInstanceFor(ctx, sv.ID, env.ID)
		if err != nil {
			continue
		}
		m := deploy.StackMember{Name: sv.Name, Instance: in}
		if sv.ComposeName != nil {
			m.Name = *sv.ComposeName
			byCompose[*sv.ComposeName] = sv.Name
		}
		if in.Compose != nil {
			m.Deps = in.Compose.DependsOn
		}
		members = append(members, m)
	}
	if lv, err := deploy.StackLevels(members); err == nil {
		for _, l := range lv {
			var names []string
			for _, m := range l {
				n := m.Name
				if k, ok := byCompose[m.Name]; ok {
					n = k
				}
				names = append(names, n)
			}
			v.Levels = append(v.Levels, names)
		}
	}
	if se, err := s.Store.GetStackEnv(ctx, st.ID, env.ID); err == nil {
		v.Imported = true
		v.Source = maskSource(se.Source)
		t := se.AppliedAt
		v.AppliedAt = &t
	}
	return v
}

func (s *Server) stackViews(ctx context.Context, p *store.Project, env *store.Environment) []*stackView {
	out := []*stackView{}
	sts, err := s.Store.ListStacks(ctx, p.ID)
	if err != nil {
		return out
	}
	for _, st := range sts {
		out = append(out, s.buildStackView(ctx, st, env))
	}
	return out
}

func (s *Server) loadStack(w http.ResponseWriter, r *http.Request) (*store.Environment, *store.Project, *store.Stack, bool) {
	ctx := r.Context()
	env, p, ok := s.envAndProject(w, r)
	if !ok {
		return nil, nil, nil, false
	}
	st, err := s.Store.GetStack(ctx, r.PathValue("stid"))
	if err != nil || st.ProjectID != p.ID {
		writeErr(w, 404, "stack not found")
		return nil, nil, nil, false
	}
	return env, p, st, true
}

func (s *Server) listStacks(w http.ResponseWriter, r *http.Request) {
	env, p, ok := s.envAndProject(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, s.stackViews(r.Context(), p, env))
}

func (s *Server) getStack(w http.ResponseWriter, r *http.Request) {
	env, _, st, ok := s.loadStack(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, s.buildStackView(r.Context(), st, env))
}

func (s *Server) deleteStack(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, p, st, ok := s.loadStack(w, r)
	if !ok {
		return
	}
	svcs, err := s.Store.ListServicesByStack(ctx, st.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	for _, sv := range svcs {
		if err := s.Deploy.DeleteService(context.WithoutCancel(ctx), sv); err != nil {
			s.storeErr(w, err)
			return
		}
	}
	if err := s.Store.DeleteStack(ctx, st.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	s.Events.Emit(ctx, p.ID, nil, nil, "stack.deleted", "스택 "+st.Name+" 삭제 ("+fmt.Sprint(len(svcs))+"개 서비스)")
	w.WriteHeader(204)
}

func (s *Server) redeployStack(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	env, p, st, ok := s.loadStack(w, r)
	if !ok {
		return
	}
	members, err := s.stackMembers(ctx, st, env)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if len(members) == 0 {
		writeErr(w, 400, "the stack has no services in this environment")
		return
	}
	lv, started, err := s.Deploy.DeployStack(st.ID, env.ID, members, "manual")
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if !started {
		writeErr(w, 409, "a deploy of this stack is already running")
		return
	}
	s.Events.Emit(ctx, p.ID, &env.ID, nil, "stack.redeploy", "스택 "+st.Name+" 전체 재배포 시작")
	writeJSON(w, 202, map[string]any{"started": true, "levels": lv})
}

var _ = reflect.DeepEqual
