package agentpki

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestIssueVerifyAndRejects(t *testing.T) {
	ca, err := NewCA()
	if err != nil {
		t.Fatal(err)
	}
	other, _ := NewCA()
	priv, _, _ := GenerateKey()
	pub := priv.Public().(ed25519.PublicKey)
	iss, err := ca.Issue("nod_1", pub)
	if err != nil {
		t.Fatal(err)
	}
	v, err := ca.Verify(iss.CertPEM, time.Now())
	if err != nil || v.NodeID != "nod_1" || v.Serial != iss.Serial || !v.PublicKey.Equal(pub) {
		t.Fatalf("verify: %+v %v", v, err)
	}
	if _, err := other.Verify(iss.CertPEM, time.Now()); err == nil {
		t.Error("certificate from another CA accepted")
	}
	if _, err := ca.Verify(iss.CertPEM, time.Now().Add(CertValidity+time.Hour)); err == nil {
		t.Error("expired certificate accepted")
	}
	if _, err := ca.Verify(iss.CertPEM, time.Now().Add(-time.Hour)); err == nil {
		t.Error("not-yet-valid certificate accepted")
	}
	if _, err := ca.Verify(ca.PEM, time.Now()); err == nil {
		t.Error("the CA certificate itself is accepted as a node certificate")
	}
	if !NeedsRenewal(time.Now().Add(9*24*time.Hour), time.Now()) || NeedsRenewal(time.Now().Add(11*24*time.Hour), time.Now()) {
		t.Error("renewal threshold")
	}
}

func TestSignatureBinding(t *testing.T) {
	priv, _, _ := GenerateKey()
	pub := priv.Public().(ed25519.PublicKey)
	n, _ := NewNonce()
	sig := Sign(priv, "nod_1", "aven.example.com", n)
	if !VerifySignature(pub, "nod_1", "AVEN.example.com:443", n, sig) {
		t.Error("valid signature (host normalised) rejected")
	}
	n2, _ := NewNonce()
	for name, ok := range map[string]bool{
		"other nonce": VerifySignature(pub, "nod_1", "aven.example.com", n2, sig),
		"other node":  VerifySignature(pub, "nod_2", "aven.example.com", n, sig),
		"other host":  VerifySignature(pub, "nod_1", "evil.example.com", n, sig),
		"garbage":     VerifySignature(pub, "nod_1", "aven.example.com", n, "zz"),
	} {
		if ok {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestCAPersistsWith0600Key(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dir, "agent-ca.key"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("CA key mode: %v %v", st, err)
	}
	b, err := LoadOrCreateCA(dir)
	if err != nil || string(a.PEM) != string(b.PEM) {
		t.Fatal("CA not reloaded")
	}
	priv, _, _ := GenerateKey()
	iss, _ := a.Issue("n", priv.Public().(ed25519.PublicKey))
	if _, err := b.Verify(iss.CertPEM, time.Now()); err != nil {
		t.Errorf("cert from first CA instance not valid for reloaded CA: %v", err)
	}
	os.Remove(filepath.Join(dir, "agent-ca.key"))
	if _, err := LoadOrCreateCA(dir); err == nil {
		t.Error("created a new CA although a certificate exists without its key")
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent")
	path := filepath.Join(dir, "credentials.json")
	if err := WriteFileAtomic(path, []byte("one")); err != nil {
		t.Fatal(err)
	}
	// 이미 있는 파일이 넓은 권한이어도 새 파일은 0600이다.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	// 예전 방식의 고정 임시 파일이 남아 있어도(다른 쓰기 중이던 것) 건드리지 않고 쓴다.
	if err := os.WriteFile(path+".tmp", []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := WriteFileAtomic(path, []byte("two")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "two" {
		t.Fatalf("content = %q, %v", b, err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v, want 0700", st.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "credentials.json" && e.Name() != "credentials.json.tmp" {
			t.Errorf("leftover temporary file %s", e.Name())
		}
	}
	// 쓸 수 없는 위치면 오류를 돌려주고 임시 파일을 남기지 않는다.
	if err := WriteFileAtomic(filepath.Join(path, "x"), []byte("x")); err == nil {
		t.Fatal("wrote below a regular file")
	}
}
