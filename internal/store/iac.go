package store

import (
	"context"
	"time"
)

// migrateIAC는 코드형 인프라(IaC)의 named partial 소유권 표를 추가한다(환경마다 주소 service.<name> → partial).
func migrateIAC(ctx context.Context, tx *Store) error {
	_, err := tx.q.ExecContext(ctx, `CREATE TABLE iac_ownership (
		environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
		address TEXT NOT NULL,
		partial TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (environment_id, address)
	)`)
	return err
}

// ListIACOwnership은 환경의 소유권(주소 → partial)이다.
func (s *Store) ListIACOwnership(ctx context.Context, environmentID string) (map[string]string, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT address, partial FROM iac_ownership WHERE environment_id=? ORDER BY address`, environmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var address, partial string
		if err := rows.Scan(&address, &partial); err != nil {
			return nil, err
		}
		out[address] = partial
	}
	return out, rows.Err()
}

// SetIACOwnership은 주소의 소유 partial을 정한다(있으면 바꾼다).
func (s *Store) SetIACOwnership(ctx context.Context, environmentID, address, partial string) error {
	_, err := s.q.ExecContext(ctx, `INSERT INTO iac_ownership (environment_id,address,partial,updated_at) VALUES (?,?,?,?)
		ON CONFLICT(environment_id,address) DO UPDATE SET partial=excluded.partial, updated_at=excluded.updated_at`,
		environmentID, address, partial, ts(time.Now().UTC()))
	return err
}

func (s *Store) DeleteIACOwnership(ctx context.Context, environmentID, address string) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM iac_ownership WHERE environment_id=? AND address=?`, environmentID, address)
	return err
}

// ClearIACOwnership은 환경의 소유권을 모두 지운다(전체 프로젝트 정의를 적용할 때).
func (s *Store) ClearIACOwnership(ctx context.Context, environmentID string) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM iac_ownership WHERE environment_id=?`, environmentID)
	return err
}
