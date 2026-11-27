package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestHyperVCommandsExpireAndUnblockTheNode(t *testing.T) {
	ctx := context.Background()

	t.Run("stale queued stop is not claimed", func(t *testing.T) {
		st, nodeID := hyperVTestHost(t)
		cmd, err := st.EnqueueHyperVCommand(ctx, nodeID, "stop")
		if err != nil {
			t.Fatal(err)
		}
		backdateHyperVCommand(t, st, cmd.ID, 3*time.Minute, 3*time.Minute)
		got, err := st.ClaimHyperVCommand(ctx, nodeID)
		if got != nil || !errors.Is(err, ErrNotFound) {
			t.Fatalf("stale stop delivered: %+v %v", got, err)
		}
		status, msg := hyperVCommandRow(t, st, cmd.ID)
		if status != "failed" || msg != "Hyper-V command expired before the host claimed it" {
			t.Fatalf("stale stop status: %s %q", status, msg)
		}
		if _, err := st.EnqueueHyperVCommand(ctx, nodeID, "start"); err != nil {
			t.Fatalf("node still blocked after queued expiry: %v", err)
		}
	})

	t.Run("enqueue expires a stale command before accepting another", func(t *testing.T) {
		st, nodeID := hyperVTestHost(t)
		cmd, err := st.EnqueueHyperVCommand(ctx, nodeID, "stop")
		if err != nil {
			t.Fatal(err)
		}
		backdateHyperVCommand(t, st, cmd.ID, 3*time.Minute, 3*time.Minute)
		next, err := st.EnqueueHyperVCommand(ctx, nodeID, "start")
		if err != nil {
			t.Fatalf("enqueue did not unblock the node: %v", err)
		}
		status, msg := hyperVCommandRow(t, st, cmd.ID)
		if status != "failed" || msg != "Hyper-V command expired before the host claimed it" {
			t.Fatalf("expired on enqueue: %s %q", status, msg)
		}
		if next.Action != "start" || next.Status != "queued" {
			t.Fatalf("replacement command: %+v", next)
		}
		// A fresh queued command still blocks a second one.
		if _, err := st.EnqueueHyperVCommand(ctx, nodeID, "stop"); !errors.Is(err, ErrConflict) {
			t.Fatalf("fresh command should block the node: %v", err)
		}
		got, err := st.ClaimHyperVCommand(ctx, nodeID)
		if err != nil || got == nil || got.ID != next.ID || got.Action != "start" || got.Status != "running" {
			t.Fatalf("fresh command not claimed: %+v %v", got, err)
		}
	})

	t.Run("running command older than five minutes fails on list and claim", func(t *testing.T) {
		st, nodeID := hyperVTestHost(t)
		cmd, err := st.EnqueueHyperVCommand(ctx, nodeID, "stop")
		if err != nil {
			t.Fatal(err)
		}
		got, err := st.ClaimHyperVCommand(ctx, nodeID)
		if err != nil || got == nil || got.ID != cmd.ID || got.Status != "running" {
			t.Fatalf("claim fresh stop: %+v %v", got, err)
		}
		backdateHyperVCommand(t, st, cmd.ID, time.Minute, 6*time.Minute)
		hosts, err := st.ListHyperVHosts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		last := hosts[nodeID].LastCommand
		if last == nil || last.ID != cmd.ID || last.Status != "failed" || last.Error != "Hyper-V host did not finish the command" {
			t.Fatalf("list did not fail the running command: %+v", last)
		}
		if _, err := st.ClaimHyperVCommand(ctx, nodeID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("claim after running expiry: %v", err)
		}
		if _, err := st.EnqueueHyperVCommand(ctx, nodeID, "start"); err != nil {
			t.Fatalf("node still blocked after running expiry: %v", err)
		}
	})

	t.Run("a command queued for under two minutes is still claimed", func(t *testing.T) {
		st, nodeID := hyperVTestHost(t)
		cmd, err := st.EnqueueHyperVCommand(ctx, nodeID, "stop")
		if err != nil {
			t.Fatal(err)
		}
		backdateHyperVCommand(t, st, cmd.ID, 90*time.Second, 90*time.Second)
		got, err := st.ClaimHyperVCommand(ctx, nodeID)
		if err != nil || got == nil || got.ID != cmd.ID || got.Action != "stop" || got.Status != "running" {
			t.Fatalf("recent stop not delivered: %+v %v", got, err)
		}
	})
}

func hyperVTestHost(t *testing.T) (*Store, string) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "hyperv.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	id := NewID("nod")
	now := ts(time.Now())
	if _, err := st.q.ExecContext(context.Background(), `INSERT INTO nodes (id, name, schedulable, labels, created_at, credential_hash) VALUES (?,?,1,'{}',?,'')`, id, "region", now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.q.ExecContext(context.Background(), `INSERT INTO node_hyperv_hosts (node_id, credential_hash, vm_name, host_name, power_state, last_seen_at) VALUES (?,?,?,?, 'running', ?)`,
		id, "credhashcredhashcredhashcredhashcredhash12", "AvenRegion", "WIN-HV01", now); err != nil {
		t.Fatal(err)
	}
	return st, id
}

func backdateHyperVCommand(t *testing.T, st *Store, id string, createdAge, updatedAge time.Duration) {
	t.Helper()
	now := time.Now()
	if _, err := st.q.ExecContext(context.Background(), `UPDATE hyperv_commands SET created_at=?, updated_at=? WHERE id=?`, ts(now.Add(-createdAge)), ts(now.Add(-updatedAge)), id); err != nil {
		t.Fatal(err)
	}
}

func hyperVCommandRow(t *testing.T, st *Store, id string) (string, string) {
	t.Helper()
	var status, msg string
	if err := st.q.QueryRowContext(context.Background(), `SELECT status, error FROM hyperv_commands WHERE id=?`, id).Scan(&status, &msg); err != nil {
		t.Fatal(err)
	}
	return status, msg
}
