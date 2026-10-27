"use client";

import { useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type ID, type InstanceRole, type ProjectRole, type WorkspaceRole } from "@/lib/api";
import { qk, useCaps, useMe, useSystem, useWorkspaceMembers } from "@/lib/queries";
import type { Workspace } from "@/lib/workspace";
import { fmtRelative } from "@/lib/format";
import type { User } from "@/lib/api";
import { t, tr } from "@/lib/i18n";
import { WorkspaceResourceSummary } from "./ResourceLimitsSettings";
import { Eye, KeyRound, LogOut, MoreHorizontal, Pencil, ScrollText, ShieldCheck, ShieldOff, Trash2, UserCog, UserMinus, UserPlus } from "lucide-react";
import { Avatar, Button, Card, CenterSpinner, CopyField, Dialog, EmptyState, ErrorBox, Field, IconButton, Input, Menu, MenuItem, Notice, RowList, Segmented, Select, useConfirm } from "./ui";

const ROLE_LABEL: Record<string, string> = { owner: "Owner", admin: "Admin", member: "Member", viewer: "Viewer" };

const roleBadge = "rounded bg-gray-100 px-1.5 py-0.5 text-xs font-medium text-gray-600";

function MoreMenu({ label, children }: { label: string; children: (close: () => void) => ReactNode }) {
  return (
    <Menu
      label={label}
      trigger={(p) => (
        <IconButton label={label} {...p}>
          <MoreHorizontal className="size-4" />
        </IconButton>
      )}
    >
      {children}
    </Menu>
  );
}

type InviteResult = { url: string; email_sent?: boolean; email_error?: string };

/** Shown once after creating an invite. With SMTP (v1.5) the link can also be mailed; otherwise the admin passes it on. */
function InviteLink({ invite }: { invite: InviteResult }) {
  return (
    <div className="space-y-2">
      <Notice>
        {invite.email_sent
          ? t("초대 링크를 만들었습니다 · 메일로 보냈습니다. 7일 동안 한 번 쓸 수 있고 지금 한 번만 보여 줍니다.")
          : t("초대 링크를 만들었습니다. 7일 동안 한 번 쓸 수 있고 지금 한 번만 보여 줍니다.")}
      </Notice>
      {invite.email_sent === false && <ErrorBox error={t("메일은 보내지 못했습니다: {error}. 링크를 직접 전달하세요.", { error: invite.email_error ?? t("알 수 없는 오류") })} />}
      <CopyField value={invite.url} />
    </div>
  );
}

/** "초대 메일 보내기" checkbox, shown only when the server has SMTP configured. */
function SendMailToggle({ checked, onChange }: { checked: boolean; onChange: (v: boolean) => void }) {
  const sys = useSystem();
  if (!sys.data?.smtp_configured) return null;
  return (
    <label className="flex cursor-pointer items-center gap-2 text-sm text-gray-600">
      <input type="checkbox" className="accent-pink-500" checked={checked} onChange={(e) => onChange(e.target.checked)} /> {t("초대 메일 보내기")}
    </label>
  );
}

/* ── Project → Members ───────────────────────────────────────────────── */


/** 사용자 2FA 상태 요약(인증 앱, 패스키 수). */
/* ── Project → Members ───────────────────────────────────────────────── */
function mfaLabel(u: User): string {
  const parts = [u.totp_enabled && t("인증 앱"), (u.passkey_count ?? 0) > 0 && t("패스키 {count}개", { count: u.passkey_count ?? 0 })].filter(Boolean);
  return parts.length ? t("켜짐({methods})", { methods: parts.join(", ") }) : t("꺼짐");
}

function PeopleSection({
  tab,
  count,
  action,
  desc,
  cols,
  filters,
  children,
}: {
  tab: string;
  count?: number;
  action?: ReactNode;
  desc?: ReactNode;
  cols: [string, string, string?];
  filters?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="max-w-3xl">
      <div className="flex items-end border-b border-gray-200">
        <div className="-mb-px flex items-center gap-2 border-b-2 border-pink-500 px-3 pb-3 text-sm font-medium text-foreground">
          {tab}
          {count !== undefined && <span className="rounded border border-gray-200 px-1.5 text-xs tabular-nums text-gray-600">{count}</span>}
        </div>
        <div className="ml-auto flex items-center gap-2 pb-2">{action}</div>
      </div>
      {desc && <p className="mt-4 text-sm text-gray-600">{desc}</p>}
      {filters && <div className="mt-4">{filters}</div>}
      <div className="mt-4 overflow-hidden rounded-lg border border-gray-200">
        <div className="flex items-center gap-3 bg-secondaryBg px-4 py-3 text-xs font-semibold text-neutral-secondary">
          <span className="flex-1">{cols[0]}</span>
          <span className="w-[116px]">{cols[1]}</span>
          {cols[2] && <span className="w-24">{cols[2]}</span>}
          <span className="w-8" />
        </div>
        {children}
      </div>
    </div>
  );
}

export function ProjectMembers({ pid }: { pid: ID }) {
  const qc = useQueryClient();
  const caps = useCaps(pid);
  const members = useQuery({ queryKey: qk.members(pid), queryFn: () => api.members(pid) });
  const users = useQuery({ queryKey: [...qk.workspaceMembers(caps.workspaceId ?? ""), "people"], queryFn: () => api.workspacePeople(caps.workspaceId!), enabled: caps.canAdmin && !!caps.workspaceId, retry: false });
  const [uid, setUid] = useState("");
  const [role, setRole] = useState<ProjectRole>("member");
  const [email, setEmail] = useState("");
  const [inviteRole, setInviteRole] = useState<ProjectRole>("member");
  const [link, setLink] = useState<InviteResult | null>(null);
  const [inviting, setInviting] = useState(false);
  const [mode, setMode] = useState<"invite" | "existing">("invite");
  const [sendMail, setSendMail] = useState(true);
  const smtp = !!useSystem().data?.smtp_configured;
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const { confirm, node } = useConfirm();
  const refresh = () => qc.invalidateQueries({ queryKey: qk.members(pid) });
  const run = async (f: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await f();
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };
  const memberIds = new Set((members.data ?? []).map((m) => m.user_id));
  const candidates = (users.data ?? []).filter((u) => !memberIds.has(u.id) && !u.disabled && u.role !== "owner" && u.role !== "admin");
  const closeInvite = () => {
    if (busy) return;
    setInviting(false);
    setLink(null);
    setError(null);
  };
  const existing = mode === "existing" && candidates.length > 0;
  const submitInvite = () =>
    run(async () => {
      if (existing) {
        await api.setMember(pid, uid, role);
        setUid("");
        await refresh();
        setInviting(false);
        return;
      }
      const r = await api.createInvite({ email: email.trim(), role: "member", project_id: pid, project_role: inviteRole, send_email: smtp && sendMail });
      setLink(r);
      setEmail("");
    });
  return (
    <>
      {node}
      <Card title={t("접근 권한")}>
      <ErrorBox error={members.error ?? (inviting ? null : error)} className="mb-3" />
      <PeopleSection
        tab={t("사람")}
        count={members.data?.length}
        cols={[t("이름"), t("접근")]}
        action={
          caps.canAdmin && (
            <Button variant="primary" size="sm" onClick={() => setInviting(true)}>
              <UserPlus className="size-3.5" /> {t("초대")}
            </Button>
          )
        }
      >
        {members.isPending ? (
          <CenterSpinner />
        ) : !members.data?.length ? (
          <div className="px-5 pb-5">
            <EmptyState icon={<UserCog className="size-5" />} title={t("멤버가 없습니다")}>
              {t("작업 공간 Owner/Admin은 멤버가 아니어도 이 프로젝트를 볼 수 있습니다.")}
            </EmptyState>
          </div>
        ) : (
          <ul className="divide-y divide-gray-200">
            {members.data.map((m) => (
              <li key={m.user_id} className="flex items-center gap-3 px-4 py-3">
                <Avatar name={m.name || m.email} />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm font-medium">{m.name || m.email}</div>
                  <div className="truncate text-xs text-gray-500">
                    {m.email} · {t("작업 공간 {role}", { role: ROLE_LABEL[m.workspace_role] })}
                  </div>
                </div>
                {caps.canAdmin ? (
                  <>
                    <Select
                      aria-label={t("{email} 역할", { email: m.email })}
                      value={m.role}
                      className="w-[116px]"
                      onChange={(e) => run(async () => (await api.setMember(pid, m.user_id, e.target.value as ProjectRole), await refresh()))}
                    >
                      <option value="admin">Admin</option>
                      <option value="member">Member</option>
                      <option value="viewer">Viewer</option>
                    </Select>
                    <MoreMenu label={t("{email} 메뉴", { email: m.email })}>
                      {(close) => (
                        <MenuItem
                          danger
                          icon={<UserMinus className="size-3.5" />}
                          onSelect={() => {
                            close();
                            confirm({
                              title: t("멤버 내보내기"),
                              message: t("{email}은 더 이상 이 프로젝트를 볼 수 없습니다.", { email: m.email }),
                              confirmLabel: t("내보내기"),
                              danger: true,
                              run: () => run(async () => (await api.removeMember(pid, m.user_id), await refresh())),
                            });
                          }}
                        >
                          {t("내보내기")}
                        </MenuItem>
                      )}
                    </MoreMenu>
                  </>
                ) : (
                  <span className={roleBadge}>{ROLE_LABEL[m.role]}</span>
                )}
              </li>
            ))}
          </ul>
        )}
      </PeopleSection>
      </Card>
      <Dialog
        open={inviting}
        onClose={closeInvite}
        title={t("멤버 초대")}
        footer={
          link ? (
            <Button variant="primary" onClick={closeInvite}>
              {t("완료")}
            </Button>
          ) : (
            <>
              <Button variant="outline" onClick={closeInvite} disabled={busy}>
                {t("취소")}
              </Button>
              <Button variant="primary" loading={busy} disabled={existing ? !uid : !email.trim()} onClick={submitInvite}>
                {existing ? t("추가") : t("초대 링크 만들기")}
              </Button>
            </>
          )
        }
      >
        {link ? (
          <InviteLink invite={link} />
        ) : (
          <form
            className="space-y-4 pt-1"
            onSubmit={(e) => {
              e.preventDefault();
              if (existing ? uid : email.trim()) submitInvite();
            }}
          >
            {candidates.length > 0 && (
              <Segmented
                label={t("초대 방식")}
                value={mode}
                onChange={setMode}
                options={[
                  { value: "invite", label: t("이메일로 초대") },
                  { value: "existing", label: t("기존 사용자") },
                ]}
              />
            )}
            {existing ? (
              <>
                <Field label={t("사용자")}>
                  <Select value={uid} onChange={(e) => setUid(e.target.value)}>
                    <option value="">{t("선택…")}</option>
                    {candidates.map((u) => (
                      <option key={u.id} value={u.id}>
                        {u.name ? `${u.name} (${u.email})` : u.email}
                      </option>
                    ))}
                  </Select>
                </Field>
                <Field label={t("역할")}>
                  <Select value={role} onChange={(e) => setRole(e.target.value as ProjectRole)}>
                    <option value="admin">Admin</option>
                    <option value="member">Member</option>
                    <option value="viewer">Viewer</option>
                  </Select>
                </Field>
              </>
            ) : (
              <>
                <Field label={t("이메일")}>
                  <Input type="email" autoFocus value={email} onChange={(e) => setEmail(e.target.value)} placeholder="teammate@example.com" />
                </Field>
                <Field label={t("프로젝트 역할")}>
                  <Select value={inviteRole} onChange={(e) => setInviteRole(e.target.value as ProjectRole)}>
                    <option value="admin">Admin</option>
                    <option value="member">Member</option>
                    <option value="viewer">Viewer</option>
                  </Select>
                </Field>
                <SendMailToggle checked={sendMail} onChange={setSendMail} />
              </>
            )}
            <ErrorBox error={error} />
            <button type="submit" className="hidden" aria-hidden tabIndex={-1} />
          </form>
        )}
      </Dialog>
    </>
  );
}

/* ── Settings → Instance users (인스턴스 admin) ─────────────────────── */

export function InstanceUsers() {
  const qc = useQueryClient();
  const me = useMe().data;
  const users = useQuery({ queryKey: qk.users, queryFn: api.users });
  const invites = useQuery({ queryKey: qk.invites, queryFn: api.invites });
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState("all");
  const [filterRole, setFilterRole] = useState("all");
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<InstanceRole>("member");
  const [link, setLink] = useState<InviteResult | null>(null);
  const [inviting, setInviting] = useState(false);
  const [sendMail, setSendMail] = useState(true);
  const smtp = !!useSystem().data?.smtp_configured;
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const { confirm, node } = useConfirm();
  const isOwner = me?.instance_role === "owner";
  const needle = search.trim().toLocaleLowerCase();
  const filteredUsers = (users.data ?? []).filter((u) => `${u.name} ${u.email}`.toLocaleLowerCase().includes(needle)
    && (status === "all" || (status === "disabled" ? u.disabled : !u.disabled))
    && (filterRole === "all" || u.role === filterRole));
  const refresh = async () => {
    await qc.invalidateQueries({ queryKey: qk.me });
    await qc.invalidateQueries({ queryKey: qk.users });
    await qc.invalidateQueries({ queryKey: qk.invites });
    await qc.invalidateQueries({ queryKey: qk.instanceOverview });
    await qc.invalidateQueries({ queryKey: qk.audit });
  };
  const run = async (f: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await f();
      await refresh();
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };
  const closeInvite = () => {
    if (busy) return;
    setInviting(false);
    setLink(null);
    setError(null);
  };
  const submitInvite = () =>
    run(async () => {
      const r = await api.createInvite({ email: email.trim(), role, send_email: smtp && sendMail });
      setLink(r);
      setEmail("");
    });
  return (
    <>
      {node}
      <Card title={t("사용자·초대")} description={t("팀을 만들기 전에도 새 사용자를 초대할 수 있습니다.")}>
      <ErrorBox error={users.error ?? invites.error ?? (inviting ? null : error)} className="mb-3" />
      <div className="space-y-6">
        <PeopleSection
          tab={t("사용자")}
          count={users.data ? filteredUsers.length : undefined}
          cols={[t("사람"), t("역할")]}
          desc={t("서버 관리자는 계정과 서버 설정을 관리합니다. 팀에 참여할 사람은 해당 작업 공간에서 추가하세요.")}
          filters={<div className="space-y-3">
            <div className="grid gap-3 sm:grid-cols-[1fr_140px_140px]">
              <Input type="search" aria-label={t("사용자 검색")} value={search} onChange={(e) => setSearch(e.target.value)} placeholder={t("이름 또는 이메일 검색")} />
              <Select aria-label={t("계정 상태 필터")} value={status} onChange={(e) => setStatus(e.target.value)}>
                <option value="all">{t("전체 상태")}</option><option value="active">{t("활성")}</option><option value="disabled">{t("비활성")}</option>
              </Select>
              <Select aria-label={t("서버 역할 필터")} value={filterRole} onChange={(e) => setFilterRole(e.target.value)}>
                <option value="all">{t("전체 역할")}</option><option value="owner">Owner</option><option value="admin">Admin</option><option value="member">Member</option><option value="viewer">Viewer</option>
              </Select>
            </div>
            {users.data && <div className="flex flex-wrap items-center gap-3 text-xs tabular-nums text-gray-500" role="status">
              <span>{t("{shown}명 표시 · 전체 {total}명", { shown: filteredUsers.length, total: users.data.length })}</span>
              {(search || status !== "all" || filterRole !== "all") && <Button size="sm" variant="ghost" onClick={() => { setSearch(""); setStatus("all"); setFilterRole("all"); }}>{t("필터 초기화")}</Button>}
            </div>}
          </div>}
          action={
            <Button variant="primary" size="sm" onClick={() => setInviting(true)}>
              <UserPlus className="size-3.5" /> {t("사용자 초대")}
            </Button>
          }
        >
          {users.isPending ? (
            <CenterSpinner />
          ) : users.data && filteredUsers.length === 0 ? (
            <p className="p-5 text-sm text-gray-500">{t("조건에 맞는 사용자가 없습니다.")}</p>
          ) : (
            <ul className="divide-y divide-gray-200" aria-label={t("서버 사용자 목록")}>
              {filteredUsers.map((u) => {
                const locked = u.builtin || (u.role === "owner" && !isOwner);
                const canDisable = u.id !== me?.id && !(u.role === "owner" && !isOwner);
                const hasMfa = u.totp_enabled || (u.passkey_count ?? 0) > 0;
                const canReset = hasMfa && !locked;
                const canDelete = !u.builtin && u.id !== me?.id && !(u.role === "owner" && !isOwner);
                // 서버 owner만, 다른 owner·비활성 계정이 아닌 사람을 읽기 전용으로 볼 수 있다.
                const canViewAs = me?.instance_owner === true && u.id !== me?.id && !u.builtin && u.role !== "owner" && !u.disabled;
                return (
                  <li key={u.id} className="flex flex-wrap items-center gap-3 px-4 py-3">
                    <Avatar name={u.name || u.email} />
                    <div className="min-w-0 flex-1 basis-[200px]">
                      <div className="truncate text-sm font-medium">
                        {u.name || u.email}
                        {u.builtin && <span className="ml-2 text-xs text-gray-500">{t("기본 계정")}</span>}
                        {u.disabled && <span className="ml-2 text-xs text-danger">{t("비활성화됨")}</span>}
                      </div>
                      <div className="truncate text-xs tabular-nums text-gray-500">
                        {t("{email} · 2FA {mfa} · 마지막 로그인 {time}", { email: u.email, mfa: mfaLabel(u), time: fmtRelative(u.last_login_at) })}
                      </div>
                    </div>
                    <Select
                      aria-label={t("{email} 역할", { email: u.email })}
                      value={u.role}
                      disabled={locked || busy}
                      className="w-[116px]"
                      onChange={(e) => run(() => api.updateUser(u.id, { role: e.target.value as InstanceRole }))}
                    >
                      {(isOwner || u.role === "owner") && <option value="owner">Owner</option>}
                      <option value="admin">Admin</option>
                      <option value="member">Member</option>
                      <option value="viewer">Viewer</option>
                    </Select>
                    {canDisable || canReset || canDelete || canViewAs ? (
                      <MoreMenu label={t("{email} 메뉴", { email: u.email })}>
                        {(close) => (
                          <>
                            {canDisable && (
                              <MenuItem
                                icon={u.disabled ? <ShieldCheck className="size-3.5" /> : <ShieldOff className="size-3.5" />}
                                onSelect={() => {
                                  close();
                                  run(() => api.updateUser(u.id, { disabled: !u.disabled }));
                                }}
                              >
                                {u.disabled ? t("활성화") : t("비활성화")}
                              </MenuItem>
                            )}
                            {canViewAs && (
                              <MenuItem
                                icon={<Eye className="size-3.5" />}
                                onSelect={() => {
                                  close();
                                  confirm({
                                    title: t("사용자로 보기"),
                                    message: t("30분 동안 {email}의 권한으로 대시보드를 읽기 전용으로 봅니다. 그동안 변경은 할 수 없고, 시작과 끝은 감사 기록에 남습니다.", { email: u.email }),
                                    confirmLabel: t("보기 시작"),
                                    run: async () => {
                                      await api.startViewAs(u.id);
                                      window.location.href = "/";
                                    },
                                  });
                                }}
                              >
                                {t("사용자로 보기")}
                              </MenuItem>
                            )}
                            {canReset && (
                              <MenuItem
                                icon={<KeyRound className="size-3.5" />}
                                onSelect={() => {
                                  close();
                                  run(() => api.updateUser(u.id, { reset_2fa: true }));
                                }}
                              >
                                {t("2FA 초기화")}
                              </MenuItem>
                            )}
                            {canDelete && (
                              <MenuItem
                                danger
                                icon={<Trash2 className="size-3.5" />}
                                onSelect={() => {
                                  close();
                                  confirm({
                                    title: t("사용자 삭제"),
                                    message: t("{email}의 세션, API 토큰, 프로젝트 멤버십이 모두 지워집니다.", { email: u.email }),
                                    confirmLabel: t("삭제"),
                                    danger: true,
                                    run: () => run(() => api.deleteUser(u.id)),
                                  });
                                }}
                              >
                                {t("삭제")}
                              </MenuItem>
                            )}
                          </>
                        )}
                      </MoreMenu>
                    ) : (
                      <span className="size-8" />
                    )}
                  </li>
                );
              })}
            </ul>
          )}
        </PeopleSection>
      </div>
      </Card>
        {!!invites.data?.length && (
          <Card flush title={t("대기 중인 초대")}>
            <RowList bare>
              {invites.data.map((i) => (
                <li key={i.id} className="flex items-center gap-3 px-4 py-3">
                  <Avatar name={i.email} />
                  <div className="min-w-0 flex-1 truncate text-sm">
                    {i.email} <span className="text-xs tabular-nums text-gray-500">· {t("{role} · 만료 {date}", { role: ROLE_LABEL[i.role], date: i.expires_at.slice(0, 10) })}</span>
                  </div>
                  <Button size="sm" onClick={() => run(() => api.deleteInvite(i.id))}>
                    {t("취소")}
                  </Button>
                </li>
              ))}
            </RowList>
          </Card>
        )}
      <Dialog
        open={inviting}
        onClose={closeInvite}
        title={t("사용자 초대")}
        footer={
          link ? (
            <Button variant="primary" onClick={closeInvite}>
              {t("완료")}
            </Button>
          ) : (
            <>
              <Button variant="outline" onClick={closeInvite} disabled={busy}>
                {t("취소")}
              </Button>
              <Button variant="primary" loading={busy} disabled={!email.trim()} onClick={submitInvite}>
                {t("초대 링크 만들기")}
              </Button>
            </>
          )
        }
      >
        {link ? (
          <InviteLink invite={link} />
        ) : (
          <form
            className="space-y-4 pt-1"
            onSubmit={(e) => {
              e.preventDefault();
              if (email.trim()) submitInvite();
            }}
          >
            <p className="text-sm text-gray-600">{t("가입할 수 있는 링크를 만듭니다. 팀 참여는 가입 후 작업 공간에서 정합니다.")}</p>
            <Field label={t("초대할 이메일")}>
              <Input type="email" autoFocus value={email} onChange={(e) => setEmail(e.target.value)} placeholder="teammate@example.com" />
            </Field>
            <Field label={t("서버 역할")}>
              <Select value={role} onChange={(e) => setRole(e.target.value as InstanceRole)}>
                {isOwner && <option value="owner">Owner</option>}
                <option value="admin">Admin</option>
                <option value="member">Member</option>
                <option value="viewer">Viewer</option>
              </Select>
            </Field>
            <SendMailToggle checked={sendMail} onChange={setSendMail} />
            <ErrorBox error={error} />
            <button type="submit" className="hidden" aria-hidden tabIndex={-1} />
          </form>
        )}
      </Dialog>
    </>
  );
}

export function AuditLog() {
  const audit = useQuery({ queryKey: qk.audit, queryFn: () => api.audit(50) });
  return (
    <Card title={t("감사 기록")}>
      <ErrorBox error={audit.error} className="mb-3" />
      {audit.isPending ? (
        <CenterSpinner />
      ) : !audit.data?.length ? (
        <EmptyState icon={<ScrollText className="size-5" />} title={t("기록이 없습니다")} />
      ) : (
        <RowList>
          {audit.data.map((a) => (
            <li key={a.id} className="px-4 py-3">
              <div className="truncate text-sm">{a.message}</div>
              <div className="truncate text-xs tabular-nums text-gray-500">
                <span className="font-mono">{a.kind}</span> · {a.actor || t("시스템")} · <span className="font-mono">{a.ip}</span> · {fmtRelative(a.created_at)}
              </div>
            </li>
          ))}
        </RowList>
      )}
    </Card>
  );
}

/* ── Settings → Workspace members (v1.6, 현재 작업 공간) ──────────────── */

/**
 * 현재 작업 공간의 멤버 관리.
 * - 기본/팀/개인 작업 공간 모두 명시적인 멤버십을 관리한다.
 * - admin 이상은 이름 변경·멤버 추가/역할 변경/내보내기, owner는 팀 작업 공간을 삭제할 수 있다.
 * - 멤버는 자신을 내보내(나가기) 작업 공간을 떠날 수 있다.
 */
export function WorkspaceMembersSettings({ ws }: { ws: Workspace }) {
  const qc = useQueryClient();
  const me = useMe().data;
  const cur = ws.curWorkspace;
  const wsid = cur?.id ?? null;
  const role = cur?.role;
  const canManage = role === "owner" || role === "admin";
  const isOwner = role === "owner";
  const isDefault = !!cur?.default;
  const isPersonal = !!cur?.personal;

  const members = useWorkspaceMembers(wsid, !!cur);
  const [name, setName] = useState(cur?.name ?? "");
  const [renaming, setRenaming] = useState(false);
  const [email, setEmail] = useState("");
  const [inviteRole, setInviteRole] = useState<WorkspaceRole>("member");
  const [adding, setAdding] = useState(false);
  const [workspaceInvite, setWorkspaceInvite] = useState<InviteResult | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [typed, setTyped] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const { confirm, node } = useConfirm();

  const refreshMembers = () => qc.invalidateQueries({ queryKey: qk.workspaceMembers(wsid ?? "") });
  const refreshWorkspaces = async () => {
    await qc.invalidateQueries({ queryKey: qk.workspaces });
    await qc.invalidateQueries({ queryKey: qk.me });
  };
  const run = async (f: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await f();
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  if (!cur) {
    return <Card title={t("작업 공간")} description={ws.workspaces.isPending ? t("불러오는 중…") : t("작업 공간을 찾을 수 없습니다.")} />;
  }


  return (
    <>
      {node}
      <Card
        title={t("작업 공간 · {name}", { name: cur.name })}
        description={t("이 작업 공간의 멤버십과 프로젝트 접근을 관리합니다. 인스턴스 운영 역할은 별도로 관리합니다.")}
        action={
          canManage && (
            <Button variant="outline" size="sm" onClick={() => (setName(cur.name), setRenaming(true))}>
              <Pencil className="size-3.5" /> {t("이름 변경")}
            </Button>
          )
        }
      >
        <ErrorBox error={members.error ?? (adding || renaming || deleting ? null : error)} className="mb-3" />
        <PeopleSection
            tab={t("멤버")}
            count={members.data?.length}
            cols={[t("이름"), t("역할")]}
            desc={t("Owner/Admin은 이 작업 공간의 모든 프로젝트와 멤버를 관리합니다. Member는 프로젝트를 만들 수 있고, Member/Viewer는 자기가 만들었거나 멤버로 추가된 프로젝트만 봅니다.")}
            action={
              canManage && (
                <Button variant="primary" size="sm" onClick={() => { setWorkspaceInvite(null); setAdding(true); }}>
                  <UserPlus className="size-3.5" /> {t("멤버 추가")}
                </Button>
              )
            }
          >
            {members.isPending ? (
              <CenterSpinner />
            ) : !members.data?.length ? (
              <div className="px-5 pb-5">
                <EmptyState icon={<UserCog className="size-5" />} title={t("멤버가 없습니다")}>
                  {t("Owner 외에 아직 멤버가 없습니다.")}
                </EmptyState>
              </div>
            ) : (
              <ul className="divide-y divide-gray-200">
                {members.data.map((m) => {
                  const self = m.user_id === me?.id;
                  const lockRole = m.role === "owner" || (!canManage && !self);
                  return (
                    <li key={m.user_id} className="flex items-center gap-3 px-4 py-3">
                      <Avatar name={m.name || m.email} />
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-sm font-medium">
                          {m.name || m.email}
                          {self && <span className="ml-2 text-xs text-gray-500">{t("나")}</span>}
                        </div>
                        <div className="truncate text-xs text-gray-500">{m.email}</div>
                      </div>
                      {canManage && (m.role !== "owner" || isOwner) && !(isPersonal && m.user_id === cur.owner_id) ? (
                        <Select
                          aria-label={t("{email} 역할", { email: m.email })}
                          value={m.role}
                          disabled={busy}
                          className="w-[116px]"
                          onChange={(e) => run(async () => (await api.setWorkspaceMember(wsid!, m.user_id, e.target.value as WorkspaceRole), await refreshMembers(), await refreshWorkspaces()))}
                        >
                          {isOwner && <option value="owner">Owner</option>}
                          <option value="admin">Admin</option>
                          <option value="member">Member</option>
                          <option value="viewer">Viewer</option>
                        </Select>
                      ) : (
                        <span className={roleBadge}>{ROLE_LABEL[m.role]}</span>
                      )}
                      {(canManage || self) && (m.role !== "owner" || isOwner) && !(isPersonal && m.user_id === cur.owner_id) ? (
                        <MoreMenu label={t("{email} 메뉴", { email: m.email })}>
                          {(close) => (
                            <MenuItem
                              danger
                              icon={self ? <LogOut className="size-3.5" /> : <UserMinus className="size-3.5" />}
                              onSelect={() => {
                                close();
                                confirm({
                                  title: self ? t("작업 공간 나가기") : t("멤버 내보내기"),
                                  message: self
                                    ? t("{workspace} 작업 공간을 떠납니다. 다시 초대받아야 들어올 수 있습니다.", { workspace: cur.name })
                                    : t("{email}은 더 이상 이 작업 공간을 볼 수 없습니다.", { email: m.email }),
                                  confirmLabel: self ? t("나가기") : t("내보내기"),
                                  danger: true,
                                  run: () =>
                                    run(async () => {
                                      await api.removeWorkspaceMember(wsid!, m.user_id);
                                      if (self) await refreshWorkspaces();
                                      else await refreshMembers();
                                    }),
                                });
                              }}
                            >
                              {self ? t("나가기") : t("내보내기")}
                            </MenuItem>
                          )}
                        </MoreMenu>
                      ) : (
                        <span className="size-8" />
                      )}
                    </li>
                  );
                })}
              </ul>
            )}
          </PeopleSection>
      </Card>

      <WorkspaceResourceSummary wsid={cur.id} />

      {isOwner && !isPersonal && !isDefault && (
        <Card title={t("작업 공간 삭제")} className="mt-8 border-t border-gray-200 pt-8">
          <div className="space-y-4">
            <div className="flex items-start gap-3 rounded-md border border-red-200 px-4 py-3 text-sm text-danger">
              <Trash2 className="mt-0.5 size-4 shrink-0" />
              {t("작업 공간을 삭제하려면 먼저 모든 프로젝트를 다른 작업 공간으로 옮기거나 삭제해야 합니다. 되돌릴 수 없습니다.")}
            </div>
            <Button variant="danger" onClick={() => (setTyped(""), setDeleting(true))}>
              {t("작업 공간 삭제")}
            </Button>
          </div>
        </Card>
      )}

      <Dialog
        open={renaming}
        onClose={() => !busy && setRenaming(false)}
        title={t("작업 공간 이름 변경")}
        footer={
          <>
            <Button variant="outline" onClick={() => setRenaming(false)} disabled={busy}>
              {t("취소")}
            </Button>
            <Button
              variant="primary"
              loading={busy}
              disabled={!name.trim() || name.trim() === cur.name}
              onClick={() => run(async () => (await api.renameWorkspace(wsid!, name.trim()), await refreshWorkspaces(), setRenaming(false)))}
            >
              {t("저장")}
            </Button>
          </>
        }
      >
        <div className="space-y-3 pt-1">
          <Field label={t("이름")}>
            <Input autoFocus value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
          {renaming && <ErrorBox error={error} />}
        </div>
      </Dialog>

      <Dialog
        open={adding}
        onClose={() => !busy && (setAdding(false), setEmail(""), setError(null))}
        title={t("멤버 추가")}
        footer={
          <>
            <Button variant="outline" onClick={() => (setAdding(false), setEmail(""), setError(null))} disabled={busy}>
              {t("취소")}
            </Button>
            <Button variant="outline" loading={busy} disabled={!email.trim() || !!workspaceInvite}
              onClick={() => run(async () => { setWorkspaceInvite(await api.inviteWorkspaceMember(wsid!, { email: email.trim(), role: inviteRole })); })}>
              {t("새 사용자 초대 링크")}
            </Button>
            <Button
              variant="primary"
              loading={busy}
              disabled={!email.trim()}
              onClick={() =>
                run(async () => {
                  await api.addWorkspaceMember(wsid!, { email: email.trim(), role: inviteRole });
                  setEmail("");
                  await refreshMembers();
                  setAdding(false);
                })
              }
            >
              {t("추가")}
            </Button>
          </>
        }
      >
        <form
          className="space-y-4 pt-1"
          onSubmit={(e) => {
            e.preventDefault();
            if (email.trim())
              run(async () => {
                await api.addWorkspaceMember(wsid!, { email: email.trim(), role: inviteRole });
                setEmail("");
                await refreshMembers();
                setAdding(false);
              });
          }}
        >
          <p className="text-sm text-gray-600">{t("Aven에 이미 있는 사용자만 추가할 수 있습니다. 새 사용자는 아래에서 초대 링크를 만들 수 있습니다.")}</p>
          {workspaceInvite && <InviteLink invite={workspaceInvite} />}
          <Field label={t("이메일")}>
            <Input type="email" autoFocus value={email} onChange={(e) => setEmail(e.target.value)} placeholder="teammate@example.com" />
          </Field>
          <Field label={t("역할")}>
            <Select value={inviteRole} onChange={(e) => setInviteRole(e.target.value as WorkspaceRole)}>
              {isOwner && <option value="owner">Owner</option>}
              <option value="admin">Admin</option>
              <option value="member">Member</option>
              <option value="viewer">Viewer</option>
            </Select>
          </Field>
          {adding && <ErrorBox error={error} />}
          <button type="submit" className="hidden" aria-hidden tabIndex={-1} />
        </form>
      </Dialog>

      <Dialog
        open={deleting}
        onClose={() => !busy && setDeleting(false)}
        title={t("{name} 삭제", { name: cur.name })}
        footer={
          <>
            <Button variant="outline" onClick={() => setDeleting(false)} disabled={busy}>
              {t("취소")}
            </Button>
            <Button
              variant="danger"
              loading={busy}
              disabled={typed !== cur.name}
              onClick={() =>
                run(async () => {
                  await api.deleteWorkspace(wsid!);
                  try {
                    window.localStorage.removeItem("keel.workspace.v1");
                  } catch {
                    /* ignore */
                  }
                  await refreshWorkspaces();
                  setDeleting(false);
                  const next = ws.wsList.find((w) => w.id !== wsid);
                  if (next) ws.switchWorkspace(next.id);
                })
              }
            >
              {t("영구 삭제")}
            </Button>
          </>
        }
      >
        <div className="space-y-3 pt-1">
          <p>
            {tr("이 작업 공간에 프로젝트가 남아 있으면 삭제할 수 없습니다(먼저 옮기거나 삭제하세요). 확인을 위해 작업 공간 이름 {name}을(를) 입력하세요.", { name: <span className="font-mono text-foreground">{cur.name}</span> })}
          </p>
          <Input mono value={typed} onChange={(e) => setTyped(e.target.value)} aria-label={t("작업 공간 이름 확인")} autoComplete="off" />
          {deleting && <ErrorBox error={error} />}
        </div>
      </Dialog>
    </>
  );
}
