package edge

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"html"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/compat"
)

// ---- 요청 제한 ----

// limiter는 (인스턴스, 클라이언트) 단위의 고정 창 카운터다. IPv6는 /64 단위로 센다(주소를 바꿔 가며 피하지 못하게).
type limiter struct {
	mu        sync.Mutex
	windows   map[string]*window
	lastSweep time.Time
}

type window struct {
	start time.Time
	size  time.Duration
	count int
}

const maxLimiterKeys = 200_000

func clientKey(ip netip.Addr) string {
	if ip.Is6() && !ip.Is4In6() {
		p, _ := ip.Prefix(64)
		return p.String()
	}
	return ip.Unmap().String()
}

// allow는 요청 하나를 세고, 한도를 넘으면 false와 남은 시간을 돌려준다.
func (l *limiter) allow(key string, rl RateLimit, now time.Time) (bool, time.Duration) {
	size := time.Duration(rl.WindowSeconds) * time.Second
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.windows == nil {
		l.windows = map[string]*window{}
	}
	if now.Sub(l.lastSweep) > time.Minute || len(l.windows) > maxLimiterKeys {
		for k, w := range l.windows {
			if now.Sub(w.start) >= w.size {
				delete(l.windows, k)
			}
		}
		l.lastSweep = now
	}
	w := l.windows[key]
	if w == nil || now.Sub(w.start) >= w.size || w.size != size {
		if w == nil && len(l.windows) >= maxLimiterKeys {
			return true, 0 // 표가 가득 차면 새 클라이언트는 세지 않는다(메모리 보호)
		}
		w = &window{start: now, size: size}
		l.windows[key] = w
	}
	w.count++
	if w.count > rl.Requests {
		return false, w.start.Add(w.size).Sub(now)
	}
	return true, 0
}

// ---- 관리형 규칙 ----

// managedRules는 흔한 공격 패턴이다(경로 조작, 민감 파일, SQL 주입, XSS, Log4Shell, 스캐너 User-Agent).
// 오탐을 줄이려고 보수적으로 고른 기본 규칙만 있다.
var managedRules = []struct {
	name string
	re   *regexp.Regexp
	ua   bool
}{
	{name: "path-traversal", re: regexp.MustCompile(`(?i)(\.\./|\.\.\\|/\.\.$|/etc/passwd|/proc/self/environ|c:\\windows\\)`)},
	{name: "sensitive-file", re: regexp.MustCompile(`(?i)/(\.env(\.[a-z0-9_-]+)?|\.git/(config|head)|\.svn/|\.htpasswd|\.htaccess|\.ds_store|wp-config\.php(\.bak)?|id_rsa)(/|$|\?)`)},
	{name: "sql-injection", re: regexp.MustCompile(`(?i)(\bunion(\s|/\*.*?\*/)+(all(\s|/\*.*?\*/)+)?select\b|\bselect\b.+\bfrom\b.+\bwhere\b.+(=|\blike\b)|\b(sleep|benchmark|pg_sleep)\s*\(\s*\d|'\s*(or|and)\s*'?\d+'?\s*=\s*'?\d+|;\s*(drop|truncate)\s+table\b|\binformation_schema\b|\bxp_cmdshell\b)`)},
	{name: "xss", re: regexp.MustCompile(`(?i)(<\s*script\b|javascript\s*:|\bon(error|load|mouseover|focus)\s*=|<\s*iframe\b|<\s*svg[^>]*\bon\w+\s*=)`)},
	{name: "log4shell", re: regexp.MustCompile(`(?i)\$\{\s*(jndi|env|sys|lower|upper|::-)`)},
	{name: "scanner", ua: true, re: regexp.MustCompile(`(?i)(sqlmap|nikto|nmap scripting engine|masscan|zgrab|acunetix|nessus|wpscan|dirbuster|gobuster|nuclei|openvas|w3af|havij)`)},
}

// managedMatch는 막아야 하는 요청이면 규칙 이름을 돌려준다.
func managedMatch(r *http.Request) string {
	raw := r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		raw += "?" + r.URL.RawQuery
	}
	decoded := raw
	for i := 0; i < 2; i++ { // 이중 인코딩까지 푼다
		if d, err := url.QueryUnescape(decoded); err == nil {
			decoded = d
		}
	}
	targets := []string{decoded, strings.ReplaceAll(decoded, "+", " ")}
	for _, h := range []string{"Referer", "Cookie", "X-Api-Version"} {
		if v := r.Header.Get(h); v != "" && strings.Contains(v, "${") {
			targets = append(targets, v)
		}
	}
	ua := r.Header.Get("User-Agent")
	for _, m := range managedRules {
		if m.ua {
			if m.re.MatchString(ua) {
				return m.name
			}
			continue
		}
		if m.name == "log4shell" && m.re.MatchString(ua) {
			return m.name
		}
		for _, t := range targets {
			if m.re.MatchString(t) {
				return m.name
			}
		}
	}
	return ""
}

// ---- 브라우저 확인(Under Attack Mode, challenge 규칙) ----

const (
	ClearanceCookie       = "__aven_clearance"
	LegacyClearanceCookie = "__keel_clearance"
	ChallengePath         = "/.aven/challenge"
	LegacyChallengePath   = "/.keel/challenge"
	CDNTracePath          = "/.aven/cdn-trace"
	LegacyCDNTracePath    = "/.keel/cdn-trace"
	challengeBits         = 14 // 브라우저에서 수 ms~수십 ms 걸리는 작업 증명
	challengeTTL          = 5 * time.Minute
	clearanceLifetime     = 24 * time.Hour
)

func sign(secret, msg string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(msg))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func uaHash(ua string) string {
	s := sha256.Sum256([]byte(ua))
	return hex.EncodeToString(s[:8])
}

// clearanceValue는 host와 User-Agent에 묶인 확인 쿠키 값이다.
func clearanceValue(secret, host, ua string, exp time.Time) string {
	e := strconv.FormatInt(exp.Unix(), 10)
	return e + "." + sign(secret, "clear|"+host+"|"+e+"|"+uaHash(ua))
}

// hasClearance는 요청에 유효한 확인 쿠키가 있는지다.
func hasClearance(r *http.Request, secret, host string, now time.Time) bool {
	c, err := r.Cookie(ClearanceCookie)
	if err != nil {
		c, err = r.Cookie(LegacyClearanceCookie)
		if err != nil {
			return false
		}
		compat.NoteLegacyResource("clearance-cookie", "cookie "+LegacyClearanceCookie, "cookie "+ClearanceCookie)
	}
	e, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return false
	}
	exp, err := strconv.ParseInt(e, 10, 64)
	if err != nil || now.Unix() >= exp {
		return false
	}
	want := sign(secret, "clear|"+host+"|"+e+"|"+uaHash(r.Header.Get("User-Agent")))
	return hmac.Equal([]byte(sig), []byte(want))
}

func newChallenge(secret, host string, now time.Time) string {
	b := make([]byte, 12)
	rand.Read(b)
	payload := strconv.FormatInt(now.Add(challengeTTL).Unix(), 10) + "." + hex.EncodeToString(b)
	return payload + "." + sign(secret, "chal|"+host+"|"+payload)
}

// powOK는 fnv1a32(challenge + ":" + nonce)의 상위 challengeBits 비트가 0인지다.
func powOK(challenge, nonce string) bool {
	if len(nonce) == 0 || len(nonce) > 16 {
		return false
	}
	h := fnv.New32a()
	h.Write([]byte(challenge + ":" + nonce))
	return h.Sum32()>>(32-challengeBits) == 0
}

func verifyChallenge(secret, host, token, nonce string, now time.Time) bool {
	i := strings.LastIndexByte(token, '.')
	if i < 0 {
		return false
	}
	payload, sig := token[:i], token[i+1:]
	if !hmac.Equal([]byte(sig), []byte(sign(secret, "chal|"+host+"|"+payload))) {
		return false
	}
	e, _, _ := strings.Cut(payload, ".")
	exp, err := strconv.ParseInt(e, 10, 64)
	if err != nil || now.Unix() >= exp {
		return false
	}
	return powOK(token, nonce)
}

// wantsChallengePage는 브라우저 탐색 요청(GET + Accept에 text/html)인지다. 그 밖의 요청은 확인 페이지 대신 거절한다.
func wantsChallengePage(r *http.Request) bool {
	return r.Method == http.MethodGet && strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html")
}

func safeReturn(raw string) string {
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, "/\\") || strings.ContainsAny(raw, "\r\n") || len(raw) > 4096 {
		return "/"
	}
	return raw
}

// writeChallenge는 확인 페이지(브라우저) 또는 403(그 밖)을 쓴다.
func writeChallenge(w http.ResponseWriter, r *http.Request, secret, host string, now time.Time) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Keel-Edge", "challenge")
	if !wantsChallengePage(r) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintln(w, "Browser verification required (Under Attack Mode). Open this URL in a web browser.")
		return
	}
	tok := newChallenge(secret, host, now)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; form-action 'self'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprintf(w, challengeHTML, html.EscapeString(host), html.EscapeString(ChallengePath), html.EscapeString(tok), html.EscapeString(safeReturn(r.URL.RequestURI())), challengeBits)
}

// handleChallengeAnswer는 확인 페이지의 POST를 검사하고, 맞으면 쿠키를 주고 원래 주소로 보낸다.
func handleChallengeAnswer(w http.ResponseWriter, r *http.Request, secret, host string, secure bool, until *time.Time, now time.Time) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !verifyChallenge(secret, host, r.PostForm.Get("c"), r.PostForm.Get("n"), now) {
		http.Error(w, "verification failed, reload the page", http.StatusForbidden)
		return
	}
	exp := now.Add(clearanceLifetime)
	if until != nil && until.Before(exp) && until.After(now) {
		exp = *until
	}
	http.SetCookie(w, &http.Cookie{Name: ClearanceCookie, Value: clearanceValue(secret, host, r.Header.Get("User-Agent"), exp),
		Path: "/", Expires: exp, MaxAge: int(exp.Sub(now).Seconds()), HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, safeReturn(r.PostForm.Get("r")), http.StatusSeeOther)
}

const challengeHTML = `<!doctype html>
<html lang="ko"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex"><title>브라우저 확인 중…</title>
<style>body{font-family:system-ui,-apple-system,sans-serif;background:#fafafa;color:#222;display:flex;min-height:100vh;align-items:center;justify-content:center;margin:0}
main{max-width:28rem;padding:2rem;text-align:center}h1{font-size:1.25rem;margin:0 0 .5rem}p{color:#555;font-size:.9rem;line-height:1.5}
.s{width:2rem;height:2rem;border:3px solid #ddd;border-top-color:#7c3aed;border-radius:50%%;margin:0 auto 1.25rem;animation:r 1s linear infinite}@keyframes r{to{transform:rotate(360deg)}}
@media (prefers-color-scheme:dark){body{background:#111;color:#eee}p{color:#aaa}.s{border-color:#333;border-top-color:#a78bfa}}</style></head>
<body><main><div class="s"></div><h1>%s 접속 전에 브라우저를 확인하고 있습니다</h1>
<p>이 사이트는 지금 공격 방어 모드(Under Attack Mode)입니다. 잠시 후 자동으로 이동합니다.</p>
<noscript><p><strong>계속하려면 JavaScript를 켜 주세요.</strong></p></noscript>
<form id="f" method="POST" action="%s"><input type="hidden" name="c" value="%s"><input type="hidden" name="n" value=""><input type="hidden" name="r" value="%s"></form>
<script>(function(){var f=document.getElementById("f"),c=f.c.value,bits=%d;
function h(s){var x=0x811c9dc5;for(var i=0;i<s.length;i++){x^=s.charCodeAt(i);x=Math.imul(x,0x01000193)>>>0}return x>>>0}
var n=0;function step(){for(var k=0;k<5000;k++,n++){if((h(c+":"+n.toString(36))>>>(32-bits))===0){f.n.value=n.toString(36);f.submit();return}}setTimeout(step,0)}
setTimeout(step,300)})()</script></main></body></html>
`
