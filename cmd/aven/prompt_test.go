package main

import "testing"

func TestPickAnswer(t *testing.T) {
	opts := []string{"shop", "blog", "42"}
	for _, tc := range []struct {
		in        string
		allowNone bool
		want      int
		ok        bool
	}{
		{"1", false, 0, true},
		{"2", false, 1, true},
		{"blog", false, 1, true},
		{"3", false, 2, true}, // numbers are positions, even when a name looks like a number
		{"0", true, -1, true},
		{"0", false, 0, false},
		{"4", false, 0, false},
		{"", false, 0, false},
		{"nope", false, 0, false},
	} {
		got, ok := pickAnswer(tc.in, opts, tc.allowNone)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%q: %d %v", tc.in, got, ok)
		}
	}
}
