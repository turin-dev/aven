package s3

import (
	"bytes"
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// flaky는 처음 fail번의 요청에 status를 돌려주고, 그 뒤는 next로 넘긴다.
type flaky struct {
	fail   int32
	status int
	code   string
	calls  atomic.Int32
	next   http.Handler
}

func (f *flaky) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if n := f.calls.Add(1); n <= f.fail {
		w.WriteHeader(f.status)
		if f.code != "" {
			w.Write([]byte("<Error><Code>" + f.code + "</Code><Message>m</Message></Error>"))
		}
		return
	}
	f.next.ServeHTTP(w, r)
}

func TestRetriesTransientErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		code   string
		fail   int32
		ok     bool
		calls  int32
	}{
		{"503 then ok", 503, "", 2, true, 3},
		{"SlowDown", 503, "SlowDown", 1, true, 2},
		{"500 InternalError", 500, "InternalError", 3, true, 4},
		{"429", 429, "", 1, true, 2},
		{"exhausted", 503, "", 10, false, 4},
		{"403 not retried", 403, "AccessDenied", 1, false, 1},
		{"404 not retried", 404, "NoSuchKey", 1, false, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake()
			fl := &flaky{fail: c.fail, status: c.status, code: c.code, next: f}
			srv := httptest.NewServer(fl)
			defer srv.Close()
			cl := &Client{Endpoint: srv.URL, Region: "us-east-1", Bucket: "bkt", AccessKey: "AK", SecretKey: "SK", backoff: time.Millisecond}
			_, err := cl.PutStream(context.Background(), "k", strings.NewReader("hello"))
			if (err == nil) != c.ok {
				t.Fatalf("err=%v want ok=%v", err, c.ok)
			}
			if got := fl.calls.Load(); got != c.calls {
				t.Errorf("calls %d want %d", got, c.calls)
			}
			if c.ok && string(f.objects["k"]) != "hello" {
				t.Errorf("object %q", f.objects["k"])
			}
		})
	}
}

func TestRetryDisabled(t *testing.T) {
	fl := &flaky{fail: 1, status: 503, next: newFake()}
	srv := httptest.NewServer(fl)
	defer srv.Close()
	cl := &Client{Endpoint: srv.URL, Bucket: "bkt", AccessKey: "AK", SecretKey: "SK", Retries: -1}
	if _, err := cl.PutStream(context.Background(), "k", strings.NewReader("x")); err == nil || fl.calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, fl.calls.Load())
	}
}

func TestRetryStopsOnContextCancel(t *testing.T) {
	fl := &flaky{fail: 100, status: 503, next: newFake()}
	srv := httptest.NewServer(fl)
	defer srv.Close()
	cl := &Client{Endpoint: srv.URL, Bucket: "bkt", AccessKey: "AK", SecretKey: "SK", Retries: 50, backoff: 50 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := cl.Get(ctx, "k"); err == nil {
		t.Fatal("want error")
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("retries ignored ctx: %s", time.Since(start))
	}
}

// 멀티파트 조각 하나가 한 번 실패해도 업로드 전체가 그대로 이어진다(같은 내용으로 다시 보낸다).
func TestMultipartPartRetried(t *testing.T) {
	f := newFake()
	var partFails atomic.Int32
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" && r.URL.Query().Get("partNumber") == "2" && partFails.Add(1) == 1 {
			w.WriteHeader(502)
			return
		}
		f.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(h)
	defer srv.Close()
	cl := &Client{Endpoint: srv.URL, Region: "r", Bucket: "bkt", AccessKey: "AK", SecretKey: "SK", PartSize: 100, backoff: time.Millisecond}
	data := make([]byte, 350)
	rand.Read(data)
	n, err := cl.PutStream(context.Background(), "big", bytes.NewReader(data))
	if err != nil || n != 350 || !bytes.Equal(f.objects["big"], data) {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if f.aborted != 0 {
		t.Error("upload aborted after a retried part")
	}
}

func TestMultipartMissingETagFails(t *testing.T) {
	f := newFake()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" && r.URL.Query().Get("partNumber") != "" {
			w.WriteHeader(200) // ETag 없음
			return
		}
		f.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(h)
	defer srv.Close()
	cl := &Client{Endpoint: srv.URL, Bucket: "bkt", AccessKey: "AK", SecretKey: "SK", PartSize: 100}
	if _, err := cl.PutStream(context.Background(), "big", bytes.NewReader(make([]byte, 250))); err == nil || !strings.Contains(err.Error(), "ETag") {
		t.Fatalf("err=%v", err)
	}
	if f.aborted != 1 || len(f.objects) != 0 {
		t.Errorf("aborted=%d objects=%d", f.aborted, len(f.objects))
	}
}

func TestEmptyRegionDefaultsToUSEast1(t *testing.T) {
	c := &Client{Endpoint: "http://h", Bucket: "b", AccessKey: "AK", SecretKey: "SK"}
	req, err := c.newReq(context.Background(), "GET", "k", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a := req.Header.Get("Authorization"); !strings.Contains(a, "/us-east-1/s3/aws4_request") {
		t.Errorf("scope: %s", a)
	}
}

// 특수 문자가 든 키는 S3 규칙(RFC 3986 unreserved만 그대로)으로 인코딩되고, 서명도 그 경로로 한다.
func TestKeyEncoding(t *testing.T) {
	c := &Client{Endpoint: "https://s3.example.com/", Region: "r", Bucket: "bkt", AccessKey: "AK", SecretKey: "SK"}
	cases := map[string]string{
		"plain/key.txt":       "/bkt/plain/key.txt",
		"a b+c=d":             "/bkt/a%20b%2Bc%3Dd",
		"한글/파일.sql.gz":        "/bkt/%ED%95%9C%EA%B8%80/%ED%8C%8C%EC%9D%BC.sql.gz",
		"x*y'z(1)!":           "/bkt/x%2Ay%27z%281%29%21",
		"/leading/slash":      "/bkt/leading/slash",
		"pct%20literal?&#":    "/bkt/pct%2520literal%3F%26%23",
		"svc-prod/2026~1.rdb": "/bkt/svc-prod/2026~1.rdb",
	}
	for key, want := range cases {
		req, err := c.newReq(context.Background(), "GET", key, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := req.URL.EscapedPath(); got != want {
			t.Errorf("%q: path %s want %s", key, got, want)
		}
		if req.URL.RawQuery != "" {
			t.Errorf("%q: key leaked into query %q", key, req.URL.RawQuery)
		}
	}
}

// 특수 문자 키가 서명을 다시 계산하는 가짜 S3에서 그대로 오간다.
func TestSpecialKeysRoundTripWithSignature(t *testing.T) {
	f := newBucketFake(t)
	f.exists = true
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := &Client{Endpoint: srv.URL, Region: "us-east-1", Bucket: "bkt", AccessKey: "AK", SecretKey: "SK"}
	ctx := context.Background()
	for _, key := range []string{"a b+c=d", "한글/파일", "x*y'z(1)!", "pct%20literal"} {
		if _, err := c.PutStream(ctx, key, strings.NewReader("v:"+key)); err != nil {
			t.Fatalf("%q put: %v", key, err)
		}
		rc, err := c.Get(ctx, key)
		if err != nil {
			t.Fatalf("%q get: %v", key, err)
		}
		var b bytes.Buffer
		b.ReadFrom(rc)
		rc.Close()
		if b.String() != "v:"+key {
			t.Errorf("%q: got %q", key, b.String())
		}
	}
}
