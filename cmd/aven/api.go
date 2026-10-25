package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

const apiUsage = "usage: aven api [-F key=value]... [--variables JSON|@file] [--operation-name name] [--raw] <query | @file | ->"

// varFlags는 반복해서 받는 -F key=value 변수다.
type varFlags []string

func (v *varFlags) String() string     { return strings.Join(*v, ",") }
func (v *varFlags) Set(s string) error { *v = append(*v, s); return nil }

// readArg는 "@path"면 파일 내용, "-"면 stdin, 아니면 값 그대로다.
func readArg(v string, stdin io.Reader) (string, error) {
	var r io.Reader
	switch {
	case v == "-":
		r = stdin
	case strings.HasPrefix(v, "@"):
		f, err := os.Open(v[1:])
		if err != nil {
			return "", err
		}
		defer f.Close()
		r = f
	default:
		return v, nil
	}
	data, err := io.ReadAll(io.LimitReader(r, 1<<20+1))
	if err != nil {
		return "", err
	}
	if len(data) > 1<<20 {
		return "", errors.New("input exceeds 1 MiB")
	}
	return string(data), nil
}

// fieldValue는 -F 값의 타입을 gh api처럼 추론한다: true/false/null/숫자는 JSON 값, 나머지는 문자열.
func fieldValue(s string) any {
	switch s {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}

// runAPI는 aven api: GraphQL 문서를 POST /graphql/v2로 보내고 응답 JSON을 출력한다.
// GraphQL 오류가 있으면 응답을 출력한 뒤 오류로 끝난다(스크립트에서 종료 코드로 알 수 있게).
func runAPI(c *config, args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("api", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	queryFlag := fs.String("query", "", "GraphQL document")
	fs.StringVar(queryFlag, "q", "", "GraphQL document")
	variablesFlag := fs.String("variables", "", "variables as a JSON object (or @file)")
	operationName := fs.String("operation-name", "", "operation to run")
	raw := fs.Bool("raw", false, "print the response without indentation")
	var fields varFlags
	fs.Var(&fields, "F", "variable key=value (repeatable)")
	fs.Var(&fields, "field", "variable key=value (repeatable)")
	if err := fs.Parse(interspersed(fs, args)); err != nil {
		return fmt.Errorf("%v\n%s", err, apiUsage)
	}
	query := *queryFlag
	if query == "" {
		switch rest := fs.Args(); {
		case len(rest) == 0:
			query = "-" // 파이프로 넘긴 쿼리
		default:
			query = strings.Join(rest, " ")
		}
	}
	query, err := readArg(query, stdin)
	if err != nil {
		return fmt.Errorf("read GraphQL query: %w", err)
	}
	if strings.TrimSpace(query) == "" {
		return errors.New(apiUsage)
	}

	variables := map[string]any{}
	if v := strings.TrimSpace(*variablesFlag); v != "" {
		text, err := readArg(v, stdin)
		if err != nil {
			return fmt.Errorf("read --variables: %w", err)
		}
		if err := json.Unmarshal([]byte(text), &variables); err != nil {
			return fmt.Errorf("invalid --variables JSON: %w", err)
		}
	}
	for _, f := range fields {
		k, v, ok := strings.Cut(f, "=")
		if !ok || k == "" {
			return fmt.Errorf("-F %q: want key=value", f)
		}
		variables[k] = fieldValue(v)
	}

	if c.URL == "" {
		return errors.New("not logged in (run: aven login <url>)")
	}
	payload, err := json.Marshal(map[string]any{"query": query, "operationName": *operationName, "variables": variables})
	if err != nil {
		return err
	}
	r, err := http.NewRequest(http.MethodPost, strings.TrimRight(c.URL, "/")+"/graphql/v2", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	if t := os.Getenv("KEEL_TOKEN"); t != "" {
		r.Header.Set("Authorization", "Bearer "+t)
	} else if c.Cookie != "" {
		r.AddCookie(&http.Cookie{Name: "keel_session", Value: c.Cookie})
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return errors.New("unauthorized (run: aven login <url>)")
	}
	out := body
	if !*raw {
		var pretty bytes.Buffer
		if json.Indent(&pretty, body, "", "  ") == nil {
			out = pretty.Bytes()
		}
	}
	fmt.Fprintln(stdout, strings.TrimRight(string(out), "\n"))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("GraphQL request: HTTP %d", resp.StatusCode)
	}
	var result struct {
		Errors []json.RawMessage `json:"errors"`
	}
	if json.Unmarshal(body, &result) == nil && len(result.Errors) > 0 {
		return errors.New("GraphQL request returned errors")
	}
	return nil
}

// interspersed는 플래그를 위치 인자 앞으로 모은다(flag 패키지는 첫 위치 인자에서 멈춘다).
// 쿼리 문자열이 '-'로 시작하지 않으므로 값을 받는 플래그만 알면 된다.
func interspersed(fs *flag.FlagSet, args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if a == "-" || !strings.HasPrefix(a, "-") {
			positional = append(positional, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		if f := fs.Lookup(name); f != nil {
			if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
				continue
			}
			if i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		}
	}
	return append(flags, positional...)
}

func cmdAPI(c *config, args []string) {
	if err := runAPI(c, args, os.Stdin, os.Stdout); err != nil {
		die("%v", err)
	}
}
