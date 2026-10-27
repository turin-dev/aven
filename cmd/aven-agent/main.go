// aven-agent는 원격 노드에서 돈다. 서버에 outbound WebSocket(wss)으로 연결하고, allowlist에 있는 작업만 실행한다.
// 이 노드의 keel-proxy(Caddy)도 agent가 띄우고 서버가 보낸 라우트를 넣는다.
// 연결이 끊겨도 컨테이너와 keel-proxy는 그대로 돈다(restart 정책 유지). agent는 다시 연결을 시도한다.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/turin-dev/aven/internal/compat"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/turin-dev/aven/internal/agent"
	"github.com/turin-dev/aven/internal/agentpki"
	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/mesh"
	"github.com/turin-dev/aven/internal/netlog"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/proxy"
)

var version = "dev"

// credentials는 노드에 저장하는 인증 정보다(0600). 새 방식: 개인키+서버가 발급한 인증서+CA. 옛 방식: Secret(Bearer).
// 옛 credentials.json(Secret만 있음)은 다음 연결 때 자동으로 키쌍+인증서로 업그레이드되고 Secret은 지워진다.
type credentials struct {
	NodeID      string `json:"node_id"`
	Secret      string `json:"secret,omitempty"`
	Key         string `json:"key,omitempty"`         // ed25519 PKCS8 PEM
	Certificate string `json:"certificate,omitempty"` // PEM
	CA          string `json:"ca,omitempty"`          // 서버 CA 인증서 PEM(갱신된 인증서 검증용)
}

func env(suffix, def string) string { return compat.EnvDefault(suffix, def) }

func envInt(suffix string, def int) int {
	if v := compat.Env(suffix); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func main() {
	server := flag.String("server", compat.Env("SERVER_URL"), "aven server URL (https://...)")
	token := flag.String("join-token", compat.Env("JOIN_TOKEN"), "one-time join token (only for the first start)")
	config := flag.String("config", env("AGENT_CREDENTIALS", filepath.Join(compat.DefaultAgentDataDir(), "credentials.json")), "credentials path (AVEN_AGENT_CREDENTIALS)")
	caFile := flag.String("ca-file", compat.Env("CA_FILE"), "extra CA certificate (PEM) to trust for the server")
	dataDir := flag.String("data", env("AGENT_DATA", compat.DefaultAgentDataDir()), "agent data directory (AVEN_AGENT_DATA)")
	httpPort := flag.Int("http-port", envInt("HTTP_PORT", 80), "keel-proxy HTTP port on this node")
	httpsPort := flag.Int("https-port", envInt("HTTPS_PORT", 443), "keel-proxy HTTPS port on this node")
	_ = flag.Int("proxy-admin-port", envInt("PROXY_ADMIN_PORT", 2019), "deprecated: keel-proxy's admin API is a unix socket in the keel-proxy-wake volume now (ignored)")
	proxyBind := flag.String("proxy-bind", compat.Env("PROXY_BIND"), "address keel-proxy binds to (empty: all IPv6+IPv4)")
	tlsMode := flag.String("tls", env("TLS", "auto"), "keel-proxy TLS mode: auto | internal | off")
	acmeEmail := flag.String("acme-email", compat.Env("ACME_EMAIL"), "ACME account email")
	railpack := flag.String("railpack", env("RAILPACK_BIN", agent.RailpackAuto), "railpack binary path, \"auto\" (default: download the pinned official release on first use, SHA-256 verified) or \"off\"")
	frontend := flag.String("railpack-frontend", env("RAILPACK_FRONTEND", "ghcr.io/railwayapp/railpack-frontend:v0.42.0"), "railpack BuildKit frontend image")
	ipv4 := flag.String("public-ipv4", compat.Env("PUBLIC_IPV4"), "public IPv4 of this node (default: detected)")
	ipv6 := flag.String("public-ipv6", compat.Env("PUBLIC_IPV6"), "public IPv6 of this node (default: detected)")
	ipv6Iface := flag.String("ipv6-iface", compat.Env("IPV6_IFACE"), "interface that per-domain public IPv6 addresses are added to (default: the one holding --public-ipv6, else the IPv6 default route's)")
	private := flag.String("private-address", compat.Env("PRIVATE_ADDRESS"), "private (Tailscale/WireGuard) address of this node")
	meshMode := flag.String("mesh", env("MESH", "auto"), "cross-node private networking gateway: on | off | auto (on when --private-address is a private IP)")
	meshPort := flag.Int("mesh-port", envInt("MESH_PORT", mesh.DefaultPort), "mesh gateway port (listens only on --private-address)")
	externalUps := flag.String("external-upstreams", compat.Env("EXTERNAL_UPSTREAMS"), "comma-separated host:port targets outside aven that this node's keel-proxy may route to (e.g. a compose container)")
	proxyNets := flag.String("proxy-networks", compat.Env("PROXY_NETWORKS"), "comma-separated docker networks keel-proxy joins (where the external upstreams live)")
	meshProxy := flag.String("mesh-proxy", compat.Env("MESH_PROXY"), "HTTP proxy (host:port, loopback or private) used to reach other mesh nodes, e.g. tailscaled's --outbound-http-proxy-listen in userspace networking")
	meshListen := flag.String("mesh-listen", compat.Env("MESH_LISTEN"), "IP the mesh gateway binds instead of --private-address, e.g. 127.0.0.1 when Tailscale runs with userspace networking and forwards to loopback (loopback or private IPs only)")
	netLogs := flag.String("network-logs", env("NETWORK_LOGS", "on"), "DNS query and network flow logs from the environment bridges (needs CAP_NET_RAW): on | off")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if *server == "" || !strings.HasPrefix(*server, "https://") {
		log.Error("server URL must use HTTPS")
		os.Exit(1)
	}
	client, err := httpClient(*caFile)
	if err != nil {
		log.Error("TLS setup", "err", err)
		os.Exit(1)
	}
	cred, err := loadCredentials(*config)
	if err != nil && *token != "" {
		cred, err = enroll(ctx, client, *server, *token)
		if err == nil {
			err = saveCredentials(*config, cred)
		}
	}
	if err != nil {
		log.Error("agent enrollment", "err", err)
		os.Exit(1)
	}

	docker := &node.Docker{}
	if err := docker.Ping(ctx); err != nil {
		log.Error("docker", "err", err)
		os.Exit(1)
	}
	if err := docker.EnsureNetwork(ctx); err != nil {
		log.Error("docker network", "err", err)
		os.Exit(1)
	}
	caddy := proxy.New(proxy.Config{HTTPPort: *httpPort, HTTPSPort: *httpsPort, BindIP: *proxyBind, MetricsHosts: proxy.ParseList(*private),
		TLS: proxy.TLSMode(*tlsMode), ACMEEmail: *acmeEmail})
	if err := caddy.EnsureRunning(ctx, docker.ActiveBridge(ctx)); err != nil {
		log.Error("keel-proxy", "err", err)
		os.Exit(1)
	}
	external, err := externalUpstreams(*externalUps)
	if err != nil {
		log.Error("external upstreams", "err", err)
		os.Exit(1)
	}
	connectProxyNetworks(ctx, caddy, proxy.ParseList(*proxyNets), log)
	startEdge(ctx, caddy, docker, log)
	v4, v6 := publicIPs()
	if *ipv4 != "" {
		v4 = *ipv4
	}
	if *ipv6 != "" {
		v6 = *ipv6
	}
	workDir := filepath.Join(*dataDir, "work")
	os.RemoveAll(workDir) // 이전 실행의 남은 빌드 디렉터리
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		log.Error("work dir", "err", err)
		os.Exit(1)
	}
	h := &agent.Handler{Docker: docker, Proxy: caddy, WorkDir: workDir, Railpack: *railpack, RailpackFrontend: *frontend,
		Version: version, IPv4: v4, IPv6: v6, IPv6Iface: strings.TrimSpace(*ipv6Iface), Private: *private, DataDir: *dataDir, TCPBind: *proxyBind, NodeID: cred.NodeID,
		ExternalUpstreams: external}
	setMeshIdentity(cred)
	if gw, err := startMesh(ctx, *meshMode, *private, *meshListen, *meshProxy, *meshPort, cred, docker, log); err != nil {
		log.Warn("mesh gateway disabled", "err", err)
	} else if gw != nil {
		h.Mesh, h.MeshPort = gw, *meshPort
	}
	if !strings.EqualFold(strings.TrimSpace(*netLogs), "off") {
		h.NetLog = &netlog.Collector{NodeID: cred.NodeID, Docker: docker, Log: log}
		go h.NetLog.Run(ctx)
	}
	startWake(ctx, h, docker, caddy, log)
	log.Info("aven-agent starting", "node", cred.NodeID, "server", *server, "version", version)
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		if cred.Key == "" {
			// 옛 Bearer 비밀키만 있으면 키쌍을 만들어 인증서로 업그레이드한다(서버가 지원하지 않으면 옛 방식 유지).
			if up, uerr := upgrade(ctx, client, *server, cred); uerr == nil {
				if serr := saveCredentials(*config, up); serr != nil {
					log.Error("saving upgraded credentials", "err", serr)
					os.Exit(1)
				}
				cred = up
				setMeshIdentity(cred)
				log.Info("upgraded node credentials to a certificate", "node", cred.NodeID)
			} else if !errors.Is(uerr, errUpgradeUnsupported) {
				log.Warn("credential upgrade failed; using the legacy secret for now", "err", uerr)
			}
		}
		err := connect(ctx, client, *server, &cred, *config, h, log)
		if ctx.Err() != nil {
			break
		}
		log.Warn("disconnected from server; containers keep running", "err", err)
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func httpClient(caFile string) (*http.Client, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("no certificates in CA file")
		}
		cfg.RootCAs = pool
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = cfg
	return &http.Client{Transport: tr}, nil
}

func loadCredentials(path string) (credentials, error) {
	var c credentials
	b, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(b, &c)
	}
	if err == nil && (c.NodeID == "" || (c.Secret == "" && (c.Key == "" || c.Certificate == ""))) {
		err = errors.New("invalid credentials")
	}
	return c, err
}

func saveCredentials(path string, c credentials) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return agentpki.WriteFileAtomic(path, b)
}

var errUpgradeUnsupported = errors.New("server does not support certificate upgrade")

// register는 enroll/upgrade 요청을 보내고 응답으로 새 credentials를 만든다.
func register(ctx context.Context, client *http.Client, server, path string, req map[string]any, key []byte) (credentials, int, error) {
	var c credentials
	body, _ := json.Marshal(req)
	hr, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(server, "/")+path, strings.NewReader(string(body)))
	if err != nil {
		return c, 0, err
	}
	hr.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(hr)
	if err != nil {
		return c, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return c, resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var r struct {
		NodeID      string `json:"node_id"`
		Certificate string `json:"certificate"`
		CA          string `json:"ca"`
		Secret      string `json:"secret"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&r); err != nil {
		return c, 200, err
	}
	if r.Certificate == "" { // 인증서를 모르는 옛 서버: Bearer 비밀키
		return credentials{NodeID: r.NodeID, Secret: r.Secret}, 200, nil
	}
	return credentials{NodeID: r.NodeID, Key: string(key), Certificate: r.Certificate, CA: r.CA}, 200, nil
}

func enroll(ctx context.Context, client *http.Client, server, token string) (credentials, error) {
	priv, keyPEM, err := agentpki.GenerateKey()
	if err != nil {
		return credentials{}, err
	}
	c, status, err := register(ctx, client, server, "/api/agent/enroll", map[string]any{
		"token": token, "name": hostname(), "public_key": agentpki.EncodePublicKey(priv.Public().(ed25519.PublicKey)),
	}, keyPEM)
	if err != nil {
		return c, fmt.Errorf("enrollment %w", err)
	}
	_ = status
	return c, nil
}

func upgrade(ctx context.Context, client *http.Client, server string, old credentials) (credentials, error) {
	priv, keyPEM, err := agentpki.GenerateKey()
	if err != nil {
		return credentials{}, err
	}
	c, status, err := register(ctx, client, server, "/api/agent/upgrade", map[string]any{
		"node_id": old.NodeID, "secret": old.Secret, "public_key": agentpki.EncodePublicKey(priv.Public().(ed25519.PublicKey)),
	}, keyPEM)
	if status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
		return c, errUpgradeUnsupported
	}
	if err == nil && c.Key == "" {
		err = errors.New("server returned no certificate")
	}
	return c, err
}

// authenticate는 연결 직후 서버의 nonce 챌린지에 개인키로 서명한다.
// audience는 이 agent가 접속하려고 설정한 서버 host다(서버가 알려 주는 값이 아니라서, 다른 서버가 이 서명을 중계할 수 없다).
func authenticate(ctx context.Context, conn *websocket.Conn, cred *credentials, audience string) error {
	priv, err := agentpki.ParsePrivateKey([]byte(cred.Key))
	if err != nil {
		return fmt.Errorf("private key: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn.SetReadLimit(agentprotocol.MaxAuthMessageBytes)
	var msg agentprotocol.Message
	_, data, err := conn.Read(ctx)
	if err == nil {
		err = json.Unmarshal(data, &msg)
	}
	var ch agentprotocol.AuthChallenge
	if err != nil || msg.Operation != agentprotocol.OpAuthChallenge || json.Unmarshal(msg.Payload, &ch) != nil || ch.Nonce == "" {
		return errors.New("server sent no authentication challenge")
	}
	payload, _ := json.Marshal(agentprotocol.AuthResponse{Certificate: cred.Certificate, Signature: agentpki.Sign(priv, cred.NodeID, audience, ch.Nonce)})
	out, _ := json.Marshal(agentprotocol.Message{Operation: agentprotocol.OpAuthResponse, Payload: payload})
	if err := conn.Write(ctx, websocket.MessageText, out); err != nil {
		return err
	}
	_, data, err = conn.Read(ctx)
	if err == nil {
		err = json.Unmarshal(data, &msg)
	}
	if err != nil || msg.Operation != agentprotocol.OpAuthOK {
		return errors.New("server rejected the node certificate")
	}
	conn.SetReadLimit(agentprotocol.MaxMessageBytes)
	return nil
}

// acceptRenewal은 서버가 연결 중에 밀어 준 새 인증서를 검증하고 저장한다(같은 키, 같은 node ID, 고정해 둔 CA가 서명한 것만).
func acceptRenewal(cred *credentials, path string, certPEM string) error {
	cert, err := agentpki.ParseCert([]byte(certPEM))
	if err != nil {
		return err
	}
	priv, err := agentpki.ParsePrivateKey([]byte(cred.Key))
	if err != nil {
		return err
	}
	if pub, ok := cert.PublicKey.(ed25519.PublicKey); !ok || !pub.Equal(priv.Public()) {
		return errors.New("renewed certificate is for a different key")
	}
	if cred.CA != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(cred.CA)) {
			return errors.New("invalid pinned CA")
		}
		if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
			return err
		}
	}
	if cert.Subject.CommonName != cred.NodeID {
		return errors.New("renewed certificate is for a different node")
	}
	next := *cred
	next.Certificate = certPEM
	if err := saveCredentials(path, next); err != nil {
		return err
	}
	*cred = next
	setMeshIdentity(next)
	return nil
}

func connect(ctx context.Context, client *http.Client, server string, cred *credentials, credPath string, h *agent.Handler, log *slog.Logger) error {
	wsURL := "wss://" + strings.TrimPrefix(strings.TrimRight(server, "/"), "https://") + "/api/agent/connect"
	opts := &websocket.DialOptions{HTTPClient: client}
	if cred.Key == "" { // 옛 방식(서버가 업그레이드를 지원하지 않을 때)
		opts.HTTPHeader = http.Header{"Authorization": []string{"Bearer " + cred.NodeID + ":" + cred.Secret}}
	}
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	conn, _, err := websocket.Dial(dctx, wsURL, opts)
	cancel()
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(agentprotocol.MaxMessageBytes)
	if cred.Key != "" {
		u, err := url.Parse(server)
		if err != nil {
			return err
		}
		if err := authenticate(ctx, conn, cred, u.Host); err != nil {
			return err
		}
	}
	log.Info("connected to server", "node", cred.NodeID, "auth", map[bool]string{true: "certificate", false: "legacy-bearer"}[cred.Key != ""])
	peer := &agentprotocol.Peer{Conn: conn}
	cctx, stop := context.WithCancel(ctx)
	defer stop()
	// 서버가 사라졌는지 확인한다(반쯤 끊긴 연결이면 다시 연결).
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-cctx.Done():
				return
			case <-t.C:
				pctx, c := context.WithTimeout(cctx, 10*time.Second)
				err := conn.Ping(pctx)
				c()
				if err != nil && cctx.Err() == nil {
					log.Warn("server heartbeat failed; reconnecting", "err", err)
					conn.CloseNow()
					return
				}
			}
		}
	}()
	return h.Serve(cctx, func(ctx context.Context) ([]byte, error) {
		for {
			_, b, err := conn.Read(ctx)
			if err != nil {
				return nil, err
			}
			var m agentprotocol.Message
			if json.Unmarshal(b, &m) == nil && m.Operation == agentprotocol.OpCertRenew {
				var r agentprotocol.CertRenew
				if json.Unmarshal(m.Payload, &r) == nil {
					if err := acceptRenewal(cred, credPath, r.Certificate); err != nil {
						log.Warn("ignoring certificate renewal", "err", err)
					} else {
						log.Info("node certificate renewed")
					}
				}
				continue
			}
			return b, nil
		}
	}, peer.Write)
}

// publicIPs는 외부로 나가는 경로의 출발 주소를 찾는다(패킷은 보내지 않는다). 사설 주소면 비운다.
func publicIPs() (v4, v6 string) {
	probe := func(network, addr string) string {
		c, err := net.Dial(network, addr)
		if err != nil {
			return ""
		}
		defer c.Close()
		ip := c.LocalAddr().(*net.UDPAddr).IP
		if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			return ""
		}
		return ip.String()
	}
	return probe("udp4", "1.1.1.1:53"), probe("udp6", "[2606:4700:4700::1111]:53")
}

func hostname() string { s, _ := os.Hostname(); return s }
