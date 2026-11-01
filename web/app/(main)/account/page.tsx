"use client";

import { toast } from "@/lib/uiState";
import { Suspense, useEffect, useState, type ReactNode } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Bell, Download, KeyRound, Monitor, Plug, ShieldCheck, User as UserIcon } from "lucide-react";
import { api, type ProjectRole, type TokenScope } from "@/lib/api";
import { qk, useMe, useProject, useProjects } from "@/lib/queries";
import { useCtxHref } from "@/lib/nav";
import { t } from "@/lib/i18n";
import { fmtRelative } from "@/lib/format";
import { QrCode } from "@/components/QrCode";
import { GraphQLApiCard } from "@/components/GraphQLApiCard";
import { PasskeysSettings } from "@/components/PasskeysSettings";
import { AuthorizedAppsSection } from "@/components/OAuthSettings";
import { ThemeToggle } from "@/components/ThemeToggle";
import { LanguageSelect } from "@/components/LanguageSelect";
import { GitHubConnectionCard } from "@/components/GitHubConnection";
import { PasswordInput, PasswordStrengthMeter } from "@/components/AuthKit";
import { Button, Card, CenterSpinner, CopyField, EmptyState, RowList, ErrorBox, Field, Input, LABEL_CLS, Notice, PageShell, Select, SideNavLayout, Textarea, cx, useConfirm } from "@/components/ui";

type SectionId = "profile" | "security" | "notifications" | "sessions" | "tokens" | "integrations" | "authorized-apps";

const NAV: { id: SectionId; label: string; icon: ReactNode }[] = [
  { id: "profile", label: t("프로필"), icon: <UserIcon className="size-4" /> },
  { id: "security", label: t("보안"), icon: <ShieldCheck className="size-4" /> },
  { id: "notifications", label: t("알림"), icon: <Bell className="size-4" /> },
  { id: "sessions", label: t("세션"), icon: <Monitor className="size-4" /> },
  { id: "tokens", label: t("API 토큰"), icon: <KeyRound className="size-4" /> },
  { id: "integrations", label: t("GitHub 연결"), icon: <Plug className="size-4" /> },
  { id: "authorized-apps", label: t("승인된 앱"), icon: <ShieldCheck className="size-4" /> },
];

/** Section intro: a header-only Card, so every account section shares the settings Card title size and width. */
function H2({ children, sub }: { children: ReactNode; sub?: ReactNode }) {
  return <Card title={children} description={sub} />;
}

const STACK = "max-w-3xl [&>*+*]:mt-8 [&>*+*]:border-t [&>*+*]:border-gray-200 [&>*+*]:pt-8";

const ROLE_LABEL: Record<string, string> = { owner: "Owner", admin: "Admin", member: "Member", viewer: "Viewer" };

/* ── profile + password ──────────────────────────────────────────────── */

function Profile() {
  const qc = useQueryClient();
  const me = useMe().data!;
  const [name, setName] = useState(me.name);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [done, setDone] = useState(false);
  return (
    <div className={STACK}>
      <H2 sub={`${me.email} · ${ROLE_LABEL[me.role] ?? me.role}${me.builtin ? ` · ${t("기본 계정(AVEN_ADMIN_PASSWORD)")}` : ""}`}>{t("프로필")}</H2>
        <form
          onSubmit={async (e) => {
            e.preventDefault();
            setBusy(true);
            setError(null);
            try {
              await api.updateMe({ name: name.trim() });
              await qc.invalidateQueries({ queryKey: qk.me });
              setDone(true);
              toast(t("저장했습니다."));
            } catch (err) {
              setError(err);
            } finally {
              setBusy(false);
            }
          }}
        >
          <Card
            title={t("이름")}
            description={t("멤버 목록과 감사 기록에 표시됩니다.")}
            footer={
              <Button type="submit" variant="primary" loading={busy} disabled={!name.trim() || name.trim() === me.name}>
                {t("저장")}
              </Button>
            }
          >
            <div className="space-y-3">
              <Input aria-label={t("이름")} value={name} onChange={(e) => (setName(e.target.value), setDone(false))} autoComplete="name" />
              <ErrorBox error={error} />
            </div>
          </Card>
        </form>
        <Card title={t("테마")} description={t("이 브라우저에만 저장됩니다. 시스템을 고르면 OS 설정을 따릅니다.")}>
          <ThemeToggle />
        </Card>
        <Card title={t("언어")} description={t("화면에 쓸 언어입니다. 이 브라우저에만 저장됩니다.")}>
          <LanguageSelect className="w-48" />
        </Card>
        <PasswordForm builtin={me.builtin} personal={[me.name, me.email]} />
    </div>
  );
}

function PasswordForm({ builtin, personal }: { builtin: boolean; personal: string[] }) {
  const [cur, setCur] = useState("");
  const [next, setNext] = useState("");
  const [again, setAgain] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  if (builtin)
    return (
      <Card title={t("비밀번호")} description={t("기본 admin 계정의 비밀번호는 서버의 AVEN_ADMIN_PASSWORD 환경변수입니다. 바꾸면 이 계정의 세션이 모두 끊깁니다.")} />
    );
  const mismatch = again.length > 0 && next !== again;
  const same = !!next && next === cur;
  return (
    <form
      method="post"
      onSubmit={async (e) => {
        e.preventDefault();
        if (next !== again) return setError(t("새 비밀번호 확인이 일치하지 않습니다."));
        if (new TextEncoder().encode(next).length > 256) return setError(t("비밀번호가 너무 깁니다."));
        setBusy(true);
        setError(null);
        try {
          await api.updateMe({ current_password: cur, new_password: next });
          setCur("");
          setNext("");
          setAgain("");
          toast(t("비밀번호를 바꿨습니다."));
        } catch (err) {
          setError(err);
        } finally {
          setBusy(false);
        }
      }}
    >
      <Card
        title={t("비밀번호 변경")}
        description={t("바꾸면 이 브라우저를 뺀 모든 세션이 끊깁니다.")}
        footer={
          <Button type="submit" variant="primary" loading={busy} disabled={!cur || !next || !again || mismatch || same}>
            {t("비밀번호 변경")}
          </Button>
        }
      >
        <div className="space-y-4">
          {/* Lets password managers attach the new password to the right account. */}
          <input type="text" name="username" autoComplete="username" value={personal[1] ?? ""} readOnly hidden />
          <Field label={t("현재 비밀번호")}>
            <PasswordInput name="current-password" autoComplete="current-password" value={cur} onChange={(e) => setCur(e.target.value)} />
          </Field>
          <Field label={t("새 비밀번호")} hint={same ? <span className="text-warn">{t("현재 비밀번호와 같습니다.")}</span> : undefined}>
            <PasswordInput name="new-password" autoComplete="new-password" maxLength={256} value={next} onChange={(e) => setNext(e.target.value)} aria-describedby="account-password-strength" />
            <PasswordStrengthMeter id="account-password-strength" password={next} personal={personal} />
          </Field>
          <Field label={t("새 비밀번호 확인")} hint={mismatch ? <span className="text-danger">{t("새 비밀번호 확인이 일치하지 않습니다.")}</span> : undefined}>
            <PasswordInput name="confirm-password" autoComplete="new-password" maxLength={256} value={again} onChange={(e) => setAgain(e.target.value)} invalid={mismatch} aria-invalid={mismatch || undefined} />
          </Field>
          <ErrorBox error={error} />
        </div>
      </Card>
    </form>
  );
}

/* ── 2FA ─────────────────────────────────────────────────────────────── */

function RecoveryCodes({ codes, onDone }: { codes: string[]; onDone: () => void }) {
  const download = () => {
    const text = `${t("Aven 복구 코드")} (${window.location.host})\n${new Date().toISOString().slice(0, 10)}\n\n${codes.join("\n")}\n`;
    const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = "aven-recovery-codes.txt";
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 0);
  };
  return (
    <div className="space-y-3">
      <p className="text-sm text-gray-600">{t("복구 코드는 지금 한 번만 보여 줍니다. 인증 앱이나 패스키를 잃어버렸을 때 코드 하나로 한 번 로그인할 수 있습니다.")}</p>
      <ul aria-label={t("복구 코드")} className="grid max-w-[360px] grid-cols-2 gap-1.5 rounded-md border border-gray-200 bg-gray-100 p-3 font-mono text-sm">
        {codes.map((c) => (
          <li key={c}>{c}</li>
        ))}
      </ul>
      <CopyField value={codes.join("\n")} label={t("모두 복사")} className="max-w-[360px]" />
      <div className="flex flex-wrap gap-2">
        <Button onClick={download}>
          <Download aria-hidden /> {t("텍스트 파일로 저장")}
        </Button>
        <Button variant="ghost" onClick={onDone}>
          {t("안전한 곳에 저장했습니다")}
        </Button>
      </div>
    </div>
  );
}

function Security() {
  const qc = useQueryClient();
  const me = useMe().data!;
  const [setup, setSetup] = useState<{ secret: string; otpauth_url: string } | null>(null);
  const [code, setCode] = useState("");
  const [pw, setPw] = useState("");
  const [codes, setCodes] = useState<string[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const run = async (f: () => Promise<void>) => {
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
  const totpCode = code.replace(/\s/g, "");
  return (
    <div className={STACK}>
      <H2 sub={t("로그인할 때 비밀번호와 함께 인증 앱(TOTP)의 6자리 코드나 패스키로 한 번 더 확인합니다.")}>{t("2단계 인증")}</H2>
        {me.mfa_setup_required && (
          <div>
            <Notice>{t("작업 공간에서 2단계 인증을 요구합니다. 인증 앱 또는 패스키를 설정해야 프로젝트와 작업 공간을 이용할 수 있습니다.")}</Notice>
          </div>
        )}
        {codes && (
          <Card title={t("복구 코드")}>
            <RecoveryCodes codes={codes} onDone={() => setCodes(null)} />
          </Card>
        )}
        <PasskeysSettings onRecoveryCodes={setCodes} />
        <Card title={t("인증 앱 (TOTP)")} description={me.totp_enabled ? t("인증 앱이 연결되어 있습니다.") : t("아직 켜지 않았습니다.")}>
          <div className="space-y-5">
            {!me.totp_enabled && !setup && (
              <Button variant="primary" loading={busy} onClick={() => run(async () => setSetup(await api.totpSetup()))}>
                {t("인증 앱 설정")}
              </Button>
            )}
            {!me.totp_enabled && setup && (
              <form
                className="space-y-4"
                onSubmit={(e) => {
                  e.preventDefault();
                  run(async () => {
                    const r = await api.totpEnable(totpCode);
                    setCodes(r.recovery_codes);
                    setSetup(null);
                    setCode("");
                    await qc.invalidateQueries(); // 2FA 강제로 막혀 있던 쿼리도 다시 불러온다
                  });
                }}
              >
                <p className="text-sm text-gray-600">
                  {t("인증 앱(1Password, Google Authenticator 등)으로 QR 코드를 찍거나, 아래 비밀 키를 직접 입력하거나 otpauth URL을 붙여 넣은 뒤, 앱이 보여 주는 코드를 입력하세요.")}
                </p>
                <QrCode value={setup.otpauth_url} alt={t("2단계 인증 설정용 QR 코드. 인증 앱으로 찍으면 이 계정이 추가됩니다. 찍을 수 없으면 아래 비밀 키를 입력하세요.")} className="rounded-md" />
                <Field label={t("비밀 키")}>
                  <CopyField value={setup.secret} />
                </Field>
                <Field label="otpauth URL">
                  <CopyField value={setup.otpauth_url} secret />
                </Field>
                <Field label={t("인증 코드")}>
                  <Input inputMode="numeric" autoComplete="one-time-code" maxLength={8} value={code} onChange={(e) => setCode(e.target.value)} placeholder="123456" className="max-w-[200px] font-mono tracking-[0.2em]" />
                </Field>
                <div className="flex gap-2">
                  <Button type="submit" variant="primary" loading={busy} disabled={!/^\d{6}$/.test(totpCode)}>
                    {t("켜기")}
                  </Button>
                  <Button variant="ghost" disabled={busy} onClick={() => (setSetup(null), setCode(""), setError(null))}>
                    {t("취소")}
                  </Button>
                </div>
              </form>
            )}
            {me.totp_enabled && (
              <>
                <Field label={t("인증 코드 또는 현재 비밀번호")} hint={t("복구 코드를 다시 만들려면 인증 앱 코드가 필요합니다. 끄려면 코드, 복구 코드 또는 현재 비밀번호를 입력하세요.")}>
                  <div className="flex max-w-[420px] gap-2">
                    <Input inputMode="numeric" autoComplete="one-time-code" maxLength={32} value={code} onChange={(e) => setCode(e.target.value)} placeholder="123456" className="font-mono" aria-label={t("인증 코드")} />
                    <Input type="password" autoComplete="current-password" value={pw} onChange={(e) => setPw(e.target.value)} placeholder={t("현재 비밀번호")} aria-label={t("현재 비밀번호")} />
                  </div>
                </Field>
                <div className="flex gap-2">
                  <Button loading={busy} disabled={!/^\d{6}$/.test(totpCode)} onClick={() => run(async () => setCodes((await api.totpRecoveryCodes({ code: totpCode })).recovery_codes))}>
                    {t("복구 코드 다시 만들기")}
                  </Button>
                  <Button
                    variant="danger"
                    loading={busy}
                    disabled={!totpCode && !pw}
                    onClick={() =>
                      run(async () => {
                        await api.totpDisable(totpCode ? { code: totpCode } : { password: pw });
                        setCode("");
                        setPw("");
                        setCodes(null);
                        await qc.invalidateQueries({ queryKey: qk.me });
                      })
                    }
                  >
                    {t("인증 앱 끄기")}
                  </Button>
                </div>
              </>
            )}
            {!me.totp_enabled && me.mfa_enabled && (
              <form
                className="space-y-3 border-t border-gray-200 pt-4"
                onSubmit={(e) => {
                  e.preventDefault();
                  run(async () => {
                    setCodes((await api.totpRecoveryCodes({ password: pw })).recovery_codes);
                    setPw("");
                  });
                }}
              >
                <Field label={t("복구 코드")} hint={t("패스키를 잃어버렸을 때 쓰는 일회용 코드입니다. 다시 만들면 예전 코드는 쓸 수 없습니다.")}>
                  <Input type="password" autoComplete="current-password" value={pw} onChange={(e) => setPw(e.target.value)} placeholder={t("현재 비밀번호")} aria-label={t("현재 비밀번호")} className="max-w-[320px]" />
                </Field>
                <Button type="submit" loading={busy} disabled={!pw}>
                  {t("복구 코드 다시 만들기")}
                </Button>
              </form>
            )}
            <ErrorBox error={error} />
          </div>
        </Card>
    </div>
  );
}

/* ── sessions ────────────────────────────────────────────────────────── */

/** Short, readable client name from a User-Agent ("Chrome · macOS", "aven CLI"). Falls back to the raw string. */
function describeClient(ua: string): { label: string; cli: boolean } {
  if (!ua) return { label: t("알 수 없는 클라이언트"), cli: false };
  if (/^(aven|keel)(-cli)?\//i.test(ua)) return { label: "Aven CLI", cli: true };
  if (/^(Go-http-client|curl|python-requests|node-fetch|undici|axios)/i.test(ua)) return { label: ua.split(" ")[0], cli: true };
  const browser = /Edg\//.test(ua) ? "Edge" : /OPR\/|Opera/.test(ua) ? "Opera" : /Firefox\//.test(ua) ? "Firefox" : /Chrome\//.test(ua) ? "Chrome" : /Safari\//.test(ua) ? "Safari" : "";
  const os = /iPhone|iPad|iPod/.test(ua) ? "iOS" : /Android/.test(ua) ? "Android" : /Mac OS X|Macintosh/.test(ua) ? "macOS" : /Windows/.test(ua) ? "Windows" : /CrOS/.test(ua) ? "ChromeOS" : /Linux/.test(ua) ? "Linux" : "";
  return { label: browser || os ? [browser, os].filter(Boolean).join(" · ") : ua, cli: false };
}

function Sessions() {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: qk.sessions, queryFn: api.sessions });
  const [error, setError] = useState<unknown>(null);
  const [pending, setPending] = useState<string | null>(null);
  const { confirm, node } = useConfirm();
  const list = [...(q.data ?? [])].sort((a, b) => Number(b.current) - Number(a.current) || b.last_seen.localeCompare(a.last_seen));
  const others = list.filter((s) => !s.current);
  const revoke = async (ids: string[], key: string) => {
    setPending(key);
    setError(null);
    try {
      // Sequential on purpose: one failure should not hide which sessions are already gone.
      for (const id of ids) await api.deleteSession(id);
    } catch (err) {
      setError(err);
    } finally {
      setPending(null);
      await qc.invalidateQueries({ queryKey: qk.sessions });
    }
  };
  return (
    <Card
      title={t("세션")}
      description={t("로그인한 브라우저와 CLI입니다. 30일 동안 쓰지 않으면 만료됩니다.")}
      action={
        others.length > 0 && (
          <Button
            size="sm"
            variant="danger-outline"
            loading={pending === "all"}
            disabled={!!pending}
            onClick={() =>
              confirm({
                title: t("다른 세션 모두 끊기"),
                message: t("이 브라우저를 뺀 {count}개 세션이 로그아웃됩니다.", { count: others.length }),
                confirmLabel: t("모두 끊기"),
                danger: true,
                run: () => revoke(others.map((s) => s.id), "all"),
              })
            }
          >
            {t("다른 세션 모두 끊기")}
          </Button>
        )
      }
    >
      {node}
      <ErrorBox error={q.error ?? error} className="mb-3" />
      {q.isPending ? (
        <CenterSpinner />
      ) : (
        <RowList>
          {list.map((s) => {
            const client = describeClient(s.user_agent);
            return (
              <li key={s.id} className="flex items-center gap-3 px-4 py-3">
                <Monitor className="size-4 shrink-0 text-gray-500" aria-hidden />
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2 text-sm">
                    <span className="truncate" title={s.user_agent || undefined}>{client.label}</span>
                    {s.current && <span className="shrink-0 rounded bg-success-surface px-1.5 py-px text-xs text-success">{t("지금 이 세션")}</span>}
                  </div>
                  <div className="text-xs tabular-nums text-gray-500">
                    <span className="font-mono">{s.ip || "-"}</span> · {t("마지막 사용 {time}", { time: fmtRelative(s.last_seen) })}
                  </div>
                </div>
                {!s.current && (
                  <Button size="sm" loading={pending === s.id} disabled={!!pending} onClick={() => revoke([s.id], s.id)} aria-label={t("{name} 세션 끊기", { name: client.label })}>
                    {t("끊기")}
                  </Button>
                )}
              </li>
            );
          })}
        </RowList>
      )}
    </Card>
  );
}

/* ── API tokens ──────────────────────────────────────────────────────── */

function Tokens() {
  const qc = useQueryClient();
  const me = useMe().data!;
  const q = useQuery({ queryKey: qk.tokens, queryFn: api.tokens });
  const projects = useProjects();
  const [name, setName] = useState("");
  const [scope, setScope] = useState<TokenScope>("environment");
  const [pid, setPid] = useState("");
  const [wsid, setWsid] = useState("");
  const [eid, setEid] = useState("");
  const [role, setRole] = useState<ProjectRole>("member");
  const [days, setDays] = useState("");
  // The secret is only kept in memory and only for as long as this section is open.
  const [created, setCreated] = useState<{ name: string; token: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const { confirm, node } = useConfirm();
  const project = useProject((scope === "project" || scope === "environment") ? pid : null);
  useEffect(() => {
    if (!pid && projects.data?.[0]) setPid(projects.data[0].id);
  }, [pid, projects.data]);
  useEffect(() => {
    setEid(project.data?.default_environment_id ?? "");
  }, [project.data]);
  const selectedWorkspace = (wsid ? me.workspaces?.find((w) => w.id === wsid)?.id : me.workspaces?.find((w) => w.personal)?.id ?? me.workspaces?.[0]?.id) ?? "";
  const targetRole = scope === "instance" ? (me.instance_admin ? "admin" : undefined)
    : scope === "workspace" ? me.workspaces?.find((w) => w.id === selectedWorkspace)?.role : me.projects[pid];
  const allowAdmin = scope !== "environment" && (targetRole === "owner" || targetRole === "admin");
  const allowMember = !!targetRole && targetRole !== "viewer";
  useEffect(() => {
    if (role === "admin" && !allowAdmin) setRole(allowMember ? "member" : "viewer");
    else if (role === "member" && !allowMember) setRole("viewer");
  }, [role, allowAdmin, allowMember]);
  const pname = (id: string | null) => projects.data?.find((p) => p.id === id)?.name ?? id ?? "";
  const usingToken = me.auth === "token";
  const daysValid = days === "" || (/^\d+$/.test(days) && Number(days) >= 1 && Number(days) <= 3650);
  return (
    <div className={STACK}>
      {node}
      <H2 sub={t("CLI(AVEN_TOKEN)와 CI에서 쓰는 토큰입니다. 토큰은 만든 사람의 권한을 넘지 못하고, environment 토큰은 그 환경의 리소스만 바꿀 수 있습니다.")}>
        {t("API 토큰")}
      </H2>
      {created && (
        <Card
          title={t("새 토큰: {name}", { name: created.name })}
          description={t("토큰을 만들었습니다. 지금 한 번만 보여 줍니다.")}
          footer={
            <Button variant="primary" onClick={() => setCreated(null)}>
              {t("복사했습니다")}
            </Button>
          }
        >
          <div className="space-y-3">
            <CopyField value={created.token} secret label={t("토큰")} />
            <p className="text-xs text-gray-600">
              {t("이 창을 닫으면 다시 볼 수 없습니다. CI 비밀 변수나 비밀번호 관리자에 바로 넣고, 코드나 채팅에는 붙여 넣지 마세요.")}
            </p>
          </div>
        </Card>
      )}
      {usingToken ? (
        <p className="text-sm text-gray-600">{t("API 토큰으로 로그인한 상태에서는 토큰을 만들 수 없습니다.")}</p>
      ) : (
        <form
          onSubmit={async (e) => {
            e.preventDefault();
            setBusy(true);
            setError(null);
            try {
              const tok = await api.createToken({
                name: name.trim(),
                role, scope,
                workspace_id: scope === "workspace" ? selectedWorkspace : undefined,
                project_id: (scope === "project" || scope === "environment") ? pid : undefined,
                environment_id: scope === "environment" ? eid : undefined,
                expires_in_days: days ? Number(days) : null,
              });
              setCreated({ name: name.trim(), token: tok.token });
              setName("");
              await qc.invalidateQueries({ queryKey: qk.tokens });
            } catch (err) {
              setError(err);
            } finally {
              setBusy(false);
            }
          }}
        >
          <Card
            title={t("새 토큰")}
            footer={
              <Button type="submit" variant="primary" loading={busy} disabled={!name.trim() || !daysValid || !targetRole || ((scope === "project" || scope === "environment") && !pid) || (scope === "environment" && !eid) || (scope === "workspace" && !selectedWorkspace)}>
                {t("토큰 만들기")}
              </Button>
            }
          >
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field label={t("이름")}>
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="github-actions" maxLength={100} autoComplete="off" />
          </Field>
          <Field label={t("범위")}>
            <Select aria-label={t("토큰 범위")} value={scope} onChange={(e) => setScope(e.target.value as TokenScope)}>
              <option value="environment">{t("환경")}</option>
              <option value="project">{t("프로젝트")}</option>
              <option value="workspace">{t("작업 공간")}</option>
              {me.instance_admin && <option value="instance">{t("인스턴스 운영")}</option>}
            </Select>
          </Field>
          {scope === "workspace" && (
            <Field label={t("작업 공간")}>
              <Select aria-label={t("토큰 작업 공간")} value={selectedWorkspace} onChange={(e) => setWsid(e.target.value)}>
                {(me.workspaces ?? []).map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}
              </Select>
            </Field>
          )}
          {(scope === "project" || scope === "environment") && (
            <Field label={t("프로젝트")}>
              <Select value={pid} onChange={(e) => setPid(e.target.value)}>
                {(projects.data ?? []).map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </Select>
            </Field>
          )}
          {scope === "environment" && (
            <Field label={t("환경")}>
              <Select value={eid} onChange={(e) => setEid(e.target.value)}>
                {(project.data?.environments ?? []).map((env) => (
                  <option key={env.id} value={env.id}>
                    {env.name}
                  </option>
                ))}
              </Select>
            </Field>
          )}
          <Field label={t("역할")}>
            <Select aria-label={t("토큰 역할")} value={role} onChange={(e) => setRole(e.target.value as ProjectRole)}>
              {allowAdmin && <option value="admin">Admin</option>}
              {allowMember && <option value="member">Member</option>}
              <option value="viewer">Viewer</option>
            </Select>
          </Field>
          <Field label={t("만료(일)")} hint={daysValid ? t("비우면 만료되지 않습니다.") : <span className="text-danger">{t("1에서 3650 사이의 일 수를 입력하세요.")}</span>}>
            <Input type="number" inputMode="numeric" min={1} max={3650} step={1} invalid={!daysValid} value={days} onChange={(e) => setDays(e.target.value)} placeholder="90" />
          </Field>
          <div className="sm:col-span-2">
            <ErrorBox error={error} />
          </div>
          </div>
          </Card>
        </form>
      )}
      <Card title={t("토큰")} action={q.data && <span className="text-sm tabular-nums text-gray-500">{t("{count}개", { count: q.data.length })}</span>}>
      <ErrorBox error={q.error} className="mb-3" />
      {q.isPending ? (
        <CenterSpinner />
      ) : !q.data?.length ? (
        <EmptyState icon={<KeyRound className="size-5" />} title={t("토큰이 없습니다")} />
      ) : (
        <RowList>
          {q.data.map((tok) => (
            <li key={tok.id} className="flex items-center gap-3 px-4 py-3">
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium">
                  {tok.name} <span className="font-mono text-xs text-gray-500">{tok.prefix}…</span>
                </div>
                <div className="text-xs tabular-nums text-gray-500">
                  {tok.scope === "instance" ? t("인스턴스 운영") : tok.scope === "workspace" ? (me.workspaces?.find((w) => w.id === tok.workspace_id)?.name ?? tok.workspace_id ?? t("작업 공간")) : pname(tok.project_id)}
                  {tok.scope === "environment" && ` · ${t("환경 {id}", { id: tok.environment_id ?? "" })}`} · {ROLE_LABEL[tok.role]} · {t("마지막 사용")}{" "}
                  {tok.last_used_at ? fmtRelative(tok.last_used_at) : t("없음")}
                  {tok.expires_at && ` · ${t("만료 {date}", { date: tok.expires_at.slice(0, 10) })}`}
                </div>
              </div>
              <Button
                size="sm"
                variant="danger"
                onClick={() =>
                  confirm({
                    title: t("토큰 폐기"),
                    message: t("{name} 토큰을 쓰는 CLI/CI는 바로 인증에 실패합니다.", { name: tok.name }),
                    confirmLabel: t("폐기"),
                    danger: true,
                    run: async () => {
                      await api.deleteToken(tok.id);
                      await qc.invalidateQueries({ queryKey: qk.tokens });
                    },
                  })
                }
              >
                {t("폐기")}
              </Button>
            </li>
          ))}
        </RowList>
      )}
      </Card>
      <GraphQLApiCard />
    </div>
  );
}

/* ── notification preferences (v1.5) ────────────────────────────────── */

function NotificationPrefs() {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: qk.notificationPrefs, queryFn: api.notificationPrefs });
  const [text, setText] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [done, setDone] = useState(false);
  if (q.isPending) return <CenterSpinner />;
  if (!q.data) return <ErrorBox error={q.error} className="max-w-3xl" />;
  const value = text ?? q.data.events.join("\n");
  const events = value
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean);
  const has = (k: string) => events.includes(k);
  const toggle = (k: string) => {
    setDone(false);
    setText((has(k) ? events.filter((e) => e !== k) : [...events, k]).join("\n"));
  };
  return (
    <div className={STACK}>
      <H2 sub={t("볼 수 있는 프로젝트에서 아래 이벤트가 생기면 상단 종 아이콘의 알림함에 넣습니다. 패턴에 *를 쓸 수 있습니다(deploy.*, *.failed, *).")}>
        {t("알림")}
      </H2>
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          setError(null);
          try {
            const r = await api.setNotificationPrefs(events);
            qc.setQueryData(qk.notificationPrefs, r);
            setText(null);
            setDone(true);
            toast(t("저장했습니다."));
          } catch (err) {
            setError(err);
          } finally {
            setBusy(false);
          }
        }}
      >
        <Card
          title={t("알림함 이벤트")}
          footer={
            <>
              <Button variant="ghost" onClick={() => (setText(q.data.defaults.join("\n")), setDone(false))}>
                {t("기본값으로")}
              </Button>
              <Button type="submit" variant="primary" loading={busy}>
                {t("저장")}
              </Button>
            </>
          }
        >
        <div className="space-y-5">
        <Field label={t("받을 이벤트")} hint={t("한 줄에 하나씩 또는 쉼표로 구분합니다. 비우면 알림함에 아무것도 넣지 않습니다.")}>
          <Textarea rows={5} mono value={value} onChange={(e) => (setText(e.target.value), setDone(false))} aria-label={t("받을 이벤트 패턴")} className="w-full" />
        </Field>
        <div>
          <div className={LABEL_CLS}>{t("빠른 선택")}</div>
          <div className="flex flex-wrap gap-1.5">
            {["deploy.*", "alert.*", ...q.data.available.filter((k) => !k.startsWith("alert."))].map((k) => (
              <button
                key={k}
                type="button"
                aria-pressed={has(k)}
                onClick={() => toggle(k)}
                className={cx(
                  "cursor-pointer rounded-md border px-2 py-0.5 font-mono text-xs transition-colors",
                  has(k) ? "border-pink-500 bg-pink-500/15 text-foreground" : "border-gray-200 bg-gray-100 text-gray-600 hover:text-foreground",
                )}
              >
                {k}
              </button>
            ))}
          </div>
        </div>
        <ErrorBox error={error} />
        </div>
        </Card>
      </form>
    </div>
  );
}

/* ── page ────────────────────────────────────────────────────────────── */

function AccountInner() {
  const me = useMe();
  const params = useSearchParams();
  const router = useRouter();
  const href = useCtxHref();
  const raw = params.get("section");
  const section = NAV.find((n) => n.id === raw)?.id ?? "profile";
  const go = (id: SectionId) => router.replace(href("/account/", { section: id === "profile" ? null : id }), { scroll: false });
  useEffect(() => {
    if (raw === "oauth-apps") router.replace("/admin/?section=oauth-apps");
  }, [raw, router]);
  useEffect(() => {
    document.title = `${t("계정")} · Aven`;
    return () => {
      document.title = "Aven";
    };
  }, []);
  return (
    <PageShell
      title={t("계정")}
      close={{ href: href("/"), label: t("계정 닫기") }}
    >
      <SideNavLayout label={t("계정 메뉴")} items={NAV} value={section} onChange={go}>
            {!me.data ? (
              <ErrorBox error={me.error} />
            ) : me.data.auth === "token" && section !== "tokens" ? (
              <p className="text-sm text-gray-600">{t("API 토큰으로 로그인한 상태에서는 계정 설정을 바꿀 수 없습니다.")}</p>
            ) : section === "profile" ? (
              <Profile />
            ) : section === "security" ? (
              <Security />
            ) : section === "notifications" ? (
              <NotificationPrefs />
            ) : section === "sessions" ? (
              <Sessions />
            ) : section === "integrations" ? (
              <div className="max-w-3xl">
                <GitHubConnectionCard />
              </div>
            ) : section === "authorized-apps" ? (
              <AuthorizedAppsSection />
            ) : (
              <Tokens />
            )}
      </SideNavLayout>
    </PageShell>
  );
}

export default function AccountPage() {
  return (
    <Suspense fallback={<div className="dot-grid min-h-full" />}>
      <AccountInner />
    </Suspense>
  );
}
