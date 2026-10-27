// Package catalog는 데이터베이스 서비스 정의와 내장 템플릿이다.

package catalog

import (
	"embed"
	"io/fs"
	"sort"
	"strings"

	"github.com/turin-dev/aven/internal/store"
)

// ServiceDef는 서비스 하나를 만드는 데 필요한 설정이다.
type ServiceDef struct {
	Name             string
	Icon             string
	SourceType       string // image | database
	DatabaseKind     string
	Image            string
	Port             int
	HealthcheckType  string
	HealthcheckPath  string
	HealthcheckTimeo int
	StartCommand     string
	PreDeployCommand string
	Volumes          []string // mount paths
	Variables        map[string]string
	Domain           bool // generated domain을 만든다(AVEN_BASE_DOMAIN이 있을 때)
	// 아래는 템플릿 정의(JSON)에서 오는 추가 설정이다.
	Domains      []DomainDef    // generated domain(대상 포트 지정 가능)
	VolumeLimits map[string]int // mount path → size_limit_mb
	TCPProxyPort int            // >0이면 TCP proxy를 켠다(컨테이너 포트)
	Canvas       *store.Canvas
	RepoURL      string // source.repo
	Branch       string
	RootDir      string
	Settings     Settings // 인스턴스 PATCH API와 같은 이름의 설정(위 필드 뒤에 덮어쓴다)
	// GeneratedPassword는 DB 서비스에서 만든 비밀번호다(다른 환경에서는 새로 만든다).
	GeneratedPassword string
}

// DomainDef는 배포할 때 만들 generated domain이다.
type DomainDef struct {
	TargetPort *int
}

type DatabaseDef struct {
	Kind      string
	Image     string
	Port      int
	DataPath  string
	Start     string
	Variables func(password string) map[string]string
	// HealthcheckPath가 있으면 http healthcheck를 쓴다(없으면 tcp).
	HealthcheckPath string
	// Domain이면 generated domain을 만든다(AVEN_BASE_DOMAIN이 있을 때, 예: bucket의 공개 S3 endpoint).
	Domain bool
}

var Databases = map[string]DatabaseDef{
	"postgres": {
		Kind: "postgres", Image: "postgres:17-alpine", Port: 5432, DataPath: "/var/lib/postgresql/data",
		Variables: func(pw string) map[string]string {
			return map[string]string{
				"POSTGRES_USER":       "postgres",
				"POSTGRES_PASSWORD":   pw,
				"POSTGRES_DB":         "aven",
				"PGDATA":              "/var/lib/postgresql/data/pgdata",
				"PGUSER":              "${{POSTGRES_USER}}",
				"PGPASSWORD":          "${{POSTGRES_PASSWORD}}",
				"PGDATABASE":          "${{POSTGRES_DB}}",
				"PGHOST":              "${{AVEN_PRIVATE_DOMAIN}}",
				"PGPORT":              "5432",
				"DATABASE_URL":        "postgresql://${{PGUSER}}:${{PGPASSWORD}}@${{PGHOST}}:${{PGPORT}}/${{PGDATABASE}}",
				"DATABASE_PUBLIC_URL": "postgresql://${{PGUSER}}:${{PGPASSWORD}}@${{AVEN_TCP_PROXY_DOMAIN}}:${{AVEN_TCP_PROXY_PORT}}/${{PGDATABASE}}",
			}
		},
	},
	"mysql": {
		Kind: "mysql", Image: "mysql:8.4", Port: 3306, DataPath: "/var/lib/mysql",
		Variables: func(pw string) map[string]string {
			return map[string]string{
				"MYSQL_ROOT_PASSWORD": pw,
				"MYSQL_DATABASE":      "aven",
				"MYSQLUSER":           "root",
				"MYSQLPASSWORD":       "${{MYSQL_ROOT_PASSWORD}}",
				"MYSQLDATABASE":       "${{MYSQL_DATABASE}}",
				"MYSQLHOST":           "${{AVEN_PRIVATE_DOMAIN}}",
				"MYSQLPORT":           "3306",
				"MYSQL_URL":           "mysql://${{MYSQLUSER}}:${{MYSQLPASSWORD}}@${{MYSQLHOST}}:${{MYSQLPORT}}/${{MYSQLDATABASE}}",
				"MYSQL_PUBLIC_URL":    "mysql://${{MYSQLUSER}}:${{MYSQLPASSWORD}}@${{AVEN_TCP_PROXY_DOMAIN}}:${{AVEN_TCP_PROXY_PORT}}/${{MYSQLDATABASE}}",
			}
		},
	},
	"redis": {
		Kind: "redis", Image: "redis:7.4-alpine", Port: 6379, DataPath: "/data",
		Start: `docker-entrypoint.sh redis-server --requirepass "$REDIS_PASSWORD" --appendonly yes`,
		Variables: func(pw string) map[string]string {
			return map[string]string{
				"REDIS_PASSWORD":   pw,
				"REDISUSER":        "default",
				"REDISPASSWORD":    "${{REDIS_PASSWORD}}",
				"REDISHOST":        "${{AVEN_PRIVATE_DOMAIN}}",
				"REDISPORT":        "6379",
				"REDIS_URL":        "redis://${{REDISUSER}}:${{REDISPASSWORD}}@${{REDISHOST}}:${{REDISPORT}}",
				"REDIS_PUBLIC_URL": "redis://${{REDISUSER}}:${{REDISPASSWORD}}@${{AVEN_TCP_PROXY_DOMAIN}}:${{AVEN_TCP_PROXY_PORT}}",
			}
		},
	},
	"mongo": {
		Kind: "mongo", Image: "mongo:8.0", Port: 27017, DataPath: "/data/db",
		Variables: func(pw string) map[string]string {
			return map[string]string{
				"MONGO_INITDB_ROOT_USERNAME": "mongo",
				"MONGO_INITDB_ROOT_PASSWORD": pw,
				"MONGOUSER":                  "${{MONGO_INITDB_ROOT_USERNAME}}",
				"MONGOPASSWORD":              "${{MONGO_INITDB_ROOT_PASSWORD}}",
				"MONGOHOST":                  "${{AVEN_PRIVATE_DOMAIN}}",
				"MONGOPORT":                  "27017",
				"MONGO_URL":                  "mongodb://${{MONGOUSER}}:${{MONGOPASSWORD}}@${{MONGOHOST}}:${{MONGOPORT}}",
				"MONGO_PUBLIC_URL":           "mongodb://${{MONGOUSER}}:${{MONGOPASSWORD}}@${{AVEN_TCP_PROXY_DOMAIN}}:${{AVEN_TCP_PROXY_PORT}}",
			}
		},
	},
}

// CredentialKeys는 DB 서비스(kind)가 환경마다 새로 만드는 비밀번호 변수 이름이다:
// 생성된 비밀번호 자체(POSTGRES_PASSWORD 등)와 그것만 가리키는 별칭(PGPASSWORD 등).
// 환경 동기화는 이 키를 옮기지도 지우지도 않는다(대상 DB의 데이터 디렉터리는 자기 비밀번호로 초기화돼 있다).
func CredentialKeys(kind string) map[string]bool {
	db, ok := Databases[kind]
	if !ok {
		return nil
	}
	const probe = "\x00keel-credential-probe\x00"
	vars := db.Variables(probe)
	other := db.Variables(probe + "2")
	out := map[string]bool{}
	for k, v := range vars {
		// 비밀번호 자체이거나 비밀번호에서 만든 값(bucket의 access key, bucket 이름 등)
		if v == probe || v != other[k] {
			out[k] = true
		}
	}
	direct := make(map[string]bool, len(out))
	for k := range out {
		direct[k] = true
	}
	for k, v := range vars {
		for pk := range direct {
			if strings.ReplaceAll(v, " ", "") == "${{"+pk+"}}" {
				out[k] = true
			}
		}
	}
	return out
}

// DatabaseService는 데이터베이스 서비스 정의를 만든다(비밀번호 생성 포함).
func DatabaseService(kind string) (ServiceDef, bool) {
	db, ok := Databases[kind]
	if !ok {
		return ServiceDef{}, false
	}
	pw := store.RandomSecret(32)
	def := ServiceDef{
		Name: kind, Icon: kind, SourceType: "database", DatabaseKind: kind, Image: db.Image, Port: db.Port,
		HealthcheckType: "tcp", StartCommand: db.Start, Volumes: []string{db.DataPath},
		Variables: db.Variables(pw), GeneratedPassword: pw, Domain: db.Domain,
	}
	if db.HealthcheckPath != "" {
		def.HealthcheckType, def.HealthcheckPath = "http", db.HealthcheckPath
	}
	return def, true
}

// ApplyToInstance는 정의를 인스턴스 설정에 반영한다.
func (d ServiceDef) ApplyToInstance(in *store.Instance) {
	sp := &in.Spec
	if d.Image != "" {
		img := d.Image
		sp.Image = &img
	}
	if d.Port > 0 {
		p := d.Port
		sp.Port = &p
	}
	if d.HealthcheckType != "" {
		sp.HealthcheckType = d.HealthcheckType
	}
	if d.HealthcheckPath != "" {
		sp.HealthcheckPath = d.HealthcheckPath
	}
	if d.HealthcheckTimeo > 0 {
		sp.HealthcheckTimeoutS = d.HealthcheckTimeo
	}
	if d.StartCommand != "" {
		c := d.StartCommand
		sp.StartCommand = &c
	}
	if d.PreDeployCommand != "" {
		c := d.PreDeployCommand
		sp.PreDeployCommand = &c
	}
	sp.RestartPolicy = "always"
	if d.RepoURL != "" {
		u := d.RepoURL
		sp.RepoURL = &u
		sp.Branch = d.Branch
		if sp.Branch == "" {
			sp.Branch = "main"
		}
		if d.RootDir != "" {
			sp.RootDir = d.RootDir
			if !strings.HasPrefix(sp.RootDir, "/") {
				sp.RootDir = "/" + sp.RootDir
			}
		}
		sp.AutoDeploy = true
		sp.RestartPolicy = "on_failure"
	}
	d.Settings.Apply(sp)
	if d.Canvas != nil {
		c := *d.Canvas
		in.Canvas = &c
	}
	if in.Variables == nil {
		in.Variables = map[string]string{}
	}
	for k, v := range d.Variables {
		in.Variables[k] = v
	}
}

// ForAnotherEnvironment는 다른 환경에 쓸 정의다. DB 서비스는 생성된 비밀번호만 새로 만든다(템플릿의 다른 값은 같다).
func (d ServiceDef) ForAnotherEnvironment() ServiceDef {
	if d.DatabaseKind == "" || d.GeneratedPassword == "" {
		return d
	}
	pw := store.RandomSecret(32)
	// 비밀번호에서 만든 값(bucket의 access key, bucket 이름)도 새 비밀번호로 다시 만든다.
	var oldDerived, newDerived map[string]string
	if db, ok := Databases[d.DatabaseKind]; ok {
		oldDerived, newDerived = db.Variables(d.GeneratedPassword), db.Variables(pw)
	}
	nv := make(map[string]string, len(d.Variables))
	for k, v := range d.Variables {
		if v == d.GeneratedPassword {
			v = pw
		} else if ov, ok := oldDerived[k]; ok && ov == v && newDerived[k] != ov {
			v = newDerived[k]
		}
		nv[k] = v
	}
	d.Variables = nv
	d.GeneratedPassword = pw
	return d
}

// Template은 내장 템플릿이다(templates/*.json에서 읽는다).
type Template struct {
	Key         string
	Name        string
	Description string
	Icon        string
	Category    string
	Tags        []string
	Definition  *Definition
	// Raw는 내장 JSON 원문이다(내보내기용).
	Raw []byte
}

// Services는 입력 없이 서비스 정의를 만든다(호출할 때마다 새 비밀번호를 만든다).
func (t Template) Services() []ServiceDef {
	defs, _, err := t.Definition.Instantiate(nil)
	if err != nil {
		panic("catalog: built-in template " + t.Key + ": " + err.Error())
	}
	return defs
}

//go:embed templates/*.json
var builtinFS embed.FS

// Templates는 내장 템플릿이다(키 → 템플릿).
var Templates = loadBuiltins()

func loadBuiltins() map[string]Template {
	out := map[string]Template{}
	ents, err := fs.ReadDir(builtinFS, "templates")
	if err != nil {
		panic(err)
	}
	for _, e := range ents {
		b, err := builtinFS.ReadFile("templates/" + e.Name())
		if err != nil {
			panic(err)
		}
		d, err := ParseDefinition(b)
		if err != nil {
			panic("catalog: " + e.Name() + ": " + err.Error())
		}
		key := strings.TrimSuffix(e.Name(), ".json")
		if d.Key != key {
			panic("catalog: " + e.Name() + ": key must be " + key)
		}
		out[key] = Template{Key: key, Name: d.Name, Description: d.Description, Icon: d.Icon, Category: d.Category, Tags: d.Tags, Definition: d, Raw: b}
	}
	return out
}

// Keys는 템플릿 키를 정렬해 돌려준다.
func Keys() []string {
	var ks []string
	for k := range Templates {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
