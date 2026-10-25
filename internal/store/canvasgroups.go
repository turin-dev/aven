package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxGroupsPerEnv     = 50
	MaxGroupServices    = 200
	maxGroupNameLen     = 60
	maxCanvasCoordinate = 1e6
)

// GroupColors는 이름으로 고를 수 있는 그룹 색이다(#rrggbb도 받는다).
var GroupColors = []string{"gray", "red", "orange", "yellow", "green", "teal", "blue", "purple", "pink"}

var hexColorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// CanvasRect는 캔버스 좌표의 사각형이다(그룹 틀의 위치와 크기).
type CanvasRect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// CanvasGroup은 환경 캔버스의 서비스 묶음이다. Rect는 서비스가 없을 때 틀을 그릴 자리다(서비스가 있으면 웹이 카드 경계로 그린다).
type CanvasGroup struct {
	ID            string      `json:"id"`
	EnvironmentID string      `json:"environment_id"`
	Name          string      `json:"name"`
	Color         string      `json:"color"`
	ServiceIDs    []string    `json:"service_ids"`
	Rect          *CanvasRect `json:"rect"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
}

// ErrInvalidLayout은 그룹/대시보드 입력 검증 실패다(API는 400으로 돌려준다).
var ErrInvalidLayout = errors.New("invalid input")

type layoutErr struct{ msg string }

func (e *layoutErr) Error() string { return e.msg }
func (e *layoutErr) Unwrap() error { return ErrInvalidLayout }

func layoutInvalid(msg string) error { return &layoutErr{msg} }

func finiteCoord(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && math.Abs(v) <= maxCanvasCoordinate
}

// Normalize는 그룹 필드를 다듬고 검증한다(이름 공백 제거, 색 기본값, 서비스 ID 중복 제거).
func (g *CanvasGroup) Normalize() error {
	g.Name = strings.TrimSpace(g.Name)
	if g.Name == "" {
		return layoutInvalid("name is required")
	}
	if utf8.RuneCountInString(g.Name) > maxGroupNameLen || strings.ContainsAny(g.Name, "\r\n\t") {
		return layoutInvalid("name is too long (max 60 chars)")
	}
	g.Color = strings.TrimSpace(g.Color)
	if g.Color == "" {
		g.Color = "gray"
	}
	ok := hexColorRe.MatchString(g.Color)
	for _, c := range GroupColors {
		ok = ok || g.Color == c
	}
	if !ok {
		return layoutInvalid("color must be one of " + strings.Join(GroupColors, ", ") + " or #rrggbb")
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, id := range g.ServiceIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		if len(id) > 64 {
			return layoutInvalid("service_ids contains an invalid id")
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) > MaxGroupServices {
		return layoutInvalid("too many services in a group (max 200)")
	}
	g.ServiceIDs = ids
	if r := g.Rect; r != nil {
		if !finiteCoord(r.X) || !finiteCoord(r.Y) || !finiteCoord(r.W) || !finiteCoord(r.H) || r.W < 0 || r.H < 0 {
			return layoutInvalid("rect must have finite x, y and non-negative w, h")
		}
		r.X, r.Y, r.W, r.H = math.Round(r.X), math.Round(r.Y), math.Round(r.W), math.Round(r.H)
	}
	return nil
}

// migrateCanvasGroupsDashboards는 캔버스 서비스 그룹과 프로젝트 관측 대시보드를 추가한다.
func migrateCanvasGroupsDashboards(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`CREATE TABLE canvas_groups (
			id TEXT PRIMARY KEY,
			environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			color TEXT NOT NULL DEFAULT 'gray',
			service_ids TEXT NOT NULL DEFAULT '[]',
			rect TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX canvas_groups_env ON canvas_groups(environment_id, created_at)`,
		`CREATE TABLE dashboards (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			widgets TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX dashboards_project ON dashboards(project_id, created_at)`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

const groupCols = `id, environment_id, name, color, service_ids, rect, created_at, updated_at`

func scanGroup(r scanner) (*CanvasGroup, error) {
	var g CanvasGroup
	var ids, created, updated string
	var rect *string
	if err := r.Scan(&g.ID, &g.EnvironmentID, &g.Name, &g.Color, &ids, &rect, &created, &updated); err != nil {
		return nil, notFound(err)
	}
	json.Unmarshal([]byte(ids), &g.ServiceIDs)
	if g.ServiceIDs == nil {
		g.ServiceIDs = []string{}
	}
	if rect != nil && *rect != "" && *rect != "null" {
		var cr CanvasRect
		if json.Unmarshal([]byte(*rect), &cr) == nil {
			g.Rect = &cr
		}
	}
	g.CreatedAt, g.UpdatedAt = parseTS(created), parseTS(updated)
	return &g, nil
}

func rectStr(r *CanvasRect) any {
	if r == nil {
		return nil
	}
	return jsonStr(r)
}

// claimServices는 같은 환경의 다른 그룹에서 ids를 뺀다(서비스는 그룹 하나에만 속한다).
func (s *Store) claimServices(ctx context.Context, envID, keepID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	others, err := s.ListGroups(ctx, envID)
	if err != nil {
		return err
	}
	now := ts(time.Now())
	for _, o := range others {
		if o.ID == keepID {
			continue
		}
		kept := make([]string, 0, len(o.ServiceIDs))
		for _, id := range o.ServiceIDs {
			if !want[id] {
				kept = append(kept, id)
			}
		}
		if len(kept) == len(o.ServiceIDs) {
			continue
		}
		if _, err := s.q.ExecContext(ctx, `UPDATE canvas_groups SET service_ids=?, updated_at=? WHERE id=?`, jsonStr(kept), now, o.ID); err != nil {
			return err
		}
	}
	return nil
}

// CreateGroup은 그룹을 만든다. 다른 그룹에 있던 서비스는 이 그룹으로 옮겨진다.
func (s *Store) CreateGroup(ctx context.Context, g *CanvasGroup) error {
	if err := g.Normalize(); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *Store) error {
		var n int
		if err := tx.q.QueryRowContext(ctx, `SELECT count(*) FROM canvas_groups WHERE environment_id=?`, g.EnvironmentID).Scan(&n); err != nil {
			return err
		}
		if n >= MaxGroupsPerEnv {
			return layoutInvalid("too many groups in this environment (max 50)")
		}
		if err := tx.claimServices(ctx, g.EnvironmentID, "", g.ServiceIDs); err != nil {
			return err
		}
		g.ID = NewID("grp")
		g.CreatedAt = time.Now().UTC()
		g.UpdatedAt = g.CreatedAt
		_, err := tx.q.ExecContext(ctx, `INSERT INTO canvas_groups (`+groupCols+`) VALUES (?,?,?,?,?,?,?,?)`,
			g.ID, g.EnvironmentID, g.Name, g.Color, jsonStr(g.ServiceIDs), rectStr(g.Rect), ts(g.CreatedAt), ts(g.UpdatedAt))
		return err
	})
}

// UpdateGroup은 이름, 색, 서비스, 위치를 저장한다. 새로 넣은 서비스는 다른 그룹에서 빠진다.
func (s *Store) UpdateGroup(ctx context.Context, g *CanvasGroup) error {
	if err := g.Normalize(); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *Store) error {
		if err := tx.claimServices(ctx, g.EnvironmentID, g.ID, g.ServiceIDs); err != nil {
			return err
		}
		g.UpdatedAt = time.Now().UTC()
		return affected(tx.q.ExecContext(ctx, `UPDATE canvas_groups SET name=?, color=?, service_ids=?, rect=?, updated_at=? WHERE id=?`,
			g.Name, g.Color, jsonStr(g.ServiceIDs), rectStr(g.Rect), ts(g.UpdatedAt), g.ID))
	})
}

func (s *Store) GetGroup(ctx context.Context, id string) (*CanvasGroup, error) {
	return scanGroup(s.q.QueryRowContext(ctx, `SELECT `+groupCols+` FROM canvas_groups WHERE id=?`, id))
}

func (s *Store) DeleteGroup(ctx context.Context, id string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM canvas_groups WHERE id=?`, id))
}

// ListGroups는 환경의 그룹을 만든 순서로 돌려준다.
func (s *Store) ListGroups(ctx context.Context, envID string) ([]*CanvasGroup, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+groupCols+` FROM canvas_groups WHERE environment_id=? ORDER BY created_at, id`, envID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*CanvasGroup{}
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
