"use client";

import type { ReactNode } from "react";
import { useSystem } from "@/lib/queries";
import { fmtBytes } from "@/lib/format";
import { Card, ErrorBox, cx, errorMessage } from "@/components/ui";
import { t } from "@/lib/i18n";

function StatusRow({ label, ok, detail, mono }: { label: string; ok?: boolean | null; detail: ReactNode; mono?: boolean }) {
  return (
    <div className="flex items-center gap-3 border-b border-gray-200 py-3.5 last:border-0">
      {ok !== undefined ? <span className={cx("size-2 shrink-0 rounded-full", ok === null ? "bg-gray-400" : ok ? "bg-green-600" : "bg-red-600")} /> : <span className="size-2 shrink-0" />}
      <span className="w-40 shrink-0 text-sm font-medium">{label}</span>
      <span className={cx("min-w-0 truncate text-sm tabular-nums", ok === false ? "text-danger" : "text-gray-600", mono && "font-mono text-sm")}>{detail}</span>
    </div>
  );
}

export function SystemStatus() {
  const sys = useSystem();
  const d = sys.data;
  if (sys.error) return <ErrorBox error={t("시스템 정보를 불러오지 못했습니다: {error}", { error: errorMessage(sys.error) })} />;
  return (
    <>
      <Card title={t("시스템 상태")}>
      <div className="rounded-lg border border-gray-200 bg-secondaryBg px-4">
        <StatusRow label="Docker" ok={d ? d.docker === "ok" : null} detail={!d ? t("확인 중") : d.docker === "ok" ? t("정상") : t("응답 없음 ({status}). docker 서비스를 확인하세요.", { status: d.docker })} />
        <StatusRow label={t("프록시")} ok={d ? d.proxy === "ok" : null} detail={!d ? t("확인 중") : d.proxy === "ok" ? t("정상") : t("응답 없음 ({status}). 프록시 포트와 로그를 확인하세요.", { status: d.proxy })} />
        <StatusRow label={t("Aven 버전")} detail={d?.version ?? "…"} mono />
        <StatusRow label={t("노드")} detail={d?.node ?? "…"} mono />
        <StatusRow label={t("기본 도메인")} ok={d ? !!d.base_domain : null} detail={d ? (d.base_domain ?? t("설정 안 됨 (AVEN_BASE_DOMAIN). 도메인 자동 생성을 쓸 수 없습니다.")) : "…"} mono={!!d?.base_domain} />
        <StatusRow label={t("공개 IPv6")} detail={d ? (d.public_ipv6 ?? t("없음")) : "…"} mono />
        <StatusRow label={t("공개 IPv4")} detail={d ? (d.public_ipv4 ?? t("없음")) : "…"} mono />
        <StatusRow label={t("디스크 여유")} ok={d ? d.disk_free_bytes > 10e9 : null} detail={d ? fmtBytes(d.disk_free_bytes) : "…"} />
        {d?.mesh && (
          <StatusRow
            label={t("노드 간 사설 네트워크")}
            ok={d.mesh.mode === "off" ? null : d.mesh.enabled}
            detail={
              d.mesh.enabled
                ? `${d.mesh.private_address}:${d.mesh.port} (AVEN_MESH=${d.mesh.mode}, AVEN_MESH_PORT=${d.mesh.port})`
                : d.mesh.mode === "off"
                  ? t("꺼짐 (AVEN_MESH=off)")
                  : t("꺼짐: AVEN_PRIVATE_ADDRESS에 Tailscale/WireGuard 사설 IP를 설정하세요")
            }
            mono={d.mesh.enabled}
          />
        )}
      </div>
      </Card>
    </>
  );
}

