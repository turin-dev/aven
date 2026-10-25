package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/secret"
)

func TestAgentModelSettingsEncryptAPIKey(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	box, err := secret.LoadOrCreate(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetSecretBox(box)
	want := AgentModelSettings{Endpoint: "https://models.example/v1", Model: "test-model", APIKey: "secret-api-key"}
	if err := s.SetAgentModelSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetSetting(ctx, "agent_model")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, want.APIKey) || !strings.Contains(stored, secret.Prefix) {
		t.Fatalf("API key was not encrypted: %q", stored)
	}
	got, err := s.AgentModelSettings(ctx)
	if err != nil || got != want {
		t.Fatalf("settings = %+v, %v; want %+v", got, err, want)
	}
}

func TestAgentThreadsAreUserScopedAndActionsAreSingleUse(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	thread := &AgentThread{UserID: BuiltinUserID, Title: "status", Messages: `[{"role":"user","content":"status"}]`, PendingCallID: "call_1", PendingAction: `{"call":{"id":"call_1"}}`}
	if err := s.CreateAgentThread(ctx, thread); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAgentThread(ctx, "another-user", thread.ID); err != ErrNotFound {
		t.Fatalf("another user read thread: %v", err)
	}
	action, err := s.ClaimAgentAction(ctx, BuiltinUserID, thread.ID, "call_1")
	if err != nil || action != thread.PendingAction {
		t.Fatalf("claim action = %q, %v", action, err)
	}
	if _, err := s.ClaimAgentAction(ctx, BuiltinUserID, thread.ID, "call_1"); err != ErrNotFound {
		t.Fatalf("claimed action twice: %v", err)
	}
	got, err := s.GetAgentThread(ctx, BuiltinUserID, thread.ID)
	if err != nil || got.PendingCallID != "" || got.PendingAction != "" {
		t.Fatalf("pending action remains: %+v %v", got, err)
	}
}
