package cronx

import (
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestMatch(t *testing.T) {
	cases := []struct {
		expr string
		t    string
		want bool
	}{
		{"* * * * *", "2026-10-16 12:34", true},
		{"*/5 * * * *", "2026-10-16 12:35", true},
		{"*/5 * * * *", "2026-10-16 12:36", false},
		{"0 3 * * *", "2026-10-16 03:00", true},
		{"0 3 * * *", "2026-10-16 04:00", false},
		{"15,45 9-17 * * MON-FRI", "2026-10-16 09:45", true},  // 금요일
		{"15,45 9-17 * * MON-FRI", "2026-10-17 09:45", false}, // 토요일
		{"0 0 * * 7", "2026-10-18 00:00", true},               // 일요일=7
		{"0 0 1 jan *", "2027-01-01 00:00", true},
		{"0 0 13 * 5", "2026-10-16 00:00", true}, // dom/dow 둘 다 제한 → OR (16일 금요일)
		{"10-20/5 * * * *", "2026-10-16 00:15", true},
		{"10-20/5 * * * *", "2026-10-16 00:25", false},
		{"@hourly", "2026-10-16 07:00", true},
		{"@daily", "2026-10-16 07:00", false},
	}
	for _, c := range cases {
		s, err := Parse(c.expr)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		if got := s.Match(at(c.t)); got != c.want {
			t.Errorf("%s @ %s = %v, want %v", c.expr, c.t, got, c.want)
		}
	}
}

func TestInvalid(t *testing.T) {
	for _, e := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "*/0 * * * *", "a b c d e", "5-1 * * * *", "* * * * * *"} {
		if _, err := Parse(e); err == nil {
			t.Errorf("%q: want error", e)
		}
	}
}

func TestNext(t *testing.T) {
	s, _ := Parse("30 4 * * *")
	if got := s.Next(at("2026-10-16 05:00")); !got.Equal(at("2026-10-17 04:30")) {
		t.Errorf("next = %v", got)
	}
	s, _ = Parse("0 0 29 2 *")
	if got := s.Next(at("2026-10-16 05:00")); !got.Equal(at("2028-02-29 00:00")) {
		t.Errorf("leap next = %v", got)
	}
}
