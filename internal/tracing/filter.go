package tracing

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/turin-dev/aven/internal/store"
)

// Filter는 trace 검색 문법(로그 필터와 같음)을 해석한 식이다. span 하나에 대해 참/거짓을 낸다.
//
//	keyword            span 이름에 포함(대소문자 무시)
//	@field:value       내장 필드 또는 속성/리소스 속성. value 끝의 *는 접두어 일치
//	@field:>N / <N / >=N / <=N / N-M  숫자 비교(@duration은 ms)
//	A B / A AND B, A OR B, -A, ( ... )
type Filter struct {
	root node
}

type node interface {
	match(*store.TraceSpan) bool
}

type andNode []node
type orNode []node
type notNode struct{ n node }
type termNode struct {
	field string // ""이면 키워드
	value string
}

func (a andNode) match(s *store.TraceSpan) bool {
	for _, n := range a {
		if !n.match(s) {
			return false
		}
	}
	return true
}

func (o orNode) match(s *store.TraceSpan) bool {
	for _, n := range o {
		if n.match(s) {
			return true
		}
	}
	return false
}

func (n notNode) match(s *store.TraceSpan) bool { return !n.n.match(s) }

// ParseFilter는 검색어를 해석한다. 빈 검색어는 모든 span과 맞는다.
func ParseFilter(q string) (*Filter, error) {
	toks, err := tokenize(q)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	if len(toks) == 0 {
		return &Filter{root: andNode{}}, nil
	}
	n, err := p.or()
	if err != nil {
		return nil, err
	}
	if p.i < len(p.toks) {
		return nil, fmt.Errorf("unexpected %q in filter", p.toks[p.i])
	}
	return &Filter{root: n}, nil
}

// Match는 span이 식과 맞는지 본다.
func (f *Filter) Match(s *store.TraceSpan) bool {
	if f == nil || f.root == nil {
		return true
	}
	return f.root.match(s)
}

// MatchSpan은 ParseFilter + Match의 줄임이다. 해석할 수 없는 검색어는 어느 span과도 맞지 않는다.
func MatchSpan(span store.TraceSpan, query string) bool {
	f, err := ParseFilter(query)
	return err == nil && f.Match(&span)
}

// tokenize는 괄호, 따옴표 묶음, 공백으로 나눈다. 따옴표 안 공백은 값에 남는다.
func tokenize(q string) ([]string, error) {
	var toks []string
	var cur strings.Builder
	quoted := false
	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, cur.String())
			cur.Reset()
		}
	}
	for _, r := range q {
		switch {
		case r == '"':
			quoted = !quoted
		case quoted:
			cur.WriteRune(r)
		case r == '(' || r == ')':
			flush()
			toks = append(toks, string(r))
		case r == ' ' || r == '\t' || r == '\n':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	if quoted {
		return nil, fmt.Errorf("unterminated quote in filter")
	}
	flush()
	return toks, nil
}

type parser struct {
	toks []string
	i    int
}

func (p *parser) peek() string {
	if p.i < len(p.toks) {
		return p.toks[p.i]
	}
	return ""
}

func (p *parser) or() (node, error) {
	var parts orNode
	for {
		n, err := p.and()
		if err != nil {
			return nil, err
		}
		parts = append(parts, n)
		if !strings.EqualFold(p.peek(), "OR") {
			break
		}
		p.i++
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	return parts, nil
}

func (p *parser) and() (node, error) {
	var parts andNode
	for p.i < len(p.toks) {
		t := p.peek()
		if t == ")" || strings.EqualFold(t, "OR") {
			break
		}
		if strings.EqualFold(t, "AND") {
			p.i++
			continue
		}
		n, err := p.unary()
		if err != nil {
			return nil, err
		}
		parts = append(parts, n)
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("empty expression in filter")
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	return parts, nil
}

func (p *parser) unary() (node, error) {
	t := p.peek()
	switch {
	case t == "(":
		p.i++
		n, err := p.or()
		if err != nil {
			return nil, err
		}
		if p.peek() != ")" {
			return nil, fmt.Errorf("missing ) in filter")
		}
		p.i++
		return n, nil
	case t == "-" || strings.EqualFold(t, "NOT"):
		p.i++
		n, err := p.unary()
		if err != nil {
			return nil, err
		}
		return notNode{n}, nil
	case strings.HasPrefix(t, "-") && len(t) > 1:
		p.toks[p.i] = t[1:]
		n, err := p.unary()
		if err != nil {
			return nil, err
		}
		return notNode{n}, nil
	}
	p.i++
	if k, v, ok := strings.Cut(t, ":"); ok && strings.HasPrefix(k, "@") && len(k) > 1 {
		return termNode{field: k[1:], value: v}, nil
	}
	return termNode{value: t}, nil
}

func (t termNode) match(s *store.TraceSpan) bool {
	if t.field == "" {
		return strings.Contains(strings.ToLower(s.Name), strings.ToLower(t.value))
	}
	switch t.field {
	case "duration":
		return compare(s.DurationMS, t.value)
	case "trace":
		return strings.EqualFold(s.TraceID, t.value)
	case "span":
		return strings.EqualFold(s.SpanID, t.value)
	case "name":
		return textMatch(s.Name, t.value)
	case "serviceName":
		name := stringValue(s.ResourceAttributes["service.name"])
		if name == "" {
			name = s.ServiceName
		}
		return textMatch(name, t.value)
	case "service":
		return strings.EqualFold(s.ServiceID, t.value) || textMatch(s.ServiceName, t.value)
	case "deployment":
		return textMatch(s.DeploymentID, t.value)
	case "replica":
		return textMatch(s.ReplicaID, t.value)
	case "component":
		return strings.EqualFold(s.Component, t.value)
	case "kind":
		return strings.EqualFold(s.Kind, t.value)
	case "status":
		return strings.EqualFold(s.Status, t.value)
	}
	v, ok := s.Attributes[t.field]
	if !ok {
		v, ok = s.ResourceAttributes[t.field]
	}
	if !ok {
		return false
	}
	switch x := v.(type) {
	case int64:
		return compare(float64(x), t.value)
	case float64:
		return compare(x, t.value)
	case int:
		return compare(float64(x), t.value)
	case bool:
		return strings.EqualFold(strconv.FormatBool(x), t.value)
	case string:
		if isComparison(t.value) {
			if f, err := strconv.ParseFloat(x, 64); err == nil {
				return compare(f, t.value)
			}
		}
		return textMatch(x, t.value)
	default:
		return textMatch(fmt.Sprint(x), t.value)
	}
}

// textMatch: 값 끝의 *는 접두어 일치, 아니면 대소문자 무시 완전 일치.
func textMatch(got, want string) bool {
	if strings.HasSuffix(want, "*") {
		return strings.HasPrefix(strings.ToLower(got), strings.ToLower(strings.TrimSuffix(want, "*")))
	}
	return strings.EqualFold(got, want)
}

func isComparison(v string) bool {
	return strings.HasPrefix(v, ">") || strings.HasPrefix(v, "<")
}

// compare는 >N, <N, >=N, <=N, N-M(포함 범위), N(같음)을 숫자로 비교한다.
func compare(actual float64, v string) bool {
	v = strings.TrimSpace(v)
	op := ""
	for _, p := range []string{">=", "<=", ">", "<", "="} {
		if strings.HasPrefix(v, p) {
			op, v = p, strings.TrimSpace(v[len(p):])
			break
		}
	}
	if op == "" {
		if lo, hi, ok := strings.Cut(v, "-"); ok && lo != "" {
			a, err1 := strconv.ParseFloat(lo, 64)
			b, err2 := strconv.ParseFloat(hi, 64)
			return err1 == nil && err2 == nil && actual >= a && actual <= b
		}
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return false
	}
	switch op {
	case ">":
		return actual > n
	case "<":
		return actual < n
	case ">=":
		return actual >= n
	case "<=":
		return actual <= n
	default:
		return actual == n
	}
}
