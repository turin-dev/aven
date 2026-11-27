package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"time"
)

type HyperVHost struct {
	NodeID      string         `json:"node_id"`
	VMName      string         `json:"vm_name"`
	HostName    string         `json:"host_name"`
	PowerState  string         `json:"power_state"`
	LastSeenAt  *time.Time     `json:"last_seen_at"`
	LastCommand *HyperVCommand `json:"last_command,omitempty"`
}

type HyperVCommand struct {
	ID        string    `json:"id"`
	NodeID    string    `json:"node_id"`
	Action    string    `json:"action"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Store) CreateHyperVPairToken(ctx context.Context, nodeID, tokenHash, vmName string, expiry time.Time) error {
	if nodeID == LocalNodeID {
		return errors.New("local node cannot use a Hyper-V host")
	}
	return s.Tx(ctx, func(tx *Store) error {
		var exists int
		if err := tx.q.QueryRowContext(ctx, `SELECT count(*) FROM nodes WHERE id=?`, nodeID).Scan(&exists); err != nil {
			return err
		}
		if exists != 1 {
			return ErrNotFound
		}
		if _, err := tx.q.ExecContext(ctx, `DELETE FROM hyperv_pair_tokens WHERE expires_at<=?`, ts(time.Now())); err != nil {
			return err
		}
		if _, err := tx.q.ExecContext(ctx, `DELETE FROM hyperv_pair_tokens WHERE node_id=?`, nodeID); err != nil {
			return err
		}
		_, err := tx.q.ExecContext(ctx, `INSERT INTO hyperv_pair_tokens(token_hash,node_id,vm_name,expires_at) VALUES(?,?,?,?)`, tokenHash, nodeID, vmName, ts(expiry))
		return err
	})
}

// ConsumeHyperVPairToken swaps a short-lived pairing token for a long-lived, node-scoped host credential.
func (s *Store) ConsumeHyperVPairToken(ctx context.Context, tokenHash, credentialHash, hostName string) (string, string, error) {
	var nodeID string
	var vmName string
	err := s.Tx(ctx, func(tx *Store) error {
		var expires string
		if err := tx.q.QueryRowContext(ctx, `SELECT node_id,vm_name,expires_at FROM hyperv_pair_tokens WHERE token_hash=?`, tokenHash).Scan(&nodeID, &vmName, &expires); err != nil {
			return ErrNotFound
		}
		if !parseTS(expires).After(time.Now().UTC()) {
			return ErrNotFound
		}
		if _, err := tx.q.ExecContext(ctx, `DELETE FROM hyperv_pair_tokens WHERE token_hash=?`, tokenHash); err != nil {
			return err
		}
		now := ts(time.Now())
		_, err := tx.q.ExecContext(ctx, `INSERT INTO node_hyperv_hosts(node_id,credential_hash,vm_name,host_name,power_state,last_seen_at)
			VALUES(?,?,?,?, 'unknown', NULL)
			ON CONFLICT(node_id) DO UPDATE SET credential_hash=excluded.credential_hash,vm_name=excluded.vm_name,host_name=excluded.host_name,power_state='unknown',last_seen_at=NULL`,
			nodeID, credentialHash, vmName, hostName)
		if err != nil {
			return err
		}
		_, err = tx.q.ExecContext(ctx, `UPDATE hyperv_commands SET status='failed',error='Hyper-V host was paired again',updated_at=? WHERE node_id=? AND status IN ('queued','running')`, now, nodeID)
		return err
	})
	return nodeID, vmName, err
}

func (s *Store) HyperVNodeIDForCredential(ctx context.Context, credentialHash string) (string, error) {
	var stored, nodeID string
	if err := s.q.QueryRowContext(ctx, `SELECT credential_hash,node_id FROM node_hyperv_hosts WHERE credential_hash=?`, credentialHash).Scan(&stored, &nodeID); err != nil {
		return "", ErrNotFound
	}
	if len(stored) != len(credentialHash) || subtle.ConstantTimeCompare([]byte(stored), []byte(credentialHash)) != 1 {
		return "", ErrNotFound
	}
	return nodeID, nil
}

func (s *Store) SetHyperVHostStatus(ctx context.Context, nodeID, powerState string) error {
	if powerState != "running" && powerState != "off" && powerState != "paused" && powerState != "unknown" {
		return errors.New("invalid Hyper-V power state")
	}
	return affected(s.q.ExecContext(ctx, `UPDATE node_hyperv_hosts SET power_state=?,last_seen_at=? WHERE node_id=?`, powerState, ts(time.Now()), nodeID))
}

const (
	// hyperVQueuedMaxAge is how long a queued command may wait for the host to claim it.
	hyperVQueuedMaxAge = 120 * time.Second
	// hyperVRunningMaxAge is how long a claimed command may stay running before it is failed.
	hyperVRunningMaxAge = 5 * time.Minute
)

// expireStaleHyperVCommands fails queued commands older than hyperVQueuedMaxAge and running
// commands that have not finished within hyperVRunningMaxAge. An empty nodeID covers every node.
// Callers that hand a command to the host must do this first so a stale command is never claimed.
func expireStaleHyperVCommands(ctx context.Context, q querier, nodeID string, now time.Time) error {
	nowTS := ts(now)
	queued := `UPDATE hyperv_commands SET status='failed', error='Hyper-V command expired before the host claimed it', updated_at=? WHERE status='queued' AND created_at<?`
	running := `UPDATE hyperv_commands SET status='failed', error='Hyper-V host did not finish the command', updated_at=? WHERE status='running' AND updated_at<?`
	queuedArgs := []any{nowTS, ts(now.Add(-hyperVQueuedMaxAge))}
	runningArgs := []any{nowTS, ts(now.Add(-hyperVRunningMaxAge))}
	if nodeID != "" {
		queued += ` AND node_id=?`
		running += ` AND node_id=?`
		queuedArgs = append(queuedArgs, nodeID)
		runningArgs = append(runningArgs, nodeID)
	}
	if _, err := q.ExecContext(ctx, queued, queuedArgs...); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, running, runningArgs...)
	return err
}

func (s *Store) ListHyperVHosts(ctx context.Context) (map[string]HyperVHost, error) {
	if err := expireStaleHyperVCommands(ctx, s.q, "", time.Now().UTC()); err != nil {
		return nil, err
	}
	rows, err := s.q.QueryContext(ctx, `SELECT h.node_id,h.vm_name,h.host_name,h.power_state,h.last_seen_at,
		c.id,c.action,c.status,c.error,c.created_at,c.updated_at
		FROM node_hyperv_hosts h LEFT JOIN hyperv_commands c ON c.id=(SELECT id FROM hyperv_commands WHERE node_id=h.node_id ORDER BY created_at DESC LIMIT 1)
		ORDER BY h.node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]HyperVHost{}
	for rows.Next() {
		var h HyperVHost
		var seen, commandID, action, status, commandError, commandCreated, commandUpdated sql.NullString
		if err := rows.Scan(&h.NodeID, &h.VMName, &h.HostName, &h.PowerState, &seen,
			&commandID, &action, &status, &commandError, &commandCreated, &commandUpdated); err != nil {
			return nil, err
		}
		if seen.Valid {
			t := parseTS(seen.String)
			h.LastSeenAt = &t
		}
		if commandID.Valid {
			h.LastCommand = &HyperVCommand{ID: commandID.String, NodeID: h.NodeID, Action: action.String, Status: status.String, Error: commandError.String, CreatedAt: parseTS(commandCreated.String), UpdatedAt: parseTS(commandUpdated.String)}
		}
		out[h.NodeID] = h
	}
	return out, rows.Err()
}

func (s *Store) EnqueueHyperVCommand(ctx context.Context, nodeID, action string) (*HyperVCommand, error) {
	if action != "start" && action != "stop" {
		return nil, errors.New("action must be start or stop")
	}
	cmd := &HyperVCommand{ID: NewID("hvc"), NodeID: nodeID, Action: action, Status: "queued", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	err := s.Tx(ctx, func(tx *Store) error {
		var host int
		if err := tx.q.QueryRowContext(ctx, `SELECT count(*) FROM node_hyperv_hosts WHERE node_id=?`, nodeID).Scan(&host); err != nil {
			return err
		}
		if host != 1 {
			return ErrNotFound
		}
		if err := expireStaleHyperVCommands(ctx, tx.q, nodeID, time.Now().UTC()); err != nil {
			return err
		}
		var pending int
		if err := tx.q.QueryRowContext(ctx, `SELECT count(*) FROM hyperv_commands WHERE node_id=? AND status IN ('queued','running')`, nodeID).Scan(&pending); err != nil {
			return err
		}
		if pending != 0 {
			return ErrConflict
		}
		_, err := tx.q.ExecContext(ctx, `INSERT INTO hyperv_commands(id,node_id,action,status,created_at,updated_at) VALUES(?,?,?,'queued',?,?)`,
			cmd.ID, cmd.NodeID, cmd.Action, ts(cmd.CreatedAt), ts(cmd.UpdatedAt))
		return err
	})
	return cmd, err
}

func (s *Store) ClaimHyperVCommand(ctx context.Context, nodeID string) (*HyperVCommand, error) {
	var cmd *HyperVCommand
	var noCommand bool
	err := s.Tx(ctx, func(tx *Store) error {
		now := time.Now().UTC()
		if err := expireStaleHyperVCommands(ctx, tx.q, nodeID, now); err != nil {
			return err
		}
		var id, action, created string
		if err := tx.q.QueryRowContext(ctx, `SELECT id,action,created_at FROM hyperv_commands WHERE node_id=? AND status='queued' ORDER BY created_at LIMIT 1`, nodeID).Scan(&id, &action, &created); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				noCommand = true
				return nil
			}
			return err
		}
		if _, err := tx.q.ExecContext(ctx, `UPDATE hyperv_commands SET status='running',updated_at=? WHERE id=? AND status='queued'`, ts(now), id); err != nil {
			return err
		}
		cmd = &HyperVCommand{ID: id, NodeID: nodeID, Action: action, Status: "running", CreatedAt: parseTS(created), UpdatedAt: now}
		return nil
	})
	if err == nil && noCommand {
		return nil, ErrNotFound
	}
	return cmd, err
}

func (s *Store) CompleteHyperVCommand(ctx context.Context, nodeID, commandID string, ok bool, commandError string) error {
	status := "done"
	if !ok {
		status = "failed"
	}
	if len(commandError) > 2048 {
		commandError = commandError[:2048]
	}
	return affected(s.q.ExecContext(ctx, `UPDATE hyperv_commands SET status=?,error=?,updated_at=? WHERE id=? AND node_id=? AND status='running'`,
		status, commandError, ts(time.Now()), commandID, nodeID))
}

// DeleteHyperVHost revokes the host credential and fails any command still queued or running.
func (s *Store) DeleteHyperVHost(ctx context.Context, nodeID string) error {
	return s.Tx(ctx, func(tx *Store) error {
		now := ts(time.Now())
		if _, err := tx.q.ExecContext(ctx, `UPDATE hyperv_commands SET status='failed', error='Hyper-V host was disconnected', updated_at=? WHERE node_id=? AND status IN ('queued','running')`, now, nodeID); err != nil {
			return err
		}
		if _, err := tx.q.ExecContext(ctx, `DELETE FROM hyperv_pair_tokens WHERE node_id=?`, nodeID); err != nil {
			return err
		}
		res, err := tx.q.ExecContext(ctx, `DELETE FROM node_hyperv_hosts WHERE node_id=?`, nodeID)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}
