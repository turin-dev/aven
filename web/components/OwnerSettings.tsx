"use client";

import { useEffect, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Crown, Download, HardDriveDownload, Lock, LogOut, Megaphone, Power, RefreshCw, Trash2, Wrench } from "lucide-react";
import { api, type AdminCapID, type AnnouncementLevel, type OwnerOverview } from "@/lib/api";
import { qk, useMe } from "@/lib/queries";
import { fmtBytes, fmtRelative, fmtTime } from "@/lib/format";
import { toast } from "@/lib/uiState";
import { Button, Card, CenterSpinner, Dialog, ErrorBox, Input, Segmented, Select, Textarea, Toggle, cx, useConfirm } from "./ui";
import { t, tr } from "@/lib/i18n";

// 서버 owner 전용 관리(docs/permissions.md). 서버 관리자(admin)와 작업 공간 owner/admin에게는 보이지 않고, 서버도 세션 owner만 허용한다.

function useOwnerOverview() {
  return useQuery({ queryKey: qk.ownerOverview, queryFn: api.ownerOverview, refetchInterval: 30_000 });
}

/** owner 작업을 실행하고 관련 쿼리를 새로 고친다. */
function useOwnerRun() {
  const qc = useQueryClient();
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const run = async (key: string, f: () => Promise<unknown>, done?: string) => {
    setBusy(key);
    setError(null);
    try {
      await f();
      if (done) toast(done, "success");
      await Promise.all([
        qc.invalidateQueries({ queryKey: ["owner"] }),
        qc.invalidateQueries({ queryKey: qk.instanceNotice }),
        qc.invalidateQueries({ queryKey: qk.audit }),
        qc.invalidateQueries({ queryKey: qk.users }),
        qc.invalidateQueries({ queryKey: qk.me }),
      ]);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(null);
    }
  };
  return { busy, error, run };
}

function Row({ label, children }: { label: ReactNode; children: ReactNode }) {
  return (
    <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1 py-2 text-sm">
      <dt className="w-48 shrink-0 text-gray-500">{label}</dt>
      <dd className="min-w-0 flex-1 break-words">{children}</dd>
    </div>
  );
}

function Flag({ on, yes = t("설정됨"), no = t("없음") }: { on: boolean; yes?: string; no?: string }) {
  return <span className={on ? "text-success" : "text-gray-500"}>{on ? yes : no}</span>;
}

function fmtUptime(sec: number) {
  const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
  return d ? t("{days}일 {hours}시간", { days: d, hours: h }) : h ? t("{hours}시간 {minutes}분", { hours: h, minutes: m }) : t("{minutes}분", { minutes: m });
}

/** 이름(문구)을 정확히 입력해야 실행되는 위험 작업 확인 창. */
function TypeToConfirm({ open, title, phrase, children, label, busy, onClose, onConfirm }: {
  open: boolean; title: string; phrase: string; children: ReactNode; label: string; busy: boolean; onClose: () => void; onConfirm: () => void;
}) {
  const [typed, setTyped] = useState("");
  useEffect(() => { if (!open) setTyped(""); }, [open]);
  return (
    <Dialog
      open={open}
      onClose={() => !busy && onClose()}
      title={title}
      footer={<>
        <Button variant="outline" onClick={onClose} disabled={busy}>{t("취소")}</Button>
        <Button variant="danger" loading={busy} disabled={typed !== phrase} onClick={onConfirm}>{label}</Button>
      </>}
    >
      <div className="space-y-3 pt-1">
        {children}
        <p>{tr("확인을 위해 {phrase}을(를) 입력하세요.", { phrase: <span className="font-mono text-foreground">{phrase}</span> })}</p>
        <Input mono value={typed} onChange={(e) => setTyped(e.target.value)} aria-label={t("확인 문구")} autoComplete="off" autoFocus />
      </div>
    </Dialog>
  );
}

/* ── 점검·공지·서버 제어 ─────────────────────────────────────────────── */

export function OwnerControl() {
  const query = useOwnerOverview();
  const data = query.data;
  return (
    <div className="space-y-8">
      <Card
        title={<span className="flex items-center gap-2"><Crown className="size-4 text-pink-500" />{t("서버 owner 전용")}</span>}
        description={t("서버 owner만 볼 수 있는 메뉴입니다. 서버 관리자와 작업 공간 owner·admin에게는 보이지 않으며, API 토큰으로는 호출할 수 없습니다.")}
        action={<Button size="sm" loading={query.isFetching} onClick={() => void query.refetch()}><RefreshCw className="size-3.5" />{t("새로고침")}</Button>}
      />
      <ErrorBox error={query.error} />
      {!data ? query.isPending && <CenterSpinner /> : <>
        <MaintenanceCard data={data} />
        <AnnouncementCard data={data} />
        <ServerCard data={data} />
        <LockdownCard />
      </>}
    </div>
  );
}

function MaintenanceCard({ data }: { data: OwnerOverview }) {
  const m = data.maintenance;
  const [enabled, setEnabled] = useState(m.enabled);
  const [message, setMessage] = useState(m.message);
  const [blockLogins, setBlockLogins] = useState(m.block_logins);
  const [pauseWebhooks, setPauseWebhooks] = useState(m.pause_webhooks);
  const { busy, error, run } = useOwnerRun();
  useEffect(() => {
    setEnabled(m.enabled); setMessage(m.message); setBlockLogins(m.block_logins); setPauseWebhooks(m.pause_webhooks);
  }, [m.enabled, m.message, m.block_logins, m.pause_webhooks]);
  const dirty = enabled !== m.enabled || message !== m.message || blockLogins !== m.block_logins || pauseWebhooks !== m.pause_webhooks;
  return (
    <Card
      title={<span className="flex items-center gap-2"><Wrench className="size-4" />{t("점검 모드")}</span>}
      description={t("켜면 서버 owner가 아닌 모든 사용자의 변경 요청(배포, 설정 변경, 프로젝트 생성 등)이 503으로 거부됩니다. 조회와 실행 중인 서비스는 그대로입니다.")}
      footer={<Button variant="primary" size="sm" loading={busy === "m"} disabled={!dirty}
        onClick={() => run("m", () => api.setMaintenance({ enabled, message, block_logins: blockLogins, pause_webhooks: pauseWebhooks }), enabled ? t("점검 모드 설정을 저장했습니다") : t("점검 모드를 껐습니다"))}>{t("저장")}</Button>}
      footerNote={m.enabled && m.started_at ? t("{time}부터 점검 중 · {by}", { time: fmtTime(m.started_at), by: m.started_by }) : undefined}
    >
      <div className="space-y-4">
        <label className="flex items-center justify-between gap-4 text-sm"><span className={cx("font-medium", enabled && "text-warn")}>{enabled ? t("점검 모드 켜짐") : t("점검 모드 꺼짐")}</span><Toggle checked={enabled} onChange={setEnabled} label={t("점검 모드")} /></label>
        <Textarea rows={2} value={message} onChange={(e) => setMessage(e.target.value)} placeholder={t("서버 점검 중입니다. 잠시 후 다시 시도해 주세요.")} aria-label={t("점검 안내 문구")} maxLength={500} />
        <label className="flex items-center justify-between gap-4 text-sm"><span>{t("owner가 아닌 계정의 새 로그인·초대 수락 막기")}</span><Toggle checked={blockLogins} onChange={setBlockLogins} label={t("로그인 차단")} /></label>
        <label className="flex items-center justify-between gap-4 text-sm"><span>{t("GitHub 웹훅 자동 배포 멈추기")}<span className="block text-xs text-gray-500">{t("멈춘 동안의 push는 다시 오지 않으니 점검 후 직접 배포하세요.")}</span></span><Toggle checked={pauseWebhooks} onChange={setPauseWebhooks} label={t("웹훅 배포 중지")} /></label>
        <ErrorBox error={error} />
      </div>
    </Card>
  );
}

const LEVELS: { value: AnnouncementLevel; label: string }[] = [{ value: "info", label: t("안내") }, { value: "warning", label: t("주의") }, { value: "critical", label: t("긴급") }];

function AnnouncementCard({ data }: { data: OwnerOverview }) {
  const a = data.announcement;
  const [enabled, setEnabled] = useState(a.enabled);
  const [message, setMessage] = useState(a.message);
  const [level, setLevel] = useState<AnnouncementLevel>(a.level);
  const { busy, error, run } = useOwnerRun();
  useEffect(() => { setEnabled(a.enabled); setMessage(a.message); setLevel(a.level); }, [a.enabled, a.message, a.level]);
  const dirty = enabled !== a.enabled || message !== a.message || level !== a.level;
  return (
    <Card
      title={<span className="flex items-center gap-2"><Megaphone className="size-4" />{t("전체 공지")}</span>}
      description={t("로그인한 모든 사용자의 대시보드 위쪽에 배너로 보입니다.")}
      footer={<Button variant="primary" size="sm" loading={busy === "a"} disabled={!dirty || (enabled && !message.trim())}
        onClick={() => run("a", () => api.setAnnouncement({ enabled, message, level }), enabled ? t("공지를 게시했습니다") : t("공지를 내렸습니다"))}>{t("저장")}</Button>}
      footerNote={a.updated_at ? t("마지막 변경 {time}", { time: fmtRelative(a.updated_at) }) : undefined}
    >
      <div className="space-y-4">
        <label className="flex items-center justify-between gap-4 text-sm"><span className="font-medium">{t("공지 게시")}</span><Toggle checked={enabled} onChange={setEnabled} label={t("공지 게시")} /></label>
        <Segmented label={t("공지 수준")} value={level} options={LEVELS} onChange={setLevel} />
        <Textarea rows={2} value={message} onChange={(e) => setMessage(e.target.value)} placeholder={t("예: 오늘 23시부터 10분간 aven 서버를 재시작합니다.")} aria-label={t("공지 내용")} maxLength={500} />
        <ErrorBox error={error} />
      </div>
    </Card>
  );
}

function ServerCard({ data }: { data: OwnerOverview }) {
  const s = data.server;
  const { busy, error, run } = useOwnerRun();
  const [restarting, setRestarting] = useState(false);
  return (
    <Card
      title={<span className="flex items-center gap-2"><Power className="size-4" />aven-server</span>}
      description={t("실행 정보와 주요 설정입니다. 비밀값은 설정 여부만 표시합니다.")}
      action={<Button size="sm" variant="outline" disabled={!s.systemd} onClick={() => setRestarting(true)}><Power className="size-3.5" />{t("재시작")}</Button>}
    >
      <dl className="divide-y divide-gray-200 rounded-lg border border-gray-200 px-4 tabular-nums">
        <Row label={t("버전")}>{s.version || t("개발 빌드")} <span className="text-gray-500">· {s.go_version}</span></Row>
        <Row label={t("노드")}>{s.node} <span className="text-gray-500">· PID {s.pid}</span></Row>
        <Row label={t("가동 시간")}>{fmtUptime(s.uptime_seconds)} <span className="text-gray-500">· {t("{time} 시작", { time: fmtTime(s.started_at) })}</span></Row>
        <Row label={t("실행 방식")}>{s.systemd ? t("systemd 서비스(재시작 가능)") : t("직접 실행(여기서 재시작할 수 없음)")}</Row>
        <Row label={t("데이터")}>{s.data_dir || "-"} <span className="text-gray-500">· keel.db {fmtBytes(s.db_bytes)}</span></Row>
        <Row label={t("공개 URL")}>{s.public_url || <span className="text-gray-500">{t("없음")}</span>}</Row>
        <Row label={t("계정·연결")}>{t("사용자 {users}명 · 활성 세션 {sessions}개 · API 토큰 {tokens}개", { users: data.counts.users, sessions: data.counts.sessions, tokens: data.counts.api_tokens })}</Row>
        <Row label="KEEL_ADMIN_PASSWORD"><Flag on={s.admin_password_set} /></Row>
        <Row label="KEEL_API_TOKEN"><Flag on={s.legacy_api_token_set} yes={t("설정됨(모든 권한)")} /></Row>
        <Row label="GitHub"><span>OAuth <Flag on={s.github_oauth_configured} /> · {t("웹훅 비밀")} <Flag on={s.github_webhook_secret_set} /></span></Row>
        <Row label="SMTP"><Flag on={s.smtp_configured} /></Row>
        <Row label={t("보안")}>{tr("Secure 쿠키 {cookie} · 옛 agent 연결 {agents} · 신뢰 프록시 {proxies}개", {
          cookie: <Flag on={s.secure_cookie} yes={t("켬")} no={t("끔")} />,
          agents: <Flag on={!s.legacy_agents_disabled} yes={t("허용")} no={t("차단")} />,
          proxies: s.trusted_proxies,
        })}</Row>
      </dl>
      <ErrorBox error={error} className="mt-3" />
      <TypeToConfirm
        open={restarting}
        title={t("aven-server 재시작")}
        phrase={t("재시작")}
        label={t("재시작")}
        busy={busy === "restart"}
        onClose={() => setRestarting(false)}
        onConfirm={() => run("restart", async () => {
          await api.restartServer();
          setRestarting(false);
        }, t("재시작을 요청했습니다. 몇 초 뒤 자동으로 다시 연결됩니다"))}
      >
        <p>{t("aven-server 프로세스만 정상 종료 후 systemd가 다시 띄웁니다. 앱 컨테이너와 프록시는 계속 동작하지만, 몇 초 동안 대시보드·API·진행 중인 로그 스트림이 끊깁니다.")}</p>
      </TypeToConfirm>
    </Card>
  );
}

function LockdownCard() {
  const [open, setOpen] = useState(false);
  const [message, setMessage] = useState("");
  const { busy, error, run } = useOwnerRun();
  return (
    <Card danger title={<span className="flex items-center gap-2"><Lock className="size-4" />{t("비상 잠금")}</span>}
      description={t("계정 탈취·침해가 의심될 때 쓰세요. 점검 모드(로그인 차단, 웹훅 배포 중지)를 켜고 owner가 아닌 모든 사용자의 세션과 API 토큰을 즉시 끊습니다.")}
      action={<Button variant="danger" size="sm" onClick={() => setOpen(true)}>{t("비상 잠금")}</Button>}>
      <ErrorBox error={error} />
      <TypeToConfirm
        open={open}
        title={t("서버 비상 잠금")}
        phrase={t("잠금")}
        label={t("지금 잠그기")}
        busy={busy === "lock"}
        onClose={() => setOpen(false)}
        onConfirm={() => run("lock", async () => {
          const r = await api.lockdown(message);
          setOpen(false);
          toast(t("잠금 완료: 세션 {sessions}개, API 토큰 {tokens}개를 끊었습니다", { sessions: r.sessions_revoked, tokens: r.tokens_revoked }), "warning");
        })}
      >
        <p>{t("owner가 아닌 사용자는 다시 로그인하고 API 토큰을 새로 만들어야 합니다. 폐기한 토큰은 되살릴 수 없습니다. 점검 모드는 나중에 직접 끄세요.")}</p>
        <Input value={message} onChange={(e) => setMessage(e.target.value)} placeholder={t("사용자에게 보일 안내(선택)")} aria-label={t("잠금 안내 문구")} maxLength={500} />
      </TypeToConfirm>
    </Card>
  );
}

/* ── owner 계정과 서버 관리자 권한 범위 ─────────────────────────────── */

const SOURCE_LABEL = { role: t("서버 역할"), builtin: t("기본 계정"), env: "KEEL_SUPERADMINS" };

export function OwnerAccess() {
  const query = useOwnerOverview();
  const data = query.data;
  return (
    <div className="space-y-8">
      <ErrorBox error={query.error} />
      {!data ? query.isPending && <CenterSpinner /> : <>
        <OwnersCard data={data} />
        <AdminCapsCard data={data} />
      </>}
    </div>
  );
}

function OwnersCard({ data }: { data: OwnerOverview }) {
  const me = useMe().data;
  const users = useQuery({ queryKey: qk.users, queryFn: api.users });
  const [target, setTarget] = useState("");
  const [stayOwner, setStayOwner] = useState(true);
  const [confirming, setConfirming] = useState(false);
  const { busy, error, run } = useOwnerRun();
  const { confirm, node } = useConfirm();
  const ownerIDs = new Set(data.owners.map((o) => o.id));
  const candidates = (users.data ?? []).filter((u) => !u.disabled && !ownerIDs.has(u.id));
  const picked = candidates.find((u) => u.id === target);
  // 기본 계정·KEEL_SUPERADMINS는 역할과 상관없이 owner라 넘겨도 owner로 남는다.
  const canStepDown = !!me && me.role === "owner" && !me.builtin;
  return (
    <>
      {node}
      <Card title={<span className="flex items-center gap-2"><Crown className="size-4" />{t("서버 owner")}</span>}
        description={t("owner는 서버의 모든 작업 공간·프로젝트에 접근하고, 이 메뉴를 쓸 수 있습니다. 최소 한 명의 활성 owner가 항상 남아야 합니다.")} flush>
        <ul className="divide-y divide-gray-200" aria-label={t("서버 owner 목록")}>
          {data.owners.map((o) => (
            <li key={o.id} className="flex flex-wrap items-center gap-3 px-4 py-3 text-sm">
              <div className="min-w-0 flex-1">
                <div className="truncate font-medium">{o.name || o.email}{o.id === me?.id && <span className="ml-2 text-xs text-pink-500">{t("나")}</span>}</div>
                <div className="truncate text-xs tabular-nums text-gray-500">{o.email} · {SOURCE_LABEL[o.source]}{o.superadmin && o.source !== "env" ? " · KEEL_SUPERADMINS" : ""} · 2FA {o.totp_enabled ? t("켬") : t("끔")}{o.last_login_at ? ` · ${t("마지막 로그인 {time}", { time: fmtRelative(o.last_login_at) })}` : ""}</div>
              </div>
              {o.disabled && <span className="text-xs text-danger">{t("비활성")}</span>}
              {o.source === "role" && o.id !== me?.id && (
                <Button size="sm" variant="ghost" loading={busy === `demote-${o.id}`} onClick={() => confirm({
                  title: t("owner 해제"), danger: true, confirmLabel: t("서버 관리자로 내리기"),
                  message: <p>{t("{email}을(를) 서버 관리자(admin)로 내립니다. 모든 작업 공간에 대한 owner 접근과 이 메뉴를 잃습니다.", { email: o.email })}</p>,
                  run: () => run(`demote-${o.id}`, () => api.updateUser(o.id, { role: "admin" }), t("owner를 해제했습니다")),
                })}>{t("owner 해제")}</Button>
              )}
            </li>
          ))}
        </ul>
      </Card>
      {data.superadmins.length > 0 && (
        <p className="-mt-4 text-xs text-gray-500">{t("KEEL_SUPERADMINS: {list} (환경변수로만 바꿀 수 있습니다)", { list: data.superadmins.join(", ") })}</p>
      )}
      <Card title={t("owner 추가·소유권 이전")} description={t("다른 계정을 서버 owner로 만듭니다. '나는 owner로 남기'를 끄면 소유권을 넘기고 자신은 서버 관리자가 됩니다.")}
        footer={<Button variant="primary" size="sm" disabled={!picked} onClick={() => setConfirming(true)}>{stayOwner || !canStepDown ? t("owner로 지정") : t("소유권 넘기기")}</Button>}>
        <div className="space-y-4">
          <Select aria-label={t("새 owner")} value={target} onChange={(e) => setTarget(e.target.value)}>
            <option value="">{t("계정 선택")}</option>
            {candidates.map((u) => <option key={u.id} value={u.id}>{u.name ? `${u.name} (${u.email})` : u.email} · {u.role}</option>)}
          </Select>
          <label className="flex items-center justify-between gap-4 text-sm">
            <span>{t("나는 owner로 남기")}{!canStepDown && <span className="block text-xs text-gray-500">{t("기본 계정·KEEL_SUPERADMINS는 항상 owner입니다.")}</span>}</span>
            <Toggle checked={stayOwner || !canStepDown} disabled={!canStepDown} onChange={setStayOwner} label={t("owner로 남기")} />
          </label>
          <ErrorBox error={error} />
        </div>
      </Card>
      <TypeToConfirm
        open={confirming && !!picked}
        title={stayOwner || !canStepDown ? t("owner 지정") : t("서버 소유권 이전")}
        phrase={picked?.email ?? ""}
        label={stayOwner || !canStepDown ? t("owner로 지정") : t("소유권 넘기기")}
        busy={busy === "transfer"}
        onClose={() => setConfirming(false)}
        onConfirm={() => run("transfer", async () => {
          const r = await api.transferOwnership({ user_id: target, stay_owner: stayOwner || !canStepDown });
          setConfirming(false);
          setTarget("");
          if (!r.still_owner) toast(t("소유권을 넘겼습니다. 이제 서버 관리자입니다"), "warning");
        }, stayOwner || !canStepDown ? t("owner로 지정했습니다") : undefined)}
      >
        <p>{t("{email}은(는) 서버의 모든 작업 공간·프로젝트·비밀값에 접근하고, 다른 owner를 해제할 수 있게 됩니다.", { email: picked?.email ?? "" })}</p>
        {!(stayOwner || !canStepDown) && <p className="text-danger">{t("넘긴 뒤 나는 서버 관리자(admin)가 되어 이 메뉴를 쓸 수 없습니다.")}</p>}
      </TypeToConfirm>
    </>
  );
}

function AdminCapsCard({ data }: { data: OwnerOverview }) {
  const initial = data.admin_caps.filter((c) => !c.allowed).map((c) => c.id);
  const [disabled, setDisabled] = useState<AdminCapID[]>(initial);
  const { busy, error, run } = useOwnerRun();
  const key = initial.join(",");
  useEffect(() => setDisabled(key ? (key.split(",") as AdminCapID[]) : []), [key]);
  const dirty = [...disabled].sort().join(",") !== [...initial].sort().join(",");
  return (
    <Card title={t("서버 관리자 권한 범위")}
      description={t("서버 관리자(admin)가 쓸 수 있는 어드민 기능을 고릅니다. 끈 기능은 어드민 메뉴에서 사라지고 API도 403으로 막힙니다. owner에게는 적용되지 않습니다.")}
      footer={<Button variant="primary" size="sm" loading={busy === "caps"} disabled={!dirty} onClick={() => run("caps", () => api.setAdminCaps(disabled), t("권한 범위를 저장했습니다"))}>{t("저장")}</Button>}
      footerNote={disabled.length ? t("{count}개 기능 차단 중", { count: disabled.length }) : t("모든 기능 허용")}
      flush>
      <ul className="divide-y divide-gray-200" aria-label={t("서버 관리자 기능")}>
        {data.admin_caps.map((c) => {
          const on = !disabled.includes(c.id);
          return (
            <li key={c.id} className="flex items-center gap-3 px-4 py-3 text-sm">
              <span className="min-w-0 flex-1">{c.label}<span className="ml-2 font-mono text-xs text-gray-500">{c.id}</span></span>
              <Toggle checked={on} label={c.label} onChange={(v) => setDisabled((d) => v ? d.filter((x) => x !== c.id) : [...d, c.id])} />
            </li>
          );
        })}
      </ul>
      <ErrorBox error={error} className="m-3" />
    </Card>
  );
}

/* ── 전체 세션·API 토큰 ─────────────────────────────────────────────── */

export function OwnerSessions() {
  const sessions = useQuery({ queryKey: qk.ownerSessions, queryFn: api.ownerSessions, refetchInterval: 30_000 });
  const tokens = useQuery({ queryKey: qk.ownerTokens, queryFn: api.ownerTokens });
  const { busy, error, run } = useOwnerRun();
  const { confirm, node } = useConfirm();
  const [search, setSearch] = useState("");
  const needle = search.trim().toLocaleLowerCase();
  const match = (...s: string[]) => !needle || s.join(" ").toLocaleLowerCase().includes(needle);
  const ses = (sessions.data ?? []).filter((s) => match(s.email, s.name, s.ip, s.user_agent));
  const toks = (tokens.data ?? []).filter((tok) => match(tok.email, tok.user_name, tok.name, tok.prefix));
  return (
    <div className="space-y-8">
      {node}
      <Card title={t("전체 세션·API 토큰")} description={t("모든 사용자의 로그인 세션과 API 토큰을 보고 끊습니다. 끊긴 사용자는 다시 로그인해야 합니다.")}>
        <Input type="search" value={search} onChange={(e) => setSearch(e.target.value)} placeholder={t("이메일, 이름, IP, 토큰 이름으로 검색")} aria-label={t("세션·토큰 검색")} />
        <ErrorBox error={error ?? sessions.error ?? tokens.error} className="mt-3" />
      </Card>
      <Card title={sessions.data ? t("로그인 세션 ({count})", { count: ses.length }) : t("로그인 세션")}
        action={<Button size="sm" variant="danger" loading={busy === "ses-all"} onClick={() => confirm({
          title: t("owner 외 모든 세션 끊기"), danger: true, confirmLabel: t("모두 끊기"),
          message: <p>{t("서버 owner를 제외한 모든 사용자가 로그아웃됩니다.")}</p>,
          run: () => run("ses-all", async () => { const r = await api.ownerRevokeSessions({ scope: "non_owners" }); toast(t("세션 {count}개를 끊었습니다", { count: r.revoked ?? 0 }), "success"); }),
        })}><LogOut className="size-3.5" />{t("owner 외 모두 끊기")}</Button>} flush>
        {sessions.isPending ? <CenterSpinner /> : ses.length === 0 ? <p className="p-5 text-sm text-gray-500">{t("세션이 없습니다.")}</p> : (
          <ul className="divide-y divide-gray-200" aria-label={t("전체 세션")}>
            {ses.map((s) => (
              <li key={s.id} className="flex flex-wrap items-center gap-3 px-4 py-3 text-sm">
                <div className="min-w-0 flex-1">
                  <div className="truncate font-medium">{s.name || s.email}<span className="ml-2 text-xs text-gray-500">{s.user_role}</span>{s.current && <span className="ml-2 text-xs text-pink-500">{t("지금 이 세션")}</span>}</div>
                  <div className="truncate text-xs tabular-nums text-gray-500" title={s.user_agent}>{s.email} · {s.ip || t("IP 없음")} · {t("마지막 사용 {time}", { time: fmtRelative(s.last_seen) })} · {t("로그인 {time}", { time: fmtTime(s.created_at) })}{s.saml_authenticated ? " · SAML" : ""}</div>
                </div>
                {!s.current && <Button size="sm" variant="ghost" loading={busy === s.id} onClick={() => run(s.id, () => api.ownerDeleteSession(s.id), t("세션을 끊었습니다"))}>{t("끊기")}</Button>}
                <Button size="sm" variant="ghost" loading={busy === `u-${s.user_id}`} onClick={() => confirm({
                  title: t("사용자의 모든 세션 끊기"), danger: true, confirmLabel: t("모두 끊기"),
                  message: <p>{s.current ? t("{email}의 모든 세션을 끊습니다(지금 쓰는 이 세션은 남깁니다).", { email: s.email }) : t("{email}의 모든 세션을 끊습니다.", { email: s.email })}</p>,
                  run: () => run(`u-${s.user_id}`, () => api.ownerRevokeSessions({ user_id: s.user_id }), t("세션을 끊었습니다")),
                })}>{t("사용자 전체")}</Button>
              </li>
            ))}
          </ul>
        )}
      </Card>
      <Card title={tokens.data ? t("API 토큰 ({count})", { count: toks.length }) : t("API 토큰")}
        action={<Button size="sm" variant="danger" loading={busy === "tok-all"} onClick={() => confirm({
          title: t("owner 외 모든 API 토큰 폐기"), danger: true, confirmLabel: t("모두 폐기"),
          message: <p>{t("서버 owner를 제외한 모든 사용자의 API 토큰을 영구 폐기합니다. CI·자동화가 멈출 수 있습니다.")}</p>,
          run: () => run("tok-all", async () => { const r = await api.ownerRevokeTokens({ scope: "non_owners" }); toast(t("API 토큰 {count}개를 폐기했습니다", { count: r.revoked ?? 0 }), "success"); }),
        })}><Trash2 className="size-3.5" />{t("owner 외 모두 폐기")}</Button>} flush>
        {tokens.isPending ? <CenterSpinner /> : toks.length === 0 ? <p className="p-5 text-sm text-gray-500">{t("API 토큰이 없습니다.")}</p> : (
          <ul className="divide-y divide-gray-200" aria-label={t("전체 API 토큰")}>
            {toks.map((tok) => (
              <li key={tok.id} className="flex flex-wrap items-center gap-3 px-4 py-3 text-sm">
                <div className="min-w-0 flex-1">
                  <div className="truncate font-medium">{tok.name}<span className="ml-2 font-mono text-xs text-gray-500">{tok.prefix}…</span></div>
                  <div className="truncate text-xs tabular-nums text-gray-500">{tok.email || tok.user_id} · {tok.scope} · {tok.role} · {tok.last_used_at ? t("마지막 사용 {time}", { time: fmtRelative(tok.last_used_at) }) : t("사용 안 함")}{tok.expires_at ? ` · ${t("만료 {time}", { time: fmtTime(tok.expires_at) })}` : ` · ${t("만료 없음")}`}</div>
                </div>
                <Button size="sm" variant="ghost" loading={busy === tok.id} onClick={() => confirm({
                  title: t("API 토큰 폐기"), danger: true, confirmLabel: t("폐기"),
                  message: <p>{tr("{email}의 토큰 {name}을(를) 영구 폐기합니다.", { email: tok.email, name: <span className="font-mono">{tok.name}</span> })}</p>,
                  run: () => run(tok.id, () => api.ownerDeleteToken(tok.id), t("토큰을 폐기했습니다")),
                })}>{t("폐기")}</Button>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  );
}

/* ── aven DB 백업 ───────────────────────────────────────────────────── */

export function OwnerBackups() {
  const backups = useQuery({ queryKey: qk.keelBackups, queryFn: api.keelBackups });
  const { busy, error, run } = useOwnerRun();
  const { confirm, node } = useConfirm();
  return (
    <div className="space-y-8">
      {node}
      <Card title={<span className="flex items-center gap-2"><HardDriveDownload className="size-4" />{t("aven DB 백업")}</span>}
        description={t("aven 자체 데이터베이스(사용자, 프로젝트, 설정, 암호화된 변수)를 서버를 멈추지 않고 데이터 디렉터리의 backups/에 복사합니다. 배포 전 수동 백업도 여기에 보입니다.")}
        action={<Button variant="primary" size="sm" loading={busy === "create"} onClick={() => run("create", api.createKeelBackup, t("백업을 만들었습니다"))}>{t("지금 백업")}</Button>}>
        <p className="text-xs text-gray-500">{t("백업 파일에는 비밀값이 암호화된 채 들어 있습니다. 복원하려면 같은 master.key가 필요합니다. 내려받은 파일은 안전하게 보관하세요.")}</p>
        <ErrorBox error={error ?? backups.error} className="mt-3" />
      </Card>
      <Card title={t("백업 목록")} flush>
        {backups.isPending ? <CenterSpinner /> : (backups.data ?? []).length === 0 ? <p className="p-5 text-sm text-gray-500">{t("백업이 없습니다.")}</p> : (
          <ul className="divide-y divide-gray-200" aria-label={t("aven DB 백업")}>
            {backups.data!.map((b) => (
              <li key={b.name} className="flex flex-wrap items-center gap-3 px-4 py-3 text-sm">
                <div className="min-w-0 flex-1">
                  <div className="truncate font-mono">{b.name}</div>
                  <div className="text-xs tabular-nums text-gray-500">{fmtBytes(b.bytes)} · {fmtTime(b.modified_at)}</div>
                </div>
                <a href={api.keelBackupUrl(b.name)} download className="inline-flex h-[26px] items-center gap-1.5 rounded-md px-1.5 text-xs text-foreground hover:bg-gray-100 focus:outline-none focus-visible:ring-2 focus-visible:ring-gray-600"><Download className="size-3.5 text-gray-500" />{t("내려받기")}</a>
                <Button size="sm" variant="ghost" loading={busy === b.name} onClick={() => confirm({
                  title: t("백업 삭제"), danger: true, confirmLabel: t("삭제"),
                  message: <p>{tr("{name}을(를) 서버에서 지웁니다.", { name: <span className="font-mono">{b.name}</span> })}</p>,
                  run: () => run(b.name, () => api.deleteKeelBackup(b.name), t("백업을 지웠습니다")),
                })}>{t("삭제")}</Button>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  );
}
