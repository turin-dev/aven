package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseAt(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"30m":                       now.Add(-30 * time.Minute),
		"2h":                        now.Add(-2 * time.Hour),
		"1d":                        now.Add(-24 * time.Hour),
		"1w":                        now.Add(-7 * 24 * time.Hour),
		"2026-07-20T12:00:00Z":      time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
		"2026-07-20T21:00:00+09:00": time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
		"2026-07-20 12:00":          time.Date(2026, 7, 20, 12, 0, 0, 0, time.Local),
	} {
		got, err := parseAt(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("%q → %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "yesterday", "5y", "-1h"} {
		if _, err := parseAt(bad, now); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParsePITRArgs(t *testing.T) {
	o, err := parsePITRArgs(nil)
	if err != nil || o.sub != "status" {
		t.Fatalf("%+v %v", o, err)
	}
	o, err = parsePITRArgs([]string{"restore", "--at", "1h", "--new-service-name", "pg-copy", "-y", "-s", "pg"})
	if err != nil || o.at != "1h" || o.newName != "pg-copy" || !o.yes || *o.tf.svc != "pg" {
		t.Fatalf("%+v %v", o, err)
	}
	o, err = parsePITRArgs([]string{"enable", "--retention-days", "14", "--destination", ""})
	if err != nil || o.retention != 14 || !o.destSet || o.dest != "" {
		t.Fatalf("%+v %v", o, err)
	}
	for _, bad := range [][]string{
		{"restore"},
		{"restore", "--at", "1h", "--in-place", "--new-service-name", "x"},
		{"status", "--at", "1h"},
		{"disable", "--retention-days", "3"},
		{"frobnicate"},
		{"status", "extra"},
	} {
		if _, err := parsePITRArgs(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestFormatPITR(t *testing.T) {
	e := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	s := &pitrStatus{Supported: true, Kind: "postgres"}
	s.Config.Enabled, s.Config.RetentionDays = true, 7
	s.Window.Earliest, s.Window.Latest, s.Window.Bases, s.Window.Logs, s.Window.Bytes = &e, &e, 1, 3, 2048
	out := formatPITR("pg", "production", s)
	for _, want := range []string{"on (postgres, retention 7 days, storage local)", "restore window:", "1 base backups, 3 log files, 2.0 KiB"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if out := formatPITR("cache", "production", &pitrStatus{Reason: "only pg"}); !strings.Contains(out, "not available: only pg") {
		t.Errorf("unsupported: %s", out)
	}
}
