package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTripAndKeyFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "master.key")
	b, err := LoadOrCreate(p)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Errorf("key mode %v", st.Mode().Perm())
	}
	enc := b.Seal("hunter2")
	if !strings.HasPrefix(enc, Prefix) || strings.Contains(enc, "hunter2") {
		t.Fatalf("not encrypted: %s", enc)
	}
	if b.Seal(enc) != enc {
		t.Error("Seal must be idempotent on ciphertext")
	}
	if b.Seal("hunter2") == enc {
		t.Error("nonce must be random")
	}
	// 같은 파일에서 다시 로드해도 복호화된다.
	b2, err := LoadOrCreate(p)
	if err != nil {
		t.Fatal(err)
	}
	pt, legacy, err := b2.Open(enc)
	if err != nil || legacy || pt != "hunter2" {
		t.Fatalf("open: %q %v %v", pt, legacy, err)
	}
	if pt, legacy, _ := b2.Open("plain"); !legacy || pt != "plain" {
		t.Error("plaintext should pass through as legacy")
	}
	// 변조 / 다른 키
	if _, _, err := b2.Open(enc[:len(enc)-3] + "AAA"); err == nil {
		t.Error("tampered ciphertext must fail")
	}
	other, _ := LoadOrCreate(filepath.Join(t.TempDir(), "k"))
	if _, _, err := other.Open(enc); err == nil {
		t.Error("wrong key must fail")
	}
	// 느슨한 권한은 고친다.
	os.Chmod(p, 0o644)
	if _, err := LoadOrCreate(p); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Error("perm not tightened")
	}
}
