package logbook

import "testing"

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
