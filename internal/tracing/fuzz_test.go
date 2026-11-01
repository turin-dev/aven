package tracing

import "testing"

// OTLP 본문은 토큰을 가진 앱이 마음대로 만든다. 해석기는 어떤 바이트에도 panic하지 않아야 한다.

func FuzzDecodeProtoRequest(f *testing.F) {
	f.Add([]byte{})
	f.Add(exportRequest(kvString("service.name", "api")))
	f.Fuzz(func(t *testing.T, b []byte) {
		if req, err := decodeProtoRequest(b); err == nil {
			convertRequest(req, nil, true)
		}
	})
}

func FuzzDecodeJSONRequest(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"` + traceA + `","spanId":"` + spanA + `","name":"x","attributes":[{"key":"a","value":{"arrayValue":{"values":[{"intValue":"1"}]}}}]}]}]}]}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		if req, err := decodeJSONRequest(b); err == nil {
			convertRequest(req, nil, true)
		}
	})
}
