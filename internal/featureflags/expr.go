// Package featureflags parses and evaluates feature flag targeting expressions.
package featureflags

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"regexp"
	"strconv"
	"strings"
)

const MaxExpressionLength = 2048

type token struct {
	kind string
	text string
	val  any
}

type node struct {
	kind string
	val  any
	name string
	op   string
	a, b *node
	list []*node
}

type parser struct {
	tokens []token
	i      int
}

// Validate checks expression syntax before it is stored.
func Validate(expression string) error {
	root, err := parse(expression)
	if err != nil {
		return err
	}
	return validateBoolean(root)
}

// Evaluate returns whether expression matches context. Missing attributes never match.
func Evaluate(expression, flagName string, context map[string]any) (bool, error) {
	root, err := parse(expression)
	if err != nil {
		return false, err
	}
	v, present, err := eval(root, flagName, context)
	if err != nil || !present {
		return false, err
	}
	b, ok := v.(bool)
	if !ok {
		return false, errors.New("targeting expression must produce a boolean")
	}
	return b, nil
}

func parse(s string) (*node, error) {
	if strings.TrimSpace(s) == "" || len(s) > MaxExpressionLength {
		return nil, errors.New("expression must be 1..2048 bytes")
	}
	ts, err := lex(s)
	if err != nil {
		return nil, err
	}
	p := parser{tokens: ts}
	n, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != "eof" {
		return nil, fmt.Errorf("unexpected token %q", p.peek().text)
	}
	return n, nil
}

func lex(s string) ([]token, error) {
	out := make([]token, 0, len(s)/2)
	for i := 0; i < len(s); {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			i++
			continue
		}
		if c == '"' {
			start := i
			i++
			escaped := false
			for i < len(s) {
				if escaped {
					escaped = false
					i++
					continue
				}
				if s[i] == '\\' {
					escaped = true
					i++
					continue
				}
				if s[i] == '"' {
					i++
					break
				}
				i++
			}
			if i > len(s) || s[i-1] != '"' {
				return nil, errors.New("unterminated string")
			}
			var v string
			if err := json.Unmarshal([]byte(s[start:i]), &v); err != nil {
				return nil, errors.New("invalid string literal")
			}
			out = append(out, token{kind: "literal", text: s[start:i], val: v})
			continue
		}
		if isIdentStart(c) {
			start := i
			i++
			for i < len(s) && isIdent(s[i]) {
				i++
			}
			word := s[start:i]
			switch word {
			case "true":
				out = append(out, token{kind: "literal", text: word, val: true})
			case "false":
				out = append(out, token{kind: "literal", text: word, val: false})
			case "null":
				out = append(out, token{kind: "literal", text: word, val: nil})
			case "in", "contains", "matches":
				out = append(out, token{kind: "op", text: word})
			default:
				out = append(out, token{kind: "ident", text: word})
			}
			continue
		}
		if (c >= '0' && c <= '9') || (c == '-' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9') || (c == '.' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9') {
			start := i
			i++
			for i < len(s) && strings.ContainsRune("0123456789.eE+-", rune(s[i])) {
				i++
			}
			text := s[start:i]
			n, err := strconv.ParseFloat(text, 64)
			if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
				return nil, fmt.Errorf("invalid number %q", text)
			}
			out = append(out, token{kind: "literal", text: text, val: n})
			continue
		}
		matched := ""
		for _, op := range []string{"&&", "||", "==", "!=", "<=", ">="} {
			if strings.HasPrefix(s[i:], op) {
				matched = op
				break
			}
		}
		if matched != "" {
			out = append(out, token{kind: "op", text: matched})
			i += len(matched)
			continue
		}
		if strings.ContainsRune("!<>()[],", rune(c)) {
			kind := "op"
			if strings.ContainsRune("()[],", rune(c)) {
				kind = "punct"
			}
			out = append(out, token{kind: kind, text: string(c)})
			i++
			continue
		}
		return nil, fmt.Errorf("unexpected character %q", c)
	}
	return append(out, token{kind: "eof"}), nil
}

func validateBoolean(n *node) error {
	if n == nil {
		return errors.New("incomplete expression")
	}
	switch n.kind {
	case "literal":
		if _, ok := n.val.(bool); ok {
			return nil
		}
	case "not":
		return validateBoolean(n.a)
	case "binary":
		switch n.op {
		case "&&", "||":
			if err := validateBoolean(n.a); err != nil {
				return err
			}
			return validateBoolean(n.b)
		case "==", "!=", "<", "<=", ">", ">=", "in", "contains", "matches":
			if n.op == "in" && n.b.kind != "list" {
				return errors.New("right side of 'in' must be a list")
			}
			if n.op == "matches" && n.b.kind == "literal" {
				pattern, ok := n.b.val.(string)
				if !ok {
					return errors.New("right side of 'matches' must be a string")
				}
				if _, err := regexp.Compile(pattern); err != nil {
					return fmt.Errorf("invalid regular expression: %w", err)
				}
			}
			return nil
		}
	}
	return errors.New("targeting expression must produce a boolean")
}

func isIdentStart(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isIdent(c byte) bool      { return isIdentStart(c) || c >= '0' && c <= '9' }

func (p *parser) peek() token {
	if p.i >= len(p.tokens) {
		return token{kind: "eof"}
	}
	return p.tokens[p.i]
}

func (p *parser) take(text string) bool {
	if p.peek().text == text {
		p.i++
		return true
	}
	return false
}

func (p *parser) parseOr() (*node, error) {
	n, err := p.parseAnd()
	for err == nil && p.take("||") {
		var right *node
		right, err = p.parseAnd()
		if err == nil {
			n = &node{kind: "binary", op: "||", a: n, b: right}
		}
	}
	return n, err
}

func (p *parser) parseAnd() (*node, error) {
	n, err := p.parseUnary()
	for err == nil && p.take("&&") {
		var right *node
		right, err = p.parseUnary()
		if err == nil {
			n = &node{kind: "binary", op: "&&", a: n, b: right}
		}
	}
	return n, err
}

func (p *parser) parseUnary() (*node, error) {
	if p.take("!") {
		n, err := p.parseUnary()
		return &node{kind: "not", a: n}, err
	}
	return p.parseCompare()
}

func (p *parser) parseCompare() (*node, error) {
	n, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	if p.peek().kind == "op" && strings.Contains(" == != < <= > >= in contains matches ", " "+p.peek().text+" ") {
		op := p.peek().text
		p.i++
		right, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		n = &node{kind: "binary", op: op, a: n, b: right}
	}
	return n, nil
}

func (p *parser) parsePrimary() (*node, error) {
	t := p.peek()
	switch t.kind {
	case "literal":
		p.i++
		return &node{kind: "literal", val: t.val}, nil
	case "ident":
		p.i++
		if t.text == "bucket" && p.take("(") {
			attr := p.peek()
			if attr.kind != "ident" || !p.take(attr.text) || !p.take(")") {
				return nil, errors.New("bucket() expects an attribute name")
			}
			return &node{kind: "bucket", name: attr.text}, nil
		}
		return &node{kind: "ident", name: t.text}, nil
	case "punct":
		if p.take("(") {
			n, err := p.parseOr()
			if err != nil || !p.take(")") {
				if err != nil {
					return nil, err
				}
				return nil, errors.New("missing closing parenthesis")
			}
			return n, nil
		}
		if p.take("[") {
			list := []*node{}
			if p.take("]") {
				return &node{kind: "list", list: list}, nil
			}
			for {
				x, err := p.parsePrimary()
				if err != nil {
					return nil, err
				}
				list = append(list, x)
				if p.take("]") {
					break
				}
				if !p.take(",") {
					return nil, errors.New("expected ',' or ']' in list")
				}
			}
			return &node{kind: "list", list: list}, nil
		}
	}
	return nil, fmt.Errorf("expected a value near %q", t.text)
}

func eval(n *node, flagName string, ctx map[string]any) (any, bool, error) {
	if n == nil {
		return nil, false, errors.New("incomplete expression")
	}
	switch n.kind {
	case "literal":
		return n.val, true, nil
	case "ident":
		v, ok := ctx[n.name]
		return v, ok && v != nil, nil
	case "bucket":
		v, ok := ctx[n.name]
		if !ok || v == nil {
			return nil, false, nil
		}
		key, ok := scalarKey(v)
		if !ok {
			return nil, false, nil
		}
		return StableBucket(flagName, key), true, nil
	case "list":
		values := make([]any, 0, len(n.list))
		for _, item := range n.list {
			v, ok, err := eval(item, flagName, ctx)
			if err != nil || !ok {
				return nil, false, err
			}
			values = append(values, v)
		}
		return values, true, nil
	case "not":
		v, ok, err := eval(n.a, flagName, ctx)
		if err != nil || !ok {
			return false, ok, err
		}
		b, isBool := v.(bool)
		if !isBool {
			return false, true, errors.New("! expects a boolean")
		}
		return !b, true, nil
	case "binary":
		left, lok, err := eval(n.a, flagName, ctx)
		if err != nil {
			return nil, false, err
		}
		if n.op == "&&" || n.op == "||" {
			lb, isBool := left.(bool)
			if lok && !isBool {
				return nil, true, errors.New("boolean operator expects booleans")
			}
			if n.op == "&&" && (!lok || !lb) {
				return false, true, nil
			}
			if n.op == "||" && lok && lb {
				return true, true, nil
			}
			right, rok, err := eval(n.b, flagName, ctx)
			if err != nil {
				return nil, false, err
			}
			rb, isBool := right.(bool)
			if rok && !isBool {
				return nil, true, errors.New("boolean operator expects booleans")
			}
			if n.op == "&&" {
				return lok && lb && rok && rb, true, nil
			}
			return lok && lb || rok && rb, true, nil
		}
		right, rok, err := eval(n.b, flagName, ctx)
		if err != nil || !lok || !rok {
			return false, false, err
		}
		return compare(left, n.op, right)
	}
	return nil, false, errors.New("unknown expression node")
}

func compare(a any, op string, b any) (any, bool, error) {
	switch op {
	case "==":
		return equal(a, b), true, nil
	case "!=":
		return !equal(a, b), true, nil
	case "in":
		values, ok := b.([]any)
		if !ok {
			return false, true, errors.New("right side of 'in' must be a list")
		}
		for _, v := range values {
			if equal(a, v) {
				return true, true, nil
			}
		}
		return false, true, nil
	case "contains":
		switch x := a.(type) {
		case string:
			y, ok := b.(string)
			return ok && strings.Contains(x, y), true, nil
		case []any:
			for _, v := range x {
				if equal(v, b) {
					return true, true, nil
				}
			}
			return false, true, nil
		default:
			return false, true, errors.New("contains expects a string or list")
		}
	case "matches":
		x, xok := a.(string)
		y, yok := b.(string)
		if !xok || !yok {
			return false, true, errors.New("matches expects strings")
		}
		matched, err := regexp.MatchString(y, x)
		return matched, true, err
	case "<", "<=", ">", ">=":
		if x, ok := number(a); ok {
			y, yok := number(b)
			if !yok {
				return false, true, nil
			}
			switch op {
			case "<":
				return x < y, true, nil
			case "<=":
				return x <= y, true, nil
			case ">":
				return x > y, true, nil
			default:
				return x >= y, true, nil
			}
		}
		x, xok := a.(string)
		y, yok := b.(string)
		if !xok || !yok {
			return false, true, nil
		}
		switch op {
		case "<":
			return x < y, true, nil
		case "<=":
			return x <= y, true, nil
		case ">":
			return x > y, true, nil
		default:
			return x >= y, true, nil
		}
	}
	return false, true, nil
}

func equal(a, b any) bool {
	if x, ok := number(a); ok {
		if y, yes := number(b); yes {
			return x == y
		}
	}
	return fmt.Sprintf("%T:%v", a, a) == fmt.Sprintf("%T:%v", b, b)
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		x, err := n.Float64()
		return x, err == nil
	}
	return 0, false
}

func scalarKey(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case bool:
		return strconv.FormatBool(x), true
	default:
		if n, ok := number(v); ok {
			return strconv.FormatFloat(n, 'g', -1, 64), true
		}
	}
	return "", false
}

// StableBucket returns the same [0,1) value for a flag and targeting key everywhere:
// FNV-1a 64 of flag + NUL + key, mixed with the murmur3 fmix64 finalizer (FNV alone leaves the high bits
// poorly spread for keys that differ only at the end, such as user-1, user-2), top 53 bits / 2^53.
// Simple enough for the TypeScript SDK to compute synchronously with BigInt.
func StableBucket(flagName, key string) float64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(flagName + "\x00" + key))
	k := h.Sum64()
	k ^= k >> 33
	k *= 0xff51afd7ed558ccd
	k ^= k >> 33
	k *= 0xc4ceb9fe1a85ec53
	k ^= k >> 33
	return float64(k>>11) / 9007199254740992
}
