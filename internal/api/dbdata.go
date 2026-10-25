package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/dbquery"
	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/node"
)

// ---- database data browser ----
//
// GET  /api/services/{sid}/instances/{eid}/data/tables                 표/컬렉션/keyspace 요약
// GET  /api/services/{sid}/instances/{eid}/data/tables/{name}?schema=&limit=&offset=
// GET  /api/services/{sid}/instances/{eid}/data/keys?pattern=&cursor=&db=   (redis)
// GET  /api/services/{sid}/instances/{eid}/data/keys/{key}?db=             (redis)
// POST /api/services/{sid}/instances/{eid}/data/query {query, db?, resp3?}
// PATCH|POST|DELETE /api/services/{sid}/instances/{eid}/data/tables/{name}/rows   행 편집(dbdata_edit.go)
//
// 실행 중인 DB 컨테이너 안에서 DB의 CLI를 일회성 exec로 실행한다(로컬/원격 노드 모두). 접속 정보는 현재 배포의
// 최종 환경변수에서 가져와 exec 환경변수로만 넘긴다. 탐색 엔드포인트는 이름을 검증·인용해서 쿼리를 만들고,
// query 엔드포인트는 운영자가 쓴 쿼리를 그대로 실행한다(인증된 관리자만). query는 감사 이벤트 database.query를 남긴다:
// 쿼리 앞 200자(공백 정리)만 기록하고 결과는 기록하지 않는다. 탐색(읽기)은 이벤트를 남기지 않는다.

// oneShotExecer는 컨테이너에서 일회성 명령을 실행한다(deploy.Manager, 테스트에서는 가짜).
type oneShotExecer interface {
	ExecRun(ctx context.Context, nodeID, container string, req node.ExecRunRequest, timeout time.Duration) (*node.ExecOutput, error)
}

const maxDataPerUser = 8

func (s *Server) dataBackend() oneShotExecer {
	if s.dataExec != nil {
		return s.dataExec
	}
	return s.Deploy
}

type dataCtx struct {
	c         *instCtx
	kind      string
	t         dbquery.Target
	node      string
	container string
}

func (s *Server) loadData(w http.ResponseWriter, r *http.Request) (*dataCtx, bool) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return nil, false
	}
	if c.svc.DatabaseKind == nil || !dbquery.Supported(*c.svc.DatabaseKind) {
		writeErr(w, 400, "데이터 브라우저는 데이터베이스 서비스(postgres, mysql, redis, mongo)에서만 쓸 수 있습니다")
		return nil, false
	}
	kind := *c.svc.DatabaseKind
	d, container, code, msg := s.execTarget(r.Context(), c.in, "")
	if d == nil {
		if code == 409 {
			msg = "데이터베이스가 실행 중이 아닙니다. 먼저 배포하세요"
		}
		writeErr(w, code, msg)
		return nil, false
	}
	env := d.Env
	if len(env) == 0 {
		res, err := s.Deploy.Resolve(r.Context(), c.in, nil)
		if err != nil {
			s.storeErr(w, err)
			return nil, false
		}
		env = res.Vars
	}
	port := 0
	if d.Spec.Port != nil {
		port = *d.Spec.Port
	}
	return &dataCtx{c: c, kind: kind, t: dbquery.Target{Kind: kind, Port: port, Vars: env, Tag: dbquery.NewTag()}, node: d.NodeID, container: container}, true
}

// dataErr는 명령 실행 실패를 응답한다.
type dataErr struct {
	code int
	msg  string
}

func (e *dataErr) Error() string { return e.msg }

func trimMsg(b []byte) string {
	m := strings.TrimSpace(strings.ToValidUTF8(string(b), "\uFFFD"))
	if len(m) > 2000 {
		m = m[:2000] + "…"
	}
	return m
}

// runData는 명령을 실행하고, 종료 코드가 0이 아니면 stderr를 담은 400 오류로 바꾼다.
func (s *Server) runData(ctx context.Context, dc *dataCtx, cmd dbquery.Command) (*node.ExecOutput, error) {
	out, err := s.dataBackend().ExecRun(ctx, dc.node, dc.container, node.ExecRunRequest{
		Cmd: cmd.Cmd, Env: cmd.Env, Stdin: cmd.Stdin, MaxOutput: dbquery.MaxOutput,
	}, dbquery.TimeoutS*time.Second)
	if err != nil {
		switch {
		case errors.Is(err, deploy.ErrNodeUnavailable), errors.Is(err, deploy.ErrAgentTooOld):
			return nil, &dataErr{409, err.Error()}
		case errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "timed out"):
			s.cleanupData(dc) // 컨테이너 안 클라이언트와 서버 쪽 쿼리를 멈춘다(dbdata_edit.go)
			return nil, &dataErr{504, fmt.Sprintf("쿼리가 %d초 안에 끝나지 않았습니다", dbquery.TimeoutS)}
		}
		s.Log.Warn("data exec", "container", dc.container, "err", err)
		return nil, &dataErr{502, "데이터베이스 컨테이너에서 명령을 실행하지 못했습니다: " + err.Error()}
	}
	if out.ExitCode != 0 && !out.Truncated {
		msg := trimMsg(out.Stderr)
		if msg == "" {
			msg = trimMsg(out.Stdout)
		}
		if out.ExitCode == 127 && msg == "" {
			msg = "DB 클라이언트를 찾을 수 없습니다"
		}
		if msg == "" {
			msg = fmt.Sprintf("exit code %d", out.ExitCode)
		}
		if dc.kind == "mongo" {
			msg = dbquery.MongoErrorMessage(msg)
		}
		return nil, &dataErr{400, msg}
	}
	return out, nil
}

func (s *Server) writeDataErr(w http.ResponseWriter, err error) {
	var de *dataErr
	if errors.As(err, &de) {
		writeErr(w, de.code, de.msg)
		return
	}
	s.storeErr(w, err)
}

func (s *Server) acquireData(w http.ResponseWriter, r *http.Request) (func(), bool) {
	key, _ := s.execUser(r)
	release, ok := s.dataSlots.acquire(key, maxDataPerUser)
	if !ok {
		writeErr(w, 429, fmt.Sprintf("동시에 실행할 수 있는 데이터 요청은 %d개까지입니다", maxDataPerUser))
	}
	return release, ok
}

type dataTable struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Kind   string `json:"kind"` // table | view | materialized_view | foreign_table | collection | keyspace
	Rows   *int64 `json:"rows"` // 근삿값(모르면 null)
}

func cellInt(v any) *int64 {
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case fmt.Stringer:
		s = x.String()
	case float64:
		n := int64(x)
		return &n
	default:
		return nil
	}
	n, err := strconv.ParseInt(strings.SplitN(s, ".", 2)[0], 10, 64)
	if err != nil || n < 0 {
		return nil
	}
	return &n
}

func cellStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func (s *Server) dataTables(w http.ResponseWriter, r *http.Request) {
	dc, ok := s.loadData(w, r)
	if !ok {
		return
	}
	release, ok := s.acquireData(w, r)
	if !ok {
		return
	}
	defer release()
	ctx := r.Context()
	tables := []dataTable{}
	var cmd dbquery.Command
	var err error
	switch {
	case dbquery.IsSQL(dc.kind):
		cmd, err = dc.t.SQL(dbquery.TablesQuery(dc.kind))
	case dc.kind == "mongo":
		cmd, err = dc.t.Mongo(dbquery.MongoCollectionsEval)
	default:
		cmd, err = dc.t.Redis(0, dbquery.RedisLine("INFO", "keyspace"))
	}
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	out, err := s.runData(ctx, dc, cmd)
	if err != nil {
		s.writeDataErr(w, err)
		return
	}
	switch {
	case dbquery.IsSQL(dc.kind):
		res := dbquery.SQLResult(dc.kind, out.Stdout, out.Truncated)
		if res.Kind != "table" || len(res.Columns) < 4 {
			if res.Kind == "text" && strings.TrimSpace(res.Text) == "" {
				break // 표 없음
			}
			writeErr(w, 502, "표 목록을 읽지 못했습니다")
			return
		}
		for _, row := range res.Rows {
			tables = append(tables, dataTable{Schema: cellStr(row[0]), Name: cellStr(row[1]), Kind: cellStr(row[2]), Rows: cellInt(row[3])})
		}
	case dc.kind == "mongo":
		v, err := dbquery.ParseMongoJSON(out.Stdout)
		arr, _ := v.([]any)
		if err != nil || (v != nil && arr == nil) {
			writeErr(w, 502, "컬렉션 목록을 읽지 못했습니다")
			return
		}
		for _, x := range arr {
			m, _ := x.(map[string]any)
			tables = append(tables, dataTable{Schema: cellStr(m["schema"]), Name: cellStr(m["name"]), Kind: cellStr(m["kind"]), Rows: cellInt(m["rows"])})
		}
	default:
		for _, k := range dbquery.ParseKeyspace(string(out.Stdout)) {
			n := k.Keys
			tables = append(tables, dataTable{Name: fmt.Sprintf("db%d", k.DB), Kind: "keyspace", Rows: &n})
		}
	}
	writeJSON(w, 200, map[string]any{"kind": dc.kind, "tables": tables, "truncated": out.Truncated})
}

func limitOffset(r *http.Request) (int, int, error) {
	q := r.URL.Query()
	limit, offset := 50, 0
	var err error
	if v := q.Get("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 1 || limit > 500 {
			return 0, 0, errors.New("limit must be 1..500")
		}
	}
	if v := q.Get("offset"); v != "" {
		if offset, err = strconv.Atoi(v); err != nil || offset < 0 || offset > 100_000_000 {
			return 0, 0, errors.New("offset must be 0..100000000")
		}
	}
	return limit, offset, nil
}

func (s *Server) dataTableRows(w http.ResponseWriter, r *http.Request) {
	dc, ok := s.loadData(w, r)
	if !ok {
		return
	}
	if dc.kind == "redis" {
		writeErr(w, 400, "redis는 data/keys로 탐색하세요")
		return
	}
	limit, offset, err := limitOffset(r)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	name, schema := r.PathValue("name"), r.URL.Query().Get("schema")
	var cmd dbquery.Command
	if dc.kind == "mongo" {
		if schema == "" {
			schema = "test"
		}
		eval, err := dbquery.MongoFindEval(schema, name, limit, offset)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		cmd, err = dc.t.Mongo(eval)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	} else {
		if schema == "" && dc.kind == "postgres" {
			schema = "public"
		}
		q, err := dbquery.RowsQuery(dc.kind, schema, name, limit, offset)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if cmd, err = dc.t.SQL(q); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	release, ok := s.acquireData(w, r)
	if !ok {
		return
	}
	defer release()
	var cols []dbquery.Column // SQL: 열 타입과 기본 키(dbdata_edit.go)
	if dbquery.IsSQL(dc.kind) {
		if cols, err = s.dataColumns(r.Context(), dc, schema, name); err != nil {
			s.writeDataErr(w, err)
			return
		}
	}
	out, err := s.runData(r.Context(), dc, cmd)
	if err != nil {
		s.writeDataErr(w, err)
		return
	}
	var res *dbquery.Result
	if dc.kind == "mongo" {
		res = dbquery.MongoResult(out.Stdout, out.Truncated)
		if res.Kind == "text" { // 빈 컬렉션
			res = dbquery.TableResult([]string{}, nil, out.Truncated)
		}
	} else {
		res = dbquery.SQLResult(dc.kind, out.Stdout, out.Truncated)
		if res.Kind == "text" && strings.TrimSpace(res.Text) == "" && !res.Truncated {
			// mysql --batch는 행이 없으면 헤더도 찍지 않는다(빈 표)
			res = dbquery.TableResult([]string{}, nil, false)
		}
		if res.Kind != "table" {
			writeErr(w, 502, "행을 읽지 못했습니다")
			return
		}
	}
	hasMore := len(res.Rows) > limit
	if hasMore {
		res.Rows = res.Rows[:limit]
	}
	types, pk := columnTypes(dc.kind, res, cols)
	writeJSON(w, 200, map[string]any{"schema": schema, "name": name, "columns": res.Columns, "rows": res.Rows,
		"limit": limit, "offset": offset, "has_more": hasMore, "truncated": res.Truncated,
		"column_types": types, "primary_key": pk, "editable": dbquery.IsSQL(dc.kind) && len(pk) > 0})
}

var cursorRe = regexp.MustCompile(`^[0-9]{1,20}$`)

func redisDB(r *http.Request) (int, error) {
	v := r.URL.Query().Get("db")
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > 1000 {
		return 0, errors.New("db must be 0..1000")
	}
	return n, nil
}

type redisKey struct {
	Key  string `json:"key"`
	Type string `json:"type"`
	TTL  int64  `json:"ttl"` // 초, -1 만료 없음
}

func (s *Server) redisRun(ctx context.Context, dc *dataCtx, db int, script string) ([]dbquery.Reply, error) {
	cmd, err := dc.t.Redis(db, script)
	if err != nil {
		return nil, &dataErr{400, err.Error()}
	}
	out, err := s.runData(ctx, dc, cmd)
	if err != nil {
		return nil, err
	}
	reps, err := dbquery.ParseRedis(string(out.Stdout))
	if err != nil {
		return nil, &dataErr{502, "redis 응답을 읽지 못했습니다: " + err.Error()}
	}
	for _, rp := range reps {
		if rp.Type == "error" {
			return nil, &dataErr{400, rp.Str}
		}
	}
	return reps, nil
}

func (s *Server) dataKeys(w http.ResponseWriter, r *http.Request) {
	dc, ok := s.loadData(w, r)
	if !ok {
		return
	}
	if dc.kind != "redis" {
		writeErr(w, 400, "data/keys는 redis에서만 쓸 수 있습니다")
		return
	}
	q := r.URL.Query()
	pattern, cursor := q.Get("pattern"), q.Get("cursor")
	if pattern == "" {
		pattern = "*"
	}
	if cursor == "" {
		cursor = "0"
	}
	if len(pattern) > 1024 || !cursorRe.MatchString(cursor) {
		writeErr(w, 400, "pattern must be at most 1024 bytes and cursor a number")
		return
	}
	db, err := redisDB(r)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	release, ok := s.acquireData(w, r)
	if !ok {
		return
	}
	defer release()
	ctx := r.Context()
	reps, err := s.redisRun(ctx, dc, db, dbquery.RedisLine("SCAN", cursor, "MATCH", pattern, "COUNT", "200"))
	if err != nil {
		s.writeDataErr(w, err)
		return
	}
	if len(reps) != 1 || reps[0].Type != "array" || len(reps[0].Elems) != 2 {
		writeErr(w, 502, "SCAN 응답을 읽지 못했습니다")
		return
	}
	next := reps[0].Elems[0].Str
	keys := []redisKey{}
	var script strings.Builder
	for _, k := range reps[0].Elems[1].Elems {
		keys = append(keys, redisKey{Key: k.Str})
		script.WriteString(dbquery.RedisLine("TYPE", k.Str))
		script.WriteString(dbquery.RedisLine("TTL", k.Str))
	}
	if len(keys) > 0 {
		meta, err := s.redisRun(ctx, dc, db, script.String())
		if err != nil {
			s.writeDataErr(w, err)
			return
		}
		for i := range keys {
			if 2*i+1 < len(meta) {
				keys[i].Type, keys[i].TTL = meta[2*i].Str, meta[2*i+1].Int
			}
		}
	}
	for i := range keys {
		keys[i].Key = strings.ToValidUTF8(keys[i].Key, "\uFFFD")
	}
	writeJSON(w, 200, map[string]any{"cursor": next, "keys": keys, "db": db})
}

// redisValueScript는 타입별로 길이와 미리보기(앞부분)를 읽는 명령이다.
func redisValueScript(typ, key string) string {
	switch typ {
	case "string":
		return dbquery.RedisLine("STRLEN", key) + dbquery.RedisLine("GETRANGE", key, "0", "65535")
	case "list":
		return dbquery.RedisLine("LLEN", key) + dbquery.RedisLine("LRANGE", key, "0", "99")
	case "set":
		return dbquery.RedisLine("SCARD", key) + dbquery.RedisLine("SSCAN", key, "0", "COUNT", "100")
	case "zset":
		return dbquery.RedisLine("ZCARD", key) + dbquery.RedisLine("ZRANGE", key, "0", "99", "WITHSCORES")
	case "hash":
		return dbquery.RedisLine("HLEN", key) + dbquery.RedisLine("HSCAN", key, "0", "COUNT", "100")
	case "stream":
		return dbquery.RedisLine("XLEN", key) + dbquery.RedisLine("XREVRANGE", key, "+", "-", "COUNT", "50")
	}
	return ""
}

func strs(r dbquery.Reply) []string {
	out := []string{}
	for _, e := range r.Elems {
		out = append(out, strings.ToValidUTF8(e.Text(), "\uFFFD"))
	}
	return out
}

// RedisValue는 타입별 미리보기 값을 JSON으로 만든다.
func RedisValue(typ string, r dbquery.Reply) (any, int) {
	switch typ {
	case "string":
		return strings.ToValidUTF8(r.Str, "\uFFFD"), len(r.Str)
	case "list":
		v := strs(r)
		return v, len(v)
	case "set":
		if len(r.Elems) == 2 { // SSCAN: [cursor, members]
			v := strs(r.Elems[1])
			return v, len(v)
		}
	case "zset":
		v := []map[string]string{}
		for i := 0; i+1 < len(r.Elems); i += 2 {
			v = append(v, map[string]string{"member": r.Elems[i].Text(), "score": r.Elems[i+1].Text()})
		}
		return v, len(v)
	case "hash":
		v := []map[string]string{}
		if len(r.Elems) == 2 {
			f := r.Elems[1].Elems
			for i := 0; i+1 < len(f); i += 2 {
				v = append(v, map[string]string{"field": f[i].Text(), "value": f[i+1].Text()})
			}
		}
		return v, len(v)
	case "stream":
		v := []map[string]any{}
		for _, e := range r.Elems {
			if len(e.Elems) == 2 {
				v = append(v, map[string]any{"id": e.Elems[0].Text(), "fields": strs(e.Elems[1])})
			}
		}
		return v, len(v)
	}
	return nil, 0
}

func (s *Server) dataKey(w http.ResponseWriter, r *http.Request) {
	dc, ok := s.loadData(w, r)
	if !ok {
		return
	}
	if dc.kind != "redis" {
		writeErr(w, 400, "data/keys는 redis에서만 쓸 수 있습니다")
		return
	}
	key := r.PathValue("key")
	if key == "" || len(key) > 4096 {
		writeErr(w, 400, "key must be 1..4096 bytes")
		return
	}
	db, err := redisDB(r)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	release, ok := s.acquireData(w, r)
	if !ok {
		return
	}
	defer release()
	ctx := r.Context()
	meta, err := s.redisRun(ctx, dc, db, dbquery.RedisLine("TYPE", key)+dbquery.RedisLine("TTL", key))
	if err != nil {
		s.writeDataErr(w, err)
		return
	}
	if len(meta) != 2 {
		writeErr(w, 502, "TYPE 응답을 읽지 못했습니다")
		return
	}
	typ, ttl := meta[0].Str, meta[1].Int
	if typ == "none" {
		writeErr(w, 404, "key not found")
		return
	}
	out := map[string]any{"key": strings.ToValidUTF8(key, "\uFFFD"), "type": typ, "ttl": ttl, "db": db, "length": nil, "value": nil, "truncated": false}
	if script := redisValueScript(typ, key); script != "" {
		reps, err := s.redisRun(ctx, dc, db, script)
		if err != nil {
			s.writeDataErr(w, err)
			return
		}
		if len(reps) == 2 {
			length := reps[0].Int
			v, n := RedisValue(typ, reps[1])
			out["length"], out["value"], out["truncated"] = length, v, int64(n) < length
		}
	}
	writeJSON(w, 200, out)
}

// queryAudit은 감사 이벤트에 남길 쿼리 요약이다(공백 정리, 앞 200자).
func queryAudit(q string) string {
	q = strings.Join(strings.Fields(q), " ")
	if r := []rune(q); len(r) > 200 {
		q = string(r[:200]) + "…"
	}
	return q
}

func (s *Server) dataQuery(w http.ResponseWriter, r *http.Request) {
	dc, ok := s.loadData(w, r)
	if !ok {
		return
	}
	var body struct {
		Query string `json:"query"`
		DB    int    `json:"db"`
		RESP3 bool   `json:"resp3"` // redis: redis-cli -3
	}
	if !decode(w, r, &body) {
		return
	}
	dc.t.RESP3 = body.RESP3 && dc.kind == "redis"
	var cmd dbquery.Command
	var err error
	switch {
	case dbquery.IsSQL(dc.kind):
		cmd, err = dc.t.SQL(body.Query)
	case dc.kind == "mongo":
		cmd, err = dc.t.Mongo(body.Query)
	default:
		cmd, err = dc.t.Redis(body.DB, body.Query)
	}
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	release, ok := s.acquireData(w, r)
	if !ok {
		return
	}
	defer release()
	ctx := r.Context()
	_, who := s.execUser(r)
	c := dc.c
	s.Events.Emit(ctx, c.p.ID, &c.env.ID, &c.svc.ID, "database.query",
		fmt.Sprintf("%s (%s): %s이 쿼리를 실행했습니다: %s", c.svc.Name, c.env.Name, who, queryAudit(body.Query)))
	start := time.Now()
	out, err := s.runData(ctx, dc, cmd)
	var de *dataErr
	if err != nil && dc.kind == "mongo" && errors.As(err, &de) && dbquery.IsMongoCursorErr(de.msg) {
		// mongosh --json은 커서(find/aggregate 결과)를 직렬화하지 못한다. 커서는 순회하기 전에는 실행되지 않으므로
		// 식 하나면 .toArray()를 붙여 한 번 더 실행한다.
		err = &dataErr{400, "결과가 커서입니다. 쿼리 끝에 .toArray()를 붙여 실행하세요"}
		if eval, ok := dbquery.MongoCursorEval(body.Query); ok {
			if rc, cerr := dc.t.Mongo(eval); cerr == nil {
				if o, rerr := s.runData(ctx, dc, rc); rerr == nil {
					out, err = o, nil
				} else if !errors.As(rerr, &de) || de.code != 400 {
					err = rerr
				}
			}
		}
	}
	if err != nil {
		s.writeDataErr(w, err)
		return
	}
	var res *dbquery.Result
	switch {
	case dbquery.IsSQL(dc.kind):
		res = dbquery.SQLResult(dc.kind, out.Stdout, out.Truncated)
	case dc.kind == "mongo":
		res = dbquery.MongoResult(out.Stdout, out.Truncated)
	default:
		res = dbquery.RedisResult(string(out.Stdout), out.Truncated) // RESP3 map 하나는 key/value 표
	}
	if res.Kind == "text" && strings.TrimSpace(res.Text) == "" {
		res.Text = "(출력 없음)"
	}
	writeJSON(w, 200, map[string]any{"kind": res.Kind, "columns": res.Columns, "rows": res.Rows, "text": res.Text,
		"truncated": res.Truncated, "row_count": len(res.Rows), "duration_ms": time.Since(start).Milliseconds()})
}
