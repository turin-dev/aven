package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/turin-dev/aven/internal/store"
)

func init() {
	// passkey 관리는 자기 계정 기능이라 viewer도 쓸 수 있다(TestRouteAuthzMatrix).
	selfService["POST /api/auth/passkeys/register/begin"] = [2]bool{true, true}
	selfService["POST /api/auth/passkeys/register/{id}/finish"] = [2]bool{true, true}
	selfService["DELETE /api/auth/passkeys/{id}"] = [2]bool{true, true}
}

const (
	waHost   = "aven.example.com"
	waOrigin = "https://" + waHost
	testPW   = "correct horse battery"
)

var b64u = base64.RawURLEncoding

// softAuthenticator는 테스트용 소프트웨어 passkey다(ES256, attestation "none", UV 플래그를 붙인다).
type softAuthenticator struct {
	key     *ecdsa.PrivateKey
	credID  []byte
	user    []byte // userHandle
	counter uint32
	origin  string
	rpID    string
}

func newSoftAuthenticator(t *testing.T) *softAuthenticator {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &softAuthenticator{key: k, credID: randomRaw(16), origin: waOrigin, rpID: waHost}
}

func (a *softAuthenticator) clientData(typ, challenge string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": a.origin, "crossOrigin": false})
	return b
}

func (a *softAuthenticator) authData(flags byte, attested []byte) []byte {
	rp := sha256.Sum256([]byte(a.rpID))
	out := append([]byte{}, rp[:]...)
	out = append(out, flags)
	out = binary.BigEndian.AppendUint32(out, a.counter)
	return append(out, attested...)
}

// create는 navigator.credentials.create() 응답(JSON)을 만든다.
func (a *softAuthenticator) create(t *testing.T, options map[string]any) string {
	t.Helper()
	pk := options["publicKey"].(map[string]any)
	user := pk["user"].(map[string]any)
	uid, err := b64u.DecodeString(user["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	a.user = uid
	if sel, _ := pk["authenticatorSelection"].(map[string]any); sel["userVerification"] != "required" || sel["residentKey"] != "required" {
		t.Errorf("authenticatorSelection: %v", sel)
	}
	ecdh, _ := a.key.PublicKey.ECDH()
	raw := ecdh.Bytes() // 0x04 || X || Y
	cose, err := webauthncbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: raw[1:33], -3: raw[33:]})
	if err != nil {
		t.Fatal(err)
	}
	attested := make([]byte, 16) // AAGUID 0
	attested = binary.BigEndian.AppendUint16(attested, uint16(len(a.credID)))
	attested = append(attested, a.credID...)
	attested = append(attested, cose...)
	ad := a.authData(0x01|0x04|0x40, attested) // UP | UV | AT
	att, err := webauthncbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": ad})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{
		"id": b64u.EncodeToString(a.credID), "rawId": b64u.EncodeToString(a.credID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64u.EncodeToString(a.clientData("webauthn.create", pk["challenge"].(string))),
			"attestationObject": b64u.EncodeToString(att),
		},
		"clientExtensionResults": map[string]any{},
	})
	return string(b)
}

// get은 navigator.credentials.get() 응답(JSON)을 만든다. 서명 카운터를 하나 올린다.
func (a *softAuthenticator) get(t *testing.T, options map[string]any) string {
	t.Helper()
	pk := options["publicKey"].(map[string]any)
	a.counter++
	ad := a.authData(0x01|0x04, nil)
	cd := a.clientData("webauthn.get", pk["challenge"].(string))
	h := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), h[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{
		"id": b64u.EncodeToString(a.credID), "rawId": b64u.EncodeToString(a.credID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64u.EncodeToString(cd),
			"authenticatorData": b64u.EncodeToString(ad),
			"signature":         b64u.EncodeToString(sig),
			"userHandle":        b64u.EncodeToString(a.user),
		},
		"clientExtensionResults": map[string]any{},
	})
	return string(b)
}

// waReq는 브라우저처럼 같은 origin(https://aven.example.com)에서 보낸 요청이다.
func waReq(h http.Handler, method, path, body, cookie string) *httptest.ResponseRecorder {
	return req(h, method, path, body, func(r *http.Request) {
		r.Host = waHost
		r.Header.Set("Origin", waOrigin)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
		}
	})
}

type ceremony struct {
	ChallengeID string         `json:"challenge_id"`
	Flow        string         `json:"flow"`
	Options     map[string]any `json:"options"`
}

// registerPasskey는 cookie 세션으로 passkey를 등록하고 받은 복구 코드를 돌려준다.
func registerPasskey(t *testing.T, h http.Handler, cookie string, a *softAuthenticator) []string {
	t.Helper()
	w := waReq(h, "POST", "/api/auth/passkeys/register/begin", `{"name":"노트북","password":"`+testPW+`"}`, cookie)
	if w.Code != 200 {
		t.Fatalf("register begin: %d %s", w.Code, w.Body)
	}
	c := decodeBody[ceremony](t, w)
	w = waReq(h, "POST", "/api/auth/passkeys/register/"+c.ChallengeID+"/finish", a.create(t, c.Options), cookie)
	if w.Code != 200 {
		t.Fatalf("register finish: %d %s", w.Code, w.Body)
	}
	return decodeBody[struct {
		Codes []string `json:"recovery_codes"`
	}](t, w).Codes
}

func TestPasskeyRegistrationAndLogin(t *testing.T) {
	f, h := authServer(t)
	u := testUser(t, f.st, "pk@example.com", store.RoleMember)
	cookie := testSessionCookie(t, f.srv, u)

	// 등록에는 현재 비밀번호와 쓸 수 있는 origin이 필요하다.
	if w := waReq(h, "POST", "/api/auth/passkeys/register/begin", `{"name":"x"}`, cookie); w.Code != 400 {
		t.Errorf("begin without password: %d", w.Code)
	}
	if w := waReq(h, "POST", "/api/auth/passkeys/register/begin", `{"name":"x","password":"wrong wrong"}`, cookie); w.Code != 401 {
		t.Errorf("begin with wrong password: %d", w.Code)
	}
	ipReq := req(h, "POST", "/api/auth/passkeys/register/begin", `{"password":"`+testPW+`"}`, func(r *http.Request) {
		r.Host = "10.0.0.1"
		r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	})
	if ipReq.Code != 400 {
		t.Errorf("begin on an IP address host: %d", ipReq.Code)
	}
	if w := req(h, "POST", "/api/auth/passkeys/register/begin", `{"password":"`+testPW+`"}`, withBearer("legacy-token")); w.Code != 403 {
		t.Errorf("passkey registration with a token: %d", w.Code)
	}

	// 다른 세션은 남의 등록 요청을 끝낼 수 없다.
	other := testSessionCookie(t, f.srv, u)
	a := newSoftAuthenticator(t)
	w := waReq(h, "POST", "/api/auth/passkeys/register/begin", `{"password":"`+testPW+`"}`, cookie)
	c := decodeBody[ceremony](t, w)
	if w := waReq(h, "POST", "/api/auth/passkeys/register/"+c.ChallengeID+"/finish", a.create(t, c.Options), other); w.Code != 400 {
		t.Errorf("finish from another session: %d", w.Code)
	}
	// 잘못된 origin으로 만든 응답은 거부한다(라이브러리의 clientData origin 검사).
	evil := newSoftAuthenticator(t)
	evil.origin = "https://evil.example.com"
	c = decodeBody[ceremony](t, waReq(h, "POST", "/api/auth/passkeys/register/begin", `{"password":"`+testPW+`"}`, cookie))
	if w := waReq(h, "POST", "/api/auth/passkeys/register/"+c.ChallengeID+"/finish", evil.create(t, c.Options), cookie); w.Code != 400 {
		t.Errorf("finish with a phishing origin: %d", w.Code)
	}

	codes := registerPasskey(t, h, cookie, a)
	if len(codes) != recoveryCodeCount {
		t.Fatalf("first passkey should create recovery codes: %v", codes)
	}
	// 같은 인증기를 두 번 등록할 수 없다.
	c = decodeBody[ceremony](t, waReq(h, "POST", "/api/auth/passkeys/register/begin", `{"password":"`+testPW+`"}`, cookie))
	if ex, _ := c.Options["publicKey"].(map[string]any)["excludeCredentials"].([]any); len(ex) != 1 {
		t.Errorf("excludeCredentials: %v", ex)
	}
	if w := waReq(h, "POST", "/api/auth/passkeys/register/"+c.ChallengeID+"/finish", a.create(t, c.Options), cookie); w.Code != 409 {
		t.Errorf("duplicate passkey: %d %s", w.Code, w.Body)
	}

	list := waReq(h, "GET", "/api/auth/passkeys", "", cookie)
	if list.Code != 200 || strings.Contains(list.Body.String(), "credential") || strings.Contains(list.Body.String(), "public") {
		t.Errorf("list leaks credential data: %d %s", list.Code, list.Body)
	}
	keys := decodeBody[[]store.Passkey](t, list)
	if len(keys) != 1 || keys[0].Name != "노트북" || keys[0].RPID != waHost {
		t.Fatalf("passkeys: %+v", keys)
	}
	me := decodeBody[map[string]any](t, waReq(h, "GET", "/api/auth/me", "", cookie))
	if me["passkey_count"] != float64(1) || me["mfa_enabled"] != true || me["totp_enabled"] != false {
		t.Errorf("me: %v", me)
	}

	// 비밀번호만으로는 로그인할 수 없고 passkey 또는 복구 코드를 요구한다.
	pwLogin := `{"email":"pk@example.com","password":"` + testPW + `"}`
	w = waReq(h, "POST", "/api/auth/login", pwLogin, "")
	if w.Code != 401 || !strings.Contains(w.Body.String(), `"totp_required"`) || !strings.Contains(w.Body.String(), `["passkey","recovery_code"]`) {
		t.Errorf("password-only login: %d %s", w.Code, w.Body)
	}
	// passkey만 있는 계정에서 6자리 코드는 통하지 않는다(확인하지 않은 TOTP 비밀이 있어도).
	waReq(h, "POST", "/api/auth/2fa/setup", "", cookie)
	pending, _ := f.st.GetUser(f.ctx, u.ID)
	pendingCode, _ := TOTPAt(pending.TOTPSecret, time.Now())
	if w := waReq(h, "POST", "/api/auth/login", `{"email":"pk@example.com","password":"`+testPW+`","totp_code":"`+pendingCode+`"}`, ""); w.Code != 401 {
		t.Errorf("unconfirmed TOTP secret accepted as a second factor: %d", w.Code)
	}
	if w := waReq(h, "POST", "/api/auth/login", `{"email":"pk@example.com","password":"`+testPW+`","totp_code":"`+codes[0]+`"}`, ""); w.Code != 204 {
		t.Errorf("recovery code login: %d %s", w.Code, w.Body)
	}

	// 비밀번호 + passkey(두 번째 단계)
	if w := waReq(h, "POST", "/api/auth/passkeys/login/begin", `{"email":"pk@example.com","password":"nope nope"}`, ""); w.Code != 401 {
		t.Errorf("factor begin with wrong password: %d", w.Code)
	}
	w = waReq(h, "POST", "/api/auth/passkeys/login/begin", pwLogin, "")
	if w.Code != 200 {
		t.Fatalf("factor begin: %d %s", w.Code, w.Body)
	}
	c = decodeBody[ceremony](t, w)
	if c.Flow != "login_factor" {
		t.Errorf("flow: %s", c.Flow)
	}
	if allow, _ := c.Options["publicKey"].(map[string]any)["allowCredentials"].([]any); len(allow) != 1 {
		t.Errorf("allowCredentials: %v", allow)
	}
	assertion := a.get(t, c.Options)
	w = waReq(h, "POST", "/api/auth/passkeys/login/factor/"+c.ChallengeID+"/finish", assertion, "")
	if w.Code != 204 {
		t.Fatalf("factor finish: %d %s", w.Code, w.Body)
	}
	sess := sessionFrom(t, w)
	if me := decodeBody[map[string]any](t, waReq(h, "GET", "/api/auth/me", "", sess)); me["email"] != "pk@example.com" {
		t.Errorf("me after passkey login: %v", me)
	}
	// challenge는 한 번만 쓸 수 있다.
	if w := waReq(h, "POST", "/api/auth/passkeys/login/factor/"+c.ChallengeID+"/finish", assertion, ""); w.Code != 400 {
		t.Errorf("replayed challenge: %d", w.Code)
	}

	// 비밀번호 없는 passkey 로그인(discoverable)
	discover := func() ceremony {
		t.Helper()
		w := waReq(h, "POST", "/api/auth/passkeys/login/begin", `{}`, "")
		if w.Code != 200 {
			t.Fatalf("discoverable begin: %d %s", w.Code, w.Body)
		}
		c := decodeBody[ceremony](t, w)
		if c.Flow != "login_discoverable" {
			t.Fatalf("flow: %s", c.Flow)
		}
		return c
	}
	c = discover()
	// 다른 흐름의 finish 엔드포인트로는 쓸 수 없다.
	if w := waReq(h, "POST", "/api/auth/passkeys/login/factor/"+c.ChallengeID+"/finish", a.get(t, c.Options), ""); w.Code != 400 {
		t.Errorf("discoverable challenge on factor endpoint: %d", w.Code)
	}
	c = discover()
	w = waReq(h, "POST", "/api/auth/passkeys/login/discoverable/"+c.ChallengeID+"/finish", a.get(t, c.Options), "")
	if w.Code != 204 {
		t.Fatalf("discoverable finish: %d %s", w.Code, w.Body)
	}
	sessionFrom(t, w)

	// cross-origin과 다른 host에서 끝내기는 거부한다.
	c = discover()
	cross := req(h, "POST", "/api/auth/passkeys/login/discoverable/"+c.ChallengeID+"/finish", a.get(t, c.Options), func(r *http.Request) {
		r.Host = waHost
		r.Header.Set("Origin", "https://evil.example.com")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
	})
	if cross.Code != 403 {
		t.Errorf("cross-origin finish: %d", cross.Code)
	}

	// 서명이 틀리거나 다른 키로 서명한 응답은 실패로 센다.
	c = discover()
	forged := newSoftAuthenticator(t)
	forged.credID, forged.user = a.credID, a.user
	if w := waReq(h, "POST", "/api/auth/passkeys/login/discoverable/"+c.ChallengeID+"/finish", forged.get(t, c.Options), ""); w.Code != 401 {
		t.Errorf("forged signature: %d %s", w.Code, w.Body)
	}
	// 알 수 없는 credential
	c = discover()
	stranger := newSoftAuthenticator(t)
	stranger.user = a.user
	if w := waReq(h, "POST", "/api/auth/passkeys/login/discoverable/"+c.ChallengeID+"/finish", stranger.get(t, c.Options), ""); w.Code != 401 {
		t.Errorf("unknown credential: %d", w.Code)
	}

	// 서명 카운터가 뒤로 가면 복제된 인증기로 보고 거부한다.
	c = discover()
	a.counter = 0
	if w := waReq(h, "POST", "/api/auth/passkeys/login/discoverable/"+c.ChallengeID+"/finish", a.get(t, c.Options), ""); w.Code != 401 {
		t.Errorf("counter regression: %d %s", w.Code, w.Body)
	}

	// 비활성화된 계정은 passkey로도 로그인하지 못한다.
	a.counter = 100
	dis, _ := f.st.GetUser(f.ctx, u.ID)
	dis.Disabled = true
	f.st.UpdateUser(f.ctx, dis)
	c = discover()
	if w := waReq(h, "POST", "/api/auth/passkeys/login/discoverable/"+c.ChallengeID+"/finish", a.get(t, c.Options), ""); w.Code != 401 {
		t.Errorf("disabled user passkey login: %d", w.Code)
	}
	dis.Disabled = false
	f.st.UpdateUser(f.ctx, dis)

	// 삭제: 비밀번호를 확인하고, 마지막 수단이면 복구 코드도 지운다.
	if w := waReq(h, "DELETE", "/api/auth/passkeys/"+keys[0].ID, `{"password":"bad bad bad"}`, cookie); w.Code != 401 {
		t.Errorf("delete with wrong password: %d", w.Code)
	}
	if w := waReq(h, "DELETE", "/api/auth/passkeys/pk_missing", `{"password":"`+testPW+`"}`, cookie); w.Code != 404 {
		t.Errorf("delete unknown passkey: %d", w.Code)
	}
	if n, _ := f.st.CountRecoveryCodes(f.ctx, u.ID); n == 0 {
		t.Error("deleting an unknown passkey removed recovery codes")
	}
	if w := waReq(h, "DELETE", "/api/auth/passkeys/"+keys[0].ID, `{"password":"`+testPW+`"}`, cookie); w.Code != 204 {
		t.Errorf("delete: %d %s", w.Code, w.Body)
	}
	if n, _ := f.st.CountRecoveryCodes(f.ctx, u.ID); n != 0 {
		t.Errorf("recovery codes kept after removing the last factor: %d", n)
	}
	if w := waReq(h, "POST", "/api/auth/login", pwLogin, ""); w.Code != 204 {
		t.Errorf("password login after removing passkey: %d", w.Code)
	}
}

func TestPasskeyAndTOTPShareRecoveryCodes(t *testing.T) {
	f, h := authServer(t)
	u := testUser(t, f.st, "both@example.com", store.RoleMember)
	cookie := testSessionCookie(t, f.srv, u)
	a := newSoftAuthenticator(t)
	if codes := registerPasskey(t, h, cookie, a); len(codes) != recoveryCodeCount {
		t.Fatalf("codes: %v", codes)
	}
	// TOTP를 켜면 새 복구 코드를 만든다.
	setup := decodeBody[struct{ Secret string }](t, waReq(h, "POST", "/api/auth/2fa/setup", "", cookie))
	code, _ := TOTPAt(setup.Secret, time.Now())
	w := waReq(h, "POST", "/api/auth/2fa/enable", `{"code":"`+code+`"}`, cookie)
	if w.Code != 200 {
		t.Fatalf("enable: %d %s", w.Code, w.Body)
	}
	rc := decodeBody[struct {
		Codes []string `json:"recovery_codes"`
	}](t, w).Codes
	if w := waReq(h, "POST", "/api/auth/login", `{"email":"both@example.com","password":"`+testPW+`"}`, ""); !strings.Contains(w.Body.String(), `["totp","passkey","recovery_code"]`) {
		t.Errorf("methods: %s", w.Body)
	}
	// TOTP 계정의 복구 코드 재발급은 비밀번호만으로는 안 된다.
	if w := waReq(h, "POST", "/api/auth/2fa/recovery-codes", `{"password":"`+testPW+`"}`, cookie); w.Code != 400 {
		t.Errorf("recovery codes by password with TOTP on: %d", w.Code)
	}
	// TOTP를 꺼도 passkey가 남아 있으면 복구 코드를 남긴다.
	if w := waReq(h, "POST", "/api/auth/2fa/disable", `{"password":"`+testPW+`"}`, cookie); w.Code != 204 {
		t.Fatalf("disable: %d %s", w.Code, w.Body)
	}
	if n, _ := f.st.CountRecoveryCodes(f.ctx, u.ID); n != len(rc) {
		t.Errorf("recovery codes after disabling TOTP with a passkey left: %d", n)
	}
	// passkey만 남은 계정은 비밀번호로 복구 코드를 다시 만든다.
	w = waReq(h, "POST", "/api/auth/2fa/recovery-codes", `{"password":"`+testPW+`"}`, cookie)
	if w.Code != 200 {
		t.Fatalf("regenerate: %d %s", w.Code, w.Body)
	}
	if w := waReq(h, "POST", "/api/auth/login", `{"email":"both@example.com","password":"`+testPW+`","totp_code":"`+rc[0]+`"}`, ""); w.Code != 401 {
		t.Errorf("old recovery code after regeneration: %d", w.Code)
	}

	// 관리자의 2FA 초기화는 passkey와 복구 코드까지 지운다.
	admin := testUser(t, f.st, "admin2@example.com", store.RoleAdmin)
	ac := testSessionCookie(t, f.srv, admin)
	if w := waReq(h, "PATCH", "/api/users/"+u.ID, `{"reset_2fa":true}`, ac); w.Code != 200 {
		t.Fatalf("reset 2fa: %d %s", w.Code, w.Body)
	}
	if n, _ := f.st.CountPasskeys(f.ctx, u.ID); n != 0 {
		t.Errorf("passkeys after reset: %d", n)
	}
	if n, _ := f.st.CountRecoveryCodes(f.ctx, u.ID); n != 0 {
		t.Errorf("recovery codes after reset: %d", n)
	}
}

func TestWorkspace2FAEnforcement(t *testing.T) {
	f, h := authServer(t)
	admin := testUser(t, f.st, "boss@example.com", store.RoleAdmin)
	member := testUser(t, f.st, "dev@example.com", store.RoleMember)
	viewer := testUser(t, f.st, "look@example.com", store.RoleViewer)
	ac, mc, vc := testSessionCookie(t, f.srv, admin), testSessionCookie(t, f.srv, member), testSessionCookie(t, f.srv, viewer)

	// admin만 바꿀 수 있고, 자기 계정에 2FA가 있어야 켤 수 있다.
	if w := waReq(h, "PATCH", "/api/workspace/security", `{"require_2fa":true}`, vc); w.Code != 403 {
		t.Errorf("viewer enables enforcement: %d", w.Code)
	}
	if w := waReq(h, "PATCH", "/api/workspace/security", `{"require_2fa":true}`, ac); w.Code != 400 {
		t.Errorf("admin without 2fa enables enforcement: %d", w.Code)
	}
	secret := NewTOTPSecret()
	f.st.SetUserTOTP(f.ctx, admin.ID, secret, true)
	if w := waReq(h, "PATCH", "/api/workspace/security", `{"require_2fa":true}`, ac); w.Code != 200 {
		t.Fatalf("enable enforcement: %d %s", w.Code, w.Body)
	}
	if got := decodeBody[map[string]bool](t, waReq(h, "GET", "/api/workspace/security", "", ac)); !got["require_2fa"] {
		t.Errorf("security: %v", got)
	}

	// 2FA가 없는 member 세션은 설정 화면에 필요한 것 말고는 막힌다.
	w := waReq(h, "GET", "/api/projects", "", mc)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "mfa_setup_required") {
		t.Errorf("projects without 2fa: %d %s", w.Code, w.Body)
	}
	if w := waReq(h, "POST", "/api/tokens", `{"name":"ci"}`, mc); w.Code != 403 {
		t.Errorf("token creation without 2fa: %d", w.Code)
	}
	me := decodeBody[map[string]any](t, waReq(h, "GET", "/api/auth/me", "", mc))
	if me["mfa_enforced"] != true || me["mfa_setup_required"] != true {
		t.Errorf("me: %v", me)
	}
	if w := waReq(h, "GET", "/api/projects", "", ac); w.Code != 200 {
		t.Errorf("admin with 2fa: %d", w.Code)
	}
	// 새로 로그인해도 세션은 만들어지고(설정하러 가야 하므로) 같은 제한을 받는다.
	w = waReq(h, "POST", "/api/auth/login", `{"email":"dev@example.com","password":"`+testPW+`"}`, "")
	if w.Code != 204 {
		t.Fatalf("login under enforcement: %d", w.Code)
	}
	fresh := sessionFrom(t, w)
	if w := waReq(h, "GET", "/api/projects", "", fresh); w.Code != 403 {
		t.Errorf("fresh session without 2fa: %d", w.Code)
	}
	// API 토큰(자동화)은 2FA 강제와 무관하다.
	tok := &store.APIToken{UserID: member.ID, Name: "ci", Prefix: "keel_m", Scope: store.ScopeWorkspace, Role: store.RoleMember}
	f.st.CreateAPIToken(f.ctx, tok, hashSecret("keel_membertoken"))
	if w := waReq(h, "GET", "/api/projects", "", ""); w.Code != 401 {
		t.Errorf("no auth: %d", w.Code)
	}
	if w := req(h, "GET", "/api/projects", "", withBearer("keel_membertoken")); w.Code != 200 {
		t.Errorf("api token under enforcement: %d %s", w.Code, w.Body)
	}

	// TOTP를 설정하면 바로 풀린다.
	setup := decodeBody[struct{ Secret string }](t, waReq(h, "POST", "/api/auth/2fa/setup", "", mc))
	code, _ := TOTPAt(setup.Secret, time.Now())
	if w := waReq(h, "POST", "/api/auth/2fa/enable", `{"code":"`+code+`"}`, mc); w.Code != 200 {
		t.Fatalf("enable totp under enforcement: %d %s", w.Code, w.Body)
	}
	if w := waReq(h, "GET", "/api/projects", "", mc); w.Code != 200 {
		t.Errorf("projects after setting up 2fa: %d", w.Code)
	}
	// 강제 중에는 마지막 수단을 끌 수 없다.
	if w := waReq(h, "POST", "/api/auth/2fa/disable", `{"password":"`+testPW+`"}`, mc); w.Code != 409 {
		t.Errorf("disable last factor under enforcement: %d", w.Code)
	}

	// passkey로 설정해도 풀린다(viewer).
	if w := waReq(h, "GET", "/api/projects", "", vc); w.Code != 403 {
		t.Errorf("viewer without 2fa: %d", w.Code)
	}
	a := newSoftAuthenticator(t)
	registerPasskey(t, h, vc, a)
	if w := waReq(h, "GET", "/api/projects", "", vc); w.Code != 200 {
		t.Errorf("viewer after passkey: %d", w.Code)
	}
	keys, _ := f.st.ListPasskeys(f.ctx, viewer.ID)
	if w := waReq(h, "DELETE", "/api/auth/passkeys/"+keys[0].ID, `{"password":"`+testPW+`"}`, vc); w.Code != 409 {
		t.Errorf("delete last passkey under enforcement: %d", w.Code)
	}

	// 끄면 모두 다시 쓸 수 있다.
	if w := waReq(h, "PATCH", "/api/workspace/security", `{"require_2fa":false}`, ac); w.Code != 200 {
		t.Fatalf("disable enforcement: %d", w.Code)
	}
	if w := waReq(h, "GET", "/api/projects", "", fresh); w.Code != 200 {
		t.Errorf("after disabling enforcement: %d", w.Code)
	}
}

func TestMFASetupEndpoints(t *testing.T) {
	for _, c := range []struct {
		method, path string
		ok           bool
	}{
		{"GET", "/api/auth/me", true},
		{"PATCH", "/api/auth/me", false},
		{"POST", "/api/auth/2fa/setup", true},
		{"POST", "/api/auth/2fa/enable", true},
		{"POST", "/api/auth/2fa/disable", false},
		{"POST", "/api/auth/2fa/recovery-codes", false},
		{"GET", "/api/auth/passkeys", true},
		{"POST", "/api/auth/passkeys/register/begin", true},
		{"POST", "/api/auth/passkeys/register/abc/finish", true},
		{"POST", "/api/auth/passkeys/register//finish", false},
		{"POST", "/api/auth/passkeys/register/a/b/finish", false},
		{"DELETE", "/api/auth/passkeys/pk_1", false},
		{"GET", "/api/projects", false},
		{"POST", "/api/tokens", false},
	} {
		if got := mfaSetupEndpoint(c.method, c.path); got != c.ok {
			t.Errorf("%s %s: %v", c.method, c.path, got)
		}
	}
	if m := fmt.Sprint(secondFactorMethods(false, true)); m != "[passkey recovery_code]" {
		t.Errorf("methods: %s", m)
	}
}
