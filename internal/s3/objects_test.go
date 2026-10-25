package s3

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// bucketFake는 bucket 하나(bkt)를 흉내 내는 S3다. 모든 요청의 SigV4 서명을 받은 그대로 다시 계산해 확인한다.
type bucketFake struct {
	mu      sync.Mutex
	t       *testing.T
	signer  *Client
	exists  bool
	objects map[string][]byte
	ctypes  map[string]string
	hashes  []string // 받은 x-amz-content-sha256
	created int
}

func newBucketFake(t *testing.T) *bucketFake {
	return &bucketFake{t: t, signer: &Client{Region: "us-east-1", AccessKey: "AK", SecretKey: "SK"}, objects: map[string][]byte{}, ctypes: map[string]string{}}
}

func (f *bucketFake) verify(r *http.Request) bool {
	at, err := time.Parse("20060102T150405Z", r.Header.Get("x-amz-date"))
	if err != nil {
		return false
	}
	req, _ := http.NewRequest(r.Method, "http://"+r.Host+r.URL.RequestURI(), nil)
	for k, v := range r.Header {
		if k != "Authorization" {
			req.Header[k] = v
		}
	}
	f.signer.sign(req, r.Header.Get("x-amz-content-sha256"), at)
	return req.Header.Get("Authorization") == r.Header.Get("Authorization")
}

func s3xmlErr(w http.ResponseWriter, code int, c string) {
	w.WriteHeader(code)
	fmt.Fprintf(w, `<Error><Code>%s</Code><Message>m</Message></Error>`, c)
}

func (f *bucketFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.verify(r) {
		s3xmlErr(w, 403, "SignatureDoesNotMatch")
		return
	}
	f.hashes = append(f.hashes, r.Header.Get("x-amz-content-sha256"))
	rest := strings.TrimPrefix(r.URL.Path, "/bkt")
	key := strings.TrimPrefix(rest, "/")
	body, _ := io.ReadAll(r.Body)
	if r.Method == "PUT" && key == "" {
		if f.exists {
			s3xmlErr(w, 409, "BucketAlreadyOwnedByYou")
			return
		}
		f.exists = true
		f.created++
		return
	}
	if !f.exists {
		s3xmlErr(w, 404, "NoSuchBucket")
		return
	}
	q := r.URL.Query()
	switch {
	case r.Method == "GET" && key == "":
		f.list(w, q.Get("prefix"), q.Get("delimiter"), q.Get("continuation-token"), q.Get("max-keys"))
	case r.Method == "PUT":
		f.objects[key] = body
		f.ctypes[key] = r.Header.Get("Content-Type")
		w.Header().Set("ETag", `"e-`+key+`"`)
	case r.Method == "GET":
		b, ok := f.objects[key]
		if !ok {
			s3xmlErr(w, 404, "NoSuchKey")
			return
		}
		w.Header().Set("Content-Type", f.ctypes[key])
		w.Header().Set("ETag", `"e-`+key+`"`)
		w.Header().Set("Last-Modified", "Wed, 21 Oct 2026 07:28:00 GMT")
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.Write(b)
	case r.Method == "DELETE":
		delete(f.objects, key)
		w.WriteHeader(204)
	default:
		w.WriteHeader(400)
	}
}

// list는 key 순서로 정렬해 delimiter로 묶는다. continuation token은 다음 항목의 위치다.
func (f *bucketFake) list(w http.ResponseWriter, prefix, delim, token, maxKeys string) {
	var keys []string
	for k := range f.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	type entry struct{ key, prefix string }
	var entries []entry
	seen := map[string]bool{}
	for _, k := range keys {
		if delim != "" {
			if i := strings.Index(k[len(prefix):], delim); i >= 0 {
				p := k[:len(prefix)+i+len(delim)]
				if !seen[p] {
					seen[p] = true
					entries = append(entries, entry{prefix: p})
				}
				continue
			}
		}
		entries = append(entries, entry{key: k})
	}
	start, _ := strconv.Atoi(token)
	n, _ := strconv.Atoi(maxKeys)
	end := min(start+n, len(entries))
	fmt.Fprint(w, `<ListBucketResult>`)
	for _, e := range entries[start:end] {
		if e.prefix != "" {
			fmt.Fprintf(w, `<CommonPrefixes><Prefix>%s</Prefix></CommonPrefixes>`, xmlEscape(e.prefix))
		} else {
			fmt.Fprintf(w, `<Contents><Key>%s</Key><Size>%d</Size><LastModified>2026-10-21T07:28:00.000Z</LastModified><ETag>"e"</ETag></Contents>`, xmlEscape(e.key), len(f.objects[e.key]))
		}
	}
	if end < len(entries) {
		fmt.Fprintf(w, `<IsTruncated>true</IsTruncated><NextContinuationToken>%d</NextContinuationToken>`, end)
	} else {
		fmt.Fprint(w, `<IsTruncated>false</IsTruncated>`)
	}
	fmt.Fprint(w, `</ListBucketResult>`)
}

func TestBucketObjectsListPutDelete(t *testing.T) {
	f := newBucketFake(t)
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := &Client{Endpoint: srv.URL, Region: "us-east-1", Bucket: "bkt", AccessKey: "AK", SecretKey: "SK"}
	ctx := context.Background()

	if _, err := c.ListPage(ctx, "", "/", "", 10); !IsNoSuchBucket(err) {
		t.Fatalf("want NoSuchBucket, got %v", err)
	}
	if err := c.CreateBucket(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateBucket(ctx); err != nil || f.created != 1 { // 이미 있으면 성공
		t.Fatalf("second create: %v created=%d", err, f.created)
	}
	for _, k := range []string{"a.txt", "docs/one.md", "docs/two md+&.md", "docs/sub/deep.bin", "img/x.png", "한글 파일.txt"} {
		body := "data:" + k
		if err := c.PutObject(ctx, k, strings.NewReader(body), int64(len(body)), "text/plain"); err != nil {
			t.Fatalf("put %q: %v", k, err)
		}
	}
	if f.hashes[len(f.hashes)-1] != unsignedPayload || f.ctypes["a.txt"] != "text/plain" || string(f.objects["한글 파일.txt"]) != "data:한글 파일.txt" {
		t.Errorf("put: hash=%s ctype=%q", f.hashes[len(f.hashes)-1], f.ctypes["a.txt"])
	}
	if err := c.PutObject(ctx, "empty", strings.NewReader(""), 0, ""); err != nil || len(f.objects["empty"]) != 0 {
		t.Errorf("empty put: %v", err)
	}

	// 최상위: 폴더 2개 + 파일, 페이지 크기 2로 이어 받기
	var prefixes []string
	var keys []string
	token := ""
	for i := 0; ; i++ {
		p, err := c.ListPage(ctx, "", "/", token, 2)
		if err != nil {
			t.Fatal(err)
		}
		prefixes = append(prefixes, p.Prefixes...)
		for _, o := range p.Objects {
			keys = append(keys, o.Key)
			if o.LastModified.IsZero() || o.ETag != "e" {
				t.Errorf("object meta %+v", o)
			}
		}
		if p.NextToken == "" {
			break
		}
		if i > 5 {
			t.Fatal("pagination does not end")
		}
		token = p.NextToken
	}
	if strings.Join(prefixes, ",") != "docs/,img/" || strings.Join(keys, ",") != "a.txt,empty,한글 파일.txt" {
		t.Errorf("top level: prefixes=%v keys=%v", prefixes, keys)
	}
	p, err := c.ListPage(ctx, "docs/", "/", "", 100)
	if err != nil || strings.Join(p.Prefixes, ",") != "docs/sub/" || len(p.Objects) != 2 || p.Objects[1].Key != "docs/two md+&.md" {
		t.Fatalf("docs/: %+v %v", p, err)
	}

	o, err := c.Open(ctx, "docs/two md+&.md")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(o)
	o.Close()
	if string(b) != "data:docs/two md+&.md" || o.Size != int64(len(b)) || o.ETag != "e-docs/two md+&.md" || o.LastModified.IsZero() {
		t.Errorf("open: %q %+v", b, o)
	}
	if _, err := c.Open(ctx, "nope"); !IsNotFound(err) {
		t.Errorf("want not found, got %v", err)
	}
	if err := c.Delete(ctx, "docs/one.md"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.objects["docs/one.md"]; ok {
		t.Error("not deleted")
	}
}

func TestBucketCreateRegionConstraint(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = r.Method + " " + r.URL.Path + " " + string(b)
	}))
	defer srv.Close()
	c := &Client{Endpoint: srv.URL, Region: "eu-west-1", Bucket: "bkt", AccessKey: "AK", SecretKey: "SK"}
	if err := c.CreateBucket(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "PUT /bkt <CreateBucketConfiguration><LocationConstraint>eu-west-1<") {
		t.Errorf("request %q", got)
	}
}
