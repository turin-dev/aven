package dokploy

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// iacDomain은 IaC settings.domains 항목이다(store.Domain JSON과 같은 이름).
type iacDomain struct {
	Host                string     `json:"host"`
	Kind                string     `json:"kind"`
	TargetPort          *int       `json:"target_port,omitempty"`
	TLS                 string     `json:"tls,omitempty"`
	TrustForwardedProto bool       `json:"trust_forwarded_proto,omitempty"`
	Paths               []pathRule `json:"paths,omitempty"`
}

type pathRule struct {
	Path        string `json:"path"`
	Service     string `json:"service,omitempty"`
	TargetPort  *int   `json:"target_port,omitempty"`
	StripPrefix bool   `json:"strip_prefix,omitempty"`
}

type hostEntry struct {
	sp *ServicePlan // 앱 서비스(스택이면 nil)
	st *StackPlan
	d  Domain
}

func isRoot(p string) bool { return p == "" || p == "/" }

// domains는 호스트별로 Dokploy 도메인 행을 합친다. 같은 호스트의 다른 경로(`/api` → 다른 서비스)는
// aven 경로 규칙이 되고, https=false(cloudflared 터널 뒤)는 도메인별 tls "off"가 된다.
func (t *translator) domains() {
	owner := map[string]string{} // host → aven 프로젝트(프로젝트 간 충돌 검사)
	for _, pp := range t.plan.Projects {
		hosts := map[string][]hostEntry{}
		for _, sp := range pp.Services {
			for _, e := range sp.pending {
				h := strings.ToLower(strings.TrimSpace(e.d.Host))
				hosts[h] = append(hosts[h], hostEntry{sp: sp, d: e.d})
			}
		}
		for _, sd := range t.pendingStack {
			if sd.pp == pp {
				h := strings.ToLower(strings.TrimSpace(sd.d.Host))
				hosts[h] = append(hosts[h], hostEntry{st: sd.st, d: sd.d})
			}
		}
		for _, h := range sortedKeys(hosts) {
			es := dedupe(hosts[h])
			if prev, ok := owner[h]; ok && prev != pp.Name {
				for _, e := range es {
					blockEntry(e, fmt.Sprintf("domain %s is also used in aven project %s", h, prev))
				}
				continue
			}
			owner[h] = pp.Name
			t.host(h, es)
		}
	}
}

// dedupe는 대소문자만 다른 중복 행(Bot.x / bot.x)을 합친다.
func dedupe(es []hostEntry) []hostEntry {
	seen := map[string]bool{}
	var out []hostEntry
	for _, e := range es {
		port := 0
		if e.d.Port != nil {
			port = *e.d.Port
		}
		k := fmt.Sprintf("%p|%p|%s|%d", e.sp, e.st, strings.TrimRight(e.d.Path, "/"), port)
		if seen[k] {
			warnEntry(e, fmt.Sprintf("duplicate Dokploy domain row for %s merged (it differed only in letter case)", strings.ToLower(e.d.Host)))
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	return out
}

func warnEntry(e hostEntry, msg string) {
	if e.sp != nil {
		e.sp.Warnings = append(e.sp.Warnings, msg)
	} else {
		e.st.Warnings = append(e.st.Warnings, msg)
	}
}

func blockEntry(e hostEntry, msg string) {
	if e.sp != nil {
		if e.sp.Status != Skip {
			e.sp.Status = Blocked
		}
		e.sp.Reasons = append(e.sp.Reasons, msg)
	} else {
		e.st.Status = Blocked
		e.st.Reasons = append(e.st.Reasons, msg)
	}
}

func (t *translator) host(h string, es []hostEntry) {
	for _, e := range es {
		if e.d.InternalPath != "" && e.d.InternalPath != "/" {
			warnEntry(e, fmt.Sprintf("domain %s: internal path %s is not supported (requests keep their path)", h, e.d.InternalPath))
		}
		if e.d.ForwardAuth || hasValue(e.d.Middlewares) {
			warnEntry(e, fmt.Sprintf("domain %s: Traefik middlewares / forward auth are not imported", h))
		}
		if e.d.CertificateType == "custom" {
			warnEntry(e, fmt.Sprintf("domain %s used a custom certificate; aven issues its own", h))
		}
	}
	// 스택 도메인: 스택 서비스는 compose import가 만들므로 경로 규칙과 섞지 않는다.
	var apps []hostEntry
	for _, e := range es {
		if e.st == nil {
			apps = append(apps, e)
			continue
		}
		if len(es) > 1 {
			blockEntry(e, fmt.Sprintf("domain %s is shared by a compose service and other services; split it after import", h))
			continue
		}
		if !isRoot(e.d.Path) {
			warnEntry(e, fmt.Sprintf("domain %s: path %s is dropped (the whole host goes to %s)", h, e.d.Path, e.d.ServiceName))
		}
		sd := StackDomain{Host: h, TLSOff: !e.d.HTTPS}
		if e.d.Port != nil {
			sd.TargetPort = *e.d.Port
		}
		if e.st.Domains == nil {
			e.st.Domains = map[string][]StackDomain{}
		}
		e.st.Domains[e.d.ServiceName] = append(e.st.Domains[e.d.ServiceName], sd)
		if sd.TLSOff {
			e.st.Warnings = append(e.st.Warnings, fmt.Sprintf("domain %s was plain HTTP in Dokploy (tunnel?): set tls off on it after import", h))
		}
	}
	if len(apps) == 0 {
		return
	}
	// 기본 서비스: 경로가 "/"인 행. 없으면 첫 행.
	var root *hostEntry
	for i := range apps {
		if isRoot(apps[i].d.Path) {
			if root == nil {
				root = &apps[i]
			} else if apps[i].sp != root.sp {
				blockEntry(apps[i], fmt.Sprintf("domain %s is already served by %s", h, root.sp.Name))
			}
		}
	}
	if root == nil {
		root = &apps[0]
		warnEntry(*root, fmt.Sprintf("domain %s has no / route in Dokploy; unmatched paths now go to %s", h, root.sp.Name))
	}
	d := iacDomain{Host: h, Kind: "custom", TargetPort: root.d.Port}
	if !root.d.HTTPS {
		d.TLS, d.TrustForwardedProto = "off", true
		root.sp.Warnings = append(root.sp.Warnings, fmt.Sprintf("domain %s was plain HTTP in Dokploy (behind a cloudflared tunnel): imported with tls off", h))
	}
	var rules []pathRule
	for i := range apps {
		e := apps[i]
		if isRoot(e.d.Path) {
			continue
		}
		p := "/" + strings.Trim(e.d.Path, "/") + "/*" // Traefik PathPrefix
		r := pathRule{Path: p, StripPrefix: e.d.StripPath}
		if e.sp != root.sp {
			r.Service = e.sp.Name
			root.sp.DependsOn = append(root.sp.DependsOn, e.sp.Name)
			if e.sp.Node != root.sp.Node {
				blockEntry(e, fmt.Sprintf("path %s%s goes to %s on another node than %s; aven path rules need both on one node", h, e.d.Path, e.sp.Name, root.sp.Name))
			}
			e.sp.Domains = append(e.sp.Domains, h+e.d.Path+" (path rule on "+root.sp.Name+")")
		}
		if e.d.Port != nil && (root.d.Port == nil || *e.d.Port != *root.d.Port || r.Service != "") {
			port := *e.d.Port
			r.TargetPort = &port
		}
		rules = append(rules, r)
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Path < rules[j].Path })
	d.Paths = rules
	root.sp.Domains = append(root.sp.Domains, h)
	var list []iacDomain
	if old, ok := root.sp.Service.Settings["domains"]; ok {
		json.Unmarshal(old, &list)
	}
	list = append(list, d)
	setSetting(root.sp, "domains", list)
}

func hasValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case []any:
		return len(x) > 0
	case string:
		return x != ""
	}
	return true
}
