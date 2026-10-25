package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

type traceSetting struct {
	InstanceID string     `json:"instance_id"`
	ServiceID  string     `json:"service_id"`
	Service    string     `json:"service_name"`
	Enabled    bool       `json:"enabled"`
	HasDomain  bool       `json:"has_public_domain"`
	LastEdgeAt *time.Time `json:"last_edge_span_at"`
	LastAppAt  *time.Time `json:"last_service_span_at"`
}

type traceSummary struct {
	TraceID     string    `json:"trace_id"`
	StartedAt   time.Time `json:"started_at"`
	DurationMS  float64   `json:"duration_ms"`
	RootName    string    `json:"root_name"`
	ServiceName string    `json:"service_name"`
	SpanCount   int       `json:"span_count"`
	ErrorCount  int       `json:"error_count"`
}

type traceSpan struct {
	TraceID      string    `json:"trace_id"`
	SpanID       string    `json:"span_id"`
	ParentSpanID string    `json:"parent_span_id"`
	ServiceName  string    `json:"service_name"`
	Component    string    `json:"component"`
	Name         string    `json:"name"`
	Kind         string    `json:"kind"`
	Status       string    `json:"status"`
	StartTime    time.Time `json:"start_time"`
	DurationMS   float64   `json:"duration_ms"`
}

type traceOpts struct {
	sub                  string
	pos                  []string
	svc, env, project    string
	jsonOut, all, errors bool
	filter, since, until string
	limit, maxSpans      int
}

// traceValueFlags는 값을 받는 플래그다(위치 인자 뒤에 와도 읽게 앞으로 옮긴다).
var traceValueFlags = map[string]bool{"s": true, "service": true, "e": true, "environment": true, "p": true, "project": true,
	"f": true, "filter": true, "S": true, "since": true, "U": true, "until": true, "n": true, "limit": true, "max-spans": true}

func parseTraceArgs(args []string) (*traceOpts, error) {
	o := &traceOpts{sub: "status"}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		o.sub, args = args[0], args[1:]
	}
	switch o.sub {
	case "on", "enable":
		o.sub = "on"
	case "off", "disable":
		o.sub = "off"
	case "ls":
		o.sub = "list"
	case "show":
		o.sub = "get"
	case "status", "list", "get":
	default:
		return nil, fmt.Errorf("unknown subcommand %q (on|off|status|list|get)", o.sub)
	}
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && traceValueFlags[strings.TrimLeft(a, "-")] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		pos = append(pos, a)
	}
	fl := flag.NewFlagSet("trace "+o.sub, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	str := func(p *string, long, short, def, usage string) {
		fl.StringVar(p, long, def, usage)
		if short != "" {
			fl.StringVar(p, short, def, usage)
		}
	}
	str(&o.svc, "service", "s", "", "service name or id (default: linked)")
	str(&o.env, "environment", "e", "", "environment name or id (default: linked)")
	str(&o.project, "project", "p", "", "project id (requires --environment)")
	str(&o.filter, "filter", "f", "", "filter expression over spans")
	str(&o.since, "since", "S", "1h", "start of the window: 30m, 2h, 1d or RFC 3339")
	str(&o.until, "until", "U", "", "end of the window (default: now)")
	fl.IntVar(&o.limit, "limit", 100, "maximum traces (1-500)")
	fl.IntVar(&o.limit, "n", 100, "maximum traces (1-500)")
	fl.IntVar(&o.maxSpans, "max-spans", 1000, "maximum spans (1-2000)")
	fl.BoolVar(&o.jsonOut, "json", false, "JSON output")
	fl.BoolVar(&o.all, "all", false, "every service in the environment")
	fl.BoolVar(&o.all, "a", false, "every service in the environment")
	fl.BoolVar(&o.errors, "errors", false, "only traces with an error span")
	if err := fl.Parse(flags); err != nil {
		return nil, err
	}
	o.pos = append(pos, fl.Args()...)
	if o.all && o.svc != "" {
		return nil, fmt.Errorf("--all conflicts with --service")
	}
	if o.project != "" && o.env == "" {
		return nil, fmt.Errorf("--project requires --environment")
	}
	if o.sub == "get" && len(o.pos) != 1 {
		return nil, fmt.Errorf("usage: aven trace get <TRACE_ID>")
	}
	if o.limit < 1 || o.limit > 500 {
		return nil, fmt.Errorf("--limit must be between 1 and 500")
	}
	if o.maxSpans < 1 || o.maxSpans > 2000 {
		return nil, fmt.Errorf("--max-spans must be between 1 and 2000")
	}
	return o, nil
}

// traceTarget는 -p/-e/-s 또는 연결 정보로 환경(필수)과 서비스(선택)를 정한다.
func (o *traceOpts) target(c *config) *link {
	if o.project == "" {
		return (&targetFlags{svc: &o.svc, env: &o.env}).resolve(c)
	}
	p := c.project(o.project)
	l := &link{ProjectID: p.ID, Project: p.Name}
	e := findEnv(p, o.env)
	if e == nil {
		die("environment %q not found (%s)", o.env, names(p.Environments, func(e envRef) string { return e.Name }))
	}
	l.EnvironmentID, l.Environment = e.ID, e.Name
	if o.svc != "" {
		s := findSvc(p, o.svc)
		if s == nil {
			die("service %q not found (%s)", o.svc, names(p.Services, func(s svcRef) string { return s.Name }))
		}
		l.ServiceID, l.Service = s.ID, s.Name
	}
	return l
}

func cmdTrace(c *config, args []string) {
	o, err := parseTraceArgs(args)
	if err != nil {
		die("%v", err)
	}
	l := o.target(c)
	envPath := "/environments/" + url.PathEscape(l.EnvironmentID)
	needService := func() {
		if l.ServiceID == "" {
			die("no service linked (pass -s <service> or --all)")
		}
	}
	switch o.sub {
	case "on", "off":
		enabled := o.sub == "on"
		var settings []traceSetting
		if o.all {
			if err := c.call("PUT", envPath+"/tracing", map[string]any{"enabled": enabled}, &settings); err != nil {
				die("%v", err)
			}
		} else {
			needService()
			var st traceSetting
			if err := c.call("PUT", l.instPath()+"/tracing", map[string]any{"enabled": enabled}, &st); err != nil {
				die("%v", err)
			}
			settings = []traceSetting{st}
		}
		if o.jsonOut {
			printTraceStatusJSON(os.Stdout, l, settings)
			return
		}
		verb := "enabled"
		if !enabled {
			verb = "disabled"
		}
		for _, st := range settings {
			fmt.Printf("tracing %s for %s (%s)\n", verb, st.Service, l.Environment)
		}
		if enabled {
			fmt.Println("the edge traces requests to public domains now; OTEL_* variables are added on the next deploy (aven redeploy)")
		}
	case "status":
		var settings []traceSetting
		if o.all || l.ServiceID == "" {
			if err := c.call("GET", envPath+"/tracing", nil, &settings); err != nil {
				die("%v", err)
			}
		} else {
			var st traceSetting
			if err := c.call("GET", l.instPath()+"/tracing", nil, &st); err != nil {
				die("%v", err)
			}
			settings = []traceSetting{st}
		}
		if o.jsonOut {
			printTraceStatusJSON(os.Stdout, l, settings)
			return
		}
		printTraceStatus(os.Stdout, settings)
	case "list":
		q := url.Values{"since": {o.since}, "limit": {fmt.Sprint(o.limit)}}
		if o.until != "" {
			q.Set("until", o.until)
		}
		if o.filter != "" {
			q.Set("filter", o.filter)
		}
		if o.errors {
			q.Set("errors", "true")
		}
		if !o.all {
			needService()
			q.Set("service", l.ServiceID)
		}
		var raw []json.RawMessage
		if err := c.call("GET", envPath+"/traces?"+q.Encode(), nil, &raw); err != nil {
			die("%v", err)
		}
		if o.jsonOut {
			printNDJSON(os.Stdout, raw)
			return
		}
		items := make([]traceSummary, len(raw))
		for i, r := range raw {
			json.Unmarshal(r, &items[i])
		}
		printTraceList(os.Stdout, items)
	case "get":
		id := o.pos[0]
		path := envPath + "/traces/" + url.PathEscape(id) + "?max_spans=" + fmt.Sprint(o.maxSpans)
		if o.jsonOut {
			var raw []json.RawMessage
			if err := c.call("GET", path, nil, &raw); err != nil {
				die("%v", err)
			}
			printNDJSON(os.Stdout, raw)
			return
		}
		var spans []traceSpan
		if err := c.call("GET", path, nil, &spans); err != nil {
			die("%v", err)
		}
		printTraceTree(os.Stdout, spans)
	}
}

func printNDJSON(w io.Writer, items []json.RawMessage) {
	for _, it := range items {
		fmt.Fprintln(w, string(it))
	}
}

func printTraceStatusJSON(w io.Writer, l *link, settings []traceSetting) {
	b, _ := json.MarshalIndent(map[string]any{
		"environment": map[string]string{"id": l.EnvironmentID, "name": l.Environment},
		"services":    settings,
	}, "", "  ")
	fmt.Fprintln(w, string(b))
}

func sinceText(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return ago(*t) // cmds.go와 같은 표기
}

func printTraceStatus(w io.Writer, settings []traceSetting) {
	if len(settings) == 0 {
		fmt.Fprintln(w, "no services in this environment")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SERVICE\tTRACED\tLAST EDGE SPAN\tLAST APP SPAN\t")
	for _, st := range settings {
		traced := "off"
		if st.Enabled {
			traced = "on"
		}
		edge := sinceText(st.LastEdgeAt)
		if !st.HasDomain {
			edge = "no public domain"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t\n", st.Service, traced, edge, sinceText(st.LastAppAt))
	}
	tw.Flush()
}

func fmtTraceMS(ms float64) string {
	switch {
	case ms >= 1000:
		return fmt.Sprintf("%.2fs", ms/1000)
	case ms >= 10:
		return fmt.Sprintf("%.0fms", ms)
	default:
		return fmt.Sprintf("%.2fms", ms)
	}
}

func printTraceList(w io.Writer, items []traceSummary) {
	if len(items) == 0 {
		fmt.Fprintln(w, "no traces in this time range")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "STARTED\tDURATION\tSPANS\tERRORS\tROOT\tSERVICE\tTRACE ID\t")
	for _, t := range items {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\t%s\t%s\t\n", t.StartedAt.Local().Format("2006-01-02 15:04:05"),
			fmtTraceMS(t.DurationMS), t.SpanCount, t.ErrorCount, t.RootName, t.ServiceName, t.TraceID)
	}
	tw.Flush()
}

// printTraceTree는 span을 부모 아래로 들여 쓴 트리로 보여 준다. 부모가 trace에 없는 span은 맨 위에 둔다.
func printTraceTree(w io.Writer, spans []traceSpan) {
	ids := map[string]bool{}
	for _, s := range spans {
		ids[s.SpanID] = true
	}
	children := map[string][]traceSpan{}
	var roots []traceSpan
	for _, s := range spans {
		if s.ParentSpanID == "" || !ids[s.ParentSpanID] {
			roots = append(roots, s)
		} else {
			children[s.ParentSpanID] = append(children[s.ParentSpanID], s)
		}
	}
	byStart := func(xs []traceSpan) {
		sort.SliceStable(xs, func(i, j int) bool { return xs[i].StartTime.Before(xs[j].StartTime) })
	}
	byStart(roots)
	if len(spans) > 0 {
		fmt.Fprintf(w, "trace %s · %d spans\n", spans[0].TraceID, len(spans))
	}
	var walk func(s traceSpan, prefix string, last, top bool)
	walk = func(s traceSpan, prefix string, last, top bool) {
		branch, next := "", prefix
		if !top {
			if last {
				branch, next = "└─ ", prefix+"   "
			} else {
				branch, next = "├─ ", prefix+"│  "
			}
		}
		who := s.Component
		if s.ServiceName != "" {
			who += " · " + s.ServiceName
		}
		status := ""
		if s.Status == "error" {
			status = "  ERROR"
		}
		fmt.Fprintf(w, "%s%s%s  [%s]  %s%s\n", prefix, branch, s.Name, who, fmtTraceMS(s.DurationMS), status)
		kids := children[s.SpanID]
		byStart(kids)
		for i, k := range kids {
			walk(k, next, i == len(kids)-1, false)
		}
	}
	for _, r := range roots {
		walk(r, "", true, true)
	}
}
