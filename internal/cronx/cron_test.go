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

// Next는 분 단위로 훑는 것과 같은 결과를 낸다(날짜를 건너뛰어도).
func TestNextMatchesBruteForce(t *testing.T) {
	exprs := []string{"30 4 * * *", "0 0 13 * 5", "*/15 9-17 * * MON-FRI", "0 12 1,15 * *", "5 0 * 2 SUN", "0 0 31 * *", "@weekly", "59 23 28-31 * *"}
	starts := []string{"2026-10-16 05:00", "2026-12-31 23:59", "2027-02-27 12:00", "2028-02-28 23:30"}
	for _, e := range exprs {
		s, err := Parse(e)
		if err != nil {
			t.Fatal(err)
		}
		for _, st := range starts {
			from := at(st)
			want := time.Time{}
			for c := from.Truncate(time.Minute).Add(time.Minute); c.Before(from.AddDate(1, 1, 0)); c = c.Add(time.Minute) {
				if s.Match(c) {
					want = c
					break
				}
			}
			if got := s.Next(from); !got.Equal(want) {
				t.Errorf("%q after %s: got %v, want %v", e, st, got, want)
			}
		}
	}
}

// 절대 오지 않는 날짜(2월 30일)는 빨리 zero를 돌려준다.
func TestNextImpossibleIsFast(t *testing.T) {
	s, err := Parse("0 0 30 2 *")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if got := s.Next(at("2026-10-16 05:00")); !got.IsZero() {
		t.Fatalf("next = %v", got)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Next took %s", d)
	}
}
