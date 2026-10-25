package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// DBPITRConfig는 DB 인스턴스 하나의 시점 복구(연속 WAL/binlog 보관) 설정과 보관 상태다.
type DBPITRConfig struct {
	Enabled       bool   `json:"enabled"`
	RetentionDays int    `json:"retention_days"`
	DestinationID string `json:"destination_id"`
	// Epoch는 같은 서비스로 복원할 때마다 1씩 늘어난다(복원 전 WAL/binlog와 섞지 않는다).
	Epoch     int        `json:"epoch"`
	EnabledAt *time.Time `json:"enabled_at,omitempty"`
	// LastBaseAt은 마지막으로 성공한 기준 백업 시각이다(nil이면 다음 주기에 만든다).
	LastBaseAt *time.Time `json:"last_base_at,omitempty"`
	// LastBaseTry는 마지막 기준 백업 시도 시각이다(실패하면 10분 뒤에 다시 한다).
	LastBaseTry *time.Time `json:"last_base_try,omitempty"`
	LastSyncAt  *time.Time `json:"last_sync_at,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	// PGLastLSN은 마지막 WAL 전환 뒤의 위치다(바뀌지 않았으면 전환하지 않는다).
	PGLastLSN string `json:"pg_last_lsn,omitempty"`
	// MySQLActive는 마지막으로 본 활성 binlog "이름 크기"다(바뀌었으면 FLUSH BINARY LOGS로 닫는다).
	MySQLActive string `json:"mysql_active,omitempty"`
}

// DBPITRObject는 저장소에 보관한 기준 백업 하나 또는 WAL/binlog 파일 하나다.
type DBPITRObject struct {
	ID            string `json:"id"`
	InstanceID    string `json:"instance_id"`
	Kind          string `json:"kind"`        // postgres | mysql
	ObjectType    string `json:"object_type"` // base | wal | binlog
	Epoch         int    `json:"epoch"`
	FileName      string `json:"file_name"`
	DestinationID string `json:"destination_id"`
	ObjectKey     string `json:"-"`
	SizeBytes     int64  `json:"size_bytes"`
	// StartedAt은 기준 백업을 시작한 시각이다(WAL/binlog는 SourceTime과 같다).
	StartedAt time.Time `json:"started_at"`
	// SourceTime은 이 시각까지의 데이터가 들어 있다는 뜻이다: 기준 백업은 완료 시각, 로그는 보관한 시각.
	SourceTime   time.Time `json:"source_time"`
	CreatedAt    time.Time `json:"created_at"`
	BaseFile     string    `json:"base_file,omitempty"`     // mysql 기준 백업의 binlog 위치
	BasePosition uint64    `json:"base_position,omitempty"` // mysql
}

func migrateDBPITR(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`CREATE TABLE db_pitr_configs (instance_id TEXT PRIMARY KEY, config TEXT NOT NULL)`,
		`CREATE TABLE db_pitr_objects (
			id TEXT PRIMARY KEY, instance_id TEXT NOT NULL, kind TEXT NOT NULL, object_type TEXT NOT NULL, epoch INTEGER NOT NULL DEFAULT 0,
			file_name TEXT NOT NULL, destination_id TEXT NOT NULL DEFAULT '', object_key TEXT NOT NULL,
			size_bytes INTEGER NOT NULL DEFAULT 0, started_at TEXT NOT NULL, source_time TEXT NOT NULL, created_at TEXT NOT NULL,
			base_file TEXT NOT NULL DEFAULT '', base_position INTEGER NOT NULL DEFAULT 0,
			UNIQUE(instance_id, epoch, object_type, file_name))`,
		`CREATE INDEX db_pitr_objects_instance ON db_pitr_objects (instance_id, source_time)`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// GetDBPITRConfig는 설정을 읽는다(없으면 꺼진 기본값: 보관 7일).
func (s *Store) GetDBPITRConfig(ctx context.Context, instanceID string) (*DBPITRConfig, error) {
	var raw string
	err := s.q.QueryRowContext(ctx, `SELECT config FROM db_pitr_configs WHERE instance_id=?`, instanceID).Scan(&raw)
	c := &DBPITRConfig{RetentionDays: 7}
	if err == sql.ErrNoRows {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(raw), c); err != nil {
		return nil, err
	}
	if c.RetentionDays <= 0 {
		c.RetentionDays = 7
	}
	return c, nil
}

func (s *Store) SetDBPITRConfig(ctx context.Context, instanceID string, c *DBPITRConfig) error {
	_, err := s.q.ExecContext(ctx, `INSERT INTO db_pitr_configs (instance_id, config) VALUES (?,?)
		ON CONFLICT(instance_id) DO UPDATE SET config=excluded.config`, instanceID, jsonStr(c))
	return err
}

// DeleteDBPITRConfig는 설정 행을 지운다(인스턴스를 지울 때).
func (s *Store) DeleteDBPITRConfig(ctx context.Context, instanceID string) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM db_pitr_configs WHERE instance_id=?`, instanceID)
	return err
}

// ListEnabledDBPITRConfigs는 켜진 설정을 인스턴스 ID별로 돌려준다.
func (s *Store) ListEnabledDBPITRConfigs(ctx context.Context) (map[string]*DBPITRConfig, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT instance_id, config FROM db_pitr_configs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*DBPITRConfig{}
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		c := &DBPITRConfig{}
		if json.Unmarshal([]byte(raw), c) != nil || !c.Enabled {
			continue
		}
		if c.RetentionDays <= 0 {
			c.RetentionDays = 7
		}
		out[id] = c
	}
	return out, rows.Err()
}

const dbPITRObjectCols = `id, instance_id, kind, object_type, epoch, file_name, destination_id, object_key, size_bytes, started_at, source_time, created_at, base_file, base_position`

func scanDBPITRObject(r scanner) (*DBPITRObject, error) {
	var o DBPITRObject
	var started, source, created string
	if err := r.Scan(&o.ID, &o.InstanceID, &o.Kind, &o.ObjectType, &o.Epoch, &o.FileName, &o.DestinationID, &o.ObjectKey,
		&o.SizeBytes, &started, &source, &created, &o.BaseFile, &o.BasePosition); err != nil {
		return nil, notFound(err)
	}
	o.StartedAt, o.SourceTime, o.CreatedAt = parseTS(started), parseTS(source), parseTS(created)
	return &o, nil
}

// UpsertDBPITRObject는 보관한 파일을 기록한다. 같은 (인스턴스, epoch, 종류, 이름)은 덮어쓴다(다시 올린 WAL).
func (s *Store) UpsertDBPITRObject(ctx context.Context, o *DBPITRObject) error {
	if o.ID == "" {
		o.ID = NewID("pitr")
	}
	if o.CreatedAt.IsZero() {
		o.CreatedAt = time.Now().UTC()
	}
	if o.StartedAt.IsZero() {
		o.StartedAt = o.SourceTime
	}
	_, err := s.q.ExecContext(ctx, `INSERT INTO db_pitr_objects (`+dbPITRObjectCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(instance_id, epoch, object_type, file_name) DO UPDATE SET destination_id=excluded.destination_id,
		object_key=excluded.object_key, size_bytes=excluded.size_bytes, started_at=excluded.started_at, source_time=excluded.source_time,
		base_file=excluded.base_file, base_position=excluded.base_position`,
		o.ID, o.InstanceID, o.Kind, o.ObjectType, o.Epoch, o.FileName, o.DestinationID, o.ObjectKey, o.SizeBytes,
		ts(o.StartedAt), ts(o.SourceTime), ts(o.CreatedAt), o.BaseFile, o.BasePosition)
	return err
}

// ListDBPITRObjects는 인스턴스의 보관 파일을 오래된 순(source_time, 이름)으로 돌려준다.
func (s *Store) ListDBPITRObjects(ctx context.Context, instanceID string) ([]*DBPITRObject, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+dbPITRObjectCols+` FROM db_pitr_objects WHERE instance_id=? ORDER BY source_time, file_name`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*DBPITRObject{}
	for rows.Next() {
		o, err := scanDBPITRObject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// CountDBPITRObjectsOnDestination은 목적지에 보관된 PITR 파일 수다(목적지 삭제 전 확인).
func (s *Store) CountDBPITRObjectsOnDestination(ctx context.Context, destID string) (int, error) {
	var n int
	err := s.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM db_pitr_objects WHERE destination_id=?`, destID).Scan(&n)
	return n, err
}

func (s *Store) DeleteDBPITRObject(ctx context.Context, id string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM db_pitr_objects WHERE id=?`, id))
}
