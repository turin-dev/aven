package store

import (
	"context"
	"encoding/json"
	"time"
)

// ---------------- Database high availability ----------------
//
// HA 클러스터는 한 환경에서 데이터베이스 서비스 하나(root)를 동반 템플릿대로 여러 서비스로 늘린 것이다.
// root 서비스는 그대로 두고(데이터, 볼륨, 서비스 ID 유지) replica/internal/edge 서비스를 이 환경에만 만든다.
// 다른 환경의 같은 서비스는 단독으로 남는다.

// HA 클러스터 상태
const (
	HAProvisioning = "provisioning" // 전환 직후: 구성원 배포와 초기화 중
	HAActive       = "active"
	HASwitching    = "switching" // 수동 전환(switchover)이나 자동 failover 진행 중
)

// HACluster는 HA로 전환한 데이터베이스 하나다.
type HACluster struct {
	ID                 string    `json:"id"`
	ProjectID          string    `json:"project_id"`
	EnvironmentID      string    `json:"environment_id"`
	RootServiceID      string    `json:"root_service_id"`
	Kind               string    `json:"kind"` // postgres | redis | mongo
	CompanionTemplate  string    `json:"companion_template"`
	StandaloneTemplate string    `json:"standalone_template"`
	PrimaryServiceID   string    `json:"primary_service_id"` // Aven이 마지막으로 확인(또는 승격)한 주 노드
	Status             string    `json:"status"`
	AutoFailover       bool      `json:"auto_failover"`
	Message            string    `json:"message"` // 마지막 상태 변화 설명
	Saved              HASaved   `json:"-"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// HASaved는 되돌리기(revert)에 필요한 전환 전 root 설정이다.
type HASaved struct {
	StartCommand *string `json:"start_command"`
	PrivatePorts []int   `json:"private_ports"`
	// Vars는 전환이 바꾼 root 변수의 이전 값이다(nil이면 원래 없던 변수).
	Vars map[string]*string `json:"vars"`
	// TCPProxyMoved면 root의 TCP proxy를 edge로 옮겼다(되돌릴 때 root로 돌려준다).
	TCPProxyMoved bool `json:"tcp_proxy_moved"`
	// Counts는 전환할 때 고른 역할별 구성원 수다.
	Counts map[string]int `json:"counts"`
}

// HAMember는 클러스터 구성원이다(root 포함).
type HAMember struct {
	ServiceID string `json:"service_id"`
	Role      string `json:"role"` // root | replica | internal | edge
	Ordinal   int    `json:"ordinal"`
}

func migrateDatabaseHA(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`CREATE TABLE ha_clusters (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
			root_service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
			kind TEXT NOT NULL,
			companion_template TEXT NOT NULL,
			standalone_template TEXT NOT NULL,
			primary_service_id TEXT NOT NULL,
			status TEXT NOT NULL,
			auto_failover INTEGER NOT NULL DEFAULT 1,
			message TEXT NOT NULL DEFAULT '',
			saved TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE(root_service_id, environment_id)
		)`,
		`CREATE INDEX ha_clusters_environment ON ha_clusters(environment_id)`,
		`CREATE TABLE ha_cluster_members (
			cluster_id TEXT NOT NULL REFERENCES ha_clusters(id) ON DELETE CASCADE,
			service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
			role TEXT NOT NULL,
			ordinal INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY(cluster_id, service_id)
		)`,
		`CREATE INDEX ha_cluster_members_service ON ha_cluster_members(service_id)`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

const haCols = `id, project_id, environment_id, root_service_id, kind, companion_template, standalone_template,
	primary_service_id, status, auto_failover, message, saved, created_at, updated_at`

func scanHACluster(r scanner) (*HACluster, error) {
	var c HACluster
	var saved, created, updated string
	if err := r.Scan(&c.ID, &c.ProjectID, &c.EnvironmentID, &c.RootServiceID, &c.Kind, &c.CompanionTemplate, &c.StandaloneTemplate,
		&c.PrimaryServiceID, &c.Status, &c.AutoFailover, &c.Message, &saved, &created, &updated); err != nil {
		return nil, notFound(err)
	}
	json.Unmarshal([]byte(saved), &c.Saved)
	c.CreatedAt, c.UpdatedAt = parseTS(created), parseTS(updated)
	return &c, nil
}

// CreateHACluster는 클러스터와 구성원을 한 번에 저장한다.
func (s *Store) CreateHACluster(ctx context.Context, c *HACluster, members []HAMember) error {
	c.ID = NewID("hac")
	c.CreatedAt = time.Now().UTC()
	c.UpdatedAt = c.CreatedAt
	return s.Tx(ctx, func(tx *Store) error {
		if _, err := tx.q.ExecContext(ctx, `INSERT INTO ha_clusters (`+haCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			c.ID, c.ProjectID, c.EnvironmentID, c.RootServiceID, c.Kind, c.CompanionTemplate, c.StandaloneTemplate,
			c.PrimaryServiceID, c.Status, c.AutoFailover, c.Message, jsonStr(c.Saved), ts(c.CreatedAt), ts(c.UpdatedAt)); err != nil {
			return conflict(err)
		}
		for _, m := range members {
			if err := tx.AddHAMember(ctx, c.ID, m); err != nil {
				return err
			}
		}
		return nil
	})
}

// AddHAMember는 구성원을 더한다.
func (s *Store) AddHAMember(ctx context.Context, clusterID string, m HAMember) error {
	_, err := s.q.ExecContext(ctx, `INSERT INTO ha_cluster_members (cluster_id, service_id, role, ordinal) VALUES (?,?,?,?)`,
		clusterID, m.ServiceID, m.Role, m.Ordinal)
	return conflict(err)
}

// RemoveHAMember는 구성원을 뺀다(서비스는 지우지 않는다).
func (s *Store) RemoveHAMember(ctx context.Context, clusterID, serviceID string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM ha_cluster_members WHERE cluster_id=? AND service_id=?`, clusterID, serviceID))
}

func (s *Store) GetHACluster(ctx context.Context, id string) (*HACluster, error) {
	return scanHACluster(s.q.QueryRowContext(ctx, `SELECT `+haCols+` FROM ha_clusters WHERE id=?`, id))
}

// GetHAClusterForService는 서비스가 (root 또는 구성원으로) 속한 환경의 클러스터다. 없으면 ErrNotFound.
func (s *Store) GetHAClusterForService(ctx context.Context, serviceID, envID string) (*HACluster, error) {
	return scanHACluster(s.q.QueryRowContext(ctx, `SELECT `+haCols+` FROM ha_clusters WHERE environment_id=? AND id IN
		(SELECT cluster_id FROM ha_cluster_members WHERE service_id=?)`, envID, serviceID))
}

// ListHAClusters는 환경의 클러스터다(envID가 비면 전체).
func (s *Store) ListHAClusters(ctx context.Context, envID string) ([]*HACluster, error) {
	q, args := `SELECT `+haCols+` FROM ha_clusters`, []any{}
	if envID != "" {
		q += ` WHERE environment_id=?`
		args = append(args, envID)
	}
	rows, err := s.q.QueryContext(ctx, q+` ORDER BY created_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*HACluster{}
	for rows.Next() {
		c, err := scanHACluster(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateHACluster는 주 노드, 상태, 자동 failover, 메시지, 저장한 설정을 바꾼다.
func (s *Store) UpdateHACluster(ctx context.Context, c *HACluster) error {
	c.UpdatedAt = time.Now().UTC()
	return affected(s.q.ExecContext(ctx, `UPDATE ha_clusters SET primary_service_id=?, status=?, auto_failover=?, message=?, saved=?, updated_at=? WHERE id=?`,
		c.PrimaryServiceID, c.Status, c.AutoFailover, c.Message, jsonStr(c.Saved), ts(c.UpdatedAt), c.ID))
}

// ListHAMembers는 구성원을 역할(root, replica, internal, edge)과 번호 순서로 돌려준다.
func (s *Store) ListHAMembers(ctx context.Context, clusterID string) ([]HAMember, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT service_id, role, ordinal FROM ha_cluster_members WHERE cluster_id=?
		ORDER BY CASE role WHEN 'root' THEN 0 WHEN 'replica' THEN 1 WHEN 'internal' THEN 2 ELSE 3 END, ordinal`, clusterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HAMember{}
	for rows.Next() {
		var m HAMember
		if err := rows.Scan(&m.ServiceID, &m.Role, &m.Ordinal); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteHACluster는 클러스터 기록만 지운다(서비스는 호출한 쪽이 정리한다).
func (s *Store) DeleteHACluster(ctx context.Context, id string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM ha_clusters WHERE id=?`, id))
}
