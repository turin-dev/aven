package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
)

// ---- templates ----

type templateInputDef struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Default     string `json:"default"`
	Required    bool   `json:"required"`
	Secret      bool   `json:"secret"`
	Pattern     string `json:"pattern"`
}

type templateInfo struct {
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	Tags        []string `json:"tags"`
	Builtin     bool     `json:"builtin"`
	Visibility  string   `json:"visibility"`
	DeployCount int      `json:"deploy_count"`
	Readme      string   `json:"readme"`
	Services    []struct {
		Name         string  `json:"name"`
		SourceType   string  `json:"source_type"`
		DatabaseKind *string `json:"database_kind"`
		Image        *string `json:"image"`
		RepoURL      *string `json:"repo_url"`
	} `json:"services"`
	Inputs []templateInputDef `json:"inputs"`
}

type namedSvc struct {
	Name string `json:"name"`
}

// kvList는 반복 가능한 --input K=V 플래그다.
type kvList map[string]string

func (k kvList) String() string { return "" }
func (k kvList) Set(s string) error {
	key, val, ok := strings.Cut(s, "=")
	if !ok || key == "" {
		return errors.New("expected KEY=VALUE")
	}
	k[key] = val
	return nil
}

// splitKey는 첫 번째 위치 인자(템플릿 키나 파일)를 플래그와 떼어 낸다(플래그가 앞뒤 어디에 와도 된다).
func splitKey(fl *flag.FlagSet, args []string) string {
	var pos string
	rest := args
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		pos, rest = rest[0], rest[1:]
	}
	fl.Parse(rest)
	if pos == "" && fl.NArg() > 0 {
		pos = fl.Arg(0)
	}
	return pos
}

// cmdTemplates: aven templates [list|show <key>|deploy <key> [--input K=V ...]|publish <file.json> [--visibility v]|export <key>|export-env [--secrets inputs]]
func cmdTemplates(c *config, args []string) {
	sub := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "list", "ls":
		fl := flag.NewFlagSet("templates list", flag.ExitOnError)
		q := fl.String("q", "", "search text")
		cat := fl.String("category", "", "category")
		tag := fl.String("tag", "", "tag")
		sortBy := fl.String("sort", "", "popular | new")
		fl.Parse(args)
		v := url.Values{}
		for k, s := range map[string]string{"q": *q, "category": *cat, "tag": *tag, "sort": *sortBy} {
			if s != "" {
				v.Set(k, s)
			}
		}
		path := "/templates"
		if len(v) > 0 {
			path += "?" + v.Encode()
		}
		var ts []templateInfo
		if err := c.call("GET", path, nil, &ts); err != nil {
			die("%v", err)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "KEY\tNAME\tCATEGORY\tSERVICES\tDEPLOYS\tVISIBILITY")
		for _, t := range ts {
			vis := t.Visibility
			if t.Builtin {
				vis = "built-in"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\n", t.Key, t.Name, t.Category, len(t.Services), t.DeployCount, vis)
		}
		tw.Flush()
	case "show", "info":
		if len(args) != 1 {
			die("usage: aven templates show <key>")
		}
		var t templateInfo
		if err := c.call("GET", "/templates/"+url.PathEscape(args[0]), nil, &t); err != nil {
			die("%v", err)
		}
		fmt.Printf("%s (%s)\n", t.Name, t.Key)
		if t.Description != "" {
			fmt.Println(t.Description)
		}
		fmt.Println("\nservices:")
		for _, s := range t.Services {
			src := s.SourceType
			switch {
			case s.DatabaseKind != nil:
				src = "database " + *s.DatabaseKind
			case s.Image != nil:
				src = "image " + *s.Image
			case s.RepoURL != nil:
				src = "repo " + *s.RepoURL
			}
			fmt.Printf("  %-20s %s\n", s.Name, src)
		}
		if len(t.Inputs) > 0 {
			fmt.Println("\ninputs:")
			for _, in := range t.Inputs {
				flags := []string{}
				if in.Required {
					flags = append(flags, "required")
				}
				if in.Secret {
					flags = append(flags, "secret")
				}
				if in.Default != "" && !in.Secret {
					flags = append(flags, "default "+in.Default)
				}
				label := in.Label
				if label == "" {
					label = in.Description
				}
				fmt.Printf("  %-20s %s [%s]\n", in.Key, label, strings.Join(flags, ", "))
			}
		}
		if t.Readme != "" {
			fmt.Println("\n" + t.Readme)
		}
	case "deploy":
		fl := flag.NewFlagSet("templates deploy", flag.ExitOnError)
		inputs := kvList{}
		fl.Var(inputs, "input", "template input KEY=VALUE (repeatable)")
		tf := addTargetFlags(fl)
		key := splitKey(fl, args)
		if key == "" {
			die("usage: aven templates deploy <key> [--input K=V ...] [-e env]")
		}
		l := tf.resolve(c)
		var svcs []namedSvc
		body := map[string]any{"environment_id": l.EnvironmentID, "inputs": map[string]string(inputs)}
		if err := c.call("POST", "/projects/"+url.PathEscape(l.ProjectID)+"/templates/"+url.PathEscape(key), body, &svcs); err != nil {
			die("%v", err)
		}
		fmt.Printf("deploying template %s to %s: %s\n", key, l.Environment, names(svcs, func(s namedSvc) string { return s.Name }))
	case "publish":
		fl := flag.NewFlagSet("templates publish", flag.ExitOnError)
		vis := fl.String("visibility", "", "private (default) | workspace | public")
		key := fl.String("key", "", "template key (default: the definition's key or name)")
		workspace := fl.String("workspace", "", "workspace ID (default: server default workspace)")
		file := splitKey(fl, args)
		if file == "" {
			die("usage: aven templates publish <file.json> [--workspace id] [--visibility private|workspace|public] [--key k]")
		}
		body, err := publishBody(file, *vis, *key)
		if err != nil {
			die("%v", err)
		}
		if *workspace != "" {
			body["workspace_id"] = *workspace
		}
		var t templateInfo
		if err := c.call("POST", "/templates", body, &t); err != nil {
			die("%v", err)
		}
		fmt.Printf("published template %s (%s, %s)\n", t.Key, t.Name, t.Visibility)
	case "export":
		if len(args) != 1 {
			die("usage: aven templates export <key> > file.json")
		}
		resp, err := c.req("GET", "/templates/"+url.PathEscape(args[0])+"/export", nil, "")
		if err != nil {
			die("%v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			die("export: %d", resp.StatusCode)
		}
		io.Copy(os.Stdout, resp.Body)
	case "export-env":
		fl := flag.NewFlagSet("templates export-env", flag.ExitOnError)
		secrets := fl.String("secrets", "generate", "generate (replace secrets with ${{ secret(32) }}) | inputs (turn them into required inputs)")
		tf := addTargetFlags(fl)
		fl.Parse(args)
		l := tf.resolve(c)
		var out struct {
			Definition json.RawMessage `json:"definition"`
			Warnings   []string        `json:"warnings"`
		}
		path := "/projects/" + url.PathEscape(l.ProjectID) + "/environments/" + url.PathEscape(l.EnvironmentID) + "/export-template?secrets=" + url.QueryEscape(*secrets)
		if err := c.call("POST", path, nil, &out); err != nil {
			die("%v", err)
		}
		for _, w := range out.Warnings {
			fmt.Fprintln(os.Stderr, "warning:", w)
		}
		var buf bytes.Buffer
		json.Indent(&buf, out.Definition, "", "  ")
		buf.WriteByte('\n')
		os.Stdout.Write(buf.Bytes())
	default:
		die("usage: aven templates [list|show <key>|deploy <key> [--input K=V ...]|publish <file.json> [--visibility v]|export <key>|export-env [--secrets inputs]]")
	}
}

// publishBody는 정의 파일을 POST /api/templates 본문으로 만든다. 파일이 이미 {definition: ...} 형태여도 된다.
func publishBody(file, vis, key string) (map[string]any, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(b, &probe); err != nil {
		return nil, fmt.Errorf("%s: invalid json: %v", file, err)
	}
	body := map[string]any{}
	if def, ok := probe["definition"]; ok {
		for k, v := range probe {
			if k != "warnings" { // export-env 응답을 그대로 저장한 파일
				body[k] = v
			}
		}
		body["definition"] = def
	} else {
		body["definition"] = json.RawMessage(b)
	}
	if vis != "" {
		body["visibility"] = vis
	}
	if key != "" {
		body["key"] = key
	}
	return body, nil
}
