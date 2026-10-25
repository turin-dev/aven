package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// netLogKind는 logs 인자에 --dns나 --network가 있으면 그 종류를 돌려준다.
func netLogKind(args []string) string {
	for _, a := range args {
		if a == "--" {
			break
		}
		switch strings.TrimLeft(strings.SplitN(a, "=", 2)[0], "-") {
		case "dns":
			if strings.HasPrefix(a, "-") && !strings.HasSuffix(a, "=false") {
				return "dns"
			}
		case "network":
			if strings.HasPrefix(a, "-") && !strings.HasSuffix(a, "=false") {
				return "network"
			}
		}
	}
	return ""
}

// netLogFilters는 종류별 전용 플래그다(값은 @필드:값 조건으로 q에 붙는다).
var netLogFilters = map[string][][3]string{
	"dns": {
		{"domain", "domain", "domain, including its subdomains"},
		{"qname", "qname", "exact name looked up"},
		{"qtype", "qtype", "record type: A, AAAA, CNAME, PTR, TXT, ..."},
		{"rcode", "rcode", "response code: NOERROR, NXDOMAIN, SERVFAIL, TIMEOUT, ..."},
		{"zone", "zone", "internal | external"},
		{"status", "status", "ok | failed"},
	},
	"network": {
		{"protocol", "protocol", "tcp | udp"},
		{"direction", "direction", "ingress | egress"},
		{"peer", "peer", "peer service name or id, or internet / edge_proxy / host / private"},
		{"peer-kind", "peer_kind", "service | internet | edge_proxy | host | private | unknown"},
		{"status", "status", "ok | refused | timeout | reset"},
		{"dropped", "dropped", "true | false (refused or timed out)"},
		{"port", "port", "source or destination port (also >=n, a..b)"},
		{"src", "src", "source IP"},
		{"dst", "dst", "destination IP"},
		{"host", "host", "source or destination IP"},
	},
}

// netLogQuery는 전용 플래그 값과 --filter를 서버 q 문법으로 합친다.
func netLogQuery(kind string, values map[string]string, filter string) string {
	var terms []string
	for _, f := range netLogFilters[kind] {
		v := strings.TrimSpace(values[f[0]])
		if v == "" {
			continue
		}
		if f[0] == "qtype" || f[0] == "rcode" {
			v = strings.ToUpper(v)
		}
		t := "@" + f[1] + ":" + v
		if strings.ContainsAny(t, " \t") {
			t = `"` + strings.ReplaceAll(t, `"`, "") + `"`
		}
		terms = append(terms, t)
	}
	if f := strings.TrimSpace(filter); f != "" {
		terms = append(terms, f)
	}
	return strings.Join(terms, " ")
}

func cmdNetLogs(c *config, kind string, args []string) {
	fl := flag.NewFlagSet("logs", flag.ExitOnError)
	fl.Bool("dns", false, "DNS query logs")
	fl.Bool("network", false, "network flow logs")
	build := fl.Bool("build", false, "")
	fl.BoolVar(build, "b", false, "")
	depID := fl.String("deployment", "", "")
	jsonOut := fl.Bool("json", false, "newline-delimited JSON output")
	n := fl.Int("n", 200, "number of records (disables streaming)")
	fl.IntVar(n, "lines", 200, "number of records (disables streaming)")
	filter := fl.String("filter", "", `filter: words, "phrase", -exclude, @field:value`)
	fl.StringVar(filter, "f", "", "same as --filter")
	since := fl.String("since", "", "records since 30s/5m/2h/1d/1w or RFC3339 (disables streaming)")
	fl.StringVar(since, "S", "", "same as --since")
	until := fl.String("until", "", "records until 30s/5m/2h/1d/1w ago or RFC3339 (disables streaming)")
	fl.StringVar(until, "U", "", "same as --until")
	values := map[string]*string{}
	for k, list := range netLogFilters {
		for _, f := range list {
			if values[f[0]] == nil {
				values[f[0]] = new(string)
				usage := f[2]
				if k == "network" && f[0] == "status" {
					usage = "ok | failed (DNS) or ok | refused | timeout | reset (network)"
				}
				fl.StringVar(values[f[0]], f[0], "", usage)
			}
		}
	}
	tf := addTargetFlags(fl)
	fl.Parse(args)
	if *build || *depID != "" {
		die("--build and --deployment cannot be combined with --dns or --network (these logs are per service, not per deployment)")
	}
	allowed := map[string]bool{}
	for _, f := range netLogFilters[kind] {
		allowed[f[0]] = true
	}
	streaming := true
	vals := map[string]string{}
	var bad []string
	fl.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "n", "lines", "since", "S", "until", "U":
			streaming = false
		case "dns", "network":
			if f.Name != kind {
				die("--dns and --network cannot be used together")
			}
		}
		if p := values[f.Name]; p != nil {
			if !allowed[f.Name] {
				bad = append(bad, "--"+f.Name)
			}
			vals[f.Name] = *p
		}
	})
	if len(bad) > 0 {
		die("%s cannot be used with --%s", strings.Join(bad, ", "), kind)
	}
	l := tf.service(c)
	q := url.Values{"tail": {strconv.Itoa(*n)}}
	if s := netLogQuery(kind, vals, *filter); s != "" {
		q.Set("q", s)
	}
	if *since != "" {
		q.Set("since", *since)
	}
	if *until != "" {
		q.Set("until", *until)
	}
	if !streaming {
		q.Set("follow", "0")
	}
	path := "/services/" + l.ServiceID + "/instances/" + l.EnvironmentID + "/" + kind + "-logs?" + q.Encode()
	if !*jsonOut {
		if kind == "dns" {
			fmt.Printf("%-8s %-8s %-6s %-9s %-40s %s\n", "TIME", "ZONE", "TYPE", "RCODE", "NAME", "ANSWERS")
		} else {
			fmt.Printf("%-8s %-7s %-5s %-21s %-21s %-18s %-19s %8s %s\n", "TIME", "DIR", "PROTO", "SOURCE", "DESTINATION", "PEER", "TRAFFIC", "LATENCY", "STATUS")
		}
	}
	err := c.sse(path, func(ev, data string) bool {
		switch ev {
		case "capture":
			var st struct {
				Active   int    `json:"active_nodes"`
				Inactive int    `json:"inactive_nodes"`
				Reason   string `json:"reason"`
			}
			if json.Unmarshal([]byte(data), &st) == nil && st.Inactive > 0 && st.Reason != "" {
				fmt.Fprintf(os.Stderr, "aven: network log capture is off on %d node(s): %s\n", st.Inactive, st.Reason)
			}
		case "log":
			if *jsonOut {
				fmt.Println(data)
			} else if line, ok := formatNetLog(kind, data); ok {
				fmt.Println(line)
			}
		case "done":
			return false
		}
		return true
	})
	if err != nil {
		die("%v", err)
	}
}

type netLogRow struct {
	T             string   `json:"t"`
	QName         string   `json:"qname"`
	QType         string   `json:"qtype"`
	Zone          string   `json:"zone"`
	RCode         string   `json:"rcode"`
	Answers       []string `json:"answers"`
	Direction     string   `json:"direction"`
	Protocol      string   `json:"protocol"`
	SrcAddr       string   `json:"src_addr"`
	SrcPort       int      `json:"src_port"`
	DstAddr       string   `json:"dst_addr"`
	DstPort       int      `json:"dst_port"`
	Peer          string   `json:"peer"`
	BytesSent     int64    `json:"bytes_sent"`
	BytesReceived int64    `json:"bytes_received"`
	LatencyMS     float64  `json:"latency_ms"`
	Status        string   `json:"status"`
	FlowState     string   `json:"flow_state"`
}

func formatNetLog(kind, data string) (string, bool) {
	var r netLogRow
	if json.Unmarshal([]byte(data), &r) != nil {
		return "", false
	}
	t := r.T
	if p, err := time.Parse(time.RFC3339Nano, r.T); err == nil {
		t = p.Local().Format("15:04:05")
	}
	if kind == "dns" {
		answers := strings.Join(r.Answers, ", ")
		if answers == "" {
			answers = "-"
		}
		return fmt.Sprintf("%-8s %-8s %-6s %-9s %-40s %s", t, r.Zone, r.QType, r.RCode, r.QName, answers), true
	}
	latency := "-"
	if r.LatencyMS > 0 {
		latency = strconv.FormatFloat(r.LatencyMS, 'f', 1, 64) + "ms"
	}
	status := r.Status
	if r.FlowState == "active" {
		status += " (active)"
	}
	traffic := "↑" + compactBytes(r.BytesSent) + " ↓" + compactBytes(r.BytesReceived)
	return fmt.Sprintf("%-8s %-7s %-5s %-21s %-21s %-18s %-19s %8s %s", t, r.Direction, r.Protocol,
		hostPort(r.SrcAddr, r.SrcPort), hostPort(r.DstAddr, r.DstPort), r.Peer, traffic, latency, status), true
}

func hostPort(addr string, port int) string {
	if strings.Contains(addr, ":") {
		return "[" + addr + "]:" + strconv.Itoa(port)
	}
	return addr + ":" + strconv.Itoa(port)
}

func compactBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64) + "G"
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + "M"
	case n >= 1<<10:
		return strconv.FormatFloat(float64(n)/(1<<10), 'f', 1, 64) + "K"
	}
	return strconv.FormatInt(n, 10) + "B"
}

// cmdNetDiag: aven netdiag <host> [port] [-r replica]. 서비스의 replica 안에서 이름 해석과 TCP 연결을 시험한다.
func cmdNetDiag(c *config, args []string) {
	fl := flag.NewFlagSet("netdiag", flag.ExitOnError)
	replica := fl.String("r", "", "replica container (default: first)")
	fl.StringVar(replica, "replica", "", "replica container (default: first)")
	jsonOut := fl.Bool("json", false, "print the raw JSON result")
	tf := addTargetFlags(fl)
	var pos []string
	for rest := args; ; {
		fl.Parse(rest)
		if fl.NArg() == 0 {
			break
		}
		pos, rest = append(pos, fl.Arg(0)), fl.Args()[1:]
	}
	if len(pos) == 0 || len(pos) > 2 {
		die("usage: aven netdiag <host> [port] [-r replica]")
	}
	host, port := pos[0], 0
	if h, p, ok := strings.Cut(host, ":"); ok && len(pos) == 1 && !strings.Contains(p, ":") {
		host, pos = h, append(pos, p)
	}
	if len(pos) == 2 {
		p, err := strconv.Atoi(pos[1])
		if err != nil || p < 1 || p > 65535 {
			die("port must be 1..65535")
		}
		port = p
	}
	l := tf.service(c)
	var res struct {
		Replica string `json:"replica"`
		Host    string `json:"host"`
		Port    int    `json:"port"`
		DNS     netDiagCheck
		TCP     netDiagCheck
	}
	var raw json.RawMessage
	if err := c.call("POST", "/services/"+l.ServiceID+"/instances/"+l.EnvironmentID+"/network-diagnostics",
		map[string]any{"host": host, "port": port, "replica": *replica}, &raw); err != nil {
		die("%v", err)
	}
	if *jsonOut {
		fmt.Println(string(raw))
		return
	}
	json.Unmarshal(raw, &res)
	fmt.Printf("replica: %s\n", res.Replica)
	res.DNS.print("DNS " + res.Host)
	if !res.TCP.Skipped {
		res.TCP.print(fmt.Sprintf("TCP %s:%d", res.Host, res.Port))
	}
	if !res.DNS.OK || (!res.TCP.Skipped && !res.TCP.OK) {
		os.Exit(1)
	}
}

type netDiagCheck struct {
	OK        bool    `json:"ok"`
	Available bool    `json:"available"`
	Skipped   bool    `json:"skipped"`
	Duration  float64 `json:"duration_ms"`
	Output    string  `json:"output"`
}

func (d netDiagCheck) print(label string) {
	state := "ok"
	switch {
	case !d.Available:
		state = "unavailable"
	case !d.OK:
		state = "FAILED"
	}
	fmt.Printf("%-28s %-11s %6.0fms\n", label, state, d.Duration)
	for _, line := range strings.Split(strings.TrimSpace(d.Output), "\n") {
		if line != "" {
			fmt.Println("  " + line)
		}
	}
}
