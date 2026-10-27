package iac

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/turin-dev/aven/internal/compat"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	Dir       = ".aven"
	LegacyDir = ".keel"
)

// Files are the authoring files `aven config` looks for in Dir, in order. Keep only one.
var Files = []string{"aven.ts", "aven.mts", "aven.js", "aven.mjs", "aven.json", "aven.yaml", "aven.yml", "keel.ts", "keel.mts", "keel.js", "keel.mjs", "keel.json", "keel.yaml", "keel.yml"}

//go:embed keel-iac.mjs
var dslJS []byte

//go:embed keel-iac.d.mts
var dslTypes []byte

//go:embed runner.mjs
var runnerJS []byte

// DSLModule is the module name that aven.ts imports.
const DSLModule = "./aven-iac.mjs"
const LegacyDSLModule = "./keel-iac.mjs"

// FindFile finds the nearest .aven/aven.{ts,json,yaml,...} from dir upwards (.keel is the fallback).
func findIn(dir, folder string) []string {
	found := []string{}
	for _, name := range Files {
		p := filepath.Join(dir, folder, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			found = append(found, p)
		}
	}
	return found
}

func FindFile(dir string) (string, error) {
	for d := dir; ; d = filepath.Dir(d) {
		found := findIn(d, Dir)
		folder := Dir
		if len(found) == 0 {
			found = findIn(d, LegacyDir)
			folder = LegacyDir
			if len(found) > 0 {
				compat.WarnOnce("iac-dir", "directory %s is in use; %s is the new path (the old path still works)", filepath.Join(d, LegacyDir), filepath.Join(d, Dir))
			}
		}
		switch len(found) {
		case 0:
		case 1:
			return found[0], nil
		default:
			return "", fmt.Errorf("more than one configuration file in %s (%s); keep only one", filepath.Join(d, folder), strings.Join(baseNames(found), ", "))
		}
		if d == filepath.Dir(d) {
			return "", fmt.Errorf("no %s/aven.ts (or aven.json / aven.yaml) here or in a parent directory (run: aven config import)", Dir)
		}
	}
}

func baseNames(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = filepath.Base(p)
	}
	return out
}

// Context is passed to defineAven((ctx) => ...).
type Context struct {
	Command       string `json:"command"`
	ProjectID     string `json:"projectId"`
	ProjectName   string `json:"projectName"`
	EnvironmentID string `json:"environmentId"`
	Environment   string `json:"environment"`
}

// Format returns ts (TypeScript/JavaScript module), json or yaml for a file name.
func Format(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	default:
		return "ts"
	}
}

// Load reads a definition file; TypeScript/JavaScript files are evaluated with bun or node.
func Load(path string, ctx Context) (*Definition, error) {
	if f := Format(path); f != "ts" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		d, err := Decode(b, f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return d, nil
	}
	return Evaluate(path, ctx)
}

// Runner picks the JavaScript runtime: $AVEN_IAC_RUNNER (bun|node|path), else bun, else node. KEEL_IAC_RUNNER still works.
// The returned args go between the binary and the runner script.
func Runner(path string) (string, []string, error) {
	pick := compat.Env("IAC_RUNNER")
	candidates := []string{"bun", "node"}
	if pick != "" {
		candidates = []string{pick}
	}
	for _, c := range candidates {
		bin, err := exec.LookPath(c)
		if err != nil {
			continue
		}
		if strings.Contains(filepath.Base(bin), "node") {
			args := []string{"--no-warnings"}
			if ext := strings.ToLower(filepath.Ext(path)); ext == ".ts" || ext == ".mts" {
				args = append(args, "--experimental-strip-types") // Node 22.6+; the default since 23.6
			}
			return bin, args, nil
		}
		return bin, nil, nil
	}
	if pick != "" {
		return "", nil, fmt.Errorf("%s=%s not found", compat.Describe("IAC_RUNNER"), pick)
	}
	return "", nil, errors.New("evaluating aven.ts needs bun or node (22.6+) in PATH; or use .aven/aven.json / aven.yaml")
}

// Evaluate runs a aven.ts/js module and decodes the JSON definition it returns.
// The module's own output (console.log) goes to stderr.
func Evaluate(path string, ctx Context) (*Definition, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	bin, args, err := Runner(abs)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "aven-iac-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	runner := filepath.Join(tmp, "runner.mjs")
	if err := os.WriteFile(runner, runnerJS, 0o600); err != nil {
		return nil, err
	}
	out := filepath.Join(tmp, "out.json")
	ctxJSON, _ := json.Marshal(ctx)
	cmd := exec.Command(bin, append(args, runner, abs, out)...)
	cmd.Dir = filepath.Dir(abs)
	cmd.Env = append(os.Environ(), "AVEN_IAC_CONTEXT="+string(ctxJSON), "KEEL_IAC_CONTEXT="+string(ctxJSON))
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = os.Stderr, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s: %s", filepath.Base(bin), msg)
	}
	if stderr.Len() > 0 {
		os.Stderr.Write(stderr.Bytes())
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return nil, fmt.Errorf("%s produced no definition: %w", path, err)
	}
	d, err := Decode(b, "json")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return d, nil
}

// WriteSupportFiles writes the DSL module, its types and a package.json (so node treats aven.ts as ESM).
func WriteSupportFiles(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	files := map[string][]byte{
		"aven-iac.mjs":   dslJS,
		"aven-iac.d.mts": dslTypes,
		// 예전 import { defineAven } from "./keel-iac.mjs" 가 그대로 동작하게 같은 파일을 쓴다.
		"keel-iac.mjs":   dslJS,
		"keel-iac.d.mts": dslTypes,
	}
	if _, err := os.Stat(filepath.Join(dir, "package.json")); errors.Is(err, os.ErrNotExist) {
		files["package.json"] = []byte("{\n  \"private\": true,\n  \"type\": \"module\"\n}\n")
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// ---- TypeScript rendering (aven config import) ----

// friendly is FIELDS in keel-iac.mjs reversed: setting → option name.
var friendly = map[string]string{
	"start_command": "start", "pre_deploy_command": "preDeploy", "healthcheck_path": "healthcheck",
	"healthcheck_type": "healthcheckType", "healthcheck_timeout_s": "healthcheckTimeout", "replicas": "replicas",
	"placement": "placement", "port": "port", "builder": "builder", "dockerfile_path": "dockerfilePath",
	"watch_paths": "watchPaths", "auto_deploy": "autoDeploy", "restart_policy": "restartPolicy",
	"restart_max_retries": "restartMaxRetries", "cron_schedule": "cron", "sleep_when_idle": "sleepWhenIdle",
	"sleep_after_s": "sleepAfter", "memory_limit_mb": "memoryLimitMB", "cpu_limit": "cpuLimit",
	"build_secrets": "buildSecrets", "wait_for_ci": "waitForCI", "overlap_seconds": "overlapSeconds",
	"draining_seconds": "drainingSeconds", "private_ports": "privatePorts", "autoscale": "autoscale",
}

var (
	identRe     = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)
	githubURLRe = regexp.MustCompile(`^https://github\.com/([\w.-]+/[\w.-]+)$`)
	reserved    = map[string]bool{}
)

func init() {
	for _, w := range strings.Fields(`break case catch class const continue debugger default delete do else enum export extends
		false finally for function if import in instanceof new null return super switch this throw true try typeof var void
		while with yield let static implements interface package private protected public await
		defineAven defineKeel project service database postgres mysql redis mongo bucket functionService group github git image preserve ctx`) {
		reserved[w] = true
	}
}

// jsIdent turns a service name into a unique camelCase identifier.
func jsIdent(name string, used map[string]bool) string {
	parts := strings.FieldsFunc(name, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') })
	id := ""
	for i, p := range parts {
		if i > 0 && p != "" {
			p = strings.ToUpper(p[:1]) + p[1:]
		}
		id += p
	}
	if id == "" || (id[0] >= '0' && id[0] <= '9') {
		id = "svc" + strings.ToUpper(id[:min(1, len(id))]) + id[min(1, len(id)):]
	}
	if reserved[id] {
		id += "Service"
	}
	base := id
	for n := 2; used[id]; n++ {
		id = fmt.Sprintf("%s%d", base, n)
	}
	used[id] = true
	return id
}

func jsKey(k string) string {
	if identRe.MatchString(k) {
		return k
	}
	return jsString(k)
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// jsValue renders JSON as a JS literal, indented to the given prefix.
func jsValue(raw json.RawMessage, indent string) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	b, _ := json.MarshalIndent(v, indent, "  ")
	return string(b)
}

// simpleDomains renders custom domains as "host" / {domain, port} when nothing else is set.
func simpleDomains(raw json.RawMessage) (string, bool) {
	var doms []map[string]any
	if json.Unmarshal(raw, &doms) != nil {
		return "", false
	}
	items := []string{}
	for _, d := range doms {
		host, _ := d["host"].(string)
		port := d["target_port"]
		for k, v := range d {
			if k != "host" && k != "target_port" && !(k == "kind" && v == "custom") {
				return "", false
			}
		}
		if port == nil {
			items = append(items, jsString(host))
		} else {
			items = append(items, fmt.Sprintf("{ domain: %s, port: %v }", jsString(host), port))
		}
	}
	return "[" + strings.Join(items, ", ") + "]", true
}

// RenderTS renders a definition as an editable .keel/aven.ts.
func RenderTS(d *Definition) []byte {
	var b strings.Builder
	used := map[string]bool{}
	helpers := map[string]bool{"defineAven": true, "project": true}
	var body strings.Builder
	idents := []string{}
	for _, s := range d.Services {
		id := jsIdent(s.Name, used)
		idents = append(idents, id)
		var ctor string
		switch {
		case s.SourceType == "database" && s.DatabaseKind != "":
			switch s.DatabaseKind {
			case "postgres", "mysql", "redis", "mongo", "bucket":
				ctor = fmt.Sprintf("%s(%s", s.DatabaseKind, jsString(s.Name))
				helpers[s.DatabaseKind] = true
			default:
				ctor = fmt.Sprintf("database(%s, %s", jsString(s.DatabaseKind), jsString(s.Name))
				helpers["database"] = true
			}
		case s.SourceType == "function":
			ctor = fmt.Sprintf("functionService(%s", jsString(s.Name))
			helpers["functionService"] = true
		default:
			ctor = fmt.Sprintf("service(%s", jsString(s.Name))
			helpers["service"] = true
		}
		lines := []string{}
		settings := map[string]json.RawMessage{}
		for k, v := range s.Settings {
			settings[k] = v
		}
		str := func(k string) (string, bool) {
			var v string
			if raw, ok := settings[k]; ok && json.Unmarshal(raw, &v) == nil {
				return v, true
			}
			return "", false
		}
		switch s.SourceType {
		case "repo":
			if url, ok := str("repo_url"); ok {
				delete(settings, "repo_url")
				ref, fn := jsString(url), "git"
				if m := githubURLRe.FindStringSubmatch(url); m != nil {
					ref, fn = jsString(m[1]), "github"
				} else if strings.Contains(url, "github.com") {
					fn = "github"
				}
				helpers[fn] = true
				opts := []string{}
				if br, ok := str("branch"); ok {
					opts = append(opts, "branch: "+jsString(br))
					delete(settings, "branch")
				}
				if rd, ok := str("root_dir"); ok {
					opts = append(opts, "rootDirectory: "+jsString(rd))
					delete(settings, "root_dir")
				}
				src := fn + "(" + ref
				if len(opts) > 0 {
					src += ", { " + strings.Join(opts, ", ") + " }"
				}
				lines = append(lines, "source: "+src+"),")
			}
		case "image":
			helpers["image"] = true
			if img, ok := str("image"); ok {
				delete(settings, "image")
				lines = append(lines, "source: image("+jsString(img)+"),")
			} else {
				lines = append(lines, "source: image(),")
			}
		}
		if raw, ok := settings["domains"]; ok {
			if lit, ok := simpleDomains(raw); ok {
				lines = append(lines, "domains: "+lit+",")
			} else {
				lines = append(lines, "domains: "+jsValue(raw, "    ")+",")
			}
			delete(settings, "domains")
		}
		keys := make([]string, 0, len(settings))
		for k := range settings {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		raw := []string{}
		for _, k := range keys {
			if opt, ok := friendly[k]; ok {
				lines = append(lines, opt+": "+jsValue(settings[k], "    ")+",")
			} else {
				raw = append(raw, "  "+jsKey(k)+": "+jsValue(settings[k], "      ")+",")
			}
		}
		if len(raw) > 0 {
			lines = append(lines, "settings: {\n    "+strings.Join(raw, "\n    ")+"\n    },")
		}
		if s.Variables != nil {
			vs := *s.Variables
			vkeys := make([]string, 0, len(vs))
			for k := range vs {
				vkeys = append(vkeys, k)
			}
			sort.Strings(vkeys)
			env := []string{}
			for _, k := range vkeys {
				if vs[k].Preserve {
					helpers["preserve"] = true
					env = append(env, "      "+jsKey(k)+": preserve(),")
				} else {
					env = append(env, "      "+jsKey(k)+": "+jsString(vs[k].Value)+",")
				}
			}
			if len(env) == 0 {
				lines = append(lines, "env: {},")
			} else {
				lines = append(lines, "env: {\n"+strings.Join(env, "\n")+"\n    },")
			}
		}
		if len(lines) == 0 {
			fmt.Fprintf(&body, "  const %s = %s);\n", id, ctor)
		} else {
			fmt.Fprintf(&body, "  const %s = %s, {\n    %s\n  });\n", id, ctor, strings.Join(lines, "\n    "))
		}
	}
	names := make([]string, 0, len(helpers))
	for h := range helpers {
		names = append(names, h)
	}
	sort.Strings(names)
	b.WriteString("// Aven Infrastructure as Code for this project (aven config plan / aven config apply).\n")
	b.WriteString("// Omitted settings are left untouched; `env` lists every variable of a service (omit it to leave variables alone).\n")
	fmt.Fprintf(&b, "import { %s } from %s;\n\n", strings.Join(names, ", "), jsString(DSLModule))
	if d.Partial != "" {
		fmt.Fprintf(&b, "export const partial = %s;\n\n", jsString(d.Partial))
	}
	b.WriteString("export default defineAven(() => {\n")
	b.WriteString(body.String())
	if len(d.Services) > 0 {
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "  return project(%s, {\n    resources: [%s],\n  });\n});\n", jsString(d.ProjectName), strings.Join(idents, ", "))
	return []byte(b.String())
}
