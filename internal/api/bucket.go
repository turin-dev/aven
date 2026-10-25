package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/turin-dev/aven/internal/catalog"
	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/s3"
	"github.com/turin-dev/aven/internal/store"
)

// ---- bucket (S3 호환 객체 저장소) ----
//
// GET    /api/services/{sid}/instances/{eid}/bucket                   bucket 이름, endpoint, 자격 증명(viewer는 가림)
// GET    /api/services/{sid}/instances/{eid}/bucket/objects?prefix=&cursor=&limit=   한 단계 목록(ListObjectsV2, delimiter "/")
// GET    /api/services/{sid}/instances/{eid}/bucket/objects/{key...}  객체 내려받기(스트림, member 이상)
// PUT    /api/services/{sid}/instances/{eid}/bucket/objects           multipart 업로드(prefix 필드 + file 여러 개, 합계 100MiB)
// DELETE /api/services/{sid}/instances/{eid}/bucket/objects/{key...}  객체 지우기
//
// aven-server가 MinIO에 S3 API(SigV4)로 직접 붙는다. 로컬 노드는 환경 네트워크의 컨테이너 IP(:9000, healthcheck와 같은 경로),
// 원격 노드는 공개 도메인(https)으로 붙는다(aven-server는 원격 노드의 컨테이너 IP에 닿지 않는다). 원격 노드에서 공개 도메인이
// 없으면 브라우저는 409다. 기본 bucket은 컨테이너 시작 명령이 만들고(catalog.BucketStart), 브라우저도 없으면 만든다.

const (
	maxBucketUpload      = 100 << 20
	maxBucketUploadFiles = 50
	maxObjectKey         = 1024
)

func isBucketService(svc *store.Service) bool {
	return svc.DatabaseKind != nil && *svc.DatabaseKind == catalog.BucketKind
}

// bucketWithoutDomain은 공개 도메인 없이 만드는 bucket 인스턴스에서 PUBLIC_ENDPOINT를 뺀다("https://"만 남지 않게).
func bucketWithoutDomain(def catalog.ServiceDef, in *store.Instance) {
	if def.DatabaseKind == catalog.BucketKind && len(in.Domains) == 0 {
		delete(in.Variables, "PUBLIC_ENDPOINT")
	}
}

// bucketTarget은 S3 호출 대상(실행 중인 MinIO 컨테이너)이다.
type bucketTarget struct {
	c         *instCtx
	dep       *store.Deployment
	node      string
	container string
	vars      map[string]string // 컨테이너의 최종 환경변수
}

// bucketEndpointHook이 있으면 S3 endpoint를 이것으로 정한다(테스트용).
var bucketEndpointHook func(ctx context.Context, t *bucketTarget) (string, error)

var errBucketUnreachable = errors.New("원격 노드에서 도는 bucket은 공개 도메인이 있어야 브라우저를 쓸 수 있습니다. 도메인을 추가하세요")

// bucketEndpoint는 aven-server가 MinIO에 닿는 주소다.
func (s *Server) bucketEndpoint(ctx context.Context, t *bucketTarget) (string, error) {
	if bucketEndpointHook != nil {
		return bucketEndpointHook(ctx, t)
	}
	if store.IsLocalNode(t.node) && s.Deploy != nil && s.Deploy.Docker != nil {
		st, err := s.Deploy.Docker.Inspect(ctx, t.container)
		if err != nil {
			return "", err
		}
		for _, ip := range st.IP(deploy.EnvNetwork(t.c.env.ID)) {
			return "http://" + net.JoinHostPort(ip, strconv.Itoa(catalog.BucketPort)), nil
		}
		return "", errors.New("bucket 컨테이너의 네트워크 주소를 찾지 못했습니다")
	}
	for _, d := range t.c.in.Domains {
		if d.TargetPort == nil || *d.TargetPort == catalog.BucketPort {
			return "https://" + d.Host, nil
		}
	}
	return "", errBucketUnreachable
}

func (s *Server) loadBucketInstance(w http.ResponseWriter, r *http.Request) (*instCtx, bool) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return nil, false
	}
	if !isBucketService(c.svc) {
		writeErr(w, 400, "bucket 서비스가 아닙니다")
		return nil, false
	}
	return c, true
}

// bucketClient는 실행 중인 bucket의 S3 클라이언트를 만든다.
func (s *Server) bucketClient(w http.ResponseWriter, r *http.Request) (*bucketTarget, *s3.Client, bool) {
	c, ok := s.loadBucketInstance(w, r)
	if !ok {
		return nil, nil, false
	}
	d, container, code, msg := s.execTarget(r.Context(), c.in, "")
	if d == nil {
		if code == 409 {
			msg = "bucket이 실행 중이 아닙니다. 먼저 배포하세요"
		}
		writeErr(w, code, msg)
		return nil, nil, false
	}
	vars := d.Env
	if len(vars) == 0 {
		res, err := s.Deploy.Resolve(r.Context(), c.in, nil)
		if err != nil {
			s.storeErr(w, err)
			return nil, nil, false
		}
		vars = res.Vars
	}
	t := &bucketTarget{c: c, dep: d, node: d.NodeOf(container), container: container, vars: vars}
	cli, err := bucketS3Client(vars)
	if err != nil {
		writeErr(w, 409, err.Error())
		return nil, nil, false
	}
	ep, err := s.bucketEndpoint(r.Context(), t)
	if err != nil {
		code := 502
		if errors.Is(err, errBucketUnreachable) {
			code = 409
		}
		writeErr(w, code, err.Error())
		return nil, nil, false
	}
	cli.Endpoint = ep
	return t, cli, true
}

// bucketS3Client는 컨테이너 환경변수의 자격 증명으로 클라이언트를 만든다(MinIO가 실제로 쓰는 MINIO_ROOT_* 우선).
func bucketS3Client(vars map[string]string) (*s3.Client, error) {
	first := func(keys ...string) string {
		for _, k := range keys {
			if v := vars[k]; v != "" {
				return v
			}
		}
		return ""
	}
	cli := &s3.Client{
		Region:    first("MINIO_SITE_REGION", "REGION"),
		Bucket:    first("BUCKET"),
		AccessKey: first("MINIO_ROOT_USER", "ACCESS_KEY_ID"),
		SecretKey: first("MINIO_ROOT_PASSWORD", "SECRET_ACCESS_KEY"),
		HTTP:      &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
	if cli.Region == "" {
		cli.Region = catalog.BucketRegion
	}
	if cli.Bucket == "" || cli.AccessKey == "" || cli.SecretKey == "" {
		return nil, errors.New("bucket 변수(BUCKET, ACCESS_KEY_ID, SECRET_ACCESS_KEY)가 비어 있습니다")
	}
	if !validBucketName(cli.Bucket) {
		return nil, errors.New("BUCKET이 올바른 bucket 이름이 아닙니다(소문자, 숫자, '-', '.' 3~63자)")
	}
	return cli, nil
}

func validBucketName(b string) bool {
	if len(b) < 3 || len(b) > 63 || strings.Contains(b, "..") {
		return false
	}
	for i := 0; i < len(b); i++ {
		ch := b[i]
		if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '.') {
			return false
		}
	}
	return b[0] != '-' && b[0] != '.' && b[len(b)-1] != '-' && b[len(b)-1] != '.'
}

// validObjectKey는 브라우저가 다루는 객체 key를 검사한다(UTF-8, 제어 문자 금지, '/'로 시작하지 않음, '.'/'..' 단계 금지).
func validObjectKey(k string, allowEmpty bool) error {
	if k == "" {
		if allowEmpty {
			return nil
		}
		return errors.New("key가 비어 있습니다")
	}
	if len(k) > maxObjectKey || !utf8.ValidString(k) {
		return errors.New("key는 1024바이트 이하의 UTF-8 문자열이어야 합니다")
	}
	for _, r := range k {
		if r < 0x20 || r == 0x7f {
			return errors.New("key에 제어 문자를 쓸 수 없습니다")
		}
	}
	if strings.HasPrefix(k, "/") {
		return errors.New("key는 '/'로 시작할 수 없습니다")
	}
	for _, seg := range strings.Split(k, "/") {
		if seg == "." || seg == ".." {
			return errors.New("key에 '.' 또는 '..' 단계를 쓸 수 없습니다")
		}
	}
	return nil
}

// s3Err는 S3 호출 실패를 응답한다.
func (s *Server) s3Err(w http.ResponseWriter, err error) {
	var e *s3.Error
	switch {
	case errors.Is(err, context.Canceled):
		writeErr(w, 499, "request cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		writeErr(w, 504, "bucket이 제때 응답하지 않았습니다")
	case s3.IsNotFound(err):
		writeErr(w, 404, "객체를 찾을 수 없습니다")
	case errors.As(err, &e):
		writeErr(w, 502, "bucket 오류: "+e.Error())
	default:
		s.Log.Warn("bucket s3", "err", err)
		writeErr(w, 502, "bucket에 연결하지 못했습니다: "+err.Error())
	}
}

// ensureBucket은 기본 bucket이 없으면 만든다(시작 명령이 아직 못 만들었거나 사용자가 지운 경우).
func ensureBucket(ctx context.Context, cli *s3.Client) error {
	_, err := cli.ListPage(ctx, "", "", "", 1)
	if s3.IsNoSuchBucket(err) {
		return cli.CreateBucket(ctx)
	}
	return err
}

func (s *Server) bucketInfo(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadBucketInstance(w, r)
	if !ok {
		return
	}
	res, err := s.Deploy.Resolve(r.Context(), c.in, nil)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	// 봉인된 변수(자격 증명 등)에서 온 값은 누구에게도 보여 주지 않는다.
	v := s.hideSealedResolved(r.Context(), c.in, res.Vars)
	var public *string
	if p := v["PUBLIC_ENDPOINT"]; p != "" && p != "https://" {
		public = &p
	} else if len(c.in.Domains) > 0 {
		p := "https://" + c.in.Domains[0].Host
		public = &p
	}
	region := v["REGION"]
	if region == "" {
		region = catalog.BucketRegion
	}
	access, secret := v["ACCESS_KEY_ID"], v["SECRET_ACCESS_KEY"]
	masked := !canSeeSecrets(r)
	if masked {
		access, secret = MaskedValue, MaskedValue // viewer는 자격 증명을 볼 수 없다
	}
	running := false
	if d, _, _, _ := s.execTarget(r.Context(), c.in, ""); d != nil {
		running = true
	}
	writeJSON(w, 200, map[string]any{
		"bucket": v["BUCKET"], "region": region, "endpoint": v["ENDPOINT"], "public_endpoint": public,
		"access_key_id": access, "secret_access_key": secret, "masked": masked, "running": running,
		"max_upload_bytes": maxBucketUpload,
	})
}

func (s *Server) bucketObjects(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	prefix := q.Get("prefix")
	if err := validObjectKey(prefix, true); err != nil {
		writeErr(w, 400, "prefix: "+err.Error())
		return
	}
	cursor := q.Get("cursor")
	if len(cursor) > 4096 {
		writeErr(w, 400, "cursor가 너무 깁니다")
		return
	}
	limit := atoiDefault(q.Get("limit"), 200)
	if limit < 1 || limit > 1000 {
		writeErr(w, 400, "limit must be 1..1000")
		return
	}
	t, cli, ok := s.bucketClient(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	page, err := cli.ListPage(ctx, prefix, "/", cursor, limit)
	if s3.IsNoSuchBucket(err) {
		if err = cli.CreateBucket(ctx); err == nil {
			page, err = cli.ListPage(ctx, prefix, "/", cursor, limit)
		}
	}
	if err != nil {
		s.s3Err(w, err)
		return
	}
	var next *string
	if page.NextToken != "" {
		next = &page.NextToken
	}
	objs := make([]s3.ObjectInfo, 0, len(page.Objects))
	for _, o := range page.Objects {
		if o.Key == prefix && strings.HasSuffix(o.Key, "/") {
			continue // 현재 "폴더" 표시 객체
		}
		objs = append(objs, o)
	}
	bucket := s.hideSealedResolved(r.Context(), t.c.in, map[string]string{"BUCKET": t.vars["BUCKET"]})["BUCKET"]
	writeJSON(w, 200, map[string]any{"bucket": bucket, "prefix": prefix, "prefixes": page.Prefixes, "objects": objs, "next_cursor": next})
}

func (s *Server) bucketDownload(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if err := validObjectKey(key, false); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	_, cli, ok := s.bucketClient(w, r)
	if !ok {
		return
	}
	obj, err := cli.Open(r.Context(), key)
	if err != nil {
		s.s3Err(w, err)
		return
	}
	defer obj.Close()
	name := path.Base(key)
	if name == "" || name == "/" || name == "." {
		name = "download"
	}
	h := w.Header()
	// 같은 출처에서 내용을 해석하지 않도록 항상 첨부 파일 + octet-stream으로 내보낸다.
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, no-store")
	if obj.Size >= 0 {
		h.Set("Content-Length", strconv.FormatInt(obj.Size, 10))
	}
	if obj.ETag != "" {
		h.Set("ETag", `"`+obj.ETag+`"`)
	}
	w.WriteHeader(200)
	io.Copy(w, obj)
}

func (s *Server) bucketUpload(w http.ResponseWriter, r *http.Request) {
	ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || ct != "multipart/form-data" {
		writeErr(w, 400, "multipart/form-data 본문이 필요합니다(prefix 필드 다음에 file)")
		return
	}
	if r.ContentLength > maxBucketUpload+(1<<20) {
		writeErr(w, 413, fmt.Sprintf("업로드는 한 번에 %dMiB까지입니다", maxBucketUpload>>20))
		return
	}
	t, cli, ok := s.bucketClient(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBucketUpload+(1<<20)) // multipart 경계/헤더 여유
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, 400, "multipart: "+err.Error())
		return
	}
	ctx := r.Context()
	if err := ensureBucket(ctx, cli); err != nil {
		s.s3Err(w, err)
		return
	}
	type uploaded struct {
		Key  string `json:"key"`
		Size int64  `json:"size"`
	}
	var done []uploaded
	var total int64
	prefix := ""
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				writeErr(w, 413, fmt.Sprintf("업로드는 한 번에 %dMiB까지입니다", maxBucketUpload>>20))
				return
			}
			writeErr(w, 400, "multipart: "+err.Error())
			return
		}
		switch part.FormName() {
		case "prefix":
			b, _ := io.ReadAll(io.LimitReader(part, maxObjectKey+1))
			prefix = string(b)
			if prefix != "" && !strings.HasSuffix(prefix, "/") {
				prefix += "/"
			}
			if err := validObjectKey(prefix, true); err != nil {
				writeErr(w, 400, "prefix: "+err.Error())
				return
			}
			continue
		case "file":
		default:
			continue
		}
		if len(done) >= maxBucketUploadFiles {
			writeErr(w, 400, fmt.Sprintf("한 번에 파일 %d개까지 올릴 수 있습니다", maxBucketUploadFiles))
			return
		}
		name := part.FileName() // 경로 부분은 버린다(Go가 이미 base name만 준다)
		name = path.Base(strings.ReplaceAll(name, `\`, "/"))
		if name == "" || name == "." || name == "/" {
			writeErr(w, 400, "파일 이름이 비어 있습니다")
			return
		}
		key := prefix + name
		if err := validObjectKey(key, false); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		n, err := cli.PutStream(ctx, key, part)
		total += n
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				writeErr(w, 413, fmt.Sprintf("업로드는 한 번에 %dMiB까지입니다", maxBucketUpload>>20))
				return
			}
			s.s3Err(w, err)
			return
		}
		done = append(done, uploaded{Key: key, Size: n})
	}
	if len(done) == 0 {
		writeErr(w, 400, "file 필드가 없습니다")
		return
	}
	_, who := s.execUser(r)
	c := t.c
	msg := fmt.Sprintf("%s (%s): %s이 bucket에 파일 %d개를 올렸습니다(%s, %d바이트)", c.svc.Name, c.env.Name, who, len(done), done[0].Key, total)
	s.Events.Emit(ctx, c.p.ID, &c.env.ID, &c.svc.ID, "bucket.upload", msg)
	writeJSON(w, 201, map[string]any{"uploaded": done})
}

func (s *Server) bucketDelete(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if err := validObjectKey(key, false); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	t, cli, ok := s.bucketClient(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := cli.Delete(ctx, key); err != nil {
		s.s3Err(w, err)
		return
	}
	_, who := s.execUser(r)
	c := t.c
	s.Events.Emit(ctx, c.p.ID, &c.env.ID, &c.svc.ID, "bucket.delete", fmt.Sprintf("%s (%s): %s이 bucket 객체 %s를 지웠습니다", c.svc.Name, c.env.Name, who, key))
	w.WriteHeader(204)
}
