// Package secret은 저장 시 암호화(AES-256-GCM)를 한다. 마스터 키는 KEEL_DATA의 master.key(0600)에 있고 첫 실행 때 만든다.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Prefix는 암호화된 값 앞에 붙는다. 접두어가 없는 값은 (옛) 평문으로 본다.
const Prefix = "enc:v1:"

type Box struct {
	gcm cipher.AEAD
	key []byte // HMAC(MAC)에 쓰는 마스터 키
}

// NewBox는 32바이트 키로 Box를 만든다.
func NewBox(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, errors.New("master key must be 32 bytes")
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(b)
	if err != nil {
		return nil, err
	}
	return &Box{gcm: g, key: append([]byte(nil), key...)}, nil
}

// LoadOrCreate는 path의 마스터 키를 읽고, 없으면 새로 만든다(0600). 권한이 느슨하면 거부한다.
func LoadOrCreate(path string) (*Box, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := writeKeyFile(path, key); err != nil {
			if errors.Is(err, os.ErrExist) { // 다른 프로세스가 먼저 만들었다
				return LoadOrCreate(path)
			}
			return nil, err
		}
		return NewBox(key)
	}
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(path); err == nil && st.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("master key %s is group/world accessible: %w", path, err)
		}
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil {
		return nil, fmt.Errorf("master key %s: %w", path, err)
	}
	return NewBox(key)
}

// writeKeyFile은 키를 임시 파일에 쓰고 fsync한 뒤 path로 hard link한다. 중간에 죽어도 path에는 완전한 키만 생기고
// (빈 파일이나 잘린 키 때문에 다음 시작이 실패하지 않는다), 이미 있으면 덮어쓰지 않고 os.ErrExist를 낸다.
func writeKeyFile(path string, key []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(key) + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return err
		}
		// hard link를 못 쓰는 파일 시스템: O_EXCL로 직접 쓴다(예전 방식).
		return writeKeyFileExcl(path, key)
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

func writeKeyFileExcl(path string, key []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(key) + "\n"); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}

// Seal은 평문을 암호화한다. 이 키로 복호화되는 암호문이나 빈 문자열은 그대로 둔다.
// 접두어만 같고 복호화되지 않는 값(우연히 "enc:v1:"로 시작하는 비밀번호 등)은 평문으로 보고 암호화한다.
// 예전에는 접두어만 보고 그대로 저장해서, 다시 읽을 때 복호화 에러가 나고 그 값이 평문으로 남았다.
func (b *Box) Seal(plain string) string {
	if plain == "" {
		return plain
	}
	if strings.HasPrefix(plain, Prefix) {
		if _, _, err := b.Open(plain); err == nil {
			return plain
		}
	}
	nonce := make([]byte, b.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err) // crypto/rand 실패는 복구할 수 없다
	}
	return Prefix + base64.StdEncoding.EncodeToString(b.gcm.Seal(nonce, nonce, []byte(plain), nil))
}

// Open은 Seal의 역변환이다. 접두어가 없으면 평문으로 보고 그대로 돌려준다(legacy=true).
func (b *Box) Open(v string) (plain string, legacy bool, err error) {
	rest, ok := strings.CutPrefix(v, Prefix)
	if !ok {
		return v, true, nil
	}
	raw, err := base64.StdEncoding.DecodeString(rest)
	if err != nil || len(raw) < b.gcm.NonceSize() {
		return "", false, errors.New("invalid encrypted value")
	}
	pt, err := b.gcm.Open(nil, raw[:b.gcm.NonceSize()], raw[b.gcm.NonceSize():], nil)
	if err != nil {
		return "", false, errors.New("cannot decrypt value (wrong master key?)")
	}
	return string(pt), false, nil
}

// MAC은 마스터 키로 msg의 HMAC-SHA256을 만든다(예: 도메인 확인 토큰). 메시지마다 용도 접두어("domain:" 등)를 붙여 구분한다.
func (b *Box) MAC(msg string) []byte {
	m := hmac.New(sha256.New, b.key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}
