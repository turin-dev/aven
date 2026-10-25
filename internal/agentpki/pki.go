// Package agentpki는 aven 내부 CA와 agent 인증서를 다룬다.
//
// Cloudflare/Traefik처럼 TLS를 중간에서 끝내는 경로에서는 TLS 클라이언트 인증서(mTLS)가 서버까지 닿지 않는다.
// 그래서 같은 보장을 애플리케이션 계층에서 만든다: agent는 키쌍(ed25519)을 직접 만들고(개인키는 노드를 떠나지 않음),
// 서버는 aven CA로 서명한 인증서(node ID를 URI SAN/CN에 묶음)를 발급한다.
// 연결할 때마다 서버가 낸 nonce에 개인키로 서명해 키 소유를 증명한다(요청 헤더만 훔쳐서는 쓸 수 없다).
package agentpki

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// CertValidity는 agent 인증서 유효기간이다. 만료 RenewBefore 전부터 연결 중에 서버가 새 인증서를 밀어준다.
	CertValidity = 30 * 24 * time.Hour
	RenewBefore  = 10 * 24 * time.Hour
	caValidity   = 10 * 365 * 24 * time.Hour
	clockSkew    = 5 * time.Minute

	URIScheme = "keel-node"
	authLabel = "keel-agent-auth-v1"
)

type CA struct {
	cert *x509.Certificate
	key  ed25519.PrivateKey
	pool *x509.CertPool
	PEM  []byte
}

// LoadOrCreateCA는 dir의 agent-ca.key(0600)/agent-ca.crt를 읽고, 없으면 새로 만든다.
func LoadOrCreateCA(dir string) (*CA, error) {
	keyPath, crtPath := filepath.Join(dir, "agent-ca.key"), filepath.Join(dir, "agent-ca.crt")
	keyPEM, kerr := os.ReadFile(keyPath)
	crtPEM, cerr := os.ReadFile(crtPath)
	if kerr == nil && cerr == nil {
		return parseCA(crtPEM, keyPEM)
	}
	if !errors.Is(kerr, os.ErrNotExist) && kerr != nil {
		return nil, kerr
	}
	if !errors.Is(cerr, os.ErrNotExist) && cerr != nil {
		return nil, cerr
	}
	if kerr == nil || cerr == nil {
		return nil, fmt.Errorf("%s: only one of agent-ca.key/agent-ca.crt exists; refusing to create a new CA", dir)
	}
	ca, keyPEM, err := newCA()
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(keyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}
	if err := writeAtomic(crtPath, ca.PEM, 0o644); err != nil {
		return nil, err
	}
	return ca, nil
}

// NewCA는 메모리에만 있는 CA를 만든다(테스트용).
func NewCA() (*CA, error) { ca, _, err := newCA(); return ca, err }

func newCA() (*CA, []byte, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randSerial()
	if err != nil {
		return nil, nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "keel agent CA", Organization: []string{"keel"}},
		NotBefore: time.Now().Add(-clockSkew), NotAfter: time.Now().Add(caValidity),
		IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, pub, priv)
	if err != nil {
		return nil, nil, err
	}
	pk8, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, nil, err
	}
	crtPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk8})
	ca, err := parseCA(crtPEM, keyPEM)
	return ca, keyPEM, err
}

func parseCA(crtPEM, keyPEM []byte) (*CA, error) {
	cert, err := ParseCert(crtPEM)
	if err != nil {
		return nil, fmt.Errorf("agent CA certificate: %w", err)
	}
	key, err := ParsePrivateKey(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("agent CA key: %w", err)
	}
	if pub, ok := cert.PublicKey.(ed25519.PublicKey); !ok || subtle.ConstantTimeCompare(pub, key.Public().(ed25519.PublicKey)) != 1 {
		return nil, errors.New("agent CA key does not match its certificate")
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &CA{cert: cert, key: key, pool: pool, PEM: crtPEM}, nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// WriteFileAtomic는 agent 쪽에서도 쓰는 0600 원자적 쓰기다.
func WriteFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeAtomic(path, data, 0o600)
}

func randSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

// Issued는 발급 결과다.
type Issued struct {
	CertPEM  []byte
	Serial   string
	NotAfter time.Time
}

// Issue는 pub에 대한 노드 인증서를 발급한다. node ID는 CN과 URI SAN(keel-node://<id>)에 들어간다.
func (ca *CA) Issue(nodeID string, pub ed25519.PublicKey) (*Issued, error) {
	return ca.IssueFor(nodeID, pub, CertValidity)
}

// IssueFor는 유효기간을 지정해 발급한다(테스트와 갱신 시험용).
func (ca *CA) IssueFor(nodeID string, pub ed25519.PublicKey, validity time.Duration) (*Issued, error) {
	if nodeID == "" || len(pub) != ed25519.PublicKeySize {
		return nil, errors.New("invalid node id or public key")
	}
	serial, err := randSerial()
	if err != nil {
		return nil, err
	}
	notAfter := time.Now().Add(validity).UTC().Truncate(time.Second)
	// serverAuth는 노드 간 mesh gateway(mTLS)에서 서버 역할을 할 때 쓴다. 옛 인증서(clientAuth만)도 mesh에서 받아들인다
	// (mesh는 양쪽 모두 clientAuth를 노드 신원 용도로 확인한다).
	tpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: nodeID, Organization: []string{"keel node"}},
		NotBefore: time.Now().Add(-clockSkew).UTC(), NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		URIs: []*url.URL{{Scheme: URIScheme, Host: nodeID}},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, pub, ca.key)
	if err != nil {
		return nil, err
	}
	return &Issued{CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), Serial: serial.Text(16), NotAfter: notAfter}, nil
}

// Verified는 CA가 서명한 유효한 인증서에서 읽은 정보다.
type Verified struct {
	NodeID    string
	Serial    string
	NotAfter  time.Time
	PublicKey ed25519.PublicKey
}

// Verify는 서명, 유효기간, 용도, node ID 바인딩을 확인한다(폐기 여부는 호출자가 serial로 확인한다).
func (ca *CA) Verify(certPEM []byte, now time.Time) (*Verified, error) {
	cert, err := ParseCert(certPEM)
	if err != nil {
		return nil, err
	}
	return VerifyNodeCert(ca.pool, cert, now)
}

// Pool은 CA 인증서만 담은 pool이다(mesh gateway의 상대 노드 인증서 검증용).
func (ca *CA) Pool() *x509.CertPool { return ca.pool }

// VerifyNodeCert는 roots(aven CA)가 서명한 노드 인증서인지(서명, 유효기간, clientAuth 용도, node ID 바인딩) 확인한다.
// 폐기 여부는 호출자가 serial로 확인한다. CA 개인키가 없는 agent도 쓴다(mesh gateway).
func VerifyNodeCert(roots *x509.CertPool, cert *x509.Certificate, now time.Time) (*Verified, error) {
	if roots == nil || cert == nil {
		return nil, errors.New("no certificate")
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return nil, fmt.Errorf("certificate: %w", err)
	}
	pub, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("certificate key is not ed25519")
	}
	if len(cert.URIs) != 1 || cert.URIs[0].Scheme != URIScheme || cert.URIs[0].Host == "" || cert.URIs[0].Host != cert.Subject.CommonName {
		return nil, errors.New("certificate carries no node identity")
	}
	return &Verified{NodeID: cert.Subject.CommonName, Serial: cert.SerialNumber.Text(16), NotAfter: cert.NotAfter, PublicKey: pub}, nil
}

func ParseCert(certPEM []byte) (*x509.Certificate, error) {
	b, _ := pem.Decode(certPEM)
	if b == nil || b.Type != "CERTIFICATE" {
		return nil, errors.New("no certificate PEM")
	}
	return x509.ParseCertificate(b.Bytes)
}

// GenerateKey는 agent의 개인키를 만든다.
func GenerateKey() (ed25519.PrivateKey, []byte, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	pk8, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, nil, err
	}
	return priv, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk8}), nil
}

func ParsePrivateKey(keyPEM []byte) (ed25519.PrivateKey, error) {
	b, _ := pem.Decode(keyPEM)
	if b == nil {
		return nil, errors.New("no key PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, err
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("key is not ed25519")
	}
	return priv, nil
}

// EncodePublicKey/DecodePublicKey: enroll/upgrade 요청에 실리는 공개키(PKIX DER의 base64).
func EncodePublicKey(pub ed25519.PublicKey) string {
	der, _ := x509.MarshalPKIXPublicKey(pub)
	return base64.StdEncoding.EncodeToString(der)
}

func DecodePublicKey(s string) (ed25519.PublicKey, error) {
	der, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, errors.New("invalid public key")
	}
	k, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, errors.New("invalid public key")
	}
	pub, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("public key is not ed25519")
	}
	return pub, nil
}

// NewNonce는 연결마다 새로 만드는 챌린지다.
func NewNonce() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NormalizeHost는 서명에 들어가는 서버 식별자(Host)를 소문자로 맞추고 기본 포트(:443)를 뗀다.
func NormalizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	return strings.TrimSuffix(h, ":443")
}

func transcript(nodeID, audience, nonce string) []byte {
	return []byte(authLabel + "\n" + nodeID + "\n" + NormalizeHost(audience) + "\n" + nonce)
}

// Sign은 agent가 챌린지에 서명한다. audience는 agent가 접속하려고 설정한 서버 host다(서버가 말해 주는 값이 아님).
func Sign(priv ed25519.PrivateKey, nodeID, audience, nonce string) string {
	return hex.EncodeToString(ed25519.Sign(priv, transcript(nodeID, audience, nonce)))
}

func VerifySignature(pub ed25519.PublicKey, nodeID, audience, nonce, sigHex string) bool {
	sig, err := hex.DecodeString(sigHex)
	return err == nil && len(sig) == ed25519.SignatureSize && ed25519.Verify(pub, transcript(nodeID, audience, nonce), sig)
}

// TLSCertificate는 노드 인증서(PEM)와 개인키로 TLS 인증서를 만든다(mesh gateway mTLS용). 키와 인증서가 맞아야 한다.
func TLSCertificate(certPEM []byte, key ed25519.PrivateKey) (*tls.Certificate, error) {
	cert, err := ParseCert(certPEM)
	if err != nil {
		return nil, err
	}
	pub, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok || !pub.Equal(key.Public()) {
		return nil, errors.New("certificate does not match the private key")
	}
	return &tls.Certificate{Certificate: [][]byte{cert.Raw}, PrivateKey: key, Leaf: cert}, nil
}

// NeedsRenewal: 만료까지 RenewBefore 미만이면 true.
func NeedsRenewal(notAfter, now time.Time) bool { return notAfter.Sub(now) < RenewBefore }
