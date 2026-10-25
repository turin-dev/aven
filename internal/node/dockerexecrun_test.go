package node

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func frame(stream byte, data string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(data)))
	return append(h, data...)
}

func TestDemuxExec(t *testing.T) {
	var b bytes.Buffer
	b.Write(frame(1, "hello "))
	b.Write(frame(2, "warn\n"))
	b.Write(frame(1, "world"))
	out, errb, trunc, err := DemuxExec(&b, 100, 100)
	if err != nil || trunc || string(out) != "hello world" || string(errb) != "warn\n" {
		t.Fatalf("%q %q %v %v", out, errb, trunc, err)
	}
	// stdout 상한: 거기서 멈추고 truncated
	b.Reset()
	b.Write(frame(1, "abcdef"))
	b.Write(frame(1, "ghij"))
	out, _, trunc, err = DemuxExec(&b, 8, 100)
	if err != nil || !trunc || string(out) != "abcdefgh" {
		t.Errorf("cap: %q %v %v", out, trunc, err)
	}
	// stderr는 상한까지만 남기고 계속 읽는다
	b.Reset()
	b.Write(frame(2, strings.Repeat("e", 10)))
	b.Write(frame(1, "ok"))
	out, errb, trunc, err = DemuxExec(&b, 100, 4)
	if err != nil || trunc || string(out) != "ok" || string(errb) != "eeee" {
		t.Errorf("stderr cap: %q %q %v %v", out, errb, trunc, err)
	}
	// 헤더 중간에서 끊긴 스트림은 오류
	if _, _, _, err := DemuxExec(bytes.NewReader(frame(1, "x")[:5]), 10, 10); err == nil {
		t.Error("short header accepted")
	}
}

func TestValidateExecEnv(t *testing.T) {
	if err := ValidateExecEnv([]string{"PGPASSWORD=x=y", "A_1=", "KEEL_NULL=\x01N\x01"}); err != nil {
		t.Errorf("valid env rejected: %v", err)
	}
	for _, bad := range [][]string{{"NOEQ"}, {"1A=x"}, {"A-B=x"}, {"A=x\x00"}, {"=x"}, {strings.Repeat("K", 129) + "=x"}} {
		if err := ValidateExecEnv(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	many := make([]string, 33)
	for i := range many {
		many[i] = "A=1"
	}
	if ValidateExecEnv(many) == nil {
		t.Error("too many entries accepted")
	}
	if ValidateExecEnv([]string{"A=" + strings.Repeat("x", 70<<10)}) == nil {
		t.Error("huge env accepted")
	}
	if ClampExecOutput(0) != 1<<20 || ClampExecOutput(10<<20) != MaxExecRunOutput || ClampExecOutput(5) != 5 {
		t.Error("ClampExecOutput")
	}
}

// 연결 deadline이 ctx보다 먼저 걸리면 읽기는 i/o timeout으로 끝나고 ctx.Err()는 아직 nil이다(실제 docker에서 502로 새던 경우).
func TestExecTimedOutConnDeadline(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	a.SetDeadline(time.Now().Add(10 * time.Millisecond))
	_, _, _, rerr := DemuxExec(a, 100, 100)
	if rerr == nil {
		t.Fatal("expected read error")
	}
	if !execTimedOut(context.Background(), rerr) {
		t.Fatalf("conn deadline error not treated as timeout: %v", rerr)
	}
	if execTimedOut(context.Background(), io.ErrUnexpectedEOF) || execTimedOut(context.Background(), nil) {
		t.Fatal("non-timeout errors must not be timeouts")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !execTimedOut(ctx, nil) {
		t.Fatal("done ctx must be a timeout")
	}
}
