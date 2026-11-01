package proxy

import (
	"log/slog"
	"net/netip"
	"net/url"
	"strings"
)

// Build에 들어오는 값 중 사용자가 정한 것(호스트, 경로, 리다이렉트 대상, upstream)은 API·agent에서 이미 검사하지만,
// 설정 파일(railway.json, keel.json), IaC, import처럼 다른 경로로 들어온 값도 있으므로 Caddy JSON을 만들기 직전에 한 번 더 본다.
// JSON 자체는 encoding/json이 만들어서 문법이 깨지지 않는다. 위험한 것은 Caddy placeholder다: host/path matcher,
// Location 헤더, upstream dial 주소는 요청마다 {…}를 바꿔 넣으므로 "{http.request.header.Authorization}" 같은 값이
// 들어가면 비밀 헤더가 리다이렉트로 새거나 모든 호스트와 맞거나 임의 주소로 연결된다. 그래서 중괄호, 공백·제어 문자를 막는다.
// 잘못된 값 하나 때문에 설정 전체가 거절되면 다른 서비스의 라우팅까지 멈추므로, 그 값만 빼고 경고를 남긴다.

// cleanHost는 라우트 호스트를 정규화한다(소문자, 끝 점 제거). 호스트 이름, 첫 label만 *인 와일드카드, IP 주소만 받는다.
func cleanHost(h string) (string, bool) {
	h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
	if h == "" || len(h) > 253 {
		return "", false
	}
	if a, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")); err == nil && a.Zone() == "" {
		return h, true
	}
	for i, l := range strings.Split(h, ".") {
		if l == "*" && i == 0 {
			continue
		}
		if l == "" || len(l) > 63 {
			return "", false
		}
		for j := 0; j < len(l); j++ {
			c := l[j]
			if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '-' && c != '_' {
				return "", false
			}
		}
	}
	if h == "*" || (IsWildcard(h) && !strings.Contains(h[2:], ".")) {
		return "", false // "*"나 "*.com"은 너무 넓다
	}
	return h, true
}

// hasUnsafeChars는 Caddy 설정 값에 넣으면 안 되는 문자(placeholder 중괄호, 공백, 제어 문자, 백슬래시)가 있는지다.
func hasUnsafeChars(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= ' ' || c == 0x7f || c == '{' || c == '}' || c == '\\' {
			return true
		}
	}
	return false
}

// validRulePath는 경로 규칙이 matcher에 그대로 넣어도 되는 값인지다(정규화한 뒤, ""는 catch-all).
func validRulePath(p string) bool {
	return p == "" || (strings.HasPrefix(p, "/") && len(p) <= 1024 && !hasUnsafeChars(p) && !strings.ContainsAny(p, "?#"))
}

// validUpstream은 dial 주소(container:port, ip:port, unix/경로)에 placeholder나 공백이 없는지다.
func validUpstream(u string) bool {
	return u != "" && len(u) <= 512 && !hasUnsafeChars(u)
}

// redirectLocation은 리다이렉트 대상을 Location 헤더 값으로 만든다. 스킴이 없으면 https다.
// 대상에 쿼리나 fragment가 있으면 그 주소로 정확히 보낸다(요청 URI를 뒤에 붙이면 "?a=1/path?q"처럼 깨진다).
// 아니면 요청 경로와 쿼리({http.request.uri})를 붙인다. 쓸 수 없는 대상이면 ok=false.
func redirectLocation(to string) (string, bool) {
	to = strings.TrimSpace(to)
	if to == "" || len(to) > 2048 || hasUnsafeChars(to) {
		return "", false
	}
	if !strings.Contains(to, "://") {
		to = "https://" + to
	}
	u, err := url.Parse(to)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Opaque != "" {
		return "", false
	}
	if _, ok := cleanHost(u.Hostname()); !ok || strings.HasPrefix(u.Hostname(), "*") {
		return "", false
	}
	if u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return to, true
	}
	return strings.TrimRight(to, "/") + "{http.request.uri}", true
}

// validBcrypt는 Hash가 bcrypt 해시 문자열($2a$/$2b$/$2y$, cost 04..31, 53자)인지다.
func validBcrypt(h string) bool {
	if len(h) != 60 || h[0] != '$' || h[1] != '2' || h[3] != '$' || h[6] != '$' {
		return false
	}
	switch h[2] {
	case 'a', 'b', 'y':
	default:
		return false
	}
	cost := int(h[4]-'0')*10 + int(h[5]-'0')
	if h[4] < '0' || h[4] > '9' || h[5] < '0' || h[5] > '9' || cost < 4 || cost > 31 {
		return false
	}
	for i := 7; i < len(h); i++ {
		c := h[i]
		if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') && c != '.' && c != '/' {
			return false
		}
	}
	return true
}

// validHeaderName은 RFC 9110 token인지다.
func validHeaderName(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') && !strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) {
			return false
		}
	}
	return true
}

// sanitizeRoutes는 Build가 쓰기 전에 라우트를 정리한 복사본을 만든다. 입력은 바꾸지 않는다.
func sanitizeRoutes(routes []Route) []Route {
	out := make([]Route, 0, len(routes))
	for _, r := range routes {
		hosts := make([]string, 0, len(r.Hosts))
		for _, h := range r.Hosts {
			if c, ok := cleanHost(h); ok {
				hosts = append(hosts, c)
			} else {
				slog.Warn("proxy: route host ignored (invalid host name)", "host", truncateForLog(h))
			}
		}
		if len(hosts) == 0 {
			continue
		}
		r.Hosts = hosts
		if !validRulePath(normPath(r.Path)) {
			// 경로 규칙만 버린다. 같은 Route의 호스트 단위 설정(basic auth 등)은 남겨야 보호가 풀리지 않는다.
			slog.Warn("proxy: path rule ignored (invalid path)", "host", hosts[0], "path", truncateForLog(r.Path))
			r.Path, r.StripPrefix, r.Upstreams = "", false, nil
		}
		if len(r.Upstreams) > 0 {
			ups := make([]string, 0, len(r.Upstreams))
			for _, u := range r.Upstreams {
				if validUpstream(u) {
					ups = append(ups, u)
				} else {
					slog.Warn("proxy: upstream ignored (invalid dial address)", "host", hosts[0], "upstream", truncateForLog(u))
				}
			}
			r.Upstreams = ups
		}
		if r.Redirect != nil {
			if _, ok := redirectLocation(r.Redirect.To); !ok {
				slog.Warn("proxy: redirect ignored (invalid target)", "host", hosts[0], "to", truncateForLog(r.Redirect.To))
				r.Redirect = nil
			}
		}
		if len(r.SetHeaders) > 0 {
			set := make(map[string]string, len(r.SetHeaders))
			for k, v := range r.SetHeaders {
				if validHeaderName(k) && !strings.ContainsAny(v, "\r\n\x00") {
					set[k] = v
				}
			}
			r.SetHeaders = set
		}
		out = append(out, r)
	}
	return out
}

func truncateForLog(s string) string {
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return strings.ToValidUTF8(s, "?")
}

// validAuth는 basic auth 설정이 Caddy에 넣을 수 있는 값인지다.
func validAuth(a *BasicAuth) bool {
	return a != nil && a.User != "" && len(a.User) <= 128 && !hasUnsafeChars(a.User) && !strings.Contains(a.User, ":") && validBcrypt(a.Hash)
}

// authUnavailable은 basic auth 설정이 깨진 호스트의 응답이다. 내용을 보여 주지 않고 503으로 막는다.
func authUnavailable() map[string]any {
	return map[string]any{"handler": "static_response", "status_code": 503, "body": "authentication is misconfigured",
		"headers": map[string][]string{"Cache-Control": {"no-store"}, "Content-Type": {"text/plain; charset=utf-8"}}}
}
