package deploy

import (
	"fmt"
	"testing"

	"github.com/turin-dev/aven/internal/config"
	"github.com/turin-dev/aven/internal/store"
)

func TestPublicIngressRank(t *testing.T) {
	cgnat := &store.Node{ID: "nod_cg", Traits: store.NodeTraits{CGNAT: true, DynamicIP: true}}
	dyn := &store.Node{ID: "nod_dyn", Traits: store.NodeTraits{DynamicIP: true}}
	tunnel := &store.Node{ID: "nod_tun", Traits: store.NodeTraits{CGNAT: true}, NodeIngress: store.NodeIngress{IngressMode: store.IngressCloudflareTunnel}}
	dc := &store.Node{ID: "nod_dc", Traits: store.NodeTraits{Site: "datacenter"}}
	for n, want := range map[*store.Node]int{cgnat: RankCGNAT, dyn: RankDynamicIP, tunnel: RankPublicOK, dc: RankPublicOK} {
		if got := PublicIngressRank(n); got != want {
			t.Errorf("%s: rank %d, want %d", n.ID, got, want)
		}
	}
	got := SortForPublic([]*store.Node{cgnat, dyn, tunnel, dc})
	var ids []string
	for _, n := range got {
		ids = append(ids, n.ID)
	}
	if fmt.Sprint(ids) != "[nod_tun nod_dc nod_dyn nod_cg]" {
		t.Fatalf("order %v", ids)
	}
}

// 같은 region에 집(CGNAT) 노드와 데이터센터 노드가 있으면, 공개 서비스는 정렬된 목록으로 데이터센터 노드를 고른다.
func TestMapRegionsPrefersPublicNodes(t *testing.T) {
	home := &store.Node{ID: "nod_home", Name: "kr-home", Schedulable: true, Labels: map[string]string{"region": "kr"}, Traits: store.NodeTraits{Site: "home", CGNAT: true}}
	vps := &store.Node{ID: "nod_vps", Name: "kr-vps", Schedulable: true, Labels: map[string]string{"region": "kr"}}
	nodes := []*store.Node{home, vps}
	all := func(string) bool { return true }
	regs := []config.RegionReplicas{{Region: "kr", Replicas: 1}}
	if got, _ := MapRegions(nodes, regs, "node_local", all); got[0].NodeID != "nod_home" {
		t.Fatalf("unsorted keeps order: %+v", got)
	}
	if got, _ := MapRegions(SortForPublic(nodes), regs, "node_local", all); got[0].NodeID != "nod_vps" {
		t.Fatalf("public picks vps: %+v", got)
	}
	// 이미 그 노드에 배포돼 있으면(here) 순위와 상관없이 그대로 둔다.
	if got, _ := MapRegions(SortForPublic(nodes), regs, "nod_home", all); got[0].NodeID != "nod_home" {
		t.Fatalf("here kept: %+v", got)
	}
}

func TestPickDrainTargetPrefersPublicNodes(t *testing.T) {
	load := map[string]int{"nod_home": 0, "nod_vps": 5, "nod_dyn": 1}
	rank := map[string]int{"nod_home": RankCGNAT, "nod_vps": RankPublicOK, "nod_dyn": RankDynamicIP}
	if got := PickDrainTarget(load, rank, false); got != "nod_home" {
		t.Fatalf("private service goes to the least loaded node: %s", got)
	}
	if got := PickDrainTarget(load, rank, true); got != "nod_vps" {
		t.Fatalf("public service goes to the best-ranked node: %s", got)
	}
	delete(load, "nod_vps")
	if got := PickDrainTarget(load, rank, true); got != "nod_dyn" {
		t.Fatalf("then dynamic IP before CGNAT: %s", got)
	}
}
