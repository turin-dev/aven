package store

import (
	"context"
	"encoding/json"
	"errors"
)

// CloudflareSettings는 Cloudflare 연동 설정이다(settings 테이블 "cloudflare", API 토큰은 마스터 키로 암호화한다).
type CloudflareSettings struct {
	Enabled bool `json:"enabled"`
	// Token은 복호화된 API 토큰이다(API로 내보내지 않는다).
	Token string `json:"token"`
	// Mode: auto(여러 노드 호스트는 Load Balancing, 쓸 수 없으면 DNS 레코드) | load_balancer | dns.
	Mode string `json:"mode"`
	// Proxied는 레코드·로드밸런서의 Cloudflare 프록시(주황 구름)다. 기본은 꺼짐.
	Proxied bool `json:"proxied"`
	// Wildcards는 소유가 확인된 와일드카드 호스트도 동기화할지다. 기본은 꺼짐.
	Wildcards bool `json:"wildcards"`
	// Zones는 관리할 존 이름이다. 비어 있으면 연동을 켜거나 동기화할 수 없다.
	Zones []string `json:"zones"`
}

const cloudflareSettingKey = "cloudflare"

// DefaultCloudflareSettings는 저장된 설정이 없을 때의 값이다(꺼짐, auto, 프록시 끔, 와일드카드 끔).
// 존 목록이 비어 있으면 연동을 켜거나 동기화할 수 없다.
func DefaultCloudflareSettings() CloudflareSettings {
	return CloudflareSettings{Mode: "auto", Proxied: false, Zones: []string{}}
}

func (s *Store) CloudflareSettings(ctx context.Context) (CloudflareSettings, error) {
	raw, err := s.GetSetting(ctx, cloudflareSettingKey)
	if errors.Is(err, ErrNotFound) {
		return DefaultCloudflareSettings(), nil
	}
	if err != nil {
		return CloudflareSettings{}, err
	}
	cfg := DefaultCloudflareSettings()
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return CloudflareSettings{}, err
	}
	if cfg.Zones == nil {
		cfg.Zones = []string{}
	}
	if cfg.Token, err = s.Unseal(cfg.Token); err != nil {
		return CloudflareSettings{}, err
	}
	return cfg, nil
}

func (s *Store) SetCloudflareSettings(ctx context.Context, cfg CloudflareSettings) error {
	if cfg.Token != "" {
		cfg.Token = s.Seal(cfg.Token)
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return s.SetSetting(ctx, cloudflareSettingKey, string(b))
}
