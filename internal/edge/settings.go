// Package edge는 서비스별(환경별) 엣지 정책(CDN 캐시, WAF, 엣지 규칙)과 그것을 실행하는 엔진이다.
// keel-proxy(Caddy)는 엣지 정책이 있는 호스트의 요청을 unix socket으로 이 엔진에 넘기고, 엔진은 WAF와 규칙을
// 적용하고 캐시를 확인한 다음 Caddy의 origin 서버(역시 unix socket)로 보내 실제 컨테이너에 닿는다.

package edge

import (
	"fmt"
	"net/netip"
	"strings"
	"time"
)

// Settings는 서비스 인스턴스(서비스 × 환경) 하나의 엣지 설정이다.
type Settings struct {
	CDN   CDN     `json:"cdn"`
	WAF   WAF     `json:"waf"`
	Rules Ruleset `json:"rules"`
}

type CDN struct {
	Enabled           bool   `json:"enabled"`
	HTMLCaching       string `json:"html_caching"`        // auto | force | never
	DefaultTTLSeconds int    `json:"default_ttl_seconds"` // 1800, 3600, 7200, 14400, 43200, 86400
	HonorSWR          bool   `json:"honor_swr"`
	PurgeOnDeploy     string `json:"purge_on_deploy"` // off | html | all

	// 서버가 관리하는 값. 세대가 바뀌면 그 이전에 저장한 캐시 항목은 쓰지 않는다(모든 노드에 라우트와 함께 전달된다).
	HTMLGeneration   uint64     `json:"html_generation"`
	AllGeneration    uint64     `json:"all_generation"`
	LastPurgedHTML   *time.Time `json:"last_purged_html"`
	LastPurgedAll    *time.Time `json:"last_purged_all"`
	LastDeployPurged string     `json:"last_deploy_purged,omitempty"`
}

type WAF struct {
	AllowIPs     []string     `json:"allow_ips"` // 이 IP/CIDR에는 WAF 검사(차단 목록, 관리형 규칙, 요청 제한, Under Attack)를 하지 않는다
	DenyIPs      []string     `json:"deny_ips"`
	RateLimit    *RateLimit   `json:"rate_limit"` // null = 끔
	ManagedRules bool         `json:"managed_rules"`
	UnderAttack  *UnderAttack `json:"under_attack"` // null = 끔
	// ClearanceSecret은 브라우저 확인 쿠키의 HMAC 키다. 서버가 만들고 API에는 보이지 않는다.
	ClearanceSecret string `json:"clearance_secret,omitempty"`
}

type RateLimit struct {
	Requests      int `json:"requests"`       // 창 하나에서 클라이언트 IP당 허용하는 요청 수
	WindowSeconds int `json:"window_seconds"` // 1..3600
}

// UnderAttack은 켠 시각과 끝나는 시각(null = 끌 때까지)이다.
type UnderAttack struct {
	Since time.Time  `json:"since"`
	Until *time.Time `json:"until"`
}

// UnderAttackActive는 now 시점에 Under Attack Mode가 켜져 있는지다.
func (w WAF) UnderAttackActive(now time.Time) bool {
	return w.UnderAttack != nil && (w.UnderAttack.Until == nil || now.Before(*w.UnderAttack.Until))
}

var DefaultTTLs = []int{1800, 3600, 7200, 14400, 43200, 86400}

// DefaultSettings는 아직 저장한 적 없는 인스턴스의 설정이다(CDN 꺼짐, Honor SWR 켬, 배포 시 HTML 비우기).
func DefaultSettings() Settings {
	s := Settings{CDN: CDN{HonorSWR: true}}
	s.Normalize()
	return s
}

// Normalize는 빈 값을 기본값으로 채운다.
func (s *Settings) Normalize() {
	if s.CDN.HTMLCaching == "" {
		s.CDN.HTMLCaching = "auto"
	}
	if s.CDN.DefaultTTLSeconds == 0 {
		s.CDN.DefaultTTLSeconds = 7200
	}
	if s.CDN.PurgeOnDeploy == "" {
		s.CDN.PurgeOnDeploy = "html"
	}
	if s.WAF.AllowIPs == nil {
		s.WAF.AllowIPs = []string{}
	}
	if s.WAF.DenyIPs == nil {
		s.WAF.DenyIPs = []string{}
	}
	if s.Rules.Version == 0 {
		s.Rules.Version = 1
	}
	if s.Rules.Rules == nil {
		s.Rules.Rules = []Rule{}
	}
}

// Enabled는 이 설정 때문에 요청이 엣지 엔진을 거쳐야 하는지다(시각과 무관: Under Attack 만료는 엔진이 요청마다 판단한다).
func (s Settings) Enabled() bool {
	if s.CDN.Enabled || len(s.WAF.DenyIPs) > 0 || s.WAF.RateLimit != nil || s.WAF.ManagedRules || s.WAF.UnderAttack != nil {
		return true
	}
	for _, r := range s.Rules.Rules {
		if r.IsEnabled() {
			return true
		}
	}
	return false
}

// Validate는 설정 전체를 검사한다. 규칙도 Normalize/Compile로 검사한다.
func (s Settings) Validate() error {
	switch s.CDN.HTMLCaching {
	case "auto", "force", "never":
	default:
		return fmt.Errorf("cdn.html_caching must be auto, force or never")
	}
	okTTL := false
	for _, t := range DefaultTTLs {
		okTTL = okTTL || t == s.CDN.DefaultTTLSeconds
	}
	if !okTTL {
		return fmt.Errorf("cdn.default_ttl_seconds must be one of 1800, 3600, 7200, 14400, 43200, 86400")
	}
	switch s.CDN.PurgeOnDeploy {
	case "off", "html", "all":
	default:
		return fmt.Errorf("cdn.purge_on_deploy must be off, html or all")
	}
	if rl := s.WAF.RateLimit; rl != nil {
		if rl.Requests < 1 || rl.Requests > 1_000_000 {
			return fmt.Errorf("waf.rate_limit.requests must be 1..1000000")
		}
		if rl.WindowSeconds < 1 || rl.WindowSeconds > 3600 {
			return fmt.Errorf("waf.rate_limit.window_seconds must be 1..3600")
		}
	}
	for name, list := range map[string][]string{"waf.allow_ips": s.WAF.AllowIPs, "waf.deny_ips": s.WAF.DenyIPs} {
		if len(list) > 256 {
			return fmt.Errorf("%s supports at most 256 entries", name)
		}
		for _, raw := range list {
			if _, err := ParsePrefix(raw); err != nil {
				return fmt.Errorf("%s: %q is not an IP address or CIDR", name, raw)
			}
		}
	}
	_, err := s.Rules.Compile()
	return err
}

// ParsePrefix는 IP 주소나 CIDR을 prefix로 읽는다(IP 하나는 /32 또는 /128).
func ParsePrefix(raw string) (netip.Prefix, error) {
	raw = strings.TrimSpace(raw)
	if p, err := netip.ParsePrefix(raw); err == nil {
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(raw)
	if err != nil {
		return netip.Prefix{}, err
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// CanonicalPrefixes는 IP/CIDR 목록을 정규화하고 중복을 없앤다(IP 하나는 주소만 남긴다).
func CanonicalPrefixes(in []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		p, err := ParsePrefix(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid IP/CIDR %q", raw)
		}
		v := p.String()
		if p.IsSingleIP() {
			v = p.Addr().String()
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out, nil
}

func prefixes(list []string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(list))
	for _, raw := range list {
		if p, err := ParsePrefix(raw); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func containsIP(list []netip.Prefix, ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	for _, p := range list {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
