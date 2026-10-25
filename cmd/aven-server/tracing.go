package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/store"
	"github.com/turin-dev/aven/internal/tracing"
)

// otlpEndpoint는 컨테이너(서비스, keel-proxy)가 span을 보낼 OTLP/HTTP 주소다. 다른 노드의 컨테이너도 보내야 하면
// 그 노드에서 닿는 주소(예: http://<사설 IP>:4318 또는 KEEL_PUBLIC_URL)로 바꾼다.
func otlpEndpoint() string { return env("KEEL_OTLP_ENDPOINT", deploy.DefaultOTLPEndpoint) }

// traceRetention은 span 보관 기간이다(KEEL_TRACE_RETENTION, 예: 72h, 기본 7일).
func traceRetention(log *slog.Logger) time.Duration {
	if v := os.Getenv("KEEL_TRACE_RETENTION"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
		log.Warn("KEEL_TRACE_RETENTION is not a duration; using the default", "value", v)
	}
	return tracing.DefaultRetention
}

// getTracingProxyToken은 keel-proxy가 edge span을 보낼 때 쓰는 토큰이다(처음 한 번 만들어 암호화해 둔다).
func getTracingProxyToken(st *store.Store) (string, error) {
	const setting = "tracing_proxy_token"
	value, err := st.GetSetting(context.Background(), setting)
	if err == nil {
		return st.Unseal(value)
	}
	if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	token := store.RandomSecret(48)
	if err := st.SetSetting(context.Background(), setting, st.Seal(token)); err != nil {
		return "", err
	}
	return token, nil
}
