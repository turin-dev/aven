package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/dbquery"
	"github.com/turin-dev/aven/internal/node"
)

// ---- 데이터 브라우저: 열 정보, 행 편집, 시간 초과 정리 ----
//
// PATCH  /api/services/{sid}/instances/{eid}/data/tables/{name}/rows?schema=  {pk, set}  행 하나 바꾸기
// POST   /api/services/{sid}/instances/{eid}/data/tables/{name}/rows?schema=  {set}      행 하나 넣기
// DELETE /api/services/{sid}/instances/{eid}/data/tables/{name}/rows?schema=  {pk}       행 하나 지우기
//
// postgres/mysql/mariadb만. 표의 열과 기본 키를 먼저 읽고(pg_attribute / information_schema.columns), pk는 기본 키
// 열을 모두(그 열만) 담아야 한다. 식별자는 인용하고 값은 문자열 리터럴로 이스케이프한다(dbquery.RowEdit).
// 감사 이벤트 database.edit: 표, 바꾼 열 이름, 기본 키 값(앞 200자)을 남기고 바꾼 값은 남기지 않는다.

// cleanupData는 시간이 지난 요청(dc.t.Tag)을 정리한다: 컨테이너 안 클라이언트 프로세스를 끝내고,
// postgres는 pg_cancel_backend, mysql은 KILL QUERY로 서버 쪽 쿼리를 멈춘다. 실패는 기록만 한다.
func (s *Server) cleanupData(dc *dataCtx) {
	tag := dc.t.Tag
	if tag == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), dbquery.KillTimeoutS*time.Second)
	defer cancel()
	run := func(cmd dbquery.Command) (*node.ExecOutput, error) {
		return s.dataBackend().ExecRun(ctx, dc.node, dc.container, node.ExecRunRequest{
			Cmd: cmd.Cmd, Env: cmd.Env, Stdin: cmd.Stdin, MaxOutput: dbquery.MaxOutput,
		}, dbquery.KillTimeoutS*time.Second)
	}
	cmd, err := dc.t.Cleanup(tag)
	if err != nil {
		return
	}
	out, err := run(cmd)
	if err != nil {
		s.Log.Warn("data cleanup", "container", dc.container, "err", err)
		return
	}
	if dc.kind == "mysql" || dc.kind == "mariadb" {
		ids := dbquery.ParseIDs(dbquery.SQLResult(dc.kind, out.Stdout, out.Truncated))
		if len(ids) == 0 {
			return
		}
		base := dc.t
		base.Tag = ""
		kc, err := base.SQL(dbquery.MySQLKillQuery(ids))
		if err != nil {
			return
		}
		if _, err := run(kc); err != nil {
			s.Log.Warn("data kill query", "container", dc.container, "err", err)
			return
		}
		s.Log.Info("data kill query", "container", dc.container, "ids", ids)
	}
}

// dataColumns는 표의 열(이름, 타입, 기본 키)을 읽는다.
func (s *Server) dataColumns(ctx context.Context, dc *dataCtx, schema, name string) ([]dbquery.Column, error) {
	q, err := dbquery.ColumnsQuery(dc.kind, schema, name)
	if err != nil {
		return nil, &dataErr{400, err.Error()}
	}
	cmd, err := dc.t.SQL(q)
	if err != nil {
		return nil, &dataErr{400, err.Error()}
	}
	out, err := s.runData(ctx, dc, cmd)
	if err != nil {
		return nil, err
	}
	cols, err := dbquery.ParseColumns(dbquery.SQLResult(dc.kind, out.Stdout, out.Truncated))
	if err != nil {
		return nil, &dataErr{502, "열 정보를 읽지 못했습니다"}
	}
	return cols, nil
}

// columnTypes는 결과 열 순서에 맞춘 타입(모르면 null)과 기본 키 열을 돌려준다. 결과에 열이 없으면
// (mysql 빈 표) 열 정보로 채운다.
func columnTypes(kind string, res *dbquery.Result, cols []dbquery.Column) ([]any, []string) {
	pk := dbquery.PrimaryKey(cols)
	if !dbquery.IsSQL(kind) {
		return nil, pk
	}
	if len(res.Columns) == 0 && len(cols) > 0 {
		for _, c := range cols {
			res.Columns = append(res.Columns, c.Name)
		}
	}
	byName := map[string]string{}
	for _, c := range cols {
		byName[c.Name] = c.Type
	}
	types := make([]any, len(res.Columns))
	for i, c := range res.Columns {
		if t, ok := byName[c]; ok {
			types[i] = t
		}
	}
	return types, pk
}

// jsonValues는 JSON 값들을 숫자를 보존해서(json.Number) 읽는다.
func jsonValues(m map[string]json.RawMessage) (map[string]any, error) {
	out := make(map[string]any, len(m))
	for k, raw := range m {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("%s: %v", k, err)
		}
		out[k] = v
	}
	return out, nil
}

func (s *Server) dataRowUpdate(w http.ResponseWriter, r *http.Request) { s.dataRowEdit(w, r, "update") }
func (s *Server) dataRowInsert(w http.ResponseWriter, r *http.Request) { s.dataRowEdit(w, r, "insert") }
func (s *Server) dataRowDelete(w http.ResponseWriter, r *http.Request) { s.dataRowEdit(w, r, "delete") }

func (s *Server) dataRowEdit(w http.ResponseWriter, r *http.Request, op string) {
	dc, ok := s.loadData(w, r)
	if !ok {
		return
	}
	if !dbquery.IsSQL(dc.kind) {
		writeErr(w, 400, "행 편집은 postgres, mysql에서만 쓸 수 있습니다")
		return
	}
	name, schema := r.PathValue("name"), r.URL.Query().Get("schema")
	if schema == "" && dc.kind == "postgres" {
		schema = "public"
	}
	var body struct {
		PK  map[string]json.RawMessage `json:"pk"`
		Set map[string]json.RawMessage `json:"set"`
	}
	if !decode(w, r, &body) {
		return
	}
	pkv, err := jsonValues(body.PK)
	if err != nil {
		writeErr(w, 400, "pk: "+err.Error())
		return
	}
	setv, err := jsonValues(body.Set)
	if err != nil {
		writeErr(w, 400, "set: "+err.Error())
		return
	}
	switch {
	case op != "insert" && len(pkv) == 0:
		writeErr(w, 400, "pk is required")
		return
	case op == "update" && len(setv) == 0:
		writeErr(w, 400, "set is required")
		return
	case op == "insert" && len(pkv) > 0, op == "delete" && len(setv) > 0:
		writeErr(w, 400, "unexpected field for this operation")
		return
	}
	edit := dbquery.RowEdit{Kind: dc.kind, Schema: schema, Table: name, PK: pkv, Set: setv}
	// 이름을 먼저 검사한다(열 정보를 읽기 전에)
	if _, err := dbquery.ColumnsQuery(dc.kind, schema, name); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	release, ok := s.acquireData(w, r)
	if !ok {
		return
	}
	defer release()
	ctx := r.Context()
	cols, err := s.dataColumns(ctx, dc, schema, name)
	if err != nil {
		s.writeDataErr(w, err)
		return
	}
	if len(cols) == 0 {
		writeErr(w, 404, "표를 찾을 수 없습니다")
		return
	}
	known := map[string]bool{}
	for _, c := range cols {
		known[c.Name] = true
	}
	for k := range setv {
		if !known[k] {
			writeErr(w, 400, fmt.Sprintf("알 수 없는 열입니다: %s", k))
			return
		}
	}
	pk := dbquery.PrimaryKey(cols)
	if op != "insert" {
		if len(pk) == 0 {
			writeErr(w, 400, "기본 키가 없는 표의 행은 바꾸거나 지울 수 없습니다. 쿼리 탭을 쓰세요")
			return
		}
		same := len(pk) == len(pkv)
		for _, k := range pk {
			if _, ok := pkv[k]; !ok {
				same = false
			}
		}
		if !same {
			writeErr(w, 400, fmt.Sprintf("pk에는 기본 키 열(%s)을 모두, 그 열만 넣어야 합니다", strings.Join(pk, ", ")))
			return
		}
	}
	var q string
	switch op {
	case "update":
		q, err = edit.UpdateSQL()
	case "insert":
		q, err = edit.InsertSQL()
	default:
		q, err = edit.DeleteSQL()
	}
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	cmd, err := dc.t.SQL(q)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	_, who := s.execUser(r)
	c := dc.c
	s.Events.Emit(ctx, c.p.ID, &c.env.ID, &c.svc.ID, "database.edit",
		fmt.Sprintf("%s (%s): %s이 표 %s의 %s", c.svc.Name, c.env.Name, who, tableAuditName(schema, name), editAudit(op, pkv, setv)))
	out, err := s.runData(ctx, dc, cmd)
	if err != nil {
		s.writeDataErr(w, err)
		return
	}
	n, err := dbquery.ParseAffected(dbquery.SQLResult(dc.kind, out.Stdout, out.Truncated))
	if err != nil {
		writeErr(w, 502, "편집 결과를 읽지 못했습니다")
		return
	}
	// mysql ROW_COUNT()는 값이 그대로인 UPDATE를 0으로 센다: postgres UPDATE와 DELETE만 0을 404로 본다.
	if n == 0 && (op == "delete" || (op == "update" && dc.kind == "postgres")) {
		writeErr(w, 404, "행을 찾을 수 없습니다")
		return
	}
	code := 200
	if op == "insert" {
		code = 201
	}
	writeJSON(w, code, map[string]any{"affected": n})
}

func tableAuditName(schema, name string) string {
	if schema == "" {
		return name
	}
	return schema + "." + name
}

// editAudit은 감사 메시지 뒷부분이다: 바꾼 열 이름과 기본 키 값만(바꾼 값은 남기지 않는다).
func editAudit(op string, pk, set map[string]any) string {
	cols := make([]string, 0, len(set))
	for k := range set {
		cols = append(cols, k)
	}
	sort.Strings(cols)
	keys := make([]string, 0, len(pk))
	for k, v := range pk {
		keys = append(keys, fmt.Sprintf("%s=%v", k, v))
	}
	sort.Strings(keys)
	where := queryAudit(strings.Join(keys, ", "))
	switch op {
	case "update":
		return fmt.Sprintf("행(%s)의 열 %s를 바꿨습니다", where, queryAudit(strings.Join(cols, ", ")))
	case "insert":
		return fmt.Sprintf("행을 넣었습니다(열: %s)", queryAudit(strings.Join(cols, ", ")))
	}
	return fmt.Sprintf("행(%s)을 지웠습니다", where)
}
