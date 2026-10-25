package agentpki

import (
	"crypto/ed25519"
	"crypto/x509"
	"testing"
	"time"
)

func TestNodeCertForMesh(t *testing.T) {
	ca, _ := NewCA()
	other, _ := NewCA()
	priv, _, _ := GenerateKey()
	iss, err := ca.Issue("nod_mesh", priv.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := ParseCert(iss.CertPEM)
	has := map[x509.ExtKeyUsage]bool{}
	for _, u := range cert.ExtKeyUsage {
		has[u] = true
	}
	if !has[x509.ExtKeyUsageClientAuth] || !has[x509.ExtKeyUsageServerAuth] {
		t.Fatalf("EKU: %v", cert.ExtKeyUsage)
	}
	if v, err := VerifyNodeCert(ca.Pool(), cert, time.Now()); err != nil || v.NodeID != "nod_mesh" || v.Serial != iss.Serial {
		t.Fatalf("verify: %+v %v", v, err)
	}
	if _, err := VerifyNodeCert(other.Pool(), cert, time.Now()); err == nil {
		t.Fatal("other CA accepted")
	}
	if _, err := VerifyNodeCert(ca.Pool(), cert, time.Now().Add(CertValidity+time.Hour)); err == nil {
		t.Fatal("expired certificate accepted")
	}
	tc, err := TLSCertificate(iss.CertPEM, priv)
	if err != nil || tc.Leaf == nil {
		t.Fatalf("tls certificate: %v", err)
	}
	wrong, _, _ := GenerateKey()
	if _, err := TLSCertificate(iss.CertPEM, wrong); err == nil {
		t.Fatal("mismatched key accepted")
	}
}
