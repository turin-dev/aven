package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// --id를 주지 않은 exec/ssh/fork/checkpoint create/destroy/file은 그것을 대상으로 한다.

type sandboxView struct {
	ID                 string     `json:"id"`
	EnvironmentID      string     `json:"environment_id"`
	NodeID             string     `json:"node_id"`
	Image              string     `json:"image"`
	Status             string     `json:"status"`
	NetworkMode        string     `json:"network_mode"`
	MemoryMB           int        `json:"memory_mb"`
	CPUCores           float64    `json:"cpu_cores"`
	IdleTimeoutMinutes int        `json:"idle_timeout_minutes"`
	ExpiresAt          *time.Time `json:"expires_at"`
	LastActivityAt     time.Time  `json:"last_activity_at"`
	ParentID           *string    `json:"parent_id"`
	CheckpointName     *string    `json:"checkpoint_name"`
	CreatedAt          time.Time  `json:"created_at"`
}

type sandboxCheckpointView struct {
	ID            string    `json:"id"`
	EnvironmentID string    `json:"environment_id"`
	NodeID        string    `json:"node_id"`
	Name          string    `json:"name"`
	CreatedAt     time.Time `json:"created_at"`
}

type sandboxExecResult struct {
	Stdout    []byte `json:"stdout"`
	Stderr    []byte `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
	Truncated bool   `json:"truncated"`
	TimedOut  bool   `json:"timed_out"`
}

const sandboxUsage = `usage: aven sandbox <command>
  create [--image img] [--checkpoint name] [--idle-timeout-minutes n] [--memory-mb n] [--cpu n]
         [--private-network] [--variable K=V]... [--env-file f]... [--node id] [--json]
  fork [id] [--idle-timeout-minutes n] [--private-network] [--variable K=V]... [--json]
  list [--json]
  exec [--id id] [--timeout s] [--cwd dir] -- <command...>
  ssh [--id id] [-- command...]
  file ls|read|write [--id id] <path> [local-file|-] [--mode 0644]
  checkpoint create <name> [--id id] | list | rename <old> <new> | delete <name>
  destroy [id]
common: -e <environment>`

// ---- active sandbox (환경별, ~/.config/keel/sandboxes.json) ----

func activeSandboxPath() string { return filepath.Join(filepath.Dir(cfgPath()), "sandboxes.json") }

func loadActiveSandboxes() map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(activeSandboxPath()); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

func setActiveSandbox(envID, id string) {
	m := loadActiveSandboxes()
	if id == "" {
		delete(m, envID)
	} else {
		m[envID] = id
	}
	os.MkdirAll(filepath.Dir(activeSandboxPath()), 0o700)
	b, _ := json.MarshalIndent(m, "", "  ")
	os.WriteFile(activeSandboxPath(), b, 0o600)
}

// ---- flags ----

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

var sandboxPairRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// splitVariableFlag는 --variable 값을 나눈다. 쉼표로 나눈 모든 조각이 KEY=VALUE일 때만 나눈다(값의 쉼표 보존).
func splitVariableFlag(v string) []string {
	parts := strings.Split(v, ",")
	for _, p := range parts {
		if !sandboxPairRe.MatchString(p) {
			return []string{v}
		}
	}
	return parts
}

type sandboxCreateFlags struct {
	idle     *int
	mem      *int
	cpu      *float64
	private  *bool
	vars     multiFlag
	envFiles multiFlag
	jsonOut  *bool
}

func addSandboxCreateFlags(fl *flag.FlagSet) *sandboxCreateFlags {
	f := &sandboxCreateFlags{
		idle:    fl.Int("idle-timeout-minutes", -1, "destroy after this many idle minutes (0 = never; default 30)"),
		mem:     fl.Int("memory-mb", 0, "memory limit in MiB (default 1024)"),
		cpu:     fl.Float64("cpu", 0, "CPU limit in cores (default 1)"),
		private: fl.Bool("private-network", false, "join the environment's private network"),
		jsonOut: fl.Bool("json", false, "print JSON"),
	}
	fl.Var(&f.vars, "variable", "KEY=VALUE variable (repeatable, comma-separable)")
	fl.Var(&f.envFiles, "env-file", "load variables from a .env file (repeatable)")
	return f
}

func (f *sandboxCreateFlags) body() map[string]any {
	vars := map[string]string{}
	for _, p := range f.envFiles {
		m, err := readDotenvFile(p)
		if err != nil {
			die("%v", err)
		}
		for k, v := range m {
			vars[k] = v
		}
	}
	for _, raw := range f.vars {
		for _, kv := range splitVariableFlag(raw) {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "" {
				die("--variable must be KEY=VALUE (got %q)", kv)
			}
			vars[k] = v
		}
	}
	body := map[string]any{"memory_mb": *f.mem, "cpu_cores": *f.cpu}
	if *f.idle >= 0 {
		body["idle_timeout_minutes"] = *f.idle
	}
	if *f.private {
		body["network"] = "private"
	}
	if len(vars) > 0 {
		body["variables"] = vars
	}
	return body
}

// sandboxTarget은 -e와 --id(또는 위치 인자)로 대상 샌드박스와 환경을 정한다. id가 없으면 active 샌드박스다.
func sandboxTarget(c *config, tf *targetFlags, id string) (envID, sbxID string) {
	if id != "" && *tf.env == "" && !c.isLinked() {
		return "", id // --id만으로도 쓸 수 있다(링크 없는 CI 등)
	}
	l := tf.resolve(c)
	if id != "" {
		return l.EnvironmentID, id
	}
	if l.EnvironmentID == "" {
		die("no environment linked (run: aven link, or pass --id)")
	}
	if a := loadActiveSandboxes()[l.EnvironmentID]; a != "" {
		return l.EnvironmentID, a
	}
	die("no active sandbox in this environment (run: aven sandbox create, or pass --id)")
	return "", ""
}

// isLinked는 현재 디렉터리(또는 상위)가 링크되어 있는지다(linked()와 달리 죽지 않는다).
func (c *config) isLinked() bool {
	dir, _ := os.Getwd()
	for d := dir; ; d = filepath.Dir(d) {
		if _, ok := c.Links[d]; ok {
			return true
		}
		if d == filepath.Dir(d) {
			return false
		}
	}
}

func sandboxEnv(c *config, tf *targetFlags) string {
	l := tf.resolve(c)
	if l.EnvironmentID == "" {
		die("no environment linked (run: aven link or pass -e)")
	}
	return l.EnvironmentID
}

func printSandbox(b sandboxView, verb string, jsonOut bool) {
	if jsonOut {
		printJSON(b)
		return
	}
	fmt.Printf("%s sandbox %s (%s, node %s, %s network)\n", verb, b.ID, b.Image, b.NodeID, b.NetworkMode)
	if b.ExpiresAt != nil {
		fmt.Printf("idle timeout %d min; destroyed if unused until %s\n", b.IdleTimeoutMinutes, b.ExpiresAt.Local().Format(time.RFC3339))
	} else {
		fmt.Println("idle timeout disabled; destroy it with: aven sandbox destroy")
	}
}

func cmdSandboxes(c *config, args []string) {
	if len(args) == 0 {
		args = []string{"list"}
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "create", "new":
		cmdSandboxCreate(c, args)
	case "fork":
		cmdSandboxFork(c, args)
	case "list", "ls":
		cmdSandboxList(c, args)
	case "exec":
		cmdSandboxExec(c, args)
	case "ssh", "connect", "shell":
		cmdSandboxSSH(c, args)
	case "file", "files":
		cmdSandboxFiles(c, args)
	case "checkpoint", "checkpoints":
		cmdSandboxCheckpoint(c, args)
	case "destroy", "rm", "delete":
		cmdSandboxDestroy(c, args)
	case "help", "-h", "--help":
		fmt.Println(sandboxUsage)
	default:
		die("%s", sandboxUsage)
	}
}

func cmdSandboxCreate(c *config, args []string) {
	fl := flag.NewFlagSet("sandbox create", flag.ExitOnError)
	tf := addTargetFlags(fl)
	image := fl.String("image", "", "container image (default ubuntu:24.04; needs /bin/sh)")
	checkpoint := fl.String("checkpoint", "", "create from a named checkpoint")
	nodeID := fl.String("node", "", "node ID (default: the server's node)")
	cf := addSandboxCreateFlags(fl)
	fl.Parse(reorderFlags(args))
	if fl.NArg() != 0 {
		die("%s", sandboxUsage)
	}
	envID := sandboxEnv(c, tf)
	body := cf.body()
	body["image"], body["checkpoint"], body["node_id"] = *image, *checkpoint, *nodeID
	var out sandboxView
	if err := c.call("POST", "/environments/"+url.PathEscape(envID)+"/sandboxes", body, &out); err != nil {
		die("%v", err)
	}
	setActiveSandbox(envID, out.ID)
	printSandbox(out, "created", *cf.jsonOut)
}

func cmdSandboxFork(c *config, args []string) {
	fl := flag.NewFlagSet("sandbox fork", flag.ExitOnError)
	tf := addTargetFlags(fl)
	idFlag := fl.String("id", "", "source sandbox ID (default: active)")
	cf := addSandboxCreateFlags(fl)
	fl.Parse(reorderFlags(args))
	id := *idFlag
	if fl.NArg() == 1 && id == "" {
		id = fl.Arg(0)
	} else if fl.NArg() != 0 {
		die("usage: aven sandbox fork [id]")
	}
	envID, id := sandboxTarget(c, tf, id)
	var out sandboxView
	if err := c.call("POST", "/sandboxes/"+url.PathEscape(id)+"/fork", cf.body(), &out); err != nil {
		die("%v", err)
	}
	if envID == "" {
		envID = out.EnvironmentID
	}
	setActiveSandbox(envID, out.ID)
	printSandbox(out, "forked "+id+" into", *cf.jsonOut)
}

func cmdSandboxList(c *config, args []string) {
	fl := flag.NewFlagSet("sandbox list", flag.ExitOnError)
	tf := addTargetFlags(fl)
	jsonOut := fl.Bool("json", false, "print JSON")
	fl.Parse(reorderFlags(args))
	if fl.NArg() != 0 {
		die("usage: aven sandbox list [-e env] [--json]")
	}
	envID := sandboxEnv(c, tf)
	var items []sandboxView
	if err := c.call("GET", "/environments/"+url.PathEscape(envID)+"/sandboxes", nil, &items); err != nil {
		die("%v", err)
	}
	if *jsonOut {
		printJSON(items)
		return
	}
	if len(items) == 0 {
		fmt.Println("no sandboxes")
		return
	}
	active := loadActiveSandboxes()[envID]
	for _, b := range items {
		mark := " "
		if b.ID == active {
			mark = "*"
		}
		idle := "never"
		if b.ExpiresAt != nil {
			idle = b.ExpiresAt.Local().Format("01-02 15:04")
		}
		fmt.Printf("%s %-20s %-8s %-8s idle-until=%-11s node=%s image=%s\n", mark, b.ID, b.Status, b.NetworkMode, idle, b.NodeID, b.Image)
	}
}

func sandboxExecArgv(pos []string) map[string]any {
	if len(pos) == 1 {
		return map[string]any{"command": pos[0]}
	}
	return map[string]any{"cmd": pos}
}

func cmdSandboxExec(c *config, args []string) {
	fl := flag.NewFlagSet("sandbox exec", flag.ExitOnError)
	tf := addTargetFlags(fl)
	idFlag := fl.String("id", "", "sandbox ID (default: active)")
	timeout := fl.Int("timeout", 0, "deadline in seconds (max 110; default 110). Exits 124 when it expires")
	cwd := fl.String("cwd", "", "working directory")
	fl.Parse(reorderFlags(args))
	pos := fl.Args()
	if len(pos) > 0 && pos[0] == "--" {
		pos = pos[1:]
	}
	if len(pos) == 0 {
		die("usage: aven sandbox exec [--id id] [--timeout s] -- <command...>")
	}
	envID, id := sandboxTarget(c, tf, *idFlag)
	body := sandboxExecArgv(pos)
	if *timeout > 0 {
		body["timeout_s"] = *timeout
	}
	if *cwd != "" {
		body["cwd"] = *cwd
	}
	// 파이프나 파일로 들어온 stdin만 보낸다(터미널이면 보내지 않는다).
	if st, err := os.Stdin.Stat(); err == nil && (st.Mode()&os.ModeNamedPipe != 0 || st.Mode().IsRegular()) {
		stdin, _ := io.ReadAll(io.LimitReader(os.Stdin, (256<<10)+1))
		if len(stdin) > 256<<10 {
			die("stdin is limited to 256 KiB")
		}
		body["stdin"] = stdin
	}
	var out sandboxExecResult
	if err := c.call("POST", "/sandboxes/"+url.PathEscape(id)+"/exec", body, &out); err != nil {
		die("%v", err)
	}
	if envID != "" {
		setActiveSandbox(envID, id)
	}
	os.Stdout.Write(out.Stdout)
	os.Stderr.Write(out.Stderr)
	if out.Truncated {
		fmt.Fprintln(os.Stderr, "aven: output truncated")
	}
	os.Exit(sandboxExitCode(out))
}

func sandboxExitCode(r sandboxExecResult) int {
	switch {
	case r.TimedOut:
		return 124
	case r.ExitCode < 0 || r.ExitCode > 255:
		return 1
	}
	return r.ExitCode
}

func cmdSandboxSSH(c *config, args []string) {
	fl := flag.NewFlagSet("sandbox ssh", flag.ExitOnError)
	tf := addTargetFlags(fl)
	idFlag := fl.String("id", "", "sandbox ID (default: active)")
	fl.Parse(reorderFlags(args))
	envID, id := sandboxTarget(c, tf, *idFlag)
	if c.URL == "" {
		die("not logged in (run: aven login <url>)")
	}
	pos := fl.Args()
	if len(pos) > 0 && pos[0] == "--" {
		pos = pos[1:]
	}
	cols, rows := 80, 24
	if w, h, ok := termSize(os.Stdout); ok {
		cols, rows = w, h
	}
	u, err := sandboxShellURL(c.URL, id, pos, cols, rows)
	if err != nil {
		die("%v", err)
	}
	if envID != "" {
		setActiveSandbox(envID, id)
	}
	os.Exit(runShellSocket(c, u))
}

func sandboxShellURL(base, id string, cmd []string, cols, rows int) (string, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("unsupported server URL %q", base)
	}
	u.Path += "/api/sandboxes/" + url.PathEscape(id) + "/shell"
	q := url.Values{}
	if len(cmd) > 0 {
		q.Set("cmd", shellJoin(cmd))
	}
	q.Set("cols", strconv.Itoa(cols))
	q.Set("rows", strconv.Itoa(rows))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// runShellSocket은 셸 WebSocket에 터미널을 잇고 원격 종료 코드를 돌려준다(aven ssh와 같은 프로토콜).
func runShellSocket(c *config, u string) int {
	ctx := context.Background()
	conn, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: c.authHeader()})
	if err != nil {
		if resp != nil && resp.Body != nil {
			var e struct {
				Error string `json:"error"`
			}
			json.NewDecoder(io.LimitReader(resp.Body, 1024)).Decode(&e)
			if resp.StatusCode == 401 {
				die("unauthorized (run: aven login <url>)")
			}
			if e.Error != "" {
				die("%s", e.Error)
			}
			die("ssh: %s", resp.Status)
		}
		die("ssh: %v", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	restore := func() {}
	if isTerminal(os.Stdin) && isTerminal(os.Stdout) {
		if r, err := makeRaw(os.Stdin); err == nil {
			restore = r
		}
	}
	resize := make(chan [2]int, 1)
	stop := watchResize(func() {
		if w, h, ok := termSize(os.Stdout); ok {
			select {
			case resize <- [2]int{w, h}:
			default:
			}
		}
	})
	code := sshPump(ctx, conn, os.Stdin, os.Stdout, os.Stderr, resize)
	stop()
	restore()
	conn.Close(websocket.StatusNormalClosure, "")
	return code
}

func cmdSandboxDestroy(c *config, args []string) {
	fl := flag.NewFlagSet("sandbox destroy", flag.ExitOnError)
	tf := addTargetFlags(fl)
	idFlag := fl.String("id", "", "sandbox ID (default: active)")
	fl.Parse(reorderFlags(args))
	id := *idFlag
	if fl.NArg() == 1 && id == "" {
		id = fl.Arg(0)
	} else if fl.NArg() != 0 {
		die("usage: aven sandbox destroy [id]")
	}
	envID, id := sandboxTarget(c, tf, id)
	if err := c.call("DELETE", "/sandboxes/"+url.PathEscape(id), nil, nil); err != nil {
		die("%v", err)
	}
	if envID != "" && loadActiveSandboxes()[envID] == id {
		setActiveSandbox(envID, "")
	}
	fmt.Printf("destroyed sandbox %s\n", id)
}

func findCheckpoint(c *config, envID, ref string) sandboxCheckpointView {
	var items []sandboxCheckpointView
	if err := c.call("GET", "/environments/"+url.PathEscape(envID)+"/sandbox-checkpoints", nil, &items); err != nil {
		die("%v", err)
	}
	for _, cp := range items {
		if cp.Name == ref || cp.ID == ref {
			return cp
		}
	}
	die("checkpoint %q not found", ref)
	return sandboxCheckpointView{}
}

func cmdSandboxCheckpoint(c *config, args []string) {
	if len(args) == 0 {
		args = []string{"list"}
	}
	sub, args := args[0], args[1:]
	fl := flag.NewFlagSet("sandbox checkpoint "+sub, flag.ExitOnError)
	tf := addTargetFlags(fl)
	idFlag := fl.String("id", "", "source sandbox ID (default: active)")
	jsonOut := fl.Bool("json", false, "print JSON")
	fl.Parse(reorderFlags(args))
	pos := fl.Args()
	switch sub {
	case "create", "capture", "save":
		if len(pos) != 1 {
			die("usage: aven sandbox checkpoint create <name> [--id id]")
		}
		_, id := sandboxTarget(c, tf, *idFlag)
		var cp sandboxCheckpointView
		if err := c.call("POST", "/sandboxes/"+url.PathEscape(id)+"/checkpoints", map[string]string{"name": pos[0]}, &cp); err != nil {
			die("%v", err)
		}
		if *jsonOut {
			printJSON(cp)
			return
		}
		fmt.Printf("saved checkpoint %s from %s (create with: aven sandbox create --checkpoint %s)\n", cp.Name, id, cp.Name)
	case "list", "ls":
		envID := sandboxEnv(c, tf)
		var items []sandboxCheckpointView
		if err := c.call("GET", "/environments/"+url.PathEscape(envID)+"/sandbox-checkpoints", nil, &items); err != nil {
			die("%v", err)
		}
		if *jsonOut {
			printJSON(items)
			return
		}
		if len(items) == 0 {
			fmt.Println("no checkpoints")
		}
		for _, cp := range items {
			fmt.Printf("%-32s %-20s node=%s %s\n", cp.Name, cp.ID, cp.NodeID, cp.CreatedAt.Local().Format(time.RFC3339))
		}
	case "rename":
		if len(pos) != 2 {
			die("usage: aven sandbox checkpoint rename <old> <new>")
		}
		cp := findCheckpoint(c, sandboxEnv(c, tf), pos[0])
		if err := c.call("PATCH", "/sandbox-checkpoints/"+url.PathEscape(cp.ID), map[string]string{"name": pos[1]}, &cp); err != nil {
			die("%v", err)
		}
		if *jsonOut {
			printJSON(cp)
			return
		}
		fmt.Printf("renamed checkpoint %s to %s\n", pos[0], pos[1])
	case "delete", "rm":
		if len(pos) != 1 {
			die("usage: aven sandbox checkpoint delete <name>")
		}
		cp := findCheckpoint(c, sandboxEnv(c, tf), pos[0])
		if err := c.call("DELETE", "/sandbox-checkpoints/"+url.PathEscape(cp.ID), nil, nil); err != nil {
			die("%v", err)
		}
		fmt.Printf("deleted checkpoint %s\n", cp.Name)
	default:
		die("usage: aven sandbox checkpoint create|list|rename|delete")
	}
}

func cmdSandboxFiles(c *config, args []string) {
	if len(args) == 0 {
		die("usage: aven sandbox file ls|read|write [--id id] <path> [local-file|-]")
	}
	action, args := args[0], args[1:]
	fl := flag.NewFlagSet("sandbox file "+action, flag.ExitOnError)
	tf := addTargetFlags(fl)
	idFlag := fl.String("id", "", "sandbox ID (default: active)")
	mode := fl.String("mode", "0644", "file mode for write (octal)")
	jsonOut := fl.Bool("json", false, "print JSON (ls)")
	fl.Parse(reorderFlags(args))
	pos := fl.Args()
	_, id := sandboxTarget(c, tf, *idFlag)
	base := "/sandboxes/" + url.PathEscape(id) + "/files"
	switch action {
	case "ls", "list":
		p := "/"
		if len(pos) == 1 {
			p = pos[0]
		} else if len(pos) > 1 {
			die("usage: aven sandbox file ls [path]")
		}
		var out struct {
			Entries []struct {
				Name    string    `json:"name"`
				Type    string    `json:"type"`
				Size    int64     `json:"size"`
				ModTime time.Time `json:"mod_time"`
			} `json:"entries"`
		}
		if err := c.call("GET", base+"/list?path="+url.QueryEscape(p), nil, &out); err != nil {
			die("%v", err)
		}
		if *jsonOut {
			printJSON(out.Entries)
			return
		}
		for _, e := range out.Entries {
			name := e.Name
			if e.Type == "dir" {
				name += "/"
			}
			fmt.Printf("%-7s %10d  %s  %s\n", e.Type, e.Size, e.ModTime.Local().Format("2006-01-02 15:04"), name)
		}
	case "read", "cat":
		if len(pos) != 1 {
			die("usage: aven sandbox file read <path>")
		}
		var out struct {
			ContentBase64 string `json:"content_base64"`
			Truncated     bool   `json:"truncated"`
		}
		if err := c.call("GET", base+"?path="+url.QueryEscape(pos[0]), nil, &out); err != nil {
			die("%v", err)
		}
		data, err := base64.StdEncoding.DecodeString(out.ContentBase64)
		if err != nil {
			die("decode sandbox file: %v", err)
		}
		os.Stdout.Write(data)
		if out.Truncated {
			fmt.Fprintln(os.Stderr, "aven: file truncated at 2 MiB")
		}
	case "write", "put":
		if len(pos) < 1 || len(pos) > 2 {
			die("usage: aven sandbox file write <path> [local-file|-]")
		}
		m, err := strconv.ParseUint(*mode, 8, 32)
		if err != nil || m > 0o7777 {
			die("--mode must be octal like 0644")
		}
		var data []byte
		if len(pos) == 2 && pos[1] != "-" {
			data, err = os.ReadFile(pos[1])
		} else {
			data, err = io.ReadAll(io.LimitReader(os.Stdin, (256<<10)+1))
		}
		if err == nil && len(data) > 256<<10 {
			err = errors.New("file is larger than 256 KiB")
		}
		if err != nil {
			die("%v", err)
		}
		body := map[string]any{"path": pos[0], "content_base64": base64.StdEncoding.EncodeToString(data), "mode": m}
		if err := c.call("PUT", base, body, nil); err != nil {
			die("%v", err)
		}
		fmt.Printf("wrote %d bytes to %s\n", len(data), pos[0])
	default:
		die("usage: aven sandbox file ls|read|write ...")
	}
}
