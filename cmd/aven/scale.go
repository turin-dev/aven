package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// scaleOpts는 aven scale의 인자다.
//
//	aven scale                       현재 replica 수와 자동 확장 상태
//	aven scale <n>                   replica 수를 재배포 없이 바로 바꾼다
//	aven scale --auto --min 1 --max 5 [--cpu 70] [--memory 80] [--rps 50] [--up-cooldown 60] [--down-cooldown 300]
//	aven scale --off                 자동 확장을 끈다
type scaleOpts struct {
	replicas         *int
	auto, off        bool
	min, max         int
	cpu, memory, rps *float64
	upCool, downCool int
	svc, env         string
}

// floatFlag는 지정됐을 때만 값이 생기는 float 플래그다.
type floatFlag struct{ p **float64 }

func (f floatFlag) String() string {
	if f.p == nil || *f.p == nil {
		return ""
	}
	return strconv.FormatFloat(**f.p, 'f', -1, 64)
}

func (f floatFlag) Set(s string) error {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return errors.New("must be a positive number")
	}
	*f.p = &v
	return nil
}

func parseScaleArgs(args []string) (*scaleOpts, error) {
	o := &scaleOpts{}
	// 숫자 인자는 플래그 앞이나 뒤 어디에 와도 된다(aven scale 3 -s web, aven scale -s web 3).
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		n, err := strconv.Atoi(args[0])
		if err != nil {
			return nil, fmt.Errorf("replicas must be a number: %q", args[0])
		}
		o.replicas = &n
		args = args[1:]
	}
	fl := flag.NewFlagSet("scale", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	fl.BoolVar(&o.auto, "auto", false, "enable autoscaling")
	fl.BoolVar(&o.off, "off", false, "disable autoscaling")
	fl.IntVar(&o.min, "min", 0, "minimum replicas (autoscaling, default 1)")
	fl.IntVar(&o.max, "max", 0, "maximum replicas (autoscaling)")
	fl.Var(floatFlag{&o.cpu}, "cpu", "target CPU percent per replica")
	fl.Var(floatFlag{&o.memory}, "memory", "target memory percent per replica")
	fl.Var(floatFlag{&o.rps}, "rps", "target requests per second per replica")
	fl.IntVar(&o.upCool, "up-cooldown", 0, "seconds between scale-ups (default 60)")
	fl.IntVar(&o.downCool, "down-cooldown", 0, "seconds before scaling down again (default 300)")
	for _, n := range []string{"s", "service"} {
		fl.StringVar(&o.svc, n, "", "service name or id")
	}
	for _, n := range []string{"e", "environment"} {
		fl.StringVar(&o.env, n, "", "environment name or id")
	}
	if err := fl.Parse(args); err != nil {
		return nil, err
	}
	if rest := fl.Args(); len(rest) > 0 {
		if o.replicas != nil || len(rest) > 1 {
			return nil, errors.New("usage: aven scale [<replicas>] [--auto --min n --max n --cpu %] [--off]")
		}
		n, err := strconv.Atoi(rest[0])
		if err != nil {
			return nil, fmt.Errorf("replicas must be a number: %q", rest[0])
		}
		o.replicas = &n
	}
	modes := 0
	for _, b := range []bool{o.replicas != nil, o.auto, o.off} {
		if b {
			modes++
		}
	}
	if modes > 1 {
		return nil, errors.New("use either <replicas>, --auto or --off")
	}
	if o.replicas != nil && (*o.replicas < 1 || *o.replicas > 10) {
		return nil, errors.New("replicas must be 1..10")
	}
	if o.auto {
		if o.min == 0 {
			o.min = 1
		}
		if o.max == 0 {
			return nil, errors.New("--auto needs --max")
		}
		if o.min < 1 || o.max < o.min || o.max > 10 {
			return nil, errors.New("need 1 <= --min <= --max <= 10")
		}
		if o.cpu == nil && o.memory == nil && o.rps == nil {
			return nil, errors.New("--auto needs at least one target: --cpu, --memory or --rps")
		}
	} else if o.min != 0 || o.max != 0 || o.cpu != nil || o.memory != nil || o.rps != nil || o.upCool != 0 || o.downCool != 0 {
		return nil, errors.New("--min/--max/--cpu/--memory/--rps/--*-cooldown need --auto")
	}
	return o, nil
}

// autoscaleBody는 PATCH instance의 autoscale 값이다.
func (o *scaleOpts) autoscaleBody() map[string]any {
	a := map[string]any{"enabled": true, "min_replicas": o.min, "max_replicas": o.max,
		"target_cpu_percent": o.cpu, "target_memory_percent": o.memory, "target_rps_per_replica": o.rps}
	if o.upCool > 0 {
		a["scale_up_cooldown_s"] = o.upCool
	}
	if o.downCool > 0 {
		a["scale_down_cooldown_s"] = o.downCool
	}
	return a
}

type scaleInstance struct {
	Replicas        int `json:"replicas"`
	CurrentReplicas int `json:"current_replicas"`
	Placement       []struct {
		NodeID   string `json:"node_id"`
		Replicas int    `json:"replicas"`
	} `json:"placement"`
	Autoscale *struct {
		Enabled     bool     `json:"enabled"`
		MinReplicas int      `json:"min_replicas"`
		MaxReplicas int      `json:"max_replicas"`
		CPU         *float64 `json:"target_cpu_percent"`
		Memory      *float64 `json:"target_memory_percent"`
		RPS         *float64 `json:"target_rps_per_replica"`
	} `json:"autoscale"`
	AutoscaleStatus *struct {
		Desired    *int   `json:"desired"`
		LastReason string `json:"last_reason"`
		Reason     string `json:"reason"`
	} `json:"autoscale_status"`
}

func cmdScale(c *config, args []string) {
	o, err := parseScaleArgs(args)
	if err != nil {
		die("%v", err)
	}
	l := (&targetFlags{svc: &o.svc, env: &o.env}).service(c)
	switch {
	case o.replicas != nil:
		var res struct {
			From int `json:"from"`
			To   int `json:"to"`
		}
		fmt.Printf("scaling %s (%s) to %d replicas (waiting for healthchecks)...\n", l.Service, l.Environment, *o.replicas)
		if err := c.call("POST", l.instPath()+"/scale", map[string]int{"replicas": *o.replicas}, &res); err != nil {
			die("%v", err)
		}
		fmt.Printf("%s (%s): replicas %d → %d\n", l.Service, l.Environment, res.From, res.To)
	case o.auto:
		var in scaleInstance
		if err := c.call("PATCH", l.instPath(), map[string]any{"autoscale": o.autoscaleBody()}, &in); err != nil {
			die("%v", err)
		}
		fmt.Printf("autoscaling %s (%s) between %d and %d replicas (now %d)\n", l.Service, l.Environment, o.min, o.max, in.CurrentReplicas)
	case o.off:
		var in scaleInstance
		if err := c.call("PATCH", l.instPath(), map[string]any{"autoscale": nil}, &in); err != nil {
			die("%v", err)
		}
		fmt.Printf("autoscaling off for %s (%s); %d replicas keep running (baseline %d applies on the next deploy)\n", l.Service, l.Environment, in.CurrentReplicas, in.Replicas)
	default:
		var in scaleInstance
		if err := c.call("GET", l.instPath(), nil, &in); err != nil {
			die("%v", err)
		}
		fmt.Print(formatScale(l.Service, l.Environment, &in))
	}
}

func formatScale(svc, env string, in *scaleInstance) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s): %d replicas running (configured %d)\n", svc, env, in.CurrentReplicas, in.Replicas)
	if len(in.Placement) > 0 {
		var parts []string
		for _, p := range in.Placement {
			parts = append(parts, fmt.Sprintf("%s×%d", p.NodeID, p.Replicas))
		}
		fmt.Fprintf(&b, "placement: %s\n", strings.Join(parts, ", "))
	}
	if a := in.Autoscale; a != nil && a.Enabled {
		var t []string
		if a.CPU != nil {
			t = append(t, fmt.Sprintf("cpu %g%%", *a.CPU))
		}
		if a.Memory != nil {
			t = append(t, fmt.Sprintf("memory %g%%", *a.Memory))
		}
		if a.RPS != nil {
			t = append(t, fmt.Sprintf("%g rps", *a.RPS))
		}
		fmt.Fprintf(&b, "autoscale: %d..%d replicas, target %s\n", a.MinReplicas, a.MaxReplicas, strings.Join(t, ", "))
		if st := in.AutoscaleStatus; st != nil {
			if st.Desired != nil {
				fmt.Fprintf(&b, "desired: %d\n", *st.Desired)
			}
			if st.Reason != "" {
				fmt.Fprintf(&b, "status: %s\n", st.Reason)
			}
			if st.LastReason != "" {
				fmt.Fprintf(&b, "last scale: %s\n", st.LastReason)
			}
		}
	} else {
		b.WriteString("autoscale: off\n")
	}
	return b.String()
}
