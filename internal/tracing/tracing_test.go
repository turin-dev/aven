package tracing

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

// ---- 테스트용 protobuf 인코더 ----

type pb []byte

func (b pb) tag(num, wire int) pb { return binary.AppendUvarint(b, uint64(num<<3|wire)) }
func (b pb) bytes(num int, v []byte) pb {
	b = b.tag(num, 2)
	b = binary.AppendUvarint(b, uint64(len(v)))
	return append(b, v...)
}
func (b pb) str(num int, s string) pb    { return b.bytes(num, []byte(s)) }
func (b pb) varint(num int, v uint64) pb { return binary.AppendUvarint(b.tag(num, 0), v) }
func (b pb) fixed64(num int, v uint64) pb {
	return binary.LittleEndian.AppendUint64(b.tag(num, 1), v)
}

func kvString(k, v string) pb { return pb{}.str(1, k).bytes(2, pb{}.str(1, v)) }
func kvInt(k string, v int64) pb {
	return pb{}.str(1, k).bytes(2, pb{}.varint(3, uint64(v)))
}

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

const (
	traceA = "4bf92f3577b34da6a3ce929d0e0e4736"
	spanA  = "00f067aa0ba902b7"
	spanB  = "00f067aa0ba902b8"
)

// exportRequest는 resource 속성과 span 두 개(부모-자식)를 담은 ExportTraceServiceRequest다.
func exportRequest(resource ...pb) []byte {
	start := uint64(time.Now().Add(-time.Minute).UnixNano())
	root := pb{}.bytes(1, mustHex(traceA)).bytes(2, mustHex(spanA)).str(5, "GET /api/users").varint(6, 2).
		fixed64(7, start).fixed64(8, start+uint64(250*time.Millisecond)).
		bytes(9, kvString("http.route", "/api/users")).bytes(9, kvInt("http.response.status_code", 200)).
		bytes(15, pb{}.varint(3, 1))
	child := pb{}.bytes(1, mustHex(traceA)).bytes(2, mustHex(spanB)).bytes(4, mustHex(spanA)).str(5, "SELECT users").varint(6, 3).
		fixed64(7, start+uint64(10*time.Millisecond)).fixed64(8, start+uint64(60*time.Millisecond)).
		bytes(9, kvString("db.system", "postgresql")).
		bytes(11, pb{}.fixed64(1, start+uint64(20*time.Millisecond)).str(2, "exception").bytes(3, kvString("exception.message", "boom"))).
		bytes(15, pb{}.str(2, "query failed").varint(3, 2))
	res := pb{}
	for _, kv := range resource {
		res = res.bytes(1, kv)
	}
	scope := pb{}.bytes(1, pb{}.str(1, "test-scope")).bytes(2, root).bytes(2, child)
	rs := pb{}.bytes(1, res).bytes(2, scope)
	return pb{}.bytes(1, rs)
}

func TestDecodeProtoRequest(t *testing.T) {
	req, err := decodeProtoRequest(exportRequest(kvString("service.name", "api")))
	if err != nil {
		t.Fatal(err)
	}
	if len(req) != 1 || len(req[0].Scopes) != 1 || len(req[0].Scopes[0].Spans) != 2 {
		t.Fatalf("shape: %+v", req)
	}
	if kvMap(req[0].Resource)["service.name"] != "api" || req[0].Scopes[0].Scope != "test-scope" {
		t.Errorf("resource/scope: %+v", req[0])
	}
	root, child := req[0].Scopes[0].Spans[0], req[0].Scopes[0].Spans[1]
	if hex.EncodeToString(root.TraceID) != traceA || root.Name != "GET /api/users" || root.Kind != 2 || root.StatusCode != 1 {
		t.Errorf("root: %+v", root)
	}
	if a := kvMap(root.Attrs); a["http.route"] != "/api/users" || a["http.response.status_code"] != int64(200) {
		t.Errorf("root attrs: %v", a)
	}
	if hex.EncodeToString(child.ParentSpanID) != spanA || child.StatusCode != 2 || child.StatusMessage != "query failed" ||
		len(child.Events) != 1 || kvMap(child.Events[0].Attrs)["exception.message"] != "boom" {
		t.Errorf("child: %+v", child)
	}
	// 잘린 메시지는 오류다
	b := exportRequest()
	if _, err := decodeProtoRequest(b[:len(b)-3]); err == nil {
		t.Error("truncated message decoded")
	}
}

func TestDecodeProtoLimits(t *testing.T) {
	sp := pb{}.bytes(1, mustHex(traceA)).bytes(2, mustHex(spanA)).str(5, strings.Repeat("x", maxValueBytes+10))
	for i := 0; i < maxAttributes+5; i++ {
		sp = sp.bytes(9, kvInt("k"+string(rune('a'+i%26))+strings.Repeat("_", i/26), int64(i)))
	}
	sp = sp.bytes(9, pb{}.str(1, "d").bytes(2, pb{}.fixed64(4, math.Float64bits(1.5))))
	got, err := decodeProtoSpan(sp)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Name) != maxValueBytes || len(got.Attrs) != maxAttributes || got.Dropped != 6 {
		t.Errorf("limits: name=%d attrs=%d dropped=%d", len(got.Name), len(got.Attrs), got.Dropped)
	}
}

func TestDecodeJSONRequest(t *testing.T) {
	body := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"api"}}]},
		"scopeSpans":[{"scope":{"name":"s"},"spans":[{"traceId":"` + traceA + `","spanId":"` + spanA + `","name":"GET /",
		"kind":"SPAN_KIND_SERVER","startTimeUnixNano":"1700000000000000000","endTimeUnixNano":1700000000500000000,
		"attributes":[{"key":"n","value":{"intValue":"42"}},{"key":"f","value":{"doubleValue":0.5}},{"key":"b","value":{"boolValue":true}},
		{"key":"arr","value":{"arrayValue":{"values":[{"stringValue":"x"}]}}}],
		"status":{"code":2,"message":"bad"}}]}]}]}`
	req, err := decodeJSONRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	sp := req[0].Scopes[0].Spans[0]
	a := kvMap(sp.Attrs)
	if sp.Kind != 2 || sp.StatusCode != 2 || sp.End-sp.Start != 5e8 || a["n"] != int64(42) || a["f"] != 0.5 || a["b"] != true {
		t.Errorf("span: %+v attrs=%v", sp, a)
	}
	if _, err := decodeJSONRequest([]byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"zz"}]}]}]}`)); err == nil {
		t.Error("bad hex accepted")
	}
}

func TestEncodeExportResponse(t *testing.T) {
	if len(encodeExportResponse(0, "")) != 0 {
		t.Error("empty response should be empty")
	}
	b := encodeExportResponse(3, "rate")
	var rejected uint64
	var msg string
	err := fields(b, func(num, wire int, r *pbReader) (bool, error) {
		v, err := r.bytes()
		return true, fields(v, func(num, wire int, r *pbReader) (bool, error) {
			if num == 1 {
				rejected, err = r.varint()
				return true, err
			}
			m, err := r.bytes()
			msg = string(m)
			return true, err
		})
	})
	if err != nil || rejected != 3 || msg != "rate" {
		t.Errorf("partial success: %d %q %v", rejected, msg, err)
	}
}

// ---- 수신기 ----

type fixture struct {
	st    *store.Store
	env   *store.Environment
	p     *store.Project
	svc   *store.Service
	token string
	h     *Receiver
	srv   *httptest.Server
	ctx   context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &fixture{st: st, ctx: ctx, p: &store.Project{Name: "acme"}}
	if f.env, err = st.CreateProject(ctx, f.p); err != nil {
		t.Fatal(err)
	}
	f.svc = &store.Service{ProjectID: f.p.ID, Name: "api", SourceType: "image"}
	if err := st.CreateService(ctx, f.svc); err != nil {
		t.Fatal(err)
	}
	in := &store.Instance{ServiceID: f.svc.ID, EnvironmentID: f.env.ID}
	if err := st.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	setting, err := st.SetTracingSetting(ctx, in.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	f.token = setting.Token
	f.h = &Receiver{Store: st, ProxyToken: "proxy-secret"}
	f.srv = httptest.NewServer(f.h)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fixture) post(t *testing.T, path, ctype, token string, body []byte) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", f.srv.URL+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", ctype)
	if token != "" {
		req.Header.Set(TokenHeader, token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestReceiverProtobuf(t *testing.T) {
	f := newFixture(t)
	if r := f.post(t, "/v1/traces", "application/x-protobuf", "", exportRequest()); r.StatusCode != 401 {
		t.Errorf("no token: %d", r.StatusCode)
	}
	if r := f.post(t, "/v1/traces", "application/x-protobuf", "wrong", exportRequest()); r.StatusCode != 401 {
		t.Errorf("bad token: %d", r.StatusCode)
	}
	if r := f.post(t, "/v1/traces", "text/plain", f.token, exportRequest()); r.StatusCode != 415 {
		t.Errorf("content type: %d", r.StatusCode)
	}
	if r := f.post(t, "/v1/metrics", "application/x-protobuf", f.token, nil); r.StatusCode != 404 {
		t.Errorf("metrics: %d", r.StatusCode)
	}
	// 앱이 다른 서비스/환경을 사칭해도 토큰의 서비스로 저장한다. 배포/replica는 resource 속성을 쓴다.
	body := exportRequest(kvString("service.name", "api-sdk"), kvString("keel.environment.id", "env_other"),
		kvString("keel.service.id", "svc_other"), kvString("keel.replica.id", "api-dep-1"), kvString("keel.deployment.id", "dep_x"))
	r := f.post(t, "/v1/traces", "application/x-protobuf", f.token, body)
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "application/x-protobuf" {
		b, _ := io.ReadAll(r.Body)
		t.Fatalf("export: %d %s", r.StatusCode, b)
	}
	spans, err := f.st.GetTraceSpans(f.ctx, f.env.ID, traceA, 0)
	if err != nil || len(spans) != 2 {
		t.Fatalf("stored: %v %d", err, len(spans))
	}
	sp := spans[0]
	if sp.ServiceID != f.svc.ID || sp.EnvironmentID != f.env.ID || sp.ProjectID != f.p.ID || sp.ServiceName != "api" ||
		sp.Component != "service" || sp.ReplicaID != "api-dep-1" || sp.DeploymentID != "dep_x" || sp.Kind != "server" || sp.Status != "ok" {
		t.Errorf("identity: %+v", sp)
	}
	if sp.Attributes["http.response.status_code"] != int64(200) || sp.DurationMS != 250 {
		t.Errorf("attrs/duration: %v %v", sp.Attributes, sp.DurationMS)
	}
	if spans[1].Status != "error" || spans[1].ParentSpanID != spanA || len(spans[1].Events) != 1 || spans[1].Attributes["otel.status_description"] != "query failed" {
		t.Errorf("child: %+v", spans[1])
	}
	// tracing을 끄면 같은 토큰은 거절된다
	in, _ := f.st.GetInstanceFor(f.ctx, f.svc.ID, f.env.ID)
	f.st.SetTracingSetting(f.ctx, in.ID, false)
	if r := f.post(t, "/v1/traces", "application/x-protobuf", f.token, body); r.StatusCode != 401 {
		t.Errorf("disabled: %d", r.StatusCode)
	}
}

func TestReceiverJSONAndGzip(t *testing.T) {
	f := newFixture(t)
	body := `{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"` + traceA + `","spanId":"` + spanA + `","name":"job",
		"startTimeUnixNano":"1","endTimeUnixNano":"2"}]}]}]}`
	r := f.post(t, "/v1/traces", "application/json", f.token, []byte(body))
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != 200 || string(b) != "{}" {
		t.Fatalf("json export: %d %s", r.StatusCode, b)
	}
	if r := f.post(t, "/v1/traces", "application/json", f.token, []byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"`+traceA+`","spanId":"0000000000000000"}]}]}]}`)); r.StatusCode != 400 {
		t.Errorf("zero span id: %d", r.StatusCode)
	}
}

func TestReceiverGRPC(t *testing.T) {
	f := newFixture(t)
	msg := exportRequest()
	frame := append([]byte{0, 0, 0, 0, 0}, msg...)
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(msg)))
	req := httptest.NewRequest("POST", grpcExportPath, bytes.NewReader(frame))
	req.Header.Set("Content-Type", "application/grpc")
	req.Header.Set("x-keel-trace-token", f.token)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, req)
	if w.Code != 200 || w.Result().Trailer.Get("Grpc-Status") != "0" && w.Header().Get("Grpc-Status") != "0" {
		t.Fatalf("grpc: %d %v %v", w.Code, w.Header(), w.Result().Trailer)
	}
	if spans, _ := f.st.GetTraceSpans(f.ctx, f.env.ID, traceA, 0); len(spans) != 2 {
		t.Errorf("grpc spans: %d", len(spans))
	}
	req = httptest.NewRequest("POST", grpcExportPath, bytes.NewReader(frame))
	req.Header.Set("Content-Type", "application/grpc")
	w = httptest.NewRecorder()
	f.h.ServeHTTP(w, req)
	if got := w.Header().Get("Grpc-Status"); got != "16" {
		t.Errorf("grpc without token: status %q", got)
	}
}

func TestReceiverProxySpans(t *testing.T) {
	f := newFixture(t)
	edge := func(env, project string) []byte {
		start := uint64(time.Now().UnixNano())
		sp := pb{}.bytes(1, mustHex(traceA)).bytes(2, mustHex("1111111111111111")).str(5, "GET /").varint(6, 2).
			fixed64(7, start).fixed64(8, start+1e6).
			bytes(9, kvString("keel.project.id", project)).bytes(9, kvString("keel.environment.id", env)).
			bytes(9, kvString("keel.service.id", f.svc.ID)).bytes(9, kvString("keel.service.name", "api")).
			bytes(9, kvString("keel.component", "edge"))
		other := pb{}.bytes(1, mustHex(traceA)).bytes(2, mustHex("2222222222222222")).str(5, "untagged").fixed64(7, start).fixed64(8, start)
		return pb{}.bytes(1, pb{}.bytes(2, pb{}.bytes(2, sp).bytes(2, other)))
	}
	if r := f.post(t, "/v1/traces", "application/x-protobuf", "proxy-secret", edge(f.env.ID, f.p.ID)); r.StatusCode != 200 {
		t.Fatalf("edge export: %d", r.StatusCode)
	}
	spans, _ := f.st.GetTraceSpans(f.ctx, f.env.ID, traceA, 0)
	if len(spans) != 1 || spans[0].Component != "edge" || spans[0].ServiceID != f.svc.ID {
		t.Fatalf("edge spans: %+v", spans)
	}
	// 없는 환경이나 프로젝트가 맞지 않는 span은 partial success로 버린다
	r := f.post(t, "/v1/traces", "application/x-protobuf", "proxy-secret", edge(f.env.ID, "prj_other"))
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != 200 || len(b) == 0 {
		t.Errorf("mismatched project: %d %x", r.StatusCode, b)
	}
}

func TestReceiverRateLimit(t *testing.T) {
	f := newFixture(t)
	now := time.Unix(1700000000, 0)
	f.h.now = func() time.Time { return now }
	mk := func(n int) []store.TraceSpan {
		out := make([]store.TraceSpan, n)
		for i := range out {
			out[i] = store.TraceSpan{ServiceID: "s", EnvironmentID: "e", ReplicaID: "r1"}
		}
		return out
	}
	kept, rejected := f.h.limit(mk(ReplicaSpanLimit - 10))
	if len(kept) != ReplicaSpanLimit-10 || rejected != 0 {
		t.Fatalf("first: %d %d", len(kept), rejected)
	}
	kept, rejected = f.h.limit(mk(25))
	if len(kept) != 10 || rejected != 15 {
		t.Errorf("over limit: %d %d", len(kept), rejected)
	}
	now = now.Add(RateWindow)
	if kept, rejected = f.h.limit(mk(5)); len(kept) != 5 || rejected != 0 {
		t.Errorf("next window: %d %d", len(kept), rejected)
	}
}

// ---- 필터 ----

func TestFilter(t *testing.T) {
	sp := store.TraceSpan{TraceID: traceA, SpanID: spanA, Name: "SELECT users", ServiceID: "svc_1", ServiceName: "api",
		DeploymentID: "dep_1", Component: "service", Kind: "client", Status: "error", DurationMS: 620,
		Attributes:         map[string]any{"http.route": "/checkout", "http.response.status_code": int64(500), "cart.items": int64(12), "flag": true},
		ResourceAttributes: map[string]any{"service.name": "api-sdk", "region": "kr"}}
	for q, want := range map[string]bool{
		"":                              true,
		"select":                        true,
		"insert":                        false,
		"@status:error":                 true,
		"@status:ok":                    false,
		"@service:api AND @kind:client": true,
		"@service:svc_1":                true,
		"@serviceName:api-sdk":          true,
		"@serviceName:api":              false,
		"@component:edge":               false,
		"@name:SELECT*":                 true,
		"@name:SELECT":                  false,
		"@duration:>500":                true,
		"@duration:<500":                false,
		"@duration:600-650":             true,
		"@duration:>=620":               true,
		"@http.route:/checkout @http.response.status_code:500": true,
		"@cart.items:>10":            true,
		"@cart.items:<10":            false,
		"@region:kr":                 true,
		"@flag:true":                 true,
		"@missing:x":                 false,
		"-@status:error":             false,
		"@status:ok OR @kind:client": true,
		"(@status:ok OR @kind:server) @service:api": false,
		"-(@status:ok OR @kind:server)":             true,
		`@name:"SELECT users"`:                      true,
		"@trace:" + strings.ToUpper(traceA):         true,
		"@deployment:dep_1":                         true,
	} {
		f, err := ParseFilter(q)
		if err != nil {
			t.Errorf("%q: %v", q, err)
			continue
		}
		if got := f.Match(&sp); got != want {
			t.Errorf("%q: got %v want %v", q, got, want)
		}
	}
	for _, bad := range []string{"(@status:error", `@name:"x`, "@a:b OR", "()"} {
		if _, err := ParseFilter(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

// 실제 h2c 연결로 gRPC trailer(grpc-status)가 오는지 본다.
func TestReceiverGRPCOverH2C(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewUnstartedServer(f.h)
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetHTTP1(true)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	defer srv.Close()
	tr := &http.Transport{Protocols: new(http.Protocols)}
	tr.Protocols.SetUnencryptedHTTP2(true)
	client := &http.Client{Transport: tr}
	msg := exportRequest()
	frame := append([]byte{0, 0, 0, 0, 0}, msg...)
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(msg)))
	req, _ := http.NewRequest("POST", srv.URL+grpcExportPath, bytes.NewReader(frame))
	req.Header.Set("Content-Type", "application/grpc")
	req.Header.Set("TE", "trailers")
	req.Header.Set("x-keel-trace-token", f.token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.ProtoMajor != 2 || resp.Trailer.Get("Grpc-Status") != "0" || len(body) != 5 {
		t.Fatalf("h2c grpc: proto=%d trailer=%v body=%x", resp.ProtoMajor, resp.Trailer, body)
	}
}
