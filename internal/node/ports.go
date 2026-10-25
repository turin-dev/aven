package node

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// 포트 감지: 컨테이너 안의 프로세스가 listen 중인 TCP 포트를 호스트에서 읽는다.
// 컨테이너의 init PID(docker inspect .State.Pid)로 /proc/<pid>/net/tcp{,6}를 열면 그 컨테이너의 network namespace가 보인다.
// docker exec를 쓰지 않으므로 셸이 없는 distroless 이미지에서도 동작한다. 호스트의 /proc를 읽어야 하므로
// aven-server와 aven-agent는 root(systemd 기본, User= 없음)로 돈다는 전제다.

// ListeningPorts는 컨테이너(들)가 listen 중인 TCP 포트다.
// Ports는 프록시가 닿을 수 있는 주소(0.0.0.0, ::, 컨테이너 IP)에서 listen하는 포트,
// Loopback은 127.0.0.1/::1에서만 listen해서 프록시가 닿지 못하는 포트다(UI가 경고한다). 둘 다 오름차순이다.
type ListeningPorts struct {
	Ports    []int `json:"ports"`
	Loopback []int `json:"loopback"`
	// Containers는 실제로 읽은 실행 중인 컨테이너 수다(0이면 읽을 컨테이너가 없었다).
	Containers int `json:"containers"`
}

// Listener는 /proc/net/tcp{,6}의 LISTEN 소켓 하나다.
type Listener struct {
	Addr netip.Addr
	Port int
}

// tcpListen은 /proc/net/tcp의 st 값 TCP_LISTEN(0x0A)이다.
const tcpListen = "0A"

// ParseProcNetTCP는 /proc/<pid>/net/tcp 또는 tcp6 내용에서 LISTEN 소켓을 고른다.
// 주소는 커널이 32비트 단위로 호스트 바이트 순서로 찍으므로 order는 그 노드의 바이트 순서다(보통 binary.NativeEndian).
func ParseProcNetTCP(r io.Reader, order binary.ByteOrder) ([]Listener, error) {
	var out []Listener
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first { // 머리글: "sl local_address rem_address st ..."
			first = false
			if strings.Contains(line, "local_address") {
				continue
			}
		}
		f := strings.Fields(line)
		if len(f) < 4 || f[3] != tcpListen {
			continue
		}
		l, err := parseLocal(f[1], order)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, sc.Err()
}

// parseLocal은 "0100007F:1F90"(IPv4) 또는 32자리 hex(IPv6):포트를 읽는다.
func parseLocal(s string, order binary.ByteOrder) (Listener, error) {
	hexAddr, hexPort, ok := strings.Cut(s, ":")
	if !ok {
		return Listener{}, fmt.Errorf("proc net: bad address %q", s)
	}
	port, err := strconv.ParseUint(hexPort, 16, 16)
	if err != nil {
		return Listener{}, fmt.Errorf("proc net: bad port %q", s)
	}
	if len(hexAddr) != 8 && len(hexAddr) != 32 {
		return Listener{}, fmt.Errorf("proc net: bad address %q", s)
	}
	b := make([]byte, len(hexAddr)/2)
	for i := 0; i < len(hexAddr); i += 8 {
		w, err := strconv.ParseUint(hexAddr[i:i+8], 16, 32)
		if err != nil {
			return Listener{}, fmt.Errorf("proc net: bad address %q", s)
		}
		order.PutUint32(b[i/2:], uint32(w))
	}
	addr, _ := netip.AddrFromSlice(b)
	return Listener{Addr: addr, Port: int(port)}, nil
}

// isLoopback은 프록시가 닿지 못하는 주소다(IPv4-mapped ::ffff:127.0.0.1 포함).
func isLoopback(a netip.Addr) bool { return a.Unmap().IsLoopback() }

// SummarizeListeners는 LISTEN 소켓을 포트 목록으로 줄인다. 한 포트라도 loopback이 아닌 주소에서 listen하면 Ports,
// loopback에서만 listen하면 Loopback이다.
func SummarizeListeners(ls []Listener) ListeningPorts {
	reach := map[int]bool{}
	loop := map[int]bool{}
	for _, l := range ls {
		if l.Port <= 0 {
			continue
		}
		if isLoopback(l.Addr) {
			loop[l.Port] = true
		} else {
			reach[l.Port] = true
		}
	}
	out := ListeningPorts{Ports: []int{}, Loopback: []int{}}
	for p := range reach {
		out.Ports = append(out.Ports, p)
	}
	for p := range loop {
		if !reach[p] {
			out.Loopback = append(out.Loopback, p)
		}
	}
	sort.Ints(out.Ports)
	sort.Ints(out.Loopback)
	return out
}

// ReadListeners는 procRoot(보통 "/proc") 아래 pid의 net/tcp와 net/tcp6에서 LISTEN 소켓을 읽는다.
// IPv6가 꺼진 커널에는 tcp6가 없으므로 둘 중 하나만 있어도 된다.
func ReadListeners(procRoot string, pid int) ([]Listener, error) {
	if pid <= 0 {
		return nil, errors.New("container is not running")
	}
	var out []Listener
	found := false
	for _, name := range []string{"tcp", "tcp6"} {
		f, err := os.Open(filepath.Join(procRoot, strconv.Itoa(pid), "net", name))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		ls, err := ParseProcNetTCP(f, binary.NativeEndian)
		f.Close()
		if err != nil {
			return nil, err
		}
		found = true
		out = append(out, ls...)
	}
	if !found {
		return nil, fmt.Errorf("process %d not found", pid)
	}
	return out, nil
}

// MaxPortScanContainers는 한 인스턴스에서 읽는 replica 수 상한이다(replica는 같은 이미지라 포트도 같다).
const MaxPortScanContainers = 8

// ContainerPIDs는 실행 중인 컨테이너의 init PID다(멈춘 컨테이너는 0이라 빠진다).
func (d *Docker) ContainerPIDs(ctx context.Context, names []string) (map[string]int, error) {
	out := map[string]int{}
	if len(names) == 0 {
		return out, nil
	}
	args := append([]string{"inspect", "--type", "container", "--format", "{{.Name}}\t{{.State.Pid}}"}, names...)
	s, err := run(ctx, nil, nil, d.bin(), args...)
	if err != nil && !strings.Contains(s, "No such") {
		return nil, err
	}
	for _, line := range strings.Split(s, "\n") {
		name, pid, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(pid); err == nil && n > 0 {
			out[strings.TrimPrefix(name, "/")] = n
		}
	}
	return out, nil
}

// InstanceListeningPorts는 이 노드에서 도는 인스턴스의 app 컨테이너(replica 전체, 최대 MaxPortScanContainers개)가
// listen 중인 포트의 합집합이다. keel.managed 라벨이 있는 컨테이너만 본다.
func (d *Docker) InstanceListeningPorts(ctx context.Context, instanceID string) (*ListeningPorts, error) {
	return InstanceListeningPorts(ctx, d, "/proc", instanceID)
}

// containerLister는 InstanceListeningPorts에 필요한 docker 기능이다(테스트에서 바꾼다).
type containerLister interface {
	ListManaged(ctx context.Context) (map[string]map[string]string, error)
	ContainerPIDs(ctx context.Context, names []string) (map[string]int, error)
}

// InstanceListeningPorts는 d로 인스턴스의 app 컨테이너를 찾아 procRoot에서 포트를 읽는다.
func InstanceListeningPorts(ctx context.Context, d containerLister, procRoot, instanceID string) (*ListeningPorts, error) {
	managed, err := d.ListManaged(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for name, l := range managed {
		if l[LabelInstance] == instanceID && l[LabelRole] == RoleApp {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) > MaxPortScanContainers {
		names = names[:MaxPortScanContainers]
	}
	pids, err := d.ContainerPIDs(ctx, names)
	if err != nil {
		return nil, err
	}
	var all []Listener
	n := 0
	var lastErr error
	for _, name := range names {
		pid := pids[name]
		if pid <= 0 {
			continue
		}
		ls, err := ReadListeners(procRoot, pid)
		if err != nil { // 그 사이에 컨테이너가 재시작했을 수 있다
			lastErr = err
			continue
		}
		n++
		all = append(all, ls...)
	}
	if n == 0 && lastErr != nil {
		return nil, lastErr
	}
	out := SummarizeListeners(all)
	out.Containers = n
	return &out, nil
}
