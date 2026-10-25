package tracing

// OTLP trace 요청(ExportTraceServiceRequest)을 protobuf 바이너리와 OTLP/JSON 둘 다 직접 해석한다.
// 의존성(grpc, protobuf 런타임)을 늘리지 않으려고 필요한 필드만 wire format에서 읽는다.
// 스키마: https://github.com/open-telemetry/opentelemetry-proto/blob/main/opentelemetry/proto/trace/v1/trace.proto

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	maxAttributes  = 128
	maxEvents      = 128
	maxLinks       = 128
	maxValueBytes  = 16 * 1024
	maxNestedDepth = 8
)

type keyValue struct {
	Key   string
	Value any
}

type otlpEvent struct {
	Time  uint64
	Name  string
	Attrs []keyValue
}

type otlpLink struct {
	TraceID, SpanID []byte
	Attrs           []keyValue
}

type otlpSpan struct {
	TraceID, SpanID, ParentSpanID []byte
	Name                          string
	Kind                          int
	Start, End                    uint64
	Attrs                         []keyValue
	Events                        []otlpEvent
	Links                         []otlpLink
	StatusCode                    int
	StatusMessage                 string
	Dropped                       int // 상한을 넘어 버린 속성/이벤트/링크 수
}

type otlpScopeSpans struct {
	Scope string
	Spans []otlpSpan
}

type otlpResourceSpans struct {
	Resource []keyValue
	Scopes   []otlpScopeSpans
}

// ---------------- protobuf ----------------

var errTruncated = errors.New("truncated protobuf message")

type pbReader struct {
	b []byte
}

func (r *pbReader) varint() (uint64, error) {
	v, n := binary.Uvarint(r.b)
	if n <= 0 {
		return 0, errTruncated
	}
	r.b = r.b[n:]
	return v, nil
}

// next는 다음 필드 번호와 wire type을 읽는다.
func (r *pbReader) next() (int, int, error) {
	k, err := r.varint()
	if err != nil {
		return 0, 0, err
	}
	if k>>3 == 0 || k>>3 > math.MaxInt32 {
		return 0, 0, errors.New("invalid protobuf field number")
	}
	return int(k >> 3), int(k & 7), nil
}

func (r *pbReader) bytes() ([]byte, error) {
	n, err := r.varint()
	if err != nil {
		return nil, err
	}
	if n > uint64(len(r.b)) {
		return nil, errTruncated
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v, nil
}

func (r *pbReader) fixed64() (uint64, error) {
	if len(r.b) < 8 {
		return 0, errTruncated
	}
	v := binary.LittleEndian.Uint64(r.b)
	r.b = r.b[8:]
	return v, nil
}

func (r *pbReader) skip(wire int) error {
	switch wire {
	case 0:
		_, err := r.varint()
		return err
	case 1:
		_, err := r.fixed64()
		return err
	case 2:
		_, err := r.bytes()
		return err
	case 5:
		if len(r.b) < 4 {
			return errTruncated
		}
		r.b = r.b[4:]
		return nil
	default:
		return fmt.Errorf("unsupported protobuf wire type %d", wire)
	}
}

// fields는 메시지 b의 필드를 차례로 f에 넘긴다. f가 처리하지 않은 필드(false)는 건너뛴다.
func fields(b []byte, f func(num, wire int, r *pbReader) (bool, error)) error {
	r := &pbReader{b: b}
	for len(r.b) > 0 {
		num, wire, err := r.next()
		if err != nil {
			return err
		}
		handled, err := f(num, wire, r)
		if err != nil {
			return err
		}
		if !handled {
			if err := r.skip(wire); err != nil {
				return err
			}
		}
	}
	return nil
}

func wantWire(got, want int) error {
	if got != want {
		return fmt.Errorf("unexpected protobuf wire type %d (want %d)", got, want)
	}
	return nil
}

// decodeProtoRequest는 ExportTraceServiceRequest 바이너리를 해석한다.
func decodeProtoRequest(b []byte) ([]otlpResourceSpans, error) {
	var out []otlpResourceSpans
	err := fields(b, func(num, wire int, r *pbReader) (bool, error) {
		if num != 1 {
			return false, nil
		}
		if err := wantWire(wire, 2); err != nil {
			return true, err
		}
		msg, err := r.bytes()
		if err != nil {
			return true, err
		}
		rs, err := decodeProtoResourceSpans(msg)
		out = append(out, rs)
		return true, err
	})
	return out, err
}

func decodeProtoResourceSpans(b []byte) (otlpResourceSpans, error) {
	var rs otlpResourceSpans
	err := fields(b, func(num, wire int, r *pbReader) (bool, error) {
		if wire != 2 || (num != 1 && num != 2) {
			return false, nil
		}
		msg, err := r.bytes()
		if err != nil {
			return true, err
		}
		if num == 1 { // Resource
			return true, fields(msg, func(num, wire int, r *pbReader) (bool, error) {
				if num != 1 || wire != 2 {
					return false, nil
				}
				kv, err := r.bytes()
				if err != nil {
					return true, err
				}
				p, err := decodeProtoKeyValue(kv, 0)
				rs.Resource = appendAttr(rs.Resource, p, nil)
				return true, err
			})
		}
		ss, err := decodeProtoScopeSpans(msg)
		rs.Scopes = append(rs.Scopes, ss)
		return true, err
	})
	return rs, err
}

func decodeProtoScopeSpans(b []byte) (otlpScopeSpans, error) {
	var ss otlpScopeSpans
	err := fields(b, func(num, wire int, r *pbReader) (bool, error) {
		if wire != 2 || (num != 1 && num != 2) {
			return false, nil
		}
		msg, err := r.bytes()
		if err != nil {
			return true, err
		}
		if num == 1 { // InstrumentationScope.name
			return true, fields(msg, func(num, wire int, r *pbReader) (bool, error) {
				if num != 1 || wire != 2 {
					return false, nil
				}
				v, err := r.bytes()
				ss.Scope = string(v)
				return true, err
			})
		}
		sp, err := decodeProtoSpan(msg)
		ss.Spans = append(ss.Spans, sp)
		return true, err
	})
	return ss, err
}

func decodeProtoSpan(b []byte) (otlpSpan, error) {
	var sp otlpSpan
	err := fields(b, func(num, wire int, r *pbReader) (bool, error) {
		switch num {
		case 1, 2, 4, 5, 9, 11, 13, 15:
			if err := wantWire(wire, 2); err != nil {
				return true, err
			}
			v, err := r.bytes()
			if err != nil {
				return true, err
			}
			switch num {
			case 1:
				sp.TraceID = v
			case 2:
				sp.SpanID = v
			case 4:
				sp.ParentSpanID = v
			case 5:
				sp.Name = truncate(string(v))
			case 9:
				p, err := decodeProtoKeyValue(v, 0)
				sp.Attrs = appendAttr(sp.Attrs, p, &sp.Dropped)
				return true, err
			case 11:
				if len(sp.Events) >= maxEvents {
					sp.Dropped++
					return true, nil
				}
				ev, err := decodeProtoEvent(v)
				sp.Events = append(sp.Events, ev)
				return true, err
			case 13:
				if len(sp.Links) >= maxLinks {
					sp.Dropped++
					return true, nil
				}
				ln, err := decodeProtoLink(v)
				sp.Links = append(sp.Links, ln)
				return true, err
			case 15:
				return true, fields(v, func(num, wire int, r *pbReader) (bool, error) {
					switch {
					case num == 2 && wire == 2:
						m, err := r.bytes()
						sp.StatusMessage = truncate(string(m))
						return true, err
					case num == 3 && wire == 0:
						c, err := r.varint()
						sp.StatusCode = int(c)
						return true, err
					}
					return false, nil
				})
			}
			return true, nil
		case 6:
			if err := wantWire(wire, 0); err != nil {
				return true, err
			}
			k, err := r.varint()
			sp.Kind = int(k)
			return true, err
		case 7, 8:
			if err := wantWire(wire, 1); err != nil {
				return true, err
			}
			t, err := r.fixed64()
			if num == 7 {
				sp.Start = t
			} else {
				sp.End = t
			}
			return true, err
		}
		return false, nil
	})
	return sp, err
}

func decodeProtoEvent(b []byte) (otlpEvent, error) {
	var ev otlpEvent
	err := fields(b, func(num, wire int, r *pbReader) (bool, error) {
		switch {
		case num == 1 && wire == 1:
			t, err := r.fixed64()
			ev.Time = t
			return true, err
		case num == 2 && wire == 2:
			v, err := r.bytes()
			ev.Name = truncate(string(v))
			return true, err
		case num == 3 && wire == 2:
			v, err := r.bytes()
			if err != nil {
				return true, err
			}
			p, err := decodeProtoKeyValue(v, 0)
			ev.Attrs = appendAttr(ev.Attrs, p, nil)
			return true, err
		}
		return false, nil
	})
	return ev, err
}

func decodeProtoLink(b []byte) (otlpLink, error) {
	var ln otlpLink
	err := fields(b, func(num, wire int, r *pbReader) (bool, error) {
		if wire != 2 || (num != 1 && num != 2 && num != 4) {
			return false, nil
		}
		v, err := r.bytes()
		if err != nil {
			return true, err
		}
		switch num {
		case 1:
			ln.TraceID = v
		case 2:
			ln.SpanID = v
		case 4:
			p, err := decodeProtoKeyValue(v, 0)
			ln.Attrs = appendAttr(ln.Attrs, p, nil)
			return true, err
		}
		return true, nil
	})
	return ln, err
}

func decodeProtoKeyValue(b []byte, depth int) (keyValue, error) {
	var kv keyValue
	err := fields(b, func(num, wire int, r *pbReader) (bool, error) {
		if wire != 2 || (num != 1 && num != 2) {
			return false, nil
		}
		v, err := r.bytes()
		if err != nil {
			return true, err
		}
		if num == 1 {
			kv.Key = string(v)
			return true, nil
		}
		kv.Value, err = decodeProtoAnyValue(v, depth+1)
		return true, err
	})
	return kv, err
}

func decodeProtoAnyValue(b []byte, depth int) (any, error) {
	if depth > maxNestedDepth {
		return nil, errors.New("attribute value nested too deeply")
	}
	var out any
	err := fields(b, func(num, wire int, r *pbReader) (bool, error) {
		switch {
		case num == 1 && wire == 2:
			v, err := r.bytes()
			out = truncate(string(v))
			return true, err
		case num == 2 && wire == 0:
			v, err := r.varint()
			out = v != 0
			return true, err
		case num == 3 && wire == 0:
			v, err := r.varint()
			out = int64(v)
			return true, err
		case num == 4 && wire == 1:
			v, err := r.fixed64()
			out = math.Float64frombits(v)
			return true, err
		case num == 5 && wire == 2: // ArrayValue
			v, err := r.bytes()
			if err != nil {
				return true, err
			}
			items := []any{}
			err = fields(v, func(num, wire int, r *pbReader) (bool, error) {
				if num != 1 || wire != 2 {
					return false, nil
				}
				iv, err := r.bytes()
				if err != nil {
					return true, err
				}
				item, err := decodeProtoAnyValue(iv, depth+1)
				if len(items) < maxAttributes {
					items = append(items, item)
				}
				return true, err
			})
			out = items
			return true, err
		case num == 6 && wire == 2: // KeyValueList
			v, err := r.bytes()
			if err != nil {
				return true, err
			}
			var kvs []keyValue
			err = fields(v, func(num, wire int, r *pbReader) (bool, error) {
				if num != 1 || wire != 2 {
					return false, nil
				}
				kb, err := r.bytes()
				if err != nil {
					return true, err
				}
				p, err := decodeProtoKeyValue(kb, depth+1)
				kvs = appendAttr(kvs, p, nil)
				return true, err
			})
			out = kvMap(kvs)
			return true, err
		case num == 7 && wire == 2:
			v, err := r.bytes()
			out = hex.EncodeToString(v[:min(len(v), maxValueBytes/2)])
			return true, err
		}
		return false, nil
	})
	return out, err
}

// ---------------- OTLP/JSON ----------------
// https://opentelemetry.io/docs/specs/otlp/#json-protobuf-encoding: 필드는 lowerCamelCase, trace/span ID는 hex,
// 64비트 정수는 문자열 또는 숫자, enum은 정수. protojson처럼 원래 이름(snake_case)과 enum 이름도 받아 준다.

type jsonNum string

func (n *jsonNum) UnmarshalJSON(b []byte) error {
	*n = jsonNum(strings.Trim(string(b), `"`))
	return nil
}

func (n jsonNum) uint64() (uint64, error) {
	if n == "" || n == "null" {
		return 0, nil
	}
	return strconv.ParseUint(string(n), 10, 64)
}

type jsonKV struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

type jsonSpan struct {
	TraceID    string          `json:"traceId"`
	TraceID2   string          `json:"trace_id"`
	SpanID     string          `json:"spanId"`
	SpanID2    string          `json:"span_id"`
	Parent     string          `json:"parentSpanId"`
	Parent2    string          `json:"parent_span_id"`
	Name       string          `json:"name"`
	Kind       json.RawMessage `json:"kind"`
	Start      jsonNum         `json:"startTimeUnixNano"`
	Start2     jsonNum         `json:"start_time_unix_nano"`
	End        jsonNum         `json:"endTimeUnixNano"`
	End2       jsonNum         `json:"end_time_unix_nano"`
	Attributes []jsonKV        `json:"attributes"`
	Events     []jsonEvent     `json:"events"`
	Links      []jsonLink      `json:"links"`
	Status     *jsonStatus     `json:"status"`
}

type jsonEvent struct {
	Time       jsonNum  `json:"timeUnixNano"`
	Time2      jsonNum  `json:"time_unix_nano"`
	Name       string   `json:"name"`
	Attributes []jsonKV `json:"attributes"`
}

type jsonLink struct {
	TraceID    string   `json:"traceId"`
	TraceID2   string   `json:"trace_id"`
	SpanID     string   `json:"spanId"`
	SpanID2    string   `json:"span_id"`
	Attributes []jsonKV `json:"attributes"`
}

type jsonStatus struct {
	Message string          `json:"message"`
	Code    json.RawMessage `json:"code"`
}

type jsonScopeSpans struct {
	Scope *struct {
		Name string `json:"name"`
	} `json:"scope"`
	Spans []jsonSpan `json:"spans"`
}

type jsonResourceSpans struct {
	Resource *struct {
		Attributes []jsonKV `json:"attributes"`
	} `json:"resource"`
	ScopeSpans  []jsonScopeSpans `json:"scopeSpans"`
	ScopeSpans2 []jsonScopeSpans `json:"scope_spans"`
}

type jsonRequest struct {
	ResourceSpans  []jsonResourceSpans `json:"resourceSpans"`
	ResourceSpans2 []jsonResourceSpans `json:"resource_spans"`
}

func or[T comparable](a, b T) T {
	var zero T
	if a != zero {
		return a
	}
	return b
}

func decodeJSONRequest(b []byte) ([]otlpResourceSpans, error) {
	var req jsonRequest
	if err := json.Unmarshal(b, &req); err != nil {
		return nil, err
	}
	var out []otlpResourceSpans
	for _, jr := range append(req.ResourceSpans, req.ResourceSpans2...) {
		var rs otlpResourceSpans
		if jr.Resource != nil {
			attrs, err := jsonAttrs(jr.Resource.Attributes, nil)
			if err != nil {
				return nil, err
			}
			rs.Resource = attrs
		}
		for _, js := range append(jr.ScopeSpans, jr.ScopeSpans2...) {
			var ss otlpScopeSpans
			if js.Scope != nil {
				ss.Scope = js.Scope.Name
			}
			for _, j := range js.Spans {
				sp, err := jsonToSpan(j)
				if err != nil {
					return nil, err
				}
				ss.Spans = append(ss.Spans, sp)
			}
			rs.Scopes = append(rs.Scopes, ss)
		}
		out = append(out, rs)
	}
	return out, nil
}

func jsonToSpan(j jsonSpan) (otlpSpan, error) {
	var sp otlpSpan
	var err error
	if sp.TraceID, err = hexID(or(j.TraceID, j.TraceID2)); err != nil {
		return sp, err
	}
	if sp.SpanID, err = hexID(or(j.SpanID, j.SpanID2)); err != nil {
		return sp, err
	}
	if sp.ParentSpanID, err = hexID(or(j.Parent, j.Parent2)); err != nil {
		return sp, err
	}
	sp.Name = truncate(j.Name)
	if sp.Kind, err = jsonEnum(j.Kind, "SPAN_KIND_", []string{"UNSPECIFIED", "INTERNAL", "SERVER", "CLIENT", "PRODUCER", "CONSUMER"}); err != nil {
		return sp, err
	}
	if sp.Start, err = or(j.Start, j.Start2).uint64(); err != nil {
		return sp, fmt.Errorf("invalid startTimeUnixNano: %w", err)
	}
	if sp.End, err = or(j.End, j.End2).uint64(); err != nil {
		return sp, fmt.Errorf("invalid endTimeUnixNano: %w", err)
	}
	if sp.Attrs, err = jsonAttrs(j.Attributes, &sp.Dropped); err != nil {
		return sp, err
	}
	for i, je := range j.Events {
		if i >= maxEvents {
			sp.Dropped++
			continue
		}
		var ev otlpEvent
		if ev.Time, err = or(je.Time, je.Time2).uint64(); err != nil {
			return sp, fmt.Errorf("invalid event timeUnixNano: %w", err)
		}
		ev.Name = truncate(je.Name)
		if ev.Attrs, err = jsonAttrs(je.Attributes, nil); err != nil {
			return sp, err
		}
		sp.Events = append(sp.Events, ev)
	}
	for i, jl := range j.Links {
		if i >= maxLinks {
			sp.Dropped++
			continue
		}
		var ln otlpLink
		if ln.TraceID, err = hexID(or(jl.TraceID, jl.TraceID2)); err != nil {
			return sp, err
		}
		if ln.SpanID, err = hexID(or(jl.SpanID, jl.SpanID2)); err != nil {
			return sp, err
		}
		if ln.Attrs, err = jsonAttrs(jl.Attributes, nil); err != nil {
			return sp, err
		}
		sp.Links = append(sp.Links, ln)
	}
	if j.Status != nil {
		sp.StatusMessage = truncate(j.Status.Message)
		if sp.StatusCode, err = jsonEnum(j.Status.Code, "STATUS_CODE_", []string{"UNSET", "OK", "ERROR"}); err != nil {
			return sp, err
		}
	}
	return sp, nil
}

func hexID(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("invalid hex ID %q", s)
	}
	return b, nil
}

func jsonEnum(raw json.RawMessage, prefix string, names []string) (int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, fmt.Errorf("invalid enum value %s", raw)
	}
	for i, name := range names {
		if strings.EqualFold(strings.TrimPrefix(s, prefix), name) {
			return i, nil
		}
	}
	return 0, fmt.Errorf("unknown enum value %q", s)
}

func jsonAttrs(in []jsonKV, dropped *int) ([]keyValue, error) {
	var out []keyValue
	for _, kv := range in {
		v, err := jsonAnyValue(kv.Value, 0)
		if err != nil {
			return nil, fmt.Errorf("attribute %q: %w", kv.Key, err)
		}
		out = appendAttr(out, keyValue{Key: kv.Key, Value: v}, dropped)
	}
	return out, nil
}

func jsonAnyValue(raw json.RawMessage, depth int) (any, error) {
	if depth > maxNestedDepth {
		return nil, errors.New("attribute value nested too deeply")
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var v struct {
		String *string         `json:"stringValue"`
		Bool   *bool           `json:"boolValue"`
		Int    *jsonNum        `json:"intValue"`
		Double json.RawMessage `json:"doubleValue"`
		Array  *struct {
			Values []json.RawMessage `json:"values"`
		} `json:"arrayValue"`
		KVList *struct {
			Values []jsonKV `json:"values"`
		} `json:"kvlistValue"`
		Bytes *string `json:"bytesValue"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	switch {
	case v.String != nil:
		return truncate(*v.String), nil
	case v.Bool != nil:
		return *v.Bool, nil
	case v.Int != nil:
		return strconv.ParseInt(string(*v.Int), 10, 64)
	case len(v.Double) > 0:
		var f float64
		if err := json.Unmarshal(v.Double, &f); err != nil {
			s := strings.Trim(string(v.Double), `"`)
			return strconv.ParseFloat(s, 64)
		}
		return f, nil
	case v.Array != nil:
		items := []any{}
		for _, item := range v.Array.Values[:min(len(v.Array.Values), maxAttributes)] {
			x, err := jsonAnyValue(item, depth+1)
			if err != nil {
				return nil, err
			}
			items = append(items, x)
		}
		return items, nil
	case v.KVList != nil:
		var kvs []keyValue
		for _, kv := range v.KVList.Values {
			x, err := jsonAnyValue(kv.Value, depth+1)
			if err != nil {
				return nil, err
			}
			kvs = appendAttr(kvs, keyValue{Key: kv.Key, Value: x}, nil)
		}
		return kvMap(kvs), nil
	case v.Bytes != nil: // JSON 인코딩에서 bytes는 base64다. 그대로 문자열로 둔다.
		return truncate(*v.Bytes), nil
	}
	return nil, nil
}

// ---------------- helpers ----------------

func truncate(s string) string {
	if len(s) > maxValueBytes {
		return s[:maxValueBytes]
	}
	return s
}

// appendAttr는 상한(128)까지만 속성을 더한다. 넘친 것은 dropped에 센다.
func appendAttr(attrs []keyValue, kv keyValue, dropped *int) []keyValue {
	if kv.Key == "" {
		return attrs
	}
	if len(attrs) >= maxAttributes {
		if dropped != nil {
			*dropped++
		}
		return attrs
	}
	return append(attrs, kv)
}

func kvMap(kvs []keyValue) map[string]any {
	out := make(map[string]any, len(kvs))
	for _, kv := range kvs {
		out[kv.Key] = kv.Value
	}
	return out
}

// encodeExportResponse는 ExportTraceServiceResponse를 만든다. rejected가 있으면 partial_success를 채운다.
func encodeExportResponse(rejected int64, msg string) []byte {
	if rejected == 0 && msg == "" {
		return []byte{}
	}
	var ps []byte
	if rejected > 0 {
		ps = append(ps, 0x08) // field 1 varint
		ps = binary.AppendUvarint(ps, uint64(rejected))
	}
	if msg != "" {
		ps = append(ps, 0x12) // field 2 bytes
		ps = binary.AppendUvarint(ps, uint64(len(msg)))
		ps = append(ps, msg...)
	}
	out := []byte{0x0a} // field 1 (partial_success) bytes
	out = binary.AppendUvarint(out, uint64(len(ps)))
	return append(out, ps...)
}

func encodeExportResponseJSON(rejected int64, msg string) []byte {
	if rejected == 0 && msg == "" {
		return []byte("{}")
	}
	b, _ := json.Marshal(map[string]any{"partialSuccess": map[string]any{"rejectedSpans": strconv.FormatInt(rejected, 10), "errorMessage": msg}})
	return b
}
