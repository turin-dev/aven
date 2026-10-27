"use client";

import { toast } from "@/lib/uiState";
import Link from "next/link";
import { createPortal } from "react-dom";
import { Suspense, useEffect, useMemo, useState, type ReactNode } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  Bell,
  Braces,
  ChartColumn,
  Check,
  ChevronRight,
  Code2,
  Eye,
  EyeOff,
  Flag,
  GitPullRequest,
  KeyRound,
  Layers,
  Pencil,
  Plug,
  Plus,
  Settings,
  Trash2,
  Undo2,
  Users,
  UsersRound,
  X,
} from "lucide-react";
import { api, NOTIFY_EVENTS, type Environment, type NotifyEvent, type PREnvironmentSettings } from "@/lib/api";
import { qk, useCanvas, useCaps, useGithub, useMe, useSharedVariables } from "@/lib/queries";
import { ProjectMembers, WorkspaceMembersSettings } from "@/components/MembersSettings";
import { AccessGroupsSettings, EnvRestrictionToggle, ProjectAccessGroups, RestrictedBadge } from "@/components/AccessControlsSettings";
import { lock, PermFieldset } from "@/components/Perm";
import { NotificationChannels, UsageAlertSettings } from "@/components/NotificationSettings";
import { UsageLimitSettings } from "@/components/UsageLimit";
import { GitHubConnectionCard } from "@/components/GitHubConnection";
import { IACSettings } from "@/components/IACSettings";
import { RegistriesView } from "@/components/nodes/RegistriesView";
import { MCPSettings } from "@/components/MCPSettings";
import { FeatureFlags } from "@/components/FeatureFlags";
import { fmtRelative, fmtTime, parseDotenv } from "@/lib/format";
import { useCtxHref, withCtx } from "@/lib/nav";
import { envColor, useWorkspace, type Workspace } from "@/lib/workspace";
import { BrandIcon } from "@/components/ServiceIcon";
import { DeleteEnvironmentDialog, NewEnvironmentDialog, PRBadge, RenameEnvironmentDialog, isPreview, sortEnvs } from "@/components/Shell";
import { RawEditor, toDotenv } from "@/components/panel/VariablesTab";
import {
  Button,
  Card,
  CopyField,
  Dialog,
  ErrorBox,
  Field,
  IconButton,
  Input,
  LABEL_CLS,
  PageShell,
  RowList,
  Segmented,
  Select,
  useConfirm,
  SideNavLayout,
  Spinner,
  Toggle,
  cx,
} from "@/components/ui";
import { t, tr } from "@/lib/i18n";
import { removeStorage, writeStorage } from "@/lib/legacyStorage";

type SectionId =
  | "general"
  | "iac"
  | "environments"
  | "flags"
  | "shared"
  | "members"
  | "workspace-members"
  | "access-groups"
  | "registries"
  | "integrations"
  | "mcp"
  | "notifications"
  | "usage"
  | "danger";

const NAV: { id: SectionId; label: string; icon: ReactNode }[] = [
  { id: "general", label: t("일반"), icon: <Settings className="size-4" /> },
  { id: "iac", label: t("코드형 인프라"), icon: <Code2 className="size-4" /> },
  { id: "environments", label: t("환경"), icon: <Layers className="size-4" /> },
  { id: "flags", label: t("기능 플래그"), icon: <Flag className="size-4" /> },
  { id: "shared", label: t("공유 변수"), icon: <Braces className="size-4" /> },
  { id: "members", label: t("멤버"), icon: <Users className="size-4" /> },
  { id: "workspace-members", label: t("작업 공간"), icon: <UsersRound className="size-4" /> },
  { id: "access-groups", label: t("접근 그룹"), icon: <UsersRound className="size-4" /> },
  { id: "registries", label: t("레지스트리"), icon: <KeyRound className="size-4" /> },
  { id: "integrations", label: t("통합"), icon: <Plug className="size-4" /> },
  { id: "mcp", label: t("AI 도구 · MCP"), icon: <Braces className="size-4" /> },
  { id: "notifications", label: t("알림 채널"), icon: <Bell className="size-4" /> },
  { id: "usage", label: t("사용량 알림·한도"), icon: <ChartColumn className="size-4" /> },
  { id: "danger", label: t("위험 구역"), icon: <AlertTriangle className="size-4" /> },
];

const ADMIN_SECTIONS: Record<string, string> = {
  workspace: "users",
  sso: "sso",
  guardrails: "security",
  audit: "audit",
  nodes: "nodes",
  system: "system",
  agent: "agent",
};

/** Section intro: a header-only Card, so it shares the Card title size, width and divider rhythm. */
function H2({ children, sub }: { children: ReactNode; sub?: ReactNode }) {
  return <Card title={children} description={sub} />;
}

/* ── general ─────────────────────────────────────────────────────────── */

function General({ ws }: { ws: Workspace }) {
  const qc = useQueryClient();
  const p = ws.detail!;
  const [name, setName] = useState(p.name);
  const [desc, setDesc] = useState(p.description);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [done, setDone] = useState(false);
  const dirty = name.trim() !== p.name || desc.trim() !== p.description;
  return (
    <Card title={t("프로젝트 정보")}>
      <form
        className="space-y-6"
        onSubmit={async (e) => {
          e.preventDefault();
          if (!name.trim()) return setError(t("이름을 비워 둘 수 없습니다."));
          setBusy(true);
          setError(null);
          try {
            await api.updateProject(p.id, { name: name.trim(), description: desc.trim() });
            await qc.invalidateQueries({ queryKey: qk.project(p.id) });
            await qc.invalidateQueries({ queryKey: qk.projects });
            setDone(true);
            toast(t("저장했습니다."));
          } catch (err) {
            setError(err);
          } finally {
            setBusy(false);
          }
        }}
      >
        <div className="space-y-6">
          <Field label={t("이름")}>
            <Input value={name} onChange={(e) => (setName(e.target.value), setDone(false))} />
          </Field>
          <Field label={t("설명")}>
            <Input value={desc} onChange={(e) => (setDesc(e.target.value), setDone(false))} placeholder={t("이 프로젝트가 하는 일")} />
          </Field>
          <Field label={t("프로젝트 ID")}>
            <CopyField value={p.id} label={t("프로젝트 ID")} />
          </Field>
          {error ? <ErrorBox error={error} /> : null}
          <div className="flex items-center justify-between space-x-4">
            <Button type="submit" variant="primary" disabled={!dirty} loading={busy}>
              {t("업데이트")}
            </Button>
            <span className="text-sm tabular-nums text-gray-500">
              {t("{time}에 만들었습니다 · 서비스 {services}개 · 환경 {envs}개", {
                time: fmtTime(p.created_at),
                services: ws.detail?.services.length ?? 0,
                envs: ws.envs.length,
              })}
            </span>
          </div>
        </div>
      </form>
    </Card>
  );
}

/* ── project workspace + github account (v1.6) ───────────────────────── */

const WS_BADGE: Record<string, [string, string]> = {
  default: [t("기본"), "bg-blue-100 text-blue-700"],
  personal: [t("개인"), "bg-gray-100 text-gray-600"],
  team: [t("팀"), "bg-green-100 text-success"],
};

/** 프로젝트가 속한 작업 공간을 보여 주고, 다른 작업 공간으로 옮기고, 클론에 쓸 GitHub 계정을 고른다(프로젝트 admin). */
function ProjectWorkspaceGithub({ ws }: { ws: Workspace }) {
  const qc = useQueryClient();
  const caps = useCaps(ws.pid);
  const me = useMe().data;
  const myConn = useQuery({ queryKey: qk.githubConnection, queryFn: api.githubConnection, staleTime: 30_000 });
  const p = ws.detail!;
  const target = ws.wsList.map((w) => ({ ...w, kind: w.default ? "default" : w.personal ? "personal" : "team" }));
  const cur = ws.wsList.find((w) => w.id === p.workspace_id);
  // member+ 작업 공간으로만 옮길 수 있다(viewer 제외).
  const movable = target.filter((w) => w.role === "owner" || w.role === "admin" || w.role === "member");
  const [moving, setMoving] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  const gh = p.github_account ?? null;
  const myConnected = !!myConn.data?.connected;
  const usesMine = gh?.user_id === me?.id;
  // 서버 owner는 GitHub를 연결한 다른 사용자의 연결도 지정할 수 있다(감사 기록에 남는다).
  const isOwner = me?.instance_owner === true;
  const ghOwners = useQuery({ queryKey: ["owner", "github-connections"], queryFn: api.ownerGithubConnections, enabled: isOwner && caps.canAdmin });
  const others = (ghOwners.data ?? []).filter((c) => c.user_id !== me?.id);
  const { confirm, node: confirmNode } = useConfirm();

  const setGithub = (userID: string | null) =>
    runGh(async () => {
      await api.setProjectGithub(p.id, userID);
      await qc.invalidateQueries({ queryKey: qk.project(p.id) });
    });
  const runGh = async (f: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await f();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <Card
        title={t("작업 공간")}
        description={t("이 프로젝트가 속한 작업 공간입니다. 멤버와 접근 권한은 작업 공간을 따릅니다.")}
        className="mt-8 border-t border-gray-200 pt-8"
      >
        <div className="space-y-4">
          <div className="flex items-center gap-2">
            <span className="text-sm font-medium">{cur?.name ?? p.workspace_id}</span>
            {cur && (() => {
              const [label, cls] = WS_BADGE[cur.default ? "default" : cur.personal ? "personal" : "team"];
              return <span className={cx("rounded px-1.5 py-0.5 text-xs font-medium", cls)}>{label}</span>;
            })()}
          </div>
          {caps.canAdmin ? (
            <div className="max-w-md space-y-2">
              <div className={LABEL_CLS}>{t("다른 작업 공간으로 옮기기")}</div>
              <Select
                aria-label={t("옮길 작업 공간")}
                value={p.workspace_id}
                disabled={busy}
                onChange={(e) => {
                  const next = e.target.value;
                  if (next === p.workspace_id) return;
                  setMoving(true);
                  setError(null);
                  setBusy(true);
                  (async () => {
                    try {
                      await api.moveProject(p.id, next);
                      writeStorage("aven.workspace.v1", next);
                      await qc.invalidateQueries({ queryKey: qk.projects });
                      await qc.invalidateQueries({ queryKey: qk.project(p.id) });
                      toast(t("프로젝트를 옮겼습니다."));
                    } catch (err) {
                      setError(err);
                    } finally {
                      setBusy(false);
                      setMoving(false);
                    }
                  })();
                }}
              >
                {movable.map((w) => (
                  <option key={w.id} value={w.id}>
                    {w.name}
                    {w.default ? t(" (기본)") : w.personal ? t(" (개인)") : ""}
                  </option>
                ))}
              </Select>
              <p className="text-xs text-gray-500">{t("member 이상인 작업 공간으로만 옮길 수 있고, 대상에 같은 이름의 프로젝트가 있으면 옮길 수 없습니다.")}</p>
              {moving && <span className="text-xs text-gray-500">{t("옮기는 중…")}</span>}
            </div>
          ) : (
            <p className="text-sm text-gray-500">{t("프로젝트를 옮기려면 프로젝트 admin 권한이 필요합니다.")}</p>
          )}
        </div>
      </Card>

      <Card
        title={
          <span className="flex items-center gap-2.5">
            <BrandIcon name="github" className="size-5" /> {t("GitHub 계정")}
          </span>
        }
        description={t("비공개 저장소 클론과 커밋 상태 표시에 쓸 GitHub 계정입니다. 각 사용자가 자기 계정을 연결하고, 프로젝트는 그중 한 사람의 연결을 씁니다.")}
        className="mt-8 border-t border-gray-200 pt-8"
      >
        <div className="space-y-4">
          <div className="text-sm">
            {gh === null ? (
              <span className="text-gray-600">
                {tr("연결 안 됨 ({code} 사용)", { code: <span className="font-mono">AVEN_GITHUB_TOKEN</span> })}
              </span>
            ) : gh.connected ? (
              <span>
                {tr("{name}{login} 계정의 GitHub 연결을 씁니다", {
                  name: <b className="text-foreground">{gh.user_name}</b>,
                  login: gh.login ? <span className="text-gray-500"> · @{gh.login}</span> : null,
                })}
                {usesMine && <span className="text-gray-500"> ({t("내 계정")})</span>}
              </span>
            ) : (
              <span className="text-warn">
                {t("{name}님의 GitHub 연결이 끊겨 있습니다. 비공개 저장소를 클론할 수 없습니다.", { name: gh.user_name })}
              </span>
            )}
          </div>
          {caps.canAdmin ? (
            <div className="flex flex-wrap items-center gap-2">
              <Button
                variant="primary"
                loading={busy}
                disabled={!myConnected || usesMine}
                title={!myConnected ? t("먼저 계정 설정에서 내 GitHub 계정을 연결하세요") : undefined}
                onClick={() => me && setGithub(me.id)}
              >
                <BrandIcon name="github" className="size-4" /> {t("내 GitHub 계정 사용")}
              </Button>
              <Button variant="outline" loading={busy} disabled={gh === null} onClick={() => setGithub(null)}>
                {t("해제")}
              </Button>
              {isOwner && others.length > 0 && (
                <Select
                  aria-label={t("다른 사용자의 GitHub 연결 지정")}
                  className="w-auto"
                  value=""
                  disabled={busy}
                  onChange={(e) => {
                    const c = others.find((o) => o.user_id === e.target.value);
                    if (c)
                      confirm({
                        title: t("다른 사용자의 GitHub 연결 지정"),
                        message: t("이 프로젝트는 앞으로 {name}(@{login})의 GitHub 연결로 클론·배포합니다. 그 사람의 비공개 저장소 권한을 쓰게 되며, 서버 owner 권한 사용으로 감사 기록에 남습니다.", {
                          name: c.user_name,
                          login: c.login,
                        }),
                        confirmLabel: t("지정"),
                        danger: true,
                        run: () => setGithub(c.user_id),
                      });
                  }}
                >
                  <option value="">{t("다른 사용자의 연결 지정 (서버 owner)…")}</option>
                  {others.map((c) => (
                    <option key={c.user_id} value={c.user_id} disabled={c.user_id === gh?.user_id}>
                      {c.user_name} · @{c.login}
                    </option>
                  ))}
                </Select>
              )}
              {!myConnected && (
                <span className="text-xs text-gray-500">
                  {tr("내 계정을 쓰려면 먼저 {link}에서 GitHub를 연결하세요.", {
                    link: <Link href="/account/?section=integrations" className="text-pink-600 hover:underline">{t("계정 설정")}</Link>,
                  })}
                </span>
              )}
            </div>
          ) : (
            <p className="text-sm text-gray-500">{t("GitHub 계정을 바꾸려면 프로젝트 admin 권한이 필요합니다.")}</p>
          )}
          {error ? <ErrorBox error={error} /> : null}
        </div>
      </Card>
      {confirmNode}
    </>
  );
}

function EnvRow({ env, ws, onRename, onDelete }: { env: Environment; ws: Workspace; onRename: () => void; onDelete: () => void }) {
  const caps = useCaps(ws.pid);
  const hidden = env.restricted && !caps.canAdmin; // 제한된 환경(Environment RBAC): 리소스는 프로젝트 admin만
  const canvas = useCanvas(env.id, !hidden);
  const def = env.id === ws.detail?.default_environment_id;
  const list = canvas.data?.services ?? [];
  const online = list.filter((s) => s.instance.status === "online" || s.instance.status === "scheduled").length;
  const href = withCtx("/", { project: ws.pid, env: env.name });
  return (
    <li className="flex items-center gap-3 px-4 py-3">
      <span className="flex size-4 shrink-0 items-center justify-center text-pink-500">{ws.env?.id === env.id ? <Check className="size-4" /> : null}</span>
      <Link href={href} className="min-w-0 truncate text-base font-medium hover:underline">
        {env.name}
      </Link>
      {def && <span className="rounded bg-green-100 px-1.5 py-0.5 text-xs font-medium text-success">{t("기본")}</span>}
      {env.restricted && <RestrictedBadge />}
      <PRBadge env={env} link />
      <span className="ml-auto shrink-0 text-sm tabular-nums text-gray-500">
        {hidden ? t("Admin 전용") : canvas.isPending ? "…" : t("서비스 {count}개 · 실행 중 {online}개", { count: list.length, online })} · {t("{time} 생성", { time: fmtRelative(env.created_at) })}
      </span>
      <EnvRestrictionToggle env={env} canAdmin={caps.canAdmin} />
      <IconButton label={t("{name} 이름 바꾸기", { name: env.name })} onClick={onRename} {...lock(caps.canEdit && !hidden)}>
        <Pencil className="size-4" />
      </IconButton>
      <IconButton label={t("{name} 삭제", { name: env.name })} onClick={onDelete} disabled={ws.envs.length <= 1} {...lock(caps.canAdmin)}>
        <Trash2 className="size-4" />
      </IconButton>
    </li>
  );
}

function Environments({ ws }: { ws: Workspace }) {
  const [creating, setCreating] = useState(false);
  const [renaming, setRenaming] = useState<Environment | null>(null);
  const [deleting, setDeleting] = useState<Environment | null>(null);
  const router = useRouter();
  const caps = useCaps(ws.pid);
  return (
    <>
      <Card
        title={t("환경")}
        description={t("환경마다 서비스 설정, 변수, 도메인, 볼륨이 따로 있습니다. production이 아닌 환경에서는 화면 둘레에 색이 표시됩니다.")}
        action={
          <Button variant="primary" onClick={() => setCreating(true)} {...lock(caps.canEdit)}>
            <Plus className="size-3.5" /> {t("새 환경")}
          </Button>
        }
      >
        <RowList>
          {sortEnvs(ws.envs).map((e) => (
            <EnvRow key={e.id} env={e} ws={ws} onRename={() => setRenaming(e)} onDelete={() => setDeleting(e)} />
          ))}
        </RowList>
      </Card>
      <PermFieldset readOnly={!caps.canAdmin} note={t("PR 미리보기 설정은 프로젝트 admin만 바꿀 수 있습니다.")} className="mt-8 max-w-3xl border-t border-gray-200 pt-8">
        <PREnvironments ws={ws} />
      </PermFieldset>
      <NewEnvironmentDialog ws={ws} open={creating} onClose={() => setCreating(false)} />
      <RenameEnvironmentDialog
        env={renaming}
        onClose={() => setRenaming(null)}
        onRenamed={(e) => e.id === ws.env?.id && router.replace(withCtx("/settings/", { project: ws.pid, env: e.name }, { section: "environments" }))}
      />
      <DeleteEnvironmentDialog
        env={deleting}
        onClose={() => setDeleting(null)}
        onDeleted={() => {
          if (deleting?.id === ws.env?.id) router.replace(withCtx("/settings/", { project: ws.pid }, { section: "environments" }));
        }}
      />
    </>
  );
}

const PR_DEFAULTS: PREnvironmentSettings = { enabled: false, base_environment_id: null, auto_delete: true, domain: true, focused: false };

function PRToggleRow({ label, hint, checked, onChange, disabled }: { label: string; hint: ReactNode; checked: boolean; onChange: (v: boolean) => void; disabled?: boolean }) {
  return (
    <div className="flex items-start gap-4">
      <div className="min-w-0 flex-1">
        <div className="text-sm font-semibold">{label}</div>
        <div className="mt-0.5 text-sm leading-snug text-gray-500">{hint}</div>
      </div>
      <Toggle checked={checked} onChange={onChange} label={label} disabled={disabled} />
    </div>
  );
}

/** PR preview environments: one `pr-<number>` environment per GitHub pull request, forked from a base environment. */
function PREnvironments({ ws }: { ws: Workspace }) {
  const [open, setOpen] = useState(false);
  const p = ws.detail!;
  const enabled = !!p.pr_environments?.enabled;
  return (
    <Card
      title={t("PR 환경")}
      description={t("GitHub PR이 열리면 Aven이 자동으로 환경을 만들고, PR이 닫히면 정리합니다.")}
      action={
        <Button variant="default" onClick={() => setOpen(true)}>
          {enabled ? t("PR 환경 설정") : t("PR 환경 켜기")}
        </Button>
      }
    >
      {open &&
        createPortal(
          <div className="fixed inset-0 z-[10000] flex animate-modal-overlay-in items-center justify-center bg-black/[.45] p-4 dark:bg-black/80" onMouseDown={(e) => e.target === e.currentTarget && setOpen(false)}>
            <div role="dialog" aria-modal="true" aria-label={t("PR 환경")} className="relative max-h-[90vh] w-full max-w-[640px] animate-modal-scale-in overflow-y-auto rounded-lg bg-background p-6 dark:bg-secondaryBg">
              <div className="mb-4 flex items-center">
                <h2 className="text-xl font-semibold">{t("PR 환경")}</h2>
                <IconButton label={t("닫기")} size="sm" onClick={() => setOpen(false)} className="ml-auto">
                  <X />
                </IconButton>
              </div>
              <PRForm ws={ws} />
            </div>
          </div>,
          document.body,
        )}
    </Card>
  );
}

function PRForm({ ws }: { ws: Workspace }) {
  const qc = useQueryClient();
  const p = ws.detail!;
  const saved0 = p.pr_environments ?? PR_DEFAULTS;
  const forks0 = p.pr_from_forks ?? false;
  const bots0 = p.pr_from_bots ?? false;
  const [cfg, setCfg] = useState<PREnvironmentSettings>(saved0);
  const [forks, setForks] = useState(forks0);
  const [bots, setBots] = useState(bots0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [saved, setSaved] = useState(false);
  useEffect(() => {
    setCfg(p.pr_environments ?? PR_DEFAULTS);
    setForks(p.pr_from_forks ?? false);
    setBots(p.pr_from_bots ?? false);
  }, [p.pr_environments, p.pr_from_forks, p.pr_from_bots]);
  const dirty = JSON.stringify(cfg) !== JSON.stringify(saved0) || forks !== forks0 || bots !== bots0;
  const standard = ws.envs.filter((e) => !isPreview(e));
  const defName = standard.find((e) => e.id === p.default_environment_id)?.name ?? t("기본 환경");
  const previews = ws.envs.filter(isPreview);
  const set = (patch: Partial<PREnvironmentSettings>) => {
    setCfg((c) => ({ ...c, ...patch }));
    setSaved(false);
  };
  return (
    <form
      className="w-full"
      onSubmit={async (e) => {
        e.preventDefault();
        setBusy(true);
        setError(null);
        try {
          await api.updateProject(p.id, { pr_environments: cfg, pr_from_forks: forks, pr_from_bots: bots });
          await qc.invalidateQueries({ queryKey: qk.project(p.id) });
          setSaved(true);
          toast(t("저장했습니다."));
        } catch (err) {
          setError(err);
        } finally {
          setBusy(false);
        }
      }}
    >
      <div className="border-b border-gray-200 px-5 py-4">
        <div className="flex items-center gap-2 text-base font-semibold">
          <GitPullRequest className="size-4 text-pink-600" aria-hidden /> {t("PR 미리보기 환경")}
        </div>
        <div className="text-sm text-gray-500">
          {tr("GitHub PR이 열리면 base 환경을 복사해 {env} 환경을 만들고 PR 브랜치를 배포합니다. 웹훅에 {event} 이벤트가 필요합니다.", {
            env: <span className="font-mono">pr-&lt;{t("번호")}&gt;</span>,
            event: <b className="text-foreground">Pull requests</b>,
          })}
        </div>
      </div>
      <div className="space-y-5 px-5 py-5">
        <PRToggleRow
          label={t("PR 환경 사용")}
          hint={t("base 환경에서 PR 저장소와 PR base 브랜치를 쓰는 서비스가 있을 때만 만듭니다.")}
          checked={cfg.enabled}
          onChange={(v) => set({ enabled: v })}
        />
        <Field label={t("base 환경")} hint={t("설정, 변수, 공유 변수를 복사합니다. 볼륨은 빈 볼륨으로 새로 만들고 데이터는 복사하지 않습니다.")}>
          <Select
            value={cfg.base_environment_id ?? ""}
            onChange={(e) => set({ base_environment_id: e.target.value || null })}
            disabled={!cfg.enabled}
          >
            <option value="">{t("프로젝트 기본 환경 ({name})", { name: defName })}</option>
            {standard.map((e) => (
              <option key={e.id} value={e.id}>
                {e.name}
              </option>
            ))}
          </Select>
        </Field>
        <PRToggleRow
          label={t("PR이 닫히면 삭제")}
          hint={t("병합되거나 닫힌 PR의 환경을 컨테이너와 볼륨까지 지웁니다.")}
          checked={cfg.auto_delete}
          onChange={(v) => set({ auto_delete: v })}
          disabled={!cfg.enabled}
        />
        <PRToggleRow
          label={t("도메인 생성")}
          hint={tr("base 환경에서 도메인이 있던 서비스에 {domain} 도메인을 붙입니다(AVEN_BASE_DOMAIN 필요).", {
            domain: <span className="font-mono">&lt;{t("서비스")}&gt;-pr-&lt;{t("번호")}&gt;</span>,
          })}
          checked={cfg.domain}
          onChange={(v) => set({ domain: v })}
          disabled={!cfg.enabled}
        />
        <PRToggleRow
          label={t("fork PR 허용")}
          hint={t("다른 저장소(fork)에서 온 PR도 배포합니다. 외부 코드가 이 서버에서 변수와 함께 실행되므로 신뢰할 수 있을 때만 켜세요.")}
          checked={forks}
          onChange={(v) => (setForks(v), setSaved(false))}
          disabled={!cfg.enabled}
        />
        <PRToggleRow
          label={t("집중형 PR 환경")}
          hint={t("변경 파일이 watch paths 또는 루트 디렉터리에 맞는 저장소 서비스와 그 변수 참조 의존 서비스만 배포합니다. 나머지 서비스의 참조 값은 base 환경에서 가져옵니다.")}
          checked={cfg.focused}
          onChange={(v) => set({ focused: v })}
          disabled={!cfg.enabled}
        />
        <PRToggleRow
          label={t("봇 PR 환경 허용")}
          hint={t("Dependabot, Renovate 등 GitHub 봇이 연 PR에도 미리보기 환경을 만듭니다.")}
          checked={bots}
          onChange={(v) => (setBots(v), setSaved(false))}
          disabled={!cfg.enabled}
        />
        <div className="flex items-center gap-3">
          <Button type="submit" variant="primary" disabled={!dirty} loading={busy}>
            {t("저장")}
          </Button>
          {previews.length > 0 && <span className="ml-auto text-sm tabular-nums text-gray-500">{t("미리보기 환경 {count}개", { count: previews.length })}</span>}
        </div>
        {error ? <ErrorBox error={error} /> : null}
      </div>
    </form>
  );
}

/* ── shared variables ────────────────────────────────────────────────── */

const KEY_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;

function SharedEditor({ env }: { env: Environment }) {
  const qc = useQueryClient();
  const shared = useSharedVariables(env.id);
  const [draft, setDraft] = useState<Record<string, string> | null>(null);
  const [shown, setShown] = useState<Set<string>>(new Set());
  const [newK, setNewK] = useState("");
  const [newV, setNewV] = useState("");
  const [raw, setRaw] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [saved, setSaved] = useState(false);
  const base = shared.data ?? {};
  const cur = draft ?? base;
  const keys = useMemo(() => [...new Set([...Object.keys(base), ...Object.keys(cur)])].sort(), [base, cur]);
  const changes = keys.filter((k) => base[k] !== cur[k]).length;

  if (shared.isPending) return <Spinner />;
  if (shared.error) return <ErrorBox error={shared.error} />;

  const set = (next: Record<string, string>) => {
    setDraft(next);
    setSaved(false);
  };

  return (
    <div className="px-4 pb-4">
      <div className="flex items-center gap-2 border-b border-gray-200 pb-3">
        <span className="mr-auto text-sm tabular-nums text-gray-600">
          {t("{count}개 변수", { count: Object.keys(cur).length })}
        </span>
        <Button variant="ghost" onClick={() => setRaw(true)} className="text-foreground/90">
          <Braces className="size-4 text-pink-600" /> {t("RAW 편집")}
        </Button>
      </div>
      <ul className="mt-2">
        {keys.map((k) => {
          const deleted = !(k in cur);
          const changed = !deleted && base[k] !== cur[k];
          return (
            <li key={k} className={cx("group flex h-11 items-center gap-3 rounded px-3 hover:bg-secondaryBg dark:hover:bg-gray-100", deleted && "opacity-50")}>
              <span className={cx("flex w-[38%] min-w-0 shrink-0 items-center gap-2 font-mono text-sm", deleted && "line-through")}>
                <span className="truncate">{k}</span>
                {(changed || deleted) && <span className={cx("size-1.5 rounded-full", deleted ? "bg-red-600" : !(k in base) ? "bg-green-600" : "bg-pink-600")} />}
              </span>
              {shown.has(k) && !deleted ? (
                <Input
                  mono
                  size="sm"
                  value={cur[k]}
                  onChange={(e) => set({ ...cur, [k]: e.target.value })}
                  aria-label={t("{key} 값", { key: k })}
                  className="h-8 flex-1"
                />
              ) : (
                <span className="min-w-0 flex-1 font-mono text-sm">*******</span>
              )}
              {deleted ? (
                <IconButton label={t("{key} 삭제 취소", { key: k })} onClick={() => set({ ...cur, [k]: base[k] })}>
                  <Undo2 className="size-4" />
                </IconButton>
              ) : (
                <>
                  <IconButton
                    label={shown.has(k) ? t("{key} 숨기기", { key: k }) : t("{key} 보기/편집", { key: k })}
                    onClick={() =>
                      setShown((s) => {
                        const n = new Set(s);
                        if (n.has(k)) n.delete(k);
                        else n.add(k);
                        return n;
                      })
                    }
                  >
                    {shown.has(k) ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
                  </IconButton>
                  <IconButton
                    label={t("{key} 삭제", { key: k })}
                    onClick={() => {
                      const n = { ...cur };
                      delete n[k];
                      set(n);
                    }}
                  >
                    <Trash2 className="size-4" />
                  </IconButton>
                </>
              )}
            </li>
          );
        })}
      </ul>
      <form
        className="mt-3 flex gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          const k = newK.trim();
          if (!KEY_RE.test(k)) return setError(t("이름은 영문, 숫자, 밑줄(_)만 쓸 수 있고 숫자로 시작할 수 없습니다."));
          setError(null);
          set({ ...cur, [k]: newV });
          setNewK("");
          setNewV("");
        }}
      >
        <Input mono value={newK} onChange={(e) => setNewK(e.target.value)} placeholder="VARIABLE_NAME" aria-label={t("새 공유 변수 이름")} className="w-[38%] shrink-0" />
        <Input mono value={newV} onChange={(e) => setNewV(e.target.value)} placeholder={t("값")} aria-label={t("새 공유 변수 값")} autoComplete="off" />
        <Button type="submit" variant="accent" size="lg" disabled={!newK.trim()}>
          <Plus className="size-4" /> {t("추가")}
        </Button>
      </form>
      <div className="mt-5 flex items-center gap-3">
        <Button
          variant="primary"
          disabled={changes === 0}
          loading={busy}
          onClick={async () => {
            setBusy(true);
            setError(null);
            try {
              await api.setSharedVariables(env.id, cur);
              await qc.invalidateQueries({ queryKey: qk.shared(env.id) });
              setDraft(null);
              setSaved(true);
              toast(t("저장했습니다."));
              toast(t("저장했습니다. 이 변수를 참조하는 서비스는 다음 배포부터 새 값을 씁니다."));
            } catch (e) {
              setError(e);
            } finally {
              setBusy(false);
            }
          }}
        >
          {changes ? t("변경 사항 {count}개 저장", { count: changes }) : t("저장")}
        </Button>
        {changes > 0 && (
          <Button variant="ghost" onClick={() => setDraft(null)}>
            {t("되돌리기")}
          </Button>
        )}
      </div>
      {error ? <ErrorBox className="mt-3" error={error} /> : null}
      {raw && (
        <RawEditor
          title={t("{name} 공유 변수 RAW 편집", { name: env.name })}
          initial={toDotenv(cur)}
          onClose={() => setRaw(false)}
          onApply={(text) => {
            set(Object.fromEntries(parseDotenv(text).filter(([k]) => KEY_RE.test(k))));
            setRaw(false);
          }}
        />
      )}
    </div>
  );
}

function SharedEnvRow({ env, open: init }: { env: Environment; open: boolean }) {
  const [open, setOpen] = useState(init);
  const shared = useSharedVariables(env.id);
  const n = Object.keys(shared.data ?? {}).length;
  return (
    <li>
      <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} className="flex w-full cursor-pointer items-center gap-3 px-4 py-3 text-left hover:bg-gray-100/50">
        <ChevronRight className={cx("size-4 text-gray-500 transition-transform", open && "rotate-90")} />
        <span className="flex-1 text-base font-medium">{env.name}</span>
        <span className="text-sm tabular-nums text-gray-500">{shared.isPending ? "…" : t("변수 {count}개", { count: n })}</span>
        <Braces className="size-4 text-gray-500" />
      </button>
      {open && <SharedEditor env={env} />}
    </li>
  );
}

function Shared({ ws }: { ws: Workspace }) {
  return (
    <>
      <Card
        title={t("공유 변수")}
        description={tr("환경 안의 모든 서비스가 {ref}으로 참조할 수 있는 변수입니다. 서비스 변수 탭의 ‘공유 변수’ 버튼으로 빠르게 연결하세요.", {
          ref: <span className="font-mono text-foreground">{t("${{shared.이름}}")}</span>,
        })}
      >
        <RowList>
          {ws.envs.map((e) => (
            <SharedEnvRow key={e.id} env={e} open={e.id === ws.env?.id && ws.envs.length === 1} />
          ))}
        </RowList>
      </Card>
    </>
  );
}

/* ── integrations ────────────────────────────────────────────────────── */

const EVENT_LABEL: Record<NotifyEvent, string> = {
  "deploy.success": t("배포 성공"),
  "deploy.failed": t("배포 실패"),
  "deploy.crashed": t("크래시"),
  "volume.backup.failed": t("백업 실패"),
  "volume.near_limit": t("볼륨 90% 도달"),
  "volume.limit_exceeded": t("볼륨 상한 초과"),
};

function Pill({ ok, yes, no }: { ok: boolean; yes: string; no: string }) {
  return (
    <span
      className={cx(
        "inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs font-medium",
        ok ? "bg-green-100 text-success" : "bg-warn-surface text-warn",
      )}
    >
      {ok ? <Check className="size-3" /> : <X className="size-3" />}
      {ok ? yes : no}
    </span>
  );
}

function Integrations({ ws }: { ws: Workspace }) {
  const qc = useQueryClient();
  const gh = useGithub();
  const p = ws.detail!;
  const [url, setUrl] = useState(p.notify_webhook_url ?? "");
  const [events, setEvents] = useState<NotifyEvent[]>(p.notify_events);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [saved, setSaved] = useState(false);
  const dirty = (url.trim() || null) !== p.notify_webhook_url || [...events].sort().join() !== [...p.notify_events].sort().join();
  return (
    <>
      <div className="space-y-8">
        <Card
          title={
            <span className="flex items-center gap-2.5">
              <BrandIcon name="github" className="size-5" /> {t("GitHub 웹훅")}
            </span>
          }
          description={t("자동 배포가 켜진 서비스는 브랜치에 푸시하면 배포됩니다.")}
        >
          <div className="space-y-5">
            {gh.error ? (
              <ErrorBox error={gh.error} />
            ) : !gh.data ? (
              <Spinner />
            ) : (
              <>
                <div>
                  <div className={LABEL_CLS}>Payload URL</div>
                  <CopyField value={gh.data.webhook_url} label={t("웹훅 URL")} />
                </div>
                <div className="flex flex-wrap gap-2">
                  <Pill ok={gh.data.secret_configured} yes={t("웹훅 시크릿 설정됨")} no={t("웹훅 시크릿 없음")} />
                  <Pill ok={gh.data.token_configured} yes={t("GitHub 토큰 있음")} no={t("GitHub 토큰 없음")} />
                </div>
                {!gh.data.secret_configured && (
                  <p className="text-sm text-warn">
                    {tr("서버에 {name}이 없으면 서명을 확인할 수 없어 웹훅을 받지 않습니다.", { name: <span className="font-mono">AVEN_GITHUB_WEBHOOK_SECRET</span> })}
                  </p>
                )}
                {!gh.data.token_configured && (
                  <p className="text-sm text-gray-500">{t("위의 GitHub 연결(또는 서버의 AVEN_GITHUB_TOKEN)이 있어야 비공개 저장소를 클론할 수 있습니다.")}</p>
                )}
                <p className="text-sm text-gray-500">{t("GitHub를 연결했다면 위의 \"저장소 웹훅 만들기\"로 아래 과정을 대신할 수 있습니다.")}</p>
                <ol className="list-decimal space-y-1.5 pl-5 text-sm text-gray-600 marker:text-gray-400">
                  <li>
                    {tr("GitHub 저장소의 {menu}을 엽니다.", { menu: <b className="text-foreground">Settings › Webhooks › Add webhook</b> })}
                  </li>
                  <li>
                    {tr("{url}에 위 주소를 붙여넣고, {type}은 {value}으로 고릅니다.", {
                      url: <b className="text-foreground">Payload URL</b>,
                      type: <b className="text-foreground">Content type</b>,
                      value: <span className="font-mono text-foreground">application/json</span>,
                    })}
                  </li>
                  <li>
                    {tr("{field}에 서버의 {name} 값을 넣습니다.", {
                      field: <b className="text-foreground">Secret</b>,
                      name: <span className="font-mono text-foreground">AVEN_GITHUB_WEBHOOK_SECRET</span>,
                    })}
                  </li>
                  <li>
                    {tr("이벤트는 {push}를 고르고 저장합니다. PR 미리보기 환경을 쓰면 {pr}, CI 대기를 쓰면 {checks} 또는 {runs}도 고르세요.", {
                      push: <b className="text-foreground">Pushes</b>,
                      pr: <b className="text-foreground">Pull requests</b>,
                      checks: <b className="text-foreground">Check suites</b>,
                      runs: <b className="text-foreground">Workflow runs</b>,
                    })}
                  </li>
                  <li>{t("서비스 설정 › 소스에서 저장소, 브랜치, 자동 배포를 확인하세요.")}</li>
                </ol>
              </>
            )}
          </div>
        </Card>

        <form
          onSubmit={async (e) => {
            e.preventDefault();
            if (url.trim() && !/^https:\/\//.test(url.trim())) return setError(t("https://로 시작하는 주소를 입력하세요."));
            setBusy(true);
            setError(null);
            try {
              await api.updateProject(p.id, { notify_webhook_url: url.trim() || null, notify_events: events });
              await qc.invalidateQueries({ queryKey: qk.project(p.id) });
              setSaved(true);
              toast(t("저장했습니다."));
            } catch (err) {
              setError(err);
            } finally {
              setBusy(false);
            }
          }}
        >
          <Card
            className="border-t border-gray-200 pt-8"
            title={t("알림 웹훅")}
            description={t("Discord나 Slack 호환 웹훅으로 이벤트를 보냅니다.")}
            footer={
              <Button type="submit" variant="primary" disabled={!dirty} loading={busy}>
                {t("저장")}
              </Button>
            }
          >
            <div className="space-y-5">
            <Field label={t("웹훅 URL")}>
              <Input mono value={url} onChange={(e) => (setUrl(e.target.value), setSaved(false))} placeholder="https://discord.com/api/webhooks/…" />
            </Field>
            <fieldset>
              <legend className={LABEL_CLS}>{t("보낼 이벤트")}</legend>
              <div className="grid gap-2 sm:grid-cols-2">
                {NOTIFY_EVENTS.map((ev) => (
                  <label key={ev} className="flex cursor-pointer items-center gap-2.5 rounded-md border border-gray-200 px-3 py-2.5 text-sm hover:border-gray-300">
                    <input
                      type="checkbox"
                      checked={events.includes(ev)}
                      onChange={(e) => {
                        setSaved(false);
                        setEvents((cur) => (e.target.checked ? [...cur, ev] : cur.filter((x) => x !== ev)));
                      }}
                      className="size-4 cursor-pointer accent-pink-500"
                    />
                    <span className="flex-1">{EVENT_LABEL[ev]}</span>
                    <span className="font-mono text-xs text-gray-400">{ev}</span>
                  </label>
                ))}
              </div>
            </fieldset>
              {error ? <ErrorBox error={error} /> : null}
            </div>
          </Card>
        </form>
      </div>
    </>
  );
}

/* ── system ──────────────────────────────────────────────────────────── */

/* ── danger ──────────────────────────────────────────────────────────── */

function DangerNote({ children }: { children: ReactNode }) {
  return (
    <div className="flex items-start gap-3 rounded-md border border-red-200 px-4 py-3 text-sm text-danger">
      <AlertTriangle className="mt-0.5 size-4 shrink-0" />
      {children}
    </div>
  );
}

function Danger({ ws }: { ws: Workspace }) {
  const qc = useQueryClient();
  const router = useRouter();
  const p = ws.detail!;
  const [open, setOpen] = useState(false);
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  return (
    <>
      <Card title={t("서비스 관리")}>
        <div className="space-y-4">
          <DangerNote>{t("서비스를 프로젝트에서 삭제하면 모든 환경의 데이터와 배포가 영구 삭제됩니다. 서비스는 캔버스의 서비스 설정에서 삭제하세요.")}</DangerNote>
          <RowList>
            {p.services.map((sv) => (
              <li key={sv.id} className="flex items-center gap-3 px-4 py-3">
                <span className="min-w-0 flex-1 truncate text-sm font-medium">{sv.name}</span>
                <Link
                  href={withCtx("/", { project: p.id }, { svc: sv.id, tab: "settings" })}
                  className="inline-flex h-[26px] items-center rounded-md border border-red-200 px-1.5 text-xs text-red-500 hover:bg-red-100 focus:outline-none focus-visible:ring-2 focus-visible:ring-red-600 dark:text-red-700"
                >
                  {t("삭제…")}
                </Link>
              </li>
            ))}
          </RowList>
        </div>
      </Card>
      <Card title={t("프로젝트 삭제")}>
        <div className="space-y-4">
          <DangerNote>{t("프로젝트를 삭제하면 모든 환경의 서비스, 컨테이너, 볼륨, 배포 기록이 영구 삭제됩니다. 되돌릴 수 없습니다.")}</DangerNote>
          <Button variant="danger" onClick={() => setOpen(true)}>
            {t("프로젝트 삭제")}
          </Button>
        </div>
      </Card>
      <Dialog
        open={open}
        onClose={() => !busy && setOpen(false)}
        title={t("{name} 삭제", { name: p.name })}
        footer={
          <>
            <Button variant="outline" onClick={() => setOpen(false)} disabled={busy}>
              {t("취소")}
            </Button>
            <Button
              variant="danger"
              loading={busy}
              disabled={typed !== p.name}
              onClick={async () => {
                setBusy(true);
                try {
                  await api.deleteProject(p.id);
                  removeStorage("aven.project.v1", "keel.project.v1");
                  await qc.invalidateQueries({ queryKey: qk.projects });
                  router.replace("/");
                } catch (e) {
                  setError(e);
                  setBusy(false);
                }
              }}
            >
              {t("영구 삭제")}
            </Button>
          </>
        }
      >
        <div className="space-y-3 pt-1">
          <p>
            {tr("서비스 {count}개와 볼륨 데이터가 모두 지워집니다. 확인을 위해 프로젝트 이름 {name}을 입력하세요.", {
              count: p.services.length,
              name: <span className="font-mono text-foreground">{p.name}</span>,
            })}
          </p>
          <Input mono value={typed} onChange={(e) => setTyped(e.target.value)} aria-label={t("프로젝트 이름 확인")} autoComplete="off" />
          {error ? <ErrorBox error={error} /> : null}
        </div>
      </Dialog>
    </>
  );
}

/* ── page ────────────────────────────────────────────────────────────── */

function SettingsInner() {
  const ws = useWorkspace();
  const params = useSearchParams();
  const router = useRouter();
  const href = useCtxHref();
  const caps = useCaps(ws.detail?.id, ws.wsid);
  const nav = NAV.filter((n) => n.id === "access-groups" ? caps.workspaceAdmin
    : n.id === "danger" || n.id === "registries" ? caps.canAdmin : true);
  const raw = params.get("section");
  const adminSection = raw && Object.hasOwn(ADMIN_SECTIONS, raw) ? ADMIN_SECTIONS[raw] : null;
  const section: SectionId = nav.find((n) => n.id === raw)?.id ?? "general";
  const go = (id: SectionId) => router.replace(href("/settings/", { section: id === "general" ? null : id }), { scroll: false });

  useEffect(() => {
    if (adminSection) router.replace(`/admin/?section=${adminSection}`);
  }, [adminSection, router]);

  useEffect(() => {
    document.title = t("설정") + " · aven";
    return () => {
      document.title = "aven";
    };
  }, []);

  if (adminSection) return <Spinner />;

  return (
    <PageShell title={t("설정")}>
      <SideNavLayout label={t("설정 메뉴")} items={nav.map((n) => ({ ...n, danger: n.id === "danger" }))} value={section} onChange={go}>
            {ws.error ? (
              <ErrorBox error={ws.error} />
            ) : section === "workspace-members" ? (
              <WorkspaceMembersSettings key={ws.wsid} ws={ws} />
            ) : section === "access-groups" && ws.wsid ? <AccessGroupsSettings key={ws.wsid} wsid={ws.wsid} />
            : !ws.detail ? (
              ws.isPending ? (
                <Spinner />
              ) : (
                <div className="text-sm text-gray-600">{t("프로젝트가 없습니다. 위쪽 프로젝트 메뉴에서 새로 만드세요.")}</div>
              )
            ) : section === "general" ? (
              <>
                {caps.readOnly && (
                  <p className="mb-4 max-w-3xl rounded-md border border-gray-200 bg-gray-100 px-3 py-2 text-sm text-gray-600">
                    {t("이 프로젝트의 Viewer라서 읽기만 할 수 있습니다. 변수 값은 가려집니다.")}
                  </p>
                )}
                <PermFieldset readOnly={!caps.canAdmin} note={t("프로젝트 정보는 프로젝트 admin만 바꿀 수 있습니다.")}>
                  <General key={ws.detail.id} ws={ws} />
                </PermFieldset>
                <div className="max-w-3xl">
                  <ProjectWorkspaceGithub key={`wsgh-${ws.detail.id}`} ws={ws} />
                </div>
              </>
            ) : section === "members" ? (
              <>
                <ProjectMembers key={ws.detail.id} pid={ws.detail.id} />
                <ProjectAccessGroups key={`groups-${ws.detail.id}`} pid={ws.detail.id} />
              </>
            ) : section === "iac" ? (
              <IACSettings key={`${ws.detail.id}-${ws.env?.id ?? ""}`} ws={ws} canEdit={caps.canEdit} canAdmin={caps.canAdmin} />





            ) : section === "environments" ? (
              <Environments ws={ws} />
            ) : section === "flags" ? (
              <FeatureFlags key={ws.detail.id} pid={ws.detail.id} environments={ws.envs} currentEnvironmentID={ws.env?.id ?? ""} canAdmin={caps.canAdmin} />
            ) : section === "shared" ? (
              <PermFieldset readOnly={!caps.canEdit} note={t("공유 변수는 member 이상만 바꿀 수 있습니다.")}>
                <Shared key={ws.detail.id} ws={ws} />
              </PermFieldset>

            ) : section === "registries" ? (
              <RegistriesView key={ws.detail.id} pid={ws.detail.id} />
            ) : section === "integrations" ? (
              <div className="space-y-8">
                <GitHubConnectionCard />
                <PermFieldset readOnly={!caps.canAdmin} note={t("알림 웹훅은 프로젝트 admin만 바꿀 수 있습니다.")} className="border-t border-gray-200 pt-8">
                  <Integrations key={ws.detail.id} ws={ws} />
                </PermFieldset>
              </div>
            ) : section === "mcp" ? (
              <MCPSettings project={ws.detail.name} env={ws.env?.name} />
            ) : section === "notifications" ? (
              <>
                <H2 sub={t("이벤트를 webhook(HMAC 서명), Slack, Discord, 이메일로 보냅니다. 실패하면 3번까지 다시 보내고 채널마다 최근 100개의 전달 기록을 남깁니다.")}>
                  {t("알림 채널")}
                </H2>
                <NotificationChannels key={ws.detail.id} pid={ws.detail.id} canEdit={caps.canAdmin} title={t("{name} 프로젝트", { name: ws.detail.name })} />
              </>
            ) : section === "usage" ? (
              <>
                <UsageAlertSettings key={ws.detail.id} project={ws.detail} canEdit={caps.canAdmin} />
                <div className="mt-8 max-w-3xl border-t border-gray-200 pt-8">
                  <UsageLimitSettings key={`limit-${ws.detail.id}`} project={ws.detail} canEdit={caps.canAdmin} />
                </div>
              </>

            ) : (
              <Danger ws={ws} />
            )}
      </SideNavLayout>
    </PageShell>
  );
}

export default function ProjectSettingsPage() {
  return (
    <Suspense fallback={<div className="dot-grid min-h-full" />}>
      <SettingsInner />
    </Suspense>
  );
}
