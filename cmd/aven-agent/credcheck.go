package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/turin-dev/aven/internal/agentpki"
)

// nodeIDRe는 서버가 만드는 node ID 형식이다(nod_ + base32). 옛 서버의 형식도 받도록 넉넉하게 둔다.
var nodeIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// checkIssued는 enroll/upgrade 응답으로 받은 credentials를 저장하기 전에 확인한다. 인증서가 있으면 함께 온 CA가 서명했고,
// 지금 유효하며, 응답의 node ID에 묶였고, 이 agent가 만든 키에 대한 것이어야 한다. 그렇지 않은 credentials를 저장하면
// 다음 시작부터 연결도 못 하고 join token은 이미 소비된 상태가 된다.
func checkIssued(c credentials, now time.Time) error {
	if !nodeIDRe.MatchString(c.NodeID) {
		return errors.New("server returned an invalid node id")
	}
	if c.Certificate == "" {
		if c.Secret == "" {
			return errors.New("server returned neither a certificate nor a secret")
		}
		return nil
	}
	if c.CA == "" {
		return errors.New("server returned a certificate without its CA")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(c.CA)) {
		return errors.New("server returned an invalid CA certificate")
	}
	return checkNodeCert(c, pool, c.Certificate, now)
}

// checkNodeCert는 certPEM이 roots가 서명한, 지금 유효한, c.NodeID와 c.Key에 대한 노드 인증서인지 본다.
// roots가 nil이면(CA를 저장하지 않은 옛 credentials) 서명은 확인하지 못하고 나머지만 본다.
func checkNodeCert(c credentials, roots *x509.CertPool, certPEM string, now time.Time) error {
	cert, err := agentpki.ParseCert([]byte(certPEM))
	if err != nil {
		return fmt.Errorf("certificate: %w", err)
	}
	priv, err := agentpki.ParsePrivateKey([]byte(c.Key))
	if err != nil {
		return fmt.Errorf("private key: %w", err)
	}
	if pub, ok := cert.PublicKey.(ed25519.PublicKey); !ok || !pub.Equal(priv.Public()) {
		return errors.New("certificate is for a different key")
	}
	if roots != nil {
		v, err := agentpki.VerifyNodeCert(roots, cert, now)
		if err != nil {
			return err
		}
		if v.NodeID != c.NodeID {
			return errors.New("certificate is for a different node")
		}
		return nil
	}
	if cert.Subject.CommonName != c.NodeID {
		return errors.New("certificate is for a different node")
	}
	if now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) {
		return errors.New("certificate is not valid now")
	}
	return nil
}
