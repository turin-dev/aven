"use client";

import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { Suspense, createContext, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Activity,
  Bell,
  Box,
  ChartNoAxesColumn,
  Check,
  ChevronDown,
  Copy,
  FileText,
  Gauge,
  GitPullRequest,
  LayoutTemplate,
  LogOut,
  Network,
  Pencil,
  Plus,
  RefreshCcw,
  RotateCw,
  Server,
  Settings,
  ShieldCheck,
  Waypoints,
  Share2,
  Trash2,
  UserRound,
  SlidersHorizontal,
  SquareTerminal,
} from "lucide-react";
import { api, type Environment } from "@/lib/api";
import { qk, useCanvas, useCaps, useMe, useNotifications, useProjects, useSystem } from "@/lib/queries";
import { ActivityPanel } from "./canvas/ActivityPanel";
import { canvasHref, useCtxHref } from "@/lib/nav";
import { Toaster } from "./Toaster";
import { MfaSetupGate } from "./MfaSetupGate";
import { InstanceNotice } from "./InstanceNotice";
import { FirstRunOnboarding } from "./SetupChecklist";
import { SyncEnvironmentDialog } from "./SyncEnvironmentDialog";
import { PublishTemplateDialog } from "./TemplateParts";
import { setActivityOpen, useActivityOpen } from "@/lib/uiState";
import { envColor, useWorkspace, type Workspace } from "@/lib/workspace";
import { useProjectEvents } from "@/lib/useProjectEvents";
import { useInboxStream } from "@/lib/useInboxStream";
import { lock, NO_PERM } from "./Perm";
import { AgentButton } from "./AgentPanel";
import { Button, Dialog, ErrorBox, Field, IconButton, Input, AvenMark, Menu, MenuItem, Select, Spinner, Textarea, cx, errorMessage } from "./ui";
import { useKeyboardShortcuts } from "./KeyboardShortcuts";
import { t, tr } from "@/lib/i18n";

function useHealth() {
  const { data, error, isPending } = useSystem();
  if (isPending) return { ok: true, pending: true, label: t("시스템 상태 확인 중") };
  if (error) return { ok: false, pending: false, label: t("시스템 상태를 불러오지 못했습니다: {error}", { error: errorMessage(error) }) };
  const bad: string[] = [];
  if (data?.docker !== "ok") bad.push("Docker");
  if (data?.proxy !== "ok") bad.push(t("프록시"));
  return bad.length
    ? { ok: false, pending: false, label: t("{list} 응답 없음. 서버에서 해당 서비스 상태를 확인하세요.", { list: bad.join(", ") }) }
    : { ok: true, pending: false, label: t("Docker와 프록시가 정상입니다") };
}

const crumbBtn =
  "flex items-center border border-gray-200 h-md py-2 px-3 space-x-2 hover:border-gray-400 focus:outline-none focus-visible:border-pink-400 group min-w-0 max-w-full border-none text-center !px-2 !py-0 !h-[30px] rounded-md hover:bg-gray-100 data-[state=open]:bg-gray-100 !space-x-1.5 cursor-pointer font-medium text-sm";
const crumbChevron = <span className="icon-container icon-sm text-lg !ml-0 text-gray-300"><ChevronDown /></span>;

const NAME_RE = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/;


/* ── dialogs ─────────────────────────────────────────────────────────── */
export function NewProjectDialog({ open, onClose, onCreated, workspaceId, workspaceName }: { open: boolean; onClose: () => void; onCreated: (id: string) => void; workspaceId?: string | null; workspaceName?: string }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [desc, setDesc] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const submit = async () => {
    if (!name.trim()) return setError(t("프로젝트 이름을 입력하세요."));
    setBusy(true);
    setError(null);
    try {
      const p = await api.createProject({ name: name.trim(), description: desc.trim() || undefined, workspace_id: workspaceId ?? undefined });
      await qc.invalidateQueries({ queryKey: qk.projects });
      setName("");
      setDesc("");
      onCreated(p.id);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onClose={() => !busy && onClose()}
      title={t("새 프로젝트")}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            {t("취소")}
          </Button>
          <Button variant="primary" type="submit" form="new-project" loading={busy}>
            {t("프로젝트 만들기")}
          </Button>
        </>
      }
    >
      <form
        id="new-project"
        className="space-y-4 pt-1"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <p>
          {t("프로젝트는 서비스와 환경을 묶는 단위입니다. production 환경이 함께 만들어집니다.")}
          {workspaceName && <> {tr("{workspace} 작업 공간에 만듭니다.", { workspace: <b className="text-foreground">{workspaceName}</b> })}</>}
        </p>
        <Field label={t("이름")}>
          <Input autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="my-project" />
        </Field>
        <Field label={t("설명")}>
          <Textarea rows={2} value={desc} onChange={(e) => setDesc(e.target.value)} placeholder={t("선택 사항")} />
        </Field>
        {error ? <ErrorBox error={error} /> : null}
      </form>
    </Dialog>
  );
}
/** v1.6: create a new (team) workspace. Any user can create one and becomes its owner. */
export function NewWorkspaceDialog({ open, onClose, onCreated }: { open: boolean; onClose: () => void; onCreated?: (id: string) => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const submit = async () => {
    if (!name.trim()) return setError(t("작업 공간 이름을 입력하세요."));
    setBusy(true);
    setError(null);
    try {
      const w = await api.createWorkspace(name.trim());
      await qc.invalidateQueries({ queryKey: qk.workspaces });
      await qc.invalidateQueries({ queryKey: qk.me });
      setName("");
      onClose();
      onCreated?.(w.id);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onClose={() => !busy && onClose()}
      title={t("새 작업 공간")}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            {t("취소")}
          </Button>
          <Button variant="primary" type="submit" form="new-workspace" loading={busy}>
            {t("만들기")}
          </Button>
        </>
      }
    >
      <form
        id="new-workspace"
        className="space-y-4 pt-1"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <p>{t("작업 공간은 프로젝트와 멤버를 묶는 단위입니다. 당신이 Owner가 되고, 멤버를 초대할 수 있습니다.")}</p>
        <Field label={t("이름")}>
          <Input autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder={t("우리 팀")} />
        </Field>
        {error ? <ErrorBox error={error} /> : null}
      </form>
    </Dialog>
  );
}
export function NewEnvironmentDialog({
  ws,
  open,
  onClose,
  onCreated,
}: {
  ws: Workspace;
  open: boolean;
  onClose: () => void;
  onCreated?: (env: Environment) => void;
}) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [copyFrom, setCopyFrom] = useState<string>("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const source = copyFrom || ws.env?.id || "";
  const [mode, setMode] = useState<"copy" | "empty">("copy");
  const submit = async () => {
    const n = name.trim();
    if (!NAME_RE.test(n)) return setError(t("이름에는 영문 소문자, 숫자, 하이픈(-)만 쓸 수 있습니다."));
    if (!ws.pid) return;
    setBusy(true);
    setError(null);
    try {
      const env = await api.createEnvironment(ws.pid, { name: n, copy_from: mode === "copy" ? source : undefined });
      await qc.invalidateQueries({ queryKey: qk.project(ws.pid) });
      setName("");
      onClose();
      onCreated?.(env);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onClose={() => !busy && onClose()}
      title={t("새 환경")}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            {t("취소")}
          </Button>
          <Button variant="primary" type="submit" form="new-env" loading={busy}>
            {t("환경 만들기")}
          </Button>
        </>
      }
    >
      <form
        id="new-env"
        className="space-y-4 pt-1"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <p>{t("환경마다 설정, 변수, 도메인, 볼륨이 따로 있습니다. 기존 환경을 복제하면 설정과 변수를 가져옵니다.")}</p>
        <Field label={t("이름")}>
          <Input mono autoFocus value={name} onChange={(e) => setName(e.target.value.toLowerCase())} placeholder="staging" />
        </Field>
        <div className="grid grid-cols-2 gap-2">
          {(["copy", "empty"] as const).map((m) => (
            <button
              key={m}
              type="button"
              onClick={() => setMode(m)}
              aria-pressed={mode === m}
              className={cx(
                "cursor-pointer rounded-md border px-3 py-2.5 text-left transition-colors",
                mode === m ? "border-pink-500 bg-pink-100/40" : "border-gray-200 hover:border-gray-300",
              )}
            >
              <div className="text-sm font-medium text-foreground">{m === "copy" ? t("환경 복제") : t("빈 환경")}</div>
              <div className="mt-0.5 text-xs text-gray-500">
                {m === "copy" ? t("설정과 변수를 복사합니다") : t("서비스만 있고 설정은 기본값")}
              </div>
            </button>
          ))}
        </div>
        {mode === "copy" && (
          <Field label={t("복제할 환경")} hint={t("볼륨 데이터는 복사하지 않습니다.")}>
            <Select value={source} onChange={(e) => setCopyFrom(e.target.value)}>
              {ws.envs.map((e) => (
                <option key={e.id} value={e.id}>
                  {e.name}
                </option>
              ))}
            </Select>
          </Field>
        )}
        {error ? <ErrorBox error={error} /> : null}
      </form>
    </Dialog>
  );
}
export function RenameEnvironmentDialog({
  env,
  onClose,
  onRenamed,
}: {
  env: Environment | null;
  onClose: () => void;
  onRenamed?: (env: Environment) => void;
}) {
  const qc = useQueryClient();
  const [name, setName] = useState(env?.name ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [prev, setPrev] = useState(env);
  if (env !== prev) {
    setPrev(env);
    setName(env?.name ?? "");
    setError(null);
  }
  const submit = async () => {
    if (!env) return;
    const n = name.trim();
    if (!NAME_RE.test(n)) return setError(t("이름에는 영문 소문자, 숫자, 하이픈(-)만 쓸 수 있습니다."));
    setBusy(true);
    try {
      const next = await api.renameEnvironment(env.id, n);
      await qc.invalidateQueries({ queryKey: qk.project(env.project_id) });
      onClose();
      onRenamed?.(next);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={!!env}
      onClose={() => !busy && onClose()}
      title={t("환경 이름 바꾸기")}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            {t("취소")}
          </Button>
          <Button variant="primary" type="submit" form="rename-env" loading={busy}>
            {t("이름 바꾸기")}
          </Button>
        </>
      }
    >
      <form
        id="rename-env"
        className="space-y-4 pt-1"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <p>{tr("비공개 도메인({domain})도 함께 바뀝니다.", { domain: <span className="font-mono">{t("서비스")}.{name || t("환경")}.keel.internal</span> })}</p>
        <Input mono autoFocus value={name} onChange={(e) => setName(e.target.value.toLowerCase())} aria-label={t("새 환경 이름")} />
        {error ? <ErrorBox error={error} /> : null}
      </form>
    </Dialog>
  );
}
export function DeleteEnvironmentDialog({
  env,
  onClose,
  onDeleted,
}: {
  env: Environment | null;
  onClose: () => void;
  onDeleted?: () => void;
}) {
  const qc = useQueryClient();
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const run = async () => {
    if (!env) return;
    setBusy(true);
    try {
      await api.deleteEnvironment(env.id);
      await qc.invalidateQueries({ queryKey: qk.project(env.project_id) });
      setTyped("");
      onClose();
      onDeleted?.();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={!!env}
      onClose={() => !busy && (setTyped(""), setError(null), onClose())}
      title={t("{name} 환경 삭제", { name: env?.name ?? "" })}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            {t("취소")}
          </Button>
          <Button variant="danger" onClick={run} loading={busy} disabled={typed !== env?.name}>
            {t("환경 삭제")}
          </Button>
        </>
      }
    >
      <div className="space-y-3 pt-1">
        <p>{t("이 환경의 컨테이너, 도메인, 변수, 볼륨이 모두 삭제됩니다. 되돌릴 수 없습니다.")}</p>
        <Field label={t("확인을 위해 {name}을(를) 입력하세요", { name: env?.name ?? "" })}>
          <Input mono value={typed} onChange={(e) => setTyped(e.target.value)} autoFocus />
        </Field>
        {error ? <ErrorBox error={error} /> : null}
      </div>
    </Dialog>
  );
}
/* ── menus ───────────────────────────────────────────────────────────── */
function WorkspaceBadge({ ws }: { ws: { default: boolean; personal: boolean } }) {
  const [label, cls] = ws.default
    ? [t("기본"), "bg-blue-100 text-blue-700"]
    : ws.personal
      ? [t("개인"), "bg-gray-100 text-gray-600"]
      : [t("팀"), "bg-green-100 text-success"];
  return <span className={cx("shrink-0 rounded px-1.5 py-0.5 text-xs font-medium", cls)}>{label}</span>;
}

/**
 * v1.6 작업 공간 전환: Railway의 프로젝트 캔버스 상단 바에는 작업 공간 경로가 없으므로, 프로젝트 드롭다운 아래쪽 섹션으로 둔다.
 * 각 항목에 기본/개인/팀 배지를 함께 보여 준다.
 */
function WorkspaceSection({ ws, close, onCreate }: { ws: Workspace; close: () => void; onCreate: () => void }) {
  const list = ws.wsList;
  if (!list.length) return null;
  return (
    <>
      <div className="my-2 h-px w-full bg-gray-100" />
      <div className="px-3 py-1 text-xs font-medium text-gray-600">{t("작업 공간")}</div>
      <div className="max-h-[200px] overflow-y-auto">
        {list.map((w) => (
          <MenuItem
            key={w.id}
            icon={w.id === ws.wsid ? <Check className="size-3.5 text-pink-600" /> : <span className="inline-block size-3.5" />}
            onSelect={() => {
              close();
              if (w.id !== ws.wsid) ws.switchWorkspace(w.id);
            }}
          >
            <span className="min-w-0 flex-1 truncate">{w.name}</span>
            <WorkspaceBadge ws={w} />
          </MenuItem>
        ))}
      </div>
      <MenuItem
        icon={<Plus className="size-3.5" />}
        onSelect={() => {
          close();
          onCreate();
        }}
      >
        {t("새 작업 공간")}
      </MenuItem>
    </>
  );
}

function ProjectMenu({ ws }: { ws: Workspace }) {
  const router = useRouter();
  const health = useHealth();
  const href = useCtxHref();
  const [creating, setCreating] = useState(false);
  const [creatingWs, setCreatingWs] = useState(false);
  const caps = useCaps(ws.pid);
  const user = useMe().data?.user;
  useKeyboardShortcuts([
    {
      keys: "mod+/",
      label: t("새 프로젝트 만들기"),
      group: t("대시보드"),
      enabled: caps.canCreateProjects,
      onTrigger: () => setCreating(true),
    },
  ]);
  const name = ws.detail?.name ?? (ws.isPending ? "…" : t("프로젝트 없음"));
  return (
    <>
      <Menu
        align="start"
        label={t("프로젝트")}
        trigger={(p) => (
          <button type="button" {...p} className={cx(crumbBtn, "select-project")} aria-label={t("프로젝트: {name}. 프로젝트 바꾸기", { name })}>
            <span className="inline-block size-4 shrink-0 overflow-hidden rounded-full" aria-hidden>
              <span className="flex h-full w-full items-center justify-center rounded-full bg-gradient-to-br from-[#6f31aa] to-[#306ee8] text-[9px] font-semibold leading-none text-white">
                {(user ?? "?").slice(0, 1).toUpperCase()}
              </span>
            </span>
            <span className="project-name max-w-[110px] truncate whitespace-nowrap text-left font-medium text-sm sm:max-w-[240px]">{name}</span>
            {!health.ok && <span className="size-2 shrink-0 rounded-full bg-red-600" title={health.label} />}
            {crumbChevron}
          </button>
        )}
      >
        {(close) => (
          <div className="w-[279px]">
            <div className="px-3 py-1 text-xs font-medium text-gray-600">{t("프로젝트|plural")}</div>
            <div className="max-h-[300px] overflow-y-auto">
              {(ws.projects.data ?? []).map((p) => (
                <MenuItem
                  key={p.id}
                  icon={p.id === ws.pid ? <Check className="size-3.5 text-pink-600" /> : <span className="inline-block size-3.5" />}
                  onSelect={() => {
                    close();
                    if (p.id !== ws.pid) ws.switchProject(p.id);
                  }}
                >
                  <span className="min-w-0 flex-1 truncate">{p.name}</span>
                </MenuItem>
              ))}
            </div>
            <div className="my-2 h-px w-full bg-gray-100" />
            <MenuItem
              icon={<LayoutTemplate />}
              onSelect={() => {
                close();
                router.push("/projects/");
              }}
            >
              {t("모든 프로젝트")}
            </MenuItem>
            {caps.canCreateProjects && (
              <MenuItem
                icon={<Plus className="size-3.5" />}
                onSelect={() => {
                  close();
                  setCreating(true);
                }}
              >
                {t("새 프로젝트")}
              </MenuItem>
            )}
            <MenuItem
              icon={<Settings className="size-3.5" />}
              onSelect={() => {
                close();
                router.push(href("/settings/"));
              }}
            >
              {t("프로젝트 설정")}
            </MenuItem>
            <WorkspaceSection ws={ws} close={close} onCreate={() => setCreatingWs(true)} />
            <div className="my-2 h-px w-full bg-gray-100" />
            <div className="flex items-center gap-1.5 px-2.5 py-1.5 text-xs text-gray-600">
              <span className={cx("size-2 shrink-0 rounded-full", health.pending ? "bg-gray-400" : health.ok ? "bg-green-600" : "bg-red-600")} />
              <span className="min-w-0">{health.label}</span>
            </div>
          </div>
        )}
      </Menu>
      <NewProjectDialog
        open={creating}
        onClose={() => setCreating(false)}
        workspaceId={ws.wsid}
        workspaceName={ws.curWorkspace?.name}
        onCreated={(id) => {
          setCreating(false);
          ws.switchProject(id);
        }}
      />
      <NewWorkspaceDialog open={creatingWs} onClose={() => setCreatingWs(false)} onCreated={(id) => ws.switchWorkspace(id)} />
    </>
  );
}

export function isPreview(e: Environment): boolean {
  return e.kind === "preview";
}

/** Standard environments first (creation order), then PR previews by PR number. */
export function sortEnvs(envs: Environment[]): Environment[] {
  const std = envs.filter((e) => !isPreview(e));
  const prs = envs.filter(isPreview).sort((a, b) => (a.pr_number ?? 0) - (b.pr_number ?? 0));
  return [...std, ...prs];
}

/** "PR #12" badge for preview environments; links to the pull request when the URL is known. */
export function PRBadge({ env, link = false }: { env: Environment; link?: boolean }) {
  if (!isPreview(env)) return null;
  const label = env.pr_number != null ? `PR #${env.pr_number}` : "PR";
  const cls =
    "inline-flex shrink-0 items-center gap-1 rounded bg-pink-500/15 px-1.5 py-0.5 text-xs font-medium text-pink-600";
  const title = env.pr_branch ? `${label} · ${env.pr_branch}` : label;
  // link는 버튼 안이 아닌 곳에서만 켠다(대화형 요소 중첩 방지).
  if (link && env.pr_url) {
    return (
      <a
        href={env.pr_url}
        target="_blank"
        rel="noreferrer noopener"
        className={cx(cls, "hover:underline")}
        title={title}
        aria-label={t("{label} GitHub에서 열기", { label })}
      >
        <GitPullRequest className="size-3" aria-hidden />
        {label}
      </a>
    );
  }
  return (
    <span className={cls} title={title}>
      <GitPullRequest className="size-3" aria-hidden />
      {label}
    </span>
  );
}
function EnvMenu({ ws }: { ws: Workspace }) {
  const [creating, setCreating] = useState(false);
  const [syncing, setSyncing] = useState(false);
  const [publishing, setPublishing] = useState(false);
  const caps = useCaps(ws.pid);
  const [renaming, setRenaming] = useState<Environment | null>(null);
  const [deleting, setDeleting] = useState<Environment | null>(null);
  const env = ws.env;
  const color = env && !ws.isDefaultEnv ? envColor(env.name) : null;
  return (
    <>
      <Menu
        align="start"
        label={t("환경")}
        trigger={(p) => (
          <button
            type="button"
            {...p}
            aria-label={t("환경: {name}. 환경 바꾸기", { name: env?.name ?? "" })}
            className={cx(crumbBtn, color && "!border")}
            style={color ? { borderColor: `${color}66`, background: `${color}1f`, color } : undefined}
          >
            {color && <span className="size-2 shrink-0 rounded-full" style={{ background: color }} />}
            <span className="environment-name min-w-0 max-w-[96px] truncate text-left font-medium text-sm sm:max-w-[160px]">{env?.name ?? "…"}</span>
            {crumbChevron}
          </button>
        )}
      >
        {(close) => (
          <div className="w-[259px]">
            <div className="px-3 py-1 text-xs font-medium text-gray-600">{t("환경")}</div>
            {sortEnvs(ws.envs).flatMap((e, i, all) => {
              const def = e.id === ws.detail?.default_environment_id;
              const firstPreview = isPreview(e) && (i === 0 || !isPreview(all[i - 1]));
              const heading = firstPreview ? (
                <div key="pr-heading" className="px-3 py-1 text-xs font-medium text-gray-600">
                  {t("PR 미리보기")}
                </div>
              ) : null;
              return [
                heading,
                <MenuItem
                  key={e.id}
                  className={e.id === env?.id ? "!text-neutral" : undefined}
                  icon={e.id === env?.id ? <Check className="size-4 text-pink-600" /> : <span className="inline-block size-4" />}
                  onSelect={() => {
                    close();
                    if (e.id !== env?.id) ws.switchEnv(e.name);
                  }}
                >
                  <span className="min-w-0 flex-1 truncate">{e.name}</span>
                  {isPreview(e) && <PRBadge env={e} />}
                  {def && <span className="ml-2 shrink-0 text-xs text-gray-400">{t("기본")}</span>}
                </MenuItem>,
              ];
            })}
            <div className="my-2 h-px w-full bg-gray-100" />
            <MenuItem
              icon={<Plus className="size-3.5" />}
              disabled={!caps.canEdit}
              reason={NO_PERM}
              onSelect={() => {
                close();
                setCreating(true);
              }}
            >
              {t("새 환경")}
            </MenuItem>
            {env && (
              <MenuItem
                icon={<Pencil className="size-3.5" />}
                disabled={!caps.canEdit}
                reason={NO_PERM}
                onSelect={() => {
                  close();
                  setRenaming(env);
                }}
              >
                {t("{name} 이름 바꾸기", { name: env.name })}
              </MenuItem>
            )}
            {env && (
              <MenuItem
                danger
                icon={<Trash2 className="size-3.5" />}
                disabled={!caps.canAdmin || ws.envs.length <= 1}
                reason={NO_PERM}
                onSelect={() => {
                  close();
                  setDeleting(env);
                }}
              >
                {t("{name} 삭제", { name: env.name })}
              </MenuItem>
            )}
            {env && (
              <MenuItem
                icon={<Copy className="size-3.5" />}
                disabled={!caps.canEdit}
                reason={NO_PERM}
                onSelect={() => {
                  close();
                  setCreating(true);
                }}
              >
                {t("{name} 복제", { name: env.name })}
              </MenuItem>
            )}
            {env && ws.envs.length > 1 && (
              <MenuItem
                icon={<RefreshCcw className="size-3.5" />}
                disabled={!caps.canEdit}
                reason={NO_PERM}
                onSelect={() => {
                  close();
                  setSyncing(true);
                }}
              >
                {t("다른 환경에서 동기화…")}
              </MenuItem>
            )}
            {env && caps.canEdit && (
              <MenuItem
                icon={<Share2 className="size-3.5" />}
                onSelect={() => {
                  close();
                  setPublishing(true);
                }}
              >
                {t("템플릿으로 게시…")}
              </MenuItem>
            )}
          </div>
        )}
      </Menu>
      <NewEnvironmentDialog ws={ws} open={creating} onClose={() => setCreating(false)} onCreated={(e) => ws.switchEnv(e.name)} />
      <SyncEnvironmentDialog target={env} envs={ws.envs} open={syncing} onClose={() => setSyncing(false)} />
      <PublishTemplateDialog pid={ws.pid} env={env} open={publishing} onClose={() => setPublishing(false)} />
      <RenameEnvironmentDialog
        env={renaming}
        onClose={() => setRenaming(null)}
        onRenamed={(e) => {
          if (e.id === env?.id) ws.switchEnv(e.name);
        }}
      />
      <DeleteEnvironmentDialog
        env={deleting}
        onClose={() => setDeleting(null)}
        onDeleted={() => {
          if (deleting?.id === env?.id) {
            const fallback = ws.envs.find((e) => e.id !== deleting?.id);
            if (fallback) ws.switchEnv(fallback.name);
          }
        }}
      />
    </>
  );
}
const ROLE_LABEL: Record<string, string> = { owner: "Owner", admin: "Admin", member: "Member", viewer: "Viewer" };
function AccountMenu({ user, side = "bottom" }: { user?: string; side?: "bottom" | "right" }) {
  const qc = useQueryClient();
  const router = useRouter();
  const href = useCtxHref();
  const me = useMe().data;
  const logout = async () => {
    try {
      await api.logout();
    } finally {
      qc.clear();
      window.location.href = "/login/";
    }
  };
  const initial = (user ?? "?").slice(0, 1).toUpperCase();
  return (
    <Menu
      label={t("계정")}
      side={side}
      trigger={(p) => (
        <button
          type="button"
          {...p}
          aria-label={t("계정 메뉴")}
          className="account-dropdown !flex !h-10 !w-10 cursor-pointer items-center justify-center rounded-lg border border-gray-200 border-none p-0 hover:bg-transparent focus:outline-none data-[state=open]:bg-gray-100 dark:data-[state=open]:bg-white/10"
        >
          <span className="relative flex items-center">
            <span
              title={user}
              className="inline-flex size-6 items-center justify-center overflow-hidden rounded-full bg-gradient-to-br from-[#6f31aa] to-[#306ee8] text-xs font-semibold text-white"
            >
              {initial}
            </span>
          </span>
        </button>
      )}
    >
      {(close) => (
        <>
          <div className="px-2.5 pb-1.5 pt-1 text-xs text-gray-500">
            {tr("{user} 계정으로 로그인됨", { user: <span className="font-medium text-foreground">{user}</span> })}
            {me && (
              <div className="mt-0.5 truncate">
                {me.email} · {ROLE_LABEL[me.role] ?? me.role}
              </div>
            )}
          </div>
          <div className="my-2 h-px w-full bg-gray-100" />
          <MenuItem
            icon={<UserRound className="size-3.5" />}
            onSelect={() => {
              close();
              router.push(href("/account/"));
            }}
          >
            {t("계정 설정")}
          </MenuItem>
          {me?.instance_admin && (
            <MenuItem icon={<ShieldCheck className="size-3.5" />} onSelect={() => { close(); router.push("/admin/"); }}>
              {t("어드민")}
            </MenuItem>
          )}
          <MenuItem icon={<LogOut className="size-3.5" />} onSelect={logout}>
            {t("로그아웃")}
          </MenuItem>
        </>
      )}
    </Menu>
  );
}
const NAV = [
  { href: "/", label: t("아키텍처"), icon: Network, match: (p: string) => p === "/" },
  { href: "/observability/", label: t("관측"), icon: ChartNoAxesColumn, match: (p: string) => p.startsWith("/observability") },
  { href: "/logs/", label: t("로그"), icon: FileText, match: (p: string) => p.startsWith("/logs") },
  { href: "/traces/", label: t("트레이스"), icon: Waypoints, match: (p: string) => p.startsWith("/traces") },
  { href: "/nodes/", label: t("노드"), icon: Server, match: (p: string) => p.startsWith("/nodes") },
  { href: "/sandboxes/", label: t("샌드박스"), icon: Box, match: (p: string) => p.startsWith("/sandboxes") },
  { href: "/usage/", label: t("사용량"), icon: Gauge, match: (p: string) => p.startsWith("/usage") },
  { href: "/templates/", label: t("템플릿"), icon: LayoutTemplate, match: (p: string) => p.startsWith("/templates") },
  { href: "/cli/", label: "CLI", icon: SquareTerminal, match: (p: string) => p.startsWith("/cli") },
  { href: "/settings/", label: t("설정"), icon: Settings, match: (p: string) => p.startsWith("/settings"), bottom: true },
  { href: "/admin/", label: t("어드민"), icon: ShieldCheck, match: (p: string) => p.startsWith("/admin"), bottom: true },
];
const barBtn =
  "relative flex min-h-[28px] cursor-pointer items-center gap-1 rounded-md border border-transparent p-1.5 text-neutral-secondary focus-visible:ring-2 focus-visible:ring-pink-500 hover:bg-gray-100 hover:text-gray-900";
function ActivityButton() {
  const pathname = usePathname() || "/";
  const router = useRouter();
  const href = useCtxHref();
  const open = useActivityOpen();
  return (
    <button
      type="button"
      aria-label={t("활동")}
      aria-pressed={open}
      title={t("활동")}
      onClick={() => {
        if (pathname !== "/") {
          setActivityOpen(true);
          router.push(href("/"));
        } else setActivityOpen((o) => !o);
      }}
      className={cx(barBtn, open && "bg-gray-100 text-gray-800")}
    >
      <span className="icon-container icon-sm text-lg text-current"><Activity /></span>
    </button>
  );
}
const ATTENTION = /(\.failed|_failed|\.crashed|alert\.firing|limit_exceeded|threshold_reached)$/;
/**
 * v1.5: per-user inbox (GET /notifications). Live via the inbox SSE stream; polled every 30 s while the stream is down.
 * Kinds follow the account's notification preferences.
 */
function BellMenu() {
  const router = useRouter();
  const qc = useQueryClient();
  const inbox = useNotifications(useInboxStream());
  const projects = useProjects();
  const list = inbox.data?.notifications ?? [];
  const unread = inbox.data?.unread ?? 0;
  const projectName = (pid: string) => projects.data?.find((p) => p.id === pid)?.name ?? t("프로젝트");
  const markRead = async (input: { ids: string[] } | { all: true }) => {
    try {
      await api.readNotifications(input);
    } finally {
      qc.invalidateQueries({ queryKey: qk.notifications });
    }
  };
  return (
    <Menu
      label={t("알림")}
      trigger={(p) => (
        <button
          type="button"
          {...p}
          aria-label={unread ? t("알림 (읽지 않음 {count}개)", { count: unread }) : t("알림")}
          title={t("알림")}
          className={cx(barBtn, p["aria-expanded"] && "bg-gray-100")}
        >
          <span className="relative">
            <span className="icon-container icon-sm text-lg text-current"><Bell /></span>
            {unread > 0 && (
              <span
                aria-hidden="true"
                className="absolute -right-[7px] -top-[6px] flex h-[14px] min-w-[14px] items-center justify-center rounded-full bg-red-600 px-[3px] text-xs font-semibold leading-none text-white"
              >
                {unread > 99 ? "99+" : unread}
              </span>
            )}
          </span>
        </button>
      )}
    >
      {(close) => (
        <div className="w-[379px]">
          <div className="flex items-center justify-between px-3 pb-2 pt-3">
            <span className="text-sm font-medium text-foreground">{t("알림")}</span>
            <span className="flex items-center gap-[10px]">
              {unread > 0 && (
                <button type="button" className="cursor-pointer text-xs text-pink-500 hover:underline" onClick={() => markRead({ all: true })}>
                  {t("모두 읽음")}
                </button>
              )}
              <Link href="/account/?section=notifications" onClick={close} className="text-xs text-gray-500 hover:text-foreground">
                {t("설정")}
              </Link>
            </span>
          </div>
          {inbox.isPending ? (
            <div className="flex justify-center py-[16px]">
              <Spinner />
            </div>
          ) : list.length === 0 ? (
            <div className="mx-3 mb-3 rounded border border-dashed border-gray-200 px-[10px] py-8 text-center text-sm text-gray-500">{t("알림이 없습니다")}</div>
          ) : (
            <div className="max-h-[420px] overflow-y-auto">
              {list.map((n) => (
                <MenuItem
                  key={n.id}
                  className="!h-auto items-start !py-2"
                  onSelect={() => {
                    close();
                    if (!n.read_at) void markRead({ ids: [n.id] });
                    router.push(n.service_id ? canvasHref({ svc: n.service_id }, { project: n.project_id }) : canvasHref({}, { project: n.project_id }));
                  }}
                >
                  <span className={cx("mt-[5px] size-[6px] shrink-0 self-start rounded-full", n.read_at ? "bg-transparent" : ATTENTION.test(n.kind) ? "bg-red-600" : "bg-pink-500")} />
                  <span className="min-w-0 flex-1">
                    <span className="flex items-center justify-between gap-[8px]">
                      <span className={cx("truncate text-xs", n.read_at ? "text-gray-500" : "font-medium text-foreground")}>
                        {projectName(n.project_id)} · {n.kind}
                      </span>
                      <span className="shrink-0 text-xs text-gray-400">{relTime(n.created_at)}</span>
                    </span>
                    <span className={cx("line-clamp-2 text-xs", n.read_at ? "text-gray-400" : "text-gray-500")}>{n.message}</span>
                  </span>
                </MenuItem>
              ))}
            </div>
          )}
          <Link href="/account/?section=notifications" onClick={close} className="flex items-center gap-2 border-t border-gray-200 px-3 py-3 text-xs font-medium text-foreground hover:text-pink-600">
            <SlidersHorizontal className="size-3.5" /> {t("환경설정 편집")}
          </Link>
        </div>
      )}
    </Menu>
  );
}
function relTime(iso: string): string {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return t("방금");
  if (s < 3600) return t("{n}분 전", { n: Math.floor(s / 60) });
  if (s < 86400) return t("{n}시간 전", { n: Math.floor(s / 3600) });
  return t("{n}일 전", { n: Math.floor(s / 86400) });
}
function TopBar({ user, ws, admin }: { user?: string; ws: Workspace; admin: boolean }) {
  return (
    <div className="relative z-10 bg-transparent px-4">
      <header className="flex h-[56px] items-center justify-between py-3">
        <div className="flex min-w-0 flex-1 items-center">
          <Link href="/projects/" aria-label={t("모든 프로젝트")} title={t("모든 프로젝트")} className="mr-2 flex shrink-0 items-center md:mr-3">
            <div className="relative">
              <AvenMark className="logo !size-6" />
            </div>
          </Link>
          {admin ? (
            <div className="flex items-center gap-2 text-sm font-medium"><ShieldCheck className="size-4" />{t("서버 관리")}</div>
          ) : <nav aria-label={t("경로")} className="flex min-w-0 flex-nowrap items-center gap-1">
            <div className="ml-1 mr-1 h-5 w-px shrink-0 border-l border-neutral-secondary md:ml-2 md:mr-2" />
            <div className="min-w-0 hidden sm:block">
              <ProjectMenu ws={ws} />
            </div>
            <div className="flex min-w-0 items-center rounded-none bg-transparent">
              <div className="mr-1 hidden h-5 w-px shrink-0 rotate-[15deg] border-l border-neutral-secondary sm:block" />
              <EnvMenu ws={ws} />
            </div>
          </nav>}
        </div>
        <nav className="ml-auto flex shrink-0 items-center gap-x-3">
          <div className="flex items-center">
            <div className="flex items-center gap-2">
              {!admin && <div className="hidden md:block">
                <ActivityButton />
              </div>}
              <BellMenu />
              <div className="mx-1 hidden h-5 w-0.5 rounded-full bg-black/[0.05] lg:block dark:bg-white/10" />
              {!admin && <AgentButton ws={ws} />}
              <div className="flex items-center md:hidden">
                <div className="mx-1 h-5 w-0.5 rounded-full bg-black/[0.05] dark:bg-white/10" />
                <AccountMenu user={user} />
              </div>
            </div>
          </div>
        </nav>
      </header>
    </div>
  );
}
function RailLink({ n, active }: { n: (typeof NAV)[number]; active: boolean }) {
  const href = useCtxHref();
  const Icon = n.icon;
  return (
    <div className="group/rail relative cursor-default">
      <Link
        href={n.href === "/admin/" ? n.href : href(n.href)}
        aria-label={n.label}
        title={n.label}
        aria-current={active ? "page" : undefined}
        className={cx(
          "relative flex h-10 w-10 cursor-pointer items-center justify-center rounded-lg transition-colors",
          active ? "text-foreground" : "text-gray-500 hover:text-foreground",
        )}
      >
        <span className="icon-container icon-sm text-lg text-current">
          <Icon />
        </span>
      </Link>
    </div>
  );
}
function Rail({ user }: { user?: string }) {
  const pathname = usePathname() || "/";
  const me = useMe().data;
  const nav = NAV.filter((n) => n.href !== "/admin/" || me?.instance_admin);
  const idx = nav.findIndex((n) => n.match(pathname));
  // Rows are h-10 (2.5rem) + gap-1 (0.25rem).
  const step = idx < 0 ? 0 : idx * 2.75;
  return (
    <nav
      aria-label={t("주 메뉴")}
      className="relative flex flex-shrink-0 flex-row items-center justify-start gap-1 overflow-x-auto px-4 py-2 md:flex-col md:overflow-visible md:px-2 md:pb-3 md:pt-4"
    >
      <div className="relative mx-auto flex shrink-0 flex-row gap-1 md:mx-0 md:flex-col">
        <div
          className={cx(
            "absolute h-10 w-10 translate-y-0 rounded-lg bg-gray-100 transition-transform duration-[250ms] ease-out translate-x-[var(--pill-x)] md:translate-x-0 md:translate-y-[var(--pill-y)] dark:bg-white/10",
            idx < 0 && "opacity-0",
          )}
          style={{ "--pill-x": `${step}rem`, "--pill-y": `${step}rem` } as React.CSSProperties}
        />
        {nav.map((n) => (
          <RailLink key={n.href} n={n} active={n.match(pathname)} />
        ))}
      </div>
      <div className="hidden flex-1 md:block" />
      <div className="hidden md:block">
        <AccountMenu user={user} side="right" />
      </div>
    </nav>
  );
}
/** Non-production environments get a colored frame and a tab hanging from the frame's top edge. */
function EnvTab({ ws }: { ws: Workspace }) {
  if (!ws.env || ws.isDefaultEnv) return null;
  return (
    <div
      className="pointer-events-none absolute left-1/2 top-0 z-[41] -translate-x-1/2 rounded-b-[6px] px-[10px] pb-[2px] text-xs font-semibold tracking-[0.02em]"
      style={{ background: envColor(ws.env.name), color: "#13111c" }}
    >
      {t("{name} 환경", { name: ws.env.name })}
    </div>
  );
}
/** Pages render their side panel (service panel) into the shell's nested-canvas grid through this. */
const PanelCtx = createContext<{ slot: HTMLElement | null; setOpen: (o: boolean) => void }>({ slot: null, setOpen: () => {} });
export function PanelPortal({ children }: { children: ReactNode }) {
  const { slot, setOpen } = useContext(PanelCtx);
  useEffect(() => {
    setOpen(true);
    return () => setOpen(false);
  }, [setOpen]);
  return slot ? createPortal(children, slot) : null;
}

/**
 * Floating side panel, modeled on Railway's dashboard (§4.1 B): `rounded-xl border border-neutral bg-background` beside the canvas with an
 * 8px right gutter, width 0→W + opacity over a 0.2s easeOut tween. Stays mounted through the exit transition.
 */
function FloatingSidePanel({ open, width = 320, label, children }: { open: boolean; width?: number; label: string; children: ReactNode }) {
  const [render, setRender] = useState(open);
  if (open && !render) setRender(true);
  useEffect(() => {
    if (open || !render) return;
    const timer = setTimeout(() => setRender(false), 200);
    return () => clearTimeout(timer);
  }, [open, render]);
  if (!render) return null;
  return (
    <div
      role="complementary"
      aria-label={label}
      aria-hidden={!open}
      inert={!open}
      className="hidden h-full min-w-0 shrink-0 overflow-hidden pb-2 transition-[width,opacity] duration-200 ease-out starting:!w-0 starting:opacity-0 motion-reduce:transition-none md:flex"
      style={{ width: open ? width + 8 : 0, opacity: open ? 1 : 0 }}
    >
      <div
        className="pointer-events-auto h-full min-w-0 shrink-0 overflow-hidden rounded-xl border border-neutral bg-background"
        style={{ width }}
      >
        {children}
      </div>
      <div className="w-2 shrink-0" />
    </div>
  );
}

/** Activity feed column of the project canvas, toggled by the top bar's ActivityButton. */
function ActivityColumn({ ws, open }: { ws: Workspace; open: boolean }) {
  const router = useRouter();
  const href = useCtxHref();
  // The canvas page owns the fetch; this only reads its cache for service names.
  const canvas = useCanvas(ws.env?.id, false);
  return (
    <FloatingSidePanel open={open} label={t("활동")}>
      {ws.pid && ws.env && (
        <ActivityPanel
          pid={ws.pid}
          envId={ws.env.id}
          services={canvas.data?.services ?? []}
          onClose={() => setActivityOpen(false)}
          onOpen={(ev) => ev.service_id && router.push(href("/", { svc: ev.service_id }), { scroll: false })}
        />
      )}
    </FloatingSidePanel>
  );
}

function ShellInner({ children }: { children: ReactNode }) {
  const me = useQuery({ queryKey: qk.me, queryFn: api.me, staleTime: 60_000 });
  const ws = useWorkspace();
  const pathname = usePathname() || "/";
  const admin = pathname.startsWith("/admin");
  useProjectEvents(me.data && !admin ? ws.pid : null);
  const frameColor = !admin && ws.env && !ws.isDefaultEnv ? `${envColor(ws.env.name)}80` : undefined;
  const [panelOpen, setPanelOpen] = useState(false);
  const [slot, setSlot] = useState<HTMLElement | null>(null);
  const ctxVal = useMemo(() => ({ slot, setOpen: setPanelOpen }), [slot]);
  // Activity lives on the project canvas only; an open service pane takes the right side instead.
  const activityOpen = useActivityOpen();
  const activityShown = activityOpen && !admin && pathname === "/" && !panelOpen && !!ws.pid && !!ws.env;

  return (
    <div className="relative h-dvh bg-transparent dark:bg-background">
      <div className="absolute inset-0 flex flex-col">
        <Toaster shifted={activityShown} />
        <TopBar user={me.data?.user} ws={ws} admin={admin} />
        <div className="flex min-h-0 flex-1 flex-col-reverse md:flex-row">
          <Rail user={me.data?.user} />
          <div className="flex min-h-0 min-w-0 flex-1 flex-row">
            <div className="relative min-h-0 min-w-0 flex-1">
              <div className="absolute inset-0 overflow-hidden bg-transparent">
                <div className="absolute inset-0 grid grid-cols-1 grid-rows-[minmax(0,1fr)]">
                  <div
                    role="region"
                    className="stack-container relative z-0 col-start-1 row-start-1 grid overflow-hidden rounded-xl md:pb-2"
                    style={{ transformOrigin: "center top" }}
                  >
                    <div
                      className={cx(
                        "nested-canvas scrollbar-hide relative block h-full max-w-full grid-cols-[minmax(0px,_1fr)] grid-rows-[auto_1fr] overflow-y-auto sm:grid sm:grid-rows-1 sm:overflow-x-hidden md:grid-rows-[initial] md:px-0",
                        panelOpen && "sm:grid-cols-[0px_100%] lg:grid-cols-[1fr_70%] xl:grid-cols-[1fr_minmax(auto,_919px)] 2xl:grid-cols-[1fr_minmax(auto,_1023px)] 3xl:grid-cols-[1fr_minmax(auto,_1199px)]",
                      )}
                    >
                      <div className="absolute inset-0 flex sm:pr-2">
                        <div className="w-2 flex-shrink-0 md:hidden" />
                        <main
                          className="canvas-container relative grid flex-1 grid-rows-[minmax(0,1fr)] overflow-hidden rounded-xl border border-neutral bg-secondaryBg dark:bg-background"
                          style={frameColor ? { borderColor: frameColor } : undefined}
                        >
                          {!admin && <EnvTab ws={ws} />}
                          <div id="keel-scroll" className="h-full min-h-0 overflow-y-auto">
                            {me.isPending ? (
                              <div className="dot-grid flex h-full items-center justify-center">
                                <Spinner />
                              </div>
                            ) : me.isError ? (
                              <div className="dot-grid flex h-full flex-col items-center justify-center gap-3 px-6 text-center">
                                <div className="text-base font-semibold">{t("서버에 연결할 수 없습니다")}</div>
                                <div className="max-w-sm text-sm text-gray-600">
                                  {t("{error}. Aven 서버가 실행 중인지 확인한 뒤 다시 시도하세요.", { error: errorMessage(me.error) })}
                                </div>
                                <Button size="sm" onClick={() => me.refetch()}>
                                  <RotateCw /> {t("다시 시도")}
                                </Button>
                              </div>
                            ) : (
                              <PanelCtx.Provider value={ctxVal}>
                                <InstanceNotice />
                                <MfaSetupGate me={me.data}>{children}</MfaSetupGate>
                                {!admin && <FirstRunOnboarding me={me.data} />}
                              </PanelCtx.Provider>
                            )}
                          </div>
                        </main>
                      </div>
                      {panelOpen && (
                        <div
                          ref={setSlot}
                          className="metro-pane-wrapper relative inset-0 z-[30] col-start-2 row-start-2 grid min-h-full w-auto min-w-full justify-items-end overflow-hidden px-0 sm:row-start-1 sm:h-full md:mx-5 md:min-w-[initial] lg:mx-0 lg:w-[calc(100%+8px)] lg:-translate-x-[17px]"
                        />
                      )}
                    </div>
                  </div>
                </div>
              </div>
            </div>
            {!admin && <ActivityColumn ws={ws} open={activityShown} />}
          </div>
        </div>
      </div>
    </div>
  );
}

export function Shell({ children }: { children: ReactNode }) {
  return (
    <Suspense
      fallback={
        <div className="dot-grid flex h-dvh items-center justify-center">
          <Spinner />
        </div>
      }
    >
      <ShellInner>{children}</ShellInner>
    </Suspense>
  );
}
