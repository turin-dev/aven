package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
)

const TraceIDHeader = "X-Aven-Trace-Id"

// traceHandlers는 trace 대상 라우트의 reverse_proxy 앞에 둘 handler다.
//
// Caddy tracing 모듈(2.11+: span_attributes, OTEL_EXPORTER_OTLP_PROTOCOL을 따르는 autoexport)이 server span을 만들고
// W3C traceparent를 upstream 요청에 넣는다. 들어온 traceparent가 있으면 그 trace를 잇고 sampled 플래그를 따른다
// (기본 ParentBased 샘플러). span_attributes의 aven.* 값으로 aven-server 수신기가 span의 서비스를 정한다.
func traceHandlers(attrs map[string]string) []map[string]any {
	return []map[string]any{
		{"handler": "tracing", "span": "{http.request.method} {http.request.uri.path}", "span_attributes": attrs},
		{"handler": "headers", "response": map[string]any{"set": map[string][]string{TraceIDHeader: {"{http.vars.trace_id}"}}}},
	}
}

// traceEnv는 proxy 컨테이너에 넣을 OTLP exporter 환경 변수다. 설정이 없으면 비어 있다.
func (c *Caddy) traceEnv() []string {
	if c.cfg.OTLPEndpoint == "" || c.cfg.TraceToken == "" {
		return nil
	}
	return []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT=" + c.cfg.OTLPEndpoint,
		"OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
		"OTEL_EXPORTER_OTLP_HEADERS=X-Aven-Trace-Token=" + c.cfg.TraceToken,
	}
}

// traceEnvMatches는 떠 있는 proxy 컨테이너의 OTEL_* 값이 지금 설정과 같은지 본다.
func (c *Caddy) traceEnvMatches(ctx context.Context) bool {
	want := c.traceEnv()
	if len(want) == 0 {
		return true
	}
	out, err := exec.CommandContext(ctx, c.cfg.DockerBin, "inspect", "--format", "{{range .Config.Env}}{{println .}}{{end}}", c.Resolve(ctx).Container).CombinedOutput()
	if err != nil {
		return false
	}
	have := map[string]bool{}
	for _, l := range strings.Split(string(out), "\n") {
		have[strings.TrimSpace(l)] = true
	}
	for _, kv := range want {
		if have[kv] {
			continue
		}
		legacy := strings.ReplaceAll(kv, "X-Aven-Trace-Token=", "X-Keel-Trace-Token=")
		if legacy != kv && have[legacy] {
			continue
		}
		return false
	}
	return true
}

func hasTracedRoute(routes []Route) bool {
	for _, r := range routes {
		if r.Trace {
			return true
		}
	}
	return false
}

// applyUntraced는 trace 라우트가 든 설정을 Caddy가 거절했을 때(2.11보다 오래된 이미지는 span_attributes를 모른다)
// tracing 없이 다시 넣는다. 라우팅이 tracing 때문에 멈추지 않게 성공으로 보고 경고만 남긴다.
func (c *Caddy) applyUntraced(ctx context.Context, routes []Route, cause error) error {
	plain := make([]Route, len(routes))
	for i, r := range routes {
		r.Trace, r.TraceAttributes = false, nil
		plain[i] = r
	}
	body, err := c.Build(plain)
	if err != nil {
		return err
	}
	if err := c.do(ctx, http.MethodPost, "/load", body); err != nil {
		return err
	}
	slog.Warn("edge tracing disabled: "+c.Resolve(ctx).Container+" rejected the tracing handler (needs Caddy 2.11+; docker pull "+c.cfg.Image+" and restart aven-server)", "err", cause)
	return nil
}
