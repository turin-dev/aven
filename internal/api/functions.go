package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/turin-dev/aven/internal/catalog"
	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/store"
)

// PendingFunctionCode는 function 코드가 현재 배포와 다를 때 pending_changes에 들어가는 키다.
const PendingFunctionCode = "function.code"

type functionView struct {
	Code      string  `json:"code"`
	Runtime   string  `json:"runtime"`
	SHA256    string  `json:"sha256"`
	UpdatedAt *string `json:"updated_at"`
	// DeployedSHA256은 현재 배포가 쓴 코드의 해시다(배포 전이면 null).
	DeployedSHA256 *string `json:"deployed_sha256"`
	MaxBytes       int     `json:"max_bytes"`
}

// functionDef는 새 function 서비스 정의다. restart는 repo 서비스처럼 on_failure로 둔다.
func functionDef() catalog.ServiceDef {
	return catalog.ServiceDef{Name: "function", Icon: "function", SourceType: "function"}
}

// functionCodeArg는 생성/수정 요청의 code를 검증한다(nil이면 기본 코드).
func functionCodeArg(code *string) (string, error) {
	if code == nil {
		return deploy.DefaultFunctionCode, nil
	}
	if len(*code) > store.MaxFunctionCodeBytes {
		return "", fmt.Errorf("code must be at most %d bytes (1 MiB)", store.MaxFunctionCodeBytes)
	}
	return *code, nil
}

// initFunctionCode는 function 서비스의 모든 환경 인스턴스에 같은 코드를 넣는다.
func (s *Server) initFunctionCode(ctx context.Context, svc *store.Service, code string) error {
	insts, err := s.Store.ListInstancesByService(ctx, svc.ID)
	if err != nil {
		return err
	}
	for _, in := range insts {
		if _, err := s.Store.SetFunctionCode(ctx, in.ID, code); err != nil {
			return err
		}
	}
	return nil
}

// copyFunctionCode는 새 환경의 function 인스턴스에 원본 환경의 코드(없으면 기본 코드)를 넣는다.
func (s *Server) copyFunctionCode(ctx context.Context, svc *store.Service, orig, in *store.Instance) error {
	if svc.SourceType != "function" {
		return nil
	}
	code := deploy.DefaultFunctionCode
	if orig != nil {
		if fc, err := s.Store.GetFunctionCode(ctx, orig.ID); err == nil {
			code = fc.Code
		}
	}
	_, err := s.Store.SetFunctionCode(ctx, in.ID, code)
	return err
}

// functionPending은 function 서비스의 코드 작업본이 배포 d의 스냅샷과 다르면 pending 목록에 function.code를 더한다.
func (s *Server) functionPending(ctx context.Context, svc *store.Service, in *store.Instance, d *store.Deployment, out []string) []string {
	if svc.SourceType != "function" || d == nil {
		return out
	}
	fc, err := s.Store.GetFunctionCode(ctx, in.ID)
	if err != nil {
		return out
	}
	if s.Store.DeploymentFunctionSHA(ctx, d.ID) != fc.SHA {
		out = append(out, PendingFunctionCode)
		sort.Strings(out)
	}
	return out
}

func (s *Server) loadFunctionInstance(w http.ResponseWriter, r *http.Request) (*instCtx, bool) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return nil, false
	}
	if c.svc.SourceType != "function" {
		writeErr(w, 400, "function 서비스가 아닙니다")
		return nil, false
	}
	return c, true
}

func (s *Server) functionView(ctx context.Context, in *store.Instance) (*functionView, error) {
	v := &functionView{Runtime: store.FunctionRuntime, MaxBytes: store.MaxFunctionCodeBytes}
	fc, err := s.Store.GetFunctionCode(ctx, in.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		v.SHA256 = store.FunctionSHA("")
	case err != nil:
		return nil, err
	default:
		v.Code, v.Runtime, v.SHA256 = fc.Code, fc.Runtime, fc.SHA
		u := fc.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
		v.UpdatedAt = &u
	}
	if in.CurrentDeploymentID != nil {
		if sha := s.Store.DeploymentFunctionSHA(ctx, *in.CurrentDeploymentID); sha != "" {
			v.DeployedSHA256 = &sha
		}
	}
	return v, nil
}

// GET /api/services/{sid}/instances/{eid}/function
func (s *Server) getFunction(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadFunctionInstance(w, r)
	if !ok {
		return
	}
	v, err := s.functionView(r.Context(), c.in)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

// PUT /api/services/{sid}/instances/{eid}/function {code, runtime?:"bun"} — 코드를 저장한다(다음 배포에 반영, ?deploy=1이면 바로 배포).
func (s *Server) putFunction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok := s.loadFunctionInstance(w, r)
	if !ok {
		return
	}
	var in struct {
		Code    *string `json:"code"`
		Runtime *string `json:"runtime"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Code == nil {
		writeErr(w, 400, "code is required")
		return
	}
	if in.Runtime != nil && *in.Runtime != store.FunctionRuntime {
		writeErr(w, 400, `runtime must be "bun"`)
		return
	}
	code, err := functionCodeArg(in.Code)
	if err != nil {
		writeErr(w, 413, err.Error())
		return
	}
	prev, _ := s.Store.GetFunctionCode(ctx, c.in.ID)
	changed := prev == nil || prev.SHA != store.FunctionSHA(code)
	if changed {
		if _, err := s.Store.SetFunctionCode(ctx, c.in.ID, code); err != nil {
			s.storeErr(w, err)
			return
		}
		s.Events.Emit(ctx, c.p.ID, &c.env.ID, &c.svc.ID, "service.updated", fmt.Sprintf("%s (%s): function 코드를 변경했습니다", c.svc.Name, c.env.Name))
	}
	if q := r.URL.Query().Get("deploy"); q == "1" || q == "true" {
		trigger := "manual" // 코드 변경은 변수만 바뀐 배포가 아니다
		if !changed && c.in.CurrentDeploymentID != nil {
			if cur, err := s.Store.GetDeployment(ctx, *c.in.CurrentDeploymentID); err == nil && len(s.functionPending(ctx, c.svc, c.in, cur, nil)) == 0 {
				trigger = s.deployTrigger(ctx, c.in)
			}
		}
		if _, err := s.Deploy.Enqueue(ctx, c.in, deploy.EnqueueOpts{Trigger: trigger}); err != nil {
			s.storeErr(w, err)
			return
		}
	}
	fresh, err := s.Store.GetInstance(ctx, c.in.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	v, err := s.functionView(ctx, fresh)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}
