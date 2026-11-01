package logbook

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSubscribeReplayAndDone(t *testing.T) {
	r, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := r.Open("dep_1")
	b.Line("one")
	past, ch, cancel := r.Subscribe("dep_1")
	defer cancel()
	if len(past) != 1 || past[0] != "one" {
		t.Fatalf("past = %v", past)
	}
	b.Line("two")
	b.Close("live")
	var got []Event
	for e := range ch {
		got = append(got, e)
	}
	if len(got) != 2 || got[0].Data != "two" || got[1].Kind != "done" || got[1].Data != "live" {
		t.Fatalf("events = %+v", got)
	}
	r.Release("dep_1")
	past, ch, _ = r.Subscribe("dep_1")
	if ch != nil || len(past) != 2 {
		t.Fatalf("after release: past=%v ch=%v", past, ch)
	}
}

// 같은 id를 다시 열면(재배포 없는 replica 변경, self-host 단계) 새 구독자는 파일에 있던 예전 줄부터 받는다.
func TestReopenReplaysFileLines(t *testing.T) {
	r, _ := NewRegistry(t.TempDir())
	b, _ := r.Open("dep_1")
	b.Line("build 1")
	b.Line("build 2")
	b.Close("live")
	r.Release("dep_1")

	b2, _ := r.Open("dep_1")
	b2.Line("scale")
	past, ch, cancel := r.Subscribe("dep_1")
	defer cancel()
	if want := []string{"build 1", "build 2", "scale"}; !slices.Equal(past, want) {
		t.Fatalf("past = %v, want %v", past, want)
	}
	b2.Line("after")
	if e := <-ch; e.Data != "after" {
		t.Fatalf("event = %+v", e)
	}
	b2.Close("live")
}

func TestInvalidIDs(t *testing.T) {
	dir := t.TempDir()
	r, _ := NewRegistry(filepath.Join(dir, "logs"))
	os.WriteFile(filepath.Join(dir, "secret.log"), []byte("top secret\n"), 0o600)
	for _, id := range []string{"", ".", "..", "../secret", "a/b", `a\b`, "x\x00y"} {
		if _, err := r.Open(id); err == nil {
			t.Errorf("Open(%q) accepted", id)
		}
		if past, ch, cancel := r.Subscribe(id); past != nil || ch != nil {
			t.Errorf("Subscribe(%q) = %v %v", id, past, ch)
		} else {
			cancel()
		}
		r.Remove(id)
	}
	if _, err := os.Stat(filepath.Join(dir, "secret.log")); err != nil {
		t.Error("Remove escaped the log directory")
	}
}

// 아주 긴 로그는 메모리 사본을 버리고 파일에서 읽는다. 결과는 같아야 한다.
func TestLongLogSpillsToFile(t *testing.T) {
	r, _ := NewRegistry(t.TempDir())
	b, _ := r.Open("dep_long")
	n := maxMemLines + 50
	for i := 0; i < n; i++ {
		b.Line(fmt.Sprintf("line %d", i))
	}
	if b.mem || b.lines != nil {
		t.Fatal("memory copy kept past the cap")
	}
	past, _, cancel := r.Subscribe("dep_long")
	cancel()
	if len(past) != n || past[0] != "line 0" || past[n-1] != fmt.Sprintf("line %d", n-1) {
		t.Fatalf("past: %d lines", len(past))
	}
	b.Close("live")
}

func TestMultilineAndLongLines(t *testing.T) {
	r, _ := NewRegistry(t.TempDir())
	b, _ := r.Open("dep_ml")
	b.Line("a\nb\n")
	long := strings.Repeat("x", maxReadLine+10)
	b.Line(long)
	b.Line("after long")
	past, _, cancel := r.Subscribe("dep_ml")
	cancel()
	if len(past) != 4 || past[0] != "a" || past[1] != "b" || past[3] != "after long" {
		t.Fatalf("memory past: %d %q", len(past), past[:2])
	}
	b.Close("live")
	r.Release("dep_ml")
	past, _, _ = r.Subscribe("dep_ml")
	if len(past) != 4 || len(past[2]) != maxReadLine || past[3] != "after long" {
		t.Fatalf("file past: %d lines", len(past))
	}
}

// 느린 구독자는 끊기고, 쓰는 쪽은 막히지 않는다.
func TestSlowSubscriberDropped(t *testing.T) {
	r, _ := NewRegistry(t.TempDir())
	b, _ := r.Open("dep_slow")
	_, ch, cancel := r.Subscribe("dep_slow")
	defer cancel()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			b.Line("x")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("writer blocked by slow subscriber")
	}
	n := 0
	for range ch {
		n++
	}
	if n == 0 || n >= 1000 {
		t.Fatalf("received %d events", n)
	}
	b.Close("live")
}

func TestConcurrentSubscribers(t *testing.T) {
	r, _ := NewRegistry(t.TempDir())
	b, _ := r.Open("dep_c")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_, ch, cancel := r.Subscribe("dep_c")
				if ch != nil {
					select {
					case <-ch:
					default:
					}
				}
				cancel()
				cancel() // 두 번 불러도 된다
			}
		}()
	}
	for i := 0; i < 200; i++ {
		b.Line("l")
		b.Status("building")
	}
	wg.Wait()
	b.Close("live")
	b.Line("ignored after close")
	b.Status("ignored")
	Discard().Line("nothing")
	Discard().Status("nothing")
}

// 같은 배포의 로그북을 다시 연 뒤 먼저 끝난 쪽이 Release해도, 진행 중인 로그북의 구독은 계속된다.
func TestReleaseKeepsReopenedBook(t *testing.T) {
	r, _ := NewRegistry(t.TempDir())
	first, _ := r.Open("dep_1")
	second, _ := r.Open("dep_1")
	first.Close("live")
	r.Release("dep_1")
	_, ch, cancel := r.Subscribe("dep_1")
	defer cancel()
	if ch == nil {
		t.Fatal("reopened book was released")
	}
	second.Line("still running")
	if e := <-ch; e.Data != "still running" {
		t.Fatalf("event: %+v", e)
	}
	second.Close("live")
	r.Release("dep_1")
	if _, ch, _ := r.Subscribe("dep_1"); ch != nil {
		t.Fatal("closed book not released")
	}
}
