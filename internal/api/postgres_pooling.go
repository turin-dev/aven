package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/catalog"
	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/store"
	"github.com/turin-dev/aven/internal/vars"
)

// 켜면 aven이 <db>-pgbouncer 이미지 서비스를 만들어 배포하고, 배포가 live가 된 뒤 DB 서비스의 연결 변수를 바꾼다:
//
//	DATABASE_URL                 → PgBouncer(비공개 네트워크)
//	DATABASE_PUBLIC_URL          → PgBouncer TCP proxy(DB가 공개돼 있었을 때만)
//	DATABASE_UNPOOLED_URL        → PostgreSQL 직접 연결(마이그레이션, LISTEN/NOTIFY, advisory lock)
//	DATABASE_PUBLIC_UNPOOLED_URL → PostgreSQL TCP proxy 직접 연결
//
// 다른 서비스는 ${{db.DATABASE_URL}} 참조를 그대로 쓰므로 자동으로 PgBouncer를 가리키게 되고, 실행 중인 참조 서비스는 다시 배포한다.

const (
	pgBouncerImage      = "edoburu/pgbouncer:v1.25.2-p0"
	pgBouncerPort       = 5432
	pgBouncerDeployWait = 15 * time.Minute
)

var pgPoolModes = map[string]bool{"transaction": true, "session": true, "statement": true}

type postgresPoolingView struct {
	// Status: off | deploying(처음 켜는 중) | updating(설정 바꾸는 중) | enabled | failed
	Status                string   `json:"status"`
	Enabled               bool     `json:"enabled"`
	PoolMode              string   `json:"pool_mode"`
	DefaultPoolSize       int      `json:"default_pool_size"`
	MaxClientConn         int      `json:"max_client_conn"`
	MaxPreparedStatements *int     `json:"max_prepared_statements"` // null이면 PgBouncer 기본값(200)
	PoolerServiceID       string   `json:"pooler_service_id,omitempty"`
	PoolerServiceName     string   `json:"pooler_service_name,omitempty"`
	DeploymentID          string   `json:"deployment_id,omitempty"`
	Error                 string   `json:"error,omitempty"`
	Variables             []string `json:"variables"` // 지금 DB 서비스에 있는 연결 변수 이름
}

func (s *Server) poolingView(ctx context.Context, p *store.PostgresState, dbIn *store.Instance) postgresPoolingView {
	v := postgresPoolingView{Status: p.PoolingStatus, Enabled: p.PoolingStatus == "enabled" || p.PoolingStatus == "updating",
		PoolMode: p.PoolMode, DefaultPoolSize: p.DefaultPoolSize, MaxClientConn: p.MaxClientConn,
		PoolerServiceID: p.PoolerServiceID, DeploymentID: p.PoolerDeploymentID, Error: p.PoolingError, Variables: []string{}}
	if v.Status == "" {
		v.Status = "off"
	}
	if p.MaxPreparedStatements >= 0 {
		n := p.MaxPreparedStatements
		v.MaxPreparedStatements = &n
	}
	if p.PoolerServiceID != "" {
		if svc, err := s.Store.GetService(ctx, p.PoolerServiceID); err == nil {
			v.PoolerServiceName = svc.Name
		}
	}
	for _, k := range []string{"DATABASE_URL", "DATABASE_PUBLIC_URL", "DATABASE_UNPOOLED_URL", "DATABASE_PUBLIC_UNPOOLED_URL"} {
		if dbIn != nil && dbIn.Variables[k] != "" {
			v.Variables = append(v.Variables, k)
		}
	}
	return v
}

func validPostgresPoolSettings(mode string, poolSize, maxClients, maxPrepared int) error {
	if !pgPoolModes[mode] {
		return errors.New("pool_mode must be transaction, session or statement")
	}
	if poolSize < 1 || poolSize > 10000 {
		return errors.New("default_pool_size must be 1..10000")
	}
	if maxClients < 1 || maxClients > 100000 {
		return errors.New("max_client_conn must be 1..100000")
	}
	if maxPrepared < -1 || maxPrepared > 100000 {
		return errors.New("max_prepared_statements must be 0..100000 (null: PgBouncer default)")
	}
	return nil
}

func pgbouncerServiceName(dbName string) string {
	const suffix = "-pgbouncer"
	if len(dbName)+len(suffix) > 40 {
		dbName = strings.TrimRight(dbName[:40-len(suffix)], "-")
	}
	return dbName + suffix
}

// pgbouncerVars는 edoburu/pgbouncer 이미지 설정 변수다. 접속 정보는 DB 서비스 변수 참조라 비밀번호가 바뀌어도 따라간다.
func pgbouncerVars(databaseName string, p *store.PostgresState) map[string]string {
	ref := func(k string) string { return "${{" + databaseName + "." + k + "}}" }
	v := map[string]string{
		"DB_HOST":           ref("PGHOST"),
		"DB_PORT":           ref("PGPORT"),
		"DB_USER":           ref("PGUSER"),
		"DB_PASSWORD":       ref("PGPASSWORD"),
		"DB_NAME":           ref("PGDATABASE"),
		"LISTEN_PORT":       strconv.Itoa(pgBouncerPort),
		"POOL_MODE":         p.PoolMode,
		"DEFAULT_POOL_SIZE": strconv.Itoa(p.DefaultPoolSize),
		"MAX_CLIENT_CONN":   strconv.Itoa(p.MaxClientConn),
		"AUTH_TYPE":         "scram-sha-256",
	}
	if p.MaxPreparedStatements >= 0 {
		v["MAX_PREPARED_STATEMENTS"] = strconv.Itoa(p.MaxPreparedStatements)
	}
	return v
}

// pgbouncerManagedKeys는 aven이 관리하는 PgBouncer 변수다(사용자가 더한 다른 변수는 건드리지 않는다).
var pgbouncerManagedKeys = []string{"DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME", "LISTEN_PORT", "POOL_MODE",
	"DEFAULT_POOL_SIZE", "MAX_CLIENT_CONN", "AUTH_TYPE", "MAX_PREPARED_STATEMENTS"}

func applyPgbouncerVars(in *store.Instance, databaseName string, p *store.PostgresState) {
	if in.Variables == nil {
		in.Variables = map[string]string{}
	}
	for _, k := range pgbouncerManagedKeys {
		delete(in.Variables, k)
	}
	for k, v := range pgbouncerVars(databaseName, p) {
		in.Variables[k] = v
	}
}

func pooledURL(poolerName string, public bool) string {
	host, port := "${{"+poolerName+".KEEL_PRIVATE_DOMAIN}}", strconv.Itoa(pgBouncerPort)
	if public {
		host, port = "${{"+poolerName+".KEEL_TCP_PROXY_DOMAIN}}", "${{"+poolerName+".KEEL_TCP_PROXY_PORT}}"
	}
	return "postgresql://${{PGUSER}}:${{PGPASSWORD}}@" + host + ":" + port + "/${{PGDATABASE}}"
}

// directPostgresURL은 직접 연결 URL을 잃었을 때 쓰는 기본값이다(catalog의 postgres 변수와 같다).
func directPostgresURL(public bool) string {
	vs := catalog.Databases["postgres"].Variables("")
	if public {
		return vs["DATABASE_PUBLIC_URL"]
	}
	return vs["DATABASE_URL"]
}

// applyPooledVars는 DB 서비스 변수를 PgBouncer를 가리키게 바꾼다. 원래 값은 *_UNPOOLED_URL로 보존한다(여러 번 불러도 같다).
func applyPooledVars(v map[string]string, poolerName string, public bool) {
	pooled := pooledURL(poolerName, false)
	if v["DATABASE_UNPOOLED_URL"] == "" {
		direct := v["DATABASE_URL"]
		if direct == "" || direct == pooled {
			direct = directPostgresURL(false)
		}
		v["DATABASE_UNPOOLED_URL"] = direct
	}
	v["DATABASE_URL"] = pooled
	pub := pooledURL(poolerName, true)
	if public {
		if v["DATABASE_PUBLIC_UNPOOLED_URL"] == "" {
			direct := v["DATABASE_PUBLIC_URL"]
			if direct == "" || direct == pub {
				direct = directPostgresURL(true)
			}
			v["DATABASE_PUBLIC_UNPOOLED_URL"] = direct
		}
		v["DATABASE_PUBLIC_URL"] = pub
	} else if direct := v["DATABASE_PUBLIC_UNPOOLED_URL"]; direct != "" {
		// PgBouncer의 공개 접속이 사라졌으면 공개 URL을 직접 연결로 되돌린다.
		v["DATABASE_PUBLIC_URL"] = direct
		delete(v, "DATABASE_PUBLIC_UNPOOLED_URL")
	}
}

// removePooledVars는 applyPooledVars를 되돌린다.
func removePooledVars(v map[string]string) {
	if direct := v["DATABASE_UNPOOLED_URL"]; direct != "" {
		v["DATABASE_URL"] = direct
		delete(v, "DATABASE_UNPOOLED_URL")
	}
	if direct := v["DATABASE_PUBLIC_UNPOOLED_URL"]; direct != "" {
		v["DATABASE_PUBLIC_URL"] = direct
		delete(v, "DATABASE_PUBLIC_UNPOOLED_URL")
	}
}

func (s *Server) getPostgresPooling(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadPostgres(w, r)
	if !ok {
		return
	}
	p, err := postgresState(r.Context(), s.Store, c.in.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if (p.PoolingStatus == "deploying" || p.PoolingStatus == "updating") && p.PoolerDeploymentID != "" {
		// 서버 재시작으로 마무리 goroutine이 없으면 다시 붙인다.
		if release, _, ok := s.tryPostgresOp(c.in.ID, "pooling"); ok {
			go s.finishPooling(context.WithoutCancel(r.Context()), c, p.PoolerDeploymentID, p.PoolingStatus == "deploying", release)
		}
	}
	writeJSON(w, 200, s.poolingView(r.Context(), p, c.in))
}

// ensurePgBouncer는 이 DB의 PgBouncer 서비스를 찾거나 만든다. 다른 환경에서 이미 만든 서비스가 있으면 그것을 쓴다.
func (s *Server) ensurePgBouncer(ctx context.Context, c *instCtx, state *store.PostgresState) (*store.Service, *store.Instance, error) {
	candidates := []string{state.PoolerServiceID}
	if insts, err := s.Store.ListInstancesByService(ctx, c.svc.ID); err == nil {
		for _, in := range insts {
			if st, err := s.Store.GetPostgresState(ctx, in.ID); err == nil && st != nil {
				candidates = append(candidates, st.PoolerServiceID)
			}
		}
	}
	var svc *store.Service
	for _, id := range candidates {
		if id == "" {
			continue
		}
		if found, err := s.Store.GetService(ctx, id); err == nil && found.ProjectID == c.p.ID && found.SourceType == "image" {
			svc = found
			break
		}
	}
	if svc == nil {
		def := catalog.ServiceDef{Name: pgbouncerServiceName(c.svc.Name), Icon: "pgbouncer", SourceType: "image", Image: pgBouncerImage,
			Port: pgBouncerPort, HealthcheckType: "tcp", Variables: pgbouncerVars(c.svc.Name, state)}
		created, err := s.createServiceFromDef(ctx, c.p, def, nil, "", nil)
		if err != nil {
			return nil, nil, err
		}
		svc = created
		// 캔버스에서 각 환경의 DB 옆에 놓는다.
		for _, pin := range s.mustInstancesByService(ctx, svc.ID) {
			if dbIn, err := s.Store.GetInstanceFor(ctx, c.svc.ID, pin.EnvironmentID); err == nil && dbIn.Canvas != nil {
				s.Store.SetInstanceCanvas(ctx, pin.ID, store.Canvas{X: dbIn.Canvas.X + 320, Y: dbIn.Canvas.Y})
			}
		}
	}
	in, err := s.Store.GetInstanceFor(ctx, svc.ID, c.env.ID)
	if err != nil {
		return nil, nil, err
	}
	state.PoolerServiceID = svc.ID
	return svc, in, nil
}

func (s *Server) mustInstancesByService(ctx context.Context, svcID string) []*store.Instance {
	insts, _ := s.Store.ListInstancesByService(ctx, svcID)
	return insts
}

// putPostgresPooling은 PgBouncer를 켜거나 설정을 바꾼다({"enabled": false}면 제거한다). 배포는 비동기다(202).
func (s *Server) putPostgresPooling(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadPostgres(w, r)
	if !ok {
		return
	}
	var body struct {
		Enabled               *bool  `json:"enabled"`
		PoolMode              string `json:"pool_mode"`
		DefaultPoolSize       int    `json:"default_pool_size"`
		MaxClientConn         int    `json:"max_client_conn"`
		MaxPreparedStatements *int   `json:"max_prepared_statements"`
		// ResetMaxPreparedStatements면 max_prepared_statements를 PgBouncer 기본값으로 되돌린다.
		ResetMaxPreparedStatements bool `json:"reset_max_prepared_statements"`
	}
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	if body.Enabled != nil && !*body.Enabled {
		s.deletePostgresPooling(w, r)
		return
	}
	release, cur, ok := s.tryPostgresOp(c.in.ID, "pooling")
	if !ok {
		writePostgresBusy(w, cur)
		return
	}
	started := false
	defer func() {
		if !started {
			release()
		}
	}()
	ctx := context.WithoutCancel(r.Context())
	state, err := postgresState(ctx, s.Store, c.in.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if body.PoolMode != "" {
		state.PoolMode = body.PoolMode
	}
	if body.DefaultPoolSize != 0 {
		state.DefaultPoolSize = body.DefaultPoolSize
	}
	if body.MaxClientConn != 0 {
		state.MaxClientConn = body.MaxClientConn
	}
	if body.MaxPreparedStatements != nil {
		state.MaxPreparedStatements = *body.MaxPreparedStatements
		if state.MaxPreparedStatements < 0 {
			writeErr(w, 400, "max_prepared_statements must be 0..100000")
			return
		}
	}
	if body.ResetMaxPreparedStatements {
		state.MaxPreparedStatements = -1
	}
	if err := validPostgresPoolSettings(state.PoolMode, state.DefaultPoolSize, state.MaxClientConn, state.MaxPreparedStatements); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if state.UpgradeStatus == "preparing" || state.UpgradeStatus == "upgrading" || state.UpgradeStatus == "reverting" {
		writeErr(w, 409, "메이저 버전 업그레이드가 진행 중입니다. 끝난 뒤 다시 시도하세요")
		return
	}
	pooler, poolIn, err := s.ensurePgBouncer(ctx, c, state)
	if err != nil {
		writeErr(w, 409, "PgBouncer 서비스를 만들지 못했습니다: "+err.Error())
		return
	}
	applyPgbouncerVars(poolIn, c.svc.Name, state)
	if c.in.TCPProxy != nil && poolIn.TCPProxy == nil { // DB가 공개돼 있었으면 공개 접속을 PgBouncer로 넘긴다
		port, err := s.Deploy.AllocateTCPPortFor(ctx, s.Deploy.HomeNode(ctx, poolIn))
		if err != nil {
			writeErr(w, 503, err.Error())
			return
		}
		poolIn.TCPProxy = &store.TCPProxy{ContainerPort: pgBouncerPort, PublicPort: port}
	}
	if err := s.Store.UpdateInstanceConfig(ctx, poolIn); err != nil {
		s.storeErr(w, err)
		return
	}
	first := state.PoolingStatus != "enabled" && state.PoolingStatus != "updating"
	dep, err := s.pg().Enqueue(ctx, poolIn, deploy.EnqueueOpts{Trigger: "postgres-pooling"})
	if err != nil {
		writeErr(w, 409, "PgBouncer를 배포하지 못했습니다: "+err.Error())
		return
	}
	if first {
		state.PoolingStatus = "deploying"
	} else {
		state.PoolingStatus = "updating"
	}
	state.PoolingError, state.PoolerDeploymentID = "", dep.ID
	if err := s.Store.UpdatePostgresState(ctx, state); err != nil {
		s.storeErr(w, err)
		return
	}
	if first {
		s.emitPostgres(ctx, c, "postgres.pooling.deploying", fmt.Sprintf("PgBouncer(%s, %s 모드)를 배포합니다", pooler.Name, state.PoolMode))
	} else {
		s.emitPostgres(ctx, c, "postgres.pooling.updated", fmt.Sprintf("PgBouncer 설정을 바꿨습니다(%s 모드, pool %d, 클라이언트 %d)", state.PoolMode, state.DefaultPoolSize, state.MaxClientConn))
	}
	started = true
	go s.finishPooling(ctx, c, dep.ID, first, release)
	writeJSON(w, 202, s.poolingView(ctx, state, c.in))
}

// finishPooling은 PgBouncer 배포를 기다렸다가 DB 연결 변수를 바꾸고 참조 서비스를 다시 배포한다.
func (s *Server) finishPooling(ctx context.Context, c *instCtx, depID string, first bool, release func()) {
	defer release()
	depErr := s.waitPostgresDeployment(ctx, depID, pgBouncerDeployWait)
	state, err := postgresState(ctx, s.Store, c.in.ID)
	if err != nil || state.PoolerDeploymentID != depID {
		return
	}
	if depErr != nil {
		state.PoolingError = "PgBouncer 배포 실패: " + depErr.Error()
		if first {
			state.PoolingStatus = "failed"
		} else {
			state.PoolingStatus = "enabled" // 연결 변수는 이미 PgBouncer를 가리킨다
		}
		s.Store.UpdatePostgresState(ctx, state)
		s.emitPostgres(ctx, c, "postgres.pooling.failed", state.PoolingError)
		return
	}
	if err := s.reloadInstCtx(ctx, c); err != nil {
		return
	}
	pooler, err := s.Store.GetService(ctx, state.PoolerServiceID)
	if err != nil {
		state.PoolingStatus, state.PoolingError = "failed", "PgBouncer 서비스를 찾을 수 없습니다"
		s.Store.UpdatePostgresState(ctx, state)
		return
	}
	poolIn, err := s.Store.GetInstanceFor(ctx, pooler.ID, c.env.ID)
	if err != nil {
		return
	}
	if err := s.pg().SyncTCP(ctx, poolIn.ID); err != nil {
		s.Log.Warn("pgbouncer tcp proxy", "err", err)
	}
	before := fmt.Sprint(c.in.Variables["DATABASE_URL"], c.in.Variables["DATABASE_PUBLIC_URL"])
	if c.in.Variables == nil {
		c.in.Variables = map[string]string{}
	}
	applyPooledVars(c.in.Variables, pooler.Name, poolIn.TCPProxy != nil)
	if err := s.Store.UpdateInstanceConfig(ctx, c.in); err != nil {
		state.PoolingStatus, state.PoolingError = "failed", "연결 변수를 저장하지 못했습니다: "+err.Error()
		s.Store.UpdatePostgresState(ctx, state)
		return
	}
	state.PoolingStatus, state.PoolingError = "enabled", ""
	s.Store.UpdatePostgresState(ctx, state)
	var redeployed []string
	if before != fmt.Sprint(c.in.Variables["DATABASE_URL"], c.in.Variables["DATABASE_PUBLIC_URL"]) {
		redeployed = s.redeployPostgresConsumers(ctx, c, poolIn.ID)
	}
	msg := fmt.Sprintf("PgBouncer(%s, %s 모드)를 켰습니다. DATABASE_URL이 PgBouncer를 가리키고 직접 연결은 DATABASE_UNPOOLED_URL입니다", pooler.Name, state.PoolMode)
	if !first {
		msg = "PgBouncer 새 설정을 배포했습니다"
	}
	if len(redeployed) > 0 {
		msg += " · 다시 배포: " + strings.Join(redeployed, ", ")
	}
	s.emitPostgres(ctx, c, "postgres.pooling.enabled", msg)
}

// postgresConsumerKeys는 PgBouncer를 켜고 끌 때 값이 바뀌는 DB 변수다.
var postgresConsumerKeys = map[string]bool{"DATABASE_URL": true, "DATABASE_PUBLIC_URL": true}

// referencesPostgresURL은 변수 값 중 하나라도 ${{db.DATABASE_URL}} 또는 ${{db.DATABASE_PUBLIC_URL}}를 참조하는지 본다.
func referencesPostgresURL(vs map[string]string, dbName string) bool {
	for _, val := range vs {
		for _, ref := range vars.References(val) {
			if scope, key, ok := strings.Cut(ref, "."); ok && scope == dbName && postgresConsumerKeys[key] {
				return true
			}
		}
	}
	return false
}

// redeployPostgresConsumers는 같은 환경에서 DB의 연결 URL을 참조하며 실행 중인 서비스를 같은 이미지로 다시 배포한다
// (변수는 배포할 때 해석되므로). 다시 배포한 서비스 이름을 돌려준다.
func (s *Server) redeployPostgresConsumers(ctx context.Context, c *instCtx, skipInstanceID string) []string {
	insts, err := s.Store.ListInstancesByEnv(ctx, c.env.ID)
	if err != nil {
		return nil
	}
	var out []string
	for _, in := range insts {
		if in.ID == c.in.ID || in.ID == skipInstanceID || in.Status != store.InstOnline || in.CurrentDeploymentID == nil {
			continue
		}
		if !referencesPostgresURL(in.Variables, c.svc.Name) {
			continue
		}
		cur, err := s.Store.GetDeployment(ctx, *in.CurrentDeploymentID)
		if err != nil || cur.Status != store.DepLive || cur.Image == nil {
			continue
		}
		if _, err := s.pg().Enqueue(ctx, in, deploy.EnqueueOpts{Trigger: "postgres-pooling", Image: cur.Image, Spec: &cur.Spec, ReqSpec: &cur.ReqSpec,
			Commit: deploy.CommitOf(cur), Branch: cur.Branch, BuilderUsed: cur.BuilderUsed}); err != nil {
			s.Log.Warn("redeploy postgres consumer", "instance", in.ID, "err", err)
			continue
		}
		if svc, err := s.Store.GetService(ctx, in.ServiceID); err == nil {
			out = append(out, svc.Name)
		}
	}
	return out
}

// deletePostgresPooling은 PgBouncer를 제거하고 DATABASE_URL을 직접 연결로 되돌린다.
// 다른 환경에서 같은 PgBouncer 서비스를 쓰고 있으면 이 환경의 인스턴스만 멈춘다.
func (s *Server) deletePostgresPooling(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadPostgres(w, r)
	if !ok {
		return
	}
	release, cur, ok := s.tryPostgresOp(c.in.ID, "pooling")
	if !ok {
		writePostgresBusy(w, cur)
		return
	}
	defer release()
	ctx := context.WithoutCancel(r.Context())
	state, err := postgresState(ctx, s.Store, c.in.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.reloadInstCtx(ctx, c); err != nil {
		s.storeErr(w, err)
		return
	}
	before := fmt.Sprint(c.in.Variables["DATABASE_URL"], c.in.Variables["DATABASE_PUBLIC_URL"])
	removePooledVars(c.in.Variables)
	if err := s.Store.UpdateInstanceConfig(ctx, c.in); err != nil {
		s.storeErr(w, err)
		return
	}
	var poolerName string
	if state.PoolerServiceID != "" {
		if pooler, err := s.Store.GetService(ctx, state.PoolerServiceID); err == nil {
			poolerName = pooler.Name
			others, _ := s.Store.ListPostgresStatesByPooler(ctx, pooler.ID)
			shared := false
			for _, o := range others {
				if o.InstanceID != c.in.ID && o.PoolingStatus != "" {
					shared = true
				}
			}
			if shared {
				if poolIn, err := s.Store.GetInstanceFor(ctx, pooler.ID, c.env.ID); err == nil {
					if err := s.pg().Stop(ctx, poolIn); err != nil {
						s.Log.Warn("stop pgbouncer", "err", err)
					}
				}
			} else {
				if err := s.pg().DeleteService(ctx, pooler); err != nil {
					writeErr(w, 502, "PgBouncer 서비스를 지우지 못했습니다: "+err.Error())
					return
				}
				for _, o := range others {
					if o.InstanceID != c.in.ID {
						o.PoolerServiceID = ""
						s.Store.UpdatePostgresState(ctx, o)
					}
				}
				state.PoolerServiceID = ""
				if s.Events != nil {
					s.Events.EmitNamed(ctx, pooler.ProjectID, nil, &pooler.ID, &pooler.Name, "service.deleted", "서비스 "+pooler.Name+" 삭제")
				}
			}
		} else {
			state.PoolerServiceID = ""
		}
	}
	state.PoolingStatus, state.PoolingError, state.PoolerDeploymentID = "", "", ""
	if err := s.Store.UpdatePostgresState(ctx, state); err != nil {
		s.storeErr(w, err)
		return
	}
	var redeployed []string
	if before != fmt.Sprint(c.in.Variables["DATABASE_URL"], c.in.Variables["DATABASE_PUBLIC_URL"]) {
		redeployed = s.redeployPostgresConsumers(ctx, c, "")
	}
	msg := "PgBouncer를 제거했습니다. DATABASE_URL이 다시 PostgreSQL을 직접 가리킵니다"
	if poolerName != "" {
		msg = fmt.Sprintf("PgBouncer(%s)를 제거했습니다. DATABASE_URL이 다시 PostgreSQL을 직접 가리킵니다", poolerName)
	}
	if len(redeployed) > 0 {
		msg += " · 다시 배포: " + strings.Join(redeployed, ", ")
	}
	s.emitPostgres(ctx, c, "postgres.pooling.removed", msg)
	writeJSON(w, 200, s.poolingView(ctx, state, c.in))
}
