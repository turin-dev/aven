package api

import (
	"context"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/node"
)

// 네트워크 진단: 서비스의 실행 중인 replica 안에서 이름 해석(DNS)과 TCP 연결을 시험한다.
// 컨테이너 안의 도구(getent/nslookup, nc/bash)를 고정된 스크립트로 부르고, 호스트와 포트는 위치 인자로만 넘긴다(셸 해석 없음).

var diagnosticHostRe = regexp.MustCompile(`(?i)^[a-z0-9_](?:[a-z0-9_-]{0,61}[a-z0-9])?(?:\.[a-z0-9_](?:[a-z0-9_-]{0,61}[a-z0-9])?)*\.?$`)

type networkDiagnosticRequest struct {
	Host    string `json:"host"`
	Port    int    `json:"port"`    // 0이면 DNS만 확인한다
	Replica string `json:"replica"` // 비우면 첫 replica
}

type networkDiagnosticCheck struct {
	OK        bool    `json:"ok"`
	Available bool    `json:"available"` // 컨테이너에 도구가 있었는지
	Skipped   bool    `json:"skipped,omitempty"`
	ExitCode  int     `json:"exit_code"`
	Duration  float64 `json:"duration_ms"`
	Output    string  `json:"output"`
}

func validDiagnosticHost(host string) bool {
	if host == "" || len(host) > 253 || strings.HasPrefix(host, "-") {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	return diagnosticHostRe.MatchString(host)
}

func (s *Server) networkDiagnostics(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	var req networkDiagnosticRequest
	if !decode(w, r, &req) {
		return
	}
	req.Host = strings.TrimSpace(req.Host)
	if !validDiagnosticHost(req.Host) {
		writeErr(w, 400, "host must be a hostname or IP address")
		return
	}
	if req.Port < 0 || req.Port > 65535 {
		writeErr(w, 400, "port must be 1..65535 (0 = DNS only)")
		return
	}
	d, container, code, msg := s.execTarget(r.Context(), c.in, strings.TrimSpace(req.Replica))
	if d == nil {
		writeErr(w, code, msg)
		return
	}
	nodeID := d.NodeOf(container)
	dns := s.runNetworkDiagnostic(r.Context(), nodeID, container, []string{"sh", "-c", dnsDiagnosticScript, "keel-dns", req.Host})
	tcp := networkDiagnosticCheck{Skipped: true, Available: true}
	if req.Port > 0 {
		tcp = s.runNetworkDiagnostic(r.Context(), nodeID, container, []string{"sh", "-c", tcpDiagnosticScript, "keel-tcp", req.Host, strconv.Itoa(req.Port)})
	}
	writeJSON(w, 200, map[string]any{
		"checked_at": time.Now().UTC(), "service_id": c.svc.ID, "replica": container,
		"host": req.Host, "port": req.Port, "dns": dns, "tcp": tcp,
	})
}

const dnsDiagnosticScript = `if command -v getent >/dev/null 2>&1; then exec getent ahosts "$1"; fi
if command -v nslookup >/dev/null 2>&1; then exec nslookup "$1"; fi
echo "DNS check unavailable: getent and nslookup are not installed in this image" >&2
exit 127`

const tcpDiagnosticScript = `if command -v nc >/dev/null 2>&1; then nc -z -w 3 "$1" "$2" && { echo "connected to $1:$2"; exit 0; }; exit 1; fi
if command -v bash >/dev/null 2>&1; then exec bash -c 'exec 3<>"/dev/tcp/$1/$2" && echo "connected to $1:$2"' keel-tcp "$1" "$2"; fi
if command -v busybox >/dev/null 2>&1; then busybox nc -z -w 3 "$1" "$2" && { echo "connected to $1:$2"; exit 0; }; exit 1; fi
echo "TCP check unavailable: nc and bash are not installed in this image" >&2
exit 127`

func (s *Server) runNetworkDiagnostic(ctx context.Context, nodeID, container string, cmd []string) networkDiagnosticCheck {
	started := time.Now()
	callCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out, err := s.dataBackend().ExecRun(callCtx, nodeID, container, node.ExecRunRequest{Cmd: cmd, MaxOutput: 4096}, 6*time.Second)
	took := float64(time.Since(started).Microseconds()) / 1000
	if err != nil {
		return networkDiagnosticCheck{ExitCode: -1, Available: true, Duration: took, Output: err.Error()}
	}
	if out == nil {
		return networkDiagnosticCheck{ExitCode: -1, Available: true, Duration: took, Output: "diagnostic command returned no result"}
	}
	output := strings.TrimSpace(string(out.Stdout) + "\n" + string(out.Stderr))
	if len(output) > 4096 {
		output = output[:4096]
	}
	return networkDiagnosticCheck{OK: out.ExitCode == 0, Available: out.ExitCode != 127, ExitCode: out.ExitCode, Duration: took, Output: output}
}
