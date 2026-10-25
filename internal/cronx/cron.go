// Package cronx는 5필드 cron 표현식(분 시 일 월 요일)을 해석한다. 시간대는 UTC다.
// 지원: *, 숫자, a-b, a-b/n, */n, 쉼표 목록, 월/요일 이름(JAN, MON), @hourly/@daily/@weekly/@monthly/@yearly.
package cronx

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Schedule struct {
	minute, hour, dom, month, dow uint64
	domStar, dowStar              bool
}

var macros = map[string]string{
	"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *",
	"@weekly": "0 0 * * 0", "@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *",
}

var monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
var dowNames = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

func Parse(expr string) (*Schedule, error) {
	expr = strings.TrimSpace(expr)
	if m, ok := macros[strings.ToLower(expr)]; ok {
		expr = m
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return nil, fmt.Errorf("cron: expected 5 fields (minute hour day month weekday), got %d", len(f))
	}
	var s Schedule
	var err error
	if s.minute, err = field(f[0], 0, 59, nil); err != nil {
		return nil, fmt.Errorf("cron minute: %w", err)
	}
	if s.hour, err = field(f[1], 0, 23, nil); err != nil {
		return nil, fmt.Errorf("cron hour: %w", err)
	}
	if s.dom, err = field(f[2], 1, 31, nil); err != nil {
		return nil, fmt.Errorf("cron day-of-month: %w", err)
	}
	if s.month, err = field(f[3], 1, 12, monthNames); err != nil {
		return nil, fmt.Errorf("cron month: %w", err)
	}
	if s.dow, err = field(f[4], 0, 7, dowNames); err != nil {
		return nil, fmt.Errorf("cron weekday: %w", err)
	}
	if s.dow&(1<<7) != 0 { // 7 = 일요일
		s.dow |= 1
	}
	s.domStar = f[2] == "*" || strings.HasPrefix(f[2], "*/")
	s.dowStar = f[4] == "*" || strings.HasPrefix(f[4], "*/")
	return &s, nil
}

func field(s string, lo, hi int, names map[string]int) (uint64, error) {
	var bits uint64
	for _, part := range strings.Split(s, ",") {
		rng, stepS, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepS)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("bad step %q", stepS)
			}
			step = n
		}
		var a, b int
		switch {
		case rng == "*":
			a, b = lo, hi
		case strings.Contains(rng, "-"):
			x, y, _ := strings.Cut(rng, "-")
			var err error
			if a, err = num(x, names); err != nil {
				return 0, err
			}
			if b, err = num(y, names); err != nil {
				return 0, err
			}
		default:
			n, err := num(rng, names)
			if err != nil {
				return 0, err
			}
			a, b = n, n
			if hasStep {
				b = hi
			}
		}
		if a < lo || b > hi || a > b {
			return 0, fmt.Errorf("%q out of range %d-%d", part, lo, hi)
		}
		for i := a; i <= b; i += step {
			bits |= 1 << uint(i)
		}
	}
	return bits, nil
}

func num(s string, names map[string]int) (int, error) {
	if n, ok := names[strings.ToLower(s)]; ok {
		return n, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("bad value %q", s)
	}
	return n, nil
}

// Match는 t(분 단위)가 일정에 맞는지 알려준다.
func (s *Schedule) Match(t time.Time) bool {
	t = t.UTC()
	if s.minute&(1<<uint(t.Minute())) == 0 || s.hour&(1<<uint(t.Hour())) == 0 || s.month&(1<<uint(t.Month())) == 0 {
		return false
	}
	domOK := s.dom&(1<<uint(t.Day())) != 0
	dowOK := s.dow&(1<<uint(t.Weekday())) != 0
	// 표준 cron: 일/요일이 둘 다 제한되면 OR, 아니면 AND
	if !s.domStar && !s.dowStar {
		return domOK || dowOK
	}
	return domOK && dowOK
}

// Next는 after 이후 처음으로 맞는 시각(분 단위)을 돌려준다. 5년 안에 없으면 zero time.
func (s *Schedule) Next(after time.Time) time.Time {
	t := after.UTC().Truncate(time.Minute).Add(time.Minute)
	end := t.AddDate(5, 0, 0)
	for t.Before(end) {
		if s.month&(1<<uint(t.Month())) == 0 {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if s.Match(t) {
			return t
		}
		if s.hour&(1<<uint(t.Hour())) == 0 {
			t = t.Truncate(time.Hour).Add(time.Hour)
			continue
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}
