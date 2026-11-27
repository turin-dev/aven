package deploy

import (
	"context"
	"sort"
	"strings"

	"github.com/turin-dev/aven/internal/compat"
	"github.com/turin-dev/aven/internal/logbook"
	"github.com/turin-dev/aven/internal/store"
)

// 공개 서비스 배치 순위. 운영자가 노드 특성(store.NodeTraits)에 적은 회선 사정으로, 공개 도메인이 있는 서비스를
// 어디에 둘지 고를 때 직접 들어오는 요청을 받기 어려운 노드를 뒤로 미룬다. 명시적으로 고른 노드는 바꾸지 않는다.

// 순위 값. 작을수록 공개 서비스에 알맞다.
const (
	RankPublicOK  = 0 // 고정 공인 주소, 또는 Cloudflare 터널로 받는다
	RankDynamicIP = 1 // 공인 IP가 바뀔 수 있다(DNS가 따라가기 전까지 끊긴다)
	RankCGNAT     = 2 // 통신사 NAT 뒤라 80/443으로 들어오는 요청을 받지 못한다
)

// PublicIngressRank는 노드가 공개 트래픽을 받기에 얼마나 알맞은지다. 터널 노드는 회선과 상관없이 받을 수 있다.
func PublicIngressRank(n *store.Node) int {
	switch {
	case n == nil || n.Tunnel():
		return RankPublicOK
	case n.Traits.CGNAT:
		return RankCGNAT
	case n.Traits.DynamicIP:
		return RankDynamicIP
	}
	return RankPublicOK
}

// SortForPublic은 노드를 공개 순위로 안정 정렬한 새 슬라이스다(같은 순위끼리는 원래 순서를 지킨다).
func SortForPublic(nodes []*store.Node) []*store.Node {
	out := append([]*store.Node(nil), nodes...)
	sort.SliceStable(out, func(i, j int) bool { return PublicIngressRank(out[i]) < PublicIngressRank(out[j]) })
	return out
}

// isPublic은 인스턴스에 공개 도메인이 있는지다.
func isPublic(in *store.Instance) bool { return in != nil && len(in.Domains) > 0 }

// warnPublicNode는 공개 서비스를 직접 요청을 받기 어려운 노드에 배포할 때 배포 로그에 경고를 남긴다.
func (m *Manager) warnPublicNode(ctx context.Context, in *store.Instance, nodeID string, b *logbook.Book) {
	if !isPublic(in) {
		return
	}
	var n *store.Node
	var err error
	if isLocal(nodeID) {
		n, err = m.Store.LocalNode(ctx)
	} else {
		n, err = m.Store.GetNode(ctx, nodeID)
	}
	if err != nil {
		return
	}
	if isLocal(nodeID) {
		if v := strings.TrimSpace(compat.Env("INGRESS_MODE")); v != "" {
			n.IngressMode = v // 로컬 노드는 AVEN_INGRESS_MODE가 우선한다
		}
	}
	switch PublicIngressRank(n) {
	case RankCGNAT:
		b.Printf("WARN: 노드 %s는 통신사 NAT(CGNAT) 뒤에 있다고 적혀 있습니다. 공개 도메인으로 들어오는 요청을 받으려면 노드의 공개 진입을 Cloudflare 터널로 바꾸세요", n.Name)
	case RankDynamicIP:
		b.Printf("WARN: 노드 %s는 공인 IP가 바뀔 수 있다고 적혀 있습니다. IP가 바뀌면 DNS가 따라갈 때까지 공개 도메인이 끊깁니다(Cloudflare DNS 동기화나 터널을 권합니다)", n.Name)
	}
}
