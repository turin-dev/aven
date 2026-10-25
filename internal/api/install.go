package api

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// installAgentScript는 GET /install-agent.sh로 내보내는 POSIX sh 설치 스크립트다(인증 없음, 비밀 없음).
//
//go:embed install-agent.sh
var installAgentScript []byte

var agentArches = map[string]bool{"amd64": true, "arm64": true}

func (s *Server) installScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "install-agent.sh", time.Time{}, strings.NewReader(string(installAgentScript)))
}

// agentBinary는 KEEL_AGENT_BIN_DIR(기본: aven-server 실행 파일과 같은 디렉터리)의 aven-agent-linux-<arch>를 찾는다.
func (s *Server) agentBinary(arch string) (string, error) {
	name := "aven-agent-linux-" + arch
	p, _, err := s.releaseFile(name)
	if err == nil {
		return p, nil
	}
	// Existing deployments may still have the previous artifact name beside the server.
	p, _, legacyErr := s.releaseFile("keel-agent-linux-" + arch)
	if legacyErr != nil {
		return "", err
	}
	return p, nil
}

type sumEntry struct {
	size int64
	mod  time.Time
	sum  string
}

var agentSums sync.Map // path → sumEntry

func fileSHA256(p string, st os.FileInfo) (string, error) {
	if e, ok := agentSums.Load(p); ok {
		if e := e.(sumEntry); e.size == st.Size() && e.mod.Equal(st.ModTime()) {
			return e.sum, nil
		}
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	agentSums.Store(p, sumEntry{st.Size(), st.ModTime(), sum})
	return sum, nil
}

// cliFile은 Aven CLI 배포 파일 하나다(aven-<os>-<arch>[.exe]). make clis가 bin/에 만든다.
type cliFile struct {
	Name string `json:"name"`
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// cliFiles는 /downloads/로 내보내는 aven CLI 바이너리 목록이다(이 이름 외에는 404).
var cliFiles = func() []cliFile {
	var out []cliFile
	for _, osName := range []string{"linux", "darwin", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			name := "aven-" + osName + "-" + arch
			if osName == "windows" {
				name += ".exe"
			}
			out = append(out, cliFile{name, osName, arch})
		}
	}
	return out
}()

func canonicalCLIFile(name string) (string, bool) {
	for _, f := range cliFiles {
		if f.Name == name {
			return f.Name, true
		}
	}
	if strings.HasPrefix(name, "keel-") {
		candidate := "aven-" + strings.TrimPrefix(name, "keel-")
		for _, f := range cliFiles {
			if f.Name == candidate {
				return f.Name, true
			}
		}
	}
	return "", false
}

// cliBinary accepts existing keel-* files when a deployment has not yet copied the Aven artifacts.
func (s *Server) cliBinary(name string) (string, os.FileInfo, error) {
	p, st, err := s.releaseFile(name)
	if err == nil {
		return p, st, nil
	}
	legacy := "keel-" + strings.TrimPrefix(name, "aven-")
	return s.releaseFile(legacy)
}

// binDir는 배포 바이너리 디렉터리다: KEEL_AGENT_BIN_DIR, 비우면 aven-server 실행 파일과 같은 디렉터리.
func (s *Server) binDir() (string, error) {
	if s.AgentBinDir != "" {
		return s.AgentBinDir, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

// releaseFile은 binDir 안의 일반 파일 name을 찾는다(name은 호출자가 허용 목록으로 검사한다).
func (s *Server) releaseFile(name string) (string, os.FileInfo, error) {
	dir, err := s.binDir()
	if err != nil {
		return "", nil, err
	}
	p := filepath.Join(dir, name)
	st, err := os.Stat(p)
	if err != nil {
		return "", nil, err
	}
	if !st.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%s is not a regular file", p)
	}
	return p, st, nil
}

// cliDownloads: GET /api/cli/downloads (인증 없음) → 이 서버에 있는 aven CLI 바이너리 목록(크기, sha256).
func (s *Server) cliDownloads(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		cliFile
		URL       string    `json:"url"`
		Size      int64     `json:"size"`
		SHA256    string    `json:"sha256"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	files := []entry{}
	for _, f := range cliFiles {
		p, st, err := s.cliBinary(f.Name)
		if err != nil {
			continue
		}
		sum, err := fileSHA256(p, st)
		if err != nil {
			continue
		}
		files = append(files, entry{f, "/downloads/" + f.Name, st.Size(), sum, st.ModTime().UTC()})
	}
	w.Header().Set("Cache-Control", "no-cache")
	writeJSON(w, 200, map[string]any{"version": s.Version, "files": files, "install_script": "/install.sh"})
}

// installCLIScript: GET /install.sh (인증 없음) → aven CLI 설치 스크립트(Linux/macOS).
func (s *Server) installCLIScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "install.sh", time.Time{}, strings.NewReader(string(installCLIScriptBody)))
}

// installCLIScriptBody는 GET /install.sh로 내보내는 aven CLI 설치 스크립트다.
//
//go:embed install-cli.sh
var installCLIScriptBody []byte

// downloadAgent: GET /downloads/{aven-agent-linux-{amd64,arm64}, aven-<os>-<arch>[.exe]}[.sha256] (인증 없음).
func (s *Server) downloadAgent(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	base, checksum := strings.CutSuffix(name, ".sha256")
	var p string
	if cliName, ok := canonicalCLIFile(base); ok {
		var err error
		if p, _, err = s.cliBinary(cliName); err != nil {
			writeErr(w, 404, base+" is not available on this server")
			return
		}
	} else {
		arch, ok := strings.CutPrefix(base, "aven-agent-linux-")
		if !ok {
			arch, ok = strings.CutPrefix(base, "keel-agent-linux-")
		}
		if !ok || !agentArches[arch] {
			writeErr(w, 404, "not found")
			return
		}
		var err error
		if p, err = s.agentBinary(arch); err != nil {
			writeErr(w, 404, "aven-agent binary for linux/"+arch+" is not available on this server")
			return
		}
	}
	f, err := os.Open(p)
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeErr(w, 500, "stat failed")
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if checksum {
		sum, err := fileSHA256(p, st)
		if err != nil {
			writeErr(w, 500, "checksum failed")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "%s  %s\n", sum, base)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+base+`"`)
	http.ServeContent(w, r, base, st.ModTime(), f)
}

// joinCommand는 새 서버에서 실행할 한 줄 설치 명령이다. 토큰은 응답에서만 1회 돌려주고 서버 로그에는 남기지 않는다.
func joinCommand(publicURL, token string) string {
	u := strings.TrimRight(publicURL, "/")
	return fmt.Sprintf("curl -fsSL %s/install-agent.sh | sudo sh -s -- --server %s --token '%s'", u, u, token)
}

// validPublicURL: join 명령(셸 한 줄)에 들어가므로 https://host[:port][/path] 형태의 안전한 문자만 허용한다.
func validPublicURL(u string) bool {
	rest, ok := strings.CutPrefix(u, "https://")
	if !ok || rest == "" || strings.HasPrefix(rest, "/") {
		return false
	}
	for _, c := range rest {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune(".-_:/[]", c)) {
			return false
		}
	}
	return true
}
