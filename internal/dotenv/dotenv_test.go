package dotenv

import (
	"errors"
	"maps"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	src := "\uFEFF# header comment\r\n" +
		"\n" +
		"PLAIN=value\n" +
		"  SPACED  =   padded value   \n" +
		"export EXPORTED=yes\n" +
		"export=notprefix\n" +
		"EMPTY=\n" +
		"EMPTY2=   # only a comment\n" +
		"INLINE=abc # trailing comment\n" +
		"HASH=abc#def\n" +
		"LEADHASH=#notcomment\n" +
		"SINGLE='it is ${{ literal }} \\n'\n" +
		"DOUBLE=\"tab\\tnl\\nquote\\\" bs\\\\ dollar\\$ unknown\\q\"  # c\n" +
		"MULTI=\"line1\nline2\n\"\n" +
		"MULTISINGLE='a\nb'\n" +
		"BACK=`x 'y' \"z\"`\n" +
		"REF=${{ postgres.DATABASE_URL }}\n" +
		"dotted.key-1=ok\n" +
		"DUP=1\n" +
		"DUP=2\n" +
		"EQ=a=b=c\n"
	got, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"PLAIN":        "value",
		"SPACED":       "padded value",
		"EXPORTED":     "yes",
		"export":       "notprefix",
		"EMPTY":        "",
		"EMPTY2":       "",
		"INLINE":       "abc",
		"HASH":         "abc#def",
		"LEADHASH":     "#notcomment",
		"SINGLE":       `it is ${{ literal }} \n`,
		"DOUBLE":       "tab\tnl\nquote\" bs\\ dollar$ unknown\\q",
		"MULTI":        "line1\nline2\n",
		"MULTISINGLE":  "a\nb",
		"BACK":         `x 'y' "z"`,
		"REF":          "${{ postgres.DATABASE_URL }}",
		"dotted.key-1": "ok",
		"DUP":          "2",
		"EQ":           "a=b=c",
	}
	if !maps.Equal(got, want) {
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s = %q, want %q", k, got[k], v)
			}
		}
		for k := range got {
			if _, ok := want[k]; !ok {
				t.Errorf("unexpected key %s", k)
			}
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, tc := range []struct {
		src  string
		line int
	}{
		{"A=1\nNOEQUALS\n", 2},
		{"1BAD=x", 1},
		{"A=\"open\nstill", 1},
		{"A='x' trailing", 1},
		{"\n\nB=\"x\"y", 3},
		{"BAD KEY=1", 1},
		{"=value", 1},
	} {
		_, err := Parse(tc.src)
		var pe *Error
		if !errors.As(err, &pe) {
			t.Errorf("%q: want *Error, got %v", tc.src, err)
			continue
		}
		if pe.Line != tc.line {
			t.Errorf("%q: line %d, want %d (%v)", tc.src, pe.Line, tc.line, err)
		}
	}
}

func TestLineNumbersAfterMultiline(t *testing.T) {
	_, err := Parse("A=\"x\ny\nz\"\nB=ok\nC")
	var pe *Error
	if !errors.As(err, &pe) || pe.Line != 5 {
		t.Fatalf("got %v", err)
	}
}

func TestRoundTrip(t *testing.T) {
	values := []string{
		"", "plain", "with space", " leading", "trailing ", "a#b", " #x", "#start",
		"quote'single", `quote"double`, "both'and\"", "back`tick", `back\slash`, `\n literal`,
		"line1\nline2", "crlf\r\nend", "\n", "tab\there", "${{ postgres.DATABASE_URL }}", "$HOME ${VAR}",
		"유니코드 값", "=", "a=b", "'", `"`, `\`, "trailing\\", "mixed ' \" \\ $ ` \n end",
		"postgres://u:p@h:5432/db?sslmode=disable", "-----BEGIN KEY-----\nabc\n-----END KEY-----\n",
	}
	m := map[string]string{}
	for i, v := range values {
		m["K"+string(rune('A'+i%26))+strings.Repeat("_", i/26)] = v
	}
	text := Marshal(m)
	got, err := Parse(text)
	if err != nil {
		t.Fatalf("parse marshalled: %v\n%s", err, text)
	}
	for k, v := range m {
		if got[k] != v {
			t.Errorf("%s: %q → %q (text %q)", k, v, got[k], Quote(v))
		}
	}
	if len(got) != len(m) {
		t.Errorf("len %d, want %d", len(got), len(m))
	}
	// 두 번째 왕복도 같은 텍스트
	if again := Marshal(got); again != text {
		t.Errorf("not stable:\n%s\n---\n%s", text, again)
	}
}

func TestMarshalSorted(t *testing.T) {
	got := Marshal(map[string]string{"B": "2", "A": "1 2", "C": ""})
	want := "A='1 2'\nB=2\nC=\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func FuzzRoundTrip(f *testing.F) {
	for _, s := range []string{"", "a b", "x\ny", `"'\$`, "#", " # "} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		if strings.ContainsRune(v, 0) {
			return
		}
		got, err := Parse(Marshal(map[string]string{"K": v}))
		if err != nil {
			t.Fatalf("%q: %v", v, err)
		}
		// 따옴표 없는 값의 앞뒤 공백 같은 것은 Quote가 따옴표를 쓴다. CR 단독도 \r 이스케이프로 보존된다.
		if got["K"] != v {
			t.Fatalf("%q → %q", v, got["K"])
		}
	})
}
