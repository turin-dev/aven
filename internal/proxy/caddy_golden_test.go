package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/turin-dev/aven/internal/edge"
)

// 골든 테스트: 운영과 같은 모양의 라우트로 Caddy.Build 결과를 testdata에 고정한다.
// 이 스냅샷에서 허용되는 자동 변경은 도메인 확인(check) 라우트 추가뿐이다.
// 의도한 변경이면 `go test ./internal/proxy -run TestCaddyGolden -update`로 다시 만든다.
var updateGolden = flag.Bool("update", false, "rewrite testdata golden files")

// 비밀처럼 보이는 값은 모두 가짜다(운영 게이트웨이 비밀이나 basic auth 해시를 넣지 않는다).
const (
	fakeGatewaySecret = "fake-gateway-secret-for-golden-test"
	fakeBcryptHash    = "$2a$10$fakefakefakefakefakefuOq1Q3b2Yc8b3o0m9i0l2b5n6x7z8a9e"
)

func goldenEdge() *edge.Settings {
	s := edge.DefaultSettings()
	s.CDN.Enabled = true
	return &s
}

// mainRoutes는 main 노드(로컬, KEEL_TLS=auto)의 라우트를 syncProxy가 만드는 모양 그대로 흉내 낸다.
func mainRoutes() []Route {
	return []Route{
		// 커스텀 도메인, 자동 TLS
		{Hosts: []string{"blog.example.com"}, Upstreams: []string{"keel-blog-obkawyvj-1:3000"}},
		{Hosts: []string{"example.com"}, Upstreams: []string{"keel-home-a1b2c3d4-1:8080"}},
		{Hosts: []string{"n8n.example.com"}, Upstreams: []string{"keel-n8n-e5f6g7h8-1:5678"}},
		// 외부 upstream(aven 밖 compose 컨테이너): upstream_paths 없음
		{Hosts: []string{"dash.example.com"}, Upstreams: []string{"compose-grafana-1:3000"}},
		{Hosts: []string{"mail.example.org"}, Upstreams: []string{"compose-mailu-front-1:80"}},
		// 외부 upstream + upstream_paths: 접두어만 보내고 나머지는 404(PathsOnly)
		{Hosts: []string{"mailu-api.example.net"}, Upstreams: []string{"compose-mailu-front-1:80"}, Path: "/api/v1/*", PathsOnly: true},
		// 경로 규칙: 기본 서비스(catch-all) + 정확히 일치 + 접두어(strip 포함, 다른 서비스로)
		{Hosts: []string{"sample-mcp.example.com"}, Upstreams: []string{"keel-web-vixyswgu-1:3000"}},
		{Hosts: []string{"sample-mcp.example.com"}, Path: "/healthz", Upstreams: []string{"keel-publicserver-2fq6gefg-1:8080"}},
		{Hosts: []string{"sample-mcp.example.com"}, Path: "/api/*", Upstreams: []string{"keel-publicserver-2fq6gefg-1:8080"}},
		{Hosts: []string{"sample-mcp.example.com"}, Path: "/agent.apk/*", StripPrefix: true, Upstreams: []string{"keel-publicserver-2fq6gefg-1:8080"}},
		// 리다이렉트(영구)
		{Hosts: []string{"www.example.com"}, Redirect: &Redirect{To: "example.com", Permanent: true}},
		// 리다이렉트(임시, 스킴 포함)
		{Hosts: []string{"old.example.com"}, Redirect: &Redirect{To: "https://blog.example.com/"}},
		// basic auth
		{Hosts: []string{"otp.example.com"}, Upstreams: []string{"keel-otp-i9j0k1l2-1:8080"}, Auth: &BasicAuth{User: "admin", Hash: fakeBcryptHash}},
		// 와일드카드(on-demand TLS + ask 서버)
		{Hosts: []string{"*.preview.example.com"}, Upstreams: []string{"keel-preview-m3n4o5p6-1:3000"}},
		// aven-server 자신: unix socket + 게이트웨이 헤더
		{Hosts: []string{"aven.example.com"}, Upstreams: []string{"unix/" + WakeDir + "/keel-server.sock"}, SetHeaders: map[string]string{"X-Keel-Gateway": fakeGatewaySecret}},
		// 엣지(CDN) 사용 호스트: :443은 엣지 엔진으로, origin 서버가 컨테이너로
		{Hosts: []string{"160.example.com"}, Upstreams: []string{"keel-sample-legacy-eomrpz63-1:8080"}, InstanceID: "ins_sample-legacy", Edge: goldenEdge()},
		// 잠든 서비스: wake handler(aven-server의 wake socket)로
		{Hosts: []string{"sleepy.example.com"}, Upstreams: []string{WakeDial}},
		// replica 여러 개: round-robin
		{Hosts: []string{"registry.example.com"}, Upstreams: []string{"keel-registry-q7r8s9t0-1:5000", "keel-registry-q7r8s9t0-2:5000", "keel-registry-q7r8s9t0-3:5000"}},
		// trace 라우트
		{Hosts: []string{"sample-api.example.com"}, Upstreams: []string{"keel-sample-api-u1v2w3x4-1:8080"}, Trace: true,
			TraceAttributes: map[string]string{"keel.instance_id": "ins_sample-api", "keel.service": "sample-api"}},
	}
}

// tunnelRoutes는 공인 IP 없이 Cloudflare 터널 뒤에 있는 tunnel 노드다: 모든 도메인이 tls=off, trust_forwarded_proto=true.
func tunnelRoutes() []Route {
	off := func(host string, ups ...string) Route {
		return Route{Hosts: []string{host}, Upstreams: ups, TLS: TLSOff, TrustProto: true}
	}
	rs := []Route{
		off("api.example.net", "keel-api-aaaa1111-1:8080", "keel-api-aaaa1111-2:8080"),
		off("mcp.example.net", "keel-mcp-bbbb2222-1:3000"),
		off("admin.example.net", "keel-admin-cccc3333-1:3000"),
		off("auth.example.net", "keel-auth-dddd4444-1:9000"),
		off("example.net", "keel-web-eeee5555-1:3000"),
		// 외부 upstream(tunnel 노드의 허용 목록)
		off("server.example.net", "100.64.0.1:8443"),
	}
	// 경로 규칙이 있는 터널 호스트
	p := off("api.example.net", "keel-files-ffff6666-1:8080")
	p.Path = "/files/*"
	return append(rs, p)
}

// goldenToken은 가짜 도메인 확인 토큰이다(운영 토큰은 마스터 키 HMAC이라 넣지 않는다).
func goldenToken(host string) string {
	sum := sha256.Sum256([]byte("golden:" + host))
	return hex.EncodeToString(sum[:])[:32]
}

// withCheckTokens는 syncProxy처럼 서비스 도메인 라우트마다 CheckToken을 채운다(aven-server 자신의 라우트는 도메인이 아니라 뺀다).
func withCheckTokens(rs []Route) []Route {
	for i := range rs {
		if len(rs[i].SetHeaders) == 0 {
			rs[i].CheckToken = goldenToken(rs[i].Hosts[0])
		}
	}
	return rs
}

func TestCaddyGolden(t *testing.T) {
	cases := []struct {
		name   string
		cfg    Config
		edge   bool
		routes []Route
	}{
		{"main", Config{TLS: TLSAuto, MetricsHosts: []string{"100.64.0.3"}}, true, withCheckTokens(mainRoutes())},
		{"tunnel", Config{TLS: TLSAuto, MetricsHosts: []string{"100.64.0.1"}}, true, withCheckTokens(tunnelRoutes())},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := New(tc.cfg)
			if tc.edge {
				c.SetEdge(&fakeEdge{})
			}
			raw, err := c.Build(tc.routes)
			if err != nil {
				t.Fatal(err)
			}
			var got bytes.Buffer
			if err := json.Indent(&got, raw, "", "  "); err != nil {
				t.Fatal(err)
			}
			got.WriteByte('\n')
			path := filepath.Join("testdata", "caddy_golden_"+tc.name+".json")
			if *updateGolden {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with -update to create)", err)
			}
			if !bytes.Equal(got.Bytes(), want) {
				t.Errorf("Caddy config for %s changed; diff against %s (rerun with -update only if intended):\n%s", tc.name, path, got.String())
			}
		})
	}
}
