package deploy

import (
	"context"
	"net/url"
	"sort"
	"strings"

	"github.com/turin-dev/aven/internal/store"
)

// DefaultOTLPEndpoint는 컨테이너가 span을 보낼 기본 주소다. host-gateway로 aven-server 호스트의 OTLP 수신기(:4318)에 닿는다.
const DefaultOTLPEndpoint = "http://host.docker.internal:4318"

// traceRoute는 인스턴스가 trace 대상이면 edge span에 붙일 aven.* 속성을 준다. 수신기는 이 속성으로 span의 서비스를 정한다.
func (m *Manager) traceRoute(ctx context.Context, in *store.Instance, deploymentID *string) (bool, map[string]string) {
	setting, err := m.Store.GetTracingSetting(ctx, in.ID)
	if err != nil || !setting.Enabled {
		return false, nil
	}
	svc, err := m.Store.GetService(ctx, in.ServiceID)
	if err != nil {
		return false, nil
	}
	env, err := m.Store.GetEnvironment(ctx, in.EnvironmentID)
	if err != nil {
		return false, nil
	}
	deployment := ""
	if deploymentID != nil {
		deployment = *deploymentID
	}
	return true, map[string]string{
		"keel.project.id":     env.ProjectID,
		"keel.environment.id": env.ID,
		"keel.service.id":     svc.ID,
		"keel.service.name":   svc.Name,
		"keel.deployment.id":  deployment,
		"keel.replica.id":     "{http.reverse_proxy.upstream.host}",
		"keel.component":      "edge",
	}
}

// runtimeTraceEnv는 컨테이너 환경 변수에 tracing 변수(OTEL_*)를 더한 사본이다. tracing이 꺼져 있거나 사용자가

// 사용자가 정한 다른 OTEL_* 값도 그대로 둔다. traced는 변수를 더했는지다.
func (m *Manager) runtimeTraceEnv(ctx context.Context, t *Target, d *store.Deployment, replica string) (env map[string]string, traced bool) {
	out := make(map[string]string, len(d.Env)+10)
	for k, v := range d.Env {
		out[k] = v
	}
	setting, err := m.Store.GetTracingSetting(ctx, t.Instance.ID)
	if err != nil || !setting.Enabled || setting.Token == "" || out["OTEL_EXPORTER_OTLP_ENDPOINT"] != "" || out["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"] != "" {
		return out, false
	}
	endpoint := m.Cfg.OTLPEndpoint
	if endpoint == "" {
		endpoint = DefaultOTLPEndpoint
	}
	setIfMissing := func(key, value string) {
		if out[key] == "" {
			out[key] = value
		}
	}
	setIfMissing("OTEL_EXPORTER_OTLP_ENDPOINT", endpoint)
	setIfMissing("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")
	setIfMissing("OTEL_EXPORTER_OTLP_HEADERS", "X-Keel-Trace-Token="+setting.Token)
	setIfMissing("OTEL_SERVICE_NAME", t.Service.Name)
	version := d.ID
	if d.CommitSHA != nil && *d.CommitSHA != "" {
		version = *d.CommitSHA
	}
	setIfMissing("OTEL_SERVICE_VERSION", version)
	// 수신기는 trace만 받는다. SDK가 metrics/logs를 같은 endpoint로 보내다 실패하지 않게 기본으로 끈다.
	setIfMissing("OTEL_METRICS_EXPORTER", "none")
	setIfMissing("OTEL_LOGS_EXPORTER", "none")
	setIfMissing("OTEL_PROPAGATORS", "tracecontext,baggage")
	attrs := map[string]string{}
	if raw := out["OTEL_RESOURCE_ATTRIBUTES"]; raw != "" {
		for _, item := range strings.Split(raw, ",") {
			if k, v, ok := strings.Cut(item, "="); ok && k != "" {
				attrs[strings.TrimSpace(k)] = v
			}
		}
	}
	// 프로젝트/환경/서비스는 수신기가 토큰으로 정한다. 여기 값은 배포/replica 구분과 SDK 쪽 표시용이다.
	for k, v := range map[string]string{
		"keel.project.id": t.Project.ID, "keel.environment.id": t.Env.ID,
		"keel.service.id": t.Service.ID, "keel.service.name": t.Service.Name,
		"keel.deployment.id": d.ID, "keel.replica.id": replica,
	} {
		attrs[k] = strings.ReplaceAll(url.QueryEscape(v), "+", "%20")
	}
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys) // 같은 입력이면 같은 문자열(컨테이너 설정 비교가 흔들리지 않게)
	items := make([]string, 0, len(keys))
	for _, k := range keys {
		items = append(items, k+"="+attrs[k])
	}
	out["OTEL_RESOURCE_ATTRIBUTES"] = strings.Join(items, ",")
	return out, true
}
