package deploy

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/compose"
	"github.com/turin-dev/aven/internal/store"
)

// StackMember는 스택 배포에 참여하는 서비스 인스턴스다. Name은 compose 서비스 이름이다.
type StackMember struct {
	Name     string
	Instance *store.Instance
	Deps     []store.Dependency
}

// StackLevels는 depends_on 순서대로 배포 단계를 나눈다(같은 단계는 동시에 배포한다).
// 스택 밖 서비스를 가리키는 의존은 무시한다. 순환이면 오류.
func StackLevels(members []StackMember) ([][]StackMember, error) {
	byName := map[string]StackMember{}
	names := []string{}
	deps := map[string][]string{}
	for _, m := range members {
		byName[m.Name] = m
		names = append(names, m.Name)
	}
	for _, m := range members {
		for _, d := range m.Deps {
			if _, ok := byName[d.Service]; ok {
				deps[m.Name] = append(deps[m.Name], d.Service)
			}
		}
	}
	lv, err := compose.Levels(names, deps)
	if err != nil {
		return nil, err
	}
	out := make([][]StackMember, len(lv))
	for i, l := range lv {
		for _, n := range l {
			out[i] = append(out[i], byName[n])
		}
	}
	return out, nil
}

var stackRuns sync.Map // "<stackID>/<envID>" → struct{}

// StackDeploying은 스택 배포가 진행 중인지 알려준다.
func StackDeploying(stackID, envID string) bool {
	_, ok := stackRuns.Load(stackID + "/" + envID)
	return ok
}

// stackLevelTimeout은 한 단계(빌드 포함)가 끝나기를 기다리는 최대 시간이다.
const stackLevelTimeout = 45 * time.Minute

// DeployStack은 스택의 서비스를 의존 순서대로 배포한다. 앞 단계가 live가 된 뒤에 다음 단계를 시작하고,
// 의존하는 서비스가 실패하면 그 서비스는 skipped로 기록한다. 이미 진행 중이면 false.
// 배포는 백그라운드에서 진행되며, 돌려주는 값은 계획한 단계(서비스 이름)다.
func (m *Manager) DeployStack(stackID, envID string, members []StackMember, trigger string) ([][]string, bool, error) {
	lv, err := StackLevels(members)
	if err != nil {
		return nil, false, err
	}
	key := stackID + "/" + envID
	if _, loaded := stackRuns.LoadOrStore(key, struct{}{}); loaded {
		return nil, false, nil
	}
	plan := make([][]string, len(lv))
	for i, l := range lv {
		for _, mem := range l {
			plan[i] = append(plan[i], mem.Name)
		}
	}
	go func() {
		defer stackRuns.Delete(key)
		m.runStack(lv, trigger)
	}()
	return plan, true, nil
}

func (m *Manager) runStack(lv [][]StackMember, trigger string) {
	ctx := context.Background()
	failed := map[string]string{} // compose 이름 → 이유
	for _, level := range lv {
		type queued struct {
			name string
			id   string
		}
		var waits []queued
		for _, mem := range level {
			reason := ""
			for _, d := range mem.Deps {
				if f, bad := failed[d.Service]; bad {
					reason = fmt.Sprintf("dependency %s did not become live (%s)", d.Service, f)
					break
				}
			}
			in, err := m.Store.GetInstance(ctx, mem.Instance.ID)
			if err != nil {
				failed[mem.Name] = "instance not found"
				continue
			}
			if reason != "" {
				m.Enqueue(ctx, in, EnqueueOpts{Trigger: trigger, Status: store.DepSkipped, Error: reason})
				failed[mem.Name] = "skipped"
				continue
			}
			d, err := m.Enqueue(ctx, in, EnqueueOpts{Trigger: trigger})
			if err != nil {
				failed[mem.Name] = err.Error()
				continue
			}
			waits = append(waits, queued{mem.Name, d.ID})
		}
		deadline := time.Now().Add(stackLevelTimeout)
		for _, w := range waits {
			for {
				d, err := m.Store.GetDeployment(ctx, w.id)
				if err != nil {
					failed[w.name] = "deployment not found"
					break
				}
				if d.Status.Terminal() {
					if d.Status != store.DepLive {
						failed[w.name] = string(d.Status)
					}
					break
				}
				if time.Now().After(deadline) {
					failed[w.name] = "timeout"
					break
				}
				time.Sleep(time.Second)
			}
		}
	}
}
