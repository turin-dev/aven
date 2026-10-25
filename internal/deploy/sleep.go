package deploy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/logbook"
	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/store"
)

// 앱 sleeping(serverless):
//   - sleep_when_idle 인스턴스에 sleep_after_s 동안 HTTP 요청이 없으면(access log 기준) 컨테이너를 멈추고 status=sleeping.
//   - 그동안 Caddy 라우트는 aven wake handler를 가리킨다. 첫 요청이 오면 컨테이너를 다시 시작하고,
//     healthcheck가 통과하면(최대 30초) 그 요청을 컨테이너로 넘긴다. 이후 요청은 라우트가 바뀌어 컨테이너로 바로 간다.

const wakeTimeout = 30 * time.Second

func (m *Manager) markAwake(instanceID string) {
	m.mu.Lock()
	m.awake[instanceID] = time.Now()
	m.mu.Unlock()
}

func (m *Manager) awakeSince(instanceID string) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.awake[instanceID]
}

// sleepEnabled는 인스턴스가 잠들 수 있는지다. config-as-code(deploy.sleepApplication)가 현재 배포에서
// 설정 값을 바꿨으면 그 배포 값을 따르고, 아니면 인스턴스 설정(sleep_when_idle, 바로 반영)을 따른다.
func (m *Manager) sleepEnabled(ctx context.Context, in *store.Instance) bool {
	// 자동 확장 중이거나 여러 노드에 나눠 띄운 서비스는 재우지 않는다(잠들기·깨우기는 한 노드의 replica만 다룬다).
	if in.Autoscale != nil && in.Autoscale.Enabled {
		return false
	}
	if in.CurrentDeploymentID != nil {
		d, err := m.Store.GetDeployment(ctx, *in.CurrentDeploymentID)
		if err == nil && len(d.Groups()) > 1 {
			return false
		}
		if err == nil && d.Spec.SleepWhenIdle != d.ReqSpec.SleepWhenIdle {
			return d.Spec.SleepWhenIdle
		}
	}
	return in.SleepWhenIdle
}

// SleepLoop은 5초마다 잠들 인스턴스를 찾는다.
func (m *Manager) SleepLoop(ctx context.Context) {
	started := time.Now()
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		insts, err := m.Store.ListInstances(ctx)
		if err != nil {
			continue
		}
		for _, in := range insts {
			if in.Status != store.InstOnline || in.CurrentDeploymentID == nil || in.CronSchedule != nil || len(in.Domains) == 0 || !m.sleepEnabled(ctx, in) {
				continue
			}
			last := m.HTTPLogs.LastRequest(in.ID)
			for _, t := range []time.Time{m.awakeSince(in.ID), started} {
				if t.After(last) {
					last = t
				}
			}
			if time.Since(last) < time.Duration(in.SleepAfterS)*time.Second {
				continue
			}
			if err := m.Sleep(ctx, in); err != nil {
				m.Log.Warn("sleep", "instance", in.ID, "err", err)
			}
		}
	}
}

// Sleep은 인스턴스의 현재 컨테이너를 멈추고(지우지 않음) 라우트를 wake handler로 돌린다.
func (m *Manager) Sleep(ctx context.Context, in *store.Instance) error {
	if m.IsSelfService(in.ServiceID) {
		return ErrSelfService
	}
	m.mu.Lock()
	if m.sleeping[in.ID] != nil || m.waking[in.ID] != nil {
		m.mu.Unlock()
		return nil
	}
	ch := make(chan struct{})
	m.sleeping[in.ID] = ch
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.sleeping, in.ID)
		m.mu.Unlock()
		close(ch)
	}()
	t, err := m.Load(ctx, in.ID)
	if err != nil {
		return err
	}
	in = t.Instance
	if in.Status != store.InstOnline || in.CurrentDeploymentID == nil || in.CronSchedule != nil || len(in.Domains) == 0 || !m.sleepEnabled(ctx, in) {
		return nil
	}
	last := m.HTTPLogs.LastRequest(in.ID)
	if awake := m.awakeSince(in.ID); awake.After(last) {
		last = awake
	}
	if time.Since(last) < time.Duration(in.SleepAfterS)*time.Second {
		return nil
	}
	d, err := m.Store.GetDeployment(ctx, *in.CurrentDeploymentID)
	if err != nil {
		return err
	}
	// 로컬은 aven-server의 wake handler, 원격은 그 노드 agent의 wake handler가 깨운다.
	if isLocal(d.NodeID) && m.Cfg.WakeAddr == "" {
		return nil
	}
	if !m.nodeOnline(d.NodeID) {
		return nil
	}
	// 중단 전 마지막으로 다시 요청과 상태를 확인한다.
	if m.HTTPLogs.LastRequest(in.ID).After(last) {
		return nil
	}
	// 먼저 상태를 바꿔 crash 감시와 reconcile이 멈춘 컨테이너를 건드리지 않게 한다.
	if err := m.Store.SetInstanceStatus(ctx, in.ID, store.InstSleeping, nil); err != nil {
		return err
	}
	if err := m.SyncProxy(ctx); err != nil {
		if restoreErr := m.Store.SetInstanceStatus(ctx, in.ID, store.InstOnline, nil); restoreErr != nil {
			return errors.Join(fmt.Errorf("sleep proxy sync: %w", err), restoreErr)
		}
		return fmt.Errorf("sleep proxy sync: %w", err)
	}
	if m.HTTPLogs.LastRequest(in.ID).After(last) {
		return m.wakeBounded(ctx, in.ID)
	}
	drv := m.driverFor(d)
	for _, c := range d.Containers {
		m.markDrain(c)
		err := drv.Stop(ctx, c, 10)
		m.undrain(c)
		if err != nil {
			wakeErr := m.wakeBounded(ctx, in.ID)
			return errors.Join(fmt.Errorf("sleep stop %s: %w", c, err), wakeErr)
		}
	}
	// sleep 중 wake 요청이 들어오면 상태가 바뀌기 전에 즉시 다시 깨운다.
	if m.HTTPLogs.LastRequest(in.ID).After(last) {
		return m.wakeBounded(ctx, in.ID)
	}
	m.emit(ctx, t, "service.sleeping", fmt.Sprintf("%s (%s): %d초 동안 요청이 없어 잠들었습니다", t.Service.Name, t.Env.Name, in.SleepAfterS))
	return nil
}

// Wake는 잠든 인스턴스를 깨우고 healthcheck가 통과할 때까지 기다린다. 동시에 여러 번 불려도 한 번만 깨운다.
func (m *Manager) Wake(ctx context.Context, instanceID string) error {
	m.mu.Lock()
	if ch := m.sleeping[instanceID]; ch != nil {
		m.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
		return m.Wake(ctx, instanceID)
	}
	if ch, ok := m.waking[instanceID]; ok {
		m.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
		in, err := m.Store.GetInstance(ctx, instanceID)
		if err == nil && in.Status != store.InstOnline {
			return errors.New("service did not wake up")
		}
		return err
	}
	ch := make(chan struct{})
	m.waking[instanceID] = ch
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.waking, instanceID)
		m.mu.Unlock()
		close(ch)
	}()
	return m.wakeBounded(ctx, instanceID)
}

func (m *Manager) wakeBounded(ctx context.Context, instanceID string) error {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wakeTimeout)
	defer cancel()
	return m.wake(wctx, instanceID)
}

func (m *Manager) wake(ctx context.Context, instanceID string) error {
	t, err := m.Load(ctx, instanceID)
	if err != nil {
		return err
	}
	in := t.Instance
	if in.Status != store.InstSleeping {
		return nil
	}
	if in.CurrentDeploymentID == nil {
		return errors.New("no current deployment")
	}
	d, err := m.Store.GetDeployment(ctx, *in.CurrentDeploymentID)
	if err != nil {
		return err
	}
	drv := m.driverFor(d)
	for _, c := range d.Containers {
		if err := drv.Start(ctx, c); err != nil {
			// 컨테이너가 없으면 같은 설정으로 다시 만든다.
			if st, ierr := drv.InspectMany(ctx, []string{c}); ierr == nil && st[c] == nil {
				for i, name := range d.Containers {
					if name == c {
						if err := drv.Run(ctx, m.runSpec(t, d, i)); err != nil {
							return err
						}
					}
				}
			} else {
				return err
			}
		}
	}
	hctx, cancel := context.WithTimeout(ctx, wakeTimeout)
	defer cancel()
	sink := logbook.Discard()
	if err := m.healthcheckAll(hctx, t, d, sink); err != nil {
		return fmt.Errorf("wake healthcheck: %w", err)
	}
	m.markAwake(in.ID)
	m.HTTPLogs.Touch(in.ID)
	if err := m.Store.SetInstanceStatus(ctx, in.ID, store.InstOnline, nil); err != nil {
		return err
	}
	if err := m.SyncProxy(ctx); err != nil {
		// wake 라우트가 살아 있어 재시도를 허용한다.
		if restoreErr := m.Store.SetInstanceStatus(ctx, in.ID, store.InstSleeping, nil); restoreErr != nil {
			return errors.Join(fmt.Errorf("wake proxy sync: %w", err), restoreErr)
		}
		return fmt.Errorf("wake proxy sync: %w", err)
	}
	m.emit(ctx, t, "service.woke", fmt.Sprintf("%s (%s): 요청이 와서 깨어났습니다", t.Service.Name, t.Env.Name))
	return nil
}

// WakeHandler는 keel-proxy가 잠든 서비스로 보낸 요청을 받는다.
// 같은 socket에서 와일드카드 도메인의 on-demand TLS 허가 확인(proxy.TLSAskHost)도 받는다.
func (m *Manager) WakeHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if proxy.HandleTLSAsk(w, r, m.TLSAllowed) {
			return
		}
		host := strings.ToLower(r.Host)
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		id, ok := m.HTTPLogs.InstanceForHost(host)
		if !ok {
			http.Error(w, "unknown host", http.StatusNotFound)
			return
		}
		// 진행 중인 sleep이 중단 직전이라도 새 요청을 감지하게 한다.
		m.HTTPLogs.Touch(id)
		if err := m.Wake(r.Context(), id); err != nil {
			m.Log.Warn("wake", "host", host, "err", err)
			http.Error(w, "service is waking up, please retry: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		target, err := m.upstreamFor(r.Context(), id, host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		rp := httputil.NewSingleHostReverseProxy(target)
		orig := rp.Director
		rp.Director = func(req *http.Request) {
			orig(req)
			req.Host = r.Host
		}
		rp.ServeHTTP(w, r)
	})
}

// TLSAllowed는 keel-proxy가 host의 인증서를 on-demand로 받아도 되는지다: 설정된 도메인(정확히 같거나 와일드카드에 맞는)이고,
// 그 도메인(정확히 같은 claim이 먼저, 없으면 와일드카드 claim)이 legacy·확인됐거나 DNS가 이 노드를 가리킬 때만(domain_states).
// claim 행이 없으면(마이그레이션 이전 경합 등) 예전처럼 허가한다. 소유 확인 전 신규 도메인은 Caddy가 on-demand 정책에 두므로
// 확인되기 전에는 ACME 발급을 시도하지 않는다.
func (m *Manager) TLSAllowed(host string) bool {
	if _, ok := m.HTTPLogs.InstanceForHost(host); !ok {
		return false
	}
	if m.Store == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, claim := range []string{host, proxy.WildcardFor(host)} {
		if claim == "" {
			continue
		}
		st, err := m.Store.GetDomainState(ctx, claim)
		switch {
		case err == nil:
			return domainTLSOK(st)
		case !errors.Is(err, store.ErrNotFound):
			return false // DB 오류: 이번 handshake만 거절한다(Caddy가 나중에 다시 묻는다)
		}
	}
	return true
}

// upstreamFor는 깨어난 인스턴스의 첫 replica 주소다(aven-server에서 컨테이너 IP로 바로 연결).
func (m *Manager) upstreamFor(ctx context.Context, instanceID, host string) (*url.URL, error) {
	t, err := m.Load(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	if t.Instance.CurrentDeploymentID == nil {
		return nil, errors.New("no running deployment")
	}
	d, err := m.Store.GetDeployment(ctx, *t.Instance.CurrentDeploymentID)
	if err != nil || len(d.Containers) == 0 {
		return nil, errors.New("no running deployment")
	}
	port := m.targetPort(t, d, host)
	states, err := m.driverFor(d).InspectMany(ctx, d.Containers)
	if err != nil {
		return nil, err
	}
	for _, c := range d.Containers {
		if st := states[c]; st != nil && st.Running {
			if ips := st.IP(EnvNetwork(t.Env.ID)); len(ips) > 0 {
				return &url.URL{Scheme: "http", Host: net.JoinHostPort(ips[0], fmt.Sprint(port))}, nil
			}
		}
	}
	return nil, errors.New("no running replica")
}
