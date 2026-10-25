package main

import (
	"net/netip"
	"testing"
)

func TestMeshBind(t *testing.T) {
	priv := netip.MustParseAddr("100.64.0.1")
	for in, want := range map[string]string{"": "100.64.0.1", "127.0.0.1": "127.0.0.1", "[::1]": "::1", "10.0.0.5": "10.0.0.5"} {
		got, err := meshBind(priv, in)
		if err != nil || got.String() != want {
			t.Errorf("meshBind(%q) = %v, %v; want %s", in, got, err, want)
		}
	}
	for _, bad := range []string{"0.0.0.0", "192.0.2.10", "::", "remote-node"} {
		if _, err := meshBind(priv, bad); err == nil {
			t.Errorf("meshBind(%q) should be rejected", bad)
		}
	}
}

func TestCheckMeshProxy(t *testing.T) {
	for _, ok := range []string{"127.0.0.1:1055", "[::1]:1055", "100.64.0.1:3128"} {
		if err := checkMeshProxy(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"127.0.0.1", "proxy:1055", "192.0.2.10:1055"} {
		if checkMeshProxy(bad) == nil {
			t.Errorf("%s should be rejected", bad)
		}
	}
}
