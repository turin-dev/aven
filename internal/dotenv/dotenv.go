// Package dotenv는 .env 텍스트를 읽고 쓴다(변수 raw 편집기, CLI --env/--import).
//
// 읽기 규칙:
//   - 빈 줄과 '#'로 시작하는 줄은 주석이다. 줄 앞의 "export "는 무시한다.
//   - KEY=VALUE. 키와 '=' 둘레의 공백은 무시한다. 같은 키가 여러 번 나오면 마지막 값이 이긴다.
//   - 따옴표 없는 값은 앞뒤 공백을 지우고, 공백 뒤의 '#'부터는 주석이다.
//   - '...'와 `...`는 글자 그대로다(여러 줄 가능).
//   - "..."는 \n \r \t \" \\ \$ \' \` 이스케이프를 풀고 여러 줄일 수 있다. 모르는 이스케이프는 그대로 둔다.
//   - 닫는 따옴표 뒤에는 공백과 주석만 올 수 있다.
//   - ${VAR} 보간은 하지 않는다(aven의 ${{ }} 참조를 그대로 보존한다).
package dotenv

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Error는 줄 번호가 있는 파싱 오류다.
type Error struct {
	Line int
	Msg  string
}

func (e *Error) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// Pair는 파일에 나온 순서대로의 키와 값이다.
type Pair struct {
	Key, Value string
}

var keyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.\-]*$`)

// Parse는 .env 텍스트를 맵으로 읽는다.
func Parse(s string) (map[string]string, error) {
	pairs, err := ParsePairs(s)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		out[p.Key] = p.Value
	}
	return out, nil
}

// ParsePairs는 .env 텍스트를 나온 순서대로 읽는다(중복 키도 그대로 둔다).
func ParsePairs(s string) ([]Pair, error) {
	s = strings.TrimPrefix(s, "\uFEFF")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	p := &parser{src: s, line: 1}
	var out []Pair
	for {
		p.skipBlankAndComments()
		if p.eof() {
			return out, nil
		}
		pair, err := p.entry()
		if err != nil {
			return nil, err
		}
		out = append(out, pair)
	}
}

type parser struct {
	src  string
	pos  int
	line int
}

func (p *parser) eof() bool { return p.pos >= len(p.src) }

func (p *parser) errf(line int, f string, a ...any) error {
	return &Error{Line: line, Msg: fmt.Sprintf(f, a...)}
}

func (p *parser) peek() byte { return p.src[p.pos] }

// skipSpaces는 같은 줄의 공백과 탭을 건너뛴다.
func (p *parser) skipSpaces() {
	for !p.eof() && (p.peek() == ' ' || p.peek() == '\t' || p.peek() == '\r') {
		p.pos++
	}
}

// restOfLine은 줄 끝(개행 제외)까지 읽고 개행을 건너뛴다.
func (p *parser) restOfLine() string {
	start := p.pos
	for !p.eof() && p.peek() != '\n' {
		p.pos++
	}
	s := p.src[start:p.pos]
	if !p.eof() {
		p.pos++
		p.line++
	}
	return s
}

func (p *parser) skipBlankAndComments() {
	for !p.eof() {
		p.skipSpaces()
		if p.eof() {
			return
		}
		switch p.peek() {
		case '\n':
			p.pos++
			p.line++
		case '#':
			p.restOfLine()
		default:
			return
		}
	}
}

func (p *parser) entry() (Pair, error) {
	line := p.line
	// 키: '='나 줄 끝까지
	start := p.pos
	for !p.eof() && p.peek() != '=' && p.peek() != '\n' {
		p.pos++
	}
	head := strings.TrimSpace(p.src[start:p.pos])
	if p.eof() || p.peek() != '=' {
		return Pair{}, p.errf(line, "expected KEY=VALUE, got %q", truncate(head))
	}
	p.pos++ // '='
	if rest, ok := strings.CutPrefix(head, "export"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
		head = strings.TrimSpace(rest)
	}
	if !keyRe.MatchString(head) {
		return Pair{}, p.errf(line, "invalid variable name %q", truncate(head))
	}
	before := p.pos
	p.skipSpaces()
	if p.eof() || p.peek() == '\n' || (p.pos > before && p.peek() == '#') {
		p.restOfLine()
		return Pair{Key: head, Value: ""}, nil
	}
	var val string
	switch q := p.peek(); q {
	case '\'', '`', '"':
		p.pos++
		v, err := p.quoted(q, line)
		if err != nil {
			return Pair{}, err
		}
		// 닫는 따옴표 뒤에는 공백과 주석만
		p.skipSpaces()
		if !p.eof() && p.peek() != '\n' && p.peek() != '#' {
			return Pair{}, p.errf(p.line, "unexpected characters after closing quote of %s", head)
		}
		p.restOfLine()
		val = v
	default:
		raw := p.restOfLine()
		// 공백 뒤의 #부터 주석
		for i := 0; i < len(raw); i++ {
			if raw[i] == '#' && i > 0 && (raw[i-1] == ' ' || raw[i-1] == '\t') {
				raw = raw[:i]
				break
			}
		}
		val = strings.TrimSpace(raw)
	}
	return Pair{Key: head, Value: val}, nil
}

// quoted는 여는 따옴표 다음부터 닫는 따옴표까지 읽는다(닫는 따옴표 다음에 멈춘다).
func (p *parser) quoted(q byte, line int) (string, error) {
	var b strings.Builder
	for {
		if p.eof() {
			return "", p.errf(line, "unterminated %c quoted value", q)
		}
		c := p.peek()
		p.pos++
		switch {
		case c == q:
			return b.String(), nil
		case c == '\n':
			p.line++
			b.WriteByte(c)
		case c == '\\' && q == '"':
			if p.eof() {
				return "", p.errf(line, "unterminated %c quoted value", q)
			}
			e := p.peek()
			p.pos++
			switch e {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case '"', '\\', '$', '\'', '`':
				b.WriteByte(e)
			case '\n':
				// 줄 끝의 역슬래시는 그대로 둔다(값에 개행이 들어간다)
				p.line++
				b.WriteByte('\\')
				b.WriteByte('\n')
			default:
				b.WriteByte('\\')
				b.WriteByte(e)
			}
		default:
			b.WriteByte(c)
		}
	}
}

func truncate(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}

var plainRe = regexp.MustCompile(`^[A-Za-z0-9_./:@%+,=\-]+$`)

// Quote는 값을 .env 값 표기로 바꾼다. Parse가 원래 값으로 되돌린다.
// 안전한 글자만 있으면 그대로, 작은따옴표·개행이 없으면 '...', 아니면 이스케이프한 "..."다.
func Quote(v string) string {
	if v == "" {
		return ""
	}
	if plainRe.MatchString(v) {
		return v
	}
	if !strings.ContainsAny(v, "'\n\r") {
		return "'" + v + "'"
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(v); i++ {
		switch c := v[i]; c {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '$':
			b.WriteString(`\$`)
		case '`':
			b.WriteString("\\`")
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Marshal은 맵을 키 이름 순서의 .env 텍스트로 쓴다(줄마다 개행으로 끝난다).
func Marshal(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(Quote(m[k]))
		b.WriteByte('\n')
	}
	return b.String()
}

// ValidKey는 .env로 쓸 수 있는 변수 이름인지 알려준다.
func ValidKey(k string) bool { return keyRe.MatchString(k) }
