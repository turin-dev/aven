// Package metrics는 aven 컨테이너의 사용량을 10초마다 모아 인스턴스별(replica 합)로 SQLite에 저장한다.
// 24시간이 지난 점은 5분 단위로 다운샘플하고, 7일이 지나면 지운다.
package metrics

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/events"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
	"github.com/turin-dev/aven/internal/sysstat"
)

const (
	Interval  = 10 * time.Second
	RawKeep   = 24 * time.Hour
	Retention = 7 * 24 * time.Hour
	CoarseRes = 300 // 24시간 이후 해상도(초)
)

// Source는 한 노드의 컨테이너 목록과 사용량을 주는 것이다(로컬 docker 또는 원격 agent).
type Source interface {
	ListManaged(ctx context.Context) (map[string]map[string]string, error)
	Stats(ctx context.Context, names []string) ([]node.Stats, error)
}

type Collector struct {
	Store  *store.Store
	Docker *node.Docker
	Log    *slog.Logger
	// Remote는 연결된 원격 노드들이다(nil이면 로컬만). 꺼진 노드는 수집하지 않는다.
	Remote func(ctx context.Context) []Source
	// RemoteNodes는 연결된 원격 노드의 노드 단위 사용량 소스다. DataDir은 로컬 노드의 디스크 여유 공간을 잴 경로다.
	RemoteNodes func(ctx context.Context) []NodeSource
	DataDir     string
	// Events가 있으면 볼륨 크기 상한 알림(volume.near_limit / volume.limit_exceeded)을 낸다.
	Events  *events.Bus
	sampler sysstat.Sampler

	mu       sync.Mutex
	volSizes map[string]int64 // docker volume name → bytes
}

func (c *Collector) Run(ctx context.Context) {
	t := time.NewTicker(Interval)
	defer t.Stop()
	lastCompact, lastVol := time.Time{}, time.Time{}
	for {
		if time.Since(lastVol) > 5*time.Minute {
			lastVol = time.Now()
			go c.refreshVolumes(ctx)
		}
		if err := c.collect(ctx); err != nil && ctx.Err() == nil && c.Log != nil {
			c.Log.Warn("metrics collect", "err", err)
		}
		if time.Since(lastCompact) > time.Hour {
			lastCompact = time.Now()
			c.compact(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// VolumeSizer는 볼륨 크기를 잴 수 있는 노드다(로컬 docker, 원격 노드는 agent의 volume.sizes).
type VolumeSizer interface {
	VolumeSizes(ctx context.Context) (map[string]int64, error)
}

func (c *Collector) refreshVolumes(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var srcs []VolumeSizer
	if c.Docker != nil {
		srcs = append(srcs, c.Docker)
	}
	if c.Remote != nil {
		for _, s := range c.Remote(ctx) {
			if vs, ok := s.(VolumeSizer); ok {
				srcs = append(srcs, vs)
			}
		}
	}
	c.mu.Lock()
	prev := c.volSizes
	c.mu.Unlock()
	sizes, ok := MergeVolumeSizes(ctx, srcs, prev)
	if !ok {
		return
	}
	c.mu.Lock()
	c.volSizes = sizes
	c.mu.Unlock()
	c.checkVolumeLimits(ctx, sizes)
}

// MergeVolumeSizes는 노드마다 잰 볼륨 크기를 합친다(볼륨 이름은 노드 사이에 겹치지 않는다; 겹치면 큰 값).
// 이번에 재지 못한 노드(꺼짐, 옛 agent)의 볼륨은 이전에 알던 값을 그대로 둔다(이번에 나오지 않은 이름만).
// 모든 노드를 재지 못하면 ok=false(이전 값을 유지한다).
func MergeVolumeSizes(ctx context.Context, srcs []VolumeSizer, prev map[string]int64) (map[string]int64, bool) {
	out := map[string]int64{}
	okAny := false
	for _, s := range srcs {
		m, err := s.VolumeSizes(ctx)
		if err != nil {
			continue
		}
		okAny = true
		for k, v := range m {
			if cur, seen := out[k]; !seen || v > cur {
				out[k] = v
			}
		}
	}
	if !okAny {
		return nil, false
	}
	for k, v := range prev {
		if _, seen := out[k]; !seen {
			out[k] = v
		}
	}
	return out, true
}

// LimitState는 크기 size(bytes)가 상한 limitMB에 대해 어떤 상태인지다: 100% 이상 exceeded, 90% 이상 near.
func LimitState(size int64, limitMB int) string {
	limit := int64(limitMB) * 1024 * 1024
	switch {
	case limit <= 0:
		return store.VolumeLimitOK
	case size >= limit:
		return store.VolumeLimitExceeded
	case size*10 >= limit*9:
		return store.VolumeLimitNear
	}
	return store.VolumeLimitOK
}

func limitRank(s string) int {
	switch s {
	case store.VolumeLimitNear:
		return 1
	case store.VolumeLimitExceeded:
		return 2
	}
	return 0
}

// LimitEvent는 상태가 prev → cur로 바뀔 때 낼 이벤트 kind다. 더 나빠질 때만(경계를 넘을 때 한 번) 낸다.
func LimitEvent(prev, cur string) string {
	if limitRank(cur) <= limitRank(prev) {
		return ""
	}
	if cur == store.VolumeLimitExceeded {
		return "volume.limit_exceeded"
	}
	return "volume.near_limit"
}

// checkVolumeLimits는 상한(size_limit_mb)이 있는 볼륨의 크기를 비교해 90%·100%를 넘을 때 한 번씩 알린다.
// docker local 볼륨은 크기를 강제할 수 없으므로 알림만 한다(soft limit). 원격 노드의 볼륨은 agent의 volume.sizes로 잰다.
// 크기를 모르는 볼륨(한 번도 재지 못한 노드)은 건너뛴다.
func (c *Collector) checkVolumeLimits(ctx context.Context, sizes map[string]int64) {
	if c.Store == nil {
		return
	}
	vols, err := c.Store.ListVolumes(ctx)
	if err != nil {
		return
	}
	for _, v := range vols {
		if v.SizeLimitMB == nil {
			if v.LimitState != store.VolumeLimitOK {
				c.Store.SetVolumeLimitState(ctx, v.ID, store.VolumeLimitOK)
			}
			continue
		}
		size, ok := sizes[v.DockerName]
		if !ok {
			continue
		}
		cur := LimitState(size, *v.SizeLimitMB)
		if cur == v.LimitState {
			continue
		}
		if err := c.Store.SetVolumeLimitState(ctx, v.ID, cur); err != nil {
			continue
		}
		kind := LimitEvent(v.LimitState, cur)
		if kind == "" || c.Events == nil {
			continue
		}
		used := fmt.Sprintf("%.1f MB / %d MB", float64(size)/(1024*1024), *v.SizeLimitMB)
		msg := fmt.Sprintf("볼륨 %s 사용량이 상한의 90%%를 넘었습니다 (%s)", v.Name, used)
		if kind == "volume.limit_exceeded" {
			msg = fmt.Sprintf("볼륨 %s 사용량이 상한을 넘었습니다 (%s). 상한은 알림용이라 쓰기는 막지 않습니다", v.Name, used)
		}
		var svc *string
		if v.InstanceID != nil {
			if in, err := c.Store.GetInstance(ctx, *v.InstanceID); err == nil {
				svc = &in.ServiceID
			}
		}
		c.Events.Emit(ctx, v.ProjectID, &v.EnvironmentID, svc, kind, msg)
	}
}

// VolumeSize는 마지막으로 잰 볼륨 크기다. 아직 모르면 nil.
func (c *Collector) VolumeSize(dockerName string) *int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.volSizes[dockerName]; ok {
		return &v
	}
	return nil
}

func (c *Collector) collect(ctx context.Context) error {
	c.collectNodes(ctx)
	sources := []Source{c.Docker}
	if c.Remote != nil {
		sources = append(sources, c.Remote(ctx)...)
	}
	byName := map[string]string{} // container → instance
	var stats []node.Stats
	var firstErr error
	for i, src := range sources {
		st, err := c.collectSource(ctx, src, byName)
		if err != nil {
			if i == 0 {
				firstErr = err
			} else if c.Log != nil {
				c.Log.Warn("metrics collect: remote node", "err", err)
			}
			continue
		}
		stats = append(stats, st...)
	}
	if firstErr != nil && len(stats) == 0 {
		return firstErr
	}
	now := time.Now().Unix()
	agg := map[string]*store.MetricRow{}
	for _, s := range stats {
		id := byName[s.Name]
		if id == "" {
			continue
		}
		r := agg[id]
		if r == nil {
			r = &store.MetricRow{T: now, Resolution: int(Interval.Seconds())}
			agg[id] = r
		}
		r.CPU += s.CPU
		r.Mem += s.Mem
		r.MemLimit += s.MemLimit
		r.Rx += s.Rx
		r.Tx += s.Tx
		r.Replicas++
	}
	vols, _ := c.Store.ListVolumes(ctx)
	for id, r := range agg {
		for _, v := range vols {
			if v.InstanceID != nil && *v.InstanceID == id {
				if sz := c.VolumeSize(v.DockerName); sz != nil {
					if r.Disk == nil {
						r.Disk = new(int64)
					}
					*r.Disk += *sz
				}
			}
		}
		if err := c.Store.InsertMetrics(ctx, id, []store.MetricRow{*r}); err != nil {
			return err
		}
	}
	return nil
}

func (c *Collector) collectSource(ctx context.Context, src Source, byName map[string]string) ([]node.Stats, error) {
	managed, err := src.ListManaged(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for name, l := range managed {
		if l[node.LabelInstance] == "" || (l[node.LabelRole] != node.RoleApp && l[node.LabelRole] != node.RoleCron) {
			continue
		}
		byName[name] = l[node.LabelInstance]
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, nil
	}
	sctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return src.Stats(sctx, names)
}

// compact는 RawKeep보다 오래된 10초 점을 CoarseRes 단위로 합치고, Retention보다 오래된 점을 지운다.
func (c *Collector) compact(ctx context.Context) {
	now := time.Now()
	c.Store.PruneMetrics(ctx, now.Add(-Retention).Unix())
	c.compactNodes(ctx, now)
	ids, err := c.Store.MetricInstances(ctx)
	if err != nil {
		return
	}
	cut := now.Add(-RawKeep).Unix()
	cut -= cut % CoarseRes
	for _, id := range ids {
		rows, err := c.Store.QueryMetrics(ctx, id, now.Add(-Retention).Unix())
		if err != nil {
			continue
		}
		var old []store.MetricRow
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
		ds := Downsample(old, CoarseRes)
		c.Store.ReplaceMetrics(ctx, id, old[0].T, cut, ds)
	}
}

// Downsample은 점들을 step초 버킷으로 묶는다. CPU/메모리는 평균, 누적 카운터(net)와 디스크는 마지막 값, replica 수는 최대.
func Downsample(rows []store.MetricRow, step int64) []store.MetricRow {
	if step <= 0 || len(rows) == 0 {
		return rows
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].T < rows[j].T })
	var out []store.MetricRow
	var cur *store.MetricRow
	var n float64
	var memSum, limSum float64
	flush := func() {
		if cur == nil {
			return
		}
		cur.CPU /= n
		cur.Mem = int64(memSum / n)
		cur.MemLimit = int64(limSum / n)
		out = append(out, *cur)
	}
	for _, r := range rows {
		b := r.T - r.T%step
		if cur == nil || cur.T != b {
			flush()
			cur = &store.MetricRow{T: b, Resolution: int(step)}
			n, memSum, limSum = 0, 0, 0
		}
		n++
		cur.CPU += r.CPU
		memSum += float64(r.Mem)
		limSum += float64(r.MemLimit)
		cur.Rx, cur.Tx = r.Rx, r.Tx
		if r.Disk != nil {
			d := *r.Disk
			cur.Disk = &d
		}
		if r.Replicas > cur.Replicas {
			cur.Replicas = r.Replicas
		}
	}
	flush()
	return out
}

// Point는 API의 MetricPoint다.
type Point struct {
	T                string  `json:"t"`
	CPUPercent       float64 `json:"cpu_percent"`
	MemoryBytes      int64   `json:"memory_bytes"`
	MemoryLimitBytes int64   `json:"memory_limit_bytes"`
	NetRxBytes       int64   `json:"net_rx_bytes"`
	NetTxBytes       int64   `json:"net_tx_bytes"`
	DiskBytes        *int64  `json:"disk_bytes"`
}

// RangeStep은 조회 범위 → (기간, 점 간격 초)다.
func RangeStep(r string) (time.Duration, int64, bool) {
	switch r {
	case "", "1h":
		return time.Hour, 10, true
	case "6h":
		return 6 * time.Hour, 60, true
	case "24h":
		return 24 * time.Hour, 300, true
	case "7d":
		return 7 * 24 * time.Hour, 1800, true
	}
	return 0, 0, false
}

// Query는 인스턴스의 점을 범위에 맞게 다운샘플해 돌려준다. 두 번째 값은 마지막 점의 replica 수다.
func Query(ctx context.Context, st *store.Store, instanceID, rng string) ([]Point, int64, int, error) {
	dur, step, _ := RangeStep(rng)
	rows, err := st.QueryMetrics(ctx, instanceID, time.Now().Add(-dur).Unix())
	if err != nil {
		return nil, 0, 0, err
	}
	replicas := 0
	if len(rows) > 0 {
		replicas = rows[len(rows)-1].Replicas
	}
	ds := Downsample(rows, step)
	pts := make([]Point, 0, len(ds))
	for _, r := range ds {
		pts = append(pts, Point{T: time.Unix(r.T, 0).UTC().Format(time.RFC3339), CPUPercent: r.CPU, MemoryBytes: r.Mem,
			MemoryLimitBytes: r.MemLimit, NetRxBytes: r.Rx, NetTxBytes: r.Tx, DiskBytes: r.Disk})
	}
	return pts, step, replicas, nil
}
