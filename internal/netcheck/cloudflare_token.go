package netcheck

import (
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

const cloudflareAPIBase = "https://api.cloudflare.com/client/v4"

type cloudflareZoneList struct {
	Success bool `json:"success"`
	Result  []struct {
		Name string `json:"name"`
	} `json:"result"`
}

// VerifyCloudflareZone checks that a token can read a Cloudflare zone covering host.
// The token is used only for this request and is never retained.
func VerifyCloudflareZone(ctx context.Context, host, token string) error {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 4096 {
		return errors.New("enter a valid Cloudflare API token")
	}
	for _, r := range token {
		if r < 0x21 || r > 0x7e {
			return errors.New("the Cloudflare API token contains unsupported characters")
		}
	}

	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	host = strings.TrimPrefix(host, "*.")
	if !validCloudflareHostname(host) {
		return errors.New("this domain cannot be checked with Cloudflare")
	}
	labels := strings.Split(host, ".")
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	permissionDenied := false

	// Any accessible parent zone covers this host. Start with the shortest
	// suffix so deeply nested hostnames usually need only one API request.
	for i := len(labels) - 2; i >= 0; i-- {
		zoneName := strings.Join(labels[i:], ".")
		q := url.Values{"name": {zoneName}, "per_page": {"50"}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cloudflareAPIBase+"/zones?"+q.Encode(), nil)
		if err != nil {
			return errors.New("could not prepare a Cloudflare zone check")
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return errors.New("could not reach the Cloudflare API")
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if readErr != nil {
			return errors.New("could not read the Cloudflare API response")
		}
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return errors.New("the Cloudflare API token is invalid or expired")
		case http.StatusForbidden:
			permissionDenied = true
			continue
		case http.StatusOK:
		default:
			return fmt.Errorf("Cloudflare zone check failed (HTTP %d)", resp.StatusCode)
		}

		var result cloudflareZoneList
		if err := json.Unmarshal(body, &result); err != nil || !result.Success {
			return errors.New("Cloudflare rejected the zone check")
		}
		for _, zone := range result.Result {
			name := strings.ToLower(strings.TrimSuffix(zone.Name, "."))
			if name == zoneName && (host == name || strings.HasSuffix(host, "."+name)) {
				return nil
			}
		}
	}

	if permissionDenied {
		return errors.New("the Cloudflare API token needs Zone:Read access to this domain's zone")
	}
	return errors.New("the Cloudflare API token cannot access a zone covering this domain; check its Zone:Read permission and zone scope")
}

func validCloudflareHostname(host string) bool {
	if len(host) == 0 || len(host) > 253 || !strings.Contains(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}
