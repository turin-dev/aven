package metrics

import (
	"context"
	"fmt"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

// 월 데이터 상한 알림. 노드 특성의 data_cap_gb(1GB = 10^9 bytes, 회선 약관의 표기)에 이번 달(UTC) 받은+보낸 바이트가
// 80%를 넘으면 node.data_cap_warning, 100%를 넘으면 node.data_cap_reached를 한 달에 한 번씩 낸다.
// 알림은 그 노드를 주 노드로 쓰는 서비스가 있는 프로젝트마다 간다(프로젝트 알림 채널과 알림함).

// DataCapSteps는 알림 단계(%)와 이벤트 kind다. 높은 단계부터 본다.
var DataCapSteps = []struct {
	Pct  int
	Kind string
}{{100, "node.data_cap_reached"}, {80, "node.data_cap_warning"}}

// DataCapBytes는 GB 상한을 바이트로 바꾼다.
func DataCapBytes(gb int) int64 { return int64(gb) * 1_000_000_000 }

// CheckDataCaps는 상한이 적힌 노드마다 이번 달 트래픽을 보고, 새로 넘은 단계가 있으면 알림을 낸다.
func (c *Collector) CheckDataCaps(ctx context.Context, now time.Time) {
	nodes, err := c.Store.ListNodes(ctx)
	if err != nil {
		return
	}
	month := store.TrafficMonth(now.UTC())
	for _, n := range nodes {
		if n.Traits.DataCapGB <= 0 {
			continue
		}
		tr, err := c.Store.NodeTrafficFor(ctx, n.ID, month)
		if err != nil {
			continue
		}
		capB := DataCapBytes(n.Traits.DataCapGB)
		pct := int(tr.Total() * 100 / capB)
		for _, step := range DataCapSteps {
			if pct < step.Pct || tr.AlertedPct >= step.Pct {
				continue
			}
			ok, err := c.Store.MarkNodeTrafficAlerted(ctx, n.ID, month, step.Pct)
			if err != nil || !ok {
				break
			}
			c.emitNode(ctx, n, step.Kind, fmt.Sprintf("노드 %s의 이번 달(%s, UTC) 트래픽이 %.1fGB로 월 데이터 상한 %dGB의 %d%%에 이르렀습니다",
				n.Name, month, float64(tr.Total())/1e9, n.Traits.DataCapGB, pct))
			break // 한 번에 가장 높은 단계 하나만 알린다(80%를 건너뛰고 100%면 100%만)
		}
	}
}

func (c *Collector) emitNode(ctx context.Context, n *store.Node, kind, msg string) {
	if c.Log != nil {
		c.Log.Warn("node data cap", "node", n.Name, "kind", kind)
	}
	if c.Events == nil {
		return
	}
	projects, err := c.Store.ProjectsOnNode(ctx, n.ID)
	if err != nil {
		return
	}
	for _, p := range projects {
		c.Events.Emit(ctx, p, nil, nil, kind, msg)
	}
}
