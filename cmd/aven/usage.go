package main

import (
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
)

type usageTotals struct {
	CPUCoreHours  float64 `json:"cpu_core_hours"`
	MemoryGBHours float64 `json:"memory_gb_hours"`
	EgressGB      float64 `json:"net_egress_gb"`
	DiskGBHours   float64 `json:"disk_gb_hours"`
	Cost          *struct {
		Total float64 `json:"total"`
	} `json:"estimated_cost"`
}

type usageGroup struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	usageTotals
}

type usageResp struct {
	From     string       `json:"from"`
	To       string       `json:"to"`
	Group    string       `json:"group"`
	Totals   usageTotals  `json:"totals"`
	Groups   []usageGroup `json:"groups"`
	Projects []usageGroup `json:"projects"`
}

// findLink는 linked()와 같지만 링크가 없으면 nil이다.
func (c *config) findLink() *link {
	dir, _ := os.Getwd()
	for d := dir; ; d = filepath.Dir(d) {
		if l, ok := c.Links[d]; ok {
			return l
		}
		if d == filepath.Dir(d) {
			return nil
		}
	}
}

// cmdUsage: aven usage [--from D] [--to D] [--group service|environment|day] [--all]
func cmdUsage(c *config, args []string) {
	fl := flag.NewFlagSet("usage", flag.ExitOnError)
	from := fl.String("from", "", "start (YYYY-MM-DD or RFC3339, UTC; default: first day of this month)")
	to := fl.String("to", "", "end (YYYY-MM-DD = end of that day, or RFC3339; default: now)")
	group := fl.String("group", "service", "service | environment | day (linked project only)")
	all := fl.Bool("all", false, "workspace usage per project (workspace admin)")
	fl.Parse(args)
	q := url.Values{}
	if *from != "" {
		q.Set("from", *from)
	}
	if *to != "" {
		q.Set("to", *to)
	}
	l := c.findLink()
	path := "/usage"
	if !*all && l != nil {
		q.Set("group", *group)
		path = "/projects/" + url.PathEscape(l.ProjectID) + "/usage"
	} else if !*all {
		die("this directory is not linked (run: aven link, or use --all for the whole workspace)")
	}
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var u usageResp
	if err := c.call("GET", path, nil, &u); err != nil {
		die("%v", err)
	}
	printUsage(os.Stdout, &u)
}

func printUsage(w io.Writer, u *usageResp) {
	rows := u.Groups
	label := u.Group
	if u.Projects != nil {
		rows, label = u.Projects, "project"
	}
	fmt.Fprintf(w, "usage %s → %s\n\n", u.From, u.To)
	hasCost := u.Totals.Cost != nil
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', tabwriter.AlignRight)
	head := "%s\tCPU (core·h)\tMEMORY (GB·h)\tEGRESS (GB)\tDISK (GB·h)\t"
	if hasCost {
		head += "EST. COST\t"
	}
	fmt.Fprintf(tw, head+"\n", strings.ToUpper(label))
	line := func(name string, t usageTotals) {
		fmt.Fprintf(tw, "%s\t%.2f\t%.2f\t%.3f\t%.2f\t", name, t.CPUCoreHours, t.MemoryGBHours, t.EgressGB, t.DiskGBHours)
		if hasCost {
			c := 0.0
			if t.Cost != nil {
				c = t.Cost.Total
			}
			fmt.Fprintf(tw, "%.2f\t", c)
		}
		fmt.Fprintln(tw)
	}
	for _, g := range rows {
		name := g.Name
		if name == "" {
			name = g.Key
		}
		line(name, g.usageTotals)
	}
	line("TOTAL", u.Totals)
	tw.Flush()
}
