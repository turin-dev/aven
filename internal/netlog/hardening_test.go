package netlog

import (
	"context"
	"errors"
	"net/netip"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/turin-dev/aven/internal/node"
)

type failingDocker struct{ calls atomic.Int64 }

func (d *failingDocker) ManagedNetworks(context.Context) ([]string, error) {
	d.calls.Add(1)
	return nil, errors.New("docker down")
}
func (d *failingDocker) NetworkInfo(context.Context, string) (*node.NetworkInfo, error) {
	return nil, errors.New("docker down")
}
func (d *failingDocker) ListManaged(context.Context) (map[string]map[string]string, error) {
	return nil, errors.New("docker down")
}
func (d *failingDocker) InspectMany(context.Context, []string) (map[string]*node.ContainerState, error) {
	return nil, errors.New("docker down")
}

// docker가 실패해도 모르는 주소마다 다시 묻지 않는다(캡처 goroutine이 매번 막혔다).
func TestClassifyBacksOffWhenDockerFails(t *testing.T) {
	d := &failingDocker{}
	c := &Collector{Docker: d}
	c.subnets = map[string][]netip.Prefix{"env_a": {netip.MustParsePrefix("172.20.0.0/16")}}
	for i := 0; i < 50; i++ {
		c.Classify("env_a", netip.AddrFrom4([4]byte{172, 20, 0, byte(10 + i)}))
	}
	if n := d.calls.Load(); n != 1 {
		t.Fatalf("docker queried %d times for 50 unknown addresses", n)
	}
}

// 기록 보관은 최근 ringSize개만 돌려주고, 넘쳐도 기록마다 전체를 복사하지 않는다.
func TestRingKeepsRecentEvents(t *testing.T) {
	c := &Collector{NodeID: "n"}
	for i := 0; i < 5*ringSize+7; i++ {
		c.record(Event{Kind: "dns"})
		if len(c.ring) >= 2*ringSize {
			t.Fatalf("ring grew to %d", len(c.ring))
		}
	}
	past, _, cancel := c.Subscribe()
	defer cancel()
	if len(past) != ringSize {
		t.Fatalf("replay has %d events, want %d", len(past), ringSize)
	}
	if want := "n-" + strconv.FormatInt(c.epoch, 10) + "-" + strconv.Itoa(5*ringSize+7); past[len(past)-1].ID != want {
		t.Fatalf("last replayed %s, want %s", past[len(past)-1].ID, want)
	}
}
