package store

import (
	"context"
	"database/sql"
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
	// IdleWatts / MaxWatts는 기계 전체의 소비전력(W)이다: 놀 때와 CPU를 다 쓸 때. 전기료 추정(EstimatePower)에 쓴다.
	IdleWatts int `json:"idle_watts,omitempty"`
	MaxWatts  int `json:"max_watts,omitempty"`
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
	}{{"downlink_mbps", t.DownlinkMbps}, {"uplink_mbps", t.UplinkMbps}, {"data_cap_gb", t.DataCapGB}, {"idle_watts", t.IdleWatts}, {"max_watts", t.MaxWatts}} {
		if v.n < 0 || v.n > 10_000_000 {
			return fmt.Errorf("%s must be between 0 and 10000000", v.name)
		}
	}
	if t.IdleWatts > 100_000 || t.MaxWatts > 100_000 {
		return errors.New("idle_watts and max_watts must be at most 100000")
	}
	if t.IdleWatts > 0 && t.MaxWatts > 0 && t.IdleWatts > t.MaxWatts {
		return errors.New("idle_watts must not be greater than max_watts")
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

// HoursPerMonth는 월 추정에 쓰는 시간이다(365일 × 24시간 ÷ 12).
const HoursPerMonth = 730.0

// PowerEstimate는 최근 CPU 사용률과 운영자가 적은 소비전력으로 어림한 월 전기 사용량과 요금이다.
type PowerEstimate struct {
	// Model은 linear(대기~최대 사이를 CPU 사용률로 보간) 또는 flat(값 하나만 적혀 늘 그만큼 쓴다고 본다)이다.
	Model         string  `json:"model"`
	AvgCPUPercent float64 `json:"avg_cpu_percent"`
	AvgWatts      float64 `json:"avg_watts"`
	KWhMonth      float64 `json:"kwh_month"`
	// CostMonth는 power_price_per_kwh가 있을 때만 채운다.
	CostMonth *float64 `json:"cost_month,omitempty"`
	Currency  string   `json:"currency,omitempty"`
	// WindowSeconds는 평균을 낸 지표 구간의 길이다(최대 7일).
	WindowSeconds int64 `json:"window_s"`
}

// EstimatePower는 평균 CPU 사용률(0..100)로 월 전기 사용량을 어림한다. 소비전력이 적혀 있지 않으면 nil이다.
func EstimatePower(t NodeTraits, avgCPU float64, windowS int64) *PowerEstimate {
	if t.IdleWatts <= 0 && t.MaxWatts <= 0 {
		return nil
	}
	avgCPU = math.Max(0, math.Min(100, avgCPU))
	e := &PowerEstimate{AvgCPUPercent: math.Round(avgCPU*10) / 10, WindowSeconds: windowS}
	switch {
	case t.IdleWatts > 0 && t.MaxWatts > 0:
		e.Model = "linear"
		e.AvgWatts = float64(t.IdleWatts) + float64(t.MaxWatts-t.IdleWatts)*avgCPU/100
	case t.MaxWatts > 0:
		e.Model, e.AvgWatts = "flat", float64(t.MaxWatts)
	default:
		e.Model, e.AvgWatts = "flat", float64(t.IdleWatts)
	}
	e.KWhMonth = e.AvgWatts * HoursPerMonth / 1000
	if t.PowerPricePerKWh != nil && t.PowerCurrency != "" {
		c := e.KWhMonth * *t.PowerPricePerKWh
		e.CostMonth, e.Currency = &c, t.PowerCurrency
	}
	e.AvgWatts = math.Round(e.AvgWatts*10) / 10
	e.KWhMonth = math.Round(e.KWhMonth*10) / 10
	return e
}

// NodeAvgCPU는 from 이후 노드 CPU 사용률의 시간 가중 평균과, 평균을 낸 구간 길이(초)다.
func (s *Store) NodeAvgCPU(ctx context.Context, nodeID string, from int64) (avg float64, windowS int64, err error) {
	var sum, w sql.NullFloat64
	err = s.q.QueryRowContext(ctx, `SELECT SUM(cpu*res), SUM(res) FROM node_metrics WHERE node_id=? AND t>=?`, NormNode(nodeID), from).Scan(&sum, &w)
	if err != nil || !w.Valid || w.Float64 <= 0 {
		return 0, 0, err
	}
	return sum.Float64 / w.Float64, int64(w.Float64), nil
}
