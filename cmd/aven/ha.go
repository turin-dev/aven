package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// aven ha [status]                                     클러스터 구성원, 역할, 상태(전환 전이면 전환 가능 여부와 선택지)
// aven ha convert [--replicas n] [--edge n] [--internal n] [--no-auto-failover] [--skip-backup] [-y]
// aven ha scale --replicas n
// aven ha switchover --to <member>                     주 노드 옮기기(Make Leader)
// aven ha auto-failover on|off
// aven ha revert [-y]                                  단독으로 되돌리기
type haOpts struct {
	sub                      string
	replicas, edge, internal *int
	noAuto, skipBackup, yes  bool
	to, toggle               string
	svc, env                 string
}

type intFlag struct{ p **int }

func (f intFlag) String() string {
	if f.p == nil || *f.p == nil {
		return ""
	}
	return fmt.Sprint(**f.p)
}

func (f intFlag) Set(s string) error {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n < 1 || n > 10 {
		return errors.New("must be a number from 1 to 10")
	}
	*f.p = &n
	return nil
}

const haUsage = "usage: aven ha [status | convert [--replicas n] [--edge n] [--no-auto-failover] [--skip-backup] [-y] | scale --replicas n | switchover --to <member> | auto-failover on|off | revert [-y]]"

func parseHAArgs(args []string) (*haOpts, error) {
	o := &haOpts{sub: "status"}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		o.sub, args = args[0], args[1:]
	}
	if o.sub == "auto-failover" {
		if len(args) == 0 || (args[0] != "on" && args[0] != "off") {
			return nil, errors.New("usage: aven ha auto-failover on|off")
		}
		o.toggle, args = args[0], args[1:]
	}
	fl := flag.NewFlagSet("ha", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	fl.Var(intFlag{&o.replicas}, "replicas", "replica count")
	fl.Var(intFlag{&o.edge}, "edge", "reverse proxy (edge) count")
	fl.Var(intFlag{&o.internal}, "internal", "coordinator (internal) count")
	fl.BoolVar(&o.noAuto, "no-auto-failover", false, "do not promote replicas automatically")
	fl.BoolVar(&o.skipBackup, "skip-backup", false, "skip the volume backup before converting")
	fl.StringVar(&o.to, "to", "", "member service name or id")
	for _, n := range []string{"y", "yes"} {
		fl.BoolVar(&o.yes, n, false, "do not ask")
	}
	for _, n := range []string{"s", "service"} {
		fl.StringVar(&o.svc, n, "", "service name or id")
	}
	for _, n := range []string{"e", "environment"} {
		fl.StringVar(&o.env, n, "", "environment name or id")
	}
	if err := fl.Parse(args); err != nil {
		return nil, err
	}
	if len(fl.Args()) > 0 {
		return nil, errors.New(haUsage)
	}
	switch o.sub {
	case "status", "convert", "revert", "auto-failover":
	case "scale":
		if o.replicas == nil {
			return nil, errors.New("usage: aven ha scale --replicas n")
		}
	case "switchover":
		if o.to == "" {
			return nil, errors.New("usage: aven ha switchover --to <member>")
		}
	default:
		return nil, errors.New(haUsage)
	}
	if o.sub != "convert" && o.sub != "scale" && (o.replicas != nil || o.edge != nil || o.internal != nil) {
		return nil, errors.New("--replicas/--edge/--internal are for convert (and --replicas for scale)")
	}
	return o, nil
}

func (o *haOpts) convertBody() map[string]any {
	b := map[string]any{"auto_failover": !o.noAuto, "skip_backup": o.skipBackup}
	for k, v := range map[string]*int{"replicas": o.replicas, "edge": o.edge, "internal": o.internal} {
		if v != nil {
			b[k] = *v
		}
	}
	return b
}

type haMember struct {
	ServiceID      string  `json:"service_id"`
	Name           string  `json:"name"`
	Role           string  `json:"role"`
	NodeID         string  `json:"node_id"`
	InstanceStatus string  `json:"instance_status"`
	DBRole         string  `json:"db_role"`
	Primary        bool    `json:"primary"`
	Healthy        bool    `json:"healthy"`
	LagBytes       *uint64 `json:"lag_bytes"`
	Error          string  `json:"error"`
}

type haCluster struct {
	Kind         string `json:"kind"`
	Status       string `json:"status"`
	AutoFailover bool   `json:"auto_failover"`
	KeelFailover bool   `json:"keel_failover"`
	Message      string `json:"message"`
	Endpoint     *struct {
		Name        string `json:"name"`
		PrivateHost string `json:"private_host"`
		URLVariable string `json:"url_variable"`
	} `json:"endpoint"`
	Members []haMember `json:"members"`
}

type haStatus struct {
	Cluster   *haCluster `json:"cluster"`
	Eligible  bool       `json:"eligible"`
	Problems  []string   `json:"problems"`
	Companion *struct {
		Name      string `json:"name"`
		Version   string `json:"version"`
		Selectors []struct {
			Role    string `json:"role"`
			Label   string `json:"label"`
			Options []int  `json:"options"`
			Default int    `json:"default"`
		} `json:"selectors"`
	} `json:"companion"`
}

func formatHA(svc, env string, st *haStatus) string {
	var b strings.Builder
	cl := st.Cluster
	if cl == nil {
		fmt.Fprintf(&b, "%s (%s): standalone\n", svc, env)
		if st.Companion != nil {
			fmt.Fprintf(&b, "companion: %s (version %s)\n", st.Companion.Name, st.Companion.Version)
			for _, s := range st.Companion.Selectors {
				flagName := map[string]string{"replica": "replicas", "edge": "edge", "internal": "internal"}[s.Role]
				fmt.Fprintf(&b, "  --%s  %s: %v (default %d)\n", flagName, s.Label, s.Options, s.Default)
			}
		}
		if st.Eligible {
			b.WriteString("ready to convert: aven ha convert\n")
		}
		for _, p := range st.Problems {
			fmt.Fprintf(&b, "  ! %s\n", p)
		}
		return b.String()
	}
	auto := "off"
	if cl.AutoFailover {
		auto = "on"
	}
	if !cl.KeelFailover {
		auto = "by " + map[string]string{"redis": "Sentinel", "mongo": "replica set election"}[cl.Kind]
	}
	fmt.Fprintf(&b, "%s (%s): %s HA cluster, %s, automatic failover %s\n", svc, env, cl.Kind, cl.Status, auto)
	if cl.Endpoint != nil {
		fmt.Fprintf(&b, "endpoint: %s (%s), connect with ${{%s.%s}}\n", cl.Endpoint.Name, cl.Endpoint.PrivateHost, cl.Endpoint.Name, cl.Endpoint.URLVariable)
	}
	if cl.Message != "" {
		fmt.Fprintf(&b, "last event: %s\n", cl.Message)
	}
	tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "MEMBER\tTIER\tROLE\tHEALTH\tLAG\tNODE")
	for _, m := range cl.Members {
		role := m.DBRole
		if role == "" {
			role = "-"
		}
		if m.Primary && role != "primary" {
			role += " (recorded primary)"
		}
		health := "healthy"
		if !m.Healthy {
			health = "unhealthy"
			if m.Error != "" {
				health += ": " + m.Error
			} else if m.InstanceStatus != "" {
				health += " (" + m.InstanceStatus + ")"
			}
		}
		lag := "-"
		if m.LagBytes != nil {
			lag = fmt.Sprintf("%d B", *m.LagBytes)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", m.Name, m.Role, role, health, lag, m.NodeID)
	}
	tw.Flush()
	return b.String()
}

func cmdHA(c *config, args []string) {
	o, err := parseHAArgs(args)
	if err != nil {
		die("%v", err)
	}
	l := (&targetFlags{svc: &o.svc, env: &o.env}).service(c)
	path := l.instPath() + "/ha"
	switch o.sub {
	case "status":
		var st haStatus
		if err := c.call("GET", path, nil, &st); err != nil {
			die("%v", err)
		}
		fmt.Print(formatHA(l.Service, l.Environment, &st))
	case "convert":
		confirm(o.yes, fmt.Sprintf("convert %s (%s) to an HA cluster? Active connections are dropped while it restarts, and services that reference it are switched to the cluster endpoint", l.Service, l.Environment))
		var res struct {
			Cluster  *haCluster `json:"cluster"`
			Migrated []string   `json:"migrated_services"`
		}
		if err := c.call("POST", path, o.convertBody(), &res); err != nil {
			die("%v", err)
		}
		fmt.Printf("converting %s (%s): %d members are deploying\n", l.Service, l.Environment, len(res.Cluster.Members))
		if len(res.Migrated) > 0 {
			fmt.Printf("variable references moved to the cluster endpoint in: %s (redeploy them to use it)\n", strings.Join(res.Migrated, ", "))
		}
		fmt.Println("check progress with: aven ha status")
	case "scale":
		var res struct {
			Cluster *haCluster `json:"cluster"`
		}
		if err := c.call("PATCH", path, map[string]any{"replicas": *o.replicas}, &res); err != nil {
			die("%v", err)
		}
		fmt.Printf("%s (%s): %d replicas\n", l.Service, l.Environment, *o.replicas)
	case "auto-failover":
		if err := c.call("PATCH", path, map[string]any{"auto_failover": o.toggle == "on"}, nil); err != nil {
			die("%v", err)
		}
		fmt.Printf("automatic failover %s for %s (%s)\n", o.toggle, l.Service, l.Environment)
	case "switchover":
		if err := c.call("POST", path+"/switchover", map[string]string{"to": o.to}, nil); err != nil {
			die("%v", err)
		}
		fmt.Printf("moving the primary of %s (%s) to %s; check with: aven ha status\n", l.Service, l.Environment, o.to)
	case "revert":
		confirm(o.yes, fmt.Sprintf("revert %s (%s) to a standalone database? Replica, proxy and coordinator services are deleted (their volumes are kept)", l.Service, l.Environment))
		if err := c.call("DELETE", path, nil, nil); err != nil {
			die("%v", err)
		}
		fmt.Printf("%s (%s) is standalone again (redeploying)\n", l.Service, l.Environment)
	}
}
