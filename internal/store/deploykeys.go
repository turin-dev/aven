package store

import (
	"context"
	"encoding/json"
	"time"
)

// DeployKey는 서비스별 SSH deploy key다. 개인키는 마스터 키로 암호화해 저장하고 API로는 내보내지 않는다.
type DeployKey struct {
	ServiceID   string    `json:"service_id"`
	PublicKey   string    `json:"public_key"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"created_at"`
	private     string
}

type deployKeyRow struct {
	Private     string    `json:"private"` // 암호화됨
	PublicKey   string    `json:"public_key"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"created_at"`
}

func deployKeySetting(serviceID string) string { return "deploykey:" + serviceID }

func (s *Store) PutDeployKey(ctx context.Context, serviceID, privatePEM, pub, fp string) error {
	b, _ := json.Marshal(deployKeyRow{Private: s.Seal(privatePEM), PublicKey: pub, Fingerprint: fp, CreatedAt: time.Now().UTC()})
	return s.SetSetting(ctx, deployKeySetting(serviceID), string(b))
}

// GetDeployKey는 서비스의 deploy key를 돌려준다(없으면 ErrNotFound).
func (s *Store) GetDeployKey(ctx context.Context, serviceID string) (*DeployKey, error) {
	v, err := s.GetSetting(ctx, deployKeySetting(serviceID))
	if err != nil || v == "" {
		return nil, ErrNotFound
	}
	var r deployKeyRow
	if err := json.Unmarshal([]byte(v), &r); err != nil {
		return nil, err
	}
	return &DeployKey{ServiceID: serviceID, PublicKey: r.PublicKey, Fingerprint: r.Fingerprint, CreatedAt: r.CreatedAt, private: r.Private}, nil
}

// PrivatePEM은 개인키를 복호화한다. 배포 때 임시 파일을 만들 때만 쓴다.
func (s *Store) DeployKeyPrivate(k *DeployKey) (string, error) { return s.Unseal(k.private) }

func (s *Store) DeleteDeployKey(ctx context.Context, serviceID string) error {
	return s.SetSetting(ctx, deployKeySetting(serviceID), "")
}

const knownHostsSetting = "ssh_known_hosts"

// KnownHosts는 처음 보고 기록한(TOFU) 호스트 키들이다(내장 github 키는 포함하지 않는다).
func (s *Store) KnownHosts(ctx context.Context) string {
	v, _ := s.GetSetting(ctx, knownHostsSetting)
	return v
}

func (s *Store) SetKnownHosts(ctx context.Context, v string) error {
	return s.SetSetting(ctx, knownHostsSetting, v)
}
