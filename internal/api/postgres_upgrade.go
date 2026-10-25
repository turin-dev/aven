package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/dbquery"
	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

//
//  1. preflight: 읽기 전용 검사(이미지/볼륨 구성 + 실행 중인 DB에 대한 SQL 검사). blocker가 있으면 시작하지 않는다.
//  2. 업그레이드: DB를 멈추고 볼륨을 통째로 백업한 뒤, pgautoupgrade 이미지(공식 postgres 이미지 + pg_upgrade 자동 실행)로
//     다시 배포한다. 새 메이저로 live가 되면 버전과 데이터를 확인하고 논리 백업을 하나 더 만든다.
//     실패하면 업그레이드 전 백업으로 자동 복구한다.
//  3. 되돌리기: 일정 기간 동안 업그레이드 전 볼륨 백업으로 복원하고 원래 이미지로 되돌린다(이후 쓰기는 사라진다).
//
// pgautoupgrade 이미지는 PostgreSQL 18부터 바뀐 공식 이미지 데이터 경로에 맞춰 볼륨을 /var/lib/postgresql에 마운트한다
// (기존 PGDATA /var/lib/postgresql/data/pgdata는 같은 볼륨 안의 /var/lib/postgresql/pgdata가 된다).

const (
	pgUpgradeImageRepo   = "pgautoupgrade/pgautoupgrade"
	minPGSourceMajor     = 14
	maxPGMajor           = 18
	pgUpgradeWait        = 2 * time.Hour
	pgUpgradeHealthcheck = 3600 // pg_upgrade가 끝날 때까지 healthcheck를 기다린다(초)
	pgRevertWindow       = 7 * 24 * time.Hour
	pgLegacyMount        = "/var/lib/postgresql/data"
	pgLegacyPGData       = "/var/lib/postgresql/data/pgdata"
	pgUpgradeMount       = "/var/lib/postgresql"
	pgUpgradePGData      = "/var/lib/postgresql/pgdata"
)

type postgresCheck struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Severity string `json:"severity"` // blocker | warning | info
	Passed   bool   `json:"passed"`
	Message  string `json:"message"`
}

type postgresPreflight struct {
	Ready            bool            `json:"ready"`
	CurrentMajor     int             `json:"current_major"`
	TargetMajor      int             `json:"target_major"`
	AvailableTargets []int           `json:"available_targets"`
	DataSizeBytes    *int64          `json:"data_size_bytes"`
	Checks           []postgresCheck `json:"checks"`
	CheckedAt        time.Time       `json:"checked_at"`

	userTables int // 업그레이드 뒤 데이터가 그대로인지 비교한다
}

func (p *postgresPreflight) add(key, label, severity string, passed bool, msg string) {
	p.Checks = append(p.Checks, postgresCheck{Key: key, Label: label, Severity: severity, Passed: passed, Message: msg})
	if !passed && severity == "blocker" {
		p.Ready = false
	}
}

type postgresUpgradeView struct {
	// Status: none | preparing | upgrading | succeeded | failed | reverting | reverted | revert_failed
	Status          string     `json:"status"`
	CurrentMajor    int        `json:"current_major,omitempty"`
	FromMajor       int        `json:"from_major,omitempty"`
	TargetMajor     int        `json:"target_major,omitempty"`
	Image           string     `json:"image,omitempty"`
	FromImage       string     `json:"from_image,omitempty"`
	BackupID        string     `json:"backup_id,omitempty"`
	PostBackupID    string     `json:"post_backup_id,omitempty"`
	DeploymentID    string     `json:"deployment_id,omitempty"`
	Error           string     `json:"error,omitempty"`
	CanRevert       bool       `json:"can_revert"`
	RevertDeadline  *time.Time `json:"revert_deadline,omitempty"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	InProgress      bool       `json:"in_progress"`
	AvailableMajors []int      `json:"available_targets"`
}

func pgUpgradeInProgress(status string) bool {
	return status == "preparing" || status == "upgrading" || status == "reverting"
}

// postgresImageMajor는 지원하는 이미지(공식 postgres 또는 pgautoupgrade의 alpine 태그)의 메이저 버전이다.
// 메이저가 고정되지 않은 태그(latest, alpine)와 debian 변형은 지원하지 않는다(alpine → debian은 collation이 바뀐다).
func postgresImageMajor(image *string) (int, bool) {
	if image == nil {
		return 0, false
	}
	ref := *image
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	i := strings.LastIndex(ref, ":")
	if i < 0 || i < strings.LastIndex(ref, "/") {
		return 0, false
	}
	repo, tag := ref[:i], ref[i+1:]
	switch strings.TrimPrefix(repo, "docker.io/") {
	case "postgres", "library/postgres", pgUpgradeImageRepo:
	default:
		return 0, false
	}
	ver, variant, _ := strings.Cut(tag, "-")
	if !strings.HasPrefix(variant, "alpine") {
		return 0, false
	}
	majorText, _, _ := strings.Cut(ver, ".")
	major, err := strconv.Atoi(majorText)
	return major, err == nil && major >= 10 && major <= maxPGMajor
}

func pgUpgradeImage(major int) string {
	return pgUpgradeImageRepo + ":" + strconv.Itoa(major) + "-alpine"
}

func pgTargets(current int) []int {
	out := []int{}
	if current < minPGSourceMajor {
		return out
	}
	for m := current + 1; m <= maxPGMajor; m++ {
		out = append(out, m)
	}
	return out
}

// pgContribExtensions는 공식 alpine 이미지(pgautoupgrade 포함)에 들어 있는 확장이다.
var pgContribExtensions = map[string]bool{
	"amcheck": true, "autoinc": true, "bloom": true, "btree_gin": true, "btree_gist": true, "citext": true, "cube": true,
	"dblink": true, "dict_int": true, "dict_xsyn": true, "earthdistance": true, "file_fdw": true, "fuzzystrmatch": true,
	"hstore": true, "insert_username": true, "intagg": true, "intarray": true, "isn": true, "lo": true, "ltree": true,
	"moddatetime": true, "pageinspect": true, "pg_buffercache": true, "pg_freespacemap": true, "pg_prewarm": true,
	"pg_stat_statements": true, "pg_surgery": true, "pg_trgm": true, "pg_visibility": true, "pg_walinspect": true,
	"pgcrypto": true, "pgrowlocks": true, "pgstattuple": true, "postgres_fdw": true, "refint": true, "seg": true,
	"sslinfo": true, "tablefunc": true, "tcn": true, "tsm_system_rows": true, "tsm_system_time": true, "unaccent": true,
	"uuid-ossp": true, "xml2": true, "plperl": true, "plperlu": true, "plpython3u": true, "pltcl": true, "pltclu": true,
}

// pgRemovedExtensions는 그 메이저부터 사라진 contrib 확장이다.
var pgRemovedExtensions = map[string]int{"adminpack": 17, "old_snapshot": 17}

// pgRuntime은 실행 중인 서버에서 읽은 preflight 값이다.
type pgRuntime struct {
	Major         int
	RegColumns    int64
	AclColumns    int64
	PreparedXacts int64
	LogicalSlots  int64
	MD5Roles      int64
	EventTriggers int64
	Extensions    []string // name@version
	SizeBytes     *int64
	UserTables    int64
}

const pgUserRels = `c.relkind IN ('r','m','p') AND n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname !~ '^pg_toast'`

const pgPreflightSQL = `SELECT current_setting('server_version_num')::int AS version_num,
 (SELECT count(*) FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid = a.attrelid JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
   WHERE a.attnum > 0 AND NOT a.attisdropped AND ` + pgUserRels + `
   AND a.atttypid IN ('pg_catalog.regcollation'::regtype, 'pg_catalog.regconfig'::regtype, 'pg_catalog.regdictionary'::regtype,
     'pg_catalog.regnamespace'::regtype, 'pg_catalog.regoper'::regtype, 'pg_catalog.regoperator'::regtype,
     'pg_catalog.regproc'::regtype, 'pg_catalog.regprocedure'::regtype)) AS reg_columns,
 (SELECT count(*) FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid = a.attrelid JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
   WHERE a.attnum > 0 AND NOT a.attisdropped AND ` + pgUserRels + ` AND a.atttypid = 'pg_catalog.aclitem'::regtype) AS aclitem_columns,
 (SELECT count(*) FROM pg_catalog.pg_prepared_xacts) AS prepared_xacts,
 (SELECT count(*) FROM pg_catalog.pg_replication_slots WHERE slot_type = 'logical') AS logical_slots,
 (SELECT count(*) FROM pg_catalog.pg_authid WHERE rolcanlogin AND rolpassword LIKE 'md5%') AS md5_roles,
 (SELECT count(*) FROM pg_catalog.pg_event_trigger) AS event_triggers,
 (SELECT coalesce(string_agg(extname || '@' || extversion, ' ' ORDER BY extname), '') FROM pg_catalog.pg_extension WHERE extname <> 'plpgsql') AS extensions,
 (SELECT sum(pg_catalog.pg_database_size(datname))::bigint FROM pg_catalog.pg_database WHERE datallowconn) AS size_bytes,
 (SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE ` + pgUserRels + `) AS user_tables
`

func parsePGRuntime(out []byte) (*pgRuntime, error) {
	cols, rows, err := dbquery.ParseCSV(out, dbquery.NullMarker, false)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, errors.New("unexpected preflight result")
	}
	get := map[string]any{}
	for i, c := range cols {
		get[c] = rows[0][i]
	}
	num := func(k string) int64 {
		if n := cellInt(get[k]); n != nil {
			return *n
		}
		return 0
	}
	vn := num("version_num")
	if vn < 100000 {
		return nil, fmt.Errorf("invalid server_version_num %v", get["version_num"])
	}
	rt := &pgRuntime{Major: int(vn / 10000), RegColumns: num("reg_columns"), AclColumns: num("aclitem_columns"),
		PreparedXacts: num("prepared_xacts"), LogicalSlots: num("logical_slots"), MD5Roles: num("md5_roles"),
		EventTriggers: num("event_triggers"), SizeBytes: cellInt(get["size_bytes"]), UserTables: num("user_tables")}
	if ext := strings.TrimSpace(cellStr(get["extensions"])); ext != "" {
		rt.Extensions = strings.Fields(ext)
	}
	return rt, nil
}

// postgresRuntime은 실행 중인 DB 컨테이너 안에서 psql로 preflight 쿼리를 실행한다.
func (s *Server) postgresRuntime(ctx context.Context, in *store.Instance) (*pgRuntime, error) {
	d, container, _, msg := s.execTarget(ctx, in, "")
	if d == nil {
		return nil, errors.New(msg)
	}
	env := d.Env
	if len(env) == 0 {
		res, err := s.Deploy.Resolve(ctx, in, nil)
		if err != nil {
			return nil, err
		}
		env = res.Vars
	}
	cmd, err := (dbquery.Target{Kind: "postgres", Port: 5432, Vars: env, Tag: dbquery.NewTag()}).SQL(pgPreflightSQL)
	if err != nil {
		return nil, err
	}
	out, err := s.dataBackend().ExecRun(ctx, d.NodeID, container, node.ExecRunRequest{
		Cmd: cmd.Cmd, Env: cmd.Env, Stdin: cmd.Stdin, MaxOutput: dbquery.MaxOutput,
	}, dbquery.TimeoutS*time.Second)
	if err != nil {
		return nil, err
	}
	if out.ExitCode != 0 {
		msg := trimMsg(out.Stderr)
		if msg == "" {
			msg = fmt.Sprintf("psql exit code %d", out.ExitCode)
		}
		return nil, errors.New(msg)
	}
	return parsePGRuntime(out.Stdout)
}

// pgVolumeLayout은 볼륨이 aven 기본 구성(이전 경로 또는 업그레이드 뒤 경로)인지 본다.
func pgVolumeLayout(mount, pgdata string) bool {
	return mount == pgLegacyMount && pgdata == pgLegacyPGData || mount == pgUpgradeMount && pgdata == pgUpgradePGData
}

func (s *Server) buildPostgresUpgradePreflight(ctx context.Context, c *instCtx, target int) *postgresPreflight {
	plan := &postgresPreflight{Ready: true, TargetMajor: target, Checks: []postgresCheck{}, AvailableTargets: []int{}, CheckedAt: time.Now().UTC()}
	imageMajor, supported := postgresImageMajor(c.in.Spec.Image)
	image := ""
	if c.in.Spec.Image != nil {
		image = *c.in.Spec.Image
	}
	plan.add("image", "PostgreSQL 이미지", "blocker", supported,
		fmt.Sprintf("메이저 버전이 고정된 공식 postgres alpine 이미지(예: postgres:17-alpine)여야 합니다. 현재: %s", image))
	if supported {
		plan.CurrentMajor = imageMajor
		plan.AvailableTargets = pgTargets(imageMajor)
		plan.add("source", "현재 버전", "blocker", imageMajor >= minPGSourceMajor && imageMajor < maxPGMajor,
			fmt.Sprintf("PostgreSQL %d에서 %d까지의 업그레이드를 지원합니다(현재 %d)", minPGSourceMajor, maxPGMajor, imageMajor))
	}
	if plan.TargetMajor == 0 {
		plan.TargetMajor = maxPGMajor
	}
	plan.add("target", "대상 버전", "blocker", supported && plan.TargetMajor > imageMajor && plan.TargetMajor <= maxPGMajor,
		fmt.Sprintf("대상 메이저는 현재보다 높고 %d 이하여야 합니다(대상 %d)", maxPGMajor, plan.TargetMajor))

	plan.add("status", "서비스 상태", "blocker", c.in.Status == store.InstOnline, "데이터베이스가 실행 중이어야 합니다")
	plan.add("replicas", "replica", "blocker", c.in.Spec.Replicas <= 1 && len(c.in.Spec.Placement) <= 1, "replica 하나로 실행 중이어야 합니다")
	plan.add("port", "포트", "blocker", c.in.Spec.Port != nil && *c.in.Spec.Port == 5432, "기본 PostgreSQL 포트 5432를 써야 합니다")
	plan.add("command", "시작 명령", "blocker", c.in.Spec.StartCommand == nil || strings.TrimSpace(*c.in.Spec.StartCommand) == "",
		"사용자 지정 시작 명령을 지운 뒤 다시 시도하세요")

	vols, err := s.Store.ListVolumesByInstance(ctx, c.in.ID)
	volumeOK := err == nil && len(vols) == 1 && pgVolumeLayout(vols[0].MountPath, c.in.Variables["PGDATA"])
	plan.add("volume", "데이터 볼륨", "blocker", volumeOK,
		fmt.Sprintf("볼륨 하나가 %s에 마운트되고 PGDATA가 %s여야 합니다(aven 기본 구성)", pgLegacyMount, pgLegacyPGData))

	state, err := postgresState(ctx, s.Store, c.in.ID)
	opOK := err == nil && !pgUpgradeInProgress(state.UpgradeStatus) && state.UpgradeStatus != "revert_failed" && !s.postgresBusy(c.in.ID)
	plan.add("operation", "진행 중인 작업", "blocker", opOK, "다른 업그레이드/되돌리기/PgBouncer 작업이 끝난 뒤 다시 시도하세요")

	depOK := false
	if c.in.CurrentDeploymentID != nil {
		if d, err := s.Store.GetDeployment(ctx, *c.in.CurrentDeploymentID); err == nil {
			depOK = d.Status == store.DepLive
		}
	}
	plan.add("deployment", "현재 배포", "blocker", depOK, "현재 배포가 정상(live) 상태여야 합니다")

	if !(c.in.Status == store.InstOnline && depOK && supported) {
		plan.add("server", "서버 검사", "blocker", false, "실행 중인 PostgreSQL에 연결할 수 없어 데이터 검사를 하지 못했습니다")
		return plan
	}
	rt, err := s.postgresRuntime(ctx, c.in)
	if err != nil {
		plan.add("server", "서버 검사", "blocker", false, "PostgreSQL에서 검사 쿼리를 실행하지 못했습니다: "+err.Error())
		return plan
	}
	plan.userTables = int(rt.UserTables)
	plan.add("server", "실행 중인 버전", "blocker", rt.Major == imageMajor,
		fmt.Sprintf("실행 중인 서버는 PostgreSQL %d입니다(이미지 %d와 같아야 합니다)", rt.Major, imageMajor))
	below16 := rt.Major < 16 && plan.TargetMajor >= 16
	plan.add("reg-types", "reg* 열", "blocker", rt.RegColumns == 0,
		fmt.Sprintf("regproc 같은 OID 참조 형식 열 %d개는 pg_upgrade가 보존할 수 없습니다. 다른 형식(text 등)으로 바꾸세요", rt.RegColumns))
	if below16 {
		plan.add("aclitem", "aclitem 열", "blocker", rt.AclColumns == 0,
			fmt.Sprintf("aclitem 열 %d개: PostgreSQL 16에서 내부 형식이 바뀌어 업그레이드할 수 없습니다", rt.AclColumns))
	}
	plan.add("prepared-xacts", "prepared 트랜잭션", "blocker", rt.PreparedXacts == 0,
		fmt.Sprintf("열린 2단계 커밋 트랜잭션 %d개를 COMMIT PREPARED 또는 ROLLBACK PREPARED로 끝내세요", rt.PreparedXacts))
	plan.add("logical-slots", "논리 복제 슬롯", "warning", rt.LogicalSlots == 0,
		fmt.Sprintf("논리 복제 슬롯 %d개는 옮겨지지 않습니다. 업그레이드 뒤 소비자가 다시 만들어야 합니다", rt.LogicalSlots))
	plan.add("md5", "비밀번호 인증", "warning", rt.MD5Roles == 0,
		fmt.Sprintf("md5 비밀번호를 쓰는 로그인 역할 %d개: SCRAM으로 바꾸세요(ALTER ROLE ... PASSWORD)", rt.MD5Roles))
	plan.add("event-triggers", "이벤트 트리거", "warning", rt.EventTriggers == 0,
		fmt.Sprintf("이벤트 트리거 %d개: 카탈로그 복원과 호환되는지 확인하세요", rt.EventTriggers))

	var missing, installed []string
	for _, e := range rt.Extensions {
		name, _, _ := strings.Cut(e, "@")
		if rm, ok := pgRemovedExtensions[name]; ok && plan.TargetMajor >= rm || !ok && !pgContribExtensions[name] {
			missing = append(missing, name)
		} else {
			installed = append(installed, e)
		}
	}
	sort.Strings(missing)
	extMsg := "기본 확장만 설치돼 있습니다"
	if len(installed) > 0 {
		extMsg = "대상 이미지에 들어 있습니다: " + strings.Join(installed, ", ") + ". 업그레이드 뒤 ALTER EXTENSION ... UPDATE로 새 버전을 적용하세요"
	}
	if len(missing) > 0 {
		extMsg = "대상 이미지에 없는 확장: " + strings.Join(missing, ", ") + ". 쓰지 않는다면 DROP EXTENSION으로 지우세요"
	}
	plan.add("extensions", "확장", "blocker", len(missing) == 0, extMsg)

	plan.DataSizeBytes = rt.SizeBytes
	sizeMsg := "데이터 크기를 알 수 없습니다"
	sizeOK := true
	if rt.SizeBytes != nil {
		sizeMsg = fmt.Sprintf("데이터 %s: 업그레이드 전 볼륨 백업과 pg_upgrade 작업 공간이 필요합니다", deploy.HumanBytes(*rt.SizeBytes))
		if volumeOK && vols[0].SizeLimitMB != nil && *rt.SizeBytes*2 > int64(*vols[0].SizeLimitMB)<<20 {
			sizeOK = false
			sizeMsg += fmt.Sprintf(". 볼륨 한도(%d MB)가 데이터의 두 배보다 작아 공간이 부족할 수 있습니다", *vols[0].SizeLimitMB)
		}
	}
	plan.add("size", "데이터 크기", map[bool]string{true: "info", false: "warning"}[sizeOK], sizeOK, sizeMsg)
	return plan
}

func (s *Server) postgresUpgradePreflight(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadPostgres(w, r)
	if !ok {
		return
	}
	target := 0
	if raw := r.URL.Query().Get("to"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			writeErr(w, 400, "to must be a PostgreSQL major version, for example 18")
			return
		}
		target = n
	}
	writeJSON(w, 200, s.buildPostgresUpgradePreflight(r.Context(), c, target))
}

func (s *Server) upgradeView(ctx context.Context, c *instCtx, st *store.PostgresState) postgresUpgradeView {
	v := postgresUpgradeView{Status: st.UpgradeStatus, FromMajor: st.UpgradeFromMajor, TargetMajor: st.UpgradeToMajor,
		FromImage: st.UpgradeFromImage, BackupID: st.UpgradeBackupID, PostBackupID: st.UpgradePostBackupID,
		DeploymentID: st.UpgradeDeploymentID, Error: st.UpgradeError, StartedAt: st.UpgradeStartedAt, FinishedAt: st.UpgradeFinishedAt,
		InProgress: pgUpgradeInProgress(st.UpgradeStatus), AvailableMajors: []int{}}
	if v.Status == "" {
		v.Status = "none"
	}
	if c.in.Spec.Image != nil {
		v.Image = *c.in.Spec.Image
	}
	if m, ok := postgresImageMajor(c.in.Spec.Image); ok {
		v.CurrentMajor = m
		v.AvailableMajors = pgTargets(m)
	}
	v.CanRevert, v.RevertDeadline = s.canRevertPostgres(ctx, st)
	return v
}

// canRevertPostgres는 업그레이드 전 백업으로 되돌릴 수 있는지와 (성공한 업그레이드면) 마감 시각을 돌려준다.
func (s *Server) canRevertPostgres(ctx context.Context, st *store.PostgresState) (bool, *time.Time) {
	if st.UpgradeBackupID == "" || st.UpgradeFromImage == "" {
		return false, nil
	}
	var deadline *time.Time
	switch st.UpgradeStatus {
	case "succeeded":
		if st.UpgradeFinishedAt != nil {
			d := st.UpgradeFinishedAt.Add(pgRevertWindow)
			deadline = &d
			if time.Now().After(d) {
				return false, deadline
			}
		}
	case "failed", "revert_failed":
	default:
		return false, nil
	}
	b, err := s.Store.GetBackup(ctx, st.UpgradeBackupID)
	if err != nil || b.Status != "done" {
		return false, deadline
	}
	return true, deadline
}

func (s *Server) getPostgresUpgrade(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadPostgres(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	st, err := postgresState(ctx, s.Store, c.in.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if pgUpgradeInProgress(st.UpgradeStatus) && !s.postgresBusy(c.in.ID) {
		s.resumePostgresUpgrade(context.WithoutCancel(ctx), c, st)
	}
	writeJSON(w, 200, s.upgradeView(ctx, c, st))
}

// resumePostgresUpgrade는 서버 재시작으로 끊긴 업그레이드/되돌리기를 이어서 마무리한다.
func (s *Server) resumePostgresUpgrade(ctx context.Context, c *instCtx, st *store.PostgresState) {
	op := "upgrade"
	if st.UpgradeStatus == "reverting" {
		op = "revert"
	}
	release, _, ok := s.tryPostgresOp(c.in.ID, op)
	if !ok {
		return
	}
	switch {
	case st.UpgradeStatus == "preparing":
		// 백업 도중 끊겼다. 설정은 아직 바꾸지 않았으므로 원래 이미지로 다시 띄운다(백업이 끝났으면 되돌리기도 할 수 있다).
		release()
		st.UpgradeStatus, st.UpgradeError = "failed", "aven 서버가 다시 시작되어 업그레이드 준비가 중단되었습니다"
		s.finishUpgradeState(ctx, st)
		if st.UpgradeBackupID == "" || !s.postgresConfigChanged(ctx, c, st) {
			s.restartPostgres(ctx, c.in.ID)
		}
	case st.UpgradeDeploymentID == "":
		release()
		st.UpgradeStatus = map[string]string{"upgrading": "failed", "reverting": "revert_failed"}[st.UpgradeStatus]
		st.UpgradeError = "aven 서버가 다시 시작되어 작업이 중단되었습니다"
		s.finishUpgradeState(ctx, st)
	case st.UpgradeStatus == "upgrading":
		go func() {
			defer release()
			s.watchUpgrade(ctx, c, st.UpgradeDeploymentID, nil)
		}()
	default:
		go func() {
			defer release()
			s.watchRevert(ctx, c, st.UpgradeDeploymentID, st.UpgradeError)
		}()
	}
}

// postgresConfigChanged는 업그레이드용 설정(이미지)이 이미 적용됐는지 본다.
func (s *Server) postgresConfigChanged(ctx context.Context, c *instCtx, st *store.PostgresState) bool {
	in, err := s.Store.GetInstance(ctx, c.in.ID)
	return err == nil && in.Spec.Image != nil && *in.Spec.Image != st.UpgradeFromImage
}

func (s *Server) finishUpgradeState(ctx context.Context, st *store.PostgresState) {
	now := time.Now().UTC()
	st.UpgradeFinishedAt = &now
	if err := s.Store.UpdatePostgresState(ctx, st); err != nil {
		s.Log.Error("save postgres upgrade state", "instance", st.InstanceID, "err", err)
	}
}

// restartPostgres는 멈춘 DB를 지금 설정으로 다시 배포한다(업그레이드 준비가 실패했을 때).
func (s *Server) restartPostgres(ctx context.Context, instanceID string) {
	in, err := s.Store.GetInstance(ctx, instanceID)
	if err != nil || in.Status != store.InstOffline {
		return
	}
	if _, err := s.pg().Enqueue(ctx, in, deploy.EnqueueOpts{Trigger: "postgres-upgrade-recovery"}); err != nil {
		s.Log.Error("restart PostgreSQL after failed upgrade preparation", "instance", in.ID, "err", err)
	}
}

func (s *Server) startPostgresUpgrade(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadPostgres(w, r)
	if !ok {
		return
	}
	var body struct {
		ToMajor int `json:"to_major"`
	}
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	ctx := context.WithoutCancel(r.Context())
	plan := s.buildPostgresUpgradePreflight(ctx, c, body.ToMajor)
	release, cur, ok := s.tryPostgresOp(c.in.ID, "upgrade")
	if !ok {
		writePostgresBusy(w, cur)
		return
	}
	if !plan.Ready {
		release()
		writeJSON(w, 409, map[string]any{"error": "preflight 검사에 blocker가 있어 업그레이드를 시작하지 않았습니다", "preflight": plan})
		return
	}
	vols, err := s.Store.ListVolumesByInstance(ctx, c.in.ID)
	if err != nil || len(vols) != 1 {
		release()
		writeErr(w, 409, "PostgreSQL 데이터 볼륨을 찾을 수 없습니다")
		return
	}
	st, err := postgresState(ctx, s.Store, c.in.ID)
	if err != nil {
		release()
		s.storeErr(w, err)
		return
	}
	now := time.Now().UTC()
	st.UpgradeStatus, st.UpgradeError = "preparing", ""
	st.UpgradeFromMajor, st.UpgradeToMajor = plan.CurrentMajor, plan.TargetMajor
	st.UpgradeFromImage, st.UpgradeFromPGData, st.UpgradeFromMount = *c.in.Spec.Image, c.in.Variables["PGDATA"], vols[0].MountPath
	st.UpgradeBackupID, st.UpgradePostBackupID, st.UpgradeDeploymentID = "", "", ""
	st.UpgradeStartedAt, st.UpgradeFinishedAt = &now, nil
	if err := s.Store.UpdatePostgresState(ctx, st); err != nil {
		release()
		s.storeErr(w, err)
		return
	}
	s.emitPostgres(ctx, c, "postgres.upgrade.started", fmt.Sprintf("PostgreSQL %d → %d 업그레이드를 시작합니다(데이터베이스가 잠시 멈춥니다)", plan.CurrentMajor, plan.TargetMajor))
	go func() {
		defer release()
		s.runPostgresUpgrade(ctx, c, vols[0], plan)
	}()
	writeJSON(w, 202, s.upgradeView(ctx, c, st))
}

// runPostgresUpgrade: 멈춤 → 볼륨 백업 → pgautoupgrade 이미지로 배포 → 확인.
func (s *Server) runPostgresUpgrade(ctx context.Context, c *instCtx, vol *store.Volume, plan *postgresPreflight) {
	st, err := postgresState(ctx, s.Store, c.in.ID)
	if err != nil {
		return
	}
	fail := func(msg string, restart bool) {
		st.UpgradeStatus, st.UpgradeError = "failed", msg
		s.finishUpgradeState(ctx, st)
		s.emitPostgres(ctx, c, "postgres.upgrade.failed", msg)
		if restart {
			s.restartPostgres(ctx, c.in.ID)
		}
	}
	if err := s.pg().Stop(ctx, c.in); err != nil {
		fail("백업 전에 PostgreSQL을 멈추지 못했습니다: "+err.Error(), true)
		return
	}
	backup, err := s.pg().CreateBackupSync(ctx, vol)
	if err != nil {
		fail("업그레이드 전 볼륨 백업에 실패했습니다: "+err.Error(), true)
		return
	}
	st.UpgradeBackupID = backup.ID
	if err := s.Store.UpdatePostgresState(ctx, st); err != nil {
		fail("상태를 저장하지 못했습니다: "+err.Error(), true)
		return
	}
	if err := s.reloadInstCtx(ctx, c); err != nil {
		fail(err.Error(), true)
		return
	}
	if vol.MountPath == pgLegacyMount {
		vol.MountPath = pgUpgradeMount
		if err := s.Store.UpdateVolume(ctx, vol); err != nil {
			fail("볼륨 마운트 경로를 바꾸지 못했습니다: "+err.Error(), true)
			return
		}
		c.in.Variables["PGDATA"] = pgUpgradePGData
	}
	image := pgUpgradeImage(st.UpgradeToMajor)
	c.in.Spec.Image = &image
	if err := s.Store.UpdateInstanceConfig(ctx, c.in); err != nil {
		s.restorePostgresConfig(ctx, c, st)
		fail("인스턴스 설정을 바꾸지 못했습니다: "+err.Error(), true)
		return
	}
	// pg_upgrade가 도는 동안 포트가 열리지 않으므로 이 배포만 healthcheck를 길게 기다린다.
	sp := c.in.Spec
	if sp.HealthcheckTimeoutS < pgUpgradeHealthcheck {
		sp.HealthcheckTimeoutS = pgUpgradeHealthcheck
	}
	req := c.in.Spec
	dep, err := s.pg().Enqueue(ctx, c.in, deploy.EnqueueOpts{Trigger: "postgres-upgrade", Spec: &sp, ReqSpec: &req})
	if err != nil {
		s.restorePostgresConfig(ctx, c, st)
		fail("업그레이드 배포를 시작하지 못했습니다: "+err.Error(), true)
		return
	}
	st.UpgradeStatus, st.UpgradeDeploymentID = "upgrading", dep.ID
	if err := s.Store.UpdatePostgresState(ctx, st); err != nil {
		s.Log.Error("save postgres upgrade state", "err", err)
	}
	s.watchUpgrade(ctx, c, dep.ID, plan)
}

// watchUpgrade는 업그레이드 배포를 기다려 확인하고, 실패하면 업그레이드 전 백업으로 자동 복구한다.
func (s *Server) watchUpgrade(ctx context.Context, c *instCtx, depID string, plan *postgresPreflight) {
	err := s.waitPostgresDeployment(ctx, depID, pgUpgradeWait)
	st, serr := postgresState(ctx, s.Store, c.in.ID)
	if serr != nil || st.UpgradeDeploymentID != depID {
		return
	}
	if err == nil {
		err = s.verifyPostgresUpgrade(ctx, c, st.UpgradeToMajor, plan)
	}
	if err != nil {
		msg := "업그레이드 실패: " + err.Error()
		s.emitPostgres(ctx, c, "postgres.upgrade.failed", msg+" · 업그레이드 전 백업으로 자동 복구합니다")
		st.UpgradeStatus, st.UpgradeError = "failed", msg
		s.Store.UpdatePostgresState(ctx, st)
		s.runPostgresRevert(ctx, c, msg)
		return
	}
	st.UpgradeStatus, st.UpgradeError = "succeeded", ""
	// 새 메이저로 논리 백업을 하나 더 만든다(업그레이드 뒤 복원 지점).
	if b, err := s.pg().CreateDBBackup(ctx, c.in, "postgres-upgrade", ""); err == nil {
		st.UpgradePostBackupID = b.ID
	} else {
		s.Log.Warn("post-upgrade backup", "instance", c.in.ID, "err", err)
	}
	s.finishUpgradeState(ctx, st)
	s.emitPostgres(ctx, c, "postgres.upgrade.succeeded", fmt.Sprintf("PostgreSQL %d로 업그레이드했습니다. %d일 동안 업그레이드 전 백업으로 되돌릴 수 있습니다",
		st.UpgradeToMajor, int(pgRevertWindow.Hours()/24)))
}

// verifyPostgresUpgrade는 새 서버가 대상 메이저로 돌고 데이터(사용자 표)가 남아 있는지 확인한다.
func (s *Server) verifyPostgresUpgrade(ctx context.Context, c *instCtx, target int, plan *postgresPreflight) error {
	var last error
	for i := 0; i < 5; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(3 * postgresPoll):
			}
		}
		if err := s.reloadInstCtx(ctx, c); err != nil {
			return err
		}
		rt, err := s.postgresRuntime(ctx, c.in)
		if err != nil {
			last = err
			continue
		}
		if rt.Major != target {
			return fmt.Errorf("서버가 PostgreSQL %d로 실행 중입니다(대상 %d)", rt.Major, target)
		}
		if plan != nil && plan.userTables > 0 && rt.UserTables == 0 {
			return errors.New("업그레이드 뒤 사용자 표가 보이지 않습니다(빈 데이터 디렉터리로 시작했을 수 있습니다)")
		}
		return nil
	}
	return fmt.Errorf("업그레이드한 서버를 확인하지 못했습니다: %v", last)
}

// restorePostgresConfig는 이미지/PGDATA/마운트 경로를 업그레이드 전 값으로 돌린다.
func (s *Server) restorePostgresConfig(ctx context.Context, c *instCtx, st *store.PostgresState) error {
	in, err := s.Store.GetInstance(ctx, c.in.ID)
	if err != nil {
		return err
	}
	img := st.UpgradeFromImage
	in.Spec.Image = &img
	if in.Variables == nil {
		in.Variables = map[string]string{}
	}
	if st.UpgradeFromPGData == "" {
		delete(in.Variables, "PGDATA")
	} else {
		in.Variables["PGDATA"] = st.UpgradeFromPGData
	}
	if vols, err := s.Store.ListVolumesByInstance(ctx, in.ID); err == nil {
		for _, v := range vols {
			if st.UpgradeFromMount != "" && v.MountPath != st.UpgradeFromMount && (v.MountPath == pgUpgradeMount || v.MountPath == pgLegacyMount) {
				v.MountPath = st.UpgradeFromMount
				if err := s.Store.UpdateVolume(ctx, v); err != nil {
					return err
				}
			}
		}
	}
	if err := s.Store.UpdateInstanceConfig(ctx, in); err != nil {
		return err
	}
	c.in = in
	return nil
}

func (s *Server) revertPostgresUpgrade(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadPostgres(w, r)
	if !ok {
		return
	}
	ctx := context.WithoutCancel(r.Context())
	release, cur, ok := s.tryPostgresOp(c.in.ID, "revert")
	if !ok {
		writePostgresBusy(w, cur)
		return
	}
	st, err := postgresState(ctx, s.Store, c.in.ID)
	if err != nil {
		release()
		s.storeErr(w, err)
		return
	}
	if can, deadline := s.canRevertPostgres(ctx, st); !can {
		release()
		msg := "되돌릴 업그레이드가 없습니다(업그레이드 전 백업이 필요합니다)"
		if deadline != nil && time.Now().After(*deadline) {
			msg = "되돌리기 기간이 지났습니다"
		}
		writeErr(w, 409, msg)
		return
	}
	prevErr := st.UpgradeError
	st.UpgradeStatus = "reverting"
	st.UpgradeDeploymentID = ""
	if err := s.Store.UpdatePostgresState(ctx, st); err != nil {
		release()
		s.storeErr(w, err)
		return
	}
	s.emitPostgres(ctx, c, "postgres.upgrade.reverting", fmt.Sprintf("업그레이드 전 백업으로 되돌립니다(PostgreSQL %d)", st.UpgradeFromMajor))
	go func() {
		defer release()
		s.runPostgresRevert(ctx, c, prevErr)
	}()
	writeJSON(w, 202, s.upgradeView(ctx, c, st))
}

// runPostgresRevert: 멈춤 → 업그레이드 전 볼륨 백업 복원 → 원래 이미지/경로로 배포.
// keepErr는 자동 복구일 때 원래 업그레이드 실패 이유다(결과 메시지에 남긴다).
func (s *Server) runPostgresRevert(ctx context.Context, c *instCtx, keepErr string) {
	st, err := postgresState(ctx, s.Store, c.in.ID)
	if err != nil {
		return
	}
	fail := func(msg string) {
		if keepErr != "" {
			msg = keepErr + " · 자동 복구 실패: " + msg
		}
		st.UpgradeStatus, st.UpgradeError = "revert_failed", msg
		s.finishUpgradeState(ctx, st)
		s.emitPostgres(ctx, c, "postgres.upgrade.revert_failed", msg)
	}
	st.UpgradeStatus, st.UpgradeDeploymentID = "reverting", ""
	s.Store.UpdatePostgresState(ctx, st)
	backup, err := s.Store.GetBackup(ctx, st.UpgradeBackupID)
	if err != nil || backup.Status != "done" {
		fail("업그레이드 전 볼륨 백업을 찾을 수 없습니다")
		return
	}
	if err := s.reloadInstCtx(ctx, c); err != nil {
		fail(err.Error())
		return
	}
	if err := s.pg().Stop(ctx, c.in); err != nil {
		fail("PostgreSQL을 멈추지 못했습니다: " + err.Error())
		return
	}
	if err := s.pg().RestoreBackupSync(ctx, backup); err != nil {
		fail("볼륨 백업을 복원하지 못했습니다: " + err.Error())
		return
	}
	if err := s.restorePostgresConfig(ctx, c, st); err != nil {
		fail("원래 설정으로 되돌리지 못했습니다: " + err.Error())
		return
	}
	dep, err := s.pg().Enqueue(ctx, c.in, deploy.EnqueueOpts{Trigger: "postgres-upgrade-revert"})
	if err != nil {
		fail("원래 버전으로 배포하지 못했습니다: " + err.Error())
		return
	}
	st.UpgradeDeploymentID = dep.ID
	s.Store.UpdatePostgresState(ctx, st)
	s.watchRevert(ctx, c, dep.ID, keepErr)
}

func (s *Server) watchRevert(ctx context.Context, c *instCtx, depID, keepErr string) {
	err := s.waitPostgresDeployment(ctx, depID, pgUpgradeWait)
	st, serr := postgresState(ctx, s.Store, c.in.ID)
	if serr != nil || st.UpgradeDeploymentID != depID {
		return
	}
	if err != nil {
		msg := "원래 버전 배포 실패: " + err.Error()
		if keepErr != "" {
			msg = keepErr + " · 자동 복구 실패: " + msg
		}
		st.UpgradeStatus, st.UpgradeError = "revert_failed", msg
		s.finishUpgradeState(ctx, st)
		s.emitPostgres(ctx, c, "postgres.upgrade.revert_failed", msg)
		return
	}
	st.UpgradeStatus = "reverted"
	if keepErr != "" {
		st.UpgradeError = keepErr + " · 업그레이드 전 백업으로 자동 복구했습니다"
	} else {
		st.UpgradeError = ""
	}
	s.finishUpgradeState(ctx, st)
	s.emitPostgres(ctx, c, "postgres.upgrade.reverted", fmt.Sprintf("PostgreSQL %d로 되돌렸습니다(업그레이드 전 백업 %s)", st.UpgradeFromMajor, st.UpgradeBackupID))
}
