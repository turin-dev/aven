package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/turin-dev/aven/internal/compat"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/coder/websocket"
)

var shellSafeRe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellJoin은 argv를 sh -lc에 넘길 한 줄로 만든다(필요한 인자만 작은따옴표로 감싼다).
func shellJoin(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if shellSafeRe.MatchString(a) {
			out[i] = a
		} else {
			out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(out, " ")
}

// execURL은 서비스 셸 WebSocket 주소다(http→ws, https→wss).
func execURL(base string, l *link, replica string, cmd []string, cols, rows int) (string, error) {
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
	u.Path += "/api" + l.instPath() + "/exec"
	q := url.Values{}
	if replica != "" {
		q.Set("replica", replica)
	}
	if len(cmd) > 0 {
		q.Set("cmd", shellJoin(cmd))
	}
	if cols > 0 && rows > 0 {
		q.Set("cols", fmt.Sprint(cols))
		q.Set("rows", fmt.Sprint(rows))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (c *config) authHeader() http.Header {
	h := http.Header{}
	if t := compat.Env("TOKEN"); t != "" {
		h.Set("Authorization", "Bearer "+t)
	} else if c.Cookie != "" {
		h.Set("Cookie", (&http.Cookie{Name: "aven_session", Value: c.Cookie}).String())
	}
	return h
}

// execFrame은 server → client 제어 메시지다.
type execFrame struct {
	Type    string `json:"type"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// resizeFrame은 client → server 크기 변경 메시지다.
func resizeFrame(cols, rows int) []byte {
	b, _ := json.Marshal(map[string]any{"type": "resize", "cols": cols, "rows": rows})
	return b
}

// sshPump는 in → binary frame, binary frame → out, resize → text frame을 잇고 원격 종료 코드를 돌려준다.
// 서버가 exit 없이 끊으면 1이다.
func sshPump(ctx context.Context, conn *websocket.Conn, in io.Reader, out, errOut io.Writer, resize <-chan [2]int) int {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				if conn.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
					return
				}
			}
			if err != nil {
				return // stdin이 끝나도 출력은 계속 받는다
			}
		}
	}()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case s, ok := <-resize:
				if !ok {
					return
				}
				conn.Write(ctx, websocket.MessageText, resizeFrame(s[0], s[1]))
			}
		}
	}()
	code, gotExit := 1, false
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			if !gotExit {
				if st := websocket.CloseStatus(err); st != websocket.StatusNormalClosure {
					var ce websocket.CloseError
					if errors.As(err, &ce) && ce.Reason != "" {
						fmt.Fprintf(errOut, "\r\nkeel: connection closed: %s\r\n", ce.Reason)
					} else {
						fmt.Fprintf(errOut, "\r\nkeel: connection closed: %v\r\n", err)
					}
				}
			}
			return code
		}
		if typ == websocket.MessageBinary {
			out.Write(data)
			continue
		}
		var f execFrame
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		switch f.Type {
		case "exit":
			code, gotExit = f.Code, true
			if code < 0 {
				code = 1
			}
		case "error":
			fmt.Fprintf(errOut, "\r\nkeel: %s\r\n", f.Message)
		}
	}
}

func cmdSSH(c *config, args []string) {
	fl := flag.NewFlagSet("ssh", flag.ExitOnError)
	replica := fl.String("replica", "", "replica (container) name; default: the first one")
	fl.StringVar(replica, "r", "", "replica (container) name")
	tf := addTargetFlags(fl)
	fl.Parse(args)
	l := tf.service(c)
	if c.URL == "" {
		die("not logged in (run: aven login <url>)")
	}
	tty := isTerminal(os.Stdin) && isTerminal(os.Stdout)
	cols, rows := 80, 24
	if w, h, ok := termSize(os.Stdout); ok {
		cols, rows = w, h
	}
	u, err := execURL(c.URL, l, *replica, fl.Args(), cols, rows)
	if err != nil {
		die("%v", err)
	}
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
	if tty {
		if r, err := makeRaw(os.Stdin); err == nil {
			restore = r
		} else {
			fmt.Fprintf(os.Stderr, "aven: raw mode unavailable (%v); using line mode\n", err)
		}
	} else {
		fmt.Fprintln(os.Stderr, "aven: stdin is not a terminal; using line mode")
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
	os.Exit(code)
}
