package node

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestGenerateDeployKey(t *testing.T) {
	k, err := GenerateDeployKey("keel-deploy-api")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(k.PublicKey, "ssh-ed25519 ") || !strings.HasSuffix(k.PublicKey, " keel-deploy-api") {
		t.Errorf("public key line: %s", k.PublicKey)
	}
	if !strings.HasPrefix(k.PrivatePEM, "-----BEGIN OPENSSH PRIVATE KEY-----") {
		t.Error("private key must be OpenSSH PEM")
	}
	signer, err := ssh.ParsePrivateKey([]byte(k.PrivatePEM))
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k.PublicKey))
	if err != nil || string(pub.Marshal()) != string(signer.PublicKey().Marshal()) {
		t.Error("public key does not match private key")
	}
	if ssh.FingerprintSHA256(pub) != k.Fingerprint {
		t.Error("fingerprint")
	}
	k2, _ := GenerateDeployKey("")
	if k2.PrivatePEM == k.PrivatePEM {
		t.Error("keys must differ")
	}
}

func TestSSHEnvKeyFileAndPinning(t *testing.T) {
	k, _ := GenerateDeployKey("x")
	env, kh, cleanup, err := SSHEnv(k.PrivatePEM, "git.example.com ssh-ed25519 AAAAexample")
	if err != nil {
		t.Fatal(err)
	}
	var cmd string
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "GIT_SSH_COMMAND="); ok {
			cmd = v
		}
		if strings.Contains(e, "OPENSSH PRIVATE KEY") || strings.Contains(e, k.PrivatePEM[40:80]) {
			t.Fatal("private key material leaked into environment")
		}
	}
	if strings.Contains(cmd, "BEGIN") {
		t.Error("key in command line")
	}
	for _, want := range []string{"IdentitiesOnly=yes", "BatchMode=yes", "StrictHostKeyChecking=accept-new", "UserKnownHostsFile=" + kh, "GlobalKnownHostsFile=/dev/null"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("GIT_SSH_COMMAND lacks %s: %s", want, cmd)
		}
	}
	if strings.Contains(cmd, "StrictHostKeyChecking=no") {
		t.Error("host key checking must not be disabled")
	}
	b, _ := os.ReadFile(kh)
	if !strings.Contains(string(b), "github.com ssh-ed25519 ") || !strings.Contains(string(b), "git.example.com ssh-ed25519") {
		t.Errorf("known_hosts: %s", b)
	}
	// 키 파일은 0600
	keyPath := strings.Fields(strings.SplitN(cmd, "-i ", 2)[1])[0]
	st, err := os.Stat(keyPath)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("key file perm: %v %v", st, err)
	}
	cleanup()
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Error("key file not removed")
	}
}

func TestMergeKnownHostsAndRepoKind(t *testing.T) {
	m := MergeKnownHosts("a x y\nb x y", "b x y\n\n# c\nc x y")
	if m != "a x y\nb x y\nc x y\n" {
		t.Errorf("%q", m)
	}
	if !IsSSHRepo("git@github.com:o/r.git") || !IsSSHRepo("ssh://h/r") || IsSSHRepo("https://github.com/o/r") {
		t.Error("IsSSHRepo")
	}
}

func TestSplitSecrets(t *testing.T) {
	rest, sec := SplitSecrets(map[string]string{"A": "1", "TOKEN": "s", "B": "2"}, []string{"TOKEN", "MISSING"})
	if len(sec) != 1 || sec["TOKEN"] != "s" || len(rest) != 2 || rest["TOKEN"] != "" {
		t.Errorf("%v %v", rest, sec)
	}
}

func TestRunSpecLimitsAndBuildSecretArgs(t *testing.T) {
	args, _ := RunSpec{Name: "n", Image: "i", MemoryMB: 256, CPUs: "0.5"}.args(true)
	j := strings.Join(args, " ")
	if !strings.Contains(j, "--memory 256m") || !strings.Contains(j, "--cpus 0.5") {
		t.Errorf("limits missing: %s", j)
	}
	args, _ = RunSpec{Name: "n", Image: "i"}.args(true)
	if strings.Contains(strings.Join(args, " "), "--memory") {
		t.Error("no limit expected")
	}
}
