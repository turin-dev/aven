package main

// aven dev configure: repo 서비스를 로컬에서 실행하는 방법(명령, 디렉터리, 포트)을 이 컴퓨터에만 저장한다.
// 서버 설정은 바꾸지 않는다. 파일은 config.json 옆의 dev.json이다.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type devServiceOverride struct {
	Command   string `json:"command,omitempty"`
	Directory string `json:"directory,omitempty"`
	Port      int    `json:"port,omitempty"`
}

func (o *devServiceOverride) empty() bool { return o.Command == "" && o.Directory == "" && o.Port == 0 }

// devProjectConfig는 서비스 ID별 로컬 설정이다.
type devProjectConfig map[string]*devServiceOverride

type devLocalConfig struct {
	Projects map[string]devProjectConfig `json:"projects"`
}

func devConfigPath() string { return filepath.Join(filepath.Dir(cfgPath()), "dev.json") }

func loadDevConfig() (*devLocalConfig, error) {
	c := &devLocalConfig{}
	b, err := os.ReadFile(devConfigPath())
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, c); err != nil {
			return nil, fmt.Errorf("%s: %w", devConfigPath(), err)
		}
	}
	if c.Projects == nil {
		c.Projects = map[string]devProjectConfig{}
	}
	return c, nil
}

func (c *devLocalConfig) save() error {
	if err := os.MkdirAll(filepath.Dir(devConfigPath()), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(devConfigPath(), b, 0o600)
}

// applyDevOverride는 o의 --command/--dir/--port를 ov에 반영한다. 빈 값(포트 0)은 그 항목을 지운다.
func applyDevOverride(ov *devServiceOverride, o *devOptions) {
	if o.command != nil {
		ov.Command = strings.TrimSpace(*o.command)
	}
	if o.dir != nil {
		ov.Directory = strings.TrimSpace(*o.dir)
	}
	if o.port != nil {
		ov.Port = *o.port
	}
}

func cmdDevConfigure(p *project, o *devOptions) {
	cfg, err := loadDevConfig()
	if err != nil {
		die("%v", err)
	}
	pc := cfg.Projects[p.ID]
	if pc == nil {
		pc = devProjectConfig{}
	}
	var targets []svcRef
	for _, s := range p.Services {
		if s.SourceType == "repo" && (o.service == "" || s.ID == o.service || s.Name == o.service) {
			targets = append(targets, s)
		}
	}
	if o.service != "" && len(targets) == 0 {
		die("code service %q not found (%s)", o.service, names(devRepoServices(p), func(s svcRef) string { return s.Name }))
	}
	if len(targets) == 0 {
		die("project %s has no code (repo) services to configure", p.Name)
	}
	if o.remove {
		for _, s := range targets {
			delete(pc, s.ID)
			fmt.Printf("removed local configuration for %s\n", s.Name)
		}
		cfg.Projects[p.ID] = pc
		if len(pc) == 0 {
			delete(cfg.Projects, p.ID)
		}
		if err := cfg.save(); err != nil {
			die("%v", err)
		}
		return
	}
	flags := o.command != nil || o.dir != nil || o.port != nil
	if flags && len(targets) != 1 {
		die("pass --service <name> with --command/--dir/--port")
	}
	interactive := !flags
	if interactive {
		if st, err := os.Stdin.Stat(); err != nil || st.Mode()&os.ModeCharDevice == 0 {
			die("aven dev configure is interactive; pass --service with --command, --dir or --port in non-interactive use")
		}
	}
	in := bufio.NewReader(os.Stdin)
	for _, s := range targets {
		ov := pc[s.ID]
		if ov == nil {
			ov = &devServiceOverride{}
		}
		if interactive {
			fmt.Fprintf(os.Stderr, "%s (empty keeps the current value, \"-\" clears it)\n", s.Name)
			ov.Command = devPrompt(in, "  command", ov.Command, "server start command")
			ov.Directory = devPrompt(in, "  directory", ov.Directory, "server root directory")
			for {
				v := devPrompt(in, "  port", portText(ov.Port), "from settings or PORT")
				n, err := strconv.Atoi(v)
				if v == "" {
					n, err = 0, nil
				}
				if err == nil && n >= 0 && n <= 65535 {
					ov.Port = n
					break
				}
				fmt.Fprintln(os.Stderr, "  port must be a number between 1 and 65535")
			}
		} else {
			applyDevOverride(ov, o)
		}
		if ov.empty() {
			delete(pc, s.ID)
			fmt.Printf("%s: using the server settings\n", s.Name)
		} else {
			pc[s.ID] = ov
			fmt.Printf("%s: command=%q directory=%q port=%s\n", s.Name, ov.Command, ov.Directory, portText(ov.Port))
		}
	}
	cfg.Projects[p.ID] = pc
	if len(pc) == 0 {
		delete(cfg.Projects, p.ID)
	}
	if err := cfg.save(); err != nil {
		die("%v", err)
	}
}

func devRepoServices(p *project) []svcRef {
	var out []svcRef
	for _, s := range p.Services {
		if s.SourceType == "repo" {
			out = append(out, s)
		}
	}
	return out
}

func portText(p int) string {
	if p <= 0 {
		return ""
	}
	return strconv.Itoa(p)
}

// devPrompt는 한 줄을 묻는다. 빈 입력은 cur를 남기고 "-"는 지운다.
func devPrompt(in *bufio.Reader, label, cur, fallback string) string {
	shown := cur
	if shown == "" {
		shown = fallback
	}
	fmt.Fprintf(os.Stderr, "%s [%s]: ", label, shown)
	line, _ := in.ReadString('\n')
	switch v := strings.TrimSpace(line); v {
	case "":
		return cur
	case "-":
		return ""
	default:
		return v
	}
}
