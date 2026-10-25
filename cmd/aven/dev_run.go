package main

// aven dev의 실행 부분: 프로세스/컨테이너 계획, 로그 다중화, 준비 대기, 정상 종료, aven dev down/clean.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 로컬 Docker 객체(컨테이너, 네트워크, 볼륨)에 붙이는 라벨. aven dev down/clean이 이것으로 찾는다.
const (
	devLabelProject = "keel.dev.project"
	devLabelEnv     = "keel.dev.environment"
	devLabelService = "keel.dev.service"
)

type devProcessSpec struct {
	service       string
	program       string
	args          []string
	dir           string
	env           []string
	describe      string
	container     bool
	database      bool
	containerName string
	image         string
	localPort     int
	// labels는 네트워크/볼륨을 만들 때 붙이는 라벨이다(aven.dev.project, keel.dev.environment).
	labels  []string
	volumes []string
}

func devVolumeName(id string) string { return "keel-dev-" + strings.ReplaceAll(id, "_", "-") }

// devDockerCLIKey는 docker CLI 자신이 읽는 환경 변수다. 이런 변수를 CLI 환경에 덮으면 CLI가 다른 데몬/설정을 쓰게 되므로
// 컨테이너에는 -e KEY=VALUE로 넘기고 CLI 환경은 그대로 둔다.
func devDockerCLIKey(k string) bool {
	u := strings.ToUpper(k)
	return u == "HOME" || u == "PATH" || u == "USERPROFILE" || u == "APPDATA" || strings.HasPrefix(u, "DOCKER_")
}

func devProcessPlan(base string, l *link, services []*devService, docker, network string) ([]devProcessSpec, error) {
	plan := make([]devProcessSpec, 0, len(services))
	windows := runtime.GOOS == "windows"
	for _, svc := range services {
		sl := *l
		sl.ServiceID, sl.Service = svc.service.ID, svc.service.Name
		if !svc.container() {
			dir, err := devWorkingDir(base, svc.dir, svc.dir != svc.instance.RootDir)
			if err != nil {
				return nil, fmt.Errorf("service %s: %w", svc.service.Name, err)
			}
			program, args := "/bin/sh", []string{"-lc", svc.command}
			if windows {
				program = os.Getenv("COMSPEC")
				if program == "" {
					program = "cmd.exe"
				}
				args = []string{"/C", svc.command}
			}
			rel, err := filepath.Rel(base, dir)
			if err != nil || strings.HasPrefix(rel, "..") {
				rel = dir
			}
			plan = append(plan, devProcessSpec{
				service: svc.service.Name, program: program, args: args, dir: dir,
				env:      runEnv(os.Environ(), svc.vars, &sl, windows),
				describe: fmt.Sprintf("%s (in %s)", svc.command, rel), localPort: svc.localPort,
			})
			continue
		}
		if svc.localPort <= 0 || svc.containerPort <= 0 {
			return nil, fmt.Errorf("service %s has an invalid port mapping", svc.service.Name)
		}
		image := strPtrValue(svc.instance.Image)
		name := fmt.Sprintf("keel-dev-%s-%s-%d", devSlug(l.Environment), devSlug(svc.service.Name), os.Getpid())
		labels := []string{devLabelProject + "=" + l.ProjectID, devLabelEnv + "=" + l.EnvironmentID}
		args := []string{
			"run", "--rm", "--name", name, "--stop-timeout", "10",
			"--network", network, "--network-alias", devPrivateDomain(svc, l.Environment), "--network-alias", svc.service.Name,
			"--add-host", "host.docker.internal:host-gateway",
			"-p", fmt.Sprintf("127.0.0.1:%d:%d/tcp", svc.localPort, svc.containerPort),
		}
		for _, lb := range append(labels, devLabelService+"="+svc.service.ID) {
			args = append(args, "--label", lb)
		}
		start := strPtrValue(svc.instance.StartCommand)
		if start != "" {
			args = append(args, "--entrypoint", "/bin/sh") // 서버 배포와 같다(deploy.runSpecNamed)
		}
		var vols []string
		for _, v := range svc.volumes {
			vols = append(vols, devVolumeName(v.ID))
			args = append(args, "--mount", "type=volume,source="+devVolumeName(v.ID)+",target="+v.MountPath)
		}
		keys := make([]string, 0, len(svc.containerVars))
		for k := range svc.containerVars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		cliEnv := map[string]string{}
		for _, k := range keys {
			if devDockerCLIKey(k) {
				args = append(args, "-e", k+"="+svc.containerVars[k])
				continue
			}
			// 값은 docker CLI의 환경에서 복사된다. 비밀 값이 argv(ps)에 드러나지 않는다.
			args = append(args, "-e", k)
			cliEnv[k] = svc.containerVars[k]
		}
		args = append(args, image)
		if start != "" {
			args = append(args, "-c", start)
		}
		plan = append(plan, devProcessSpec{
			service: svc.service.Name, program: docker, args: args,
			env:       runEnv(os.Environ(), cliEnv, &sl, windows),
			describe:  image,
			container: true, database: svc.service.SourceType == "database",
			containerName: name, image: image, localPort: svc.localPort, labels: labels, volumes: vols,
		})
	}
	return plan, nil
}

// printDevPlan은 --dry-run 출력이다. 변수 값은 보이지 않는다(-e KEY=VALUE는 값을 가린다).
func printDevPlan(w io.Writer, plan []devProcessSpec) {
	for _, s := range plan {
		fmt.Fprintf(w, "# %s\n", s.service)
		if s.dir != "" {
			fmt.Fprintf(w, "cd %s && ", shellQuote(s.dir))
		}
		parts := []string{shellQuote(s.program)}
		for i, a := range s.args {
			if i > 0 && s.args[i-1] == "-e" && strings.Contains(a, "=") {
				k, _, _ := strings.Cut(a, "=")
				a = k + "=***"
			}
			parts = append(parts, shellQuote(a))
		}
		fmt.Fprintln(w, strings.Join(parts, " "))
	}
}

func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=,@+%", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ---- 로그 ----

var devColors = []string{"36", "33", "32", "35", "34", "31", "96", "93", "92", "95"}

// devLogMux는 여러 서비스의 출력을 "<service> | line" 줄 단위로 섞는다. 이름은 같은 폭으로 맞춘다.
type devLogMux struct {
	mu     sync.Mutex
	out    io.Writer
	width  int
	colors map[string]string
}

func newDevLogMux(out io.Writer, plan []devProcessSpec, color bool) *devLogMux {
	m := &devLogMux{out: out}
	if color {
		m.colors = map[string]string{}
	}
	for i, s := range plan {
		m.width = max(m.width, len(s.service))
		if color {
			m.colors[s.service] = devColors[i%len(devColors)]
		}
	}
	return m
}

func (m *devLogMux) line(service, line string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := fmt.Sprintf("%-*s |", m.width, service)
	if c, ok := m.colors[service]; ok {
		prefix = "\x1b[" + c + "m" + prefix + "\x1b[0m"
	}
	_, err := fmt.Fprintf(m.out, "%s %s\n", prefix, strings.TrimSuffix(line, "\r"))
	return err
}

type devPrefixWriter struct {
	mu      sync.Mutex
	mux     *devLogMux
	service string
	buffer  []byte
}

func (w *devPrefixWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buffer = append(w.buffer, p...)
	for {
		i := bytes.IndexByte(w.buffer, '\n')
		if i < 0 {
			break
		}
		if err := w.mux.line(w.service, string(w.buffer[:i])); err != nil {
			return n, err
		}
		w.buffer = w.buffer[i+1:]
	}
	// 줄바꿈 없이 아주 긴 출력은 잘라서 내보낸다.
	if len(w.buffer) > 64*1024 {
		err := w.mux.line(w.service, string(w.buffer))
		w.buffer = nil
		return n, err
	}
	return n, nil
}

func (w *devPrefixWriter) flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buffer) == 0 {
		return nil
	}
	err := w.mux.line(w.service, string(w.buffer))
	w.buffer = nil
	return err
}

// ---- 프로세스 ----

type managedDevProcess struct {
	spec   devProcessSpec
	cmd    *exec.Cmd
	done   chan struct{}
	result error
}

func (p *managedDevProcess) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func startDevProcess(spec devProcessSpec, mux *devLogMux) (*managedDevProcess, error) {
	cmd := exec.Command(spec.program, spec.args...)
	cmd.Dir, cmd.Env = spec.dir, spec.env
	configureDevProcess(cmd)
	out := &devPrefixWriter{mux: mux, service: spec.service}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", spec.service, err)
	}
	p := &managedDevProcess{spec: spec, cmd: cmd, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		_ = out.flush()
		p.result = err
		if err != nil {
			_ = mux.line(spec.service, "aven dev: process exited: "+err.Error())
		} else {
			_ = mux.line(spec.service, "aven dev: process exited")
		}
		close(p.done)
	}()
	return p, nil
}

// devPortReady는 포트에서 앱이 연결을 받는지 본다. docker의 포트 프록시는 컨테이너 안 앱이 아직 듣지 않아도 연결을 받고
// 바로 끊으므로, 연결 뒤 곧바로 닫히면(EOF/RST) 아직 준비되지 않은 것으로 본다. 데이터가 오거나 읽기가 시간 초과면 준비된 것이다.
func devPortReady(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 300*time.Millisecond)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	var b [1]byte
	n, err := conn.Read(b[:])
	if n > 0 {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

var errDevNotReady = errors.New("not ready")

// waitDevReady는 컨테이너가 로컬 포트에서 연결을 받을 때까지 기다린다.
func waitDevReady(ctx context.Context, p *managedDevProcess, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		if devPortReady(p.spec.localPort) {
			return nil
		}
		select {
		case <-p.done:
			return fmt.Errorf("exited before it was ready")
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errDevNotReady
		case <-tick.C:
		}
	}
}

// prepareDocker는 임시 네트워크와 볼륨을 만들고 없는 이미지를 받는다. 네트워크를 만들었으면 true다.
func prepareDocker(ctx context.Context, plan []devProcessSpec, docker, network string, mux *devLogMux) (bool, error) {
	var first *devProcessSpec
	for i := range plan {
		if plan[i].container {
			first = &plan[i]
			break
		}
	}
	if first == nil {
		return false, nil
	}
	args := []string{"network", "create"}
	for _, lb := range first.labels {
		args = append(args, "--label", lb)
	}
	if out, err := exec.CommandContext(ctx, docker, append(args, network)...).CombinedOutput(); err != nil {
		return false, fmt.Errorf("create local Docker network: %s", strings.TrimSpace(string(out)))
	}
	for _, s := range plan {
		if !s.container {
			continue
		}
		for _, v := range s.volumes {
			args := []string{"volume", "create"}
			for _, lb := range s.labels {
				args = append(args, "--label", lb)
			}
			if out, err := exec.CommandContext(ctx, docker, append(args, v)...).CombinedOutput(); err != nil {
				return true, fmt.Errorf("create local Docker volume %s: %s", v, strings.TrimSpace(string(out)))
			}
		}
		if exec.CommandContext(ctx, docker, "image", "inspect", s.image).Run() == nil {
			continue
		}
		_ = mux.line(s.service, "aven dev: pulling "+s.image)
		pull := exec.CommandContext(ctx, docker, "pull", "--quiet", s.image)
		w := &devPrefixWriter{mux: mux, service: s.service}
		pull.Stdout, pull.Stderr = w, w
		err := pull.Run()
		_ = w.flush()
		if err != nil {
			if ctx.Err() != nil {
				return true, ctx.Err()
			}
			return true, fmt.Errorf("pull %s for %s: %w", s.image, s.service, err)
		}
	}
	return true, nil
}

// runDevSession은 Ctrl-C(SIGINT/SIGTERM)로 멈추는 세션을 돌린다. 두 번째 Ctrl-C는 기다리지 않고 강제로 끝낸다.
func runDevSession(plan []devProcessSpec, docker, network string, out *os.File) error {
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, devSignals()...)
	defer signal.Stop(sig)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	force := make(chan struct{})
	go func() {
		if _, ok := <-sig; !ok {
			return
		}
		fmt.Fprintln(os.Stderr, "\nkeel dev: stopping services (press Ctrl-C again to force)")
		cancel()
		if _, ok := <-sig; ok {
			close(force)
		}
	}()
	return runDevProcesses(ctx, plan, docker, network, out, isTerminal(out) && os.Getenv("NO_COLOR") == "", force)
}

// runDevProcesses는 database → image → repo 순서로 띄우고 ctx가 끝나거나 모두 끝날 때까지 기다린다.
// 한 서비스가 끝나도 나머지는 계속 돈다. ctx로 멈췄으면 nil이다.
func runDevProcesses(ctx context.Context, plan []devProcessSpec, docker, network string, output io.Writer, color bool, force <-chan struct{}) error {
	mux := newDevLogMux(output, plan, color)
	var processes []*managedDevProcess
	networkCreated, err := prepareDocker(ctx, plan, docker, network, mux)
	shutdown := func() {
		stopDevProcesses(processes, docker, force)
		if networkCreated {
			_ = exec.Command(docker, "network", "rm", network).Run()
		}
	}
	if err != nil {
		shutdown()
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	stages := []func(devProcessSpec) bool{
		func(s devProcessSpec) bool { return s.database },
		func(s devProcessSpec) bool { return s.container && !s.database },
		func(s devProcessSpec) bool { return !s.container },
	}
	for _, inStage := range stages {
		var started []*managedDevProcess
		for _, spec := range plan {
			if !inStage(spec) {
				continue
			}
			if ctx.Err() != nil {
				shutdown()
				return nil
			}
			p, err := startDevProcess(spec, mux)
			if err != nil {
				shutdown()
				return err
			}
			processes = append(processes, p)
			started = append(started, p)
		}
		// 같은 단계의 컨테이너는 함께 띄우고, 다음 단계 전에 포트가 열릴 때까지 기다린다(시간 초과는 경고만 한다).
		for _, p := range started {
			if !p.spec.container {
				continue
			}
			switch err := waitDevReady(ctx, p, 60*time.Second); {
			case err == nil:
				_ = mux.line(p.spec.service, fmt.Sprintf("aven dev: ready on localhost:%d", p.spec.localPort))
			case ctx.Err() != nil:
				shutdown()
				return nil
			case errors.Is(err, errDevNotReady):
				_ = mux.line(p.spec.service, fmt.Sprintf("aven dev: not accepting connections on localhost:%d yet; continuing", p.spec.localPort))
			default:
				_ = mux.line(p.spec.service, "aven dev: "+err.Error())
			}
		}
	}
	allDone := make(chan struct{})
	go func() {
		for _, p := range processes {
			<-p.done
		}
		close(allDone)
	}()
	select {
	case <-ctx.Done():
		shutdown()
		return nil
	case <-allDone:
		shutdown()
		var failed []string
		for _, p := range processes {
			if p.result != nil {
				failed = append(failed, p.spec.service)
			}
		}
		if len(failed) > 0 {
			return fmt.Errorf("all services exited (failed: %s)", strings.Join(failed, ", "))
		}
		return nil
	}
}

// stopDevProcesses는 로컬 프로세스 그룹에 SIGTERM을, 컨테이너에 docker stop을 보내고 끝날 때까지(최대 15초) 기다린다.
// 시간이 지나거나 force가 닫히면 강제로 끝낸다(SIGKILL, docker rm -f).
func stopDevProcesses(processes []*managedDevProcess, docker string, force <-chan struct{}) {
	if len(processes) == 0 {
		return
	}
	stopCtx, cancelStops := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelStops()
	for _, p := range processes {
		if p.exited() {
			continue
		}
		if p.spec.container {
			go func(name string) {
				_ = exec.CommandContext(stopCtx, docker, "stop", "--time", "10", name).Run()
			}(p.spec.containerName)
		} else {
			_ = terminateDevProcess(p.cmd)
		}
	}
	allDone := make(chan struct{})
	go func() {
		for _, p := range processes {
			<-p.done
		}
		close(allDone)
	}()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case <-allDone:
		return
	case <-timer.C:
	case <-force:
	}
	for _, p := range processes {
		if p.exited() {
			continue
		}
		_ = killDevProcess(p.cmd)
		if p.spec.container {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = exec.CommandContext(ctx, docker, "rm", "-f", p.spec.containerName).Run()
			cancel()
		}
	}
	select {
	case <-allDone:
	case <-time.After(3 * time.Second):
	}
}

// ---- Docker 도우미 ----

func devDocker() (string, error) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		return "", errors.New("Docker is required for image and database services (install Docker and start its daemon)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, docker, "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		return "", fmt.Errorf("Docker is not ready: %s", strings.TrimSpace(string(out)))
	}
	return docker, nil
}

func devFilters(projectID, envID string) []string {
	f := []string{"--filter", "label=" + devLabelProject + "=" + projectID}
	if envID != "" {
		f = append(f, "--filter", "label="+devLabelEnv+"="+envID)
	}
	return f
}

func devDockerList(docker string, args ...string) ([]string, error) {
	out, err := exec.Command(docker, args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("docker %s: %s", args[0], strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

// devLeftovers는 이 프로젝트/환경에서 아직 도는 aven dev 컨테이너 수다.
func devLeftovers(docker, projectID, envID string) int {
	ids, _ := devDockerList(docker, append([]string{"ps", "-q"}, devFilters(projectID, envID)...)...)
	return len(ids)
}

// devDown은 aven dev가 만든 컨테이너와 네트워크(volumes면 볼륨까지)를 지운다. envID가 비면 프로젝트 전체다.
func devDown(docker, projectID, envID string, volumes bool, w io.Writer) error {
	filters := devFilters(projectID, envID)
	ids, err := devDockerList(docker, append([]string{"ps", "-aq"}, filters...)...)
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		_ = exec.Command(docker, append([]string{"stop", "--time", "10"}, ids...)...).Run()
		_ = exec.Command(docker, append([]string{"rm", "-f"}, ids...)...).Run()
	}
	fmt.Fprintf(w, "aven dev: stopped %d container(s)\n", len(ids))
	nets, err := devDockerList(docker, append([]string{"network", "ls", "-q"}, filters...)...)
	if err != nil {
		return err
	}
	for _, n := range nets {
		_ = exec.Command(docker, "network", "rm", n).Run()
	}
	if !volumes {
		return nil
	}
	vols, err := devDockerList(docker, append([]string{"volume", "ls", "-q"}, filters...)...)
	if err != nil {
		return err
	}
	if len(vols) > 0 {
		if out, err := exec.Command(docker, append([]string{"volume", "rm"}, vols...)...).CombinedOutput(); err != nil {
			return fmt.Errorf("remove volumes: %s", strings.TrimSpace(string(out)))
		}
	}
	fmt.Fprintf(w, "aven dev: removed %d volume(s)\n", len(vols))
	return nil
}
