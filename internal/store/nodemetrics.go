package store

import "context"

// NodeMetricRow는 노드 하나의 한 시점 사용량이다(인스턴스 metrics와 같은 보관 정책).
type NodeMetricRow struct {
	T          int64
	CPU        float64 // 노드 전체 CPU 사용률 0..100
	Mem        int64   // 사용 중인 메모리 bytes
	DiskFree   int64
	Resolution int // 초
}

// migrateV9는 노드별 metrics 테이블과 draining 표시를 추가한다.
func migrateV9(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`ALTER TABLE nodes ADD COLUMN draining INTEGER NOT NULL DEFAULT 0`,
		`CREATE TABLE node_metrics (node_id TEXT NOT NULL, t INTEGER NOT NULL, res INTEGER NOT NULL, cpu REAL NOT NULL, mem INTEGER NOT NULL, disk_free INTEGER NOT NULL)`,
		`CREATE INDEX node_metrics_node_t ON node_metrics(node_id, t)`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) InsertNodeMetrics(ctx context.Context, nodeID string, rows []NodeMetricRow) error {
	return s.Tx(ctx, func(tx *Store) error {
		for _, r := range rows {
			if _, err := tx.q.ExecContext(ctx, `INSERT INTO node_metrics (node_id, t, res, cpu, mem, disk_free) VALUES (?,?,?,?,?,?)`,
				nodeID, r.T, r.Resolution, r.CPU, r.Mem, r.DiskFree); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) QueryNodeMetrics(ctx context.Context, nodeID string, from int64) ([]NodeMetricRow, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT t, res, cpu, mem, disk_free FROM node_metrics WHERE node_id=? AND t>=? ORDER BY t`, nodeID, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NodeMetricRow{}
	for rows.Next() {
		var r NodeMetricRow
		if err := rows.Scan(&r.T, &r.Resolution, &r.CPU, &r.Mem, &r.DiskFree); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReplaceNodeMetrics는 [from,to) 구간의 원본 점을 다운샘플한 점으로 바꾼다.
func (s *Store) ReplaceNodeMetrics(ctx context.Context, nodeID string, from, to int64, rows []NodeMetricRow) error {
	return s.Tx(ctx, func(tx *Store) error {
		if _, err := tx.q.ExecContext(ctx, `DELETE FROM node_metrics WHERE node_id=? AND t>=? AND t<?`, nodeID, from, to); err != nil {
			return err
		}
		for _, r := range rows {
			if _, err := tx.q.ExecContext(ctx, `INSERT INTO node_metrics (node_id, t, res, cpu, mem, disk_free) VALUES (?,?,?,?,?,?)`,
				nodeID, r.T, r.Resolution, r.CPU, r.Mem, r.DiskFree); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) NodeMetricNodes(ctx context.Context) ([]string, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT DISTINCT node_id FROM node_metrics`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		out = append(out, id)
	}
	return out, rows.Err()
}

// PruneNodeMetrics는 before보다 오래된 점과 삭제된 노드의 점을 지운다.
func (s *Store) PruneNodeMetrics(ctx context.Context, before int64) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM node_metrics WHERE t < ? OR node_id NOT IN (SELECT id FROM nodes)`, before)
	return err
}

func (s *Store) DeleteNodeMetrics(ctx context.Context, nodeID string) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM node_metrics WHERE node_id=?`, nodeID)
	return err
}
