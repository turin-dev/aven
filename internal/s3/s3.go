// Package s3는 S3 호환 저장소(AWS S3, MinIO, R2 등)용 최소 클라이언트다. SigV4 서명, path-style 주소, 멀티파트 스트림 업로드만 구현한다.
// 업로드는 크기를 모르는 스트림을 받으므로 한 번에 PartSize만큼만 메모리에 둔다(전체 파일을 버퍼링하지 않는다).
package s3

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type Client struct {
	Endpoint  string // https://host[:port] 또는 http://...
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	HTTP      *http.Client
	// PartSize는 멀티파트 한 조각의 크기다(기본 8MiB, S3 최소 5MiB).
	PartSize int
	now      func() time.Time
}

const defaultPartSize = 8 << 20

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 0}
}

func (c *Client) nowUTC() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now().UTC()
}

// encodePath는 S3 규칙으로 경로를 인코딩한다('/'는 그대로).
func encodePath(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		ch := p[i]
		if ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_' || ch == '.' || ch == '~' || ch == '/' {
			b.WriteByte(ch)
		} else {
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

func encodeQuery(v string) string { return strings.ReplaceAll(encodePath(v), "/", "%2F") }

func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string{}, q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, encodeQuery(k)+"="+encodeQuery(v))
		}
	}
	return strings.Join(parts, "&")
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// sign은 req에 SigV4 Authorization 헤더를 넣는다. payloadHash는 본문의 sha256(hex)이다.
func (c *Client) sign(req *http.Request, payloadHash string, t time.Time) {
	amzDate := t.Format("20060102T150405Z")
	date := t.Format("20060102")
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	// 서명할 헤더: host, x-amz-*, content-md5, content-type, range
	var names []string
	vals := map[string]string{"host": req.URL.Host}
	for k, v := range req.Header {
		lk := strings.ToLower(k)
		if lk == "host" {
			continue
		}
		if strings.HasPrefix(lk, "x-amz-") || lk == "content-md5" || lk == "content-type" || lk == "range" {
			vals[lk] = strings.TrimSpace(strings.Join(v, ","))
		}
	}
	for k := range vals {
		names = append(names, k)
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, n := range names {
		ch.WriteString(n + ":" + vals[n] + "\n")
	}
	signed := strings.Join(names, ";")
	canonical := strings.Join([]string{req.Method, req.URL.EscapedPath(), canonicalQuery(req.URL.Query()), ch.String(), signed, payloadHash}, "\n")
	scope := date + "/" + c.Region + "/s3/aws4_request"
	sts := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonical))
	k := hmacSHA256([]byte("AWS4"+c.SecretKey), date)
	k = hmacSHA256(k, c.Region)
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, sts))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.AccessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

func (c *Client) newReq(ctx context.Context, method, key string, q url.Values, body []byte) (*http.Request, error) {
	base, err := url.Parse(strings.TrimRight(c.Endpoint, "/"))
	if err != nil || base.Host == "" {
		return nil, errors.New("invalid S3 endpoint")
	}
	path := "/" + c.Bucket
	if key != "" {
		path += "/" + strings.TrimLeft(key, "/")
	}
	u := &url.URL{Scheme: base.Scheme, Host: base.Host, Path: path, RawPath: encodePath(path), RawQuery: canonicalQuery(q)}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = int64(len(body))
	}
	c.sign(req, sha256Hex(body), c.nowUTC())
	return req, nil
}

// Error는 S3가 돌려준 오류 응답이다.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("s3: %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("s3: http %d", e.Status)
}

func parseError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	var x struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	xml.Unmarshal(b, &x)
	return &Error{Status: resp.StatusCode, Code: x.Code, Message: x.Message}
}

func (c *Client) do(ctx context.Context, method, key string, q url.Values, body []byte) (*http.Response, error) {
	req, err := c.newReq(ctx, method, key, q, body)
	if err != nil {
		return nil, err
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, parseError(resp)
	}
	return resp, nil
}

func (c *Client) simple(ctx context.Context, method, key string, q url.Values, body []byte) (http.Header, []byte, error) {
	resp, err := c.do(ctx, method, key, q, body)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	return resp.Header, b, nil
}

// PutStream은 r을 key로 올리고 올린 바이트 수를 돌려준다. PartSize보다 작으면 단일 PUT, 크면 멀티파트다.
// 실패하면 멀티파트 업로드를 중단한다(조각이 남지 않는다).
func (c *Client) PutStream(ctx context.Context, key string, r io.Reader) (int64, error) {
	ps := c.PartSize
	if ps <= 0 {
		ps = defaultPartSize
	}
	buf := make([]byte, ps)
	n, err := io.ReadFull(r, buf)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		_, _, perr := c.simple(ctx, http.MethodPut, key, nil, buf[:n])
		return int64(n), perr
	}
	if err != nil {
		return 0, err
	}
	_, body, err := c.simple(ctx, http.MethodPost, key, url.Values{"uploads": {""}}, []byte{})
	if err != nil {
		return 0, err
	}
	var init struct {
		UploadID string `xml:"UploadId"`
	}
	if xml.Unmarshal(body, &init) != nil || init.UploadID == "" {
		return 0, errors.New("s3: no upload id")
	}
	abort := func() {
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		c.simple(actx, http.MethodDelete, key, url.Values{"uploadId": {init.UploadID}}, nil)
	}
	type part struct {
		N    int
		ETag string
	}
	var parts []part
	total := int64(0)
	for pn := 1; ; pn++ {
		h, _, perr := c.simple(ctx, http.MethodPut, key, url.Values{"partNumber": {fmt.Sprint(pn)}, "uploadId": {init.UploadID}}, buf[:n])
		if perr != nil {
			abort()
			return total, perr
		}
		parts = append(parts, part{pn, h.Get("ETag")})
		total += int64(n)
		n, err = io.ReadFull(r, buf)
		if err == io.EOF {
			break
		}
		if err != nil && err != io.ErrUnexpectedEOF {
			abort()
			return total, err
		}
		if err == io.ErrUnexpectedEOF { // 마지막 조각
			pn++
			h, _, perr := c.simple(ctx, http.MethodPut, key, url.Values{"partNumber": {fmt.Sprint(pn)}, "uploadId": {init.UploadID}}, buf[:n])
			if perr != nil {
				abort()
				return total, perr
			}
			parts = append(parts, part{pn, h.Get("ETag")})
			total += int64(n)
			break
		}
	}
	var x bytes.Buffer
	x.WriteString("<CompleteMultipartUpload>")
	for _, p := range parts {
		fmt.Fprintf(&x, "<Part><PartNumber>%d</PartNumber><ETag>%s</ETag></Part>", p.N, xmlEscape(p.ETag))
	}
	x.WriteString("</CompleteMultipartUpload>")
	_, rb, err := c.simple(ctx, http.MethodPost, key, url.Values{"uploadId": {init.UploadID}}, x.Bytes())
	if err != nil {
		abort()
		return total, err
	}
	// S3는 200 안에 오류를 담을 수 있다.
	if bytes.Contains(rb, []byte("<Error>")) {
		abort()
		return total, errors.New("s3: complete multipart failed: " + string(bytes.TrimSpace(rb[:min(len(rb), 300)])))
	}
	return total, nil
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// Get은 객체를 스트림으로 연다. 호출자가 Close한다.
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	resp, err := c.do(ctx, http.MethodGet, key, nil, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (c *Client) Delete(ctx context.Context, key string) error {
	_, _, err := c.simple(ctx, http.MethodDelete, key, nil, nil)
	var e *Error
	if errors.As(err, &e) && e.Status == 404 {
		return nil
	}
	return err
}

type Object struct {
	Key  string
	Size int64
}

// List는 prefix로 시작하는 객체를 모두 돌려준다(버킷 접근 확인에도 쓴다).
func (c *Client) List(ctx context.Context, prefix string) ([]Object, error) {
	var out []Object
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		_, b, err := c.simple(ctx, http.MethodGet, "", q, nil)
		if err != nil {
			return nil, err
		}
		var r struct {
			Contents []struct {
				Key  string `xml:"Key"`
				Size int64  `xml:"Size"`
			} `xml:"Contents"`
			Truncated bool   `xml:"IsTruncated"`
			Next      string `xml:"NextContinuationToken"`
		}
		if err := xml.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		for _, o := range r.Contents {
			out = append(out, Object{o.Key, o.Size})
		}
		if !r.Truncated || r.Next == "" {
			return out, nil
		}
		token = r.Next
	}
}
