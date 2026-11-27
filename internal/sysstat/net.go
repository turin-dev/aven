package sysstat

import (
	"strconv"
	"strings"
)

// virtualIfacePrefixes는 호스트 밖으로 나가지 않거나(브리지, veth) 물리 NIC 트래픽을 한 번 더 세는(VPN 터널) 인터페이스다.
// 회선의 월 데이터 상한은 물리 NIC를 지나간 바이트로 재므로 이들은 뺀다.
var virtualIfacePrefixes = []string{"lo", "docker", "br-", "veth", "virbr", "vnet", "tailscale", "wg", "tun", "tap", "cni", "flannel", "cali", "vxlan", "zt", "kube", "dummy", "ifb"}

// PhysicalIface는 이름으로 보아 물리(또는 상위 링크) 인터페이스인지다.
func PhysicalIface(name string) bool {
	if name == "" {
		return false
	}
	for _, p := range virtualIfacePrefixes {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	return true
}

// ParseNetDev는 /proc/net/dev에서 물리 인터페이스의 받은/보낸 바이트 합을 읽는다.
func ParseNetDev(s string) (rx, tx uint64, ok bool) {
	for _, line := range strings.Split(s, "\n") {
		name, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		f := strings.Fields(rest)
		if len(f) < 9 || !PhysicalIface(name) {
			continue
		}
		r, err1 := strconv.ParseUint(f[0], 10, 64)
		t, err2 := strconv.ParseUint(f[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		rx += r
		tx += t
		ok = true
	}
	return rx, tx, ok
}

// NetBytes는 호스트 물리 인터페이스의 누적 받은/보낸 바이트다(부팅 이후, 재부팅하면 0부터). 읽을 수 없으면 ok=false.
func NetBytes() (rx, tx uint64, ok bool) { return hostNet() }
