"use client";

import { useState } from "react";
import { Globe } from "lucide-react";
import type { ID } from "@/lib/api";
import { fmtRelative } from "@/lib/format";
import { useSetTracing, useTracingSettings, type TracingSetting } from "@/lib/tracing";
import { BrandIcon } from "@/components/ServiceIcon";
import { Button, Dialog, EmptyState, ErrorBox, RowList, Spinner, Toggle, cx } from "@/components/ui";
import { t, tr } from "@/lib/i18n";

function Seen({ label, at, none }: { label: string; at: string | null; none?: string }) {
  return (
    <span className="flex items-center gap-1.5" title={at ? new Date(at).toLocaleString() : undefined}>
      <span className={cx("size-1.5 rounded-full", at ? "bg-green-500" : "bg-gray-300")} aria-hidden="true" />
      <span className="text-gray-500">{label}</span>
      <span className={at ? "tabular-nums text-foreground" : "text-gray-400"}>{at ? fmtRelative(at) : (none ?? t("아직 없음"))}</span>
    </span>
  );
}

function Row({ s, icon, canEdit, busy, onToggle }: { s: TracingSetting; icon: string; canEdit: boolean; busy: boolean; onToggle: (v: boolean) => void }) {
  return (
    <li className="flex items-center gap-3 px-4 py-3">
      <BrandIcon name={icon} className="size-4 shrink-0" />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate text-sm font-medium text-foreground">{s.service_name}</span>
          {!s.has_public_domain && (
            <span
              className="inline-flex items-center gap-1 rounded-md bg-gray-200 px-1.5 py-0.5 text-xs font-medium text-gray-500"
              title={t("edge가 이 서비스의 요청을 보지 못합니다. 앱이 직접 보낸 span만 나타납니다.")}
            >
              <Globe className="size-3" />
              {t("공개 도메인 없음")}
            </span>
          )}
        </div>
        <div className="mt-1 flex flex-wrap gap-x-4 gap-y-1 text-xs">
          <Seen label="Edge" at={s.last_edge_span_at} none={s.has_public_domain ? undefined : t("해당 없음")} />
          <Seen label={t("앱")} at={s.last_service_span_at} />
        </div>
      </div>
      <span className="text-xs text-gray-500">Traced</span>
      <Toggle checked={s.enabled} onChange={onToggle} label={t("{name} 트레이싱", { name: s.service_name })} disabled={!canEdit || busy} />
    </li>
  );
}

export function TracingSetup({
  open,
  onClose,
  eid,
  envName,
  canEdit,
  icons,
}: {
  /** service id → icon key (canvas). */
  icons: Map<ID, string>;
  open: boolean;
  onClose: () => void;
  eid: ID | undefined;
  envName: string;
  canEdit: boolean;
}) {
  const settings = useTracingSettings(open ? eid : null, open);
  const set = useSetTracing(eid);
  const [pending, setPending] = useState<string | null>(null);
  const list = settings.data ?? [];
  const toggle = (sid: ID | "all", enabled: boolean) => {
    setPending(sid);
    set.mutate({ sid, enabled }, { onSettled: () => setPending(null) });
  };
  const allOn = list.length > 0 && list.every((s) => s.enabled);
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("트레이싱 설정")}
      wide
      footer={
        <>
          {canEdit && list.length > 1 && (
            <Button variant="outline" loading={pending === "all"} onClick={() => toggle("all", !allOn)}>
              {allOn ? t("모두 끄기") : t("모두 켜기")}
            </Button>
          )}
          <Button variant="primary" onClick={onClose}>
            {t("완료")}
          </Button>
        </>
      }
    >
      <p className="mb-4 text-sm text-gray-500">
        {tr("{env} 환경에만 적용됩니다. 켜면 edge가 공개 도메인으로 오는 요청을 바로 trace하고, 다음 배포부터 {vars} 변수가 서비스에 들어갑니다.", {
          env: <span className="font-medium text-foreground">{envName}</span>,
          vars: <code className="font-mono text-xs">OTEL_*</code>,
        })}
      </p>
      {set.error && <ErrorBox error={set.error} className="mb-3" />}
      {settings.error ? (
        <ErrorBox error={settings.error} />
      ) : settings.isPending ? (
        <div className="flex justify-center py-8">
          <Spinner />
        </div>
      ) : list.length === 0 ? (
        <EmptyState title={t("trace할 서비스가 없습니다")}>{t("이 환경에 trace할 서비스가 없습니다. 데이터베이스는 목록에 나오지 않습니다.")}</EmptyState>
      ) : (
        <RowList>
          {list.map((s) => (
            <Row key={s.service_id} s={s} icon={icons.get(s.service_id) ?? "docker"} canEdit={canEdit} busy={pending !== null} onToggle={(v) => toggle(s.service_id, v)} />
          ))}
        </RowList>
      )}
      <details className="mt-4 text-sm text-gray-500">
        <summary className="cursor-pointer select-none text-foreground">{t("앱에서 span 보내기")}</summary>
        <div className="mt-2 space-y-2">
          <p>
            {tr("OpenTelemetry SDK는 Aven이 넣는 변수({endpoint}, {headers}, {service} 등)를 그대로 읽습니다. endpoint를 코드에 적지 마세요. 직접 {endpointVar}를 정하면 Aven은 변수를 넣지 않습니다.", {
              endpoint: <code className="font-mono text-xs">OTEL_EXPORTER_OTLP_ENDPOINT</code>,
              headers: <code className="font-mono text-xs">OTEL_EXPORTER_OTLP_HEADERS</code>,
              service: <code className="font-mono text-xs">OTEL_SERVICE_NAME</code>,
              endpointVar: <code className="font-mono text-xs">OTEL_EXPORTER_OTLP_ENDPOINT</code>,
            })}
          </p>
          <pre className="overflow-x-auto rounded-md bg-gray-100 p-3 font-mono text-xs text-foreground">
            {`# Node.js
npm install @opentelemetry/api @opentelemetry/auto-instrumentations-node
node --require @opentelemetry/auto-instrumentations-node/register server.js

# Python
pip install opentelemetry-distro opentelemetry-exporter-otlp
opentelemetry-bootstrap -a install
opentelemetry-instrument gunicorn app:app`}
          </pre>
          <p>{t("자동 계측(eBPF)은 아직 지원하지 않습니다.")}</p>
        </div>
      </details>
    </Dialog>
  );
}
