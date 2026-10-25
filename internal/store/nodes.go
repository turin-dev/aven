package store

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"time"
)

const LocalNodeID = "node_local"

// IsLocalNode는 node ID가 로컬 노드(빈 값 포함)인지다.
func IsLocalNode(id string) bool { return id == "" || id == LocalNodeID }

// NormNode는 빈 node ID를 로컬 노드 ID로 바꾼다.
func NormNode(id string) string {
	if IsLocalNode(id) {
		return LocalNodeID
	}
	return id
}

type Node struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	IsLocal       bool              `json:"is_local"`
	Status        string            `json:"status"`
	Schedulable   bool              `json:"schedulable"`
	Address       NodeAddress       `json:"address"`
	Version       string            `json:"version"`
	DockerVersion string            `json:"docker_version"`
	CPUCores      int               `json:"cpu_cores"`
	MemoryBytes   uint64            `json:"memory_bytes"`
	DiskFreeBytes uint64            `json:"disk_free_bytes"`
	Labels        map[string]string `json:"labels"`
	Draining      bool              `json:"-"` // drain이 시작된 노드(status는 연결된 동안 "draining")
	MeshPort      int               `json:"-"` // agent가 보고한 mesh gateway 포트(API는 mesh 상태로 보여준다)
	// ExternalUpstreams는 그 노드의 keel-proxy가 보낼 수 있는 aven 밖의 upstream(host:port)이다. 노드 설정(KEEL_EXTERNAL_UPSTREAMS)이 정하고
	// 서버는 넓힐 수 없다. 로컬 노드는 aven-server 설정에서 채운다.
	ExternalUpstreams []string `json:"external_upstreams"`
	// Ingress는 이 노드로 공개 트래픽이 들어오는 방식이다(도메인 DNS 안내, 터널 노드의 TLS 기본값, TCP 프록시 호스트).
	// 로컬 노드는 KEEL_INGRESS_MODE / KEEL_INGRESS_HOSTNAME / KEEL_TCP_PROXY_DOMAIN이 있으면 그 값이 우선한다.
	NodeIngress
	// IPv6Pool은 도메인별 공개 IPv6를 배정할 이 노드의 풀(CIDR, /64~/120)이다. 비면 도메인은 노드의 기본 IPv6를 같이 쓴다.
	// 로컬 노드는 KEEL_IPV6_POOL이 있으면 그 값이 우선한다.
	IPv6Pool   string    `json:"ipv6_pool"`
	LastSeenAt time.Time `json:"last_seen_at"`
	CreatedAt  time.Time `json:"created_at"`
}

type NodeAddress struct {
	IPv4    *string `json:"ipv4"`
	IPv6    *string `json:"ipv6"`
	Private *string `json:"private"`
}

func (s *Store) LocalNode(ctx context.Context) (*Node, error) {
	var n Node
	var labels, created string
	err := s.q.QueryRowContext(ctx, `SELECT id, name, schedulable, labels, created_at, `+ingressCols+` FROM nodes WHERE id=?`, LocalNodeID).
		Scan(&n.ID, &n.Name, &n.Schedulable, &labels, &created, &n.IngressMode, &n.IngressHostname, &n.TCPHostname, &n.IPv6Pool)
	if err != nil {
		return nil, notFound(err)
	}
	n.IsLocal, n.Status = true, "online"
	n.CreatedAt, n.LastSeenAt = parseTS(created), time.Now().UTC()
	json.Unmarshal([]byte(labels), &n.Labels)
	if n.Labels == nil {
		n.Labels = map[string]string{}
	}
	return &n, nil
}

func (s *Store) UpdateLocalNode(ctx context.Context, n *Node) error {
	return affected(s.q.ExecContext(ctx, `UPDATE nodes SET name=?, schedulable=?, labels=? WHERE id=?`, n.Name, n.Schedulable, jsonStr(n.Labels), LocalNodeID))
}

func (s *Store) CreateJoinToken(ctx context.Context, hash, name string, expiry time.Time) error {
	_, err := s.q.ExecContext(ctx, `INSERT INTO node_join_tokens (token_hash, name, expires_at) VALUES (?,?,?)`, hash, name, ts(expiry))
	return err
}

// ConsumeJoinToken은 단일 트랜잭션으로 미사용 토큰을 소비하고 노드와 장기 인증키 해시를 저장한다.
func (s *Store) ConsumeJoinToken(ctx context.Context, hash, credentialHash string) (*Node, error) {
	var result *Node
	err := s.Tx(ctx, func(tx *Store) error {
		var name, expires string
		if err := tx.q.QueryRowContext(ctx, `SELECT name, expires_at FROM node_join_tokens WHERE token_hash=?`, hash).Scan(&name, &expires); err != nil {
			return ErrNotFound
		}
		if !parseTS(expires).After(time.Now().UTC()) {
			return ErrNotFound
		}
		if _, err := tx.q.ExecContext(ctx, `DELETE FROM node_join_tokens WHERE token_hash=?`, hash); err != nil {
			return err
		}
		result = &Node{ID: NewID("nod"), Name: name, Status: "offline", Schedulable: true, Labels: map[string]string{}, CreatedAt: time.Now().UTC()}
		_, err := tx.q.ExecContext(ctx, `INSERT INTO nodes (id, name, schedulable, labels, created_at, credential_hash) VALUES (?,?,?,?,?,?)`, result.ID, name, true, "{}", ts(result.CreatedAt), credentialHash)
		return err
	})
	return result, err
}

func (s *Store) GetNode(ctx context.Context, id string) (*Node, error) {
	var n Node
	var address, labels, created, info string
	var seen *string
	err := s.q.QueryRowContext(ctx, `SELECT id, name, schedulable, labels, created_at, status, address, last_seen_at, info, draining, `+ingressCols+` FROM nodes WHERE id=?`, id).
		Scan(&n.ID, &n.Name, &n.Schedulable, &labels, &created, &n.Status, &address, &seen, &info, &n.Draining, &n.IngressMode, &n.IngressHostname, &n.TCPHostname, &n.IPv6Pool)
	if err != nil {
		return nil, notFound(err)
	}
	var ni NodeInfo
	if json.Unmarshal([]byte(info), &ni) == nil {
		n.Version, n.DockerVersion, n.CPUCores, n.MemoryBytes, n.DiskFreeBytes = ni.Version, ni.DockerVersion, ni.CPUCores, ni.MemoryBytes, ni.DiskFreeBytes
		n.MeshPort = ni.MeshPort
		n.ExternalUpstreams = ni.ExternalUpstreams
	}
	n.IsLocal = id == LocalNodeID
	n.CreatedAt = parseTS(created)
	if seen != nil {
		n.LastSeenAt = parseTS(*seen)
	}
	json.Unmarshal([]byte(labels), &n.Labels)
	json.Unmarshal([]byte(address), &n.Address)
	if n.Labels == nil {
		n.Labels = map[string]string{}
	}
	return &n, nil
}

func (s *Store) ListNodes(ctx context.Context) ([]*Node, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT id FROM nodes ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]*Node, 0, len(ids))
	for _, id := range ids {
		n, err := s.GetNode(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

func (s *Store) VerifyNodeCredential(ctx context.Context, id, credentialHash string) error {
	var stored string
	if err := s.q.QueryRowContext(ctx, `SELECT credential_hash FROM nodes WHERE id=? AND id<>?`, id, LocalNodeID).Scan(&stored); err != nil {
		return ErrNotFound
	}
	if len(stored) != 64 || subtle.ConstantTimeCompare([]byte(stored), []byte(credentialHash)) != 1 {
		return ErrNotFound
	}
	return nil
}

// SetNodeStatus는 원격 노드의 연결 상태와 마지막 연결 시각을 기록한다.
func (s *Store) SetNodeStatus(ctx context.Context, id, status string) error {
	return affected(s.q.ExecContext(ctx, `UPDATE nodes SET status=?, last_seen_at=? WHERE id=? AND id<>?`, status, ts(time.Now()), id, LocalNodeID))
}

// NodeInfo는 agent가 연결 때 보고하는 노드 정보다.
type NodeInfo struct {
	Version       string `json:"version"`
	DockerVersion string `json:"docker_version"`
	CPUCores      int    `json:"cpu_cores"`
	MemoryBytes   uint64 `json:"memory_bytes"`
	DiskFreeBytes uint64 `json:"disk_free_bytes"`
	// MeshPort는 agent의 mesh gateway 포트다(0이면 mesh를 쓸 수 없는 노드).
	MeshPort int `json:"mesh_port,omitempty"`
	// ExternalUpstreams는 agent가 허용하는 aven 밖의 upstream(host:port)이다.
	ExternalUpstreams []string `json:"external_upstreams,omitempty"`
}

// SetNodeInfo는 agent가 보고한 주소와 정보를 저장한다.
func (s *Store) SetNodeInfo(ctx context.Context, id string, address NodeAddress, info NodeInfo) error {
	return affected(s.q.ExecContext(ctx, `UPDATE nodes SET address=?, info=?, last_seen_at=? WHERE id=? AND id<>?`, jsonStr(address), jsonStr(info), ts(time.Now()), id, LocalNodeID))
}

func (s *Store) UpdateNode(ctx context.Context, n *Node) error {
	if n.ID == LocalNodeID {
		return s.UpdateLocalNode(ctx, n)
	}
	return affected(s.q.ExecContext(ctx, `UPDATE nodes SET name=?, schedulable=?, labels=? WHERE id=?`, n.Name, n.Schedulable, jsonStr(n.Labels), n.ID))
}

func (s *Store) DeleteNode(ctx context.Context, id string) error {
	if id == LocalNodeID {
		return errors.New("local node cannot be removed")
	}
	return affected(s.q.ExecContext(ctx, `DELETE FROM nodes WHERE id=?`, id))
}

func (s *Store) CountNodeInstances(ctx context.Context, id string) (int, error) {
	var n int
	err := s.q.QueryRowContext(ctx, `SELECT count(*) FROM instances WHERE node_id=?`, id).Scan(&n)
	return n, err
}

// SetNodeDraining은 노드의 drain 표시를 켜고 끈다. 켜면 새 배포를 받지 않는다(schedulable=false).
func (s *Store) SetNodeDraining(ctx context.Context, id string, draining bool) error {
	if id == LocalNodeID {
		return errors.New("local node cannot be drained")
	}
	if draining {
		return affected(s.q.ExecContext(ctx, `UPDATE nodes SET draining=1, schedulable=0 WHERE id=?`, id))
	}
	return affected(s.q.ExecContext(ctx, `UPDATE nodes SET draining=0 WHERE id=?`, id))
}

// NodeInstances는 node_id가 이 노드로 지정된 인스턴스 ID들이다.
func (s *Store) NodeInstances(ctx context.Context, id string) ([]string, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT id FROM instances WHERE node_id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var i string
		if err := rows.Scan(&i); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// SetInstanceNode는 인스턴스의 배치 노드를 바꾼다.
func (s *Store) SetInstanceNode(ctx context.Context, instanceID, nodeID string) error {
	return affected(s.q.ExecContext(ctx, `UPDATE instances SET node_id=?, updated_at=? WHERE id=?`, nodeID, ts(time.Now()), instanceID))
}

// 노드 ingress 방식.
const (
	IngressDirect           = "direct"            // 공인 주소로 바로 받는다(기본)
	IngressCloudflareTunnel = "cloudflare_tunnel" // 공인 주소 없이 Cloudflare 터널(cloudflared)로만 받는다
)

// NodeIngress는 노드의 공개 진입 설정이다.
type NodeIngress struct {
	IngressMode     string `json:"ingress_mode"`     // direct | cloudflare_tunnel
	IngressHostname string `json:"ingress_hostname"` // 커스텀 도메인이 CNAME으로 가리킬 이름(비우면 A/AAAA 안내)
	TCPHostname     string `json:"tcp_hostname"`     // TCP 프록시 접속 호스트(비우면 기존 방식)
}

// Tunnel은 Cloudflare 터널 노드인지다.
func (n NodeIngress) Tunnel() bool { return n.IngressMode == IngressCloudflareTunnel }

const ingressCols = `COALESCE(ingress_mode,'direct'), COALESCE(ingress_hostname,''), COALESCE(tcp_hostname,''), COALESCE(ipv6_pool,'')`

// migrateNodeIngress는 노드에 ingress 방식, ingress 호스트 이름, TCP 프록시 호스트 이름을 더한다.
func migrateNodeIngress(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`ALTER TABLE nodes ADD COLUMN ingress_mode TEXT DEFAULT 'direct'`,
		`ALTER TABLE nodes ADD COLUMN ingress_hostname TEXT`,
		`ALTER TABLE nodes ADD COLUMN tcp_hostname TEXT`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// SetNodeIngress는 노드(로컬 포함)의 ingress 설정을 저장한다. 빈 호스트 이름은 NULL로 둔다.
func (s *Store) SetNodeIngress(ctx context.Context, id string, in NodeIngress) error {
	mode := in.IngressMode
	if mode == "" {
		mode = IngressDirect
	}
	null := func(v string) any {
		if v == "" {
			return nil
		}
		return v
	}
	return affected(s.q.ExecContext(ctx, `UPDATE nodes SET ingress_mode=?, ingress_hostname=?, tcp_hostname=? WHERE id=?`,
		mode, null(in.IngressHostname), null(in.TCPHostname), NormNode(id)))
}
