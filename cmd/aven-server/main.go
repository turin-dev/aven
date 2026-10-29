// aven-server: control plane + (M1에서는) 로컬 노드 agent를 한 프로세스에서 실행한다.
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"github.com/turin-dev/aven/internal/compat"
	"github.com/turin-dev/aven/internal/secret"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/turin-dev/aven/internal/agentpki"
	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/alerts"
	"github.com/turin-dev/aven/internal/api"
	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/events"
	"github.com/turin-dev/aven/internal/ghstatus"
	"github.com/turin-dev/aven/internal/logbook"
	"github.com/turin-dev/aven/internal/metrics"
	"github.com/turin-dev/aven/internal/netcheck"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/notify"
	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/selfhost"
	"github.com/turin-dev/aven/internal/store"
	"github.com/turin-dev/aven/internal/tracing"
	"github.com/turin-dev/aven/internal/usage"
	"github.com/turin-dev/aven/internal/webui"
)

var version = "dev"

// env reads AVEN_<suffix>, then KEEL_<suffix>. The old name logs one deprecation warning.
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
	if len(os.Args) > 1 && os.Args[1] == "selfhost" {
		os.Exit(selfhost.Main(os.Args[2:]))
	}
	listen := flag.String("listen", env("LISTEN", "[::1]:7070"), "API/UI listen address")
	dataDir := flag.String("data", env("DATA", compat.DefaultDataDir()), "data directory (AVEN_DATA)")
	flag.Parse()

	logLevel := slog.LevelInfo
	if compat.Env("DEBUG") != "" {
		logLevel = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))

	if err := run(log, *listen, *dataDir); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, listen, dataDir string) error {
	password := compat.Env("ADMIN_PASSWORD")
	if len(password) < 8 {
		return errors.New(compat.Describe("ADMIN_PASSWORD") + "(8자 이상)가 필요합니다")
	}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return err
	}
	st, err := store.Open(compat.DBPath(dataDir))
	if err != nil {
		return err
	}
	defer st.Close()
	box, err := secret.LoadOrCreate(filepath.Join(dataDir, "master.key"))
	if err != nil {
		return err
	}
	st.SetSecretBox(box)
	if err := st.EncryptLegacySecrets(context.Background()); err != nil {
		return err
	}
	key, err := sessionKey(filepath.Join(dataDir, "session.key"))
	if err != nil {
		return err
	}
	logs, err := logbook.NewRegistry(filepath.Join(dataDir, "logs"))
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	proxyTraceToken, err := getTracingProxyToken(st)
	if err != nil {
		return err
	}

	docker := &node.Docker{}
	if err := docker.Ping(ctx); err != nil {
		return err
	}
	if err := docker.EnsureNetwork(ctx); err != nil {
		return err
	}
	caddy := proxy.New(proxy.Config{
		HTTPPort:  envInt("HTTP_PORT", 80),
		HTTPSPort: envInt("HTTPS_PORT", 443),
		BindIP:    compat.Env("PROXY_BIND"),
		TLS:       proxy.TLSMode(env("TLS", "auto")),
		ACMEEmail: compat.Env("ACME_EMAIL"),
		// 사설 주소(Tailscale)를 Host로 한 :80/metrics에 Caddy 지표를 낸다(Prometheus가 Traefik 때와 같은 대상으로 수집).
		MetricsHosts: proxy.ParseList(compat.Env("PRIVATE_ADDRESS")),

		OTLPEndpoint: otlpEndpoint(), TraceToken: proxyTraceToken,
	})
	if err := caddy.EnsureRunning(ctx, docker.ActiveBridge(ctx)); err != nil {
		return err
	}
	// aven 밖의 upstream(다른 compose의 컨테이너 등): 허용 목록과 keel-proxy가 붙을 네트워크.
	externalUps := proxy.ParseList(compat.Env("EXTERNAL_UPSTREAMS"))
	for _, u := range externalUps {
		if !proxy.ValidExternalUpstream(u) {
			return fmt.Errorf("%s: %q is not a container-name or private-IP host:port", compat.Describe("EXTERNAL_UPSTREAMS"), u)
		}
	}
	proxyName := caddy.Resolve(ctx).Container
	for _, n := range proxy.ParseList(compat.Env("PROXY_NETWORKS")) {
		if err := caddy.Connect(ctx, n); err != nil {
			log.Warn("proxy could not join network", "proxy", proxyName, "network", n, "err", err)
		} else {
			log.Info("proxy joined network", "proxy", proxyName, "network", n)
		}
	}
	startEdge(ctx, caddy, docker, log)

	bus := &events.Bus{Store: st, Log: log}
	// 알림: 채널(webhook/Slack/Discord/이메일), 예전 프로젝트 webhook, 사용자 알림함. 사설 목적지는 AVEN_NOTIFY_ALLOW_PRIVATE=1일 때만.
	mailer, err := notify.MailerFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	pricing, err := usage.PricingFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	notifier := &notify.Notifier{Store: st, Log: log, Mailer: mailer, PublicURL: compat.Env("PUBLIC_URL"),
		Guard: &notify.Guard{AllowPrivate: compat.Env("NOTIFY_ALLOW_PRIVATE") == "1"}}
	bus.Notifier = notifier
	ipv4, ipv6 := publicIPs()
	railpackBin := compat.Env("RAILPACK_BIN")
	if railpackBin == "" {
		railpackBin = findRailpack()
	}
	selfHost, selfSecret, err := selfRoute()
	if err != nil {
		return err
	}
	cfg := deploy.Config{
		DataDir:            dataDir,
		BaseDomain:         strings.TrimPrefix(strings.ToLower(compat.Env("BASE_DOMAIN")), "."),
		PublicIPv4:         env("PUBLIC_IPV4", ipv4),
		PublicIPv6:         env("PUBLIC_IPV6", ipv6),
		TCPProxyDomain:     compat.Env("TCP_PROXY_DOMAIN"),
		NodeHostname:       nodeHostname(),
		ProxyBind:          compat.Env("PROXY_BIND"),
		GitHubToken:        compat.Env("GITHUB_TOKEN"),
		GitHubClientID:     compat.Env("GITHUB_CLIENT_ID"),
		GitHubClientSecret: compat.Env("GITHUB_CLIENT_SECRET"),
		ExternalUpstreams:  externalUps,
		SelfHost:           selfHost,
		Railpack:           railpackBin,
		RailpackFrontend:   env("RAILPACK_FRONTEND", "ghcr.io/railwayapp/railpack-frontend:v0.42.0"),
		TCPPortMin:         envInt("TCP_PORT_MIN", 20000),
		TCPPortMax:         envInt("TCP_PORT_MAX", 29999),
		PrivateAddress:     strings.TrimSpace(compat.Env("PRIVATE_ADDRESS")),
		Mesh:               env("MESH", "auto"),
		MeshPort:           envInt("MESH_PORT", 7443),
		OTLPEndpoint:       otlpEndpoint(),
		IPv6Iface:          strings.TrimSpace(compat.Env("IPV6_IFACE")),
		SelfServiceID:      strings.TrimSpace(compat.Env("SELF_SERVICE")),
		SelfEnvFile:        env("SELF_ENV_FILE", compat.DefaultEnvFile()),
	}
	// 도메인별 공개 IPv6: AVEN_IPV6_POOL(CIDR, /64~/120)이 있으면 로컬 노드 풀로 쓴다(PATCH /api/nodes/node_local 값보다 우선).
	if v := strings.TrimSpace(compat.Env("IPV6_POOL")); v != "" {
		if _, err := node.ParseIPv6Pool(v); err != nil {
			log.Warn(compat.Describe("IPV6_POOL")+" ignored", "err", err)
		} else {
			cfg.IPv6Pool = v
		}
	}
	// aven-server 자신의 공개 호스트: keel-proxy와 공유하는 볼륨 안 unix socket으로 받는다(컷오버 뒤 Caddy가 TLS를 끝낸다).
	if selfHost != "" {
		names := caddy.Resolve(ctx)
		mp, err := docker.VolumeMountpoint(ctx, names.WakeVolume)
		if err != nil {
			return fmt.Errorf("self route: %w", err)
		}
		sockName := selfSocket
		if names.Legacy {
			sockName = legacySelfSocket
		}
		cfg.SelfUpstream = "unix/" + names.WakeDir + "/" + sockName
		cfg.SelfHeaders = map[string]string{api.GatewayHeader: selfSecret}
		selfListenPath = filepath.Join(mp, sockName)
	}
	// agent 인증용 내부 CA(키는 AVEN_DATA/agent-ca.key, 0600). 노드 간 mesh도 이 CA의 노드 인증서로 mTLS를 한다.
	agentCA, err := agentpki.LoadOrCreateCA(dataDir)
	if err != nil {
		return fmt.Errorf("agent CA: %w", err)
	}
	log.Info("config", "base_domain", cfg.BaseDomain, "ipv4", cfg.PublicIPv4, "ipv6", cfg.PublicIPv6, "railpack", cfg.Railpack)
	hub := &agentprotocol.Hub{}
	mgr := &deploy.Manager{
		Store: st, Docker: docker, Proxy: caddy, Logs: logs, Events: bus, Cfg: cfg, Log: log, Agents: hub, MeshCA: agentCA,
		IPRunner: node.SystemIPRunner(), // 도메인별 IPv6 주소(root일 때만, ip -6 addr replace/del)
	}
	// GitHub commit status: GitHub 토큰(대시보드 연결 또는 AVEN_GITHUB_TOKEN)이 있으면 github.com 저장소의 커밋에 배포 상태를 올린다
	// (비동기, 실패해도 배포와 무관). 토큰은 요청마다 다시 읽는다.
	poster := &ghstatus.Poster{Store: st, TokenFunc: mgr.DeploymentGitHubToken, APIBase: env("GITHUB_API_URL", ghstatus.DefaultAPIBase),
		PublicURL: compat.Env("PUBLIC_URL"), Log: log}
	mgr.StatusHook = poster.Hook
	// 원격 노드가 (다시) 연결되면 그 노드의 reconcile과 라우트 동기화를 바로 이어서 한다.
	hub.OnRequest = mgr.HandleAgentRequest
	hub.OnConnect = func(nodeID string) {
		mgr.StartNodeStreams(nodeID) // access log + docker 이벤트 스트림
		rctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		time.Sleep(time.Second) // 연결 직후 status 기록과 ping이 먼저 끝나게
		if err := mgr.ReconcileNode(rctx, nodeID); err != nil && rctx.Err() == nil {
			log.Warn("reconcile on agent connect", "node", nodeID, "err", err)
		}
		_ = mgr.SyncEgressNode(rctx, nodeID) // 아웃바운드 IPv6 차단 규칙(재부팅이나 agent 교체 뒤 다시 맞춘다, 오류는 egress가 기록)
		mgr.SyncDomainIPv6Async()            // 도메인별 IPv6 주소(재부팅이나 agent 교체 뒤 다시 붙인다)
	}
	if err := mgr.Init(ctx); err != nil {
		return err
	}
	// 노드 간 사설 네트워킹: AVEN_PRIVATE_ADDRESS(사설 IP):AVEN_MESH_PORT에서만 mTLS로 listen한다(AVEN_MESH=off로 끈다).
	if err := mgr.StartLocalMesh(ctx); err != nil {
		log.Warn("mesh gateway disabled", "err", err)
	} else if mgr.LocalMesh != nil {
		log.Info("mesh gateway enabled", "listen", mgr.LocalMesh.Listen)
	}
	go mgr.ReconcileLoop(ctx, 30*time.Second)
	go mgr.EgressLoop(ctx, 30*time.Second)  // 아웃바운드 IPv6 차단 규칙(outbound_ipv6=false)을 컨테이너 주소에 맞춘다
	go mgr.DomainIPv6Loop(ctx, time.Minute) // 도메인별 공개 IPv6(노드 풀이 있을 때만): 시작할 때, 도메인 변경 때, 60초마다
	// DNS 조회/연결(flow) 로그: 환경 bridge의 패킷 헤더를 관찰한다(CAP_NET_RAW 필요, AVEN_NETWORK_LOGS=off로 끈다).
	if !strings.EqualFold(strings.TrimSpace(env("NETWORK_LOGS", "on")), "off") {
		mgr.StartLocalNetworkLogs(ctx)
	}
	go mgr.WatchCrashes(ctx)
	go mgr.CronLoop(ctx)
	go mgr.ImageUpdateLoop(ctx)
	// wake handler: keel-proxy와 같이 쓰는 볼륨의 unix socket에서만 listen한다(외부에 열지 않음).
	if wl, err := listenWake(ctx, docker, caddy); err != nil {
		log.Warn("wake handler disabled (sleeping services cannot wake)", "err", err)
	} else {
		mgr.Cfg.WakeAddr = proxy.WakeDial
		ws := &http.Server{Handler: mgr.WakeHandler(), ReadHeaderTimeout: 10 * time.Second}
		go ws.Serve(wl)
		go func() { <-ctx.Done(); ws.Close() }()
		log.Info("wake handler listening", "socket", proxy.WakeSocket)
	}
	go mgr.SleepLoop(ctx) // 로컬(wake handler가 있을 때)과 원격 노드의 sleeping 판정
	go caddy.FollowAccessLogs(ctx, mgr.HTTPLogs.Add)
	collector := &metrics.Collector{Store: st, Docker: docker, Log: log, DataDir: dataDir, Events: bus, RemoteNodes: func(ctx context.Context) []metrics.NodeSource {
		var out []metrics.NodeSource
		for _, id := range mgr.OnlineRemoteNodeIDs(ctx) {
			id := id
			out = append(out, metrics.NodeSource{ID: id, Stats: func(ctx context.Context) (metrics.NodeStat, error) {
				s, err := mgr.NodeStats(ctx, id)
				return metrics.NodeStat{CPUPercent: s.CPUPercent, MemoryBytes: s.MemoryBytes, DiskFreeBytes: s.DiskFreeBytes}, err
			}})
		}
		return out
	}, Remote: func(ctx context.Context) []metrics.Source {
		var out []metrics.Source
		for _, d := range mgr.OnlineRemoteDrivers(ctx) {
			out = append(out, d)
		}
		return out
	}}
	go collector.Run(ctx)
	// 지표 알림 규칙(30초마다)과 시간별 사용량 집계(매시, 놓친 시간은 보관 중인 지표로 채운다).
	sources := &alerts.Sources{Store: st, HTTP: mgr.HTTPLogs.Window, Restarts: mgr.InstanceRestarts, VolumeSize: collector.VolumeSize}
	go (&alerts.Evaluator{Store: st, Events: bus, Log: log, Value: sources.Value}).Run(ctx)
	// 수평 자동 확장(30초마다, 최근 2분 지표 평균으로 HPA 방식 계산).
	go mgr.AutoscaleLoop(ctx)
	go (&usage.Roller{Store: st, Events: bus, Log: log, Instances: mgr}).Run(ctx)
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			st.PruneEvents(ctx, time.Now().Add(-30*24*time.Hour))
			st.PruneAudit(ctx, time.Now().Add(-365*24*time.Hour)) // 감사 기록은 1년 보관
			st.PruneSessions(ctx)                                 // 만료된 세션과 초대
			st.PruneOAuth(ctx)                                    // 만료된 OAuth 코드, 토큰
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	hostname, _ := os.Hostname()
	traceReceiver := &tracing.Receiver{Store: st, ProxyToken: proxyTraceToken, Log: log}
	srv := &api.Server{
		Store: st, Agents: hub, Deploy: mgr, Events: bus, Metrics: collector, AdminPassword: password, APIToken: compat.Env("API_TOKEN"),
		AllowFileRepo: compat.Env("ALLOW_FILE_REPO") != "",
		SessionKey:    key, SecureCookie: compat.Env("INSECURE_COOKIE") == "",
		Web: webui.FS(), Version: version, NodeName: hostname, Log: log,
		GitHubSecret: compat.Env("GITHUB_WEBHOOK_SECRET"), GitHubAPIBase: env("GITHUB_API_URL", "https://api.github.com"),
		GitHubClientID: compat.Env("GITHUB_CLIENT_ID"), GitHubClientSecret: compat.Env("GITHUB_CLIENT_SECRET"), PublicURL: compat.Env("PUBLIC_URL"),
		AgentBinDir: compat.Env("AGENT_BIN_DIR"),
		Notifier:    notifier, Pricing: pricing,
		Traces: traceReceiver,
	}
	go tracing.RetentionLoop(ctx, st, traceRetention(log), log)
	trusted, err := parsePrefixes(compat.Env("TRUSTED_PROXIES"))
	if err != nil {
		return fmt.Errorf("%s: %w", compat.Describe("TRUSTED_PROXIES"), err)
	}
	srv.TrustedProxies = trusted
	go srv.RunHA(ctx) // 데이터베이스 HA 클러스터 감시와 자동 failover(10초마다)
	// 공개 도메인 상태 확인(DNS 분류, Cloudflare/터널 감지, 소유 확인, 인증서): 60초마다, 검증된 도메인은 최대 10분까지 간격을 늘린다.
	domainChecker := &netcheck.Checker{Store: st, Targets: srv.NetCheckTargets, CheckCerts: true, Log: log}
	// 확인 전 신규 도메인이 검증되면 라우트를 다시 보내 ACME 보류(on-demand)를 풀고 관리 정책으로 옮긴다(원격 노드 포함).
	domainChecker.OnVerified = func(host string) {
		log.Info("domain verified; resyncing proxy", "host", host)
		go func() {
			if err := mgr.SyncProxy(context.WithoutCancel(ctx)); err != nil {
				log.Warn("proxy sync after domain verification", "host", host, "err", err)
			}
		}()
	}
	srv.SetNetCheck(domainChecker)
	go domainChecker.Run(ctx)
	// 옛 Bearer 비밀키 agent는 기본 허용(첫 인증서 연결 때 자동 업그레이드).
	srv.CA = agentCA
	srv.DisableLegacyAgents = compat.Env("AGENT_LEGACY_BEARER") == "off"
	// AVEN_SUPERADMINS=a@x,b@y: 모든 작업 공간에서 owner인 계정(작업 공간 격리를 넘는다).
	srv.SuperAdmins = map[string]bool{}
	for _, e := range strings.Split(compat.Env("SUPERADMINS"), ",") {
		if e = store.NormalizeEmail(e); e != "" {
			srv.SuperAdmins[e] = true
			log.Info("superadmin", "email", e)
		}
	}
	// 새로 설치한 서버면 aven-server 자신을 캔버스에서 다룰 Aven 프로젝트를 만들어 둔다(docs/selfhost.md).
	if err := srv.EnsureSelfProjectOnFirstRun(ctx); err != nil {
		log.Warn("self project", "err", err)
	}
	handler := srv.Handler()
	go srv.SandboxLoop(ctx)
	go srv.SelfUpdateLoop(ctx) // aven-server(self 서비스) 저장소의 새 커밋 확인(30분마다)
	hs := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	// OTLP 수신기(OTLP/HTTP + h2c OTLP/gRPC). 컨테이너가 host-gateway로 닿아야 하므로 기본은 모든 주소다.
	// 요청마다 trace 토큰을 확인한다. 바깥에 열고 싶지 않으면 AVEN_OTLP_LISTEN을 docker bridge 주소로 좁히거나 off로 끈다.
	traceServer := &http.Server{Handler: traceReceiver, ReadHeaderTimeout: 10 * time.Second}
	traceServer.Protocols = new(http.Protocols)
	traceServer.Protocols.SetHTTP1(true)
	traceServer.Protocols.SetUnencryptedHTTP2(true)
	if addr := env("OTLP_LISTEN", ":4318"); addr != "off" {
		if traceListener, err := net.Listen("tcp", addr); err != nil {
			log.Error("OTLP receiver disabled", "addr", addr, "err", err)
		} else {
			log.Info("OTLP receiver listening", "addr", traceListener.Addr().String())
			go func() {
				if err := traceServer.Serve(traceListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Error("OTLP receiver stopped", "err", err)
				}
			}()
		}
	}
	// AVEN_LISTEN_UNIX: TLS를 끝내는 gateway(reverse proxy)와만 공유하는 unix socket. 이 listener로 온 요청만
	// X-Forwarded-Proto를 믿는다(TCP listener는 AVEN_TRUSTED_PROXIES 설정을 그대로 따른다).
	if sock := compat.Env("LISTEN_UNIX"); sock != "" {
		ul, err := listenUnix(sock)
		if err != nil {
			return fmt.Errorf("%s: %w", compat.Describe("LISTEN_UNIX"), err)
		}
		// AVEN_GATEWAY_SECRET_FILE: gateway(Traefik middleware)가 모든 요청에 붙이는 X-Keel-Gateway 비밀. 맞는 요청만 전달 헤더를 믿는다.
		gwSecret := ""
		if f := compat.Env("GATEWAY_SECRET_FILE"); f != "" {
			if gwSecret, err = gatewaySecret(f); err != nil {
				return fmt.Errorf("%s: %w", compat.Describe("GATEWAY_SECRET_FILE"), err)
			}
		} else {
			log.Warn(compat.Describe("LISTEN_UNIX") + " without " + compat.Describe("GATEWAY_SECRET_FILE") + ": every peer that can reach the socket is trusted for X-Forwarded-* headers")
		}
		us := &http.Server{Handler: api.ViaGateway(gwSecret, handler), ReadHeaderTimeout: 10 * time.Second}
		go us.Serve(ul)
		go func() {
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			us.Shutdown(shutdown)
		}()
		log.Info("aven-server listening (trusted gateway socket)", "socket", sock)
	}
	if selfListenPath != "" {
		sl, err := listenUnix(selfListenPath)
		if err != nil {
			return fmt.Errorf("self route socket: %w", err)
		}
		ss := &http.Server{Handler: api.ViaGateway(selfSecret, handler), ReadHeaderTimeout: 10 * time.Second}
		go ss.Serve(sl)
		go func() {
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ss.Shutdown(shutdown)
		}()
		log.Info("aven-server listening for the proxy (own public host)", "host", selfHost, "socket", selfListenPath)
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	// 원격 agent의 enroll/connect는 TLS가 필요하다. 직접 TLS로 열거나(AVEN_TLS_CERT/AVEN_TLS_KEY),
	// TLS를 끝내는 reverse proxy 뒤라면 AVEN_TRUSTED_PROXIES로 그 proxy만 믿는다.
	certFile, keyFile := compat.Env("TLS_CERT"), compat.Env("TLS_KEY")
	if (certFile == "") != (keyFile == "") {
		return errors.New(compat.Describe("TLS_CERT") + " and " + compat.Describe("TLS_KEY") + " must be set together")
	}
	if certFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return fmt.Errorf("load TLS certificate: %w", err)
		}
		hs.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		ln = tls.NewListener(ln, hs.TLSConfig)
	}
	log.Info("aven-server listening", "addr", ln.Addr().String(), "version", version, "tls", certFile != "", "trusted_proxies", len(trusted))
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hs.Shutdown(shutdown)
		traceServer.Shutdown(shutdown)
	}()
	if err := hs.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// parsePrefixes는 쉼표로 구분한 IP/CIDR 목록을 읽는다(단일 IP는 /32, /128).
func parsePrefixes(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if p, err := netip.ParsePrefix(f); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(f)
		if err != nil {
			return nil, fmt.Errorf("invalid address %q", f)
		}
		out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
	}
	return out, nil
}

// gatewaySecret은 gateway 비밀을 읽는다(없으면 32바이트 난수를 0600으로 만든다). 32자 미만이면 거부한다.
func gatewaySecret(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		v := strings.TrimSpace(string(b))
		if len(v) < 32 {
			return "", errors.New("secret is shorter than 32 characters")
		}
		return v, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	v, err := agentprotocol.Secret()
	if err != nil {
		return "", err
	}
	return v, os.WriteFile(path, []byte(v+"\n"), 0o600)
}

func sessionKey(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
		return b, nil
	}
	b := make([]byte, 32)
	rand.Read(b)
	return b, os.WriteFile(path, b, 0o600)
}

// publicIPs는 외부로 나가는 경로의 출발 주소를 찾는다(패킷은 보내지 않는다). 사설/ULA 주소면 비운다.
func publicIPs() (v4, v6 string) {
	probe := func(network, addr string) string {
		c, err := net.Dial(network, addr)
		if err != nil {
			return ""
		}
		defer c.Close()
		ip := c.LocalAddr().(*net.UDPAddr).IP
		if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || (ip.To4() != nil && ip[len(ip)-4] == 100 && ip[len(ip)-3]&0xc0 == 64) {
			return ""
		}
		return ip.String()
	}
	return probe("udp4", "1.1.1.1:53"), probe("udp6", "[2606:4700:4700::1111]:53")
}

func findRailpack() string {
	if p, err := exec.LookPath("railpack"); err == nil {
		return p
	}
	exe, _ := os.Executable()
	for _, c := range []string{filepath.Join(filepath.Dir(exe), "railpack"), filepath.Join(filepath.Dir(exe), "..", ".tools", "railpack"), "/usr/local/bin/railpack"} {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

func nodeHostname() string {
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return ""
}

// listenUnix는 gateway용 unix socket을 만든다(root 전용 0600; 이전 실행의 socket 파일은 지운다).
// selfSocket은 aven-proxy와 공유하는 볼륨 안에서 aven-server가 자기 공개 호스트 요청을 받는 socket이다.
// 예전 keel-proxy 레이아웃에서는 legacySelfSocket을 그대로 쓴다.
const selfSocket = "aven-server.sock"
const legacySelfSocket = "keel-server.sock"

// selfListenPath는 selfSocket의 호스트 경로다(자기 라우트를 쓸 때만).
var selfListenPath string

// selfRoute는 AVEN_PUBLIC_URL의 호스트와 keel-proxy가 붙일 비밀을 정한다. AVEN_SELF_ROUTE=off이거나 https URL이 아니면 쓰지 않는다.
// 비밀은 AVEN_GATEWAY_SECRET_FILE이 있으면 그것, 없으면 실행마다 새로 만든다(proxy 설정은 시작할 때 다시 넣는다).
func selfRoute() (host, secret string, err error) {
	if strings.EqualFold(compat.Env("SELF_ROUTE"), "off") {
		return "", "", nil
	}
	u, err := url.Parse(compat.Env("PUBLIC_URL"))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return "", "", nil
	}
	if f := compat.Env("GATEWAY_SECRET_FILE"); f != "" {
		if secret, err = gatewaySecret(f); err != nil {
			return "", "", fmt.Errorf("%s: %w", compat.Describe("GATEWAY_SECRET_FILE"), err)
		}
	} else {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return "", "", err
		}
		secret = hex.EncodeToString(b)
	}
	return strings.ToLower(u.Hostname()), secret, nil
}

func listenUnix(path string) (net.Listener, error) {
	if st, err := os.Lstat(path); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		os.Remove(path)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

func listenWake(ctx context.Context, docker *node.Docker, caddy *proxy.Caddy) (net.Listener, error) {
	mp, err := docker.VolumeMountpoint(ctx, caddy.Resolve(ctx).WakeVolume)
	if err != nil {
		return nil, err
	}
	sock := filepath.Join(mp, "wake.sock")
	os.Remove(sock)
	l, err := net.Listen("unix", sock)
	if err != nil {
		return nil, err
	}
	// keel-proxy(root)와 aven-server(root)만 wake socket에 접근한다.
	if err := os.Chmod(sock, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}
