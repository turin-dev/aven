package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// ---------------- Notification channels ----------------

// 알림 채널 종류
const (
	ChannelWebhook = "webhook"
	ChannelSlack   = "slack"
	ChannelDiscord = "discord"
	ChannelEmail   = "email"
)

// ValidChannelKind는 알림 채널 종류인지 본다.
func ValidChannelKind(k string) bool {
	return k == ChannelWebhook || k == ChannelSlack || k == ChannelDiscord || k == ChannelEmail
}

// ChannelConfig는 채널 설정이다. 저장할 때 통째로 암호화한다(URL에 토큰이 들어 있는 경우가 많다).
type ChannelConfig struct {
	URL        string   `json:"url,omitempty"`
	Secret     string   `json:"secret,omitempty"` // HMAC 서명 키(webhook)
	Recipients []string `json:"recipients,omitempty"`
}

// NotificationChannel은 이벤트를 보내는 곳이다. ProjectID가 nil이면 작업 공간 전체 채널이다.
type NotificationChannel struct {
	ID        string        `json:"id"`
	ProjectID *string       `json:"project_id"`
	Kind      string        `json:"kind"`
	Name      string        `json:"name"`
	Config    ChannelConfig `json:"-"`
	Events    []string      `json:"events"`
	Enabled   bool          `json:"enabled"`
	CreatedAt time.Time     `json:"created_at"`
}

const channelCols = `id, project_id, kind, name, config, events, enabled, created_at`

func (s *Store) scanChannel(r scanner) (*NotificationChannel, error) {
	var c NotificationChannel
	var pid sql.NullString
	var cfg, events, created string
	if err := r.Scan(&c.ID, &pid, &c.Kind, &c.Name, &cfg, &events, &c.Enabled, &created); err != nil {
		return nil, notFound(err)
	}
	c.ProjectID = nullStr(pid)
	if cfg != "" {
		plain, err := s.Unseal(cfg)
		if err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(plain), &c.Config)
	}
	json.Unmarshal([]byte(events), &c.Events)
	if c.Events == nil {
		c.Events = []string{}
	}
	c.CreatedAt = parseTS(created)
	return &c, nil
}

func (s *Store) CreateChannel(ctx context.Context, c *NotificationChannel) error {
	c.ID, c.CreatedAt = NewID("nch"), time.Now().UTC()
	if c.Events == nil {
		c.Events = []string{}
	}
	_, err := s.q.ExecContext(ctx, `INSERT INTO notification_channels (`+channelCols+`) VALUES (?,?,?,?,?,?,?,?)`,
		c.ID, c.ProjectID, c.Kind, c.Name, s.Seal(jsonStr(c.Config)), jsonStr(c.Events), c.Enabled, ts(c.CreatedAt))
	return err
}

func (s *Store) UpdateChannel(ctx context.Context, c *NotificationChannel) error {
	return affected(s.q.ExecContext(ctx, `UPDATE notification_channels SET name=?, config=?, events=?, enabled=? WHERE id=?`,
		c.Name, s.Seal(jsonStr(c.Config)), jsonStr(c.Events), c.Enabled, c.ID))
}

func (s *Store) GetChannel(ctx context.Context, id string) (*NotificationChannel, error) {
	return s.scanChannel(s.q.QueryRowContext(ctx, `SELECT `+channelCols+` FROM notification_channels WHERE id=?`, id))
}

func (s *Store) DeleteChannel(ctx context.Context, id string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM notification_channels WHERE id=?`, id))
}

// ListChannels는 프로젝트의 채널(projectID가 ""이면 작업 공간 채널)이다.
func (s *Store) ListChannels(ctx context.Context, projectID string) ([]*NotificationChannel, error) {
	if projectID == "" {
		return s.queryChannels(ctx, `WHERE project_id IS NULL ORDER BY created_at`)
	}
	return s.queryChannels(ctx, `WHERE project_id=? ORDER BY created_at`, projectID)
}

// ChannelsForProject는 프로젝트 이벤트를 받을 켜진 채널이다(프로젝트 채널 + 작업 공간 채널).
func (s *Store) ChannelsForProject(ctx context.Context, projectID string) ([]*NotificationChannel, error) {
	return s.queryChannels(ctx, `WHERE enabled=1 AND (project_id=? OR project_id IS NULL) ORDER BY created_at`, projectID)
}

func (s *Store) queryChannels(ctx context.Context, where string, args ...any) ([]*NotificationChannel, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+channelCols+` FROM notification_channels `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NotificationChannel{}
	for rows.Next() {
		c, err := s.scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Delivery는 채널로 보낸 한 번의 전달 기록이다(채널마다 최근 MaxDeliveries개).
type Delivery struct {
	ID         string    `json:"id"`
	ChannelID  string    `json:"channel_id"`
	EventID    string    `json:"event_id"`
	EventKind  string    `json:"event_kind"`
	Status     string    `json:"status"` // ok | failed
	Attempts   int       `json:"attempts"`
	StatusCode *int      `json:"status_code"`
	Error      string    `json:"error"`
	DurationMS int64     `json:"duration_ms"`
	CreatedAt  time.Time `json:"created_at"`
}

const MaxDeliveries = 100

// AddDelivery는 전달 기록을 남기고 채널마다 최근 MaxDeliveries개만 남긴다.
func (s *Store) AddDelivery(ctx context.Context, d *Delivery) error {
	d.ID, d.CreatedAt = NewID("dlv"), time.Now().UTC()
	return s.Tx(ctx, func(tx *Store) error {
		if _, err := tx.q.ExecContext(ctx, `INSERT INTO notification_deliveries (id, channel_id, event_id, event_kind, status, attempts, status_code, error, duration_ms, created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			d.ID, d.ChannelID, d.EventID, d.EventKind, d.Status, d.Attempts, d.StatusCode, d.Error, d.DurationMS, ts(d.CreatedAt)); err != nil {
			return err
		}
		_, err := tx.q.ExecContext(ctx, `DELETE FROM notification_deliveries WHERE channel_id=? AND id NOT IN
			(SELECT id FROM notification_deliveries WHERE channel_id=? ORDER BY created_at DESC, id DESC LIMIT ?)`, d.ChannelID, d.ChannelID, MaxDeliveries)
		return err
	})
}

func (s *Store) ListDeliveries(ctx context.Context, channelID string) ([]*Delivery, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT id, channel_id, event_id, event_kind, status, attempts, status_code, error, duration_ms, created_at
		FROM notification_deliveries WHERE channel_id=? ORDER BY created_at DESC, id DESC LIMIT ?`, channelID, MaxDeliveries)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Delivery{}
	for rows.Next() {
		var d Delivery
		var code sql.NullInt64
		var created string
		if err := rows.Scan(&d.ID, &d.ChannelID, &d.EventID, &d.EventKind, &d.Status, &d.Attempts, &code, &d.Error, &d.DurationMS, &created); err != nil {
			return nil, err
		}
		if code.Valid {
			v := int(code.Int64)
			d.StatusCode = &v
		}
		d.CreatedAt = parseTS(created)
		out = append(out, &d)
	}
	return out, rows.Err()
}

// ---------------- In-app notifications ----------------

// DefaultNotificationEvents는 구독 설정을 바꾸지 않은 사용자가 받는 이벤트다.
var DefaultNotificationEvents = []string{"deploy.failed", "deploy.crashed", "volume.limit_exceeded", "alert.*"}

// UserNotification은 사용자 알림함의 한 항목이다.
type UserNotification struct {
	ID            string     `json:"id"`
	UserID        string     `json:"-"`
	EventID       string     `json:"event_id"`
	ProjectID     string     `json:"project_id"`
	EnvironmentID *string    `json:"environment_id"`
	ServiceID     *string    `json:"service_id"`
	Kind          string     `json:"kind"`
	Message       string     `json:"message"`
	CreatedAt     time.Time  `json:"created_at"`
	ReadAt        *time.Time `json:"read_at"`
}

// MaxUserNotifications는 사용자마다 남기는 알림 수다.
const MaxUserNotifications = 200

// AddUserNotifications는 이벤트 하나를 여러 사용자 알림함에 넣는다(사용자마다 최근 MaxUserNotifications개).
func (s *Store) AddUserNotifications(ctx context.Context, e *Event, userIDs []string) error {
	if len(userIDs) == 0 {
		return nil
	}
	return s.Tx(ctx, func(tx *Store) error {
		for _, uid := range userIDs {
			if _, err := tx.q.ExecContext(ctx, `INSERT INTO user_notifications (id, user_id, event_id, project_id, environment_id, service_id, kind, message, created_at) VALUES (?,?,?,?,?,?,?,?,?)`,
				NewID("ntf"), uid, e.ID, e.ProjectID, e.EnvironmentID, e.ServiceID, e.Kind, e.Message, ts(e.CreatedAt)); err != nil {
				return err
			}
			if _, err := tx.q.ExecContext(ctx, `DELETE FROM user_notifications WHERE user_id=? AND id NOT IN
				(SELECT id FROM user_notifications WHERE user_id=? ORDER BY created_at DESC, id DESC LIMIT ?)`, uid, uid, MaxUserNotifications); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListUserNotifications는 사용자 알림(최근 순)과 읽지 않은 수다.
func (s *Store) ListUserNotifications(ctx context.Context, userID string, unreadOnly bool, limit int) ([]*UserNotification, int, error) {
	q := `SELECT id, user_id, event_id, project_id, environment_id, service_id, kind, message, created_at, read_at FROM user_notifications WHERE user_id=?`
	if unreadOnly {
		q += ` AND read_at IS NULL`
	}
	rows, err := s.q.QueryContext(ctx, q+` ORDER BY created_at DESC, id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*UserNotification{}
	for rows.Next() {
		var n UserNotification
		var env, svc, read sql.NullString
		var created string
		if err := rows.Scan(&n.ID, &n.UserID, &n.EventID, &n.ProjectID, &env, &svc, &n.Kind, &n.Message, &created, &read); err != nil {
			return nil, 0, err
		}
		n.EnvironmentID, n.ServiceID, n.ReadAt = nullStr(env), nullStr(svc), parseNullTS(read)
		n.CreatedAt = parseTS(created)
		out = append(out, &n)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var unread int
	err = s.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_notifications WHERE user_id=? AND read_at IS NULL`, userID).Scan(&unread)
	return out, unread, err
}

// MarkNotificationsRead는 사용자의 알림을 읽음으로 표시한다(ids가 비면 전부). 바뀐 수를 돌려준다.
func (s *Store) MarkNotificationsRead(ctx context.Context, userID string, ids []string) (int64, error) {
	now := ts(time.Now())
	var res sql.Result
	var err error
	if len(ids) == 0 {
		res, err = s.q.ExecContext(ctx, `UPDATE user_notifications SET read_at=? WHERE user_id=? AND read_at IS NULL`, now, userID)
	} else {
		args := []any{now, userID}
		for _, id := range ids {
			args = append(args, id)
		}
		res, err = s.q.ExecContext(ctx, `UPDATE user_notifications SET read_at=? WHERE user_id=? AND read_at IS NULL AND id IN (?`+strings.Repeat(",?", len(ids)-1)+`)`, args...)
	}
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// UserNotificationEvents는 사용자의 알림 구독 이벤트다(설정하지 않았으면 기본값).
func (s *Store) UserNotificationEvents(ctx context.Context, userID string) ([]string, error) {
	var raw sql.NullString
	if err := s.q.QueryRowContext(ctx, `SELECT notification_events FROM users WHERE id=?`, userID).Scan(&raw); err != nil {
		return nil, notFound(err)
	}
	if !raw.Valid {
		return append([]string(nil), DefaultNotificationEvents...), nil
	}
	out := []string{}
	json.Unmarshal([]byte(raw.String), &out)
	return out, nil
}

// AllUserNotificationEvents는 활성 사용자 → 구독 이벤트다(알림함 배포용).
func (s *Store) AllUserNotificationEvents(ctx context.Context) (map[string][]string, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT id, notification_events FROM users WHERE disabled=0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var id string
		var raw sql.NullString
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		if !raw.Valid {
			out[id] = DefaultNotificationEvents
			continue
		}
		var ev []string
		json.Unmarshal([]byte(raw.String), &ev)
		out[id] = ev
	}
	return out, rows.Err()
}

func (s *Store) SetUserNotificationEvents(ctx context.Context, userID string, events []string) error {
	if events == nil {
		events = []string{}
	}
	return affected(s.q.ExecContext(ctx, `UPDATE users SET notification_events=? WHERE id=?`, jsonStr(events), userID))
}

// ---------------- Alert rules ----------------

// 알림 규칙 상태
const (
	AlertOK      = "ok"
	AlertPending = "pending"
	AlertFiring  = "firing"
)

// AlertRule은 지표가 기준을 for_seconds 동안 넘으면 alert.firing, 돌아오면 alert.resolved를 내는 규칙이다.
type AlertRule struct {
	ID              string     `json:"id"`
	ProjectID       string     `json:"project_id"`
	EnvironmentID   *string    `json:"environment_id"`
	ServiceID       *string    `json:"service_id"`
	Name            string     `json:"name"`
	Metric          string     `json:"metric"`
	Comparator      string     `json:"comparator"`
	Threshold       float64    `json:"threshold"`
	ForSeconds      int        `json:"for_seconds"`
	Enabled         bool       `json:"enabled"`
	LastState       string     `json:"last_state"`
	LastValue       *float64   `json:"last_value"`
	PendingSince    *time.Time `json:"pending_since"`
	LastFiredAt     *time.Time `json:"last_fired_at"`
	LastEvaluatedAt *time.Time `json:"last_evaluated_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

const alertCols = `id, project_id, environment_id, service_id, name, metric, comparator, threshold, for_seconds, enabled,
	last_state, last_value, pending_since, last_fired_at, last_evaluated_at, created_at`

func scanAlert(r scanner) (*AlertRule, error) {
	var a AlertRule
	var env, svc, pending, fired, evald sql.NullString
	var val sql.NullFloat64
	var created string
	if err := r.Scan(&a.ID, &a.ProjectID, &env, &svc, &a.Name, &a.Metric, &a.Comparator, &a.Threshold, &a.ForSeconds, &a.Enabled,
		&a.LastState, &val, &pending, &fired, &evald, &created); err != nil {
		return nil, notFound(err)
	}
	a.EnvironmentID, a.ServiceID = nullStr(env), nullStr(svc)
	if val.Valid {
		a.LastValue = &val.Float64
	}
	a.PendingSince, a.LastFiredAt, a.LastEvaluatedAt = parseNullTS(pending), parseNullTS(fired), parseNullTS(evald)
	a.CreatedAt = parseTS(created)
	return &a, nil
}

func (s *Store) CreateAlertRule(ctx context.Context, a *AlertRule) error {
	a.ID, a.CreatedAt, a.LastState = NewID("alr"), time.Now().UTC(), AlertOK
	_, err := s.q.ExecContext(ctx, `INSERT INTO alert_rules (`+alertCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.ProjectID, a.EnvironmentID, a.ServiceID, a.Name, a.Metric, a.Comparator, a.Threshold, a.ForSeconds, a.Enabled,
		a.LastState, nil, nil, nil, nil, ts(a.CreatedAt))
	return err
}

// UpdateAlertRule은 규칙 정의를 바꾸고 평가 상태를 처음(ok)으로 돌린다.
func (s *Store) UpdateAlertRule(ctx context.Context, a *AlertRule) error {
	a.LastState, a.PendingSince = AlertOK, nil
	return affected(s.q.ExecContext(ctx, `UPDATE alert_rules SET environment_id=?, service_id=?, name=?, metric=?, comparator=?, threshold=?, for_seconds=?, enabled=?,
		last_state=?, pending_since=NULL WHERE id=?`, a.EnvironmentID, a.ServiceID, a.Name, a.Metric, a.Comparator, a.Threshold, a.ForSeconds, a.Enabled, a.LastState, a.ID))
}

// SetAlertState는 평가 결과를 저장한다.
func (s *Store) SetAlertState(ctx context.Context, a *AlertRule) error {
	var val any
	if a.LastValue != nil {
		val = *a.LastValue
	}
	_, err := s.q.ExecContext(ctx, `UPDATE alert_rules SET last_state=?, last_value=?, pending_since=?, last_fired_at=?, last_evaluated_at=? WHERE id=?`,
		a.LastState, val, nullTS(a.PendingSince), nullTS(a.LastFiredAt), nullTS(a.LastEvaluatedAt), a.ID)
	return err
}

func (s *Store) GetAlertRule(ctx context.Context, id string) (*AlertRule, error) {
	return scanAlert(s.q.QueryRowContext(ctx, `SELECT `+alertCols+` FROM alert_rules WHERE id=?`, id))
}

func (s *Store) DeleteAlertRule(ctx context.Context, id string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM alert_rules WHERE id=?`, id))
}

// ListAlertRules는 프로젝트의 규칙이다(projectID가 ""이면 모든 켜진 규칙).
func (s *Store) ListAlertRules(ctx context.Context, projectID string) ([]*AlertRule, error) {
	q, args := `SELECT `+alertCols+` FROM alert_rules WHERE enabled=1 ORDER BY created_at`, []any{}
	if projectID != "" {
		q, args = `SELECT `+alertCols+` FROM alert_rules WHERE project_id=? ORDER BY created_at`, []any{projectID}
	}
	rows, err := s.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*AlertRule{}
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---------------- Usage ----------------

// UsageRow는 서비스·환경의 한 시간 사용량이다. Hour는 그 시간의 시작(unix 초)이다.
type UsageRow struct {
	ProjectID      string  `json:"project_id"`
	EnvironmentID  string  `json:"environment_id"`
	ServiceID      string  `json:"service_id"`
	Hour           int64   `json:"hour"`
	CPUCoreSeconds float64 `json:"cpu_core_seconds"`
	MemGBSeconds   float64 `json:"memory_gb_seconds"`
	EgressBytes    int64   `json:"net_egress_bytes"`
	DiskGBHours    float64 `json:"disk_gb_hours"`
}

// UpsertUsage는 시간별 집계를 넣거나 같은 (서비스, 환경, 시간)을 덮어쓴다(다시 집계해도 결과가 같다).
func (s *Store) UpsertUsage(ctx context.Context, rows []UsageRow) error {
	return s.Tx(ctx, func(tx *Store) error {
		for _, r := range rows {
			if _, err := tx.q.ExecContext(ctx, `INSERT INTO usage_hourly (project_id, environment_id, service_id, hour, cpu_core_seconds, memory_gb_seconds, net_egress_bytes, disk_gb_hours)
				VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(service_id, environment_id, hour) DO UPDATE SET project_id=excluded.project_id,
				cpu_core_seconds=excluded.cpu_core_seconds, memory_gb_seconds=excluded.memory_gb_seconds, net_egress_bytes=excluded.net_egress_bytes, disk_gb_hours=excluded.disk_gb_hours`,
				r.ProjectID, r.EnvironmentID, r.ServiceID, r.Hour, r.CPUCoreSeconds, r.MemGBSeconds, r.EgressBytes, r.DiskGBHours); err != nil {
				return err
			}
		}
		return nil
	})
}

// QueryUsage는 [from, to) 시간의 집계다(projectID가 ""이면 모든 프로젝트).
func (s *Store) QueryUsage(ctx context.Context, projectID string, from, to int64) ([]UsageRow, error) {
	q := `SELECT project_id, environment_id, service_id, hour, cpu_core_seconds, memory_gb_seconds, net_egress_bytes, disk_gb_hours FROM usage_hourly WHERE hour>=? AND hour<?`
	args := []any{from, to}
	if projectID != "" {
		q += ` AND project_id=?`
		args = append(args, projectID)
	}
	rows, err := s.q.QueryContext(ctx, q+` ORDER BY hour`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageRow{}
	for rows.Next() {
		var r UsageRow
		if err := rows.Scan(&r.ProjectID, &r.EnvironmentID, &r.ServiceID, &r.Hour, &r.CPUCoreSeconds, &r.MemGBSeconds, &r.EgressBytes, &r.DiskGBHours); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PruneUsage는 before보다 오래된 집계와 지워진 프로젝트의 집계를 지운다.
func (s *Store) PruneUsage(ctx context.Context, before int64) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM usage_hourly WHERE hour < ? OR project_id NOT IN (SELECT id FROM projects)`, before)
	return err
}

// UsageAlertState는 프로젝트의 이번 달 사용량 알림 기록이다(항목 → 이미 알린 최고 비율 80|100).
type UsageAlertState struct {
	Month string         `json:"month"`
	Sent  map[string]int `json:"sent"`
}

func (s *Store) GetUsageAlertState(ctx context.Context, projectID string) (UsageAlertState, error) {
	var raw string
	var st UsageAlertState
	if err := s.q.QueryRowContext(ctx, `SELECT usage_alert_state FROM projects WHERE id=?`, projectID).Scan(&raw); err != nil {
		return st, notFound(err)
	}
	json.Unmarshal([]byte(raw), &st)
	if st.Sent == nil {
		st.Sent = map[string]int{}
	}
	return st, nil
}

func (s *Store) SetUsageAlertState(ctx context.Context, projectID string, st UsageAlertState) error {
	_, err := s.q.ExecContext(ctx, `UPDATE projects SET usage_alert_state=? WHERE id=?`, jsonStr(st), projectID)
	return err
}
