package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type AgentModelSettings struct {
	Endpoint string `json:"endpoint"`
	Model    string `json:"model"`
	APIKey   string `json:"api_key"`
}

// AgentModelSettings returns the workspace model configuration with its key decrypted for server use.
func (s *Store) AgentModelSettings(ctx context.Context) (AgentModelSettings, error) {
	raw, err := s.GetSetting(ctx, "agent_model")
	if errors.Is(err, ErrNotFound) {
		return AgentModelSettings{}, nil
	}
	if err != nil {
		return AgentModelSettings{}, err
	}
	var cfg AgentModelSettings
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return AgentModelSettings{}, err
	}
	cfg.APIKey, err = s.Unseal(cfg.APIKey)
	return cfg, err
}

func (s *Store) SetAgentModelSettings(ctx context.Context, cfg AgentModelSettings) error {
	cfg.APIKey = s.Seal(cfg.APIKey)
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return s.SetSetting(ctx, "agent_model", string(b))
}

type AgentThread struct {
	ID            string    `json:"id"`
	UserID        string    `json:"-"`
	ProjectID     string    `json:"project_id,omitempty"`
	Title         string    `json:"title"`
	Messages      string    `json:"-"`
	PendingCallID string    `json:"-"`
	PendingAction string    `json:"-"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (s *Store) CreateAgentThread(ctx context.Context, t *AgentThread) error {
	if t.ID == "" {
		t.ID = NewID("agt")
	}
	now := time.Now().UTC()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	if t.Messages == "" {
		t.Messages = "[]"
	}
	_, err := s.q.ExecContext(ctx, `INSERT INTO agent_threads (id,user_id,project_id,title,messages,pending_call_id,pending_action,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, t.ID, t.UserID, t.ProjectID, t.Title, t.Messages, t.PendingCallID, t.PendingAction, ts(t.CreatedAt), ts(t.UpdatedAt))
	return conflict(err)
}

func scanAgentThread(row scanner) (*AgentThread, error) {
	var t AgentThread
	var created, updated string
	if err := row.Scan(&t.ID, &t.UserID, &t.ProjectID, &t.Title, &t.Messages, &t.PendingCallID, &t.PendingAction, &created, &updated); err != nil {
		return nil, notFound(err)
	}
	t.CreatedAt, t.UpdatedAt = parseTS(created), parseTS(updated)
	return &t, nil
}

const agentThreadCols = `id,user_id,project_id,title,messages,pending_call_id,pending_action,created_at,updated_at`

func (s *Store) GetAgentThread(ctx context.Context, userID, id string) (*AgentThread, error) {
	return scanAgentThread(s.q.QueryRowContext(ctx, `SELECT `+agentThreadCols+` FROM agent_threads WHERE user_id=? AND id=?`, userID, id))
}

func (s *Store) ListAgentThreads(ctx context.Context, userID string, limit int) ([]*AgentThread, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.q.QueryContext(ctx, `SELECT `+agentThreadCols+` FROM agent_threads WHERE user_id=? ORDER BY updated_at DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*AgentThread{}
	for rows.Next() {
		t, err := scanAgentThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) SaveAgentThread(ctx context.Context, t *AgentThread) error {
	t.UpdatedAt = time.Now().UTC()
	return affected(s.q.ExecContext(ctx, `UPDATE agent_threads SET project_id=?,title=?,messages=?,pending_call_id=?,pending_action=?,updated_at=? WHERE id=? AND user_id=?`,
		t.ProjectID, t.Title, t.Messages, t.PendingCallID, t.PendingAction, ts(t.UpdatedAt), t.ID, t.UserID))
}

// ClaimAgentAction atomically consumes a pending action so repeated confirmations cannot run it twice.
func (s *Store) ClaimAgentAction(ctx context.Context, userID, threadID, callID string) (string, error) {
	if userID == "" || threadID == "" || callID == "" {
		return "", ErrNotFound
	}
	var action string
	err := s.Tx(ctx, func(tx *Store) error {
		if err := tx.q.QueryRowContext(ctx, `SELECT pending_action FROM agent_threads WHERE id=? AND user_id=? AND pending_call_id=?`, threadID, userID, callID).Scan(&action); err != nil {
			return notFound(err)
		}
		res, err := tx.q.ExecContext(ctx, `UPDATE agent_threads SET pending_call_id='',pending_action='',updated_at=? WHERE id=? AND user_id=? AND pending_call_id=?`,
			ts(time.Now().UTC()), threadID, userID, callID)
		return affected(res, err)
	})
	return action, err
}

func (s *Store) DeleteAgentThread(ctx context.Context, userID, id string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM agent_threads WHERE user_id=? AND id=?`, userID, id))
}
