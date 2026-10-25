package ha

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ---- 엔진별 명령(구성원 컨테이너 안에서 일회성 exec로 실행) ----
//
// 비밀번호는 환경변수(PGPASSWORD, REDISCLI_AUTH, MONGO_URI)로만 넘기고 argv에 넣지 않는다.

// Command는 컨테이너에서 실행할 일회성 명령이다.
type Command struct {
	Cmd   []string
	Env   []string
	Stdin []byte
}

// Supported는 HA 전환을 지원하는 데이터베이스 종류인지 알려준다.
func Supported(kind string) bool { return kind == "postgres" || kind == "redis" || kind == "mongo" }

// HasEdge는 엔진이 edge(프록시)를 안정된 접속 지점으로 쓰는지 알려준다(mongo는 드라이버가 주 노드를 찾는다).
func HasEdge(kind string) bool { return kind == "postgres" || kind == "redis" }

// AutoFailoverByKeel은 Aven 제어부가 직접 주 노드를 승격하는 엔진인지 알려준다(나머지는 엔진이 스스로 뽑는다).
func AutoFailoverByKeel(kind string) bool { return kind == "postgres" }

const prelude = `T=""
if timeout -s KILL 5 true >/dev/null 2>&1; then T="timeout -s KILL 20"; fi
`

func sh(script string) []string { return []string{"sh", "-c", prelude + script} }

func first(vs map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := vs[k]; v != "" {
			return v
		}
	}
	return ""
}

func pgEnv(vs map[string]string) []string {
	user := first(vs, "PGUSER", "POSTGRES_USER")
	if user == "" {
		user = "postgres"
	}
	db := first(vs, "PGDATABASE", "POSTGRES_DB")
	if db == "" {
		db = user
	}
	env := []string{"PGHOST=127.0.0.1", "PGPORT=5432", "PGUSER=" + user, "PGDATABASE=" + db, "PGCONNECT_TIMEOUT=5",
		"PGAPPNAME=keel-ha", "PSQLRC=/dev/null"}
	if pw := first(vs, "PGPASSWORD", "POSTGRES_PASSWORD"); pw != "" {
		env = append(env, "PGPASSWORD="+pw)
	}
	return env
}

func pgSQL(vs map[string]string, query string) Command {
	return Command{Env: pgEnv(vs), Stdin: []byte(query), Cmd: sh(`exec $T psql -X -q -A -t -F '|' -v ON_ERROR_STOP=1 -f -`)}
}

func redisCmd(vs map[string]string, port int, args string) Command {
	env := []string{}
	if pw := first(vs, "REDISPASSWORD", "REDIS_PASSWORD"); pw != "" {
		env = append(env, "REDISCLI_AUTH="+pw)
	}
	return Command{Env: env, Stdin: []byte(args + "\n"), Cmd: sh(fmt.Sprintf(`exec $T redis-cli --raw -h 127.0.0.1 -p %d`, port))}
}

func mongoCmd(vs map[string]string, eval string) Command {
	u := url.URL{Scheme: "mongodb", Host: "127.0.0.1:27017", Path: "/", RawQuery: "directConnection=true&serverSelectionTimeoutMS=5000"}
	if user := first(vs, "MONGOUSER", "MONGO_INITDB_ROOT_USERNAME"); user != "" {
		u.User = url.UserPassword(user, first(vs, "MONGOPASSWORD", "MONGO_INITDB_ROOT_PASSWORD"))
		u.RawQuery += "&authSource=admin"
	}
	return Command{Env: []string{"MONGO_URI=" + u.String()}, Stdin: []byte(eval), Cmd: sh(
		`M=$(command -v mongosh) || { echo "mongosh not found in image" >&2; exit 127; }
exec $T "$M" "$MONGO_URI" --quiet --norc --eval "$(cat)"`)}
}

const pgProbeSQL = `SELECT pg_is_in_recovery(),
  CASE WHEN pg_is_in_recovery() THEN COALESCE(pg_last_wal_receive_lsn(), pg_last_wal_replay_lsn()) ELSE pg_current_wal_lsn() END,
  (SELECT count(*) FROM pg_stat_replication),
  COALESCE((SELECT status FROM pg_stat_wal_receiver LIMIT 1), '');`

// ProbeCommand는 구성원의 역할과 복제 위치를 읽는 명령이다. vars는 구성원 컨테이너의 최종 환경변수다.
func ProbeCommand(kind string, vs map[string]string) (Command, error) {
	switch kind {
	case "postgres":
		return pgSQL(vs, pgProbeSQL), nil
	case "redis":
		return redisCmd(vs, 6379, "INFO replication"), nil
	case "mongo":
		return mongoCmd(vs, `print(EJSON.stringify(db.hello()))`), nil
	}
	return Command{}, fmt.Errorf("HA is not supported for %q", kind)
}

// Probe는 구성원 하나의 상태 확인 결과다.
type Probe struct {
	Role      string `json:"role"`               // primary | replica | uninitialized
	Offset    uint64 `json:"offset"`             // WAL LSN(postgres) 또는 복제 offset(redis)
	Upstream  string `json:"upstream,omitempty"` // replica가 따라가는 주 노드 주소
	Streaming bool   `json:"streaming"`          // replica가 주 노드와 연결돼 있는지
	Followers int    `json:"followers"`          // 주 노드에 붙은 replica 수
	Self      string `json:"self,omitempty"`     // 엔진이 아는 자기 주소(mongo)
	Primary   string `json:"primary,omitempty"`  // 엔진이 아는 주 노드 주소(mongo)
	SetName   string `json:"set_name,omitempty"` // mongo replica set 이름
	Members   int    `json:"members,omitempty"`  // mongo replica set 구성원 수
}

// ParseLSN은 postgres LSN(예: 16/B374D848)을 숫자로 바꾼다.
func ParseLSN(s string) (uint64, error) {
	hi, lo, ok := strings.Cut(strings.TrimSpace(s), "/")
	if !ok {
		return 0, fmt.Errorf("bad LSN %q", s)
	}
	h, err := strconv.ParseUint(hi, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("bad LSN %q", s)
	}
	l, err := strconv.ParseUint(lo, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("bad LSN %q", s)
	}
	return h<<32 | l, nil
}

// ParseProbe는 ProbeCommand 출력을 해석한다.
func ParseProbe(kind, out string) (Probe, error) {
	out = strings.TrimSpace(out)
	switch kind {
	case "postgres":
		line := out
		if i := strings.LastIndexByte(out, '\n'); i >= 0 {
			line = out[i+1:]
		}
		f := strings.Split(strings.TrimSpace(line), "|")
		if len(f) != 4 {
			return Probe{}, fmt.Errorf("unexpected psql output %q", out)
		}
		p := Probe{Role: "primary"}
		if f[0] == "t" {
			p.Role = "replica"
		}
		if f[1] != "" {
			lsn, err := ParseLSN(f[1])
			if err != nil {
				return Probe{}, err
			}
			p.Offset = lsn
		}
		p.Followers, _ = strconv.Atoi(f[2])
		p.Streaming = f[3] == "streaming"
		return p, nil
	case "redis":
		kv := map[string]string{}
		for _, l := range strings.Split(out, "\n") {
			if k, v, ok := strings.Cut(strings.TrimSpace(l), ":"); ok {
				kv[k] = v
			}
		}
		var p Probe
		switch kv["role"] {
		case "master":
			p.Role = "primary"
			p.Offset, _ = strconv.ParseUint(kv["master_repl_offset"], 10, 64)
			p.Followers, _ = strconv.Atoi(kv["connected_slaves"])
		case "slave":
			p.Role = "replica"
			p.Offset, _ = strconv.ParseUint(kv["slave_repl_offset"], 10, 64)
			p.Upstream = kv["master_host"]
			p.Streaming = kv["master_link_status"] == "up"
		default:
			return Probe{}, fmt.Errorf("unexpected INFO replication output %q", trunc(out))
		}
		return p, nil
	case "mongo":
		line := out
		if i := strings.LastIndexByte(out, '\n'); i >= 0 {
			line = out[i+1:]
		}
		var h struct {
			IsWritablePrimary bool     `json:"isWritablePrimary"`
			Secondary         bool     `json:"secondary"`
			SetName           string   `json:"setName"`
			Me                string   `json:"me"`
			Primary           string   `json:"primary"`
			Hosts             []string `json:"hosts"`
		}
		if err := json.Unmarshal([]byte(line), &h); err != nil {
			return Probe{}, fmt.Errorf("unexpected hello output %q", trunc(out))
		}
		p := Probe{Self: h.Me, Primary: h.Primary, SetName: h.SetName, Members: len(h.Hosts)}
		switch {
		case h.SetName == "":
			p.Role = "uninitialized"
		case h.IsWritablePrimary:
			p.Role = "primary"
		case h.Secondary:
			p.Role, p.Streaming, p.Upstream = "replica", h.Primary != "", h.Primary
		default:
			p.Role = "recovering"
		}
		return p, nil
	}
	return Probe{}, fmt.Errorf("HA is not supported for %q", kind)
}

func trunc(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

// PromoteCommand는 postgres replica를 주 노드로 승격하는 명령이다(WAL을 다 재생할 때까지 최대 60초 기다린다).
func PromoteCommand(vs map[string]string) Command {
	return pgSQL(vs, `SELECT pg_promote(true, 60);`)
}

// PromoteOK는 PromoteCommand 출력이 성공인지 알려준다.
func PromoteOK(out string) bool { return strings.TrimSpace(out) == "t" }

// RedisPriorityCommand는 redis 노드의 replica-priority를 바꾼다(낮을수록 Sentinel이 먼저 승격한다).
func RedisPriorityCommand(vs map[string]string, priority int) Command {
	return redisCmd(vs, 6379, fmt.Sprintf("CONFIG SET replica-priority %d", priority))
}

// SentinelFailoverCommand는 그 노드의 Sentinel에 failover를 요청한다.
func SentinelFailoverCommand(vs map[string]string) Command {
	return redisCmd(vs, 26379, "SENTINEL FAILOVER keel")
}

// RedisOK는 redis-cli --raw 출력이 OK인지 알려준다.
func RedisOK(out string) bool { return strings.TrimSpace(out) == "OK" }

// MongoAddr는 mongo replica set 구성원 주소다(사설 도메인:27017).
func MongoAddr(privateDomain string) string { return privateDomain + ":27017" }

func jsStr(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// MongoReconcileCommand는 replica set을 hosts(첫 항목이 root)에 맞추는 명령이다. 한 번에 한 가지만 바꾼다
// (초기화, 구성원 하나 추가 또는 제거). 출력은 {"action": "initiated|added|removed|ok|wait", ...} 한 줄이다.
func MongoReconcileCommand(vs map[string]string, set string, hosts []string) Command {
	script := `const RS = ` + jsStr(set) + `, HOSTS = ` + jsStr(hosts) + `;
const out = (o) => print(EJSON.stringify(o));
const h = db.hello();
if (!h.setName) {
  rs.initiate({_id: RS, members: [{_id: 0, host: HOSTS[0], priority: 2}]});
  out({action: "initiated", host: HOSTS[0]});
} else if (!h.isWritablePrimary) {
  out({action: "wait", primary: h.primary || ""});
} else {
  const cfg = rs.conf();
  const have = cfg.members.map((m) => m.host);
  const add = HOSTS.filter((x) => !have.includes(x));
  const del = have.filter((x) => !HOSTS.includes(x) && x !== h.me);
  if (add.length) { rs.add({host: add[0], priority: 1}); out({action: "added", host: add[0]}); }
  else if (del.length) { rs.remove(del[0]); out({action: "removed", host: del[0]}); }
  else out({action: "ok", members: have.length});
}`
	return mongoCmd(vs, script)
}

// MongoAction은 MongoReconcileCommand 출력의 action이다.
func MongoAction(out string) (string, error) {
	out = strings.TrimSpace(out)
	if i := strings.LastIndexByte(out, '\n'); i >= 0 {
		out = out[i+1:]
	}
	var r struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.Action == "" {
		return "", fmt.Errorf("unexpected mongosh output %q", trunc(out))
	}
	return r.Action, nil
}

// MongoPreferCommand는 주 노드에서 실행해 target의 우선순위를 가장 높게 바꾼다. target이 따라잡으면
// MongoDB가 스스로 선거를 열어 target을 주 노드로 만든다(priority takeover).
func MongoPreferCommand(vs map[string]string, target string) Command {
	return mongoCmd(vs, `const T = `+jsStr(target)+`;
const cfg = rs.conf();
if (!cfg.members.some((m) => m.host === T)) throw new Error("member " + T + " is not in the replica set");
cfg.members.forEach((m) => { m.priority = m.host === T ? 2 : 1; });
rs.reconfig(cfg);
print(EJSON.stringify({action: "ok"}));`)
}

// MongoStandaloneCommand는 root(주 노드)만 남기도록 replica set을 강제로 다시 설정한다(되돌리기).
func MongoStandaloneCommand(vs map[string]string, root string) Command {
	return mongoCmd(vs, `const R = `+jsStr(root)+`;
const cfg = rs.conf();
cfg.members = cfg.members.filter((m) => m.host === R);
if (cfg.members.length !== 1) throw new Error("root " + R + " is not in the replica set");
cfg.members[0].priority = 1;
rs.reconfig(cfg, {force: true});
print(EJSON.stringify({action: "ok"}));`)
}

// ---- failover 판단 ----

// MemberState는 controller가 본 구성원 하나의 상태다.
type MemberState struct {
	ServiceID string
	Name      string
	OK        bool  // 마지막 상태 확인이 성공했는지
	Probe     Probe // OK일 때만 의미가 있다
	Fails     int   // 연속 실패 횟수
}

// PickPromotion은 승격할 replica를 고른다: 상태 확인이 성공한 replica 중 WAL 위치(offset)가 가장 앞선 것.
// 같으면 목록 순서(ordinal)를 따른다. 후보가 없으면 false.
func PickPromotion(states []MemberState, primaryID string) (string, bool) {
	best := -1
	for i, s := range states {
		if s.ServiceID == primaryID || !s.OK || s.Probe.Role != "replica" {
			continue
		}
		if best < 0 || s.Probe.Offset > states[best].Probe.Offset {
			best = i
		}
	}
	if best < 0 {
		return "", false
	}
	return states[best].ServiceID, true
}

// LagBytes는 주 노드 위치에서 replica 위치를 뺀 값이다(replica가 앞서거나 알 수 없으면 0).
func LagBytes(primary, replica uint64) uint64 {
	if replica >= primary {
		return 0
	}
	return primary - replica
}

// ObservedPrimary는 스스로 주 노드를 뽑는 엔진(redis, mongo)에서 지금 주 노드인 구성원을 고른다. 주 노드라고
// 답한 구성원이 여럿이면(잠깐의 split brain) 붙은 replica가 가장 많은 쪽, 같으면 recorded를 고른다.
func ObservedPrimary(states []MemberState, recorded string) (string, bool) {
	best := -1
	for i, s := range states {
		if !s.OK || s.Probe.Role != "primary" {
			continue
		}
		if best < 0 || s.Probe.Followers > states[best].Probe.Followers ||
			(s.Probe.Followers == states[best].Probe.Followers && s.ServiceID == recorded) {
			best = i
		}
	}
	if best < 0 {
		return "", false
	}
	return states[best].ServiceID, true
}
