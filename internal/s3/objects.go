package s3

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 객체 브라우저용 호출: bucket 만들기, 한 페이지 목록(ListObjectsV2 + delimiter), 메타데이터와 함께 열기, 스트림 PUT.

// unsignedPayload는 본문을 서명하지 않는 SigV4 표시다(크기가 큰 스트림 업로드에서 본문을 두 번 읽지 않는다).
const unsignedPayload = "UNSIGNED-PAYLOAD"

// CreateBucket은 c.Bucket을 만든다. 이미 내 bucket이면 성공으로 본다(BucketAlreadyOwnedByYou).
func (c *Client) CreateBucket(ctx context.Context) error {
	var body []byte
	if c.Region != "" && c.Region != "us-east-1" {
		body = []byte("<CreateBucketConfiguration><LocationConstraint>" + xmlEscape(c.Region) + "</LocationConstraint></CreateBucketConfiguration>")
	} else {
		body = []byte{}
	}
	_, _, err := c.simple(ctx, http.MethodPut, "", nil, body)
	var e *Error
	if errors.As(err, &e) && e.Code == "BucketAlreadyOwnedByYou" {
		return nil
	}
	return err
}

// IsNoSuchBucket은 bucket이 없다는 오류인지 알려준다.
func IsNoSuchBucket(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == "NoSuchBucket"
}

// IsNotFound는 객체(또는 bucket)가 없다는 오류인지 알려준다.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && (e.Status == http.StatusNotFound || e.Code == "NoSuchKey" || e.Code == "NoSuchBucket")
}

// ObjectInfo는 목록의 객체 하나다.
type ObjectInfo struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"last_modified"`
	ETag         string    `json:"etag"`
}

// Page는 ListObjectsV2 한 페이지다. Prefixes는 delimiter로 묶인 "폴더"다.
type Page struct {
	Prefixes  []string
	Objects   []ObjectInfo
	NextToken string
}

// ListPage는 prefix 아래를 한 페이지 나열한다. delimiter가 "/"면 바로 아래 단계만 돌려준다.
// token은 이전 페이지의 NextToken이다. maxKeys는 1..1000(0이면 1000).
func (c *Client) ListPage(ctx context.Context, prefix, delimiter, token string, maxKeys int) (*Page, error) {
	if maxKeys <= 0 || maxKeys > 1000 {
		maxKeys = 1000
	}
	q := url.Values{"list-type": {"2"}, "prefix": {prefix}, "max-keys": {strconv.Itoa(maxKeys)}}
	if delimiter != "" {
		q.Set("delimiter", delimiter)
	}
	if token != "" {
		q.Set("continuation-token", token)
	}
	_, b, err := c.simple(ctx, http.MethodGet, "", q, nil)
	if err != nil {
		return nil, err
	}
	var r struct {
		Contents []struct {
			Key          string `xml:"Key"`
			Size         int64  `xml:"Size"`
			LastModified string `xml:"LastModified"`
			ETag         string `xml:"ETag"`
		} `xml:"Contents"`
		CommonPrefixes []struct {
			Prefix string `xml:"Prefix"`
		} `xml:"CommonPrefixes"`
		Truncated bool   `xml:"IsTruncated"`
		Next      string `xml:"NextContinuationToken"`
	}
	if err := xml.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("s3: list response: %w", err)
	}
	p := &Page{Prefixes: []string{}, Objects: []ObjectInfo{}}
	for _, cp := range r.CommonPrefixes {
		p.Prefixes = append(p.Prefixes, cp.Prefix)
	}
	for _, o := range r.Contents {
		lm, _ := time.Parse(time.RFC3339Nano, o.LastModified)
		p.Objects = append(p.Objects, ObjectInfo{Key: o.Key, Size: o.Size, LastModified: lm, ETag: strings.Trim(o.ETag, `"`)})
	}
	if r.Truncated {
		p.NextToken = r.Next
	}
	return p, nil
}

// ObjectReader는 열린 객체 본문과 메타데이터다. 호출자가 Close한다.
type ObjectReader struct {
	io.ReadCloser
	Size         int64 // 모르면 -1
	ContentType  string
	LastModified time.Time
	ETag         string
}

// Open은 객체를 메타데이터와 함께 연다.
func (c *Client) Open(ctx context.Context, key string) (*ObjectReader, error) {
	resp, err := c.do(ctx, http.MethodGet, key, nil, nil)
	if err != nil {
		return nil, err
	}
	o := &ObjectReader{ReadCloser: resp.Body, Size: resp.ContentLength, ContentType: resp.Header.Get("Content-Type"), ETag: strings.Trim(resp.Header.Get("ETag"), `"`)}
	o.LastModified, _ = http.ParseTime(resp.Header.Get("Last-Modified"))
	return o, nil
}

// PutObject는 크기를 아는 스트림을 PUT 한 번으로 올린다(본문은 서명하지 않는다: UNSIGNED-PAYLOAD).
// 본문을 메모리에 모으지 않는다. S3의 단일 PUT 상한(5GiB)까지 된다.
func (c *Client) PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	if key == "" {
		return errors.New("s3: empty key")
	}
	if size < 0 {
		return errors.New("s3: unknown size")
	}
	u, err := c.objectURL(key, nil)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), io.NopCloser(io.LimitReader(r, size)))
	if err != nil {
		return err
	}
	req.ContentLength = size
	if size == 0 {
		req.Body = http.NoBody
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	c.sign(req, unsignedPayload, c.nowUTC())
	resp, err := c.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return parseError(resp)
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return nil
}

// objectURL은 newReq와 같은 규칙(path-style)으로 주소를 만든다.
func (c *Client) objectURL(key string, q url.Values) (*url.URL, error) {
	base, err := url.Parse(strings.TrimRight(c.Endpoint, "/"))
	if err != nil || base.Host == "" {
		return nil, errors.New("invalid S3 endpoint")
	}
	path := "/" + c.Bucket
	if key != "" {
		path += "/" + strings.TrimLeft(key, "/")
	}
	return &url.URL{Scheme: base.Scheme, Host: base.Host, Path: path, RawPath: encodePath(path), RawQuery: canonicalQuery(q)}, nil
}
