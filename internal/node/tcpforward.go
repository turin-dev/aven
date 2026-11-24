package node

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// MaxTCPAllowCIDRs bounds a TCP proxy allowlist. Each connection walks the whole list in a shell.
const MaxTCPAllowCIDRs = 32

// tcpAllowCheck runs (through socat SYSTEM) for every accepted connection when a TCP proxy has an
// allowlist. socat puts the client address in SOCAT_PEERADDR; the forwarder listens on IPv4 only, so
// it is a dotted IPv4 address. The script contains no commas, which socat would read as option
// separators, and gets its data from the environment, never from interpolation.
const tcpAllowCheck = `toint() { local IFS=.; set -- $1; echo $(( ($1 << 24) + ($2 << 16) + ($3 << 8) + $4 )); }
p=$(toint "${SOCAT_PEERADDR#::ffff:}") || exit 1
for c in $AVEN_TCP_ALLOW; do
  b=${c#*/}
  m=$(( b == 0 ? 0 : (0xFFFFFFFF << (32 - b)) & 0xFFFFFFFF ))
  if [ $(( p & m )) -eq $(( $(toint "${c%/*}") & m )) ]; then exec socat - "TCP:$AVEN_TCP_TARGET"; fi
done
echo "aven: refused $SOCAT_PEERADDR: not in the TCP proxy allowlist" >&2
exit 1`

// TCPAllowAll is the allowlist entry that admits every client. A list containing it normalizes to
// exactly [TCPAllowAll], so a service can opt out of the server-wide default list.
const TCPAllowAll = "0.0.0.0/0"

// NormalizeTCPAllowCIDRs validates an allowlist and returns it as masked IPv4 prefixes, sorted and
// without duplicates. A bare address is a /32. An empty result means "no list" (the default applies),
// and a list that contains 0.0.0.0/0 becomes [TCPAllowAll].
//
// Only IPv4 is accepted: the forwarder sees IPv4 peers only (IPv6 clients that Docker proxies arrive
// from the bridge gateway), so an IPv6 entry could never match.
func NormalizeTCPAllowCIDRs(in []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	allowAll := false
	for _, raw := range in {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		p, err := parseTCPAllowPrefix(s)
		if err != nil {
			return nil, err
		}
		// Every entry is parsed before 0.0.0.0/0 short-circuits, so a typo next to an
		// allow-all entry is still rejected instead of silently opening the proxy.
		if p.Bits() == 0 {
			allowAll = true
			continue
		}
		if k := p.String(); !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	if allowAll {
		return []string{TCPAllowAll}, nil
	}
	if len(out) > MaxTCPAllowCIDRs {
		return nil, fmt.Errorf("at most %d ranges are allowed", MaxTCPAllowCIDRs)
	}
	sortPrefixes(out)
	return out, nil
}

// parseTCPAllowPrefix parses one allowlist entry as a masked IPv4 prefix.
// An IPv4-mapped address is accepted only when its prefix length covers the
// mapped IPv4 bits (96 or more). A shorter length, such as ::ffff:10.0.0.0/8,
// is not the IPv4 range 10.0.0.0/8; treating it as 0.0.0.0/0 would allow every client.
func parseTCPAllowPrefix(s string) (netip.Prefix, error) {
	var p netip.Prefix
	var err error
	if strings.Contains(s, "/") {
		p, err = netip.ParsePrefix(s)
	} else {
		var a netip.Addr
		a, err = netip.ParseAddr(s)
		if err == nil {
			p = netip.PrefixFrom(a, a.BitLen())
		}
	}
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%q is not an IP address or CIDR range", s)
	}
	if a := p.Addr(); a.Is4In6() {
		bits := p.Bits() - 96
		if bits < 0 {
			return netip.Prefix{}, fmt.Errorf("%q: only IPv4 ranges are supported (the TCP proxy cannot see IPv6 client addresses)", s)
		}
		p = netip.PrefixFrom(a.Unmap(), bits)
	}
	if !p.Addr().Is4() {
		return netip.Prefix{}, fmt.Errorf("%q: only IPv4 ranges are supported (the TCP proxy cannot see IPv6 client addresses)", s)
	}
	return p.Masked(), nil
}

func sortPrefixes(ps []string) {
	key := func(s string) (netip.Addr, int) {
		p := netip.MustParsePrefix(s)
		return p.Addr(), p.Bits()
	}
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0; j-- {
			a, ab := key(ps[j-1])
			b, bb := key(ps[j])
			if c := a.Compare(b); c < 0 || c == 0 && ab <= bb {
				break
			}
			ps[j-1], ps[j] = ps[j], ps[j-1]
		}
	}
}

// TCPForward is the socat forwarder of a TCP proxy: it listens on Port and forwards to Target:Port.
type TCPForward struct {
	Port   int
	Target string
	Allow  []string // normalized (NormalizeTCPAllowCIDRs); empty or [TCPAllowAll] forwards every client
}

// Restricted reports whether the forwarder checks client addresses.
func (f TCPForward) Restricted() bool {
	return len(f.Allow) > 0 && !(len(f.Allow) == 1 && f.Allow[0] == TCPAllowAll)
}

// EffectiveTCPAllow is the list a proxy enforces: its own list when it has one, else the default.
func EffectiveTCPAllow(own, def []string) []string {
	if len(own) > 0 {
		return own
	}
	return def
}

// Signature identifies the forwarder's configuration; a container whose label differs is replaced.
// Without an allowlist it is the same "public:container:target" value earlier versions wrote, so
// existing forwarders are not recreated on upgrade.
func (f TCPForward) Signature(publicPort int) string {
	sig := fmt.Sprintf("%d:%d:%s", publicPort, f.Port, f.Target)
	if f.Restricted() {
		sig += ":allow=" + strings.Join(f.Allow, ",")
	}
	return sig
}

// Cmd and Env are the container command (arguments to socat) and environment.
func (f TCPForward) Cmd() []string {
	listen := fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr", f.Port)
	if !f.Restricted() {
		return []string{listen, fmt.Sprintf("TCP:%s:%d", f.Target, f.Port)}
	}
	// socat 1.8 removes double quotes from an address unless the parameter is single-quoted.
	return []string{listen, `SYSTEM:'eval "$AVEN_TCP_CHECK"'`}
}

func (f TCPForward) Env() map[string]string {
	if !f.Restricted() {
		return nil
	}
	return map[string]string{
		"AVEN_TCP_CHECK":  tcpAllowCheck,
		"AVEN_TCP_ALLOW":  strings.Join(f.Allow, " "),
		"AVEN_TCP_TARGET": f.Target + ":" + strconv.Itoa(f.Port),
	}
}

// ErrTCPAllowUnsupported is returned when a node's agent is too old to enforce an allowlist.
var ErrTCPAllowUnsupported = errors.New("this node's aven-agent does not support TCP proxy allowlists; upgrade the agent")
