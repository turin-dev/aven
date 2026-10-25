package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

func migrateDBBackups(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`CREATE TABLE backup_destinations (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, type TEXT NOT NULL, config TEXT NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE db_backups (id TEXT PRIMARY KEY, service_id TEXT NOT NULL, environment_id TEXT NOT NULL, instance_id TEXT NOT NULL,
			kind TEXT NOT NULL, status TEXT NOT NULL, trigger TEXT NOT NULL, destination_id TEXT NOT NULL DEFAULT '', object_key TEXT NOT NULL DEFAULT '',
			file_name TEXT NOT NULL DEFAULT '', size_bytes INTEGER NOT NULL DEFAULT 0, error TEXT, created_at TEXT NOT NULL, finished_at TEXT)`,
		`CREATE INDEX db_backups_instance ON db_backups (instance_id, created_at)`,
		`CREATE TABLE db_backup_configs (instance_id TEXT PRIMARY KEY, config TEXT NOT NULL)`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// ---------------- 목적지 ----------------

const (
	DestLocal = "local"
	DestS3    = "s3"
)

// BackupDestination은 백업 저장 위치다. local은 KEEL_DATA 아래, s3는 S3 호환 저장소다.
// SecretKey는 저장할 때 마스터 키로 암호화하고, API 응답에는 넣지 않는다.
type BackupDestination struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Endpoint  string    `json:"endpoint,omitempty"`
	Region    string    `json:"region,omitempty"`
	Bucket    string    `json:"bucket,omitempty"`
	Prefix    string    `json:"prefix,omitempty"`
	AccessKey string    `json:"access_key,omitempty"`
	SecretKey string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
}

type destConfig struct {
	Endpoint  string `json:"endpoint,omitempty"`
	Region    string `json:"region,omitempty"`
	Bucket    string `json:"bucket,omitempty"`
	Prefix    string `json:"prefix,omitempty"`
	AccessKey string `json:"access_key,omitempty"`
	SecretKey string `json:"secret_key,omitempty"` // 암호화됨
}

func (s *Store) destRow(d *BackupDestination) string {
	b, _ := json.Marshal(destConfig{d.Endpoint, d.Region, d.Bucket, d.Prefix, d.AccessKey, s.Seal(d.SecretKey)})
	return string(b)
}

func (s *Store) scanDest(r scanner) (*BackupDestination, error) {
	var d BackupDestination
	var cfg, created string
	if err := r.Scan(&d.ID, &d.Name, &d.Type, &cfg, &created); err != nil {
		return nil, notFound(err)
	}
	var c destConfig
	json.Unmarshal([]byte(cfg), &c)
	sec, err := s.Unseal(c.SecretKey)
	if err != nil {
		return nil, err
	}
	d.Endpoint, d.Region, d.Bucket, d.Prefix, d.AccessKey, d.SecretKey = c.Endpoint, c.Region, c.Bucket, c.Prefix, c.AccessKey, sec
	d.CreatedAt = parseTS(created)
	return &d, nil
}

const destCols = `id, name, type, config, created_at`

func (s *Store) CreateBackupDestination(ctx context.Context, d *BackupDestination) error {
	d.ID, d.CreatedAt = NewID("bkd"), time.Now().UTC()
	_, err := s.q.ExecContext(ctx, `INSERT INTO backup_destinations (`+destCols+`) VALUES (?,?,?,?,?)`, d.ID, d.Name, d.Type, s.destRow(d), ts(d.CreatedAt))
	return conflict(err)
}

func (s *Store) UpdateBackupDestination(ctx context.Context, d *BackupDestination) error {
	return affected(s.q.ExecContext(ctx, `UPDATE backup_destinations SET name=?, type=?, config=? WHERE id=?`, d.Name, d.Type, s.destRow(d), d.ID))
}

func (s *Store) GetBackupDestination(ctx context.Context, id string) (*BackupDestination, error) {
	return s.scanDest(s.q.QueryRowContext(ctx, `SELECT `+destCols+` FROM backup_destinations WHERE id=?`, id))
}

func (s *Store) ListBackupDestinations(ctx context.Context) ([]*BackupDestination, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+destCols+` FROM backup_destinations ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*BackupDestination{}
	for rows.Next() {
		d, err := s.scanDest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) DeleteBackupDestination(ctx context.Context, id string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM backup_destinations WHERE id=?`, id))
}

// ---------------- DB 백업 기록 ----------------

type DBBackup struct {
	ID            string     `json:"id"`
	ServiceID     string     `json:"service_id"`
	EnvironmentID string     `json:"environment_id"`
	InstanceID    string     `json:"instance_id"`
	Kind          string     `json:"kind"`   // postgres | mysql | mariadb | redis | mongo
	Status        string     `json:"status"` // running | done | failed
	Trigger       string     `json:"trigger"`
	DestinationID string     `json:"destination_id"` // 빈 값 = local
	ObjectKey     string     `json:"-"`
	FileName      string     `json:"file_name"`
	SizeBytes     int64      `json:"size_bytes"`
	Error         *string    `json:"error"`
	CreatedAt     time.Time  `json:"created_at"`
	FinishedAt    *time.Time `json:"finished_at"`
}

const dbBackupCols = `id, service_id, environment_id, instance_id, kind, status, trigger, destination_id, object_key, file_name, size_bytes, error, created_at, finished_at`

func scanDBBackup(r scanner) (*DBBackup, error) {
	var b DBBackup
	var errS, fin sql.NullString
	var created string
	if err := r.Scan(&b.ID, &b.ServiceID, &b.EnvironmentID, &b.InstanceID, &b.Kind, &b.Status, &b.Trigger, &b.DestinationID, &b.ObjectKey,
		&b.FileName, &b.SizeBytes, &errS, &created, &fin); err != nil {
		return nil, notFound(err)
	}
	b.Error, b.CreatedAt, b.FinishedAt = nullStr(errS), parseTS(created), parseNullTS(fin)
	return &b, nil
}

func (s *Store) CreateDBBackup(ctx context.Context, b *DBBackup) error {
	b.ID, b.CreatedAt = NewID("dbb"), time.Now().UTC()
	_, err := s.q.ExecContext(ctx, `INSERT INTO db_backups (`+dbBackupCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		b.ID, b.ServiceID, b.EnvironmentID, b.InstanceID, b.Kind, b.Status, b.Trigger, b.DestinationID, b.ObjectKey, b.FileName, b.SizeBytes, b.Error, ts(b.CreatedAt), nullTS(b.FinishedAt))
	return err
}

func (s *Store) UpdateDBBackup(ctx context.Context, b *DBBackup) error {
	_, err := s.q.ExecContext(ctx, `UPDATE db_backups SET status=?, size_bytes=?, error=?, finished_at=? WHERE id=?`, b.Status, b.SizeBytes, b.Error, nullTS(b.FinishedAt), b.ID)
	return err
}

func (s *Store) GetDBBackup(ctx context.Context, id string) (*DBBackup, error) {
	return scanDBBackup(s.q.QueryRowContext(ctx, `SELECT `+dbBackupCols+` FROM db_backups WHERE id=?`, id))
}

// ListDBBackups는 인스턴스의 백업을 최신순으로 돌려준다.
func (s *Store) ListDBBackups(ctx context.Context, instanceID string) ([]*DBBackup, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+dbBackupCols+` FROM db_backups WHERE instance_id=? ORDER BY created_at DESC, id DESC`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*DBBackup{}
	for rows.Next() {
		b, err := scanDBBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) DeleteDBBackup(ctx context.Context, id string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM db_backups WHERE id=?`, id))
}

func (s *Store) FailRunningDBBackups(ctx context.Context) error {
	_, err := s.q.ExecContext(ctx, `UPDATE db_backups SET status='failed', error='server restarted during backup' WHERE status='running'`)
	return err
}

// ---------------- 일정 ----------------

// DBBackupConfig는 DB 서비스 인스턴스의 정기 백업 설정이다.
type DBBackupConfig struct {
	Enabled       bool   `json:"enabled"`
	Cron          string `json:"cron"`
	Retention     int    `json:"retention"`      // 보관 개수(성공한 백업 기준)
	DestinationID string `json:"destination_id"` // 빈 값 = local
}

func (s *Store) GetDBBackupConfig(ctx context.Context, instanceID string) (*DBBackupConfig, error) {
	var raw string
	err := s.q.QueryRowContext(ctx, `SELECT config FROM db_backup_configs WHERE instance_id=?`, instanceID).Scan(&raw)
	c := &DBBackupConfig{Cron: "0 3 * * *", Retention: 7}
	if err == sql.ErrNoRows {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(raw), c)
	return c, nil
}

func (s *Store) SetDBBackupConfig(ctx context.Context, instanceID string, c *DBBackupConfig) error {
	_, err := s.q.ExecContext(ctx, `INSERT INTO db_backup_configs (instance_id, config) VALUES (?,?) ON CONFLICT(instance_id) DO UPDATE SET config=excluded.config`, instanceID, jsonStr(c))
	return err
}

// ListEnabledDBBackupConfigs는 정기 백업이 켜진 인스턴스의 설정을 돌려준다.
func (s *Store) ListEnabledDBBackupConfigs(ctx context.Context) (map[string]*DBBackupConfig, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT instance_id, config FROM db_backup_configs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*DBBackupConfig{}
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var c DBBackupConfig
		if json.Unmarshal([]byte(raw), &c) == nil && c.Enabled {
			out[id] = &c
		}
	}
	return out, rows.Err()
}

// ExecDBBackupKey는 파일 이름과 저장소 키를 기록한다.
func (s *Store) ExecDBBackupKey(ctx context.Context, b *DBBackup) {
	s.q.ExecContext(ctx, `UPDATE db_backups SET file_name=?, object_key=? WHERE id=?`, b.FileName, b.ObjectKey, b.ID)
}

// CountDBBackupsOnDestination은 목적지에 남아 있는 백업 기록 수다.
func (s *Store) CountDBBackupsOnDestination(ctx context.Context, destID string) (int, error) {
	var n int
	err := s.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM db_backups WHERE destination_id=?`, destID).Scan(&n)
	return n, err
}
