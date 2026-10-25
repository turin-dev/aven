package agentprotocol

import (
	"errors"
	"fmt"
	"net"
	"net/url"
)

// DB 논리 백업/복원(원격 노드).
//
//   - db.backup(stream): agent가 이 노드의 aven DB 컨테이너(role=app)를 확인하고, 그 이미지로 일회성 helper 컨테이너를 만들어
//     덤프 도구(pg_dump, mysqldump, redis-cli --rdb, mongodump)를 실행한다. stdout을 DataChunk로 보내고 DBBackupResult로 끝난다.
//   - upload.begin{kind:"db.restore", db}: 받은 데이터를 upload.finish 때 복원 도구(pg_restore, mysql, mongorestore,
//     redis RDB 배치)의 stdin으로 넣는다.
//
// 서버는 스크립트·호스트·컨테이너 설정을 보내지 못한다. 스크립트는 아래 표(agent 바이너리에 고정)에서 고르고, 접속 주소는
// agent가 DB 컨테이너를 조회해 정한다. 서버가 보내는 것은 종류·대상 컨테이너·포트·이미지·자격 증명(환경변수로만 넘긴다)뿐이다.
const UploadDBTool = "db.restore"

// DBToolRequest는 db.backup과 upload.begin{kind:"db.restore"}의 대상이다(agent가 엄격하게 검사한다).
type DBToolRequest struct {
	Kind      string `json:"kind"`      // postgres | mysql | mariadb | redis | mongo
	Container string `json:"container"` // 이 노드의 aven DB 컨테이너(keel.role=app)
	Network   string `json:"network"`   // 컨테이너가 붙은 환경 네트워크
	Port      int    `json:"port"`      // DB가 listen하는 포트
	Image     string `json:"image"`     // helper 컨테이너 이미지(DB 이미지: 도구가 들어 있다)
	Job       string `json:"job"`       // helper 컨테이너 이름 접미사(keel-dbb-<job> / keel-dbr-<job>)
	User      string `json:"user,omitempty"`
	Database  string `json:"database,omitempty"`
	Password  string `json:"password,omitempty"`
	// Volume/MountPath는 redis 복원과 postgres PITR(pitr-wal, pitr-restore): DB 컨테이너의 데이터 볼륨을 helper에 붙인다.
	Volume    string `json:"volume,omitempty"`
	MountPath string `json:"mount_path,omitempty"`
	// Action이 비면 논리 덤프/복원이다. pitr-*는 시점 복구 도구(dbpitr.go)다.
	Action       string   `json:"action,omitempty"`
	Files        []string `json:"files,omitempty"`         // pitr-binlog: 받을 닫힌 binlog
	TargetTime   string   `json:"target_time,omitempty"`   // pitr-restore: RFC3339 UTC
	DataDir      string   `json:"data_dir,omitempty"`      // pitr-restore(postgres): PGDATA
	KeepArchive  bool     `json:"keep_archive,omitempty"`  // pitr-restore(postgres)
	BaseFile     string   `json:"base_file,omitempty"`     // pitr-restore(mysql): 기준 백업의 binlog 위치
	BasePosition uint64   `json:"base_position,omitempty"` // pitr-restore(mysql)
}

// DBBackupResult는 db.backup의 최종 응답이다(보낸 총 바이트, 검증용).
type DBBackupResult struct {
	Bytes int64 `json:"bytes"`
}

// DBCreds는 덤프/복원 도구가 쓰는 자격 증명이다(스크립트에 넣지 않고 환경변수로만 넘긴다).
type DBCreds struct {
	User     string
	Database string
	Password string
}

// DBTool은 일회성 helper 컨테이너가 실행할 셸 스크립트와 환경이다.
type DBTool struct {
	Script  string
	Env     map[string]string
	Ext     string // 백업 파일 확장자
	Gzip    bool   // 서버가 덤프 스트림을 gzip으로 압축한다(복원 때는 풀어서 stdin으로 넣는다)
	DataDir bool   // 복원 때 대상 볼륨을 DATADIR에 마운트해야 한다(redis)
}

var dbDefaultPorts = map[string]int{"postgres": 5432, "mysql": 3306, "mariadb": 3306, "redis": 6379, "mongo": 27017}

// IsDBKind는 논리 백업을 지원하는 DB 종류인지 알려준다.
func IsDBKind(kind string) bool { _, ok := dbDefaultPorts[kind]; return ok }

// DBPort는 DB 서비스가 listen하는 포트다(port가 0이면 종류의 기본 포트).
func DBPort(kind string, port int) int {
	if port > 0 {
		return port
	}
	return dbDefaultPorts[kind]
}

const mysqlPrelude = `MYSQL=$(command -v mariadb || command -v mysql) || { echo "mysql client not found in image" >&2; exit 127; }
DUMP=$(command -v mariadb-dump || command -v mysqldump) || { echo "mysqldump not found in image" >&2; exit 127; }
`

// DBDumpTool은 kind의 덤프 명령을 만든다. 덤프는 stdout으로 나온다.
func DBDumpTool(kind, host string, port int, c DBCreds) (*DBTool, error) {
	port = DBPort(kind, port)
	env := map[string]string{"DBHOST": host, "DBPORT": fmt.Sprint(port)}
	switch kind {
	case "postgres":
		env["DBUSER"], env["DBNAME"], env["PGPASSWORD"] = c.User, c.Database, c.Password
		return &DBTool{Env: env, Ext: "dump",
			Script: `exec pg_dump -Fc -h "$DBHOST" -p "$DBPORT" -U "$DBUSER" -d "$DBNAME"`}, nil
	case "mysql", "mariadb":
		env["DBUSER"], env["MYSQL_PWD"] = c.User, c.Password
		return &DBTool{Env: env, Ext: "sql.gz", Gzip: true, Script: mysqlPrelude +
			`DBS=$("$MYSQL" -h "$DBHOST" -P "$DBPORT" -u "$DBUSER" -N -B -e 'SHOW DATABASES' | grep -Ev '^(information_schema|performance_schema|mysql|sys)$')
[ -n "$DBS" ] || { echo "no user databases to dump" >&2; exit 3; }
exec "$DUMP" -h "$DBHOST" -P "$DBPORT" -u "$DBUSER" --single-transaction --routines --triggers --events --databases $DBS`}, nil
	case "redis":
		env["REDISCLI_AUTH"] = c.Password
		return &DBTool{Env: env, Ext: "rdb",
			Script: `set -e
redis-cli -h "$DBHOST" -p "$DBPORT" --rdb /tmp/aven.rdb >&2
cat /tmp/aven.rdb`}, nil
	case "mongo":
		u, err := MongoURI(host, port, c)
		if err != nil {
			return nil, err
		}
		env["MONGO_URI"] = u
		return &DBTool{Env: env, Ext: "archive.gz", Script: `exec mongodump --uri "$MONGO_URI" --archive --gzip`}, nil
	}
	return nil, fmt.Errorf("logical backup is not supported for %q", kind)
}

// DBRestoreTool은 kind의 복원 명령을 만든다. 덤프는 stdin으로 들어온다(mysql은 gunzip한 SQL).
func DBRestoreTool(kind, host string, port int, c DBCreds) (*DBTool, error) {
	t, err := DBDumpTool(kind, host, port, c)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "postgres":
		t.Script = `exec pg_restore --clean --if-exists --no-owner --no-privileges -h "$DBHOST" -p "$DBPORT" -U "$DBUSER" -d "$DBNAME"`
	case "mysql", "mariadb":
		t.Script = mysqlPrelude + `exec "$MYSQL" -h "$DBHOST" -P "$DBPORT" -u "$DBUSER"`
	case "redis":
		// RDB는 실행 중인 서버에 넣을 수 없다. 서비스를 멈춘 상태에서 볼륨에 dump.rdb를 놓고, 임시 redis로 읽어 AOF로 바꿔 둔다
		// (서비스가 appendonly yes로 시작해도 같은 데이터로 올라온다).
		t.DataDir = true
		t.Script = `set -e
cd "$DATADIR"
rm -rf appendonlydir appendonly.aof dump.rdb
cat > dump.rdb
[ -s dump.rdb ] || { echo "empty rdb" >&2; exit 2; }
redis-server --port 6399 --bind 127.0.0.1 --dir "$DATADIR" --dbfilename dump.rdb --appendonly no --save "" --daemonize yes >/dev/null
i=0
until redis-cli -p 6399 ping 2>/dev/null | grep -q PONG; do i=$((i+1)); [ $i -lt 600 ] || { echo "temporary redis did not start" >&2; exit 4; }; sleep 0.5; done
redis-cli -p 6399 config set appendonly yes >/dev/null
sleep 1
while [ "$(redis-cli -p 6399 info persistence | tr -d '\r' | sed -n 's/^aof_rewrite_in_progress:\(.*\)/\1/p')" != "0" ]; do sleep 0.5; done
redis-cli -p 6399 shutdown nosave >/dev/null 2>&1 || true
echo "restored $(wc -c < dump.rdb) bytes" >&2`
		delete(t.Env, "DBHOST")
	case "mongo":
		t.Script = `exec mongorestore --uri "$MONGO_URI" --archive --gzip --drop`
		t.Gzip = false
	}
	return t, nil
}

// MongoURI는 mongodump/mongorestore 접속 URI다(사용자/비밀번호는 URL 인코딩).
func MongoURI(host string, port int, c DBCreds) (string, error) {
	if c.User == "" {
		return "", errors.New("mongo user is not set (MONGOUSER / MONGO_INITDB_ROOT_USERNAME)")
	}
	u := url.URL{Scheme: "mongodb", User: url.UserPassword(c.User, c.Password), Host: net.JoinHostPort(host, fmt.Sprint(port)), Path: "/", RawQuery: "authSource=admin"}
	return u.String(), nil
}
