package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// aven domain: 공개 도메인 추가·조회·변경. API: POST/PATCH/DELETE …/domains[/{host}], POST …/domains/{host}/verify (docs/api-v1.md).
//
//	aven domain [host] [--port n]       도메인 추가(호스트가 없으면 생성 도메인). 예전과 같다.
//	aven domain list                    이 서비스의 도메인
//	aven domain status <host>           검증, 프록시, DNS 레코드(상태·현재 값), 인증서, 경고
//	aven domain port <host> <n|auto>    대상 포트 변경(auto: 서비스 포트를 따른다)
//	aven domain rm <host> [-y]          도메인 삭제
//	aven domain verify <host>           DNS·검증 상태를 지금 다시 확인
//	aven domain rename <old> <new>      생성 도메인 이름 변경(접미사는 그대로, <new>는 첫 이름만 써도 된다)

type domainRecordView struct {
	Type        string   `json:"type"`
	Name        string   `json:"name"`
	Value       string   `json:"value"`
	Status      string   `json:"status"`
	Current     []string `json:"current"`
	Alternative bool     `json:"alternative"`
	Note        string   `json:"note"`
}

type domainDetail struct {
	Host                string               `json:"host"`
	Kind                string               `json:"kind"`
	TargetPort          *int                 `json:"target_port"`
	EffectivePort       int                  `json:"effective_port"`
	Status              string               `json:"status"`
	Proxy               string               `json:"proxy"`
	IPv6                string               `json:"ipv6"`
	TLS                 string               `json:"tls"`
	TrustForwardedProto bool                 `json:"trust_forwarded_proto"`
	Upstream            string               `json:"upstream"`
	Redirect            *struct{ To string } `json:"redirect"`
	DNS                 []domainRecordView   `json:"dns"`
	Verification        *struct {
		Method     string     `json:"method"`
		VerifiedAt *time.Time `json:"verified_at"`
		TXT        struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"txt"`
	} `json:"verification"`
	Cert *struct {
		Status   string     `json:"status"`
		NotAfter *time.Time `json:"not_after"`
	} `json:"cert"`
	CheckedAt *time.Time `json:"checked_at"`
	LastError string     `json:"last_error"`
	Warnings  []string   `json:"warnings"`
}

// domainSubcommands는 `aven domain <sub>`로 읽는 하위 명령이다. 호스트 이름에는 점이 있으므로 겹치지 않는다.
var domainSubcommands = map[string]bool{"list": true, "ls": true, "status": true, "show": true, "port": true,
	"rm": true, "remove": true, "delete": true, "verify": true, "rename": true, "add": true}

func cmdDomain(c *config, args []string) {
	fl := flag.NewFlagSet("domain", flag.ExitOnError)
	port := fl.Int("port", 0, "target container port (default: the service port)")
	fl.IntVar(port, "p", 0, "target container port")
	yes := fl.Bool("y", false, "don't ask for confirmation")
	fl.BoolVar(yes, "yes", false, "don't ask for confirmation")
	jsonOut := fl.Bool("json", false, "print the response as JSON")
	tf := addTargetFlags(fl)
	fl.Parse(reorderFlags(args))
	pos := fl.Args()
	if len(pos) > 0 && domainSubcommands[pos[0]] {
		sub, rest := pos[0], pos[1:]
		l := tf.service(c)
		switch sub {
		case "add":
			domainAdd(c, l, rest, *port)
		case "list", "ls":
			domainList(c, l, rest, *jsonOut)
		case "status", "show":
			d := domainFind(c, l, oneHost("status", rest))
			printJSONOr(*jsonOut, d, formatDomainStatus(d))
		case "port":
			domainPort(c, l, rest, *jsonOut)
		case "rm", "remove", "delete":
			host := oneHost("rm", rest)
			confirm(*yes, fmt.Sprintf("remove %s from %s (%s)?", host, l.Service, l.Environment))
			if err := c.call("DELETE", domainPath(l, host), nil, nil); err != nil {
				die("%v", err)
			}
			fmt.Println("removed", host)
		case "verify":
			var d domainDetail
			if err := c.call("POST", domainPath(l, oneHost("verify", rest))+"/verify", map[string]any{}, &d); err != nil {
				die("%v", err)
			}
			printJSONOr(*jsonOut, d, formatDomainStatus(&d))
		case "rename":
			if len(rest) != 2 {
				die("usage: aven domain rename <old-host> <new-host | new-label>")
			}
			old := strings.ToLower(rest[0])
			var d domainDetail
			if err := c.call("PATCH", domainPath(l, old), map[string]any{"host": renameHost(old, rest[1])}, &d); err != nil {
				die("%v", err)
			}
			fmt.Printf("renamed %s → https://%s\n", old, d.Host)
		}
		return
	}
	if len(pos) > 1 {
		die("usage: aven domain [host] [--port n] | aven domain list|status|port|rm|verify|rename (see aven help)")
	}
	domainAdd(c, tf.service(c), pos, *port)
}

func domainPath(l *link, host string) string {
	return l.instPath() + "/domains/" + url.PathEscape(strings.ToLower(host))
}

func oneHost(sub string, rest []string) string {
	if len(rest) != 1 {
		die("usage: aven domain %s <host>", sub)
	}
	return strings.ToLower(rest[0])
}

// domainAdd는 예전 `aven domain [host] [--port n]`이다.
func domainAdd(c *config, l *link, pos []string, port int) {
	if len(pos) > 1 {
		die("usage: aven domain [host] [--port n]")
	}
	body := map[string]any{}
	if len(pos) == 1 {
		body["host"] = strings.ToLower(pos[0])
	}
	if port != 0 {
		if port < 1 || port > 65535 {
			die("--port must be 1..65535")
		}
		body["target_port"] = port
	}
	var d domainDetail
	if err := c.call("POST", l.instPath()+"/domains", body, &d); err != nil {
		die("%v", err)
	}
	fmt.Printf("https://%s (%s, %s)\n", d.Host, d.Kind, d.Status)
	if len(d.DNS) > 0 {
		fmt.Println("\nset these DNS records:")
		for _, r := range d.DNS {
			fmt.Printf("  %-6s %-40s %s\n", r.Type, r.Name, r.Value)
		}
	}
}

// domainList는 인스턴스 응답의 domains다.
func domainList(c *config, l *link, rest []string, jsonOut bool) {
	if len(rest) > 0 {
		die("usage: aven domain list [--json]")
	}
	var in struct {
		Domains []domainDetail `json:"domains"`
	}
	if err := c.call("GET", l.instPath(), nil, &in); err != nil {
		die("%v", err)
	}
	printJSONOr(jsonOut, in.Domains, formatDomainList(l.Service, l.Environment, in.Domains))
}

func domainFind(c *config, l *link, host string) *domainDetail {
	var in struct {
		Domains []domainDetail `json:"domains"`
	}
	if err := c.call("GET", l.instPath(), nil, &in); err != nil {
		die("%v", err)
	}
	for i := range in.Domains {
		if in.Domains[i].Host == host {
			return &in.Domains[i]
		}
	}
	die("%s is not a domain of %s (%s); see: aven domain list", host, l.Service, l.Environment)
	return nil
}

func domainPort(c *config, l *link, rest []string, jsonOut bool) {
	if len(rest) != 2 {
		die("usage: aven domain port <host> <port | auto>")
	}
	tp, err := parseTargetPort(rest[1])
	if err != nil {
		die("%v", err)
	}
	var d domainDetail
	if err := c.call("PATCH", domainPath(l, rest[0]), map[string]any{"target_port": tp}, &d); err != nil {
		die("%v", err)
	}
	if jsonOut {
		printJSON(d)
		return
	}
	fmt.Printf("%s → port %s\n", d.Host, domainPortLabel(&d))
}

// parseTargetPort는 포트 번호 또는 auto/default(→ null, 서비스 포트를 따른다)다.
func parseTargetPort(s string) (any, error) {
	switch strings.ToLower(s) {
	case "auto", "default", "none":
		return nil, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return nil, fmt.Errorf("port must be 1..65535 or auto")
	}
	return n, nil
}

// renameHost는 rename의 새 이름이다. 점이 없으면 첫 이름(label)만 바꾸고 이전 호스트의 접미사를 붙인다.
func renameHost(old, next string) string {
	next = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(next), "."))
	if strings.Contains(next, ".") {
		return next
	}
	if _, suffix, ok := strings.Cut(old, "."); ok {
		return next + "." + suffix
	}
	return next
}

func domainPortLabel(d *domainDetail) string {
	switch {
	case d.TargetPort != nil:
		return strconv.Itoa(*d.TargetPort)
	case d.EffectivePort > 0:
		return strconv.Itoa(d.EffectivePort) + " (service port)"
	default:
		return "service port"
	}
}

// domainProxyLabel은 마지막 확인에서 본 앞단이다.
func domainProxyLabel(p string) string {
	switch p {
	case "cloudflare":
		return "Cloudflare proxy detected"
	case "tunnel":
		return "Cloudflare tunnel"
	case "direct":
		return "direct"
	case "none":
		return "no DNS records"
	case "":
		return "not checked yet"
	}
	return p
}

func formatDomainList(svc, env string, ds []domainDetail) string {
	var b strings.Builder
	if len(ds) == 0 {
		fmt.Fprintf(&b, "%s (%s) has no domains (add one: aven domain [host])\n", svc, env)
		return b.String()
	}
	fmt.Fprintf(&b, "%-44s %-10s %-12s %-18s %s\n", "HOST", "KIND", "STATUS", "PORT", "PROXY")
	for i := range ds {
		d := &ds[i]
		proxy := d.Proxy
		if proxy == "" {
			proxy = "-"
		}
		kind := d.Kind
		if d.Upstream != "" {
			kind += "*"
		}
		fmt.Fprintf(&b, "%-44s %-10s %-12s %-18s %s\n", d.Host, kind, d.Status, domainPortLabel(d), proxy)
	}
	return b.String()
}

func formatDomainStatus(d *domainDetail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "https://%s\n", d.Host)
	fmt.Fprintf(&b, "  kind:          %s\n", d.Kind)
	fmt.Fprintf(&b, "  status:        %s\n", d.Status)
	fmt.Fprintf(&b, "  port:          %s\n", domainPortLabel(d))
	if d.Upstream != "" {
		fmt.Fprintf(&b, "  upstream:      %s (external)\n", d.Upstream)
	}
	if d.Redirect != nil && d.Redirect.To != "" {
		fmt.Fprintf(&b, "  redirect:      %s\n", d.Redirect.To)
	}
	if d.TLS != "" {
		fmt.Fprintf(&b, "  tls:           %s\n", d.TLS)
	}
	fmt.Fprintf(&b, "  proxy:         %s\n", domainProxyLabel(d.Proxy))
	if d.IPv6 != "" {
		fmt.Fprintf(&b, "  ipv6:          %s (dedicated)\n", d.IPv6)
	}
	if v := d.Verification; v != nil {
		switch {
		case v.Method != "" && v.VerifiedAt != nil:
			fmt.Fprintf(&b, "  verification:  %s (verified %s)\n", v.Method, when(v.VerifiedAt))
		case v.Method != "":
			fmt.Fprintf(&b, "  verification:  %s\n", v.Method)
		default:
			fmt.Fprintf(&b, "  verification:  not verified (DNS pointing here, or TXT %s = %s)\n", v.TXT.Name, v.TXT.Value)
		}
	}
	if d.Cert != nil {
		exp := ""
		if d.Cert.NotAfter != nil {
			exp = ", expires " + d.Cert.NotAfter.Local().Format("2006-01-02")
		}
		fmt.Fprintf(&b, "  certificate:   %s%s\n", d.Cert.Status, exp)
	} else {
		fmt.Fprintf(&b, "  certificate:   unknown\n")
	}
	if d.CheckedAt != nil {
		fmt.Fprintf(&b, "  checked:       %s\n", when(d.CheckedAt))
	}
	if d.LastError != "" {
		fmt.Fprintf(&b, "  last error:    %s\n", d.LastError)
	}
	if len(d.DNS) > 0 {
		b.WriteString("\nDNS records:\n")
		fmt.Fprintf(&b, "  %-6s %-36s %-34s %-9s %s\n", "TYPE", "NAME", "VALUE", "STATUS", "CURRENT")
		for _, r := range d.DNS {
			st := r.Status
			if st == "" {
				st = "-"
			}
			cur := strings.Join(r.Current, ", ")
			if cur == "" {
				cur = "-"
			}
			typ := r.Type
			if r.Alternative {
				typ += "*"
			}
			fmt.Fprintf(&b, "  %-6s %-36s %-34s %-9s %s\n", typ, r.Name, r.Value, st, cur)
			if r.Note != "" {
				fmt.Fprintf(&b, "         note: %s\n", r.Note)
			}
		}
		for _, r := range d.DNS {
			if r.Alternative {
				b.WriteString("  * alternative to the CNAME record above\n")
				break
			}
		}
	}
	for _, w := range d.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}
	return b.String()
}

// rawJSON은 --json 출력용으로 서버 응답을 그대로 읽는다.
func rawJSON(c *config, method, path string, body any) json.RawMessage {
	var raw json.RawMessage
	if err := c.call(method, path, body, &raw); err != nil {
		die("%v", err)
	}
	return raw
}
