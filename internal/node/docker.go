// Package node는 한 노드의 Docker 엔진을 다룬다.
// docker/git CLI를 직접 호출하는 로컬 드라이버다. keel.managed=true 라벨이 없는 리소스는 절대 건드리지 않는다.
package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	NetworkName     = "keel" // proxy 기본 네트워크(레거시 v0 호환)
	LabelManaged    = "keel.managed"
	LabelProject    = "keel.project"
	LabelEnv        = "keel.environment"
	LabelService    = "keel.service"
	LabelInstance   = "keel.instance"
	LabelDeploy     = "keel.deployment"
	LabelReplica    = "keel.replica"
	LabelRole       = "keel.role" // app | cron | predeploy | tcpproxy | helper | proxy
	LabelLegacyApp  = "keel.app"
	LabelVolumeID   = "keel.volume"
	LabelSandbox    = "keel.sandbox"
	RoleApp         = "app"
	RoleCron        = "cron"
	RolePreDeploy   = "predeploy"
	RoleTCPProxy    = "tcpproxy"
	RoleHelper      = "helper"
	RoleSandbox     = "sandbox"
	RoleMeshProxy   = "meshproxy" // 다른 노드의 서비스로 가는 mesh forwarder(포트 publish 없음, agent/서버가 직접 관리)
	HelperImage     = "busybox:stable"
	TCPForwardImage = "alpine/socat:1.8.0.3"
)

type Docker struct {
	Bin string // 기본 "docker"
}

func (d *Docker) bin() string {
	if d.Bin == "" {
		return "docker"
	}
	return d.Bin
}

// run은 명령을 실행하고, out이 있으면 출력을 줄 단위로 넘긴다.
func run(ctx context.Context, env []string, out func(string), name string, args ...string) (string, error) {
	return runIn(ctx, env, nil, out, name, args...)
}

func runIn(ctx context.Context, env []string, stdin io.Reader, out func(string), name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 5 * time.Second // 취소 시 자식 프로세스가 pipe를 잡고 있어도 멈추지 않게
	if env != nil {
		cmd.Env = env
	}
	cmd.Stdin = stdin
	var buf bytes.Buffer
	if out == nil {
		cmd.Stdout, cmd.Stderr = &buf, &buf
		err := cmd.Run()
		s := strings.TrimSpace(buf.String())
		if err != nil {
			if ctx.Err() != nil {
				return s, ctx.Err()
			}
			return s, fmt.Errorf("%s %s: %w: %s", name, args[0], err, lastLine(s))
		}
		return s, nil
	}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return "", err
	}
	var tail []string
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			l := sc.Text()
			out(l)
			tail = append(tail, l)
			if len(tail) > 5 {
				tail = tail[1:]
			}
		}
		io.Copy(io.Discard, pr)
	}()
	err := cmd.Wait()
	pw.Close()
	wg.Wait()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("%s %s: %w: %s", name, args[0], err, strings.Join(tail, " | "))
	}
	return "", nil
}

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

func (d *Docker) Ping(ctx context.Context) error {
	_, err := run(ctx, nil, nil, d.bin(), "version", "--format", "{{.Server.Version}}")
	return err
}

func (d *Docker) Version(ctx context.Context) string {
	s, _ := run(ctx, nil, nil, d.bin(), "version", "--format", "{{.Server.Version}}")
	return s
}

// ---------------- networks ----------------

// EnsureNetwork는 IPv6가 켜진 aven bridge 네트워크를 만든다(이미 있으면 그대로 둔다).
func (d *Docker) EnsureNetwork(ctx context.Context) error {
	return d.EnsureNamedNetwork(ctx, NetworkName, nil)
}

func (d *Docker) EnsureNamedNetwork(ctx context.Context, name string, labels map[string]string) error {
	if _, err := run(ctx, nil, nil, d.bin(), "network", "inspect", name); err == nil {
		return nil
	}
	args := []string{"network", "create", "--driver", "bridge", "--ipv6", "--label", LabelManaged + "=true"}
	for k, v := range labels {
		args = append(args, "--label", k+"="+v)
	}
	args = append(args, name)
	s, err := run(ctx, nil, nil, d.bin(), args...)
	if err != nil && strings.Contains(s, "already exists") {
		return nil
	}
	return err
}

// Connect는 컨테이너를 네트워크에 붙인다. 이미 붙어 있으면 무시한다.
func (d *Docker) Connect(ctx context.Context, network, container string) error {
	s, err := run(ctx, nil, nil, d.bin(), "network", "connect", network, container)
	if err != nil && (strings.Contains(s, "already exists") || strings.Contains(s, "already attached")) {
		return nil
	}
	return err
}

// RemoveNetwork는 aven이 만든 네트워크만 지운다. 붙어 있는 proxy는 먼저 뗀다.
func (d *Docker) RemoveNetwork(ctx context.Context, name string, detach ...string) error {
	s, err := run(ctx, nil, nil, d.bin(), "network", "inspect", "--format", "{{index .Labels \""+LabelManaged+"\"}}", name)
	if err != nil {
		return nil // 없음
	}
	if strings.TrimSpace(s) != "true" {
		return fmt.Errorf("network %s is not managed by aven", name)
	}
	for _, c := range detach {
		run(ctx, nil, nil, d.bin(), "network", "disconnect", "-f", name, c)
	}
	_, err = run(ctx, nil, nil, d.bin(), "network", "rm", name)
	return err
}

// NetworkInfo는 bridge 네트워크의 서브넷과 호스트 쪽 gateway 주소다.
type NetworkInfo struct {
	Subnets  []netip.Prefix
	Gateways []netip.Addr // IPv4 먼저
}

// NetworkInfo는 aven 네트워크의 IPAM 설정(서브넷, gateway)을 읽는다. aven 라벨이 없는 네트워크는 거부한다.
func (d *Docker) NetworkInfo(ctx context.Context, name string) (*NetworkInfo, error) {
	s, err := run(ctx, nil, nil, d.bin(), "network", "inspect", "--format", "{{json .}}", name)
	if err != nil {
		return nil, fmt.Errorf("network %s: %s", name, strings.TrimSpace(s))
	}
	return ParseNetworkInfo([]byte(s))
}

// ParseNetworkInfo는 `docker network inspect --format '{{json .}}'` 출력을 읽는다.
func ParseNetworkInfo(b []byte) (*NetworkInfo, error) {
	var res struct {
		Labels map[string]string
		IPAM   struct {
			Config []struct {
				Subnet  string
				Gateway string
			}
		}
	}
	if err := json.Unmarshal(bytes.TrimSpace(b), &res); err != nil {
		return nil, fmt.Errorf("network inspect: bad output: %w", err)
	}
	if res.Labels[LabelManaged] != "true" {
		return nil, errors.New("network is not managed by aven")
	}
	out := &NetworkInfo{}
	var v6 []netip.Addr
	for _, c := range res.IPAM.Config {
		if p, err := netip.ParsePrefix(c.Subnet); err == nil {
			out.Subnets = append(out.Subnets, p.Masked())
		}
		if a, err := netip.ParseAddr(c.Gateway); err == nil {
			if a.Unmap().Is4() {
				out.Gateways = append(out.Gateways, a.Unmap())
			} else {
				v6 = append(v6, a)
			}
		}
	}
	out.Gateways = append(out.Gateways, v6...)
	if len(out.Subnets) == 0 || len(out.Gateways) == 0 {
		return nil, errors.New("network has no subnet/gateway")
	}
	return out, nil
}

// ManagedNetworks는 aven 라벨이 붙은 네트워크 이름을 돌려준다.
func (d *Docker) ManagedNetworks(ctx context.Context) ([]string, error) {
	s, err := run(ctx, nil, nil, d.bin(), "network", "ls", "--filter", "label="+LabelManaged+"=true", "--format", "{{.Name}}")
	if err != nil {
		return nil, err
	}
	return splitLines(s), nil
}

func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// ---------------- git ----------------

type Commit struct {
	SHA, Message, Author string
}

// GitEnv는 git 실행 환경이다. 토큰은 argv가 아니라 GIT_CONFIG_* 환경변수(http.extraHeader)로 넘긴다.
func GitEnv(token string) []string {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/bin/true")
	if token != "" {
		auth := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
		env = append(env, "GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
			"GIT_CONFIG_VALUE_0=AUTHORIZATION: basic "+auth)
	}
	return env
}

// Clone은 브랜치를 얕게 clone하고 커밋 정보를 돌려준다. sha가 있으면 정확히 그 커밋을 checkout한다
// (SHA로 fetch, 서버가 허용하지 않으면 브랜치 전체 이력을 받아서 찾는다).
func Clone(ctx context.Context, repo, branch, sha, dir, token string, out func(string)) (*Commit, error) {
	return cloneEnv(ctx, GitEnv(token), repo, branch, sha, dir, out)
}

func cloneEnv(ctx context.Context, env []string, repo, branch, sha, dir string, out func(string)) (*Commit, error) {
	if _, err := run(ctx, env, out, "git", "clone", "--depth", "1", "--single-branch", "--branch", branch, "--", repo, dir); err != nil {
		return nil, err
	}
	if sha != "" {
		head, _ := run(ctx, env, nil, "git", "-C", dir, "rev-parse", "HEAD")
		if head != sha {
			out(fmt.Sprintf("checkout %s (branch HEAD is %s)", sha, head))
			if _, err := run(ctx, env, nil, "git", "-C", dir, "fetch", "--depth", "1", "origin", sha); err != nil {
				out("fetch by SHA not allowed by the remote; fetching branch history")
				if _, err := run(ctx, env, nil, "git", "-C", dir, "fetch", "--unshallow", "origin", branch); err != nil {
					return nil, err
				}
			}
			if _, err := run(ctx, env, nil, "git", "-C", dir, "-c", "advice.detachedHead=false", "checkout", "--detach", sha); err != nil {
				return nil, fmt.Errorf("commit %s not found on %s: %w", sha, branch, err)
			}
		}
	}
	s, err := run(ctx, env, nil, "git", "-C", dir, "log", "-1", "--format=%H%x00%an%x00%s")
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(s, "\x00", 3)
	for len(parts) < 3 {
		parts = append(parts, "")
	}
	return &Commit{SHA: parts[0], Author: parts[1], Message: parts[2]}, nil
}

// ---------------- images / build ----------------

// Build는 Dockerfile로 이미지를 만든다. buildArgs 값은 argv가 아니라 env로 넘긴다(`--build-arg KEY`).
func (d *Docker) Build(ctx context.Context, contextDir, dockerfile, tag string, labels, buildArgs map[string]string, out func(string)) error {
	return d.BuildTarget(ctx, contextDir, dockerfile, tag, "", labels, buildArgs, out)
}

// BuildTarget은 멀티 스테이지 Dockerfile의 target 스테이지까지 빌드한다(target이 비면 마지막 스테이지).
func (d *Docker) BuildTarget(ctx context.Context, contextDir, dockerfile, tag, target string, labels, buildArgs map[string]string, out func(string)) error {
	return d.BuildSecrets(ctx, contextDir, dockerfile, tag, target, labels, buildArgs, nil, out)
}

// BuildSecrets는 BuildTarget에 BuildKit build secret을 더한다. secret은 `--secret id=NAME,env=KEEL_SECRET_NAME`으로 넘기므로
// 값은 argv, 이미지 레이어, `docker history`에 남지 않는다(Dockerfile은 `RUN --mount=type=secret,id=NAME`으로 읽는다).
func (d *Docker) BuildSecrets(ctx context.Context, contextDir, dockerfile, tag, target string, labels, buildArgs, secrets map[string]string, out func(string)) error {
	args := []string{"build", "--progress=plain", "-t", tag, "-f", dockerfile}
	if target != "" {
		args = append(args, "--target", target)
	}
	for k, v := range labels {
		args = append(args, "--label", k+"="+v)
	}
	env := append(os.Environ(), "DOCKER_BUILDKIT=1")
	for _, k := range sortedKeys(buildArgs) {
		if _, isSecret := secrets[k]; isSecret {
			continue // 비밀은 build arg로 넘기지 않는다
		}
		args = append(args, "--build-arg", k)
		env = append(env, k+"="+buildArgs[k])
	}
	for _, k := range sortedKeys(secrets) {
		args = append(args, "--secret", "id="+k+",env=KEEL_SECRET_"+k)
		env = append(env, "KEEL_SECRET_"+k+"="+secrets[k])
	}
	args = append(args, contextDir)
	_, err := run(ctx, env, out, d.bin(), args...)
	return err
}

// RailpackBuild는 `railpack prepare`로 빌드 계획을 만들고, docker buildx(기본 builder)에
// railpack BuildKit frontend를 지정해 이미지를 만든다. 별도 BuildKit 컨테이너가 필요 없다.
// 변수는 BuildKit secret으로 넘긴다(값이 argv나 이미지 레이어에 남지 않는다).
func (d *Docker) RailpackBuild(ctx context.Context, railpack, frontend, srcDir, workDir, tag string, vars map[string]string, out func(string)) (*RailpackInfo, error) {
	plan := workDir + "/railpack-plan.json"
	info := workDir + "/railpack-info.json"
	args := []string{"prepare", srcDir, "--plan-out", plan, "--info-out", info}
	// railpack 설정용 변수(RAILPACK_*)만 prepare에 넘긴다. 나머지는 secret으로만 전달한다.
	for _, k := range sortedKeys(vars) {
		if strings.HasPrefix(k, "RAILPACK_") || strings.HasPrefix(k, "NIXPACKS_") {
			args = append(args, "--env", k+"="+vars[k])
		}
	}
	if _, err := run(ctx, nil, out, railpack, args...); err != nil {
		return nil, err
	}
	ri := &RailpackInfo{}
	if b, err := os.ReadFile(info); err == nil {
		json.Unmarshal(b, ri)
	}
	// plan에 secret 이름을 넣어 frontend가 빌드 단계에 변수를 마운트하게 한다.
	if b, err := os.ReadFile(plan); err == nil && len(vars) > 0 {
		var p map[string]any
		if json.Unmarshal(b, &p) == nil {
			names := []string{}
			if old, ok := p["secrets"].([]any); ok {
				for _, o := range old {
					if s, ok := o.(string); ok {
						names = append(names, s)
					}
				}
			}
			for _, k := range sortedKeys(vars) {
				names = append(names, k)
			}
			p["secrets"] = dedupe(names)
			nb, _ := json.MarshalIndent(p, "", "  ")
			os.WriteFile(plan, nb, 0o640)
		}
	}
	bargs := []string{"--load", "--progress=plain",
		"--build-arg", "BUILDKIT_SYNTAX=" + frontend, "-f", plan, "-t", tag}
	env := os.Environ()
	h := fnv(vars)
	bargs = append(bargs, "--build-arg", "secrets-hash="+h)
	for _, k := range sortedKeys(vars) {
		bargs = append(bargs, "--secret", "id="+k+",env=KEEL_SECRET_"+k)
		env = append(env, "KEEL_SECRET_"+k+"="+vars[k])
	}
	bargs = append(bargs, srcDir)
	build := func(builder string) error {
		_, err := run(ctx, env, out, d.bin(), append([]string{"buildx", "build", "--builder", builder}, bargs...)...)
		return err
	}
	err := build("default")
	if err != nil && ctx.Err() == nil && needsContainerBuilder(err) {
		if out != nil {
			out("기본 builder가 merge op를 지원하지 않습니다(containerd image store가 아님). docker-container builder " + RailpackBuilder + "로 다시 빌드합니다")
		}
		if err := d.ensureRailpackBuilder(ctx); err != nil {
			return nil, err
		}
		err = build(RailpackBuilder)
	}
	if err != nil {
		return nil, err
	}
	return ri, nil
}

// RailpackBuilder는 기본 builder로 railpack 빌드를 못 할 때 쓰는 docker-container driver buildx builder 이름이다.
const RailpackBuilder = "keel-railpack"

// needsContainerBuilder는 기본(docker driver) builder가 railpack frontend의 merge op를 거부했는지 본다.
// containerd image store를 쓰지 않는 daemon(Docker 28 이하, 업그레이드한 호스트)의 기본 builder가 그렇다.
func needsContainerBuilder(err error) bool {
	s := err.Error()
	return strings.Contains(s, "mergeop") || strings.Contains(s, "containerd image store")
}

// ensureRailpackBuilder는 RailpackBuilder가 없으면 만든다(BuildKit 컨테이너는 첫 빌드 때 뜬다).
func (d *Docker) ensureRailpackBuilder(ctx context.Context) error {
	if _, err := run(ctx, nil, nil, d.bin(), "buildx", "inspect", RailpackBuilder); err == nil {
		return nil
	}
	if _, err := run(ctx, nil, nil, d.bin(), "buildx", "create", "--name", RailpackBuilder, "--driver", "docker-container"); err != nil {
		// 동시에 다른 빌드가 만들었을 수 있다
		if _, ierr := run(ctx, nil, nil, d.bin(), "buildx", "inspect", RailpackBuilder); ierr != nil {
			return fmt.Errorf("railpack용 buildx builder를 만들지 못했습니다: %w", err)
		}
	}
	return nil
}

type RailpackInfo struct {
	DetectedProviders []string `json:"detectedProviders"`
}

func fnv(m map[string]string) string {
	var h uint64 = 14695981039346656037
	for _, k := range sortedKeys(m) {
		for _, c := range []byte(k + "=" + m[k] + "\x00") {
			h ^= uint64(c)
			h *= 1099511628211
		}
	}
	return fmt.Sprintf("%016x", h)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// RegistryAuth는 pull에 쓸 인증 정보다.
type RegistryAuth struct {
	Host     string `json:"host"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// ImageHost는 이미지 참조의 registry 호스트다(없으면 docker.io).
func ImageHost(image string) string {
	first, _, ok := strings.Cut(image, "/")
	if ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		return first
	}
	return "docker.io"
}

// MatchAuth는 이미지 호스트에 맞는 인증 정보를 고른다.
func MatchAuth(image string, auths []RegistryAuth) *RegistryAuth {
	host := ImageHost(image)
	for i := range auths {
		h := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(auths[i].Host, "https://"), "http://"), "/")
		if h == host || (host == "docker.io" && (h == "index.docker.io" || h == "registry-1.docker.io")) {
			return &auths[i]
		}
	}
	return nil
}

// Pull은 이미지를 받는다. auth가 있으면 배포마다 임시 DOCKER_CONFIG 디렉터리를 만들어 쓴다(argv/로그에 인증 정보가 남지 않는다).
func (d *Docker) Pull(ctx context.Context, image string, auth *RegistryAuth, out func(string)) error {
	var env []string
	if auth != nil {
		dir, err := os.MkdirTemp("", "keel-docker-config-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		key := ImageHost(image)
		if key == "docker.io" {
			key = "https://index.docker.io/v1/"
		}
		cfg, _ := json.Marshal(map[string]any{"auths": map[string]any{key: map[string]string{
			"auth": base64.StdEncoding.EncodeToString([]byte(auth.Username + ":" + auth.Password))}}})
		if err := os.WriteFile(dir+"/config.json", cfg, 0o600); err != nil {
			return err
		}
		env = append(os.Environ(), "DOCKER_CONFIG="+dir)
	}
	_, err := run(ctx, env, out, d.bin(), "pull", image)
	return err
}

func (d *Docker) ImageExists(ctx context.Context, image string) bool {
	_, err := run(ctx, nil, nil, d.bin(), "image", "inspect", "--format", "{{.Id}}", image)
	return err == nil
}

// ImageDigest returns a pulled image's registry manifest digest, if Docker recorded one.
func (d *Docker) ImageDigest(ctx context.Context, image string) (string, error) {
	if _, digest, ok := strings.Cut(image, "@"); ok && digest != "" {
		return digest, nil
	}
	out, err := run(ctx, nil, nil, d.bin(), "image", "inspect", "--format", "{{json .RepoDigests}}", image)
	if err != nil {
		return "", err
	}
	var refs []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &refs); err != nil {
		return "", err
	}
	for _, ref := range refs {
		if _, digest, ok := strings.Cut(ref, "@"); ok && strings.HasPrefix(digest, "sha256:") {
			return digest, nil
		}
	}
	return "", nil
}

// Tag는 이미지에 다른 이름을 붙인다.
func (d *Docker) Tag(ctx context.Context, src, dst string) error {
	_, err := run(ctx, nil, nil, d.bin(), "tag", src, dst)
	return err
}

// ImagesByRepo는 repository 이름(예: keel/svc_x)의 이미지 태그 목록이다.
func (d *Docker) ImagesByRepo(ctx context.Context, repo string) []string {
	s, err := run(ctx, nil, nil, d.bin(), "images", "--filter", "reference="+repo, "--format", "{{.Repository}}:{{.Tag}}")
	if err != nil {
		return nil
	}
	return splitLines(s)
}

func (d *Docker) RemoveImage(ctx context.Context, image string) {
	run(ctx, nil, nil, d.bin(), "rmi", image)
}

// CommitSandbox snapshots a running Aven sandbox's writable layer into an internal image.
func (d *Docker) CommitSandbox(ctx context.Context, container, image string) error {
	tag := strings.TrimPrefix(image, "keel/sandbox:")
	if len(tag) < 4 || len(tag) > 80 || strings.Trim(tag, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
		return fmt.Errorf("invalid sandbox snapshot image")
	}
	st, err := d.Inspect(ctx, container)
	if err != nil {
		return err
	}
	if st.Labels[LabelManaged] != "true" || st.Labels[LabelRole] != RoleSandbox {
		return fmt.Errorf("container %s is not a Aven sandbox", container)
	}
	if !st.Running {
		return fmt.Errorf("sandbox %s is not running", container)
	}
	_, err = run(ctx, nil, nil, d.bin(), "commit", container, image)
	return err
}

// ---------------- containers ----------------

type Mount struct {
	Volume   string // docker named volume
	Path     string
	Bind     string // 호스트 경로(비어 있지 않으면 bind mount. 로컬 노드의 compose 스택에서만 쓴다)
	ReadOnly bool
}

type RunSpec struct {
	Name       string
	Image      string
	Env        map[string]string
	Labels     map[string]string
	Network    string
	Aliases    []string
	ExtraHosts []string
	Mounts     []Mount
	Restart    string   // docker --restart 값. 비우면 no
	Entrypoint *string  // nil이면 이미지 기본값
	Cmd        []string // 이미지 CMD를 대신한다
	Publish    []string // -p 인자
	Hostname   string
	User       string // docker --user
	WorkDir    string // docker --workdir
	MemoryMB   int    // docker --memory (MiB). 0이면 제한 없음
	CPUs       string // docker --cpus (예: "0.5"). 비면 제한 없음
}

func (s RunSpec) args(detach bool) ([]string, []string) {
	args := []string{"run"}
	if detach {
		args = append(args, "-d")
	}
	args = append(args, "--name", s.Name, "--log-opt", "max-size=20m", "--log-opt", "max-file=3")
	if s.Network != "" {
		args = append(args, "--network", s.Network)
		for _, a := range s.Aliases {
			args = append(args, "--network-alias", a)
		}
	}
	for _, host := range s.ExtraHosts {
		args = append(args, "--add-host", host)
	}
	if s.Restart != "" {
		args = append(args, "--restart", s.Restart)
	}
	if s.Hostname != "" {
		args = append(args, "--hostname", s.Hostname)
	}
	for _, k := range sortedKeys(s.Labels) {
		args = append(args, "--label", k+"="+s.Labels[k])
	}
	for _, m := range s.Mounts {
		if m.Bind != "" {
			mount := "type=bind,source=" + m.Bind + ",target=" + m.Path
			if m.ReadOnly {
				mount += ",readonly"
			}
			args = append(args, "--mount", mount)
			continue
		}
		args = append(args, "--mount", "type=volume,source="+m.Volume+",target="+m.Path)
	}
	if s.MemoryMB > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", s.MemoryMB))
	}
	if s.CPUs != "" {
		args = append(args, "--cpus", s.CPUs)
	}
	if s.User != "" {
		args = append(args, "--user", s.User)
	}
	if s.WorkDir != "" {
		args = append(args, "--workdir", s.WorkDir)
	}
	for _, p := range s.Publish {
		args = append(args, "-p", p)
	}
	// 환경변수 값은 커맨드라인에 노출하지 않으려고 `-e KEY`로 넘기고, 값은 docker CLI 프로세스의 env로 전달한다.
	env := os.Environ()
	for _, k := range sortedKeys(s.Env) {
		args = append(args, "-e", k)
		env = append(env, k+"="+s.Env[k])
	}
	if s.Entrypoint != nil {
		args = append(args, "--entrypoint", *s.Entrypoint)
	}
	args = append(args, s.Image)
	args = append(args, s.Cmd...)
	return args, env
}

// Run은 컨테이너를 백그라운드로 띄운다.
func (d *Docker) Run(ctx context.Context, s RunSpec) error {
	args, env := s.args(true)
	_, err := run(ctx, env, nil, d.bin(), args...)
	return err
}

// Create는 컨테이너를 만들기만 한다(시작하지 않음).
func (d *Docker) Create(ctx context.Context, s RunSpec) error {
	args, env := s.args(false)
	args[0] = "create"
	_, err := run(ctx, env, nil, d.bin(), args...)
	return err
}

// RunToCompletion은 일회성 컨테이너를 띄워 끝날 때까지 기다리고, 로그를 넘긴 뒤 지운다. 종료 코드를 돌려준다.
func (d *Docker) RunToCompletion(ctx context.Context, s RunSpec, out func(stream, line string)) (int, error) {
	s.Restart = ""
	if err := d.Run(ctx, s); err != nil {
		return -1, err
	}
	defer d.Remove(context.WithoutCancel(ctx), s.Name, 2)
	logCtx, stopLogs := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.FollowLogs(logCtx, s.Name, "all", true, func(l LogLine) { out(l.Stream, l.Msg) })
	}()
	code, err := d.Wait(ctx, s.Name)
	// 로그가 다 흘러나올 시간을 조금 준다.
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	stopLogs()
	<-done
	return code, err
}

func (d *Docker) Wait(ctx context.Context, name string) (int, error) {
	s, err := run(ctx, nil, nil, d.bin(), "wait", name)
	if err != nil {
		return -1, err
	}
	var code int
	fmt.Sscanf(strings.TrimSpace(s), "%d", &code)
	return code, nil
}

type ContainerState struct {
	Name         string
	Running      bool
	Status       string // created | running | restarting | exited | paused | dead
	ExitCode     int
	RestartCount int
	StartedAt    time.Time
	IPs          map[string][]string // network → IPs (IPv6 먼저)
	Labels       map[string]string
	Volumes      []string // 마운트된 named volume 이름
}

// IP는 network에서 컨테이너 주소를 돌려준다(IPv6 우선).
func (c *ContainerState) IP(network string) []string { return c.IPs[network] }

var ErrNoContainer = errors.New("no such container")

func (d *Docker) Inspect(ctx context.Context, name string) (*ContainerState, error) {
	m, err := d.InspectMany(ctx, []string{name})
	if err != nil {
		return nil, err
	}
	if st, ok := m[name]; ok {
		return st, nil
	}
	return nil, ErrNoContainer
}

// InspectMany는 여러 컨테이너를 한 번에 조회한다. 없는 컨테이너는 결과에서 빠진다.
func (d *Docker) InspectMany(ctx context.Context, names []string) (map[string]*ContainerState, error) {
	out := map[string]*ContainerState{}
	if len(names) == 0 {
		return out, nil
	}
	args := append([]string{"inspect", "--type", "container"}, names...)
	cmd := exec.CommandContext(ctx, d.bin(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil && stdout.Len() == 0 {
		if strings.Contains(stderr.String(), "No such") {
			return out, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("docker inspect: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var res []struct {
		Name         string
		RestartCount int
		Config       struct{ Labels map[string]string }
		Mounts       []struct {
			Type string
			Name string
		}
		State struct {
			Running   bool
			Status    string
			ExitCode  int
			StartedAt string
		}
		NetworkSettings struct {
			Networks map[string]struct {
				IPAddress         string
				GlobalIPv6Address string
			}
		}
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return nil, fmt.Errorf("inspect: bad output: %w", err)
	}
	for _, r := range res {
		cs := &ContainerState{Name: strings.TrimPrefix(r.Name, "/"), Running: r.State.Running, Status: r.State.Status,
			ExitCode: r.State.ExitCode, RestartCount: r.RestartCount, IPs: map[string][]string{}, Labels: r.Config.Labels}
		cs.StartedAt, _ = time.Parse(time.RFC3339Nano, r.State.StartedAt)
		for _, mt := range r.Mounts {
			if mt.Type == "volume" && mt.Name != "" {
				cs.Volumes = append(cs.Volumes, mt.Name)
			}
		}
		for n, nw := range r.NetworkSettings.Networks {
			if nw.GlobalIPv6Address != "" {
				cs.IPs[n] = append(cs.IPs[n], nw.GlobalIPv6Address)
			}
			if nw.IPAddress != "" {
				cs.IPs[n] = append(cs.IPs[n], nw.IPAddress)
			}
		}
		out[cs.Name] = cs
	}
	return out, nil
}

// Remove는 컨테이너를 graceful하게 멈추고 지운다. 없으면 무시한다.
func (d *Docker) Remove(ctx context.Context, name string, graceSeconds int) error {
	s, err := run(ctx, nil, nil, d.bin(), "stop", "-t", fmt.Sprint(graceSeconds), name)
	if err != nil && strings.Contains(s, "No such") {
		return nil
	}
	s, err = run(ctx, nil, nil, d.bin(), "rm", "-f", "-v", name)
	if err != nil && strings.Contains(s, "No such") {
		return nil
	}
	return err
}

func (d *Docker) Stop(ctx context.Context, name string, graceSeconds int) error {
	_, err := run(ctx, nil, nil, d.bin(), "stop", "-t", fmt.Sprint(graceSeconds), name)
	return err
}

func (d *Docker) Start(ctx context.Context, name string) error {
	_, err := run(ctx, nil, nil, d.bin(), "start", name)
	return err
}

func (d *Docker) Restart(ctx context.Context, name string, graceSeconds int) error {
	_, err := run(ctx, nil, nil, d.bin(), "restart", "-t", fmt.Sprint(graceSeconds), name)
	return err
}

// ListManaged는 aven이 만든 컨테이너 이름 → 라벨 맵을 돌려준다.
func (d *Docker) ListManaged(ctx context.Context) (map[string]map[string]string, error) {
	s, err := run(ctx, nil, nil, d.bin(), "ps", "-a", "--filter", "label="+LabelManaged+"=true",
		"--format", "{{.Names}}\t{{.Labels}}")
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		name, labels, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		m := map[string]string{}
		for _, kv := range strings.Split(labels, ",") {
			if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "keel.") {
				m[k] = v
			}
		}
		out[name] = m
	}
	return out, nil
}

// ---------------- logs ----------------

type LogLine struct {
	T      time.Time
	Stream string // stdout | stderr
	Msg    string
}

// TailLogs는 컨테이너의 마지막 로그 n줄을 돌려준다.
func (d *Docker) TailLogs(ctx context.Context, name string, n int) []string {
	var lines []string
	run(ctx, nil, func(l string) { lines = append(lines, l) }, d.bin(), "logs", "--tail", fmt.Sprint(n), name)
	return lines
}

// FollowLogs는 `docker logs --timestamps`를 stdout/stderr 구분해 넘긴다. follow면 ctx가 끝날 때까지 따라간다.
func (d *Docker) FollowLogs(ctx context.Context, name, tail string, follow bool, out func(LogLine)) error {
	return d.LogsSince(ctx, name, tail, "", follow, out)
}

// LogsSince는 FollowLogs와 같고, since(RFC3339)가 있으면 그 이후 로그만 읽는다.
func (d *Docker) LogsSince(ctx context.Context, name, tail, since string, follow bool, out func(LogLine)) error {
	args := []string{"logs", "--timestamps", "--tail", tail}
	if since != "" {
		args = append(args, "--since", since)
	}
	if follow {
		args = append(args, "-f")
	}
	args = append(args, name)
	cmd := exec.CommandContext(ctx, d.bin(), args...)
	cmd.WaitDelay = 2 * time.Second
	so, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	se, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	read := func(r io.Reader, stream string) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			l := ParseLogLine(sc.Text())
			l.Stream = stream
			mu.Lock()
			out(l)
			mu.Unlock()
		}
	}
	wg.Add(2)
	go read(so, "stdout")
	go read(se, "stderr")
	wg.Wait()
	return cmd.Wait()
}

// ParseLogLine은 "2026-10-16T12:00:00.123456789Z message" 형식을 나눈다.
func ParseLogLine(s string) LogLine {
	if ts, msg, ok := strings.Cut(s, " "); ok {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			return LogLine{T: t, Msg: msg}
		}
	}
	return LogLine{T: time.Now().UTC(), Msg: s}
}

// ---------------- events ----------------

type ContainerEvent struct {
	Action string
	Name   string
	Labels map[string]string
	Exit   string
}

// Events는 aven 컨테이너의 die/start 이벤트를 넘긴다. ctx가 끝날 때까지 돈다.
func (d *Docker) Events(ctx context.Context, out func(ContainerEvent)) error {
	cmd := exec.CommandContext(ctx, d.bin(), "events", "--filter", "type=container", "--filter", "label="+LabelManaged+"=true",
		"--filter", "event=die", "--filter", "event=start", "--format", "{{json .}}")
	cmd.WaitDelay = 2 * time.Second
	so, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(so)
	for sc.Scan() {
		var ev struct {
			Action string `json:"Action"`
			Actor  struct {
				Attributes map[string]string `json:"Attributes"`
			} `json:"Actor"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		a := ev.Actor.Attributes
		out(ContainerEvent{Action: ev.Action, Name: a["name"], Labels: a, Exit: a["exitCode"]})
	}
	return cmd.Wait()
}

// ---------------- volumes ----------------

func (d *Docker) EnsureVolume(ctx context.Context, name string, labels map[string]string) error {
	if _, err := run(ctx, nil, nil, d.bin(), "volume", "inspect", name); err == nil {
		return nil
	}
	args := []string{"volume", "create", "--label", LabelManaged + "=true"}
	for _, k := range sortedKeys(labels) {
		args = append(args, "--label", k+"="+labels[k])
	}
	args = append(args, name)
	_, err := run(ctx, nil, nil, d.bin(), args...)
	return err
}

// RemoveVolume은 aven 라벨이 있는 볼륨만 지운다.
func (d *Docker) RemoveVolume(ctx context.Context, name string) error {
	s, err := run(ctx, nil, nil, d.bin(), "volume", "inspect", "--format", "{{index .Labels \""+LabelManaged+"\"}}", name)
	if err != nil {
		return nil
	}
	if strings.TrimSpace(s) != "true" {
		return fmt.Errorf("volume %s is not managed by aven", name)
	}
	_, err = run(ctx, nil, nil, d.bin(), "volume", "rm", "-f", name)
	return err
}

// VolumeSizes는 aven 볼륨 이름 → 바이트 크기다(`docker system df -v`, 느리므로 가끔만 부른다).
func (d *Docker) VolumeSizes(ctx context.Context) (map[string]int64, error) {
	s, err := run(ctx, nil, nil, d.bin(), "system", "df", "-v", "--format", "{{json .Volumes}}")
	if err != nil {
		return nil, err
	}
	var vols []struct {
		Name   string
		Size   string
		Labels string
	}
	if err := json.Unmarshal([]byte(s), &vols); err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, v := range vols {
		if strings.Contains(v.Labels, LabelManaged+"=true") {
			out[v.Name] = ParseSize(v.Size)
		}
	}
	return out, nil
}

// ExportVolume은 볼륨 내용을 tar 스트림으로 w에 쓴다(짧게 사는 helper 컨테이너 사용).
func (d *Docker) ExportVolume(ctx context.Context, volume, name string, w io.Writer) error {
	cmd := exec.CommandContext(ctx, d.bin(), "run", "--rm", "--name", name, "--label", LabelManaged+"=true", "--label", LabelRole+"="+RoleHelper,
		"--network", "none", "--mount", "type=volume,source="+volume+",target=/data,readonly", HelperImage, "tar", "-C", "/data", "-cf", "-", ".")
	cmd.WaitDelay = 5 * time.Second
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = w, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("export volume: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// SaveImage는 이미지를 docker save tar 스트림으로 w에 쓴다(빌드 노드 → 배포 노드 전송용).
func (d *Docker) SaveImage(ctx context.Context, image string, w io.Writer) error {
	cmd := exec.CommandContext(ctx, d.bin(), "save", image)
	cmd.WaitDelay = 5 * time.Second
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = w, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker save %s: %v: %s", image, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// LoadImage는 docker save 형식(tar, gzip도 가능)을 docker load로 읽는다.
func (d *Docker) LoadImage(ctx context.Context, r io.Reader) error {
	cmd := exec.CommandContext(ctx, d.bin(), "load", "-q")
	cmd.WaitDelay = 5 * time.Second
	var out bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = r, &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker load: %v: %s", err, strings.TrimSpace(out.String()))
	}
	return nil
}

// ImportVolume은 볼륨을 비우고 r의 tar 스트림을 푼다.
func (d *Docker) ImportVolume(ctx context.Context, volume, name string, r io.Reader) error {
	cmd := exec.CommandContext(ctx, d.bin(), "run", "--rm", "-i", "--name", name, "--label", LabelManaged+"=true", "--label", LabelRole+"="+RoleHelper,
		"--network", "none", "--mount", "type=volume,source="+volume+",target=/data", HelperImage,
		"sh", "-c", "find /data -mindepth 1 -maxdepth 1 -exec rm -rf {} + && tar -C /data -xpf -")
	cmd.WaitDelay = 5 * time.Second
	var out bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = r, &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("import volume: %v: %s", err, strings.TrimSpace(out.String()))
	}
	return nil
}

// ---------------- stats ----------------

type Stats struct {
	Name      string
	CPU       float64
	Mem       int64
	MemLimit  int64
	Rx, Tx    int64
	BlockRead int64
}

// Stats는 `docker stats --no-stream --format json`으로 지정한 컨테이너의 사용량을 읽는다.
func (d *Docker) Stats(ctx context.Context, names []string) ([]Stats, error) {
	if len(names) == 0 {
		return nil, nil
	}
	args := append([]string{"stats", "--no-stream", "--format", "json"}, names...)
	cmd := exec.CommandContext(ctx, d.bin(), args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Run() // 일부 컨테이너가 사라졌어도 나머지 결과는 쓴다
	var out []Stats
	for _, line := range splitLines(stdout.String()) {
		var r struct {
			Name     string `json:"Name"`
			CPUPerc  string `json:"CPUPerc"`
			MemUsage string `json:"MemUsage"`
			NetIO    string `json:"NetIO"`
		}
		if json.Unmarshal([]byte(line), &r) != nil {
			continue
		}
		s := Stats{Name: r.Name}
		fmt.Sscanf(strings.TrimSuffix(r.CPUPerc, "%"), "%g", &s.CPU)
		if a, b, ok := strings.Cut(r.MemUsage, "/"); ok {
			s.Mem, s.MemLimit = ParseSize(a), ParseSize(b)
		}
		if a, b, ok := strings.Cut(r.NetIO, "/"); ok {
			s.Rx, s.Tx = ParseSize(a), ParseSize(b)
		}
		out = append(out, s)
	}
	return out, nil
}

// ParseSize는 docker의 사람이 읽는 크기("1.5MiB", "2kB", "0B")를 바이트로 바꾼다.
func ParseSize(s string) int64 {
	s = strings.TrimSpace(s)
	units := []struct {
		suf string
		mul float64
	}{
		{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40},
		{"kB", 1e3}, {"KB", 1e3}, {"MB", 1e6}, {"GB", 1e9}, {"TB", 1e12}, {"B", 1},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suf) {
			var v float64
			fmt.Sscanf(strings.TrimSpace(strings.TrimSuffix(s, u.suf)), "%g", &v)
			return int64(v * u.mul)
		}
	}
	var v float64
	fmt.Sscanf(s, "%g", &v)
	return int64(v)
}

// ExposedPorts는 이미지가 EXPOSE한 TCP 포트 목록(오름차순)이다.
func (d *Docker) ExposedPorts(ctx context.Context, image string) []int {
	s, err := run(ctx, nil, nil, d.bin(), "image", "inspect", "--format", "{{json .Config.ExposedPorts}}", image)
	if err != nil {
		return nil
	}
	var m map[string]any
	json.Unmarshal([]byte(s), &m)
	var out []int
	for k := range m {
		var p int
		var proto string
		if n, _ := fmt.Sscanf(k, "%d/%s", &p, &proto); n >= 1 && (proto == "" || proto == "tcp") {
			out = append(out, p)
		}
	}
	sort.Ints(out)
	return out
}

// VolumeMountpoint는 볼륨의 호스트 경로다.
func (d *Docker) VolumeMountpoint(ctx context.Context, name string) (string, error) {
	s, err := run(ctx, nil, nil, d.bin(), "volume", "inspect", "--format", "{{.Mountpoint}}", name)
	return strings.TrimSpace(s), err
}

// RunStream은 일회성 컨테이너(`docker run --rm`)를 실행하면서 stdin을 넣고 stdout을 w로 흘린다. DB 논리 백업/복원에 쓴다.
// stderr는 줄 단위로 넘긴다. 종료 코드를 돌려주고, ctx가 끝나도 컨테이너는 지운다.
func (d *Docker) RunStream(ctx context.Context, s RunSpec, stdin io.Reader, stdout io.Writer, stderr func(string)) (int, error) {
	s.Restart = ""
	args, env := s.args(false)
	pre := []string{"run", "--rm"}
	if stdin != nil {
		pre = append(pre, "-i")
	}
	args = append(pre, args[1:]...)
	cmd := exec.CommandContext(ctx, d.bin(), args...)
	cmd.Env = env
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdin = stdin
	if stdout == nil {
		stdout = io.Discard
	}
	cmd.Stdout = stdout
	errPipe, err := cmd.StderrPipe()
	if err != nil {
		return -1, err
	}
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	defer d.Remove(context.WithoutCancel(ctx), s.Name, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(errPipe)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			if stderr != nil {
				stderr(sc.Text())
			}
		}
	}()
	<-done
	err = cmd.Wait()
	if err == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return -1, err
}
