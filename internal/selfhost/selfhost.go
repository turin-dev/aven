// Package selfhost는 aven-server를 Aven이 배포하는 컨테이너로 돌린다(Aven on Aven, docs/selfhost.md).
//
// aven-server 컨테이너("aven-server")는 호스트 네트워크·PID 네임스페이스와 docker.sock을 쓰는 특권 컨테이너다.
// 이 스펙은 코드에 고정되어 있고 사용자 서비스의 RunSpec으로는 표현할 수 없다(agent도 거부한다).
// self 서비스(KEEL_SELF_SERVICE)의 배포는 이미지를 만든 뒤 컨테이너를 띄우는 대신 upgrader 컨테이너
// ("aven-selfupgrade", 새 이미지)에 넘긴다. upgrader는 이전 aven-server를 멈추고 새 이미지로 다시 띄운 뒤
// 헬스체크가 통과하지 않으면 이전 컨테이너로 되돌린다. SQLite를 두 프로세스가 동시에 열지 않도록 항상 멈춘 뒤에 띄운다.
package selfhost

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	ContainerName = "aven-server"      // 지금 도는 aven-server
	PrevName      = "aven-server-prev" // 교체 중 멈춰 둔 이전 aven-server(실패하면 되살린다)
	UpgraderName  = "aven-selfupgrade" // 교체를 맡는 일회성 컨테이너(새 이미지)

	EnvActive = "KEEL_SELFHOST"   // 컨테이너 안에서 "1"
	EnvImage  = "KEEL_SELF_IMAGE" // 컨테이너가 도는 이미지
	LabelSelf = "keel.self"

	DefaultEnvFile = "/etc/keel/aven.env"
	DefaultUnit    = "aven-server" // bootstrap이 대신하는 systemd 유닛
	// BinDir은 이미지 안에서 aven-server, railpack, agent·CLI 다운로드가 있는 곳이다(Dockerfile).
	BinDir     = "/usr/local/bin"
	DockerSock = "/var/run/docker.sock"
	resultFile = "selfhost-result.json"
)

// Active는 aven-server가 self 컨테이너 안에서 도는지 알려준다.
func Active() bool { return os.Getenv(EnvActive) == "1" }

// Image는 지금 도는 self 컨테이너의 이미지다(컨테이너가 아니면 "").
func Image() string {
	if !Active() {
		return ""
	}
	return os.Getenv(EnvImage)
}

// Paths는 aven-server 컨테이너에 같은 경로로 마운트하는 호스트 경로다.
type Paths struct {
	DataDir    string // KEEL_DATA(SQLite, 키, 로그, 빌드 작업 디렉터리)
	EnvFile    string // aven.env(docker --env-file로 읽는다: 따옴표를 벗기지 않으니 값에 따옴표를 쓰지 않는다)
	DockerRoot string // docker info의 DockerRootDir(keel-proxy와 공유하는 볼륨 안 unix socket을 만든다)
}

func (p Paths) withDefaults() Paths {
	if p.DataDir == "" {
		p.DataDir = "/var/lib/keel"
	}
	if p.EnvFile == "" {
		p.EnvFile = DefaultEnvFile
	}
	if p.DockerRoot == "" {
		p.DockerRoot = "/var/lib/docker"
	}
	return p
}

// RunArgs는 aven-server 컨테이너를 만드는 docker run 인자다. 새 이미지의 upgrader가 자기 코드의 스펙으로 띄우므로
// 스펙을 바꾸면 다음 배포부터 반영된다.
func RunArgs(image string, p Paths) []string {
	p = p.withDefaults()
	envDir := filepath.Dir(p.EnvFile)
	volumes := filepath.Join(p.DockerRoot, "volumes")
	return []string{"run", "-d", "--name", ContainerName,
		"--restart", "unless-stopped",
		// 호스트 노드 자체를 다룬다: Tailscale 주소에서 listen, 도메인별 IPv6 주소(ip), egress 규칙(ip6tables),
		// 네트워크 로그(raw socket), 컨테이너 포트 감지(/proc/<pid>/net), 호스트 지표.
		"--network", "host", "--pid", "host", "--uts", "host", "--privileged",
		"--env-file", p.EnvFile,
		"-e", EnvActive + "=1", "-e", EnvImage + "=" + image, "-e", "KEEL_DATA=" + p.DataDir,
		// env 파일의 호스트 경로 대신 이미지에 든 railpack과 다운로드 바이너리를 쓴다.
		"-e", "KEEL_RAILPACK_BIN=" + BinDir + "/railpack", "-e", "KEEL_AGENT_BIN_DIR=" + BinDir,
		"-v", DockerSock + ":" + DockerSock,
		"-v", p.DataDir + ":" + p.DataDir,
		// 읽기·쓰기: 대시보드의 서버 설정이 env 파일을 고친다(internal/selfhost/envfile.go).
		"-v", envDir + ":" + envDir,
		"-v", volumes + ":" + volumes,
		"-v", "/root/.docker:/root/.docker", // buildx builder(keel-railpack)와 docker 설정
		// 빌드 이미지의 keel.managed=true 라벨을 덮어쓴다: reconcile·이벤트 감시가 이 컨테이너를 앱으로 보지 않게.
		"--label", "keel.managed=false", "--label", LabelSelf + "=server",
		"--stop-timeout", "30",
		"--log-opt", "max-size=50m", "--log-opt", "max-file=5",
		"--health-cmd", "aven-server selfhost health",
		"--health-interval", "15s", "--health-timeout", "5s", "--health-retries", "3",
		"--health-start-period", "3m", "--health-start-interval", "2s",
		image}
}

// UpgradeOpts는 upgrader가 할 일이다.
type UpgradeOpts struct {
	Image      string // 새 이미지
	Deployment string // self 서비스 배포 ID
	Previous   string // 교체 전 현재 배포 ID(되돌릴 때 다시 현재로 둔다)
	Paths      Paths
	Timeout    time.Duration // 새 aven-server가 healthy가 될 때까지 기다리는 시간
}

// UpgraderArgs는 aven-server(이전 버전)가 upgrader 컨테이너를 띄우는 docker run 인자다.
func UpgraderArgs(o UpgradeOpts) []string {
	p := o.Paths.withDefaults()
	envDir := filepath.Dir(p.EnvFile)
	return []string{"run", "-d", "--name", UpgraderName,
		"--network", "host",
		"--label", "keel.managed=false", "--label", LabelSelf + "=upgrader",
		"-v", DockerSock + ":" + DockerSock,
		"-v", p.DataDir + ":" + p.DataDir,
		"-v", envDir + ":" + envDir + ":ro",
		o.Image, "aven-server", "selfhost", "upgrade",
		"--image", o.Image, "--deployment", o.Deployment, "--previous", o.Previous,
		"--data", p.DataDir, "--env-file", p.EnvFile, "--docker-root", p.DockerRoot}
}

// Result는 upgrader가 되돌렸을 때 남기는 기록이다. 되살아난 이전 aven-server가 시작할 때 읽어 배포를 실패로 끝낸다.
type Result struct {
	Deployment string    `json:"deployment"`
	Previous   string    `json:"previous"`
	Image      string    `json:"image"`
	Error      string    `json:"error"`
	Log        []string  `json:"log"`
	At         time.Time `json:"at"`
}

func resultPath(dataDir string) string { return filepath.Join(dataDir, resultFile) }

func WriteResult(dataDir string, r Result) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := resultPath(dataDir) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, resultPath(dataDir))
}

// TakeResult는 남은 결과를 읽고 지운다(없으면 nil).
func TakeResult(dataDir string) (*Result, error) {
	b, err := os.ReadFile(resultPath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	os.Remove(resultPath(dataDir))
	r := &Result{}
	if err := json.Unmarshal(b, r); err != nil {
		return nil, fmt.Errorf("%s: %w", resultFile, err)
	}
	return r, nil
}

// ---------------- docker ----------------

func docker(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return s, fmt.Errorf("docker %s: %w: %s", args[0], err, s)
	}
	return s, nil
}

// state는 컨테이너 상태와 헬스체크 상태다("" ""면 없음).
func state(ctx context.Context, name string) (status, health string) {
	out, err := docker(ctx, "inspect", "--format", "{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}}", name)
	if err != nil {
		return "", ""
	}
	status, health, _ = strings.Cut(out+" ", " ")
	return status, strings.TrimSpace(health)
}

// DockerRoot는 docker 데이터 루트다.
func DockerRoot(ctx context.Context) string {
	if out, err := docker(ctx, "info", "--format", "{{.DockerRootDir}}"); err == nil && out != "" {
		return out
	}
	return "/var/lib/docker"
}

// waitHealthy는 컨테이너가 healthy가 될 때까지 기다린다. 끝나 버리거나 unhealthy면 바로 실패한다.
func waitHealthy(ctx context.Context, name string, timeout time.Duration, logf func(string, ...any)) error {
	deadline := time.Now().Add(timeout)
	last := ""
	for {
		st, h := state(ctx, name)
		if st+h != last {
			logf("%s: %s %s", name, st, h)
			last = st + h
		}
		switch {
		case st == "running" && h == "healthy":
			return nil
		case st == "":
			return fmt.Errorf("%s가 사라졌습니다", name)
		case st == "exited" || st == "dead":
			return fmt.Errorf("%s가 종료됐습니다", name)
		case h == "unhealthy":
			return fmt.Errorf("%s 헬스체크 실패", name)
		case time.Now().After(deadline):
			return fmt.Errorf("%s가 %s 안에 healthy가 되지 않았습니다 (%s %s)", name, timeout, st, h)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// Upgrade는 upgrader 컨테이너 안에서 aven-server를 새 이미지로 바꾼다. 실패하면 이전 컨테이너를 되살리고 결과를 남긴다.
func Upgrade(ctx context.Context, o UpgradeOpts, logf func(string, ...any)) error {
	if o.Timeout == 0 {
		o.Timeout = 4 * time.Minute
	}
	if st, _ := state(ctx, ContainerName); st != "running" {
		return fmt.Errorf("%s 컨테이너가 돌고 있지 않습니다(%q): 아무것도 바꾸지 않았습니다", ContainerName, st)
	}
	if _, err := docker(ctx, "image", "inspect", "--format", "{{.Id}}", o.Image); err != nil {
		return fmt.Errorf("새 이미지 %s가 없습니다: %w", o.Image, err)
	}
	docker(ctx, "rm", "-f", PrevName)
	if _, err := docker(ctx, "rename", ContainerName, PrevName); err != nil {
		return err
	}
	logf("이전 aven-server를 멈춥니다")
	if _, err := docker(ctx, "stop", "-t", "30", PrevName); err != nil {
		docker(ctx, "rename", PrevName, ContainerName)
		docker(ctx, "start", ContainerName)
		return err
	}
	rollback := func(cause error) error {
		logf("되돌립니다: %v", cause)
		var tail []string
		if out, err := docker(ctx, "logs", "--tail", "40", ContainerName); err == nil && out != "" {
			tail = strings.Split(out, "\n")
		}
		docker(ctx, "rm", "-f", ContainerName)
		// 결과를 먼저 남긴다: 되살아난 이전 aven-server가 시작하면서 읽는다.
		if err := WriteResult(o.Paths.withDefaults().DataDir, Result{Deployment: o.Deployment, Previous: o.Previous, Image: o.Image,
			Error: cause.Error(), Log: tail, At: time.Now().UTC()}); err != nil {
			logf("WARN: 결과를 남기지 못했습니다: %v", err)
		}
		if _, err := docker(ctx, "rename", PrevName, ContainerName); err != nil {
			return fmt.Errorf("%v; 되돌리기 실패(rename): %w", cause, err)
		}
		if _, err := docker(ctx, "start", ContainerName); err != nil {
			return fmt.Errorf("%v; 되돌리기 실패(start): %w", cause, err)
		}
		if err := waitHealthy(ctx, ContainerName, o.Timeout, logf); err != nil {
			logf("WARN: 이전 aven-server도 healthy가 아닙니다: %v", err)
		}
		return cause
	}
	logf("새 aven-server를 띄웁니다: %s", o.Image)
	if _, err := docker(ctx, RunArgs(o.Image, o.Paths)...); err != nil {
		return rollback(err)
	}
	if err := waitHealthy(ctx, ContainerName, o.Timeout, logf); err != nil {
		return rollback(err)
	}
	docker(ctx, "rm", "-f", PrevName)
	logf("교체 완료")
	return nil
}

// BootstrapOpts는 systemd aven-server를 컨테이너로 옮길 때 쓸 값이다.
type BootstrapOpts struct {
	Image   string
	Unit    string // 대신할 systemd 유닛(비우면 없음)
	Paths   Paths
	Timeout time.Duration
	// Deployment/Previous는 대시보드 배포로 시작했을 때 채운다. 실패하면 결과를 남겨 다시 시작한 systemd 서버가 배포를 실패로 끝낸다.
	Deployment string
	Previous   string
}

// Bootstrap은 호스트에서 systemd aven-server를 self 컨테이너로 처음 옮긴다(root로 실행).
// 실패하면 컨테이너를 지우고 systemd 서비스를 다시 시작한다. 성공하면 systemd 유닛을 비활성화한다(비상 복구용으로 남는다).
func Bootstrap(ctx context.Context, o BootstrapOpts, logf func(string, ...any)) error {
	image, unit, p, timeout := o.Image, o.Unit, o.Paths, o.Timeout
	if timeout == 0 {
		timeout = 4 * time.Minute
	}
	if err := CheckEnvFile(p.withDefaults().EnvFile); err != nil {
		return err
	}
	if st, _ := state(ctx, ContainerName); st != "" {
		return fmt.Errorf("%s 컨테이너가 이미 있습니다(%s)", ContainerName, st)
	}
	if _, err := docker(ctx, "image", "inspect", "--format", "{{.Id}}", image); err != nil {
		return fmt.Errorf("이미지 %s가 없습니다: %w", image, err)
	}
	systemctl := func(args ...string) error {
		out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if unit != "" {
		logf("systemd %s를 멈춥니다", unit)
		if err := systemctl("stop", unit); err != nil {
			return err
		}
	}
	fail := func(cause error) error {
		logf("실패: %v", cause)
		var tail []string
		if out, err := docker(ctx, "logs", "--tail", "40", ContainerName); err == nil {
			logf("%s", out)
			if out != "" {
				tail = strings.Split(out, "\n")
			}
		}
		docker(ctx, "rm", "-f", ContainerName)
		// 대시보드에서 고친 설정 때문에 실패했을 수 있다: systemd 서버가 다시 뜨도록 마지막 정상 설정으로 되돌린다.
		if restored, err := RestoreGoodEnv(p.withDefaults().DataDir, p.withDefaults().EnvFile); err != nil {
			logf("WARN: 서버 설정을 되돌리지 못했습니다: %v", err)
		} else if restored {
			logf("서버 설정(%s)을 마지막으로 정상 동작한 내용으로 되돌렸습니다", p.withDefaults().EnvFile)
		}
		if o.Deployment != "" {
			if err := WriteResult(p.withDefaults().DataDir, Result{Deployment: o.Deployment, Previous: o.Previous, Image: image,
				Error: cause.Error(), Log: tail, At: time.Now().UTC()}); err != nil {
				logf("WARN: 결과를 남기지 못했습니다: %v", err)
			}
		}
		if unit != "" {
			if err := systemctl("start", unit); err != nil {
				return fmt.Errorf("%v; systemd %s 재시작 실패: %w", cause, unit, err)
			}
			logf("systemd %s를 다시 시작했습니다", unit)
		}
		return cause
	}
	logf("aven-server 컨테이너를 띄웁니다: %s", image)
	if _, err := docker(ctx, RunArgs(image, p)...); err != nil {
		return fail(err)
	}
	if err := waitHealthy(ctx, ContainerName, timeout, logf); err != nil {
		return fail(err)
	}
	if unit != "" {
		if err := systemctl("disable", unit); err != nil {
			logf("WARN: %v", err)
		}
	}
	logf("완료: aven-server가 컨테이너로 돕니다")
	return nil
}

// CheckEnvFile은 env 파일을 docker --env-file로 읽어도 systemd와 같은 값이 되는지 본다.
// docker는 따옴표를 벗기지 않으므로 따옴표로 감싼 값이 있으면 고치라고 알린다.
func CheckEnvFile(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("env 파일 %s: %w", path, err)
	}
	var quoted []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			quoted = append(quoted, strings.TrimSpace(k))
		}
	}
	if len(quoted) > 0 {
		return fmt.Errorf("env 파일 %s의 값에서 따옴표를 빼야 합니다(컨테이너는 docker --env-file로 읽어 따옴표까지 값으로 씁니다): %s", path, strings.Join(quoted, ", "))
	}
	return nil
}

// SystemdUnit은 이 프로세스가 도는 systemd 서비스 유닛 이름이다(systemd 밖이면 "").
func SystemdUnit() string {
	if os.Getenv("INVOCATION_ID") == "" {
		return ""
	}
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	return unitFromCgroup(string(b))
}

func unitFromCgroup(s string) string {
	for _, line := range strings.Split(s, "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		segs := strings.Split(parts[2], "/")
		for i := len(segs) - 1; i >= 0; i-- {
			if strings.HasSuffix(segs[i], ".service") {
				return strings.TrimSuffix(segs[i], ".service")
			}
		}
	}
	return ""
}

// ---------------- health ----------------

// Health는 KEEL_LISTEN의 /healthz를 확인한다(컨테이너 HEALTHCHECK).
func Health(ctx context.Context) error {
	listen := os.Getenv("KEEL_LISTEN")
	if listen == "" {
		listen = "[::1]:7070"
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return err
	}
	if host == "" || host == "::" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	scheme := "http"
	client := &http.Client{Timeout: 4 * time.Second}
	if os.Getenv("KEEL_TLS_CERT") != "" {
		scheme = "https"
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} // 자기 자신(로컬 주소)만 확인한다
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+net.JoinHostPort(host, port)+"/healthz", nil)
	if err != nil {
		return err
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("/healthz: %s", res.Status)
	}
	return nil
}

// ---------------- CLI ----------------

// Main은 `aven-server selfhost <upgrade|bootstrap|health|run-args>`다.
func Main(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: aven-server selfhost upgrade|bootstrap|health|run-args [flags]")
		return 2
	}
	logf := func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, "%s %s\n", time.Now().UTC().Format("15:04:05"), fmt.Sprintf(format, a...))
	}
	ctx := context.Background()
	fs := flag.NewFlagSet("selfhost "+args[0], flag.ExitOnError)
	image := fs.String("image", "", "aven-server image")
	dep := fs.String("deployment", "", "self service deployment id")
	prev := fs.String("previous", "", "previous current deployment id")
	data := fs.String("data", envOr("KEEL_DATA", "/var/lib/keel"), "KEEL_DATA")
	envFile := fs.String("env-file", DefaultEnvFile, "env file passed to the container")
	dockerRoot := fs.String("docker-root", "", "docker data root (default: docker info)")
	unit := fs.String("systemd-unit", DefaultUnit, "bootstrap: systemd unit to replace (empty: none)")
	timeout := fs.Duration("timeout", 4*time.Minute, "health wait")
	fs.Parse(args[1:])
	paths := func() Paths {
		root := *dockerRoot
		if root == "" {
			root = DockerRoot(ctx)
		}
		return Paths{DataDir: *data, EnvFile: *envFile, DockerRoot: root}
	}
	var err error
	switch args[0] {
	case "health":
		hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		err = Health(hctx)
	case "run-args":
		if *image == "" {
			err = errors.New("--image is required")
			break
		}
		var b bytes.Buffer
		for _, a := range RunArgs(*image, paths()) {
			fmt.Fprintf(&b, "%q ", a)
		}
		fmt.Println("docker " + strings.TrimSpace(b.String()))
	case "upgrade":
		if *image == "" || *dep == "" {
			err = errors.New("--image and --deployment are required")
			break
		}
		err = Upgrade(ctx, UpgradeOpts{Image: *image, Deployment: *dep, Previous: *prev, Paths: paths(), Timeout: *timeout}, logf)
	case "bootstrap":
		if *image == "" {
			err = errors.New("--image is required")
			break
		}
		err = Bootstrap(ctx, BootstrapOpts{Image: *image, Unit: *unit, Paths: paths(), Timeout: *timeout, Deployment: *dep, Previous: *prev}, logf)
	default:
		err = fmt.Errorf("unknown selfhost command %q", args[0])
	}
	if err != nil {
		logf("ERROR: %v", err)
		return 1
	}
	return 0
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
