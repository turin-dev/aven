package ha

import (
	"strconv"
	"strings"

	"github.com/turin-dev/aven/internal/catalog"
	"github.com/turin-dev/aven/internal/vars"
)

// Topology는 클러스터 구성원의 현재 모양이다(모두 서비스 이름).
type Topology struct {
	RootName string
	Primary  string   // 현재 주 노드
	Data     []string // 데이터 노드(root가 처음)
	Internal []string
	Edge     string // edge 서비스(없으면 "")
}

// HostRef는 서비스의 사설 도메인 참조다. 서비스 이름을 바꾸면 Aven이 참조도 같이 바꾼다.
func HostRef(name string) string { return "${{" + name + ".AVEN_PRIVATE_DOMAIN}}" }

// Format은 clusterWiring 형식을 채운다: {host} {name} {rootName} {port}.
func (t Topology) Format(format, name string, port int) string {
	if format == "" {
		format = "{host}"
	}
	return strings.NewReplacer("{host}", HostRef(name), "{name}", name, "{rootName}", t.RootName, "{port}", strconv.Itoa(port)).Replace(format)
}

func (t Topology) list(format string, names []string, port int) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = t.Format(format, n, port)
	}
	return strings.Join(out, ",")
}

// 강등된 주 노드가 다시 합류하는 방법(rejoinVariable)
const (
	RejoinStandby = "standby" // 깨끗하게 멈춘 주 노드: 데이터를 그대로 두고 대기 노드로 시작한다
	RejoinReclone = "reclone" // 장애로 밀려난 주 노드: 데이터를 옆으로 옮겨 두고 새 주 노드에서 다시 복제한다
)

// DataVars는 데이터 노드 name에 넣을 배선 변수다. rejoin이 비면 RejoinReclone이다.
func DataVars(w *catalog.ClusterWiring, t Topology, name, rejoin string) map[string]string {
	out := map[string]string{}
	if w == nil {
		return out
	}
	set := func(k, v string) {
		if k != "" {
			out[k] = v
		}
	}
	role := "replica"
	if name == t.Primary {
		role = "primary"
	}
	if rejoin == "" {
		rejoin = RejoinReclone
	}
	set(w.RoleVariable, role)
	set(w.PrimaryHostVariable, t.Format(w.PrimaryHostFormat, t.Primary, 0))
	set(w.PeerHostsVariable, t.list(w.PeerHostFormat, t.Data, 0))
	set(w.QuorumVariable, strconv.Itoa(len(t.Data)/2+1))
	set(w.ReplicaNodeNameVariable, name)
	set(w.RejoinVariable, rejoin)
	if w.CoordinatorHostsVariable != "" && len(t.Internal) > 0 {
		f := w.CoordinatorHostFormat
		if f == "" {
			f = "{host}:{port}"
		}
		out[w.CoordinatorHostsVariable] = t.list(f, t.Internal, w.CoordinatorPort)
	}
	return out
}

// InternalVars는 internal 구성원(조정 노드) name에 넣을 배선 변수다.
func InternalVars(w *catalog.ClusterWiring, t Topology, name string) map[string]string {
	out := map[string]string{}
	if w == nil {
		return out
	}
	if w.InternalNodeNameVariable != "" {
		out[w.InternalNodeNameVariable] = name
	}
	if w.InternalNodesVariable != "" {
		out[w.InternalNodesVariable] = t.list(w.InternalNodeFormat, t.Internal, w.CoordinatorPort)
	}
	return out
}

// EdgeVars는 edge 서비스에 넣을 배선 변수다. EdgePrimaryOnly면 현재 주 노드만 넣는다.
func EdgeVars(w *catalog.ClusterWiring, t Topology) map[string]string {
	out := map[string]string{}
	if w == nil || w.DataNodesVariable == "" {
		return out
	}
	nodes := t.Data
	if w.EdgePrimaryOnly {
		nodes = []string{t.Primary}
	}
	out[w.DataNodesVariable] = t.list(w.DataNodeFormat, nodes, 0)
	return out
}

// WiringKeys는 배선이 관리하는 변수 이름 전체다(되돌릴 때 root에서 지운다).
func WiringKeys(w *catalog.ClusterWiring) []string {
	if w == nil {
		return nil
	}
	var out []string
	for _, k := range []string{w.RoleVariable, w.PrimaryHostVariable, w.PeerHostsVariable, w.QuorumVariable, w.ReplicaNodeNameVariable,
		w.RejoinVariable, w.CoordinatorHostsVariable, w.InternalNodeNameVariable, w.InternalNodesVariable, w.DataNodesVariable} {
		if k != "" {
			out = append(out, k)
		}
	}
	return out
}

// MigrateRefs는 값 안의 ${{ from.KEY }} 참조 중 KEY가 keys에 있는 것만 ${{ to.KEY }}로 바꾼다.
// 전환할 때 다른 서비스가 root의 연결 변수(DATABASE_URL 등) 대신 edge의 같은 변수를 쓰게 한다.
func MigrateRefs(value, from, to string, keys map[string]bool) string {
	if !strings.Contains(value, "${{") {
		return value
	}
	out := value
	for _, ref := range vars.References(value) {
		scope, name, ok := strings.Cut(ref, ".")
		if !ok || scope != from || !keys[name] {
			continue
		}
		out = replaceRef(out, from, to, name)
	}
	return out
}

// replaceRef는 ${{ from.name }}(공백 허용)를 ${{to.name}}으로 바꾼다.
func replaceRef(value, from, to, name string) string {
	var b strings.Builder
	rest := value
	for {
		i := strings.Index(rest, "${{")
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		j := strings.Index(rest[i:], "}}")
		if j < 0 {
			b.WriteString(rest)
			return b.String()
		}
		inner := strings.TrimSpace(rest[i+3 : i+j])
		b.WriteString(rest[:i])
		if inner == from+"."+name {
			b.WriteString("${{" + to + "." + name + "}}")
		} else {
			b.WriteString(rest[i : i+j+2])
		}
		rest = rest[i+j+2:]
	}
}
