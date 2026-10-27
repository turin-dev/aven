package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/config"
	"github.com/turin-dev/aven/internal/cronx"
	"github.com/turin-dev/aven/internal/logbook"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/store"
	"github.com/turin-dev/aven/internal/watch"
)

// nodeKey는 빈 node ID를 로컬 노드 ID로 바꾼다.
func nodeKey(id string) string {
	if isLocal(id) {
		return store.LocalNodeID
	}
	return id
}

// homeNode는 인스턴스의 컨테이너가 도는 노드다: 현재 배포가 있으면 그 배포의 노드, 없으면 지정된 노드.
// (node_id를 바꾸면 다음 배포부터 새 노드에서 돌고, 그 전까지는 이전 노드가 계속 서비스한다.)
func (m *Manager) homeNode(ctx context.Context, in *store.Instance) string {
	if in.CurrentDeploymentID != nil {
		if d, err := m.Store.GetDeployment(ctx, *in.CurrentDeploymentID); err == nil {
			return nodeKey(d.NodeID)
		}
	}
	return nodeKey(in.NodeID)
}

// HomeNode는 인스턴스의 컨테이너가 도는 노드 ID다.
func (m *Manager) HomeNode(ctx context.Context, in *store.Instance) string {
	return m.homeNode(ctx, in)
}

// instanceNodes는 인스턴스의 컨테이너가 있을 수 있는 노드들이다.
// placement로 여러 노드에 나눠 띄웠으면 그 노드들도 들어간다.
func (m *Manager) instanceNodes(ctx context.Context, in *store.Instance) []string {
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		n = nodeKey(n)
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	add(in.NodeID)
	add(m.homeNode(ctx, in))
	for _, n := range m.servingNodes(ctx, in) {
		add(n)
	}
	for _, p := range in.Placement {
		add(p.NodeID)
	}
	return out
}

// remoteNodes는 등록된 원격 노드 목록이다.
func (m *Manager) remoteNodes(ctx context.Context) []*store.Node {
	if m.Agents == nil {
		return nil
	}
	nodes, err := m.Store.ListNodes(ctx)
	if err != nil {
		return nil
	}
	var out []*store.Node
	for _, n := range nodes {
		if !isLocal(n.ID) {
			out = append(out, n)
		}
	}
	return out
}

// onlineDrivers는 로컬 노드와 연결된 원격 노드의 드라이버다.
func (m *Manager) onlineDrivers(ctx context.Context) []Driver {
	out := []Driver{m.driverForNode(store.LocalNodeID)}
	for _, n := range m.remoteNodes(ctx) {
		if m.nodeOnline(n.ID) {
			out = append(out, m.driverForNode(n.ID))
		}
	}
	return out
}

// OnlineRemoteDrivers는 metrics 수집용으로 연결된 원격 노드 드라이버를 돌려준다.
func (m *Manager) OnlineRemoteDrivers(ctx context.Context) []Driver {
	return m.onlineDrivers(ctx)[1:]
}

// volumeNode는 볼륨이 있는 노드다(붙은 인스턴스의 노드, 없으면 로컬).
func (m *Manager) volumeNode(ctx context.Context, v *store.Volume) string {
	if v.InstanceID == nil {
		return store.LocalNodeID
	}
	in, err := m.Store.GetInstance(ctx, *v.InstanceID)
	if err != nil {
		return store.LocalNodeID
	}
	return m.homeNode(ctx, in)
}

// IsRemoteVolume은 볼륨이 원격 노드에 있는지다(백업/복원은 아직 로컬만 지원).
func (m *Manager) IsRemoteVolume(ctx context.Context, v *store.Volume) bool {
	return !isLocal(m.volumeNode(ctx, v))
}

// removeEnvNetwork는 모든 노드에서 환경 네트워크를 지운다(꺼진 원격 노드는 건너뛴다).
func (m *Manager) removeEnvNetwork(ctx context.Context, envID string) {
	// 환경 네트워크에 붙은 mesh forwarder를 먼저 지운다(DB에서 환경이 지워졌으므로 mesh 원하는 상태에서 빠진다).
	if err := m.SyncMesh(ctx); err != nil {
		m.Log.Warn("mesh sync before network removal", "err", err)
	}
	for _, name := range []string{EnvNetwork(envID), LegacyEnvNetwork(envID)} {
		if err := m.Docker.RemoveNetwork(ctx, name, proxy.ContainerName, proxy.LegacyContainerName); err != nil {
			m.Log.Warn("remove network", "network", name, "err", err)
		}
		for _, n := range m.remoteNodes(ctx) {
			if m.nodeOnline(n.ID) {
				if err := m.driverForNode(n.ID).RemoveNetwork(ctx, name); err != nil {
					m.Log.Warn("remove network", "node", n.ID, "network", name, "err", err)
				}
			}
		}
	}
}

// applyConfig는 config-as-code 파일을 배포 설정에 적용한다. watch patterns에 안 맞으면 errSkipped.
// environments.<환경 이름>(미리보기 환경이면 environments.pr) 블록을 먼저 합친다.
// deploy.region이 다른 노드를 가리키면 인스턴스를 옮기고 새 배포를 만든 뒤 errMoved를 돌려준다.
func (m *Manager) applyConfig(ctx context.Context, t *Target, d *store.Deployment, b *logbook.Book, name string, cfg *config.File) error {
	if cfg == nil {
		return nil
	}
	envName, preview := "", false
	if t != nil && t.Env != nil {
		envName, preview = t.Env.Name, t.Env.Kind == store.EnvPreview
	}
	cfg, block := cfg.ForEnvironment(envName, preview)
	if block != "" {
		b.Printf("%s: environments.%s 설정을 덮어씁니다", name, block)
	}
	for _, w := range cfg.Warnings() {
		b.Printf("WARN: %s: %s", name, w)
	}
	regions, rerr := cfg.Regions()
	if rerr != nil {
		return fmt.Errorf("%s: %w", name, rerr)
	}
	if len(regions) > 1 && t != nil {
		if err := m.applyMultiRegion(ctx, t, d, b, regions); err != nil {
			return err
		}
	} else if region, _ := cfg.Region(); region != "" && t != nil {
		if err := m.applyRegion(ctx, t, d, b, region); err != nil {
			return err
		}
	}
	changed, err := cfg.Apply(&d.Spec)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	b.Printf("%s 설정을 적용합니다: %s", name, strings.Join(changed, ", "))
	if d.Spec.CronSchedule != nil && *d.Spec.CronSchedule != "" {
		if _, err := cronx.Parse(*d.Spec.CronSchedule); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	m.Store.UpdateDeployment(context.WithoutCancel(ctx), d)
	if d.Trigger == "github" && d.ChangedFiles != nil && cfg.Build.WatchPatterns != nil && !watch.Match(cfg.Build.WatchPatterns, d.ChangedFiles) {
		return errSkipped
	}
	return nil
}

// errMoved는 deploy.region 때문에 인스턴스를 다른 노드로 옮기고 그 노드에 새 배포를 만들었다는 뜻이다(이 배포는 removed).
var errMoved = errors.New("moved to another node by deploy.region")

// RegionNodes는 region 값에 맞는 배포 가능한 노드다: 이름이 같거나(대소문자 무시) region=<값> 라벨이 있는 노드.
func RegionNodes(nodes []*store.Node, region string) []*store.Node {
	var out []*store.Node
	for _, n := range nodes {
		if !n.Schedulable {
			continue
		}
		if strings.EqualFold(n.Name, region) || n.Labels["region"] == region {
			out = append(out, n)
		}
	}
	return out
}

// applyRegion은 config-as-code의 deploy.region을 배치에 반영한다.
// 이미 맞는 노드면 그대로 두고, 볼륨이 있거나(데이터 이동은 명시적인 node_id 변경으로만 한다) 업로드 배포면 경고만 남긴다.
// 다른 노드로 옮길 때는 인스턴스의 node_id를 바꾸고 같은 커밋으로 새 배포를 만든 뒤 errMoved를 돌려준다.
func (m *Manager) applyRegion(ctx context.Context, t *Target, d *store.Deployment, b *logbook.Book, region string) error {
	nodes, err := m.Store.ListNodes(ctx)
	if err != nil {
		return err
	}
	cands := RegionNodes(nodes, region)
	if len(cands) == 0 {
		b.Printf("WARN: deploy.region=%s: 이름이나 region 라벨이 맞는 배포 가능한 노드가 없어 무시합니다", region)
		return nil
	}
	here := nodeKey(d.NodeID)
	var target *store.Node
	for _, n := range cands {
		if nodeKey(n.ID) == here {
			b.Printf("deploy.region=%s: 노드 %s에 배포합니다", region, n.Name)
			return nil
		}
		if target == nil && m.nodeOnline(n.ID) {
			target = n
		}
	}
	if vols, err := m.Store.ListVolumesByInstance(ctx, t.Instance.ID); err != nil {
		return err
	} else if len(vols) > 0 {
		b.Printf("WARN: deploy.region=%s: 볼륨이 붙은 서비스는 region으로 노드를 옮기지 않습니다(설정의 node_id 변경으로 옮기세요). 무시합니다", region)
		return nil
	}
	if strings.HasPrefix(d.Source, "upload:") {
		b.Printf("WARN: deploy.region=%s: CLI 업로드 배포는 다른 노드로 다시 보낼 수 없어 무시합니다", region)
		return nil
	}
	if target == nil {
		b.Printf("WARN: deploy.region=%s: 맞는 노드가 모두 연결되어 있지 않아 무시합니다", region)
		return nil
	}
	cur, err := m.Store.GetInstance(ctx, t.Instance.ID)
	if err != nil {
		return err
	}
	cur.NodeID = target.ID
	if err := m.Store.UpdateInstanceConfig(context.WithoutCancel(ctx), cur); err != nil {
		return err
	}
	spec, req := d.Spec, d.ReqSpec
	var commit *node.Commit
	if d.CommitSHA != nil {
		commit = commitOf(d)
	}
	nd, err := m.Enqueue(context.WithoutCancel(ctx), cur, EnqueueOpts{Trigger: d.Trigger, Spec: &spec, ReqSpec: &req, VarsRaw: d.VarsRaw,
		Commit: commit, Branch: d.Branch, ChangedFiles: d.ChangedFiles})
	if err != nil {
		return fmt.Errorf("deploy.region=%s: 노드 %s에 새 배포를 만들지 못했습니다: %w", region, target.Name, err)
	}
	b.Printf("deploy.region=%s: 서비스를 노드 %s로 옮기고 새 배포 %s에서 다시 빌드합니다", region, target.Name, nd.ID)
	m.emit(ctx, t, "service.updated", fmt.Sprintf("%s (%s): deploy.region=%s에 따라 노드를 %s(으)로 바꿨습니다", t.Service.Name, t.Env.Name, region, target.Name))
	return errMoved
}

// remoteBuild는 대상 노드에서 소스를 받고 이미지를 만든다(레지스트리 불필요).
func (m *Manager) remoteBuild(ctx context.Context, t *Target, d *store.Deployment, b *logbook.Book, rd *remoteDriver) error {
	if d.Spec.RepoURL == nil || *d.Spec.RepoURL == "" {
		return errors.New("repo_url이 설정되지 않았습니다")
	}
	repo := *d.Spec.RepoURL
	b.Printf("git clone %s (%s) — 노드 %s에서", repo, d.Spec.Branch, d.NodeID)
	var res *agentprotocol.FetchResult
	err := stage(ctx, m.Timeouts.Clone, "clone", func(ctx context.Context) (err error) {
		sha := ""
		if d.Trigger == "github" && d.CommitSHA != nil {
			sha = *d.CommitSHA
		}
		req := agentprotocol.FetchRequest{Repo: repo, Branch: d.Spec.Branch, SHA: sha,
			Token: m.githubTokenFor(ctx, t.Service.ID, repo), RootDir: d.Spec.RootDir}
		if key, kh := m.sshKeyFor(ctx, t.Service.ID, repo, b); key != "" {
			req.SSHKey, req.KnownHosts = key, kh
		}
		res, err = rd.fetchSource(ctx, req, b.Line)
		if err == nil && res.KnownHosts != "" {
			m.saveKnownHosts(ctx, res.KnownHosts)
		}
		return err
	})
	if err != nil {
		return err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		rd.cleanupSource(cctx, res.Workspace)
		cancel()
	}()
	c := res.Commit
	m.setCommit(ctx, d, &c)
	b.Printf("commit %s %q by %s", short(c.SHA), c.Message, c.Author)
	return m.remoteBuildWorkspace(ctx, t, d, b, rd, res)
}

// remoteUploadBuild는 CLI 업로드(tar.gz)를 대상 노드로 보내 풀고 그 노드에서 빌드한다.
func (m *Manager) remoteUploadBuild(ctx context.Context, t *Target, d *store.Deployment, b *logbook.Book, rd *remoteDriver, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open upload: %w", err)
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil {
		b.Printf("업로드된 소스(%s)를 노드 %s로 보냅니다", HumanBytes(st.Size()), d.NodeID)
	}
	var res *agentprotocol.FetchResult
	err = stage(ctx, m.Timeouts.Clone*4, "upload", func(ctx context.Context) (err error) {
		res, err = rd.uploadSource(ctx, f, d.Spec.RootDir)
		return err
	})
	// 업로드 파일은 run이 배포 끝에 지운다(placement의 다른 노드에도 보낸다).
	if err != nil {
		return err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		rd.cleanupSource(cctx, res.Workspace)
		cancel()
	}()
	return m.remoteBuildWorkspace(ctx, t, d, b, rd, res)
}

// remoteBuildWorkspace는 노드에 풀린 소스(workspace)에서 config-as-code를 적용하고 이미지를 만든다.
func (m *Manager) remoteBuildWorkspace(ctx context.Context, t *Target, d *store.Deployment, b *logbook.Book, rd *remoteDriver, res *agentprotocol.FetchResult) error {
	if res.ConfigName != "" {
		cfg, err := config.Parse(res.ConfigName, res.Config)
		if err != nil {
			return fmt.Errorf("config-as-code: %w", err)
		}
		if err := m.applyConfig(ctx, t, d, b, res.ConfigName, cfg); err != nil {
			return err
		}
	}
	m.setStatus(ctx, d, b, store.DepBuilding)
	key := m.sourceBuildKey(ctx, t, d, "")
	if m.reuseBuild(ctx, t, d, b, key) {
		m.Store.UpdateDeployment(context.WithoutCancel(ctx), d)
		return nil
	}
	image := m.imageRepo(t.Service.ID) + ":" + strings.ReplaceAll(d.ID, "_", "-")
	builder, err := m.remoteBuildImage(ctx, t, d, b, rd, res.Workspace, d.Spec.Builder, image)
	if err != nil {
		return err
	}
	d.BuilderUsed = &builder
	d.Image = &image
	d.BuildCacheKey = key
	m.Store.UpdateDeployment(context.WithoutCancel(ctx), d)
	return nil
}

// remoteBuildImage는 노드에 풀린 workspace를 builder로 빌드해 image를 만든다. d는 읽기만 한다. agent가 실제로 쓴 builder를 돌려준다.
func (m *Manager) remoteBuildImage(ctx context.Context, t *Target, d *store.Deployment, b *logbook.Book, rd *remoteDriver, workspace, builder, image string) (string, error) {
	vars, err := m.Resolve(ctx, t.Instance, &Override{InstanceID: t.Instance.ID, Vars: d.VarsRaw, Spec: &d.Spec, Deployment: d})
	if err != nil {
		return "", err
	}
	// builder는 agent가 정하므로 build.buildCommand는 RAILPACK_BUILD_CMD로 항상 넘긴다(Dockerfile 빌드는 ARG로 선언된 변수만 쓰므로 영향이 없다).
	buildVars := withBuildCommand(&d.Spec, vars.Vars, b)
	labels := map[string]string{node.LabelManaged: "true", node.LabelService: t.Service.ID, node.LabelDeploy: d.ID}
	var used string
	err = stage(ctx, m.Timeouts.Build, "build", func(ctx context.Context) (err error) {
		used, err = rd.buildImage(ctx, agentprotocol.BuildRequest{Workspace: workspace, RootDir: d.Spec.RootDir, Builder: builder,
			DockerfilePath: d.Spec.DockerfilePath, Image: image, Labels: labels, Vars: buildVars, Secrets: d.Spec.BuildSecrets}, b.Line)
		return err
	})
	return used, err
}
