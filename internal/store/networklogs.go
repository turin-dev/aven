package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// NetworkLog는 보관하는 DNS 조회 또는 연결(flow) 기록이다. Data는 netlog 기록(JSON)이다.
// ServiceID는 기록의 주인(질의한 서비스, flow를 시작했거나 받은 서비스), RelatedServiceID는 flow의 상대 서비스다.
type NetworkLog struct {
	ID               string          `json:"id"`
	EnvironmentID    string          `json:"environment_id"`
	ServiceID        string          `json:"service_id"`
	RelatedServiceID string          `json:"related_service_id,omitempty"`
	Kind             string          `json:"kind"`
	RecordedAt       time.Time       `json:"recorded_at"`
	Data             json.RawMessage `json:"data"`
}

// NetworkLogQuery는 ListNetworkLogs의 조건이다. Services는 flow의 양쪽 어느 쪽이든 맞으면 고른다.
type NetworkLogQuery struct {
	EnvironmentID string
	Kind          string
	Services      []string
	Since, Until  time.Time // 0이면 제한 없음
	Limit         int
}

func validNetworkLog(l *NetworkLog) error {
	if l.Kind != "dns" && l.Kind != "network" {
		return errors.New("network log kind must be dns or network")
	}
	if l.EnvironmentID == "" || !json.Valid(l.Data) {
		return errors.New("network log needs an environment and valid JSON data")
	}
	return nil
}

// AddNetworkLogs는 기록을 한 트랜잭션으로 넣고 새로 들어간 기록만 돌려준다(같은 ID는 무시: agent 재연결 replay).
// 환경이 이미 지워졌으면 그 기록은 건너뛴다.
func (s *Store) AddNetworkLogs(ctx context.Context, logs []*NetworkLog) ([]*NetworkLog, error) {
	var inserted []*NetworkLog
	err := s.Tx(ctx, func(tx *Store) error {
		inserted = inserted[:0]
		for _, l := range logs {
			if l.ID == "" {
				l.ID = NewID("nlog")
			}
			if err := validNetworkLog(l); err != nil {
				return err
			}
			if l.RecordedAt.IsZero() {
				l.RecordedAt = time.Now()
			}
			l.RecordedAt = l.RecordedAt.UTC()
			r, err := tx.q.ExecContext(ctx, `INSERT OR IGNORE INTO network_logs
				(id, environment_id, service_id, related_service_id, kind, recorded_at, data)
				SELECT ?,?,?,?,?,?,? WHERE EXISTS (SELECT 1 FROM environments WHERE id=?)`,
				l.ID, l.EnvironmentID, l.ServiceID, l.RelatedServiceID, l.Kind, ts(l.RecordedAt), string(l.Data), l.EnvironmentID)
			if err != nil {
				return err
			}
			if n, _ := r.RowsAffected(); n > 0 {
				inserted = append(inserted, l)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return inserted, nil
}

// AddNetworkLog는 기록 하나를 넣는다. 새로 들어갔으면 true.
func (s *Store) AddNetworkLog(ctx context.Context, l *NetworkLog) (bool, error) {
	in, err := s.AddNetworkLogs(ctx, []*NetworkLog{l})
	return len(in) > 0, err
}

// ListNetworkLogs는 조건에 맞는 기록을 최신 것부터 돌려준다(Limit 기본 500, 최대 5000).
func (s *Store) ListNetworkLogs(ctx context.Context, f NetworkLogQuery) ([]*NetworkLog, error) {
	if f.Limit <= 0 || f.Limit > 5000 {
		f.Limit = 500
	}
	q := `SELECT id, environment_id, service_id, related_service_id, kind, recorded_at, data
		FROM network_logs WHERE environment_id=?`
	args := []any{f.EnvironmentID}
	if f.Kind != "" {
		q += ` AND kind=?`
		args = append(args, f.Kind)
	}
	if len(f.Services) > 0 {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(f.Services)), ",")
		q += ` AND (service_id IN (` + ph + `) OR related_service_id IN (` + ph + `))`
		for range 2 {
			for _, id := range f.Services {
				args = append(args, id)
			}
		}
	}
	if !f.Since.IsZero() {
		q += ` AND recorded_at >= ?`
		args = append(args, ts(f.Since))
	}
	if !f.Until.IsZero() {
		q += ` AND recorded_at <= ?`
		args = append(args, ts(f.Until))
	}
	q += ` ORDER BY recorded_at DESC, id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*NetworkLog
	for rows.Next() {
		var l NetworkLog
		var recorded, data string
		if err := rows.Scan(&l.ID, &l.EnvironmentID, &l.ServiceID, &l.RelatedServiceID, &l.Kind, &recorded, &data); err != nil {
			return nil, err
		}
		l.RecordedAt = parseTS(recorded)
		l.Data = json.RawMessage(data)
		out = append(out, &l)
	}
	return out, rows.Err()
}

// PruneNetworkLogs는 cutoff보다 오래된 기록을 지우고, 그래도 maxRows보다 많으면 오래된 것부터 지운다(0이면 개수 제한 없음).
func (s *Store) PruneNetworkLogs(ctx context.Context, cutoff time.Time, maxRows int) (int64, error) {
	r, err := s.q.ExecContext(ctx, `DELETE FROM network_logs WHERE recorded_at < ?`, ts(cutoff))
	if err != nil {
		return 0, err
	}
	n, _ := r.RowsAffected()
	if maxRows > 0 {
		r, err = s.q.ExecContext(ctx, `DELETE FROM network_logs WHERE recorded_at < (
			SELECT recorded_at FROM network_logs ORDER BY recorded_at DESC LIMIT 1 OFFSET ?)`, maxRows-1)
		if err != nil {
			return n, err
		}
		m, _ := r.RowsAffected()
		n += m
	}
	return n, nil
}

// migrateNetworkLogs는 DNS 조회와 연결(flow) 기록 테이블을 만든다(보관 기간은 deploy.NetworkLogRetention).
func migrateNetworkLogs(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS network_logs (
			id TEXT PRIMARY KEY,
			environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
			service_id TEXT NOT NULL DEFAULT '',
			related_service_id TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL CHECK(kind IN ('dns', 'network')),
			recorded_at TEXT NOT NULL,
			data TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS network_logs_env_kind_time ON network_logs(environment_id, kind, recorded_at DESC)`,
		`CREATE INDEX IF NOT EXISTS network_logs_time ON network_logs(recorded_at)`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
