package imageupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const manifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json"

var challengeParam = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9_-]*)="((?:\\.|[^"])*)"`)

type Auth struct {
	Username string
	Password string
}

type Candidate struct {
	Image       string // image tag saved in the instance settings
	Digest      string
	DigestImage string // immutable reference used for the deployment
}

type Client struct {
	HTTP *http.Client
	Auth *Auth
	base string // registry API origin override used by tests
}

func NewClient(auth *Auth) *Client {
	return &Client{HTTP: &http.Client{Timeout: 20 * time.Second}, Auth: auth}
}

type Discovery struct {
	CurrentDigest string
	Candidate     *Candidate
}

// Discover checks the configured source tag. Full semantic-version tags move only
// within their current major line; mutable tags keep their name and compare digests.
func (c *Client) Discover(ctx context.Context, image, policy, previousDigest string) (Discovery, error) {
	r, err := Parse(image)
	if err != nil {
		return Discovery{}, err
	}
	if r.Digest != "" {
		return Discovery{}, nil // digest references are immutable by definition
	}
	currentDigest, err := c.ManifestDigest(ctx, r, r.Tag)
	if err != nil {
		return Discovery{}, err
	}
	out := Discovery{CurrentDigest: currentDigest}
	major, minor, patch, isVersion := r.IsSemver()
	if !isVersion {
		if previousDigest != "" && currentDigest != previousDigest {
			out.Candidate = &Candidate{Image: image, Digest: currentDigest, DigestImage: r.DigestRef(currentDigest)}
		}
		return out, nil
	}
	if policy != "patch" && policy != "minor" {
		return out, nil
	}
	tags, err := c.Tags(ctx, r)
	if err != nil {
		return Discovery{}, err
	}
	var bestMajor, bestMinor, bestPatch int
	found := false
	for _, tag := range tags {
		ma, mi, pa, ok := (Reference{Tag: tag}).IsSemver()
		if !ok || ma != major || mi < minor || (mi == minor && pa <= patch) {
			continue
		}
		if policy == "patch" && mi != minor {
			continue
		}
		if !found || ma > bestMajor || (ma == bestMajor && (mi > bestMinor || (mi == bestMinor && pa > bestPatch))) {
			bestMajor, bestMinor, bestPatch, found = ma, mi, pa, true
		}
	}
	if !found {
		return out, nil
	}
	tag := r.VersionTag(bestMajor, bestMinor, bestPatch)
	digest, err := c.ManifestDigest(ctx, r, tag)
	if err != nil {
		return Discovery{}, err
	}
	if digest == currentDigest {
		return out, nil
	}
	target := r.replaceTag(tag)
	out.Candidate = &Candidate{Image: target, Digest: digest, DigestImage: r.DigestRef(digest)}
	return out, nil
}

func (r Reference) replaceTag(tag string) string {
	name := r.Original
	if before, _, ok := strings.Cut(name, "@"); ok {
		name = before
	}
	if i := strings.LastIndexByte(name, ':'); i > strings.LastIndexByte(name, '/') {
		name = name[:i]
	}
	return name + ":" + tag
}

func (c *Client) Tags(ctx context.Context, r Reference) ([]string, error) {
	base := c.baseURL(r)
	u := base + "/tags/list?n=1000"
	seen := map[string]bool{}
	tags := []string{}
	for pages := 0; u != "" && pages < 10; pages++ {
		resp, err := c.request(ctx, http.MethodGet, u, r, "")
		if err != nil {
			return nil, err
		}
		var body struct {
			Tags []string `json:"tags"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("read image tags: %w", decodeErr)
		}
		for _, tag := range body.Tags {
			if !seen[tag] {
				seen[tag] = true
				tags = append(tags, tag)
			}
		}
		u = nextLink(resp.Header.Get("Link"), base)
	}
	if u != "" {
		return nil, errors.New("registry returned too many tag pages")
	}
	sort.Strings(tags)
	return tags, nil
}

func (c *Client) ManifestDigest(ctx context.Context, r Reference, tag string) (string, error) {
	u := c.baseURL(r) + "/manifests/" + url.PathEscape(tag)
	resp, err := c.request(ctx, http.MethodHead, u, r, manifestAccept)
	if err != nil {
		return "", err
	}
	digest := resp.Header.Get("Docker-Content-Digest")
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && digest != "" {
		resp.Body.Close()
		return digest, nil
	}
	resp.Body.Close()
	resp, err = c.request(ctx, http.MethodGet, u, r, manifestAccept)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("registry manifest request returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	if digest = resp.Header.Get("Docker-Content-Digest"); digest != "" {
		return digest, nil
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (c *Client) baseURL(r Reference) string {
	origin := "https://" + r.Registry
	if r.Registry == "docker.io" {
		origin = "https://registry-1.docker.io"
	}
	if c.base != "" {
		origin = c.base
	}
	return origin + "/v2/" + r.Repository
}

func (c *Client) request(ctx context.Context, method, rawURL string, r Reference, accept string) (*http.Response, error) {
	makeRequest := func(token string, basic bool) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
		if err != nil {
			return nil, err
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		} else if basic && c.Auth != nil {
			req.SetBasicAuth(c.Auth.Username, c.Auth.Password)
		}
		return c.http().Do(req)
	}
	resp, err := makeRequest("", false)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		if err == nil && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
			resp.Body.Close()
			return nil, fmt.Errorf("registry request returned %s", resp.Status)
		}
		return resp, err
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	resp.Body.Close()
	scheme, params := parseChallenge(challenge)
	switch strings.ToLower(scheme) {
	case "bearer":
		access, err := c.bearerToken(ctx, params, r)
		if err != nil {
			return nil, err
		}
		resp, err = makeRequest(access, false)
	case "basic":
		resp, err = makeRequest("", true)
	default:
		return nil, fmt.Errorf("registry requested unsupported authentication: %s", scheme)
	}
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("registry request returned %s", resp.Status)
	}
	return resp, nil
}

func (c *Client) bearerToken(ctx context.Context, params map[string]string, r Reference) (string, error) {
	realm := params["realm"]
	u, err := url.Parse(realm)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", errors.New("registry provided an invalid bearer-token endpoint")
	}
	q := u.Query()
	if service := params["service"]; service != "" {
		q.Set("service", service)
	}
	scope := params["scope"]
	if scope == "" {
		scope = "repository:" + r.Repository + ":pull"
	}
	q.Set("scope", scope)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	if c.Auth != nil {
		req.SetBasicAuth(c.Auth.Username, c.Auth.Password)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("registry token request returned %s", resp.Status)
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", err
	}
	if body.Token == "" {
		body.Token = body.AccessToken
	}
	if body.Token == "" {
		return "", errors.New("registry token response was empty")
	}
	return body.Token, nil
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func parseChallenge(v string) (string, map[string]string) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(v), " ")
	params := map[string]string{}
	for _, m := range challengeParam.FindAllStringSubmatch(rest, -1) {
		value, _ := strconv.Unquote(`"` + m[2] + `"`)
		params[m[1]] = value
	}
	return scheme, params
}

func nextLink(header, base string) string {
	for _, part := range strings.Split(header, ",") {
		urlPart, rest, ok := strings.Cut(strings.TrimSpace(part), ";")
		if !ok || !strings.Contains(strings.ToLower(rest), `rel="next"`) {
			continue
		}
		raw := strings.Trim(strings.TrimSpace(urlPart), "<>")
		u, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		b, _ := url.Parse(base)
		if u.IsAbs() {
			if u.Scheme != b.Scheme || u.Host != b.Host {
				return "" // never send registry credentials to a different origin
			}
			return u.String()
		}
		return b.ResolveReference(u).String()
	}
	return ""
}
