package metrics

import (
	"context"
	"sort"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

// NodeStat은 노드 하나의 한 시점 사용량이다.
type NodeStat struct {
	CPUPercent    float64
	MemoryBytes   uint64
	DiskFreeBytes uint64
}

// NodeSource는 연결된 원격 노드에서 사용량을 읽어 오는 함수다.
type NodeSource struct {
	ID    string
	Stats func(ctx context.Context) (NodeStat, error)
}

// collectNodes는 로컬 노드와 연결된 원격 노드의 사용량을 저장한다(컨테이너 metrics와 같은 주기).
func (c *Collector) collectNodes(ctx context.Context) {
	now := time.Now().Unix()
	row := func(s NodeStat) store.NodeMetricRow {
		return store.NodeMetricRow{T: now, Resolution: int(Interval.Seconds()), CPU: s.CPUPercent, Mem: int64(s.MemoryBytes), DiskFree: int64(s.DiskFreeBytes)}
	}
	ls := c.sampler.Sample(c.DataDir)
	c.Store.InsertNodeMetrics(ctx, store.LocalNodeID, []store.NodeMetricRow{row(NodeStat{ls.CPUPercent, ls.MemoryBytes, ls.DiskFreeBytes})})
	if c.RemoteNodes == nil {
		return
	}
	for _, src := range c.RemoteNodes(ctx) {
		sctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		st, err := src.Stats(sctx)
		cancel()
		if err != nil {
			if c.Log != nil && ctx.Err() == nil {
				c.Log.Warn("node metrics: remote node", "node", src.ID, "err", err)
			}
			continue
		}
		c.Store.InsertNodeMetrics(ctx, src.ID, []store.NodeMetricRow{row(st)})
	}
}

func (c *Collector) compactNodes(ctx context.Context, now time.Time) {
	c.Store.PruneNodeMetrics(ctx, now.Add(-Retention).Unix())
	ids, err := c.Store.NodeMetricNodes(ctx)
	if err != nil {
		return
	}
	cut := now.Add(-RawKeep).Unix()
	cut -= cut % CoarseRes
	for _, id := range ids {
		rows, err := c.Store.QueryNodeMetrics(ctx, id, now.Add(-Retention).Unix())
		if err != nil {
			continue
		}
		var old []store.NodeMetricRow
		fine := false
		for _, r := range rows {
			if r.T >= cut {
				break
			}
			old = append(old, r)
			if r.Resolution < CoarseRes {
				fine = true
			}
		}
		if !fine || len(old) == 0 {
			continue
		}
		c.Store.ReplaceNodeMetrics(ctx, id, old[0].T, cut, DownsampleNodes(old, CoarseRes))
	}
}

// DownsampleNodes는 점들을 step초 버킷으로 묶는다. CPU/메모리/디스크 여유는 평균이다.
func DownsampleNodes(rows []store.NodeMetricRow, step int64) []store.NodeMetricRow {
	if step <= 0 || len(rows) == 0 {
		return rows
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].T < rows[j].T })
	var out []store.NodeMetricRow
	var cur *store.NodeMetricRow
	var n, mem, disk float64
	flush := func() {
		if cur == nil {
			return
		}
		cur.CPU /= n
		cur.Mem = int64(mem / n)
		cur.DiskFree = int64(disk / n)
		out = append(out, *cur)
	}
	for _, r := range rows {
		b := r.T - r.T%step
		if cur == nil || cur.T != b {
			flush()
			cur = &store.NodeMetricRow{T: b, Resolution: int(step)}
			n, mem, disk = 0, 0, 0
		}
		n++
		cur.CPU += r.CPU
		mem += float64(r.Mem)
		disk += float64(r.DiskFree)
	}
	flush()
	return out
}

// NodePoint는 GET /nodes/{nid}/metrics의 점이다.
type NodePoint struct {
	T             string  `json:"t"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryBytes   int64   `json:"memory_bytes"`
	DiskFreeBytes int64   `json:"disk_free_bytes"`
}

// QueryNode는 노드의 점을 범위에 맞게 다운샘플해 돌려준다.
func QueryNode(ctx context.Context, st *store.Store, nodeID, rng string) ([]NodePoint, int64, error) {
	dur, step, _ := RangeStep(rng)
	rows, err := st.QueryNodeMetrics(ctx, nodeID, time.Now().Add(-dur).Unix())
	if err != nil {
		return nil, 0, err
	}
	ds := DownsampleNodes(rows, step)
	pts := make([]NodePoint, 0, len(ds))
	for _, r := range ds {
		pts = append(pts, NodePoint{T: time.Unix(r.T, 0).UTC().Format(time.RFC3339), CPUPercent: r.CPU, MemoryBytes: r.Mem, DiskFreeBytes: r.DiskFree})
	}
	return pts, step, nil
}
