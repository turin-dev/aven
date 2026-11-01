package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/agentpki"
)

// issuedCred는 ca가 nodeID에 발급한 인증서를 담은 credentials다.
func issuedCred(t *testing.T, ca *agentpki.CA, nodeID string, validity time.Duration) (credentials, ed25519.PrivateKey) {
	t.Helper()
	priv, keyPEM, err := agentpki.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	is, err := ca.IssueFor(nodeID, priv.Public().(ed25519.PublicKey), validity)
	if err != nil {
		t.Fatal(err)
	}
	return credentials{NodeID: nodeID, Key: string(keyPEM), Certificate: string(is.CertPEM), CA: string(ca.PEM)}, priv
}

func TestCheckIssued(t *testing.T) {
	ca, _ := agentpki.NewCA()
	other, _ := agentpki.NewCA()
	now := time.Now()
	good, _ := issuedCred(t, ca, "nod_aaaaaaaaaaaaa", time.Hour)
	otherKey, _ := issuedCred(t, ca, "nod_aaaaaaaaaaaaa", time.Hour)
	foreign, _ := issuedCred(t, other, "nod_aaaaaaaaaaaaa", time.Hour)
	expired, _ := issuedCred(t, ca, "nod_aaaaaaaaaaaaa", time.Second)

	for _, tc := range []struct {
		name string
		mod  func(c credentials) credentials
		at   time.Time
		ok   bool
	}{
		{"valid certificate", func(c credentials) credentials { return c }, now, true},
		{"legacy secret", func(credentials) credentials { return credentials{NodeID: "nod_aaaaaaaaaaaaa", Secret: "s"} }, now, true},
		{"legacy without secret", func(credentials) credentials { return credentials{NodeID: "nod_aaaaaaaaaaaaa"} }, now, false},
		{"empty node id", func(c credentials) credentials { c.NodeID = ""; return c }, now, false},
		{"node id with path characters", func(c credentials) credentials { c.NodeID = "../x"; return c }, now, false},
		{"certificate for another node", func(c credentials) credentials { c.NodeID = "nod_bbbbbbbbbbbbb"; return c }, now, false},
		{"certificate for another key", func(c credentials) credentials { c.Certificate = otherKey.Certificate; return c }, now, false},
		{"certificate from another CA", func(c credentials) credentials { c.Certificate = foreign.Certificate; return c }, now, false},
		{"missing CA", func(c credentials) credentials { c.CA = ""; return c }, now, false},
		{"garbage CA", func(c credentials) credentials { c.CA = "nope"; return c }, now, false},
		{"garbage certificate", func(c credentials) credentials { c.Certificate = "nope"; return c }, now, false},
		{"expired certificate", func(c credentials) credentials { c.Key, c.Certificate = expired.Key, expired.Certificate; return c }, now.Add(time.Minute), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkIssued(tc.mod(good), tc.at)
			if (err == nil) != tc.ok {
				t.Fatalf("checkIssued: err=%v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestAcceptRenewal(t *testing.T) {
	ca, _ := agentpki.NewCA()
	other, _ := agentpki.NewCA()
	cur, priv := issuedCred(t, ca, "nod_aaaaaaaaaaaaa", 20*24*time.Hour)
	pub := priv.Public().(ed25519.PublicKey)
	issue := func(c *agentpki.CA, node string, validity time.Duration) string {
		is, err := c.IssueFor(node, pub, validity)
		if err != nil {
			t.Fatal(err)
		}
		return string(is.CertPEM)
	}
	path := filepath.Join(t.TempDir(), "credentials.json")
	for _, tc := range []struct {
		name string
		cert string
		ok   bool
	}{
		{"other CA", issue(other, "nod_aaaaaaaaaaaaa", 30*24*time.Hour), false},
		{"other node", issue(ca, "nod_bbbbbbbbbbbbb", 30*24*time.Hour), false},
		{"older than the current one", issue(ca, "nod_aaaaaaaaaaaaa", time.Hour), false},
		{"other key", func() string { c, _ := issuedCred(t, ca, "nod_aaaaaaaaaaaaa", 30*24*time.Hour); return c.Certificate }(), false},
		{"valid", issue(ca, "nod_aaaaaaaaaaaaa", 30*24*time.Hour), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := cur
			err := acceptRenewal(&c, path, tc.cert)
			if (err == nil) != tc.ok {
				t.Fatalf("acceptRenewal: err=%v, want ok=%v", err, tc.ok)
			}
			if tc.ok {
				saved, err := loadCredentials(path)
				if err != nil || saved.Certificate != tc.cert || c.Certificate != tc.cert {
					t.Fatalf("renewed certificate not stored: %v", err)
				}
			} else if c.Certificate != cur.Certificate {
				t.Fatal("rejected renewal changed the in-memory credentials")
			}
		})
	}
}

// enroll은 리다이렉트를 따라가지 않는다(307이면 join token이 담긴 본문이 다른 host로 다시 간다).
func TestEnrollDoesNotFollowRedirects(t *testing.T) {
	var leaked atomic.Int32
	elsewhere := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer elsewhere.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	client, _ := httpClient("")
	client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	if _, err := enroll(context.Background(), client, srv.URL, "join-token"); err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatalf("enroll through a redirect: %v", err)
	}
	if leaked.Load() != 0 {
		t.Fatal("join token was re-posted to the redirect target")
	}
}

// enroll은 서버가 준 인증서가 자기 키에 대한 것이 아니면 저장할 credentials를 돌려주지 않는다.
func TestEnrollRejectsMismatchedCertificate(t *testing.T) {
	ca, _ := agentpki.NewCA()
	someone, _ := issuedCred(t, ca, "nod_aaaaaaaaaaaaa", time.Hour)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"node_id": someone.NodeID, "certificate": someone.Certificate, "ca": someone.CA})
	}))
	defer srv.Close()
	client, _ := httpClient("")
	client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	if _, err := enroll(context.Background(), client, srv.URL, "join-token"); err == nil {
		t.Fatal("enroll accepted a certificate for a different key")
	}
}
