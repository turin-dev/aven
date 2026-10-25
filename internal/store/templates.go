package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// ---------------- 사용자 템플릿(마켓플레이스) ----------------

// 템플릿 공개 범위
const (
	TemplatePrivate   = "private"   // 만든 사람(과 작업 공간 admin)만
	TemplateWorkspace = "workspace" // 작업 공간의 모든 사용자
	TemplatePublic    = "public"    // 작업 공간의 모든 사용자 + 공개 표시(다른 설치로 나눌 수 있는 템플릿)
)

// UserTemplate은 사용자가 올린 템플릿이다. Definition은 검증된 정의 JSON이다(생성 함수는 계산하지 않은 그대로).
type UserTemplate struct {
	ID          string          `json:"id"`
	WorkspaceID string          `json:"workspace_id"`
	Key         string          `json:"key"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Readme      string          `json:"readme"`
	Icon        string          `json:"icon"`
	Category    string          `json:"category"`
	Tags        []string        `json:"tags"`
	Definition  json.RawMessage `json:"definition"`
	Visibility  string          `json:"visibility"`
	OwnerUserID string          `json:"owner_user_id"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	DeployCount int             `json:"deploy_count"`
}

const templateCols = `id, key, name, description, readme, icon, category, tags, definition, visibility, owner_user_id, created_at, updated_at, deploy_count, workspace_id`

func scanTemplate(r scanner) (*UserTemplate, error) {
	var t UserTemplate
	var tags, def, created, updated string
	if err := r.Scan(&t.ID, &t.Key, &t.Name, &t.Description, &t.Readme, &t.Icon, &t.Category, &tags, &def, &t.Visibility,
		&t.OwnerUserID, &created, &updated, &t.DeployCount, &t.WorkspaceID); err != nil {
		return nil, notFound(err)
	}
	json.Unmarshal([]byte(tags), &t.Tags)
	if t.Tags == nil {
		t.Tags = []string{}
	}
	t.Definition = json.RawMessage(def)
	t.CreatedAt, t.UpdatedAt = parseTS(created), parseTS(updated)
	return &t, nil
}

func (s *Store) CreateTemplate(ctx context.Context, t *UserTemplate) error {
	if t.WorkspaceID == "" {
		t.WorkspaceID = DefaultWorkspaceID
	}
	t.ID = NewID("tpl")
	t.CreatedAt = time.Now().UTC()
	t.UpdatedAt = t.CreatedAt
	if t.Tags == nil {
		t.Tags = []string{}
	}
	_, err := s.q.ExecContext(ctx, `INSERT INTO templates (`+templateCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,0,?)`,
		t.ID, t.Key, t.Name, t.Description, t.Readme, t.Icon, t.Category, jsonStr(t.Tags), string(t.Definition), t.Visibility,
		t.OwnerUserID, ts(t.CreatedAt), ts(t.UpdatedAt), t.WorkspaceID)
	return conflict(err)
}

func (s *Store) GetTemplateByKey(ctx context.Context, key string) (*UserTemplate, error) {
	return scanTemplate(s.q.QueryRowContext(ctx, `SELECT `+templateCols+` FROM templates WHERE key=?`, key))
}

// ListTemplates는 모든 사용자 템플릿이다(공개 범위는 호출자가 거른다).
func (s *Store) ListTemplates(ctx context.Context) ([]*UserTemplate, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+templateCols+` FROM templates ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*UserTemplate{}
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) UpdateTemplate(ctx context.Context, t *UserTemplate) error {
	t.UpdatedAt = time.Now().UTC()
	if t.Tags == nil {
		t.Tags = []string{}
	}
	return affected(s.q.ExecContext(ctx, `UPDATE templates SET name=?, description=?, readme=?, icon=?, category=?, tags=?, definition=?, visibility=?, updated_at=? WHERE id=?`,
		t.Name, t.Description, t.Readme, t.Icon, t.Category, jsonStr(t.Tags), string(t.Definition), t.Visibility, ts(t.UpdatedAt), t.ID))
}

func (s *Store) DeleteTemplate(ctx context.Context, id string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM templates WHERE id=?`, id))
}

// CountTemplateDeploy는 배포 횟수를 하나 올린다. 사용자 템플릿이 아니면(내장) 별도 집계 테이블에 올린다.
func (s *Store) CountTemplateDeploy(ctx context.Context, key string, builtin bool) error {
	if builtin {
		_, err := s.q.ExecContext(ctx, `INSERT INTO builtin_template_stats (key, deploy_count) VALUES (?, 1)
			ON CONFLICT(key) DO UPDATE SET deploy_count = deploy_count + 1`, key)
		return err
	}
	_, err := s.q.ExecContext(ctx, `UPDATE templates SET deploy_count = deploy_count + 1 WHERE key=?`, key)
	return err
}

// BuiltinTemplateDeploys는 내장 템플릿의 배포 횟수다.
func (s *Store) BuiltinTemplateDeploys(ctx context.Context) (map[string]int, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT key, deploy_count FROM builtin_template_stats`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n sql.NullInt64
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = int(n.Int64)
	}
	return out, rows.Err()
}
