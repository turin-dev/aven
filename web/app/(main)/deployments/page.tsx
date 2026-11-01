"use client";

import Link from "next/link";
import { Suspense, useEffect, useMemo, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { useQueries } from "@tanstack/react-query";
import { GitCommitHorizontal, Rocket, Search } from "lucide-react";
import { api, isTerminal, type CanvasData, type Deployment, type ID, type ProjectDetail } from "@/lib/api";
import { qk } from "@/lib/queries";
import { canvasHref, useCtxHref } from "@/lib/nav";
import { fmtDuration, fmtRelative, fmtTime, shortSha } from "@/lib/format";
import { useWorkspace } from "@/lib/workspace";
import { t } from "@/lib/i18n";
import { BrandIcon, iconKey } from "@/components/ServiceIcon";
import { StatusBadge, TRIGGER_LABEL } from "@/components/StatusBadge";
import { RedirectNotice } from "@/components/Redirect";
import { CenterSpinner, EmptyState, ErrorBox, Input, PageBody, PageShell, Segmented, Select, errorMessage } from "@/components/ui";

// /deployments/?id=D redirects to the deployment on its canvas (legacy links, notifications).
// /deployments/ without an id lists the latest deployment of every service across all projects the user can see.

/** Find the project and environment of a deployment, then open it on the canvas. */
function Go({ id }: { id: string }) {
  const router = useRouter();
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const d = await api.getDeployment(id);
        const projects = await api.listProjects();
        // Look the environment up in all projects at once; one inaccessible project must not hide the others.
        const details = await Promise.all(projects.map((p) => api.getProject(p.id).catch(() => null)));
        if (cancelled) return;
        const hit = details.flatMap((p) => (p ? p.environments.filter((e) => e.id === d.environment_id).map((e) => ({ p, e })) : []))[0];
        router.replace(hit ? canvasHref({ svc: d.service_id, dep: d.id }, { project: hit.p.id, env: hit.e.name }) : canvasHref({ svc: d.service_id, dep: d.id }));
      } catch (e) {
        if (!cancelled) setError(t("배포 {id}를 찾지 못했습니다 ({err}). 삭제된 서비스의 배포일 수 있습니다.", { id, err: errorMessage(e) }));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [id, router]);
  return <RedirectNotice error={error} />;
}

type Row = { dep: Deployment; project: ProjectDetail; env: { id: ID; name: string }; service: CanvasData["services"][number] };
type StatusFilter = "all" | "active" | "failed" | "live";

const FAILED = new Set(["failed", "crashed"]);

function matches(r: Row, f: StatusFilter): boolean {
  if (f === "active") return !isTerminal(r.dep.status);
  if (f === "failed") return FAILED.has(r.dep.status);
  if (f === "live") return r.dep.status === "live";
  return true;
}

function DeploymentRow({ r }: { r: Row }) {
  const d = r.dep;
  const title = d.commit_message?.split("\n")[0] || d.image || TRIGGER_LABEL[d.trigger] || d.trigger;
  return (
    <li>
      <Link
        href={canvasHref({ svc: d.service_id, dep: d.id }, { project: r.project.id, env: r.env.name })}
        className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-1 px-4 py-3 hover:bg-gray-100/50 md:grid-cols-[minmax(0,2fr)_minmax(0,3fr)_auto] focus-visible:bg-gray-100/50 focus-visible:outline-none"
      >
        <span className="flex min-w-0 items-center gap-3">
          <span className="flex size-8 shrink-0 items-center justify-center rounded-md border border-gray-200 bg-gray-50">
            <BrandIcon name={iconKey(r.service)} className="size-4" />
          </span>
          <span className="min-w-0">
            <span className="block truncate text-sm font-medium text-foreground">{r.service.name}</span>
            <span className="block truncate text-xs text-gray-500">
              {r.project.name} · {r.env.name}
            </span>
          </span>
        </span>
        <span className="col-span-2 row-start-2 min-w-0 md:col-span-1 md:row-start-auto">
          <span className="block truncate text-sm text-gray-600" title={d.commit_message ?? undefined}>
            {title}
          </span>
          <span className="flex items-center gap-1.5 truncate text-xs text-gray-500">
            {d.commit_sha && (
              <>
                <GitCommitHorizontal className="size-3.5 shrink-0" aria-hidden="true" />
                <span className="font-mono">{shortSha(d.commit_sha)}</span>
                {d.branch && <span className="truncate">· {d.branch}</span>}
                <span>·</span>
              </>
            )}
            <span>{TRIGGER_LABEL[d.trigger] ?? d.trigger}</span>
            {d.commit_author && <span className="truncate">· {d.commit_author}</span>}
          </span>
        </span>
        <span className="col-start-2 row-start-1 flex shrink-0 items-center gap-3 md:col-start-3">
          <span className="text-right text-xs tabular-nums text-gray-500">
            <time dateTime={d.created_at} title={fmtTime(d.created_at)} className="block">
              {fmtRelative(d.created_at)}
            </time>
            {d.started_at && <span className="block">{fmtDuration(d.started_at, d.finished_at)}</span>}
          </span>
          <StatusBadge status={d.status} className="w-[92px] justify-center" />
        </span>
      </Link>
    </li>
  );
}

function DeploymentsList() {
  const ws = useWorkspace();
  const href = useCtxHref();
  const projects = useMemo(() => ws.allProjects.data ?? [], [ws.allProjects.data]);
  const [status, setStatus] = useState<StatusFilter>("all");
  const [pid, setPid] = useState("");
  const [q, setQ] = useState("");

  useEffect(() => {
    document.title = `${t("배포|plural")} · aven`;
    return () => {
      document.title = "aven";
    };
  }, []);

  const details = useQueries({
    queries: projects.map((p) => ({ queryKey: qk.project(p.id), queryFn: () => api.getProject(p.id), staleTime: 5_000 })),
  });
  const envs = details.flatMap((d) => (d.data ? d.data.environments.map((e) => ({ project: d.data!, env: e })) : []));
  const canvases = useQueries({
    queries: envs.map(({ env }) => ({
      queryKey: qk.canvas(env.id),
      queryFn: () => api.canvas(env.id),
      staleTime: 10_000,
      retry: false,
      refetchInterval: (query: { state: { data?: CanvasData } }) =>
        query.state.data?.services.some((s) => s.latest_deployment && !isTerminal(s.latest_deployment.status)) ? 3000 : 20_000,
    })),
  });

  const rows: Row[] = [];
  canvases.forEach((c, i) => {
    for (const s of c.data?.services ?? []) {
      if (s.latest_deployment) rows.push({ dep: s.latest_deployment, project: envs[i].project, env: envs[i].env, service: s });
    }
  });
  rows.sort((a, b) => Date.parse(b.dep.created_at) - Date.parse(a.dep.created_at));
  const needle = q.trim().toLowerCase();
  const shown = rows.filter(
    (r) =>
      matches(r, status) &&
      (!pid || r.project.id === pid) &&
      (!needle ||
        [r.service.name, r.project.name, r.env.name, r.dep.commit_message, r.dep.commit_sha, r.dep.branch, r.dep.image, r.dep.commit_author].some((v) => v?.toLowerCase().includes(needle))),
  );
  const counts = { active: rows.filter((r) => matches(r, "active")).length, failed: rows.filter((r) => matches(r, "failed")).length };
  const loading = ws.isPending || details.some((d) => d.isPending) || canvases.some((c) => c.isPending);
  // Restricted environments answer 403 to non-admins; only surface an error when nothing could be read.
  const firstError = details.find((d) => d.error)?.error ?? canvases.find((c) => c.error)?.error;

  return (
    <PageShell title={t("배포|plural")} close={{ href: href("/"), label: t("닫기") }}>
      <PageBody className="space-y-4">
        <p className="text-sm text-gray-600">{t("볼 수 있는 모든 프로젝트와 환경에서 서비스별 최근 배포를 모아 봅니다.")}</p>
        <div className="flex flex-wrap items-center gap-3">
          <div className="relative min-w-[220px] flex-1">
            <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-gray-400" aria-hidden="true" />
            <Input
              type="search"
              aria-label={t("배포 검색")}
              placeholder={t("서비스, 커밋, 브랜치, 이미지로 검색")}
              value={q}
              onChange={(e) => setQ(e.target.value)}
              className="w-full pl-9"
            />
          </div>
          {projects.length > 1 && (
            <Select aria-label={t("프로젝트")} value={pid} onChange={(e) => setPid(e.target.value)} className="w-[200px]">
              <option value="">{t("모든 프로젝트")}</option>
              {projects.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </Select>
          )}
          <Segmented
            label={t("상태")}
            value={status}
            onChange={setStatus}
            options={[
              { value: "all", label: t("전체") },
              { value: "active", label: counts.active ? t("진행 중 {n}", { n: counts.active }) : t("진행 중") },
              { value: "failed", label: counts.failed ? t("실패 {n}", { n: counts.failed }) : t("실패") },
              { value: "live", label: t("활성|deployment") },
            ]}
          />
        </div>
        {!rows.length && firstError ? <ErrorBox error={ws.error ?? firstError} /> : ws.error ? <ErrorBox error={ws.error} /> : null}
        {loading && !rows.length ? (
          <CenterSpinner />
        ) : shown.length === 0 ? (
          <EmptyState icon={<Rocket className="size-5" />} title={rows.length ? t("조건에 맞는 배포가 없습니다") : t("아직 배포가 없습니다")} />
        ) : (
          <ul className="divide-y divide-gray-200 rounded-lg border border-gray-200 bg-secondaryBg" aria-label={t("최근 배포")} aria-busy={loading}>
            {shown.map((r) => (
              <DeploymentRow key={`${r.env.id}:${r.dep.id}`} r={r} />
            ))}
          </ul>
        )}
      </PageBody>
    </PageShell>
  );
}

function Inner() {
  const id = useSearchParams().get("id");
  return id ? <Go id={id} /> : <DeploymentsList />;
}

export default function DeploymentsPage() {
  return (
    <Suspense fallback={<RedirectNotice />}>
      <Inner />
    </Suspense>
  );
}
