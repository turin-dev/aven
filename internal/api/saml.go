package api

import (
	"context"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/beevik/etree"
	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"
	dsig "github.com/russellhaering/goxmldsig"
	"github.com/turin-dev/aven/internal/store"
)

const (
	samlRequestTTL = 10 * time.Minute
	samlMaxXMLSize = 1 << 20
	samlACSEntity  = "/api/auth/saml/acs"
	samlMetaEntity = "/api/auth/saml/metadata"
)

type samlSettingsInput struct {
	MetadataURL   *string   `json:"metadata_url"`
	MetadataXML   string    `json:"metadata_xml"`
	ClearMetadata bool      `json:"clear_metadata"`
	Domains       *[]string `json:"domains"`
	DefaultRole   *string   `json:"default_role"`
	EnforceSSO    *bool     `json:"enforce_sso"`
}

func (s *Server) samlStatus(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, 200, map[string]any{"configured": false, "enforce_sso": false})
		return
	}
	cfg, err := s.Store.GetSAMLConfig(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	// 로그인 화면용 공개 응답이라 도메인 목록 등은 노출하지 않는다.
	writeJSON(w, 200, map[string]any{"configured": cfg.Configured && hasVerifiedSAMLDomain(cfg), "enforce_sso": cfg.EnforceSSO})
}

func (s *Server) getSAMLSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.Store.GetSAMLConfig(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	base := strings.TrimRight(s.publicBase(r), "/")
	writeJSON(w, 200, map[string]any{
		"config":          cfg,
		"sp_metadata_url": base + samlMetaEntity,
		"acs_url":         base + samlACSEntity,
		"login_url":       base + "/api/auth/saml/login",
		"idp":             samlIdPSummary(cfg),
	})
}

// samlIdPInfo는 설정 화면에 보여 줄 IdP 메타데이터 요약이다(서명 인증서 만료 경고용).
type samlIdPInfo struct {
	EntityID     string     `json:"entity_id"`
	SSOURL       string     `json:"sso_url"`
	CertNotAfter *time.Time `json:"cert_not_after,omitempty"`
}

func samlIdPSummary(cfg *store.SAMLConfig) *samlIdPInfo {
	if cfg.MetadataXML == "" {
		return nil
	}
	md, err := samlsp.ParseMetadata([]byte(cfg.MetadataXML))
	if err != nil || len(md.IDPSSODescriptors) == 0 {
		return nil
	}
	info := &samlIdPInfo{EntityID: md.EntityID}
	for _, d := range md.IDPSSODescriptors {
		for _, ep := range d.SingleSignOnServices {
			if info.SSOURL == "" && (ep.Binding == saml.HTTPRedirectBinding || ep.Binding == saml.HTTPPostBinding) {
				info.SSOURL = ep.Location
			}
		}
		for _, key := range d.KeyDescriptors {
			if key.Use != "" && key.Use != "signing" {
				continue
			}
			for _, c := range key.KeyInfo.X509Data.X509Certificates {
				der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(c.Data), ""))
				if err != nil {
					continue
				}
				cert, err := x509.ParseCertificate(der)
				if err != nil {
					continue
				}
				// 여러 인증서가 있으면(롤오버) 가장 늦게 만료되는 것을 기준으로 한다.
				if info.CertNotAfter == nil || cert.NotAfter.After(*info.CertNotAfter) {
					t := cert.NotAfter.UTC()
					info.CertNotAfter = &t
				}
			}
		}
	}
	return info
}

func (s *Server) putSAMLSettings(w http.ResponseWriter, r *http.Request) {
	var body samlSettingsInput
	r.Body = http.MaxBytesReader(w, r.Body, samlMaxXMLSize+16<<10)
	if !decode(w, r, &body) {
		return
	}
	cfg, err := s.Store.GetSAMLConfig(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	actor := principalFrom(r)
	wasConfigured, wasEnforced := cfg.MetadataXML != "", cfg.EnforceSSO
	if body.ClearMetadata {
		cfg.MetadataXML, cfg.MetadataURL = "", ""
	} else if body.MetadataURL != nil && strings.TrimSpace(*body.MetadataURL) != "" {
		if strings.TrimSpace(body.MetadataXML) != "" {
			writeErr(w, 400, "metadata_url과 metadata_xml 중 하나만 지정하세요")
			return
		}
		metadataURL := strings.TrimSpace(*body.MetadataURL)
		metadata, raw, err := fetchSAMLMetadata(r.Context(), metadataURL)
		if err != nil {
			writeErr(w, 400, "IdP 메타데이터 URL을 읽을 수 없습니다")
			return
		}
		if err := validateSAMLMetadata(metadata); err != nil {
			writeErr(w, 400, "유효한 SAML IdP 메타데이터가 아닙니다")
			return
		}
		cfg.MetadataXML, cfg.MetadataURL = string(raw), metadataURL
	} else if strings.TrimSpace(body.MetadataXML) != "" {
		metadataXML := strings.TrimSpace(body.MetadataXML)
		metadata, err := samlsp.ParseMetadata([]byte(metadataXML))
		if err != nil || validateSAMLMetadata(metadata) != nil {
			writeErr(w, 400, "유효한 SAML IdP 메타데이터가 아닙니다")
			return
		}
		cfg.MetadataXML, cfg.MetadataURL = metadataXML, ""
	}
	if body.Domains != nil {
		domains, err := updateSAMLVerifiedDomains(cfg.Domains, *body.Domains)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		cfg.Domains = domains
	}
	if body.DefaultRole != nil {
		role := strings.TrimSpace(*body.DefaultRole)
		// owner는 자동 등록 기본값으로 줄 수 없다(IdP 쪽 설정 실수 하나로 소유권이 넘어가지 않게).
		if !store.ValidWorkspaceRole(role) || role == store.RoleOwner || roleLevel(role) > roleLevel(actor.User.Role) {
			writeErr(w, 400, "기본 역할은 admin, member, viewer 중 하나이며 현재 관리자 역할보다 높을 수 없습니다")
			return
		}
		cfg.DefaultRole = role
	}
	if body.EnforceSSO != nil {
		cfg.EnforceSSO = *body.EnforceSSO
	}
	if cfg.EnforceSSO {
		if cfg.MetadataXML == "" {
			writeErr(w, 400, "SSO 적용 전에 IdP 메타데이터를 설정하세요")
			return
		}
		if len(cfg.Domains) == 0 || !hasVerifiedSAMLDomain(cfg) {
			writeErr(w, 400, "SSO 적용 전에 이메일 도메인을 검증하세요")
			return
		}

		if !wasEnforced && (!actor.SAML || !verifiedSAMLDomain(cfg, emailDomain(actor.User.Email))) {
			writeErr(w, 403, "검증된 도메인의 SAML 세션으로 로그인한 관리자만 SSO를 적용할 수 있습니다")
			return
		}
	}
	if cfg.DefaultRole == "" {
		cfg.DefaultRole = store.RoleMember
	}
	if err := s.Store.SaveSAMLConfig(r.Context(), cfg); err != nil {
		s.storeErr(w, err)
		return
	}
	switch isConfigured := cfg.MetadataXML != ""; {
	case !wasConfigured && isConfigured:
		s.audit(r, "saml.connected", "workspace", "SAML IdP를 연결했습니다")
	case wasConfigured && !isConfigured:
		s.audit(r, "saml.disconnected", "workspace", "SAML IdP 연결을 해제했습니다")
	}
	if wasEnforced != cfg.EnforceSSO {
		msg := "SAML SSO 적용을 해제했습니다"
		if cfg.EnforceSSO {
			msg = "SAML SSO 적용을 켰습니다"
		}
		s.audit(r, "saml.enforcement_updated", "workspace", msg)
	}
	s.audit(r, "saml.settings_updated", "workspace", "SAML SSO 설정을 업데이트했습니다")
	writeJSON(w, 200, map[string]any{"config": cfg})
}

func (s *Server) verifySAMLDomain(w http.ResponseWriter, r *http.Request) {
	domain, ok := normalizeSAMLDomain(r.PathValue("host")) // {host}: 이메일 도메인
	if !ok {
		writeErr(w, 400, "이메일 도메인이 올바르지 않습니다")
		return
	}
	cfg, err := s.Store.GetSAMLConfig(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	index := -1
	for i := range cfg.Domains {
		if cfg.Domains[i].Domain == domain {
			index = i
			break
		}
	}
	if index < 0 {
		writeErr(w, 404, "도메인을 찾을 수 없습니다")
		return
	}
	record := "_keel-sso-verification." + domain
	values, err := net.DefaultResolver.LookupTXT(r.Context(), record)
	if err != nil {
		writeErr(w, 400, "DNS TXT 레코드를 찾을 수 없습니다")
		return
	}
	verified := false
	for _, value := range values {
		if subtle.ConstantTimeCompare([]byte(value), []byte(cfg.Domains[index].Token)) == 1 {
			verified = true
			break
		}
	}
	if !verified {
		writeErr(w, 400, "DNS TXT 레코드 값이 일치하지 않습니다")
		return
	}
	cfg.Domains[index].Verified = true
	if err := s.Store.SaveSAMLConfig(r.Context(), cfg); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "saml.domain_verified", domain, domain+" 도메인을 검증했습니다")
	writeJSON(w, 200, map[string]any{"domain": cfg.Domains[index]})
}

func updateSAMLVerifiedDomains(previous []store.VerifiedDomain, requested []string) ([]store.VerifiedDomain, error) {
	old := make(map[string]store.VerifiedDomain, len(previous))
	for _, d := range previous {
		old[d.Domain] = d
	}
	if len(requested) > 50 {
		return nil, errors.New("도메인은 최대 50개까지 추가할 수 있습니다")
	}
	out := make([]store.VerifiedDomain, 0, len(requested))
	seen := map[string]bool{}
	for _, value := range requested {
		domain, ok := normalizeSAMLDomain(value)
		if !ok {
			return nil, fmt.Errorf("이메일 도메인이 올바르지 않습니다: %s", value)
		}
		if seen[domain] {
			return nil, fmt.Errorf("도메인이 중복되었습니다: %s", domain)
		}
		seen[domain] = true
		if d, exists := old[domain]; exists {
			out = append(out, d)
		} else {
			out = append(out, store.VerifiedDomain{Domain: domain, Token: "keel-sso=" + randomToken(24)})
		}
	}
	return out, nil
}

func normalizeSAMLDomain(value string) (string, bool) {
	domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	if len(domain) == 0 || len(domain) > 253 || !strings.Contains(domain, ".") {
		return "", false
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", false
			}
		}
	}
	return domain, true
}

func emailDomain(email string) string {
	_, domain, ok := strings.Cut(store.NormalizeEmail(email), "@")
	if !ok {
		return ""
	}
	return domain
}

func verifiedSAMLDomain(cfg *store.SAMLConfig, domain string) bool {
	for _, d := range cfg.Domains {
		if d.Verified && d.Domain == domain {
			return true
		}
	}
	return false
}

func hasVerifiedSAMLDomain(cfg *store.SAMLConfig) bool {
	for _, d := range cfg.Domains {
		if d.Verified {
			return true
		}
	}
	return false
}

// samlAuthAllowed는 SSO 적용 중 검증된 도메인 사용자의 비SAML 세션을 막는다(적용 즉시 재인증 요구).
// admin@local은 비상(break-glass) 계정이라 예외이고, API 토큰은 CLI/자동화용이라 세션에만 적용한다.
func (s *Server) samlAuthAllowed(ctx context.Context, w http.ResponseWriter, p *principal) bool {
	if p.User.Builtin || p.Via != viaSession {
		return true
	}
	cfg, err := s.Store.GetSAMLConfig(ctx)
	if err != nil {
		s.storeErr(w, err)
		return false
	}
	if cfg.EnforceSSO && verifiedSAMLDomain(cfg, emailDomain(p.User.Email)) && !p.SAML {
		writeErr(w, 401, "sso_required")
		return false
	}
	return true
}

func (s *Server) samlLogin(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.Store.GetSAMLConfig(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	sp, err := s.samlServiceProvider(r, cfg)
	if err != nil {
		samlFail(w, 503, "SAML SSO가 설정되지 않았습니다")
		return
	}
	binding := saml.HTTPRedirectBinding
	if sp.GetSSOBindingLocation(binding) == "" {
		binding = saml.HTTPPostBinding
	}
	request, err := sp.MakeAuthenticationRequest(sp.GetSSOBindingLocation(binding), binding, saml.HTTPPostBinding)
	if err != nil {
		samlFail(w, 503, "SAML 인증을 시작할 수 없습니다")
		return
	}
	relay := randomToken(32)
	next := safeSAMLRedirect(r.URL.Query().Get("next"))
	if err := s.Store.CreateSAMLLoginRequest(r.Context(), hashSecret(relay), request.ID, next, time.Now().Add(samlRequestTTL)); err != nil {
		s.storeErr(w, err)
		return
	}
	if request.Destination == "" {
		samlFail(w, 503, "IdP에 지원되는 SSO 바인딩이 없습니다")
		return
	}
	if sp.GetSSOBindingLocation(saml.HTTPRedirectBinding) == "" {
		// HTTP-POST 바인딩만 지원하는 IdP: 자동 제출 폼을 돌려준다.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write([]byte("<!DOCTYPE html><html><body>"))
		w.Write(request.Post(relay))
		w.Write([]byte("</body></html>"))
		return
	}
	destination, err := request.Redirect(relay, sp)
	if err != nil {
		samlFail(w, 503, "SAML 인증을 시작할 수 없습니다")
		return
	}
	http.Redirect(w, r, destination.String(), http.StatusFound)
}

func safeSAMLRedirect(value string) string {
	if value == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\\\r\n") {
		return "/"
	}
	return value
}

func (s *Server) samlMetadata(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.Store.GetSAMLConfig(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if !cfg.Configured {
		http.NotFound(w, r)
		return
	}
	sp, err := s.samlServiceProvider(r, cfg)
	if err != nil {
		http.Error(w, "SAML metadata is unavailable", http.StatusServiceUnavailable)
		return
	}
	metadata, err := xml.MarshalIndent(sp.Metadata(), "", "  ")
	if err != nil {
		http.Error(w, "SAML metadata is unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/samlmetadata+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Write(metadata)
}

func (s *Server) samlServiceProvider(r *http.Request, cfg *store.SAMLConfig) (*saml.ServiceProvider, error) {
	if cfg.MetadataXML == "" {
		return nil, errors.New("SAML IdP metadata is not configured")
	}
	idp, err := samlsp.ParseMetadata([]byte(cfg.MetadataXML))
	if err != nil || validateSAMLMetadata(idp) != nil {
		return nil, errors.New("SAML IdP metadata is invalid")
	}
	base := strings.TrimRight(s.publicBase(r), "/")
	metadataURL, err := url.Parse(base + samlMetaEntity)
	if err != nil {
		return nil, err
	}
	acsURL, err := url.Parse(base + samlACSEntity)
	if err != nil {
		return nil, err
	}
	return &saml.ServiceProvider{
		EntityID:          metadataURL.String(),
		MetadataURL:       *metadataURL,
		AcsURL:            *acsURL,
		IDPMetadata:       idp,
		AllowIDPInitiated: true,
		SignatureVerifier: strongSAMLSignatureVerifier{},
	}, nil
}

// strongSAMLSignatureVerifier lets crewjam verify XML signatures while
// rejecting SHA-1 signature and digest algorithms before verification.
type strongSAMLSignatureVerifier struct{}

func (strongSAMLSignatureVerifier) VerifySignature(validationContext *dsig.ValidationContext, element *etree.Element) error {
	signatureMethod := element.FindElement("./Signature/SignedInfo/SignatureMethod")
	digestMethod := element.FindElement("./Signature/SignedInfo/Reference/DigestMethod")
	if signatureMethod == nil || digestMethod == nil {
		return errors.New("SAML signature algorithms are missing")
	}
	switch signatureMethod.SelectAttrValue("Algorithm", "") {
	case "http://www.w3.org/2001/04/xmldsig-more#rsa-sha256",
		"http://www.w3.org/2001/04/xmldsig-more#rsa-sha384",
		"http://www.w3.org/2001/04/xmldsig-more#rsa-sha512",
		"http://www.w3.org/2001/04/xmldsig-more#ecdsa-sha256",
		"http://www.w3.org/2001/04/xmldsig-more#ecdsa-sha384",
		"http://www.w3.org/2001/04/xmldsig-more#ecdsa-sha512":
	default:
		return errors.New("SAML signature algorithm is not allowed")
	}
	switch digestMethod.SelectAttrValue("Algorithm", "") {
	case "http://www.w3.org/2001/04/xmlenc#sha256",
		"http://www.w3.org/2001/04/xmldsig-more#sha384",
		"http://www.w3.org/2001/04/xmlenc#sha512":
	default:
		return errors.New("SAML digest algorithm is not allowed")
	}
	_, err := validationContext.Validate(element)
	return err
}

func validateSAMLMetadata(metadata *saml.EntityDescriptor) error {
	if metadata == nil || metadata.EntityID == "" || len(metadata.IDPSSODescriptors) == 0 {
		return errors.New("metadata must contain an IdP entity descriptor")
	}
	hasSSO, hasSigningCert := false, false
	for _, descriptor := range metadata.IDPSSODescriptors {
		for _, endpoint := range descriptor.SingleSignOnServices {
			if endpoint.Binding == saml.HTTPRedirectBinding || endpoint.Binding == saml.HTTPPostBinding {
				u, err := url.Parse(endpoint.Location)
				if err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
					hasSSO = true
				}
			}
		}
		for _, key := range descriptor.KeyDescriptors {
			if key.Use != "" && key.Use != "signing" {
				continue
			}
			for _, cert := range key.KeyInfo.X509Data.X509Certificates {
				if strings.TrimSpace(cert.Data) != "" {
					if _, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cert.Data)); err == nil {
						hasSigningCert = true
					}
				}
			}
		}
	}
	if !hasSSO || !hasSigningCert {
		return errors.New("metadata requires an HTTP SSO endpoint and signing certificate")
	}
	return nil
}

func fetchSAMLMetadata(ctx context.Context, rawURL string) (*saml.EntityDescriptor, []byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, nil, errors.New("invalid metadata URL")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, candidate := range ips {
			ip := candidate.IP
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
				continue
			}
			conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = errors.New("metadata host resolves only to a restricted address")
		}
		return nil, lastErr
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 10 * time.Second, Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("metadata request returned status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, samlMaxXMLSize+1))
	if err != nil {
		return nil, nil, err
	}
	if len(raw) > samlMaxXMLSize {
		return nil, nil, errors.New("metadata is too large")
	}
	metadata, err := samlsp.ParseMetadata(raw)
	return metadata, raw, err
}

func (s *Server) samlACS(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	if err := r.ParseForm(); err != nil {
		samlFail(w, 400, "SAML 응답이 올바르지 않습니다")
		return
	}
	cfg, err := s.Store.GetSAMLConfig(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	sp, err := s.samlServiceProvider(r, cfg)
	if err != nil {
		samlFail(w, 503, "SAML SSO가 설정되지 않았습니다")
		return
	}
	relay := r.PostForm.Get("RelayState")
	var requestID, redirectTo string
	var requestIDs []string
	redirectTo = "/"
	if relay != "" && len(relay) <= 80 {
		requestID, redirectTo, err = s.Store.ConsumeSAMLLoginRequest(r.Context(), hashSecret(relay))
		switch {
		case err == nil:
			requestIDs = []string{requestID}
		case errors.Is(err, store.ErrNotFound):
			// 우리가 만든 상태가 아닌 RelayState(IdP 기본값 등)는 IdP-initiated 응답으로 취급한다.
			// 같은 응답은 RelayState 없이도 IdP-initiated로 받아들여지므로 보안상 차이는 없다.
			requestID, redirectTo = "", "/"
		default:
			s.storeErr(w, err)
			return
		}
	}
	sp.ValidateRequestID = func(response saml.Response, possible []string) error {
		if len(possible) == 0 { // IdP-initiated response
			return nil
		}
		if response.InResponseTo != possible[0] {
			return errors.New("SAML response does not match the pending request")
		}
		return nil
	}
	assertion, err := sp.ParseResponse(r, requestIDs)
	if err != nil || assertion == nil {
		samlFail(w, 401, "SAML 응답의 서명 또는 조건을 확인할 수 없습니다")
		return
	}
	if assertion.ID == "" || assertion.Subject == nil || assertion.Subject.NameID == nil || len(assertion.AuthnStatements) == 0 || assertion.Conditions == nil {
		samlFail(w, 401, "SAML 응답에 로그인 정보가 없습니다")
		return
	}
	if requestID != "" && !assertionConfirmsRequest(assertion, requestID, sp.AcsURL.String()) {
		samlFail(w, 401, "SAML 응답이 로그인 요청과 일치하지 않습니다")
		return
	}
	email, name, err := samlUserIdentity(assertion)
	if err != nil || !validEmail(email) {
		samlFail(w, 403, "IdP 응답에 유효한 이메일 주소가 없습니다")
		return
	}
	email = store.NormalizeEmail(email)
	if !verifiedSAMLDomain(cfg, emailDomain(email)) {
		samlFail(w, 403, "검증된 이메일 도메인의 사용자만 SSO에 로그인할 수 있습니다")
		return
	}
	if err := s.Store.UseSAMLAssertionID(r.Context(), assertion.ID, time.Now().Add(24*time.Hour)); err != nil {
		if errors.Is(err, store.ErrConflict) {
			samlFail(w, 401, "이미 사용된 SAML 응답입니다")
		} else {
			s.storeErr(w, err)
		}
		return
	}
	u, err := s.findOrProvisionSAMLUser(r.Context(), email, name, cfg.DefaultRole)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			samlFail(w, 403, "사용자 자동 등록을 완료할 수 없습니다")
		} else if errors.Is(err, store.ErrConflict) {
			samlFail(w, 409, "계정이 동시에 생성되었습니다. 다시 로그인하세요")
		} else {
			s.storeErr(w, err)
		}
		return
	}
	if u.Disabled {
		samlFail(w, 403, "비활성화된 계정입니다")
		return
	}
	r = withPrincipal(r, &principal{User: u, Via: viaSAML, SAML: true})
	if err := s.startSAMLSession(r.Context(), w, r, u); err != nil {
		s.storeErr(w, err)
		return
	}
	_ = s.Store.TouchUserLogin(r.Context(), u.ID)
	s.audit(r, "auth.saml_login", u.ID, u.Display()+" SAML 로그인")
	writeSAMLLanding(w, safeSAMLRedirect(redirectTo))
}

// samlFail은 브라우저가 직접 여는 SAML 엔드포인트(로그인 시작, ACS)의 오류를 HTML로 보여 준다.
func samlFail(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!DOCTYPE html><html lang="ko"><head><meta charset="utf-8"><title>SSO 로그인 실패</title></head><body style="font-family:system-ui,sans-serif;max-width:480px;margin:15vh auto;padding:0 16px"><h1 style="font-size:18px">SSO 로그인에 실패했습니다</h1><p>%s</p><p><a href="/login">로그인 화면으로 돌아가기</a></p></body></html>`, html.EscapeString(msg))
}

// writeSAMLLanding은 같은 origin 문서에서 목적지로 이동시킨다. ACS는 IdP의 cross-site POST로 열리므로
// 바로 302/303을 주면 그 리다이렉트 체인에서 SameSite=Strict 세션 쿠키가 빠진다.
func writeSAMLLanding(w http.ResponseWriter, dest string) {
	u := html.EscapeString(dest)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<!DOCTYPE html><html lang="ko"><head><meta charset="utf-8"><meta http-equiv="refresh" content="0;url=%s"><title>Aven</title></head><body><p>로그인했습니다. <a href="%s">계속하기</a></p></body></html>`, u, u)
}

func assertionConfirmsRequest(assertion *saml.Assertion, requestID, recipient string) bool {
	for _, confirmation := range assertion.Subject.SubjectConfirmations {
		data := confirmation.SubjectConfirmationData
		if confirmation.Method == "urn:oasis:names:tc:SAML:2.0:cm:bearer" && data != nil && data.InResponseTo == requestID && data.Recipient == recipient {
			return true
		}
	}
	return false
}

func samlUserIdentity(assertion *saml.Assertion) (email, name string, err error) {
	var emails []string
	var names []string
	for _, statement := range assertion.AttributeStatements {
		for _, attribute := range statement.Attributes {
			attributeName := strings.ToLower(strings.TrimSpace(attribute.Name + " " + attribute.FriendlyName))
			emailAttribute := strings.Contains(attributeName, "email") || strings.Contains(attributeName, "mail") || strings.Contains(attributeName, "userprincipalname") || strings.Contains(attributeName, "upn") || strings.Contains(attributeName, "0.9.2342.19200300.100.1.3")
			nameAttribute := strings.Contains(attributeName, "displayname") || strings.Contains(attributeName, "fullname") || strings.TrimSpace(attribute.Name) == "name" || strings.TrimSpace(attribute.FriendlyName) == "name"
			for _, value := range attribute.Values {
				valueText := strings.TrimSpace(value.Value)
				if valueText == "" {
					continue
				}
				if emailAttribute {
					emails = append(emails, valueText)
				}
				if nameAttribute {
					names = append(names, valueText)
				}
			}
		}
	}
	if assertion.Subject.NameID != nil {
		value := strings.TrimSpace(assertion.Subject.NameID.Value)
		if strings.EqualFold(assertion.Subject.NameID.Format, string(saml.EmailAddressNameIDFormat)) || strings.Contains(value, "@") {
			emails = append(emails, value)
		}
	}
	uniqueEmails := uniqueStrings(emails)
	if len(uniqueEmails) != 1 {
		return "", "", errors.New("assertion must contain exactly one email address")
	}
	if len(uniqueStrings(names)) > 1 {
		return "", "", errors.New("assertion contains conflicting names")
	}
	if len(names) == 1 {
		name = strings.TrimSpace(names[0])
	}
	return uniqueEmails[0], name, nil
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value))
		if key != "" && !seen[key] {
			seen[key] = true
			out = append(out, strings.TrimSpace(value))
		}
	}
	return out
}

func (s *Server) findOrProvisionSAMLUser(ctx context.Context, email, name, defaultRole string) (*store.User, error) {
	u, err := s.Store.GetUserByEmail(ctx, email)
	if err == nil {
		if u.Builtin {
			return nil, store.ErrNotFound
		}
		return u, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	role := defaultRole
	if !store.ValidWorkspaceRole(role) {
		role = store.RoleMember
	}
	if name == "" || !validName(name) {
		name = strings.Split(email, "@")[0]
	}
	u = &store.User{Email: email, Name: name, Role: role}
	if err := s.Store.CreateUser(ctx, u); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return s.Store.GetUserByEmail(ctx, email)
		}
		return nil, err
	}
	return u, nil
}
