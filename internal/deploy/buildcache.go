package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"

	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// repo 배포의 빌드 입력(커밋 SHA, root_dir, 빌드 설정, 빌드 시점 변수)을 해시해 build_cache_key로 남기고,
// 서비스의 skip_builds가 켜져 있으면 같은 키로 만든 이미지가 배포 노드에 아직 남아 있을 때 빌드 없이 그 이미지를 쓴다.

// buildCacheInputs는 빌드 결과를 정하는 입력이다. JSON으로 직렬화해 해시하며 값 자체는 저장하지 않는다.
type buildCacheInputs struct {
	CommitSHA      string            `json:"commit_sha"`
	RootDir        string            `json:"root_dir"`
	Builder        string            `json:"builder"`
	DockerfilePath string            `json:"dockerfile_path"`
	BuildCommand   string            `json:"build_command,omitempty"`
	BuildSecrets   []string          `json:"build_secrets,omitempty"`
	ComposeTarget  string            `json:"compose_target,omitempty"`
	ComposeArgs    map[string]string `json:"compose_args,omitempty"`
	Variables      map[string]string `json:"variables"`
}

// buildCacheKey는 d의 빌드 입력 해시다. 커밋이 정해진 저장소 소스(업로드 아님)일 때만 키를 만든다.
// vars는 빌드에 영향을 주는 변수(buildTimeVars)다. json.Marshal은 map 키를 정렬하므로 순서와 무관하다.
func buildCacheKey(d *store.Deployment, builder string, vars map[string]string) string {
	if d == nil || d.Source != "" || d.CommitSHA == nil || *d.CommitSHA == "" {
		return ""
	}
	secrets := append([]string(nil), d.Spec.BuildSecrets...)
	sort.Strings(secrets)
	in := buildCacheInputs{CommitSHA: *d.CommitSHA, RootDir: d.Spec.RootDir, Builder: builder,
		DockerfilePath: d.Spec.DockerfilePath, BuildSecrets: secrets, Variables: vars}
	if in.Variables == nil {
		in.Variables = map[string]string{}
	}
	if d.Spec.BuildCommand != nil {
		in.BuildCommand = *d.Spec.BuildCommand
	}
	if c := d.Spec.Compose; c != nil {
		in.ComposeTarget, in.ComposeArgs = c.Target, c.BuildArgs
	}
	data, err := json.Marshal(in)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// buildTimeVars는 해석된 변수 중 빌드 결과에 영향을 주는 사용자 변수다.
// 자동 주입 변수(KEEL_DEPLOYMENT_ID, KEEL_ENVIRONMENT_* 등)는 배포/환경마다 달라 빼고, 사용자가 같은 이름으로 정의했을 때만 넣는다.
// dockerfile이 주어지면 Dockerfile 빌드처럼 ARG로 선언된 변수와 build secret만 남긴다(나머지는 빌드에 넘어가지 않는다).
func buildTimeVars(d *store.Deployment, resolved map[string]string, dockerfile string) map[string]string {
	out := map[string]string{}
	for k := range d.VarsRaw {
		if v, ok := resolved[k]; ok {
			out[k] = v
		}
	}
	if dockerfile == "" {
		return out
	}
	plain, secrets := node.SplitSecrets(out, d.Spec.BuildSecrets)
	args := node.BuildArgs(dockerfile, plain)
	if args == nil {
		args = map[string]string{}
	}
	for k, v := range secrets {
		args[k] = v
	}
	return args
}

// sourceBuildKey는 d의 build cache key를 계산한다. root가 있으면 로컬 소스 경로다(builder=auto 판정과 ARG 필터에 쓴다).
// 키를 만들 수 없으면(업로드, 커밋 없음, 변수 해석 실패) ""다.
func (m *Manager) sourceBuildKey(ctx context.Context, t *Target, d *store.Deployment, root string) string {
	if t.Service.SourceType != "repo" || d.Source != "" || d.CommitSHA == nil || *d.CommitSHA == "" {
		return ""
	}
	res, err := m.Resolve(ctx, t.Instance, &Override{InstanceID: t.Instance.ID, Vars: d.VarsRaw, Spec: &d.Spec, Deployment: d})
	if err != nil {
		return ""
	}
	builder, dockerfile := d.Spec.Builder, ""
	if root != "" {
		if p, err := safeJoin(root, d.Spec.DockerfilePath); err == nil {
			_, statErr := os.Stat(p)
			if builder == "auto" || builder == "" {
				builder = "railpack"
				if statErr == nil {
					builder = "dockerfile"
				}
			}
			if builder == "dockerfile" && statErr == nil {
				dockerfile = p
			}
		}
	}
	return buildCacheKey(d, builder, buildTimeVars(d, res.Vars, dockerfile))
}

// reuseBuild는 skip_builds가 켜져 있고 강제 빌드가 아니면 같은 key로 빌드한 이미지 중 배포 노드에 남아 있는 가장 최근 것을
// d에 채운다. placement의 다른 노드에 이미지가 없으면 prepareExtraImages가 같은 커밋으로 그 노드에서 빌드한다.
func (m *Manager) reuseBuild(ctx context.Context, t *Target, d *store.Deployment, b interface{ Printf(string, ...any) }, key string) bool {
	if key == "" || !d.Spec.SkipBuilds {
		return false
	}
	if d.ForceBuild {
		b.Printf("강제 빌드: 보존된 이미지를 재사용하지 않고 새로 빌드합니다")
		return false
	}
	cands, err := m.Store.BuildCacheCandidates(ctx, t.Service.ID, key)
	if err != nil {
		b.Printf("WARN: 빌드 캐시를 조회하지 못해 새로 빌드합니다: %v", err)
		return false
	}
	if !m.nodeOnline(d.NodeID) {
		return false
	}
	drv := m.driverForNode(d.NodeID)
	for _, c := range cands {
		if c.ID == d.ID || c.Image == nil || *c.Image == "" || !drv.ImageExists(ctx, *c.Image) {
			continue
		}
		origin := c.ID
		if c.BuildReusedFrom != nil && *c.BuildReusedFrom != "" {
			origin = *c.BuildReusedFrom // 재사용의 재사용이면 실제로 빌드한 배포를 가리킨다
		}
		img := *c.Image
		d.Image, d.BuildReusedFrom, d.BuildCacheKey = &img, &origin, key
		if c.BuilderUsed != nil {
			bu := *c.BuilderUsed
			d.BuilderUsed = &bu
		}
		b.Printf("빌드 건너뜀 — 배포 %s의 이미지를 재사용합니다 (%s)", origin, img)
		return true
	}
	b.Printf("skipped builds: 같은 빌드 입력으로 만든 이미지가 남아 있지 않아 새로 빌드합니다")
	return false
}
