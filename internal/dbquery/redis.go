package dbquery

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Reply는 redis-cli --no-raw 출력 한 개(RESP 응답 하나)를 읽은 값이다.
type Reply struct {
	// string | status | integer | double | bignum | nil | error | bool | verbatim | array | set | map
	// (map은 Elems에 키, 값을 번갈아 담는다)
	Type  string  `json:"type"`
	Str   string  `json:"str,omitempty"`
	Int   int64   `json:"int,omitempty"`
	Elems []Reply `json:"elems,omitempty"`
}

// QuoteRedisArg는 redis-cli가 stdin 줄을 나눌 때(sdssplitargs) 그대로 한 인자로 읽는 큰따옴표 문자열을 만든다.
// 출력 가능한 ASCII 밖의 바이트는 \xHH로 쓴다(키에 줄바꿈·따옴표·바이너리가 있어도 안전하다).
func QuoteRedisArg(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\':
			b.WriteString(`\\`)
		case c == '"':
			b.WriteString(`\"`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		case c < 0x20 || c >= 0x7f:
			fmt.Fprintf(&b, `\x%02x`, c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// RedisLine은 명령 하나를 stdin 한 줄로 만든다.
func RedisLine(args ...string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = QuoteRedisArg(a)
	}
	return strings.Join(q, " ") + "\n"
}

var (
	idxRe      = regexp.MustCompile(`^( *)(\d+)([)~#]) `)
	keyspaceRe = regexp.MustCompile(`(?m)^db(\d+):keys=(\d+),expires=(\d+)`)
)

// ParseRedis는 redis-cli --no-raw 출력에 들어 있는 응답들을 차례로 읽는다.
func ParseRedis(out string) ([]Reply, error) {
	out = strings.ReplaceAll(out, "\r\n", "\n")
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return []Reply{}, nil
	}
	p := &redisParser{lines: lines}
	var res []Reply
	for p.pos < len(p.lines) {
		r, err := p.value(0, p.lines[p.pos])
		if err != nil {
			return nil, err
		}
		res = append(res, r)
	}
	return res, nil
}

type redisParser struct {
	lines []string
	pos   int
}

// value는 현재 줄(rest는 그 줄의 col 이후)에서 시작하는 값 하나를 읽고 다음 줄로 넘어간다.
func (p *redisParser) value(col int, rest string) (Reply, error) {
	if m := idxRe.FindStringSubmatch(rest); m != nil {
		return p.array(col, rest)
	}
	p.pos++
	return scalar(rest)
}

// aggType은 번호 뒤 구분자의 응답 종류다: ")" 배열(push 포함), "~" set(RESP3), "#" map(RESP3).
func aggType(sep byte) string {
	switch sep {
	case '~':
		return "set"
	case '#':
		return "map"
	}
	return "array"
}

// array는 "1) 값\n2) 값"(배열), "1~ 값"(set), "1# 키 => 값"(map) 형식을 읽는다. 첫 원소는 rest에 있고,
// 다음 원소는 col만큼 들여 쓴 줄에 있다. map은 Elems에 키, 값을 번갈아 담는다.
func (p *redisParser) array(col int, rest string) (Reply, error) {
	var agg Reply
	var sep byte
	width := -1 // 번호 칸 폭(오른쪽 정렬: 원소가 10개 이상이면 " 1) "). 한 응답 안에서는 같다
	for want := 1; ; want++ {
		m := idxRe.FindStringSubmatch(rest)
		if m == nil {
			break
		}
		n, _ := strconv.Atoi(m[2])
		w := len(m[1]) + len(m[2])
		if n != want || (width >= 0 && w != width) || (sep != 0 && m[3][0] != sep) {
			break // 다음 응답의 시작
		}
		if sep == 0 {
			sep = m[3][0]
			agg = Reply{Type: aggType(sep), Elems: []Reply{}}
		}
		width = w
		body := rest[w+2:]
		if sep == '#' {
			k, v, ok := splitMapEntry(body)
			if !ok {
				return Reply{}, fmt.Errorf("bad map entry %q", body)
			}
			key, err := scalar(k)
			if err != nil {
				return Reply{}, err
			}
			agg.Elems = append(agg.Elems, key)
			body = v
		}
		el, err := p.value(col+w+2, body)
		if err != nil {
			return Reply{}, err
		}
		agg.Elems = append(agg.Elems, el)
		if p.pos >= len(p.lines) {
			break
		}
		ln := p.lines[p.pos]
		if len(ln) < col || strings.TrimLeft(ln[:col], " ") != "" {
			// verbatim 문자열(RESP3 '=')은 redis-cli가 들여쓰기 없이 그대로 찍는다: 중첩 응답 안에서 다음 번호가
			// 같은 들여쓰기로 이어지면 그 사이 줄은 앞 원소의 이어지는 줄이다.
			j := -1
			if col > 0 && el.Type == "status" {
				j = p.nextIndex(col, width, want+1, sep)
			}
			if j < 0 {
				break
			}
			lines := append([]string{el.Str}, p.lines[p.pos:j]...)
			agg.Elems[len(agg.Elems)-1] = Reply{Type: "verbatim", Str: strings.Join(lines, "\n")}
			p.pos = j
			ln = p.lines[j]
		}
		rest = ln[col:]
	}
	if sep == 0 { // "2) ..."처럼 1로 시작하지 않으면 배열이 아니라 글자다
		p.pos++
		return scalar(rest)
	}
	return agg, nil
}

// nextIndex는 p.pos부터 col만큼 들여 쓴 want번 원소 줄을 찾는다(최대 1000줄, 없으면 -1).
func (p *redisParser) nextIndex(col, width, want int, sep byte) int {
	for j := p.pos; j < len(p.lines) && j < p.pos+1000; j++ {
		ln := p.lines[j]
		if len(ln) < col || strings.TrimLeft(ln[:col], " ") != "" {
			continue
		}
		m := idxRe.FindStringSubmatch(ln[col:])
		if m == nil {
			continue
		}
		if n, _ := strconv.Atoi(m[2]); n == want && len(m[1])+len(m[2]) == width && m[3][0] == sep {
			return j
		}
	}
	return -1
}

// splitMapEntry는 map 원소 `키 => 값`을 나눈다. 키는 한 줄짜리 값이다(따옴표 문자열은 이스케이프를 건너뛴다).
func splitMapEntry(s string) (key, rest string, ok bool) {
	end := -1
	if strings.HasPrefix(s, `"`) {
		for i := 1; i < len(s); i++ {
			if s[i] == '\\' {
				i++
				continue
			}
			if s[i] == '"' {
				end = i + 1
				break
			}
		}
		if end < 0 || !strings.HasPrefix(s[end:], " => ") {
			return "", "", false
		}
	} else if end = strings.Index(s, " => "); end < 0 {
		return "", "", false
	}
	return s[:end], s[end+4:], true
}

func scalar(s string) (Reply, error) {
	switch {
	case s == "(nil)":
		return Reply{Type: "nil"}, nil
	case s == "(empty array)" || s == "(empty list or set)" || s == "(empty push)" || s == "(empty aggregate type)":
		return Reply{Type: "array", Elems: []Reply{}}, nil
	case s == "(empty set)":
		return Reply{Type: "set", Elems: []Reply{}}, nil
	case s == "(empty hash)":
		return Reply{Type: "map", Elems: []Reply{}}, nil
	case s == "(true)" || s == "(false)":
		return Reply{Type: "bool", Str: strings.Trim(s, "()")}, nil
	case strings.HasPrefix(s, "(big number) "):
		return Reply{Type: "bignum", Str: strings.TrimPrefix(s, "(big number) ")}, nil
	case strings.HasPrefix(s, "(integer) "):
		n, err := strconv.ParseInt(strings.TrimPrefix(s, "(integer) "), 10, 64)
		if err != nil {
			return Reply{}, fmt.Errorf("bad integer reply %q", s)
		}
		return Reply{Type: "integer", Int: n}, nil
	case strings.HasPrefix(s, "(double) "):
		return Reply{Type: "double", Str: strings.TrimPrefix(s, "(double) ")}, nil
	case strings.HasPrefix(s, "(error) "):
		return Reply{Type: "error", Str: strings.TrimPrefix(s, "(error) ")}, nil
	case len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"':
		v, err := unrepr(s[1 : len(s)-1])
		if err != nil {
			return Reply{}, err
		}
		return Reply{Type: "string", Str: v}, nil
	}
	return Reply{Type: "status", Str: s}, nil
}

// unrepr는 redis-cli가 문자열을 찍을 때 쓰는 이스케이프(sdscatrepr)를 푼다.
func unrepr(s string) (string, error) {
	if !strings.Contains(s, `\`) {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		if i+1 >= len(s) {
			return "", errors.New("bad escape in reply")
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'a':
			b.WriteByte('\a')
		case 'b':
			b.WriteByte('\b')
		case 'x':
			if i+2 >= len(s) {
				return "", errors.New("bad \\x escape in reply")
			}
			n, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
			if err != nil {
				return "", errors.New("bad \\x escape in reply")
			}
			b.WriteByte(byte(n))
			i += 2
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String(), nil
}

// KeyspaceDB는 INFO keyspace의 DB 하나다.
type KeyspaceDB struct {
	DB      int   `json:"db"`
	Keys    int64 `json:"keys"`
	Expires int64 `json:"expires"`
}

// ParseKeyspace는 INFO keyspace 출력(redis-cli는 INFO를 항상 raw로 찍는다)을 읽는다.
func ParseKeyspace(out string) []KeyspaceDB {
	out = strings.ReplaceAll(out, `\r\n`, "\n") // 혹시 따옴표로 찍힌 경우
	res := []KeyspaceDB{}
	for _, m := range keyspaceRe.FindAllStringSubmatch(out, -1) {
		db, _ := strconv.Atoi(m[1])
		k, _ := strconv.ParseInt(m[2], 10, 64)
		e, _ := strconv.ParseInt(m[3], 10, 64)
		res = append(res, KeyspaceDB{DB: db, Keys: k, Expires: e})
	}
	return res
}

// Text는 응답을 사람이 읽는 형태로 돌려준다(값 미리보기용).
func (r Reply) Text() string {
	switch r.Type {
	case "string", "status", "double", "bool", "bignum", "verbatim":
		return toValid(r.Str)
	case "integer":
		return strconv.FormatInt(r.Int, 10)
	case "nil":
		return ""
	case "error":
		return "(error) " + r.Str
	case "map":
		parts := []string{}
		for i := 0; i+1 < len(r.Elems); i += 2 {
			parts = append(parts, r.Elems[i].Text()+": "+r.Elems[i+1].Text())
		}
		return strings.Join(parts, ", ")
	}
	parts := make([]string, len(r.Elems))
	for i, e := range r.Elems {
		parts[i] = e.Text()
	}
	return strings.Join(parts, ", ")
}

// RedisResult는 redis 쿼리 출력을 결과로 바꾼다: 응답이 RESP3 map 하나면 key/value 표, 아니면 출력 그대로의 글자.
func RedisResult(out string, truncated bool) *Result {
	if !truncated {
		if reps, err := ParseRedis(out); err == nil && len(reps) == 1 && reps[0].Type == "map" && len(reps[0].Elems) > 0 {
			rows := [][]any{}
			e := reps[0].Elems
			for i := 0; i+1 < len(e); i += 2 {
				var v any = e[i+1].Text()
				if e[i+1].Type == "nil" {
					v = nil
				}
				rows = append(rows, []any{e[i].Text(), v})
			}
			return TableResult([]string{"key", "value"}, rows, false)
		}
	}
	return TextResult(out, truncated)
}
