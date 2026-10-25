package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// OwnerHold는 서버 owner가 프로젝트나 작업 공간을 정지한 기록이다(악용·과금 대응). 정지 중에는 새 배포(웹훅 포함),
// 재시작, scale, cron 즉시 실행이 막힌다. 사용량 한도 정지(UsageSuspension)와 달리 서버 owner만 풀 수 있다.
type OwnerHold struct {
	Scope     string    `json:"scope"` // project | workspace | service | deploy
	TargetID  string    `json:"target_id"`
	Reason    string    `json:"reason"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	// StoppedInstances는 정지 때 실행 중이어서 멈춘 인스턴스다(재개가 다시 시작한다).
	StoppedInstances []string `json:"stopped_instances"`
}

const (
	HoldProject   = "project"
	HoldWorkspace = "workspace"
	// HoldService는 서비스 하나의 정지다(모든 환경의 인스턴스를 멈추고 배포·재시작을 막는다). target_id는 서비스 ID다.
	HoldService = "service"
	// HoldDeploy는 서비스의 배포 차단이다. 도는 컨테이너는 그대로 두고 새 배포(웹훅·이미지 업데이트 포함)만 막는다.
	HoldDeploy = "deploy"
)

// migrateOwnerTools는 서버 owner 도구(프로젝트·작업 공간 정지, 사용자로 보기)의 테이블이다.
func migrateOwnerTools(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS owner_holds (
			scope TEXT NOT NULL,
			target_id TEXT NOT NULL,
			reason TEXT NOT NULL DEFAULT '',
			created_by TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			stopped_instances TEXT NOT NULL DEFAULT '[]',
			PRIMARY KEY (scope, target_id)
		)`,
		// 대상이 지워지면 정지 기록도 지운다(target_id는 두 테이블 중 하나를 가리켜 외래 키를 걸 수 없다).
		`CREATE TRIGGER IF NOT EXISTS owner_holds_project_deleted AFTER DELETE ON projects
			BEGIN DELETE FROM owner_holds WHERE scope='project' AND target_id=OLD.id; END`,
		`CREATE TRIGGER IF NOT EXISTS owner_holds_workspace_deleted AFTER DELETE ON workspaces
			BEGIN DELETE FROM owner_holds WHERE scope='workspace' AND target_id=OLD.id; END`,
		// 서버 owner의 "사용자로 보기": 그 세션의 요청을 기한까지 user_id의 권한으로 읽기 전용 처리한다.
		`CREATE TABLE IF NOT EXISTS session_view_as (
			session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			until TEXT NOT NULL
		)`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// migrateServiceHolds는 서비스 단위 정지·배포 차단(scope service|deploy)을 서비스와 함께 지우는 트리거다.
func migrateServiceHolds(ctx context.Context, tx *Store) error {
	_, err := tx.q.ExecContext(ctx, `CREATE TRIGGER IF NOT EXISTS owner_holds_service_deleted AFTER DELETE ON services
		BEGIN DELETE FROM owner_holds WHERE scope IN ('service','deploy') AND target_id=OLD.id; END`)
	return err
}

const ownerHoldCols = `scope, target_id, reason, created_by, created_at, stopped_instances`

func scanOwnerHold(r scanner) (*OwnerHold, error) {
	var h OwnerHold
	var at, stopped string
	if err := r.Scan(&h.Scope, &h.TargetID, &h.Reason, &h.CreatedBy, &at, &stopped); err != nil {
		return nil, notFound(err)
	}
	h.CreatedAt = parseTS(at)
	h.StoppedInstances = []string{}
	json.Unmarshal([]byte(stopped), &h.StoppedInstances)
	return &h, nil
}

// SetOwnerHold는 정지 기록을 만들거나 바꾼다.
func (s *Store) SetOwnerHold(ctx context.Context, h *OwnerHold) error {
	if h.StoppedInstances == nil {
		h.StoppedInstances = []string{}
	}
	if h.CreatedAt.IsZero() {
		h.CreatedAt = time.Now().UTC()
	}
	_, err := s.q.ExecContext(ctx, `INSERT INTO owner_holds (`+ownerHoldCols+`) VALUES (?,?,?,?,?,?)
		ON CONFLICT(scope, target_id) DO UPDATE SET reason=excluded.reason, stopped_instances=excluded.stopped_instances`,
		h.Scope, h.TargetID, h.Reason, h.CreatedBy, ts(h.CreatedAt), jsonStr(h.StoppedInstances))
	return err
}

func (s *Store) GetOwnerHold(ctx context.Context, scope, targetID string) (*OwnerHold, error) {
	return scanOwnerHold(s.q.QueryRowContext(ctx, `SELECT `+ownerHoldCols+` FROM owner_holds WHERE scope=? AND target_id=?`, scope, targetID))
}

func (s *Store) DeleteOwnerHold(ctx context.Context, scope, targetID string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM owner_holds WHERE scope=? AND target_id=?`, scope, targetID))
}

func (s *Store) ListOwnerHolds(ctx context.Context) ([]*OwnerHold, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+ownerHoldCols+` FROM owner_holds ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*OwnerHold{}
	for rows.Next() {
		h, err := scanOwnerHold(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ProjectOwnerHold는 프로젝트에 걸린 정지다: 프로젝트 자체의 정지, 없으면 그 작업 공간의 정지(둘 다 없으면 nil).
func (s *Store) ProjectOwnerHold(ctx context.Context, projectID string) (*OwnerHold, error) {
	h, err := scanOwnerHold(s.q.QueryRowContext(ctx, `SELECT `+ownerHoldCols+` FROM owner_holds
		WHERE (scope='project' AND target_id=?) OR (scope='workspace' AND target_id=(SELECT workspace_id FROM projects WHERE id=?))
		ORDER BY CASE scope WHEN 'project' THEN 0 ELSE 1 END LIMIT 1`, projectID, projectID))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return h, err
}

// InstanceOwnerHold는 인스턴스가 속한 프로젝트(또는 작업 공간)나 서비스의 정지다(없으면 nil). 배포·재시작 차단 검사용.
func (s *Store) InstanceOwnerHold(ctx context.Context, instanceID string) (*OwnerHold, error) {
	var pid, sid string
	err := s.q.QueryRowContext(ctx, `SELECT sv.project_id, sv.id FROM instances i JOIN services sv ON sv.id=i.service_id WHERE i.id=?`, instanceID).Scan(&pid, &sid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if h, err := s.ProjectOwnerHold(ctx, pid); err != nil || h != nil {
		return h, err
	}
	return s.optionalHold(ctx, HoldService, sid)
}

// InstanceDeployBlock은 인스턴스 서비스의 배포 차단(HoldDeploy)이다(없으면 nil).
func (s *Store) InstanceDeployBlock(ctx context.Context, instanceID string) (*OwnerHold, error) {
	h, err := scanOwnerHold(s.q.QueryRowContext(ctx, `SELECT `+ownerHoldCols+` FROM owner_holds
		WHERE scope='deploy' AND target_id=(SELECT service_id FROM instances WHERE id=?)`, instanceID))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return h, err
}

// ServiceHold는 서비스 정지, 없으면 배포 차단이다(둘 다 없으면 nil). 멤버에게 보여 줄 표시용.
func (s *Store) ServiceHold(ctx context.Context, serviceID string) (*OwnerHold, error) {
	if h, err := s.optionalHold(ctx, HoldService, serviceID); err != nil || h != nil {
		return h, err
	}
	return s.optionalHold(ctx, HoldDeploy, serviceID)
}

func (s *Store) optionalHold(ctx context.Context, scope, targetID string) (*OwnerHold, error) {
	h, err := s.GetOwnerHold(ctx, scope, targetID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return h, err
}

// ---------------- 사용자로 보기 ----------------

// SetSessionViewAs는 세션을 until까지 userID의 시점으로 보게 한다.
func (s *Store) SetSessionViewAs(ctx context.Context, sessionID, userID string, until time.Time) error {
	_, err := s.q.ExecContext(ctx, `INSERT INTO session_view_as (session_id, user_id, until) VALUES (?,?,?)
		ON CONFLICT(session_id) DO UPDATE SET user_id=excluded.user_id, until=excluded.until`, sessionID, userID, ts(until))
	return err
}

// SessionViewAs는 세션의 보기 대상과 기한이다(없으면 ErrNotFound).
func (s *Store) SessionViewAs(ctx context.Context, sessionID string) (userID string, until time.Time, err error) {
	var at string
	err = s.q.QueryRowContext(ctx, `SELECT user_id, until FROM session_view_as WHERE session_id=?`, sessionID).Scan(&userID, &at)
	if err != nil {
		return "", time.Time{}, notFound(err)
	}
	return userID, parseTS(at), nil
}

func (s *Store) DeleteSessionViewAs(ctx context.Context, sessionID string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM session_view_as WHERE session_id=?`, sessionID))
}
