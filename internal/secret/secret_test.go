package secret

import (
	"errors"
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

// 접두어로 시작하지만 이 키로 열리지 않는 값은 평문이다: 암호화해 저장하고 그대로 되돌려 받아야 한다.
func TestSealPrefixLookingPlaintext(t *testing.T) {
	b, err := NewBox(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	other, _ := NewBox([]byte("0123456789abcdef0123456789abcdef"))
	for _, plain := range []string{
		Prefix,
		Prefix + "not-base64!",
		Prefix + "AAAA",
		other.Seal("from another key"),
	} {
		enc := b.Seal(plain)
		if enc == plain {
			t.Errorf("%q stored unencrypted", plain)
			continue
		}
		got, legacy, err := b.Open(enc)
		if err != nil || legacy || got != plain {
			t.Errorf("round trip %q: got %q legacy=%v err=%v", plain, got, legacy, err)
		}
	}
	// 진짜 암호문은 다시 감싸지 않는다.
	enc := b.Seal("x")
	if b.Seal(enc) != enc {
		t.Error("Seal must keep its own ciphertext")
	}
}

func TestNewBoxRejectsBadKeys(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33} {
		if _, err := NewBox(make([]byte, n)); err == nil {
			t.Errorf("key of %d bytes accepted", n)
		}
	}
	p := filepath.Join(t.TempDir(), "master.key")
	os.WriteFile(p, []byte("c2hvcnQ=\n"), 0o600) // "short"
	if _, err := LoadOrCreate(p); err == nil {
		t.Error("short key file accepted")
	}
}

func TestNoncesUnique(t *testing.T) {
	b, _ := NewBox(make([]byte, 32))
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		enc := b.Seal("same")
		if seen[enc] {
			t.Fatal("ciphertext repeated (nonce reuse)")
		}
		seen[enc] = true
	}
}

// 키 파일은 원자적으로 만든다: 임시 파일이 남지 않고, 이미 있는 키는 덮어쓰지 않는다.
func TestLoadOrCreateAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "master.key")
	a, err := LoadOrCreate(p)
	if err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("leftover files: %v", ents)
	}
	if err := writeKeyFile(p, make([]byte, 32)); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrite existing key: %v", err)
	}
	b, err := LoadOrCreate(p)
	if err != nil {
		t.Fatal(err)
	}
	if pt, _, err := b.Open(a.Seal("v")); err != nil || pt != "v" {
		t.Fatal("key changed after second load")
	}
}
