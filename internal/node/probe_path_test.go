package node

import (
	"context"
	"testing"
)

// A healthcheck path that is not a valid URL must fail the probe, not panic.
func TestProbeStateInvalidPathDoesNotPanic(t *testing.T) {
	st := &ContainerState{IPs: map[string][]string{"net": {"127.0.0.1"}}}
	for _, p := range []string{"/%zz", "/a\nb", "/\x7f"} {
		ok, detail := ProbeState(context.Background(), st, "net", "http", 1, p)
		if ok || detail == "" {
			t.Errorf("%q: ok=%v detail=%q", p, ok, detail)
		}
	}
}
