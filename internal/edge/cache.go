package edge

import (
	"container/list"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var staticTypes = []string{
	"image/*", "font/*", "audio/*", "video/*",
	"text/css*", "text/csv*", "text/javascript*", "text/xml*",
	"application/javascript*", "application/x-javascript*", "application/xml*", "application/wasm*",
	"application/manifest+json*", "application/pdf*", "application/zip*", "application/gzip*",
	"application/x-gzip*", "application/x-tar*", "application/font*", "application/vnd.ms-fontobject*",
}

// IsStaticType은 Content-Type이 정적 자산인지다.
func IsStaticType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	for _, p := range staticTypes {
		if wildcardMatch(p, ct) {
			return true
		}
	}
	return false
}

func isHTML(ct string) bool {
	mt, _, _ := mime.ParseMediaType(ct)
	return mt == "text/html" || mt == "application/xhtml+xml"
}

// cacheControl은 Cache-Control 응답 헤더를 읽은 것이다.
type cacheControl struct {
	noStore, private, noCache bool
	maxAge, sMaxAge           int // -1 = 없음
	swr, sie                  int // stale-while-revalidate / stale-if-error (초, 0 = 없음)
}

func parseCacheControl(h http.Header) cacheControl {
	cc := cacheControl{maxAge: -1, sMaxAge: -1}
	for _, line := range h.Values("Cache-Control") {
		for _, part := range strings.Split(line, ",") {
			k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
			k = strings.ToLower(strings.TrimSpace(k))
			n, err := strconv.Atoi(strings.Trim(strings.TrimSpace(v), `"`))
			if err != nil || n < 0 {
				n = -1
			}
			switch k {
			case "no-store":
				cc.noStore = true
			case "private":
				cc.private = true
			case "no-cache":
				cc.noCache = true
			case "max-age":
				cc.maxAge = n
			case "s-maxage":
				cc.sMaxAge = n
			case "stale-while-revalidate":
				cc.swr = max(n, 0)
			case "stale-if-error":
				cc.sie = max(n, 0)
			}
		}
	}
	return cc
}

// cacheableStatus는 저장할 수 있는 상태 코드다.
func cacheableStatus(code int) bool {
	switch code {
	case 200, 203, 204, 300, 301, 302, 307, 308, 404, 410:
		return true
	}
	return false
}

// decision은 응답 하나를 캐시할지와 얼마나 둘지다.
type decision struct {
	store    bool
	ttl      time.Duration
	swr, sie time.Duration
	html     bool
	// heuristic이면 응답에 명시한 기간 없이 Default TTL로 저장한다.
	heuristic bool
}

// 정적 자산과 Force 모드 HTML의 Default TTL. 그 밖의 응답(JSON 등)은 명시한 max-age가 있어야 캐시한다.
func decide(cdn CDN, status int, h http.Header, override *ActionParams) decision {
	ct := h.Get("Content-Type")
	d := decision{html: isHTML(ct)}
	if !cacheableStatus(status) {
		return d
	}
	if h.Get("Set-Cookie") != "" {
		return d
	}
	for _, v := range h.Values("Vary") {
		for _, f := range strings.Split(v, ",") {
			f = strings.TrimSpace(f)
			if f == "*" || strings.EqualFold(f, "Cookie") {
				return d
			}
		}
	}
	if (status >= 300 && status < 400) && h.Get("Content-Length") == "0" {
		return d
	}
	mt, _, _ := mime.ParseMediaType(ct)
	if mt == "text/event-stream" {
		return d
	}
	cc := parseCacheControl(h)
	if cdn.HonorSWR && cc.swr > 0 {
		d.swr = time.Duration(cc.swr) * time.Second
	}
	if cc.sie > 0 {
		d.sie = time.Duration(cc.sie) * time.Second
	}
	if override != nil && override.TTLSeconds > 0 {
		d.store, d.ttl = true, time.Duration(override.TTLSeconds)*time.Second
		return d
	}
	if cc.noStore || cc.private {
		return d
	}
	if d.html && cdn.HTMLCaching == "never" {
		return d
	}
	explicit := cc.sMaxAge
	if explicit < 0 {
		explicit = cc.maxAge
	}
	if cc.noCache {
		explicit = 0
	}
	if explicit >= 0 {
		if explicit == 0 {
			return d
		}
		d.store, d.ttl = true, time.Duration(explicit)*time.Second
		return d
	}
	if status != 200 && status != 203 {
		return d // 휴리스틱 기간은 성공 응답에만
	}
	if IsStaticType(ct) || (d.html && cdn.HTMLCaching == "force") {
		d.store, d.ttl, d.heuristic = true, time.Duration(cdn.DefaultTTLSeconds)*time.Second, true
	}
	return d
}

// assetTypes는 쿠키가 있는 요청에서도 휴리스틱으로 캐시하는 정적 자산이다(사용자마다 다르게 만드는 일이 드물다).
// PDF, ZIP, CSV, XML처럼 로그인한 사용자에게 내려 주는 문서(청구서, 내보내기)는 여기 없다.
var assetTypes = []string{"image/*", "font/*", "text/css*", "text/javascript*", "application/javascript*", "application/x-javascript*",
	"application/wasm*", "application/font*", "application/vnd.ms-fontobject*"}

func isAssetType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	for _, p := range assetTypes {
		if wildcardMatch(p, ct) {
			return true
		}
	}
	return false
}

// decideFor는 요청을 고려한 저장 결정이다. 쿠키를 보낸 요청의 응답은 origin이 Cache-Control로 허락했거나
// (명시 TTL), 정적 자산이거나, HTML Force 모드일 때만 휴리스틱으로 저장한다. 세션 쿠키로 만든 개인 문서를
// 다른 사람에게 주지 않기 위해서다.
func decideFor(cdn CDN, status int, h http.Header, override *ActionParams, reqCookie bool) decision {
	d := decide(cdn, status, h, override)
	if d.store && d.heuristic && reqCookie && !d.html && !isAssetType(h.Get("Content-Type")) {
		d.store = false
	}
	return d
}

// encodingClass는 Accept-Encoding을 캐시 키용 값으로 줄인다(br > gzip > identity). origin에도 이 값만 보낸다.
func encodingClass(ae string) string {
	accepted := map[string]bool{}
	for _, part := range strings.Split(strings.ToLower(ae), ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		q := 1.0
		if k, v, ok := strings.Cut(strings.TrimSpace(params), "="); ok && strings.TrimSpace(k) == "q" {
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				q = f
			}
		}
		if q > 0 {
			accepted[strings.TrimSpace(name)] = true
		}
	}
	switch {
	case accepted["br"]:
		return "br"
	case accepted["gzip"]:
		return "gzip"
	}
	return "identity"
}

// canonicalQuery는 쿼리 파라미터 순서를 정렬한다(?a=1&b=2와 ?b=2&a=1은 같은 항목).
func canonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, "&")
	sort.Strings(parts)
	return strings.Join(parts, "&")
}

// primaryKey는 Vary를 반영하기 전의 캐시 키다(인스턴스, scheme, 호스트, 경로, 정렬한 쿼리, 인코딩).
// scheme이 키에 없으면 http 요청에 origin이 준 https 리다이렉트가 https 요청에도 나가 리다이렉트 고리가 생긴다.
func primaryKey(instanceID string, secure bool, host string, u *url.URL, enc string) string {
	scheme := "http"
	if secure {
		scheme = "https"
	}
	return instanceID + "\x00" + scheme + "\x00" + host + "\x00" + u.EscapedPath() + "?" + canonicalQuery(u.RawQuery) + "\x00" + enc
}

// entry는 저장한 응답 하나다.
type entry struct {
	key      string
	instance string
	status   int
	header   http.Header
	body     []byte
	stored   time.Time
	ttl      time.Duration
	swr, sie time.Duration
	html     bool
	htmlGen  uint64
	allGen   uint64
	size     int64
	elem     *list.Element
}

func (e *entry) fresh(now time.Time) bool { return now.Sub(e.stored) < e.ttl }
func (e *entry) withinSWR(now time.Time) bool {
	return e.swr > 0 && now.Sub(e.stored) < e.ttl+e.swr
}
func (e *entry) withinSIE(now time.Time) bool {
	return e.sie > 0 && now.Sub(e.stored) < e.ttl+e.sie
}

// validFor는 항목이 지금 세대(purge) 기준으로 유효한지다.
func (e *entry) validFor(cdn CDN) bool {
	if e.allGen != cdn.AllGeneration {
		return false
	}
	return !e.html || e.htmlGen == cdn.HTMLGeneration
}

// Cache는 메모리 LRU 캐시다. 용량(바이트)을 넘으면 오래 안 쓴 항목부터 버린다.
type Cache struct {
	mu      sync.Mutex
	max     int64
	maxItem int64
	used    int64
	lru     *list.List
	items   map[string]*entry
	vary    map[string][]string // primary key → Vary 헤더 이름들(정규화)
}

func NewCache(maxBytes, maxItem int64) *Cache {
	return &Cache{max: maxBytes, maxItem: maxItem, lru: list.New(), items: map[string]*entry{}, vary: map[string][]string{}}
}

// fullKey는 primary key에 요청의 Vary 헤더 값을 붙인다.
func (c *Cache) fullKey(pk string, h http.Header) string {
	c.mu.Lock()
	names := c.vary[pk]
	c.mu.Unlock()
	return varyKey(pk, names, h)
}

func varyKey(pk string, names []string, h http.Header) string {
	if len(names) == 0 {
		return pk
	}
	var b strings.Builder
	b.WriteString(pk)
	for _, n := range names {
		b.WriteString("\x00")
		b.WriteString(n)
		b.WriteString("=")
		b.WriteString(strings.Join(h.Values(n), ","))
	}
	return b.String()
}

func varyNames(h http.Header) []string {
	var names []string
	for _, v := range h.Values("Vary") {
		for _, f := range strings.Split(v, ",") {
			f = http.CanonicalHeaderKey(strings.TrimSpace(f))
			if f != "" && f != "Accept-Encoding" && !contains(names, f) {
				names = append(names, f) // Accept-Encoding은 이미 키에 있다
			}
		}
	}
	sort.Strings(names)
	return names
}

func (c *Cache) get(key string) *entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.items[key]
	if e != nil {
		c.lru.MoveToFront(e.elem)
	}
	return e
}

// put은 항목을 저장한다. 너무 크면 저장하지 않고 false다.
func (c *Cache) put(pk string, reqHeader http.Header, e *entry) bool {
	e.size = int64(len(e.body)) + 512
	for k, vs := range e.header {
		e.size += int64(len(k))
		for _, v := range vs {
			e.size += int64(len(v))
		}
	}
	if e.size > c.maxItem || e.size > c.max {
		return false
	}
	names := varyNames(e.header)
	e.key = varyKey(pk, names, reqHeader)
	c.mu.Lock()
	defer c.mu.Unlock()
	// Vary 이름은 있는 것만 기억한다. 대부분의 응답은 없으므로, 쿼리를 바꿔 가며 보낸 요청마다 표가 자라지 않는다.
	if len(names) > 0 {
		if len(c.vary) >= 4*len(c.items)+1024 {
			c.vary = map[string][]string{} // 다음 저장 때 다시 배운다(LRU에서 빠진 키의 이름이 쌓이지 않게)
		}
		c.vary[pk] = names
	} else {
		delete(c.vary, pk)
	}
	if old := c.items[e.key]; old != nil {
		c.removeLocked(old)
	}
	e.elem = c.lru.PushFront(e)
	c.items[e.key] = e
	c.used += e.size
	for c.used > c.max {
		last := c.lru.Back()
		if last == nil {
			break
		}
		c.removeLocked(last.Value.(*entry))
	}
	return true
}

func (c *Cache) remove(e *entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items[e.key] == e {
		c.removeLocked(e)
	}
}

func (c *Cache) removeLocked(e *entry) {
	delete(c.items, e.key)
	c.lru.Remove(e.elem)
	c.used -= e.size
}

// PurgeStale은 세대가 지난(purge된) 항목을 지운다. valid가 false인 항목만 버린다.
func (c *Cache) PurgeStale(valid func(e *entry) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.items {
		if !valid(e) {
			c.removeLocked(e)
		}
	}
	if len(c.vary) > 4*len(c.items)+1024 {
		c.vary = map[string][]string{} // Vary 이름은 다음 저장 때 다시 배운다
	}
}

// Stats는 저장한 항목 수와 바이트다.
func (c *Cache) Stats() (items int, bytes int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items), c.used
}
