package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

// 프로젝트 관측 대시보드: 위젯(지표 차트, 로그, HTTP 로그, 이벤트)을 격자에 배치한다.

const (
	MaxDashboardsPerProject = 50
	MaxDashboardWidgets     = 30
	DashboardColumns        = 12
	maxWidgetRows           = 8
	maxWidgetTitleLen       = 100
)

// WidgetTypes는 위젯 종류다. metric: 지표 차트, logs: 환경 로그, http: 서비스 HTTP 로그, events: 프로젝트 이벤트.
var WidgetTypes = []string{"metric", "logs", "http", "events"}

// WidgetMetrics는 metric 위젯이 그릴 수 있는 지표다(지표 API의 점 필드).
var WidgetMetrics = []string{"cpu_percent", "memory_bytes", "net_rx_bytes", "net_tx_bytes", "disk_bytes"}

// WidgetRanges는 지표 API의 기간이다.
var WidgetRanges = []string{"1h", "6h", "24h", "7d"}

// Widget은 대시보드의 칸 하나다. x, y는 격자 위치(정렬 순서), w는 열 수(1~12), h는 행 수(1~8)다.
type Widget struct {
	ID            string  `json:"id"`
	Type          string  `json:"type"`
	Title         string  `json:"title"`
	ServiceID     *string `json:"service_id,omitempty"`
	EnvironmentID *string `json:"environment_id,omitempty"`
	Metric        string  `json:"metric,omitempty"`
	Range         string  `json:"range"`
	X             int     `json:"x"`
	Y             int     `json:"y"`
	W             int     `json:"w"`
	H             int     `json:"h"`
}

type Dashboard struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	Widgets   []Widget  `json:"widgets"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func dashOneOf(v string, list []string) bool {
	for _, x := range list {
		if v == x {
			return true
		}
	}
	return false
}

func dashTrimID(p *string) *string {
	if p == nil || strings.TrimSpace(*p) == "" {
		return nil
	}
	v := strings.TrimSpace(*p)
	return &v
}

// Normalize는 대시보드와 위젯을 다듬고 검증한다. 위젯 ID가 없거나 겹치면 새로 만든다.
// 서비스/환경이 이 프로젝트 것인지는 API가 확인한다.
func (d *Dashboard) Normalize() error {
	d.Name = strings.TrimSpace(d.Name)
	if d.Name == "" {
		return layoutInvalid("name is required")
	}
	if utf8.RuneCountInString(d.Name) > maxGroupNameLen || strings.ContainsAny(d.Name, "\r\n\t") {
		return layoutInvalid("name is too long (max 60 chars)")
	}
	if d.Widgets == nil {
		d.Widgets = []Widget{}
	}
	if len(d.Widgets) > MaxDashboardWidgets {
		return layoutInvalid("too many widgets (max 30)")
	}
	seen := map[string]bool{}
	for i := range d.Widgets {
		w := &d.Widgets[i]
		if w.ID == "" || seen[w.ID] || len(w.ID) > 64 {
			w.ID = NewID("wdg")
		}
		seen[w.ID] = true
		if !dashOneOf(w.Type, WidgetTypes) {
			return layoutInvalid("widget type must be one of metric, logs, http, events")
		}
		w.Title = strings.TrimSpace(w.Title)
		if utf8.RuneCountInString(w.Title) > maxWidgetTitleLen || strings.ContainsAny(w.Title, "\r\n\t") {
			return layoutInvalid("widget title is too long (max 100 chars)")
		}
		w.ServiceID, w.EnvironmentID = dashTrimID(w.ServiceID), dashTrimID(w.EnvironmentID)
		if w.Range == "" {
			w.Range = "6h"
		}
		if !dashOneOf(w.Range, WidgetRanges) {
			return layoutInvalid("widget range must be one of 1h, 6h, 24h, 7d")
		}
		switch w.Type {
		case "metric":
			if !dashOneOf(w.Metric, WidgetMetrics) {
				return layoutInvalid("metric widget needs metric: one of " + strings.Join(WidgetMetrics, ", "))
			}
			if w.EnvironmentID == nil {
				return layoutInvalid("metric widget needs environment_id")
			}
		case "logs":
			if w.EnvironmentID == nil {
				return layoutInvalid("logs widget needs environment_id")
			}
			w.Metric = ""
		case "http":
			if w.EnvironmentID == nil || w.ServiceID == nil {
				return layoutInvalid("http widget needs service_id and environment_id")
			}
			w.Metric = ""
		case "events":
			w.Metric = ""
		}
		if w.W == 0 {
			w.W = 6
		}
		if w.H == 0 {
			w.H = 3
		}
		if w.X < 0 || w.Y < 0 || w.X >= DashboardColumns || w.Y > 10000 {
			return layoutInvalid("widget x must be 0-11 and y non-negative")
		}
		if w.W < 1 || w.W > DashboardColumns || w.H < 1 || w.H > maxWidgetRows {
			return layoutInvalid("widget w must be 1-12 and h 1-8")
		}
	}
	return nil
}

const dashCols = `id, project_id, name, widgets, created_at, updated_at`

func scanDashboard(r scanner) (*Dashboard, error) {
	var d Dashboard
	var widgets, created, updated string
	if err := r.Scan(&d.ID, &d.ProjectID, &d.Name, &widgets, &created, &updated); err != nil {
		return nil, notFound(err)
	}
	json.Unmarshal([]byte(widgets), &d.Widgets)
	if d.Widgets == nil {
		d.Widgets = []Widget{}
	}
	d.CreatedAt, d.UpdatedAt = parseTS(created), parseTS(updated)
	return &d, nil
}

func (s *Store) CreateDashboard(ctx context.Context, d *Dashboard) error {
	if err := d.Normalize(); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *Store) error {
		var n int
		if err := tx.q.QueryRowContext(ctx, `SELECT count(*) FROM dashboards WHERE project_id=?`, d.ProjectID).Scan(&n); err != nil {
			return err
		}
		if n >= MaxDashboardsPerProject {
			return layoutInvalid("too many dashboards in this project (max 50)")
		}
		d.ID = NewID("dsh")
		d.CreatedAt = time.Now().UTC()
		d.UpdatedAt = d.CreatedAt
		_, err := tx.q.ExecContext(ctx, `INSERT INTO dashboards (`+dashCols+`) VALUES (?,?,?,?,?,?)`,
			d.ID, d.ProjectID, d.Name, jsonStr(d.Widgets), ts(d.CreatedAt), ts(d.UpdatedAt))
		return err
	})
}

func (s *Store) UpdateDashboard(ctx context.Context, d *Dashboard) error {
	if err := d.Normalize(); err != nil {
		return err
	}
	d.UpdatedAt = time.Now().UTC()
	return affected(s.q.ExecContext(ctx, `UPDATE dashboards SET name=?, widgets=?, updated_at=? WHERE id=?`, d.Name, jsonStr(d.Widgets), ts(d.UpdatedAt), d.ID))
}

func (s *Store) GetDashboard(ctx context.Context, id string) (*Dashboard, error) {
	return scanDashboard(s.q.QueryRowContext(ctx, `SELECT `+dashCols+` FROM dashboards WHERE id=?`, id))
}

func (s *Store) DeleteDashboard(ctx context.Context, id string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM dashboards WHERE id=?`, id))
}

// ListDashboards는 프로젝트의 대시보드를 만든 순서로 돌려준다.
func (s *Store) ListDashboards(ctx context.Context, projectID string) ([]*Dashboard, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+dashCols+` FROM dashboards WHERE project_id=? ORDER BY created_at, id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Dashboard{}
	for rows.Next() {
		d, err := scanDashboard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
