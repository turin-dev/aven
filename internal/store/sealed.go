package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

//
// 봉인은 (소유자, 키, 값) 단위로 기록한다. 소유자는 인스턴스 ID이거나 SharedSealOwner(envID)다.
// 값의 해시를 함께 적어 두므로, 키를 지웠다가 다른 값으로 다시 만들면 봉인이 풀린 새 변수가 된다(unseal 대신
// 삭제 후 재생성). 반대로 롤백·동기화 등으로 예전에 봉인된 값이 되돌아오면 그대로 봉인 상태다.
// 행은 지우지 않는다(값이 바뀌어도 예전 값의 봉인을 기억해야 롤백 때 새지 않는다).

// migrateSealedVariables는 봉인 변수 기록 테이블을 추가한다.
func migrateSealedVariables(ctx context.Context, tx *Store) error {
	_, err := tx.q.ExecContext(ctx, `CREATE TABLE sealed_variables (
		owner TEXT NOT NULL,
		key TEXT NOT NULL,
		value_hash TEXT NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY (owner, key, value_hash)
	)`)
	return err
}

// SharedSealOwner는 환경 공유 변수의 봉인 소유자 이름이다.
func SharedSealOwner(envID string) string { return "shared:" + envID }

// SealHash는 봉인 기록에 쓰는 (소유자, 키, 값)의 sha256(hex)이다.
func SealHash(owner, key, value string) string {
	h := sha256.New()
	h.Write([]byte(owner))
	h.Write([]byte{0})
	h.Write([]byte(key))
	h.Write([]byte{0})
	h.Write([]byte(value))
	return hex.EncodeToString(h.Sum(nil))
}

// SealVariables는 vars의 keys를 지금 값으로 봉인한다. vars에 없는 키는 건너뛴다.
func (s *Store) SealVariables(ctx context.Context, owner string, vars map[string]string, keys []string) error {
	now := ts(time.Now().UTC())
	for _, k := range keys {
		v, ok := vars[k]
		if !ok {
			continue
		}
		if _, err := s.q.ExecContext(ctx, `INSERT INTO sealed_variables (owner, key, value_hash, created_at) VALUES (?,?,?,?)
			ON CONFLICT(owner, key, value_hash) DO NOTHING`, owner, k, SealHash(owner, k, v), now); err != nil {
			return err
		}
	}
	return nil
}

// SealRecords는 소유자별 키 → 봉인된 값 해시 집합이다.
type SealRecords map[string]map[string]map[string]bool

// SealedRecords는 owners의 봉인 기록을 읽는다.
func (s *Store) SealedRecords(ctx context.Context, owners ...string) (SealRecords, error) {
	out := SealRecords{}
	if len(owners) == 0 {
		return out, nil
	}
	args := make([]any, len(owners))
	for i, o := range owners {
		args[i] = o
	}
	rows, err := s.q.QueryContext(ctx, `SELECT owner, key, value_hash FROM sealed_variables WHERE owner IN (?`+strings.Repeat(",?", len(owners)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var o, k, h string
		if err := rows.Scan(&o, &k, &h); err != nil {
			return nil, err
		}
		if out[o] == nil {
			out[o] = map[string]map[string]bool{}
		}
		if out[o][k] == nil {
			out[o][k] = map[string]bool{}
		}
		out[o][k][h] = true
	}
	return out, rows.Err()
}

// Sealed는 vars 중 지금 값이 봉인된 키 집합이다.
func (r SealRecords) Sealed(owner string, vars map[string]string) map[string]bool {
	out := map[string]bool{}
	recs := r[owner]
	for k, v := range vars {
		if hs := recs[k]; hs != nil && hs[SealHash(owner, k, v)] {
			out[k] = true
		}
	}
	return out
}

// SealedKeys는 owner의 vars 중 봉인된 키 집합이다.
func (s *Store) SealedKeys(ctx context.Context, owner string, vars map[string]string) (map[string]bool, error) {
	if len(vars) == 0 {
		return map[string]bool{}, nil
	}
	r, err := s.SealedRecords(ctx, owner)
	if err != nil {
		return nil, err
	}
	return r.Sealed(owner, vars), nil
}

// SortedKeys는 집합의 키를 정렬해 돌려준다(빈 집합이면 빈 슬라이스).
func SortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
