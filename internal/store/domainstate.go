package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// domain_states는 공개 도메인 호스트의 소유(claim)와 DNS·인증서 확인 상태다. 호스트가 기본 키라서 두 서비스가 같은 호스트를
// 동시에 추가해도 하나만 성공한다. 행은 인스턴스 스펙(Spec.Domains)을 쓰는 같은 트랜잭션에서 만들고 지운다(syncDomainClaims).

// 확인 방법(verify_method).
const (
	VerifyLegacy = "legacy" // 이 테이블이 생기기 전부터 있던 호스트(검증된 것으로 본다)
	VerifyDNS    = "dns"    // DNS가 이 노드(주소 또는 ingress 호스트)를 가리킨다
	VerifyHTTP   = "http"   // /.well-known/keel/domain-check 응답에 토큰이 있다
	VerifyTXT    = "txt"    // _keel-verify.<host> TXT 레코드에 토큰이 있다
)

// DNS 상태(dns_status). pending은 아직 한 번도 확인하지 않은 상태다.
const (
	DNSPending    = "pending"
	DNSActive     = "active"
	DNSPendingDNS = "pending_dns"
	DNSProxied    = "proxied"
	DNSError      = "error"
)

// DomainState는 domain_states 행이다.
type DomainState struct {
	Host         string       `json:"host"`
	InstanceID   string       `json:"instance_id"`
	VerifyMethod string       `json:"verify_method"`
	VerifiedAt   *time.Time   `json:"verified_at"`
	DNSStatus    string       `json:"dns_status"`
	Proxy        string       `json:"proxy"` // direct | cloudflare | tunnel | none | ""(미확인)
	Observed     []ObservedRR `json:"observed"`
	CertStatus   string       `json:"cert_status"`
	CertNotAfter *time.Time   `json:"cert_not_after"`
	LastError    string       `json:"last_error"`
	CheckedAt    *time.Time   `json:"checked_at"`
	CreatedAt    time.Time    `json:"created_at"`
	// IPv6는 이 도메인에 배정된 공개 IPv6 주소다(서비스 노드에 IPv6 풀이 없으면 "").
	IPv6 string `json:"ipv6"`
}

// ObservedRR은 확인 때 실제로 조회된 레코드 하나다.
type ObservedRR struct {
	Type  string `json:"type"` // A | AAAA | CNAME | TXT
	Value string `json:"value"`
}

// Verified는 호스트가 legacy이거나 확인을 통과했는지다.
func (d *DomainState) Verified() bool {
	return d != nil && (d.VerifyMethod == VerifyLegacy || d.VerifiedAt != nil)
}

// HostClaimedError는 다른 인스턴스가 이미 가진 호스트를 추가하려 할 때의 오류다(errors.Is(err, ErrConflict)).
type HostClaimedError struct {
	Host       string
	InstanceID string
}

func (e *HostClaimedError) Error() string {
	return fmt.Sprintf("도메인 %s은(는) 이미 다른 서비스에서 사용 중입니다", e.Host)
}

func (e *HostClaimedError) Is(target error) bool { return target == ErrConflict }

// migrateDomainStates는 도메인 claim·상태 테이블을 만들고, 지금 스펙에 있는 호스트를 모두 legacy(확인됨)로 채운다.
// 같은 호스트가 이미 두 인스턴스에 있으면(예전 경합) 먼저 읽힌 쪽이 claim을 갖고, 다른 쪽은 그대로 둔다(동작은 바뀌지 않는다).
func migrateDomainStates(ctx context.Context, tx *Store) error {
	if _, err := tx.q.ExecContext(ctx, `CREATE TABLE domain_states (
		host TEXT PRIMARY KEY,
		instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
		verify_method TEXT NOT NULL DEFAULT '',
		verified_at TEXT,
		dns_status TEXT NOT NULL DEFAULT 'pending',
		proxy TEXT NOT NULL DEFAULT '',
		observed TEXT NOT NULL DEFAULT '[]',
		cert_status TEXT NOT NULL DEFAULT '',
		cert_not_after TEXT,
		last_error TEXT,
		checked_at TEXT,
		created_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	if _, err := tx.q.ExecContext(ctx, `CREATE INDEX domain_states_instance ON domain_states(instance_id)`); err != nil {
		return err
	}
	rows, err := tx.q.QueryContext(ctx, `SELECT id, spec FROM instances ORDER BY updated_at, id`)
	if err != nil {
		return err
	}
	type row struct{ id, spec string }
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.spec); err != nil {
			rows.Close()
			return err
		}
		all = append(all, r)
	}
	rows.Close()
	now := ts(time.Now())
	for _, r := range all {
		var sp Spec
		json.Unmarshal([]byte(r.spec), &sp)
		for _, d := range sp.Domains {
			h := claimHost(d.Host)
			if h == "" {
				continue
			}
			if _, err := tx.q.ExecContext(ctx, `INSERT OR IGNORE INTO domain_states (host, instance_id, verify_method, verified_at, created_at) VALUES (?,?,?,?,?)`,
				h, r.id, VerifyLegacy, now, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func claimHost(h string) string { return strings.ToLower(strings.TrimSpace(h)) }

// syncDomainClaims는 인스턴스 스펙을 쓰기 직전에(같은 트랜잭션 안에서) claim 행을 스펙의 호스트 목록에 맞춘다.
//   - 스펙에서 빠진 호스트: 이 인스턴스의 행을 지운다.
//   - 새로 생긴 호스트: 행을 만든다. 다른 인스턴스가 가진 호스트면 *HostClaimedError(ErrConflict).
//   - 그대로인 호스트: 행이 없으면 만든다(있으면 그대로). 예전 경합으로 다른 인스턴스가 가진 경우에도 실패시키지 않는다.
//
// old가 nil이면 새 인스턴스다. domain_states 테이블이 아직 없으면(v1 이전 마이그레이션 중) 아무것도 하지 않는다.
func (s *Store) syncDomainClaims(ctx context.Context, instanceID string, old *Spec, next []Domain) error {
	if !s.tableExists(ctx, "domain_states") {
		return nil
	}
	before := map[string]bool{}
	if old != nil {
		for _, d := range old.Domains {
			before[claimHost(d.Host)] = true
		}
	}
	after := map[string]bool{}
	for _, d := range next {
		if h := claimHost(d.Host); h != "" {
			after[h] = true
		}
	}
	for h := range before {
		if !after[h] {
			if _, err := s.q.ExecContext(ctx, `DELETE FROM domain_states WHERE host=? AND instance_id=?`, h, instanceID); err != nil {
				return err
			}
		}
	}
	now := ts(time.Now())
	for h := range after {
		if before[h] {
			if _, err := s.q.ExecContext(ctx, `INSERT OR IGNORE INTO domain_states (host, instance_id, created_at) VALUES (?,?,?)`, h, instanceID, now); err != nil {
				return err
			}
			continue
		}
		var owner string
		err := s.q.QueryRowContext(ctx, `SELECT instance_id FROM domain_states WHERE host=?`, h).Scan(&owner)
		switch {
		case err == nil && owner == instanceID:
			continue
		case err == nil:
			return &HostClaimedError{Host: h, InstanceID: owner}
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if _, err := s.q.ExecContext(ctx, `INSERT INTO domain_states (host, instance_id, created_at) VALUES (?,?,?)`, h, instanceID, now); err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint") || strings.Contains(err.Error(), "PRIMARY KEY") {
				return &HostClaimedError{Host: h}
			}
			return err
		}
	}
	return nil
}

// instanceSpecForUpdate는 claim 비교용으로 지금 저장된 스펙을 읽는다(없으면 nil).
func (s *Store) instanceSpecForUpdate(ctx context.Context, id string) (*Spec, error) {
	var raw string
	err := s.q.QueryRowContext(ctx, `SELECT spec FROM instances WHERE id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sp Spec
	json.Unmarshal([]byte(raw), &sp)
	return &sp, nil
}

const domainStateCols = `host, instance_id, verify_method, verified_at, dns_status, proxy, observed, cert_status, cert_not_after, last_error, checked_at, created_at, COALESCE(ipv6,'')`

func scanDomainState(r scanner) (*DomainState, error) {
	var d DomainState
	var verified, certNA, lastErr, checked sql.NullString
	var observed, created string
	if err := r.Scan(&d.Host, &d.InstanceID, &d.VerifyMethod, &verified, &d.DNSStatus, &d.Proxy, &observed, &d.CertStatus, &certNA, &lastErr, &checked, &created, &d.IPv6); err != nil {
		return nil, notFound(err)
	}
	d.VerifiedAt, d.CertNotAfter, d.CheckedAt = parseNullTS(verified), parseNullTS(certNA), parseNullTS(checked)
	d.LastError = lastErr.String
	d.CreatedAt = parseTS(created)
	json.Unmarshal([]byte(observed), &d.Observed)
	if d.Observed == nil {
		d.Observed = []ObservedRR{}
	}
	return &d, nil
}

// GetDomainState는 호스트의 claim·상태 행이다.
func (s *Store) GetDomainState(ctx context.Context, host string) (*DomainState, error) {
	return scanDomainState(s.q.QueryRowContext(ctx, `SELECT `+domainStateCols+` FROM domain_states WHERE host=?`, claimHost(host)))
}

// ListDomainStates는 모든 claim·상태 행이다(호스트 순).
func (s *Store) ListDomainStates(ctx context.Context) ([]*DomainState, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+domainStateCols+` FROM domain_states ORDER BY host`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*DomainState{}
	for rows.Next() {
		d, err := scanDomainState(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DomainCheck는 상태 확인 한 번의 결과다(netcheck가 쓴다).
type DomainCheck struct {
	DNSStatus string
	Proxy     string
	Observed  []ObservedRR
	// VerifyMethod가 비어 있지 않으면 이번 확인으로 검증됐다는 뜻이다(이미 검증된 호스트는 방법·시각을 바꾸지 않는다).
	VerifyMethod string
	CertStatus   string
	CertNotAfter *time.Time
	LastError    string
	CheckedAt    time.Time
}

// RecordDomainCheck는 확인 결과를 저장한다. 그 사이 호스트가 다른 인스턴스로 옮겨졌거나 지워졌으면 아무것도 하지 않는다.
// 인증서 정보는 이번 확인에서 알아낸 경우(CertStatus가 비어 있지 않을 때)만 덮는다.
func (s *Store) RecordDomainCheck(ctx context.Context, host, instanceID string, c DomainCheck) error {
	obs := c.Observed
	if obs == nil {
		obs = []ObservedRR{}
	}
	var lastErr any
	if c.LastError != "" {
		lastErr = c.LastError
	}
	var vm, va any
	if c.VerifyMethod != "" {
		vm, va = c.VerifyMethod, ts(c.CheckedAt)
	}
	var certNA any
	if c.CertNotAfter != nil {
		certNA = ts(*c.CertNotAfter)
	}
	_, err := s.q.ExecContext(ctx, `UPDATE domain_states SET dns_status=?, proxy=?, observed=?, last_error=?, checked_at=?,
		verify_method=CASE WHEN verified_at IS NULL AND ? IS NOT NULL THEN ? ELSE verify_method END,
		verified_at=CASE WHEN verified_at IS NULL AND ? IS NOT NULL THEN ? ELSE verified_at END,
		cert_status=CASE WHEN ?<>'' THEN ? ELSE cert_status END,
		cert_not_after=CASE WHEN ?<>'' THEN ? ELSE cert_not_after END
		WHERE host=? AND instance_id=?`,
		c.DNSStatus, c.Proxy, jsonStr(obs), lastErr, ts(c.CheckedAt),
		vm, vm, va, va,
		c.CertStatus, c.CertStatus, c.CertStatus, certNA,
		claimHost(host), instanceID)
	return err
}

// DomainToken은 호스트의 소유 확인 토큰이다: HMAC-SHA256(마스터 키, "domain:"+host)의 앞 32자(hex).
// TXT _keel-verify.<host> = "keel-verify=<token>", HTTP /.well-known/keel/domain-check = "keel-domain-check=<token>".
// 마스터 키가 없으면(테스트) 빈 키로 만든다.
func (s *Store) DomainToken(host string) string {
	msg := "domain:" + claimHost(host)
	var sum []byte
	if b := *s.box; b != nil {
		sum = b.MAC(msg)
	} else {
		m := hmac.New(sha256.New, nil)
		m.Write([]byte(msg))
		sum = m.Sum(nil)
	}
	return hex.EncodeToString(sum)[:32]
}

// RenameInstanceDomain은 인스턴스 도메인 하나의 호스트를 oldHost에서 newHost로 바꾼다(생성 도메인 이름 변경).
// in.Domains는 호출자가 이미 새 호스트로 바꾼 값이다. 한 트랜잭션에서 다음을 함께 한다:
//   - domain_states claim 행을 새 호스트로 옮긴다(소유·확인 방법·생성 시각은 유지, DNS·인증서 관찰 값은 새 이름으로 다시 확인하도록 비운다).
//     새 호스트를 다른 인스턴스가 가졌으면 *HostClaimedError(ErrConflict).
//   - basic auth 해시(domain_auth:<inst>:<host>)를 새 키로 옮긴다.
//   - 스펙을 쓴다(UpdateInstanceConfig, claim 동기화 포함).
func (s *Store) RenameInstanceDomain(ctx context.Context, in *Instance, oldHost, newHost string) error {
	oldH, newH := claimHost(oldHost), claimHost(newHost)
	return s.Tx(ctx, func(tx *Store) error {
		if tx.tableExists(ctx, "domain_states") && oldH != newH {
			var owner string
			err := tx.q.QueryRowContext(ctx, `SELECT instance_id FROM domain_states WHERE host=?`, newH).Scan(&owner)
			switch {
			case err == nil && owner != in.ID:
				return &HostClaimedError{Host: newH, InstanceID: owner}
			case err == nil:
				// 같은 인스턴스에 남은 행(예전 경합 등): 옮겨 오는 행이 이긴다.
				if _, err := tx.q.ExecContext(ctx, `DELETE FROM domain_states WHERE host=? AND instance_id=?`, newH, in.ID); err != nil {
					return err
				}
			case !errors.Is(err, sql.ErrNoRows):
				return err
			}
			if _, err := tx.q.ExecContext(ctx, `UPDATE domain_states SET host=?, dns_status=?, proxy='', observed='[]', cert_status='',
				cert_not_after=NULL, last_error=NULL, checked_at=NULL WHERE host=? AND instance_id=?`, newH, DNSPending, oldH, in.ID); err != nil {
				return err
			}
		}
		oldKey, newKey := DomainAuthKey(in.ID, oldHost), DomainAuthKey(in.ID, newHost)
		if oldKey != newKey {
			if _, err := tx.q.ExecContext(ctx, `DELETE FROM settings WHERE key=?`, newKey); err != nil {
				return err
			}
			if _, err := tx.q.ExecContext(ctx, `UPDATE settings SET key=? WHERE key=?`, newKey, oldKey); err != nil {
				return err
			}
		}
		return tx.UpdateInstanceConfig(ctx, in)
	})
}
