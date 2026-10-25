package store

import (
	"context"
	"time"
)

// RecordNodeCert는 aven CA가 노드에 발급한 인증서(serial)를 기록한다. 만료된 기록은 이때 함께 지운다.
func (s *Store) RecordNodeCert(ctx context.Context, serial, nodeID string, notAfter time.Time) error {
	s.q.ExecContext(ctx, `DELETE FROM node_certs WHERE not_after < ?`, ts(time.Now().Add(-24*time.Hour)))
	_, err := s.q.ExecContext(ctx, `INSERT INTO node_certs (serial, node_id, not_after, issued_at) VALUES (?,?,?,?)`, serial, nodeID, ts(notAfter), ts(time.Now()))
	return err
}

// NodeCertActive는 이 인증서가 발급 기록에 있고, 폐기되지 않았고, 노드가 아직 존재하는지 본다(만료 검사는 x509가 한다).
func (s *Store) NodeCertActive(ctx context.Context, serial, nodeID string) bool {
	var n int
	err := s.q.QueryRowContext(ctx, `SELECT count(*) FROM node_certs c JOIN nodes n ON n.id=c.node_id
		WHERE c.serial=? AND c.node_id=? AND c.revoked_at IS NULL AND n.id<>?`, serial, nodeID, LocalNodeID).Scan(&n)
	return err == nil && n == 1
}

// ActiveNodeCertSerials는 노드별로 폐기되지 않고 만료되지 않은 인증서 serial이다(mesh gateway의 상대 확인용, 노드당 최신 8개).
func (s *Store) ActiveNodeCertSerials(ctx context.Context) (map[string][]string, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT c.serial, c.node_id FROM node_certs c JOIN nodes n ON n.id=c.node_id
		WHERE c.revoked_at IS NULL AND c.not_after > ? AND n.id<>? ORDER BY c.issued_at DESC`, ts(time.Now()), LocalNodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var serial, node string
		if err := rows.Scan(&serial, &node); err != nil {
			return nil, err
		}
		if len(out[node]) < 8 {
			out[node] = append(out[node], serial)
		}
	}
	return out, rows.Err()
}

// RevokeNodeCerts는 노드의 모든 인증서를 폐기하고 옛 Bearer 비밀키도 지운다. 폐기된 인증서는 만료 전이어도 거부된다.
func (s *Store) RevokeNodeCerts(ctx context.Context, nodeID string) error {
	return s.Tx(ctx, func(tx *Store) error {
		if _, err := tx.q.ExecContext(ctx, `UPDATE node_certs SET revoked_at=? WHERE node_id=? AND revoked_at IS NULL`, ts(time.Now()), nodeID); err != nil {
			return err
		}
		_, err := tx.q.ExecContext(ctx, `UPDATE nodes SET credential_hash='' WHERE id=?`, nodeID)
		return err
	})
}

// RetireLegacyCredential는 인증서로 첫 연결에 성공한 노드의 옛 Bearer 비밀키를 지운다(그 뒤로 Bearer는 거부).
func (s *Store) RetireLegacyCredential(ctx context.Context, nodeID string) error {
	_, err := s.q.ExecContext(ctx, `UPDATE nodes SET credential_hash='' WHERE id=? AND credential_hash<>''`, nodeID)
	return err
}

// ConsumeJoinTokenForCert는 join token을 소비하고 노드를 만든 뒤 인증서 발급 기록을 같은 트랜잭션에 남긴다.
// issue는 새 노드 ID로 인증서를 발급해 (serial, notAfter)를 돌려준다.
func (s *Store) ConsumeJoinTokenForCert(ctx context.Context, hash string, issue func(nodeID string) (serial string, notAfter time.Time, err error)) (*Node, error) {
	var result *Node
	err := s.Tx(ctx, func(tx *Store) error {
		var name, expires string
		if err := tx.q.QueryRowContext(ctx, `SELECT name, expires_at FROM node_join_tokens WHERE token_hash=?`, hash).Scan(&name, &expires); err != nil {
			return ErrNotFound
		}
		if !parseTS(expires).After(time.Now().UTC()) {
			return ErrNotFound
		}
		if _, err := tx.q.ExecContext(ctx, `DELETE FROM node_join_tokens WHERE token_hash=?`, hash); err != nil {
			return err
		}
		result = &Node{ID: NewID("nod"), Name: name, Status: "offline", Schedulable: true, Labels: map[string]string{}, CreatedAt: time.Now().UTC()}
		serial, notAfter, err := issue(result.ID)
		if err != nil {
			return err
		}
		if _, err := tx.q.ExecContext(ctx, `INSERT INTO nodes (id, name, schedulable, labels, created_at, credential_hash) VALUES (?,?,?,?,?,'')`, result.ID, name, true, "{}", ts(result.CreatedAt)); err != nil {
			return err
		}
		_, err = tx.q.ExecContext(ctx, `INSERT INTO node_certs (serial, node_id, not_after, issued_at) VALUES (?,?,?,?)`, serial, result.ID, ts(notAfter), ts(time.Now()))
		return err
	})
	return result, err
}
