package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

//
//	aven pitr [status]
//	aven pitr enable [--retention-days 7] [--destination <id>]
//	aven pitr disable [-y]
//	aven pitr base                                   기준 백업을 지금 만든다
//	aven pitr restore --at <time> [--new-service-name n | --in-place] [-y] [--no-wait]
//	aven pitr delete-archive [-y]                    (꺼진 뒤) 보관한 WAL/binlog와 기준 백업을 지운다

type pitrConfig struct {
	Enabled       bool       `json:"enabled"`
	RetentionDays int        `json:"retention_days"`
	DestinationID string     `json:"destination_id"`
	EnabledAt     *time.Time `json:"enabled_at"`
	LastBaseAt    *time.Time `json:"last_base_at"`
	LastSyncAt    *time.Time `json:"last_sync_at"`
	LastError     string     `json:"last_error"`
}

type pitrRestore struct {
	Status          string     `json:"status"`
	Step            string     `json:"step"`
	Error           string     `json:"error"`
	TargetTime      time.Time  `json:"target_time"`
	TargetServiceID string     `json:"target_service_id"`
	InPlace         bool       `json:"in_place"`
	StartedAt       time.Time  `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at"`
}

type pitrStatus struct {
	Supported bool         `json:"supported"`
	Reason    string       `json:"reason"`
	Kind      string       `json:"kind"`
	Config    pitrConfig   `json:"config"`
	Running   []string     `json:"running"`
	Restore   *pitrRestore `json:"restore"`
	Window    struct {
		Earliest *time.Time `json:"earliest"`
		Latest   *time.Time `json:"latest"`
		Bases    int        `json:"bases"`
		Logs     int        `json:"logs"`
		Bytes    int64      `json:"bytes"`
	} `json:"window"`
}

var relDurRe = regexp.MustCompile(`^(\d+)(s|m|h|d|w)$`)

// parseAt는 --at 값이다: RFC3339, 로컬 "YYYY-MM-DD HH:MM[:SS]", 또는 지금부터 거슬러 30m/2h/1d/1w.
func parseAt(v string, now time.Time) (time.Time, error) {
	v = strings.TrimSpace(v)
	if m := relDurRe.FindStringSubmatch(v); m != nil {
		n, _ := strconv.Atoi(m[1])
		unit := map[string]time.Duration{"s": time.Second, "m": time.Minute, "h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour}[m[2]]
		return now.Add(-time.Duration(n) * unit).Truncate(time.Second), nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("--at %q: use RFC3339 (2026-07-20T12:00:00Z), local \"YYYY-MM-DD HH:MM[:SS]\" or a duration ago (30m, 2h, 1d, 1w)", v)
}

func fmtWhen(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05 MST")
}

func formatPITR(svc, env string, s *pitrStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s): point-in-time recovery ", svc, env)
	if !s.Supported {
		fmt.Fprintf(&b, "not available: %s\n", s.Reason)
		return b.String()
	}
	if s.Config.Enabled {
		dest := s.Config.DestinationID
		if dest == "" {
			dest = "local"
		}
		fmt.Fprintf(&b, "on (%s, retention %d days, storage %s)\n", s.Kind, s.Config.RetentionDays, dest)
	} else {
		b.WriteString("off\n")
	}
	if s.Window.Earliest != nil {
		fmt.Fprintf(&b, "restore window: %s → %s\n", fmtWhen(s.Window.Earliest), fmtWhen(s.Window.Latest))
	} else {
		b.WriteString("restore window: none yet (waiting for the first base backup and archived logs)\n")
	}
	fmt.Fprintf(&b, "archive: %d base backups, %d log files, %s\n", s.Window.Bases, s.Window.Logs, humanBytes(s.Window.Bytes))
	if s.Config.Enabled {
		fmt.Fprintf(&b, "last base backup: %s\nlast sync: %s\n", fmtWhen(s.Config.LastBaseAt), fmtWhen(s.Config.LastSyncAt))
	}
	if len(s.Running) > 0 {
		fmt.Fprintf(&b, "running: %s\n", strings.Join(s.Running, ", "))
	}
	if s.Config.LastError != "" {
		fmt.Fprintf(&b, "error: %s\n", s.Config.LastError)
	}
	if r := s.Restore; r != nil {
		fmt.Fprintf(&b, "last restore: %s to %s (%s)", r.Status, r.TargetTime.Local().Format("2006-01-02 15:04:05 MST"), map[bool]string{true: "in place", false: "new service"}[r.InPlace])
		if r.Error != "" {
			fmt.Fprintf(&b, ": %s", r.Error)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

type pitrOpts struct {
	sub                  string
	yes, inPlace, noWait bool
	at, newName, dest    string
	retention            int
	destSet              bool
	tf                   *targetFlags
}

func parsePITRArgs(args []string) (*pitrOpts, error) {
	o := &pitrOpts{sub: "status"}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		o.sub, args = args[0], args[1:]
	}
	switch o.sub {
	case "status", "enable", "disable", "base", "restore", "delete-archive":
	default:
		return nil, fmt.Errorf("unknown pitr command %q (status, enable, disable, base, restore, delete-archive)", o.sub)
	}
	fl := flag.NewFlagSet("pitr "+o.sub, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	o.tf = addTargetFlags(fl)
	fl.BoolVar(&o.yes, "y", false, "skip the confirmation prompt")
	fl.BoolVar(&o.yes, "yes", false, "skip the confirmation prompt")
	fl.StringVar(&o.at, "at", "", "restore target")
	fl.StringVar(&o.newName, "new-service-name", "", "name of the restored service")
	fl.BoolVar(&o.inPlace, "in-place", false, "overwrite this service instead of creating a new one")
	fl.BoolVar(&o.noWait, "no-wait", false, "do not wait for the restore to finish")
	fl.IntVar(&o.retention, "retention-days", 0, "archive retention in days (1..90)")
	fl.Func("destination", "backup destination id (empty = local)", func(s string) error { o.dest, o.destSet = s, true; return nil })
	if err := fl.Parse(args); err != nil {
		return nil, err
	}
	if fl.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fl.Arg(0))
	}
	if o.sub == "restore" {
		if o.at == "" {
			return nil, errors.New("restore needs --at <time>")
		}
		if o.inPlace && o.newName != "" {
			return nil, errors.New("use either --in-place or --new-service-name")
		}
	} else if o.at != "" || o.newName != "" || o.inPlace || o.noWait {
		return nil, errors.New("--at/--new-service-name/--in-place/--no-wait are for pitr restore")
	}
	if o.sub != "enable" && (o.retention != 0 || o.destSet) {
		return nil, errors.New("--retention-days/--destination are for pitr enable")
	}
	return o, nil
}

func cmdPITR(c *config, args []string) {
	o, err := parsePITRArgs(args)
	if err != nil {
		die("%v\nusage: aven pitr [status|enable|disable|base|restore|delete-archive] (see aven help)", err)
	}
	l := o.tf.service(c)
	p := l.instPath() + "/pitr"
	switch o.sub {
	case "status":
		var s pitrStatus
		if err := c.call("GET", p, nil, &s); err != nil {
			die("%v", err)
		}
		fmt.Print(formatPITR(l.Service, l.Environment, &s))
	case "enable":
		body := map[string]any{"enabled": true}
		if o.retention != 0 {
			body["retention_days"] = o.retention
		}
		if o.destSet {
			body["destination_id"] = o.dest
		}
		var res struct {
			Config  pitrConfig `json:"config"`
			Warning string     `json:"warning"`
		}
		if err := c.call("PUT", p, body, &res); err != nil {
			die("%v", err)
		}
		fmt.Printf("point-in-time recovery on for %s (%s): retention %d days; the first base backup starts within a minute\n", l.Service, l.Environment, res.Config.RetentionDays)
		if res.Warning != "" {
			fmt.Fprintf(os.Stderr, "warning: %s\n", res.Warning)
		}
	case "disable":
		confirm(o.yes, fmt.Sprintf("Disable point-in-time recovery for %s (%s)? The archive is kept until you run aven pitr delete-archive.", l.Service, l.Environment))
		if err := c.call("PUT", p, map[string]any{"enabled": false}, nil); err != nil {
			die("%v", err)
		}
		fmt.Printf("point-in-time recovery off for %s (%s)\n", l.Service, l.Environment)
	case "base":
		if err := c.call("POST", p+"/base", nil, nil); err != nil {
			die("%v", err)
		}
		fmt.Printf("base backup of %s (%s) started\n", l.Service, l.Environment)
	case "delete-archive":
		confirm(o.yes, fmt.Sprintf("Delete every archived base backup and log of %s (%s)?", l.Service, l.Environment))
		var res struct {
			Deleted int `json:"deleted"`
		}
		if err := c.call("DELETE", p+"/archive", nil, &res); err != nil {
			die("%v", err)
		}
		fmt.Printf("deleted %d archive files\n", res.Deleted)
	case "restore":
		at, err := parseAt(o.at, time.Now())
		if err != nil {
			die("%v", err)
		}
		where := "a new service"
		if o.newName != "" {
			where = "a new service " + o.newName
		}
		if o.inPlace {
			where = l.Service + " itself (its current data is overwritten)"
		}
		confirm(o.yes, fmt.Sprintf("Restore %s (%s) to %s into %s?", l.Service, l.Environment, at.UTC().Format(time.RFC3339), where))
		body := map[string]any{"target_time": at.UTC().Format(time.RFC3339), "in_place": o.inPlace}
		if o.newName != "" {
			body["new_service_name"] = o.newName
		}
		var r pitrRestore
		if err := c.call("POST", p+"/restore", body, &r); err != nil {
			die("%v", err)
		}
		fmt.Printf("restore to %s started\n", r.TargetTime.UTC().Format(time.RFC3339))
		if o.noWait {
			return
		}
		step := ""
		for r.Status == "running" {
			time.Sleep(3 * time.Second)
			if err := c.call("GET", p+"/restore", nil, &r); err != nil {
				die("%v", err)
			}
			if r.Step != step && r.Status == "running" {
				step = r.Step
				fmt.Printf("  %s…\n", step)
			}
		}
		if r.Status == "failed" {
			die("restore failed: %s", r.Error)
		}
		fmt.Println("restore finished")
	}
}
