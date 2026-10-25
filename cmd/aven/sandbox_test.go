package main

import (
	"reflect"
	"testing"
)

func TestSplitVariableFlag(t *testing.T) {
	cases := map[string][]string{
		"A=1":                       {"A=1"},
		"A=1,B=2":                   {"A=1", "B=2"},
		"LIST=a,b,c":                {"LIST=a,b,c"}, // 값의 쉼표는 보존
		"A=1,B=x,y":                 {"A=1,B=x,y"},
		"URL=Postgres.DATABASE_URL": {"URL=Postgres.DATABASE_URL"},
	}
	for in, want := range cases {
		if got := splitVariableFlag(in); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestSandboxExecArgvAndExitCode(t *testing.T) {
	if got := sandboxExecArgv([]string{"ls | wc -l"}); got["command"] != "ls | wc -l" {
		t.Fatalf("single arg: %v", got)
	}
	if got := sandboxExecArgv([]string{"echo", "a b"}); !reflect.DeepEqual(got["cmd"], []string{"echo", "a b"}) {
		t.Fatalf("argv: %v", got)
	}
	for _, c := range []struct {
		r    sandboxExecResult
		want int
	}{{sandboxExecResult{ExitCode: 0}, 0}, {sandboxExecResult{ExitCode: 3}, 3}, {sandboxExecResult{ExitCode: -1}, 1},
		{sandboxExecResult{ExitCode: -1, TimedOut: true}, 124}} {
		if got := sandboxExitCode(c.r); got != c.want {
			t.Errorf("%+v: %d, want %d", c.r, got, c.want)
		}
	}
}

func TestSandboxShellURL(t *testing.T) {
	u, err := sandboxShellURL("https://aven.example.com/", "sbx_abc", []string{"ls", "-la"}, 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	if want := "wss://aven.example.com/api/sandboxes/sbx_abc/shell?cmd=ls+-la&cols=100&rows=30"; u != want {
		t.Fatalf("url = %s", u)
	}
	if _, err := sandboxShellURL("ftp://x", "sbx_abc", nil, 80, 24); err == nil {
		t.Fatal("ftp accepted")
	}
}
