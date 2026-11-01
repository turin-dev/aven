"use client";

import Link from "next/link";
import { useQueries } from "@tanstack/react-query";
import { AlertTriangle, CheckCircle2, Cpu, HardDrive, MemoryStick, Server, XCircle } from "lucide-react";
import { api, type ID, type AvenEvent, type AvenNode } from "@/lib/api";
import { qk, useNodeMetrics, useProjects } from "@/lib/queries";
import { canvasHref } from "@/lib/nav";
import { fmtBytes, fmtRelative } from "@/lib/format";
import { diskLevel, fmtPct, nodeUsage } from "@/lib/metrics";
import { t } from "@/lib/i18n";
import { cx } from "../ui";
import { Gauge } from "./Gauge";
import { NODE_STATUS } from "./NodesView";

/** Compact per-node usage for the admin overview: status, CPU, memory and free disk. */
export function NodeHealthRow({ n }: { n: AvenNode }) {
  const m = useNodeMetrics(n.id, "1h", n.status !== "offline");
  const usage = n.status === "offline" ? null : nodeUsage(m.data?.points, n.memory_bytes);
  const st = NODE_STATUS[n.status];
  const diskFree = usage?.diskFree ?? n.disk_free_bytes;
  const dl = diskLevel(diskFree);
  return (
    <li className="grid gap-x-6 gap-y-3 px-5 py-3.5 sm:grid-cols-[minmax(0,1.2fr)_repeat(3,minmax(0,1fr))] sm:items-center">
      <div className="flex min-w-0 items-center gap-2.5">
        <Server className="size-4 shrink-0 text-gray-500" aria-hidden />
        <div className="min-w-0">
          <div className="truncate text-sm font-medium">
            {n.name}
            {n.is_local && <span className="ml-2 text-xs font-normal text-gray-500">{t("현재 서버")}</span>}
          </div>
          <div className={cx("flex items-center gap-1.5 text-xs", st.text)}>
            <span aria-hidden className={cx("size-1.5 rounded-full", st.dot)} />
            {st.label}
            {n.status === "offline" && <span className="text-gray-500">· {t("마지막 응답 {time}", { time: fmtRelative(n.last_seen_at) })}</span>}
            {!n.schedulable && n.status === "online" && <span className="text-gray-500">· {t("새 배포 중지")}</span>}
          </div>
        </div>
      </div>
      <Gauge icon={<Cpu className="size-3.5" />} label="CPU" pct={usage?.cpu ?? null} value={usage?.cpu != null ? fmtPct(usage.cpu) : "—"} sub={t("{cores}코어", { cores: n.cpu_cores })} />
      <Gauge
        icon={<MemoryStick className="size-3.5" />}
        label={t("메모리")}
        pct={usage?.memoryPct ?? null}
        value={usage?.memoryPct != null ? fmtPct(usage.memoryPct) : "—"}
        sub={usage ? t("{used} / {total} 사용", { used: fmtBytes(usage.memory), total: fmtBytes(n.memory_bytes) }) : t("전체 {total}", { total: fmtBytes(n.memory_bytes) })}
      />
      <Gauge icon={<HardDrive className="size-3.5" />} label={t("디스크 여유")} pct={null} bar={false} level={dl} value={fmtBytes(diskFree)} sub={dl !== "ok" ? t("10 GB 미만 남았습니다") : undefined} />
    </li>
  );
}

/* ── recent problems ────────────────────────────────────────────────── */

const PROBLEM_KINDS: Record<string, string> = {
  "deploy.failed": t("배포 실패"),
  "deploy.crashed": t("크래시"),
  "service.offline": t("서비스 중단"),
  "alert.firing": t("알림 발생"),
  "backup.failed": t("백업 실패"),
  "backup.restore_failed": t("복원 실패"),
  "cron.failed": t("cron 실패"),
};
/** How many projects the overview scans for recent failures (each costs one request). */
const MAX_PROJECTS = 20;
const WINDOW_MS = 24 * 3600_000;

export type ProblemEvent = AvenEvent & { project_name: string; workspace_id: ID };

/** Failure events of the last 24 hours across the projects the caller can open (newest first). */
export function useRecentProblems(enabled: boolean) {
  const projects = useProjects();
  const list = (projects.data ?? []).slice(0, MAX_PROJECTS);
  const qs = useQueries({
    queries: list.map((p) => ({
      queryKey: [...qk.events(p.id), "ui-admin-problems"],
      queryFn: () => api.events(p.id, undefined, 50),
      enabled,
      staleTime: 30_000,
      refetchInterval: 60_000,
    })),
  });
  const since = Date.now() - WINDOW_MS;
  const events: ProblemEvent[] = [];
  qs.forEach((q, i) => {
    for (const e of q.data ?? []) {
      if (!(e.kind in PROBLEM_KINDS) || Date.parse(e.created_at) < since) continue;
      events.push({ ...e, project_name: list[i].name, workspace_id: list[i].workspace_id });
    }
  });
  events.sort((a, b) => b.created_at.localeCompare(a.created_at));
  return {
    events: events.slice(0, 8),
    total: events.length,
    loading: projects.isPending || qs.some((q) => q.isPending && q.fetchStatus !== "idle"),
    truncated: (projects.data?.length ?? 0) > MAX_PROJECTS,
  };
}

export function ProblemEventList({ events }: { events: ProblemEvent[] }) {
  return (
    <ul className="divide-y divide-gray-100" aria-label={t("최근 오류")}>
      {events.map((e) => (
        <li key={e.id} className="flex min-w-0 items-start gap-3 px-5 py-2.5 text-sm">
          <XCircle className="mt-0.5 size-4 shrink-0 text-danger" aria-hidden />
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-baseline gap-x-2">
              <span className="font-medium text-danger">{PROBLEM_KINDS[e.kind] ?? e.kind}</span>
              {e.service_id && e.environment_id ? (
                <Link
                  href={canvasHref({ svc: e.service_id }, { project: e.project_id, env: e.environment_id, ws: e.workspace_id })}
                  className="truncate font-medium hover:underline"
                >
                  {e.service_name || t("서비스")}
                </Link>
              ) : e.service_name ? <span className="truncate">{e.service_name}</span> : null}
              <span className="truncate text-xs text-gray-500">{e.project_name}</span>
            </div>
            {e.message && e.message !== PROBLEM_KINDS[e.kind] && <p className="mt-0.5 line-clamp-2 break-words text-xs text-gray-600">{e.message}</p>}
          </div>
          <time dateTime={e.created_at} className="shrink-0 text-xs tabular-nums text-gray-500">{fmtRelative(e.created_at)}</time>
        </li>
      ))}
    </ul>
  );
}

/* ── health line ────────────────────────────────────────────────────── */

export type HealthState = "ok" | "warn" | "bad" | "unknown";

export function HealthItem({ label, state, detail, href }: { label: string; state: HealthState; detail: string; href?: string }) {
  const Icon = state === "ok" ? CheckCircle2 : state === "unknown" ? AlertTriangle : state === "warn" ? AlertTriangle : XCircle;
  const tone = state === "ok" ? "text-success" : state === "warn" ? "text-warn" : state === "bad" ? "text-danger" : "text-gray-400";
  const body = (
    <>
      <Icon className={cx("mt-0.5 size-4 shrink-0", tone)} aria-hidden />
      <div className="min-w-0">
        <div className="text-sm font-medium">{label}</div>
        <div className={cx("truncate text-xs tabular-nums", state === "bad" ? "text-danger" : state === "warn" ? "text-warn" : "text-gray-500")} title={detail}>{detail}</div>
      </div>
    </>
  );
  return (
    <li className="min-w-0">
      {href ? (
        <Link href={href} className="flex min-w-0 items-start gap-2.5 rounded-md p-2 -m-2 hover:bg-gray-50 focus-visible:outline-2 focus-visible:outline-pink-500">{body}</Link>
      ) : (
        <div className="flex min-w-0 items-start gap-2.5">{body}</div>
      )}
    </li>
  );
}
