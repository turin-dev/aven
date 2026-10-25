"use client";

import { useQuery } from "@tanstack/react-query";
import { api, type CanvasService, type Environment } from "@/lib/api";
import { fmtBytes } from "@/lib/format";
import { t, dateLocale } from "@/lib/i18n";
import { Card, CenterSpinner, ErrorBox, RowList } from "../ui";

function Stat({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="min-w-0 rounded-lg border border-gray-200 bg-secondaryBg px-4 py-3">
      <div className="text-xs font-semibold text-gray-600">{label}</div>
      <div className="mt-1 truncate text-xl font-semibold tabular-nums" title={value}>
        {value}
      </div>
      {sub && <div className="mt-0.5 truncate text-xs tabular-nums text-gray-500" title={sub}>{sub}</div>}
    </div>
  );
}

export function DbStats({ svc, env }: { svc: CanvasService; env: Environment }) {
  const tables = useQuery({ queryKey: ["data-tables", svc.id, env.id], queryFn: () => api.dataTables(svc.id, env.id), staleTime: 10_000, retry: false });
  const kind = svc.database_kind ?? "postgres";
  const noun = kind === "redis" ? t("데이터베이스") : kind === "mongo" ? t("컬렉션") : t("표");
  const rowNoun = kind === "redis" ? t("키") : kind === "mongo" ? t("문서") : t("행");
  if (tables.error) return <div className="pt-8"><ErrorBox error={tables.error} /></div>;
  if (tables.isPending) return <CenterSpinner />;
  const list = tables.data.tables;
  const rows = list.reduce((n, tb) => n + (tb.rows ?? 0), 0);
  const vol = svc.volumes[0];
  const limit = vol?.size_limit_mb != null ? vol.size_limit_mb * 1024 * 1024 : null;
  const top = [...list].sort((a, b) => (b.rows ?? -1) - (a.rows ?? -1));
  const label = (tb: (typeof list)[number]) => (tb.schema && tb.schema !== "public" ? `${tb.schema}.${tb.name}` : tb.name);
  return (
    <div className="pt-8">
      <Card className="!max-w-none" title={t("통계")} description={tables.data.truncated ? t("행 수는 데이터베이스 통계에서 읽은 추정치입니다. 목록이 길어 일부만 셉니다.") : t("행 수는 데이터베이스 통계에서 읽은 추정치입니다.")}>
        <div className="grid gap-4 sm:grid-cols-3">
          <Stat label={noun} value={list.length.toLocaleString(dateLocale())} sub={list.some((tb) => tb.kind === "view") ? t("view {count}개 포함", { count: list.filter((tb) => tb.kind === "view").length }) : undefined} />
          <Stat label={t("{rowNoun} (추정)", { rowNoun })} value={rows.toLocaleString(dateLocale())} />
          <Stat
            label={t("디스크")}
            value={vol ? (vol.size_bytes === null ? t("측정 전") : fmtBytes(vol.size_bytes)) : t("볼륨 없음")}
            sub={vol ? (limit ? t("{name} · 상한 {limit}", { name: vol.name, limit: fmtBytes(limit) }) : vol.name) : undefined}
          />
        </div>
        {top.length > 0 && (
          <ul className="mt-4 divide-y divide-gray-200 overflow-hidden rounded-lg border border-gray-200 bg-secondaryBg" aria-label={t("{noun}별 {rowNoun} 수", { noun, rowNoun })}>
            {top.map((tb) => (
              <li key={`${tb.schema}.${tb.name}`} className="flex h-11 items-center gap-3 px-4 text-sm">
                <span className="min-w-0 flex-1 truncate font-mono">{label(tb)}</span>
                {tb.kind !== "table" && tb.kind !== "collection" && <span className="shrink-0 text-xs text-gray-500">{tb.kind}</span>}
                <span className="w-28 shrink-0 text-right tabular-nums text-gray-600">{tb.rows != null ? `~${tb.rows.toLocaleString(dateLocale())}` : "—"}</span>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  );
}

export function DbConfig({ svc, spec, tcp }: { svc: CanvasService; spec: { url: string; pub: string; client: string }; tcp: boolean }) {
  const Row = ({ k, v }: { k: string; v: string }) => (
    <li className="flex h-11 items-center justify-between gap-4 px-4 text-sm">
      <span className="text-gray-600">{k}</span>
      <span className="min-w-0 truncate font-mono">{v}</span>
    </li>
  );
  return (
    <Card title={t("설정")} description={t("세부 설정은 설정 탭에서 바꿉니다.")}>
      <RowList>
        <Row k={t("이미지")} v={svc.instance.image ?? "—"} />
        <Row k={t("비공개 연결 변수")} v={spec.url} />
        <Row k={t("공개 연결 변수")} v={spec.pub} />
        <Row k={t("TCP 프록시")} v={tcp ? t("켜짐") : t("꺼짐")} />
        <Row k={t("클라이언트")} v={spec.client} />
      </RowList>
    </Card>
  );
}
