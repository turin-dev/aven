package deploy

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

func TestTruncateLine(t *testing.T) {
	if got := truncateLine("short"); got != "short" {
		t.Fatalf("short line changed: %q", got)
	}
	long := strings.Repeat("가", maxLogLine) // 3바이트 문자: 경계가 문자 중간에 걸린다
	got := truncateLine(long)
	if !utf8.ValidString(got) || !strings.Contains(got, "bytes truncated") || len(got) > maxLogLine+64 {
		t.Fatalf("truncated line: len=%d valid=%v", len(got), utf8.ValidString(got))
	}
}

func TestLineLimiterKeepsHeadAndTail(t *testing.T) {
	cases := []struct {
		name       string
		head, tail int
		n          int
		want       []string
	}{
		{"under limit", 3, 2, 4, []string{"0", "1", "2", "3"}},
		{"exactly full", 2, 2, 4, []string{"0", "1", "2", "3"}},
		{"middle omitted", 2, 2, 7, []string{"0", "1", "omitted 3", "5", "6"}},
		{"no tail", 2, 0, 5, []string{"0", "1", "omitted 3"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			l := newLineLimiter(func(s string) { got = append(got, s) }, func(n int) string { return fmt.Sprintf("omitted %d", n) }, c.head, c.tail)
			for i := 0; i < c.n; i++ {
				l.Add(fmt.Sprint(i))
			}
			l.Flush()
			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestLimitedLogConcurrent(t *testing.T) {
	var mu sync.Mutex
	n := 0
	line, flush := limitedLog(func(string) { mu.Lock(); n++; mu.Unlock() })
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < buildLogHead; i++ {
				line("x")
			}
		}()
	}
	wg.Wait()
	flush()
	if want := buildLogHead + 1 + buildLogTail; n != want {
		t.Fatalf("lines written %d, want %d", n, want)
	}
}

// floodDriver는 cron 실행에서 출력을 많이 내는 로컬 드라이버다.
type floodDriver struct {
	*localFake
	lines int
}

func (f *floodDriver) RunToCompletion(_ context.Context, _ node.RunSpec, out func(stream, line string)) (int, error) {
	for i := 0; i < f.lines; i++ {
		out("stdout", fmt.Sprintf("line %d", i))
	}
	out("stderr", strings.Repeat("y", 3*maxLogLine))
	return 3, nil
}

// cron 실행 로그는 앞·뒤만 남기고 긴 줄은 자른다(끝의 오류 출력은 남는다).
func TestCronRunLogIsBounded(t *testing.T) {
	ctx := context.Background()
	f := newScaleFixture(t)
	d := f.live(t)
	f.m.localDrv = &floodDriver{localFake: &localFake{n: f.local}, lines: cronLogHead + cronLogTail + 500}
	tg, err := f.m.Load(ctx, f.in.ID)
	if err != nil {
		t.Fatal(err)
	}
	run := &store.CronRun{ID: store.NewID("run"), DeploymentID: d.ID, InstanceID: f.in.ID, Container: "keel-cron-web-test"}
	if err := f.st.CreateCronRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	f.m.execCron(tg, d, run)
	logs := f.m.CronRunLogs(run.ID)
	if len(logs) != cronLogHead+1+cronLogTail {
		t.Fatalf("entries %d", len(logs))
	}
	if logs[0].Msg != "line 0" || !strings.Contains(logs[cronLogHead].Msg, "생략") {
		t.Fatalf("head/marker: %q %q", logs[0].Msg, logs[cronLogHead].Msg)
	}
	last := logs[len(logs)-1]
	if last.Stream != "stderr" || len(last.Msg) > maxLogLine+64 {
		t.Fatalf("last entry: stream=%s len=%d", last.Stream, len(last.Msg))
	}
	if run.Status != "failed" || run.ExitCode == nil || *run.ExitCode != 3 {
		t.Fatalf("run %s %v", run.Status, run.ExitCode)
	}
}
