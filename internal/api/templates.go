package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/catalog"
	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/guardrails"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
	"github.com/turin-dev/aven/internal/vars"
)

type nodeCommit = node.Commit

// ---- 템플릿 목록/상세 ----
//
// 내장 템플릿(catalog, templates/*.json)과 사용자 템플릿(templates 테이블)을 함께 보여 준다. 사용자 템플릿 공개 범위:
// private은 만든 사람과 작업 공간 admin만, workspace/public은 로그인한 모든 사용자에게 보인다.

type templateService struct {
	Name         string  `json:"name"`
	SourceType   string  `json:"source_type"`
	DatabaseKind *string `json:"database_kind,omitempty"`
	Image        *string `json:"image,omitempty"`
	RepoURL      *string `json:"repo_url,omitempty"`
	Icon         string  `json:"icon,omitempty"`
}

type templateView struct {
	Key         string            `json:"key"`
	WorkspaceID string            `json:"workspace_id,omitempty"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Icon        string            `json:"icon"`
	Category    string            `json:"category"`
	Tags        []string          `json:"tags"`
	Services    []templateService `json:"services"`
	Inputs      []catalog.Input   `json:"inputs"`
	Builtin     bool              `json:"builtin"`
	Visibility  string            `json:"visibility"`
	OwnerUserID *string           `json:"owner_user_id"`
	DeployCount int               `json:"deploy_count"`
	CreatedAt   *time.Time        `json:"created_at"`
	UpdatedAt   *time.Time        `json:"updated_at"`
	CanEdit     bool              `json:"can_edit"`
}

type templateDetail struct {
	templateView
	Readme     string          `json:"readme"`
	Definition json.RawMessage `json:"definition"`
}

// foundTemplate은 키로 찾은 템플릿(내장 또는 사용자)이다.
type foundTemplate struct {
	key     string
	def     *catalog.Definition
	builtin *catalog.Template
	user    *store.UserTemplate
}

func (f *foundTemplate) name() string {
	if f.user != nil {
		return f.user.Name
	}
	return f.builtin.Name
}

func templateServices(d *catalog.Definition) []templateService {
	out := []templateService{}
	for _, sv := range d.Services {
		ts := templateService{Name: sv.Name, Icon: sv.Icon}
		switch {
		case sv.Source.Database != "":
			k := sv.Source.Database
			ts.SourceType, ts.DatabaseKind = "database", &k
			if db, ok := catalog.Databases[k]; ok {
				img := db.Image
				ts.Image = &img
			}
			if ts.Icon == "" {
				ts.Icon = k
			}
		case sv.Source.Repo != nil:
			u := sv.Source.Repo.URL
			ts.SourceType, ts.RepoURL = "repo", &u
		default:
			img := sv.Source.Image
			ts.SourceType, ts.Image = "image", &img
		}
		out = append(out, ts)
	}
	return out
}

func inputsOf(d *catalog.Definition) []catalog.Input {
	if d.Inputs == nil {
		return []catalog.Input{}
	}
	return d.Inputs
}

func (s *Server) templateLevel(r *http.Request, t *store.UserTemplate) int {
	p := principalFrom(r)
	if p == nil {
		return lvOwner
	}
	lv, err := s.workspaceAccess(r.Context(), p, t.WorkspaceID)
	if err != nil {
		return lvNone
	}
	return lv
}
func (s *Server) canSeeTemplate(r *http.Request, t *store.UserTemplate) bool {
	if t.Visibility == store.TemplatePublic {
		return true
	}
	lv := s.templateLevel(r, t)
	if lv == lvNone {
		return false
	}
	p := principalFrom(r)
	return t.Visibility == store.TemplateWorkspace || lv >= lvAdmin || p == nil || t.OwnerUserID == p.User.ID
}
func (s *Server) canEditTemplate(r *http.Request, t *store.UserTemplate) bool {
	lv := s.templateLevel(r, t)
	p := principalFrom(r)
	return lv >= lvAdmin || lv >= lvMember && (p == nil || t.OwnerUserID == p.User.ID)
}

func builtinView(t catalog.Template, count int) templateView {
	tags := t.Tags
	if tags == nil {
		tags = []string{}
	}
	return templateView{Key: t.Key, Name: t.Name, Description: t.Description, Icon: t.Icon, Category: t.Category, Tags: tags,
		Services: templateServices(t.Definition), Inputs: inputsOf(t.Definition), Builtin: true, Visibility: store.TemplatePublic, DeployCount: count}
}

func (s *Server) userView(r *http.Request, t *store.UserTemplate, d *catalog.Definition) templateView {
	owner := t.OwnerUserID
	created, updated := t.CreatedAt, t.UpdatedAt
	v := templateView{Key: t.Key, Name: t.Name, Description: t.Description, Icon: t.Icon, Category: t.Category, Tags: t.Tags,
		WorkspaceID: t.WorkspaceID, Services: []templateService{}, Inputs: []catalog.Input{}, Visibility: t.Visibility, OwnerUserID: &owner, DeployCount: t.DeployCount,
		CreatedAt: &created, UpdatedAt: &updated, CanEdit: s.canEditTemplate(r, t)}
	if d != nil {
		v.Services, v.Inputs = templateServices(d), inputsOf(d)
	}
	return v
}

// parseStored는 저장된 정의를 읽는다(저장할 때 검증했다).
func parseStored(t *store.UserTemplate) (*catalog.Definition, error) {
	return catalog.ParseDefinition(t.Definition)
}

// findTemplate은 주체에게 보이는 템플릿을 찾는다(보이지 않으면 ErrNotFound).
func (s *Server) findTemplate(r *http.Request, key string) (*foundTemplate, error) {
	if t, ok := catalog.Templates[key]; ok {
		return &foundTemplate{key: key, def: t.Definition, builtin: &t}, nil
	}
	if s.Store == nil {
		return nil, store.ErrNotFound
	}
	ut, err := s.Store.GetTemplateByKey(r.Context(), key)
	if err != nil {
		return nil, err
	}
	if !s.canSeeTemplate(r, ut) {
		return nil, store.ErrNotFound
	}
	d, err := parseStored(ut)
	if err != nil {
		return nil, err
	}
	return &foundTemplate{key: key, def: d, user: ut}, nil
}

func templateMatches(v templateView, q, category, tag string) bool {
	if category != "" && v.Category != category {
		return false
	}
	if tag != "" {
		ok := false
		for _, t := range v.Tags {
			if t == tag {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	if q == "" {
		return true
	}
	hay := strings.ToLower(v.Key + " " + v.Name + " " + v.Description + " " + v.Category + " " + strings.Join(v.Tags, " "))
	for _, sv := range v.Services {
		hay += " " + sv.Name
	}
	for _, w := range strings.Fields(q) {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

// listTemplates: GET /api/templates?q=&category=&tag=&sort=popular|new
func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	qv := r.URL.Query()
	q, category, tag, sortBy := strings.ToLower(strings.TrimSpace(qv.Get("q"))), qv.Get("category"), qv.Get("tag"), qv.Get("sort")
	if sortBy != "" && sortBy != "popular" && sortBy != "new" {
		writeErr(w, 400, "sort must be popular or new")
		return
	}
	counts := map[string]int{}
	var users []*store.UserTemplate
	if s.Store != nil {
		var err error
		if counts, err = s.Store.BuiltinTemplateDeploys(ctx); err != nil {
			s.storeErr(w, err)
			return
		}
		if users, err = s.Store.ListTemplates(ctx); err != nil {
			s.storeErr(w, err)
			return
		}
	}
	out := []templateView{}
	for _, k := range catalog.Keys() {
		if catalog.Templates[k].Definition.IsHACompanion() { // HA 동반 템플릿은 DB 전환에서만 쓴다(ha.go)
			continue
		}
		if v := builtinView(catalog.Templates[k], counts[k]); templateMatches(v, q, category, tag) {
			out = append(out, v)
		}
	}
	var mine []templateView
	for _, t := range users {
		if !s.canSeeTemplate(r, t) {
			continue
		}
		d, err := parseStored(t)
		if err != nil {
			s.Log.Warn("stored template", "key", t.Key, "err", err)
			continue
		}
		if v := s.userView(r, t, d); templateMatches(v, q, category, tag) {
			mine = append(mine, v)
		}
	}
	sort.SliceStable(mine, func(i, j int) bool { return mine[i].Name < mine[j].Name })
	out = append(out, mine...)
	switch sortBy {
	case "popular":
		sort.SliceStable(out, func(i, j int) bool { return out[i].DeployCount > out[j].DeployCount })
	case "new":
		sort.SliceStable(out, func(i, j int) bool {
			a, b := out[i].CreatedAt, out[j].CreatedAt
			switch {
			case a == nil:
				return false // 내장 템플릿은 맨 뒤
			case b == nil:
				return true
			}
			return a.After(*b)
		})
	}
	writeJSON(w, 200, out)
}

func (s *Server) getTemplate(w http.ResponseWriter, r *http.Request) {
	f, err := s.findTemplate(r, r.PathValue("key"))
	if err != nil {
		s.templateErr(w, err)
		return
	}
	writeJSON(w, 200, s.detailOf(r, f))
}

func (s *Server) detailOf(r *http.Request, f *foundTemplate) templateDetail {
	if f.builtin != nil {
		counts := map[string]int{}
		if s.Store != nil {
			counts, _ = s.Store.BuiltinTemplateDeploys(r.Context())
		}
		return templateDetail{templateView: builtinView(*f.builtin, counts[f.key]), Readme: f.def.Readme, Definition: f.builtin.Raw}
	}
	return templateDetail{templateView: s.userView(r, f.user, f.def), Readme: f.user.Readme, Definition: f.user.Definition}
}

func (s *Server) templateErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, 404, "template not found")
		return
	}
	s.storeErr(w, err)
}

// exportTemplate: GET /api/templates/{key}/export — 정의 JSON 파일로 내려받는다.
func (s *Server) exportTemplate(w http.ResponseWriter, r *http.Request) {
	f, err := s.findTemplate(r, r.PathValue("key"))
	if err != nil {
		s.templateErr(w, err)
		return
	}
	body := []byte(f.user.Definition)
	if f.builtin != nil {
		body = f.builtin.Raw
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+f.key+`.json"`)
	w.WriteHeader(200)
	w.Write(body)
}

// ---- 사용자 템플릿 만들기/바꾸기/지우기 ----

type templateInput struct {
	WorkspaceID string          `json:"workspace_id"`
	Key         *string         `json:"key"`
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	Readme      *string         `json:"readme"`
	Icon        *string         `json:"icon"`
	Category    *string         `json:"category"`
	Tags        *[]string       `json:"tags"`
	Visibility  *string         `json:"visibility"`
	Definition  json.RawMessage `json:"definition"`
}

// applyMeta는 요청의 메타데이터를 정의에 덮어쓴다(정의 JSON과 테이블 열이 같은 값을 갖게 한다).
func (in *templateInput) applyMeta(d *catalog.Definition) {
	set := func(dst *string, p *string) {
		if p != nil {
			*dst = strings.TrimSpace(*p)
		}
	}
	set(&d.Name, in.Name)
	set(&d.Description, in.Description)
	set(&d.Icon, in.Icon)
	set(&d.Category, in.Category)
	if in.Readme != nil {
		d.Readme = *in.Readme
	}
	if in.Tags != nil {
		d.Tags = append([]string{}, (*in.Tags)...)
	}
}

func validVisibility(v string) bool {
	return v == store.TemplatePrivate || v == store.TemplateWorkspace || v == store.TemplatePublic
}

// writeTemplateDef는 검증한 정의를 저장할 JSON으로 만든다. 문제가 있으면 400을 쓰고 false.
func writeTemplateDef(w http.ResponseWriter, d *catalog.Definition) ([]byte, bool) {
	if d.Name == "" {
		d.Name = d.Key
	}
	if d.Icon == "" {
		for _, sv := range d.Services {
			if sv.Name == d.PrimaryService() {
				d.Icon = sv.Icon
				if d.Icon == "" {
					d.Icon = sv.Source.Database
				}
			}
		}
	}
	if err := d.Validate(); err != nil {
		writeValidation(w, err)
		return nil, false
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil || len(b) > catalog.MaxDefinitionBytes {
		writeErr(w, 400, "definition is too large")
		return nil, false
	}
	return b, true
}

func writeValidation(w http.ResponseWriter, err error) {
	var ve *catalog.ValidationError
	if errors.As(err, &ve) {
		writeJSON(w, 400, map[string]any{"error": err.Error(), "problems": ve.Problems})
		return
	}
	writeErr(w, 400, err.Error())
}

func storedFrom(t *store.UserTemplate, d *catalog.Definition, def []byte) {
	t.Name, t.Description, t.Readme, t.Icon, t.Category = d.Name, d.Description, d.Readme, d.Icon, d.Category
	t.Tags = append([]string{}, d.Tags...)
	t.Definition = def
}

// createTemplate: POST /api/templates {definition, key?, name?, ..., visibility?}
func (s *Server) createTemplate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in templateInput
	if !decode(w, r, &in) {
		return
	}
	if len(in.Definition) == 0 || string(in.Definition) == "null" {
		writeErr(w, 400, "definition is required")
		return
	}
	d, err := catalog.ParseDefinition(in.Definition)
	if err != nil {
		writeValidation(w, err)
		return
	}
	in.applyMeta(d)
	if in.Key != nil {
		d.Key = strings.TrimSpace(*in.Key)
	}
	if d.Key == "" && d.Name != "" {
		d.Key = sanitizeName(d.Name)
	}
	if d.Key == "" {
		writeErr(w, 400, "key or name is required")
		return
	}
	if _, builtin := catalog.Templates[d.Key]; builtin {
		writeErr(w, 409, "template key is used by a built-in template: "+d.Key)
		return
	}
	vis := store.TemplatePrivate
	if in.Visibility != nil {
		vis = *in.Visibility
	}
	if !validVisibility(vis) {
		writeErr(w, 400, "visibility must be private, workspace or public")
		return
	}
	def, ok := writeTemplateDef(w, d)
	if !ok {
		return
	}
	wsID, ok := s.createTargetWorkspace(w, r, in.WorkspaceID)
	if !ok {
		return
	}
	t := &store.UserTemplate{Key: d.Key, Visibility: vis, WorkspaceID: wsID}
	if p := principalFrom(r); p != nil {
		t.OwnerUserID = p.User.ID
	}
	storedFrom(t, d, def)
	if err := s.Store.CreateTemplate(ctx, t); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeErr(w, 409, "template already exists: "+t.Key)
			return
		}
		s.storeErr(w, err)
		return
	}
	s.audit(r, "template.created", t.Key, "템플릿 "+t.Name+"("+t.Key+") 등록")
	writeJSON(w, 201, templateDetail{templateView: s.userView(r, t, d), Readme: t.Readme, Definition: t.Definition})
}

// loadEditableTemplate은 고칠 수 있는 사용자 템플릿을 찾는다(내장은 400, 보이지 않으면 404, 권한이 없으면 403).
func (s *Server) loadEditableTemplate(w http.ResponseWriter, r *http.Request) (*store.UserTemplate, bool) {
	key := r.PathValue("key")
	if _, ok := catalog.Templates[key]; ok {
		writeErr(w, 400, "built-in templates cannot be changed")
		return nil, false
	}
	t, err := s.Store.GetTemplateByKey(r.Context(), key)
	if err != nil || !s.canSeeTemplate(r, t) {
		if err == nil || errors.Is(err, store.ErrNotFound) {
			writeErr(w, 404, "template not found")
		} else {
			s.storeErr(w, err)
		}
		return nil, false
	}
	if !s.canEditTemplate(r, t) {
		writeErr(w, 403, "템플릿을 만든 사람이나 작업 공간 admin만 바꿀 수 있습니다")
		return nil, false
	}
	return t, true
}

func (s *Server) patchTemplate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	t, ok := s.loadEditableTemplate(w, r)
	if !ok {
		return
	}
	var in templateInput
	if !decode(w, r, &in) {
		return
	}
	if in.Key != nil && strings.TrimSpace(*in.Key) != t.Key {
		writeErr(w, 400, "key cannot be changed")
		return
	}
	d, err := parseStored(t)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if len(in.Definition) > 0 && string(in.Definition) != "null" {
		nd, err := catalog.ParseDefinition(in.Definition)
		if err != nil {
			writeValidation(w, err)
			return
		}
		// 새 정의에 메타데이터가 없으면 지금 값을 둔다.
		if nd.Name == "" {
			nd.Name = d.Name
		}
		if nd.Description == "" {
			nd.Description = d.Description
		}
		if nd.Readme == "" {
			nd.Readme = d.Readme
		}
		if nd.Icon == "" {
			nd.Icon = d.Icon
		}
		if nd.Category == "" {
			nd.Category = d.Category
		}
		if nd.Tags == nil {
			nd.Tags = d.Tags
		}
		d = nd
	}
	in.applyMeta(d)
	d.Key = t.Key
	if in.Visibility != nil {
		if !validVisibility(*in.Visibility) {
			writeErr(w, 400, "visibility must be private, workspace or public")
			return
		}
		t.Visibility = *in.Visibility
	}
	def, ok := writeTemplateDef(w, d)
	if !ok {
		return
	}
	storedFrom(t, d, def)
	if err := s.Store.UpdateTemplate(ctx, t); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "template.updated", t.Key, "템플릿 "+t.Name+"("+t.Key+") 변경")
	writeJSON(w, 200, templateDetail{templateView: s.userView(r, t, d), Readme: t.Readme, Definition: t.Definition})
}

func (s *Server) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadEditableTemplate(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteTemplate(r.Context(), t.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "template.deleted", t.Key, "템플릿 "+t.Name+"("+t.Key+") 삭제")
	w.WriteHeader(204)
}

// ---- 배포 ----

type deployTemplateInput struct {
	ProjectID     string            `json:"project_id"`
	EnvironmentID string            `json:"environment_id"`
	Inputs        map[string]string `json:"inputs"`
}

// deployTemplate은 템플릿의 서비스를 만들고(이름이 겹치면 바꾸고 참조도 따라 바꾼다) 지정한 환경에 배포한다.
// POST /api/projects/{pid}/templates/{key} {environment_id, inputs}
func (s *Server) deployTemplate(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.Context(), r.PathValue("pid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var in deployTemplateInput
	if !decode(w, r, &in) {
		return
	}
	s.runTemplateDeploy(w, r, p, in.EnvironmentID, r.PathValue("key"), in.Inputs)
}

// deployTemplateByKey: POST /api/templates/{key}/deploy {project_id, environment_id, inputs}.
// 라우트는 작업 공간 member 이상이고, 대상 프로젝트(환경)의 member 권한은 여기서 검사한다.
func (s *Server) deployTemplateByKey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in deployTemplateInput
	if !decode(w, r, &in) {
		return
	}
	if in.ProjectID == "" {
		writeErr(w, 400, "project_id is required")
		return
	}
	p, err := s.Store.GetProject(ctx, in.ProjectID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if pr := principalFrom(r); pr != nil {
		eid := in.EnvironmentID
		if eid == "" {
			eid = p.DefaultEnvironmentID
		}
		lv, err := s.projectLevel(ctx, pr, p.ID, eid)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		if lv == lvNone {
			writeErr(w, 404, "not found")
			return
		}
		if lv < lvMember {
			writeErr(w, 403, "권한이 없습니다")
			return
		}
	}
	s.runTemplateDeploy(w, r, p, in.EnvironmentID, r.PathValue("key"), in.Inputs)
}

func (s *Server) runTemplateDeploy(w http.ResponseWriter, r *http.Request, p *store.Project, envID, key string, inputs map[string]string) {
	ctx := r.Context()
	f, err := s.findTemplate(r, key)
	if err != nil {
		s.templateErr(w, err)
		return
	}
	if f.def.IsHACompanion() {
		writeErr(w, 400, "고가용성(HA) 동반 템플릿은 단독으로 배포할 수 없습니다. 데이터베이스의 데이터 → 설정 → 고가용성에서 전환하세요")
		return
	}
	if envID == "" {
		envID = p.DefaultEnvironmentID
	}
	env, err := s.Store.GetEnvironment(ctx, envID)
	if err != nil || env.ProjectID != p.ID {
		writeErr(w, 400, "environment_id must be an environment of this project")
		return
	}
	defs, primary, err := f.def.Instantiate(inputs)
	if err != nil {
		var ie *catalog.InputError
		if errors.As(err, &ie) {
			writeJSON(w, 400, map[string]any{"error": ie.Error(), "missing": nonNil(ie.Missing), "invalid": nonNil(ie.Invalid), "unknown": nonNil(ie.Unknown)})
			return
		}
		s.storeErr(w, err)
		return
	}
	// 서비스를 만들기 전에 설정을 모두 검사한다(일부만 만들어지지 않게).
	for _, d := range defs {
		probe := &store.Instance{}
		probe.Spec.Defaults()
		d.ApplyToInstance(probe)
		if err := s.checkSourcePolicy(ctx, guardrails.Source{Type: d.SourceType, DatabaseKind: d.DatabaseKind, RepoURL: probe.RepoURL, Image: probe.Image}); err != nil {
			if errors.Is(err, store.ErrDeploymentSourceRestricted) {
				writeErr(w, 403, d.Name+": "+err.Error())
			} else {
				s.storeErr(w, err)
			}
			return
		}
		if err := s.validateSpec(&store.Service{SourceType: d.SourceType}, &probe.Spec); err != nil {
			writeErr(w, 400, d.Name+": "+err.Error())
			return
		}
	}
	// 실제 이름을 먼저 정한다(이미 있는 서비스와 겹치면 -2 등).
	rename := map[string]string{}
	reserved := map[string]bool{}
	for i := range defs {
		name, err := s.uniqueServiceName(ctx, p.ID, defs[i].Name)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		for n := 2; reserved[name]; n++ {
			name = sanitizeName(defs[i].Name + "-" + itoa(n))
		}
		reserved[name] = true
		rename[defs[i].Name] = name
	}
	for i := range defs {
		nv := map[string]string{}
		for k, v := range defs[i].Variables {
			for from, to := range rename {
				if from != to {
					v = vars.RenameService(v, from, to)
				}
			}
			nv[k] = v
		}
		defs[i].Variables = nv
		defs[i].Name = rename[defs[i].Name]
	}
	tkey := f.key
	var created []*store.Service
	for _, d := range defs {
		svc, err := s.createServiceFromDef(ctx, p, d, nil, "main", &tkey)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		created = append(created, svc)
	}
	// 대표 서비스(primary)를 맨 앞에 둔다.
	created[0], created[primary] = created[primary], created[0]
	for _, svc := range created {
		inst, err := s.Store.GetInstanceFor(context.WithoutCancel(ctx), svc.ID, env.ID)
		if err != nil {
			continue
		}
		if _, err := s.Deploy.Enqueue(ctx, inst, deploy.EnqueueOpts{Trigger: "template"}); err != nil {
			s.Log.Warn("template deploy", "service", svc.Name, "err", err)
		}
	}
	if err := s.Store.CountTemplateDeploy(ctx, f.key, f.builtin != nil); err != nil {
		s.Log.Warn("template deploy count", "err", err)
	}
	s.Events.Emit(ctx, p.ID, &env.ID, nil, "template.deployed", "템플릿 "+f.name()+"을(를) "+env.Name+" 환경에 배포합니다")
	writeJSON(w, 201, created)
}

func nonNil(ss []string) []string {
	if ss == nil {
		return []string{}
	}
	return ss
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	b := []byte{}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
