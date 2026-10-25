package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// ComposeSpec은 compose 스택에서 가져온 서비스의 런타임 설정 중 aven Spec에 대응 필드가 없는 것들이다.
type ComposeSpec struct {
	Name       string            `json:"name"`                  // compose 서비스 이름
	Entrypoint []string          `json:"entrypoint,omitempty"`  // compose entrypoint (argv)
	Command    []string          `json:"command,omitempty"`     // compose command (argv)
	User       string            `json:"user,omitempty"`        // docker --user
	WorkingDir string            `json:"working_dir,omitempty"` // docker --workdir
	Aliases    []string          `json:"aliases,omitempty"`     // 환경 네트워크에서 추가로 풀리는 이름(compose 서비스 이름 등)
	Labels     map[string]string `json:"labels,omitempty"`      // 컨테이너 라벨(aven 라벨은 덮지 못한다)
	BuildArgs  map[string]string `json:"build_args,omitempty"`  // compose build.args
	Target     string            `json:"build_target,omitempty"`
	BindMounts []BindMount       `json:"bind_mounts,omitempty"` // 명시적으로 허용한 bind mount(로컬 노드 전용)
	DependsOn  []Dependency      `json:"depends_on,omitempty"`
	EnvKeys    []string          `json:"env_keys,omitempty"` // compose가 넣은 변수 키(다시 가져올 때 제거 판정용)
}

type BindMount struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}

// Dependency는 depends_on 항목이다. Condition: started | healthy | completed.
type Dependency struct {
	Service   string `json:"service"` // compose 서비스 이름
	Condition string `json:"condition"`
}

// Stack은 프로젝트 안에서 compose 파일 하나에서 만든 서비스 묶음이다.
type Stack struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// StackEnv는 스택을 환경에 마지막으로 가져온 원본 정보다.
type StackEnv struct {
	StackID       string          `json:"stack_id"`
	EnvironmentID string          `json:"environment_id"`
	Source        json.RawMessage `json:"source"`
	AppliedAt     time.Time       `json:"applied_at"`
}

// migrateV10은 compose 스택을 추가한다.
func migrateV10(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`CREATE TABLE stacks (id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE, name TEXT NOT NULL, created_at TEXT NOT NULL, UNIQUE(project_id, name))`,
		`CREATE TABLE stack_envs (stack_id TEXT NOT NULL REFERENCES stacks(id) ON DELETE CASCADE, environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE, source TEXT NOT NULL, applied_at TEXT NOT NULL, PRIMARY KEY (stack_id, environment_id))`,
		`ALTER TABLE services ADD COLUMN stack_id TEXT`,
		`ALTER TABLE services ADD COLUMN compose_name TEXT`,
		`CREATE INDEX services_stack ON services(stack_id)`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func scanStack(r scanner) (*Stack, error) {
	var st Stack
	var created string
	if err := r.Scan(&st.ID, &st.ProjectID, &st.Name, &created); err != nil {
		return nil, notFound(err)
	}
	st.CreatedAt = parseTS(created)
	return &st, nil
}

func (s *Store) CreateStack(ctx context.Context, st *Stack) error {
	if st.ID == "" {
		st.ID = NewID("stk")
	}
	st.CreatedAt = time.Now().UTC()
	_, err := s.q.ExecContext(ctx, `INSERT INTO stacks (id, project_id, name, created_at) VALUES (?,?,?,?)`, st.ID, st.ProjectID, st.Name, ts(st.CreatedAt))
	return conflict(err)
}

func (s *Store) GetStack(ctx context.Context, id string) (*Stack, error) {
	return scanStack(s.q.QueryRowContext(ctx, `SELECT id, project_id, name, created_at FROM stacks WHERE id=?`, id))
}

func (s *Store) GetStackByName(ctx context.Context, projectID, name string) (*Stack, error) {
	return scanStack(s.q.QueryRowContext(ctx, `SELECT id, project_id, name, created_at FROM stacks WHERE project_id=? AND name=?`, projectID, name))
}

func (s *Store) ListStacks(ctx context.Context, projectID string) ([]*Stack, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT id, project_id, name, created_at FROM stacks WHERE project_id=? ORDER BY created_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Stack{}
	for rows.Next() {
		st, err := scanStack(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) DeleteStack(ctx context.Context, id string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM stacks WHERE id=?`, id))
}

// ListServicesByStack은 스택에 속한 서비스를 만든 순서로 돌려준다.
func (s *Store) ListServicesByStack(ctx context.Context, stackID string) ([]*Service, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+serviceCols+` FROM services WHERE stack_id=? ORDER BY created_at, name`, stackID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Service{}
	for rows.Next() {
		sv, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sv)
	}
	return out, rows.Err()
}

func (s *Store) SetServiceStack(ctx context.Context, serviceID string, stackID, composeName *string) error {
	return affected(s.q.ExecContext(ctx, `UPDATE services SET stack_id=?, compose_name=? WHERE id=?`, stackID, composeName, serviceID))
}

func (s *Store) PutStackEnv(ctx context.Context, e *StackEnv) error {
	e.AppliedAt = time.Now().UTC()
	_, err := s.q.ExecContext(ctx, `INSERT INTO stack_envs (stack_id, environment_id, source, applied_at) VALUES (?,?,?,?)
		ON CONFLICT(stack_id, environment_id) DO UPDATE SET source=excluded.source, applied_at=excluded.applied_at`,
		e.StackID, e.EnvironmentID, string(e.Source), ts(e.AppliedAt))
	return err
}

func (s *Store) GetStackEnv(ctx context.Context, stackID, envID string) (*StackEnv, error) {
	var e StackEnv
	var src, applied string
	err := s.q.QueryRowContext(ctx, `SELECT stack_id, environment_id, source, applied_at FROM stack_envs WHERE stack_id=? AND environment_id=?`, stackID, envID).Scan(&e.StackID, &e.EnvironmentID, &src, &applied)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	e.Source, e.AppliedAt = json.RawMessage(src), parseTS(applied)
	return &e, nil
}
