package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// argon2id 매개변수(OWASP 권장 최소값: 19 MiB, 2회, 병렬 1). 해시 문자열에 함께 저장하므로 나중에 올려도 옛 해시는 검증된다.
const (
	argonMemory  = 19 * 1024
	argonTime    = 2
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16
)

const (
	minPasswordLen = 1 // 길이 제한 없음: 비어 있지만 않으면 된다
	maxPasswordLen = 256
)

// hashSlots는 동시에 계산하는 argon2 해시 수를 제한한다(로그인 폭주로 메모리를 다 쓰지 않게).
var hashSlots = make(chan struct{}, 4)

func withHashSlot[T any](f func() T) T {
	hashSlots <- struct{}{}
	defer func() { <-hashSlots }()
	return f()
}

// HashPassword는 PHC 형식의 argon2id 해시를 만든다: $argon2id$v=19$m=..,t=..,p=..$salt$hash
func HashPassword(pw string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := withHashSlot(func() []byte {
		return argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	})
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword는 해시와 비밀번호가 맞는지 상수 시간으로 비교한다. 형식이 틀린 해시는 false다.
func VerifyPassword(hash, pw string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var v int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m < 1024 || m > 1<<20 || t < 1 || t > 10 || p < 1 || p > 16 {
		return false
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return false
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) < 16 || len(want) > 64 {
		return false
	}
	got := withHashSlot(func() []byte { return argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want))) })
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash는 없는 계정으로 로그인할 때도 같은 시간이 걸리게 검증에 쓰는 해시다.
var dummyHash, _ = HashPassword("keel-dummy-password-for-timing")

// checkPasswordPolicy는 새 비밀번호의 길이를 확인한다.
func checkPasswordPolicy(pw string) error {
	n := utf8.RuneCountInString(pw)
	if n < minPasswordLen {
		return errors.New("비밀번호를 입력하세요")
	}
	if len(pw) > maxPasswordLen {
		return errors.New("비밀번호가 너무 깁니다")
	}
	return nil
}
