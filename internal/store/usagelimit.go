package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// 프로젝트 월간 사용량 한도(hard limit)와 한도 도달로 인한 정지(suspension) 기록.
// usage_alert(soft, 알림만)와 따로 둔다. 한 프로젝트에 한 행(project_usage_limits)이다.

const (
	UsageLimitStop   = "stop"   // 한도에 닿으면 프로젝트의 실행 중인 서비스를 모두 멈추고 새 배포를 막는다
	UsageLimitNotify = "notify" // 한도에 닿으면 usage.limit_reached만 낸다
)

// UsageLimit은 프로젝트의 달력 월(UTC) 사용량 한도다. 비운 항목은 보지 않는다.
type UsageLimit struct {
	CPUCoreHours  *float64 `json:"cpu_core_hours,omitempty"`
	MemoryGBHours *float64 `json:"memory_gb_hours,omitempty"`
	EgressGB      *float64 `json:"egress_gb,omitempty"`
	Action        string   `json:"action"` // stop | notify
}

// Empty는 한도가 하나도 없는지다.
func (u *UsageLimit) Empty() bool {
	return u == nil || (u.CPUCoreHours == nil && u.MemoryGBHours == nil && u.EgressGB == nil)
}

// UsageSuspension은 한도 도달로 프로젝트를 멈춘 기록이다. Month(YYYY-MM, UTC)가 지나면 효력이 없다.
type UsageSuspension struct {
	Month       string    `json:"month"`
	Reason      string    `json:"reason"`
	SuspendedAt time.Time `json:"suspended_at"`
	Until       time.Time `json:"until"` // 다음 달 1일 00:00 UTC
	// StoppedInstances는 정지 때 실행 중이어서 멈춘 인스턴스다(resume이 다시 시작한다).
	StoppedInstances []string `json:"stopped_instances"`
	// LiftedAt은 정지가 풀린 시각이다(한도를 올리거나 지움, 달이 바뀜). 풀려도 멈춘 서비스는 자동으로 다시 시작하지 않으므로
	// 기록은 resume(멈춘 서비스 다시 시작)까지 남는다.
	LiftedAt *time.Time `json:"lifted_at"`
}

// ActiveAt은 t에 정지가 유효한지다(풀리지 않았고 같은 달 안).
func (s *UsageSuspension) ActiveAt(t time.Time) bool {
	return s != nil && s.LiftedAt == nil && s.Month == t.UTC().Format("2006-01")
}

// ProjectUsageLimit은 project_usage_limits 한 행이다.
type ProjectUsageLimit struct {
	ProjectID  string
	Limit      *UsageLimit
	Suspension *UsageSuspension
	// ReachedMonth/Reached는 이번 달 이미 usage.limit_reached를 낸 항목이다(항목마다 달에 한 번).
	ReachedMonth string
	Reached      []string
}

func migrateUsageLimits(ctx context.Context, tx *Store) error {
	_, err := tx.q.ExecContext(ctx, `CREATE TABLE project_usage_limits (
		project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
		limit_json TEXT NOT NULL DEFAULT '',
		suspension TEXT NOT NULL DEFAULT '',
		reached_month TEXT NOT NULL DEFAULT '',
		reached TEXT NOT NULL DEFAULT '[]'
	)`)
	return err
}

const usageLimitCols = `project_id, limit_json, suspension, reached_month, reached`

func scanUsageLimit(r scanner) (*ProjectUsageLimit, error) {
	var pl ProjectUsageLimit
	var lim, susp, reached string
	if err := r.Scan(&pl.ProjectID, &lim, &susp, &pl.ReachedMonth, &reached); err != nil {
		return nil, notFound(err)
	}
	if lim != "" {
		var u UsageLimit
		if json.Unmarshal([]byte(lim), &u) == nil && !u.Empty() {
			pl.Limit = &u
		}
	}
	if susp != "" {
		var s UsageSuspension
		if json.Unmarshal([]byte(susp), &s) == nil && s.Month != "" {
			pl.Suspension = &s
		}
	}
	json.Unmarshal([]byte(reached), &pl.Reached)
	return &pl, nil
}

// GetUsageLimit은 프로젝트의 한도 행이다. 행이 없으면 빈 값(에러 아님).
func (s *Store) GetUsageLimit(ctx context.Context, projectID string) (*ProjectUsageLimit, error) {
	pl, err := scanUsageLimit(s.q.QueryRowContext(ctx, `SELECT `+usageLimitCols+` FROM project_usage_limits WHERE project_id=?`, projectID))
	if err == ErrNotFound {
		return &ProjectUsageLimit{ProjectID: projectID}, nil
	}
	return pl, err
}

// ListUsageLimits는 한도나 정지 기록이 있는 모든 프로젝트의 행이다.
func (s *Store) ListUsageLimits(ctx context.Context) ([]*ProjectUsageLimit, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+usageLimitCols+` FROM project_usage_limits WHERE limit_json<>'' OR suspension<>'' ORDER BY project_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ProjectUsageLimit
	for rows.Next() {
		pl, err := scanUsageLimit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, pl)
	}
	return out, rows.Err()
}

// SetUsageLimit은 한도를 바꾼다(nil이나 빈 값이면 끈다). 정지 기록은 그대로 둔다.
func (s *Store) SetUsageLimit(ctx context.Context, projectID string, u *UsageLimit) error {
	v := ""
	if !u.Empty() {
		v = jsonStr(u)
	}
	_, err := s.q.ExecContext(ctx, `INSERT INTO project_usage_limits (project_id, limit_json) VALUES (?,?)
		ON CONFLICT(project_id) DO UPDATE SET limit_json=excluded.limit_json`, projectID, v)
	return err
}

// SetUsageSuspension은 정지 기록을 바꾼다(nil이면 지운다).
func (s *Store) SetUsageSuspension(ctx context.Context, projectID string, sp *UsageSuspension) error {
	v := ""
	if sp != nil {
		if sp.StoppedInstances == nil {
			sp.StoppedInstances = []string{}
		}
		v = jsonStr(sp)
	}
	_, err := s.q.ExecContext(ctx, `INSERT INTO project_usage_limits (project_id, suspension) VALUES (?,?)
		ON CONFLICT(project_id) DO UPDATE SET suspension=excluded.suspension`, projectID, v)
	return err
}

// SetUsageLimitReached는 이번 달 이미 알린 한도 항목을 기록한다.
func (s *Store) SetUsageLimitReached(ctx context.Context, projectID, month string, reached []string) error {
	if reached == nil {
		reached = []string{}
	}
	_, err := s.q.ExecContext(ctx, `INSERT INTO project_usage_limits (project_id, reached_month, reached) VALUES (?,?,?)
		ON CONFLICT(project_id) DO UPDATE SET reached_month=excluded.reached_month, reached=excluded.reached`, projectID, month, jsonStr(reached))
	return err
}

// SuspensionForInstance는 인스턴스가 속한 프로젝트의 정지 기록이다(없으면 nil). 배포 차단 검사용.
func (s *Store) SuspensionForInstance(ctx context.Context, instanceID string) (*UsageSuspension, error) {
	var raw string
	err := s.q.QueryRowContext(ctx, `SELECT l.suspension FROM project_usage_limits l
		JOIN services sv ON sv.project_id = l.project_id JOIN instances i ON i.service_id = sv.id WHERE i.id=?`, instanceID).Scan(&raw)
	if err == sql.ErrNoRows || (err == nil && raw == "") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sp UsageSuspension
	if json.Unmarshal([]byte(raw), &sp) != nil || sp.Month == "" {
		return nil, nil
	}
	return &sp, nil
}

// ---------------- 자동 확장 상태 ----------------

// AutoscaleRec은 stabilization window의 추천 값 하나다.
type AutoscaleRec struct {
	At      time.Time `json:"at"`
	Desired int       `json:"desired"`
}

// AutoscaleStateRow는 인스턴스의 자동 확장 기록(cooldown, 추천 값, 표시용 상태)이다. 재시작해도 남는다.
type AutoscaleStateRow struct {
	InstanceID string
	CfgSig     string
	LastScale  *time.Time
	Recs       []AutoscaleRec
	Status     json.RawMessage // deploy.AutoscaleStatus JSON(표시용)
}

func migrateAutoscaleState(ctx context.Context, tx *Store) error {
	_, err := tx.q.ExecContext(ctx, `CREATE TABLE autoscale_state (
		instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
		cfg_sig TEXT NOT NULL DEFAULT '',
		last_scale_at TEXT,
		recs TEXT NOT NULL DEFAULT '[]',
		status TEXT NOT NULL DEFAULT '{}',
		updated_at TEXT NOT NULL
	)`)
	return err
}

// GetAutoscaleState는 인스턴스의 저장된 자동 확장 기록이다(없으면 ErrNotFound).
func (s *Store) GetAutoscaleState(ctx context.Context, instanceID string) (*AutoscaleStateRow, error) {
	var r AutoscaleStateRow
	var last sql.NullString
	var recs, status string
	if err := s.q.QueryRowContext(ctx, `SELECT instance_id, cfg_sig, last_scale_at, recs, status FROM autoscale_state WHERE instance_id=?`, instanceID).
		Scan(&r.InstanceID, &r.CfgSig, &last, &recs, &status); err != nil {
		return nil, notFound(err)
	}
	r.LastScale = parseNullTS(last)
	json.Unmarshal([]byte(recs), &r.Recs)
	r.Status = json.RawMessage(status)
	return &r, nil
}

// PutAutoscaleState는 자동 확장 기록을 저장한다. 인스턴스가 이미 지워졌으면 아무것도 하지 않는다.
func (s *Store) PutAutoscaleState(ctx context.Context, r *AutoscaleStateRow) error {
	recs := r.Recs
	if recs == nil {
		recs = []AutoscaleRec{}
	}
	status := string(r.Status)
	if status == "" {
		status = "{}"
	}
	_, err := s.q.ExecContext(ctx, `INSERT INTO autoscale_state (instance_id, cfg_sig, last_scale_at, recs, status, updated_at)
		SELECT ?,?,?,?,?,? WHERE EXISTS (SELECT 1 FROM instances WHERE id=?)
		ON CONFLICT(instance_id) DO UPDATE SET cfg_sig=excluded.cfg_sig, last_scale_at=excluded.last_scale_at, recs=excluded.recs,
		status=excluded.status, updated_at=excluded.updated_at`,
		r.InstanceID, r.CfgSig, nullTS(r.LastScale), jsonStr(recs), status, ts(time.Now()), r.InstanceID)
	return err
}

// DeleteAutoscaleState는 자동 확장 기록을 지운다(자동 확장을 끈 인스턴스).
func (s *Store) DeleteAutoscaleState(ctx context.Context, instanceID string) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM autoscale_state WHERE instance_id=?`, instanceID)
	return err
}
