package node

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// BuiltinKnownHosts는 github.com의 공개 호스트 키다(https://api.github.com/meta의 ssh_keys).
const BuiltinKnownHosts = `github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl
github.com ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBEmKSENjQEezOmxkZMy7opKgwFB9nkt5YRrYMjNuG5N87uRgg6CLrbo5wAdT/y6v0mKV0U2w0WZ2YB/++Tpockg=
github.com ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCj7ndNxQowgcQnjshcLrqPEiiphnt+VTTvDP6mHBL9j1aNUkY4Ue1gvwnGLVlOhGeYrnZaMgRK6+PKCUXaDbC7qtbW8gIkhL7aGCsOr/C56SJMy/BCZfxd1nWzAOxSDPgVsmerOBYfNqltV9/hWCqBywINIR+5dIg6JTJ72pcEpEjcYgXkE2YEFXV1JHnsKgbLWNlhScqb2UmyRkQyytRLtL+38TGxkxCflmO+5Z8CSSNY7GidjMIZ7Q4zMjA2n1nGrlTDkzwDCsw+wqFPGQA179cnfGWOWRVruj16z6XyvxvjJwbz0wQZ75XK5tKSb7FNyeIEs4TT4jk+S4dhPeAUC5y+bDYirYgM4GC7uEnztnZyaVWQ7B381AK4Qdrwt51ZqExKbQpTUNn+EjqoTwvqNj4kqx5QUCI0ThS/YkOxJCXmPUWZbhjpCg56i+2aB6CmK2JGhn57K5mj0MNdBXA4/WnwH6XoPWJzK5Nyu2zB3nAZp+S5hpQs+p1vN1/wsjk=
`

// DeployKey는 서비스별로 만든 ed25519 deploy key다.
type DeployKey struct {
	PrivatePEM  string // OpenSSH 형식 개인키(저장할 때는 암호화한다)
	PublicKey   string // authorized_keys 한 줄(GitHub에 등록)
	Fingerprint string // SHA256:...
}

// GenerateDeployKey는 새 ed25519 키 쌍을 만든다. comment는 공개키 줄 끝에 붙는다.
func GenerateDeployKey(comment string) (*DeployKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	blk, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return nil, err
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp)))
	if comment != "" {
		line += " " + comment
	}
	return &DeployKey{PrivatePEM: string(pem.EncodeToMemory(blk)), PublicKey: line, Fingerprint: ssh.FingerprintSHA256(sp)}, nil
}

// SSHEnv는 ssh로 git을 실행할 환경을 만든다. 개인키는 0600 임시 파일에 두고(cleanup이 지운다), GIT_SSH_COMMAND에는 그 경로만 들어간다.
// 호스트 키는 known_hosts에 고정한다: 이미 아는 호스트는 키가 다르면 거부하고, 모르는 호스트는 처음 본 키를 기록한다(accept-new).
// 반환하는 knownHostsPath를 clone 뒤에 읽으면 새로 기록된 키를 얻는다.
func SSHEnv(keyPEM, knownHosts string) (env []string, knownHostsPath string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "keel-ssh-")
	if err != nil {
		return nil, "", nil, err
	}
	cleanup = func() { os.RemoveAll(dir) }
	keyPath := filepath.Join(dir, "id")
	knownHostsPath = filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(keyPath, []byte(strings.TrimRight(keyPEM, "\n")+"\n"), 0o600); err != nil {
		cleanup()
		return nil, "", nil, err
	}
	if err := os.WriteFile(knownHostsPath, []byte(MergeKnownHosts(BuiltinKnownHosts, knownHosts)), 0o600); err != nil {
		cleanup()
		return nil, "", nil, err
	}
	cmd := fmt.Sprintf("ssh -i %s -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=%s -o HashKnownHosts=no -o GlobalKnownHostsFile=/dev/null -o IdentityAgent=none",
		keyPath, knownHostsPath)
	env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/bin/true", "GIT_SSH_COMMAND="+cmd)
	return env, knownHostsPath, cleanup, nil
}

// MergeKnownHosts는 두 known_hosts 텍스트를 줄 단위로 합친다(중복 제거, 순서 유지).
func MergeKnownHosts(a, b string) string {
	seen := map[string]bool{}
	var out []string
	for _, l := range strings.Split(a+"\n"+b, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return strings.Join(out, "\n") + "\n"
}

// CloneSSH는 deploy key로 SSH clone을 한다. 새로 기록된 known_hosts 전체(내장 키 포함)를 함께 돌려준다.
func CloneSSH(ctx context.Context, repo, branch, sha, dir, keyPEM, knownHosts string, out func(string)) (*Commit, string, error) {
	env, khPath, cleanup, err := SSHEnv(keyPEM, knownHosts)
	if err != nil {
		return nil, "", err
	}
	defer cleanup()
	c, err := cloneEnv(ctx, env, repo, branch, sha, dir, out)
	kh, _ := os.ReadFile(khPath)
	return c, string(kh), err
}

// IsSSHRepo는 repo URL이 SSH 전송을 쓰는지 알려준다.
func IsSSHRepo(repo string) bool {
	return strings.HasPrefix(repo, "ssh://") || strings.HasPrefix(repo, "git@")
}
