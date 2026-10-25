package edge

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
)

// 규칙은 priority 오름차순으로 평가한다. block/allow/challenge/redirect는 처음 맞는 것이 결과를 정한다(terminal).
// cache_override, rewrite, set_header는 적용하고 다음 규칙으로 계속 간다.
type Ruleset struct {
	Version int    `json:"version"`
	Rules   []Rule `json:"rules"`
}

type Rule struct {
	ID          string `json:"id,omitempty"`
	Description string `json:"description,omitempty"`
	Priority    uint32 `json:"priority"`
	Enabled     *bool  `json:"enabled,omitempty"` // 없으면 켬
	If          *Expr  `json:"if"`
	Then        Action `json:"then"`
}

func (r Rule) IsEnabled() bool { return r.Enabled == nil || *r.Enabled }

func (r Rule) label() string {
	if r.Description != "" {
		return r.Description
	}
	if r.ID != "" {
		return r.ID
	}
	return fmt.Sprintf("priority %d", r.Priority)
}

// Expr은 조건 하나(attr/op/value[/key]) 또는 and/or/not 묶음이다.
type Expr struct {
	And []*Expr `json:"and,omitempty"`
	Or  []*Expr `json:"or,omitempty"`
	Not *Expr   `json:"not,omitempty"`

	Attr  string `json:"attr,omitempty"`
	Key   string `json:"key,omitempty"`
	Op    string `json:"op,omitempty"`
	Value Value  `json:"value,omitempty"`
}

// Value는 문자열 하나 또는 문자열 배열이다(in / not_in만 배열).
type Value struct {
	One    string
	List   []string
	IsList bool
}

func (v Value) MarshalJSON() ([]byte, error) {
	if v.IsList {
		if v.List == nil {
			return []byte("[]"), nil
		}
		return json.Marshal(v.List)
	}
	return json.Marshal(v.One)
}

func (v *Value) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if strings.HasPrefix(s, "[") {
		v.IsList = true
		return json.Unmarshal(b, &v.List)
	}
	v.IsList = false
	return json.Unmarshal(b, &v.One)
}

func (v Value) IsZero() bool { return !v.IsList && v.One == "" }

type Action struct {
	Action string        `json:"action"`
	Params *ActionParams `json:"params,omitempty"`
}

type ActionParams struct {
	Status int    `json:"status,omitempty"` // block 400..499, redirect 301/302/307/308
	Body   string `json:"body,omitempty"`   // block

	Host          string `json:"host,omitempty"`     // redirect: 다른 호스트로
	Location      string `json:"location,omitempty"` // redirect: 정확한 http(s) URL로
	PreservePath  *bool  `json:"preserve_path,omitempty"`
	PreserveQuery *bool  `json:"preserve_query,omitempty"`

	TTLSeconds int  `json:"ttl_seconds,omitempty"` // cache_override
	Bypass     bool `json:"bypass,omitempty"`

	Path string `json:"path,omitempty"` // rewrite: origin에 보낼 경로(쿼리 포함 가능)

	Name   string `json:"name,omitempty"`   // set_header
	Value  string `json:"value,omitempty"`  // set_header (빈 값 = 헤더 삭제)
	Target string `json:"target,omitempty"` // set_header: response(기본) | request
}

const (
	ActBlock         = "block"
	ActAllow         = "allow"
	ActChallenge     = "challenge"
	ActRedirect      = "redirect"
	ActCacheOverride = "cache_override"
	ActRewrite       = "rewrite" // aven 확장
	ActSetHeader     = "set_header"
)

const (
	MaxRules         = 100
	MaxRulesetBytes  = 64 << 10
	maxClauses       = 32
	maxDepth         = 4
	maxValueLen      = 256
	maxListLen       = 64
	maxWildcards     = 8
	maxBlockBodyLen  = 4096
	maxCacheOverride = 30 * 24 * 60 * 60
)

var attrOps = map[string][]string{
	"ipv4.src":    {"eq", "neq", "in", "not_in"},
	"ip.src":      {"eq", "neq", "in", "not_in"}, // aven 확장: IPv6도 맞춘다
	"http.host":   {"eq", "neq", "in", "matches"},
	"http.path":   {"eq", "neq", "contains", "matches"},
	"http.header": {"eq", "neq", "contains", "matches"},
	"http.method": {"eq", "neq", "in", "not_in"}, // aven 확장
}

// Normalize는 저장하기 전에 규칙을 정리한다: ID가 없으면 newID()로 채우고, priority 순으로 정렬하고,
// host/method 값을 정규 형식(소문자/대문자)으로 바꾼다.
func (rs *Ruleset) Normalize(newID func() string) {
	if rs.Version == 0 {
		rs.Version = 1
	}
	if rs.Rules == nil {
		rs.Rules = []Rule{}
	}
	for i := range rs.Rules {
		r := &rs.Rules[i]
		if r.ID == "" && newID != nil {
			r.ID = newID()
		}
		walk(r.If, func(e *Expr) {
			e.Attr = strings.ToLower(strings.TrimSpace(e.Attr))
			e.Op = strings.ToLower(strings.TrimSpace(e.Op))
			e.Key = strings.ToLower(strings.TrimSpace(e.Key))
			conv := func(s string) string { return s }
			switch e.Attr {
			case "http.host":
				conv = func(s string) string { return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".") }
			case "http.method":
				conv = func(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }
			case "ipv4.src", "ip.src":
				conv = strings.TrimSpace
			}
			e.Value.One = conv(e.Value.One)
			for j := range e.Value.List {
				e.Value.List[j] = conv(e.Value.List[j])
			}
		})
	}
	sort.SliceStable(rs.Rules, func(i, j int) bool { return rs.Rules[i].Priority < rs.Rules[j].Priority })
}

func walk(e *Expr, f func(*Expr)) {
	if e == nil {
		return
	}
	f(e)
	for _, c := range e.And {
		walk(c, f)
	}
	for _, c := range e.Or {
		walk(c, f)
	}
	walk(e.Not, f)
}

// compiledRule은 평가 준비가 끝난 규칙이다.
type compiledRule struct {
	Rule
	match func(*reqInfo) bool
}

// Compile은 규칙 전체를 검사하고 평가 함수로 바꾼다. 꺼진 규칙도 검사하지만 결과에서는 뺀다.
func (rs Ruleset) Compile() ([]compiledRule, error) {
	if rs.Version != 1 {
		return nil, errors.New("rules.version must be 1")
	}
	if len(rs.Rules) > MaxRules {
		return nil, fmt.Errorf("a ruleset supports at most %d rules", MaxRules)
	}
	if b, _ := json.Marshal(rs); len(b) > MaxRulesetBytes {
		return nil, errors.New("the ruleset is larger than 64 KiB")
	}
	prio := map[uint32]bool{}
	ids := map[string]bool{}
	var out []compiledRule
	for i, r := range rs.Rules {
		where := fmt.Sprintf("rules[%d]", i)
		if prio[r.Priority] {
			return nil, fmt.Errorf("%s.priority: %d is used by another rule (priorities must be unique)", where, r.Priority)
		}
		prio[r.Priority] = true
		if r.ID != "" {
			if ids[r.ID] {
				return nil, fmt.Errorf("%s.id: %q is used by another rule", where, r.ID)
			}
			ids[r.ID] = true
		}
		if len(r.Description) > maxValueLen {
			return nil, fmt.Errorf("%s.description: longer than %d characters", where, maxValueLen)
		}
		if r.If == nil {
			return nil, fmt.Errorf("%s.if: a condition is required", where)
		}
		clauses := 0
		m, err := compileExpr(r.If, 1, &clauses, where+".if")
		if err != nil {
			return nil, err
		}
		if clauses > maxClauses {
			return nil, fmt.Errorf("%s.if: more than %d condition clauses", where, maxClauses)
		}
		if err := validateAction(r.Then, where+".then"); err != nil {
			return nil, err
		}
		if r.IsEnabled() {
			out = append(out, compiledRule{Rule: r, match: m})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
	return out, nil
}

func compileExpr(e *Expr, depth int, clauses *int, where string) (func(*reqInfo) bool, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("%s: conditions are nested deeper than %d levels", where, maxDepth)
	}
	kinds := 0
	if e.And != nil {
		kinds++
	}
	if e.Or != nil {
		kinds++
	}
	if e.Not != nil {
		kinds++
	}
	if e.Attr != "" || e.Op != "" {
		kinds++
	}
	if kinds != 1 {
		return nil, fmt.Errorf("%s: use exactly one of and, or, not, or an attr/op/value clause", where)
	}
	group := func(list []*Expr, name string, all bool) (func(*reqInfo) bool, error) {
		if len(list) == 0 {
			return nil, fmt.Errorf("%s.%s: needs at least one condition", where, name)
		}
		fs := make([]func(*reqInfo) bool, 0, len(list))
		for i, c := range list {
			if c == nil {
				return nil, fmt.Errorf("%s.%s[%d]: empty condition", where, name, i)
			}
			f, err := compileExpr(c, depth+1, clauses, fmt.Sprintf("%s.%s[%d]", where, name, i))
			if err != nil {
				return nil, err
			}
			fs = append(fs, f)
		}
		if all {
			return func(r *reqInfo) bool {
				for _, f := range fs {
					if !f(r) {
						return false
					}
				}
				return true
			}, nil
		}
		return func(r *reqInfo) bool {
			for _, f := range fs {
				if f(r) {
					return true
				}
			}
			return false
		}, nil
	}
	switch {
	case e.And != nil:
		return group(e.And, "and", true)
	case e.Or != nil:
		return group(e.Or, "or", false)
	case e.Not != nil:
		f, err := compileExpr(e.Not, depth+1, clauses, where+".not")
		if err != nil {
			return nil, err
		}
		return func(r *reqInfo) bool { return !f(r) }, nil
	}
	*clauses++
	return compileClause(e, where)
}

func compileClause(e *Expr, where string) (func(*reqInfo) bool, error) {
	ops, ok := attrOps[e.Attr]
	if !ok {
		return nil, fmt.Errorf("%s.attr: unknown attribute %q (ipv4.src, ip.src, http.host, http.path, http.header, http.method)", where, e.Attr)
	}
	okOp := false
	for _, o := range ops {
		okOp = okOp || o == e.Op
	}
	if !okOp {
		return nil, fmt.Errorf("%s.op: %q is not supported for %s (use %s)", where, e.Op, e.Attr, strings.Join(ops, ", "))
	}
	list := e.Op == "in" || e.Op == "not_in"
	if list != e.Value.IsList {
		if list {
			return nil, fmt.Errorf("%s.value: %s needs an array of strings", where, e.Op)
		}
		return nil, fmt.Errorf("%s.value: %s needs a single string", where, e.Op)
	}
	vals := []string{e.Value.One}
	if list {
		vals = e.Value.List
		if len(vals) == 0 || len(vals) > maxListLen {
			return nil, fmt.Errorf("%s.value: needs 1..%d entries", where, maxListLen)
		}
	}
	for _, v := range vals {
		if len(v) > maxValueLen {
			return nil, fmt.Errorf("%s.value: longer than %d characters", where, maxValueLen)
		}
		if e.Op == "matches" && strings.Count(v, "*") > maxWildcards {
			return nil, fmt.Errorf("%s.value: more than %d * wildcards", where, maxWildcards)
		}
	}
	if (e.Attr == "http.header") != (e.Key != "") {
		if e.Key == "" {
			return nil, fmt.Errorf("%s.key: http.header needs a header name", where)
		}
		return nil, fmt.Errorf("%s.key: only http.header takes a key", where)
	}
	if e.Key != "" && !validToken(e.Key) {
		return nil, fmt.Errorf("%s.key: %q is not a valid header name", where, e.Key)
	}
	neg := e.Op == "neq" || e.Op == "not_in"
	switch e.Attr {
	case "ipv4.src", "ip.src":
		ps := make([]netip.Prefix, 0, len(vals))
		for _, v := range vals {
			p, err := ParsePrefix(v)
			if err != nil || (e.Attr == "ipv4.src" && !p.Addr().Is4()) {
				return nil, fmt.Errorf("%s.value: %q is not an IPv4 address or CIDR", where, v)
			}
			ps = append(ps, p)
		}
		v4only := e.Attr == "ipv4.src"
		return func(r *reqInfo) bool {
			if !r.ip.IsValid() || (v4only && !r.ip.Is4()) {
				return false // 값이 없으면 부정 비교도 맞지 않는다
			}
			return containsIP(ps, r.ip) != neg
		}, nil
	case "http.method":
		for _, v := range vals {
			if !validToken(v) {
				return nil, fmt.Errorf("%s.value: %q is not an HTTP method", where, v)
			}
		}
		return func(r *reqInfo) bool { return contains(vals, r.method) != neg }, nil
	}
	var get func(*reqInfo) (string, bool)
	switch e.Attr {
	case "http.host":
		get = func(r *reqInfo) (string, bool) { return r.host, r.host != "" }
	case "http.path":
		get = func(r *reqInfo) (string, bool) { return r.path, true }
	case "http.header":
		key := http.CanonicalHeaderKey(e.Key)
		get = func(r *reqInfo) (string, bool) {
			v, ok := r.header[key]
			if !ok || len(v) == 0 {
				return "", false
			}
			return strings.Join(v, ", "), true
		}
	}
	var cmp func(string) bool
	switch e.Op {
	case "eq", "neq":
		cmp = func(s string) bool { return s == vals[0] }
	case "in":
		cmp = func(s string) bool { return contains(vals, s) }
	case "contains":
		cmp = func(s string) bool { return strings.Contains(s, vals[0]) }
	case "matches":
		pat := vals[0]
		cmp = func(s string) bool { return wildcardMatch(pat, s) }
	}
	return func(r *reqInfo) bool {
		s, ok := get(r)
		if !ok {
			return false
		}
		return cmp(s) != neg
	}, nil
}

func validateAction(a Action, where string) error {
	p := ActionParams{}
	if a.Params != nil {
		p = *a.Params
	}
	noParams := func() error {
		if p != (ActionParams{}) {
			return fmt.Errorf("%s.params: %s takes no parameters", where, a.Action)
		}
		return nil
	}
	switch a.Action {
	case ActBlock:
		if p.Status != 0 && (p.Status < 400 || p.Status > 499) {
			return fmt.Errorf("%s.params.status: block status must be 400..499", where)
		}
		if len(p.Body) > maxBlockBodyLen {
			return fmt.Errorf("%s.params.body: longer than 4096 bytes", where)
		}
	case ActAllow, ActChallenge:
		return noParams()
	case ActRedirect:
		if (p.Host == "") == (p.Location == "") {
			return fmt.Errorf("%s.params: redirect needs exactly one of host or location", where)
		}
		if p.Location != "" {
			u, err := url.Parse(p.Location)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return fmt.Errorf("%s.params.location: must be an absolute http(s) URL", where)
			}
			if p.PreservePath != nil || p.PreserveQuery != nil {
				return fmt.Errorf("%s.params: preserve_path/preserve_query only apply to host redirects", where)
			}
		}
		if p.Host != "" && (len(p.Host) > 253 || strings.Trim(strings.ToLower(p.Host), "abcdefghijklmnopqrstuvwxyz0123456789.-:") != "") {
			return fmt.Errorf("%s.params.host: %q is not a host name", where, p.Host)
		}
		switch p.Status {
		case 0, 301, 302, 307, 308:
		default:
			return fmt.Errorf("%s.params.status: redirect status must be 301, 302, 307 or 308", where)
		}
	case ActCacheOverride:
		if p.Bypass == (p.TTLSeconds != 0) {
			return fmt.Errorf("%s.params: cache_override needs exactly one of ttl_seconds or bypass", where)
		}
		if p.TTLSeconds < 0 || p.TTLSeconds > maxCacheOverride {
			return fmt.Errorf("%s.params.ttl_seconds: must be 1..2592000", where)
		}
	case ActRewrite:
		if !strings.HasPrefix(p.Path, "/") || strings.HasPrefix(p.Path, "//") || len(p.Path) > 2048 || strings.ContainsAny(p.Path, " \r\n#") {
			return fmt.Errorf("%s.params.path: rewrite needs a path starting with /", where)
		}
	case ActSetHeader:
		if !validToken(p.Name) {
			return fmt.Errorf("%s.params.name: %q is not a valid header name", where, p.Name)
		}
		if strings.ContainsAny(p.Value, "\r\n") || len(p.Value) > 2048 {
			return fmt.Errorf("%s.params.value: invalid header value", where)
		}
		switch p.Target {
		case "", "response", "request":
		default:
			return fmt.Errorf("%s.params.target: must be response or request", where)
		}
		if h := http.CanonicalHeaderKey(p.Name); h == "Host" || h == "Content-Length" || h == "Transfer-Encoding" || h == "Connection" || strings.HasPrefix(h, "X-Keel-") {
			return fmt.Errorf("%s.params.name: %s cannot be set by a rule", where, h)
		}
	case "":
		return fmt.Errorf("%s.action: required", where)
	default:
		return fmt.Errorf("%s.action: unknown action %q (block, allow, challenge, redirect, cache_override, rewrite, set_header)", where, a.Action)
	}
	return nil
}

func validToken(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && !strings.ContainsRune("!#$%&'*+-.^_`|~", r) {
			return false
		}
	}
	return true
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// wildcardMatch는 pat 전체가 s와 맞는지다. *는 0개 이상의 아무 문자다.
func wildcardMatch(pat, s string) bool {
	parts := strings.Split(pat, "*")
	if len(parts) == 1 {
		return pat == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, mid := range parts[1 : len(parts)-1] {
		i := strings.Index(s, mid)
		if i < 0 {
			return false
		}
		s = s[i+len(mid):]
	}
	return len(s) >= len(last) && strings.HasSuffix(s, last)
}

// reqInfo는 규칙이 보는 요청 속성이다.
type reqInfo struct {
	ip     netip.Addr
	host   string
	path   string
	method string
	header http.Header
}
