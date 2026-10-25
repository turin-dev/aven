package dbquery

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Result는 쿼리 결과다. Kind가 table이면 Columns/Rows, text면 Text다.
// Rows의 칸은 string, nil(NULL), 또는 mongo의 JSON 값(숫자/불리언/객체/배열)이다.
type Result struct {
	Kind      string   `json:"kind"` // table | text
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	Text      string   `json:"text"`
	Truncated bool     `json:"truncated"`
}

// TextResult는 글자 결과다.
func TextResult(s string, truncated bool) *Result {
	return &Result{Kind: "text", Columns: []string{}, Rows: [][]any{}, Text: toValid(s), Truncated: truncated}
}

func toValid(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "\uFFFD")
}

// ParseCSV는 psql --csv 출력(헤더 + 행)을 읽는다. 따옴표 없이 nullMarker와 같은 칸은 NULL(nil)이다.
// 열 수가 다른 줄이 있으면(여러 결과가 이어진 경우 등) 오류다. truncated면 마지막 미완성 줄은 버린다.
func ParseCSV(out []byte, nullMarker string, truncated bool) ([]string, [][]any, error) {
	recs, err := splitCSV(out, truncated)
	if err != nil {
		return nil, nil, err
	}
	if len(recs) == 0 {
		return nil, nil, errors.New("no result")
	}
	cols := []string{}
	for _, f := range recs[0] {
		cols = append(cols, toValid(f.v))
	}
	rows := [][]any{}
	for _, rec := range recs[1:] {
		if len(rec) != len(cols) {
			return nil, nil, fmt.Errorf("row has %d fields, header has %d", len(rec), len(cols))
		}
		if sameRecord(rec, recs[0]) {
			// psql은 결과마다 헤더를 찍는다: 같은 모양의 결과가 이어지면 헤더가 행처럼 보인다
			return nil, nil, errMultiResult
		}
		row := make([]any, len(rec))
		for i, f := range rec {
			if !f.quoted && f.v == nullMarker {
				row[i] = nil
			} else {
				row[i] = toValid(f.v)
			}
		}
		rows = append(rows, row)
	}
	return cols, rows, nil
}

type csvField struct {
	v      string
	quoted bool
}

// errMultiResult는 출력에 결과가 여러 개 이어져 있어 표 하나로 읽을 수 없다는 뜻이다(글자로 보여 준다).
var errMultiResult = errors.New("multiple result sets")

func sameRecord(a, b []csvField) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// splitCSV는 RFC 4180 CSV를 읽으면서 칸이 따옴표로 감싸였는지 기억한다(encoding/csv는 이것을 알려 주지 않는다).
func splitCSV(b []byte, truncated bool) ([][]csvField, error) {
	var recs [][]csvField
	var rec []csvField
	var cur strings.Builder
	quoted, inQuotes, fieldStarted := false, false, false
	endField := func() {
		rec = append(rec, csvField{v: cur.String(), quoted: quoted})
		cur.Reset()
		quoted, fieldStarted = false, false
	}
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inQuotes {
			if c == '"' {
				if i+1 < len(b) && b[i+1] == '"' {
					cur.WriteByte('"')
					i++
				} else {
					inQuotes = false
				}
			} else {
				cur.WriteByte(c)
			}
			continue
		}
		switch c {
		case '"':
			if fieldStarted && !quoted {
				cur.WriteByte(c) // 따옴표 없는 칸 가운데의 "는 글자로 둔다
			} else {
				quoted, inQuotes, fieldStarted = true, true, true
			}
		case ',':
			endField()
		case '\r':
			if i+1 < len(b) && b[i+1] == '\n' {
				continue
			}
			cur.WriteByte(c)
		case '\n':
			endField()
			recs = append(recs, rec)
			rec = nil
		default:
			fieldStarted = true
			cur.WriteByte(c)
		}
	}
	if inQuotes {
		if truncated {
			return recs, nil
		}
		return nil, errors.New("unterminated quoted field")
	}
	if fieldStarted || len(rec) > 0 || cur.Len() > 0 {
		if truncated {
			return recs, nil // 잘린 마지막 줄
		}
		endField()
		recs = append(recs, rec)
	}
	return recs, nil
}

// ParseTSV는 mysql --batch 출력(헤더 + 탭 구분 행)을 읽는다. \t \n \\ \0 이스케이프를 풀고, NULL은 nil이다
// (batch 모드는 NULL과 문자열 'NULL'을 구분하지 않는다). truncated면 마지막 미완성 줄은 버린다.
func ParseTSV(out []byte, truncated bool) ([]string, [][]any, error) {
	s := string(out)
	if truncated {
		if i := strings.LastIndexByte(s, '\n'); i >= 0 {
			s = s[:i+1]
		} else {
			s = ""
		}
	}
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil, nil, errors.New("no result")
	}
	lines := strings.Split(s, "\n")
	cols := []string{}
	for _, c := range strings.Split(lines[0], "\t") {
		cols = append(cols, toValid(unescapeTSV(c)))
	}
	rows := [][]any{}
	for _, ln := range lines[1:] {
		if ln == lines[0] {
			return nil, nil, errMultiResult // mysql도 결과마다 헤더를 찍는다
		}
		fs := strings.Split(ln, "\t")
		if len(fs) != len(cols) {
			return nil, nil, fmt.Errorf("row has %d fields, header has %d", len(fs), len(cols))
		}
		row := make([]any, len(fs))
		for i, f := range fs {
			if f == "NULL" {
				row[i] = nil
			} else {
				row[i] = toValid(unescapeTSV(f))
			}
		}
		rows = append(rows, row)
	}
	return cols, rows, nil
}

func unescapeTSV(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '0':
			b.WriteByte(0)
		case 'b':
			b.WriteByte('\b')
		case 'Z':
			b.WriteByte(26)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// TableResult는 행 수를 MaxRows로 자른 표 결과다.
func TableResult(cols []string, rows [][]any, truncated bool) *Result {
	if len(rows) > MaxRows {
		rows, truncated = rows[:MaxRows], true
	}
	if rows == nil {
		rows = [][]any{}
	}
	return &Result{Kind: "table", Columns: cols, Rows: rows, Truncated: truncated}
}

// SQLResult는 psql/mysql 쿼리 출력을 결과로 바꾼다. 표로 읽을 수 없으면(여러 결과, 출력 없음) 글자다.
func SQLResult(kind string, out []byte, truncated bool) *Result {
	if len(bytes.TrimSpace(out)) == 0 {
		return TextResult("", truncated)
	}
	var cols []string
	var rows [][]any
	var err error
	if kind == "postgres" {
		cols, rows, err = ParseCSV(out, NullMarker, truncated)
	} else {
		cols, rows, err = ParseTSV(out, truncated)
	}
	if err != nil {
		return TextResult(strings.ReplaceAll(string(out), NullMarker, "NULL"), truncated)
	}
	return TableResult(cols, rows, truncated)
}

// ParseMongoJSON은 mongosh --json 출력을 읽는다(숫자는 json.Number로 보존한다).
func ParseMongoJSON(out []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(out)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// DocsTable은 문서 배열을 표로 바꾼다: 열은 처음 나온 순서의 최상위 키(_id가 있으면 맨 앞), 없는 키는 nil.
// v가 객체 배열이 아니면 ok=false.
func DocsTable(v any) (cols []string, rows [][]any, ok bool) {
	arr, isArr := v.([]any)
	if !isArr {
		return nil, nil, false
	}
	seen := map[string]bool{}
	cols = []string{}
	for _, d := range arr {
		m, isObj := d.(map[string]any)
		if !isObj {
			return nil, nil, false
		}
		if _, ok := m["_id"]; ok && !seen["_id"] {
			seen["_id"] = true
			cols = append([]string{"_id"}, cols...)
		}
	}
	// 나머지 열: 문서 순서대로, 문서 안에서는 키 이름순(JSON 객체를 map으로 읽어 원래 순서는 알 수 없다).
	for _, d := range arr {
		for _, k := range orderedKeys(d.(map[string]any)) {
			if !seen[k] {
				seen[k] = true
				cols = append(cols, k)
			}
		}
	}
	rows = [][]any{}
	for _, d := range arr {
		m := d.(map[string]any)
		row := make([]any, len(cols))
		for i, c := range cols {
			if x, ok := m[c]; ok {
				row[i] = x
			}
		}
		rows = append(rows, row)
	}
	return cols, rows, true
}

// orderedKeys는 키를 사전순으로 돌려준다(encoding/json의 map은 순서를 보존하지 않는다).
func orderedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// MongoResult는 mongosh 출력을 결과로 바꾼다: 문서 배열이면 표, 아니면 보기 좋게 들여 쓴 JSON 글자.
func MongoResult(out []byte, truncated bool) *Result {
	if len(bytes.TrimSpace(out)) == 0 {
		return TextResult("", truncated)
	}
	v, err := ParseMongoJSON(out)
	if err != nil {
		return TextResult(string(out), truncated)
	}
	if cols, rows, ok := DocsTable(v); ok && len(rows) > 0 {
		return TableResult(cols, rows, truncated)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return TextResult(string(out), truncated)
	}
	return TextResult(string(b), truncated)
}
