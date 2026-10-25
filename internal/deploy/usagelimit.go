package deploy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

// ErrProjectSuspended는 사용량 한도(action=stop)로 정지된 프로젝트에 새 배포를 넣으려 할 때다(API 409).
var ErrProjectSuspended = errors.New("project suspended by usage limit")

// checkSuspended는 인스턴스의 프로젝트가 서버 owner 정지(store.OwnerHold) 또는 사용량 한도로 정지 중이면
// ErrProjectSuspended를 감싼 에러다.
func (m *Manager) checkSuspended(ctx context.Context, in *store.Instance) error {
	if h, err := m.Store.InstanceOwnerHold(ctx, in.ID); err == nil && h != nil {
		if h.Scope == store.HoldService {
			return fmt.Errorf("%w: 서버 관리자가 이 서비스를 정지했습니다%s. 서버 관리자가 재개할 때까지 배포·재시작할 수 없습니다", ErrProjectSuspended, holdReason(h))
		}
		what := "프로젝트"
		if h.Scope == store.HoldWorkspace {
			what = "작업 공간"
		}
		return fmt.Errorf("%w: 서버 owner가 이 %s를 정지했습니다%s. 서버 owner가 재개할 때까지 배포·재시작할 수 없습니다", ErrProjectSuspended, what, holdReason(h))
	}
	sp, err := m.Store.SuspensionForInstance(ctx, in.ID)
	if err != nil || !sp.ActiveAt(time.Now()) {
		return nil
	}
	return fmt.Errorf("%w: 이번 달 사용량 한도에 도달해 프로젝트가 정지되었습니다(%s). %s까지 새 배포를 할 수 없습니다. 관리자가 한도를 올리거나 지우면 풀립니다",
		ErrProjectSuspended, sp.Reason, sp.Until.UTC().Format("2006-01-02 15:04 UTC"))
}

// checkDeployBlocked는 서버 관리자가 서비스의 배포를 막아 두었으면 에러다(도는 컨테이너와 재시작은 그대로 둔다).
func (m *Manager) checkDeployBlocked(ctx context.Context, in *store.Instance) error {
	h, err := m.Store.InstanceDeployBlock(ctx, in.ID)
	if err != nil || h == nil {
		return nil
	}
	return fmt.Errorf("%w: 서버 관리자가 이 서비스의 배포를 막았습니다%s. 차단이 풀릴 때까지 새로 배포할 수 없습니다", ErrProjectSuspended, holdReason(h))
}

func holdReason(h *store.OwnerHold) string {
	if h.Reason == "" {
		return ""
	}
	return " (" + h.Reason + ")"
}

// SuspendedErr는 인스턴스의 프로젝트가 사용량 한도로 정지 중이면 ErrProjectSuspended를 감싼 에러다(없으면 nil).
// Enqueue를 거치지 않는 작업(재시작, 수동 scale, cron 즉시 실행)을 API가 막을 때 쓴다.
func (m *Manager) SuspendedErr(ctx context.Context, in *store.Instance) error {
	return m.checkSuspended(ctx, in)
}

// StartAgain은 멈춘 인스턴스를 다시 띄운다: 현재 배포에 이미지가 있으면 그 이미지·설정으로 다시 배포(redeploy)하고,
// 없으면 인스턴스 설정으로 새로 배포한다. 사용량 한도 정지를 풀 때(resume) 쓴다.
func (m *Manager) StartAgain(ctx context.Context, in *store.Instance) (*store.Deployment, error) {
	if in.CurrentDeploymentID != nil {
		if cur, err := m.Store.GetDeployment(ctx, *in.CurrentDeploymentID); err == nil && cur.Image != nil {
			return m.Enqueue(ctx, in, EnqueueOpts{Trigger: "redeploy", Image: cur.Image, Spec: &cur.Spec, ReqSpec: &cur.ReqSpec,
				Commit: CommitOf(cur), Branch: cur.Branch, BuilderUsed: cur.BuilderUsed})
		}
	}
	return m.Enqueue(ctx, in, EnqueueOpts{Trigger: "manual"})
}
