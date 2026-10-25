package api

// PostgreSQL 전용 작업: PgBouncer 연결 풀링(postgres_pooling.go)과 메이저 버전 업그레이드(postgres_upgrade.go).
// 두 작업 모두 시간이 걸리므로 요청은 202로 바로 돌아가고, 진행은 백그라운드 goroutine이 postgres_states에 기록한다.
// 서버가 재시작돼 goroutine이 사라지면 다음 GET이 배포 상태를 보고 이어서 마무리한다.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/store"
)

// postgresOps는 PostgreSQL 작업에 쓰는 배포 관리자 기능이다(deploy.Manager, 테스트에서는 가짜).
type postgresOps interface {
	Stop(ctx context.Context, in *store.Instance) error
	Enqueue(ctx context.Context, in *store.Instance, o deploy.EnqueueOpts) (*store.Deployment, error)
	CreateBackupSync(ctx context.Context, v *store.Volume) (*store.Backup, error)
	RestoreBackupSync(ctx context.Context, b *store.Backup) error
	CreateDBBackup(ctx context.Context, in *store.Instance, trigger, destID string) (*store.DBBackup, error)
	DeleteService(ctx context.Context, svc *store.Service) error
	SyncTCP(ctx context.Context, instanceID string) error
}

func (s *Server) pg() postgresOps {
	if s.pgOps != nil {
		return s.pgOps
	}
	return s.Deploy
}

// postgresPoll은 배포 상태를 확인하는 간격이다(테스트에서 줄인다).
var postgresPoll = time.Second

// tryPostgresOp은 인스턴스에 다른 PostgreSQL 작업이 돌고 있지 않으면 op을 표시하고 해제 함수를 돌려준다.
func (s *Server) tryPostgresOp(instanceID, op string) (func(), string, bool) {
	s.pgMu.Lock()
	defer s.pgMu.Unlock()
	if s.pgBusy == nil {
		s.pgBusy = map[string]string{}
	}
	if cur, busy := s.pgBusy[instanceID]; busy {
		return nil, cur, false
	}
	s.pgBusy[instanceID] = op
	return func() {
		s.pgMu.Lock()
		delete(s.pgBusy, instanceID)
		s.pgMu.Unlock()
	}, "", true
}

func (s *Server) postgresBusy(instanceID string) bool {
	s.pgMu.Lock()
	defer s.pgMu.Unlock()
	_, busy := s.pgBusy[instanceID]
	return busy
}

var postgresOpNames = map[string]string{"pooling": "PgBouncer 설정", "upgrade": "메이저 버전 업그레이드", "revert": "업그레이드 되돌리기"}

func writePostgresBusy(w http.ResponseWriter, op string) {
	writeErr(w, 409, fmt.Sprintf("이 데이터베이스에서 %s 작업이 진행 중입니다. 끝난 뒤 다시 시도하세요", postgresOpNames[op]))
}

func (s *Server) loadPostgres(w http.ResponseWriter, r *http.Request) (*instCtx, bool) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return nil, false
	}
	if c.svc.SourceType != "database" || c.svc.DatabaseKind == nil || *c.svc.DatabaseKind != "postgres" {
		writeErr(w, 400, "PostgreSQL 데이터베이스 서비스에서만 쓸 수 있습니다")
		return nil, false
	}
	return c, true
}

func postgresState(ctx context.Context, st *store.Store, instanceID string) (*store.PostgresState, error) {
	p, err := st.GetPostgresState(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		p = store.DefaultPostgresState(instanceID)
	}
	return p, nil
}

// waitPostgresDeployment는 배포가 live가 되거나 끝날 때까지 기다린다.
func (s *Server) waitPostgresDeployment(ctx context.Context, id string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tick := time.NewTicker(postgresPoll)
	defer tick.Stop()
	for {
		done, err := s.postgresDeploymentDone(ctx, id)
		if done {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("배포가 %s 안에 끝나지 않았습니다", timeout)
		case <-tick.C:
		}
	}
}

// postgresDeploymentDone은 배포가 끝났는지와, 끝났다면 실패 이유(live면 nil)를 돌려준다.
func (s *Server) postgresDeploymentDone(ctx context.Context, id string) (bool, error) {
	d, err := s.Store.GetDeployment(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return true, errors.New("배포를 찾을 수 없습니다")
		}
		return false, nil // 일시적인 DB 오류는 다시 확인한다
	}
	if d.Status == store.DepLive {
		return true, nil
	}
	if d.Status.Terminal() {
		if d.Error != nil && *d.Error != "" {
			return true, errors.New(*d.Error)
		}
		return true, fmt.Errorf("배포가 %s 상태로 끝났습니다", d.Status)
	}
	return false, nil
}

func (s *Server) emitPostgres(ctx context.Context, c *instCtx, kind, msg string) {
	if s.Events == nil {
		return
	}
	s.Events.Emit(ctx, c.p.ID, &c.env.ID, &c.svc.ID, kind, fmt.Sprintf("%s (%s): %s", c.svc.Name, c.env.Name, msg))
}

// reloadInstCtx는 백그라운드 작업이 최신 인스턴스 설정을 쓰도록 다시 읽는다.
func (s *Server) reloadInstCtx(ctx context.Context, c *instCtx) error {
	in, err := s.Store.GetInstance(ctx, c.in.ID)
	if err != nil {
		return err
	}
	svc, err := s.Store.GetService(ctx, c.svc.ID)
	if err != nil {
		return err
	}
	c.in, c.svc = in, svc
	return nil
}
