package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// PostgresState는 PostgreSQL 인스턴스별 PgBouncer 연결 풀링 설정과 마지막 메이저 업그레이드(되돌리기 정보 포함)다.
type PostgresState struct {
	InstanceID string `json:"instance_id"`

	// PoolerServiceID는 aven이 만든 PgBouncer 서비스다. PoolingStatus: "" | deploying | enabled | failed.
	PoolerServiceID string `json:"pooler_service_id"`
	PoolingStatus   string `json:"pooling_status"`
	PoolingError    string `json:"pooling_error"`
	PoolMode        string `json:"pool_mode"`
	DefaultPoolSize int    `json:"default_pool_size"`
	MaxClientConn   int    `json:"max_client_conn"`
	// MaxPreparedStatements가 음수면 PgBouncer 기본값(200)을 쓴다. 0은 프로토콜 수준 prepared statement 지원을 끈다.
	MaxPreparedStatements int    `json:"max_prepared_statements"`
	PoolerDeploymentID    string `json:"pooler_deployment_id"`

	// UpgradeStatus: "" | preparing | upgrading | succeeded | failed | reverting | reverted | revert_failed.
	UpgradeStatus       string     `json:"upgrade_status"`
	UpgradeError        string     `json:"upgrade_error"`
	UpgradeFromMajor    int        `json:"upgrade_from_major"`
	UpgradeToMajor      int        `json:"upgrade_to_major"`
	UpgradeFromImage    string     `json:"upgrade_from_image"`
	UpgradeFromPGData   string     `json:"upgrade_from_pgdata"`
	UpgradeFromMount    string     `json:"upgrade_from_mount_path"`
	UpgradeBackupID     string     `json:"upgrade_backup_id"`      // 업그레이드 전 볼륨 백업
	UpgradePostBackupID string     `json:"upgrade_post_backup_id"` // 업그레이드 뒤 논리(DB) 백업
	UpgradeDeploymentID string     `json:"upgrade_deployment_id"`
	UpgradeStartedAt    *time.Time `json:"upgrade_started_at"`
	UpgradeFinishedAt   *time.Time `json:"upgrade_finished_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// DefaultPostgresState는 아직 저장된 상태가 없는 인스턴스의 기본값이다(PgBouncer 기본값과 같다).
func DefaultPostgresState(instanceID string) *PostgresState {
	return &PostgresState{InstanceID: instanceID, PoolMode: "transaction", DefaultPoolSize: 20, MaxClientConn: 1000, MaxPreparedStatements: -1}
}

const postgresStateCols = `instance_id, pooler_service_id, pooling_status, pooling_error, pool_mode, default_pool_size, max_client_conn,
	max_prepared_statements, pooler_deployment_id, upgrade_status, upgrade_error, upgrade_from_major, upgrade_to_major, upgrade_from_image,
	upgrade_from_pgdata, upgrade_from_mount_path, upgrade_backup_id, upgrade_post_backup_id, upgrade_deployment_id,
	upgrade_started_at, upgrade_finished_at, updated_at`

func scanPostgresState(r scanner) (*PostgresState, error) {
	var p PostgresState
	var started, finished sql.NullString
	var updated string
	err := r.Scan(&p.InstanceID, &p.PoolerServiceID, &p.PoolingStatus, &p.PoolingError, &p.PoolMode, &p.DefaultPoolSize, &p.MaxClientConn,
		&p.MaxPreparedStatements, &p.PoolerDeploymentID, &p.UpgradeStatus, &p.UpgradeError, &p.UpgradeFromMajor, &p.UpgradeToMajor, &p.UpgradeFromImage,
		&p.UpgradeFromPGData, &p.UpgradeFromMount, &p.UpgradeBackupID, &p.UpgradePostBackupID, &p.UpgradeDeploymentID,
		&started, &finished, &updated)
	if err != nil {
		return nil, notFound(err)
	}
	p.UpgradeStartedAt, p.UpgradeFinishedAt = parseNullTS(started), parseNullTS(finished)
	p.UpdatedAt = parseTS(updated)
	return &p, nil
}

// GetPostgresState는 첫 PgBouncer/업그레이드 작업 전에는 nil, nil을 돌려준다.
func (s *Store) GetPostgresState(ctx context.Context, instanceID string) (*PostgresState, error) {
	p, err := scanPostgresState(s.q.QueryRowContext(ctx, `SELECT `+postgresStateCols+` FROM postgres_states WHERE instance_id=?`, instanceID))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return p, err
}

// ListPostgresStatesByPooler는 같은 PgBouncer 서비스를 쓰는 인스턴스(환경별)의 상태다.
func (s *Store) ListPostgresStatesByPooler(ctx context.Context, poolerServiceID string) ([]*PostgresState, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+postgresStateCols+` FROM postgres_states WHERE pooler_service_id=? ORDER BY instance_id`, poolerServiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PostgresState
	for rows.Next() {
		p, err := scanPostgresState(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdatePostgresState는 상태를 저장한다(없으면 만든다).
func (s *Store) UpdatePostgresState(ctx context.Context, p *PostgresState) error {
	p.UpdatedAt = time.Now().UTC()
	_, err := s.q.ExecContext(ctx, `INSERT INTO postgres_states (`+postgresStateCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(instance_id) DO UPDATE SET
		pooler_service_id=excluded.pooler_service_id, pooling_status=excluded.pooling_status, pooling_error=excluded.pooling_error,
		pool_mode=excluded.pool_mode, default_pool_size=excluded.default_pool_size, max_client_conn=excluded.max_client_conn,
		max_prepared_statements=excluded.max_prepared_statements,
		pooler_deployment_id=excluded.pooler_deployment_id, upgrade_status=excluded.upgrade_status, upgrade_error=excluded.upgrade_error,
		upgrade_from_major=excluded.upgrade_from_major, upgrade_to_major=excluded.upgrade_to_major,
		upgrade_from_image=excluded.upgrade_from_image, upgrade_from_pgdata=excluded.upgrade_from_pgdata,
		upgrade_from_mount_path=excluded.upgrade_from_mount_path, upgrade_backup_id=excluded.upgrade_backup_id,
		upgrade_post_backup_id=excluded.upgrade_post_backup_id, upgrade_deployment_id=excluded.upgrade_deployment_id,
		upgrade_started_at=excluded.upgrade_started_at, upgrade_finished_at=excluded.upgrade_finished_at, updated_at=excluded.updated_at`,
		p.InstanceID, p.PoolerServiceID, p.PoolingStatus, p.PoolingError, p.PoolMode, p.DefaultPoolSize, p.MaxClientConn,
		p.MaxPreparedStatements, p.PoolerDeploymentID, p.UpgradeStatus, p.UpgradeError, p.UpgradeFromMajor, p.UpgradeToMajor, p.UpgradeFromImage,
		p.UpgradeFromPGData, p.UpgradeFromMount, p.UpgradeBackupID, p.UpgradePostBackupID, p.UpgradeDeploymentID,
		nullTS(p.UpgradeStartedAt), nullTS(p.UpgradeFinishedAt), ts(p.UpdatedAt))
	return err
}
