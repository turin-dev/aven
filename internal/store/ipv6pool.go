package store

import (
	"context"
	"database/sql"
	"net/netip"
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

// IPv6Reserve는 풀 하나에서 배정하면 안 되는 주소다(노드의 기본 IPv6, 이미 쓰는 주소, 노드 서브넷).
type IPv6Reserve struct {
	Addrs    map[netip.Addr]bool
	Prefixes []netip.Prefix
	// NoNew면 새 주소를 배정하지 않는다(노드 주소를 확인하지 못해 무엇이 비었는지 모른다). 이미 배정된 주소는 그대로 둔다.
	NoNew bool
}

// Has는 a가 예약된 주소인지다.
func (r *IPv6Reserve) Has(a netip.Addr) bool {
	if r == nil {
		return false
	}
	if r.Addrs[a] {
		return true
	}
	for _, p := range r.Prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
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
//   - 이미 풀 안의 주소를 가진 호스트는 그대로 둔다(주소가 바뀌지 않는다). 그 주소가 예약(reserve)되었으면 다시 배정한다.
//   - reserve는 풀마다 건너뛸 주소다(nil이면 없음).
//
// 풀이 가득 차면 그 호스트는 주소 없이 남는다(다음 호출에서 다시 시도한다). 바뀐 항목을 돌려준다.
func (s *Store) ReconcileDomainIPv6(ctx context.Context, want map[string]netip.Prefix, reserve map[netip.Prefix]*IPv6Reserve) ([]IPv6Assignment, error) {
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
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		used := map[netip.Addr]bool{}
		var need []row
		for _, r := range all {
			pool, ok := want[r.host]
			cur, perr := netip.ParseAddr(r.ipv6)
			keep := r.ipv6 != "" && perr == nil && ok && pool.Contains(cur) && cur != pool.Addr() && !used[cur] && !reserve[pool].Has(cur)
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
			var a netip.Addr
			ok := false
			if rs := reserve[pool]; rs == nil || !rs.NoNew {
				a, ok = nextFreeIPv6(pool, used, rs)
			}
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

// nextFreeIPv6은 pool에서 used와 rs(예약)에 없는 가장 낮은 주소다(네트워크 주소와 ::1은 건너뛴다).
func nextFreeIPv6(pool netip.Prefix, used map[netip.Addr]bool, rs *IPv6Reserve) (netip.Addr, bool) {
	cand := pool.Addr().Next().Next() // 네트워크 주소, ::1 건너뛰기
	for cand.IsValid() && pool.Contains(cand) {
		if !used[cand] && !rs.Has(cand) {
			return cand, true
		}
		// 예약 서브넷 안이면 그 끝 다음으로 건너뛴다.
		next := cand.Next()
		if rs != nil {
			for _, p := range rs.Prefixes {
				if p.Contains(cand) {
					if end := lastAddr(p).Next(); end.IsValid() && next.Less(end) {
						next = end
					}
				}
			}
		}
		cand = next
	}
	return netip.Addr{}, false
}

// lastAddr는 p의 마지막 주소다.
func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Masked().Addr().As16()
	for i := p.Bits(); i < 128; i++ {
		b[i/8] |= 1 << (7 - uint(i%8))
	}
	return netip.AddrFrom16(b)
}

// DomainIPv6는 호스트에 배정된 주소다(없으면 "").
func (s *Store) DomainIPv6(ctx context.Context, host string) string {
	var v sql.NullString
	if err := s.q.QueryRowContext(ctx, `SELECT ipv6 FROM domain_states WHERE host=?`, claimHost(host)).Scan(&v); err != nil {
		return ""
	}
	return v.String
}
