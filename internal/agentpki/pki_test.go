package agentpki

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
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
