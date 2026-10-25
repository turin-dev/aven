package api

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/beevik/etree"
	"github.com/crewjam/saml"
	dsig "github.com/russellhaering/goxmldsig"
	"github.com/turin-dev/aven/internal/store"
)

type samlTestSPProvider struct{ metadata *saml.EntityDescriptor }

func (p samlTestSPProvider) GetServiceProvider(_ *http.Request, _ string) (*saml.EntityDescriptor, error) {
	return p.metadata, nil
}

type samlResponseMutation int

const (
	samlResponseValid samlResponseMutation = iota
	samlResponseWrongAudience
	samlResponseFutureNotBefore
	samlResponseBadSignature
	samlResponseExpired
	samlResponseSlightSkew
	samlResponseUnsigned
)

type samlTestFixture struct {
	server     *Server
	handler    http.Handler
	idp        saml.IdentityProvider
	spMetadata *saml.EntityDescriptor
	sp         *saml.ServiceProvider
}

func newSAMLTestFixture(t *testing.T) *samlTestFixture {
	t.Helper()
	ctx := context.Background()
	st := testStore(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	certTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Aven SAML test IdP"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, certTemplate, certTemplate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	parseURL := func(raw string) url.URL {
		t.Helper()
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return *u
	}
	idp := saml.IdentityProvider{
		Key: key, Signer: key, Certificate: cert,
		MetadataURL:     parseURL("https://idp.example.test/metadata"),
		SSOURL:          parseURL("https://idp.example.test/sso"),
		SignatureMethod: dsig.RSASHA256SignatureMethod,
	}
	metadataXML, err := xml.Marshal(idp.Metadata())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &store.SAMLConfig{
		MetadataXML: string(metadataXML),
		Domains:     []store.VerifiedDomain{{Domain: "example.com", Token: "keel-sso=verified", Verified: true}},
		DefaultRole: store.RoleMember,
	}
	if err := st.SaveSAMLConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		Store:      st,
		PublicURL:  "https://aven.example.test",
		SessionKey: make([]byte, 32),
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	request := httptest.NewRequest(http.MethodGet, "https://aven.example.test/", nil)
	sp, err := server.samlServiceProvider(request, cfg)
	if err != nil {
		t.Fatal(err)
	}
	spMetadata := sp.Metadata()
	idp.ServiceProviderProvider = samlTestSPProvider{metadata: spMetadata}
	return &samlTestFixture{server: server, handler: server.Handler(), idp: idp, spMetadata: spMetadata, sp: sp}
}

// responseFor는 SP-initiated 요청(requestID)에 대한 정상 응답을 만든다.
func (f *samlTestFixture) responseFor(t *testing.T, requestID, email string) string {
	t.Helper()
	spDescriptor := f.spMetadata.SPSSODescriptors[0]
	acs := spDescriptor.AssertionConsumerServices[0]
	req := &saml.IdpAuthnRequest{
		IDP: &f.idp, HTTPRequest: httptest.NewRequest(http.MethodPost, f.idp.SSOURL.String(), nil),
		ServiceProviderMetadata: f.spMetadata, SPSSODescriptor: &spDescriptor, ACSEndpoint: &acs, Now: time.Now().UTC(),
		Request: saml.AuthnRequest{ID: requestID},
	}
	session := &saml.Session{ID: "s-" + requestID, UserName: "u", UserEmail: email, UserCommonName: "SSO Person"}
	if err := (saml.DefaultAssertionMaker{}).MakeAssertion(req, session); err != nil {
		t.Fatal(err)
	}
	if err := req.MakeResponse(); err != nil {
		t.Fatal(err)
	}
	doc := etree.NewDocument()
	doc.SetRoot(req.ResponseEl)
	raw, err := doc.WriteToBytes()
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName && c.Value != "" {
			return c.Value
		}
	}
	t.Fatalf("no session cookie (status %d: %s)", w.Code, w.Body.String())
	return ""
}

func expectSAMLLanding(t *testing.T, w *httptest.ResponseRecorder, dest string) {
	t.Helper()
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `content="0;url=`+dest+`"`) {
		t.Fatalf("landing status %d: %s", w.Code, w.Body.String())
	}
}

func (f *samlTestFixture) response(t *testing.T, mutation samlResponseMutation) string {
	t.Helper()
	now := time.Now().UTC()
	spDescriptor := f.spMetadata.SPSSODescriptors[0]
	acs := spDescriptor.AssertionConsumerServices[0]
	req := &saml.IdpAuthnRequest{
		IDP:                     &f.idp,
		HTTPRequest:             httptest.NewRequest(http.MethodPost, f.idp.SSOURL.String(), nil),
		ServiceProviderMetadata: f.spMetadata,
		SPSSODescriptor:         &spDescriptor,
		ACSEndpoint:             &acs,
		Now:                     now,
	}
	session := &saml.Session{ID: "test-session", UserName: "sso-user", UserEmail: "sso-user@example.com", UserCommonName: "SAML User", UserGivenName: "SAML"}
	if err := (saml.DefaultAssertionMaker{}).MakeAssertion(req, session); err != nil {
		t.Fatal(err)
	}
	switch mutation {
	case samlResponseWrongAudience:
		req.Assertion.Conditions.AudienceRestrictions[0].Audience.Value = "https://wrong.example.test/"
	case samlResponseFutureNotBefore:
		req.Assertion.Conditions.NotBefore = now.Add(10 * time.Minute)
	case samlResponseExpired:
		// 허용 오차(MaxClockSkew)를 넘어 만료된 assertion.
		req.Assertion.Conditions.NotOnOrAfter = now.Add(-saml.MaxClockSkew - time.Minute)
	case samlResponseSlightSkew:
		// IdP 시계가 1분 빠른 경우는 허용 오차 안이라 받아들여야 한다.
		req.Assertion.Conditions.NotBefore = now.Add(time.Minute)
	}
	if mutation == samlResponseUnsigned {
		// 서명 없는 Response/Assertion을 그대로 내보낸다.
		resp := &saml.Response{
			ID: "id-unsigned", Version: "2.0", IssueInstant: now, Destination: acs.Location,
			Issuer:    &saml.Issuer{Format: "urn:oasis:names:tc:SAML:2.0:nameid-format:entity", Value: f.idp.MetadataURL.String()},
			Status:    saml.Status{StatusCode: saml.StatusCode{Value: saml.StatusSuccess}},
			Assertion: req.Assertion,
		}
		doc := etree.NewDocument()
		doc.SetRoot(resp.Element())
		raw, err := doc.WriteToBytes()
		if err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString(raw)
	}
	if err := req.MakeResponse(); err != nil {
		t.Fatal(err)
	}
	doc := etree.NewDocument()
	doc.SetRoot(req.ResponseEl)
	raw, err := doc.WriteToBytes()
	if err != nil {
		t.Fatal(err)
	}
	if mutation == samlResponseBadSignature {
		root := etree.NewDocument()
		if err := root.ReadFromBytes(raw); err != nil {
			t.Fatal(err)
		}
		signatureValue := root.FindElement("//SignatureValue")
		if signatureValue == nil || signatureValue.Text() == "" {
			t.Fatal("generated response has no signature value")
		}
		value := signatureValue.Text()
		replacement := "A"
		if value[0] == 'A' {
			replacement = "B"
		}
		signatureValue.SetText(replacement + value[1:])
		raw, err = root.WriteToBytes()
		if err != nil {
			t.Fatal(err)
		}
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func samlACSRequest(encoded, relay string) *http.Request {
	form := url.Values{"SAMLResponse": {encoded}}
	if relay != "" {
		form.Set("RelayState", relay)
	}
	r := httptest.NewRequest(http.MethodPost, "https://aven.example.test/api/auth/saml/acs", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "198.18.10.20:1234"
	return r
}

func TestSAMLACSAcceptsSignedAssertionAndJITProvisions(t *testing.T) {
	f := newSAMLTestFixture(t)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, samlACSRequest(f.response(t, samlResponseValid), ""))
	expectSAMLLanding(t, w, "/")
	u, err := f.server.Store.GetUserByEmail(context.Background(), "sso-user@example.com")
	if err != nil {
		t.Fatalf("JIT user was not created: %v", err)
	}
	if u.Role != store.RoleMember {
		t.Fatalf("JIT role %q", u.Role)
	}
	se, err := f.server.Store.GetSessionByHash(context.Background(), hashSecret(sessionCookie(t, w)))
	if err != nil || !se.SAMLAuthenticated {
		t.Fatalf("session not marked as SAML: %+v %v", se, err)
	}
}

func TestSAMLACSRejectsInvalidSignature(t *testing.T) {
	f := newSAMLTestFixture(t)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, samlACSRequest(f.response(t, samlResponseBadSignature), ""))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("tampered signature status %d: %s", w.Code, w.Body.String())
	}
}

func TestSAMLACSRejectsWrongAudience(t *testing.T) {
	f := newSAMLTestFixture(t)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, samlACSRequest(f.response(t, samlResponseWrongAudience), ""))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong audience status %d: %s", w.Code, w.Body.String())
	}
}

func TestSAMLACSRejectsClockSkew(t *testing.T) {
	f := newSAMLTestFixture(t)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, samlACSRequest(f.response(t, samlResponseFutureNotBefore), ""))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("future assertion status %d: %s", w.Code, w.Body.String())
	}
}

func TestSAMLACSRejectsAssertionReplay(t *testing.T) {
	f := newSAMLTestFixture(t)
	encoded := f.response(t, samlResponseValid)
	first := httptest.NewRecorder()
	f.handler.ServeHTTP(first, samlACSRequest(encoded, ""))
	if first.Code != http.StatusOK {
		t.Fatalf("first assertion status %d: %s", first.Code, first.Body.String())
	}
	second := httptest.NewRecorder()
	f.handler.ServeHTTP(second, samlACSRequest(encoded, ""))
	if second.Code != http.StatusUnauthorized {
		t.Fatalf("replayed assertion status %d: %s", second.Code, second.Body.String())
	}
}

func TestSAMLACSRejectsMismatchedSPInitiatedRequest(t *testing.T) {
	f := newSAMLTestFixture(t)
	ctx := context.Background()
	relay := randomToken(32)
	if err := f.server.Store.CreateSAMLLoginRequest(ctx, hashSecret(relay), "expected-request-id", "/", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// An unsolicited response cannot be substituted for the outstanding request.
	encoded := f.response(t, samlResponseValid)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, samlACSRequest(encoded, relay))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("mismatched request status %d: %s", w.Code, w.Body.String())
	}
}

func TestSAMLEnforcementBlocksPasswordAndKeepsBreakGlass(t *testing.T) {
	f := newSAMLTestFixture(t)
	cfg, err := f.server.Store.GetSAMLConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg.EnforceSSO = true
	if err := f.server.Store.SaveSAMLConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	user := testUser(t, f.server.Store, "person@example.com", store.RoleMember)
	_ = user
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"person@example.com","password":"correct horse battery"}`))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "198.18.10.21:1234"
	f.handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "sso_required") {
		t.Fatalf("domain password login status %d: %s", w.Code, w.Body.String())
	}

	breakGlass := &store.User{ID: store.BuiltinUserID, Email: store.BuiltinUserEmail, Name: "admin", Role: store.RoleOwner, Builtin: true}
	_ = breakGlass
	if !f.server.samlAuthAllowed(context.Background(), httptest.NewRecorder(), &principal{User: breakGlass}) {
		t.Fatal("built-in break-glass principal was blocked")
	}
}

func TestSAMLMetadataAndSettingsRoutes(t *testing.T) {
	f := newSAMLTestFixture(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "https://aven.example.test/api/auth/saml/metadata", nil)
	f.handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), "samlmetadata+xml") {
		t.Fatalf("metadata status %d, content type %q: %s", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
}

func TestSAMLMetadataRejectsUnsafeURL(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "http://127.0.0.1/private", "https://user:pass@example.com/idp.xml"} {
		if _, _, err := fetchSAMLMetadata(context.Background(), raw); err == nil {
			t.Errorf("unsafe metadata URL accepted: %s", raw)
		}
	}
}

func TestSAMLSignatureVerifierRejectsSHA1(t *testing.T) {
	doc := etree.NewDocument()
	if err := doc.ReadFromString(`<Assertion><Signature><SignedInfo><SignatureMethod Algorithm="http://www.w3.org/2000/09/xmldsig#rsa-sha1"/><Reference><DigestMethod Algorithm="http://www.w3.org/2000/09/xmldsig#sha1"/></Reference></SignedInfo></Signature></Assertion>`); err != nil {
		t.Fatal(err)
	}
	err := (strongSAMLSignatureVerifier{}).VerifySignature(nil, doc.Root())
	if err == nil {
		t.Fatal("SHA-1 signature was accepted")
	}
}

func TestSAMLLoginStateIsSingleUse(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	relay := randomToken(32)
	if err := st.CreateSAMLLoginRequest(ctx, hashSecret(relay), "request-1", "/deployments/", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	requestID, destination, err := st.ConsumeSAMLLoginRequest(ctx, hashSecret(relay))
	if err != nil || requestID != "request-1" || destination != "/deployments/" {
		t.Fatalf("consumed state = (%q, %q, %v)", requestID, destination, err)
	}
	if _, _, err := st.ConsumeSAMLLoginRequest(ctx, hashSecret(relay)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("reused relay state error = %v", err)
	}
}

func TestNormalizeSAMLDomain(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{{" Example.COM. ", "example.com", true}, {"localhost", "", false}, {"*.example.com", "", false}, {"a..example.com", "", false}, {"-bad.example", "", false}} {
		got, ok := normalizeSAMLDomain(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("normalizeSAMLDomain(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestSAMLDomainUpdatesPreserveVerification(t *testing.T) {
	previous := []store.VerifiedDomain{{Domain: "example.com", Token: "proof", Verified: true}}
	got, err := updateSAMLVerifiedDomains(previous, []string{"EXAMPLE.com", "new.example.org"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].Verified || got[0].Token != "proof" || got[1].Verified || !strings.HasPrefix(got[1].Token, "keel-sso=") {
		t.Fatalf("domain update lost proof state: %+v", got)
	}
}

func TestSafeSAMLRedirect(t *testing.T) {
	for _, value := range []string{"https://evil.example", "//evil.example", "/\\evil.example", "/good\r\nLocation: https://evil.example"} {
		if got := safeSAMLRedirect(value); got != "/" {
			t.Errorf("unsafe redirect %q became %q", value, got)
		}
	}
	if got := safeSAMLRedirect("/settings/?section=sso"); got != "/settings/?section=sso" {
		t.Fatalf("relative redirect changed: %q", got)
	}
}

func TestSAMLAssertionIdentityRejectsConflictingEmails(t *testing.T) {
	assertion := &saml.Assertion{
		Subject:             &saml.Subject{NameID: &saml.NameID{Format: string(saml.EmailAddressNameIDFormat), Value: "a@example.com"}},
		AttributeStatements: []saml.AttributeStatement{{Attributes: []saml.Attribute{{Name: "email", Values: []saml.AttributeValue{{Value: "b@example.com"}}}}}},
	}
	if _, _, err := samlUserIdentity(assertion); err == nil {
		t.Fatal("conflicting email identities were accepted")
	}
}

func TestSAMLResponseXMLIsBoundedByFormLimit(t *testing.T) {
	f := newSAMLTestFixture(t)
	large := strings.Repeat("A", (2<<20)+1)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, samlACSRequest(large, ""))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversize ACS response status %d: %s", w.Code, w.Body.String())
	}
}

func TestSAMLAssertionResponseHasSignedXML(t *testing.T) {
	f := newSAMLTestFixture(t)
	encoded := f.response(t, samlResponseValid)
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "SignatureValue") || !strings.Contains(string(raw), "example.com") {
		t.Fatalf("expected signed response with user identity, got %q", string(raw[:min(len(raw), 160)]))
	}
}

func TestSAMLACSRejectsExpiredAndAcceptsSmallSkew(t *testing.T) {
	f := newSAMLTestFixture(t)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, samlACSRequest(f.response(t, samlResponseExpired), ""))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expired assertion status %d: %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	f.handler.ServeHTTP(w, samlACSRequest(f.response(t, samlResponseSlightSkew), ""))
	expectSAMLLanding(t, w, "/")
}

func TestSAMLACSRejectsUnsignedResponse(t *testing.T) {
	f := newSAMLTestFixture(t)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, samlACSRequest(f.response(t, samlResponseUnsigned), ""))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned response status %d: %s", w.Code, w.Body.String())
	}
}

func TestSAMLACSRejectsResponseSignedByOtherKey(t *testing.T) {
	f := newSAMLTestFixture(t)
	// 같은 entity ID를 쓰지만 메타데이터에 없는 키로 서명한 IdP.
	other := newSAMLTestFixture(t)
	other.idp.MetadataURL = f.idp.MetadataURL
	other.idp.ServiceProviderProvider = samlTestSPProvider{metadata: f.spMetadata}
	other.spMetadata = f.spMetadata
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, samlACSRequest(other.response(t, samlResponseValid), ""))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("foreign-key response status %d: %s", w.Code, w.Body.String())
	}
}

func TestSAMLACSRejectsUnverifiedDomain(t *testing.T) {
	f := newSAMLTestFixture(t)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, samlACSRequest(f.responseFor(t, "", "someone@other.org"), ""))
	if w.Code != http.StatusForbidden {
		t.Fatalf("unverified domain status %d: %s", w.Code, w.Body.String())
	}
	if _, err := f.server.Store.GetUserByEmail(context.Background(), "someone@other.org"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unverified-domain user was provisioned: %v", err)
	}
}

func TestSAMLSPInitiatedFlow(t *testing.T) {
	f := newSAMLTestFixture(t)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "https://aven.example.test/api/auth/saml/login?next=/settings/?section=sso", nil))
	if w.Code != http.StatusFound {
		t.Fatalf("login status %d: %s", w.Code, w.Body.String())
	}
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil || loc.Host != "idp.example.test" {
		t.Fatalf("redirect %q", w.Header().Get("Location"))
	}
	relay := loc.Query().Get("RelayState")
	compressed, err := base64.StdEncoding.DecodeString(loc.Query().Get("SAMLRequest"))
	if err != nil {
		t.Fatal(err)
	}
	rawReq, err := io.ReadAll(flate.NewReader(bytes.NewReader(compressed)))
	if err != nil {
		t.Fatal(err)
	}
	var authn saml.AuthnRequest
	if err := xml.Unmarshal(rawReq, &authn); err != nil || authn.ID == "" {
		t.Fatalf("authn request: %v %s", err, rawReq)
	}
	encoded := f.responseFor(t, authn.ID, "jit@example.com")
	w = httptest.NewRecorder()
	f.handler.ServeHTTP(w, samlACSRequest(encoded, relay))
	expectSAMLLanding(t, w, "/settings/?section=sso")
	// RelayState는 1회용이라 같은 상태로 다시 보내면 IdP-initiated로 취급되고, assertion 재사용은 막힌다.
	w = httptest.NewRecorder()
	f.handler.ServeHTTP(w, samlACSRequest(encoded, relay))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("replayed SP-initiated response status %d: %s", w.Code, w.Body.String())
	}
}

func TestSAMLEnforcementSessionsAndTokens(t *testing.T) {
	f := newSAMLTestFixture(t)
	ctx := context.Background()
	st := f.server.Store
	user := testUser(t, st, "person@example.com", store.RoleMember)
	outsider := testUser(t, st, "contractor@other.org", store.RoleMember)
	newSession := func(u *store.User, viaSAML bool) string {
		tok := randomToken(32)
		if err := st.CreateSession(ctx, &store.Session{UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour), SAMLAuthenticated: viaSAML}, hashSecret(tok)); err != nil {
			t.Fatal(err)
		}
		return tok
	}
	passwordSession, samlSession, outsiderSession := newSession(user, false), newSession(user, true), newSession(outsider, false)
	apiToken := apiTokenPrefix + randomToken(24)
	if err := st.CreateAPIToken(ctx, &store.APIToken{UserID: user.ID, Name: "cli"}, hashSecret(apiToken)); err != nil {
		t.Fatal(err)
	}
	me := func(cookie, bearer string) int {
		r := httptest.NewRequest(http.MethodGet, "https://aven.example.test/api/auth/me", nil)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		return w.Code
	}
	if code := me(passwordSession, ""); code != 200 {
		t.Fatalf("before enforcement password session = %d", code)
	}
	cfg, _ := st.GetSAMLConfig(ctx)
	cfg.EnforceSSO = true
	if err := st.SaveSAMLConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		cookie, bearer string
		want           int
	}{
		"password session is cut off": {passwordSession, "", 401},
		"saml session keeps working":  {samlSession, "", 200},
		"other domain unaffected":     {outsiderSession, "", 200},
		"api token keeps working":     {"", apiToken, 200},
	} {
		if code := me(tc.cookie, tc.bearer); code != tc.want {
			t.Errorf("%s: status %d, want %d", name, code, tc.want)
		}
	}
}

func TestSAMLSettingsEnforcementRequiresSAMLAdmin(t *testing.T) {
	f := newSAMLTestFixture(t)
	ctx := context.Background()
	admin := testUser(t, f.server.Store, "boss@example.com", store.RoleAdmin)
	put := func(p *principal, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, "/api/saml/settings", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		f.server.putSAMLSettings(w, withPrincipal(r, p))
		return w
	}
	if w := put(&principal{User: admin, Via: viaSession}, `{"enforce_sso":true}`); w.Code != 403 {
		t.Fatalf("password admin enabled enforcement: %d %s", w.Code, w.Body.String())
	}
	if w := put(&principal{User: admin, Via: viaSession, SAML: true}, `{"default_role":"owner"}`); w.Code != 400 {
		t.Fatalf("owner default role accepted: %d %s", w.Code, w.Body.String())
	}
	if w := put(&principal{User: admin, Via: viaSession, SAML: true}, `{"enforce_sso":true,"default_role":"viewer"}`); w.Code != 200 {
		t.Fatalf("saml admin could not enable enforcement: %d %s", w.Code, w.Body.String())
	}
	cfg, _ := f.server.Store.GetSAMLConfig(ctx)
	if !cfg.EnforceSSO || cfg.DefaultRole != store.RoleViewer {
		t.Fatalf("config not saved: %+v", cfg)
	}
	// 이미 적용된 뒤에는 break-glass 관리자도 다른 설정을 바꾸거나 적용을 끌 수 있어야 한다.
	breakGlass := &store.User{ID: store.BuiltinUserID, Email: store.BuiltinUserEmail, Role: store.RoleOwner, Builtin: true}
	if w := put(&principal{User: breakGlass, Via: viaSession}, `{"enforce_sso":false}`); w.Code != 200 {
		t.Fatalf("break-glass admin could not disable enforcement: %d %s", w.Code, w.Body.String())
	}
	// 메타데이터를 지우면 적용도 켤 수 없다.
	if w := put(&principal{User: admin, Via: viaSession, SAML: true}, `{"clear_metadata":true,"enforce_sso":true}`); w.Code != 400 {
		t.Fatalf("enforcement without IdP accepted: %d %s", w.Code, w.Body.String())
	}
}

func TestSAMLIdPSummaryReportsCertExpiry(t *testing.T) {
	f := newSAMLTestFixture(t)
	cfg, _ := f.server.Store.GetSAMLConfig(context.Background())
	info := samlIdPSummary(cfg)
	if info == nil || info.EntityID != f.idp.MetadataURL.String() || info.SSOURL != f.idp.SSOURL.String() || info.CertNotAfter == nil {
		t.Fatalf("summary %+v", info)
	}
	if !info.CertNotAfter.Equal(f.idp.Certificate.NotAfter.UTC()) {
		t.Fatalf("cert expiry %v, want %v", info.CertNotAfter, f.idp.Certificate.NotAfter)
	}
}
