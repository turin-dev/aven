package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/config"
	"github.com/turin-dev/aven/internal/events"
	"github.com/turin-dev/aven/internal/logbook"
	"github.com/turin-dev/aven/internal/store"
)

// 재배포 없는 replica 수 변경(수동 scale, 자동 확장)과 여러 노드 배치(placement).
//
// 배치 규칙: 배포 노드(인스턴스 node_id = placement 첫 항목)가 주 노드다. 주 노드에서 먼저 빌드(또는 pull)하고,
// 나머지 노드는 같은 커밋 SHA(업로드면 같은 tarball)로 각자 빌드한다(레지스트리가 없으므로). 모든 노드의 replica가
// healthcheck를 통과해야 live가 되고, 하나라도 실패하면 모든 노드의 새 컨테이너를 지운다(이전 배포가 계속 서비스한다).
// 각 노드의 keel-proxy는 자기 노드의 replica로만 보낸다(도메인 DNS가 노드들을 round-robin으로 가리킨다).

// ErrScaleUnavailable은 지금 replica 수를 바꿀 수 없을 때다(배포 중, 중지됨, cron 등).
var ErrScaleUnavailable = errors.New("cannot scale now")

// scaleDownPause는 replica를 프록시에서 뺀 뒤 멈추기 전에 기다리는 시간이다(진행 중인 요청이 끝나도록).
var scaleDownPause = 2 * time.Second

// MaxReplicas는 인스턴스 하나의 replica 상한이다.
const MaxReplicas = 10

// ---------------- 배치 계산 ----------------

// Distribute는 total개의 replica를 weights(노드별 기준 replica 수)에 비례해 나눈다. weight가 1 이상인 노드는 최소 1개를 받고,
// weight가 0인 노드는 0개다. total이 weight 있는 노드 수보다 작으면 노드마다 1개(합계가 total보다 커진다).
// 결과의 순서와 노드는 weights와 같다.
func Distribute(total int, weights []store.Placement) []store.Placement {
	out := make([]store.Placement, len(weights))
	sumW, n := 0, 0
	for i, w := range weights {
		out[i] = store.Placement{NodeID: w.NodeID}
		if w.Replicas > 0 {
			sumW += w.Replicas
			n++
		}
	}
	if n == 0 {
		return out
	}
	if total < n {
		total = n
	}
	for i, w := range weights {
		if w.Replicas > 0 {
			out[i].Replicas = 1
		}
	}
	// 남은 replica는 이상적인 몫(total*w/sumW)에서 가장 많이 모자란 노드에 하나씩 준다(같으면 앞 노드).
	for left := total - n; left > 0; left-- {
		best, gap := -1, 0.0
		for i, w := range weights {
			if w.Replicas <= 0 {
				continue
			}
			g := float64(total)*float64(w.Replicas)/float64(sumW) - float64(out[i].Replicas)
			if best < 0 || g > gap+1e-9 {
				best, gap = i, g
			}
		}
		out[best].Replicas++
	}
	return out
}

// normalizePlacement는 placement를 노드 키로 바꾸고 같은 노드를 합친 뒤 주 노드를 맨 앞에 둔다(없으면 weight 0으로 넣는다).
func normalizePlacement(primary string, ps []store.Placement) []store.Placement {
	primary = nodeKey(primary)
	out := []store.Placement{{NodeID: primary}}
	idx := map[string]int{primary: 0}
	for _, p := range ps {
		k := nodeKey(p.NodeID)
		if i, ok := idx[k]; ok {
			out[i].Replicas += p.Replicas
			continue
		}
		idx[k] = len(out)
		out = append(out, store.Placement{NodeID: k, Replicas: p.Replicas})
	}
	return out
}

// planWeights는 배포의 노드별 기준 replica 수다: placement가 있으면 그것, 없으면 배포 노드 하나.
func planWeights(d *store.Deployment) []store.Placement {
	if len(d.Spec.Placement) > 0 {
		return normalizePlacement(d.NodeID, d.Spec.Placement)
	}
	n := d.Spec.Replicas
	if n < 1 {
		n = 1
	}
	return []store.Placement{{NodeID: nodeKey(d.NodeID), Replicas: n}}
}

// deployPlan은 새 배포가 노드마다 띄울 replica 수다(주 노드가 첫 항목). 자동 확장이 켜져 있으면 지금 live 배포의 replica 수를
// [min, max] 안에서 이어받는다(재배포해도 확장한 상태가 유지된다, Kubernetes HPA처럼).
func (m *Manager) deployPlan(ctx context.Context, in *store.Instance, d *store.Deployment) []store.Placement {
	w := planWeights(d)
	total := store.PlacementTotal(w)
	if cur, err := m.Store.GetInstance(ctx, in.ID); err == nil {
		if as := cur.Autoscale; as != nil && as.Enabled && d.Spec.CronSchedule == nil {
			if cur.CurrentDeploymentID != nil && *cur.CurrentDeploymentID != d.ID {
				if prev, err := m.Store.GetDeployment(ctx, *cur.CurrentDeploymentID); err == nil && len(prev.AllContainers()) > 0 {
					total = len(prev.AllContainers())
				}
			}
			total = clampInt(total, as.MinReplicas, as.MaxReplicas)
		}
	}
	return Distribute(total, w)
}

func clampInt(v, lo, hi int) int {
	if lo > 0 && v < lo {
		v = lo
	}
	if hi > 0 && v > hi {
		v = hi
	}
	return v
}

// assignContainers는 plan대로 컨테이너 이름을 정한다. 이름의 번호는 노드와 상관없이 1부터 이어진다.
func assignContainers(svc string, d *store.Deployment, plan []store.Placement) {
	d.Containers, d.Extra = nil, nil
	idx := 0
	for k, p := range plan {
		var names []string
		for j := 0; j < p.Replicas; j++ {
			names = append(names, containerName(svc, d.ID, idx))
			idx++
		}
		if k == 0 {
			d.Containers = names
			continue
		}
		if len(names) > 0 {
			d.Extra = append(d.Extra, store.NodeContainers{NodeID: p.NodeID, Containers: names})
		}
	}
}

// replicaIndex는 컨테이너 이름(keel-<svc>-<dep8>-<n>)의 replica 번호(0부터)다. 모르면 0.
func replicaIndex(name string) int {
	i := strings.LastIndex(name, "-")
	if i < 0 {
		return 0
	}
	n, err := strconv.Atoi(name[i+1:])
	if err != nil || n < 1 {
		return 0
	}
	return n - 1
}

func describePlan(plan []store.Placement) string {
	var parts []string
	for _, p := range plan {
		if p.Replicas > 0 {
			parts = append(parts, fmt.Sprintf("%s×%d", p.NodeID, p.Replicas))
		}
	}
	return strings.Join(parts, ", ")
}

// extraPlacementNodes는 배포 노드가 아닌 placement 노드(replica가 있는 것)다.
func extraPlacementNodes(d *store.Deployment) []string {
	var out []string
	for _, p := range planWeights(d)[1:] {
		if p.Replicas > 0 {
			out = append(out, p.NodeID)
		}
	}
	return out
}

// ---------------- config-as-code multiRegionConfig ----------------

// MapRegions는 multiRegionConfig의 region들을 노드 placement로 바꾼다. region마다 RegionNodes 중 배포 노드(here)가 있으면 그것,
// 없으면 연결된 첫 노드를 고른다. 한 노드가 여러 region에 걸리면 replica를 합친다. 배포 노드가 있으면 맨 앞이다.
// 노드를 찾지 못한 region은 missing으로 돌려준다.
func MapRegions(nodes []*store.Node, regions []config.RegionReplicas, here string, online func(string) bool) ([]store.Placement, []string) {
	var out []store.Placement
	var missing []string
	idx := map[string]int{}
	for _, r := range regions {
		var pick string
		for _, n := range RegionNodes(nodes, r.Region) {
			if nodeKey(n.ID) == nodeKey(here) {
				pick = nodeKey(n.ID)
				break
			}
			if pick == "" && online(n.ID) {
				pick = nodeKey(n.ID)
			}
		}
		if pick == "" {
			missing = append(missing, r.Region)
			continue
		}
		if i, ok := idx[pick]; ok {
			out[i].Replicas += r.Replicas
			continue
		}
		idx[pick] = len(out)
		out = append(out, store.Placement{NodeID: pick, Replicas: r.Replicas})
	}
	if i, ok := idx[nodeKey(here)]; ok && i > 0 {
		p := out[i]
		copy(out[1:i+1], out[:i])
		out[0] = p
	}
	return out, missing
}

// applyMultiRegion은 config-as-code의 deploy.multiRegionConfig(region 둘 이상)를 이 배포의 placement로 쓴다.
// 볼륨이 있거나 cron 서비스면 경고만 남긴다. 인스턴스 설정(placement)은 바꾸지 않는다(배포 스냅샷에만 남는다).
func (m *Manager) applyMultiRegion(ctx context.Context, t *Target, d *store.Deployment, b *logbook.Book, regions []config.RegionReplicas) error {
	if vols, err := m.Store.ListVolumesByInstance(ctx, t.Instance.ID); err != nil {
		return err
	} else if len(vols) > 0 {
		b.Printf("WARN: deploy.multiRegionConfig: 볼륨이 붙은 서비스는 여러 노드에 나눠 띄우지 않습니다. 무시합니다")
		return nil
	}
	if d.Spec.CronSchedule != nil && *d.Spec.CronSchedule != "" {
		b.Printf("WARN: deploy.multiRegionConfig: cron 서비스는 여러 노드에 나눠 띄우지 않습니다. 무시합니다")
		return nil
	}
	nodes, err := m.Store.ListNodes(ctx)
	if err != nil {
		return err
	}
	pl, missing := MapRegions(nodes, regions, d.NodeID, m.nodeOnline)
	for _, r := range missing {
		b.Printf("WARN: deploy.multiRegionConfig: region %s에 맞는 연결된 배포 가능 노드가 없어 건너뜁니다", r)
	}
	if len(pl) == 0 {
		b.Printf("WARN: deploy.multiRegionConfig: 맞는 노드가 하나도 없어 무시합니다")
		return nil
	}
	d.Spec.Placement = pl
	d.Spec.Replicas = store.PlacementTotal(pl)
	b.Printf("deploy.multiRegionConfig: %s", describePlan(normalizePlacement(d.NodeID, pl)))
	if nodeKey(pl[0].NodeID) != nodeKey(d.NodeID) {
		b.Printf("deploy.multiRegionConfig: 배포 노드 %s는 어느 region에도 맞지 않아 replica를 띄우지 않습니다(빌드만 합니다)", nodeKey(d.NodeID))
	}
	m.Store.UpdateDeployment(context.WithoutCancel(ctx), d)
	return nil
}

// ---------------- 다른 노드의 이미지 준비 ----------------

// prepareExtraImages는 배포 노드의 이미지가 준비된 뒤 placement의 다른 노드에도 같은 이미지(같은 태그)를 동시에 준비한다.
// image 서비스는 노드마다 pull, 저장소 서비스는 같은 커밋 SHA로 clone해서 빌드, 업로드는 같은 tarball을 보내 빌드한다.
func (m *Manager) prepareExtraImages(ctx context.Context, t *Target, d *store.Deployment, b *logbook.Book, upload string) error {
	extra := extraPlacementNodes(d)
	if len(extra) == 0 {
		return nil
	}
	for _, n := range extra {
		if !m.nodeOnline(n) {
			return fmt.Errorf("placement 노드 %s가 연결되어 있지 않습니다", n)
		}
	}
	b.Printf("placement: 노드 %s에도 이미지 %s를 준비합니다", strings.Join(extra, ", "), *d.Image)
	errs := make([]error, len(extra))
	var wg sync.WaitGroup
	for i, n := range extra {
		wg.Add(1)
		go func(i int, n string) {
			defer wg.Done()
			if err := m.prepareImageOn(ctx, t, d, b, n, upload); err != nil {
				errs[i] = fmt.Errorf("node %s: %w", n, err)
			}
		}(i, n)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// prepareImageOn은 노드 nodeID에 배포 이미지를 준비한다. d는 읽기만 한다(다른 노드 준비와 동시에 돈다).
func (m *Manager) prepareImageOn(ctx context.Context, t *Target, d *store.Deployment, b *logbook.Book, nodeID, upload string) error {
	drv := m.driverForNode(nodeID)
	img := *d.Image
	if drv.ImageExists(ctx, img) {
		b.Printf("노드 %s: 이미지 %s가 이미 있습니다", nodeID, img)
		return nil
	}
	// build_node=local: 로컬에 빌드 이미지가 있으면 그 노드에서 다시 빌드하지 않고 보낸다.
	if d.Spec.BuildNode == store.BuildNodeLocal && !isLocal(nodeID) && m.driverForNode(store.LocalNodeID).ImageExists(ctx, img) {
		return m.shipImage(ctx, b, img, nodeID)
	}
	used := ""
	if d.BuilderUsed != nil {
		used = *d.BuilderUsed
	}
	builder := d.Spec.Builder
	if used == "dockerfile" || used == "railpack" {
		builder = used // 주 노드와 같은 builder로 빌드한다
	}
	// function 서비스는 배포 스냅샷 코드로 빌드 컨텍스트를 다시 만들 수 있다(나중의 scale out 등).
	if upload == "" && t.Service.SourceType == "function" {
		cp := *d
		p, err := m.functionUpload(ctx, &cp, nil)
		if err != nil {
			return err
		}
		defer os.Remove(p)
		upload, builder, d = p, "dockerfile", &cp
	}
	switch {
	case used == "image" || t.Service.SourceType == "image" || t.Service.SourceType == "database":
		b.Printf("노드 %s: docker pull %s", nodeID, img)
		return stage(ctx, m.Timeouts.Build, "pull", func(ctx context.Context) error {
			return drv.Pull(ctx, img, m.registryAuth(ctx, t.Project.ID, img, nil), func(l string) { b.Line("  [" + nodeID + "] " + l) })
		})
	case upload != "":
		if _, err := os.Stat(upload); err != nil {
			return fmt.Errorf("업로드 소스가 남아 있지 않습니다: %w", err)
		}
		return m.buildUploadOn(ctx, t, d, b, nodeID, upload, builder, img)
	case t.Service.SourceType == "repo" && d.CommitSHA != nil && d.Spec.RepoURL != nil:
		return m.buildRepoOn(ctx, t, d, b, nodeID, builder, img)
	}
	return fmt.Errorf("이미지 %s를 이 노드에 준비할 방법이 없습니다(커밋 SHA나 소스가 없습니다)", img)
}

// nodeBook은 다른 노드의 빌드 로그 줄에 노드 표시를 붙인다.
func nodeLine(b *logbook.Book, nodeID string) func(string) {
	return func(l string) { b.Line("  [" + nodeID + "] " + l) }
}

// buildRepoOn은 노드 nodeID에서 주 노드가 빌드한 커밋(SHA 고정)을 clone해 같은 태그로 빌드한다.
func (m *Manager) buildRepoOn(ctx context.Context, t *Target, d *store.Deployment, b *logbook.Book, nodeID, builder, img string) error {
	repo, sha := *d.Spec.RepoURL, *d.CommitSHA
	b.Printf("노드 %s: git clone %s @ %s", nodeID, repo, short(sha))
	if isLocal(nodeID) {
		dir := filepath.Join(m.WorkDir, d.ID+"-"+nodeKey(nodeID))
		defer os.RemoveAll(dir)
		if err := stage(ctx, m.Timeouts.Clone, "clone", func(ctx context.Context) error {
			_, err := m.cloneRepo(ctx, t.Service.ID, repo, d.Spec.Branch, sha, filepath.Join(dir, "src"), b)
			return err
		}); err != nil {
			return err
		}
		root, err := safeJoin(filepath.Join(dir, "src"), d.Spec.RootDir)
		if err != nil {
			return err
		}
		_, err = m.buildLocalImage(ctx, t, d, b, root, dir, builder, img)
		return err
	}
	rd := &remoteDriver{hub: m.Agents, node: nodeID}
	var res *agentprotocol.FetchResult
	if err := stage(ctx, m.Timeouts.Clone, "clone", func(ctx context.Context) (err error) {
		req := agentprotocol.FetchRequest{Repo: repo, Branch: d.Spec.Branch, SHA: sha, Token: m.githubTokenFor(ctx, t.Service.ID, repo), RootDir: d.Spec.RootDir}
		if key, kh := m.sshKeyFor(ctx, t.Service.ID, repo, b); key != "" {
			req.SSHKey, req.KnownHosts = key, kh
		}
		res, err = rd.fetchSource(ctx, req, nodeLine(b, nodeID))
		return err
	}); err != nil {
		return err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		rd.cleanupSource(cctx, res.Workspace)
		cancel()
	}()
	if res.Commit.SHA != "" && res.Commit.SHA != sha {
		return fmt.Errorf("노드 %s가 받은 커밋 %s가 주 노드의 커밋 %s와 다릅니다", nodeID, short(res.Commit.SHA), short(sha))
	}
	_, err := m.remoteBuildImage(ctx, t, d, b, rd, res.Workspace, builder, img)
	return err
}

// buildUploadOn은 CLI 업로드 tarball을 노드 nodeID로 보내(로컬이면 풀어서) 같은 태그로 빌드한다.
func (m *Manager) buildUploadOn(ctx context.Context, t *Target, d *store.Deployment, b *logbook.Book, nodeID, upload, builder, img string) error {
	b.Printf("노드 %s: 업로드된 소스로 빌드합니다", nodeID)
	if isLocal(nodeID) {
		dir := filepath.Join(m.WorkDir, d.ID+"-"+nodeKey(nodeID))
		defer os.RemoveAll(dir)
		if err := extractTarGz(upload, filepath.Join(dir, "src")); err != nil {
			return fmt.Errorf("extract upload: %w", err)
		}
		root, err := safeJoin(filepath.Join(dir, "src"), d.Spec.RootDir)
		if err != nil {
			return err
		}
		_, err = m.buildLocalImage(ctx, t, d, b, root, dir, builder, img)
		return err
	}
	f, err := os.Open(upload)
	if err != nil {
		return fmt.Errorf("open upload: %w", err)
	}
	defer f.Close()
	rd := &remoteDriver{hub: m.Agents, node: nodeID}
	var res *agentprotocol.FetchResult
	if err := stage(ctx, m.Timeouts.Clone*4, "upload", func(ctx context.Context) (err error) {
		res, err = rd.uploadSource(ctx, f, d.Spec.RootDir)
		return err
	}); err != nil {
		return err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		rd.cleanupSource(cctx, res.Workspace)
		cancel()
	}()
	_, err = m.remoteBuildImage(ctx, t, d, b, rd, res.Workspace, builder, img)
	return err
}

// ---------------- 재배포 없는 replica 수 변경 ----------------

// ScaleResult는 ScaleReplicas의 결과다.
type ScaleResult struct {
	From, To int
	Nodes    []store.Placement // 노드별 replica 수(변경 후)
}

func (m *Manager) scaleLock(instanceID string) *sync.Mutex {
	m.scaleMu.Lock()
	defer m.scaleMu.Unlock()
	if m.scaleLocks == nil {
		m.scaleLocks = map[string]*sync.Mutex{}
	}
	l := m.scaleLocks[instanceID]
	if l == nil {
		l = &sync.Mutex{}
		m.scaleLocks[instanceID] = l
	}
	return l
}

// deploymentInProgress는 인스턴스에 진행 중인(대기열 포함, CI 대기 제외) 배포가 있는지다.
func (m *Manager) deploymentInProgress(ctx context.Context, instanceID string) bool {
	deps, err := m.Store.ListDeployments(ctx, instanceID, 20)
	if err != nil {
		return true
	}
	for _, d := range deps {
		if !d.Status.Terminal() && d.Status != store.DepWaiting {
			return true
		}
	}
	return false
}

// CurrentReplicas는 인스턴스 현재 배포의 replica 컨테이너 수(모든 노드 합)다. 배포가 없으면 0.
func (m *Manager) CurrentReplicas(ctx context.Context, in *store.Instance) int {
	if in.CurrentDeploymentID == nil {
		return 0
	}
	d, err := m.Store.GetDeployment(ctx, *in.CurrentDeploymentID)
	if err != nil {
		return 0
	}
	return len(d.AllContainers())
}

// ScaleReplicas는 현재 live 배포의 replica 수를 재빌드 없이 n개로 바꾼다(모든 노드 합).
// 늘릴 때는 같은 이미지·환경변수로 컨테이너를 띄우고 healthcheck가 통과한 뒤 프록시 upstream에 넣는다(실패하면 새 컨테이너만 지운다).
// 줄일 때는 프록시에서 먼저 빼고 잠시 기다린 뒤 draining_seconds로 멈추고 지운다.
// placement가 있으면 노드별 기준 replica 수에 비례해 나눈다(노드마다 최소 1개). 인스턴스 설정의 replicas는 바꾸지 않는다.
func (m *Manager) ScaleReplicas(ctx context.Context, in *store.Instance, n int, reason string) (*ScaleResult, error) {
	if n < 1 || n > MaxReplicas {
		return nil, fmt.Errorf("replicas must be 1..%d", MaxReplicas)
	}
	if m.IsSelfService(in.ServiceID) {
		return nil, ErrSelfService
	}
	l := m.scaleLock(in.ID)
	l.Lock()
	defer l.Unlock()
	ctx = context.WithoutCancel(ctx)
	t, err := m.Load(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	cur := t.Instance
	if reason := scaleBlocker(cur); reason != "" {
		return nil, fmt.Errorf("%w: %s", ErrScaleUnavailable, reason)
	}
	if m.deploymentInProgress(ctx, cur.ID) {
		return nil, fmt.Errorf("%w: 배포가 진행 중입니다", ErrScaleUnavailable)
	}
	d, err := m.Store.GetDeployment(ctx, *cur.CurrentDeploymentID)
	if err != nil {
		return nil, err
	}
	if d.Status != store.DepLive || d.Image == nil || d.Spec.CronSchedule != nil {
		return nil, fmt.Errorf("%w: 현재 배포가 live가 아닙니다 (%s)", ErrScaleUnavailable, d.Status)
	}
	plan := Distribute(n, planWeights(d))
	res := &ScaleResult{From: len(d.AllContainers()), To: store.PlacementTotal(plan), Nodes: plan}
	have := map[string][]string{}
	for _, g := range d.Groups() {
		have[nodeKey(g.NodeID)] = append([]string(nil), g.Containers...)
	}
	for _, p := range plan {
		if len(have[p.NodeID]) != p.Replicas && !m.nodeOnline(p.NodeID) {
			return nil, fmt.Errorf("%w: 노드 %s가 연결되어 있지 않습니다", ErrScaleUnavailable, p.NodeID)
		}
	}
	if res.From == res.To && sameCounts(plan, have) {
		return res, nil
	}
	if err := m.Store.PrepareDeploymentResources(ctx, d, res.To); err != nil {
		return nil, err
	}
	committedResources := false
	defer func() {
		if !committedResources {
			if err := m.Store.PrepareDeploymentResources(ctx, d, res.From); err != nil && m.Log != nil {
				m.Log.Warn("restore replica resource reservation", "deployment", d.ID, "err", err)
			}
		}
	}()
	book := logbook.Discard()
	if m.Logs != nil {
		if bk, err := m.Logs.Open(d.ID); err == nil {
			book = bk
			defer func() {
				bk.Close(string(store.DepLive))
				m.Logs.Release(d.ID)
			}()
		}
	}
	book.Printf("==> replica %d → %d (%s)", res.From, res.To, reason)

	// 1) 늘리기: 새 컨테이너를 띄우고 healthcheck가 끝날 때까지 프록시에 넣지 않는다.
	used := map[int]bool{}
	for _, c := range d.AllContainers() {
		used[replicaIndex(c)] = true
	}
	added := map[string][]string{}
	var started []string
	cleanup := func() {
		for nk, names := range added {
			for _, c := range names {
				m.driverForNode(nk).Remove(ctx, c, 0)
				m.undrain(c)
			}
		}
	}
	for _, p := range plan {
		for k := len(have[p.NodeID]); k < p.Replicas; k++ {
			i := 0
			for used[i] {
				i++
			}
			used[i] = true
			name := containerName(t.Service.Name, d.ID, i)
			drv := m.driverForNode(p.NodeID)
			m.markDrain(name) // healthcheck 전에는 죽어도 crash로 보지 않고, reconcile도 건드리지 않는다
			added[p.NodeID] = append(added[p.NodeID], name)
			drv.Remove(ctx, name, 0)
			if err := drv.Run(ctx, m.runSpecNamed(t, d, name, i)); err != nil {
				book.Printf("ERROR: 컨테이너 %s 시작 실패: %v", name, err)
				cleanup()
				return nil, err
			}
			started = append(started, name)
			book.Printf("컨테이너 %s 시작 (node %s)", name, p.NodeID)
		}
	}
	if len(started) > 0 {
		hcTimeout := time.Duration(d.Spec.HealthcheckTimeoutS) * time.Second
		for nk, names := range added {
			if err := stage(ctx, hcTimeout, "healthcheck", func(ctx context.Context) error {
				return m.healthcheckNames(ctx, t, d, nk, names, book)
			}); err != nil {
				book.Printf("ERROR: 새 replica healthcheck 실패: %v", err)
				cleanup()
				return nil, fmt.Errorf("새 replica healthcheck 실패: %w", err)
			}
		}
		// 그사이 새 배포로 바뀌었으면 새 컨테이너는 쓸모가 없다.
		if fresh, err := m.Store.GetInstance(ctx, cur.ID); err != nil || fresh.CurrentDeploymentID == nil || *fresh.CurrentDeploymentID != d.ID {
			cleanup()
			return nil, fmt.Errorf("%w: 그사이 현재 배포가 바뀌었습니다", ErrScaleUnavailable)
		}
	}
	// 2) 줄이기: 각 노드에서 번호가 큰 replica부터 뺀다.
	removed := map[string][]string{}
	for _, p := range plan {
		names := have[p.NodeID]
		if len(names) > p.Replicas {
			sort.SliceStable(names, func(a, b int) bool { return replicaIndex(names[a]) < replicaIndex(names[b]) })
			removed[p.NodeID] = append([]string(nil), names[p.Replicas:]...)
			have[p.NodeID] = names[:p.Replicas]
		}
	}
	for nk, names := range added {
		have[nk] = append(have[nk], names...)
	}
	// 새 목록을 저장하고 프록시를 맞춘다(새 replica는 들어가고, 뺄 replica는 빠진다).
	setGroups(d, have)
	if err := m.Store.SetDeploymentContainers(ctx, d.ID, d.Containers, d.Extra); err != nil {
		cleanup()
		return nil, err
	}
	committedResources = true
	for _, c := range started {
		m.undrain(c)
	}
	for nk, err := range m.syncProxy(ctx) {
		if err != nil && (len(have[nk]) > 0 || len(removed[nk]) > 0) {
			book.Printf("WARN: 노드 %s 프록시 반영 실패(재시도 예정): %v", nk, err)
		}
	}
	if len(removed) > 0 {
		for _, names := range removed {
			for _, c := range names {
				m.markDrain(c)
			}
		}
		if scaleDownPause > 0 {
			time.Sleep(scaleDownPause)
		}
		grace := d.Spec.DrainGrace(10)
		for nk, names := range removed {
			drv := m.driverForNode(nk)
			for _, c := range names {
				book.Printf("replica %s를 프록시에서 뺐습니다. 종료합니다 (SIGTERM 후 최대 %d초)", c, grace)
				drv.Stop(ctx, c, grace)
				drv.Remove(ctx, c, 0)
				m.undrain(c)
			}
		}
	}
	book.Printf("replica %d → %d 완료 (%s)", res.From, res.To, describePlan(plan))
	if m.Events != nil {
		msg := fmt.Sprintf("%s (%s): replica %d → %d", t.Service.Name, t.Env.Name, res.From, res.To)
		if reason != "" {
			msg += " (" + reason + ")"
		}
		m.emit(events.WithDeployment(ctx, d.ID), t, "service.scaled", msg)
	}
	m.syncMeshAsync()
	m.syncEgressAsync()
	m.syncDomainIPv6Async()
	return res, nil
}

func sameCounts(plan []store.Placement, have map[string][]string) bool {
	for _, p := range plan {
		if len(have[p.NodeID]) != p.Replicas {
			return false
		}
	}
	return true
}

// setGroups는 노드별 컨테이너 목록을 배포에 다시 넣는다(주 노드는 Containers, 나머지는 Extra, 노드 순서는 유지).
func setGroups(d *store.Deployment, have map[string][]string) {
	sortNames := func(names []string) []string {
		out := append([]string(nil), names...)
		sort.SliceStable(out, func(a, b int) bool { return replicaIndex(out[a]) < replicaIndex(out[b]) })
		return out
	}
	d.Containers = sortNames(have[nodeKey(d.NodeID)])
	seen := map[string]bool{nodeKey(d.NodeID): true}
	var extra []store.NodeContainers
	for _, g := range d.Extra {
		k := nodeKey(g.NodeID)
		seen[k] = true
		if len(have[k]) > 0 {
			extra = append(extra, store.NodeContainers{NodeID: k, Containers: sortNames(have[k])})
		}
	}
	// placement에 있지만 지금까지 replica가 없던 노드
	var rest []string
	for k := range have {
		if !seen[k] && len(have[k]) > 0 {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		extra = append(extra, store.NodeContainers{NodeID: k, Containers: sortNames(have[k])})
	}
	d.Extra = extra
}

// scaleBlocker는 인스턴스 상태 때문에 replica 수를 바꿀 수 없는 이유다(없으면 "").
func scaleBlocker(in *store.Instance) string {
	switch {
	case in.CurrentDeploymentID == nil:
		return "아직 배포된 적이 없습니다"
	case in.CronSchedule != nil:
		return "cron 서비스는 replica가 없습니다"
	case in.Status == store.InstSleeping:
		return "서비스가 잠들어 있습니다"
	case in.Status == store.InstOffline:
		return "서비스가 중지되어 있습니다"
	case in.Status == store.InstDeploying:
		return "배포가 진행 중입니다"
	case in.Status != store.InstOnline:
		return "서비스가 실행 중이 아닙니다 (" + in.Status + ")"
	}
	return ""
}

// 현재 배포 스냅샷의 replicas도 같이 바꿔 pending_changes에 남지 않게 한다. placement가 있으면 비율을 유지해 나눈다.
func (m *Manager) ScaleManual(ctx context.Context, in *store.Instance, n int) (*ScaleResult, error) {
	res, err := m.ScaleReplicas(ctx, in, n, "수동")
	if err != nil {
		return nil, err
	}
	ctx = context.WithoutCancel(ctx)
	cur, err := m.Store.GetInstance(ctx, in.ID)
	if err != nil {
		return res, nil
	}
	if len(cur.Placement) == 0 {
		cur.Replicas = res.To
	} else {
		// placement는 노드별 기준 수다. 새 분배를 그대로 기준으로 삼는다.
		var pl []store.Placement
		for _, p := range res.Nodes {
			if p.Replicas > 0 {
				pl = append(pl, p)
			}
		}
		cur.Placement = pl
		cur.Replicas = res.To
	}
	if err := m.Store.UpdateInstanceConfig(ctx, cur); err != nil {
		return res, err
	}
	if cur.CurrentDeploymentID != nil {
		if d, err := m.Store.GetDeployment(ctx, *cur.CurrentDeploymentID); err == nil {
			d.Spec.Replicas, d.ReqSpec.Replicas = cur.Replicas, cur.Replicas
			if len(cur.Placement) > 0 {
				if len(d.Spec.Placement) > 0 {
					d.Spec.Placement = cur.Placement
				}
				if len(d.ReqSpec.Placement) > 0 {
					d.ReqSpec.Placement = cur.Placement
				}
			}
			m.Store.SetDeploymentReplicas(ctx, d.ID, d.Spec, d.ReqSpec)
		}
	}
	return res, nil
}
