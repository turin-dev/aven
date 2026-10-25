package s3

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// AWS 문서의 SigV4 예제(GET Object, Range 헤더)로 서명을 검증한다.
func TestSigV4AWSExample(t *testing.T) {
	c := &Client{Endpoint: "https://examplebucket.s3.amazonaws.com", Region: "us-east-1", Bucket: "", AccessKey: "AKIAIOSFODNN7EXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
	req, _ := http.NewRequest("GET", "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	c.sign(req, sha256Hex(nil), time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))
	got := req.Header.Get("Authorization")
	want := "Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if !strings.HasSuffix(got, want) {
		t.Fatalf("signature mismatch: %s", got)
	}
	if !strings.Contains(got, "SignedHeaders=host;range;x-amz-content-sha256;x-amz-date") {
		t.Errorf("signed headers: %s", got)
	}
}

type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	uploads map[string]map[int][]byte
	maxBody int
	deleted []string
	aborted int
}

func newFake() *fakeS3 {
	return &fakeS3{objects: map[string][]byte{}, uploads: map[string]map[int][]byte{}}
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AK/") || r.Header.Get("x-amz-content-sha256") == "" {
		w.WriteHeader(403)
		return
	}
	key := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/bkt"), "/")
	q := r.URL.Query()
	body, _ := io.ReadAll(r.Body)
	if len(body) > f.maxBody {
		f.maxBody = len(body)
	}
	switch {
	case r.Method == "POST" && q.Has("uploads"):
		f.uploads["u1"] = map[int][]byte{}
		fmt.Fprint(w, `<InitiateMultipartUploadResult><UploadId>u1</UploadId></InitiateMultipartUploadResult>`)
	case r.Method == "PUT" && q.Get("uploadId") != "":
		var n int
		fmt.Sscan(q.Get("partNumber"), &n)
		f.uploads["u1"][n] = body
		w.Header().Set("ETag", fmt.Sprintf(`"etag%d"`, n))
	case r.Method == "POST" && q.Get("uploadId") != "":
		var x struct {
			Parts []struct {
				N int `xml:"PartNumber"`
			} `xml:"Part"`
		}
		xml.Unmarshal(body, &x)
		var all []byte
		for _, p := range x.Parts {
			all = append(all, f.uploads["u1"][p.N]...)
		}
		f.objects[key] = all
		fmt.Fprint(w, `<CompleteMultipartUploadResult/>`)
	case r.Method == "DELETE" && q.Get("uploadId") != "":
		f.aborted++
		w.WriteHeader(204)
	case r.Method == "PUT":
		f.objects[key] = body
	case r.Method == "GET" && key == "":
		fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>`)
		for k, v := range f.objects {
			if strings.HasPrefix(k, q.Get("prefix")) {
				fmt.Fprintf(w, `<Contents><Key>%s</Key><Size>%d</Size></Contents>`, k, len(v))
			}
		}
		fmt.Fprint(w, `</ListBucketResult>`)
	case r.Method == "GET":
		b, ok := f.objects[key]
		if !ok {
			w.WriteHeader(404)
			fmt.Fprint(w, `<Error><Code>NoSuchKey</Code><Message>nope</Message></Error>`)
			return
		}
		w.Write(b)
	case r.Method == "DELETE":
		f.deleted = append(f.deleted, key)
		delete(f.objects, key)
		w.WriteHeader(204)
	}
}

func TestPutStreamSmallAndMultipart(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := &Client{Endpoint: srv.URL, Region: "us-east-1", Bucket: "bkt", AccessKey: "AK", SecretKey: "SK", PartSize: 1000}
	ctx := context.Background()

	small := make([]byte, 300)
	rand.Read(small)
	if n, err := c.PutStream(ctx, "p/small.bin", bytes.NewReader(small)); err != nil || n != 300 || !bytes.Equal(f.objects["p/small.bin"], small) {
		t.Fatalf("small: %d %v", n, err)
	}
	for _, size := range []int{1000, 2500, 3000} {
		big := make([]byte, size)
		rand.Read(big)
		n, err := c.PutStream(ctx, "p/big.bin", bytes.NewReader(big))
		if err != nil || n != int64(size) || !bytes.Equal(f.objects["p/big.bin"], big) {
			t.Fatalf("size %d: n=%d err=%v got=%d", size, n, err, len(f.objects["p/big.bin"]))
		}
	}
	if f.maxBody > 1000 {
		t.Errorf("request body %d exceeds part size: whole file was buffered", f.maxBody)
	}
	objs, err := c.List(ctx, "p/")
	if err != nil || len(objs) != 2 {
		t.Fatalf("list: %v %v", objs, err)
	}
	rc, err := c.Get(ctx, "p/small.bin")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(b, small) {
		t.Error("get mismatch")
	}
	if _, err := c.Get(ctx, "nope"); err == nil || !strings.Contains(err.Error(), "NoSuchKey") {
		t.Errorf("want NoSuchKey, got %v", err)
	}
	if err := c.Delete(ctx, "p/small.bin"); err != nil || len(f.deleted) != 1 {
		t.Errorf("delete: %v", err)
	}
}

func TestPutStreamAbortsOnReadError(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := &Client{Endpoint: srv.URL, Region: "r", Bucket: "bkt", AccessKey: "AK", SecretKey: "SK", PartSize: 100}
	pr, pw := io.Pipe()
	go func() {
		pw.Write(make([]byte, 250))
		pw.CloseWithError(fmt.Errorf("dump failed"))
	}()
	if _, err := c.PutStream(context.Background(), "x", pr); err == nil {
		t.Fatal("want error")
	}
	if f.aborted != 1 || len(f.objects) != 0 {
		t.Errorf("multipart not aborted: aborted=%d objects=%d", f.aborted, len(f.objects))
	}
}
