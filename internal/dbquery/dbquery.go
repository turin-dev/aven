// Package dbquery는 DB 서비스의 데이터 브라우저용 명령을 만들고 출력을 해석한다.
//
// 명령은 실행 중인 DB 컨테이너 안에서 그 DB의 CLI(psql, mysql/mariadb, mongosh, redis-cli)로 실행한다(일회성 exec).
// 비밀번호는 환경변수(PGPASSWORD, MYSQL_PWD, REDISCLI_AUTH, MONGO_URI)로만 넘기고 argv에 넣지 않는다.
// 쿼리 본문은 stdin으로 넘긴다(argv 길이 제한과 셸 인용 문제를 피한다).
// 기계가 읽는 출력: postgres는 psql --csv, mysql은 --batch TSV, mongo는 mongosh --json=relaxed, redis는 redis-cli --no-raw.
package dbquery

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// MaxRows는 결과 표에 담는 최대 행 수다.
	MaxRows = 1000
	// MaxOutput은 명령 stdout 상한이다.
	MaxOutput = 1 << 20
	// MaxQuery는 쿼리 본문 최대 크기다.
	MaxQuery = 64 << 10
	// TimeoutS는 명령 실행 시간 제한(초)이다.
	TimeoutS = 30
	// NullMarker는 psql이 NULL 대신 찍는 표시다(CSV에서 빈 문자열과 구분하기 위해).
	NullMarker = "\x01NULL\x01"
	// KillTimeoutS는 시간 초과 뒤 정리 명령(Cleanup)의 실행 시간 제한(초)이다.
	KillTimeoutS = 10
	// MongoMaxTimeMS는 mongo 탐색 쿼리의 서버 쪽 실행 시간 제한이다.
	MongoMaxTimeMS = (TimeoutS - 5) * 1000
)

// Command는 컨테이너에서 실행할 일회성 명령이다.
type Command struct {
	Cmd   []string
	Env   []string
	Stdin []byte
}

// Target은 데이터 브라우저가 접속할 DB다. Vars는 컨테이너의 최종 환경변수다.
type Target struct {
	Kind string // postgres | mysql | mariadb | redis | mongo
	Port int
	Vars map[string]string
	// Tag는 요청 하나를 찾는 표시다(NewTag). 있으면 클라이언트 프로세스 환경변수 KEEL_DATA_TAG, postgres
	// application_name keel-data-<tag>, mysql 쿼리 앞 주석 /* keel-data:<tag> */로 남겨 시간 초과 때 정리한다(Cleanup).
	Tag string
	// RESP3이면 redis-cli를 -3으로 실행한다(map/set/double 등 RESP3 응답).
	RESP3 bool
}

// tagEnv는 Tag가 있으면 클라이언트 프로세스를 찾는 환경변수를 붙인다.
func (t Target) tagEnv(c Command) Command {
	if t.Tag != "" {
		c.Env = append(c.Env, kv("AVEN_DATA_TAG", t.Tag), kv("KEEL_DATA_TAG", t.Tag))
	}
	return c
}

// appName은 postgres application_name이다.
func (t Target) appName() string {
	if t.Tag == "" {
		return "aven-data"
	}
	return "aven-data-" + t.Tag
}

var delimiterRe = regexp.MustCompile(`(?im)^\s*delimiter\s`)

// mysqlTagged는 쿼리 앞에 요청 표시 주석을 붙인다(processlist의 Info에 보인다). DELIMITER 명령이 있으면
// (클라이언트 명령은 버퍼가 비어 있을 때만 알아본다) 붙이지 않는다.
func (t Target) mysqlTagged(query string) string {
	if t.Tag == "" || delimiterRe.MatchString(query) {
		return query
	}
	// 주석은 다음 문장 하나에 붙는다: 편집 스크립트는 세션 설정 다음 문장(실제 편집)에 붙인다.
	pre := ""
	if strings.HasPrefix(query, MySQLSessionPrelude) {
		pre, query = MySQLSessionPrelude, strings.TrimPrefix(query, MySQLSessionPrelude)
	}
	return pre + "/* aven-data:" + t.Tag + " */\n" + query
}

var defaultPorts = map[string]int{"postgres": 5432, "mysql": 3306, "mariadb": 3306, "redis": 6379, "mongo": 27017}

// Supported는 데이터 브라우저가 지원하는 DB 종류인지 알려준다.
func Supported(kind string) bool { _, ok := defaultPorts[kind]; return ok }

// IsSQL은 SQL DB(표 탐색 + SQL 쿼리)인지 알려준다.
func IsSQL(kind string) bool { return kind == "postgres" || kind == "mysql" || kind == "mariadb" }

func (t Target) port() int {
	if t.Port > 0 {
		return t.Port
	}
	return defaultPorts[t.Kind]
}

func first(vars map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := vars[k]; v != "" {
			return v
		}
	}
	return ""
}

// timeoutPrelude는 이미지에 timeout이 있으면 클라이언트를 실행 시간 제한과 함께 띄운다
// (서버가 exec 연결을 끊어도 컨테이너 안 프로세스는 남기 때문이다). busybox 옛 문법이면 쓰지 않는다.
const timeoutPrelude = `T=""
if timeout -s KILL 5 true >/dev/null 2>&1; then T="timeout -s KILL 40"; fi
`

func shell(script string) []string { return []string{"sh", "-c", timeoutPrelude + script} }

func kv(k, v string) string { return k + "=" + v }

// SQL은 pg/mysql에서 query를 실행하는 명령이다.
func (t Target) SQL(query string) (Command, error) {
	if err := checkQuery(query); err != nil {
		return Command{}, err
	}
	switch t.Kind {
	case "postgres":
		user := first(t.Vars, "PGUSER", "POSTGRES_USER")
		if user == "" {
			user = "postgres"
		}
		db := first(t.Vars, "PGDATABASE", "POSTGRES_DB")
		if db == "" {
			db = user
		}
		env := []string{kv("PGHOST", "127.0.0.1"), kv("PGPORT", fmt.Sprint(t.port())), kv("PGUSER", user), kv("PGDATABASE", db),
			kv("PGCONNECT_TIMEOUT", "10"), kv("PGAPPNAME", t.appName()), kv("PGOPTIONS", fmt.Sprintf("-c statement_timeout=%ds", TimeoutS)),
			kv("PSQLRC", "/dev/null")}
		if pw := first(t.Vars, "PGPASSWORD", "POSTGRES_PASSWORD"); pw != "" {
			env = append(env, kv("PGPASSWORD", pw))
		}
		return t.tagEnv(Command{Env: env, Stdin: []byte(query), Cmd: shell(
			`exec $T psql -X -q -v ON_ERROR_STOP=1 --csv -P "null=$KEEL_NULL" -f -`)}.withEnv("KEEL_NULL", NullMarker)), nil
	case "mysql", "mariadb":
		user := first(t.Vars, "MYSQLUSER")
		if user == "" {
			user = "root"
		}
		env := []string{kv("DBUSER", user), kv("DBPORT", fmt.Sprint(t.port())),
			kv("DBNAME", first(t.Vars, "MYSQLDATABASE", "MYSQL_DATABASE", "MARIADB_DATABASE"))}
		if pw := first(t.Vars, "MYSQLPASSWORD", "MYSQL_ROOT_PASSWORD", "MARIADB_ROOT_PASSWORD"); pw != "" {
			env = append(env, kv("MYSQL_PWD", pw))
		}
		// --comments: 요청 표시 주석을 서버에 보낸다(기본은 클라이언트가 주석을 지운다).
		return t.tagEnv(Command{Env: env, Stdin: []byte(t.mysqlTagged(query)), Cmd: shell(`M=$(command -v mariadb || command -v mysql) || { echo "mysql client not found in image" >&2; exit 127; }
set -- --protocol=TCP -h 127.0.0.1 -P "$DBPORT" -u "$DBUSER" --batch --comments --default-character-set=utf8mb4 --connect-timeout=10
[ -n "$DBNAME" ] && set -- "$@" -D "$DBNAME"
exec $T "$M" "$@"`)}), nil
	}
	return Command{}, fmt.Errorf("SQL is not supported for %q", t.Kind)
}

func (c Command) withEnv(k, v string) Command {
	c.Env = append(c.Env, kv(k, v))
	return c
}

// Mongo는 mongosh로 eval 식을 실행하는 명령이다(결과는 relaxed Extended JSON).
func (t Target) Mongo(eval string) (Command, error) {
	if t.Kind != "mongo" {
		return Command{}, errors.New("not a mongo database")
	}
	if err := checkQuery(eval); err != nil {
		return Command{}, err
	}
	u := url.URL{Scheme: "mongodb", Host: fmt.Sprintf("127.0.0.1:%d", t.port()), Path: "/"}
	if user := first(t.Vars, "MONGOUSER", "MONGO_INITDB_ROOT_USERNAME"); user != "" {
		u.User = url.UserPassword(user, first(t.Vars, "MONGOPASSWORD", "MONGO_INITDB_ROOT_PASSWORD"))
		u.RawQuery = "authSource=admin"
	}
	if db := first(t.Vars, "MONGODATABASE"); db != "" && validMongoDB(db) == nil {
		u.Path = "/" + db
	}
	// 접속 URI(비밀번호 포함)는 환경변수로 넘긴다. eval 식은 stdin으로 받는다.
	return t.tagEnv(Command{Env: []string{kv("MONGO_URI", u.String())}, Stdin: []byte(eval), Cmd: shell(
		`M=$(command -v mongosh) || { echo "mongosh not found in image (MongoDB 6+ images include it)" >&2; exit 127; }
exec $T "$M" "$MONGO_URI" --quiet --norc --json=relaxed --eval "$(cat)"`)}), nil
}

// Redis는 redis-cli로 명령들을 실행한다(stdin 한 줄에 명령 하나, --no-raw 출력). db는 논리 DB 번호다.
func (t Target) Redis(db int, script string) (Command, error) {
	if t.Kind != "redis" {
		return Command{}, errors.New("not a redis database")
	}
	if db < 0 || db > 1000 {
		return Command{}, errors.New("db must be 0..1000")
	}
	if err := checkQuery(script); err != nil {
		return Command{}, err
	}
	env := []string{kv("DBPORT", fmt.Sprint(t.port())), kv("DBN", fmt.Sprint(db))}
	if pw := first(t.Vars, "REDISPASSWORD", "REDIS_PASSWORD"); pw != "" {
		env = append(env, kv("REDISCLI_AUTH", pw))
	}
	if !strings.HasSuffix(script, "\n") {
		script += "\n"
	}
	proto := ""
	if t.RESP3 {
		proto = " -3"
	}
	return t.tagEnv(Command{Env: env, Stdin: []byte(script), Cmd: shell(`exec $T redis-cli --no-raw` + proto + ` -h 127.0.0.1 -p "$DBPORT" -n "$DBN"`)}), nil
}

func checkQuery(q string) error {
	if strings.TrimSpace(q) == "" {
		return errors.New("query is empty")
	}
	if len(q) > MaxQuery {
		return fmt.Errorf("query is too large (max %d bytes)", MaxQuery)
	}
	if strings.ContainsRune(q, 0) || !utf8.ValidString(q) {
		return errors.New("query must be valid UTF-8 without NUL")
	}
	return nil
}

// ---- identifiers ----

// ValidIdent는 SQL 식별자(스키마/표 이름)로 받을 수 있는 값인지 검사한다. 인용은 QuoteIdent가 한다.
func ValidIdent(s string) error {
	if s == "" || len(s) > 128 {
		return errors.New("name must be 1..128 bytes")
	}
	if !utf8.ValidString(s) {
		return errors.New("name must be valid UTF-8")
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return errors.New("name must not contain control characters")
		}
	}
	return nil
}

// QuoteIdent는 kind의 규칙으로 식별자를 인용한다: postgres는 큰따옴표(안의 "는 두 번), mysql은 백틱(안의 백틱은 두 번).
func QuoteIdent(kind, s string) (string, error) {
	if err := ValidIdent(s); err != nil {
		return "", err
	}
	switch kind {
	case "postgres":
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`, nil
	case "mysql", "mariadb":
		return "`" + strings.ReplaceAll(s, "`", "``") + "`", nil
	}
	return "", fmt.Errorf("identifiers are not supported for %q", kind)
}

// TablesQuery는 사용자 표 목록 쿼리다. 열: schema, name, kind, rows(근삿값, 모르면 NULL).
func TablesQuery(kind string) string {
	switch kind {
	case "postgres":
		return `SELECT n.nspname AS schema, c.relname AS name,
  CASE c.relkind WHEN 'v' THEN 'view' WHEN 'm' THEN 'materialized_view' WHEN 'f' THEN 'foreign_table' ELSE 'table' END AS kind,
  CASE WHEN c.reltuples < 0 OR c.relkind IN ('v','f') THEN NULL ELSE c.reltuples::bigint END AS rows
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r','p','v','m','f') AND NOT c.relispartition
  AND n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg\_toast%' AND n.nspname NOT LIKE 'pg\_temp%'
ORDER BY 1, 2 LIMIT 1000;
`
	case "mysql", "mariadb":
		return "SELECT table_schema AS `schema`, table_name AS `name`, IF(table_type = 'VIEW', 'view', 'table') AS `kind`, table_rows AS `rows`\n" +
			"FROM information_schema.tables WHERE table_schema NOT IN ('mysql','information_schema','performance_schema','sys')\n" +
			"ORDER BY 1, 2 LIMIT 1000;\n"
	}
	return ""
}

// RowsQuery는 표 하나의 행을 읽는 쿼리다(limit+1개를 읽어 다음 페이지가 있는지 안다).
func RowsQuery(kind, schema, table string, limit, offset int) (string, error) {
	if limit < 1 || limit > 500 || offset < 0 || offset > 100_000_000 {
		return "", errors.New("limit must be 1..500 and offset 0..100000000")
	}
	t, err := QuoteIdent(kind, table)
	if err != nil {
		return "", err
	}
	ref := t
	if schema != "" {
		s, err := QuoteIdent(kind, schema)
		if err != nil {
			return "", err
		}
		ref = s + "." + t
	}
	return fmt.Sprintf("SELECT * FROM %s LIMIT %d OFFSET %d;\n", ref, limit+1, offset), nil
}

// ---- mongo ----

func jsStr(s string) string {
	b, _ := json.Marshal(s) // JSON 문자열은 JS 문자열 리터럴이다(U+2028/2029도 이스케이프된다)
	return string(b)
}

func validMongoDB(s string) error {
	if s == "" || len(s) > 64 || strings.ContainsAny(s, "/\\. \"$\x00") || !utf8.ValidString(s) {
		return errors.New("invalid database name")
	}
	return nil
}

// ValidCollection은 mongo 컬렉션 이름을 검사한다.
func ValidCollection(s string) error {
	if s == "" || len(s) > 255 || strings.ContainsAny(s, "$\x00") || strings.HasPrefix(s, "system.") || !utf8.ValidString(s) {
		return errors.New("invalid collection name")
	}
	return nil
}

// MongoCollectionsEval은 사용자 DB의 컬렉션 목록과 문서 수(추정)를 돌려주는 식이다.
const MongoCollectionsEval = `(() => {
  const out = [];
  for (const d of db.adminCommand({ listDatabases: 1, nameOnly: true }).databases) {
    if (["admin", "config", "local"].includes(d.name)) continue;
    const s = db.getSiblingDB(d.name);
    for (const c of s.getCollectionInfos({}, { nameOnly: true })) {
      if (c.name.startsWith("system.")) continue;
      let n = null;
      if (c.type !== "view") { try { n = s.getCollection(c.name).estimatedDocumentCount(); } catch (e) {} }
      out.push({ schema: d.name, name: c.name, kind: c.type === "view" ? "view" : "collection", rows: n });
    }
  }
  return out;
})()`

// IsMongoCursorErr는 mongosh --json이 결과(커서 객체)를 직렬화하지 못했다는 오류인지 알려준다.
func IsMongoCursorErr(msg string) bool {
	return strings.Contains(msg, "Converting circular structure to EJSON")
}

// MongoCursorEval은 커서를 돌려주는 식 하나를 배열로 바꾸는 식이다. 여러 문장이면 mongosh가 실행 전에
// 구문 오류를 낸다(부작용 없음). 줄 끝 주석이 있어도 되게 닫는 괄호 앞에 줄을 바꾼다.
func MongoCursorEval(eval string) (string, bool) {
	e := strings.TrimRight(strings.TrimSpace(eval), "; \t\r\n")
	if e == "" {
		return "", false
	}
	return "(" + e + "\n).toArray()", true
}

// MongoErrorMessage는 mongosh --json이 찍는 오류 객체({"message":..., "stack":...})에서 메시지만 꺼낸다.
// JSON이 아니면 그대로 돌려준다.
func MongoErrorMessage(msg string) string {
	var e struct {
		Message  string `json:"message"`
		CodeName string `json:"codeName"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSuffix(msg, "…")), &e); err != nil || e.Message == "" {
		// 2000자에서 잘렸으면 JSON이 깨진다: "message" 값만 직접 읽는다
		if i := strings.Index(msg, `"message": "`); i >= 0 && strings.HasPrefix(strings.TrimSpace(msg), "{") {
			var s string
			if json.NewDecoder(strings.NewReader(msg[i+len(`"message": `):])).Decode(&s) == nil && s != "" {
				return s
			}
		}
		return msg
	}
	if e.CodeName != "" && !strings.Contains(e.Message, e.CodeName) {
		return e.Message + " (" + e.CodeName + ")"
	}
	return e.Message
}

// MongoFindEval은 컬렉션의 문서를 읽는 식이다(이름은 JSON 문자열 리터럴로 넣어 주입을 막는다).
func MongoFindEval(dbName, coll string, limit, offset int) (string, error) {
	if err := validMongoDB(dbName); err != nil {
		return "", err
	}
	if err := ValidCollection(coll); err != nil {
		return "", err
	}
	if limit < 1 || limit > 500 || offset < 0 || offset > 100_000_000 {
		return "", errors.New("limit must be 1..500 and offset 0..100000000")
	}
	// maxTimeMS: 서버 쪽에서도 실행 시간 제한보다 먼저 멈춘다(클라이언트를 끊어도 서버 작업은 남기 때문이다).
	return fmt.Sprintf("db.getSiblingDB(%s).getCollection(%s).find({}).skip(%d).limit(%d).maxTimeMS(%d).toArray()",
		jsStr(dbName), jsStr(coll), offset, limit+1, MongoMaxTimeMS), nil
}
