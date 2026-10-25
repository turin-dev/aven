// Package api는 aven의 HTTP API와 웹 UI 서빙을 담당한다. 계약은 docs/api-v1.md에 있다.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/turin-dev/aven/internal/agentpki"
	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/events"
	"github.com/turin-dev/aven/internal/metrics"
	"github.com/turin-dev/aven/internal/notify"
	"github.com/turin-dev/aven/internal/store"
	"github.com/turin-dev/aven/internal/tracing"
	"github.com/turin-dev/aven/internal/usage"
)

type Server struct {
	Store         *store.Store
	Agents        *agentprotocol.Hub
	Deploy        *deploy.Manager
	Events        *events.Bus
	Metrics       *metrics.Collector
	AdminPassword string
	APIToken      string // 비어 있으면 Bearer 인증을 쓰지 않음
	AllowFileRepo bool   // 개발용: file:// 저장소 허용
	SessionKey    []byte
	SecureCookie  bool
	Web           fs.FS
	Version       string
	NodeName      string
	Log           *slog.Logger
	GitHubSecret  string
	GitHubAPIBase string // defaults to github.com's REST API
	// GitHub OAuth App(대시보드의 "GitHub 연결"). 둘 다 있어야 연결 버튼이 동작한다.
	GitHubClientID     string
	GitHubClientSecret string
	GitHubOAuthBase    string // 기본 https://github.com (테스트용)
	ghStates           githubStates
	PublicURL          string // webhook URL 안내용(예: https://aven.example.com)
	// TrustedProxies는 X-Forwarded-Proto를 믿을 reverse proxy 주소 대역이다(agent 연결의 TLS 판단용). 비우면 믿지 않는다.
	TrustedProxies []netip.Prefix
	// AgentBinDir는 /downloads/aven-agent-linux-<arch>로 내보낼 바이너리 디렉터리다(비우면 aven-server 실행 파일 옆).
	AgentBinDir string

	// CA는 agent 인증서를 발급/검증하는 aven 내부 CA다(nil이면 agent enroll/connect는 503).
	CA *agentpki.CA
	// SuperAdmins는 모든 작업 공간(남의 개인·팀 작업 공간 포함)에서 owner인 계정의 이메일이다(KEEL_SUPERADMINS, 정규화된 소문자).
	// 작업 공간 격리를 넘으므로 운영자가 환경변수로만 정한다. API 토큰·OAuth로 오면 그 토큰의 범위·역할 상한은 그대로다.
	SuperAdmins map[string]bool
	// DisableLegacyAgents가 true면 옛 Bearer 비밀키 연결을 거부한다(인증서로 업그레이드하지 않은 agent는 접속 불가).
	DisableLegacyAgents bool

	// Notifier는 알림 채널 전송(시험 전송 포함)과 메일(Notifier.Mailer)이다. nil이면 시험 전송·메일은 503.
	Notifier *notify.Notifier
	// Pricing이 있으면 사용량 응답에 추정 비용을 붙인다(KEEL_PRICE_*).
	Pricing *usage.Pricing
	// Traces는 OTLP 수신기다. 컨테이너가 KEEL_OTLP_LISTEN 대신 API 주소(KEEL_PUBLIC_URL)로 보낼 수도 있게 /v1/traces에도 단다.
	Traces *tracing.Receiver

	dns     dnsCache
	limOnce sync.Once
	limits  *limits
	// envOps가 있으면 환경 docker 리소스 작업(네트워크, 삭제)을 Deploy 대신 이것으로 한다(테스트용).
	envOps envOps
	// execImpl이 있으면 셸 세션을 Deploy 대신 이것으로 연다(테스트용). execIdle은 유휴 제한(0이면 30분).
	execImpl execer
	execIdle time.Duration
	execs    execLimiter
	// dataExec이 있으면 데이터 브라우저 명령을 Deploy 대신 이것으로 실행한다(테스트용).
	dataExec  oneShotExecer
	dataSlots execLimiter
	// pg*는 PostgreSQL PgBouncer/메이저 업그레이드 작업 상태다(postgres.go). pgOps가 있으면 Deploy 대신 쓴다(테스트용).
	pgMu   sync.Mutex
	pgBusy map[string]string
	pgOps  postgresOps
	// sbxImpl이 있으면 샌드박스 컨테이너 작업을 Deploy 대신 이것으로 한다(테스트용).
	sbxImpl sandboxBackend
	// netImpl이 있으면 networking 화면의 노드 조회(포트 감지 등)를 Deploy 대신 이것으로 한다(테스트용).
	netImpl networkingBackend

	// routes는 Handler()가 등록한 인증 라우트와 권한 선언이다(Routes()).
	routes []Route
	// restartFn이 있으면 owner의 재시작 요청 때 SIGTERM 대신 이것을 부른다(테스트용).
	restartFn func()

	// authzProbe가 true면(테스트) 권한 검사를 통과한 요청에 핸들러 대신 299를 돌려준다.
	authzProbe bool
}

type envOps interface {
	EnsureEnvironment(ctx context.Context, env *store.Environment) error
	DeleteEnvironment(ctx context.Context, env *store.Environment) error
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	if s.Deploy != nil {
		s.Deploy.EnqueueHook = s.auditBorrowedGitHub
	}
	// OAuth 2.0 / OpenID Connect provider (oauth.go). /oauth/consent/는 웹 UI 페이지다.
	mux.HandleFunc("GET /.well-known/openid-configuration", oauthCORS(s.oauthDiscovery))
	mux.HandleFunc("GET /oauth/.well-known/openid-configuration", oauthCORS(s.oauthDiscovery))
	mux.HandleFunc("GET /oauth/jwks", oauthCORS(s.oauthJWKS))
	mux.HandleFunc("GET /oauth/auth", s.oauthAuthorize)
	mux.HandleFunc("GET /oauth/authorize", s.oauthAuthorize)
	mux.HandleFunc("/oauth/token", oauthCORS(oauthMethod("POST", s.oauthToken)))
	mux.HandleFunc("/oauth/me", oauthCORS(oauthMethod("GET, POST", s.oauthUserInfo)))
	mux.HandleFunc("/oauth/userinfo", oauthCORS(oauthMethod("GET, POST", s.oauthUserInfo)))
	mux.HandleFunc("/oauth/revoke", oauthCORS(oauthMethod("POST", s.oauthRevoke)))
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("POST /api/auth/logout", s.logout)
	mux.HandleFunc("POST /api/auth/view-as/stop", s.stopViewAs)
	mux.HandleFunc("GET /api/auth/saml/status", s.samlStatus)
	mux.HandleFunc("GET /api/auth/saml/login", s.samlLogin)
	mux.HandleFunc("POST /api/auth/saml/acs", s.samlACS)
	mux.HandleFunc("GET /api/auth/saml/metadata", s.samlMetadata)
	mux.HandleFunc("POST /api/auth/passkeys/login/begin", s.beginPasskeyLogin)
	mux.HandleFunc("POST /api/auth/passkeys/login/factor/{challenge}/finish", s.finishPasskeyFactorLogin)
	mux.HandleFunc("POST /api/auth/passkeys/login/discoverable/{challenge}/finish", s.finishPasskeyDiscoverableLogin)
	mux.HandleFunc("POST /api/webhooks/github", s.githubWebhook)
	// GitHub에서 돌아오는 cross-site 이동에는 SameSite=Strict 세션 쿠키가 붙지 않으므로 핸들러가 직접 세션을 확인한다
	// (없으면 같은 origin에서 한 번 다시 이동). 권한(작업 공간 admin, 세션만)은 githubOAuthReturn이 검사한다.
	mux.HandleFunc("GET /api/github/oauth/callback", s.githubOAuthReturn)
	mux.HandleFunc("POST /api/agent/enroll", s.enrollAgent)
	mux.HandleFunc("POST /api/agent/upgrade", s.upgradeAgent)
	mux.HandleFunc("GET /api/agent/connect", s.connectAgent)
	mux.HandleFunc("GET /install-agent.sh", s.installScript)
	mux.HandleFunc("GET /downloads/{file}", s.downloadAgent)
	mux.HandleFunc("GET /install.sh", s.installCLIScript)
	mux.HandleFunc("GET /api/cli/downloads", s.cliDownloads)

	mux.HandleFunc("POST /api/auth/accept-invite", s.acceptInvite)
	if s.Traces != nil {
		mux.Handle("POST /v1/traces", s.Traces)
	}

	// 모든 인증 라우트는 필요한 권한을 선언한다(authz.go). 프로젝트 라우트는 경로 변수로 리소스의 프로젝트/환경을 찾는다.
	s.routes = nil
	h := func(pattern string, a access, f http.HandlerFunc) { s.register(mux, pattern, a, f) }
	R, M, A := proj(lvViewer), proj(lvMember), proj(lvAdmin)
	instanceAdmin := instance(lvAdmin)

	// auth / account
	h("GET /api/auth/me", anyUser(), s.me)
	h("GET /api/feature-flags", anyUser(), s.projectFlagSnapshot)
	h("POST /api/feature-flags/evaluate", anyUser(), s.evaluateProjectFlag)
	h("PATCH /api/auth/me", selfOnly(), s.patchMe)
	h("GET /api/auth/sessions", selfOnly(), s.listMySessions)
	h("DELETE /api/auth/sessions/{sesid}", selfOnly(), s.deleteMySession)
	h("POST /api/auth/2fa/setup", selfOnly(), s.totpSetup)
	h("POST /api/auth/2fa/enable", selfOnly(), s.totpEnable)
	h("POST /api/auth/2fa/disable", selfOnly(), s.totpDisable)
	h("POST /api/auth/2fa/recovery-codes", selfOnly(), s.totpRecoveryCodes)
	h("GET /api/auth/passkeys", selfOnly(), s.listPasskeys)
	h("POST /api/auth/passkeys/register/begin", selfOnly(), s.beginPasskeyRegistration)
	h("POST /api/auth/passkeys/register/{id}/finish", selfOnly(), s.finishPasskeyRegistration)
	h("DELETE /api/auth/passkeys/{id}", selfOnly(), s.deletePasskey)
	h("GET /api/workspace/security", instanceAdmin, s.getWorkspaceSecurity)
	h("PATCH /api/workspace/security", instanceAdminNoToken(), s.patchWorkspaceSecurity)
	h("GET /api/tokens", selfOnly(), s.listTokens)
	h("POST /api/tokens", selfOnly(), s.createToken)
	h("DELETE /api/tokens/{tid}", selfOnly(), s.deleteToken)
	h("GET /api/oauth/apps", instanceAdminNoToken(), s.listOAuthApps)
	h("POST /api/oauth/apps", instanceAdminNoToken(), s.createOAuthApp)
	h("GET /api/oauth/apps/{appid}", instanceAdminNoToken(), s.getOAuthApp)
	h("PATCH /api/oauth/apps/{appid}", instanceAdminNoToken(), s.updateOAuthApp)
	h("DELETE /api/oauth/apps/{appid}", instanceAdminNoToken(), s.deleteOAuthApp)
	h("GET /api/oauth/apps/{appid}/secrets", instanceAdminNoToken(), s.listOAuthClientSecrets)
	h("POST /api/oauth/apps/{appid}/secrets", instanceAdminNoToken(), s.createOAuthClientSecret)
	h("DELETE /api/oauth/apps/{appid}/secrets/{secretid}", instanceAdminNoToken(), s.revokeOAuthClientSecret)
	h("GET /api/oauth/authorized-apps", selfOnly(), s.listOAuthAuthorizedApps)
	h("PATCH /api/oauth/authorized-apps/{grantid}", selfOnly(), s.patchOAuthGrant)
	h("DELETE /api/oauth/authorized-apps/{grantid}", selfOnly(), s.revokeOAuthGrant)
	h("GET /api/oauth/requests/{requestid}", selfOnly(), s.getOAuthConsentRequest)
	h("POST /api/oauth/requests/{requestid}", selfOnly(), s.finishOAuthConsent)

	// users / invites / audit (작업 공간)
	h("GET /api/users", instanceAdmin, s.listUsers)
	h("GET /api/users/{uid}", instanceAdmin, s.getUser)
	h("PATCH /api/users/{uid}", instanceAdminNoToken(), s.patchUser)
	h("DELETE /api/users/{uid}", instanceAdminNoToken(), s.deleteUser)
	h("GET /api/invites", instanceAdmin, s.listInvites)
	h("POST /api/invites", selfOnly(), s.createInvite) // 비admin은 자기가 admin인 프로젝트로만 초대
	h("DELETE /api/invites/{iid}", instanceAdminNoToken(), s.deleteInvite)
	h("GET /api/audit", instanceAdmin, s.listAudit)
	h("GET /api/saml/settings", instanceAdminNoToken(), s.getSAMLSettings)
	h("PUT /api/saml/settings", instanceAdminNoToken(), s.putSAMLSettings)
	h("POST /api/saml/domains/{host}/verify", instanceAdminNoToken(), s.verifySAMLDomain)
	// enterprise access controls(access_controls.go): 접근 그룹, guardrail 정책
	h("GET /api/access-controls", instanceAdmin, s.getAccessControlSettings)
	h("PATCH /api/access-controls", instanceAdminNoToken(), s.patchAccessControlSettings)
	h("GET /api/access-controls/policies", anyUser(), s.getAccessPolicies)
	h("GET /api/access-groups", ws(lvAdmin), s.listAccessGroups)
	h("POST /api/access-groups", wsAdminNoToken(), s.saveAccessGroup)
	h("PUT /api/access-groups/{gid}", wsAdminNoToken(), s.saveAccessGroup)
	h("DELETE /api/access-groups/{gid}", wsAdminNoToken(), s.deleteAccessGroup)
	h("GET /api/projects/{pid}/access-groups", proj(lvViewer), s.getProjectAccessGroups)
	h("PUT /api/projects/{pid}/access-groups", wsAdminNoToken(), s.putProjectAccessGroups)

	h("GET /api/system", anyUser(), s.system)
	h("GET /api/ai/settings", anyUser(), s.getAgentSettings)
	h("PUT /api/ai/settings", instanceAdmin, s.putAgentSettings)
	h("POST /api/ai/settings/test", instanceAdmin, s.testAgentSettings)
	h("GET /api/ai/threads", anyUser(), s.listAgentThreads)
	h("POST /api/ai/chat", anyUser(), s.agentChat)
	h("POST /api/ai/confirm", anyUser(), s.confirmAgentAction)
	h("GET /api/ai/threads/{id}", anyUser(), s.getAgentThread)
	h("DELETE /api/ai/threads/{id}", anyUser(), s.deleteAgentThread)
	h("GET /api/system/github", instanceAdmin, s.systemGitHub)
	// GitHub 연결은 사용자별이다(github_oauth.go): 누구나 자기 계정을 연결하고, 저장소 목록·웹훅 등록은 자기 토큰으로 한다.
	h("GET /api/github/connection", anyUser(), s.githubConnection)
	h("GET /api/github/oauth/start", selfOnly(), s.githubOAuthStart)
	h("DELETE /api/github/connection", selfOnly(), s.githubDisconnect)
	h("GET /api/github/repos", anyUser(), s.githubRepos)
	h("POST /api/github/webhooks", selfOnly(), s.githubEnsureWebhook)
	h("GET /api/nodes", anyUser(), s.listNodes)
	h("POST /api/nodes/join-tokens", instanceAdmin, s.createJoinToken)
	h("PATCH /api/nodes/{nid}", instanceAdmin, s.patchNode)
	h("DELETE /api/nodes/{nid}", instanceAdmin, s.deleteNode)
	h("GET /api/nodes/{nid}/metrics", anyUser(), s.nodeMetrics)
	h("POST /api/nodes/{nid}/drain", instanceAdmin, s.drainNode)

	// projects / environments
	// 작업 공간(workspaces.go). 권한은 핸들러가 작업 공간 멤버십으로 확인한다(멤버가 아니면 404).
	h("GET /api/workspaces", anyUser(), s.listWorkspaces)
	h("POST /api/workspaces", selfOnly(), s.createWorkspace)
	h("PATCH /api/workspaces/{wsid}", wsAdminNoToken(), s.patchWorkspace)
	h("DELETE /api/workspaces/{wsid}", access{Kind: accWorkspace, Level: lvOwner, NoToken: true}, s.deleteWorkspace)
	h("GET /api/workspaces/{wsid}/members", ws(lvViewer), s.listWorkspaceMembersAPI)
	h("POST /api/workspaces/{wsid}/members", wsAdminNoToken(), s.addWorkspaceMember)
	h("PATCH /api/workspaces/{wsid}/members/{uid}", wsAdminNoToken(), s.patchWorkspaceMember)
	h("DELETE /api/workspaces/{wsid}/members/{uid}", selfOnly(), s.removeWorkspaceMember)

	h("GET /api/workspaces/{wsid}", ws(lvViewer), s.getWorkspace)
	h("GET /api/workspaces/{wsid}/access-groups", ws(lvAdmin), s.listAccessGroups)
	h("POST /api/workspaces/{wsid}/access-groups", wsAdminNoToken(), s.saveAccessGroup)
	h("PUT /api/workspaces/{wsid}/access-groups/{gid}", wsAdminNoToken(), s.saveAccessGroup)
	h("DELETE /api/workspaces/{wsid}/access-groups/{gid}", wsAdminNoToken(), s.deleteAccessGroup)
	h("GET /api/workspaces/{wsid}/access-group-settings", ws(lvViewer), s.getWorkspaceGroupSettings)
	h("PATCH /api/workspaces/{wsid}/access-group-settings", wsAdminNoToken(), s.patchWorkspaceGroupSettings)
	h("POST /api/workspaces/{wsid}/invites", wsAdminNoToken(), s.createInvite)
	h("GET /api/instance/security", instanceAdmin, s.getWorkspaceSecurity)
	h("GET /api/instance/overview", instanceAdmin, s.getInstanceOverview)
	h("DELETE /api/instance/workspaces/{wsid}", access{Kind: accInstance, Level: lvAdmin, NoToken: true}, s.deleteInstanceWorkspace)
	h("GET /api/instance/resource-limits", instanceAdmin, s.listResourceLimits)
	h("PUT /api/instance/resource-limits/{wsid}", instanceAdminNoToken(), s.putResourceLimits)
	h("GET /api/workspaces/{wsid}/resource-limits", ws(lvViewer), s.getResourceLimits)
	h("PATCH /api/instance/security", instanceAdminNoToken(), s.patchWorkspaceSecurity)

	h("GET /api/projects", anyUser(), s.listProjects)
	h("POST /api/projects", anyUser(), s.createProject) // 대상 작업 공간 member 이상(createTargetWorkspace)
	h("PUT /api/projects/{pid}/workspace", projNoToken(lvAdmin), s.moveProject)
	h("PUT /api/projects/{pid}/github", projNoToken(lvAdmin), s.setProjectGitHub)
	h("GET /api/projects/{pid}", R, s.getProject)
	h("PATCH /api/projects/{pid}", A, s.updateProject)
	h("DELETE /api/projects/{pid}", A, s.deleteProject)
	h("GET /api/projects/{pid}/flags", R, s.listFeatureFlags)
	h("POST /api/projects/{pid}/flags", A, s.createFeatureFlag)
	h("PUT /api/projects/{pid}/flags/{name}", A, s.updateFeatureFlag)
	h("DELETE /api/projects/{pid}/flags/{name}", A, s.deleteFeatureFlag)
	h("GET /api/projects/{pid}/environments/{eid}/config", R, s.exportIACConfig)
	h("POST /api/projects/{pid}/environments/{eid}/config/plan", M, s.planIACConfig)
	h("POST /api/projects/{pid}/environments/{eid}/config/apply", M, s.applyIACConfig)
	h("GET /api/projects/{pid}/environments/{eid}/config/partials", R, s.listIACPartials)
	h("POST /api/projects/{pid}/environments/{eid}/config/partials/release", A, s.releaseIACPartial)
	h("POST /api/projects/{pid}/environments/{eid}/config/partials/transfer", A, s.transferIACPartial)
	h("GET /api/projects/{pid}/members", R, s.listMembers)
	h("PUT /api/projects/{pid}/members/{uid}", projNoToken(lvAdmin), s.putMember)
	h("DELETE /api/projects/{pid}/members/{uid}", projNoToken(lvAdmin), s.deleteMember)
	h("POST /api/projects/{pid}/environments", M, s.createEnvironment)
	h("GET /api/projects/{pid}/registries", A, s.getRegistries)
	h("PUT /api/projects/{pid}/registries", A, s.putRegistries)
	h("PATCH /api/environments/{eid}", M, s.updateEnvironment)
	h("DELETE /api/environments/{eid}", A, s.deleteEnvironment)
	h("GET /api/environments/{eid}/canvas", R, s.canvas)
	h("GET /api/environments/{eid}/sandboxes", R, s.listSandboxes)
	h("POST /api/environments/{eid}/sandboxes", M, s.createSandbox)
	h("GET /api/environments/{eid}/sandbox-checkpoints", R, s.listSandboxCheckpoints)
	h("GET /api/sandboxes/{sbid}", R, s.showSandbox)
	h("DELETE /api/sandboxes/{sbid}", M, s.destroySandbox)
	h("POST /api/sandboxes/{sbid}/fork", M, s.forkSandbox)
	h("POST /api/sandboxes/{sbid}/heartbeat", M, s.sandboxHeartbeat)
	h("POST /api/sandboxes/{sbid}/checkpoints", M, s.createSandboxCheckpoint)
	h("POST /api/sandboxes/{sbid}/exec", M, s.sandboxExec)
	h("GET /api/sandboxes/{sbid}/shell", M, s.sandboxShell)
	h("GET /api/sandboxes/{sbid}/files", M, s.readSandboxFile)
	h("PUT /api/sandboxes/{sbid}/files", M, s.writeSandboxFile)
	h("GET /api/sandboxes/{sbid}/files/list", M, s.listSandboxFiles)
	h("PATCH /api/sandbox-checkpoints/{cpid}", M, s.renameSandboxCheckpoint)
	h("DELETE /api/sandbox-checkpoints/{cpid}", M, s.deleteSandboxCheckpoint)
	h("GET /api/environments/{eid}/groups", R, s.listGroups)
	h("POST /api/environments/{eid}/groups", M, s.createGroup)
	h("PATCH /api/environments/{eid}/groups/{gid}", M, s.patchGroup)
	h("DELETE /api/environments/{eid}/groups/{gid}", M, s.deleteGroup)
	h("GET /api/environments/{eid}/shared-variables", R, s.getSharedVars)
	h("PUT /api/environments/{eid}/shared-variables", M, s.putSharedVars)
	h("GET /api/environments/{eid}/logs", R, s.envLogs)
	h("GET /api/environments/{eid}/dns-logs", R, s.envDNSLogs)
	h("GET /api/environments/{eid}/network-logs", R, s.envNetworkLogs)
	h("GET /api/environments/{eid}/private-dns/available", R, s.envPrivateDNSAvailable)
	h("GET /api/environments/{eid}/metrics", R, s.envMetrics)
	h("GET /api/environments/{eid}/tracing", R, s.listTracingSettings)
	h("PUT /api/environments/{eid}/tracing", M, s.putEnvTracing)
	h("GET /api/environments/{eid}/trace-attributes", R, s.traceAttributeKeys)
	h("GET /api/environments/{eid}/traces", R, s.listTraces)
	h("GET /api/environments/{eid}/traces/{id}", R, s.getTrace)
	h("POST /api/environments/{eid}/volumes", M, s.createVolume)
	h("GET /api/environments/{eid}/sync-preview", R, s.syncPreview)
	h("POST /api/environments/{eid}/sync", M, s.syncEnvironment)

	// compose stacks
	h("POST /api/projects/{pid}/environments/{eid}/compose/preview", M, s.composePreview)
	h("POST /api/projects/{pid}/environments/{eid}/compose/apply", M, s.composeApply)
	h("GET /api/projects/{pid}/environments/{eid}/stacks", R, s.listStacks)
	h("GET /api/projects/{pid}/environments/{eid}/stacks/{stid}", R, s.getStack)
	h("DELETE /api/projects/{pid}/environments/{eid}/stacks/{stid}", M, s.deleteStack)
	h("POST /api/projects/{pid}/environments/{eid}/stacks/{stid}/redeploy", M, s.redeployStack)

	// services / instances
	h("POST /api/projects/{pid}/services", M, s.createService)
	h("PATCH /api/services/{sid}", M, s.updateService)
	h("DELETE /api/services/{sid}", M, s.deleteService)
	h("GET /api/services/{sid}/deploy-key", R, s.getDeployKey)
	h("POST /api/services/{sid}/deploy-key", M, s.createDeployKey)
	h("DELETE /api/services/{sid}/deploy-key", M, s.deleteDeployKey)
	h("GET /api/services/{sid}/instances/{eid}", R, s.getInstance)
	h("GET /api/services/{sid}/instances/{eid}/admin", R, s.getServiceAdmin)                        // 서버 관리자 전용(핸들러가 확인)
	h("POST /api/services/{sid}/admin/holds", projNoToken(lvViewer), s.createServiceHold)           // 서버 관리자 전용
	h("DELETE /api/services/{sid}/admin/holds/{scope}", projNoToken(lvViewer), s.deleteServiceHold) // 서버 관리자 전용
	h("PATCH /api/services/{sid}/instances/{eid}", M, s.patchInstance)
	h("GET /api/services/{sid}/instances/{eid}/edge", R, s.getEdgeSettings)
	h("PATCH /api/services/{sid}/instances/{eid}/edge", M, s.patchEdgeSettings)
	h("PUT /api/services/{sid}/instances/{eid}/edge/rules", M, s.putEdgeRules)
	h("POST /api/services/{sid}/instances/{eid}/edge/cdn/purge", M, s.purgeEdgeCache)
	h("POST /api/services/{sid}/instances/{eid}/edge/waf/under-attack", M, s.enableUnderAttack)
	h("DELETE /api/services/{sid}/instances/{eid}/edge/waf/under-attack", M, s.disableUnderAttack)
	h("PUT /api/services/{sid}/instances/{eid}/canvas", M, s.putCanvas)
	h("GET /api/services/{sid}/instances/{eid}/private-dns/available", R, s.privateDNSAvailable)
	h("GET /api/services/{sid}/instances/{eid}/function", M, s.getFunction)
	h("PUT /api/services/{sid}/instances/{eid}/function", M, s.putFunction)
	h("GET /api/services/{sid}/instances/{eid}/variables/resolved", R, s.resolvedVars)
	h("GET /api/services/{sid}/instances/{eid}/variables/raw", M, s.getRawVars)
	h("PUT /api/services/{sid}/instances/{eid}/variables/raw", M, s.putRawVars)
	h("POST /api/services/{sid}/instances/{eid}/domains", M, s.addDomain)
	h("PATCH /api/services/{sid}/instances/{eid}/domains/{host}", M, s.patchDomain)
	h("DELETE /api/services/{sid}/instances/{eid}/domains/{host}", M, s.deleteDomain)
	h("POST /api/services/{sid}/instances/{eid}/domains/{host}/verify", M, s.verifyDomain)
	h("GET /api/services/{sid}/instances/{eid}/db-backups", R, s.listDBBackups)
	h("POST /api/services/{sid}/instances/{eid}/db-backups", M, s.createDBBackup)
	h("GET /api/services/{sid}/instances/{eid}/postgres/pooling", R, s.getPostgresPooling)
	h("PUT /api/services/{sid}/instances/{eid}/postgres/pooling", M, s.putPostgresPooling)
	h("DELETE /api/services/{sid}/instances/{eid}/postgres/pooling", M, s.deletePostgresPooling)
	h("GET /api/services/{sid}/instances/{eid}/postgres/upgrade", R, s.getPostgresUpgrade)
	h("GET /api/services/{sid}/instances/{eid}/postgres/upgrade/preflight", R, s.postgresUpgradePreflight)
	h("POST /api/services/{sid}/instances/{eid}/postgres/upgrade", M, s.startPostgresUpgrade)
	h("POST /api/services/{sid}/instances/{eid}/postgres/upgrade/revert", M, s.revertPostgresUpgrade)
	h("GET /api/services/{sid}/instances/{eid}/db-backups/config", R, s.getDBBackupConfig)
	h("PUT /api/services/{sid}/instances/{eid}/db-backups/config", M, s.putDBBackupConfig)
	h("GET /api/services/{sid}/instances/{eid}/pitr", R, s.getPITR)
	h("PUT /api/services/{sid}/instances/{eid}/pitr", M, s.putPITR)
	h("POST /api/services/{sid}/instances/{eid}/pitr/base", M, s.createPITRBase)
	h("POST /api/services/{sid}/instances/{eid}/pitr/restore", M, s.restorePITR)
	h("GET /api/services/{sid}/instances/{eid}/pitr/restore", R, s.getPITRRestore)
	h("DELETE /api/services/{sid}/instances/{eid}/pitr/archive", M, s.deletePITRArchive)
	h("GET /api/db-backups/{id}", R, s.getDBBackup)
	h("DELETE /api/db-backups/{id}", M, s.deleteDBBackup)
	h("GET /api/db-backups/{id}/download", M, s.downloadDBBackup)
	h("POST /api/db-backups/{id}/restore", M, s.restoreDBBackup)
	h("GET /api/db-backups/{id}/restore", R, s.getDBRestore)
	h("GET /api/backup-destinations", instanceAdmin, s.listDestinations)
	h("POST /api/backup-destinations", instanceAdmin, s.createDestination)
	h("PATCH /api/backup-destinations/{id}", instanceAdmin, s.patchDestination)
	h("DELETE /api/backup-destinations/{id}", instanceAdmin, s.deleteDestination)
	h("POST /api/backup-destinations/{id}/test", instanceAdmin, s.testDestination)
	h("POST /api/services/{sid}/instances/{eid}/tcp-proxy", M, s.addTCPProxy)
	h("DELETE /api/services/{sid}/instances/{eid}/tcp-proxy", M, s.deleteTCPProxy)
	h("PATCH /api/services/{sid}/instances/{eid}/tcp-proxy", M, s.patchTCPProxy)
	h("GET /api/services/{sid}/instances/{eid}/tcp-proxy", R, s.getTCPProxy)
	h("GET /api/services/{sid}/instances/{eid}/networking", R, s.getNetworking)
	h("POST /api/services/{sid}/instances/{eid}/deploy", M, s.deployInstance)
	h("POST /api/services/{sid}/instances/{eid}/restart", M, s.restartInstance)
	h("POST /api/services/{sid}/instances/{eid}/scale", M, s.scaleInstance)
	h("POST /api/services/{sid}/instances/{eid}/stop", M, s.stopInstance)
	h("GET /api/services/{sid}/instances/{eid}/deployments", R, s.listDeployments)
	h("GET /api/services/{sid}/instances/{eid}/image-updates", R, s.listImageUpdates)
	h("POST /api/services/{sid}/instances/{eid}/image-updates/check", M, s.checkImageUpdates)
	h("POST /api/services/{sid}/instances/{eid}/image-updates/{uid}/apply", M, s.applyImageUpdate)
	h("POST /api/services/{sid}/instances/{eid}/image-updates/{uid}/skip", M, s.skipImageUpdate)
	h("POST /api/services/{sid}/instances/{eid}/cron/run", M, s.runCron)
	h("GET /api/services/{sid}/instances/{eid}/http-logs", R, s.httpLogs)
	h("GET /api/services/{sid}/instances/{eid}/dns-logs", R, s.serviceDNSLogs)
	h("GET /api/services/{sid}/instances/{eid}/network-logs", R, s.serviceNetworkLogs)
	h("POST /api/services/{sid}/instances/{eid}/network-diagnostics", M, s.networkDiagnostics)
	h("GET /api/services/{sid}/instances/{eid}/tracing", R, s.getTracingSetting)
	h("PUT /api/services/{sid}/instances/{eid}/tracing", M, s.putTracingSetting)
	h("GET /api/services/{sid}/instances/{eid}/metrics", R, s.instanceMetrics)
	h("POST /api/services/{sid}/instances/{eid}/upload", M, s.upload)
	h("GET /api/services/{sid}/instances/{eid}/exec", M, s.execShell)
	h("GET /api/services/{sid}/instances/{eid}/data/tables", M, s.dataTables)
	h("GET /api/services/{sid}/instances/{eid}/data/tables/{name}", M, s.dataTableRows)
	h("GET /api/services/{sid}/instances/{eid}/data/keys", M, s.dataKeys)
	h("GET /api/services/{sid}/instances/{eid}/data/keys/{key}", M, s.dataKey)
	h("POST /api/services/{sid}/instances/{eid}/data/query", M, s.dataQuery)
	h("PATCH /api/services/{sid}/instances/{eid}/data/tables/{name}/rows", M, s.dataRowUpdate)
	h("POST /api/services/{sid}/instances/{eid}/data/tables/{name}/rows", M, s.dataRowInsert)
	h("DELETE /api/services/{sid}/instances/{eid}/data/tables/{name}/rows", M, s.dataRowDelete)
	// bucket 브라우저(bucket.go): 보기는 viewer(자격 증명은 가림), 내려받기/올리기/지우기는 member
	h("GET /api/services/{sid}/instances/{eid}/bucket", R, s.bucketInfo)
	h("GET /api/services/{sid}/instances/{eid}/bucket/objects", R, s.bucketObjects)
	h("PUT /api/services/{sid}/instances/{eid}/bucket/objects", M, s.bucketUpload)
	h("GET /api/services/{sid}/instances/{eid}/bucket/objects/{key...}", M, s.bucketDownload)
	h("DELETE /api/services/{sid}/instances/{eid}/bucket/objects/{key...}", M, s.bucketDelete)
	// 데이터베이스 고가용성(ha.go)
	s.haRoutes(h, R, M)

	// deployments
	h("GET /api/deployments/{did}", R, s.getDeployment)
	h("POST /api/deployments/{did}/redeploy", M, s.redeploy)
	h("POST /api/deployments/{did}/rollback", M, s.rollback)
	h("POST /api/deployments/{did}/cancel", M, s.cancelDeployment)
	h("GET /api/deployments/{did}/build-logs", R, s.buildLogs)
	h("GET /api/deployments/{did}/deploy-logs", R, s.deployLogs)
	h("GET /api/deployments/{did}/cron-runs", R, s.cronRuns)
	h("GET /api/cron-runs/{rid}/logs", R, s.cronRunLogs)

	// volumes / backups
	h("PATCH /api/volumes/{vid}", M, s.updateVolume)
	h("DELETE /api/volumes/{vid}", M, s.deleteVolume)
	h("GET /api/volumes/{vid}/backups", R, s.listBackups)
	h("POST /api/volumes/{vid}/backups", M, s.createBackup)
	h("PUT /api/volumes/{vid}/backup-schedule", M, s.backupSchedule)
	h("GET /api/volumes/{vid}/backup-schedule", R, s.getBackupSchedule)
	h("GET /api/environments/{eid}/volumes", R, s.listEnvVolumes)
	h("POST /api/backups/{bid}/restore", M, s.restoreBackup)
	h("DELETE /api/backups/{bid}", M, s.deleteBackup)

	// templates / events
	h("GET /api/templates", anyUser(), s.listTemplates)
	h("POST /api/projects/{pid}/templates/{key}", M, s.deployTemplate)
	// 템플릿 마켓플레이스: 보기는 공개 범위로 거르고(핸들러), 바꾸기는 만든 사람이나 작업 공간 admin(핸들러), 배포는 대상 프로젝트 member(핸들러)
	h("POST /api/templates", anyUser(), s.createTemplate)
	h("GET /api/templates/{key}", anyUser(), s.getTemplate)
	h("PATCH /api/templates/{key}", anyUser(), s.patchTemplate)
	h("DELETE /api/templates/{key}", anyUser(), s.deleteTemplate)
	h("GET /api/templates/{key}/export", anyUser(), s.exportTemplate)
	h("POST /api/templates/{key}/deploy", anyUser(), s.deployTemplateByKey)
	h("POST /api/projects/{pid}/environments/{eid}/export-template", M, s.exportEnvironmentTemplate)
	h("GET /api/projects/{pid}/events", R, s.listEvents)
	h("GET /api/projects/{pid}/events/stream", R, s.streamEvents)

	// notifications: 채널(프로젝트 / 작업 공간), 사용자 알림함과 구독 설정, 메일 시험
	h("GET /api/projects/{pid}/notification-channels", R, s.listProjectChannels)
	h("POST /api/projects/{pid}/notification-channels", A, s.createProjectChannel)
	h("PATCH /api/projects/{pid}/notification-channels/{cid}", A, s.patchProjectChannel)
	h("DELETE /api/projects/{pid}/notification-channels/{cid}", A, s.deleteProjectChannel)
	h("POST /api/projects/{pid}/notification-channels/{cid}/test", A, s.testProjectChannel)
	h("GET /api/projects/{pid}/notification-channels/{cid}/deliveries", M, s.projectChannelDeliveries)
	h("GET /api/notification-channels", instanceAdmin, s.listWorkspaceChannels)
	h("POST /api/notification-channels", instanceAdmin, s.createWorkspaceChannel)
	h("PATCH /api/notification-channels/{cid}", instanceAdmin, s.patchWorkspaceChannel)
	h("DELETE /api/notification-channels/{cid}", instanceAdmin, s.deleteWorkspaceChannel)
	h("POST /api/notification-channels/{cid}/test", instanceAdmin, s.testWorkspaceChannel)
	h("GET /api/notification-channels/{cid}/deliveries", instanceAdmin, s.workspaceChannelDeliveries)
	h("POST /api/system/smtp/test", instanceAdmin, s.testSMTP)
	h("GET /api/auth/notification-preferences", selfOnly(), s.getNotificationPrefs)
	h("PUT /api/auth/notification-preferences", selfOnly(), s.putNotificationPrefs)
	h("GET /api/notifications", anyUser(), s.listNotifications)
	// 새 알림함 항목 SSE(inboxstream.go)
	h("GET /api/notifications/stream", anyUser(), s.streamNotifications)
	h("POST /api/notifications/read", access{Kind: accSelf}, s.readNotifications) // 자기 알림함만 바꾼다(토큰도 허용)

	// metric alert rules
	h("GET /api/projects/{pid}/alert-rules", R, s.listAlertRules)
	h("POST /api/projects/{pid}/alert-rules", A, s.createAlertRule)
	h("PATCH /api/projects/{pid}/alert-rules/{arid}", A, s.patchAlertRule)
	h("DELETE /api/projects/{pid}/alert-rules/{arid}", A, s.deleteAlertRule)

	// observability dashboards
	h("GET /api/projects/{pid}/dashboards", R, s.listDashboards)
	h("POST /api/projects/{pid}/dashboards", M, s.createDashboard)
	h("GET /api/projects/{pid}/dashboards/{dbid}", R, s.getDashboard)
	h("PATCH /api/projects/{pid}/dashboards/{dbid}", M, s.patchDashboard)
	h("DELETE /api/projects/{pid}/dashboards/{dbid}", M, s.deleteDashboard)

	// usage
	h("GET /api/projects/{pid}/usage", R, s.projectUsage)
	h("GET /api/projects/{pid}/usage/limit", R, s.getUsageLimit)
	h("POST /api/projects/{pid}/usage/resume", A, s.resumeUsage)
	h("GET /api/usage", instanceAdmin, s.workspaceUsage)

	// 서버 owner 전용 관리(owner.go). 서버 관리자(admin)·작업 공간 owner는 쓸 수 없고 세션으로만 부른다.
	h("GET /api/instance/notice", anyUser(), s.getInstanceNotice)
	h("GET /api/owner", ownerOnly(), s.getOwnerOverview)
	h("GET /api/owner/setup", ownerOnly(), s.getSetup)
	h("PUT /api/owner/setup", ownerOnly(), s.putSetup)
	h("GET /api/owner/self-service", ownerOnly(), s.getSelfService)
	h("POST /api/owner/self-service", ownerOnly(), s.createSelfService)
	h("DELETE /api/owner/self-service", ownerOnly(), s.deleteSelfService)
	h("POST /api/owner/self-service/check", ownerOnly(), s.checkSelfUpdateNow)
	h("GET /api/owner/server-config", ownerOnly(), s.getServerConfig)
	h("PUT /api/owner/server-config", ownerOnly(), s.putServerConfig)
	h("PUT /api/owner/admin-caps", ownerOnly(), s.putAdminCaps)
	h("PUT /api/owner/maintenance", ownerOnly(), s.putMaintenance)
	h("PUT /api/owner/announcement", ownerOnly(), s.putAnnouncement)
	h("GET /api/owner/sessions", ownerOnly(), s.listAllSessions)
	h("DELETE /api/owner/sessions/{sesid}", ownerOnly(), s.deleteAnySession)
	h("POST /api/owner/sessions/revoke", ownerOnly(), s.revokeSessions)
	h("GET /api/owner/tokens", ownerOnly(), s.listAllTokens)
	h("DELETE /api/owner/tokens/{tid}", ownerOnly(), s.deleteAnyToken)
	h("POST /api/owner/tokens/revoke", ownerOnly(), s.revokeTokens)
	h("POST /api/owner/transfer", ownerOnly(), s.transferOwnership)
	h("POST /api/owner/lockdown", ownerOnly(), s.lockdown)
	h("GET /api/owner/backups", ownerOnly(), s.listKeelBackups)
	h("POST /api/owner/backups", ownerOnly(), s.createKeelBackup)
	h("GET /api/owner/backups/{name}", ownerOnly(), s.downloadKeelBackup)
	h("DELETE /api/owner/backups/{name}", ownerOnly(), s.deleteKeelBackup)
	h("POST /api/owner/restart", ownerOnly(), s.restartServer)
	h("GET /api/owner/github-connections", ownerOnly(), s.listGitHubConnectionOwners)
	h("PUT /api/owner/workspaces/{wsid}/owner", ownerOnly(), s.forceWorkspaceOwner)
	h("GET /api/owner/holds", ownerOnly(), s.listOwnerHolds)
	h("POST /api/owner/view-as", ownerOnly(), s.startViewAs)
	h("POST /api/owner/holds", ownerOnly(), s.createOwnerHold)
	h("DELETE /api/owner/holds/{scope}/{tid}", ownerOnly(), s.releaseOwnerHold)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeErr(w, 404, "not found") })
	mux.Handle("/mcp", s.mcpHandler(mux))
	// Public GraphQL API(graphql.go): POST는 REST 라우트를 감싼 GraphQL, GET은 브라우저용 playground다.
	mux.Handle("POST /graphql/v2", s.graphQLHandler(mux))
	mux.HandleFunc("GET /graphql/v2", s.graphiql)
	if s.Web != nil {
		mux.Handle("/", spaHandler(s.Web))
	}
	return s.secureHeaders(s.logRequests(mux))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) storeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrResourceLimit):
		writeErr(w, 409, err.Error())
	case errors.Is(err, store.ErrLastWorkspaceOwner), errors.Is(err, store.ErrPersonalOwner), errors.Is(err, store.ErrWorkspaceMembership):
		writeErr(w, 409, err.Error())
	case errors.Is(err, errMaintenanceLogin):
		writeJSON(w, 503, map[string]any{"error": err.Error(), "maintenance": true})
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, 404, "not found")
	case errors.As(err, new(*store.HostClaimedError)):
		writeErr(w, 409, err.Error())
	case errors.Is(err, store.ErrConflict):
		writeErr(w, 409, "already exists")
	case errors.Is(err, store.ErrDeploymentSourceRestricted):
		writeErr(w, 403, err.Error())
	case errors.Is(err, context.Canceled):
		writeErr(w, 499, "request cancelled")
	case errors.Is(err, deploy.ErrNodeUnavailable):
		writeErr(w, 409, err.Error())
	case errors.Is(err, deploy.ErrSelfInUse), errors.Is(err, deploy.ErrSelfService):
		writeErr(w, 409, err.Error())
	case errors.Is(err, deploy.ErrProjectSuspended):
		writeErr(w, 409, err.Error())
	case errors.Is(err, deploy.ErrNodeOffline):
		writeErr(w, http.StatusServiceUnavailable, err.Error())
	default:
		s.Log.Error("internal", "err", err)
		writeErr(w, 500, "internal error")
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeErr(w, 400, "read body: "+err.Error())
		return false
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		body = []byte("{}")
	}
	if err := json.Unmarshal(body, v); err != nil {
		writeErr(w, 400, "invalid json: "+err.Error())
		return false
	}
	return true
}

func (s *Server) logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		h.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			s.Log.Debug("http", "method", r.Method, "path", r.URL.Path, "dur", time.Since(start))
		}
	})
}

var (
	nameRe   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)
	domainRe = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
	envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)
	branchRe = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
)

// ---- system ----

func (s *Server) system(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	st := func(err error) string {
		if err != nil {
			return "error"
		}
		return "ok"
	}
	var free int64
	var fsst syscall.Statfs_t
	if syscall.Statfs(s.Deploy.Cfg.DataDir, &fsst) == nil {
		free = int64(fsst.Bavail) * int64(fsst.Bsize)
	}
	writeJSON(w, 200, map[string]any{
		"version": s.Version, "node": s.NodeName,
		"docker": st(s.Deploy.Docker.Ping(ctx)), "proxy": st(s.Deploy.Proxy.Ping(ctx)),
		"base_domain": nilIfEmpty(s.Deploy.Cfg.BaseDomain), "public_ipv4": nilIfEmpty(s.Deploy.Cfg.PublicIPv4),
		"public_ipv6": nilIfEmpty(s.Deploy.Cfg.PublicIPv6), "disk_free_bytes": free,
		"cpu_cores": runtime.NumCPU(), "tcp_proxy_domain": nilIfEmpty(s.Deploy.TCPProxyHost()),
		"smtp_configured": s.mailer() != nil, "pricing": s.Pricing,
		"mesh": s.meshSettings(),
	})
}

// meshSettings는 로컬 노드의 mesh 설정(KEEL_MESH, KEEL_MESH_PORT, KEEL_PRIVATE_ADDRESS)이다.
func (s *Server) meshSettings() map[string]any {
	mode := strings.ToLower(s.Deploy.Cfg.Mesh)
	if mode == "" {
		mode = "auto"
	}
	port := s.Deploy.Cfg.MeshPort
	if port == 0 {
		port = 7443
	}
	return map[string]any{"mode": mode, "port": port, "private_address": nilIfEmpty(s.Deploy.Cfg.PrivateAddress), "enabled": s.Deploy.LocalMesh != nil}
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *Server) systemGitHub(w http.ResponseWriter, r *http.Request) {
	base := s.PublicURL
	if base == "" {
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	writeJSON(w, 200, map[string]any{
		"webhook_url":       strings.TrimRight(base, "/") + "/api/webhooks/github",
		"secret_configured": s.GitHubSecret != "",
		"token_configured":  s.Deploy.EnvGitHubToken() != "" || s.Deploy.UserGitHubConnection(r.Context(), principalID(r)) != nil,
		"connected":         s.Deploy.UserGitHubConnection(r.Context(), principalID(r)) != nil,
	})
}
