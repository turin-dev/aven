package store

import (
	"context"
	"database/sql"
	"time"
)

const (
	SandboxCreating = "creating"
	SandboxRunning  = "running"

	SandboxIsolated = "isolated" // 전용 bridge 네트워크: 외부로만 나간다
	SandboxPrivate  = "private"  // 환경 private network에 붙는다
)

// Sandbox는 프로젝트 환경 하나에 묶인 일회용 컨테이너다.
type Sandbox struct {
	ID            string  `json:"id"`
	ProjectID     string  `json:"project_id"`
	EnvironmentID string  `json:"environment_id"`
	NodeID        string  `json:"node_id"`
	ContainerName string  `json:"container_name"`
	NetworkName   string  `json:"-"`
	NetworkMode   string  `json:"network_mode"`
	Image         string  `json:"image"`
	Status        string  `json:"status"`
	MemoryMB      int     `json:"memory_mb"`
	CPUCores      float64 `json:"cpu_cores"`
	// IdleTimeoutMinutes가 0이면 유휴 상태로 두어도 지우지 않는다(ExpiresAt nil).
	IdleTimeoutMinutes int        `json:"idle_timeout_minutes"`
	ExpiresAt          *time.Time `json:"expires_at"`
	LastActivityAt     time.Time  `json:"last_activity_at"`
	ParentID           *string    `json:"parent_id"`
	CheckpointName     *string    `json:"checkpoint_name"`
	CreatedAt          time.Time  `json:"created_at"`
}

// SandboxCheckpoint는 샌드박스 파일시스템의 이름 붙은 스냅샷이다(노드에 남는 docker 이미지).
type SandboxCheckpoint struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	EnvironmentID string    `json:"environment_id"`
	NodeID        string    `json:"node_id"`
	Name          string    `json:"name"`
	Image         string    `json:"-"`
	CreatedAt     time.Time `json:"created_at"`
}

const sandboxCols = `id, project_id, environment_id, node_id, container_name, network_name, network_mode, image, status,
	memory_mb, cpu_cores, idle_timeout_minutes, expires_at, last_activity_at, parent_id, checkpoint_name, created_at`

// sandboxExpiry는 마지막 활동 시각에서 유휴 제한이 지난 시각이다(제한 없음이면 nil).
func sandboxExpiry(last time.Time, idleMinutes int) *time.Time {
	if idleMinutes <= 0 {
		return nil
	}
	t := last.Add(time.Duration(idleMinutes) * time.Minute)
	return &t
}

func scanSandbox(r scanner) (*Sandbox, error) {
	var b Sandbox
	var expires sql.NullString
	var activity, created string
	if err := r.Scan(&b.ID, &b.ProjectID, &b.EnvironmentID, &b.NodeID, &b.ContainerName, &b.NetworkName, &b.NetworkMode,
		&b.Image, &b.Status, &b.MemoryMB, &b.CPUCores, &b.IdleTimeoutMinutes, &expires, &activity, &b.ParentID,
		&b.CheckpointName, &created); err != nil {
		return nil, notFound(err)
	}
	b.ExpiresAt, b.LastActivityAt, b.CreatedAt = parseNullTS(expires), parseTS(activity), parseTS(created)
	return &b, nil
}

func (s *Store) scanSandboxes(rows *sql.Rows, err error) ([]*Sandbox, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Sandbox{}
	for rows.Next() {
		b, err := scanSandbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// CreateSandbox는 status=creating으로 행을 넣는다. 컨테이너를 띄운 뒤 MarkSandboxRunning을 부른다.
func (s *Store) CreateSandbox(ctx context.Context, b *Sandbox) error {
	return s.withResourceCheck(ctx, b.ProjectID, func(tx *Store, _ ResourceLimits) error { return tx.createSandbox(ctx, b) })
}

func (s *Store) createSandbox(ctx context.Context, b *Sandbox) error {
	if b.ID == "" {
		b.ID = NewID("sbx")
	}
	if b.NetworkMode == "" {
		b.NetworkMode = SandboxIsolated
	}
	b.CreatedAt = time.Now().UTC()
	b.LastActivityAt = b.CreatedAt
	b.ExpiresAt = sandboxExpiry(b.LastActivityAt, b.IdleTimeoutMinutes)
	b.Status = SandboxCreating
	_, err := s.q.ExecContext(ctx, `INSERT INTO sandboxes (`+sandboxCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		b.ID, b.ProjectID, b.EnvironmentID, b.NodeID, b.ContainerName, b.NetworkName, b.NetworkMode, b.Image, b.Status,
		b.MemoryMB, b.CPUCores, b.IdleTimeoutMinutes, nullTS(b.ExpiresAt), ts(b.LastActivityAt), b.ParentID,
		b.CheckpointName, ts(b.CreatedAt))
	return conflict(err)
}

func (s *Store) MarkSandboxRunning(ctx context.Context, id string) error {
	return affected(s.q.ExecContext(ctx, `UPDATE sandboxes SET status=? WHERE id=?`, SandboxRunning, id))
}

func (s *Store) GetSandbox(ctx context.Context, id string) (*Sandbox, error) {
	return scanSandbox(s.q.QueryRowContext(ctx, `SELECT `+sandboxCols+` FROM sandboxes WHERE id=?`, id))
}

func (s *Store) ListSandboxes(ctx context.Context, environmentID string) ([]*Sandbox, error) {
	return s.scanSandboxes(s.q.QueryContext(ctx, `SELECT `+sandboxCols+` FROM sandboxes WHERE environment_id=? ORDER BY created_at DESC, id`, environmentID))
}

// ListAllSandboxes는 모든 환경의 샌드박스다(노드에 남은 고아 컨테이너 정리용).
func (s *Store) ListAllSandboxes(ctx context.Context) ([]*Sandbox, error) {
	return s.scanSandboxes(s.q.QueryContext(ctx, `SELECT `+sandboxCols+` FROM sandboxes ORDER BY created_at`))
}

// ListExpiredSandboxes는 유휴 제한이 now 이전에 지난 샌드박스다.
func (s *Store) ListExpiredSandboxes(ctx context.Context, now time.Time) ([]*Sandbox, error) {
	return s.scanSandboxes(s.q.QueryContext(ctx, `SELECT `+sandboxCols+` FROM sandboxes WHERE expires_at IS NOT NULL AND expires_at<=? ORDER BY expires_at`, ts(now)))
}

// TouchSandbox는 마지막 활동 시각을 now로 바꾸고 유휴 만료 시각을 다시 계산한다.
func (s *Store) TouchSandbox(ctx context.Context, id string, now time.Time) error {
	var idle int
	if err := s.q.QueryRowContext(ctx, `SELECT idle_timeout_minutes FROM sandboxes WHERE id=?`, id).Scan(&idle); err != nil {
		return notFound(err)
	}
	return affected(s.q.ExecContext(ctx, `UPDATE sandboxes SET last_activity_at=?, expires_at=? WHERE id=?`,
		ts(now), nullTS(sandboxExpiry(now, idle)), id))
}

func (s *Store) DeleteSandbox(ctx context.Context, id string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM sandboxes WHERE id=?`, id))
}

const sandboxCheckpointCols = `id, project_id, environment_id, node_id, name, image, created_at`

func scanSandboxCheckpoint(r scanner) (*SandboxCheckpoint, error) {
	var c SandboxCheckpoint
	var created string
	if err := r.Scan(&c.ID, &c.ProjectID, &c.EnvironmentID, &c.NodeID, &c.Name, &c.Image, &created); err != nil {
		return nil, notFound(err)
	}
	c.CreatedAt = parseTS(created)
	return &c, nil
}

// SaveSandboxCheckpoint는 이름이 같은 checkpoint가 있으면 새 캡처로 바꾼다(id는 유지).
// 바뀐 경우 이전 이미지와 노드를 돌려준다(호출자가 지운다).
func (s *Store) SaveSandboxCheckpoint(ctx context.Context, c *SandboxCheckpoint) (oldNode, oldImage string, err error) {
	c.CreatedAt = time.Now().UTC()
	err = s.Tx(ctx, func(tx *Store) error {
		prev, err := scanSandboxCheckpoint(tx.q.QueryRowContext(ctx,
			`SELECT `+sandboxCheckpointCols+` FROM sandbox_checkpoints WHERE environment_id=? AND name=?`, c.EnvironmentID, c.Name))
		switch {
		case err == nil:
			c.ID, oldNode, oldImage = prev.ID, prev.NodeID, prev.Image
			_, err = tx.q.ExecContext(ctx, `UPDATE sandbox_checkpoints SET node_id=?, image=?, created_at=? WHERE id=?`,
				c.NodeID, c.Image, ts(c.CreatedAt), c.ID)
			return conflict(err)
		case err != ErrNotFound:
			return err
		}
		if c.ID == "" {
			c.ID = NewID("scp")
		}
		_, err = tx.q.ExecContext(ctx, `INSERT INTO sandbox_checkpoints (`+sandboxCheckpointCols+`) VALUES (?,?,?,?,?,?,?)`,
			c.ID, c.ProjectID, c.EnvironmentID, c.NodeID, c.Name, c.Image, ts(c.CreatedAt))
		return conflict(err)
	})
	return oldNode, oldImage, err
}

func (s *Store) GetSandboxCheckpoint(ctx context.Context, id string) (*SandboxCheckpoint, error) {
	return scanSandboxCheckpoint(s.q.QueryRowContext(ctx, `SELECT `+sandboxCheckpointCols+` FROM sandbox_checkpoints WHERE id=?`, id))
}

// FindSandboxCheckpoint는 환경의 checkpoint를 id 또는 이름으로 찾는다.
func (s *Store) FindSandboxCheckpoint(ctx context.Context, environmentID, ref string) (*SandboxCheckpoint, error) {
	return scanSandboxCheckpoint(s.q.QueryRowContext(ctx, `SELECT `+sandboxCheckpointCols+` FROM sandbox_checkpoints
		WHERE environment_id=? AND (id=? OR name=?) ORDER BY id=? DESC LIMIT 1`, environmentID, ref, ref, ref))
}

func (s *Store) ListSandboxCheckpoints(ctx context.Context, environmentID string) ([]*SandboxCheckpoint, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+sandboxCheckpointCols+` FROM sandbox_checkpoints WHERE environment_id=? ORDER BY created_at DESC, id`, environmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SandboxCheckpoint{}
	for rows.Next() {
		c, err := scanSandboxCheckpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) RenameSandboxCheckpoint(ctx context.Context, id, name string) error {
	return affected(s.q.ExecContext(ctx, `UPDATE sandbox_checkpoints SET name=? WHERE id=?`, name, id))
}

func (s *Store) DeleteSandboxCheckpoint(ctx context.Context, id string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM sandbox_checkpoints WHERE id=?`, id))
}
