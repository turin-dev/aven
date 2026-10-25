package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/turin-dev/aven/internal/catalog"
	"github.com/turin-dev/aven/internal/store"
)

// fakeBucketS3는 bucket 하나를 흉내 내는 S3다(서명 형식과 access key만 확인한다. 서명 자체는 internal/s3 테스트가 확인한다).
type fakeBucketS3 struct {
	mu      sync.Mutex
	bucket  string
	access  string
	exists  bool
	objects map[string][]byte
}

func (f *fakeBucketS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential="+f.access+"/") {
		w.WriteHeader(403)
		fmt.Fprint(w, `<Error><Code>InvalidAccessKeyId</Code></Error>`)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/"+f.bucket)
	if !ok {
		w.WriteHeader(404)
		fmt.Fprint(w, `<Error><Code>NoSuchBucket</Code></Error>`)
		return
	}
	key := strings.TrimPrefix(rest, "/")
	q := r.URL.Query()
	body, _ := io.ReadAll(r.Body)
	if r.Method == "PUT" && key == "" {
		f.exists = true
		return
	}
	if !f.exists {
		w.WriteHeader(404)
		fmt.Fprint(w, `<Error><Code>NoSuchBucket</Code></Error>`)
		return
	}
	switch {
	case r.Method == "GET" && key == "":
		prefix := q.Get("prefix")
		var keys []string
		for k := range f.objects {
			if strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>`)
		seen := map[string]bool{}
		for _, k := range keys {
			if d := q.Get("delimiter"); d != "" {
				if i := strings.Index(k[len(prefix):], d); i >= 0 {
					p := k[:len(prefix)+i+1]
					if !seen[p] {
						seen[p] = true
						fmt.Fprintf(w, `<CommonPrefixes><Prefix>%s</Prefix></CommonPrefixes>`, p)
					}
					continue
				}
			}
			fmt.Fprintf(w, `<Contents><Key>%s</Key><Size>%d</Size><LastModified>2026-10-21T07:28:00Z</LastModified><ETag>"x"</ETag></Contents>`, k, len(f.objects[k]))
		}
		fmt.Fprint(w, `</ListBucketResult>`)
	case r.Method == "PUT":
		f.objects[key] = body
	case r.Method == "GET":
		b, ok := f.objects[key]
		if !ok {
			w.WriteHeader(404)
			fmt.Fprint(w, `<Error><Code>NoSuchKey</Code></Error>`)
			return
		}
		w.Header().Set("Content-Type", "text/html") // 응답에서는 octet-stream으로 바뀌어야 한다
		w.Write(b)
	case r.Method == "DELETE":
		delete(f.objects, key)
		w.WriteHeader(204)
	}
}

type bucketFixture struct {
	*ghFixture
	svc  *store.Service
	in   *store.Instance
	dep  *store.Deployment
	fake *fakeBucketS3
}

func newBucketFixture(t *testing.T) *bucketFixture {
	f := newGHFixture(t)
	code, out := f.call(f.srv.createService, "POST", "/x", `{"source_type":"bucket","name":"files"}`, map[string]string{"pid": f.p.ID})
	if code != 201 {
		t.Fatalf("create bucket service: %d %v", code, out)
	}
	svc, err := f.st.GetService(f.ctx, out["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	in, _ := f.st.GetInstanceFor(f.ctx, svc.ID, f.prod.ID)
	res, err := f.mgr.Resolve(f.ctx, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := &store.Deployment{ServiceID: svc.ID, EnvironmentID: f.prod.ID, InstanceID: in.ID, Status: store.DepLive, Trigger: "manual",
		NodeID: store.LocalNodeID, Containers: []string{"keel-files-1"}, Env: res.Vars}
	if err := f.st.CreateDeployment(f.ctx, d); err != nil {
		t.Fatal(err)
	}
	f.st.SetInstanceStatus(f.ctx, in.ID, "online", &d.ID)
	in, _ = f.st.GetInstanceFor(f.ctx, svc.ID, f.prod.ID)
	fake := &fakeBucketS3{bucket: res.Vars["BUCKET"], access: res.Vars["ACCESS_KEY_ID"], objects: map[string][]byte{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	bucketEndpointHook = func(_ context.Context, bt *bucketTarget) (string, error) {
		if bt.container != "keel-files-1" || !store.IsLocalNode(bt.node) {
			return "", fmt.Errorf("unexpected target %s %s", bt.node, bt.container)
		}
		return srv.URL, nil
	}
	t.Cleanup(func() { bucketEndpointHook = nil })
	return &bucketFixture{ghFixture: f, svc: svc, in: in, dep: d, fake: fake}
}

func (bf *bucketFixture) pv(key string) map[string]string {
	pv := map[string]string{"sid": bf.svc.ID, "eid": bf.prod.ID}
	if key != "" {
		pv["key"] = key
	}
	return pv
}

func TestBucketServiceVariables(t *testing.T) {
	bf := newBucketFixture(t)
	if bf.svc.SourceType != "database" || bf.svc.DatabaseKind == nil || *bf.svc.DatabaseKind != "bucket" {
		t.Fatalf("service: %+v", bf.svc)
	}
	v := bf.dep.Env
	host := bf.in.Domains[0].Host
	if v["ENDPOINT"] != "http://files.production.keel.internal:9000" || v["PUBLIC_ENDPOINT"] != "https://"+host || v["REGION"] != "us-east-1" {
		t.Errorf("endpoints: %v", v)
	}
	if v["MINIO_ROOT_USER"] != v["ACCESS_KEY_ID"] || v["AWS_SECRET_ACCESS_KEY"] != v["SECRET_ACCESS_KEY"] || v["AWS_S3_BUCKET_NAME"] != v["BUCKET"] || v["AWS_ENDPOINT_URL"] != v["ENDPOINT"] || len(v["SECRET_ACCESS_KEY"]) != 32 {
		t.Errorf("aliases: %v", v)
	}
	vols, _ := bf.st.ListVolumesByEnv(bf.ctx, bf.prod.ID)
	found := false
	for _, vol := range vols {
		if vol.InstanceID != nil && *vol.InstanceID == bf.in.ID && vol.MountPath == "/data" {
			found = true
		}
	}
	if !found || bf.in.HealthcheckType != "http" {
		t.Errorf("volume=%v health=%s", found, bf.in.HealthcheckType)
	}

	// 공개 도메인이 없으면 PUBLIC_ENDPOINT를 만들지 않는다.
	bf.mgr.Cfg.BaseDomain = ""
	code, out := bf.call(bf.srv.createService, "POST", "/x", `{"source_type":"database","database_kind":"bucket","name":"nodomain"}`, map[string]string{"pid": bf.p.ID})
	if code != 201 {
		t.Fatalf("%d %v", code, out)
	}
	in2, _ := bf.st.GetInstanceFor(bf.ctx, out["id"].(string), bf.prod.ID)
	if _, ok := in2.Variables["PUBLIC_ENDPOINT"]; ok || len(in2.Domains) != 0 || in2.Variables["BUCKET"] == v["BUCKET"] {
		t.Errorf("no-domain bucket: %v %v", in2.Variables, in2.Domains)
	}
}

func multipartBody(t *testing.T, prefix string, files map[string]string) (string, string) {
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	if prefix != "" {
		mw.WriteField("prefix", prefix)
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fw, _ := mw.CreateFormFile("file", n)
		fw.Write([]byte(files[n]))
	}
	mw.Close()
	return b.String(), mw.FormDataContentType()
}

func (bf *bucketFixture) do(h http.HandlerFunc, method, target, body, ctype string, pv map[string]string, level int) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	for k, v := range pv {
		r.SetPathValue(k, v)
	}
	if level > 0 {
		r = r.WithContext(context.WithValue(r.Context(), grantKey{}, level))
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func TestBucketBrowser(t *testing.T) {
	bf := newBucketFixture(t)
	// 처음 목록: bucket이 아직 없으면 만든다.
	code, out := bf.call(bf.srv.bucketObjects, "GET", "/x", "", bf.pv(""))
	if code != 200 || !bf.fake.exists || out["bucket"] != bf.fake.bucket || len(out["objects"].([]any)) != 0 || out["next_cursor"] != nil {
		t.Fatalf("empty list: %d %v", code, out)
	}
	body, ct := multipartBody(t, "docs", map[string]string{"a.txt": "hello", "b c.bin": "world!"})
	w := bf.do(bf.srv.bucketUpload, "PUT", "/x", body, ct, bf.pv(""), 0)
	if w.Code != 201 || string(bf.fake.objects["docs/a.txt"]) != "hello" || string(bf.fake.objects["docs/b c.bin"]) != "world!" {
		t.Fatalf("upload: %d %s %v", w.Code, w.Body.String(), bf.fake.objects)
	}
	body, ct = multipartBody(t, "", map[string]string{"top.txt": "t"})
	if w := bf.do(bf.srv.bucketUpload, "PUT", "/x", body, ct, bf.pv(""), 0); w.Code != 201 {
		t.Fatalf("upload top: %d %s", w.Code, w.Body.String())
	}
	code, out = bf.call(bf.srv.bucketObjects, "GET", "/x", "", bf.pv(""))
	b, _ := json.Marshal(out["prefixes"])
	objs := out["objects"].([]any)
	if code != 200 || string(b) != `["docs/"]` || len(objs) != 1 || objs[0].(map[string]any)["key"] != "top.txt" {
		t.Fatalf("root list: %d %v", code, out)
	}
	code, out = bf.call(bf.srv.bucketObjects, "GET", "/x?prefix=docs/", "", bf.pv(""))
	if objs := out["objects"].([]any); code != 200 || len(objs) != 2 || objs[1].(map[string]any)["size"] != float64(6) {
		t.Fatalf("docs list: %d %v", code, out)
	}

	w = bf.do(bf.srv.bucketDownload, "GET", "/x", "", "", bf.pv("docs/b c.bin"), 0)
	if w.Code != 200 || w.Body.String() != "world!" || w.Header().Get("Content-Type") != "application/octet-stream" ||
		!strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") || !strings.Contains(w.Header().Get("Content-Disposition"), "b c.bin") {
		t.Fatalf("download: %d %v %q", w.Code, w.Header(), w.Body.String())
	}
	if w := bf.do(bf.srv.bucketDownload, "GET", "/x", "", "", bf.pv("docs/missing"), 0); w.Code != 404 {
		t.Errorf("missing download: %d", w.Code)
	}
	if w := bf.do(bf.srv.bucketDelete, "DELETE", "/x", "", "", bf.pv("docs/a.txt"), 0); w.Code != 204 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if _, ok := bf.fake.objects["docs/a.txt"]; ok {
		t.Error("object not deleted")
	}
	evs, _ := bf.st.ListEvents(bf.ctx, bf.p.ID, "", 50)
	kinds := map[string]bool{}
	for _, e := range evs {
		kinds[e.Kind] = true
	}
	if !kinds["bucket.upload"] || !kinds["bucket.delete"] {
		t.Errorf("audit events: %v", kinds)
	}

	// 입력 검사
	for _, k := range []string{"", "/abs", "a/../b", "bad\nkey", strings.Repeat("k", 1025)} {
		if w := bf.do(bf.srv.bucketDelete, "DELETE", "/x", "", "", bf.pv(k), 0); w.Code != 400 {
			t.Errorf("key %q: %d", k, w.Code)
		}
	}
	for _, q := range []string{"/x?limit=0", "/x?limit=1001", "/x?prefix=%2Fabs"} {
		if code, _ := bf.call(bf.srv.bucketObjects, "GET", q, "", bf.pv("")); code != 400 {
			t.Errorf("%s: %d", q, code)
		}
	}
	if w := bf.do(bf.srv.bucketUpload, "PUT", "/x", `{}`, "application/json", bf.pv(""), 0); w.Code != 400 {
		t.Errorf("json upload: %d", w.Code)
	}
	body, ct = multipartBody(t, "x", map[string]string{"big.bin": "z"})
	r := httptest.NewRequest("PUT", "/x", strings.NewReader(body))
	r.Header.Set("Content-Type", ct)
	r.ContentLength = maxBucketUpload + 2<<20
	for k, v := range bf.pv("") {
		r.SetPathValue(k, v)
	}
	rec := httptest.NewRecorder()
	bf.srv.bucketUpload(rec, r)
	if rec.Code != 413 {
		t.Errorf("oversized upload: %d", rec.Code)
	}
	// bucket이 아닌 서비스
	if code, _ := bf.call(bf.srv.bucketObjects, "GET", "/x", "", map[string]string{"sid": bf.db.ID, "eid": bf.prod.ID}); code != 400 {
		t.Errorf("non-bucket service: %d", code)
	}
}

func TestBucketInfoMasksCredentialsForViewer(t *testing.T) {
	bf := newBucketFixture(t)
	get := func(level int) map[string]any {
		w := bf.do(bf.srv.bucketInfo, "GET", "/x", "", "", bf.pv(""), level)
		if w.Code != 200 {
			t.Fatalf("info: %d %s", w.Code, w.Body.String())
		}
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	m := get(lvMember)
	if m["access_key_id"] != bf.dep.Env["ACCESS_KEY_ID"] || m["secret_access_key"] != bf.dep.Env["SECRET_ACCESS_KEY"] || m["masked"] != false ||
		m["bucket"] != bf.dep.Env["BUCKET"] || m["running"] != true || m["public_endpoint"] != bf.dep.Env["PUBLIC_ENDPOINT"] {
		t.Errorf("member info: %v", m)
	}
	v := get(lvViewer)
	raw, _ := json.Marshal(v)
	if v["masked"] != true || v["secret_access_key"] != MaskedValue || v["access_key_id"] != MaskedValue || strings.Contains(string(raw), bf.dep.Env["SECRET_ACCESS_KEY"]) {
		t.Errorf("viewer info leaks credentials: %s", raw)
	}
	if v["bucket"] != bf.dep.Env["BUCKET"] || v["endpoint"] != "http://files.production.keel.internal:9000" {
		t.Errorf("viewer info: %v", v)
	}
}

func TestBucketRemoteNodeNeedsDomain(t *testing.T) {
	bf := newBucketFixture(t)
	bucketEndpointHook = nil
	d := &store.Deployment{ServiceID: bf.svc.ID, EnvironmentID: bf.prod.ID, InstanceID: bf.in.ID, Status: store.DepLive, Trigger: "manual",
		NodeID: "nod_remote1", Containers: []string{"keel-files-2"}, Env: bf.dep.Env}
	if err := bf.st.CreateDeployment(bf.ctx, d); err != nil {
		t.Fatal(err)
	}
	bf.st.SetInstanceStatus(bf.ctx, bf.in.ID, "online", &d.ID)
	in, _ := bf.st.GetInstanceFor(bf.ctx, bf.svc.ID, bf.prod.ID)
	in.Domains = nil
	if err := bf.st.UpdateInstanceConfig(bf.ctx, in); err != nil {
		t.Fatal(err)
	}
	if code, out := bf.call(bf.srv.bucketObjects, "GET", "/x", "", bf.pv("")); code != 409 || !strings.Contains(fmt.Sprint(out["error"]), "공개 도메인") {
		t.Errorf("remote without domain: %d %v", code, out)
	}
	// 공개 도메인이 있으면 https로 붙는다(9000이 아닌 포트로 가는 도메인은 쓰지 않는다).
	other := 9001
	in.Domains = []store.Domain{{Host: "console.example.com", Kind: "custom", TargetPort: &other}, {Host: "files.example.com", Kind: "generated"}}
	c := &instCtx{svc: bf.svc, env: bf.prod, p: bf.p, in: in}
	ep, err := bf.srv.bucketEndpoint(bf.ctx, &bucketTarget{c: c, node: "nod_remote1", container: "keel-files-2"})
	if err != nil || ep != "https://files.example.com" {
		t.Errorf("remote endpoint %q %v", ep, err)
	}
	if _, ok := catalog.Databases[catalog.BucketKind]; !ok {
		t.Error("bucket kind not registered")
	}
}
