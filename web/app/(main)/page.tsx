"use client";

import Link from "next/link";
import { Suspense, useEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ChevronRight, Database, Layers, LayoutTemplate, OctagonPause, Plus, RefreshCcw, TriangleAlert } from "lucide-react";
import { api } from "@/lib/api";
import { qk, useCanvas, useCaps } from "@/lib/queries";
import { lock } from "@/components/Perm";
import { useCanvasState, withCtx, type NewView } from "@/lib/nav";
import { useWorkspace } from "@/lib/workspace";
import { Canvas } from "@/components/canvas/Canvas";
import { ServiceContextMenu } from "@/components/canvas/CanvasMenus";
import { useContextTrigger } from "@/components/ContextMenu";
import { ServiceCard } from "@/components/ServiceCard";
import { ServicePanel } from "@/components/panel/ServicePanel";
import { StagedBar } from "@/components/panel/StagedBar";
import { CreatePalette } from "@/components/CreatePalette";
import { SyncEnvironmentDialog } from "@/components/SyncEnvironmentDialog";
import { Button, ErrorBox, Kbd, Spinner, cx, errorMessage } from "@/components/ui";
import { BrandIcon } from "@/components/ServiceIcon";
import { toast } from "@/lib/uiState";
import { t, tr } from "@/lib/i18n";
import { useKeyboardShortcuts } from "@/components/KeyboardShortcuts";
import { RestrictedEnvironmentNotice, useGuardrails } from "@/components/AccessControlsSettings";

function useIsDesktop() {
  const [desk, setDesk] = useState(true);
  useEffect(() => {
    const m = window.matchMedia("(min-width: 768px)");
    const h = () => setDesk(m.matches);
    h();
    m.addEventListener("change", h);
    return () => m.removeEventListener("change", h);
  }, []);
  return desk;
}

/** Width of the service panel column (matches the shell's nested-canvas grid). */
function panelWidth(w: number) {
  if (w >= 1900) return 1200;
  if (w >= 1536) return 1024;
  if (w >= 1280) return 920;
  if (w >= 1024) return Math.round((w - 56) * 0.7);
  return w;
}

function Center({ children }: { children: React.ReactNode }) {
  return <div className="dot-grid flex h-full items-center justify-center px-4">{children}</div>;
}

/** First-run canvas: the common ways to start, one click each (the palette opens on the matching step). */
function EmptyCanvas({ canEdit, onPick, onFunction }: { canEdit: boolean; onPick: (v: NewView) => void; onFunction: () => void }) {
  // 배포 출처 guardrail: GitHub 조직으로 확인할 수 없는 출처는 팔레트처럼 숨긴다(서버도 403).
  const restricted = useGuardrails().deploymentSources;
  const all: { id: string; label: string; desc: string; icon: React.ReactNode; run: () => void }[] = [
    { id: "repo", label: t("GitHub 저장소"), desc: t("저장소를 빌드해서 배포합니다"), icon: <BrandIcon name="github" className="size-5" />, run: () => onPick("repo") },
    { id: "database", label: t("데이터베이스"), desc: "Postgres, MySQL, Redis, MongoDB", icon: <Database className="size-5 text-gray-600" />, run: () => onPick("database") },
    { id: "template", label: t("템플릿|item"), desc: t("미리 구성된 서비스 묶음"), icon: <LayoutTemplate className="size-5 text-gray-600" />, run: () => onPick("template") },
    { id: "image", label: t("Docker 이미지"), desc: t("레지스트리의 이미지를 그대로 실행합니다"), icon: <BrandIcon name="docker" className="size-5" />, run: () => onPick("image") },
    { id: "function", label: "Function", desc: t("TypeScript 파일 하나로 시작합니다"), icon: <BrandIcon name="function" className="size-5" />, run: onFunction },
    { id: "compose", label: "Docker Compose", desc: t("compose 파일의 서비스를 한 번에 가져옵니다"), icon: <Layers className="size-5 text-gray-600" />, run: () => onPick("compose") },
  ];
  const tiles = restricted ? all.filter((x) => x.id === "repo" || x.id === "database") : all;
  return (
    <div className="dot-grid flex h-full flex-col items-center justify-center overflow-y-auto px-6 py-16 text-center">
      <div className="flex size-12 items-center justify-center rounded-xl border border-dashed border-gray-300 bg-secondaryBg">
        <Plus className="size-5 text-gray-600" aria-hidden />
      </div>
      <h1 className="mt-4 text-lg font-semibold">{t("첫 서비스를 만들어 보세요")}</h1>
      <p className="mt-1.5 max-w-sm text-sm leading-relaxed text-gray-600">
        {t("GitHub 저장소, Docker 이미지, 데이터베이스, 템플릿 중에서 고를 수 있습니다.")}
      </p>
      <ul className="mt-6 grid w-full max-w-[640px] grid-cols-1 gap-2 sm:grid-cols-2" aria-label={t("서비스 추가")}>
        {tiles.map((tile) => (
          <li key={tile.id}>
            <button
              type="button"
              {...lock(canEdit)}
              onClick={() => canEdit && tile.run()}
              className="group flex w-full cursor-pointer items-center gap-3 rounded-lg border border-gray-200 bg-secondaryBg px-4 py-3 text-left transition-[border-color,box-shadow] duration-150 hover:border-pink-300 hover:shadow-[0px_0px_0px_4px_rgba(166,_103,_228,_0.08)] focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-500 aria-disabled:cursor-not-allowed aria-disabled:opacity-60 aria-disabled:hover:border-gray-200 aria-disabled:hover:shadow-none"
            >
              <span className="flex size-9 shrink-0 items-center justify-center rounded-md border border-gray-200 bg-background">{tile.icon}</span>
              <span className="min-w-0 flex-1">
                <span className="block truncate text-sm font-medium">{tile.label}</span>
                <span className="block truncate text-xs text-gray-500">{tile.desc}</span>
              </span>
              <ChevronRight className="size-4 shrink-0 text-gray-400 transition-transform group-hover:translate-x-0.5" aria-hidden />
            </button>
          </li>
        ))}
      </ul>
      <p className="mt-5 text-xs text-gray-500">
        {tr("{keys}로 언제든 만들기 팔레트를 열 수 있습니다", { keys: <Kbd>⌘ / Ctrl + K</Kbd> })}
      </p>
    </div>
  );
}

function CanvasPageInner() {
  const qc = useQueryClient();
  const ws = useWorkspace();
  const caps = useCaps(ws.pid);
  const restricted = !!ws.env?.restricted && !caps.canAdmin; // Environment RBAC: 리소스는 프로젝트 admin만
  const canvas = useCanvas(ws.env?.id, !restricted);
  const { svc, tab, dep, dtab, isNew, go } = useCanvasState();
  const desktop = useIsDesktop();
  const [syncing, setSyncing] = useState(false);
  const listSuppress = useRef(false);
  const [listCtx, setListCtx] = useState<{ x: number; y: number; id: string } | null>(null);
  const listTrigger = useContextTrigger((x, y, e) => {
    const id = (e.target as HTMLElement | null)?.closest?.<HTMLElement>("[data-svc-id]")?.dataset.svcId;
    if (id) {
      listSuppress.current = true;
      setTimeout(() => (listSuppress.current = false), 900);
      setListCtx({ x, y, id });
    }
  });
  const list = useMemo(() => canvas.data?.services ?? [], [canvas.data]);
  // `leavingSvc` stays set until the URL actually changes, so the pane never flashes back in before unmounting.
  const [leavingSvc, setLeavingSvc] = useState<string | null>(null);
  const [prevSvc, setPrevSvc] = useState(svc);
  if (prevSvc !== svc) {
    setPrevSvc(svc);
    setLeavingSvc(null);
  }
  const leaving = !!svc && leavingSvc === svc;
  const leaveTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => () => void (leaveTimer.current && clearTimeout(leaveTimer.current)), []);
  const cancelLeave = () => {
    if (leaveTimer.current) clearTimeout(leaveTimer.current);
    leaveTimer.current = null;
    setLeavingSvc(null);
  };
  const closePanel = (patch: Parameters<typeof go>[0] = { svc: null, dep: null }) => {
    if (!svc || leaving) return;
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return go(patch);
    setLeavingSvc(svc);
    leaveTimer.current = setTimeout(() => {
      leaveTimer.current = null;
      go(patch);
    }, 160);
  };
  const open = (id: string) => {
    if (id === svc) return closePanel();
    cancelLeave();
    go({ svc: id, dep: null, tab: "deployments" });
  };
  const selected = list.find((s) => s.id === svc);
  const envId = ws.env?.id ?? "";
  const pending = ws.isPending || (!!ws.env && canvas.isPending);
  const error = ws.error ?? canvas.error;
  const canEdit = caps.canEdit && !restricted;
  // Part of the canvas covered on the right: the open service pane. The activity panel is a shell column
  const canvasInset = typeof window === "undefined" ? 0 : svc && desktop ? panelWidth(window.innerWidth) : 0;
  const creatingFunction = useRef(false);
  const createFunction = async () => {
    if (!canEdit || !ws.pid || !envId || creatingFunction.current) return;
    creatingFunction.current = true;
    try {
      const created = await api.createService(ws.pid, { source_type: "function", environment_id: envId });
      await Promise.all([qc.invalidateQueries({ queryKey: qk.canvas(envId) }), qc.invalidateQueries({ queryKey: qk.project(ws.pid) })]);
      go({ isNew: null, svc: created.id, tab: "code", dep: null }, "replace");
    } catch (e) {
      toast(errorMessage(e), "error");
    } finally {
      creatingFunction.current = false;
    }
  };
  useKeyboardShortcuts([
    {
      keys: ".",
      label: t("캔버스에 Function 만들기"),
      group: t("프로젝트 캔버스"),
      enabled: canEdit && !!ws.pid && !svc && !isNew,
      onTrigger: createFunction,
    },
  ]);

  return (
    <div className="relative h-full overflow-hidden">
      {restricted && ws.env ? (
        <Center>
          <RestrictedEnvironmentNotice name={ws.env.name} />
        </Center>
      ) : pending ? (
        <Center>
          <Spinner />
        </Center>
      ) : error ? (
        <Center>
          <div className="w-full max-w-md">
            <ErrorBox error={error} />
          </div>
        </Center>
      ) : !ws.pid ? (
        <Center>
          <div className="text-center">
            <h1 className="text-lg font-semibold">{t("프로젝트가 없습니다")}</h1>
            <p className="mt-1.5 max-w-xs text-sm text-gray-600">{t("이 작업 공간에는 아직 프로젝트가 없습니다. 프로젝트를 만들면 이 캔버스에 서비스가 나타납니다.")}</p>
            <div className="mt-5 flex justify-center gap-2">
              {caps.canCreateProjects && (
                <Link href={withCtx("/projects/", { ws: ws.wsid }, { new: "project" })} className="inline-flex h-[34px] items-center gap-2 rounded-md border border-pink-500 bg-pink-500 px-3 text-sm text-white hover:bg-pink-600">
                  <Plus className="size-4" aria-hidden /> {t("새 프로젝트")}
                </Link>
              )}
              <Link href={withCtx("/projects/", { ws: ws.wsid })} className="inline-flex h-[34px] items-center rounded-md border border-gray-200 px-3 text-sm text-foreground hover:bg-gray-100">
                {t("모든 프로젝트")}
              </Link>
            </div>
          </div>
        </Center>
      ) : list.length === 0 ? (
        <EmptyCanvas canEdit={canEdit} onPick={(v) => go({ isNew: v })} onFunction={createFunction} />
      ) : desktop ? (
        <Canvas
          key={envId}
          envId={envId}
          services={list}
          stacks={canvas.data?.stacks}
          pid={ws.pid ?? undefined}
          selected={svc}
          onOpen={open}
          onBackground={() => svc && closePanel()}
          envName={ws.env?.name}
          onCreate={(v) => go({ isNew: v })}
          rightInset={canvasInset}
        />
      ) : (
        <div className="dot-grid h-full overflow-y-auto px-4 pb-10 pt-16">
          <div className="mx-auto flex max-w-[288px] flex-col gap-5" {...listTrigger}>
            {list.map((s) => (
              <div
                key={s.id}
                role="button"
                tabIndex={0}
                onClick={() => !listSuppress.current && open(s.id)}
                onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && (e.preventDefault(), open(s.id))}
                aria-label={t("{name} 서비스 열기", { name: s.name })}
                data-svc-id={s.id}
                className="cursor-pointer rounded-xl text-left"
              >
                <ServiceCard svc={s} envId={envId} selected={s.id === svc} />
              </div>
            ))}
          </div>
        </div>
      )}

      {!pending && ws.pid && !(svc && desktop) && (
        <div className="pointer-events-none absolute right-4 top-4 z-20 flex items-center gap-2">
          {ws.env && ws.envs.length > 1 && (
            <Button
              className="pointer-events-auto !bg-secondaryBg hover:!border-gray-200 hover:!bg-gray-100 dark:!bg-gray-100 dark:hover:!bg-gray-200"
              {...lock(canEdit)}
              onClick={() => setSyncing(true)}
            >
              <RefreshCcw className="text-gray-500" /> {t("동기화")}
            </Button>
          )}
          <Button className="pointer-events-auto" {...lock(canEdit)} onClick={() => go({ isNew: "1" })}>
            <Plus className="text-gray-500" /> {t("추가")}
          </Button>
        </div>
      )}
      {ws.env && ws.envs.length > 1 && (
        <SyncEnvironmentDialog target={ws.env} envs={ws.envs} open={syncing} onClose={() => setSyncing(false)} />
      )}

      {/* 서버 owner가 이 프로젝트(또는 작업 공간)를 정지했다: 배포·재시작이 막힌다. */}
      {!pending && ws.detail?.owner_hold && (
        <div
          role="alert"
          className="absolute left-1/2 top-16 z-30 flex w-[min(32rem,calc(100%-2rem))] -translate-x-1/2 items-start gap-2 rounded-md border border-gray-200 bg-danger-surface px-3 py-2 text-sm text-danger"
        >
          <OctagonPause className="mt-0.5 size-4 shrink-0" />
          <span>
            {tr("서버 owner가 이 {target}를 정지했습니다{reason}. 재개될 때까지 배포와 재시작을 할 수 없습니다.", {
              target: ws.detail.owner_hold.scope === "workspace" ? t("작업 공간") : t("프로젝트"),
              reason: ws.detail.owner_hold.reason ? ` (${ws.detail.owner_hold.reason})` : "",
            })}
          </span>
        </div>
      )}

      {/* 서버 owner가 멤버가 아닌 프로젝트: 배포가 다른 사람의 GitHub 연결을 쓴다(허용, 감사 기록에 남음). */}
      {!pending && ws.detail?.github_borrowed && ws.detail.github_account && !(svc && !desktop) && (
        <div
          role="status"
          className="absolute bottom-4 left-4 z-30 flex max-w-sm items-start gap-2 rounded-md border border-yellow-200 bg-warn-surface px-3 py-2 text-xs leading-relaxed text-warn"
        >
          <TriangleAlert className="mt-0.5 size-3.5 shrink-0" />
          <span>
            {tr("이 프로젝트의 GitHub 배포는 {user}{login}의 GitHub 연결을 씁니다. 서버 owner 권한으로 배포하면 감사 기록에 남습니다.", {
              user: <b>{ws.detail.github_account.user_name}</b>,
              login: ws.detail.github_account.login ? ` (@${ws.detail.github_account.login})` : "",
            })}
          </span>
        </div>
      )}

      {!pending && !restricted && envId && list.length > 0 && (
        <div
          className={cx(
            "pointer-events-none absolute left-0 z-40 flex justify-center px-4",
            svc && !desktop ? "bottom-4" : ws.isDefaultEnv ? "top-2" : "top-7",
          )}
          style={{ right: canvasInset }}
        >
          <StagedBar envId={envId} services={list} canEdit={canEdit} />
        </div>
      )}

      {listCtx && ws.env && list.find((s) => s.id === listCtx.id) && (
        <ServiceContextMenu
          key={`${listCtx.id}${listCtx.x}${listCtx.y}`}
          x={listCtx.x}
          y={listCtx.y}
          svc={list.find((s) => s.id === listCtx.id)!}
          envId={envId}
          envName={ws.env.name}
          onDone={() => setListCtx((c) => (c === listCtx ? null : c))}
        />
      )}

      {svc && ws.env && (
        <ServicePanel
          key={`${svc}:${envId}`}
          svc={selected}
          loading={canvas.isPending}
          env={ws.env}
          tab={tab}
          dep={dep}
          dtab={dtab}
          leaving={leaving}
          onClose={() => closePanel({ svc: null, dep: null, tab: "deployments" })}
          onTab={(v) => go({ tab: v, dep: null }, "replace")}
          onDep={(id) => go({ dep: id, dtab: "build" }, id ? "push" : "replace")}
          onDtab={(v) => go({ dtab: v }, "replace")}
        />
      )}

      {isNew && canEdit && ws.pid && ws.env && (
        <CreatePalette
          pid={ws.pid}
          env={ws.env}
          view={isNew}
          services={list}
          onView={(v) => go({ isNew: v }, "replace")}
          onClose={() => go({ isNew: null }, "replace")}
          onCreated={(id) => go({ isNew: null, svc: id, tab: "deployments", dep: null }, "replace")}
        />
      )}
    </div>
  );
}

export default function CanvasPage() {
  return (
    <Suspense
      fallback={
        <div className="dot-grid flex h-full items-center justify-center">
          <Spinner />
        </div>
      }
    >
      <CanvasPageInner />
    </Suspense>
  );
}
