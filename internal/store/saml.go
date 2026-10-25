package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// VerifiedDomain is a DNS proof challenge for a workspace email domain.
type VerifiedDomain struct {
	Domain   string `json:"domain"`
	Token    string `json:"token"`
	Verified bool   `json:"verified"`
}

// SAMLConfig contains the workspace SAML service provider configuration.
// MetadataXML is deliberately omitted from JSON responses; the IdP's public
// signing certificates are available through the IdP itself.
type SAMLConfig struct {
	MetadataXML string           `json:"-"`
	MetadataURL string           `json:"metadata_url"`
	Domains     []VerifiedDomain `json:"domains"`
	DefaultRole string           `json:"default_role"`
	EnforceSSO  bool             `json:"enforce_sso"`
	Configured  bool             `json:"configured"`
	UpdatedAt   *time.Time       `json:"updated_at,omitempty"`
}

func (s *Store) GetSAMLConfig(ctx context.Context) (*SAMLConfig, error) {
	var cfg SAMLConfig
	var domains, updated string
	err := s.q.QueryRowContext(ctx, `SELECT metadata_xml, metadata_url, verified_domains, default_role, enforce_sso, updated_at FROM saml_config WHERE id=1`).
		Scan(&cfg.MetadataXML, &cfg.MetadataURL, &domains, &cfg.DefaultRole, &cfg.EnforceSSO, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return &SAMLConfig{DefaultRole: RoleMember, Domains: []VerifiedDomain{}}, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(domains), &cfg.Domains); err != nil {
		return nil, err
	}
	if cfg.Domains == nil {
		cfg.Domains = []VerifiedDomain{}
	}
	t := parseTS(updated)
	cfg.UpdatedAt = &t
	cfg.Configured = cfg.MetadataXML != ""
	return &cfg, nil
}

func (s *Store) SaveSAMLConfig(ctx context.Context, cfg *SAMLConfig) error {
	if cfg.DefaultRole == "" {
		cfg.DefaultRole = RoleMember
	}
	if cfg.Domains == nil {
		cfg.Domains = []VerifiedDomain{}
	}
	now := time.Now().UTC()
	domains, err := json.Marshal(cfg.Domains)
	if err != nil {
		return err
	}
	_, err = s.q.ExecContext(ctx, `INSERT INTO saml_config (id, metadata_xml, metadata_url, verified_domains, default_role, enforce_sso, updated_at)
		VALUES (1,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET metadata_xml=excluded.metadata_xml, metadata_url=excluded.metadata_url,
		verified_domains=excluded.verified_domains, default_role=excluded.default_role, enforce_sso=excluded.enforce_sso, updated_at=excluded.updated_at`,
		cfg.MetadataXML, cfg.MetadataURL, string(domains), cfg.DefaultRole, cfg.EnforceSSO, ts(now))
	if err == nil {
		cfg.Configured = cfg.MetadataXML != ""
		cfg.UpdatedAt = &now
	}
	return err
}

// CreateSAMLLoginRequest stores a one-time request correlation record.
func (s *Store) CreateSAMLLoginRequest(ctx context.Context, relayHash, requestID, redirectTo string, expires time.Time) error {
	_, err := s.q.ExecContext(ctx, `INSERT INTO saml_login_requests (relay_hash, request_id, redirect_to, expires_at) VALUES (?,?,?,?)`, relayHash, requestID, redirectTo, ts(expires))
	return conflict(err)
}

// ConsumeSAMLLoginRequest atomically removes and returns a valid request state.
func (s *Store) ConsumeSAMLLoginRequest(ctx context.Context, relayHash string) (requestID, redirectTo string, err error) {
	err = s.Tx(ctx, func(tx *Store) error {
		if _, e := tx.q.ExecContext(ctx, `DELETE FROM saml_login_requests WHERE expires_at <= ?`, ts(time.Now())); e != nil {
			return e
		}
		e := tx.q.QueryRowContext(ctx, `SELECT request_id, redirect_to FROM saml_login_requests WHERE relay_hash=? AND expires_at > ?`, relayHash, ts(time.Now())).Scan(&requestID, &redirectTo)
		if e != nil {
			return notFound(e)
		}
		_, e = tx.q.ExecContext(ctx, `DELETE FROM saml_login_requests WHERE relay_hash=?`, relayHash)
		return e
	})
	return
}

// UseSAMLAssertionID records a verified assertion ID once to reject replay.
func (s *Store) UseSAMLAssertionID(ctx context.Context, assertionID string, expires time.Time) error {
	if _, err := s.q.ExecContext(ctx, `DELETE FROM saml_assertion_replays WHERE expires_at <= ?`, ts(time.Now())); err != nil {
		return err
	}
	_, err := s.q.ExecContext(ctx, `INSERT INTO saml_assertion_replays (assertion_id, expires_at) VALUES (?,?)`, assertionID, ts(expires))
	return conflict(err)
}
