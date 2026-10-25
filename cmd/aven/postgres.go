package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// aven postgres: PostgreSQL 연결 풀링(PgBouncer)과 메이저 버전 업그레이드.
//
//	aven postgres pooling [status]
//	aven postgres pooling enable [--pool-mode transaction] [--default-pool-size n] [--max-client-conn n] [--max-prepared-statements n]
//	aven postgres pooling configure [같은 플래그]
//	aven postgres pooling disable [-y]
//	aven postgres upgrade [status]
//	aven postgres upgrade check [--to 18]
//	aven postgres upgrade start [--to 18] [-y]
//	aven postgres upgrade revert [-y]
//
// 바꾸는 명령은 작업이 끝날 때까지 기다린다(--detach면 바로 돌아온다).
type pgOpts struct {
	area, action         string // pooling|upgrade, status|enable|...
	poolMode             string
	poolSize, maxClients int
	maxPrepared          int // -1: 지정 안 함
	resetPrepared        bool
	to                   int
	yes, detach, jsonOut bool
	svc, env             string
}

var pgActions = map[string]map[string]string{
	"pooling": {"": "status", "status": "status", "enable": "enable", "add": "enable", "configure": "configure", "config": "configure",
		"disable": "disable", "remove": "disable"},
	"upgrade": {"": "status", "status": "status", "check": "check", "preflight": "check", "start": "start", "run": "start", "revert": "revert"},
}

const pgUsage = "usage: aven postgres pooling [status|enable|configure|disable] | aven postgres upgrade [status|check|start|revert] (see aven help)"

func parsePostgresArgs(args []string) (*pgOpts, error) {
	o := &pgOpts{maxPrepared: -1}
	if len(args) == 0 {
		return nil, errors.New(pgUsage)
	}
	o.area = map[string]string{"pooling": "pooling", "pgbouncer": "pooling", "pool": "pooling", "upgrade": "upgrade"}[args[0]]
	if o.area == "" {
		return nil, errors.New(pgUsage)
	}
	args = args[1:]
	verb := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		verb, args = args[0], args[1:]
	}
	act, ok := pgActions[o.area][verb]
	if !ok {
		return nil, fmt.Errorf("unknown %s command %q\n%s", o.area, verb, pgUsage)
	}
	o.action = act
	fl := flag.NewFlagSet("postgres", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	fl.StringVar(&o.poolMode, "pool-mode", "", "transaction | session | statement")
	fl.IntVar(&o.poolSize, "default-pool-size", 0, "server connections per user/database pair")
	fl.IntVar(&o.maxClients, "max-client-conn", 0, "maximum client connections")
	fl.IntVar(&o.maxPrepared, "max-prepared-statements", -1, "prepared statements per connection (0 disables)")
	fl.BoolVar(&o.resetPrepared, "default-prepared-statements", false, "reset max prepared statements to the PgBouncer default")
	fl.IntVar(&o.to, "to", 0, "target PostgreSQL major version")
	for _, n := range []string{"y", "yes"} {
		fl.BoolVar(&o.yes, n, false, "skip the confirmation prompt")
	}
	fl.BoolVar(&o.detach, "detach", false, "return without waiting for the operation to finish")
	fl.BoolVar(&o.jsonOut, "json", false, "print JSON")
	for _, n := range []string{"s", "service"} {
		fl.StringVar(&o.svc, n, "", "service name or id")
	}
	for _, n := range []string{"e", "environment"} {
		fl.StringVar(&o.env, n, "", "environment name or id")
	}
	if err := fl.Parse(args); err != nil {
		return nil, err
	}
	if fl.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q\n%s", fl.Arg(0), pgUsage)
	}
	poolFlags := o.poolMode != "" || o.poolSize != 0 || o.maxClients != 0 || o.maxPrepared >= 0 || o.resetPrepared
	if poolFlags && o.action != "enable" && o.action != "configure" {
		return nil, errors.New("--pool-mode/--default-pool-size/--max-client-conn/--max-prepared-statements need pooling enable or configure")
	}
	if o.poolMode != "" && o.poolMode != "transaction" && o.poolMode != "session" && o.poolMode != "statement" {
		return nil, errors.New("--pool-mode must be transaction, session or statement")
	}
	if o.poolSize < 0 || o.maxClients < 0 || o.maxPrepared < -1 {
		return nil, errors.New("pool sizes must be positive")
	}
	if o.action == "configure" && !poolFlags {
		return nil, errors.New("configure needs at least one of --pool-mode, --default-pool-size, --max-client-conn, --max-prepared-statements")
	}
	if o.to != 0 && o.action != "check" && o.action != "start" {
		return nil, errors.New("--to needs upgrade check or start")
	}
	return o, nil
}

func (o *pgOpts) poolingBody() map[string]any {
	b := map[string]any{"enabled": true}
	if o.poolMode != "" {
		b["pool_mode"] = o.poolMode
	} else if o.action == "enable" {
		b["pool_mode"] = "transaction"
	}
	if o.poolSize > 0 {
		b["default_pool_size"] = o.poolSize
	}
	if o.maxClients > 0 {
		b["max_client_conn"] = o.maxClients
	}
	if o.maxPrepared >= 0 {
		b["max_prepared_statements"] = o.maxPrepared
	}
	if o.resetPrepared {
		b["reset_max_prepared_statements"] = true
	}
	return b
}

type pgPooling struct {
	Status                string   `json:"status"`
	PoolMode              string   `json:"pool_mode"`
	DefaultPoolSize       int      `json:"default_pool_size"`
	MaxClientConn         int      `json:"max_client_conn"`
	MaxPreparedStatements *int     `json:"max_prepared_statements"`
	PoolerServiceName     string   `json:"pooler_service_name"`
	Error                 string   `json:"error"`
	Variables             []string `json:"variables"`
}

type pgCheck struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Severity string `json:"severity"`
	Passed   bool   `json:"passed"`
	Message  string `json:"message"`
}

type pgPreflight struct {
	Ready            bool      `json:"ready"`
	CurrentMajor     int       `json:"current_major"`
	TargetMajor      int       `json:"target_major"`
	AvailableTargets []int     `json:"available_targets"`
	Checks           []pgCheck `json:"checks"`
}

type pgUpgrade struct {
	Status         string     `json:"status"`
	CurrentMajor   int        `json:"current_major"`
	FromMajor      int        `json:"from_major"`
	TargetMajor    int        `json:"target_major"`
	Image          string     `json:"image"`
	BackupID       string     `json:"backup_id"`
	PostBackupID   string     `json:"post_backup_id"`
	Error          string     `json:"error"`
	CanRevert      bool       `json:"can_revert"`
	RevertDeadline *time.Time `json:"revert_deadline"`
	InProgress     bool       `json:"in_progress"`
	Available      []int      `json:"available_targets"`
}

func formatPooling(svc, env string, p *pgPooling) string {
	var b strings.Builder
	if p.Status == "off" {
		fmt.Fprintf(&b, "%s (%s): PgBouncer off — DATABASE_URL connects directly to PostgreSQL\n", svc, env)
		return b.String()
	}
	fmt.Fprintf(&b, "%s (%s): PgBouncer %s (%s)\n", svc, env, p.Status, p.PoolerServiceName)
	prep := "default"
	if p.MaxPreparedStatements != nil {
		prep = fmt.Sprint(*p.MaxPreparedStatements)
	}
	fmt.Fprintf(&b, "pool mode: %s · default pool size: %d · max client conn: %d · max prepared statements: %s\n",
		p.PoolMode, p.DefaultPoolSize, p.MaxClientConn, prep)
	if len(p.Variables) > 0 {
		fmt.Fprintf(&b, "variables: %s\n", strings.Join(p.Variables, ", "))
	}
	if p.Error != "" {
		fmt.Fprintf(&b, "error: %s\n", p.Error)
	}
	return b.String()
}

func formatPreflight(p *pgPreflight) string {
	var b strings.Builder
	fmt.Fprintf(&b, "preflight: PostgreSQL %d → %d\n", p.CurrentMajor, p.TargetMajor)
	for _, c := range p.Checks {
		mark := "ok  "
		switch {
		case !c.Passed && c.Severity == "blocker":
			mark = "FAIL"
		case !c.Passed:
			mark = "warn"
		case c.Severity == "info":
			mark = "info"
		}
		fmt.Fprintf(&b, "  [%s] %s", mark, c.Label)
		if !c.Passed || c.Severity == "info" {
			fmt.Fprintf(&b, ": %s", c.Message)
		}
		b.WriteString("\n")
	}
	if p.Ready {
		b.WriteString("ready: no blockers\n")
	} else {
		b.WriteString("not ready: fix the FAIL items and re-run the check\n")
	}
	return b.String()
}

func formatUpgrade(svc, env string, u *pgUpgrade) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s): PostgreSQL %d (%s)\n", svc, env, u.CurrentMajor, u.Image)
	if u.Status != "none" {
		fmt.Fprintf(&b, "last upgrade: %d → %d %s\n", u.FromMajor, u.TargetMajor, u.Status)
	}
	if u.BackupID != "" {
		fmt.Fprintf(&b, "pre-upgrade volume backup: %s\n", u.BackupID)
	}
	if u.PostBackupID != "" {
		fmt.Fprintf(&b, "post-upgrade database backup: %s\n", u.PostBackupID)
	}
	if u.Error != "" {
		fmt.Fprintf(&b, "error: %s\n", u.Error)
	}
	if u.CanRevert {
		if u.RevertDeadline != nil {
			fmt.Fprintf(&b, "revert available until %s (aven postgres upgrade revert)\n", u.RevertDeadline.Local().Format("2006-01-02 15:04"))
		} else {
			b.WriteString("revert available (aven postgres upgrade revert)\n")
		}
	}
	if len(u.Available) > 0 && !u.InProgress {
		fmt.Fprintf(&b, "available targets: %s\n", strings.Trim(fmt.Sprint(u.Available), "[]"))
	}
	return b.String()
}

func printJSONOr(jsonOut bool, v any, text string) {
	if jsonOut {
		b, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Print(text)
}

func cmdPostgres(c *config, args []string) {
	o, err := parsePostgresArgs(args)
	if err != nil {
		die("%v", err)
	}
	l := (&targetFlags{svc: &o.svc, env: &o.env}).service(c)
	base := l.instPath() + "/postgres"
	switch o.area + " " + o.action {
	case "pooling status":
		var p pgPooling
		if err := c.call("GET", base+"/pooling", nil, &p); err != nil {
			die("%v", err)
		}
		printJSONOr(o.jsonOut, &p, formatPooling(l.Service, l.Environment, &p))
	case "pooling enable", "pooling configure":
		var p pgPooling
		if err := c.call("PUT", base+"/pooling", o.poolingBody(), &p); err != nil {
			die("%v", err)
		}
		if !o.jsonOut {
			fmt.Printf("deploying PgBouncer %s for %s (%s)...\n", p.PoolerServiceName, l.Service, l.Environment)
		}
		if !o.detach {
			for p.Status == "deploying" || p.Status == "updating" {
				time.Sleep(2 * time.Second)
				if err := c.call("GET", base+"/pooling", nil, &p); err != nil {
					die("%v", err)
				}
			}
		}
		printJSONOr(o.jsonOut, &p, formatPooling(l.Service, l.Environment, &p))
		if p.Status == "failed" || (!o.detach && p.Error != "") {
			os.Exit(1)
		}
	case "pooling disable":
		confirm(o.yes, fmt.Sprintf("remove PgBouncer from %s (%s)? Active pooled connections are dropped and DATABASE_URL points at PostgreSQL again", l.Service, l.Environment))
		var p pgPooling
		if err := c.call("DELETE", base+"/pooling", nil, &p); err != nil {
			die("%v", err)
		}
		printJSONOr(o.jsonOut, &p, formatPooling(l.Service, l.Environment, &p))
	case "upgrade status":
		var u pgUpgrade
		if err := c.call("GET", base+"/upgrade", nil, &u); err != nil {
			die("%v", err)
		}
		printJSONOr(o.jsonOut, &u, formatUpgrade(l.Service, l.Environment, &u))
	case "upgrade check", "upgrade start":
		q := ""
		if o.to > 0 {
			q = fmt.Sprintf("?to=%d", o.to)
		}
		var p pgPreflight
		if err := c.call("GET", base+"/upgrade/preflight"+q, nil, &p); err != nil {
			die("%v", err)
		}
		if o.action == "check" || !p.Ready {
			printJSONOr(o.jsonOut, &p, formatPreflight(&p))
			if !p.Ready {
				os.Exit(1)
			}
			return
		}
		if !o.jsonOut {
			fmt.Print(formatPreflight(&p))
		}
		confirm(o.yes, fmt.Sprintf("upgrade %s (%s) from PostgreSQL %d to %d? The database is stopped during the upgrade; a backup is taken first",
			l.Service, l.Environment, p.CurrentMajor, p.TargetMajor))
		var u pgUpgrade
		if err := c.call("POST", base+"/upgrade", map[string]int{"to_major": p.TargetMajor}, &u); err != nil {
			die("%v", err)
		}
		c.waitPostgresUpgrade(base, o, l, &u)
	case "upgrade revert":
		confirm(o.yes, fmt.Sprintf("revert %s (%s) to the pre-upgrade backup? Writes made after the upgrade are lost", l.Service, l.Environment))
		var u pgUpgrade
		if err := c.call("POST", base+"/upgrade/revert", nil, &u); err != nil {
			die("%v", err)
		}
		c.waitPostgresUpgrade(base, o, l, &u)
	}
}

func (c *config) waitPostgresUpgrade(base string, o *pgOpts, l *link, u *pgUpgrade) {
	last := ""
	for !o.detach && u.InProgress {
		if u.Status != last && !o.jsonOut {
			fmt.Printf("%s...\n", map[string]string{"preparing": "stopping the database and taking a volume backup",
				"upgrading": "running pg_upgrade on the new major", "reverting": "restoring the pre-upgrade backup"}[u.Status])
			last = u.Status
		}
		time.Sleep(3 * time.Second)
		if err := c.call("GET", base+"/upgrade", nil, u); err != nil {
			die("%v", err)
		}
	}
	printJSONOr(o.jsonOut, u, formatUpgrade(l.Service, l.Environment, u))
	if u.Status == "failed" || u.Status == "revert_failed" || u.Status == "reverted" && u.Error != "" {
		os.Exit(1)
	}
}
