// Package events는 활동 피드(Event)를 저장하고, 구독자(SSE)에게 뿌리고, 알림(Notifier: 채널·사용자 알림함)으로 넘긴다.
package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/turin-dev/aven/internal/store"
)

// legacyKind는 이벤트 kind → 예전 Project.notify_events 값이다(프로젝트 webhook 호환).
var legacyKind = map[string]string{
	"deploy.live":           "deploy.success",
	"deploy.failed":         "deploy.failed",
	"deploy.crashed":        "deploy.crashed",
	"volume.backup.failed":  "volume.backup.failed",
	"backup.failed":         "volume.backup.failed",
	"volume.near_limit":     "volume.near_limit",
	"volume.limit_exceeded": "volume.limit_exceeded",
}

// LegacyKind는 이벤트 kind를 예전 notify_events 이름으로 바꾼다(해당 없으면 false).
func LegacyKind(kind string) (string, bool) {
	v, ok := legacyKind[kind]
	return v, ok
}

// Notifier는 저장된 이벤트를 알림 채널과 사용자 알림함으로 보낸다. Dispatch는 별도 goroutine에서 불린다.
type Notifier interface {
	Dispatch(e *store.Event)
}

type Bus struct {
	Store *store.Store
	Log   *slog.Logger
	// Notifier가 있으면 모든 이벤트를 넘긴다(nil이면 알림을 보내지 않는다).
	Notifier Notifier

	mu   sync.Mutex
	subs map[chan *store.Event]string // ch → projectID
}

func (b *Bus) Subscribe(projectID string) (chan *store.Event, func()) {
	ch := make(chan *store.Event, 64)
	b.mu.Lock()
	if b.subs == nil {
		b.subs = map[chan *store.Event]string{}
	}
	b.subs[ch] = projectID
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
}

// Emit은 이벤트를 저장하고 구독자와 알림으로 보낸다. 실패해도 호출자를 막지 않는다.
func (b *Bus) Emit(ctx context.Context, projectID string, envID, serviceID *string, kind, message string) {
	var name *string
	if serviceID != nil {
		if svc, err := b.Store.GetService(context.WithoutCancel(ctx), *serviceID); err == nil {
			name = &svc.Name
		}
	}
	b.EmitNamed(ctx, projectID, envID, serviceID, name, kind, message)
}

type actorKey struct{}
type deploymentKey struct{}

// WithActor는 이 context로 낸 이벤트에 남길 사용자 표시를 붙인다(API 요청의 인증된 사용자).
func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

// ActorFrom은 WithActor로 붙인 사용자 표시다(없으면 "").
func ActorFrom(ctx context.Context) string {
	v, _ := ctx.Value(actorKey{}).(string)
	return v
}

// WithDeployment는 이 context로 낸 이벤트에 배포 ID를 붙인다(deploy.* 이벤트).
func WithDeployment(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, deploymentKey{}, id)
}

// EmitNamed는 서비스 이름을 직접 넘긴다(이미 지워진 서비스의 이벤트 등).
func (b *Bus) EmitNamed(ctx context.Context, projectID string, envID, serviceID, serviceName *string, kind, message string) {
	ctx = context.WithoutCancel(ctx)
	e := &store.Event{ProjectID: projectID, EnvironmentID: envID, ServiceID: serviceID, ServiceName: serviceName, Kind: kind, Message: message}
	if a := ActorFrom(ctx); a != "" {
		e.Actor = &a
	}
	if d, _ := ctx.Value(deploymentKey{}).(string); d != "" {
		e.DeploymentID = &d
	}
	if err := b.Store.CreateEvent(ctx, e); err != nil {
		if b.Log != nil {
			b.Log.Warn("event store", "err", err)
		}
		return
	}
	b.mu.Lock()
	for ch, pid := range b.subs {
		if pid != projectID {
			continue
		}
		select {
		case ch <- e:
		default:
		}
	}
	b.mu.Unlock()
	if b.Notifier != nil {
		go b.Notifier.Dispatch(e)
	}
}

// Payload는 Discord({content})와 Slack({text}) 양쪽에서 읽히도록 두 키를 모두 넣는다(예전 프로젝트 webhook 형식).
func Payload(projectName, kind, message string) []byte {
	text := "[" + projectName + "] " + kind + ": " + message
	b, _ := json.Marshal(map[string]string{"content": text, "text": text})
	return b
}
