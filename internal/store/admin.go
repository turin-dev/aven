package store

import (
	"context"
	"database/sql"
	"time"
)

// WorkspaceInventory exposes operating counts without project names or config.
type WorkspaceInventory struct {
	Workspace
	Members            int `json:"members"`
	Projects           int `json:"projects"`
	Services           int `json:"services"`
	Instances          int `json:"instances"`
	UnhealthyInstances int `json:"unhealthy_instances"`
}

type InstanceUserCounts struct {
	Total    int `json:"total"`
	Active   int `json:"active"`
	Disabled int `json:"disabled"`
	Admins   int `json:"admins"`
}

type InstanceInventory struct {
	Users          InstanceUserCounts    `json:"users"`
	PendingInvites int                   `json:"pending_invites"`
	Workspaces     []*WorkspaceInventory `json:"workspaces"`
}

func (s *Store) InstanceInventory(ctx context.Context) (*InstanceInventory, error) {
	out := &InstanceInventory{Workspaces: []*WorkspaceInventory{}}
	err := s.Tx(ctx, func(tx *Store) error {
		if err := tx.q.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(disabled=0),0), coalesce(sum(disabled!=0),0),
			coalesce(sum(disabled=0 AND role IN ('owner','admin')),0) FROM users`).
			Scan(&out.Users.Total, &out.Users.Active, &out.Users.Disabled, &out.Users.Admins); err != nil {
			return err
		}
		if err := tx.q.QueryRowContext(ctx, `SELECT count(*) FROM invites WHERE expires_at>?`, ts(time.Now())).Scan(&out.PendingInvites); err != nil {
			return err
		}
		rows, err := tx.q.QueryContext(ctx, `SELECT `+prefixCols("w.", workspaceCols)+`,
			(SELECT count(*) FROM workspace_members m WHERE m.workspace_id=w.id),
			(SELECT count(*) FROM projects p WHERE p.workspace_id=w.id),
			(SELECT count(*) FROM services sv JOIN projects p ON p.id=sv.project_id WHERE p.workspace_id=w.id),
			(SELECT count(*) FROM instances i JOIN services sv ON sv.id=i.service_id JOIN projects p ON p.id=sv.project_id WHERE p.workspace_id=w.id),
			(SELECT count(*) FROM instances i JOIN services sv ON sv.id=i.service_id JOIN projects p ON p.id=sv.project_id WHERE p.workspace_id=w.id AND i.status IN ('failed','crashed'))
			FROM workspaces w ORDER BY w.id!=?, w.personal DESC, w.name COLLATE NOCASE, w.id`, DefaultWorkspaceID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var w WorkspaceInventory
			var owner sql.NullString
			var created string
			if err := rows.Scan(&w.ID, &w.Name, &w.Personal, &owner, &created, &w.Members, &w.Projects, &w.Services, &w.Instances, &w.UnhealthyInstances); err != nil {
				return err
			}
			w.OwnerID, w.CreatedAt, w.Default = nullStr(owner), parseTS(created), w.ID == DefaultWorkspaceID
			out.Workspaces = append(out.Workspaces, &w)
		}
		return rows.Err()
	})
	return out, err
}
