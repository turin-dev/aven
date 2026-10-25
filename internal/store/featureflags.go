package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

const (
	FeatureFlagBool   = "bool"
	FeatureFlagString = "string"
	FeatureFlagNumber = "number"
	FeatureFlagJSON   = "json"
)

type FeatureFlagRule struct {
	ID    string          `json:"id"`
	When  string          `json:"when"`
	Value json.RawMessage `json:"value"`
}

// FeatureFlag is a project-wide identity with an environment-specific default and targeting rules.
type FeatureFlag struct {
	ID            string            `json:"id"`
	ProjectID     string            `json:"project_id"`
	Name          string            `json:"name"`
	Type          string            `json:"type"`
	EnvironmentID string            `json:"environment_id"`
	DefaultValue  json.RawMessage   `json:"default_value"`
	Rules         []FeatureFlagRule `json:"rules"`
	Configured    bool              `json:"configured"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

func FeatureFlagZeroValue(flagType string) json.RawMessage {
	switch flagType {
	case FeatureFlagBool:
		return json.RawMessage("false")
	case FeatureFlagNumber:
		return json.RawMessage("0")
	case FeatureFlagJSON:
		return json.RawMessage("null")
	default:
		return json.RawMessage(`""`)
	}
}

// featureFlagSelect joins the requested environment's value. Environments created after the flag have no row yet;
// they inherit the default value (not the rules) of the project's default environment, then the type's zero value.
const featureFlagSelect = `SELECT f.id, f.project_id, f.name, f.type, v.environment_id, v.default_value, v.rules, v.updated_at, d.default_value
	FROM feature_flags f
	JOIN projects p ON p.id=f.project_id
	LEFT JOIN feature_flag_values v ON v.flag_id=f.id AND v.environment_id=?
	LEFT JOIN feature_flag_values d ON d.flag_id=f.id AND d.environment_id=p.default_environment_id`

func scanFeatureFlag(row scanner, environmentID string) (*FeatureFlag, error) {
	var f FeatureFlag
	var envID, def, rules, updated, inherited sql.NullString
	if err := row.Scan(&f.ID, &f.ProjectID, &f.Name, &f.Type, &envID, &def, &rules, &updated, &inherited); err != nil {
		return nil, notFound(err)
	}
	f.EnvironmentID = environmentID
	switch {
	case envID.Valid:
		f.Configured = true
		f.DefaultValue = json.RawMessage(def.String)
		f.UpdatedAt = parseTS(updated.String)
	case inherited.Valid:
		f.DefaultValue = json.RawMessage(inherited.String)
	default:
		f.DefaultValue = FeatureFlagZeroValue(f.Type)
	}
	if rules.Valid && rules.String != "" {
		if err := json.Unmarshal([]byte(rules.String), &f.Rules); err != nil {
			return nil, err
		}
	}
	if f.Rules == nil {
		f.Rules = []FeatureFlagRule{}
	}
	return &f, nil
}

func (s *Store) ListFeatureFlags(ctx context.Context, projectID, environmentID string) ([]*FeatureFlag, error) {
	rows, err := s.q.QueryContext(ctx, featureFlagSelect+` WHERE f.project_id=? ORDER BY f.name`, environmentID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*FeatureFlag{}
	for rows.Next() {
		f, err := scanFeatureFlag(rows, environmentID)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) GetFeatureFlag(ctx context.Context, projectID, name, environmentID string) (*FeatureFlag, error) {
	return scanFeatureFlag(s.q.QueryRowContext(ctx, featureFlagSelect+` WHERE f.project_id=? AND f.name=?`, environmentID, projectID, name), environmentID)
}

// CreateFeatureFlag inserts the project-wide identity and seeds all current environments with the initial default.
// Targeting rules are stored only in the selected environment; other environments start with no rules.
func (s *Store) CreateFeatureFlag(ctx context.Context, f *FeatureFlag) error {
	now := time.Now().UTC()
	f.ID, f.UpdatedAt = NewID("flag"), now
	return s.Tx(ctx, func(tx *Store) error {
		var belongs int
		if err := tx.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM environments WHERE id=? AND project_id=?`, f.EnvironmentID, f.ProjectID).Scan(&belongs); err != nil {
			return err
		}
		if belongs == 0 {
			return ErrNotFound
		}
		if _, err := tx.q.ExecContext(ctx, `INSERT INTO feature_flags (id, project_id, name, type, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
			f.ID, f.ProjectID, f.Name, f.Type, ts(now), ts(now)); err != nil {
			return conflict(err)
		}
		rows, err := tx.q.QueryContext(ctx, `SELECT id FROM environments WHERE project_id=?`, f.ProjectID)
		if err != nil {
			return err
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, id := range ids {
			rules := []FeatureFlagRule{}
			if id == f.EnvironmentID {
				rules = f.Rules
			}
			if _, err := tx.q.ExecContext(ctx, `INSERT INTO feature_flag_values (flag_id, environment_id, default_value, rules, updated_at) VALUES (?,?,?,?,?)`,
				f.ID, id, string(f.DefaultValue), jsonStr(rules), ts(now)); err != nil {
				return err
			}
		}
		return nil
	})
}

// UpdateFeatureFlag saves one environment. A forced type change clears rules in all environments and resets their defaults.
func (s *Store) UpdateFeatureFlag(ctx context.Context, projectID, name, environmentID, flagType string, force bool, defaultValue json.RawMessage, rules []FeatureFlagRule) error {
	now := time.Now().UTC()
	return s.Tx(ctx, func(tx *Store) error {
		var id, oldType string
		if err := tx.q.QueryRowContext(ctx, `SELECT id, type FROM feature_flags WHERE project_id=? AND name=?`, projectID, name).Scan(&id, &oldType); err != nil {
			return notFound(err)
		}
		var belongs int
		if err := tx.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM environments WHERE id=? AND project_id=?`, environmentID, projectID).Scan(&belongs); err != nil {
			return err
		}
		if belongs == 0 {
			return ErrNotFound
		}
		if oldType != flagType {
			if !force {
				return ErrConflict
			}
			if _, err := tx.q.ExecContext(ctx, `UPDATE feature_flags SET type=?, updated_at=? WHERE id=?`, flagType, ts(now), id); err != nil {
				return err
			}
			if _, err := tx.q.ExecContext(ctx, `DELETE FROM feature_flag_values WHERE flag_id=?`, id); err != nil {
				return err
			}
			rows, err := tx.q.QueryContext(ctx, `SELECT id FROM environments WHERE project_id=?`, projectID)
			if err != nil {
				return err
			}
			ids := []string{}
			for rows.Next() {
				var env string
				if err := rows.Scan(&env); err != nil {
					rows.Close()
					return err
				}
				ids = append(ids, env)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			rows.Close()
			zero := string(FeatureFlagZeroValue(flagType))
			for _, env := range ids {
				if _, err := tx.q.ExecContext(ctx, `INSERT INTO feature_flag_values (flag_id, environment_id, default_value, rules, updated_at) VALUES (?,?,?,?,?)`, id, env, zero, `[]`, ts(now)); err != nil {
					return err
				}
			}
		} else {
			if _, err := tx.q.ExecContext(ctx, `UPDATE feature_flags SET updated_at=? WHERE id=?`, ts(now), id); err != nil {
				return err
			}
		}
		_, err := tx.q.ExecContext(ctx, `INSERT INTO feature_flag_values (flag_id, environment_id, default_value, rules, updated_at) VALUES (?,?,?,?,?)
			ON CONFLICT(flag_id, environment_id) DO UPDATE SET default_value=excluded.default_value, rules=excluded.rules, updated_at=excluded.updated_at`,
			id, environmentID, string(defaultValue), jsonStr(rules), ts(now))
		return err
	})
}

func (s *Store) DeleteFeatureFlag(ctx context.Context, projectID, name string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM feature_flags WHERE project_id=? AND name=?`, projectID, name))
}
