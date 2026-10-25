package store

import (
	"context"
	"database/sql"
	"net/netip"
	"sort"
	"strings"
)

// 도메인별 공개 IPv6(§7).
//
// 노드마다 IPv6 풀(nodes.ipv6_pool, CIDR)을 두고, 공개 도메인(domain_states 행)마다 그 도메인을 서비스하는 노드의 풀에서 /128
// 주소 하나를 배정한다(domain_states.ipv6, NULL이 아닌 값끼리 UNIQUE). 배정은 결정적이다: 풀의 네트워크 주소와 ::1을 건너뛴
// 가장 낮은 빈 주소. 도메인이 지워지면 행과 함께 주소도 풀린다. 다른 노드로 옮겨 가면 새 노드의 풀에서 다시 배정한다.

// migrateDomainIPv6는 노드 IPv6 풀과 도메인별 IPv6 주소 열을 더한다.
func migrateDomainIPv6(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`ALTER TABLE nodes ADD COLUMN ipv6_pool TEXT`,
		`ALTER TABLE domain_states ADD COLUMN ipv6 TEXT`,
		`CREATE UNIQUE INDEX domain_states_ipv6 ON domain_states(ipv6) WHERE ipv6 IS NOT NULL`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// SetNodeIPv6Pool은 노드(로컬 포함)의 IPv6 풀을 저장한다. 빈 값은 NULL(풀 없음)이다. 검증은 호출자가 한다.
func (s *Store) SetNodeIPv6Pool(ctx context.Context, id, pool string) error {
	var v any
	if pool = strings.TrimSpace(pool); pool != "" {
		v = pool
	}
	return affected(s.q.ExecContext(ctx, `UPDATE nodes SET ipv6_pool=? WHERE id=?`, v, NormNode(id)))
}

// ManagedIPv6s는 지금 배정된 도메인 IPv6 주소 전체다(주소 → 호스트).
func (s *Store) ManagedIPv6s(ctx context.Context) (map[netip.Addr]string, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT host, ipv6 FROM domain_states WHERE ipv6 IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[netip.Addr]string{}
	for rows.Next() {
		var h, v string
		if err := rows.Scan(&h, &v); err != nil {
			return nil, err
		}
		if a, err := netip.ParseAddr(v); err == nil {
			out[a] = h
		}
	}
	return out, rows.Err()
}

// IPv6Assignment은 ReconcileDomainIPv6의 변경 하나다.
type IPv6Assignment struct {
	Host string
	Old  string // 이전 주소(없으면 "")
	New  string // 새 주소(풀이 없어 풀렸으면 "")
}

// ReconcileDomainIPv6은 한 트랜잭션에서 domain_states의 ipv6를 want(호스트 → 그 호스트를 서비스하는 노드의 풀)에 맞춘다.
//   - want에 없는 호스트(풀이 없는 노드)와 주소가 풀 밖인 호스트(다른 노드로 옮겨 감, 풀이 바뀜)는 주소를 푼다.
//   - 주소가 없는 호스트는 풀에서 가장 낮은 빈 주소(네트워크 주소와 ::1 제외)를 받는다. 순서는 created_at, host 순이다.
//   - 이미 풀 안의 주소를 가진 호스트는 그대로 둔다(주소가 바뀌지 않는다).
//
// 풀이 가득 차면 그 호스트는 주소 없이 남는다(다음 호출에서 다시 시도한다). 바뀐 항목을 돌려준다.
func (s *Store) ReconcileDomainIPv6(ctx context.Context, want map[string]netip.Prefix) ([]IPv6Assignment, error) {
	var changes []IPv6Assignment
	err := s.Tx(ctx, func(tx *Store) error {
		changes = nil
		rows, err := tx.q.QueryContext(ctx, `SELECT host, COALESCE(ipv6,'') FROM domain_states ORDER BY created_at, host`)
		if err != nil {
			return err
		}
		type row struct{ host, ipv6 string }
		var all []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.host, &r.ipv6); err != nil {
				rows.Close()
				return err
			}
			all = append(all, r)
		}
		rows.Close()
		used := map[netip.Addr]bool{}
		var need []row
		for _, r := range all {
			pool, ok := want[r.host]
			cur, perr := netip.ParseAddr(r.ipv6)
			keep := r.ipv6 != "" && perr == nil && ok && pool.Contains(cur) && cur != pool.Addr() && !used[cur]
			if keep {
				used[cur] = true
				continue
			}
			if r.ipv6 != "" {
				if _, err := tx.q.ExecContext(ctx, `UPDATE domain_states SET ipv6=NULL WHERE host=?`, r.host); err != nil {
					return err
				}
				if !ok {
					changes = append(changes, IPv6Assignment{Host: r.host, Old: r.ipv6})
				}
			}
			if ok {
				need = append(need, r)
			}
		}
		for _, r := range need {
			pool := want[r.host]
			a, ok := nextFreeIPv6(pool, used)
			if !ok {
				if r.ipv6 != "" {
					changes = append(changes, IPv6Assignment{Host: r.host, Old: r.ipv6})
				}
				continue
			}
			if _, err := tx.q.ExecContext(ctx, `UPDATE domain_states SET ipv6=? WHERE host=?`, a.String(), r.host); err != nil {
				return err
			}
			used[a] = true
			changes = append(changes, IPv6Assignment{Host: r.host, Old: r.ipv6, New: a.String()})
		}
		return nil
	})
	return changes, err
}

// nextFreeIPv6은 pool에서 used에 없는 가장 낮은 주소다(네트워크 주소와 ::1은 건너뛴다).
func nextFreeIPv6(pool netip.Prefix, used map[netip.Addr]bool) (netip.Addr, bool) {
	// 풀 안의 사용 중 주소만 정렬해서 훑는다(풀이 커도 배정된 수만큼만 돈다).
	var in []netip.Addr
	for a := range used {
		if pool.Contains(a) {
			in = append(in, a)
		}
	}
	sort.Slice(in, func(i, j int) bool { return in[i].Less(in[j]) })
	cand := pool.Addr().Next().Next() // 네트워크 주소, ::1 건너뛰기
	for _, a := range in {
		if a.Less(cand) {
			continue
		}
		if a != cand {
			break
		}
		cand = cand.Next()
	}
	if !cand.IsValid() || !pool.Contains(cand) {
		return netip.Addr{}, false
	}
	return cand, true
}

// DomainIPv6는 호스트에 배정된 주소다(없으면 "").
func (s *Store) DomainIPv6(ctx context.Context, host string) string {
	var v sql.NullString
	if err := s.q.QueryRowContext(ctx, `SELECT ipv6 FROM domain_states WHERE host=?`, claimHost(host)).Scan(&v); err != nil {
		return ""
	}
	return v.String
}
