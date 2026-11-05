"use client";

import Link from "next/link";
import { Star } from "lucide-react";
import type { CanvasService, Project } from "@/lib/api";
import { isTerminal } from "@/lib/api";
import { useCanvas } from "@/lib/queries";
import { relTime } from "@/components/InboxMenu";
import { withCtx } from "@/lib/nav";
import { cx } from "@/components/ui";
import { ServiceIcon } from "@/components/ServiceIcon";
import { dateLocale, t } from "@/lib/i18n";

export function BucketIcon({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" width="1em" height="1em" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" className={className}>
      <path d="M6 7c0-2.8 2.2-5 5-5h2c2.8 0 5 2.2 5 5" />
      <path d="M5 11h14" />
      <path d="m18 11-.8 9c-.1 1.1-1.1 2-2.2 2H9c-1.1 0-2.1-.9-2.2-2L6 11" />
    </svg>
  );
}

const isBucket = (s: CanvasService) => s.database_kind === "bucket";

// Sleeping (app sleeping) and scheduled (cron between runs) services are fine, not "down".
const HEALTHY = new Set(["online", "sleeping", "scheduled"]);

/** Live status of a project's default environment, read from the (polled) canvas query. */
export function useProjectStatus(p: Project) {
  const canvas = useCanvas(p.default_environment_id);
  const all = canvas.data?.services ?? [];
  const services = all.filter((s) => !isBucket(s));
  const buckets = all.length - services.length;
  const healthy = services.filter((s) => HEALTHY.has(s.instance.status)).length;
  const crashed = services.filter((s) => s.instance.status === "crashed" || s.instance.status === "failed").length;
  const deploying = services.filter((s) => s.instance.status === "deploying" || (s.latest_deployment && !isTerminal(s.latest_deployment.status))).length;
  const deployed = services.some((s) => s.instance.status !== "idle" || s.latest_deployment);
  let lastDeploy: string | null = null;
  for (const s of all) {
    const at = s.latest_deployment?.created_at;
    if (at && (!lastDeploy || at > lastDeploy)) lastDeploy = at;
  }
  return {
    pending: canvas.isPending && !!p.default_environment_id,
    error: canvas.isError,
    all,
    services,
    buckets,
    healthy,
    crashed,
    deploying,
    deployed,
    lastDeploy,
    envName: canvas.data?.environment.name ?? "production",
  };
}

type Status = ReturnType<typeof useProjectStatus>;

function statusTone(s: Status): { dot: string; label: string; text?: string } {
  const n = s.services.length;
  if (s.crashed > 0) return { dot: "bg-red-500", label: t("서비스 {crashed}/{n} 크래시", { crashed: s.crashed, n }), text: "text-red-600" };
  if (s.deploying > 0) return { dot: "bg-yellow-500 motion-safe:animate-pulse", label: t("{count}개 배포 중", { count: s.deploying }), text: "text-warn" };
  if (!s.deployed) return { dot: "bg-gray-400", label: t("배포 안 됨") };
  if (s.healthy === n) return { dot: "bg-green-500", label: t("서비스 {online}/{n} 온라인", { online: s.healthy, n }) };
  return { dot: "bg-gray-400", label: t("서비스 {online}/{n} 온라인", { online: s.healthy, n }) };
}

function StatusLine({ status }: { status: Status }) {
  const { pending, error, services, buckets, envName, lastDeploy } = status;
  const n = services.length;
  if (pending) return <span className="h-3 w-40 animate-pulse rounded bg-gray-100" aria-label={t("불러오는 중")} />;
  if (error) return <span>{t("상태를 불러오지 못했습니다")}</span>;
  if (n === 0) {
    if (buckets > 0)
      return (
        <div className="flex items-center gap-2">
          <div aria-hidden="true" className="icon-container icon-sm text-current text-lg">
            <BucketIcon />
          </div>
          <span>{t("버킷 {count}개", { count: buckets })}</span>
        </div>
      );
    return <span>{t("서비스 없음")}</span>;
  }
  const tone = statusTone(status);
  return (
    <div className="flex min-w-0 flex-1 items-center gap-2 whitespace-nowrap tabular-nums">
      <div className={cx("h-1.5 w-1.5 shrink-0 rounded-full", tone.dot)} aria-hidden />
      <span className="shrink-0">{envName}</span>
      <span aria-hidden>·</span>
      <span className={cx("min-w-0 truncate", tone.text)}>{tone.label}</span>
      {buckets > 0 && (
        <>
          <span aria-hidden>·</span>
          <span className="shrink-0">{t("버킷 {count}개", { count: buckets })}</span>
        </>
      )}
      {lastDeploy && (
        <time dateTime={lastDeploy} title={t("마지막 배포: {time}", { time: new Date(lastDeploy).toLocaleString(dateLocale()) })} className="ml-auto shrink-0 pl-2 text-gray-500">
          {relTime(lastDeploy)}
        </time>
      )}
    </div>
  );
}

export function FavoriteButton({ p, favorite, onFavorite }: { p: Project; favorite: boolean; onFavorite: () => void }) {
  return (
    <button
      type="button"
      aria-pressed={favorite}
      aria-label={favorite ? t("즐겨찾기 해제") : t("즐겨찾기에 추가")}
      title={favorite ? t("즐겨찾기 해제") : t("즐겨찾기에 추가")}
      data-testid={`project-favorite-toggle-${p.id}`}
      onClick={onFavorite}
      className={cx(
        "pointer-events-auto relative z-20 shrink-0 cursor-pointer rounded-md p-1 transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-accent",
        favorite ? "text-yellow-500 hover:text-yellow-600" : "text-gray-400 hover:text-foreground",
      )}
    >
      <div className="icon-container icon-sm text-current text-lg" aria-hidden="true">
        <Star fill={favorite ? "currentColor" : "none"} />
      </div>
    </button>
  );
}

const CARD =
  "bg-background dark:bg-gray-50 outline outline-1 outline-black/[0.08] dark:outline-white/[0.1] rounded-lg relative overflow-hidden hover:outline-black/[0.12] hover:shadow-[0_3px_8px_rgba(0,0,0,0.06)] dark:hover:outline-white/15 dark:hover:bg-gray-100 transition-all shadow-[0_1px_2px_rgba(0,0,0,0.04)] dark:shadow-[0_1px_2px_rgba(0,0,0,0.2)] group/card";

const PREVIEW_STYLE: React.CSSProperties = {
  backgroundImage: "radial-gradient(circle, color-mix(in srgb, var(--foreground) 8%, transparent) 1px, transparent 1px)",
  backgroundSize: "10px 10px",
  backgroundPosition: "calc(50% - 5px) calc(50% - 10px)",
  boxShadow: "inset 0 0 0 0.5px var(--gray-200)",
};

const MAX_TILES = 8;

function tileCols(n: number) {
  if (n <= 3) return Math.max(n, 1);
  if (n === 4) return 2;
  if (n <= 6) return 3;
  return 4;
}

function ServiceTile({ p, s }: { p: Project; s: CanvasService }) {
  return (
    <Link
      title={s.name}
      aria-label={t("{name} 열기", { name: s.name })}
      href={withCtx("/", { project: p.id }, { svc: s.id })}
      className="pointer-events-auto rounded-xl group/icon focus:outline-none focus-visible:ring-2 focus-visible:ring-accent"
    >
      <div className="flex h-10 w-10 items-center justify-center rounded-lg border border-black/[0.08] bg-background transition-colors group-hover/icon:bg-gray-100 dark:border-white/[0.1] dark:bg-gray-100">
        <div aria-hidden="true" className="icon-container icon-md text-current text-2xl">
          {isBucket(s) ? <BucketIcon /> : <ServiceIcon service={s} />}
        </div>
      </div>
    </Link>
  );
}

export function ProjectCard({ p, favorite, onFavorite }: { p: Project; favorite: boolean; onFavorite: () => void }) {
  const status = useProjectStatus(p);
  const tiles = status.all.slice(0, MAX_TILES);
  return (
    <div className="h-full" data-project-id={p.id}>
      <div className={cx(CARD, "h-full")}>
        <Link className="absolute inset-0" href={withCtx("/", { project: p.id })}>
          <span className="sr-only">{t("{name} 프로젝트 열기", { name: p.name })}</span>
        </Link>
        <div className="pointer-events-none relative z-10 flex h-full flex-col">
          <div className={cx("px-4 pt-4", p.description ? "pb-2" : "pb-4")}>
            <div className="flex items-center gap-1.5">
              <span className="truncate text-sm font-semibold">{p.name}</span>
              <div className="ml-auto flex h-[26px] items-center">
                <FavoriteButton p={p} favorite={favorite} onFavorite={onFavorite} />
              </div>
            </div>
            {p.description && (
              <p className="mb-1 mt-0.5 truncate text-gray-500" style={{ fontSize: 14 }}>
                {p.description}
              </p>
            )}
          </div>
          <div className="flex min-h-0 flex-1 flex-col" style={{ padding: "0px 8px 8px" }}>
            <div
              data-project-preview="true"
              aria-busy={status.pending}
              className="relative flex min-h-[220px] flex-1 items-center justify-center rounded bg-secondaryBg py-6 transition-colors dark:bg-background dark:group-hover/card:bg-secondaryBg"
              style={PREVIEW_STYLE}
            >
              {tiles.length > 0 && (
                <div style={{ transform: "translateY(-10px)" }}>
                  <div className="grid w-fit gap-[10px]" style={{ gridTemplateColumns: `repeat(${tileCols(tiles.length)}, minmax(0, 1fr))` }}>
                    {tiles.map((s) => (
                      <ServiceTile key={s.id} p={p} s={s} />
                    ))}
                  </div>
                </div>
              )}
              <div className="absolute bottom-0 left-0 right-0 flex items-center px-2 py-2 text-xs text-neutral-secondary">
                <StatusLine status={status} />
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

export function ProjectRow({ p, favorite, onFavorite }: { p: Project; favorite: boolean; onFavorite: () => void }) {
  const status = useProjectStatus(p);
  const tiles = status.all.slice(0, 4);
  const more = status.all.length - tiles.length;
  return (
    <div className="group/card relative transition-colors hover:bg-secondaryBg dark:hover:bg-gray-100" data-project-id={p.id}>
      <Link className="absolute inset-0" href={withCtx("/", { project: p.id })}>
        <span className="sr-only">{t("{name} 프로젝트 열기", { name: p.name })}</span>
      </Link>
      <div className="pointer-events-none relative z-10 flex min-h-[60px] items-center gap-4 px-4 py-2.5">
        <div className="min-w-0 flex-1">
          <span className="block truncate text-sm font-semibold">{p.name}</span>
          {p.description && <p className="truncate text-sm text-gray-500">{p.description}</p>}
        </div>
        <div className="hidden shrink-0 items-center gap-1.5 md:flex">
          {tiles.map((s) => (
            <Link
              key={s.id}
              title={s.name}
              aria-label={t("{name} 열기", { name: s.name })}
              href={withCtx("/", { project: p.id }, { svc: s.id })}
              className="pointer-events-auto rounded-lg group/icon focus:outline-none focus-visible:ring-2 focus-visible:ring-accent"
            >
              <div className="flex h-7 w-7 items-center justify-center rounded-md border border-black/[0.08] bg-background transition-colors group-hover/icon:bg-gray-100 dark:border-white/[0.1] dark:bg-gray-100">
                <div aria-hidden="true" className="icon-container icon-sm text-current text-lg">
                  {isBucket(s) ? <BucketIcon /> : <ServiceIcon service={s} className="size-4" />}
                </div>
              </div>
            </Link>
          ))}
          {more > 0 && <span className="text-xs text-neutral-secondary">+{more}</span>}
        </div>
        <div className="hidden w-[300px] shrink-0 items-center text-xs text-neutral-secondary sm:flex">
          <StatusLine status={status} />
        </div>
        <FavoriteButton p={p} favorite={favorite} onFavorite={onFavorite} />
      </div>
    </div>
  );
}

export const LIST_FRAME = cx(CARD.replace(" group/card", ""), "divide-y divide-black/[0.06] dark:divide-white/[0.06]");

export function ProjectSkeleton({ view }: { view: "grid" | "list" }) {
  if (view === "list")
    return (
      <div className={LIST_FRAME} aria-busy="true">
        {Array.from({ length: 3 }).map((_, i) => (
          <div key={i} className="flex h-[60px] items-center px-4">
            <div className="h-4 w-28 animate-pulse rounded bg-gray-100" />
          </div>
        ))}
        <span className="sr-only">{t("프로젝트 불러오는 중…")}</span>
      </div>
    );
  return (
    <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3" aria-busy="true">
      {Array.from({ length: 3 }).map((_, i) => (
        <div key={i} className={cx(CARD, "h-[307px] px-4 pt-4")}>
          <div className="h-4 w-28 animate-pulse rounded bg-gray-100" />
        </div>
      ))}
      <span className="sr-only">{t("프로젝트 불러오는 중…")}</span>
    </div>
  );
}
