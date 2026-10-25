package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/turin-dev/aven/internal/edge"
)

// GetEdgeSettings는 인스턴스(서비스 × 환경)의 엣지 설정이다. 저장한 적 없으면 기본값이다.
func (s *Store) GetEdgeSettings(ctx context.Context, instanceID string) (edge.Settings, error) {
	var raw string
	err := s.q.QueryRowContext(ctx, `SELECT config FROM edge_settings WHERE instance_id=?`, instanceID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return edge.DefaultSettings(), nil
	}
	if err != nil {
		return edge.Settings{}, err
	}
	settings := edge.DefaultSettings()
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return edge.Settings{}, err
	}
	settings.Normalize()
	return settings, nil
}

// SaveEdgeSettings는 엣지 설정을 통째로 저장한다(검사는 호출자가 한다).
func (s *Store) SaveEdgeSettings(ctx context.Context, instanceID string, settings edge.Settings) error {
	settings.Normalize()
	b, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	_, err = s.q.ExecContext(ctx, `INSERT INTO edge_settings (instance_id, config) VALUES (?,?)
		ON CONFLICT(instance_id) DO UPDATE SET config=excluded.config`, instanceID, string(b))
	return err
}

// ListEdgeSettings는 저장된 엣지 설정 전부다(instance id → 설정).
func (s *Store) ListEdgeSettings(ctx context.Context) (map[string]edge.Settings, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT instance_id, config FROM edge_settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]edge.Settings{}
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		settings := edge.DefaultSettings()
		if json.Unmarshal([]byte(raw), &settings) == nil {
			settings.Normalize()
			out[id] = settings
		}
	}
	return out, rows.Err()
}

// migrateEdgeSettings는 인스턴스별 엣지 설정(JSON) 표를 만든다.
func migrateEdgeSettings(ctx context.Context, tx *Store) error {
	_, err := tx.q.ExecContext(ctx, `CREATE TABLE edge_settings (
		instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
		config TEXT NOT NULL DEFAULT '{}'
	)`)
	return err
}
