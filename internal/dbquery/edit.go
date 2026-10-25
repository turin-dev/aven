package dbquery

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ---- 요청 표시와 시간 초과 정리 ----

var tagRe = regexp.MustCompile(`^[0-9a-f]{8,32}$`)

// NewTag는 요청 하나를 찾는 무작위 표시(16진수 24자)다.
func NewTag() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// killClients는 KEEL_DATA_TAG=$KILL_TAG 환경변수를 가진 프로세스(시간이 지난 DB 클라이언트)에 TERM을 보낸다.
// timeout(1)은 TERM을 자식에게 넘긴다. 정리 명령 자신은 이 환경변수가 없어서 걸리지 않는다.
const killClients = `for d in /proc/[0-9]*; do
  p=${d#/proc/}
  [ "$p" = "$$" ] && continue
  if tr '\0' '\n' < "$d/environ" 2>/dev/null | grep -qx "KEEL_DATA_TAG=$KILL_TAG"; then kill -TERM "$p" 2>/dev/null; fi
done
`

// PgCancelQuery는 tag 요청의 서버 쪽 쿼리를 취소하는 쿼리다(application_name으로 찾는다).
func PgCancelQuery(tag string) (string, error) {
	if !tagRe.MatchString(tag) {
		return "", errors.New("invalid tag")
	}
	return "SELECT count(pg_cancel_backend(pid)) AS cancelled FROM pg_stat_activity\n" +
		"WHERE application_name = 'keel-data-" + tag + "' AND pid <> pg_backend_pid();\n", nil
}

// MySQLFindQuery는 tag 주석이 붙은 실행 중인 쿼리의 processlist id를 찾는 쿼리다.
// 이 쿼리 자신의 Info에도 같은 글자가 있으므로 CONNECTION_ID()는 뺀다.
func MySQLFindQuery(tag string) (string, error) {
	if !tagRe.MatchString(tag) {
		return "", errors.New("invalid tag")
	}
	return "SELECT ID AS id FROM information_schema.PROCESSLIST WHERE INFO LIKE '%keel-data:" + tag +
		"%' AND ID <> CONNECTION_ID();\n", nil
}

// MySQLKillQuery는 processlist id들의 쿼리를 멈추는 문장이다(연결은 남긴다). id가 없으면 빈 문자열.
func MySQLKillQuery(ids []int64) string {
	var b strings.Builder
	for _, id := range ids {
		if id > 0 {
			fmt.Fprintf(&b, "KILL QUERY %d;\n", id)
		}
	}
	return b.String()
}

// ParseIDs는 첫 열이 양의 정수인 행들의 값을 꺼낸다(MySQLFindQuery 결과).
func ParseIDs(res *Result) []int64 {
	var ids []int64
	if res == nil || res.Kind != "table" {
		return nil
	}
	for _, row := range res.Rows {
		if len(row) == 0 {
			continue
		}
		s, _ := row[0].(string)
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
			ids = append(ids, n)
		}
	}
	return ids
}

// Cleanup은 시간이 지난 tag 요청을 정리하는 명령이다: 컨테이너 안 클라이언트 프로세스를 끝내고, postgres는
// pg_cancel_backend로, mysql은 쿼리 id를 찾아(출력을 ParseIDs로 읽어 MySQLKillQuery를 실행) 서버 쪽 쿼리를 멈춘다.
func (t Target) Cleanup(tag string) (Command, error) {
	if !tagRe.MatchString(tag) {
		return Command{}, errors.New("invalid tag")
	}
	base := t
	base.Tag, base.RESP3 = "", false
	var c Command
	var err error
	switch {
	case t.Kind == "postgres":
		q, _ := PgCancelQuery(tag)
		c, err = base.SQL(q)
	case IsSQL(t.Kind):
		q, _ := MySQLFindQuery(tag)
		c, err = base.SQL(q)
	default:
		c = Command{Cmd: shell(":")}
	}
	if err != nil {
		return Command{}, err
	}
	c.Cmd = append([]string{}, c.Cmd...)
	c.Cmd[len(c.Cmd)-1] = timeoutPrelude + killClients + strings.TrimPrefix(c.Cmd[len(c.Cmd)-1], timeoutPrelude)
	c.Env = append(append([]string{}, c.Env...), kv("KILL_TAG", tag))
	return c, nil
}

// ---- 리터럴 ----

// QuoteLiteral은 문자열을 kind의 SQL 문자열 리터럴로 인용한다.
// postgres는 E'...', mysql은 '...'이고 둘 다 백슬래시와 작은따옴표를 두 번 쓴다. postgres는
// standard_conforming_strings 설정과 무관하고, mysql은 NO_BACKSLASH_ESCAPES 모드에서도 문자열 밖으로 나갈 수 없다
// (편집 문장은 MySQLSessionPrelude로 그 모드를 끈다).
func QuoteLiteral(kind, s string) (string, error) {
	if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
		return "", errors.New("value must be valid UTF-8 without NUL")
	}
	esc := strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(s)
	switch kind {
	case "postgres":
		return "E'" + esc + "'", nil
	case "mysql", "mariadb":
		return "'" + esc + "'", nil
	}
	return "", fmt.Errorf("literals are not supported for %q", kind)
}

// MySQLSessionPrelude는 편집 문장 앞에 붙여 백슬래시 이스케이프를 켠다(QuoteLiteral과 맞춘다).
const MySQLSessionPrelude = "SET SESSION sql_mode = REPLACE(@@SESSION.sql_mode, 'NO_BACKSLASH_ESCAPES', '');\n"

var numberRe = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

// SQLValue는 JSON 값(json.Number로 읽은 것)을 SQL 값으로 바꾼다: null은 NULL, 불리언은 TRUE/FALSE,
// 숫자·문자열은 문자열 리터럴(열 타입으로 바뀐다), 객체·배열은 JSON 글자 리터럴.
func SQLValue(kind string, v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "NULL", nil
	case bool:
		if x {
			return "TRUE", nil
		}
		return "FALSE", nil
	case json.Number:
		if !numberRe.MatchString(x.String()) {
			return "", errors.New("invalid number")
		}
		return QuoteLiteral(kind, x.String())
	case float64:
		return QuoteLiteral(kind, strconv.FormatFloat(x, 'f', -1, 64))
	case string:
		return QuoteLiteral(kind, x)
	case map[string]any, []any:
		b, err := json.Marshal(x)
		if err != nil {
			return "", err
		}
		return QuoteLiteral(kind, string(b))
	}
	return "", fmt.Errorf("unsupported value %T", v)
}

// ---- 열 정보 ----

// Column은 표의 열 하나다.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
	PK   bool   `json:"pk"`
}

func tableRef(kind, schema, table string) (string, error) {
	t, err := QuoteIdent(kind, table)
	if err != nil {
		return "", err
	}
	if schema == "" {
		return t, nil
	}
	s, err := QuoteIdent(kind, schema)
	if err != nil {
		return "", err
	}
	return s + "." + t, nil
}

// ColumnsQuery는 표의 열 이름, 타입, 기본 키 여부를 읽는 쿼리다(열 순서대로). 열: name, type, pk.
// postgres는 pg_attribute + pg_index(indisprimary), mysql은 information_schema.columns(COLUMN_KEY='PRI').
func ColumnsQuery(kind, schema, table string) (string, error) {
	switch kind {
	case "postgres":
		ref, err := tableRef(kind, schema, table)
		if err != nil {
			return "", err
		}
		lit, err := QuoteLiteral(kind, ref)
		if err != nil {
			return "", err
		}
		return "SELECT a.attname AS name, format_type(a.atttypid, a.atttypmod) AS type,\n" +
			"  COALESCE(a.attnum = ANY(i.indkey), false) AS pk\n" +
			"FROM pg_attribute a LEFT JOIN pg_index i ON i.indrelid = a.attrelid AND i.indisprimary\n" +
			"WHERE a.attrelid = " + lit + "::regclass AND a.attnum > 0 AND NOT a.attisdropped\n" +
			"ORDER BY a.attnum;\n", nil
	case "mysql", "mariadb":
		if err := ValidIdent(table); err != nil {
			return "", err
		}
		tl, err := QuoteLiteral(kind, table)
		if err != nil {
			return "", err
		}
		sl := "DATABASE()"
		if schema != "" {
			if err := ValidIdent(schema); err != nil {
				return "", err
			}
			if sl, err = QuoteLiteral(kind, schema); err != nil {
				return "", err
			}
		}
		return MySQLSessionPrelude + "SELECT COLUMN_NAME AS `name`, COLUMN_TYPE AS `type`, IF(COLUMN_KEY = 'PRI', 1, 0) AS `pk`\n" +
			"FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = " + sl + " AND TABLE_NAME = " + tl + "\n" +
			"ORDER BY ORDINAL_POSITION;\n", nil
	}
	return "", fmt.Errorf("columns are not supported for %q", kind)
}

// ParseColumns는 ColumnsQuery 결과를 읽는다.
func ParseColumns(res *Result) ([]Column, error) {
	if res == nil {
		return nil, errors.New("no result")
	}
	if res.Kind == "text" && strings.TrimSpace(res.Text) == "" {
		return []Column{}, nil // mysql: 행이 없으면 헤더도 없다
	}
	if res.Kind != "table" || len(res.Columns) < 3 {
		return nil, errors.New("unexpected columns result")
	}
	cols := []Column{}
	for _, row := range res.Rows {
		name, _ := row[0].(string)
		typ, _ := row[1].(string)
		pk, _ := row[2].(string)
		cols = append(cols, Column{Name: name, Type: typ, PK: pk == "t" || pk == "1" || pk == "true"})
	}
	return cols, nil
}

// PrimaryKey는 기본 키 열 이름들이다(열 순서대로).
func PrimaryKey(cols []Column) []string {
	pk := []string{}
	for _, c := range cols {
		if c.PK {
			pk = append(pk, c.Name)
		}
	}
	return pk
}

// ---- 행 편집 ----

// RowEdit은 표 한 행 편집이다. PK는 기본 키 열 → 값(모든 기본 키 열), Set은 바꿀/넣을 열 → 값.
type RowEdit struct {
	Kind   string
	Schema string
	Table  string
	PK     map[string]any
	Set    map[string]any
}

// MaxEditColumns는 한 번에 바꿀 수 있는 열 수다.
const MaxEditColumns = 500

func sortedMapKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func (e RowEdit) assignments(m map[string]any, sep string, where bool) (string, error) {
	parts := []string{}
	for _, k := range sortedMapKeys(m) {
		id, err := QuoteIdent(e.Kind, k)
		if err != nil {
			return "", fmt.Errorf("column %q: %v", k, err)
		}
		if where && m[k] == nil {
			return "", fmt.Errorf("primary key %q must not be null", k)
		}
		v, err := SQLValue(e.Kind, m[k])
		if err != nil {
			return "", fmt.Errorf("column %q: %v", k, err)
		}
		parts = append(parts, id+" = "+v)
	}
	return strings.Join(parts, sep), nil
}

func (e RowEdit) check(needPK, needSet bool) (string, error) {
	if !IsSQL(e.Kind) {
		return "", fmt.Errorf("row editing is not supported for %q", e.Kind)
	}
	if needPK && len(e.PK) == 0 {
		return "", errors.New("pk is required")
	}
	if needSet && len(e.Set) == 0 {
		return "", errors.New("set is required")
	}
	if len(e.Set) > MaxEditColumns || len(e.PK) > MaxEditColumns {
		return "", fmt.Errorf("at most %d columns", MaxEditColumns)
	}
	return tableRef(e.Kind, e.Schema, e.Table)
}

// affected는 편집 문장을 영향받은 행 수(열 affected) 하나를 돌려주는 스크립트로 감싼다.
func (e RowEdit) affected(stmt, mysqlLimit string) string {
	if e.Kind == "postgres" {
		return "WITH r AS (" + stmt + " RETURNING 1) SELECT count(*) AS affected FROM r;\n"
	}
	return MySQLSessionPrelude + stmt + mysqlLimit + ";\nSELECT ROW_COUNT() AS affected;\n"
}

// UpdateSQL은 기본 키로 고른 행 하나의 열을 바꾸는 스크립트다(결과: affected).
func (e RowEdit) UpdateSQL() (string, error) {
	ref, err := e.check(true, true)
	if err != nil {
		return "", err
	}
	set, err := e.assignments(e.Set, ", ", false)
	if err != nil {
		return "", err
	}
	where, err := e.assignments(e.PK, " AND ", true)
	if err != nil {
		return "", err
	}
	return e.affected("UPDATE "+ref+" SET "+set+" WHERE "+where, " LIMIT 1"), nil
}

// DeleteSQL은 기본 키로 고른 행 하나를 지우는 스크립트다(결과: affected).
func (e RowEdit) DeleteSQL() (string, error) {
	ref, err := e.check(true, false)
	if err != nil {
		return "", err
	}
	where, err := e.assignments(e.PK, " AND ", true)
	if err != nil {
		return "", err
	}
	return e.affected("DELETE FROM "+ref+" WHERE "+where, " LIMIT 1"), nil
}

// InsertSQL은 행 하나를 넣는 스크립트다(Set이 비면 모든 열이 기본값, 결과: affected).
func (e RowEdit) InsertSQL() (string, error) {
	ref, err := e.check(false, false)
	if err != nil {
		return "", err
	}
	if len(e.Set) == 0 {
		if e.Kind == "postgres" {
			return e.affected("INSERT INTO "+ref+" DEFAULT VALUES", ""), nil
		}
		return e.affected("INSERT INTO "+ref+" () VALUES ()", ""), nil
	}
	cols, vals := []string{}, []string{}
	for _, k := range sortedMapKeys(e.Set) {
		id, err := QuoteIdent(e.Kind, k)
		if err != nil {
			return "", fmt.Errorf("column %q: %v", k, err)
		}
		v, err := SQLValue(e.Kind, e.Set[k])
		if err != nil {
			return "", fmt.Errorf("column %q: %v", k, err)
		}
		cols, vals = append(cols, id), append(vals, v)
	}
	return e.affected("INSERT INTO "+ref+" ("+strings.Join(cols, ", ")+") VALUES ("+strings.Join(vals, ", ")+")", ""), nil
}

// ParseAffected는 편집 스크립트 결과의 affected 값을 읽는다.
func ParseAffected(res *Result) (int64, error) {
	if res == nil || res.Kind != "table" || len(res.Rows) != 1 || len(res.Rows[0]) != 1 {
		return 0, errors.New("unexpected edit result")
	}
	s, _ := res.Rows[0][0].(string)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, errors.New("unexpected edit result")
	}
	return n, nil
}
