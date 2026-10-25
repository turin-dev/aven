package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"
)

// aven agent                          대화형 세션
// aven agent -p "프롬프트"             한 번 묻고 끝낸다(--json: 결과를 JSON으로)
// aven agent --list                   이전 대화 목록(연결된 프로젝트)
// aven agent --thread-id <id>         이전 대화를 이어 간다(최근 기록을 보여 준다)
// -s/--service, -e/--environment      대화 대상(기본: 연결 정보), -y: 바꾸는 작업을 묻지 않고 승인
type agentOpts struct {
	prompt   string
	json     bool
	list     bool
	threadID string
	svc, env string
	yes      bool
}

func parseAgentFlags(args []string) (*agentOpts, error) {
	o := &agentOpts{}
	fl := flag.NewFlagSet("agent", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	for _, n := range []string{"p", "prompt"} {
		fl.StringVar(&o.prompt, n, "", "send a single prompt")
	}
	fl.BoolVar(&o.json, "json", false, "print the response as JSON")
	fl.BoolVar(&o.list, "list", false, "list agent threads")
	fl.StringVar(&o.threadID, "thread-id", "", "continue an existing thread")
	for _, n := range []string{"s", "service"} {
		fl.StringVar(&o.svc, n, "", "service name or id")
	}
	for _, n := range []string{"e", "environment"} {
		fl.StringVar(&o.env, n, "", "environment name or id")
	}
	for _, n := range []string{"y", "yes"} {
		fl.BoolVar(&o.yes, n, false, "approve actions without asking")
	}
	if err := fl.Parse(args); err != nil {
		return nil, err
	}
	if rest := fl.Args(); len(rest) > 0 {
		if o.prompt != "" {
			return nil, errors.New("usage: aven agent [-p prompt] [--json] [--list] [--thread-id id] [-s service] [-e env] [-y]")
		}
		o.prompt = strings.Join(rest, " ")
	}
	if o.list && (o.prompt != "" || o.threadID != "") {
		return nil, errors.New("--list cannot be combined with a prompt or --thread-id")
	}
	if o.json && o.prompt == "" && !o.list {
		return nil, errors.New("--json needs -p <prompt> or --list")
	}
	return o, nil
}

// linkedOpt는 연결 정보가 있으면 돌려준다(aven agent는 연결 없이도 쓸 수 있다).
func (c *config) linkedOpt() *link {
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

type agentThreadInfo struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Title     string    `json:"title"`
	UpdatedAt time.Time `json:"updated_at"`
}

type agentToolCall struct {
	ToolName string `json:"toolName"`
	Args     any    `json:"args"`
	Result   any    `json:"result,omitempty"`
	IsError  bool   `json:"isError"`
}

// agentTurn은 한 번의 요청(chat 또는 confirm)에서 받은 결과다.
type agentTurn struct {
	ThreadID string
	Response string
	Tools    []agentToolCall
	Pending  *agentPending
	Err      string
}

type agentPending struct {
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments any    `json:"arguments"`
	Summary   string `json:"summary"`
}

// stream은 POST 요청의 SSE 응답을 읽는다. token은 out에 바로 쓴다(nil이면 쓰지 않는다).
func (c *config) agentStream(path string, body any, out io.Writer) (*agentTurn, error) {
	b, _ := json.Marshal(body)
	resp, err := c.req("POST", path, bytes.NewReader(b), "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		if resp.StatusCode == 401 {
			return nil, errors.New("unauthorized (run: aven login <url>)")
		}
		return nil, fmt.Errorf("%d %s", resp.StatusCode, e.Error)
	}
	t := &agentTurn{}
	var text strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	event := ""
	for sc.Scan() {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "event: "):
			event = l[7:]
		case strings.HasPrefix(l, "data: "):
			data := []byte(l[6:])
			switch event {
			case "thread":
				var v struct {
					ThreadID string `json:"thread_id"`
				}
				json.Unmarshal(data, &v)
				t.ThreadID = v.ThreadID
			case "token":
				var v struct {
					Text string `json:"text"`
				}
				json.Unmarshal(data, &v)
				text.WriteString(v.Text)
				if out != nil {
					fmt.Fprint(out, v.Text)
				}
			case "tool":
				var v struct {
					Name      string `json:"name"`
					Arguments any    `json:"arguments"`
					Result    any    `json:"result"`
					IsError   bool   `json:"is_error"`
					Declined  bool   `json:"declined"`
				}
				json.Unmarshal(data, &v)
				t.Tools = append(t.Tools, agentToolCall{ToolName: v.Name, Args: v.Arguments, Result: v.Result, IsError: v.IsError})
				if out != nil {
					status := ""
					switch {
					case v.Declined:
						status = " (거절됨)"
					case v.IsError:
						status = " (실패)"
					}
					fmt.Fprintf(out, "\n  ↳ %s%s\n", v.Name, status)
				}
			case "confirmation_required":
				var p agentPending
				json.Unmarshal(data, &p)
				t.Pending = &p
			case "done":
				var v struct {
					Response string `json:"response"`
				}
				json.Unmarshal(data, &v)
				t.Response = v.Response
			case "error":
				var v struct {
					Message string `json:"message"`
				}
				json.Unmarshal(data, &v)
				t.Err = v.Message
			}
		}
	}
	if err := sc.Err(); err != nil {
		return t, err
	}
	if t.Response == "" {
		t.Response = text.String()
	}
	return t, nil
}

// agentSession은 한 사용자의 대화다(대화형/한 번 모두).
type agentSession struct {
	c        *config
	threadID string
	ctx      map[string]string // project_id, environment_id, service_id
	yes      bool
	in       *bufio.Reader
	out      io.Writer // nil이면 스트리밍 출력 없음(--json)
	tools    []agentToolCall
}

// send는 프롬프트를 보내고, 확인이 필요한 작업이 나오면 승인 여부를 물어 이어 간다. 마지막 응답을 돌려준다.
func (s *agentSession) send(prompt string) (string, error) {
	body := map[string]any{"prompt": prompt, "thread_id": s.threadID}
	for k, v := range s.ctx {
		body[k] = v
	}
	t, err := s.c.agentStream("/ai/chat", body, s.out)
	return s.follow(t, err)
}

// follow는 확인이 필요한 작업마다 승인 여부를 물어 대화를 끝까지 이어 간다.
func (s *agentSession) follow(t *agentTurn, err error) (string, error) {
	for {
		if err != nil {
			return "", err
		}
		if t.ThreadID != "" {
			s.threadID = t.ThreadID
		}
		s.tools = append(s.tools, t.Tools...)
		if t.Err != "" {
			return t.Response, errors.New(t.Err)
		}
		if t.Pending == nil {
			return t.Response, nil
		}
		approve := s.approve(t.Pending)
		t, err = s.c.agentStream("/ai/confirm", map[string]any{"thread_id": s.threadID, "call_id": t.Pending.CallID, "approve": approve}, s.out)
	}
}

func (s *agentSession) approve(p *agentPending) bool {
	summary := p.Summary
	if summary == "" {
		summary = p.Name
	}
	w := s.out
	if w == nil {
		w = os.Stderr
	}
	fmt.Fprintf(w, "\n\n⚠ 확인 필요: %s\n", summary)
	if args, err := json.Marshal(p.Arguments); err == nil && string(args) != "{}" {
		fmt.Fprintf(w, "  %s %s\n", p.Name, args)
	}
	if s.yes {
		fmt.Fprintln(w, "  승인했습니다 (-y)")
		return true
	}
	if st, err := os.Stdin.Stat(); err != nil || st.Mode()&os.ModeCharDevice == 0 {
		fmt.Fprintln(w, "  대화형 터미널이 아니라서 거절했습니다 (승인하려면 -y)")
		return false
	}
	fmt.Fprint(w, "  실행할까요? [y/N] ")
	line, _ := s.in.ReadString('\n')
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes"
}

func cmdAgent(c *config, args []string) {
	o, err := parseAgentFlags(args)
	if err != nil {
		die("%v", err)
	}
	ctx := map[string]string{}
	if l := c.linkedOpt(); l != nil {
		r := (&targetFlags{svc: &o.svc, env: &o.env}).resolve(c)
		ctx["project_id"], ctx["environment_id"] = r.ProjectID, r.EnvironmentID
		if r.ServiceID != "" {
			ctx["service_id"] = r.ServiceID
		}
	} else if o.svc != "" || o.env != "" {
		die("this directory is not linked (run: aven link) — -s/-e need a linked project")
	}

	if o.list {
		path := "/ai/threads"
		if pid := ctx["project_id"]; pid != "" {
			path += "?project_id=" + url.QueryEscape(pid)
		}
		var threads []agentThreadInfo
		if err := c.call("GET", path, nil, &threads); err != nil {
			die("%v", err)
		}
		if o.json {
			b, _ := json.MarshalIndent(threads, "", "  ")
			fmt.Println(string(b))
			return
		}
		if len(threads) == 0 {
			fmt.Println("no agent threads")
			return
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tUPDATED\tTITLE")
		for _, t := range threads {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", t.ID, ago(t.UpdatedAt), firstLine(t.Title))
		}
		tw.Flush()
		return
	}

	s := &agentSession{c: c, threadID: o.threadID, ctx: ctx, yes: o.yes, in: bufio.NewReader(os.Stdin), out: os.Stdout}
	if o.prompt != "" {
		if o.json {
			s.out = nil
		}
		resp, err := s.send(o.prompt)
		if o.json {
			out := map[string]any{"threadId": s.threadID, "response": resp, "toolCalls": s.tools}
			if s.tools == nil {
				out["toolCalls"] = []agentToolCall{}
			}
			if err != nil {
				out["error"] = err.Error()
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(b))
			if err != nil {
				os.Exit(1)
			}
			return
		}
		fmt.Println()
		if err != nil {
			die("%v", err)
		}
		return
	}

	fmt.Println("Aven Agent (type 'exit' or Ctrl+C to quit)")
	if o.threadID != "" {
		if p := replayAgentThread(c, o.threadID, os.Stdout); p != nil {
			fmt.Print("\nKeel Agent: ")
			t, err := c.agentStream("/ai/confirm", map[string]any{"thread_id": o.threadID, "call_id": p.CallID, "approve": s.approve(p)}, s.out)
			if _, err := s.follow(t, err); err != nil {
				fmt.Fprintf(os.Stderr, "\nkeel: %v\n", err)
			}
			fmt.Println()
		}
	}
	for {
		fmt.Print("\n> You: ")
		line, err := s.in.ReadString('\n')
		prompt := strings.TrimSpace(line)
		if prompt == "exit" || prompt == "quit" || (err != nil && prompt == "") {
			fmt.Println()
			return
		}
		if prompt == "" {
			continue
		}
		fmt.Print("\nKeel Agent: ")
		if _, err := s.send(prompt); err != nil {
			fmt.Fprintf(os.Stderr, "\nkeel: %v\n", err)
		}
		fmt.Println()
	}
}

// replayAgentThread는 이어 갈 대화의 최근 메시지를 보여 주고, 대기 중인 작업이 있으면 돌려준다.
func replayAgentThread(c *config, id string, w io.Writer) *agentPending {
	var t struct {
		Title    string `json:"title"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
			Tool    string `json:"tool"`
		} `json:"messages"`
		PendingAction *agentPending `json:"pending_action"`
	}
	if err := c.call("GET", "/ai/threads/"+url.PathEscape(id), nil, &t); err != nil {
		die("%v", err)
	}
	msgs := t.Messages
	if len(msgs) > 10 {
		msgs = msgs[len(msgs)-10:]
	}
	for _, m := range msgs {
		switch m.Role {
		case "user":
			fmt.Fprintf(w, "\n> You: %s\n", m.Content)
		case "assistant":
			fmt.Fprintf(w, "\nKeel Agent: %s\n", m.Content)
		case "tool":
			fmt.Fprintf(w, "  ↳ %s\n", m.Tool)
		}
	}
	return t.PendingAction
}
