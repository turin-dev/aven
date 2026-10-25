package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// API: GET/PATCH .../edge, PUT .../edge/rules, POST .../edge/cdn/purge, POST/DELETE .../edge/waf/under-attack (docs/api-v1.md).

type edgeView struct {
	CDN struct {
		Enabled           bool       `json:"enabled"`
		HTMLCaching       string     `json:"html_caching"`
		DefaultTTLSeconds int        `json:"default_ttl_seconds"`
		HonorSWR          bool       `json:"honor_swr"`
		PurgeOnDeploy     string     `json:"purge_on_deploy"`
		LastPurgedHTML    *time.Time `json:"last_purged_html"`
		LastPurgedAll     *time.Time `json:"last_purged_all"`
	} `json:"cdn"`
	WAF struct {
		AllowIPs  []string `json:"allow_ips"`
		DenyIPs   []string `json:"deny_ips"`
		RateLimit *struct {
			Requests      int `json:"requests"`
			WindowSeconds int `json:"window_seconds"`
		} `json:"rate_limit"`
		ManagedRules bool `json:"managed_rules"`
		UnderAttack  *struct {
			Since time.Time  `json:"since"`
			Until *time.Time `json:"until"`
		} `json:"under_attack"`
	} `json:"waf"`
	Rules             json.RawMessage `json:"rules"`
	Available         bool            `json:"available"`
	Domains           []string        `json:"domains"`
	UnderAttackActive bool            `json:"under_attack_active"`
}

var ttlNames = map[string]int{"30m": 1800, "1h": 3600, "2h": 7200, "4h": 14400, "12h": 43200, "1d": 86400, "24h": 86400}

// parseTTL은 --default-ttl 값(30m, 1h, 2h, 4h, 12h, 1d 또는 그 초)을 초로 바꾼다.
func parseTTL(s string) (int, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if n, ok := ttlNames[s]; ok {
		return n, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		for _, v := range ttlNames {
			if v == n {
				return n, nil
			}
		}
	}
	return 0, fmt.Errorf("--default-ttl must be 30m, 1h, 2h, 4h, 12h, 1d or matching seconds")
}

func ttlLabel(sec int) string {
	for _, k := range []string{"30m", "1h", "2h", "4h", "12h", "1d"} {
		if ttlNames[k] == sec {
			return k
		}
	}
	return strconv.Itoa(sec) + "s"
}

// parseRateLimit은 "100/60s", "100/1m" 또는 "off"를 읽는다.
func parseRateLimit(s string) (map[string]int, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "off" || s == "none" || s == "0" {
		return nil, nil
	}
	n, w, ok := strings.Cut(s, "/")
	req, err := strconv.Atoi(n)
	if !ok || err != nil || req < 1 {
		return nil, errors.New("--rate-limit must look like 100/60s (requests per window) or off")
	}
	d, err := time.ParseDuration(w)
	if err != nil {
		if sec, err2 := strconv.Atoi(w); err2 == nil {
			d = time.Duration(sec) * time.Second
		} else {
			return nil, errors.New("--rate-limit window must be a duration like 60s or 1m")
		}
	}
	if d < time.Second || d > time.Hour || d%time.Second != 0 {
		return nil, errors.New("--rate-limit window must be 1s..1h")
	}
	return map[string]int{"requests": req, "window_seconds": int(d / time.Second)}, nil
}

type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }
func (l *listFlag) Set(s string) error {
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*l = append(*l, p)
		}
	}
	return nil
}

func when(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

func formatCDN(svc, env string, v *edgeView) string {
	var b strings.Builder
	state := "disabled"
	if v.CDN.Enabled {
		state = "enabled"
	}
	fmt.Fprintf(&b, "%s (%s): CDN caching %s\n", svc, env, state)
	if !v.Available {
		b.WriteString("available: no (add a public domain first: aven domain)\n")
	}
	fmt.Fprintf(&b, "html caching: %s\ndefault ttl: %s\nhonor stale-while-revalidate: %v\npurge on deploy: %s\n",
		v.CDN.HTMLCaching, ttlLabel(v.CDN.DefaultTTLSeconds), v.CDN.HonorSWR, v.CDN.PurgeOnDeploy)
	fmt.Fprintf(&b, "last purged: html %s, all %s\n", when(v.CDN.LastPurgedHTML), when(v.CDN.LastPurgedAll))
	return b.String()
}

func formatUnderAttack(svc, env string, v *edgeView, now time.Time) string {
	ua := v.WAF.UnderAttack
	switch {
	case !v.UnderAttackActive || ua == nil:
		return fmt.Sprintf("%s (%s): Under Attack Mode off\n", svc, env)
	case ua.Until == nil:
		return fmt.Sprintf("%s (%s): Under Attack Mode ON until disabled (since %s)\n", svc, env, ua.Since.Local().Format("2006-01-02 15:04"))
	}
	left := ua.Until.Sub(now).Round(time.Minute)
	return fmt.Sprintf("%s (%s): Under Attack Mode ON, %s left (until %s)\n", svc, env, left, ua.Until.Local().Format("2006-01-02 15:04"))
}

func formatWAF(svc, env string, v *edgeView, now time.Time) string {
	var b strings.Builder
	b.WriteString(formatUnderAttack(svc, env, v, now))
	list := func(l []string) string {
		if len(l) == 0 {
			return "-"
		}
		return strings.Join(l, ", ")
	}
	fmt.Fprintf(&b, "allow ips: %s\ndeny ips: %s\n", list(v.WAF.AllowIPs), list(v.WAF.DenyIPs))
	if rl := v.WAF.RateLimit; rl != nil {
		fmt.Fprintf(&b, "rate limit: %d requests / %ds per client IP\n", rl.Requests, rl.WindowSeconds)
	} else {
		b.WriteString("rate limit: off\n")
	}
	fmt.Fprintf(&b, "managed rules: %v\n", v.WAF.ManagedRules)
	return b.String()
}

func edgeFlags(name string) (*flag.FlagSet, *targetFlags, *bool) {
	fl := flag.NewFlagSet(name, flag.ExitOnError)
	tf := addTargetFlags(fl)
	asJSON := fl.Bool("json", false, "output JSON")
	return fl, tf, asJSON
}

func cmdCDN(c *config, args []string) {
	sub := "status"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	fl, tf, asJSON := edgeFlags("cdn " + sub)
	html := fl.String("html-caching", "", "auto | force | never")
	ttl := fl.String("default-ttl", "", "30m, 1h, 2h, 4h, 12h, 1d (or seconds)")
	swr := fl.Bool("swr", false, "honor stale-while-revalidate")
	noSWR := fl.Bool("no-swr", false, "ignore stale-while-revalidate")
	pod := fl.String("purge-on-deploy", "", "off | html | all")
	fl.Parse(reorderFlags(args))
	l := tf.service(c)
	var v edgeView
	var err error
	switch sub {
	case "status":
		err = c.call("GET", l.instPath()+"/edge", nil, &v)
	case "enable", "disable":
		err = c.call("PATCH", l.instPath()+"/edge", map[string]any{"cdn": map[string]any{"enabled": sub == "enable"}}, &v)
	case "update":
		cdn := map[string]any{}
		if *html != "" {
			cdn["html_caching"] = *html
		}
		if *ttl != "" {
			n, perr := parseTTL(*ttl)
			if perr != nil {
				die("%v", perr)
			}
			cdn["default_ttl_seconds"] = n
		}
		if *swr && *noSWR {
			die("use either --swr or --no-swr")
		}
		if *swr || *noSWR {
			cdn["honor_swr"] = *swr
		}
		if *pod != "" {
			cdn["purge_on_deploy"] = *pod
		}
		if len(cdn) == 0 {
			die("usage: aven cdn update [--html-caching auto|force|never] [--default-ttl 2h] [--swr|--no-swr] [--purge-on-deploy off|html|all]")
		}
		err = c.call("PATCH", l.instPath()+"/edge", map[string]any{"cdn": cdn}, &v)
	case "purge":
		if fl.NArg() != 1 || (fl.Arg(0) != "html" && fl.Arg(0) != "all") {
			die("usage: aven cdn purge html|all")
		}
		err = c.call("POST", l.instPath()+"/edge/cdn/purge", map[string]string{"scope": fl.Arg(0)}, &v)
		if err == nil && !*asJSON {
			fmt.Printf("purged %s cached content of %s (%s); edge nodes drop it within seconds\n", fl.Arg(0), l.Service, l.Environment)
			return
		}
	default:
		die("usage: aven cdn [status|enable|disable|update|purge html|all] [-s service] [-e env] [--json]")
	}
	if err != nil {
		die("%v", err)
	}
	if *asJSON {
		printJSON(v)
		return
	}
	fmt.Print(formatCDN(l.Service, l.Environment, &v))
}

func cmdWAF(c *config, args []string) {
	sub := "status"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	if sub == "under-attack" {
		cmdUnderAttack(c, args)
		return
	}
	fl, tf, asJSON := edgeFlags("waf " + sub)
	var allow, deny listFlag
	fl.Var(&allow, "allow-ip", "IP or CIDR that skips WAF checks (repeatable, comma separated); replaces the list")
	fl.Var(&deny, "deny-ip", "IP or CIDR to block (repeatable, comma separated); replaces the list")
	clearAllow := fl.Bool("clear-allow", false, "empty the allow list")
	clearDeny := fl.Bool("clear-deny", false, "empty the deny list")
	rate := fl.String("rate-limit", "", "requests per window per client IP, e.g. 100/60s, or off")
	managed := fl.String("managed-rules", "", "on | off")
	fl.Parse(reorderFlags(args))
	l := tf.service(c)
	var v edgeView
	var err error
	switch sub {
	case "status":
		err = c.call("GET", l.instPath()+"/edge", nil, &v)
	case "update":
		waf := map[string]any{}
		if *clearAllow {
			waf["allow_ips"] = []string{}
		} else if len(allow) > 0 {
			waf["allow_ips"] = allow
		}
		if *clearDeny {
			waf["deny_ips"] = []string{}
		} else if len(deny) > 0 {
			waf["deny_ips"] = deny
		}
		if *rate != "" {
			rl, perr := parseRateLimit(*rate)
			if perr != nil {
				die("%v", perr)
			}
			waf["rate_limit"] = rl
		}
		switch *managed {
		case "":
		case "on", "true":
			waf["managed_rules"] = true
		case "off", "false":
			waf["managed_rules"] = false
		default:
			die("--managed-rules must be on or off")
		}
		if len(waf) == 0 {
			die("usage: aven waf update [--allow-ip ip]... [--deny-ip ip]... [--clear-allow] [--clear-deny] [--rate-limit 100/60s|off] [--managed-rules on|off]")
		}
		err = c.call("PATCH", l.instPath()+"/edge", map[string]any{"waf": waf}, &v)
	default:
		die("usage: aven waf [status|update|under-attack status|enable|disable] [-s service] [-e env] [--json]")
	}
	if err != nil {
		die("%v", err)
	}
	if *asJSON {
		printJSON(v)
		return
	}
	fmt.Print(formatWAF(l.Service, l.Environment, &v, time.Now()))
}

func cmdUnderAttack(c *config, args []string) {
	sub := "status"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	fl, tf, asJSON := edgeFlags("waf under-attack " + sub)
	duration := fl.String("duration", "", "1h, 3h, 12h or 24h (default: until disabled)")
	fl.Parse(reorderFlags(args))
	l := tf.service(c)
	var v edgeView
	var err error
	switch sub {
	case "status":
		err = c.call("GET", l.instPath()+"/edge", nil, &v)
	case "enable":
		err = c.call("POST", l.instPath()+"/edge/waf/under-attack", map[string]string{"duration": *duration}, &v)
	case "disable":
		err = c.call("DELETE", l.instPath()+"/edge/waf/under-attack", nil, &v)
	default:
		die("usage: aven waf under-attack status|enable [--duration 1h|3h|12h|24h]|disable [-s service] [-e env] [--json]")
	}
	if err != nil {
		die("%v", err)
	}
	if *asJSON {
		printJSON(v)
		return
	}
	fmt.Print(formatUnderAttack(l.Service, l.Environment, &v, time.Now()))
}

// cmdEdge는 엣지 규칙(JSON)을 읽고 쓴다: aven edge rules [get] / aven edge rules set <file.json|->.
func cmdEdge(c *config, args []string) {
	if len(args) == 0 || args[0] != "rules" {
		die("usage: aven edge rules [get] | aven edge rules set <file.json|-> [-s service] [-e env]")
	}
	args = args[1:]
	sub := "get"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	fl, tf, _ := edgeFlags("edge rules " + sub)
	fl.Parse(reorderFlags(args))
	l := tf.service(c)
	var v edgeView
	switch sub {
	case "get":
		if err := c.call("GET", l.instPath()+"/edge", nil, &v); err != nil {
			die("%v", err)
		}
	case "set":
		if fl.NArg() != 1 {
			die("usage: aven edge rules set <file.json|->")
		}
		var raw []byte
		var err error
		if fl.Arg(0) == "-" {
			raw, err = io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		} else {
			raw, err = os.ReadFile(fl.Arg(0))
		}
		if err != nil {
			die("%v", err)
		}
		var rs json.RawMessage
		if err := json.Unmarshal(raw, &rs); err != nil {
			die("%s: invalid JSON: %v", fl.Arg(0), err)
		}
		if err := c.call("PUT", l.instPath()+"/edge/rules", rs, &v); err != nil {
			die("%v", err)
		}
		fmt.Fprintf(os.Stderr, "saved edge rules for %s (%s)\n", l.Service, l.Environment)
	default:
		die("usage: aven edge rules [get] | aven edge rules set <file.json|->")
	}
	var pretty any
	json.Unmarshal(v.Rules, &pretty)
	printJSON(pretty)
}

func init() {
	// reorderFlags가 이 플래그의 값도 함께 옮기게 한다(aven cdn update --html-caching force --default-ttl 4h).
	for _, k := range []string{"html-caching", "default-ttl", "purge-on-deploy", "allow-ip", "deny-ip", "rate-limit", "managed-rules", "duration"} {
		valueFlag[k] = true
	}
}
