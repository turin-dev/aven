package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

var ErrResourceLimit = errors.New("작업 공간 자원 한도를 넘었습니다")

// ResourceLimits bounds allocated capacity, not metered CPU time or disk writes.
// nil is unlimited; zero prevents allocations of that resource.
type ResourceLimits struct {
	CPU      *float64 `json:"cpu_cores"`
	MemoryMB *int64   `json:"memory_mb"`
	VolumeMB *int64   `json:"volume_mb"`
}

type ResourceUsage struct {
	CPU             float64 `json:"cpu_cores"`
	MemoryMB        int64   `json:"memory_mb"`
	VolumeMB        int64   `json:"volume_mb"`
	UnlimitedCPU    int     `json:"unlimited_cpu"`
	UnlimitedMemory int     `json:"unlimited_memory"`
	UnlimitedVolume int     `json:"unlimited_volume"`
	Instances       int     `json:"instances"`
	Sandboxes       int     `json:"sandboxes"`
	Volumes         int     `json:"volumes"`
}

func (l ResourceLimits) Validate() error {
	if l.CPU != nil && (math.IsNaN(*l.CPU) || math.IsInf(*l.CPU, 0) || *l.CPU < 0 || *l.CPU > 65536 || (*l.CPU > 0 && *l.CPU < .01)) {
		return errors.New("CPU 한도는 0 또는 0.01~65536 코어여야 합니다")
	}
	if l.MemoryMB != nil && (*l.MemoryMB < 0 || *l.MemoryMB > 1<<30 || (*l.MemoryMB > 0 && *l.MemoryMB < 6)) {
		return errors.New("메모리 한도는 0 또는 6~1073741824 MiB여야 합니다")
	}
	if l.VolumeMB != nil && (*l.VolumeMB < 0 || *l.VolumeMB > 1<<30) {
		return errors.New("볼륨 할당 한도는 0~1073741824 MiB여야 합니다")
	}
	return nil
}

func migrateResourceLimits(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`CREATE TABLE workspace_resource_limits (
			workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
			cpu_cores REAL CHECK(cpu_cores >= 0),
			memory_mb INTEGER CHECK(memory_mb >= 0),
			volume_mb INTEGER CHECK(volume_mb >= 0)
		)`,
		`ALTER TABLE deployments ADD COLUMN resource_replicas INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) WorkspaceResourceLimits(ctx context.Context, wsID string) (ResourceLimits, error) {
	var l ResourceLimits
	err := s.q.QueryRowContext(ctx, `SELECT l.cpu_cores, l.memory_mb, l.volume_mb FROM workspaces w
		LEFT JOIN workspace_resource_limits l ON l.workspace_id=w.id WHERE w.id=?`, wsID).
		Scan(&l.CPU, &l.MemoryMB, &l.VolumeMB)
	return l, notFound(err)
}

func (s *Store) SetWorkspaceResourceLimits(ctx context.Context, wsID string, l ResourceLimits) error {
	if err := l.Validate(); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *Store) error {
		if _, err := tx.GetWorkspace(ctx, wsID); err != nil {
			return err
		}
		if _, err := tx.q.ExecContext(ctx, `INSERT INTO workspace_resource_limits (workspace_id,cpu_cores,memory_mb,volume_mb)
			VALUES (?,?,?,?) ON CONFLICT(workspace_id) DO UPDATE SET cpu_cores=excluded.cpu_cores,memory_mb=excluded.memory_mb,volume_mb=excluded.volume_mb`, wsID, l.CPU, l.MemoryMB, l.VolumeMB); err != nil {
			return err
		}
		return tx.checkWorkspaceResources(ctx, wsID)
	})
}

type instanceResources struct {
	cpu             float64
	memory          int64
	noCPU, noMemory bool
}

func (r *instanceResources) include(sp Spec, replicas int) {
	n := max(1, sp.Replicas, PlacementTotal(sp.Placement), replicas)
	if sp.Autoscale != nil && sp.Autoscale.Enabled {
		n = max(n, sp.Autoscale.MaxReplicas)
	}
	if sp.CPULimit == nil || *sp.CPULimit <= 0 || math.IsNaN(*sp.CPULimit) || math.IsInf(*sp.CPULimit, 0) {
		r.noCPU = true
	} else {
		r.cpu = math.Max(r.cpu, *sp.CPULimit*float64(n))
	}
	if sp.MemoryLimitMB == nil || *sp.MemoryLimitMB <= 0 {
		r.noMemory = true
	} else {
		r.memory = max(r.memory, int64(*sp.MemoryLimitMB)*int64(n))
	}
}

// Reserve the larger of requested, queued and live capacity for each instance.
// Stopped instances keep their requested capacity. Rolling-deploy overlap and
// build containers are not additional allocations in this steady-state quota.
func (s *Store) WorkspaceResourceUsage(ctx context.Context, wsID string) (ResourceUsage, error) {
	var out ResourceUsage
	byInstance := map[string]*instanceResources{}
	rows, err := s.q.QueryContext(ctx, `SELECT i.id,i.spec FROM instances i JOIN services sv ON sv.id=i.service_id
		JOIN projects p ON p.id=sv.project_id WHERE p.workspace_id=?`, wsID)
	if err != nil {
		return out, err
	}
	err = func() error {
		defer rows.Close()
		for rows.Next() {
			var id, raw string
			if err := rows.Scan(&id, &raw); err != nil {
				return err
			}
			var sp Spec
			if err := json.Unmarshal([]byte(raw), &sp); err != nil {
				return err
			}
			r := &instanceResources{}
			r.include(sp, 0)
			byInstance[id] = r
		}
		return rows.Err()
	}()
	if err != nil {
		return out, err
	}
	rows, err = s.q.QueryContext(ctx, `SELECT d.instance_id,d.spec,d.containers,d.extra_containers,d.resource_replicas
		FROM deployments d JOIN instances i ON i.id=d.instance_id JOIN services sv ON sv.id=i.service_id JOIN projects p ON p.id=sv.project_id
		WHERE p.workspace_id=? AND
		(d.id=i.current_deployment_id OR d.status IN ('waiting','queued','initializing','building','pre_deploy','deploying','healthchecking','live'))`, wsID)
	if err != nil {
		return out, err
	}
	err = func() error {
		defer rows.Close()
		for rows.Next() {
			var id, raw, containers, extra string
			var reserved int
			if err := rows.Scan(&id, &raw, &containers, &extra, &reserved); err != nil {
				return err
			}
			var sp Spec
			var cs []string
			var groups []NodeContainers
			if err := json.Unmarshal([]byte(raw), &sp); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(containers), &cs); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(extra), &groups); err != nil {
				return err
			}
			n := len(cs)
			for _, g := range groups {
				n += len(g.Containers)
			}
			if r := byInstance[id]; r != nil {
				r.include(sp, max(n, reserved))
			}
		}
		return rows.Err()
	}()
	if err != nil {
		return out, err
	}
	for _, r := range byInstance {
		out.CPU += r.cpu
		out.MemoryMB += r.memory
		if r.noCPU {
			out.UnlimitedCPU++
		}
		if r.noMemory {
			out.UnlimitedMemory++
		}
	}
	out.Instances = len(byInstance)
	var sandboxCPU float64
	var sandboxMemory int64
	var noCPU, noMemory int
	err = s.q.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(cpu_cores),0),coalesce(sum(memory_mb),0),
		coalesce(sum(cpu_cores<=0),0),coalesce(sum(memory_mb<=0),0) FROM sandboxes b JOIN projects p ON p.id=b.project_id WHERE p.workspace_id=?`, wsID).
		Scan(&out.Sandboxes, &sandboxCPU, &sandboxMemory, &noCPU, &noMemory)
	if err != nil {
		return out, err
	}
	out.CPU += sandboxCPU
	out.MemoryMB += sandboxMemory
	out.UnlimitedCPU += noCPU
	out.UnlimitedMemory += noMemory
	err = s.q.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(v.size_limit_mb),0),coalesce(sum(v.size_limit_mb IS NULL OR v.size_limit_mb<=0),0)
		FROM volumes v JOIN projects p ON p.id=v.project_id WHERE p.workspace_id=?`, wsID).
		Scan(&out.Volumes, &out.VolumeMB, &out.UnlimitedVolume)
	return out, err
}

func (s *Store) checkWorkspaceResources(ctx context.Context, wsID string) error {
	l, err := s.WorkspaceResourceLimits(ctx, wsID)
	if err != nil {
		return err
	}
	if l.CPU == nil && l.MemoryMB == nil && l.VolumeMB == nil {
		return nil
	}
	u, err := s.WorkspaceResourceUsage(ctx, wsID)
	if err != nil {
		return err
	}
	for _, v := range []struct {
		label     string
		used      float64
		limit     *float64
		unlimited int
	}{
		{"CPU (코어)", u.CPU, l.CPU, u.UnlimitedCPU},
		{"메모리 (MiB)", float64(u.MemoryMB), asFloat(l.MemoryMB), u.UnlimitedMemory},
		{"볼륨 할당량 (MiB)", float64(u.VolumeMB), asFloat(l.VolumeMB), u.UnlimitedVolume},
	} {
		if v.limit == nil {
			continue
		}
		if v.unlimited > 0 {
			return fmt.Errorf("%w: %s 제한이 없는 항목 %d개에 먼저 개별 한도를 설정하고 실행 중인 서비스는 재배포하세요", ErrResourceLimit, v.label, v.unlimited)
		}
		if v.used > *v.limit+1e-8 {
			return fmt.Errorf("%w: %s 할당량 %.4g / 한도 %.4g", ErrResourceLimit, v.label, v.used, *v.limit)
		}
	}
	return nil
}

func asFloat(n *int64) *float64 {
	if n == nil {
		return nil
	}
	f := float64(*n)
	return &f
}

// Every allocation mutation runs in the same SQLite transaction as admission.
// This also covers internal callers (templates, environment copies, CLI, AI).
func (s *Store) withResourceCheck(ctx context.Context, projectID string, f func(*Store, ResourceLimits) error) error {
	if !s.tableExists(ctx, "workspace_resource_limits") {
		return f(s, ResourceLimits{})
	}
	return s.Tx(ctx, func(tx *Store) error {
		var wsID string
		if err := tx.q.QueryRowContext(ctx, `SELECT workspace_id FROM projects WHERE id=?`, projectID).Scan(&wsID); err != nil {
			return notFound(err)
		}
		l, err := tx.WorkspaceResourceLimits(ctx, wsID)
		if err != nil {
			return err
		}
		if err := f(tx, l); err != nil {
			return err
		}
		return tx.checkWorkspaceResources(ctx, wsID)
	})
}

func (s *Store) serviceProjectID(ctx context.Context, serviceID string) (string, error) {
	var id string
	err := s.q.QueryRowContext(ctx, `SELECT project_id FROM services WHERE id=?`, serviceID).Scan(&id)
	return id, notFound(err)
}

// Newly created resources receive finite defaults when the workspace is capped.
// Explicit limits are never silently reduced. Existing resources are unchanged.
func resourceDefaults(sp *Spec, l ResourceLimits) {
	if sp.CPULimit == nil && l.CPU != nil {
		n := math.Min(1, *l.CPU)
		sp.CPULimit = &n
	}
	if sp.MemoryLimitMB == nil && l.MemoryMB != nil {
		n := int(min(512, *l.MemoryMB))
		sp.MemoryLimitMB = &n
	}
}

// PrepareDeploymentResources reserves the final replica plan BEFORE starting
// containers, including config-as-code and autoscaling/manual scaling changes.
// The reservation survives a crash; retrying or removing the deployment releases it.
func (s *Store) PrepareDeploymentResources(ctx context.Context, d *Deployment, replicas int) error {
	pid, err := s.serviceProjectID(ctx, d.ServiceID)
	if err != nil {
		return err
	}
	return s.withResourceCheck(ctx, pid, func(tx *Store, l ResourceLimits) error {
		return affected(tx.q.ExecContext(ctx, `UPDATE deployments SET spec=?,resource_replicas=? WHERE id=?`, jsonStr(d.Spec), replicas, d.ID))
	})
}
