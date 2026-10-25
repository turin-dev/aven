package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/events"
	"github.com/turin-dev/aven/internal/logbook"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/selfhost"
	"github.com/turin-dev/aven/internal/store"
)

// self 서비스(KEEL_SELF_SERVICE): Aven이 자기 자신(aven-server)을 빌드하고 배포한다(docs/selfhost.md).
// 빌드까지는 보통 서비스와 같고, 컨테이너를 띄우는 대신 aven-selfupgrade 컨테이너가 aven-server 컨테이너를 교체한다.
// 이 배포는 컨테이너를 갖지 않으므로 reconcile·scale·sleep 대상이 아니다.

// selfOtherWait는 교체 전에 다른 배포가 끝나기를 기다리는 최대 시간이다(교체하면 진행 중인 배포는 실패 처리된다).
var selfOtherWait = 15 * time.Minute

// selfDocker는 upgrader 컨테이너를 다루는 docker 명령이다(테스트에서 바꾼다).
type selfDocker interface {
	Run(ctx context.Context, args []string) error
	Wait(ctx context.Context, name string) (int, error)
	TailLogs(ctx context.Context, name string, n int) []string
	Remove(ctx context.Context, name string, grace int) error
	// Host는 호스트 명령(systemd-run, systemctl, journalctl)을 실행한다. Unit은 이 프로세스의 systemd 유닛이다(밖이면 "").
	Host(ctx context.Context, name string, args ...string) (string, error)
	Unit() string
}

type hostSelfDocker struct{ *node.Docker }

func (h hostSelfDocker) Run(ctx context.Context, args []string) error {
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (hostSelfDocker) Host(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return s, fmt.Errorf("%s: %w: %s", name, err, s)
	}
	return s, nil
}

func (hostSelfDocker) Unit() string { return selfhost.SystemdUnit() }

func (m *Manager) selfDocker() selfDocker {
	if m.selfDrv != nil {
		return m.selfDrv
	}
	return hostSelfDocker{m.Docker}
}

func (m *Manager) isSelf(t *Target) bool {
	return m.IsSelfService(t.Service.ID)
}

// SettingSelfService는 대시보드에서 정한 self 서비스 ID를 담는 settings 키다(KEEL_SELF_SERVICE가 있으면 그 값이 우선한다).
const SettingSelfService = "self_service"

// SelfServiceID는 aven-server 자신을 배포하는 서비스 ID다(없으면 "").
func (m *Manager) SelfServiceID() string {
	if m.Cfg.SelfServiceID != "" {
		return m.Cfg.SelfServiceID
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.selfID
}

// SelfServiceFromEnv는 self 서비스를 KEEL_SELF_SERVICE로 정했는지 알려준다(그러면 대시보드에서 바꿀 수 없다).
func (m *Manager) SelfServiceFromEnv() bool { return m.Cfg.SelfServiceID != "" }

// SetSelfService는 self 서비스를 정하거나(id) 해제한다(""). DB에 저장하므로 재시작해도 유지된다.
func (m *Manager) SetSelfService(ctx context.Context, id string) error {
	if m.SelfServiceFromEnv() {
		return errors.New("self 서비스가 KEEL_SELF_SERVICE로 정해져 있어 대시보드에서 바꿀 수 없습니다")
	}
	var err error
	if id == "" {
		err = m.Store.DeleteSetting(ctx, SettingSelfService)
	} else {
		err = m.Store.SetSetting(ctx, SettingSelfService, id)
	}
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.selfID = id
	m.mu.Unlock()
	return nil
}

func (m *Manager) loadSelfService(ctx context.Context) {
	if v, err := m.Store.GetSetting(ctx, SettingSelfService); err == nil {
		m.mu.Lock()
		m.selfID = v
		m.mu.Unlock()
	}
}

// SelfEnvFile은 aven-server 컨테이너에 넘기는 env 파일(서버 설정)이다.
func (m *Manager) SelfEnvFile() string { return m.selfEnvFile() }

func (m *Manager) selfEnvFile() string {
	if m.Cfg.SelfEnvFile != "" {
		return m.Cfg.SelfEnvFile
	}
	return selfhost.DefaultEnvFile
}

// IsSelfService는 서비스가 aven-server 자신을 배포하는 self 서비스인지 알려준다.
func (m *Manager) IsSelfService(serviceID string) bool {
	id := m.SelfServiceID()
	return id != "" && serviceID == id
}

// ErrSelfService는 컨테이너를 다루는 작업(중지, 재시작, scale, sleep)을 self 서비스에 하려 할 때의 오류다.
var ErrSelfService = errors.New("aven-server 자신(self 서비스)은 중지·재시작·scale·sleep할 수 없습니다. 다시 배포하거나 호스트에서 docker로 다루세요(docs/selfhost.md)")

// ErrSelfInUse는 self 서비스(또는 그것을 담은 환경·프로젝트)를 지우려 할 때의 오류다. 먼저 self 서비스 지정을 해제해야 한다.
var ErrSelfInUse = errors.New("aven-server 자신(self 서비스)이 들어 있어 지울 수 없습니다. 먼저 어드민 › Owner · 초기 설정에서 Aven 관리를 해제하세요")

// checkSelfPlacement는 self 서비스가 로컬 노드에만 배치되는지 본다(빌드 전에 확인한다).
func (m *Manager) checkSelfPlacement(d *store.Deployment) error {
	if !isLocal(d.NodeID) || len(extraPlacementNodes(d)) > 0 {
		return errors.New("aven-server 자신(self 서비스)은 aven-server가 도는 로컬 노드에 replica 1개로만 배포할 수 있습니다")
	}
	return nil
}

// selfDeploy는 이미지가 준비된 self 배포를 aven-selfupgrade에 넘긴다. 교체에 성공하면 이 프로세스는 멈추고
// 새 aven-server가 시작하면서 배포를 live로 끝낸다(finishSelfDeploy). 여기로 돌아오면 upgrader가 이전 서버를 건드리기 전에 실패한 것이다.
func (m *Manager) selfDeploy(ctx context.Context, t *Target, d *store.Deployment, b *logbook.Book) error {
	if err := m.checkSelfPlacement(d); err != nil {
		return err
	}
	sd := m.selfDocker()
	unit := ""
	if !selfhost.Active() {
		if unit = sd.Unit(); unit == "" {
			return errors.New("aven-server가 self 컨테이너로도 systemd 서비스로도 돌고 있지 않아 스스로 교체할 수 없습니다(docs/selfhost.md의 bootstrap). 이미지는 빌드됐습니다: " + *d.Image)
		}
	}
	envFile := m.selfEnvFile()
	if err := selfhost.CheckEnvFile(envFile); err != nil {
		return err
	}
	m.setStatus(ctx, d, b, store.DepDeploying)
	if err := m.waitOtherDeployments(ctx, d, b); err != nil {
		return err
	}
	prev := ""
	if cur, err := m.Store.GetInstance(ctx, t.Instance.ID); err == nil && cur.CurrentDeploymentID != nil && *cur.CurrentDeploymentID != d.ID {
		prev = *cur.CurrentDeploymentID
	}
	paths := selfhost.Paths{DataDir: m.Cfg.DataDir, EnvFile: envFile, DockerRoot: selfhost.DockerRoot(ctx)}
	if unit != "" {
		return m.selfBootstrap(ctx, d, b, unit, prev, paths)
	}
	sd.Remove(ctx, selfhost.UpgraderName, 0)
	args := selfhost.UpgraderArgs(selfhost.UpgradeOpts{Image: *d.Image, Deployment: d.ID, Previous: prev, Paths: paths})
	b.Printf("aven-selfupgrade가 aven-server를 %s로 교체합니다. 대시보드와 API가 잠시 끊깁니다", *d.Image)
	if err := sd.Run(ctx, args); err != nil {
		return fmt.Errorf("aven-selfupgrade 시작 실패: %w", err)
	}
	// 교체에 성공하면 이 프로세스가 먼저 멈추므로 Wait는 돌아오지 않는다.
	// 넘긴 뒤에는 취소해도 upgrader를 멈출 수 없으므로 취소와 무관하게 기다린다.
	code, werr := sd.Wait(context.WithoutCancel(ctx), selfhost.UpgraderName)
	for _, l := range sd.TailLogs(context.WithoutCancel(ctx), selfhost.UpgraderName, 50) {
		b.Line("  | " + l)
	}
	sd.Remove(context.WithoutCancel(ctx), selfhost.UpgraderName, 0)
	if werr != nil {
		return fmt.Errorf("aven-selfupgrade: %w", werr)
	}
	return fmt.Errorf("aven-selfupgrade가 aven-server를 바꾸지 못하고 끝났습니다 (exit %d)", code)
}

// selfBootstrapWait는 systemd에서 컨테이너로 옮기는 bootstrap 유닛을 기다리는 최대 시간이다.
var selfBootstrapWait = 10 * time.Minute

// selfBootstrap은 systemd로 도는 aven-server를 처음으로 self 컨테이너로 옮긴다. bootstrap은 이 서버의 systemd 유닛을
// 멈추므로 이 프로세스 밖의 transient 유닛(systemd-run)에서 돌린다. 성공하면 이 프로세스가 먼저 멈추고 새 컨테이너가
// 배포를 live로 끝낸다. 실패하면 bootstrap이 결과를 남기고 systemd 유닛을 다시 시작하며, 다시 시작한 서버가 배포를 실패로 끝낸다.
// 여기로 돌아오면 bootstrap이 이 서버를 멈추기 전에 실패한 것이다.
func (m *Manager) selfBootstrap(ctx context.Context, d *store.Deployment, b *logbook.Book, unit, prev string, p selfhost.Paths) error {
	sd := m.selfDocker()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	job := "aven-selfbootstrap-" + strings.TrimPrefix(d.ID, "dep_")
	args := []string{"--unit", job, "--collect", "--quiet", "--description", "aven-server self bootstrap " + d.ID,
		exe, "selfhost", "bootstrap", "--image", *d.Image, "--systemd-unit", unit, "--deployment", d.ID, "--previous", prev,
		"--data", p.DataDir, "--env-file", p.EnvFile, "--docker-root", p.DockerRoot}
	b.Printf("aven-server가 systemd 유닛 %s로 돌고 있습니다. 이번 배포로 aven-server 컨테이너로 옮깁니다(유닛은 비활성화되고 비상용으로 남습니다)", unit)
	b.Printf("bootstrap을 %s에서 시작합니다. 대시보드와 API가 잠시 끊깁니다", job)
	if _, err := sd.Host(ctx, "systemd-run", args...); err != nil {
		return fmt.Errorf("bootstrap 시작 실패: %w", err)
	}
	// 성공하면 bootstrap이 이 서버를 멈추므로 아래 루프는 끝나지 않는다. 넘긴 뒤에는 취소와 무관하게 기다린다.
	wctx := context.WithoutCancel(ctx)
	deadline := time.Now().Add(selfBootstrapWait)
	for time.Now().Before(deadline) {
		if _, err := sd.Host(wctx, "systemctl", "is-active", "--quiet", job); err != nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if out, err := sd.Host(wctx, "journalctl", "-u", job, "-n", "40", "--no-pager", "-o", "cat"); err == nil && out != "" {
		for _, l := range strings.Split(out, "\n") {
			b.Line("  | " + l)
		}
	}
	return errors.New("bootstrap이 aven-server를 컨테이너로 옮기지 못하고 끝났습니다(위 로그 참고)")
}

// waitOtherDeployments는 진행 중인 다른 배포가 끝나기를 기다린다(교체 중 서버가 멈추면 그 배포들은 실패한다).
func (m *Manager) waitOtherDeployments(ctx context.Context, d *store.Deployment, b *logbook.Book) error {
	deadline := time.Now().Add(selfOtherWait)
	told := false
	for {
		deps, err := m.Store.ListUnfinished(ctx)
		if err != nil {
			return err
		}
		var others []string
		for _, o := range deps {
			if o.ID != d.ID {
				others = append(others, o.ID)
			}
		}
		if len(others) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			b.Printf("WARN: %s가 지나도 다른 배포 %d개가 끝나지 않았습니다. 그대로 교체합니다(그 배포들은 실패 처리됩니다): %s", selfOtherWait, len(others), strings.Join(others, ", "))
			return nil
		}
		if !told {
			b.Printf("진행 중인 다른 배포 %d개가 끝나면 교체합니다: %s", len(others), strings.Join(others, ", "))
			told = true
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(5 * time.Second):
		}
	}
}

// finishSelfDeploy는 시작할 때(Init, 진행 중 배포를 실패 처리하기 전에) self 배포의 교체 결과를 반영한다.
//   - upgrader가 되돌렸으면(결과 파일) 그 배포를 실패로 끝내고 이전 배포를 다시 현재로 둔다.
//   - 지금 도는 이미지의 배포가 아직 진행 중이면 교체에 성공한 것이다: live로 끝내고 이전 배포를 removed로 둔다.
func (m *Manager) finishSelfDeploy(ctx context.Context) {
	if m.SelfServiceID() == "" {
		return
	}
	res, err := selfhost.TakeResult(m.Cfg.DataDir)
	if err != nil {
		m.Log.Warn("selfhost result", "err", err)
	}
	if res != nil {
		m.selfRolledBack(ctx, res)
	}
	img := selfhost.Image()
	if img == "" {
		return
	}
	deps, err := m.Store.ListUnfinished(ctx)
	if err != nil {
		return
	}
	for _, d := range deps {
		if d.Image == nil || *d.Image != img || (res != nil && d.ID == res.Deployment) {
			continue
		}
		t, err := m.Load(ctx, d.InstanceID)
		if err != nil || !m.isSelf(t) {
			continue
		}
		b, berr := m.Logs.Open(d.ID)
		if berr == nil {
			b.Printf("새 aven-server(%s)가 시작했습니다", img)
		}
		prev := t.Instance.CurrentDeploymentID
		// 새 서버가 이 설정으로 떴다: 다음 교체가 실패하면 되돌릴 기준으로 둔다.
		if err := selfhost.SaveGoodEnv(m.Cfg.DataDir, m.selfEnvFile()); err != nil {
			m.Log.Warn("selfhost: save env snapshot", "err", err)
		}
		m.Store.SetInstanceStatus(ctx, t.Instance.ID, store.InstOnline, &d.ID)
		m.finish(ctx, d, store.DepLive, "")
		if prev != nil && *prev != d.ID {
			m.Store.SetDeploymentStatus(ctx, *prev, store.DepRemoved)
		}
		m.emit(events.WithDeployment(ctx, d.ID), t, "deploy.live", fmt.Sprintf("%s (%s) 배포 완료%s", t.Service.Name, t.Env.Name, commitSuffix(d)))
		if berr == nil {
			b.Printf("배포 완료: aven-server가 %s로 돕니다", img)
			b.Close(string(store.DepLive))
			m.Logs.Release(d.ID)
		}
		go m.collectUpgraderLogs(d.ID, t.Service.ID)
	}
}

// selfRolledBack은 upgrader가 되돌린 self 배포를 실패로 끝낸다.
func (m *Manager) selfRolledBack(ctx context.Context, res *selfhost.Result) {
	d, err := m.Store.GetDeployment(ctx, res.Deployment)
	if err != nil {
		return
	}
	if b, err := m.Logs.Open(d.ID); err == nil {
		b.Printf("ERROR: 새 aven-server가 healthy가 되지 않아 이전 aven-server로 되돌렸습니다: %s", res.Error)
		for _, l := range res.Log {
			b.Line("  | " + l)
		}
		b.Close(string(store.DepFailed))
		m.Logs.Release(d.ID)
	}
	// 대시보드에서 고친 서버 설정 때문일 수 있다: 이전 서버가 쓰던 설정으로 env 파일을 되돌린다.
	if restored, err := selfhost.RestoreGoodEnv(m.Cfg.DataDir, m.selfEnvFile()); err != nil {
		m.Log.Warn("selfhost: restore env", "err", err)
	} else if restored {
		m.Log.Warn("selfhost: restored the server settings that last worked", "file", m.selfEnvFile())
		if b, err := m.Logs.Open(d.ID); err == nil {
			b.Printf("서버 설정(%s)을 마지막으로 정상 동작한 내용으로 되돌렸습니다", m.selfEnvFile())
			b.Close(string(store.DepFailed))
			m.Logs.Release(d.ID)
		}
	}
	m.finish(ctx, d, store.DepFailed, "rolled back: "+res.Error)
	if res.Previous != "" {
		m.Store.SetInstanceStatus(ctx, d.InstanceID, store.InstOnline, &res.Previous)
		m.Store.SetDeploymentStatus(ctx, res.Previous, store.DepLive)
	}
	m.selfDocker().Remove(ctx, selfhost.UpgraderName, 0)
	if t, err := m.Load(ctx, d.InstanceID); err == nil {
		m.emit(events.WithDeployment(ctx, d.ID), t, "deploy.failed", fmt.Sprintf("%s (%s) 배포 실패: 이전 aven-server로 되돌렸습니다: %s", t.Service.Name, t.Env.Name, res.Error))
	}
}

// collectUpgraderLogs는 upgrader가 끝나면 그 로그를 배포 로그에 덧붙이고 컨테이너와 오래된 이미지를 지운다.
func (m *Manager) collectUpgraderLogs(depID, serviceID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	sd := m.selfDocker()
	if _, err := sd.Wait(ctx, selfhost.UpgraderName); err != nil {
		return
	}
	if lines := sd.TailLogs(ctx, selfhost.UpgraderName, 50); len(lines) > 0 {
		if b, err := m.Logs.Open(depID); err == nil {
			b.Printf("aven-selfupgrade 로그:")
			for _, l := range lines {
				b.Line("  | " + l)
			}
			b.Close(string(store.DepLive))
			m.Logs.Release(depID)
		}
	}
	sd.Remove(ctx, selfhost.UpgraderName, 0)
	m.pruneImages(ctx, serviceID)
}
