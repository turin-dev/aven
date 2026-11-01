package tracing

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/turin-dev/aven/internal/store"
)

func TestTruncateUTF8(t *testing.T) {
	s := strings.Repeat("가", 10) // 3바이트 문자
	for n := 0; n <= len(s)+1; n++ {
		got := truncateUTF8(s, n)
		if !utf8.ValidString(got) || len(got) > n && len(s) > n {
			t.Fatalf("truncateUTF8(%d) = %q", n, got)
		}
	}
	if truncateUTF8("abc", 10) != "abc" {
		t.Fatal("short string changed")
	}
}

// replica ID를 바꿔 가며 보내도 서비스 전체 한도에 걸리고, 창 표가 끝없이 늘지 않는다.
func TestLimitRotatingReplicaIDs(t *testing.T) {
	h := &Receiver{}
	now := time.Unix(1_700_000_000, 0)
	h.now = func() time.Time { return now }
	var spans []store.TraceSpan
	for i := 0; i < ServiceSpanLimit+500; i++ {
		spans = append(spans, store.TraceSpan{ServiceID: "s", EnvironmentID: "e", ReplicaID: fmt.Sprintf("r%d", i)})
	}
	kept, rejected := h.limit(spans)
	if len(kept) != ServiceSpanLimit || rejected != 500 {
		t.Fatalf("kept %d rejected %d", len(kept), rejected)
	}
	if len(h.windows) > ServiceSpanLimit {
		t.Fatalf("replica windows grew to %d", len(h.windows))
	}
	// 다른 서비스는 따로 센다.
	if kept, _ := h.limit([]store.TraceSpan{{ServiceID: "other", EnvironmentID: "e", ReplicaID: "r"}}); len(kept) != 1 {
		t.Fatal("other service limited")
	}
}

// 큰 span은 개수 한도 전에 크기 한도에 걸린다.
func TestLimitBytes(t *testing.T) {
	h := &Receiver{}
	now := time.Unix(1_700_000_000, 0)
	h.now = func() time.Time { return now }
	big := strings.Repeat("x", 1<<20)
	mk := func() store.TraceSpan {
		return store.TraceSpan{ServiceID: "s", EnvironmentID: "e", ReplicaID: "r", Attributes: map[string]any{"blob": big}}
	}
	var spans []store.TraceSpan
	for i := 0; i < 40; i++ {
		spans = append(spans, mk())
	}
	kept, rejected := h.limit(spans)
	if len(kept) >= 16 || len(kept) == 0 || rejected == 0 {
		t.Fatalf("byte limit: kept %d rejected %d", len(kept), rejected)
	}
	now = now.Add(RateWindow)
	if kept, _ := h.limit([]store.TraceSpan{mk()}); len(kept) != 1 {
		t.Fatal("next window must accept again")
	}
}

func TestSpanBytesCountsNestedValues(t *testing.T) {
	sp := store.TraceSpan{Name: "n", Attributes: map[string]any{"a": []any{"xxxx", map[string]any{"k": "yyyy"}}, "b": int64(1)},
		Events: []store.TraceEvent{{Name: "e", Attributes: map[string]any{"m": strings.Repeat("z", 100)}}}}
	if n := spanBytes(&sp); n < 256+100+8 {
		t.Fatalf("spanBytes too small: %d", n)
	}
}

// 아주 먼 미래에 시작한 span은 저장하지 않고 partial success로 알린다.
func TestReceiverDropsFutureSpans(t *testing.T) {
	f := newFixture(t)
	start := uint64(time.Now().Add(48 * time.Hour).UnixNano())
	sp := pb{}.bytes(1, mustHex(traceA)).bytes(2, mustHex(spanA)).str(5, "future").fixed64(7, start).fixed64(8, start+1)
	body := pb{}.bytes(1, pb{}.bytes(2, pb{}.bytes(2, sp)))
	r := f.post(t, "/v1/traces", "application/x-protobuf", f.token, body)
	if r.StatusCode != 200 {
		t.Fatalf("export: %d", r.StatusCode)
	}
	if spans, _ := f.st.GetTraceSpans(f.ctx, f.env.ID, traceA, 0); len(spans) != 0 {
		t.Fatalf("future span stored: %+v", spans)
	}
}

// 속성 이름도 상한까지만 저장한다.
func TestLongAttributeKeyTruncated(t *testing.T) {
	long := strings.Repeat("k", 10000)
	req, err := decodeProtoRequest(pb{}.bytes(1, pb{}.bytes(2, pb{}.bytes(2,
		pb{}.bytes(1, mustHex(traceA)).bytes(2, mustHex(spanA)).bytes(9, kvString(long, "v"))))))
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range req[0].Scopes[0].Spans[0].Attrs {
		if len(kv.Key) > maxKeyBytes {
			t.Fatalf("key length %d", len(kv.Key))
		}
	}
}

func TestReceiverGRPCRejectsUnknownFlags(t *testing.T) {
	f := newFixture(t)
	msg := exportRequest()
	frame := append([]byte{2, 0, 0, 0, 0}, msg...)
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(msg)))
	req := httptest.NewRequest("POST", grpcExportPath, bytes.NewReader(frame))
	req.Header.Set("Content-Type", "application/grpc")
	req.Header.Set(TokenHeader, f.token)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, req)
	if got := w.Header().Get("Grpc-Status"); got != "3" {
		t.Fatalf("grpc flags 2: status %q", got)
	}
}

func TestParseFilterLimits(t *testing.T) {
	if _, err := ParseFilter(strings.Repeat("(", 100) + "x" + strings.Repeat(")", 100)); err == nil {
		t.Error("deep nesting accepted")
	}
	if _, err := ParseFilter(strings.Repeat("-", 100) + "x"); err == nil {
		t.Error("deep negation accepted")
	}
	if _, err := ParseFilter(strings.Repeat("a ", 3000)); err == nil {
		t.Error("oversized filter accepted")
	}
	if _, err := ParseFilter("((a OR b) -c) @duration:>10"); err != nil {
		t.Errorf("normal filter rejected: %v", err)
	}
}
