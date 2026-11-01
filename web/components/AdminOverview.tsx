"use client";

import Link from "next/link";
import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUpRight, Building2, CheckCircle2, CircleAlert, Crown, Folder, Mail, Pause, Play, RefreshCw, Server, Trash2, Users } from "lucide-react";
import { api, type AdminWorkspace, type OwnerHold, type OwnerHoldScope, type PreviousOwnerAction, type Project, type SelfServiceStatus } from "@/lib/api";
import { qk, useMe, useNodes, useProjects, useSystem } from "@/lib/queries";
import { canvasHref } from "@/lib/nav";
import { fmtBytes, fmtRelative } from "@/lib/format";
import { diskLevel } from "@/lib/metrics";
import { HealthItem, NodeHealthRow, ProblemEventList, useRecentProblems, type HealthState } from "./nodes/NodeHealth";
import { useSetupStatus } from "./SetupChecklist";
import { dateLocale, t, tr } from "@/lib/i18n";
import { Button, Card, Dialog, ErrorBox, Input, Select, Spinner, cx, errorMessage } from "./ui";

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
  const me = useMe().data;
  const owner = me?.instance_owner === true;
  const data = query.data;
  const sys = useSystem();
  const nodes = useNodes();
  const setup = useSetupStatus(owner);
  const self = useQuery({ queryKey: qk.selfService, queryFn: api.selfService, enabled: owner, staleTime: 60_000 });
  const problems = useRecentProblems(!!data);
  const projects = data?.workspaces.reduce((n, w) => n + w.projects, 0) ?? 0;
  const services = data?.workspaces.reduce((n, w) => n + w.services, 0) ?? 0;
  const unhealthy = data?.workspaces.reduce((n, w) => n + w.unhealthy_instances, 0) ?? 0;
  const online = data?.nodes.filter((n) => n.status !== "offline").length ?? 0;
  const offline = data?.nodes.filter((n) => n.status === "offline").length ?? 0;
  const s = sys.data;
  const nodeList = [...(nodes.data ?? [])].sort((a, b) => Number(b.is_local) - Number(a.is_local) || a.name.localeCompare(b.name));
  const offlineNodes = nodeList.filter((n) => n.status === "offline");
  const lowDisk = nodeList.filter((n) => n.status !== "offline" && diskLevel(n.disk_free_bytes) !== "ok");
  const update = self.data?.update;

  // Things that need the operator, most severe first. Each links to where it is fixed.
  const attention: { key: string; tone: "bad" | "warn"; text: string; href: string }[] = [];
  if (s && s.docker !== "ok") attention.push({ key: "docker", tone: "bad", text: t("Docker가 응답하지 않습니다 ({status}). 배포와 재시작이 실패합니다.", { status: s.docker }), href: "/admin/?section=system" });
  if (s && s.proxy !== "ok") attention.push({ key: "proxy", tone: "bad", text: t("프록시(aven-proxy)가 응답하지 않습니다 ({status}). 도메인 접속이 끊길 수 있습니다.", { status: s.proxy }), href: "/admin/?section=system" });
  for (const n of offlineNodes) attention.push({ key: `off-${n.id}`, tone: "bad", text: t("노드 {name}의 연결이 끊겼습니다 · 마지막 응답 {time}", { name: n.name, time: fmtRelative(n.last_seen_at) }), href: "/admin/?section=nodes" });
  for (const n of lowDisk) attention.push({ key: `disk-${n.id}`, tone: diskLevel(n.disk_free_bytes) === "danger" ? "bad" : "warn", text: t("노드 {name}의 디스크 여유가 {free}뿐입니다.", { name: n.name, free: fmtBytes(n.disk_free_bytes) }), href: "/admin/?section=nodes" });
  if (unhealthy > 0) attention.push({ key: "unhealthy", tone: "warn", text: t("배포 실패·실행 오류 상태인 서비스가 {count}개 있습니다.", { count: unhealthy }), href: "/admin/?section=workspaces" });
  if (owner && setup.data && !setup.data.complete) attention.push({ key: "setup", tone: "warn", text: t("초기 설정 필수 항목이 {count}개 남았습니다.", { count: setup.data.required - setup.data.required_done }), href: "/admin/?section=setup" });
  if (owner && update?.error) attention.push({ key: "update-error", tone: "warn", text: t("업데이트를 확인하지 못했습니다: {error}", { error: update.error }), href: "/admin/?section=setup" });

  const dockerState: HealthState = !s ? "unknown" : s.docker === "ok" ? "ok" : "bad";
  const proxyState: HealthState = !s ? "unknown" : s.proxy === "ok" ? "ok" : "bad";
  const localDisk = s ? diskLevel(s.disk_free_bytes) : "ok";
  return (
    <div className="max-w-3xl space-y-6">
      <Card title={t("전체 현황")} description={t("계정, 작업 공간과 서버 상태를 한눈에 확인하세요.")}><Refresh query={query} /></Card>
      <ErrorBox error={query.error} />

      <Card title={t("서버 상태")} description={s ? t("aven-server {version} · {node}", { version: s.version, node: s.node }) : undefined}>
        {sys.error ? <ErrorBox error={sys.error} /> : (
          <ul className="grid gap-x-6 gap-y-4 sm:grid-cols-2 lg:grid-cols-4" aria-label={t("서버 상태")}>
            <HealthItem label="Docker" state={dockerState} detail={!s ? t("확인 중") : s.docker === "ok" ? t("정상") : t("응답 없음 ({status})", { status: s.docker })} href="/admin/?section=system" />
            <HealthItem label={t("프록시")} state={proxyState} detail={!s ? t("확인 중") : s.proxy === "ok" ? t("정상") : t("응답 없음 ({status})", { status: s.proxy })} href="/admin/?section=system" />
            <HealthItem
              label={t("노드")}
              state={!nodes.data ? "unknown" : offlineNodes.length ? "bad" : "ok"}
              detail={!nodes.data ? t("확인 중") : offlineNodes.length ? t("{online}/{total} 연결됨 · 끊김 {offline}개", { online: nodeList.length - offlineNodes.length, total: nodeList.length, offline: offlineNodes.length }) : t("{total}개 모두 연결됨", { total: nodeList.length })}
              href="/admin/?section=nodes"
            />
            <HealthItem
              label={t("디스크 여유")}
              state={!s ? "unknown" : localDisk === "danger" ? "bad" : localDisk === "warn" ? "warn" : "ok"}
              detail={s ? t("이 서버 {free} 남음", { free: fmtBytes(s.disk_free_bytes) }) : t("확인 중")}
              href="/admin/?section=system"
            />
          </ul>
        )}
      </Card>

      {data && (
        <Card
          title={t("확인이 필요한 항목")}
          flush
          footerNote={problems.truncated ? t("최근 오류는 프로젝트 {count}개까지만 확인합니다.", { count: 20 }) : t("최근 오류는 지난 24시간, 내가 열 수 있는 프로젝트 기준입니다.")}
        >
          {attention.length === 0 && problems.events.length === 0 ? (
            <p className="flex items-center gap-2 px-5 py-4 text-sm text-gray-600">
              <CheckCircle2 className="size-4 text-success" aria-hidden />
              {problems.loading ? t("최근 오류를 확인하는 중입니다.") : t("지금 확인할 문제가 없습니다.")}
            </p>
          ) : (
            <>
              {attention.length > 0 && (
                <ul className="divide-y divide-gray-100" aria-label={t("확인이 필요한 항목")}>
                  {attention.map((a) => (
                    <li key={a.key}>
                      <Link href={a.href} className="flex items-center gap-3 px-5 py-2.5 text-sm hover:bg-gray-50 focus-visible:outline-2 focus-visible:outline-pink-500">
                        <CircleAlert className={cx("size-4 shrink-0", a.tone === "bad" ? "text-danger" : "text-warn")} aria-hidden />
                        <span className="min-w-0 flex-1">{a.text}</span>
                        <ArrowUpRight className="size-3.5 shrink-0 text-gray-400" aria-hidden />
                      </Link>
                    </li>
                  ))}
                </ul>
              )}
              {problems.events.length > 0 && (
                <div className={cx(attention.length > 0 && "border-t border-gray-200")}>
                  <div className="px-5 pt-3 text-xs font-semibold text-gray-600">
                    {t("최근 오류 {count}건", { count: problems.total })}
                  </div>
                  <ProblemEventList events={problems.events} />
                </div>
              )}
            </>
          )}
        </Card>
      )}

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
        <Card
          title={t("노드 사용량")}
          description={t("최근 1시간 지표의 마지막 값입니다. 75% 이상은 노란색, 90% 이상은 빨간색으로 표시합니다.")}
          action={<Link href="/admin/?section=nodes" className="text-sm text-pink-600 hover:underline">{t("서버 노드")}</Link>}
          flush
        >
          {nodes.error ? <ErrorBox error={nodes.error} className="m-4" /> : !nodes.data ? <div className="p-5"><Spinner /></div> : nodeList.length === 0 ? <p className="px-5 py-4 text-sm text-gray-500">{t("등록된 노드가 없습니다.")}</p> : (
            <ul className="divide-y divide-gray-100" aria-label={t("노드 사용량")}>
              {nodeList.map((n) => <NodeHealthRow key={n.id} n={n} />)}
            </ul>
          )}
        </Card>
      </>}
      {owner && self.data && <SelfUpdateSummary data={self.data} />}
    </div>
  );
}

/** Owner-only: running version and the result of the last update check (Aven on Aven). */
function SelfUpdateSummary({ data }: { data: SelfServiceStatus }) {
  const u = data.update;
  const href = data.enabled && data.service_id
    ? canvasHref({ svc: data.service_id }, { project: data.project_id, env: data.environment_id, ws: data.workspace_id })
    : "/admin/?section=setup";
  const checked = u?.checked_at && !u.checked_at.startsWith("0001") ? fmtRelative(u.checked_at) : null;
  return (
    <Card title={t("Aven 업데이트")} description={t("지금 버전 {version}", { version: data.server.version || t("개발 빌드") })}>
      <div className="flex flex-wrap items-center gap-3 text-sm">
        {!data.enabled ? (
          <span className="text-gray-600">{t("캔버스에서 Aven을 관리하면 새 버전을 확인하고 대시보드에서 업데이트할 수 있습니다.")}</span>
        ) : !u ? (
          <span className="text-gray-600">{t("아직 업데이트를 확인하지 않았습니다")}</span>
        ) : u.available ? (
          <span className="flex items-center gap-2 font-medium">
            <span className="size-2 rounded-full bg-pink-500" aria-hidden />
            {u.behind ? t("새 버전이 있습니다 · 커밋 {count}개", { count: u.behind }) : t("새 버전이 있습니다")}
          </span>
        ) : u.error ? (
          <span className="text-danger">{t("업데이트를 확인하지 못했습니다: {error}", { error: u.error })}</span>
        ) : (
          <span className="flex items-center gap-2"><CheckCircle2 className="size-4 text-success" aria-hidden />{t("최신 버전입니다")}</span>
        )}
        {checked && <span className="text-xs text-gray-500">{t("{when} 확인", { when: checked })}</span>}
        <Link href={href} className="ml-auto text-sm text-pink-600 hover:underline">
          {data.enabled ? t("캔버스에서 열기") : t("초기 설정 열기")}
        </Link>
      </div>
    </Card>
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
      {error ? <span className="text-xs text-danger" role="alert" title={errorMessage(error)}>{t("재개 실패: {error}", { error: errorMessage(error) })}</span> : null}
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
