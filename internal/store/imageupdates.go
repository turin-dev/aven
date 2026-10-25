package store

import (
	"context"
	"database/sql"
	"time"
)

func migrateImageUpdates(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`CREATE TABLE image_update_state (
			instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
			last_digest TEXT NOT NULL DEFAULT '',
			last_checked_at TEXT
		)`,
		`CREATE TABLE image_updates (
			id TEXT PRIMARY KEY,
			instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
			current_image TEXT NOT NULL,
			target_image TEXT NOT NULL,
			digest TEXT NOT NULL,
			digest_image TEXT NOT NULL,
			status TEXT NOT NULL,
			automatic INTEGER NOT NULL DEFAULT 0,
			detected_at TEXT NOT NULL,
			started_at TEXT,
			finished_at TEXT,
			deployment_id TEXT NOT NULL DEFAULT '',
			error TEXT
		)`,
		`CREATE INDEX image_updates_instance ON image_updates(instance_id, detected_at DESC)`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

type ImageUpdateState struct {
	InstanceID    string
	LastDigest    string
	LastCheckedAt *time.Time
}

type ImageUpdate struct {
	ID           string     `json:"id"`
	InstanceID   string     `json:"instance_id"`
	CurrentImage string     `json:"current_image"`
	TargetImage  string     `json:"target_image"`
	Digest       string     `json:"digest"`
	DigestImage  string     `json:"digest_image"`
	Status       string     `json:"status"` // available | deploying | applied | failed | skipped | superseded
	Automatic    bool       `json:"automatic"`
	DetectedAt   time.Time  `json:"detected_at"`
	StartedAt    *time.Time `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
	DeploymentID string     `json:"deployment_id,omitempty"`
	Error        *string    `json:"error"`
}

const imageUpdateCols = `id, instance_id, current_image, target_image, digest, digest_image, status, automatic, detected_at, started_at, finished_at, deployment_id, error`

func scanImageUpdate(r scanner) (*ImageUpdate, error) {
	var u ImageUpdate
	var auto int
	var detected string
	var started, finished, errMsg sql.NullString
	if err := r.Scan(&u.ID, &u.InstanceID, &u.CurrentImage, &u.TargetImage, &u.Digest, &u.DigestImage, &u.Status, &auto,
		&detected, &started, &finished, &u.DeploymentID, &errMsg); err != nil {
		return nil, notFound(err)
	}
	u.Automatic, u.DetectedAt = auto != 0, parseTS(detected)
	u.StartedAt, u.FinishedAt, u.Error = parseNullTS(started), parseNullTS(finished), nullStr(errMsg)
	return &u, nil
}

func (s *Store) GetImageUpdateState(ctx context.Context, instanceID string) (*ImageUpdateState, error) {
	var st ImageUpdateState
	var checked sql.NullString
	err := s.q.QueryRowContext(ctx, `SELECT instance_id, last_digest, last_checked_at FROM image_update_state WHERE instance_id=?`, instanceID).
		Scan(&st.InstanceID, &st.LastDigest, &checked)
	if err == sql.ErrNoRows {
		return &ImageUpdateState{InstanceID: instanceID}, nil
	}
	if err != nil {
		return nil, err
	}
	st.LastCheckedAt = parseNullTS(checked)
	return &st, nil
}

func (s *Store) SetImageUpdateState(ctx context.Context, st *ImageUpdateState) error {
	_, err := s.q.ExecContext(ctx, `INSERT INTO image_update_state(instance_id, last_digest, last_checked_at) VALUES(?,?,?)
		ON CONFLICT(instance_id) DO UPDATE SET last_digest=excluded.last_digest, last_checked_at=excluded.last_checked_at`,
		st.InstanceID, st.LastDigest, nullTS(st.LastCheckedAt))
	return err
}

// SaveAvailableImageUpdate creates a history row once per digest. An already seen
// failed/applied update is returned unchanged so the scheduler does not retry it forever.
func (s *Store) SaveAvailableImageUpdate(ctx context.Context, u *ImageUpdate) (*ImageUpdate, error) {
	var out *ImageUpdate
	err := s.Tx(ctx, func(tx *Store) error {
		rows, err := tx.q.QueryContext(ctx, `SELECT `+imageUpdateCols+` FROM image_updates WHERE instance_id=? AND digest=? ORDER BY detected_at DESC LIMIT 1`, u.InstanceID, u.Digest)
		if err != nil {
			return err
		}
		if rows.Next() {
			out, err = scanImageUpdate(rows)
			rows.Close()
			if err != nil {
				return err
			}
			if out.Status == "superseded" {
				_, err = tx.q.ExecContext(ctx, `UPDATE image_updates SET status='available', current_image=?, target_image=?, digest_image=?, detected_at=?, error=NULL WHERE id=?`,
					u.CurrentImage, u.TargetImage, u.DigestImage, ts(time.Now().UTC()), out.ID)
				if err != nil {
					return err
				}
				out.Status, out.CurrentImage, out.TargetImage, out.DigestImage = "available", u.CurrentImage, u.TargetImage, u.DigestImage
			}
			return nil
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if _, err := tx.q.ExecContext(ctx, `UPDATE image_updates SET status='superseded' WHERE instance_id=? AND status='available'`, u.InstanceID); err != nil {
			return err
		}
		u.ID, u.Status, u.DetectedAt = NewID("imgup"), "available", time.Now().UTC()
		_, err = tx.q.ExecContext(ctx, `INSERT INTO image_updates (`+imageUpdateCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			u.ID, u.InstanceID, u.CurrentImage, u.TargetImage, u.Digest, u.DigestImage, u.Status, boolInt(u.Automatic), ts(u.DetectedAt), nil, nil, "", nil)
		if err != nil {
			return err
		}
		copy := *u
		out = &copy
		return nil
	})
	return out, err
}

func (s *Store) GetImageUpdate(ctx context.Context, id string) (*ImageUpdate, error) {
	return scanImageUpdate(s.q.QueryRowContext(ctx, `SELECT `+imageUpdateCols+` FROM image_updates WHERE id=?`, id))
}

func (s *Store) ListImageUpdates(ctx context.Context, instanceID string, limit int) ([]*ImageUpdate, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	rows, err := s.q.QueryContext(ctx, `SELECT `+imageUpdateCols+` FROM image_updates WHERE instance_id=? ORDER BY detected_at DESC, id DESC LIMIT ?`, instanceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ImageUpdate{}
	for rows.Next() {
		u, err := scanImageUpdate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) ListAvailableImageUpdates(ctx context.Context) ([]*ImageUpdate, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+imageUpdateCols+` FROM image_updates WHERE status='available' ORDER BY detected_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ImageUpdate{}
	for rows.Next() {
		u, err := scanImageUpdate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) ClaimImageUpdate(ctx context.Context, id string, automatic bool) (bool, error) {
	from := "'available'"
	if !automatic {
		from += ", 'failed'"
	}
	res, err := s.q.ExecContext(ctx, `UPDATE image_updates SET status='deploying', automatic=?, started_at=?, finished_at=NULL, error=NULL WHERE id=? AND status IN (`+from+`)`,
		boolInt(automatic), ts(time.Now().UTC()), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *Store) SetImageUpdateDeployment(ctx context.Context, id, deploymentID string) error {
	return affected(s.q.ExecContext(ctx, `UPDATE image_updates SET deployment_id=? WHERE id=? AND status='deploying'`, deploymentID, id))
}

// ResetImageUpdate ends a claimed update that never got a running deployment.
// "available" puts it back in the queue; other statuses finish it.
func (s *Store) ResetImageUpdate(ctx context.Context, id, status string, errMsg string) error {
	var msg, finished any
	if errMsg != "" {
		msg = errMsg
	}
	if status != "available" {
		finished = ts(time.Now().UTC())
	}
	_, err := s.q.ExecContext(ctx, `UPDATE image_updates SET status=?, error=?, deployment_id='', finished_at=? WHERE id=? AND status='deploying'`, status, msg, finished, id)
	return err
}

// SkipImageUpdate marks a version as skipped ("이 버전 건너뛰기"). The digest stays
// recorded, so later checks that find the same version do not offer it again.
func (s *Store) SkipImageUpdate(ctx context.Context, id string) error {
	return affected(s.q.ExecContext(ctx, `UPDATE image_updates SET status='skipped', finished_at=? WHERE id=? AND status IN ('available','failed')`, ts(time.Now().UTC()), id))
}

// SupersedeImageUpdate retires an available update whose source image changed.
func (s *Store) SupersedeImageUpdate(ctx context.Context, id string) error {
	_, err := s.q.ExecContext(ctx, `UPDATE image_updates SET status='superseded' WHERE id=? AND status='available'`, id)
	return err
}

// SupersedeImageUpdatesWithDigest retires available updates that a regular deployment already rolled out.
func (s *Store) SupersedeImageUpdatesWithDigest(ctx context.Context, instanceID, digest string) error {
	_, err := s.q.ExecContext(ctx, `UPDATE image_updates SET status='superseded' WHERE instance_id=? AND digest=? AND status='available'`, instanceID, digest)
	return err
}

func (s *Store) CompleteImageUpdate(ctx context.Context, deploymentID, status, errMsg string) (*ImageUpdate, error) {
	var out *ImageUpdate
	err := s.Tx(ctx, func(tx *Store) error {
		var err error
		out, err = scanImageUpdate(tx.q.QueryRowContext(ctx, `SELECT `+imageUpdateCols+` FROM image_updates WHERE deployment_id=? AND status='deploying'`, deploymentID))
		if err == ErrNotFound {
			out = nil
			return nil
		}
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		var msg any
		if errMsg != "" {
			msg = errMsg
		}
		if _, err = tx.q.ExecContext(ctx, `UPDATE image_updates SET status=?, finished_at=?, error=? WHERE id=?`, status, ts(now), msg, out.ID); err != nil {
			return err
		}
		out.Status, out.FinishedAt, out.Error = status, &now, nullStr(sql.NullString{String: errMsg, Valid: errMsg != ""})
		if status == "applied" {
			_, err = tx.q.ExecContext(ctx, `INSERT INTO image_update_state(instance_id, last_digest, last_checked_at) VALUES(?,?,?)
				ON CONFLICT(instance_id) DO UPDATE SET last_digest=excluded.last_digest`, out.InstanceID, out.Digest, ts(now))
		}
		return err
	})
	return out, err
}

// RestoreImageIf changes the configured image back only when it still points at the
// failed update, so a concurrent user edit is preserved.
func (s *Store) RestoreImageIf(ctx context.Context, instanceID, expected, previous string) error {
	return s.Tx(ctx, func(tx *Store) error {
		in, err := tx.GetInstance(ctx, instanceID)
		if err != nil || in.Image == nil || *in.Image != expected {
			return err
		}
		in.Image = &previous
		return tx.UpdateInstanceConfig(ctx, in)
	})
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
