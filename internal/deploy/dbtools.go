package deploy

import (
	"fmt"

	"github.com/turin-dev/aven/internal/agentprotocol"
)

// dbTool은 DB 논리 백업/복원용 일회성 컨테이너가 실행할 셸 스크립트와 환경이다.
// 비밀번호는 스크립트(argv)에 넣지 않고 환경변수로만 넘긴다. 스크립트 표는 agent와 같이 쓴다(agentprotocol/dbtool.go):
// 원격 노드에서는 agent가 같은 표로 helper 컨테이너를 만든다(서버는 스크립트를 보내지 않는다).
type dbTool = agentprotocol.DBTool

// IsDBKind는 논리 백업을 지원하는 DB 종류인지 알려준다.
func IsDBKind(kind string) bool { return agentprotocol.IsDBKind(kind) }

// SameDBFamily는 백업을 이 종류의 DB에 복원할 수 있는지 알려준다(mysql과 mariadb는 서로 복원 가능).
func SameDBFamily(a, b string) bool {
	norm := func(k string) string {
		if k == "mariadb" {
			return "mysql"
		}
		return k
	}
	return norm(a) == norm(b)
}

func first(vars map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := vars[k]; v != "" {
			return v
		}
	}
	return ""
}

// DBPort는 DB 서비스가 listen하는 포트다.
func DBPort(kind string, port int) int { return agentprotocol.DBPort(kind, port) }

func dbCreds(kind string, vars map[string]string) agentprotocol.DBCreds {
	switch kind {
	case "postgres":
		user := first(vars, "PGUSER", "POSTGRES_USER")
		if user == "" {
			user = "postgres"
		}
		db := first(vars, "PGDATABASE", "POSTGRES_DB")
		if db == "" {
			db = user
		}
		return agentprotocol.DBCreds{User: user, Database: db, Password: first(vars, "PGPASSWORD", "POSTGRES_PASSWORD")}
	case "mysql", "mariadb":
		user := first(vars, "MYSQLUSER")
		if user == "" {
			user = "root"
		}
		return agentprotocol.DBCreds{User: user, Password: first(vars, "MYSQLPASSWORD", "MYSQL_ROOT_PASSWORD", "MARIADB_ROOT_PASSWORD")}
	case "redis":
		return agentprotocol.DBCreds{Password: first(vars, "REDISPASSWORD", "REDIS_PASSWORD")}
	case "mongo":
		return agentprotocol.DBCreds{User: first(vars, "MONGOUSER", "MONGO_INITDB_ROOT_USERNAME"), Password: first(vars, "MONGOPASSWORD", "MONGO_INITDB_ROOT_PASSWORD")}
	}
	return agentprotocol.DBCreds{}
}

// dumpTool은 kind의 덤프 명령을 만든다. 덤프는 stdout으로 나온다.
func dumpTool(kind, host string, port int, vars map[string]string) (*dbTool, error) {
	if !IsDBKind(kind) {
		return nil, fmt.Errorf("logical backup is not supported for %q", kind)
	}
	return agentprotocol.DBDumpTool(kind, host, port, dbCreds(kind, vars))
}

// restoreTool은 kind의 복원 명령을 만든다. 덤프는 stdin으로 들어온다(mysql은 gunzip한 SQL).
func restoreTool(kind, host string, port int, vars map[string]string) (*dbTool, error) {
	if !IsDBKind(kind) {
		return nil, fmt.Errorf("logical backup is not supported for %q", kind)
	}
	return agentprotocol.DBRestoreTool(kind, host, port, dbCreds(kind, vars))
}

func mongoURI(host string, port int, vars map[string]string) (string, error) {
	return agentprotocol.MongoURI(host, port, dbCreds("mongo", vars))
}
