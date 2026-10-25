package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/secret"
)

func TestSecretsEncryptedAtRest(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ps, _ := s.ListProjects(ctx)
	pid := ps[0].ID

	// 키가 생기기 전에 평문으로 저장된 registry 비밀번호는 EncryptLegacySecrets가 암호화한다.
	if err := s.SetRegistries(ctx, pid, []Registry{{Host: "ghcr.io", Username: "u", Password: "plain-pass"}}); err != nil {
		t.Fatal(err)
	}
	raw := func() string {
		var r string
		s.q.QueryRowContext(ctx, `SELECT registries FROM projects WHERE id=?`, pid).Scan(&r)
		return r
	}
	if !strings.Contains(raw(), "plain-pass") {
		t.Fatal("precondition: plaintext before key")
	}
	box, err := secret.LoadOrCreate(filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetSecretBox(box)
	if err := s.EncryptLegacySecrets(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw(), "plain-pass") || !strings.Contains(raw(), secret.Prefix) {
		t.Fatalf("not encrypted: %s", raw())
	}
	regs, err := s.GetRegistries(ctx, pid)
	if err != nil || regs[0].Password != "plain-pass" {
		t.Fatalf("decrypt: %+v %v", regs, err)
	}
	// 다시 저장해도 이중 암호화되지 않는다.
	if err := s.SetRegistries(ctx, pid, regs); err != nil {
		t.Fatal(err)
	}
	if regs2, _ := s.GetRegistries(ctx, pid); regs2[0].Password != "plain-pass" {
		t.Error("double encryption")
	}

	// S3 비밀, deploy key 개인키
	d := &BackupDestination{Name: "s3", Type: DestS3, Endpoint: "http://x", Bucket: "b", AccessKey: "ak", SecretKey: "s3-secret-value"}
	if err := s.CreateBackupDestination(ctx, d); err != nil {
		t.Fatal(err)
	}
	var cfg string
	s.q.QueryRowContext(ctx, `SELECT config FROM backup_destinations WHERE id=?`, d.ID).Scan(&cfg)
	if strings.Contains(cfg, "s3-secret-value") {
		t.Errorf("s3 secret stored in plaintext: %s", cfg)
	}
	got, _ := s.GetBackupDestination(ctx, d.ID)
	if got.SecretKey != "s3-secret-value" {
		t.Error("s3 secret roundtrip")
	}
	if b, _ := jsonMarshal(got); strings.Contains(string(b), "s3-secret-value") {
		t.Error("secret must never be JSON-marshalled")
	}
	if err := s.PutDeployKey(ctx, "svc_1", "PRIVATE-KEY-DATA", "ssh-ed25519 AAA", "SHA256:x"); err != nil {
		t.Fatal(err)
	}
	var stored string
	s.q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='deploykey:svc_1'`).Scan(&stored)
	if strings.Contains(stored, "PRIVATE-KEY-DATA") {
		t.Error("deploy key stored in plaintext")
	}
	k, _ := s.GetDeployKey(ctx, "svc_1")
	if pem, err := s.DeployKeyPrivate(k); err != nil || pem != "PRIVATE-KEY-DATA" {
		t.Errorf("deploy key roundtrip: %q %v", pem, err)
	}
	if b, _ := jsonMarshal(k); strings.Contains(string(b), "PRIVATE") {
		t.Error("private key in JSON")
	}
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
