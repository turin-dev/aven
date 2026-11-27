// Package cloudflare는 Aven이 쓰는 Cloudflare API v4의 일부(토큰 확인, 존, DNS 레코드, Load Balancing)다.
// 요청은 API 토큰(Bearer)으로 보낸다. 토큰은 호출자가 넘기며 이 패키지는 저장하거나 로그에 남기지 않는다.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL은 Cloudflare API 주소다.
const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

// ManagedMarker는 Aven이 만든 레코드의 comment, 로드밸런서·pool·monitor의 description 앞부분이다.
// 이 표시가 있는 것만 바꾸거나 지운다.
const ManagedMarker = "aven:managed"

// IsManaged는 comment/description이 Aven이 만든 것인지다.
func IsManaged(s string) bool { return strings.HasPrefix(s, ManagedMarker) }

// Client는 API 클라이언트다.
type Client struct {
	Token   string
	BaseURL string // 비우면 DefaultBaseURL
	HTTP    *http.Client
}

// Error는 API가 돌려준 오류다.
type Error struct {
	Status int
	Codes  []int
	Msg    string
}

func (e *Error) Error() string {
	if e.Msg == "" {
		return fmt.Sprintf("cloudflare: HTTP %d", e.Status)
	}
	return "cloudflare: " + e.Msg
}

// Forbidden은 권한이 없거나 기능을 쓸 수 없는(구독 없음) 응답인지다.
func Forbidden(err error) bool {
	var e *Error
	return errors.As(err, &e) && (e.Status == http.StatusForbidden || e.Status == http.StatusUnauthorized || e.Status == http.StatusPaymentRequired)
}

type envelope struct {
	Success    bool            `json:"success"`
	Errors     []apiMsg        `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo *struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

type apiMsg struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body, out any) (*envelope, error) {
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	u := strings.TrimRight(base, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("User-Agent", "aven")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloudflare: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("cloudflare: %s %s: %w", method, path, err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, &Error{Status: resp.StatusCode, Msg: fmt.Sprintf("%s %s: HTTP %d (not a JSON API response)", method, path, resp.StatusCode)}
	}
	if !env.Success || resp.StatusCode >= 300 {
		e := &Error{Status: resp.StatusCode}
		var msgs []string
		for _, m := range env.Errors {
			e.Codes = append(e.Codes, m.Code)
			msgs = append(msgs, fmt.Sprintf("%s (%d)", m.Message, m.Code))
		}
		e.Msg = fmt.Sprintf("%s %s: HTTP %d", method, path, resp.StatusCode)
		if len(msgs) > 0 {
			e.Msg += ": " + strings.Join(msgs, "; ")
		}
		return nil, e
	}
	if out != nil && len(env.Result) > 0 && string(env.Result) != "null" {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return nil, fmt.Errorf("cloudflare: %s %s: %w", method, path, err)
		}
	}
	return &env, nil
}

// list는 페이지를 모두 읽는다(최대 maxPages).
func list[T any](ctx context.Context, c *Client, path string, q url.Values, perPage int) ([]T, error) {
	const maxPages = 200
	var all []T
	if q == nil {
		q = url.Values{}
	}
	for page := 1; page <= maxPages; page++ {
		q.Set("page", fmt.Sprint(page))
		if perPage > 0 {
			q.Set("per_page", fmt.Sprint(perPage))
		}
		var items []T
		env, err := c.do(ctx, http.MethodGet, path, q, nil, &items)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if env.ResultInfo == nil || env.ResultInfo.TotalPages <= page || len(items) == 0 {
			return all, nil
		}
	}
	return all, nil
}

// VerifyToken은 토큰이 유효하고 active인지 확인한다.
func (c *Client) VerifyToken(ctx context.Context) error {
	var r struct {
		Status string `json:"status"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/user/tokens/verify", nil, nil, &r); err != nil {
		return err
	}
	if r.Status != "active" {
		return fmt.Errorf("cloudflare: token status is %q", r.Status)
	}
	return nil
}

// Zone은 토큰으로 볼 수 있는 존이다.
type Zone struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Account struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"account"`
	Plan struct {
		Name string `json:"name"`
	} `json:"plan"`
}

// Zones는 토큰이 볼 수 있는 존 전체다.
func (c *Client) Zones(ctx context.Context) ([]Zone, error) {
	return list[Zone](ctx, c, "/zones", nil, 50)
}

// DNSRecord는 DNS 레코드다.
type DNSRecord struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment,omitempty"`
}

// DNSRecords는 존의 레코드 전체다.
func (c *Client) DNSRecords(ctx context.Context, zoneID string) ([]DNSRecord, error) {
	return list[DNSRecord](ctx, c, "/zones/"+url.PathEscape(zoneID)+"/dns_records", nil, 500)
}

func (c *Client) CreateDNSRecord(ctx context.Context, zoneID string, r DNSRecord) (DNSRecord, error) {
	var out DNSRecord
	r.ID = ""
	_, err := c.do(ctx, http.MethodPost, "/zones/"+url.PathEscape(zoneID)+"/dns_records", nil, r, &out)
	return out, err
}

func (c *Client) UpdateDNSRecord(ctx context.Context, zoneID string, r DNSRecord) (DNSRecord, error) {
	var out DNSRecord
	id := r.ID
	r.ID = ""
	_, err := c.do(ctx, http.MethodPut, "/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(id), nil, r, &out)
	return out, err
}

func (c *Client) DeleteDNSRecord(ctx context.Context, zoneID, id string) error {
	_, err := c.do(ctx, http.MethodDelete, "/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(id), nil, nil, nil)
	return err
}

// LoadBalancer는 존의 로드밸런서다(Name은 호스트 이름).
type LoadBalancer struct {
	ID             string   `json:"id,omitempty"`
	Name           string   `json:"name"`
	Description    string   `json:"description,omitempty"`
	DefaultPools   []string `json:"default_pools"`
	FallbackPool   string   `json:"fallback_pool"`
	Proxied        bool     `json:"proxied"`
	SteeringPolicy string   `json:"steering_policy,omitempty"`
	TTL            int      `json:"ttl,omitempty"`
	Enabled        *bool    `json:"enabled,omitempty"`
}

func (c *Client) LoadBalancers(ctx context.Context, zoneID string) ([]LoadBalancer, error) {
	return list[LoadBalancer](ctx, c, "/zones/"+url.PathEscape(zoneID)+"/load_balancers", nil, 0)
}

func (c *Client) CreateLoadBalancer(ctx context.Context, zoneID string, lb LoadBalancer) (LoadBalancer, error) {
	var out LoadBalancer
	lb.ID = ""
	_, err := c.do(ctx, http.MethodPost, "/zones/"+url.PathEscape(zoneID)+"/load_balancers", nil, lb, &out)
	return out, err
}

func (c *Client) UpdateLoadBalancer(ctx context.Context, zoneID string, lb LoadBalancer) (LoadBalancer, error) {
	var out LoadBalancer
	id := lb.ID
	lb.ID = ""
	_, err := c.do(ctx, http.MethodPut, "/zones/"+url.PathEscape(zoneID)+"/load_balancers/"+url.PathEscape(id), nil, lb, &out)
	return out, err
}

func (c *Client) DeleteLoadBalancer(ctx context.Context, zoneID, id string) error {
	_, err := c.do(ctx, http.MethodDelete, "/zones/"+url.PathEscape(zoneID)+"/load_balancers/"+url.PathEscape(id), nil, nil, nil)
	return err
}

// Origin은 pool의 endpoint다.
type Origin struct {
	Name    string  `json:"name"`
	Address string  `json:"address"`
	Enabled bool    `json:"enabled"`
	Weight  float64 `json:"weight"`
}

// Pool은 계정의 pool이다. Latitude/Longitude는 proximity steering에 쓴다.
type Pool struct {
	ID             string   `json:"id,omitempty"`
	Name           string   `json:"name"`
	Description    string   `json:"description,omitempty"`
	Enabled        bool     `json:"enabled"`
	MinimumOrigins int      `json:"minimum_origins"`
	Monitor        string   `json:"monitor,omitempty"`
	Origins        []Origin `json:"origins"`
	Latitude       *float64 `json:"latitude,omitempty"`
	Longitude      *float64 `json:"longitude,omitempty"`
}

func (c *Client) Pools(ctx context.Context, accountID string) ([]Pool, error) {
	return list[Pool](ctx, c, "/accounts/"+url.PathEscape(accountID)+"/load_balancers/pools", nil, 0)
}

func (c *Client) CreatePool(ctx context.Context, accountID string, p Pool) (Pool, error) {
	var out Pool
	p.ID = ""
	_, err := c.do(ctx, http.MethodPost, "/accounts/"+url.PathEscape(accountID)+"/load_balancers/pools", nil, p, &out)
	return out, err
}

func (c *Client) UpdatePool(ctx context.Context, accountID string, p Pool) (Pool, error) {
	var out Pool
	id := p.ID
	p.ID = ""
	_, err := c.do(ctx, http.MethodPut, "/accounts/"+url.PathEscape(accountID)+"/load_balancers/pools/"+url.PathEscape(id), nil, p, &out)
	return out, err
}

func (c *Client) DeletePool(ctx context.Context, accountID, id string) error {
	_, err := c.do(ctx, http.MethodDelete, "/accounts/"+url.PathEscape(accountID)+"/load_balancers/pools/"+url.PathEscape(id), nil, nil, nil)
	return err
}

// Monitor는 계정의 health monitor다.
type Monitor struct {
	ID          string `json:"id,omitempty"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Port        int    `json:"port,omitempty"`
	Interval    int    `json:"interval,omitempty"`
	Retries     int    `json:"retries,omitempty"`
	Timeout     int    `json:"timeout,omitempty"`
}

func (c *Client) Monitors(ctx context.Context, accountID string) ([]Monitor, error) {
	return list[Monitor](ctx, c, "/accounts/"+url.PathEscape(accountID)+"/load_balancers/monitors", nil, 0)
}

func (c *Client) CreateMonitor(ctx context.Context, accountID string, m Monitor) (Monitor, error) {
	var out Monitor
	m.ID = ""
	_, err := c.do(ctx, http.MethodPost, "/accounts/"+url.PathEscape(accountID)+"/load_balancers/monitors", nil, m, &out)
	return out, err
}

func (c *Client) DeleteMonitor(ctx context.Context, accountID, id string) error {
	_, err := c.do(ctx, http.MethodDelete, "/accounts/"+url.PathEscape(accountID)+"/load_balancers/monitors/"+url.PathEscape(id), nil, nil, nil)
	return err
}
