package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// 노드 월 트래픽(월 데이터 상한).
//
// 수집기가 10초마다 노드 물리 인터페이스의 누적 카운터(부팅 이후)를 넘기면, 직전 값과의 차이를 그 달(UTC 달력 월) 합계에 더한다.
// 카운터가 줄었으면 재부팅으로 보고 지금 값을 그대로 더한다. 노드가 꺼져 있던 동안의 트래픽은 다음 표본에서 한꺼번에 들어온다.

// NodeTraffic은 노드 하나의 한 달 트래픽이다.
type NodeTraffic struct {
	Month   string `json:"month"` // "2026-11" (UTC)
	RxBytes int64  `json:"rx_bytes"`
	TxBytes int64  `json:"tx_bytes"`
	// AlertedPct는 이번 달에 이미 보낸 상한 알림 단계다(0, 80, 100).
	AlertedPct int `json:"-"`
}

// Total은 받은 바이트와 보낸 바이트의 합이다. 회선 상한은 보통 양방향 합으로 센다.
func (t NodeTraffic) Total() int64 { return t.RxBytes + t.TxBytes }

// TrafficMonth는 t가 속한 집계 월이다.
func TrafficMonth(t time.Time) string { return t.UTC().Format("2006-01") }

// maxLinkBytesPerSec은 표본 하나에 더할 수 있는 최대 속도다(400Gbit/s). 넘으면 카운터가 이상한 것으로 보고 버린다.
const maxLinkBytesPerSec = 50_000_000_000

func migrateNodeTraffic(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS node_traffic_counters (
			node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
			last_rx INTEGER NOT NULL, last_tx INTEGER NOT NULL, last_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS node_traffic (
			node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
			month TEXT NOT NULL,
			rx_bytes INTEGER NOT NULL DEFAULT 0, tx_bytes INTEGER NOT NULL DEFAULT 0,
			alerted_pct INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (node_id, month)
		)`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// AddNodeTraffic은 누적 카운터 표본(rx, tx)을 받아 직전 표본과의 차이를 now가 속한 달에 더한다.
func (s *Store) AddNodeTraffic(ctx context.Context, nodeID string, rx, tx uint64, now time.Time) error {
	nodeID = NormNode(nodeID)
	return s.Tx(ctx, func(t *Store) error {
		var lastRx, lastTx, lastAt int64
		err := t.q.QueryRowContext(ctx, `SELECT last_rx, last_tx, last_at FROM node_traffic_counters WHERE node_id=?`, nodeID).Scan(&lastRx, &lastTx, &lastAt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		first := errors.Is(err, sql.ErrNoRows)
		if _, err := t.q.ExecContext(ctx, `INSERT INTO node_traffic_counters (node_id, last_rx, last_tx, last_at) VALUES (?,?,?,?)
			ON CONFLICT(node_id) DO UPDATE SET last_rx=excluded.last_rx, last_tx=excluded.last_tx, last_at=excluded.last_at`,
			nodeID, int64(rx), int64(tx), now.Unix()); err != nil {
			return err
		}
		if first {
			return nil // 기준점만 잡는다
		}
		delta := func(cur uint64, last int64) int64 {
			if int64(cur) >= last {
				return int64(cur) - last
			}
			return int64(cur) // 재부팅으로 카운터가 0부터 다시 시작했다
		}
		dRx, dTx := delta(rx, lastRx), delta(tx, lastTx)
		elapsed := now.Unix() - lastAt
		if elapsed < 1 {
			elapsed = 1
		}
		if dRx > maxLinkBytesPerSec*elapsed || dTx > maxLinkBytesPerSec*elapsed {
			return nil
		}
		if dRx == 0 && dTx == 0 {
			return nil
		}
		_, err = t.q.ExecContext(ctx, `INSERT INTO node_traffic (node_id, month, rx_bytes, tx_bytes) VALUES (?,?,?,?)
			ON CONFLICT(node_id, month) DO UPDATE SET rx_bytes=rx_bytes+excluded.rx_bytes, tx_bytes=tx_bytes+excluded.tx_bytes`,
			nodeID, TrafficMonth(now), dRx, dTx)
		return err
	})
}

// NodeTrafficFor는 노드의 그 달 트래픽이다(기록이 없으면 0).
func (s *Store) NodeTrafficFor(ctx context.Context, nodeID, month string) (NodeTraffic, error) {
	t := NodeTraffic{Month: month}
	err := s.q.QueryRowContext(ctx, `SELECT rx_bytes, tx_bytes, alerted_pct FROM node_traffic WHERE node_id=? AND month=?`, NormNode(nodeID), month).
		Scan(&t.RxBytes, &t.TxBytes, &t.AlertedPct)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return t, err
}

// HasNodeTrafficCounter는 이 노드의 카운터를 한 번이라도 받았는지다(받지 못했으면 트래픽을 "모름"으로 보여 준다).
func (s *Store) HasNodeTrafficCounter(ctx context.Context, nodeID string) bool {
	var one int
	return s.q.QueryRowContext(ctx, `SELECT 1 FROM node_traffic_counters WHERE node_id=?`, NormNode(nodeID)).Scan(&one) == nil
}

// MarkNodeTrafficAlerted는 그 달에 pct 단계 알림을 보냈다고 기록한다. 이미 그 이상을 보냈으면 false다(중복 알림 방지).
func (s *Store) MarkNodeTrafficAlerted(ctx context.Context, nodeID, month string, pct int) (bool, error) {
	res, err := s.q.ExecContext(ctx, `INSERT INTO node_traffic (node_id, month, alerted_pct) VALUES (?,?,?)
		ON CONFLICT(node_id, month) DO UPDATE SET alerted_pct=excluded.alerted_pct WHERE node_traffic.alerted_pct < excluded.alerted_pct`,
		NormNode(nodeID), month, pct)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ProjectsOnNode는 이 노드를 주 노드로 쓰는 인스턴스가 있는 프로젝트들이다(노드 알림을 보낼 곳).
func (s *Store) ProjectsOnNode(ctx context.Context, nodeID string) ([]string, error) {
	ids := []string{NormNode(nodeID)}
	if ids[0] == LocalNodeID {
		ids = append(ids, "") // 아주 예전 인스턴스는 node_id가 비어 있을 수 있다
	}
	rows, err := s.q.QueryContext(ctx, `SELECT DISTINCT s.project_id FROM instances i JOIN services s ON s.id = i.service_id
		WHERE i.node_id IN (?, ?) ORDER BY s.project_id`, ids[0], ids[len(ids)-1])
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
