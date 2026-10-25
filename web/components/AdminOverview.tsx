"use client";

import Link from "next/link";
import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUpRight, Building2, CircleAlert, Crown, Folder, Mail, Pause, Play, RefreshCw, Server, Trash2, Users } from "lucide-react";
import { api, type AdminWorkspace, type OwnerHold, type OwnerHoldScope, type PreviousOwnerAction, type Project } from "@/lib/api";
import { qk, useMe, useProjects } from "@/lib/queries";
import { dateLocale, t, tr } from "@/lib/i18n";
import { Button, Card, Dialog, ErrorBox, Input, Select, Spinner, cx } from "./ui";

function useOverview() {
  return useQuery({ queryKey: qk.instanceOverview, queryFn: api.instanceOverview, refetchInterval: 30_000 });
}

function Refresh({ query }: { query: ReturnType<typeof useOverview> }) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-3 text-xs tabular-nums text-gray-500">
      <span>{query.data ? t("확인 시각 {time} · 30초마다 갱신", { time: new Date(query.data.generated_at).toLocaleTimeString(dateLocale()) }) : t("현황을 불러옵니다")}</span>
      <Button size="sm" loading={query.isFetching} onClick={() => void query.refetch()}><RefreshCw className="size-3.5" />{t("새로고침")}</Button>
    </div>
  );
}

export function AdminOverview() {
  const query = useOverview();
  const data = query.data;
  const projects = data?.workspaces.reduce((n, w) => n + w.projects, 0) ?? 0;
  const services = data?.workspaces.reduce((n, w) => n + w.services, 0) ?? 0;
  const unhealthy = data?.workspaces.reduce((n, w) => n + w.unhealthy_instances, 0) ?? 0;
  const online = data?.nodes.filter((n) => n.status !== "offline").length ?? 0;
  const offline = data?.nodes.filter((n) => n.status === "offline").length ?? 0;
  return (
    <div className="max-w-5xl space-y-6">
      <Card title={t("전체 현황")} description={t("계정, 작업 공간과 서버 상태를 한눈에 확인하세요.")}><Refresh query={query} /></Card>
      <ErrorBox error={query.error} />
      {!data && query.isPending ? <Spinner /> : data && <>
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
          {[
            { label: t("활성 사용자"), value: data.users.active, detail: t("전체 {total}명 · 비활성 {disabled}명", { total: data.users.total, disabled: data.users.disabled }), icon: Users, section: "users" },
            { label: t("대기 중인 초대"), value: data.pending_invites, detail: t("아직 수락하지 않은 유효한 초대"), icon: Mail, section: "users" },
            { label: t("작업 공간"), value: data.workspaces.length, detail: t("개인 {personal}개 · 팀 {team}개 · 기본 {basic}개", { personal: data.workspaces.filter((w) => w.personal).length, team: data.workspaces.filter((w) => !w.personal && !w.default).length, basic: data.workspaces.filter((w) => w.default).length }), icon: Building2, section: "workspaces" },
            { label: t("프로젝트|plural"), value: projects, detail: t("서비스 {count}개", { count: services }), icon: Folder, section: "workspaces" },
            { label: t("연결된 노드"), value: `${online} / ${data.nodes.length}`, detail: offline ? t("연결 끊김 {count}개", { count: offline }) : t("서버·에이전트 연결 기준"), icon: Server, section: "nodes", warn: offline > 0 },
            { label: t("오류 상태"), value: unhealthy, detail: t("환경별 서비스의 배포 실패·실행 오류"), icon: CircleAlert, section: "workspaces", warn: unhealthy > 0 },
          ].map(({ label, value, detail, icon: Icon, section, warn }) => (
            <Link key={label} href={`/admin/?section=${section}`} className="rounded-lg border border-gray-200 p-4 transition-colors hover:border-gray-400 focus-visible:outline-pink-500">
              <div className="flex items-center gap-2 text-sm text-gray-500"><Icon className="size-4" />{label}<ArrowUpRight className="ml-auto size-3.5" /></div>
              <div className={cx("mt-3 text-3xl font-semibold tabular-nums", warn && "text-warn")}>{value}</div>
              <p className="mt-2 text-xs tabular-nums text-gray-500">{detail}</p>
            </Link>
          ))}
        </div>
        <Card title={t("서버 연결 상태")} description={t("오프라인 노드는 서버 노드 메뉴에서 확인하세요.")}>
          {data.nodes.length === 0 ? <p className="text-sm text-gray-500">{t("등록된 노드가 없습니다.")}</p> : (
            <ul className="divide-y divide-gray-200" aria-label={t("서버 연결 상태")}>
              {data.nodes.map((n) => (
                <li key={n.id} className="flex flex-wrap items-center gap-3 py-3 text-sm">
                  <Server className="size-4 shrink-0 text-gray-500" />
                  <span className="min-w-0 flex-1 truncate">{n.name}{n.is_local && <span className="ml-2 text-xs text-gray-500">{t("현재 서버")}</span>}</span>
                  {!n.schedulable && <span className="text-xs text-gray-500">{t("새 배포 중지")}</span>}
                  <span className={cx("text-xs font-medium", n.status === "offline" ? "text-danger" : n.status === "draining" ? "text-warn" : "text-success")}>
                    {n.status === "offline" ? t("연결 끊김") : n.status === "draining" ? t("비우는 중") : t("연결됨")}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </>}
    </div>
  );
}

const workspaceType = (w: AdminWorkspace) => w.personal ? "personal" : w.default ? "default" : "team";
const typeName = { personal: t("개인"), team: t("팀"), default: t("기본") };

/** 서버 owner의 프로젝트 삭제: 이름을 입력해야 지울 수 있다. */
function DeleteProjectDialog({ project, onClose }: { project: Project | null; onClose: () => void }) {
  const qc = useQueryClient();
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const close = () => {
    if (busy) return;
    setTyped("");
    setError(null);
    onClose();
  };
  if (!project) return null;
  return (
    <Dialog
      open
      onClose={close}
      title={t("{name} 삭제", { name: project.name })}
      footer={
        <>
          <Button variant="outline" onClick={close} disabled={busy}>{t("취소")}</Button>
          <Button
            variant="danger"
            loading={busy}
            disabled={typed !== project.name}
            onClick={async () => {
              setBusy(true);
              try {
                await api.deleteProject(project.id);
                await Promise.all([qc.invalidateQueries({ queryKey: qk.projects }), qc.invalidateQueries({ queryKey: qk.instanceOverview }), qc.invalidateQueries({ queryKey: qk.me })]);
                setBusy(false);
                setTyped("");
                onClose();
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
        <p>{t("서버 owner 권한으로 이 프로젝트를 삭제합니다. 모든 환경의 서비스, 컨테이너, 볼륨, 배포 기록이 영구 삭제되며 되돌릴 수 없습니다.")}</p>
        <p>{tr("확인을 위해 프로젝트 이름 {name}을 입력하세요.", { name: <span className="font-mono text-foreground">{project.name}</span> })}</p>
        <Input mono value={typed} onChange={(e) => setTyped(e.target.value)} aria-label={t("프로젝트 이름 확인")} autoComplete="off" />
        <ErrorBox error={error} />
      </div>
    </Dialog>
  );
}

/** 서버 관리자의 작업 공간 삭제: 안의 프로젝트도 함께 지워지며 이름을 입력해야 지울 수 있다. */
function DeleteWorkspaceDialog({ workspace, onClose }: { workspace: AdminWorkspace | null; onClose: () => void }) {
  const qc = useQueryClient();
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const close = () => {
    if (busy) return;
    setTyped("");
    setError(null);
    onClose();
  };
  if (!workspace) return null;
  return (
    <Dialog
      open
      onClose={close}
      title={t("{name} 작업 공간 삭제", { name: workspace.name })}
      footer={
        <>
          <Button variant="outline" onClick={close} disabled={busy}>{t("취소")}</Button>
          <Button
            variant="danger"
            loading={busy}
            disabled={typed !== workspace.name}
            onClick={async () => {
              setBusy(true);
              try {
                await api.adminDeleteWorkspace(workspace.id, typed);
                await Promise.all([qc.invalidateQueries({ queryKey: qk.projects }), qc.invalidateQueries({ queryKey: qk.instanceOverview }), qc.invalidateQueries({ queryKey: qk.me })]);
                setBusy(false);
                setTyped("");
                onClose();
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
          {tr("서버 관리자 권한으로 이 작업 공간을 삭제합니다. 멤버, 초대, 접근 그룹, 작업 공간 토큰과 함께 {scope} 영구 삭제되며 되돌릴 수 없습니다.", {
            scope: workspace.projects > 0
              ? tr("프로젝트 {count}개(서비스 {services}개)의 모든 환경, 컨테이너, 볼륨, 배포 기록이", {
                  count: <span className="font-semibold tabular-nums text-foreground">{workspace.projects}</span>,
                  services: workspace.services,
                })
              : t("작업 공간 설정이"),
          })}
        </p>
        {workspace.personal && <p className="text-gray-500">{t("개인 작업 공간은 주인이 다음에 로그인하면 빈 작업 공간으로 다시 만들어집니다.")}</p>}
        <p>{tr("확인을 위해 작업 공간 이름 {name}을 입력하세요.", { name: <span className="font-mono text-foreground">{workspace.name}</span> })}</p>
        <Input mono value={typed} onChange={(e) => setTyped(e.target.value)} aria-label={t("작업 공간 이름 확인")} autoComplete="off" />
        <ErrorBox error={error} />
      </div>
    </Dialog>
  );
}

const ownerHoldsKey = ["owner", "holds"] as const;

/** 서버 owner의 정지: 실행 중인 서비스를 모두 멈추고 재개할 때까지 배포·재시작을 막는다. */
export function HoldDialog({ target, onClose, onChanged }: { target: { scope: OwnerHoldScope; id: string; name: string } | null; onClose: () => void; onChanged?: () => void }) {
  const qc = useQueryClient();
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const close = () => {
    if (busy) return;
    setReason("");
    setError(null);
    onClose();
  };
  if (!target) return null;
  const what = target.scope === "workspace" ? t("작업 공간") : t("프로젝트");
  return (
    <Dialog
      open
      onClose={close}
      title={t("{name} {what} 정지", { name: target.name, what })}
      footer={
        <>
          <Button variant="outline" onClick={close} disabled={busy}>{t("취소")}</Button>
          <Button
            variant="danger"
            loading={busy}
            onClick={async () => {
              setBusy(true);
              try {
                await api.createOwnerHold(target.scope, target.id, reason.trim());
                await Promise.all([qc.invalidateQueries({ queryKey: ownerHoldsKey }), qc.invalidateQueries({ queryKey: qk.instanceOverview })]);
                onChanged?.();
                setBusy(false);
                setReason("");
                onClose();
              } catch (e) {
                setError(e);
                setBusy(false);
              }
            }}
          >
            {t("정지")}
          </Button>
        </>
      }
    >
      <div className="space-y-3 pt-1">
        <p>
          {t("서버 owner 권한으로 이 {what}의 실행 중인 서비스를 모두 멈춥니다. 재개할 때까지 새 배포(GitHub push 포함), 재시작, scale, cron 즉시 실행이 막히며, 서버 owner만 재개할 수 있습니다. 데이터와 볼륨은 그대로 남습니다.", { what })}
        </p>
        <Input value={reason} onChange={(e) => setReason(e.target.value)} maxLength={200} placeholder={t("사유 (멤버에게 보입니다, 선택)")} aria-label={t("정지 사유")} />
        <ErrorBox error={error} />
      </div>
    </Dialog>
  );
}

/** 서버 owner의 작업 공간 소유권 강제 변경(퇴사자 정리 등). */
function ForceOwnerDialog({ workspace, onClose }: { workspace: AdminWorkspace | null; onClose: () => void }) {
  const qc = useQueryClient();
  const users = useQuery({ queryKey: qk.users, queryFn: api.users, enabled: !!workspace });
  const [userID, setUserID] = useState("");
  const [previous, setPrevious] = useState<PreviousOwnerAction>("demote");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const close = () => {
    if (busy) return;
    setUserID("");
    setError(null);
    onClose();
  };
  if (!workspace) return null;
  const candidates = (users.data ?? []).filter((u) => !u.disabled && !u.builtin);
  return (
    <Dialog
      open
      onClose={close}
      title={t("{name} 소유권 변경", { name: workspace.name })}
      footer={
        <>
          <Button variant="outline" onClick={close} disabled={busy}>{t("취소")}</Button>
          <Button
            variant="danger"
            loading={busy}
            disabled={!userID}
            onClick={async () => {
              setBusy(true);
              try {
                await api.forceWorkspaceOwner(workspace.id, userID, previous);
                await Promise.all([qc.invalidateQueries({ queryKey: qk.instanceOverview }), qc.invalidateQueries({ queryKey: qk.me })]);
                setBusy(false);
                setUserID("");
                onClose();
              } catch (e) {
                setError(e);
                setBusy(false);
              }
            }}
          >
            {t("owner로 지정")}
          </Button>
        </>
      }
    >
      <div className="space-y-3 pt-1">
        <p>
          {t("서버 owner 권한으로 작업 공간의 owner를 바꿉니다. 개인 작업 공간 owner 고정과 마지막 owner 유지 규칙을 우회하며, 감사 기록에 남습니다.")}
          {workspace.personal && ` ${t("개인 작업 공간은 팀 작업 공간으로 바뀌고, 기존 주인은 다음 로그인 때 새 개인 작업 공간을 받습니다.")}`}
        </p>
        <Select aria-label={t("새 owner")} value={userID} onChange={(e) => setUserID(e.target.value)}>
          <option value="">{t("새 owner 선택…")}</option>
          {candidates.map((u) => <option key={u.id} value={u.id}>{u.name ? `${u.name} · ${u.email}` : u.email}</option>)}
        </Select>
        <Select aria-label={t("기존 owner 처리")} value={previous} onChange={(e) => setPrevious(e.target.value as PreviousOwnerAction)}>
          <option value="demote">{t("기존 owner는 admin으로 낮추기")}</option>
          <option value="remove">{t("기존 owner는 작업 공간에서 내보내기")}</option>
          <option value="keep">{t("기존 owner는 그대로 두기")}</option>
        </Select>
        <ErrorBox error={error ?? users.error} />
      </div>
    </Dialog>
  );
}

/** 정지 상태 표시와 정지·재개 버튼(서버 owner 전용). */
export function HoldControl({ scope, id, name, hold, onHold, onChanged }: { scope: OwnerHoldScope; id: string; name: string; hold?: OwnerHold; onHold: () => void; onChanged?: () => void }) {
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  if (!hold) {
    return <button type="button" onClick={onHold} aria-label={t("{name} 정지", { name })} className="inline-flex items-center gap-1 text-warn hover:underline"><Pause className="size-3.5" />{t("정지")}</button>;
  }
  return (
    <span className="inline-flex items-center gap-2">
      <span className="rounded bg-danger-surface px-1.5 py-0.5 text-xs font-medium text-danger" title={hold.reason || undefined}>{t("정지됨")}</span>
      <button
        type="button"
        disabled={busy}
        aria-label={t("{name} 재개", { name })}
        className="inline-flex items-center gap-1 text-success hover:underline disabled:opacity-50"
        onClick={async () => {
          setBusy(true);
          setError(null);
          try {
            await api.releaseOwnerHold(scope, id);
            await Promise.all([qc.invalidateQueries({ queryKey: ownerHoldsKey }), qc.invalidateQueries({ queryKey: qk.instanceOverview })]);
            onChanged?.();
          } catch (e) {
            setError(e);
          } finally {
            setBusy(false);
          }
        }}
      >
        <Play className="size-3.5" />{t("재개")}
      </button>
      {error ? <span className="text-xs text-danger">{t("재개 실패")}</span> : null}
    </span>
  );
}

export function AdminWorkspaces() {
  const query = useOverview();
  const me = useMe().data;
  // 서버 owner는 모든 작업 공간·프로젝트에 owner/admin 권한이 있다. 서버 admin은 현황만 본다.
  const owner = me?.instance_role === "owner";
  const projects = useProjects();
  const [deleting, setDeleting] = useState<Project | null>(null);
  const [deletingWorkspace, setDeletingWorkspace] = useState<AdminWorkspace | null>(null);
  const [holding, setHolding] = useState<{ scope: OwnerHoldScope; id: string; name: string } | null>(null);
  const [owning, setOwning] = useState<AdminWorkspace | null>(null);
  const holds = useQuery({ queryKey: ownerHoldsKey, queryFn: api.ownerHolds, enabled: owner });
  const holdOf = (scope: OwnerHoldScope, id: string) => holds.data?.find((h) => h.scope === scope && h.target_id === id);
  // 작업 공간 삭제는 서버 관리자 기능이다(owner가 "workspaces" 기능을 막으면 숨긴다. 서버도 403).
  const canDeleteWorkspace = owner || (me?.admin_caps?.includes("workspaces") ?? false);
  const [search, setSearch] = useState("");
  const [type, setType] = useState("all");
  const needle = search.trim().toLocaleLowerCase();
  const rows = query.data?.workspaces.filter((w) => (type === "all" || workspaceType(w) === type)
    && `${w.name} ${w.id}`.toLocaleLowerCase().includes(needle)) ?? [];
  return (
    <div className="max-w-5xl space-y-6">
      <Card title={t("전체 작업 공간")} description={owner ? t("개인·팀 작업 공간의 현황을 확인하고, 서버 owner는 모든 프로젝트를 열고 관리할 수 있습니다.") : canDeleteWorkspace ? t("개인·팀 작업 공간의 멤버 수와 프로젝트 현황을 확인하고, 필요 없는 작업 공간을 삭제하세요.") : t("개인·팀 작업 공간의 멤버 수와 프로젝트 현황을 확인하세요.")}>
        <div className="space-y-4">
          <Refresh query={query} />
          <div className="grid gap-3 sm:grid-cols-[1fr_160px]">
            <Input type="search" aria-label={t("작업 공간 검색")} placeholder={t("이름 또는 ID 검색")} value={search} onChange={(e) => setSearch(e.target.value)} />
            <Select aria-label={t("작업 공간 종류")} value={type} onChange={(e) => setType(e.target.value)}>
              <option value="all">{t("전체 종류")}</option><option value="personal">{t("개인")}</option><option value="team">{t("팀")}</option><option value="default">{t("기본")}</option>
            </Select>
          </div>
          {query.data && <p className="text-xs tabular-nums text-gray-500" role="status">{t("{shown}개 표시 · 전체 {total}개", { shown: rows.length, total: query.data.workspaces.length })}</p>}
          <ErrorBox error={query.error} />
        </div>
      </Card>
      {!query.data && query.isPending ? <Spinner /> : query.data && (
        rows.length === 0 ? <p className="rounded-lg border border-gray-200 p-6 text-sm text-gray-500">{t("조건에 맞는 작업 공간이 없습니다.")}</p> :
          <ul className="divide-y divide-gray-200 rounded-lg border border-gray-200" aria-label={t("전체 작업 공간 목록")}>
            {rows.map((w) => (
              <li key={w.id} className="flex flex-wrap items-center gap-4 p-4">
                <div className="min-w-0 flex-1 basis-[230px]">
                  <div className="flex items-center gap-2"><h3 className="truncate text-sm font-semibold" title={w.name}>{w.name}</h3><span className="shrink-0 rounded bg-gray-100 px-1.5 py-0.5 text-xs text-gray-500">{typeName[workspaceType(w)]}</span></div>
                  <p className="mt-1 truncate font-mono text-xs text-gray-500" title={w.id}>{w.id}</p>
                  <p className="mt-2 text-xs tabular-nums text-gray-500">{t("멤버 {members}명 · 프로젝트 {projects}개 · 서비스 {services}개", { members: w.members, projects: w.projects, services: w.services })}</p>
                  {w.unhealthy_instances > 0 && <p className="mt-1 text-xs tabular-nums text-warn">{t("배포 실패·실행 오류 {count}건", { count: w.unhealthy_instances })}</p>}
                </div>
                <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-sm">
                  {me?.workspaces?.some((own) => own.id === w.id) && <Link href={`/projects/?ws=${encodeURIComponent(w.id)}`} aria-label={t("{name} 열기", { name: w.name })} className="text-gray-500 hover:text-foreground">{t("열기")}</Link>}
                  <Link href={`/admin/?section=resources&workspace=${encodeURIComponent(w.id)}`} aria-label={t("{name} 자원 제한", { name: w.name })} className="text-pink-600 hover:underline">{t("자원 제한")}</Link>
                  {owner && <HoldControl scope="workspace" id={w.id} name={w.name} hold={holdOf("workspace", w.id)} onHold={() => setHolding({ scope: "workspace", id: w.id, name: w.name })} />}
                  {owner && <button type="button" onClick={() => setOwning(w)} aria-label={t("{name} 소유권 변경", { name: w.name })} className="inline-flex items-center gap-1 text-gray-500 hover:text-foreground"><Crown className="size-3.5" />{t("소유권")}</button>}
                  {canDeleteWorkspace && !w.default && <button type="button" onClick={() => setDeletingWorkspace(w)} aria-label={t("{name} 작업 공간 삭제", { name: w.name })} className="inline-flex items-center gap-1 text-danger hover:underline"><Trash2 className="size-3.5" />{t("삭제")}</button>}
                </div>
                {owner && (projects.data ?? []).some((p) => p.workspace_id === w.id) && (
                  <ul className="w-full space-y-1 border-t border-gray-100 pt-3" aria-label={t("{name} 프로젝트", { name: w.name })}>
                    {(projects.data ?? []).filter((p) => p.workspace_id === w.id).map((p) => (
                      <li key={p.id} className="flex items-center gap-3 text-sm">
                        <Folder className="size-3.5 shrink-0 text-gray-400" />
                        <span className="min-w-0 flex-1 truncate" title={p.name}>{p.name}</span>
                        <Link href={`/?project=${encodeURIComponent(p.id)}`} aria-label={t("{name} 열기", { name: p.name })} className="text-gray-500 hover:text-foreground">{t("열기")}</Link>
                        <HoldControl scope="project" id={p.id} name={p.name} hold={holdOf("project", p.id)} onHold={() => setHolding({ scope: "project", id: p.id, name: p.name })} />
                        <button type="button" onClick={() => setDeleting(p)} aria-label={t("{name} 삭제", { name: p.name })} className="inline-flex items-center gap-1 text-danger hover:underline"><Trash2 className="size-3.5" />{t("삭제")}</button>
                      </li>
                    ))}
                  </ul>
                )}
              </li>
            ))}
          </ul>
      )}
      <DeleteProjectDialog project={deleting} onClose={() => setDeleting(null)} />
      <DeleteWorkspaceDialog workspace={deletingWorkspace} onClose={() => setDeletingWorkspace(null)} />
      <HoldDialog target={holding} onClose={() => setHolding(null)} />
      <ForceOwnerDialog workspace={owning} onClose={() => setOwning(null)} />
    </div>
  );
}
