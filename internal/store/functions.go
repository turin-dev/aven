package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// MaxFunctionCodeBytes는 function 서비스 코드(파일 하나)의 최대 크기다.
const MaxFunctionCodeBytes = 1 << 20

// FunctionRuntime은 function 서비스가 지금 지원하는 유일한 런타임이다.
const FunctionRuntime = "bun"

// FunctionCode는 인스턴스(서비스 × 환경)의 function 코드다. 비밀이 아니므로 평문으로 둔다.
type FunctionCode struct {
	InstanceID string    `json:"-"`
	Code       string    `json:"code"`
	Runtime    string    `json:"runtime"`
	SHA        string    `json:"sha256"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// migrateFunctions는 function 서비스 코드(인스턴스별 작업본)와 배포별 코드 스냅샷을 추가한다.
func migrateFunctions(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`CREATE TABLE function_code (
			instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
			code TEXT NOT NULL,
			runtime TEXT NOT NULL DEFAULT 'bun',
			sha TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE deployment_function_code (
			deployment_id TEXT PRIMARY KEY REFERENCES deployments(id) ON DELETE CASCADE,
			code TEXT NOT NULL,
			runtime TEXT NOT NULL DEFAULT 'bun',
			sha TEXT NOT NULL
		)`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// FunctionSHA는 코드의 sha256(hex)이다.
func FunctionSHA(code string) string {
	h := sha256.Sum256([]byte(code))
	return hex.EncodeToString(h[:])
}

// SetFunctionCode는 인스턴스의 function 코드 작업본을 저장한다(다음 배포에 반영된다).
func (s *Store) SetFunctionCode(ctx context.Context, instanceID, code string) (*FunctionCode, error) {
	if len(code) > MaxFunctionCodeBytes {
		return nil, errors.New("function code is larger than 1 MiB")
	}
	fc := &FunctionCode{InstanceID: instanceID, Code: code, Runtime: FunctionRuntime, SHA: FunctionSHA(code), UpdatedAt: time.Now().UTC()}
	_, err := s.q.ExecContext(ctx, `INSERT INTO function_code (instance_id, code, runtime, sha, updated_at) VALUES (?,?,?,?,?)
		ON CONFLICT(instance_id) DO UPDATE SET code=excluded.code, runtime=excluded.runtime, sha=excluded.sha, updated_at=excluded.updated_at`,
		fc.InstanceID, fc.Code, fc.Runtime, fc.SHA, ts(fc.UpdatedAt))
	if err != nil {
		return nil, err
	}
	return fc, nil
}

// GetFunctionCode는 인스턴스의 function 코드 작업본이다. 없으면 ErrNotFound.
func (s *Store) GetFunctionCode(ctx context.Context, instanceID string) (*FunctionCode, error) {
	fc := &FunctionCode{InstanceID: instanceID}
	var updated string
	err := s.q.QueryRowContext(ctx, `SELECT code, runtime, sha, updated_at FROM function_code WHERE instance_id=?`, instanceID).
		Scan(&fc.Code, &fc.Runtime, &fc.SHA, &updated)
	if err != nil {
		return nil, notFound(err)
	}
	fc.UpdatedAt = parseTS(updated)
	return fc, nil
}

// SnapshotFunctionCode는 배포가 쓸 코드를 기록한다. image가 있으면(redeploy/rollback) 같은 이미지를 만든
// 이전 배포의 스냅샷을, 없으면 인스턴스의 현재 작업본을 복사한다. 복사할 코드가 없으면 아무것도 하지 않는다.
func (s *Store) SnapshotFunctionCode(ctx context.Context, d *Deployment) error {
	if d.Image != nil {
		res, err := s.q.ExecContext(ctx, `INSERT OR IGNORE INTO deployment_function_code (deployment_id, code, runtime, sha)
			SELECT ?, f.code, f.runtime, f.sha FROM deployment_function_code f JOIN deployments d ON d.id=f.deployment_id
			WHERE d.instance_id=? AND d.image=? AND d.id<>? ORDER BY d.created_at DESC LIMIT 1`, d.ID, d.InstanceID, *d.Image, d.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return nil
		}
	}
	_, err := s.q.ExecContext(ctx, `INSERT OR IGNORE INTO deployment_function_code (deployment_id, code, runtime, sha)
		SELECT ?, code, runtime, sha FROM function_code WHERE instance_id=?`, d.ID, d.InstanceID)
	return err
}

// DeploymentFunctionCode는 배포가 쓴 function 코드 스냅샷이다. 없으면 ErrNotFound.
func (s *Store) DeploymentFunctionCode(ctx context.Context, deploymentID string) (*FunctionCode, error) {
	fc := &FunctionCode{}
	err := s.q.QueryRowContext(ctx, `SELECT code, runtime, sha FROM deployment_function_code WHERE deployment_id=?`, deploymentID).
		Scan(&fc.Code, &fc.Runtime, &fc.SHA)
	if err != nil {
		return nil, notFound(err)
	}
	return fc, nil
}

// DeploymentFunctionSHA는 배포 스냅샷 코드의 해시다(없으면 "").
func (s *Store) DeploymentFunctionSHA(ctx context.Context, deploymentID string) string {
	var sha sql.NullString
	s.q.QueryRowContext(ctx, `SELECT sha FROM deployment_function_code WHERE deployment_id=?`, deploymentID).Scan(&sha)
	return sha.String
}
