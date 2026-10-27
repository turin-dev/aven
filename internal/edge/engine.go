package edge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/compat"
)

// Caddy가 엔진으로 넘길 때 덮어쓰는 헤더(클라이언트가 보낸 값은 Caddy가 지운다). origin 서버는 이 값으로
// X-Forwarded-For / X-Forwarded-Proto를 만들고 헤더를 지운 다음 컨테이너로 보낸다.
const (
	HeaderClientIP          = "X-Aven-Client-Ip"
	LegacyHeaderClientIP    = "X-Keel-Client-Ip"
	HeaderClientProto       = "X-Aven-Client-Proto"
	LegacyHeaderClientProto = "X-Keel-Client-Proto"
)

// Policy는 호스트 하나에 적용할 설정이다(호스트를 가진 서비스 인스턴스의 설정).
type Policy struct {
	InstanceID string
	Settings   Settings
}

type compiled struct {
	Policy
	rules       []compiledRule
	allow, deny []netip.Prefix
	secret      string
}

// Engine은 엣지 요청을 처리하는 http.Handler다. 노드마다(aven-server, aven-agent) 하나씩 돈다.
type Engine struct {
	Origin   http.RoundTripper // Caddy origin 서버로 가는 transport
	NodeName string
	Log      *slog.Logger
	Now      func() time.Time

	cache    *Cache
	limiter  limiter
	mu       sync.RWMutex
	hosts    map[string]*compiled
	rp       *httputil.ReverseProxy
	inflight sync.Map
	secret   string // 정책에 키가 없을 때 쓰는 프로세스 키
}

// NewEngine은 origin transport와 캐시 크기(바이트)로 엔진을 만든다.
func NewEngine(origin http.RoundTripper, cacheBytes, maxItemBytes int64) *Engine {
	b := make([]byte, 32)
	rand.Read(b)
	e := &Engine{Origin: origin, cache: NewCache(cacheBytes, maxItemBytes), hosts: map[string]*compiled{}, secret: hex.EncodeToString(b)}
	if h, err := os.Hostname(); err == nil {
		e.NodeName = h
	}
	e.rp = &httputil.ReverseProxy{
		Rewrite:        e.rewrite,
		Transport:      roundTripper{e},
		ModifyResponse: e.modifyResponse,
		ErrorHandler:   e.errorHandler,
		ErrorLog:       nil,
	}
	return e
}

type roundTripper struct{ e *Engine }

func (t roundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.e.Origin.RoundTrip(r)
}

// UnixTransport는 unix socket(Caddy origin 서버)으로만 연결하는 transport다.
func UnixTransport(path string) *http.Transport {
	d := &net.Dialer{Timeout: 5 * time.Second}
	return &http.Transport{
		DialContext:           func(ctx context.Context, _, _ string) (net.Conn, error) { return d.DialContext(ctx, "unix", path) },
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   256,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 0,
		DisableCompression:    true,
	}
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) log() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}

// Configure는 호스트별 정책을 통째로 바꾼다. purge로 세대가 바뀐 캐시 항목은 이때 메모리에서 지운다.
func (e *Engine) Configure(hosts map[string]Policy) {
	next := make(map[string]*compiled, len(hosts))
	byInst := map[string]*compiled{}
	for host, p := range hosts {
		if c, ok := byInst[p.InstanceID]; ok && p.InstanceID != "" {
			next[strings.ToLower(host)] = c
			continue
		}
		c := &compiled{Policy: p, allow: prefixes(p.Settings.WAF.AllowIPs), deny: prefixes(p.Settings.WAF.DenyIPs), secret: p.Settings.WAF.ClearanceSecret}
		if c.secret == "" {
			c.secret = e.secret
		}
		rules, err := p.Settings.Rules.Compile()
		if err != nil {
			e.log().Warn("edge rules ignored", "host", host, "err", err)
		}
		c.rules = rules
		byInst[p.InstanceID] = c
		next[strings.ToLower(host)] = c
	}
	e.mu.Lock()
	e.hosts = next
	e.mu.Unlock()
	e.cache.PurgeStale(func(en *entry) bool {
		c := byInst[en.instance]
		return c != nil && c.Settings.CDN.Enabled && en.validFor(c.Settings.CDN)
	})
}

// CacheStats는 캐시 항목 수와 바이트다.
func (e *Engine) CacheStats() (int, int64) { return e.cache.Stats() }

func (e *Engine) lookup(host string) *compiled {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if c := e.hosts[host]; c != nil {
		return c
	}
	if i := strings.IndexByte(host, '.'); i > 0 {
		return e.hosts["*"+host[i:]]
	}
	return nil
}

func canonicalHost(h string) string {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if host, _, err := net.SplitHostPort(h); err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(h, "[]")
}

// reqState는 origin으로 보내는 요청 하나의 캐시 상태다(context로 ModifyResponse/ErrorHandler에 넘긴다).
type reqState struct {
	pol        *compiled
	caching    bool
	pk         string
	reqHeader  http.Header
	override   *ActionParams
	stale      *entry
	headAsGet  bool
	respHeader []ActionParams
}

type stateKey struct{}

func stateOf(r *http.Request) *reqState {
	st, _ := r.Context().Value(stateKey{}).(*reqState)
	return st
}

var errServeStale = errors.New("serve stale")

func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	now := e.now()
	host := canonicalHost(r.Host)
	pol := e.lookup(host)
	if pol == nil {
		http.Error(w, "edge: no policy for this host", http.StatusBadGateway)
		return
	}
	ip, _ := netip.ParseAddr(strings.TrimSpace(r.Header.Get(HeaderClientIP)))
	ip = ip.Unmap()
	secure := r.Header.Get(HeaderClientProto) == "https"
	s := pol.Settings
	info := &reqInfo{ip: ip, host: host, path: r.URL.Path, method: r.Method, header: r.Header}

	switch r.URL.Path {
	case ChallengePath, LegacyChallengePath:
		if r.URL.Path == LegacyChallengePath {
			compat.NoteLegacyResource("waf-path", "path "+LegacyChallengePath, "path "+ChallengePath)
		}
		var until *time.Time
		if s.WAF.UnderAttack != nil {
			until = s.WAF.UnderAttack.Until
		}
		handleChallengeAnswer(w, r, pol.secret, host, secure, until, now)
		return
	case CDNTracePath, LegacyCDNTracePath:
		if r.URL.Path == LegacyCDNTracePath {
			compat.NoteLegacyResource("waf-path", "path "+LegacyCDNTracePath, "path "+CDNTracePath)
		}
		if s.CDN.Enabled {
			e.trace(w, r, host, ip, now)
			return
		}
	}

	if !containsIP(pol.allow, ip) {
		if containsIP(pol.deny, ip) {
			deny(w, http.StatusForbidden, "Forbidden by WAF IP rule\n", "waf-ip")
			return
		}
		if s.WAF.ManagedRules {
			if name := managedMatch(r); name != "" {
				deny(w, http.StatusForbidden, "Blocked by managed WAF rule ("+name+")\n", "waf-managed")
				return
			}
		}
		if rl := s.WAF.RateLimit; rl != nil && ip.IsValid() {
			if ok, retry := e.limiter.allow(pol.InstanceID+"|"+clientKey(ip), *rl, now); !ok {
				w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
				deny(w, http.StatusTooManyRequests, "Too many requests\n", "waf-rate-limit")
				return
			}
		}
		if s.WAF.UnderAttackActive(now) && !hasClearance(r, pol.secret, host, now) {
			writeChallenge(w, r, pol.secret, host, now)
			return
		}
	}

	st := &reqState{pol: pol}
	bypass := false
rules:
	for _, rule := range pol.rules {
		if !rule.match(info) {
			continue
		}
		p := ActionParams{}
		if rule.Then.Params != nil {
			p = *rule.Then.Params
		}
		switch rule.Then.Action {
		case ActBlock:
			status := p.Status
			if status == 0 {
				status = http.StatusForbidden
			}
			body := p.Body
			if body == "" {
				body = http.StatusText(status) + "\n"
			}
			deny(w, status, body, "rule")
			return
		case ActAllow:
			break rules
		case ActChallenge:
			if !hasClearance(r, pol.secret, host, now) {
				writeChallenge(w, r, pol.secret, host, now)
				return
			}
		case ActRedirect:
			redirect(w, r, p, secure)
			return
		case ActCacheOverride:
			if p.Bypass {
				bypass, st.override = true, nil
			} else {
				bypass, st.override = false, &p
			}
		case ActRewrite:
			path, query, hasQuery := strings.Cut(p.Path, "?")
			r.URL.Path, r.URL.RawPath = path, ""
			if hasQuery {
				r.URL.RawQuery = query
			}
			info.path = path
		case ActSetHeader:
			if p.Target == "request" {
				if p.Value == "" {
					r.Header.Del(p.Name)
				} else {
					r.Header.Set(p.Name, p.Value)
				}
			} else {
				st.respHeader = append(st.respHeader, p)
			}
		}
	}

	st.caching = s.CDN.Enabled && !bypass && (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
		r.Header.Get("Authorization") == "" && r.Header.Get("Upgrade") == ""
	if st.caching {
		enc := encodingClass(r.Header.Get("Accept-Encoding"))
		r.Header.Set("Accept-Encoding", enc)
		st.pk = primaryKey(pol.InstanceID, host, r.URL, enc)
		key := e.cache.fullKey(st.pk, r.Header)
		ent := e.cache.get(key)
		if ent != nil && !ent.validFor(s.CDN) {
			e.cache.remove(ent)
			ent = nil
		}
		if ent != nil {
			if ent.fresh(now) {
				e.serveEntry(w, r, ent, "HIT", st.respHeader, now)
				return
			}
			if s.CDN.HonorSWR && ent.withinSWR(now) {
				e.serveEntry(w, r, ent, "STALE", st.respHeader, now)
				e.refresh(r, st, key)
				return
			}
			if ent.withinSIE(now) {
				st.stale = ent
			}
		}
		st.headAsGet = r.Method == http.MethodHead
		st.reqHeader = r.Header.Clone()
	}
	e.rp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), stateKey{}, st)))
}

func deny(w http.ResponseWriter, status int, body, reason string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Aven-Edge", reason)
	w.Header().Set("X-Keel-Edge", reason)
	w.WriteHeader(status)
	io.WriteString(w, body)
}

func redirect(w http.ResponseWriter, r *http.Request, p ActionParams, secure bool) {
	status := p.Status
	if status == 0 {
		status = http.StatusFound
	}
	loc := p.Location
	if p.Host != "" {
		scheme := "http"
		if secure {
			scheme = "https"
		}
		loc = scheme + "://" + p.Host
		if p.PreservePath == nil || *p.PreservePath {
			loc += r.URL.EscapedPath()
		} else {
			loc += "/"
		}
		if (p.PreserveQuery == nil || *p.PreserveQuery) && r.URL.RawQuery != "" {
			loc += "?" + r.URL.RawQuery
		}
	}
	w.Header().Set("Location", loc)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
}

func applyRespHeaders(h http.Header, list []ActionParams) {
	for _, p := range list {
		if p.Value == "" {
			h.Del(p.Name)
		} else {
			h.Set(p.Name, p.Value)
		}
	}
}

func (e *Engine) rewrite(pr *httputil.ProxyRequest) {
	pr.Out.URL.Scheme = "http"
	pr.Out.URL.Host = "keel-edge-origin"
	pr.Out.Host = pr.In.Host
	if st := stateOf(pr.In); st != nil && st.headAsGet {
		pr.Out.Method = http.MethodGet // HEAD 미스도 본문을 받아 저장한다(클라이언트에는 헤더만 간다)
	}
}

// hopHeaders는 저장하지 않는 연결 단위 헤더다.
var hopHeaders = []string{"Connection", "Keep-Alive", "Proxy-Connection", "Transfer-Encoding", "Upgrade", "Te", "Trailer", "Proxy-Authenticate", "Proxy-Authorization"}

func (e *Engine) modifyResponse(resp *http.Response) error {
	st := stateOf(resp.Request)
	if st == nil || !st.caching {
		if st != nil {
			applyRespHeaders(resp.Header, st.respHeader)
		}
		return nil
	}
	now := e.now()
	if st.stale != nil && resp.StatusCode >= 500 {
		return errServeStale
	}
	cdn := st.pol.Settings.CDN
	d := decide(cdn, resp.StatusCode, resp.Header, st.override)
	if !d.store {
		resp.Header.Set("X-Cache", "DYNAMIC")
		applyRespHeaders(resp.Header, st.respHeader)
		return nil
	}
	if resp.ContentLength > e.cache.maxItem {
		resp.Header.Set("X-Cache", "MISS")
		applyRespHeaders(resp.Header, st.respHeader)
		return nil
	}
	h := resp.Header.Clone()
	for _, k := range hopHeaders {
		h.Del(k)
	}
	h.Del("Age")
	ent := &entry{instance: st.pol.InstanceID, status: resp.StatusCode, header: h, stored: now, ttl: d.ttl, swr: d.swr, sie: d.sie,
		html: d.html, htmlGen: cdn.HTMLGeneration, allGen: cdn.AllGeneration}
	pk, reqHeader, cache := st.pk, st.reqHeader, e.cache
	resp.Body = &teeBody{rc: resp.Body, limit: e.cache.maxItem, done: func(body []byte) {
		ent.body = body
		ent.header.Set("Content-Length", strconv.Itoa(len(body)))
		cache.put(pk, reqHeader, ent)
	}}
	resp.Header.Set("X-Cache", "MISS")
	applyRespHeaders(resp.Header, st.respHeader)
	return nil
}

func (e *Engine) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	st := stateOf(r)
	if st != nil && st.stale != nil && (errors.Is(err, errServeStale) || r.Context().Err() == nil) {
		e.serveEntry(w, r, st.stale, "STALE", st.respHeader, e.now())
		return
	}
	if r.Context().Err() == nil {
		e.log().Debug("edge origin error", "host", r.Host, "err", err)
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "edge: upstream unavailable", http.StatusBadGateway)
}

// teeBody는 클라이언트로 보내는 본문을 limit까지 모았다가, 끝까지 문제없이 읽으면 done을 부른다.
type teeBody struct {
	rc     io.ReadCloser
	buf    []byte
	limit  int64
	over   bool
	failed bool
	done   func([]byte)
	once   sync.Once
}

func (t *teeBody) Read(p []byte) (int, error) {
	n, err := t.rc.Read(p)
	if n > 0 && !t.over {
		if int64(len(t.buf)+n) > t.limit {
			t.over, t.buf = true, nil
		} else {
			t.buf = append(t.buf, p[:n]...)
		}
	}
	if err == io.EOF && !t.over && !t.failed {
		t.once.Do(func() { t.done(t.buf) })
	} else if err != nil && err != io.EOF {
		t.failed = true
	}
	return n, err
}

func (t *teeBody) Close() error { return t.rc.Close() }

// serveEntry는 캐시 항목으로 응답한다. If-None-Match가 ETag와 맞으면 304다.
func (e *Engine) serveEntry(w http.ResponseWriter, r *http.Request, ent *entry, xcache string, respHeader []ActionParams, now time.Time) {
	h := w.Header()
	for k, vs := range ent.header {
		h[k] = append([]string(nil), vs...)
	}
	age := int(now.Sub(ent.stored).Seconds())
	h.Set("Age", strconv.Itoa(max(age, 0)))
	h.Set("X-Cache", xcache)
	applyRespHeaders(h, respHeader)
	if inm := r.Header.Get("If-None-Match"); inm != "" && etagMatch(inm, ent.header.Get("Etag")) {
		for k := range h {
			switch k {
			case "Etag", "Cache-Control", "Vary", "Expires", "Last-Modified", "Date", "Content-Location", "Age", "X-Cache":
			default:
				h.Del(k)
			}
		}
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(ent.status)
	if r.Method != http.MethodHead {
		w.Write(ent.body)
	}
}

// etagMatch는 If-None-Match 약한 비교다(W/ 무시, 쉼표 목록, *).
func etagMatch(inm, etag string) bool {
	if etag == "" {
		return false
	}
	norm := func(s string) string { return strings.TrimPrefix(strings.TrimSpace(s), "W/") }
	for _, t := range strings.Split(inm, ",") {
		t = strings.TrimSpace(t)
		if t == "*" || norm(t) == norm(etag) {
			return true
		}
	}
	return false
}

// refresh는 stale-while-revalidate 창 안의 항목을 백그라운드에서 다시 받는다. 같은 키는 한 번만 받는다.
func (e *Engine) refresh(r *http.Request, st *reqState, key string) {
	if _, busy := e.inflight.LoadOrStore(key, struct{}{}); busy {
		return
	}
	out := r.Clone(context.Background())
	out.Method = http.MethodGet
	out.URL.Scheme, out.URL.Host = "http", "keel-edge-origin"
	out.RequestURI = ""
	out.Body = nil
	out.ContentLength = 0
	out.Header.Del("If-None-Match")
	out.Header.Del("If-Modified-Since")
	pk, reqHeader := st.pk, r.Header.Clone()
	pol := st.pol
	override := st.override
	go func() {
		defer e.inflight.Delete(key)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		resp, err := e.Origin.RoundTrip(out.WithContext(ctx))
		if err != nil {
			return
		}
		defer resp.Body.Close()
		cdn := pol.Settings.CDN
		d := decide(cdn, resp.StatusCode, resp.Header, override)
		if !d.store {
			return
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, e.cache.maxItem+1))
		if err != nil || int64(len(body)) > e.cache.maxItem {
			return
		}
		h := resp.Header.Clone()
		for _, k := range hopHeaders {
			h.Del(k)
		}
		h.Del("Age")
		h.Set("Content-Length", strconv.Itoa(len(body)))
		e.cache.put(pk, reqHeader, &entry{instance: pol.InstanceID, status: resp.StatusCode, header: h, body: body, stored: e.now(),
			ttl: d.ttl, swr: d.swr, sie: d.sie, html: d.html, htmlGen: cdn.HTMLGeneration, allGen: cdn.AllGeneration})
	}()
}

func (e *Engine) trace(w http.ResponseWriter, r *http.Request, host string, ip netip.Addr, now time.Time) {
	items, bytes := e.cache.Stats()
	v := map[string]any{"node": e.NodeName, "host": host, "ip": ip.String(), "ts": now.UTC().Format(time.RFC3339Nano),
		"cache_items": items, "cache_bytes": bytes}
	w.Header().Set("Cache-Control", "no-store")
	if _, ok := r.URL.Query()["json"]; ok {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, k := range []string{"node", "host", "ip", "ts", "cache_items", "cache_bytes"} {
		fmt.Fprintf(w, "%s=%v\n", k, v[k])
	}
}
