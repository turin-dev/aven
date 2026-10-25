package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ---- TOTP (RFC 6238: HMAC-SHA1, 30초, 6자리) ----

const (
	totpPeriod = 30
	totpDigits = 6
	totpSkew   = 1 // 앞뒤 한 step까지 허용(시계 오차)
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret은 160비트 무작위 비밀을 base32(패딩 없음)로 만든다.
func NewTOTPSecret() string {
	b := make([]byte, 20)
	rand.Read(b)
	return b32.EncodeToString(b)
}

// HOTP는 RFC 4226 값(digits 자리)이다.
func HOTP(key []byte, counter uint64, digits int) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, code%mod)
}

func decodeTOTPSecret(secret string) ([]byte, error) {
	return b32.DecodeString(strings.ToUpper(strings.TrimRight(strings.ReplaceAll(secret, " ", ""), "=")))
}

// TOTPAt은 t 시각의 코드다.
func TOTPAt(secret string, t time.Time) (string, error) {
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		return "", err
	}
	return HOTP(key, uint64(t.Unix()/totpPeriod), totpDigits), nil
}

// MatchTOTP는 code가 t 기준 ±totpSkew step 안의 코드인지 보고, 맞으면 그 step을 돌려준다(재사용 방지에 쓴다).
func MatchTOTP(secret, code string, t time.Time) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return 0, false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	key, err := decodeTOTPSecret(secret)
	if err != nil || len(key) == 0 {
		return 0, false
	}
	cur := t.Unix() / totpPeriod
	var step int64
	ok := 0
	for d := int64(-totpSkew); d <= totpSkew; d++ {
		s := cur + d
		if s < 0 {
			continue
		}
		// 모든 후보를 비교한다(맞는 위치로 시간이 새지 않게).
		if hmac.Equal([]byte(HOTP(key, uint64(s), totpDigits)), []byte(code)) {
			step, ok = s, 1
		}
	}
	return step, ok == 1
}

// OTPAuthURL은 인증 앱에 등록하는 otpauth:// URL이다.
func OTPAuthURL(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{"secret": {secret}, "issuer": {issuer}, "algorithm": {"SHA1"}, "digits": {fmt.Sprint(totpDigits)}, "period": {fmt.Sprint(totpPeriod)}}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// ---- 복구 코드 ----

const recoveryCodeCount = 10

const recoveryAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// NewRecoveryCodes는 xxxxx-xxxxx 형식의 일회용 복구 코드를 만든다.
func NewRecoveryCodes() []string {
	out := make([]string, recoveryCodeCount)
	for i := range out {
		b := make([]byte, 10)
		rand.Read(b)
		for j := range b {
			b[j] = recoveryAlphabet[int(b[j])%len(recoveryAlphabet)]
		}
		out[i] = string(b[:5]) + "-" + string(b[5:])
	}
	return out
}

// normalizeRecovery는 입력을 저장 형식으로 맞춘다(대소문자, 공백, 하이픈 위치 무시).
func normalizeRecovery(code string) string {
	c := strings.ToLower(strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(code)))
	if len(c) != 10 {
		return ""
	}
	return c[:5] + "-" + c[5:]
}

// hashSecret은 세션/초대/API 토큰/복구 코드를 저장할 때 쓰는 SHA-256 hex다(원래 값은 저장하지 않는다).
func hashSecret(v string) string {
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:])
}
