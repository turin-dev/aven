package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/secret"
)

func TestChannelConfigEncryptedAtRest(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	box, err := secret.LoadOrCreate(filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetSecretBox(box)
	ps, _ := s.ListProjects(ctx)
	pid := ps[0].ID
	c := &NotificationChannel{ProjectID: &pid, Kind: ChannelWebhook, Name: "ci", Config: ChannelConfig{URL: "https://hooks.example.com/T0KEN", Secret: "hmac-s3cret"}, Events: []string{"deploy.*"}, Enabled: true}
	if err := s.CreateChannel(ctx, c); err != nil {
		t.Fatal(err)
	}
	var raw string
	s.q.QueryRowContext(ctx, `SELECT config FROM notification_channels WHERE id=?`, c.ID).Scan(&raw)
	if strings.Contains(raw, "T0KEN") || strings.Contains(raw, "hmac-s3cret") || raw == "" {
		t.Errorf("channel config stored in plaintext: %s", raw)
	}
	got, err := s.GetChannel(ctx, c.ID)
	if err != nil || got.Config.URL != c.Config.URL || got.Config.Secret != "hmac-s3cret" || got.Events[0] != "deploy.*" || !got.Enabled {
		t.Errorf("round trip: %+v %v", got, err)
	}
	// 작업 공간 채널은 모든 프로젝트 이벤트를 받고, 꺼진 채널은 빠진다
	ws := &NotificationChannel{Kind: ChannelSlack, Name: "all", Config: ChannelConfig{URL: "https://hooks.slack.com/x"}, Events: []string{"*"}, Enabled: true}
	off := &NotificationChannel{ProjectID: &pid, Kind: ChannelSlack, Name: "off", Config: ChannelConfig{URL: "https://hooks.slack.com/y"}, Enabled: false}
	s.CreateChannel(ctx, ws)
	s.CreateChannel(ctx, off)
	cs, _ := s.ChannelsForProject(ctx, pid)
	if len(cs) != 2 {
		t.Errorf("channels for project: %d", len(cs))
	}
	if l, _ := s.ListChannels(ctx, ""); len(l) != 1 || l[0].ID != ws.ID {
		t.Errorf("workspace channels: %+v", l)
	}
	// 프로젝트를 지우면 프로젝트 채널도 지워진다
	s.DeleteProject(ctx, pid)
	if _, err := s.GetChannel(ctx, c.ID); err != ErrNotFound {
		t.Errorf("channel after project delete: %v", err)
	}
}

func TestUserNotificationPrefsDefault(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ev, err := s.UserNotificationEvents(ctx, BuiltinUserID)
	if err != nil || strings.Join(ev, ",") != strings.Join(DefaultNotificationEvents, ",") {
		t.Errorf("default: %v %v", ev, err)
	}
	s.SetUserNotificationEvents(ctx, BuiltinUserID, []string{})
	if ev, _ := s.UserNotificationEvents(ctx, BuiltinUserID); len(ev) != 0 {
		t.Errorf("empty prefs should stay empty: %v", ev)
	}
	if _, err := s.UserNotificationEvents(ctx, "usr_nope"); err != ErrNotFound {
		t.Errorf("unknown user: %v", err)
	}
}
