package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"strconv"
	"strings"
)

// aven tcp-proxy: 서비스의 TCP 프록시(공개 host:port → 컨테이너 포트). API: GET/POST/PATCH/DELETE …/tcp-proxy.
// aven networking(별칭 net): GET …/networking 개요(감지된 포트, 실제 포트, 도메인, TCP 프록시, 사설 이름, 아웃바운드 IPv6).

type tcpProxyStatus struct {
	Enabled       bool   `json:"enabled"`
	Host          string `json:"host"`
	PublicPort    *int   `json:"public_port"`
	ContainerPort *int   `json:"container_port"`
	Available     bool   `json:"available"`
	Reason        string `json:"reason"`
}

type networkingOverview struct {
	DetectedPorts []struct {
		Port   int    `json:"port"`
		Source string `json:"source"`
	} `json:"detected_ports"`
	PortDetection struct {
		Source     string `json:"source"`
		Reason     string `json:"reason"`
		Node       string `json:"node"`
		Containers int    `json:"containers"`
	} `json:"port_detection"`
	EffectivePort int            `json:"effective_port"`
	Domains       []domainDetail `json:"domains"`
	TCPProxy      tcpProxyStatus `json:"tcp_proxy"`
	Private       struct {
		DNSName   string   `json:"dns_name"`
		FQDN      string   `json:"fqdn"`
		Aliases   []string `json:"aliases"`
		IPv4      []string `json:"ipv4"`
		IPv6      []string `json:"ipv6"`
		MeshPorts []int    `json:"mesh_ports"`
	} `json:"private"`
	Outbound struct {
		IPv6Enabled      bool   `json:"ipv6_enabled"`
		NodeIPv6Egress   *bool  `json:"node_ipv6_egress"`
		EgressReason     string `json:"egress_reason"`
		EnforceSupported *bool  `json:"enforce_supported"`
		EnforceReason    string `json:"enforce_reason"`
		Node             string `json:"node"`
		StaticIPs        struct {
			IPv4 *string `json:"ipv4"`
			IPv6 *string `json:"ipv6"`
		} `json:"static_ips"`
	} `json:"outbound"`
}

func cmdTCPProxy(c *config, args []string) {
	fl := flag.NewFlagSet("tcp-proxy", flag.ExitOnError)
	port := fl.Int("port", 0, "container port behind the proxy (enable; default: the service port)")
	fl.IntVar(port, "p", 0, "container port behind the proxy")
	yes := fl.Bool("y", false, "don't ask for confirmation")
	fl.BoolVar(yes, "yes", false, "don't ask for confirmation")
	jsonOut := fl.Bool("json", false, "print the response as JSON")
	tf := addTargetFlags(fl)
	fl.Parse(reorderFlags(args))
	sub, rest := "status", fl.Args()
	if len(rest) > 0 {
		sub, rest = rest[0], rest[1:]
	}
	l := tf.service(c)
	path := l.instPath() + "/tcp-proxy"
	switch sub {
	case "status", "show":
		if len(rest) > 0 {
			die("usage: aven tcp-proxy [status] [--json]")
		}
		raw := rawJSON(c, "GET", path, nil)
		var v tcpProxyStatus
		json.Unmarshal(raw, &v)
		printJSONOr(*jsonOut, raw, formatTCPProxy(l.Service, l.Environment, &v))
	case "enable", "on":
		if len(rest) > 0 {
			die("usage: aven tcp-proxy enable [--port n]")
		}
		cp := *port
		if cp == 0 {
			var n networkingOverview
			if err := c.call("GET", l.instPath()+"/networking", nil, &n); err != nil {
				die("%v", err)
			}
			if cp = n.EffectivePort; cp == 0 {
				die("could not tell the service port; pass --port n")
			}
		}
		if cp < 1 || cp > 65535 {
			die("--port must be 1..65535")
		}
		if err := c.call("POST", path, map[string]int{"container_port": cp}, nil); err != nil {
			die("%v", err)
		}
		showTCPProxy(c, l, path, *jsonOut)
	case "port":
		if len(rest) != 1 {
			die("usage: aven tcp-proxy port <n>")
		}
		cp, err := strconv.Atoi(rest[0])
		if err != nil || cp < 1 || cp > 65535 {
			die("port must be 1..65535")
		}
		if err := c.call("PATCH", path, map[string]int{"container_port": cp}, nil); err != nil {
			die("%v", err)
		}
		showTCPProxy(c, l, path, *jsonOut)
	case "disable", "off", "rm":
		if len(rest) > 0 {
			die("usage: aven tcp-proxy disable [-y]")
		}
		confirm(*yes, fmt.Sprintf("disable the TCP proxy of %s (%s)? its public port is released", l.Service, l.Environment))
		if err := c.call("DELETE", path, nil, nil); err != nil {
			die("%v", err)
		}
		fmt.Printf("disabled the TCP proxy of %s (%s)\n", l.Service, l.Environment)
	default:
		die("usage: aven tcp-proxy [status|enable [--port n]|disable|port <n>]")
	}
}

func showTCPProxy(c *config, l *link, path string, jsonOut bool) {
	raw := rawJSON(c, "GET", path, nil)
	var v tcpProxyStatus
	json.Unmarshal(raw, &v)
	printJSONOr(jsonOut, raw, formatTCPProxy(l.Service, l.Environment, &v))
}

func formatTCPProxy(svc, env string, v *tcpProxyStatus) string {
	var b strings.Builder
	switch {
	case v.Enabled && v.PublicPort != nil && v.ContainerPort != nil:
		host := v.Host
		if host == "" {
			host = "(no public host)"
		}
		fmt.Fprintf(&b, "TCP proxy of %s (%s): %s:%d → port %d\n", svc, env, host, *v.PublicPort, *v.ContainerPort)
	case v.Available:
		fmt.Fprintf(&b, "TCP proxy of %s (%s): off (enable: aven tcp-proxy enable --port n)\n", svc, env)
	default:
		fmt.Fprintf(&b, "TCP proxy of %s (%s): off\n", svc, env)
	}
	if !v.Available && v.Reason != "" {
		fmt.Fprintf(&b, "  unavailable: %s\n", v.Reason)
	}
	return b.String()
}

func cmdNetworking(c *config, args []string) {
	fl := flag.NewFlagSet("networking", flag.ExitOnError)
	jsonOut := fl.Bool("json", false, "print the response as JSON")
	tf := addTargetFlags(fl)
	fl.Parse(reorderFlags(args))
	if fl.NArg() > 0 {
		die("usage: aven networking [-s service] [-e env] [--json]")
	}
	l := tf.service(c)
	raw := rawJSON(c, "GET", l.instPath()+"/networking", nil)
	var v networkingOverview
	json.Unmarshal(raw, &v)
	printJSONOr(*jsonOut, raw, formatNetworking(l.Service, l.Environment, &v))
}

func ptrStr(s *string) string {
	if s == nil || *s == "" {
		return "-"
	}
	return *s
}

func formatNetworking(svc, env string, v *networkingOverview) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Networking of %s (%s)\n", svc, env)

	b.WriteString("\nPorts\n")
	fmt.Fprintf(&b, "  effective port: %d\n", v.EffectivePort)
	if len(v.DetectedPorts) == 0 {
		b.WriteString("  detected:       -\n")
	} else {
		var ps []string
		for _, p := range v.DetectedPorts {
			ps = append(ps, fmt.Sprintf("%d (%s)", p.Port, p.Source))
		}
		fmt.Fprintf(&b, "  detected:       %s\n", strings.Join(ps, ", "))
	}
	if v.PortDetection.Source != "proc" && v.PortDetection.Reason != "" {
		fmt.Fprintf(&b, "  (listening ports not read: %s)\n", v.PortDetection.Reason)
	}
	for _, p := range v.DetectedPorts {
		if p.Source == "loopback" {
			fmt.Fprintf(&b, "  warning: port %d listens on localhost only; the proxy cannot reach it (listen on 0.0.0.0 or ::)\n", p.Port)
		}
	}

	b.WriteString("\nPublic domains\n")
	if len(v.Domains) == 0 {
		b.WriteString("  -\n")
	}
	for i := range v.Domains {
		d := &v.Domains[i]
		meta := []string{d.Kind, d.Status, "port " + domainPortLabel(d)}
		if d.Proxy == "cloudflare" || d.Proxy == "tunnel" {
			meta = append(meta, domainProxyLabel(d.Proxy))
		}
		fmt.Fprintf(&b, "  https://%s  (%s)\n", d.Host, strings.Join(meta, " · "))
	}

	b.WriteString("\nTCP proxy\n")
	t := v.TCPProxy
	switch {
	case t.Enabled && t.PublicPort != nil && t.ContainerPort != nil:
		fmt.Fprintf(&b, "  %s:%d → port %d\n", t.Host, *t.PublicPort, *t.ContainerPort)
	case t.Available:
		b.WriteString("  off\n")
	default:
		fmt.Fprintf(&b, "  unavailable: %s\n", t.Reason)
	}

	b.WriteString("\nPrivate network\n")
	p := v.Private
	fmt.Fprintf(&b, "  name:    %s\n", p.FQDN)
	if len(p.Aliases) > 0 {
		fmt.Fprintf(&b, "  aliases: %s\n", strings.Join(p.Aliases, ", "))
	}
	if ips := append(append([]string{}, p.IPv6...), p.IPv4...); len(ips) > 0 {
		fmt.Fprintf(&b, "  ips:     %s\n", strings.Join(ips, ", "))
	}
	if len(p.MeshPorts) > 0 {
		var ps []string
		for _, n := range p.MeshPorts {
			ps = append(ps, strconv.Itoa(n))
		}
		fmt.Fprintf(&b, "  ports open to other nodes: %s (IPv4 only)\n", strings.Join(ps, ", "))
	}

	b.WriteString("\nOutbound\n")
	o := v.Outbound
	state := "on"
	if !o.IPv6Enabled {
		state = "off"
	}
	fmt.Fprintf(&b, "  outbound IPv6: %s\n", state)
	switch {
	case o.NodeIPv6Egress == nil:
		reason := o.EgressReason
		if reason == "" {
			reason = "unknown"
		}
		fmt.Fprintf(&b, "  node %s IPv6 egress: unknown (%s)\n", o.Node, reason)
	case *o.NodeIPv6Egress:
		fmt.Fprintf(&b, "  node %s IPv6 egress: yes\n", o.Node)
	default:
		reason := ""
		if o.EgressReason != "" {
			reason = " (" + o.EgressReason + ")"
		}
		fmt.Fprintf(&b, "  node %s IPv6 egress: no%s\n", o.Node, reason)
	}
	if o.EnforceSupported != nil && !*o.EnforceSupported && o.EnforceReason != "" {
		fmt.Fprintf(&b, "  per-service blocking unavailable: %s\n", o.EnforceReason)
	}
	fmt.Fprintf(&b, "  static IPs: IPv6 %s, IPv4 %s\n", ptrStr(o.StaticIPs.IPv6), ptrStr(o.StaticIPs.IPv4))
	return b.String()
}
