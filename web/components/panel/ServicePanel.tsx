"use client";

import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Ban, Loader2, Pencil, X } from "lucide-react";
import { api, type CanvasService, type Environment, type OwnerHold } from "@/lib/api";
import { PANEL_TABS, type DepTab, type PanelTab } from "@/lib/nav";
import { useServiceActions } from "@/lib/useServiceActions";
import { qk, useCaps } from "@/lib/queries";
import { Button, IconButton, cx, errorMessage, useEscape } from "../ui";
import { HeaderTabs, escCloses, tabId, tabPanelId } from "./deployments/HeaderTabs";
import { ServiceIcon } from "../ServiceIcon";
import { PanelPortal } from "../Shell";
import { DeploymentsTab } from "./DeploymentsTab";
import { DeploymentPanel } from "./DeploymentPanel";
import { VariablesTab } from "./VariablesTab";
import { MetricsTab } from "./MetricsTab";
import { SettingsTab, VolumeBackupsTab } from "./SettingsTab";
import { PitrSection } from "./PitrSection";
import { DataTab } from "./DataTab";
import { BucketTab } from "./BucketTab";
import { ShellTab } from "./ShellTab";
import { FunctionTab } from "./FunctionTab";
import { AdminTab } from "./AdminTab";
import { useKeyboardShortcuts } from "@/components/KeyboardShortcuts";
import { SelfServiceNotice } from "@/components/SelfService";
import { t, tr } from "@/lib/i18n";

const TAB_LABEL: Record<PanelTab, string> = {
  deployments: t("배포"),
  backups: t("백업"),
  code: t("코드"),
  data: t("데이터베이스"),
  bucket: t("버킷"),
  variables: t("변수"),
  metrics: t("지표"),
  shell: t("셸"),
  settings: t("설정"),
  admin: t("어드민"),
};

export const panelCls = cx(
  "absolute inset-x-0 bottom-0 top-0 flex flex-col overflow-hidden bg-background shadow-md outline-none animate-sheet-in dark:bg-secondaryBg",
  "md:left-auto md:right-0 md:top-6 md:w-full md:animate-metro-top-in md:rounded-t-xl md:border xl:w-[calc(100%-24px)]",
  "lg:rounded-none lg:rounded-br-xl lg:rounded-tl-xl lg:border-r-0 lg:border-l lg:border-t",
);

function ServiceName({ svc, env, canEdit }: { svc: CanvasService; env: Environment; canEdit: boolean }) {
  const qc = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState(svc.name);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const input = useRef<HTMLInputElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  // Set while Esc cancels so the input's blur (fired as it unmounts) does not save.
  const cancelled = useRef(false);
  // Esc while renaming cancels the edit instead of closing the pane (this entry sits on top of the Esc stack).
  useEscape(() => stop(), editing);

  function stop() {
    cancelled.current = true;
    setEditing(false);
    setValue(svc.name);
    setError(null);
    requestAnimationFrame(() => trigger.current?.focus());
  }
  async function save() {
    const next = value.trim();
    if (!next || next === svc.name) return stop();
    setBusy(true);
    setError(null);
    try {
      await api.updateService(svc.id, { name: next });
      await qc.invalidateQueries({ queryKey: qk.canvas(env.id) });
      qc.invalidateQueries({ queryKey: ["project"] });
      setEditing(false);
      requestAnimationFrame(() => trigger.current?.focus());
    } catch (e) {
      setError(e);
      input.current?.focus();
    } finally {
      setBusy(false);
    }
  }

  if (editing)
    return (
      <form
        className="relative flex min-w-0 flex-1 items-center"
        onSubmit={(e) => {
          e.preventDefault();
          save();
        }}
      >
        <label htmlFor="panel-rename" className="sr-only">
          {t("서비스 이름")}
        </label>
        <input
          id="panel-rename"
          ref={input}
          autoFocus
          value={value}
          disabled={busy}
          spellCheck={false}
          autoComplete="off"
          onFocus={(e) => e.currentTarget.select()}
          onChange={(e) => setValue(e.target.value.toLowerCase())}
          onBlur={() => !cancelled.current && !busy && !error && save()}
          aria-invalid={!!error || undefined}
          aria-describedby={error ? "panel-rename-error" : undefined}
          className={cx(
            "text-h3 -ml-1.5 h-10 w-full min-w-0 max-w-[420px] rounded-md border bg-transparent px-1.5 font-semibold text-foreground focus:outline-none",
            error ? "border-red-500" : "border-gray-200 focus:border-pink-400",
          )}
        />
        {busy && <Loader2 className="ml-2 size-4 shrink-0 animate-spin text-gray-500" aria-label={t("저장 중")} />}
        {error ? (
          <p id="panel-rename-error" role="alert" className="absolute left-0 top-full mt-1 truncate text-xs text-red-600">
            {errorMessage(error)}
          </p>
        ) : null}
      </form>
    );

  return (
    <h1 id="panel-title" className="text-h3 flex min-w-0 flex-grow items-center font-semibold">
      {canEdit ? (
        <button
          ref={trigger}
          type="button"
          onClick={() => {
            cancelled.current = false;
            setValue(svc.name);
            setEditing(true);
          }}
          title={t("이름 변경")}
          aria-label={t("{name}, 이름 변경", { name: svc.name })}
          className="group -ml-1.5 flex min-w-0 cursor-pointer items-center rounded-md px-1.5 text-left hover:bg-gray-200 focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-500"
        >
          <span className="truncate">{svc.name}</span>
          <span className="ml-5 hidden size-[26px] shrink-0 items-center justify-center p-1 text-gray-500 group-hover:flex group-focus-visible:flex">
            <Pencil className="size-4" aria-hidden />
          </span>
        </button>
      ) : (
        <span className="truncate" title={svc.name}>
          {svc.name}
        </span>
      )}
    </h1>
  );
}

/**
 * Metro-pane header, modeled on Railway's dashboard: service icon, name and the close button only. The actions keel used to put here live where
 * the layout places them: redeploy / restart / remove in the active deployment card's kebab (DeploymentsTab), the first deploy in
 * the Deployments empty state, stop / delete in Settings → Danger zone (and the canvas card's context menu).
 */
function Header({ svc, env, onClose }: { svc: CanvasService; env: Environment; onClose: () => void }) {
  const canEdit = useCaps(svc.project_id).canEdit;
  return (
    <div className="mb-6 flex w-full items-center justify-between space-x-4 px-5 md:px-0">
      <div className="flex min-w-0 items-center gap-4">
        <div className="flex min-w-0 items-center space-x-4">
          <span className="icon-container icon-xl shrink-0 rounded-full text-2xl text-foreground">
            <ServiceIcon service={svc} className="size-8" />
          </span>
          <ServiceName key={svc.id} svc={svc} env={env} canEdit={canEdit} />
        </div>
      </div>
      <div className="flex items-center gap-4">
        <IconButton label={t("패널 닫기")} onClick={onClose} className="shrink-0 !text-foreground" data-testid="close-metro-pane">
          <X />
        </IconButton>
      </div>
    </div>
  );
}

export function ServicePanel({
  svc,
  loading,
  env,
  tab,
  dep,
  dtab,
  onClose,
  onTab,
  onDep,
  onDtab,
  leaving,
}: {
  /** true while the pane plays its exit animation (the page unmounts it afterwards). */
  leaving?: boolean;
  svc: CanvasService | undefined;
  loading: boolean;
  env: Environment;
  tab: PanelTab;
  dep: string | null;
  dtab: DepTab;
  onClose: () => void;
  onTab: (t: PanelTab) => void;
  onDep: (id: string | null) => void;
  onDtab: (t: DepTab) => void;
}) {
  const sid = svc?.id ?? "";
  const actions = useServiceActions(sid, env.id);
  const panel = useRef<HTMLElement>(null);
  // Esc closes the pane; the first Esc only leaves a focused field (and never fires from the shell's terminal).
  useEscape(() => escCloses(panel.current) && onClose(), !dep);
  useEffect(() => {
    panel.current?.focus({ preventScroll: true });
  }, [sid]);
  // Closing deployment details returns focus to the card that opened it (or the pane), not to <body>.
  const lastDep = useRef<string | null>(null);
  useEffect(() => {
    if (dep) {
      lastDep.current = dep;
      return;
    }
    const prev = lastDep.current;
    lastDep.current = null;
    if (!prev) return;
    const a = document.activeElement;
    if (a && a !== document.body && panel.current?.contains(a)) return;
    const card = panel.current?.querySelector<HTMLElement>(`[data-dep-link="${CSS.escape(prev)}"]`);
    (card ?? panel.current)?.focus({ preventScroll: !card });
  }, [dep]);
  const caps = useCaps(svc?.project_id);
  // Viewer는 셸과 데이터 브라우저를 쓸 수 없다(서버도 403). bucket 탭은 viewer도 본다(목록만, 자격 증명은 가림).
  const isBucket = svc?.database_kind === "bucket";
  // aven-server 자신(self 서비스)은 컨테이너·변수가 Aven 밖에 있어 배포와 설정(소스·빌드)만 다룬다.
  const self = !!svc?.instance.self;
  const tabs = PANEL_TABS.filter(
    (tb) =>
      (!self || tb === "deployments" || tb === "settings" || tb === "admin") &&
      // 어드민 탭은 서버 관리자(users.role owner/admin)만 본다. 워크스페이스·프로젝트 역할과 무관하다.
      (tb !== "admin" || caps.instanceAdmin) &&
      (tb !== "data" || (svc?.source_type === "database" && !isBucket)) &&
      (tb !== "bucket" || isBucket) &&
      (tb !== "backups" || (svc?.volumes.length ?? 0) > 0) &&
      // function 코드 API는 member 이상만 쓸 수 있다.
      (tb !== "code" || (svc?.source_type === "function" && caps.canEdit)) &&
      (caps.canEdit || (tb !== "data" && tb !== "shell")),
  );
  const cur = tabs.includes(tab) ? tab : "deployments";
  const stacked = !!dep;
  useKeyboardShortcuts([
    { keys: "g d", label: t("배포 탭으로 이동"), group: t("서비스"), enabled: tabs.includes("deployments"), onTrigger: () => onTab("deployments") },
    { keys: "g v", label: t("변수 탭으로 이동"), group: t("서비스"), enabled: tabs.includes("variables"), onTrigger: () => onTab("variables") },
    { keys: "g m", label: t("지표 탭으로 이동"), group: t("서비스"), enabled: tabs.includes("metrics"), onTrigger: () => onTab("metrics") },
    { keys: "g s", label: t("설정 탭으로 이동"), group: t("서비스"), enabled: tabs.includes("settings"), onTrigger: () => onTab("settings") },
  ]);
  const header = svc && (
    <div className="z-50 flex min-w-0 flex-col border-b !border-gray-100 pt-8 md:px-12 md:pt-12 md:!border-gray-200">
      <Header svc={svc} env={env} onClose={onClose} />
      <div className="mb-[-2px] px-5 md:px-0">
        <HeaderTabs
          tabs={tabs.map((tb) => ({ id: tb, label: TAB_LABEL[tb] }))}
          value={cur}
          onChange={onTab}
          label={t("서비스 탭")}
          idPrefix="svc"
          className="border-b border-gray-100 pb-3 md:border-transparent md:pb-0"
        />
      </div>
    </div>
  );
  return (
    <PanelPortal>
      <aside
        ref={panel}
        tabIndex={-1}
        aria-labelledby="panel-title"
        data-noscroll
        onPointerDown={(e) => e.stopPropagation()}
        className={cx(
          "metro-pane bottom-0 right-0 top-0 z-[30] w-full overflow-hidden shadow-md outline-none md:mt-6 md:rounded-t-xl md:border lg:rounded-none lg:rounded-br-xl lg:rounded-tl-xl lg:border-r-0 lg:border-l lg:border-t xl:w-[calc(100%-24px)]",
          leaving ? "pointer-events-none animate-metro-out" : "animate-metro-in",
          "transition-[translate,filter] duration-200",
          stacked && "md:-translate-x-6 md:translate-y-6 md:mb-6 lg:rounded-tr-xl lg:border-r",
        )}
      >
        {!svc ? (
          loading ? (
            <PanelSkeleton />
          ) : (
            <div className="flex h-full flex-col bg-background dark:bg-secondaryBg">
              <div className="flex justify-end px-5 pt-8 md:px-12 md:pt-12">
                <IconButton label={t("패널 닫기")} onClick={onClose} className="!text-foreground" data-testid="close-metro-pane">
                  <X />
                </IconButton>
              </div>
              <div className="px-5 pt-6 md:px-12">
                <div role="status" className="rounded-lg border border-dashed border-gray-200 px-6 py-10 text-center">
                  <div className="text-sm font-semibold text-foreground">{t("서비스를 찾을 수 없습니다")}</div>
                  <div className="mt-1 text-sm text-gray-500">
                    {tr("이 서비스는 {env} 환경에 없거나 삭제되었습니다.", { env: <span className="font-mono text-foreground">{env.name}</span> })}
                  </div>
                  <Button variant="outline" className="mx-auto mt-4" onClick={onClose}>
                    {t("캔버스로 돌아가기")}
                  </Button>
                </div>
              </div>
            </div>
          )
        ) : (
          <div className="service-tabs grid min-h-full w-full grid-cols-[minmax(0px,_1fr)] grid-rows-[auto_minmax(0px,_1fr)] bg-background sm:h-full sm:overflow-auto dark:bg-secondaryBg">
            {header}
            <div className="metro-pane-content sm:overflow-auto sm:[scrollbar-gutter:stable]">
              <div role="tabpanel" id={tabPanelId("svc")} aria-labelledby={tabId("svc", cur)} className="h-full px-5 pt-8 md:px-12">
                {svc.instance.admin_hold && cur !== "admin" && <AdminHoldNotice hold={svc.instance.admin_hold} />}
                {svc.instance.self && cur === "deployments" && <SelfServiceNotice self={svc.instance.self} svcId={svc.id} envId={env.id} />}
                {cur === "deployments" && <DeploymentsTab svc={svc} env={env} actions={actions} onOpenDep={(id) => onDep(id)} onTab={onTab} />}
                {cur === "backups" && (
                  <>
                    <VolumeBackupsTab svc={svc} />
                    {svc.source_type === "database" && (svc.database_kind === "postgres" || svc.database_kind === "mysql") && <PitrSection svc={svc} env={env} />}
                  </>
                )}
                {cur === "code" && <FunctionTab key={`${svc.id}:${env.id}`} svc={svc} env={env} actions={actions} />}
                {cur === "data" && <DataTab svc={svc} env={env} />}
                {cur === "bucket" && <BucketTab key={`${svc.id}:${env.id}`} svc={svc} env={env} />}
                {cur === "variables" && <VariablesTab key={svc.id} svc={svc} env={env} />}
                {cur === "metrics" && <MetricsTab svc={svc} env={env} />}
                {cur === "shell" && <ShellTab key={`${svc.id}:${env.id}`} svc={svc} env={env} />}
                {cur === "settings" && <SettingsTab key={svc.id} svc={svc} env={env} actions={actions} />}
                {cur === "admin" && <AdminTab key={`${svc.id}:${env.id}`} svc={svc} env={env} />}
              </div>
            </div>
          </div>
        )}
      </aside>
      {svc && dep && (
        <DeploymentPanel key={dep} id={dep} svc={svc} env={env} actions={actions} tab={dtab} onTab={onDtab} onClose={() => onDep(null)} />
      )}
    </PanelPortal>
  );
}

/** 서버 관리자가 이 서비스를 정지했거나 배포를 막았다(멤버에게도 보인다). */
function AdminHoldNotice({ hold }: { hold: OwnerHold }) {
  const reason = hold.reason ? ` (${hold.reason})` : "";
  return (
    <div role="alert" className="mb-6 flex items-start gap-2 rounded-md border border-gray-200 bg-danger-surface px-3 py-2 text-sm text-danger">
      <Ban className="mt-0.5 size-4 shrink-0" />
      <span>
        {hold.scope === "deploy"
          ? t("서버 관리자가 이 서비스의 배포를 막았습니다{reason}. 실행 중인 컨테이너는 그대로지만 차단이 풀릴 때까지 새로 배포할 수 없습니다.", { reason })
          : t("서버 관리자가 이 서비스를 정지했습니다{reason}. 재개될 때까지 배포와 재시작을 할 수 없습니다.", { reason })}
      </span>
    </div>
  );
}

function PanelSkeleton() {
  return (
    <div role="status" aria-label={t("서비스 불러오는 중")} className="flex h-full flex-col bg-background dark:bg-secondaryBg">
      <div className="border-b border-gray-100 px-5 pt-8 md:border-gray-200 md:px-12 md:pt-12">
        <div className="mb-6 flex items-center gap-4">
          <div className="size-8 animate-pulse rounded-full bg-gray-100" />
          <div className="h-[26px] w-40 animate-pulse rounded bg-gray-100" />
        </div>
        <div className="flex gap-8 pb-3">
          {[40, 40, 40, 40].map((w, i) => (
            <div key={i} className="h-5 animate-pulse rounded bg-gray-100" style={{ width: w }} />
          ))}
        </div>
      </div>
      <div className="flex flex-col gap-3 px-5 pt-8 md:px-12">
        <div className="h-[86px] animate-pulse rounded-lg bg-gray-100" />
        <div className="h-[68px] animate-pulse rounded-md bg-gray-100 opacity-70" />
        <div className="h-[68px] animate-pulse rounded-md bg-gray-100 opacity-50" />
      </div>
    </div>
  );
}
