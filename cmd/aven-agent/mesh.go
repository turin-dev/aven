package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"sync/atomic"

	"github.com/turin-dev/aven/internal/agentpki"
	"github.com/turin-dev/aven/internal/mesh"
	"github.com/turin-dev/aven/internal/node"
)

// meshCert는 mesh gateway가 쓰는 현재 노드 인증서다(인증서가 갱신되면 바뀐다).
var meshCert atomic.Pointer[tls.Certificate]

// setMeshIdentity는 credentials의 키와 인증서로 mesh용 TLS 인증서를 만든다(옛 Bearer 비밀키만 있으면 비운다).
func setMeshIdentity(c credentials) {
	if c.Key == "" || c.Certificate == "" {
		meshCert.Store(nil)
		return
	}
	key, err := agentpki.ParsePrivateKey([]byte(c.Key))
	if err != nil {
		meshCert.Store(nil)
		return
	}
	tc, err := agentpki.TLSCertificate([]byte(c.Certificate), key)
	if err != nil {
		meshCert.Store(nil)
		return
	}
	meshCert.Store(tc)
}

func currentMeshCert() (*tls.Certificate, error) {
	if c := meshCert.Load(); c != nil {
		return c, nil
	}
	return nil, errors.New("no node certificate")
}

// startMesh는 mesh gateway를 사설 주소에서 띄운다. 꺼져 있거나 쓸 수 없으면 (nil, 이유)다.
// listen이 있으면 그 IP에 바인딩한다(다른 노드에는 여전히 private 주소를 알린다). Tailscale userspace-networking처럼
// 사설 주소가 인터페이스에 없고 들어온 연결을 루프백으로 넘겨주는 노드용이다.
func startMesh(ctx context.Context, mode, private, listen, proxy string, port int, cred credentials, docker *node.Docker, log *slog.Logger) (*mesh.Gateway, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "off" {
		return nil, nil
	}
	if mode != "on" && mode != "auto" && mode != "" {
		return nil, fmt.Errorf("--mesh must be on, off or auto (got %q)", mode)
	}
	ip, ok := mesh.ParsePrivateIP(private)
	if !ok {
		if mode == "auto" || mode == "" {
			if private != "" {
				log.Info("mesh gateway off: --private-address is not a private IP", "private", private)
			}
			return nil, nil
		}
		return nil, errors.New("--private-address must be a private IP (RFC1918, ULA or 100.64.0.0/10) for the mesh gateway")
	}
	if port < 1024 || port > 65535 {
		return nil, errors.New("--mesh-port must be 1024..65535")
	}
	if cred.CA == "" || cred.Key == "" {
		return nil, errors.New("mesh needs a certificate-based node identity (re-enroll or restart after the credential upgrade)")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(cred.CA)) {
		return nil, errors.New("invalid pinned CA")
	}
	bind, err := meshBind(ip, listen)
	if err != nil {
		return nil, err
	}
	if proxy != "" {
		if err := checkMeshProxy(proxy); err != nil {
			return nil, err
		}
	}
	gw := &mesh.Gateway{NodeID: cred.NodeID, Listen: netip.AddrPortFrom(bind, uint16(port)).String(), Proxy: proxy, Roots: pool,
		Cert: currentMeshCert, Resolve: &mesh.DockerResolver{Docker: docker}, Log: log}
	go gw.Run(ctx)
	return gw, nil
}

// meshBind는 gateway가 바인딩할 IP다. 비면 사설 주소, 아니면 루프백이나 사설 IP만 받는다(공인 인터페이스는 열지 않는다).
func meshBind(private netip.Addr, listen string) (netip.Addr, error) {
	listen = strings.Trim(strings.TrimSpace(listen), "[]")
	if listen == "" {
		return private, nil
	}
	ip, err := netip.ParseAddr(listen)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("--mesh-listen must be an IP address (got %q)", listen)
	}
	if ip.IsLoopback() {
		return ip, nil
	}
	if p, ok := mesh.ParsePrivateIP(listen); ok {
		return p, nil
	}
	return netip.Addr{}, errors.New("--mesh-listen must be a loopback or private IP")
}

// checkMeshProxy는 프록시 주소가 이 노드나 사설망 안(루프백/사설 IP:port)인지 본다.
func checkMeshProxy(proxy string) error {
	ap, err := netip.ParseAddrPort(proxy)
	if err != nil {
		return fmt.Errorf("--mesh-proxy must be IP:port (got %q)", proxy)
	}
	if ap.Addr().IsLoopback() {
		return nil
	}
	if _, ok := mesh.ParsePrivateIP(ap.Addr().String()); ok {
		return nil
	}
	return errors.New("--mesh-proxy must be a loopback or private IP:port")
}
