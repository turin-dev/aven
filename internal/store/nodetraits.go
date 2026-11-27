package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// NodeTraits는 운영자가 서버를 추가할 때 적는, 클라우드 콘솔에는 없는 그 기계의 사정이다.
// 집 PC인지, 전기요금이 얼마인지, 회선이 어떤지처럼 agent가 스스로 알 수 없는 값이라 사람이 입력한다.
// 모든 필드는 선택이다. 0/빈 값은 "모름"이다.
type NodeTraits struct {
	// Site는 기계가 놓인 곳이다: datacenter | home | office | other.
	Site string `json:"site,omitempty"`
	// PowerPricePerKWh는 1kWh당 전기요금이다(PowerCurrency 단위). nil이면 모름.
	PowerPricePerKWh *float64 `json:"power_price_per_kwh,omitempty"`
	// PowerCurrency는 ISO 4217 통화 코드다(예: KRW, EUR, USD).
	PowerCurrency string `json:"power_currency,omitempty"`
	// UPS는 정전 때 버티는 무정전 전원이 있는지다.
	UPS bool `json:"ups,omitempty"`
	// Connection은 회선 종류다: fiber | cable | dsl | mobile | satellite | datacenter | other.
	Connection string `json:"connection,omitempty"`
	// DownlinkMbps / UplinkMbps는 계약(또는 실측) 대역폭이다.
	DownlinkMbps int `json:"downlink_mbps,omitempty"`
	UplinkMbps   int `json:"uplink_mbps,omitempty"`
	// DataCapGB는 월 데이터 상한이다. 0이면 무제한이거나 모름.
	DataCapGB int `json:"data_cap_gb,omitempty"`
	// DynamicIP는 공인 IP가 바뀔 수 있는 회선인지다.
	DynamicIP bool `json:"dynamic_ip,omitempty"`
	// CGNAT은 통신사 NAT 뒤라 들어오는 연결을 받을 수 없는지다(보통 Cloudflare 터널이 필요하다).
	CGNAT bool `json:"cgnat,omitempty"`
	// Note는 자유 메모다("거실 미니PC", "밤에는 꺼짐" 등).
	Note string `json:"note,omitempty"`
}

var (
	NodeSites       = []string{"datacenter", "home", "office", "other"}
	NodeConnections = []string{"fiber", "cable", "dsl", "mobile", "satellite", "datacenter", "other"}
	currencyRe      = regexp.MustCompile(`^[A-Z]{3}$`)
)

func oneOf(v string, set []string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

// Normalize는 공백과 대소문자를 정리하고 값을 검증한다.
func (t *NodeTraits) Normalize() error {
	t.Site = strings.ToLower(strings.TrimSpace(t.Site))
	t.Connection = strings.ToLower(strings.TrimSpace(t.Connection))
	t.PowerCurrency = strings.ToUpper(strings.TrimSpace(t.PowerCurrency))
	t.Note = strings.TrimSpace(t.Note)
	if t.Site != "" && !oneOf(t.Site, NodeSites) {
		return fmt.Errorf("site must be one of %s", strings.Join(NodeSites, ", "))
	}
	if t.Connection != "" && !oneOf(t.Connection, NodeConnections) {
		return fmt.Errorf("connection must be one of %s", strings.Join(NodeConnections, ", "))
	}
	if p := t.PowerPricePerKWh; p != nil {
		if math.IsNaN(*p) || math.IsInf(*p, 0) || *p < 0 || *p > 1_000_000 {
			return errors.New("power_price_per_kwh must be between 0 and 1000000")
		}
		if t.PowerCurrency == "" {
			return errors.New("power_currency is required with power_price_per_kwh")
		}
	}
	if t.PowerCurrency != "" && !currencyRe.MatchString(t.PowerCurrency) {
		return errors.New("power_currency must be a 3-letter ISO 4217 code")
	}
	for _, v := range []struct {
		name string
		n    int
	}{{"downlink_mbps", t.DownlinkMbps}, {"uplink_mbps", t.UplinkMbps}, {"data_cap_gb", t.DataCapGB}} {
		if v.n < 0 || v.n > 10_000_000 {
			return fmt.Errorf("%s must be between 0 and 10000000", v.name)
		}
	}
	if utf8.RuneCountInString(t.Note) > 200 {
		return errors.New("note must be at most 200 characters")
	}
	return nil
}

// migrateNodeTraits는 노드와 가입 토큰에 특성(JSON) 열을 더한다. 토큰에 담긴 특성은 가입하는 노드로 옮겨진다.
// schema_version을 되돌려 다시 돌려도 되도록 이미 있는 열은 건너뛴다.
func migrateNodeTraits(ctx context.Context, tx *Store) error {
	for _, table := range []string{"nodes", "node_join_tokens"} {
		cols, err := tx.tableColumns(ctx, table)
		if err != nil {
			return err
		}
		if slices.Contains(cols, "traits") {
			continue
		}
		if _, err := tx.q.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN traits TEXT NOT NULL DEFAULT '{}'`); err != nil {
			return err
		}
	}
	return nil
}

func parseTraits(raw string) NodeTraits {
	var t NodeTraits
	json.Unmarshal([]byte(raw), &t)
	return t
}

// SetNodeTraits는 노드(로컬 포함)의 특성을 통째로 바꾼다. 검증은 호출자가 한다(Normalize).
func (s *Store) SetNodeTraits(ctx context.Context, id string, t NodeTraits) error {
	return affected(s.q.ExecContext(ctx, `UPDATE nodes SET traits=? WHERE id=?`, jsonStr(t), NormNode(id)))
}
