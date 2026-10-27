// Package store는 aven의 영속 상태(SQLite)를 다룬다.
// 모델: Project ─ Environment ─ Service ─ ServiceInstance(Service×Environment) ─ Deployment.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/turin-dev/aven/internal/secret"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type Store struct {
	db  *sql.DB
	q   querier // db 또는 진행 중인 tx
	box **secret.Box
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, q: db, box: new(*secret.Box)}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func NewID(prefix string) string {
	b := make([]byte, 8)
	rand.Read(b)
	return prefix + "_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

// RandomSecret은 비밀번호 등으로 쓸 영숫자 문자열을 만든다.
func RandomSecret(n int) string {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// 저장 형식은 고정 폭이라 문자열 정렬이 시간 정렬과 같다.
const tsFormat = "2006-01-02T15:04:05.000000000Z"

func ts(t time.Time) string { return t.UTC().Format(tsFormat) }

func nullTS(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}

func parseTS(s string) time.Time {
	t, err := time.Parse(tsFormat, s)
	if err != nil {
		t, _ = time.Parse(time.RFC3339Nano, s)
	}
	return t
}

func parseNullTS(s sql.NullString) *time.Time {
	if !s.Valid {
		return nil
	}
	t := parseTS(s.String)
	return &t
}

func nullStr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}

func jsonStr(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

type scanner interface{ Scan(...any) error }

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func affected(res sql.Result, err error) error {
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			return ErrConflict
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func conflict(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint") {
		return ErrConflict
	}
	return err
}

// Tx는 여러 쓰기를 한 트랜잭션으로 묶는다. f 안에서는 인자로 받은 tx Store만 써야 한다(커넥션이 하나라 바깥 Store를 쓰면 막힌다).
func (s *Store) Tx(ctx context.Context, f func(tx *Store) error) error {
	if _, ok := s.q.(*sql.Tx); ok {
		return f(s)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := f(&Store{db: s.db, q: tx, box: s.box}); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ---------------- Projects ----------------

type Project struct {
	ID                   string    `json:"id"`
	Name                 string    `json:"name"`
	Description          string    `json:"description"`
	CreatedAt            time.Time `json:"created_at"`
	DefaultEnvironmentID string    `json:"default_environment_id"`
	NotifyWebhookURL     *string   `json:"notify_webhook_url"`
	NotifyEvents         []string  `json:"notify_events"`
	// PREnvironments는 GitHub PR마다 미리보기 환경을 만드는 설정이다.
	PREnvironments PRSettings `json:"pr_environments"`
	// PRFromForks가 true면 fork 저장소에서 온 PR도 미리보기 환경을 만든다.
	PRFromForks bool `json:"pr_from_forks"`
	// PRFromBots가 true면 GitHub 봇이 연 PR도 미리보기 환경을 만든다.
	PRFromBots bool `json:"pr_from_bots"`
	// UsageAlert는 달력 월 기준 사용량 알림 기준이다(nil이면 없음). 80%와 100%에서 한 번씩 usage.threshold_reached를 낸다.
	UsageAlert *UsageAlert `json:"usage_alert"`
	// WorkspaceID는 프로젝트가 속한 작업 공간이다(비우고 만들면 기본 작업 공간).
	WorkspaceID string `json:"workspace_id"`
	// GitHubUserID는 이 프로젝트의 GitHub 저장소 접근(클론, 커밋 상태, PR 파일)에 쓰는 사용자 연결이다(nil이면 KEEL_GITHUB_TOKEN만).
	GitHubUserID *string `json:"github_user_id"`
}

// UsageAlert는 프로젝트의 월간 사용량 알림 기준이다. 비운 항목은 보지 않는다.
type UsageAlert struct {
	CPUCoreHours  *float64 `json:"cpu_core_hours,omitempty"`
	MemoryGBHours *float64 `json:"memory_gb_hours,omitempty"`
	EgressGB      *float64 `json:"egress_gb,omitempty"`
}

// Empty는 기준이 하나도 없는지다.
func (u *UsageAlert) Empty() bool {
	return u == nil || (u.CPUCoreHours == nil && u.MemoryGBHours == nil && u.EgressGB == nil)
}

// PRSettings는 프로젝트의 PR 미리보기 환경 설정이다.
type PRSettings struct {
	Enabled           bool    `json:"enabled"`
	BaseEnvironmentID *string `json:"base_environment_id"` // nil이면 프로젝트 기본 환경
	AutoDelete        bool    `json:"auto_delete"`         // PR이 닫히면 환경을 지운다(기본 true)
	Domain            bool    `json:"domain"`              // 도메인이 있던 서비스에 생성 도메인을 붙인다(기본 true)
	Focused           bool    `json:"focused"`             // PR 파일에 영향받은 서비스와 그 의존 서비스만 배포한다
}

// prStored는 projects.pr_settings 저장 형식이다. 빈 값('{}')이면 기본값(auto_delete, domain = true)이 된다.
type prStored struct {
	Enabled    bool    `json:"enabled"`
	Base       *string `json:"base_environment_id"`
	AutoDelete *bool   `json:"auto_delete"`
	Domain     *bool   `json:"domain"`
	FromForks  bool    `json:"from_forks"`
	FromBots   bool    `json:"from_bots"`
	Focused    bool    `json:"focused"`
}

func encodePR(p *Project) string {
	ad, dm := p.PREnvironments.AutoDelete, p.PREnvironments.Domain
	return jsonStr(prStored{Enabled: p.PREnvironments.Enabled, Base: p.PREnvironments.BaseEnvironmentID, AutoDelete: &ad, Domain: &dm,
		FromForks: p.PRFromForks, FromBots: p.PRFromBots, Focused: p.PREnvironments.Focused})
}

func decodePR(raw string, p *Project) {
	var st prStored
	json.Unmarshal([]byte(raw), &st)
	p.PREnvironments = PRSettings{Enabled: st.Enabled, BaseEnvironmentID: st.Base, AutoDelete: st.AutoDelete == nil || *st.AutoDelete, Domain: st.Domain == nil || *st.Domain, Focused: st.Focused}
	p.PRFromForks = st.FromForks
	p.PRFromBots = st.FromBots
}

// projectCols는 INSERT에도 쓴다(v1 이전 중에는 pr_settings 컬럼이 아직 없어서 따로 둔다).
const projectCols = `id, name, description, default_environment_id, notify_webhook_url, notify_events, created_at`
const projectSelect = projectCols + `, pr_settings, usage_alert, workspace_id, github_user_id`

func scanProject(r scanner) (*Project, error) {
	var p Project
	var hook, ghUser sql.NullString
	var events, created, pr, ua string
	if err := r.Scan(&p.ID, &p.Name, &p.Description, &p.DefaultEnvironmentID, &hook, &events, &created, &pr, &ua, &p.WorkspaceID, &ghUser); err != nil {
		return nil, notFound(err)
	}
	decodePR(pr, &p)
	if ua != "" {
		var u UsageAlert
		if json.Unmarshal([]byte(ua), &u) == nil && !u.Empty() {
			p.UsageAlert = &u
		}
	}
	p.NotifyWebhookURL, p.GitHubUserID = nullStr(hook), nullStr(ghUser)
	json.Unmarshal([]byte(events), &p.NotifyEvents)
	if p.NotifyEvents == nil {
		p.NotifyEvents = []string{}
	}
	p.CreatedAt = parseTS(created)
	return &p, nil
}

func (s *Store) ListProjects(ctx context.Context) ([]*Project, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+projectSelect+` FROM projects ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetProject(ctx context.Context, id string) (*Project, error) {
	return scanProject(s.q.QueryRowContext(ctx, `SELECT `+projectSelect+` FROM projects WHERE id=?`, id))
}

// CreateProject는 프로젝트와 production 환경을 함께 만든다.
func (s *Store) CreateProject(ctx context.Context, p *Project) (*Environment, error) {
	p.ID = NewID("prj")
	p.CreatedAt = time.Now().UTC()
	if p.NotifyEvents == nil {
		p.NotifyEvents = []string{"deploy.failed", "deploy.crashed", "volume.backup.failed"}
	}
	if p.PREnvironments == (PRSettings{}) {
		p.PREnvironments.AutoDelete, p.PREnvironments.Domain = true, true
	}
	env := &Environment{ID: NewID("env"), ProjectID: p.ID, Name: "production", Kind: EnvStandard, CreatedAt: p.CreatedAt}
	p.DefaultEnvironmentID = env.ID
	err := s.Tx(ctx, func(tx *Store) error {
		// 작업 공간을 정하지 않으면 예전 컬럼만 쓴다(작업 공간 컬럼이 생기기 전의 마이그레이션도 이 함수를 부른다).
		var err error
		if p.WorkspaceID == "" && p.GitHubUserID == nil {
			_, err = tx.q.ExecContext(ctx, `INSERT INTO projects (`+projectCols+`) VALUES (?,?,?,?,?,?,?)`,
				p.ID, p.Name, p.Description, p.DefaultEnvironmentID, p.NotifyWebhookURL, jsonStr(p.NotifyEvents), ts(p.CreatedAt))
		} else {
			if p.WorkspaceID == "" {
				p.WorkspaceID = DefaultWorkspaceID
			}
			_, err = tx.q.ExecContext(ctx, `INSERT INTO projects (`+projectCols+`, workspace_id, github_user_id) VALUES (?,?,?,?,?,?,?,?,?)`,
				p.ID, p.Name, p.Description, p.DefaultEnvironmentID, p.NotifyWebhookURL, jsonStr(p.NotifyEvents), ts(p.CreatedAt), p.WorkspaceID, p.GitHubUserID)
		}
		if err != nil {
			return conflict(err)
		}
		_, err = tx.q.ExecContext(ctx, `INSERT INTO environments (id, project_id, name, created_at) VALUES (?,?,?,?)`,
			env.ID, env.ProjectID, env.Name, ts(env.CreatedAt))
		return err
	})
	if err != nil {
		return nil, err
	}
	return env, nil
}

func (s *Store) UpdateProject(ctx context.Context, p *Project) error {
	ua := ""
	if !p.UsageAlert.Empty() {
		ua = jsonStr(p.UsageAlert)
	}
	return affected(s.q.ExecContext(ctx, `UPDATE projects SET name=?, description=?, default_environment_id=?, notify_webhook_url=?, notify_events=?, pr_settings=?, usage_alert=? WHERE id=?`,
		p.Name, p.Description, p.DefaultEnvironmentID, p.NotifyWebhookURL, jsonStr(p.NotifyEvents), encodePR(p), ua, p.ID))
}

func (s *Store) DeleteProject(ctx context.Context, id string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM projects WHERE id=?`, id))
}

// ---------------- Environments ----------------

const (
	EnvStandard = "standard"
	EnvPreview  = "preview" // GitHub PR 미리보기 환경
)

type Environment struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	// Kind는 standard | preview다. preview 환경은 PR 하나에 묶인다(아래 PR 필드, standard면 null).
	Kind     string  `json:"kind"`
	PRNumber *int    `json:"pr_number"`
	PRURL    *string `json:"pr_url"`
	PRBranch *string `json:"pr_branch"`
	// PRRepo는 PR base 저장소의 정규화된 주소다(github.com/owner/repo). webhook에서 환경을 찾을 때 쓴다.
	PRRepo *string `json:"-"`
	// BaseEnvironmentID는 PR 환경에서 배포하지 않은 서비스의 변수 참조를 제공하는 원본 환경이다.
	BaseEnvironmentID *string `json:"-"`
	// Restricted면(Environment RBAC) 프로젝트 admin만 이 환경의 리소스에 접근한다(api/access_controls.go).
	Restricted bool `json:"restricted"`
}

const envCols = `id, project_id, name, created_at, kind, pr_number, pr_url, pr_branch, pr_repo, base_environment_id, restricted`

func scanEnv(r scanner) (*Environment, error) {
	var e Environment
	var created string
	var num sql.NullInt64
	var restricted int
	var url, branch, repo, base sql.NullString
	if err := r.Scan(&e.ID, &e.ProjectID, &e.Name, &created, &e.Kind, &num, &url, &branch, &repo, &base, &restricted); err != nil {
		return nil, notFound(err)
	}
	e.CreatedAt = parseTS(created)
	if num.Valid {
		n := int(num.Int64)
		e.PRNumber = &n
	}
	e.PRURL, e.PRBranch, e.PRRepo, e.BaseEnvironmentID = nullStr(url), nullStr(branch), nullStr(repo), nullStr(base)
	e.Restricted = restricted != 0
	if e.Kind == "" {
		e.Kind = EnvStandard
	}
	return &e, nil
}

func (s *Store) GetEnvironment(ctx context.Context, id string) (*Environment, error) {
	return scanEnv(s.q.QueryRowContext(ctx, `SELECT `+envCols+` FROM environments WHERE id=?`, id))
}

func (s *Store) queryEnvs(ctx context.Context, where string, args ...any) ([]*Environment, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+envCols+` FROM environments `+where+` ORDER BY created_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Environment{}
	for rows.Next() {
		e, err := scanEnv(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) ListEnvironments(ctx context.Context, projectID string) ([]*Environment, error) {
	if projectID != "" {
		return s.queryEnvs(ctx, `WHERE project_id=?`, projectID)
	}
	return s.queryEnvs(ctx, ``)
}

// ListPreviewEnvironments는 저장소(정규화된 주소)의 PR 번호에 묶인 미리보기 환경이다(프로젝트마다 하나씩일 수 있다).
func (s *Store) ListPreviewEnvironments(ctx context.Context, repo string, number int) ([]*Environment, error) {
	return s.queryEnvs(ctx, `WHERE kind='preview' AND pr_repo=? AND pr_number=?`, repo, number)
}

func (s *Store) CreateEnvironment(ctx context.Context, e *Environment) error {
	e.ID = NewID("env")
	e.CreatedAt = time.Now().UTC()
	if e.Kind == "" {
		e.Kind = EnvStandard
	}
	_, err := s.q.ExecContext(ctx, `INSERT INTO environments (`+envCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.ProjectID, e.Name, ts(e.CreatedAt), e.Kind, e.PRNumber, e.PRURL, e.PRBranch, e.PRRepo, e.BaseEnvironmentID, e.Restricted)
	return conflict(err)
}

// SetEnvironmentPR는 미리보기 환경의 PR 정보(브랜치/URL)를 갱신한다.
func (s *Store) SetEnvironmentPR(ctx context.Context, id string, url, branch *string) error {
	return affected(s.q.ExecContext(ctx, `UPDATE environments SET pr_url=?, pr_branch=? WHERE id=?`, url, branch, id))
}

func (s *Store) SetEnvironmentBase(ctx context.Context, id string, baseID *string) error {
	return affected(s.q.ExecContext(ctx, `UPDATE environments SET base_environment_id=? WHERE id=?`, baseID, id))
}

func (s *Store) RenameEnvironment(ctx context.Context, id, name string) error {
	return affected(s.q.ExecContext(ctx, `UPDATE environments SET name=? WHERE id=?`, name, id))
}

func (s *Store) DeleteEnvironment(ctx context.Context, id string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM environments WHERE id=?`, id))
}

// ---------------- Services ----------------

type Service struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"project_id"`
	Name         string    `json:"name"`
	Icon         *string   `json:"icon"`
	SourceType   string    `json:"source_type"`
	DatabaseKind *string   `json:"database_kind"`
	Template     *string   `json:"template"`
	CreatedAt    time.Time `json:"created_at"`
	// StackID/ComposeName: compose 스택에서 만든 서비스면 스택 id와 compose 서비스 이름이다.
	StackID     *string `json:"stack_id"`
	ComposeName *string `json:"compose_name"`
}

const serviceCols = `id, project_id, name, icon, source_type, database_kind, template, created_at, stack_id, compose_name`

func scanService(r scanner) (*Service, error) {
	var sv Service
	var icon, kind, tmpl, stack, cname sql.NullString
	var created string
	if err := r.Scan(&sv.ID, &sv.ProjectID, &sv.Name, &icon, &sv.SourceType, &kind, &tmpl, &created, &stack, &cname); err != nil {
		return nil, notFound(err)
	}
	sv.Icon, sv.DatabaseKind, sv.Template = nullStr(icon), nullStr(kind), nullStr(tmpl)
	sv.StackID, sv.ComposeName = nullStr(stack), nullStr(cname)
	sv.CreatedAt = parseTS(created)
	return &sv, nil
}

func (s *Store) GetService(ctx context.Context, id string) (*Service, error) {
	return scanService(s.q.QueryRowContext(ctx, `SELECT `+serviceCols+` FROM services WHERE id=?`, id))
}

func (s *Store) ListServices(ctx context.Context, projectID string) ([]*Service, error) {
	q := `SELECT ` + serviceCols + ` FROM services`
	args := []any{}
	if projectID != "" {
		q += ` WHERE project_id=?`
		args = append(args, projectID)
	}
	rows, err := s.q.QueryContext(ctx, q+` ORDER BY created_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Service{}
	for rows.Next() {
		sv, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sv)
	}
	return out, rows.Err()
}

func (s *Store) CreateService(ctx context.Context, sv *Service) error {
	if sv.ID == "" {
		sv.ID = NewID("svc")
	}
	sv.CreatedAt = time.Now().UTC()
	if !s.tableExists(ctx, "stacks") { // v0→v1 이전 중에는 stack 컬럼이 아직 없다
		_, err := s.q.ExecContext(ctx, `INSERT INTO services (id, project_id, name, icon, source_type, database_kind, template, created_at) VALUES (?,?,?,?,?,?,?,?)`,
			sv.ID, sv.ProjectID, sv.Name, sv.Icon, sv.SourceType, sv.DatabaseKind, sv.Template, ts(sv.CreatedAt))
		return conflict(err)
	}
	_, err := s.q.ExecContext(ctx, `INSERT INTO services (`+serviceCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		sv.ID, sv.ProjectID, sv.Name, sv.Icon, sv.SourceType, sv.DatabaseKind, sv.Template, ts(sv.CreatedAt), sv.StackID, sv.ComposeName)
	return conflict(err)
}

func (s *Store) UpdateService(ctx context.Context, sv *Service) error {
	return affected(s.q.ExecContext(ctx, `UPDATE services SET name=?, icon=? WHERE id=?`, sv.Name, sv.Icon, sv.ID))
}

func (s *Store) DeleteService(ctx context.Context, id string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM services WHERE id=?`, id))
}

// ---------------- Instances ----------------

type Domain struct {
	Host       string `json:"host"`
	Kind       string `json:"kind"` // generated | custom
	TargetPort *int   `json:"target_port"`
	// Paths는 이 호스트의 경로 규칙이다. 규칙에 걸리지 않은 요청은 이 서비스로 간다.
	Paths []PathRule `json:"paths,omitempty"`
	// TLS는 도메인별 TLS 모드다: "" (전역 기본값) | auto | internal | off.
	TLS string `json:"tls,omitempty"`
	// TrustForwardedProto는 앞단 프록시(cloudflared 등)가 보낸 X-Forwarded-Proto를 upstream에 그대로 전달한다.
	TrustForwardedProto bool `json:"trust_forwarded_proto,omitempty"`
	// Redirect가 있으면 이 호스트는 upstream 대신 to로 리다이렉트한다.
	Redirect *Redirect `json:"redirect,omitempty"`
	// BasicAuth는 호스트 전체에 HTTP basic auth를 건다. 비밀번호 해시는 설정 테이블(DomainAuthKey)에만 저장한다.
	BasicAuth *BasicAuth `json:"basic_auth,omitempty"`
	// Upstream이 있으면 이 호스트는 서비스의 컨테이너 대신 aven 밖의 upstream(host:port, 예: 다른 compose의 컨테이너)으로 간다.
	// 그 노드의 허용 목록(KEEL_EXTERNAL_UPSTREAMS)에 있어야 하고, 작업 공간 admin만 정할 수 있다. 배포 상태와 무관하게 라우팅한다.
	Upstream string `json:"upstream,omitempty"`
	// UpstreamPaths가 있으면 그 경로 접두어("/api/v1")만 upstream으로 보내고 나머지는 404다.
	UpstreamPaths []string `json:"upstream_paths,omitempty"`
}

// PathRule은 호스트 안의 경로 하나를 어떤 서비스로 보낼지 정한다.
// Path가 "/*"로 끝나면 접두어 규칙(/api/*는 /api와 /api/…), 아니면 정확히 일치한다.
type PathRule struct {
	Path        string `json:"path"`
	Service     string `json:"service,omitempty"` // 같은 환경의 서비스 이름. 비면 이 서비스
	TargetPort  *int   `json:"target_port,omitempty"`
	StripPrefix bool   `json:"strip_prefix,omitempty"`
}

type Redirect struct {
	To        string `json:"to"` // 호스트 이름 또는 http(s) URL
	Permanent bool   `json:"permanent"`
}

type BasicAuth struct {
	Username    string `json:"username"`
	HasPassword bool   `json:"has_password"`
	// Password는 요청에서만 쓴다(저장·응답에는 없다).
	Password string `json:"password,omitempty"`
}

// DomainAuthKey는 basic auth bcrypt 해시를 저장하는 설정 키다.
func DomainAuthKey(instanceID, host string) string { return "domain_auth:" + instanceID + ":" + host }

type TCPProxy struct {
	ContainerPort int `json:"container_port"`
	PublicPort    int `json:"public_port"`
}

type Canvas struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Spec은 환경별 서비스 설정이다. 배포마다 스냅샷을 남긴다.
type Spec struct {
	RepoURL             *string   `json:"repo_url"`
	Branch              string    `json:"branch"`
	RootDir             string    `json:"root_dir"`
	Image               *string   `json:"image"`
	Builder             string    `json:"builder"`
	DockerfilePath      string    `json:"dockerfile_path"`
	WatchPaths          []string  `json:"watch_paths"`
	AutoDeploy          bool      `json:"auto_deploy"`
	StartCommand        *string   `json:"start_command"`
	PreDeployCommand    *string   `json:"pre_deploy_command"`
	Port                *int      `json:"port"`
	HealthcheckType     string    `json:"healthcheck_type"`
	HealthcheckPath     string    `json:"healthcheck_path"`
	HealthcheckTimeoutS int       `json:"healthcheck_timeout_s"`
	RestartPolicy       string    `json:"restart_policy"`
	RestartMaxRetries   int       `json:"restart_max_retries"`
	Replicas            int       `json:"replicas"`
	CronSchedule        *string   `json:"cron_schedule"`
	SleepWhenIdle       bool      `json:"sleep_when_idle"`
	SleepAfterS         int       `json:"sleep_after_s"`
	Domains             []Domain  `json:"domains"`
	TCPProxy            *TCPProxy `json:"tcp_proxy"`
	// 자원 제한(docker --memory / --cpus). nil이면 제한 없음.
	MemoryLimitMB *int     `json:"memory_limit_mb,omitempty"`
	CPULimit      *float64 `json:"cpu_limit,omitempty"`
	// BuildSecrets는 빌드 비밀로 다룰 변수 이름이다. BuildKit --secret으로만 넘기고 build arg로는 넘기지 않는다.
	BuildSecrets []string `json:"build_secrets,omitempty"`
	// SkipBuilds면 repo 배포에서 같은 빌드 입력의 보존된 이미지를 재사용한다.
	SkipBuilds bool `json:"skip_builds,omitempty"`
	// BuildNode가 BuildNodeLocal이면 원격 노드에 배포하는 저장소/업로드 서비스도 aven-server 노드에서 빌드하고
	// 이미지를 대상 노드로 보낸다(대상 노드의 디스크·CPU를 아낀다). 비우면 대상 노드에서 빌드한다.
	BuildNode string `json:"build_node,omitempty"`
	// Compose는 compose 스택에서 가져온 서비스의 추가 런타임 설정이다(일반 서비스는 nil).
	Compose *ComposeSpec `json:"compose,omitempty"`
	// WaitForCI가 true면 GitHub push로 만든 배포를 그 커밋의 CI(check suite/workflow run)가 성공할 때까지 waiting으로 둔다.
	WaitForCI bool `json:"wait_for_ci"`
	// OverlapSeconds는 새 배포가 live가 된 뒤 이전 컨테이너를 지우기 전까지 더 남겨 두는 시간이다(0..600).
	OverlapSeconds int `json:"overlap_seconds"`
	// DrainingSeconds는 이전 컨테이너를 멈출 때 SIGTERM 후 SIGKILL까지 기다리는 시간이다(docker stop -t, 0..300).
	// nil이면 기본값: 일반 전환·중지·재시작 10초, 볼륨 서비스의 recreate 전환 30초.
	DrainingSeconds *int `json:"draining_seconds"`
	// PrivatePorts는 다른 노드에 mesh로 내보낼 포트다(비우면 [PORT]). 같은 노드 안에서는 모든 포트가 그대로 열려 있다. 바로 반영된다.
	PrivatePorts []int `json:"private_ports,omitempty"`
	// PrivateDNSName은 사설 네트워크에서 쓸 추가 이름(<dns>, <dns>.<env>.keel.internal)이다. 비우면 서비스 이름만 쓴다.
	// 서비스 이름 alias는 항상 남는다. 다음 배포 때 반영된다(pending_changes에 보인다).
	PrivateDNSName string `json:"private_dns_name,omitempty"`
	// OutboundIPv6이 false면 이 서비스의 컨테이너는 IPv6로 fc00::/7(ULA, 환경/mesh) 밖에 새로 연결할 수 없다
	// (노드의 ip6tables DOCKER-USER 규칙). nil이면 켜짐(기본값). 재배포 없이 바로 반영된다(pending_changes에 넣지 않는다).
	OutboundIPv6 *bool `json:"outbound_ipv6,omitempty"`
	// BuildCommand와 RequiredMountPath는 config-as-code(build.buildCommand, deploy.requiredMountPath)로만 정해진다(배포 스냅샷에만 있다).
	BuildCommand      *string `json:"build_command,omitempty"`
	RequiredMountPath string  `json:"required_mount_path,omitempty"`
	// Autoscale은 수평 자동 확장 설정이다(nil이면 꺼짐). 재배포 없이 바로 반영된다(pending_changes에 넣지 않는다).
	Autoscale *Autoscale `json:"autoscale,omitempty"`
	// Placement는 여러 노드에 replica를 나눠 띄우는 배치다(비우면 node_id 하나에 replicas개).
	// 첫 항목이 주 노드(node_id)다. 총 replica 수는 합계이고, 볼륨이 붙은 서비스에는 쓸 수 없다.
	Placement []Placement `json:"placement,omitempty"`
	// 이미지 자동 업데이트(image 서비스). 바로 반영된다(pending_changes에 넣지 않는다).
	// policy: disabled(기본, "") | patch | minor, window: anytime | weekends | night | custom(UTC, days 0=일요일, end hour는 제외).
	ImageUpdatePolicy    string `json:"image_update_policy,omitempty"`
	ImageUpdateWindow    string `json:"image_update_window,omitempty"`
	ImageUpdateDays      []int  `json:"image_update_days,omitempty"`
	ImageUpdateStartHour int    `json:"image_update_start_hour,omitempty"`
	ImageUpdateEndHour   int    `json:"image_update_end_hour,omitempty"`
}

// BuildNodeLocal은 Spec.BuildNode의 값으로, aven-server가 도는 로컬 노드에서 빌드한다는 뜻이다.
const BuildNodeLocal = "local"

// Placement는 노드 하나와 그 노드에 띄울 replica 수다.
type Placement struct {
	NodeID   string `json:"node_id"`
	Replicas int    `json:"replicas"`
}

// Autoscale은 Kubernetes HPA 방식의 수평 자동 확장 설정이다. target이 하나 이상 있어야 한다.
type Autoscale struct {
	Enabled             bool     `json:"enabled"`
	MinReplicas         int      `json:"min_replicas"`
	MaxReplicas         int      `json:"max_replicas"`
	TargetCPUPercent    *float64 `json:"target_cpu_percent"`
	TargetMemoryPercent *float64 `json:"target_memory_percent"`
	TargetRPSPerReplica *float64 `json:"target_rps_per_replica"`
	ScaleUpCooldownS    int      `json:"scale_up_cooldown_s"`
	ScaleDownCooldownS  int      `json:"scale_down_cooldown_s"`
}

// AutoscaleDefaults는 비어 있는 cooldown에 기본값(올림 60초, 내림 300초)을 채운다.
func (a *Autoscale) Defaults() {
	if a == nil {
		return
	}
	if a.ScaleUpCooldownS <= 0 {
		a.ScaleUpCooldownS = 60
	}
	if a.ScaleDownCooldownS <= 0 {
		a.ScaleDownCooldownS = 300
	}
}

// PlacementTotal은 placement의 replica 합계다(placement가 없으면 0).
func PlacementTotal(ps []Placement) int {
	n := 0
	for _, p := range ps {
		n += p.Replicas
	}
	return n
}

// MultiNode는 placement가 둘 이상의 노드에 걸치는지다.
func (sp *Spec) MultiNode() bool { return len(sp.Placement) > 1 }

// DrainGrace는 컨테이너를 멈출 때 쓸 docker stop -t 값이다. draining_seconds가 없으면 def.
func (sp *Spec) DrainGrace(def int) int {
	if sp.DrainingSeconds != nil && *sp.DrainingSeconds >= 0 {
		return *sp.DrainingSeconds
	}
	return def
}

// Defaults는 비어 있는 설정에 기본값을 채운다.
func (sp *Spec) Defaults() {
	if sp.Branch == "" {
		sp.Branch = "main"
	}
	if sp.RootDir == "" {
		sp.RootDir = "/"
	}
	if sp.Builder == "" {
		sp.Builder = "auto"
	}
	if sp.DockerfilePath == "" {
		sp.DockerfilePath = "Dockerfile"
	}
	if sp.WatchPaths == nil {
		sp.WatchPaths = []string{}
	}
	if sp.HealthcheckType == "" {
		sp.HealthcheckType = "http"
	}
	if sp.HealthcheckPath == "" {
		sp.HealthcheckPath = "/"
	}
	if sp.HealthcheckTimeoutS <= 0 {
		sp.HealthcheckTimeoutS = 120
	}
	if sp.RestartPolicy == "" {
		sp.RestartPolicy = "on_failure"
	}
	if sp.RestartMaxRetries <= 0 {
		sp.RestartMaxRetries = 10
	}
	if sp.Replicas <= 0 {
		sp.Replicas = 1
	}
	if sp.Domains == nil {
		sp.Domains = []Domain{}
	}
	if sp.SleepAfterS <= 0 {
		sp.SleepAfterS = 600
	}
}

// EffectivePort는 컨테이너가 listen할 포트다. port가 없으면 PORT 변수, 그것도 없으면 8080.
func (sp *Spec) EffectivePort(env map[string]string) int {
	if sp.Port != nil && *sp.Port > 0 {
		return *sp.Port
	}
	var p int
	if _, err := fmt.Sscanf(env["PORT"], "%d", &p); err == nil && p > 0 && p < 65536 {
		return p
	}
	return 8080
}

type InstanceStatus = string

const (
	InstIdle      = "idle"
	InstDeploying = "deploying"
	InstOnline    = "online"
	InstCrashed   = "crashed"
	InstFailed    = "failed"
	InstOffline   = "offline"
	InstSleeping  = "sleeping"
	InstScheduled = "scheduled"
)

type Instance struct {
	ID            string `json:"id"`
	ServiceID     string `json:"service_id"`
	EnvironmentID string `json:"environment_id"`
	NodeID        string `json:"node_id"`
	Spec
	Variables           map[string]string `json:"variables"`
	Status              string            `json:"status"`
	CurrentDeploymentID *string           `json:"current_deployment_id"`
	Canvas              *Canvas           `json:"canvas"`
	UpdatedAt           time.Time         `json:"updated_at"`
}

const instanceCols = `id, service_id, environment_id, spec, variables, status, current_deployment_id, canvas, updated_at, node_id`

func scanInstance(r scanner) (*Instance, error) {
	var in Instance
	var spec, vars, updated string
	var cur, canvas sql.NullString
	if err := r.Scan(&in.ID, &in.ServiceID, &in.EnvironmentID, &spec, &vars, &in.Status, &cur, &canvas, &updated, &in.NodeID); err != nil {
		return nil, notFound(err)
	}
	json.Unmarshal([]byte(spec), &in.Spec)
	in.Spec.Defaults()
	json.Unmarshal([]byte(vars), &in.Variables)
	if in.Variables == nil {
		in.Variables = map[string]string{}
	}
	in.CurrentDeploymentID = nullStr(cur)
	if canvas.Valid {
		in.Canvas = &Canvas{}
		json.Unmarshal([]byte(canvas.String), in.Canvas)
	}
	in.UpdatedAt = parseTS(updated)
	return &in, nil
}

func (s *Store) GetInstance(ctx context.Context, id string) (*Instance, error) {
	return scanInstance(s.q.QueryRowContext(ctx, `SELECT `+instanceCols+` FROM instances WHERE id=?`, id))
}

func (s *Store) GetInstanceFor(ctx context.Context, serviceID, envID string) (*Instance, error) {
	return scanInstance(s.q.QueryRowContext(ctx, `SELECT `+instanceCols+` FROM instances WHERE service_id=? AND environment_id=?`, serviceID, envID))
}

func (s *Store) queryInstances(ctx context.Context, where string, args ...any) ([]*Instance, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+instanceCols+` FROM instances `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Instance{}
	for rows.Next() {
		in, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

func (s *Store) ListInstances(ctx context.Context) ([]*Instance, error) {
	return s.queryInstances(ctx, `ORDER BY updated_at`)
}

func (s *Store) ListInstancesByEnv(ctx context.Context, envID string) ([]*Instance, error) {
	return s.queryInstances(ctx, `WHERE environment_id=? ORDER BY id`, envID)
}

func (s *Store) ListInstancesByService(ctx context.Context, serviceID string) ([]*Instance, error) {
	return s.queryInstances(ctx, `WHERE service_id=?`, serviceID)
}

func (s *Store) CreateInstance(ctx context.Context, in *Instance) error {
	pid, err := s.serviceProjectID(ctx, in.ServiceID)
	if err != nil {
		return err
	}
	return s.withResourceCheck(ctx, pid, func(tx *Store, l ResourceLimits) error {
		resourceDefaults(&in.Spec, l)
		return tx.createInstance(ctx, in)
	})
}

func (s *Store) createInstance(ctx context.Context, in *Instance) error {
	if in.ID == "" {
		in.ID = NewID("ins")
	}
	in.UpdatedAt = time.Now().UTC()
	in.Spec.Defaults()
	if in.Variables == nil {
		in.Variables = map[string]string{}
	}
	if in.Status == "" {
		in.Status = InstIdle
	}
	var canvas any
	if in.Canvas != nil {
		canvas = jsonStr(in.Canvas)
	}
	if in.NodeID == "" {
		in.NodeID = "node_local"
	}
	// v0→v1 이전 중에는 node_id가 아직 없는 v1 스키마에 작성한다.
	if !s.tableExists(ctx, "nodes") {
		_, err := s.q.ExecContext(ctx, `INSERT INTO instances (id, service_id, environment_id, spec, variables, status, current_deployment_id, canvas, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			in.ID, in.ServiceID, in.EnvironmentID, jsonStr(in.Spec), jsonStr(in.Variables), in.Status, in.CurrentDeploymentID, canvas, ts(in.UpdatedAt))
		return conflict(err)
	}
	_, err := s.q.ExecContext(ctx, `INSERT INTO instances (`+instanceCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		in.ID, in.ServiceID, in.EnvironmentID, jsonStr(in.Spec), jsonStr(in.Variables), in.Status, in.CurrentDeploymentID, canvas, ts(in.UpdatedAt), in.NodeID)
	if err != nil {
		return conflict(err)
	}
	return s.syncDomainClaims(ctx, in.ID, nil, in.Domains) // 도메인 호스트 claim(domainstate.go)
}

// UpdateInstanceConfig는 설정/변수만 바꾼다(상태 필드는 건드리지 않는다).
func (s *Store) UpdateInstanceConfig(ctx context.Context, in *Instance) error {
	pid, err := s.serviceProjectID(ctx, in.ServiceID)
	if err != nil {
		return err
	}
	return s.withResourceCheck(ctx, pid, func(tx *Store, _ ResourceLimits) error { return tx.updateInstanceConfig(ctx, in) })
}

func (s *Store) updateInstanceConfig(ctx context.Context, in *Instance) error {
	// 스펙과 도메인 호스트 claim(domainstate.go)을 한 트랜잭션으로 쓴다.
	return s.Tx(ctx, func(tx *Store) error {
		old, err := tx.instanceSpecForUpdate(ctx, in.ID)
		if err != nil {
			return err
		}
		in.UpdatedAt = time.Now().UTC()
		if err := affected(tx.q.ExecContext(ctx, `UPDATE instances SET spec=?, variables=?, node_id=?, updated_at=? WHERE id=?`,
			jsonStr(in.Spec), jsonStr(in.Variables), in.NodeID, ts(in.UpdatedAt), in.ID)); err != nil {
			return err
		}
		return tx.syncDomainClaims(ctx, in.ID, old, in.Domains)
	})
}

func (s *Store) SetInstanceCanvas(ctx context.Context, id string, c Canvas) error {
	return affected(s.q.ExecContext(ctx, `UPDATE instances SET canvas=? WHERE id=?`, jsonStr(c), id))
}

// SetInstanceStatus는 상태를 바꾸고, currentDep이 nil이 아니면 현재 배포도 바꾼다.
func (s *Store) SetInstanceStatus(ctx context.Context, id, status string, currentDep *string) error {
	_, err := s.q.ExecContext(ctx, `UPDATE instances SET status=?, current_deployment_id=COALESCE(?, current_deployment_id), updated_at=? WHERE id=?`,
		status, currentDep, ts(time.Now()), id)
	return err
}

func (s *Store) ClearInstanceCurrent(ctx context.Context, id string) error {
	_, err := s.q.ExecContext(ctx, `UPDATE instances SET current_deployment_id=NULL WHERE id=?`, id)
	return err
}

// ---------------- Deployments ----------------

type DeployStatus string

const (
	DepWaiting        DeployStatus = "waiting" // wait_for_ci: CI 결과를 기다리는 중(큐에 넣지 않음)
	DepQueued         DeployStatus = "queued"
	DepInitializing   DeployStatus = "initializing"
	DepBuilding       DeployStatus = "building"
	DepPreDeploy      DeployStatus = "pre_deploy"
	DepDeploying      DeployStatus = "deploying"
	DepHealthchecking DeployStatus = "healthchecking"
	DepLive           DeployStatus = "live"
	DepFailed         DeployStatus = "failed"
	DepCrashed        DeployStatus = "crashed"
	DepCancelled      DeployStatus = "cancelled"
	DepRemoved        DeployStatus = "removed"
	DepSkipped        DeployStatus = "skipped"
)

// Terminal은 배포 파이프라인이 끝난 상태인지 알려준다(live는 이후 crashed/removed로 바뀔 수 있다).
func (s DeployStatus) Terminal() bool {
	switch s {
	case DepLive, DepFailed, DepCrashed, DepCancelled, DepRemoved, DepSkipped:
		return true
	}
	return false
}

// Succeeded는 한 번이라도 live가 됐던 배포인지(롤백 대상이 될 수 있는지) 알려준다.
func (s DeployStatus) Succeeded() bool {
	return s == DepLive || s == DepRemoved || s == DepCrashed
}

type Replica struct {
	Name     string `json:"name"`
	Status   string `json:"status"` // starting | running | exited | restarting
	Restarts int    `json:"restarts"`
	NodeID   string `json:"node_id,omitempty"` // replica가 도는 노드(placement로 여러 노드에 나눴을 때 구분용)
}

type Deployment struct {
	ID              string       `json:"id"`
	ServiceID       string       `json:"service_id"`
	EnvironmentID   string       `json:"environment_id"`
	InstanceID      string       `json:"service_instance_id"`
	NodeID          string       `json:"node_id"`
	Status          DeployStatus `json:"status"`
	Trigger         string       `json:"trigger"`
	CommitSHA       *string      `json:"commit_sha"`
	CommitMessage   *string      `json:"commit_message"`
	CommitAuthor    *string      `json:"commit_author"`
	Branch          *string      `json:"branch"`
	Image           *string      `json:"image"`
	BuilderUsed     *string      `json:"builder_used"`
	BuildReusedFrom *string      `json:"build_reused_from,omitempty"`
	Replicas        []Replica    `json:"replicas"`
	Error           *string      `json:"error"`
	CreatedAt       time.Time    `json:"created_at"`
	StartedAt       *time.Time   `json:"started_at"`
	FinishedAt      *time.Time   `json:"finished_at"`

	// 내부 필드
	Spec          Spec              `json:"-"` // 배포에 쓰인 설정(config-as-code 반영 후)
	ReqSpec       Spec              `json:"-"` // 배포를 만들 때 요청한 설정(인스턴스 설정 스냅샷, pending_changes 비교용)
	VarsRaw       map[string]string `json:"-"` // 사용자 변수 스냅샷(참조 포함). 롤백 시 복원한다
	Env           map[string]string `json:"-"` // 컨테이너에 넣은 최종 환경변수
	Containers    []string          `json:"-"` // 배포 노드(NodeID)의 replica 컨테이너
	Extra         []NodeContainers  `json:"-"` // placement의 다른 노드에서 도는 replica 컨테이너
	Source        string            `json:"-"` // "" | "upload:<path>"
	ChangedFiles  []string          `json:"-"` // github push의 변경 파일(watch pattern 판정용)
	BuildCacheKey string            `json:"-"`
	ForceBuild    bool              `json:"-"`
}

// NodeContainers는 한 노드에서 도는 배포의 replica 컨테이너다.
type NodeContainers struct {
	NodeID     string   `json:"node_id"`
	Containers []string `json:"containers"`
}

// Groups는 노드별 컨테이너다. 배포 노드(NodeID)가 항상 첫 항목이다.
func (d *Deployment) Groups() []NodeContainers {
	out := []NodeContainers{{NodeID: d.NodeID, Containers: d.Containers}}
	for _, g := range d.Extra {
		if NormNode(g.NodeID) == NormNode(d.NodeID) {
			continue // 주 노드와 같은 노드는 Containers에 있다
		}
		out = append(out, g)
	}
	return out
}

// AllContainers는 모든 노드의 replica 컨테이너다(배포 노드 것이 먼저).
func (d *Deployment) AllContainers() []string {
	var out []string
	for _, g := range d.Groups() {
		out = append(out, g.Containers...)
	}
	return out
}

// NodeOf는 컨테이너가 도는 노드다(모르면 배포 노드).
func (d *Deployment) NodeOf(container string) string {
	for _, g := range d.Extra {
		for _, c := range g.Containers {
			if c == container {
				return g.NodeID
			}
		}
	}
	return d.NodeID
}

const depCols = `id, service_id, environment_id, instance_id, status, trigger, commit_sha, commit_message, commit_author, branch,
	image, builder_used, error, spec, vars_raw, env, containers, source, changed_files, created_at, started_at, finished_at, req_spec, node_id, extra_containers,
	build_cache_key, build_reused_from, force_build`

func scanDep(r scanner) (*Deployment, error) {
	var d Deployment
	var sha, msg, author, branch, image, builder, errS, started, finished, reused sql.NullString
	var spec, varsRaw, env, containers, changed, created, reqSpec, extra string
	err := r.Scan(&d.ID, &d.ServiceID, &d.EnvironmentID, &d.InstanceID, &d.Status, &d.Trigger, &sha, &msg, &author, &branch,
		&image, &builder, &errS, &spec, &varsRaw, &env, &containers, &d.Source, &changed, &created, &started, &finished, &reqSpec, &d.NodeID, &extra,
		&d.BuildCacheKey, &reused, &d.ForceBuild)
	if err != nil {
		return nil, notFound(err)
	}
	json.Unmarshal([]byte(extra), &d.Extra)
	d.CommitSHA, d.CommitMessage, d.CommitAuthor, d.Branch = nullStr(sha), nullStr(msg), nullStr(author), nullStr(branch)
	d.Image, d.BuilderUsed, d.Error = nullStr(image), nullStr(builder), nullStr(errS)
	d.BuildReusedFrom = nullStr(reused)
	json.Unmarshal([]byte(spec), &d.Spec)
	d.Spec.Defaults()
	json.Unmarshal([]byte(reqSpec), &d.ReqSpec)
	d.ReqSpec.Defaults()
	json.Unmarshal([]byte(varsRaw), &d.VarsRaw)
	json.Unmarshal([]byte(env), &d.Env)
	json.Unmarshal([]byte(containers), &d.Containers)
	json.Unmarshal([]byte(changed), &d.ChangedFiles)
	if d.VarsRaw == nil {
		d.VarsRaw = map[string]string{}
	}
	if d.Env == nil {
		d.Env = map[string]string{}
	}
	d.Replicas = []Replica{}
	d.CreatedAt = parseTS(created)
	d.StartedAt, d.FinishedAt = parseNullTS(started), parseNullTS(finished)
	return &d, nil
}

func (s *Store) CreateDeployment(ctx context.Context, d *Deployment) error {
	pid, err := s.serviceProjectID(ctx, d.ServiceID)
	if err != nil {
		return err
	}
	return s.withResourceCheck(ctx, pid, func(tx *Store, l ResourceLimits) error {
		if d.Status != DepSkipped {
			resourceDefaults(&d.Spec, l)
		}
		return tx.createDeployment(ctx, d)
	})
}

func (s *Store) createDeployment(ctx context.Context, d *Deployment) error {
	d.ID = NewID("dep")
	d.CreatedAt = time.Now().UTC()
	if d.Status == "" {
		d.Status = DepQueued
	}
	if d.NodeID == "" {
		d.NodeID = "node_local"
	}
	_, err := s.q.ExecContext(ctx, `INSERT INTO deployments (`+depCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.ID, d.ServiceID, d.EnvironmentID, d.InstanceID, d.Status, d.Trigger, d.CommitSHA, d.CommitMessage, d.CommitAuthor, d.Branch,
		d.Image, d.BuilderUsed, d.Error, jsonStr(d.Spec), jsonStr(d.VarsRaw), jsonStr(d.Env), jsonStr(d.Containers), d.Source,
		jsonStr(d.ChangedFiles), ts(d.CreatedAt), nullTS(d.StartedAt), nullTS(d.FinishedAt), jsonStr(d.ReqSpec), d.NodeID, extraJSON(d.Extra),
		d.BuildCacheKey, d.BuildReusedFrom, d.ForceBuild)
	return err
}

func extraJSON(e []NodeContainers) string {
	if len(e) == 0 {
		return "[]"
	}
	return jsonStr(e)
}

func (s *Store) UpdateDeployment(ctx context.Context, d *Deployment) error {
	_, err := s.q.ExecContext(ctx, `UPDATE deployments SET status=?, commit_sha=?, commit_message=?, commit_author=?, branch=?,
		image=?, builder_used=?, error=?, spec=?, env=?, containers=?, started_at=?, finished_at=?, extra_containers=?,
		build_cache_key=?, build_reused_from=?, force_build=? WHERE id=?`,
		d.Status, d.CommitSHA, d.CommitMessage, d.CommitAuthor, d.Branch, d.Image, d.BuilderUsed, d.Error,
		jsonStr(d.Spec), jsonStr(d.Env), jsonStr(d.Containers), nullTS(d.StartedAt), nullTS(d.FinishedAt), extraJSON(d.Extra),
		d.BuildCacheKey, d.BuildReusedFrom, d.ForceBuild, d.ID)
	return err
}

// SetDeploymentContainers는 배포의 replica 컨테이너 목록만 바꾼다(재배포 없는 replica 수 변경).
// 다른 필드는 건드리지 않으므로 진행 중인 다른 갱신과 섞이지 않는다.
func (s *Store) SetDeploymentContainers(ctx context.Context, id string, containers []string, extra []NodeContainers) error {
	if containers == nil {
		containers = []string{}
	}
	return affected(s.q.ExecContext(ctx, `UPDATE deployments SET containers=?, extra_containers=? WHERE id=?`, jsonStr(containers), extraJSON(extra), id))
}

// SetDeploymentReplicas는 배포 스냅샷(spec, req_spec)의 replicas를 바꾼다. 수동 scale은 다음 pending_changes 비교에서 바뀐 것으로 보이지 않게 한다.
func (s *Store) SetDeploymentReplicas(ctx context.Context, id string, spec, reqSpec Spec) error {
	return affected(s.q.ExecContext(ctx, `UPDATE deployments SET spec=?, req_spec=? WHERE id=?`, jsonStr(spec), jsonStr(reqSpec), id))
}

func (s *Store) SetDeploymentStatus(ctx context.Context, id string, st DeployStatus) error {
	_, err := s.q.ExecContext(ctx, `UPDATE deployments SET status=? WHERE id=?`, st, id)
	return err
}

func (s *Store) GetDeployment(ctx context.Context, id string) (*Deployment, error) {
	return scanDep(s.q.QueryRowContext(ctx, `SELECT `+depCols+` FROM deployments WHERE id=?`, id))
}

func (s *Store) queryDeps(ctx context.Context, where string, args ...any) ([]*Deployment, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+depCols+` FROM deployments `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Deployment{}
	for rows.Next() {
		d, err := scanDep(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) ListDeployments(ctx context.Context, instanceID string, limit int) ([]*Deployment, error) {
	return s.queryDeps(ctx, `WHERE instance_id=? ORDER BY created_at DESC LIMIT ?`, instanceID, limit)
}

// BuildCacheCandidates는 서비스의 같은 빌드 입력으로 만든 이미지를 최신 순으로 돌려준다.
// 호출자는 실제 빌드 노드에 이미지가 남아 있는지 확인한다.
func (s *Store) BuildCacheCandidates(ctx context.Context, serviceID, key string) ([]*Deployment, error) {
	return s.queryDeps(ctx, `WHERE service_id=? AND build_cache_key=? AND image IS NOT NULL ORDER BY created_at DESC LIMIT 100`, serviceID, key)
}

func (s *Store) LatestDeployment(ctx context.Context, instanceID string) (*Deployment, error) {
	return scanDep(s.q.QueryRowContext(ctx, `SELECT `+depCols+` FROM deployments WHERE instance_id=? ORDER BY created_at DESC LIMIT 1`, instanceID))
}

// ListUnfinished는 서버 재시작 시 정리해야 할, 끝나지 않은 배포를 돌려준다.
// waiting(CI 대기) 배포는 시작한 적이 없으므로 넣지 않는다: 재시작 후에도 CI 결과 webhook을 기다린다.
func (s *Store) ListUnfinished(ctx context.Context) ([]*Deployment, error) {
	return s.queryDeps(ctx, `WHERE status NOT IN ('live','failed','crashed','cancelled','removed','skipped','waiting') ORDER BY created_at`)
}

// ListWaitingBySHA는 커밋 sha의 CI 결과를 기다리는 배포다.
func (s *Store) ListWaitingBySHA(ctx context.Context, sha string) ([]*Deployment, error) {
	return s.queryDeps(ctx, `WHERE status='waiting' AND commit_sha=? ORDER BY created_at`, sha)
}

// ListWaitingByInstance는 인스턴스의 CI 대기 배포다.
func (s *Store) ListWaitingByInstance(ctx context.Context, instanceID string) ([]*Deployment, error) {
	return s.queryDeps(ctx, `WHERE status='waiting' AND instance_id=? ORDER BY created_at`, instanceID)
}

// TransitionDeployment는 배포가 아직 from 상태일 때만 to로 바꾼다(webhook 중복 전달 등 경쟁 방지).
// to가 끝난 상태면 finished_at과 error도 기록한다. 바꿨으면 true.
func (s *Store) TransitionDeployment(ctx context.Context, id string, from, to DeployStatus, errMsg *string) (bool, error) {
	var res sql.Result
	var err error
	if to.Terminal() {
		res, err = s.q.ExecContext(ctx, `UPDATE deployments SET status=?, error=COALESCE(?, error), finished_at=? WHERE id=? AND status=?`, to, errMsg, ts(time.Now()), id, from)
	} else {
		res, err = s.q.ExecContext(ctx, `UPDATE deployments SET status=? WHERE id=? AND status=?`, to, id, from)
	}
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ---------------- Shared variables ----------------

func (s *Store) GetSharedVars(ctx context.Context, envID string) (map[string]string, error) {
	var data string
	err := s.q.QueryRowContext(ctx, `SELECT data FROM shared_variables WHERE environment_id=?`, envID).Scan(&data)
	out := map[string]string{}
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(data), &out)
	return out, nil
}

func (s *Store) SetSharedVars(ctx context.Context, envID string, vars map[string]string) error {
	if vars == nil {
		vars = map[string]string{}
	}
	_, err := s.q.ExecContext(ctx, `INSERT INTO shared_variables (environment_id, data) VALUES (?,?)
		ON CONFLICT(environment_id) DO UPDATE SET data=excluded.data`, envID, jsonStr(vars))
	return err
}

// ---------------- Volumes / Backups ----------------

type Volume struct {
	ID            string  `json:"id"`
	ProjectID     string  `json:"project_id"`
	EnvironmentID string  `json:"environment_id"`
	InstanceID    *string `json:"service_instance_id"`
	Name          string  `json:"name"`
	MountPath     string  `json:"mount_path"`
	SizeBytes     *int64  `json:"size_bytes"`
	// SizeLimitMB는 크기 상한(MB, 1MB = 1,048,576 bytes)이다. docker local 볼륨은 크기를 강제할 수 없어
	// 주기적인 크기 측정으로 90%·100% 도달 이벤트만 낸다(soft limit). nil이면 상한 없음.
	SizeLimitMB *int      `json:"size_limit_mb"`
	CreatedAt   time.Time `json:"created_at"`

	DockerName string  `json:"-"`
	BackupCron *string `json:"-"`
	BackupKeep int     `json:"-"`
	// LimitState는 마지막으로 알린 상한 상태다: "" | near | exceeded (같은 상태는 한 번만 알린다).
	LimitState string `json:"-"`
}

// 볼륨 크기 상한 상태
const (
	VolumeLimitOK       = ""
	VolumeLimitNear     = "near"
	VolumeLimitExceeded = "exceeded"
)

const volumeCols = `id, project_id, environment_id, instance_id, name, mount_path, docker_name, backup_cron, backup_keep, created_at, size_limit_mb, limit_state`

func scanVolume(r scanner) (*Volume, error) {
	var v Volume
	var inst, cron sql.NullString
	var limit sql.NullInt64
	var created string
	if err := r.Scan(&v.ID, &v.ProjectID, &v.EnvironmentID, &inst, &v.Name, &v.MountPath, &v.DockerName, &cron, &v.BackupKeep, &created, &limit, &v.LimitState); err != nil {
		return nil, notFound(err)
	}
	if limit.Valid {
		n := int(limit.Int64)
		v.SizeLimitMB = &n
	}
	v.InstanceID, v.BackupCron = nullStr(inst), nullStr(cron)
	v.CreatedAt = parseTS(created)
	return &v, nil
}

func (s *Store) GetVolume(ctx context.Context, id string) (*Volume, error) {
	return scanVolume(s.q.QueryRowContext(ctx, `SELECT `+volumeCols+` FROM volumes WHERE id=?`, id))
}

func (s *Store) queryVolumes(ctx context.Context, where string, args ...any) ([]*Volume, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+volumeCols+` FROM volumes `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Volume{}
	for rows.Next() {
		v, err := scanVolume(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) ListVolumes(ctx context.Context) ([]*Volume, error) {
	return s.queryVolumes(ctx, `ORDER BY created_at`)
}
func (s *Store) ListVolumesByEnv(ctx context.Context, envID string) ([]*Volume, error) {
	return s.queryVolumes(ctx, `WHERE environment_id=? ORDER BY created_at`, envID)
}
func (s *Store) ListVolumesByInstance(ctx context.Context, instanceID string) ([]*Volume, error) {
	return s.queryVolumes(ctx, `WHERE instance_id=? ORDER BY created_at`, instanceID)
}
func (s *Store) ListVolumesByProject(ctx context.Context, projectID string) ([]*Volume, error) {
	return s.queryVolumes(ctx, `WHERE project_id=? ORDER BY created_at`, projectID)
}

func (s *Store) CreateVolume(ctx context.Context, v *Volume) error {
	return s.withResourceCheck(ctx, v.ProjectID, func(tx *Store, l ResourceLimits) error {
		if v.SizeLimitMB == nil && l.VolumeMB != nil {
			n := int(min(1024, *l.VolumeMB))
			v.SizeLimitMB = &n
		}
		return tx.createVolume(ctx, v)
	})
}

func (s *Store) createVolume(ctx context.Context, v *Volume) error {
	v.ID = NewID("vol")
	v.DockerName = "aven-" + strings.ReplaceAll(v.ID, "_", "-")
	v.CreatedAt = time.Now().UTC()
	if v.BackupKeep <= 0 {
		v.BackupKeep = 7
	}
	_, err := s.q.ExecContext(ctx, `INSERT INTO volumes (`+volumeCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		v.ID, v.ProjectID, v.EnvironmentID, v.InstanceID, v.Name, v.MountPath, v.DockerName, v.BackupCron, v.BackupKeep, ts(v.CreatedAt), v.SizeLimitMB, v.LimitState)
	return conflict(err)
}

func (s *Store) UpdateVolume(ctx context.Context, v *Volume) error {
	return s.withResourceCheck(ctx, v.ProjectID, func(tx *Store, _ ResourceLimits) error { return tx.updateVolume(ctx, v) })
}

func (s *Store) updateVolume(ctx context.Context, v *Volume) error {
	return affected(s.q.ExecContext(ctx, `UPDATE volumes SET instance_id=?, name=?, mount_path=?, backup_cron=?, backup_keep=?, size_limit_mb=? WHERE id=?`,
		v.InstanceID, v.Name, v.MountPath, v.BackupCron, v.BackupKeep, v.SizeLimitMB, v.ID))
}

// SetVolumeLimitState는 마지막으로 알린 크기 상한 상태를 기록한다.
func (s *Store) SetVolumeLimitState(ctx context.Context, id, state string) error {
	return affected(s.q.ExecContext(ctx, `UPDATE volumes SET limit_state=? WHERE id=?`, state, id))
}

func (s *Store) DeleteVolume(ctx context.Context, id string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM volumes WHERE id=?`, id))
}

type Backup struct {
	ID        string    `json:"id"`
	VolumeID  string    `json:"volume_id"`
	SizeBytes int64     `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
	Status    string    `json:"status"` // running | done | failed
	Error     *string   `json:"error"`
	Path      string    `json:"-"`
}

const backupCols = `id, volume_id, path, size_bytes, status, error, created_at`

func scanBackup(r scanner) (*Backup, error) {
	var b Backup
	var errS sql.NullString
	var created string
	if err := r.Scan(&b.ID, &b.VolumeID, &b.Path, &b.SizeBytes, &b.Status, &errS, &created); err != nil {
		return nil, notFound(err)
	}
	b.Error = nullStr(errS)
	b.CreatedAt = parseTS(created)
	return &b, nil
}

func (s *Store) CreateBackup(ctx context.Context, b *Backup) error {
	b.ID = NewID("bak")
	b.CreatedAt = time.Now().UTC()
	_, err := s.q.ExecContext(ctx, `INSERT INTO backups (`+backupCols+`) VALUES (?,?,?,?,?,?,?)`,
		b.ID, b.VolumeID, b.Path, b.SizeBytes, b.Status, b.Error, ts(b.CreatedAt))
	return err
}

func (s *Store) UpdateBackup(ctx context.Context, b *Backup) error {
	_, err := s.q.ExecContext(ctx, `UPDATE backups SET path=?, size_bytes=?, status=?, error=? WHERE id=?`, b.Path, b.SizeBytes, b.Status, b.Error, b.ID)
	return err
}

func (s *Store) GetBackup(ctx context.Context, id string) (*Backup, error) {
	return scanBackup(s.q.QueryRowContext(ctx, `SELECT `+backupCols+` FROM backups WHERE id=?`, id))
}

func (s *Store) ListBackups(ctx context.Context, volumeID string) ([]*Backup, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+backupCols+` FROM backups WHERE volume_id=? ORDER BY created_at DESC`, volumeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Backup{}
	for rows.Next() {
		b, err := scanBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) DeleteBackup(ctx context.Context, id string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM backups WHERE id=?`, id))
}

func (s *Store) FailRunningBackups(ctx context.Context) error {
	_, err := s.q.ExecContext(ctx, `UPDATE backups SET status='failed', error='server restarted during backup' WHERE status='running'`)
	return err
}

// ---------------- Events ----------------

type Event struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	EnvironmentID *string   `json:"environment_id"`
	ServiceID     *string   `json:"service_id"`
	ServiceName   *string   `json:"service_name"` // 이벤트 시점의 서비스 이름(삭제된 서비스도 남는다)
	Kind          string    `json:"kind"`
	Message       string    `json:"message"`
	CreatedAt     time.Time `json:"created_at"`
	// Actor는 이벤트를 일으킨 사용자 표시(이름 또는 이메일)다. 시스템(webhook, 스케줄러 등)이 낸 이벤트는 null.
	Actor *string `json:"actor"`
	// DeploymentID는 배포와 관련된 이벤트(deploy.*)의 배포다(없으면 null).
	DeploymentID *string `json:"deployment_id"`
}

func (s *Store) CreateEvent(ctx context.Context, e *Event) error {
	e.ID = NewID("evt")
	e.CreatedAt = time.Now().UTC()
	_, err := s.q.ExecContext(ctx, `INSERT INTO events (id, project_id, environment_id, service_id, service_name, kind, message, created_at, actor, deployment_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.ProjectID, e.EnvironmentID, e.ServiceID, e.ServiceName, e.Kind, e.Message, ts(e.CreatedAt), e.Actor, e.DeploymentID)
	return err
}

func (s *Store) ListEvents(ctx context.Context, projectID, envID string, limit int) ([]*Event, error) {
	q := `SELECT id, project_id, environment_id, service_id, service_name, kind, message, created_at, actor, deployment_id FROM events WHERE project_id=?`
	args := []any{projectID}
	if envID != "" {
		q += ` AND (environment_id=? OR environment_id IS NULL)`
		args = append(args, envID)
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Event{}
	for rows.Next() {
		var e Event
		var env, svc, svcName, actor, dep sql.NullString
		var created string
		if err := rows.Scan(&e.ID, &e.ProjectID, &env, &svc, &svcName, &e.Kind, &e.Message, &created, &actor, &dep); err != nil {
			return nil, err
		}
		e.EnvironmentID, e.ServiceID, e.ServiceName, e.Actor, e.DeploymentID = nullStr(env), nullStr(svc), nullStr(svcName), nullStr(actor), nullStr(dep)
		e.CreatedAt = parseTS(created)
		out = append(out, &e)
	}
	return out, rows.Err()
}

func (s *Store) PruneEvents(ctx context.Context, before time.Time) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM events WHERE created_at < ?`, ts(before))
	return err
}

// ---------------- Cron runs ----------------

type CronRun struct {
	ID           string     `json:"id"`
	DeploymentID string     `json:"deployment_id"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
	ExitCode     *int       `json:"exit_code"`
	Status       string     `json:"status"` // running | succeeded | failed
	InstanceID   string     `json:"-"`
	Container    string     `json:"-"`
}

const cronCols = `id, deployment_id, instance_id, container, started_at, finished_at, exit_code, status`

func scanCron(r scanner) (*CronRun, error) {
	var c CronRun
	var started string
	var finished sql.NullString
	var code sql.NullInt64
	if err := r.Scan(&c.ID, &c.DeploymentID, &c.InstanceID, &c.Container, &started, &finished, &code, &c.Status); err != nil {
		return nil, notFound(err)
	}
	c.StartedAt = parseTS(started)
	c.FinishedAt = parseNullTS(finished)
	if code.Valid {
		v := int(code.Int64)
		c.ExitCode = &v
	}
	return &c, nil
}

func (s *Store) CreateCronRun(ctx context.Context, c *CronRun) error {
	if c.ID == "" {
		c.ID = NewID("run")
	}
	c.StartedAt = time.Now().UTC()
	c.Status = "running"
	_, err := s.q.ExecContext(ctx, `INSERT INTO cron_runs (`+cronCols+`) VALUES (?,?,?,?,?,?,?,?)`,
		c.ID, c.DeploymentID, c.InstanceID, c.Container, ts(c.StartedAt), nil, nil, c.Status)
	return err
}

func (s *Store) FinishCronRun(ctx context.Context, c *CronRun) error {
	now := time.Now().UTC()
	c.FinishedAt = &now
	_, err := s.q.ExecContext(ctx, `UPDATE cron_runs SET finished_at=?, exit_code=?, status=? WHERE id=?`, ts(now), c.ExitCode, c.Status, c.ID)
	return err
}

func (s *Store) GetCronRun(ctx context.Context, id string) (*CronRun, error) {
	return scanCron(s.q.QueryRowContext(ctx, `SELECT `+cronCols+` FROM cron_runs WHERE id=?`, id))
}

func (s *Store) ListCronRuns(ctx context.Context, deploymentID string, limit int) ([]*CronRun, error) {
	return s.queryCron(ctx, `WHERE deployment_id=? ORDER BY started_at DESC LIMIT ?`, deploymentID, limit)
}

func (s *Store) ListCronRunsByInstance(ctx context.Context, instanceID string, limit int) ([]*CronRun, error) {
	return s.queryCron(ctx, `WHERE instance_id=? ORDER BY started_at DESC LIMIT ?`, instanceID, limit)
}

func (s *Store) ListRunningCron(ctx context.Context) ([]*CronRun, error) {
	return s.queryCron(ctx, `WHERE status='running'`)
}

func (s *Store) queryCron(ctx context.Context, where string, args ...any) ([]*CronRun, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+cronCols+` FROM cron_runs `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*CronRun{}
	for rows.Next() {
		c, err := scanCron(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---------------- Metrics ----------------

type MetricRow struct {
	T          int64
	CPU        float64
	Mem        int64
	MemLimit   int64
	Rx, Tx     int64
	Disk       *int64
	Replicas   int
	Resolution int // 초
}

func (s *Store) InsertMetrics(ctx context.Context, instanceID string, rows []MetricRow) error {
	return s.Tx(ctx, func(tx *Store) error {
		for _, r := range rows {
			if _, err := tx.q.ExecContext(ctx, `INSERT INTO metrics (instance_id, t, res, cpu, mem, mem_limit, rx, tx, disk, replicas) VALUES (?,?,?,?,?,?,?,?,?,?)`,
				instanceID, r.T, r.Resolution, r.CPU, r.Mem, r.MemLimit, r.Rx, r.Tx, r.Disk, r.Replicas); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) QueryMetrics(ctx context.Context, instanceID string, from int64) ([]MetricRow, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT t, res, cpu, mem, mem_limit, rx, tx, disk, replicas FROM metrics WHERE instance_id=? AND t>=? ORDER BY t`, instanceID, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MetricRow{}
	for rows.Next() {
		var r MetricRow
		var disk sql.NullInt64
		if err := rows.Scan(&r.T, &r.Resolution, &r.CPU, &r.Mem, &r.MemLimit, &r.Rx, &r.Tx, &disk, &r.Replicas); err != nil {
			return nil, err
		}
		if disk.Valid {
			r.Disk = &disk.Int64
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReplaceMetrics는 [from,to) 구간의 원본 점을 다운샘플한 점으로 바꾼다.
func (s *Store) ReplaceMetrics(ctx context.Context, instanceID string, from, to int64, rows []MetricRow) error {
	return s.Tx(ctx, func(tx *Store) error {
		if _, err := tx.q.ExecContext(ctx, `DELETE FROM metrics WHERE instance_id=? AND t>=? AND t<?`, instanceID, from, to); err != nil {
			return err
		}
		for _, r := range rows {
			if _, err := tx.q.ExecContext(ctx, `INSERT INTO metrics (instance_id, t, res, cpu, mem, mem_limit, rx, tx, disk, replicas) VALUES (?,?,?,?,?,?,?,?,?,?)`,
				instanceID, r.T, r.Resolution, r.CPU, r.Mem, r.MemLimit, r.Rx, r.Tx, r.Disk, r.Replicas); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) MetricInstances(ctx context.Context) ([]string, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT DISTINCT instance_id FROM metrics`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) PruneMetrics(ctx context.Context, before int64) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM metrics WHERE t < ? OR instance_id NOT IN (SELECT id FROM instances)`, before)
	return err
}

// ---------------- KV settings ----------------

func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	return v, notFound(err)
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.q.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM settings WHERE key=?`, key)
	return err
}

// ---------------- Registries ----------------

// Registry는 private registry 인증 정보다. Password는 API로 내보내지 않는다.
type Registry struct {
	Host     string `json:"host"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// SetSecretBox는 저장 시 암호화에 쓸 마스터 키를 지정한다. 지정 전에는 값을 평문으로 둔다.
func (s *Store) SetSecretBox(b *secret.Box) { *s.box = b }

// Seal은 비밀 값을 암호화한다(키가 없으면 그대로).
func (s *Store) Seal(v string) string {
	if b := *s.box; b != nil {
		return b.Seal(v)
	}
	return v
}

// Unseal은 Seal의 역변환이다. 접두어가 없는 값은 평문으로 본다.
func (s *Store) Unseal(v string) (string, error) {
	b := *s.box
	if b == nil {
		return v, nil
	}
	p, _, err := b.Open(v)
	return p, err
}

// EncryptLegacySecrets는 평문으로 저장된 비밀(registry 비밀번호)을 마스터 키로 암호화해 다시 저장한다.
func (s *Store) EncryptLegacySecrets(ctx context.Context) error {
	rows, err := s.q.QueryContext(ctx, `SELECT id, registries FROM projects WHERE registries LIKE '%"password"%'`)
	if err != nil {
		return err
	}
	type row struct{ id, raw string }
	var todo []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.raw); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, r)
	}
	rows.Close()
	for _, r := range todo {
		var regs []Registry
		json.Unmarshal([]byte(r.raw), &regs)
		plain := false
		for _, g := range regs {
			if g.Password != "" && !strings.HasPrefix(g.Password, secret.Prefix) {
				plain = true
			}
		}
		if !plain {
			continue
		}
		for i := range regs {
			regs[i].Password = s.Seal(regs[i].Password)
		}
		if _, err := s.q.ExecContext(ctx, `UPDATE projects SET registries=? WHERE id=?`, jsonStr(regs), r.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) GetRegistries(ctx context.Context, projectID string) ([]Registry, error) {
	var raw string
	if err := s.q.QueryRowContext(ctx, `SELECT registries FROM projects WHERE id=?`, projectID).Scan(&raw); err != nil {
		return nil, notFound(err)
	}
	out := []Registry{}
	json.Unmarshal([]byte(raw), &out)
	for i := range out {
		p, err := s.Unseal(out[i].Password)
		if err != nil {
			return nil, fmt.Errorf("registry %s: %w", out[i].Host, err)
		}
		out[i].Password = p
	}
	return out, nil
}

func (s *Store) SetRegistries(ctx context.Context, projectID string, regs []Registry) error {
	if regs == nil {
		regs = []Registry{}
	}
	enc := make([]Registry, len(regs))
	for i, g := range regs {
		g.Password = s.Seal(g.Password)
		enc[i] = g
	}
	return affected(s.q.ExecContext(ctx, `UPDATE projects SET registries=? WHERE id=?`, jsonStr(enc), projectID))
}
