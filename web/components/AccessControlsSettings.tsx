"use client";

/* Enterprise access controls, modeled on Railway's dashboard (access groups, environment RBAC, guardrails).
   The server enforces every rule (403/404); these screens configure them and explain blocked actions. */

import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Lock, MoreHorizontal, Pencil, Plus, ShieldAlert, Trash2, Users, X } from "lucide-react";
import { api, type AccessGroup, type AccessGroupInput, type Environment, type ID, type ProjectRole } from "@/lib/api";
import { qk, useCaps, useMe } from "@/lib/queries";
import { toast } from "@/lib/uiState";
import { t, tr } from "@/lib/i18n";
import { Avatar, Button, Card, CenterSpinner, Dialog, EmptyState, ErrorBox, Field, IconButton, Input, Menu, MenuItem, Select, Toggle, cx, useConfirm } from "./ui";

const ROLE_LABEL: Record<ProjectRole, string> = { admin: "Admin", member: "Member", viewer: "Viewer" };
const roleBadge = "rounded bg-gray-100 px-1.5 py-0.5 text-xs font-medium text-gray-600";

/** Guardrail summary for the current user (any role). */
export function useAccessPolicies() {
  return useQuery({ queryKey: qk.accessPolicies, queryFn: api.accessPolicies, staleTime: 30_000 });
}

/** Restrictions that apply to the current user right now (non-admins for public networking, everyone for sources). */
export function useGuardrails() {
  const p = useAccessPolicies().data;
  const admin = p?.instance_admin ?? false;
  return {
    generatedDomains: !!p?.restrict_generated_domains && !admin,
    tcpProxy: !!p?.restrict_tcp_proxy && !admin,
    deploymentSources: !!p?.restrict_deployment_sources,
  };
}

function PolicyNotice({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <p role="note" className={cx("flex items-start gap-2 rounded-md border border-yellow-200 bg-warn-surface px-3 py-2 text-sm text-warn", className)}>
      <ShieldAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
      <span>{children}</span>
    </p>
  );
}

/** Banner in a service's networking settings when a workspace guardrail blocks generating domains / TCP proxies. */
export function NetworkingGuardrailNotice({ domains, tcp }: { domains: boolean; tcp: boolean }) {
  if (!domains && !tcp) return null;
  const message =
    domains && tcp
      ? t("작업 공간 정책으로 생성 도메인과 TCP 프록시는 작업 공간 Admin만 만들 수 있습니다. 사용자 지정 도메인과 비공개 네트워크는 그대로 쓸 수 있습니다.")
      : domains
        ? t("작업 공간 정책으로 생성 도메인은 작업 공간 Admin만 만들 수 있습니다. 사용자 지정 도메인과 비공개 네트워크는 그대로 쓸 수 있습니다.")
        : t("작업 공간 정책으로 TCP 프록시는 작업 공간 Admin만 만들 수 있습니다. 비공개 네트워크는 그대로 쓸 수 있습니다.");
  return <PolicyNotice>{message}</PolicyNotice>;
}

/** Shown instead of the canvas when the environment is restricted and the user is not a project admin. */
export function RestrictedEnvironmentNotice({ name }: { name: string }) {
  return (
    <div className="w-full max-w-md text-center">
      <div className="mx-auto flex size-12 items-center justify-center rounded-lg border border-gray-200 bg-secondaryBg">
        <Lock className="size-5 text-gray-600" />
      </div>
      <h1 className="mt-4 text-lg font-semibold">{t("{name} 환경은 제한되어 있습니다", { name })}</h1>
      <p className="mt-1.5 text-sm leading-relaxed text-gray-600">
        {t("이 환경의 서비스, 변수, 로그, 지표와 설정은 프로젝트 Admin만 볼 수 있습니다. git push로 시작되는 배포는 그대로 진행됩니다.")}
      </p>
    </div>
  );
}

/* ── Project → Environments: 제한(Environment RBAC) ───────────────────── */

export function RestrictedBadge() {
  return (
    <span className="inline-flex items-center gap-1 rounded bg-yellow-100 px-1.5 py-0.5 text-xs font-medium text-yellow-700" title={t("프로젝트 Admin만 이 환경의 리소스에 접근합니다")}>
      <Lock className="size-3" aria-hidden /> {t("제한됨")}
    </span>
  );
}

/** "제한" switch in the environment list. Only project admins can change it (server returns 403 otherwise). */
export function EnvRestrictionToggle({ env, canAdmin }: { env: Environment; canAdmin: boolean }) {
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  const { confirm, node } = useConfirm();
  const apply = async (restricted: boolean) => {
    setBusy(true);
    try {
      await api.updateEnvironment(env.id, { restricted });
      await Promise.all([qc.invalidateQueries({ queryKey: qk.project(env.project_id) }), qc.invalidateQueries({ queryKey: qk.canvas(env.id) })]);
      toast(restricted ? t("{name} 환경을 제한했습니다.", { name: env.name }) : t("{name} 환경의 제한을 풀었습니다.", { name: env.name }));
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e), "error");
    } finally {
      setBusy(false);
    }
  };
  return (
    <label className="flex shrink-0 items-center gap-2 text-sm text-gray-600" title={canAdmin ? undefined : t("프로젝트 Admin만 바꿀 수 있습니다")}>
      {node}
      {t("제한")}
      <Toggle
        checked={!!env.restricted}
        disabled={!canAdmin || busy}
        label={t("{name} 환경 제한", { name: env.name })}
        onChange={(v) =>
          v
            ? confirm({
                title: t("{name} 환경 제한", { name: env.name }),
                message: t("프로젝트 Admin이 아닌 사람은 이 환경이 있다는 것만 보고 서비스, 변수, 로그, 지표, 설정에는 접근할 수 없습니다. git push 배포는 계속됩니다. 이 환경을 복사해 만든 환경도 제한됩니다."),
                confirmLabel: t("제한"),
                run: () => apply(true),
              })
            : void apply(false)
        }
      />
    </label>
  );
}

function useRunner() {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const run = async (f: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await f();
      return true;
    } catch (err) {
      setError(err);
      return false;
    } finally {
      setBusy(false);
    }
  };
  return { busy, error, setError, run };
}

/* ── Workspace → 접근 그룹 ───────────────────────────────────────────── */

function CheckList<T extends { id: ID }>({
  label,
  items,
  selected,
  onChange,
  render,
  empty,
}: {
  label: string;
  items: T[];
  selected: ID[];
  onChange: (ids: ID[]) => void;
  render: (item: T) => ReactNode;
  empty: string;
}) {
  const [q, setQ] = useState("");
  const shown = items.filter((it) => !q || JSON.stringify(it).toLowerCase().includes(q.toLowerCase()));
  return (
    <fieldset className="min-w-0">
      <legend className="mb-2 text-xs font-semibold text-gray-600">
        {label} <span className="font-normal tabular-nums text-gray-500">· {t("{n}개 선택", { n: selected.length })}</span>
      </legend>
      <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("검색")} aria-label={t("{label} 검색", { label })} className="mb-2" />
      <div className="max-h-56 overflow-auto rounded-md border border-gray-200">
        {shown.length === 0 ? (
          <p className="p-3 text-sm text-gray-500">{empty}</p>
        ) : (
          shown.map((it) => (
            <label key={it.id} className="flex cursor-pointer items-center gap-2 border-b border-gray-100 px-3 py-2 text-sm last:border-b-0 hover:bg-secondaryBg">
              <input
                type="checkbox"
                className="accent-pink-500"
                checked={selected.includes(it.id)}
                onChange={() => onChange(selected.includes(it.id) ? selected.filter((v) => v !== it.id) : [...selected, it.id])}
              />
              {render(it)}
            </label>
          ))
        )}
      </div>
    </fieldset>
  );
}

function GroupDialog({ group, onClose, wsid }: { group: AccessGroup | "new" | null; onClose: () => void; wsid: ID }) {
  const qc = useQueryClient();
  const users = useQuery({ queryKey: [...qk.workspaceMembers(wsid), "people"], queryFn: () => api.workspacePeople(wsid), enabled: !!group });
  const projects = useQuery({ queryKey: [...qk.projects, wsid], queryFn: () => api.listProjects(wsid), enabled: !!group });
  const [form, setForm] = useState<AccessGroupInput>({ name: "", role: "member", user_ids: [], project_ids: [] });
  const { busy, error, setError, run } = useRunner();
  useEffect(() => {
    if (!group) return;
    setError(null);
    setForm(group === "new" ? { name: "", role: "member", user_ids: [], project_ids: [] } : { name: group.name, role: group.role, user_ids: group.user_ids, project_ids: group.project_ids });
  }, [group, setError]);
  // Workspace admins see every project anyway; groups matter for members and viewers.
  const people = (users.data ?? []).filter((u) => !u.disabled && u.role !== "owner" && u.role !== "admin");
  const save = () =>
    run(async () => {
      const input = { ...form, name: form.name.trim() };
      if (group === "new") await api.createAccessGroup(input, wsid);
      else if (group) await api.updateAccessGroup(group.id, input, wsid);
      await Promise.all([qc.invalidateQueries({ queryKey: qk.accessGroups }), qc.invalidateQueries({ queryKey: ["project-access-groups"] })]);
      onClose();
    });
  return (
    <Dialog
      open={!!group}
      onClose={() => !busy && onClose()}
      wide
      title={group === "new" ? t("새 접근 그룹") : t("접근 그룹 편집")}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={busy}>
            {t("취소")}
          </Button>
          <Button variant="primary" onClick={save} loading={busy} disabled={!form.name.trim()}>
            {group === "new" ? t("그룹 만들기") : t("저장")}
          </Button>
        </>
      }
    >
      <form
        className="space-y-5 pt-1"
        onSubmit={(e) => {
          e.preventDefault();
          if (form.name.trim()) void save();
        }}
      >
        <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_180px]">
          <Field label={t("이름")}>
            <Input autoFocus value={form.name} maxLength={100} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder={t("예: Backend team")} />
          </Field>
          <Field label={t("기본 역할")}>
            <Select value={form.role} onChange={(e) => setForm({ ...form, role: e.target.value as ProjectRole })}>
              <option value="admin">Admin</option>
              <option value="member">Member</option>
              <option value="viewer">Viewer</option>
            </Select>
          </Field>
        </div>
        <p className="text-sm text-gray-600">
          {tr("아래 사람들은 아래 프로젝트에서 {role} 역할을 받습니다. 직접 부여한 프로젝트 권한이 더 높으면 그쪽이 적용됩니다.", {
            role: <span className="font-medium text-foreground">{ROLE_LABEL[form.role]}</span>,
          })}
          {form.role === "admin" && ` ${t("그룹 Admin은 제한된 환경에 접근하지만 작업 공간 Admin이 되거나 가드레일을 우회하지는 않습니다.")}`}
        </p>
        <div className="grid gap-5 md:grid-cols-2">
          <CheckList
            label={t("사람")}
            items={people}
            selected={form.user_ids}
            onChange={(user_ids) => setForm({ ...form, user_ids })}
            empty={users.isPending ? t("불러오는 중…") : t("추가할 수 있는 Member/Viewer 사용자가 없습니다")}
            render={(u) => (
              <>
                <span className="min-w-0 truncate">{u.name || u.email}</span>
                <span className="ml-auto truncate text-xs text-gray-500">{u.email}</span>
              </>
            )}
          />
          <CheckList
            label={t("프로젝트")}
            items={projects.data ?? []}
            selected={form.project_ids}
            onChange={(project_ids) => setForm({ ...form, project_ids })}
            empty={projects.isPending ? t("불러오는 중…") : t("프로젝트가 없습니다")}
            render={(p) => <span className="min-w-0 truncate">{p.name}</span>}
          />
        </div>
        <ErrorBox error={error} />
        <button type="submit" className="hidden" aria-hidden tabIndex={-1} />
      </form>
    </Dialog>
  );
}

export function AccessGroupsSettings({ wsid }: { wsid: ID }) {
  const qc = useQueryClient();
  const controls = useQuery({ queryKey: qk.workspaceGroupSettings(wsid), queryFn: () => api.workspaceGroupSettings(wsid) });
  const groups = useQuery({ queryKey: qk.workspaceAccessGroups(wsid), queryFn: () => api.accessGroups(wsid) });
  const users = useQuery({ queryKey: [...qk.workspaceMembers(wsid), "people"], queryFn: () => api.workspacePeople(wsid) });
  const projects = useQuery({ queryKey: [...qk.projects, wsid], queryFn: () => api.listProjects(wsid) });
  const [editing, setEditing] = useState<AccessGroup | "new" | null>(null);
  const { busy, error, run } = useRunner();
  const { confirm, node } = useConfirm();
  const enabled = !!controls.data?.access_groups_enabled;
  const userName = useMemo(() => new Map((users.data ?? []).map((u) => [u.id, u.name || u.email])), [users.data]);
  const projectName = useMemo(() => new Map((projects.data ?? []).map((p) => [p.id, p.name])), [projects.data]);

  const refresh = () =>
    Promise.all([
      qc.invalidateQueries({ queryKey: qk.workspaceGroupSettings(wsid) }),
      qc.invalidateQueries({ queryKey: qk.accessPolicies }),
      qc.invalidateQueries({ queryKey: qk.accessGroups }),
      qc.invalidateQueries({ queryKey: ["project-access-groups"] }),
      qc.invalidateQueries({ queryKey: qk.audit }),
    ]);

  // Preview for the confirmation: in aven groups only add access on top of direct project permissions.
  const gained = useMemo(() => {
    const people = new Set<ID>();
    for (const g of groups.data ?? []) if (g.project_ids.length) g.user_ids.forEach((u) => people.add(u));
    return people.size;
  }, [groups.data]);

  const toggleEnforcement = (on: boolean) =>
    confirm({
      title: on ? t("접근 그룹 켜기") : t("접근 그룹 끄기"),
      message: on ? (
        <div className="space-y-2">
          <p>{t("그룹에 속한 사람은 그룹의 프로젝트를 그룹 역할로 볼 수 있게 됩니다. 지금 설정으로는 {count}명이 그룹을 통해 프로젝트 접근을 받습니다.", { count: gained })}</p>
          <p>{t("직접 부여한 프로젝트 권한과 작업 공간 Admin의 접근은 바뀌지 않고, 아무도 기존 접근을 잃지 않습니다. 작업 공간 범위 API 토큰은 만든 사람의 권한을 따릅니다.")}</p>
        </div>
      ) : (
        <p>{t("그룹이 준 프로젝트 접근이 모두 사라지고 직접 부여한 권한만 남습니다. 그룹 설정은 지워지지 않으며 나중에 다시 켤 수 있습니다.")}</p>
      ),
      confirmLabel: on ? t("켜기") : t("끄기"),
      danger: !on,
      run: () => run(async () => (await api.setWorkspaceGroupSettings(wsid, on), await refresh())),
    });

  return (
    <>
      {node}
      <GroupDialog wsid={wsid} group={editing} onClose={() => setEditing(null)} />
      <Card
        title={t("접근 그룹 적용")}
        description={t("그룹은 사람 집합과 프로젝트 집합에 프로젝트 역할 하나를 줍니다. 꺼져 있는 동안에도 그룹을 만들고 고칠 수 있으며, 켜야 접근에 반영됩니다.")}
        action={
          controls.data && (
            <div className="flex items-center gap-2">
              <span className="text-sm text-gray-600">{enabled ? t("켜짐") : t("꺼짐")}</span>
              <Toggle checked={enabled} onChange={toggleEnforcement} disabled={busy} label={t("접근 그룹 적용")} />
            </div>
          )
        }
      >
        <ErrorBox error={controls.error ?? error} />
      </Card>
      <Card
        title={t("그룹")}
        description={t("작업 공간 Admin은 그룹과 관계없이 모든 프로젝트에 접근합니다. 사람과 프로젝트는 여러 그룹에 속할 수 있고, 같은 프로젝트에 여러 경로로 접근하면 가장 높은 역할이 적용됩니다.")}
        action={
          <Button variant="primary" size="sm" onClick={() => setEditing("new")}>
            <Plus className="size-3.5" /> {t("새 그룹")}
          </Button>
        }
        flush={!!groups.data?.length}
      >
        <ErrorBox error={groups.error} className="mb-3" />
        {groups.isPending ? (
          <CenterSpinner />
        ) : !groups.data?.length ? (
          <EmptyState icon={<Users className="size-5" />} title={t("접근 그룹이 없습니다")}>
            {t("팀 단위로 프로젝트 접근을 관리하려면 그룹을 만드세요.")}
          </EmptyState>
        ) : (
          <ul className="divide-y divide-gray-200">
            {groups.data.map((g) => (
              <li key={g.id} className="flex items-center gap-3 px-4 py-3">
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className="truncate text-sm font-medium">{g.name}</span>
                    <span className={roleBadge}>{ROLE_LABEL[g.role]}</span>
                  </div>
                  <div className="mt-0.5 truncate text-xs tabular-nums text-gray-500" title={g.project_ids.map((id) => projectName.get(id) ?? id).join(", ")}>
                    {t("사람 {users}명 · 프로젝트 {projects}개", { users: g.user_ids.length, projects: g.project_ids.length })}
                    {g.project_ids.length > 0 && ` · ${g.project_ids.slice(0, 3).map((id) => projectName.get(id) ?? id).join(", ")}${g.project_ids.length > 3 ? " …" : ""}`}
                  </div>
                </div>
                <div className="hidden -space-x-2 sm:flex" aria-hidden>
                  {g.user_ids.slice(0, 4).map((id) => (
                    <Avatar key={id} name={userName.get(id) ?? id} size={24} />
                  ))}
                </div>
                <Menu
                  label={t("{name} 메뉴", { name: g.name })}
                  trigger={(p) => (
                    <IconButton label={t("{name} 메뉴", { name: g.name })} {...p}>
                      <MoreHorizontal className="size-4" />
                    </IconButton>
                  )}
                >
                  {(close) => (
                    <>
                      <MenuItem
                        icon={<Pencil className="size-3.5" />}
                        onSelect={() => {
                          close();
                          setEditing(g);
                        }}
                      >
                        {t("편집")}
                      </MenuItem>
                      <MenuItem
                        danger
                        icon={<Trash2 className="size-3.5" />}
                        onSelect={() => {
                          close();
                          confirm({
                            title: t("그룹 삭제"),
                            message: t("{name} 그룹이 준 프로젝트 접근이 사라집니다. 직접 부여한 프로젝트 권한은 그대로입니다.", { name: g.name }),
                            confirmLabel: t("그룹 삭제"),
                            danger: true,
                            run: () => run(async () => (await api.deleteAccessGroup(g.id, wsid), await refresh())),
                          });
                        }}
                      >
                        {t("그룹 삭제")}
                      </MenuItem>
                    </>
                  )}
                </Menu>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </>
  );
}

/* ── Workspace → 가드레일 ────────────────────────────────────────────── */

function PolicyRow({ title, scope, children, checked, onChange, disabled }: { title: string; scope: string; children: ReactNode; checked: boolean; onChange: (v: boolean) => void; disabled?: boolean }) {
  return (
    <li className="flex items-start gap-4 px-4 py-4">
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-sm font-medium">{title}</span>
          <span className={roleBadge}>{scope}</span>
        </div>
        <p className="mt-1 text-sm text-gray-600">{children}</p>
      </div>
      <Toggle checked={checked} onChange={onChange} disabled={disabled} label={title} />
    </li>
  );
}

export function GuardrailsSettings() {
  const qc = useQueryClient();
  const controls = useQuery({ queryKey: qk.accessControls, queryFn: api.accessControls });
  const [org, setOrg] = useState("");
  const { busy, error, run } = useRunner();
  const d = controls.data;
  const update = (input: Parameters<typeof api.updateAccessControls>[0]) =>
    run(async () => {
      await api.updateAccessControls(input);
      await Promise.all([
        qc.invalidateQueries({ queryKey: qk.accessControls }),
        qc.invalidateQueries({ queryKey: qk.accessPolicies }),
        qc.invalidateQueries({ queryKey: qk.audit }),
      ]);
    });
  const orgs = d?.approved_github_orgs ?? [];
  const addOrg = async () => {
    const v = org.trim().replace(/^https?:\/\/github\.com\//i, "").replace(/\/.*$/, "");
    if (!v) return;
    if (await update({ approved_github_orgs: [...orgs, v] })) setOrg("");
  };
  if (controls.isPending) return <CenterSpinner />;
  if (!d) return <ErrorBox error={controls.error} className="max-w-3xl" />;
  return (
    <>
      <Card title={t("가드레일")} description={t("모든 작업 공간에 적용되는 서버 정책입니다.")} flush>
        <ul className="divide-y divide-gray-200">
          <PolicyRow
            title={t("생성 도메인 제한")}
            scope={t("Admin 외")}
            checked={d.restrict_generated_domains}
            disabled={busy}
            onChange={(v) => void update({ restrict_generated_domains: v })}
          >
            {t("서버 관리자가 아니면 서비스 도메인(KEEL_BASE_DOMAIN 아래)을 만들 수 없습니다. 켜져 있는 동안 템플릿 서비스와 새 환경에는 누구에게도 도메인이 자동 생성되지 않습니다. 사용자 지정 도메인과 비공개 네트워크는 그대로입니다.")}
          </PolicyRow>
          <PolicyRow title={t("TCP 프록시 제한")} scope={t("Admin 외")} checked={d.restrict_tcp_proxy} disabled={busy} onChange={(v) => void update({ restrict_tcp_proxy: v })}>
            {t("서버 관리자가 아니면 공개 TCP 프록시를 만들 수 없습니다. 켜져 있는 동안 데이터베이스와 새 환경에 TCP 프록시가 자동으로 생기지 않습니다.")}
          </PolicyRow>
          <PolicyRow
            title={t("승인된 출처에서만 배포")}
            scope={t("Admin 포함 모두")}
            checked={d.restrict_deployment_sources}
            disabled={busy || (!d.restrict_deployment_sources && orgs.length === 0)}
            onChange={(v) => void update({ restrict_deployment_sources: v })}
          >
            {t("승인된 GitHub 조직의 저장소만 배포합니다. 다른 저장소(개인 저장소 포함), Docker 이미지, Function, Compose 이미지, 사용자 템플릿, CLI 업로드(aven up)는 막고 내장 데이터베이스(공식 이미지)는 허용합니다. 새 배포·새 서비스·소스 변경에 적용되며 기존 배포는 그대로입니다.")}
            {!d.restrict_deployment_sources && orgs.length === 0 && ` ${t("먼저 아래에서 조직을 하나 이상 추가하세요.")}`}
          </PolicyRow>
        </ul>
      </Card>
      <Card
        title={t("승인된 출처")}
        description={t("저장소 URL의 소유자(github.com/<조직>/…)를 대소문자 구분 없이 비교합니다. 정책이 켜져 있는 동안 마지막 조직은 지울 수 없습니다.")}
      >
        <form
          className="flex gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            void addOrg();
          }}
        >
          <Input value={org} onChange={(e) => setOrg(e.target.value)} placeholder={t("GitHub 조직 이름 (예: acme)")} aria-label={t("GitHub 조직")} className="flex-1" />
          <Button type="submit" variant="primary" disabled={!org.trim()} loading={busy}>
            <Plus className="size-4" /> {t("GitHub 조직 추가")}
          </Button>
        </form>
        {orgs.length > 0 && (
          <ul className="mt-4 divide-y divide-gray-200 rounded-lg border border-gray-200">
            {orgs.map((o) => {
              const last = d.restrict_deployment_sources && orgs.length === 1;
              return (
                <li key={o} className="flex items-center gap-3 px-4 py-2.5">
                  <span className="font-mono text-sm">github.com/{o}</span>
                  <IconButton
                    label={t("{name} 삭제", { name: o })}
                    className="ml-auto"
                    disabled={busy || last}
                    title={last ? t("배포 출처 제한을 먼저 끄세요") : t("{name} 삭제", { name: o })}
                    onClick={() => void update({ approved_github_orgs: orgs.filter((x) => x !== o) })}
                  >
                    <X className="size-4" />
                  </IconButton>
                </li>
              );
            })}
          </ul>
        )}
        <ErrorBox error={error} className="mt-3" />
      </Card>
    </>
  );
}

/* ── Project → Members → 그룹 ────────────────────────────────────────── */

export function ProjectAccessGroups({ pid }: { pid: ID }) {
  const qc = useQueryClient();
  const caps = useCaps(pid);
  const me = useMe().data;
  const data = useQuery({ queryKey: qk.projectAccessGroups(pid), queryFn: () => api.projectAccessGroups(pid) });
  const all = useQuery({ queryKey: qk.workspaceAccessGroups(caps.workspaceId ?? ""), queryFn: () => api.accessGroups(caps.workspaceId!), enabled: caps.workspaceAdmin && !!caps.workspaceId, retry: false });
  const users = useQuery({ queryKey: [...qk.workspaceMembers(caps.workspaceId ?? ""), "people"], queryFn: () => api.workspacePeople(caps.workspaceId!), enabled: caps.workspaceAdmin && !!caps.workspaceId, retry: false });
  const [picking, setPicking] = useState(false);
  const [sel, setSel] = useState<ID[]>([]);
  const { busy, error, run } = useRunner();
  const canManage = caps.workspaceAdmin && !me?.token;
  const userName = new Map((users.data ?? []).map((u) => [u.id, u]));
  const d = data.data;
  if (data.isPending) return <CenterSpinner />;
  if (!d) return <ErrorBox error={data.error} className="mt-10 max-w-3xl" />;
  if (!d.groups.length && !canManage) return null;
  // One row per person: the strongest group role, with every group that grants access.
  const people = new Map<ID, { role: ProjectRole; groups: string[] }>();
  const rank = { viewer: 1, member: 2, admin: 3 } as const;
  for (const g of d.group_grants) {
    const cur = people.get(g.user_id);
    if (!cur) people.set(g.user_id, { role: g.role, groups: [g.group_name] });
    else {
      cur.groups.push(g.group_name);
      if (rank[g.role] > rank[cur.role]) cur.role = g.role;
    }
  }
  const save = () =>
    run(async () => {
      await api.setProjectAccessGroups(pid, sel);
      await Promise.all([qc.invalidateQueries({ queryKey: qk.projectAccessGroups(pid) }), qc.invalidateQueries({ queryKey: qk.accessGroups })]);
      setPicking(false);
    });
  return (
    <section className="mt-10 max-w-3xl">
      <div className="flex items-end border-b border-gray-200">
        <div className="-mb-px flex items-center gap-2 border-b-2 border-pink-500 px-3 pb-3 text-sm font-medium text-foreground">
          {t("그룹")} <span className="rounded border border-gray-200 px-1.5 text-xs tabular-nums text-gray-600">{d.groups.length}</span>
        </div>
        <div className="ml-auto flex items-center gap-2 pb-2">
          {canManage && (
            <Button
              variant="primary"
              size="sm"
              onClick={() => {
                setSel(d.groups.map((g) => g.id));
                setPicking(true);
              }}
            >
              <Plus className="size-3.5" /> {t("그룹에 추가")}
            </Button>
          )}
        </div>
      </div>
      <p className="mt-4 text-sm text-gray-600">
        {d.enabled ? t("그룹에 속한 사람은 그룹 역할로 이 프로젝트에 접근합니다.") : t("접근 그룹이 꺼져 있어 아래 그룹은 아직 접근을 주지 않습니다(작업 공간 설정 → 접근 그룹).")}
      </p>
      <ErrorBox error={error} className="mt-3" />
      {d.groups.length > 0 && (
        <ul className="mt-4 divide-y divide-gray-200 rounded-lg border border-gray-200">
          {d.groups.map((g) => (
            <li key={g.id} className="flex items-center gap-3 px-4 py-3">
              <Users className="size-4 text-gray-500" />
              <span className="min-w-0 flex-1 truncate text-sm font-medium">{g.name}</span>
              <span className="text-xs tabular-nums text-gray-500">{t("사람 {n}명", { n: g.user_count })}</span>
              <span className={roleBadge}>{ROLE_LABEL[g.role]}</span>
            </li>
          ))}
        </ul>
      )}
      {people.size > 0 && (
        <>
          <h3 className="mt-6 text-sm font-semibold">{t("그룹으로 접근하는 사람")}</h3>
          <ul className={cx("mt-2 divide-y divide-gray-200 rounded-lg border border-gray-200", !d.enabled && "opacity-60")}>
            {[...people.entries()].map(([uid, p]) => {
              const u = userName.get(uid);
              return (
                <li key={uid} className="flex items-center gap-3 px-4 py-3">
                  <Avatar name={u?.name || u?.email || uid} />
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-sm font-medium">{u ? u.name || u.email : uid}</div>
                    <div className="truncate text-xs text-gray-500">{t("그룹: {groups}", { groups: p.groups.join(", ") })}</div>
                  </div>
                  <span className={roleBadge}>{ROLE_LABEL[p.role]}</span>
                </li>
              );
            })}
          </ul>
        </>
      )}
      <Dialog
        open={picking}
        onClose={() => !busy && setPicking(false)}
        title={t("그룹에 추가")}
        footer={
          <>
            <Button variant="outline" onClick={() => setPicking(false)} disabled={busy}>
              {t("취소")}
            </Button>
            <Button variant="primary" onClick={save} loading={busy}>
              {t("저장")}
            </Button>
          </>
        }
      >
        {all.isPending ? (
          <CenterSpinner />
        ) : !all.data?.length ? (
          <p>{t("아직 접근 그룹이 없습니다. 작업 공간 설정 → 접근 그룹에서 만드세요.")}</p>
        ) : (
          <CheckList
            label={t("그룹")}
            items={all.data}
            selected={sel}
            onChange={setSel}
            empty={t("일치하는 그룹이 없습니다")}
            render={(g) => (
              <>
                <span className="min-w-0 truncate">{g.name}</span>
                <span className={cx("ml-auto", roleBadge)}>{ROLE_LABEL[g.role]}</span>
              </>
            )}
          />
        )}
        <ErrorBox error={error} className="mt-3" />
      </Dialog>
    </section>
  );
}
