// Package tracing는 OTLP trace 수신기(OTLP/HTTP protobuf·JSON, OTLP/gRPC)와 trace 검색 문법을 제공한다.
//
// 서비스 컨테이너는 tracing을 켠 인스턴스마다 발급한 토큰(X-Keel-Trace-Token)으로 span을 보낸다. 수신기는 토큰으로
// 프로젝트/환경/서비스를 정하므로 앱이 보낸 resource 속성으로 다른 서비스를 사칭할 수 없다. keel-proxy(Caddy)의 edge span은
// 서버 전용 proxy 토큰으로 오고, 라우트에 붙인 aven.* span 속성으로 서비스를 정한다.
package tracing

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

const (
	MaxRequestBytes = 4 << 20
	// MaxSpansPerRequest는 export 한 번에 받는 span 수 상한이다.
	MaxSpansPerRequest = 10000
	// ReplicaSpanLimit은 replica 하나가 RateWindow 동안 보낼 수 있는 span 수다(넘치면 partial success로 거절).
	ReplicaSpanLimit = 1000
	RateWindow       = 10 * time.Second
	// TokenHeader는 수신기가 요구하는 자격 증명 헤더다(OTEL_EXPORTER_OTLP_HEADERS로 주입).
	TokenHeader       = "X-Aven-Trace-Token"
	LegacyTokenHeader = "X-Keel-Trace-Token"

	TraceIDHeader       = "X-Aven-Trace-Id"
	LegacyTraceIDHeader = "X-Keel-Trace-Id"

	grpcExportPath = "/opentelemetry.proto.collector.trace.v1.TraceService/Export"
)

// DefaultRetention은 span 보관 기간 기본값이다(KEEL_TRACE_RETENTION으로 바꾼다).
const DefaultRetention = 7 * 24 * time.Hour

type Receiver struct {
	Store      *store.Store
	ProxyToken string
	Log        *slog.Logger

	mu      sync.Mutex
	windows map[string]*rateWindow
	envs    map[string]envCacheEntry
	now     func() time.Time
}

type rateWindow struct {
	start time.Time
	n     int
}

type envCacheEntry struct {
	projectID string
	ok        bool
	at        time.Time
}

// exportError는 OTLP 응답 코드로 바꿀 오류다.
type exportError struct {
	status int // HTTP 상태
	grpc   int // gRPC 상태 코드
	msg    string
}

func (e *exportError) Error() string { return e.msg }

func badRequest(format string, a ...any) *exportError {
	return &exportError{status: http.StatusBadRequest, grpc: 3, msg: fmt.Sprintf(format, a...)}
}

var errUnauthorized = &exportError{status: http.StatusUnauthorized, grpc: 16, msg: "missing or invalid " + TokenHeader}

func (h *Receiver) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

func (h *Receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == grpcExportPath || strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
		h.serveGRPC(w, r)
		return
	}
	if r.URL.Path != "/v1/traces" {
		if r.URL.Path == "/v1/metrics" || r.URL.Path == "/v1/logs" {
			// SDK가 metrics/logs도 보내면 거절한다. OTEL_METRICS_EXPORTER=none / OTEL_LOGS_EXPORTER=none으로 끈다.
			http.Error(w, "aven accepts traces only; set OTEL_METRICS_EXPORTER=none and OTEL_LOGS_EXPORTER=none", http.StatusNotFound)
			return
		}
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (media != "application/x-protobuf" && media != "application/json") {
		http.Error(w, "unsupported content type (use application/x-protobuf or application/json)", http.StatusUnsupportedMediaType)
		return
	}
	body := io.Reader(r.Body)
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, "invalid gzip body", http.StatusBadRequest)
			return
		}
		defer gz.Close()
		body = gz
	}
	payload, err := io.ReadAll(io.LimitReader(body, MaxRequestBytes+1))
	if err != nil {
		http.Error(w, "could not read OTLP request", http.StatusBadRequest)
		return
	}
	if len(payload) > MaxRequestBytes {
		http.Error(w, "OTLP request exceeds 4 MiB", http.StatusRequestEntityTooLarge)
		return
	}
	rejected, msg, xerr := h.export(r.Context(), requestToken(r), payload, media == "application/json")
	if xerr != nil {
		http.Error(w, xerr.msg, xerr.status)
		return
	}
	if media == "application/json" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(encodeExportResponseJSON(rejected, msg))
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	w.Write(encodeExportResponse(rejected, msg))
}

// serveGRPC는 OTLP/gRPC TraceService.Export 하나만 구현한다(h2c, unary 호출).
func (h *Receiver) serveGRPC(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/grpc")
	w.Header().Set("Trailer", "Grpc-Status, Grpc-Message")
	fail := func(code int, msg string) {
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Grpc-Status", fmt.Sprint(code))
		w.Header().Set("Grpc-Message", msg)
	}
	if r.Method != http.MethodPost || r.URL.Path != grpcExportPath {
		fail(12, "unimplemented: aven accepts traces only")
		return
	}
	frame, err := io.ReadAll(io.LimitReader(r.Body, MaxRequestBytes+6))
	if err != nil || len(frame) < 5 {
		fail(3, "invalid gRPC frame")
		return
	}
	if len(frame) > MaxRequestBytes+5 {
		fail(8, "OTLP request exceeds 4 MiB")
		return
	}
	n := binary.BigEndian.Uint32(frame[1:5])
	if uint64(n) != uint64(len(frame)-5) {
		fail(3, "invalid gRPC frame length")
		return
	}
	payload := frame[5:]
	if frame[0] == 1 {
		if !strings.EqualFold(r.Header.Get("Grpc-Encoding"), "gzip") {
			fail(12, "unsupported grpc-encoding")
			return
		}
		gz, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			fail(3, "invalid gzip message")
			return
		}
		payload, err = io.ReadAll(io.LimitReader(gz, MaxRequestBytes+1))
		gz.Close()
		if err != nil || len(payload) > MaxRequestBytes {
			fail(3, "invalid gzip message")
			return
		}
	}
	rejected, msg, xerr := h.export(r.Context(), requestToken(r), payload, false)
	if xerr != nil {
		fail(xerr.grpc, xerr.msg)
		return
	}
	resp := encodeExportResponse(rejected, msg)
	out := make([]byte, 5, 5+len(resp))
	binary.BigEndian.PutUint32(out[1:], uint32(len(resp)))
	out = append(out, resp...)
	w.WriteHeader(http.StatusOK)
	w.Write(out)
	w.Header().Set("Grpc-Status", "0")
	w.Header().Set("Grpc-Message", "")
}

func requestToken(r *http.Request) string {
	if t := r.Header.Get(TokenHeader); t != "" {
		return t
	}
	if t := r.Header.Get(LegacyTokenHeader); t != "" {
		return t
	}
	if a := r.Header.Get("Authorization"); len(a) > 7 && strings.EqualFold(a[:7], "bearer ") {
		return strings.TrimSpace(a[7:])
	}
	return ""
}

// export는 OTLP 요청을 해석해 저장한다. 속도 제한이나 알 수 없는 서비스 때문에 버린 span 수를 돌려준다.
func (h *Receiver) export(ctx context.Context, token string, payload []byte, isJSON bool) (int64, string, *exportError) {
	if token == "" {
		return 0, "", errUnauthorized
	}
	isProxy := h.ProxyToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(h.ProxyToken)) == 1
	var identity *store.TraceIdentity
	if !isProxy {
		id, err := h.Store.LookupTraceToken(ctx, token)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) && h.Log != nil {
				h.Log.Warn("trace token lookup", "err", err)
			}
			return 0, "", errUnauthorized
		}
		identity = id
	}
	var req []otlpResourceSpans
	var err error
	if isJSON {
		req, err = decodeJSONRequest(payload)
	} else {
		req, err = decodeProtoRequest(payload)
	}
	if err != nil {
		return 0, "", badRequest("invalid OTLP trace payload: %v", err)
	}
	spans, err := convertRequest(req, identity, isProxy)
	if err != nil {
		return 0, "", badRequest("%v", err)
	}
	var rejected int64
	var msg string
	if isProxy {
		kept := spans[:0]
		for _, sp := range spans {
			if h.knownEnvironment(ctx, sp.ProjectID, sp.EnvironmentID) {
				kept = append(kept, sp)
			} else {
				rejected++
			}
		}
		spans = kept
		if rejected > 0 {
			msg = "spans for unknown environments were dropped"
		}
	} else {
		spans, rejected = h.limit(spans)
		if rejected > 0 {
			msg = fmt.Sprintf("rate limit: %d spans per %s per replica", ReplicaSpanLimit, RateWindow)
		}
	}
	if len(spans) > 0 {
		if err := h.Store.InsertTraceSpans(ctx, spans); err != nil {
			if h.Log != nil {
				h.Log.Error("store trace spans", "err", err)
			}
			return 0, "", &exportError{status: http.StatusServiceUnavailable, grpc: 14, msg: "could not store trace spans"}
		}
	}
	return rejected, msg, nil
}

// limit은 replica별 고정 창 속도 제한을 적용한다.
func (h *Receiver) limit(spans []store.TraceSpan) ([]store.TraceSpan, int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.windows == nil {
		h.windows = map[string]*rateWindow{}
	}
	now := h.clock()
	if len(h.windows) > 10000 { // 오래된 창 정리
		for k, w := range h.windows {
			if now.Sub(w.start) >= RateWindow {
				delete(h.windows, k)
			}
		}
	}
	kept := spans[:0]
	var rejected int64
	for _, sp := range spans {
		key := sp.ServiceID + "/" + sp.EnvironmentID + "/" + sp.ReplicaID
		w := h.windows[key]
		if w == nil || now.Sub(w.start) >= RateWindow {
			w = &rateWindow{start: now}
			h.windows[key] = w
		}
		if w.n >= ReplicaSpanLimit {
			rejected++
			continue
		}
		w.n++
		kept = append(kept, sp)
	}
	return kept, rejected
}

// knownEnvironment는 proxy가 붙인 환경이 실제로 있고 프로젝트가 맞는지 확인한다(1분 캐시).
func (h *Receiver) knownEnvironment(ctx context.Context, projectID, envID string) bool {
	now := h.clock()
	h.mu.Lock()
	if h.envs == nil {
		h.envs = map[string]envCacheEntry{}
	}
	e, hit := h.envs[envID]
	h.mu.Unlock()
	if !hit || now.Sub(e.at) > time.Minute {
		env, err := h.Store.GetEnvironment(ctx, envID)
		e = envCacheEntry{at: now, ok: err == nil}
		if err == nil {
			e.projectID = env.ProjectID
		}
		h.mu.Lock()
		h.envs[envID] = e
		h.mu.Unlock()
	}
	return e.ok && e.projectID == projectID
}

func convertRequest(req []otlpResourceSpans, identity *store.TraceIdentity, isProxy bool) ([]store.TraceSpan, error) {
	var out []store.TraceSpan
	for _, rs := range req {
		resource := kvMap(rs.Resource)
		for _, ss := range rs.Scopes {
			for _, raw := range ss.Spans {
				if len(out) >= MaxSpansPerRequest {
					return nil, fmt.Errorf("OTLP request contains more than %d spans", MaxSpansPerRequest)
				}
				if len(raw.TraceID) != 16 || len(raw.SpanID) != 8 || allZero(raw.TraceID) || allZero(raw.SpanID) {
					return nil, errors.New("invalid OTLP trace or span ID")
				}
				if len(raw.ParentSpanID) != 0 && (len(raw.ParentSpanID) != 8 || allZero(raw.ParentSpanID)) {
					return nil, errors.New("invalid OTLP parent span ID")
				}
				if raw.Start > 1<<63-1 || raw.End > 1<<63-1 || raw.End < raw.Start {
					return nil, errors.New("invalid OTLP span time range")
				}
				attrs := kvMap(raw.Attrs)
				var id store.TraceIdentity
				if isProxy {
					id = proxyIdentity(attrs, resource)
					if id.EnvironmentID == "" || id.ProjectID == "" {
						continue // aven 라우트가 아닌 span(속성 없음)은 버린다
					}
				} else {
					id = *identity
					// 배포/replica는 컨테이너마다 다르므로 주입한 resource 속성을 쓴다(서비스 범위는 토큰이 정한다).
					if v := stringValue(resource["keel.deployment.id"]); v != "" {
						id.DeploymentID = v
					}
					if v := stringValue(resource["keel.replica.id"]); v != "" {
						id.ReplicaID = v
					}
				}
				if id.ServiceName == "" {
					id.ServiceName = stringValue(resource["service.name"])
				}
				if id.Component == "" {
					id.Component = "service"
				}
				if raw.Dropped > 0 {
					attrs["keel.dropped_count"] = int64(raw.Dropped)
				}
				if raw.StatusMessage != "" {
					attrs["otel.status_description"] = raw.StatusMessage
				}
				if ss.Scope != "" {
					attrs["otel.scope.name"] = ss.Scope
				}
				start := time.Unix(0, int64(raw.Start)).UTC()
				end := time.Unix(0, int64(raw.End)).UTC()
				out = append(out, store.TraceSpan{
					TraceID: hex.EncodeToString(raw.TraceID), SpanID: hex.EncodeToString(raw.SpanID),
					ParentSpanID: hex.EncodeToString(raw.ParentSpanID), ProjectID: id.ProjectID, EnvironmentID: id.EnvironmentID,
					ServiceID: id.ServiceID, ServiceName: id.ServiceName, DeploymentID: id.DeploymentID,
					ReplicaID: id.ReplicaID, Component: id.Component, Name: raw.Name, Kind: spanKind(raw.Kind),
					Status: spanStatus(raw.StatusCode), StartTime: start, EndTime: end,
					DurationMS: float64(end.Sub(start)) / float64(time.Millisecond), Attributes: attrs,
					ResourceAttributes: resource, Events: convertEvents(raw.Events), Links: convertLinks(raw.Links),
				})
			}
		}
	}
	return out, nil
}

func proxyIdentity(attrs, resource map[string]any) store.TraceIdentity {
	value := func(key string) string {
		if v := stringValue(attrs[key]); v != "" {
			return v
		}
		return stringValue(resource[key])
	}
	component := value("keel.component")
	if component == "" {
		component = "edge"
	}
	return store.TraceIdentity{
		ProjectID: value("keel.project.id"), EnvironmentID: value("keel.environment.id"),
		ServiceID: value("keel.service.id"), ServiceName: value("keel.service.name"),
		DeploymentID: value("keel.deployment.id"), ReplicaID: value("keel.replica.id"), Component: component,
	}
}

func allZero(value []byte) bool {
	for _, b := range value {
		if b != 0 {
			return false
		}
	}
	return true
}

func stringValue(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func spanKind(kind int) string {
	switch kind {
	case 2:
		return "server"
	case 3:
		return "client"
	case 4:
		return "producer"
	case 5:
		return "consumer"
	default:
		return "internal"
	}
}

func spanStatus(code int) string {
	switch code {
	case 1:
		return "ok"
	case 2:
		return "error"
	default:
		return "unset"
	}
}

func convertEvents(events []otlpEvent) []store.TraceEvent {
	out := make([]store.TraceEvent, 0, len(events))
	for _, ev := range events {
		if ev.Time > 1<<63-1 {
			continue
		}
		out = append(out, store.TraceEvent{Name: ev.Name, Time: time.Unix(0, int64(ev.Time)).UTC(), Attributes: kvMap(ev.Attrs)})
	}
	return out
}

func convertLinks(links []otlpLink) []store.TraceLink {
	out := make([]store.TraceLink, 0, len(links))
	for _, ln := range links {
		if len(ln.TraceID) != 16 || len(ln.SpanID) != 8 {
			continue
		}
		out = append(out, store.TraceLink{TraceID: hex.EncodeToString(ln.TraceID), SpanID: hex.EncodeToString(ln.SpanID), Attributes: kvMap(ln.Attrs)})
	}
	return out
}

// RetentionLoop는 시작할 때와 매시간 retention보다 오래된 span을 지운다.
func RetentionLoop(ctx context.Context, st *store.Store, retention time.Duration, log *slog.Logger) {
	if retention <= 0 {
		retention = DefaultRetention
	}
	prune := func() {
		if err := st.PruneTraceSpans(context.WithoutCancel(ctx), time.Now().Add(-retention)); err != nil && log != nil {
			log.Warn("prune trace spans", "err", err)
		}
	}
	prune()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
}
