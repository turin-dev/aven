package main

import (
	"encoding/json"
	"testing"
)

func TestParseLoginArgs(t *testing.T) {
	for _, tc := range []struct {
		args       []string
		url, email string
		bad        bool
	}{
		{[]string{"https://k.example"}, "https://k.example", "", false},
		{[]string{"--email", "a@x.io", "https://k.example"}, "https://k.example", "a@x.io", false},
		{[]string{"https://k.example", "--email=a@x.io"}, "https://k.example", "a@x.io", false},
		{[]string{}, "", "", true},
		{[]string{"--email"}, "", "", true},
		{[]string{"a", "b"}, "", "", true},
		{[]string{"--password", "x", "u"}, "", "", true},
	} {
		u, e, err := parseLoginArgs(tc.args)
		if (err != nil) != tc.bad || (!tc.bad && (u != tc.url || e != tc.email)) {
			t.Errorf("%v: %q %q %v", tc.args, u, e, err)
		}
	}
}

func TestWhoamiLine(t *testing.T) {
	var old meInfo
	json.Unmarshal([]byte(`{"user":"admin"}`), &old)
	if got := whoamiLine(old, "https://k", "session"); got != "admin @ https://k (session)" {
		t.Errorf("old server: %s", got)
	}
	var me meInfo
	json.Unmarshal([]byte(`{"user":"Alice","email":"a@x.io","role":"member","token":{"name":"ci","scope":"environment","role":"member"}}`), &me)
	if got := whoamiLine(me, "https://k", "KEEL_TOKEN"); got != "Alice <a@x.io> [member] @ https://k (KEEL_TOKEN, token ci environment/member)" {
		t.Errorf("new server: %s", got)
	}
}
