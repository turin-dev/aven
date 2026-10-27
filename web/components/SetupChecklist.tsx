"use client";

import Link from "next/link";
import { useState, type ReactNode } from "react";
import { usePathname } from "next/navigation";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { BookOpen, CheckCircle2, Circle, RefreshCw, Rocket } from "lucide-react";
import { api, type Me, type SetupItemID, type SetupStatus } from "@/lib/api";
import { qk } from "@/lib/queries";
import { toast } from "@/lib/uiState";
import { GitHubOnboarding } from "./GitHubOnboarding";
import { SelfServiceSetupAction } from "./SelfService";
import { Button, Card, CenterSpinner, CopyField, Dialog, ErrorBox, Toggle, cx } from "./ui";
import { t, tr } from "@/lib/i18n";

// 초기 설정 체크리스트(서버 owner 전용, GET /api/owner/setup). 완료 여부는 서버가 계산하고 문구는 여기서 id로 고른다.

export const SETUP_GUIDE_URL = "https://github.com/turin-dev/aven/blob/main/docs/self-hosting.md";

type ItemCopy = { title: string; body: ReactNode; action?: (s: SetupStatus) => ReactNode };

const Env = ({ children }: { children: ReactNode }) => <code className="rounded bg-gray-100 px-1 py-0.5 font-mono text-xs">{children}</code>;
const Go = ({ href, children }: { href: string; children: ReactNode }) => (
  <Link href={href}><Button size="sm">{children}</Button></Link>
);

const COPY: Record<SetupItemID, ItemCopy> = {
  public_url: {
    title: t("공개 URL"),
    body: tr("대시보드, GitHub OAuth·웹훅, agent 설치 명령이 쓰는 주소입니다. {file}의 {env}에 https:// 주소를 넣고 aven-server를 재시작하세요.", {
      file: <Env>/etc/keel/aven.env</Env>,
      env: <Env>KEEL_PUBLIC_URL</Env>,
    }),
    action: (s) => s.server.public_url ? <span className="font-mono text-xs text-gray-500">{s.server.public_url}</span> : null,
  },
  base_domain: {
    title: t("기본 도메인"),
    body: tr("서비스에 자동으로 붙는 {svcDomain} 주소의 뿌리입니다. {baseEnv}을 설정하고 DNS에 와일드카드 레코드({wildcard}의 A/AAAA)를 이 서버로 향하게 하세요.", {
      svcDomain: <Env>{t("서비스.기본도메인")}</Env>,
      baseEnv: <Env>KEEL_BASE_DOMAIN</Env>,
      wildcard: <Env>{t("*.기본도메인")}</Env>,
    }),
    action: (s) => s.server.base_domain ? <span className="font-mono text-xs text-gray-500">*.{s.server.base_domain}</span> : null,
  },
  owner_account: {
    title: t("개인 owner 계정"),
    body: tr("{admin} 기본 계정은 {password}로 로그인하는 비상용 계정입니다. 내 이메일로 계정을 만들고(사용자·초대) 서버 owner로 지정한 뒤(Owner · 권한) 그 계정으로 작업하세요.", {
      admin: <Env>admin@local</Env>,
      password: <Env>KEEL_ADMIN_PASSWORD</Env>,
    }),
    action: () => <div className="flex gap-2"><Go href="/admin/?section=users">{t("사용자·초대")}</Go><Go href="/admin/?section=owner-access">{t("Owner · 권한")}</Go></div>,
  },
  mfa: {
    title: t("2단계 인증"),
    body: t("지금 로그인한 계정에 OTP 앱이나 패스키를 등록하세요. 서버 owner 계정은 모든 프로젝트를 다룰 수 있으니 꼭 보호해야 합니다."),
    action: () => <Go href="/account/?section=security">{t("계정 › 보안")}</Go>,
  },
  first_project: {
    title: t("첫 프로젝트 배포"),
    body: t("프로젝트를 만들고 GitHub 저장소, Docker 이미지 또는 템플릿으로 서비스를 추가해 배포해 보세요."),
    action: () => <Go href="/projects/">{t("프로젝트|plural")}</Go>,
  },
  github: {
    title: t("GitHub 연동"),
    body: tr("GitHub OAuth App을 만들고 {clientId}, {clientSecret}, 웹훅 비밀값 {webhookSecret}을 설정하면 저장소 선택, 비공개 저장소 배포, push 자동 배포를 쓸 수 있습니다. OAuth App의 callback URL은 아래 주소입니다.", {
      clientId: <Env>KEEL_GITHUB_CLIENT_ID</Env>,
      clientSecret: <Env>KEEL_GITHUB_CLIENT_SECRET</Env>,
      webhookSecret: <Env>KEEL_GITHUB_WEBHOOK_SECRET</Env>,
    }),
    action: (s) => s.server.public_url ? <CopyField value={s.server.github_callback_url} className="w-full max-w-md" /> : null,
  },
  smtp: {
    title: t("메일 발송(SMTP)"),
    body: tr("초대, 비밀번호 재설정, 알림 메일에 필요합니다. {host}, {port}, {username}, {password}, {from}을 설정하세요.", {
      host: <Env>KEEL_SMTP_HOST</Env>,
      port: <Env>KEEL_SMTP_PORT</Env>,
      username: <Env>KEEL_SMTP_USERNAME</Env>,
      password: <Env>KEEL_SMTP_PASSWORD</Env>,
      from: <Env>KEEL_SMTP_FROM</Env>,
    }),
  },
  backup_destination: {
    title: t("백업 저장소"),
    body: t("데이터베이스 서비스의 설정 › DB 백업에서 S3 호환 저장소를 백업 대상으로 추가하세요. Aven 자체 DB(사용자, 프로젝트, 설정)는 Owner · DB 백업에서 받아 둘 수 있습니다."),
    action: () => <Go href="/admin/?section=owner-backups">{t("Owner · DB 백업")}</Go>,
  },
  self_service: {
    title: t("Aven을 캔버스에서 관리"),
    body: t("aven-server 자신을 Aven 프로젝트의 서비스로 둡니다. 캔버스에서 배포하면 저장소의 새 버전으로 이 서버를 바꾸고, 정상이 되지 않으면 이전 버전으로 되돌립니다. 이 프로젝트는 서버 owner만 볼 수 있습니다."),
    action: () => <SelfServiceSetupAction />,
  },
  node: {
    title: t("서버 노드 추가"),
    body: t("다른 서버에 aven-agent를 설치하면 그 서버에도 서비스를 배치할 수 있습니다. 한 대로 운영한다면 건너뛰어도 됩니다."),
    action: () => <Go href="/admin/?section=nodes">{t("서버 노드")}</Go>,
  },
};

export function useSetupStatus(enabled = true) {
  return useQuery({ queryKey: qk.setupStatus, queryFn: api.setupStatus, enabled, staleTime: 30_000 });
}

function useDismiss() {
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  const set = async (dismissed: boolean) => {
    setBusy(true);
    try {
      await api.setSetupDismissed(dismissed);
      await qc.invalidateQueries({ queryKey: qk.setupStatus });
      return true;
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e), "error");
      return false;
    } finally {
      setBusy(false);
    }
  };
  return { busy, set };
}

function Check({ done }: { done: boolean }) {
  return done
    ? <CheckCircle2 className="size-4 shrink-0 text-success" aria-label={t("완료")} />
    : <Circle className="size-4 shrink-0 text-gray-400" aria-label={t("남음")} />;
}

/* ── 어드민 › 초기 설정 ─────────────────────────────────────────────── */

export function SetupChecklist() {
  const query = useSetupStatus();
  const dismiss = useDismiss();
  const s = query.data;
  const section = (title: string, items: SetupStatus["items"]) => (
    <Card title={title} flush>
      <ul className="divide-y divide-gray-100">
        {items.map((it) => {
          const c = COPY[it.id];
          const action = c.action?.(s!);
          return (
            <li key={it.id} className="flex gap-3 px-5 py-4">
              <div className="pt-0.5"><Check done={it.done} /></div>
              <div className="min-w-0 flex-1 space-y-2">
                <div className={cx("text-sm font-medium", it.done && "text-gray-500")}>{c.title}</div>
                <p className="text-sm text-gray-600">{c.body}</p>
                {action && <div>{action}</div>}
              </div>
            </li>
          );
        })}
      </ul>
    </Card>
  );
  return (
    <div className="space-y-8">
      <Card
        title={<span className="flex items-center gap-2"><Rocket className="size-4 text-pink-500" />{t("초기 설정")}</span>}
        description={t("새로 설치한 Aven 서버를 운영할 준비가 됐는지 확인합니다. 항목은 서버 설정을 읽어 자동으로 체크됩니다. 환경 변수를 바꾼 항목은 aven-server를 재시작해야 반영됩니다.")}
        action={<Button size="sm" loading={query.isFetching} onClick={() => void query.refetch()}><RefreshCw className="size-3.5" />{t("새로고침")}</Button>}
        footerNote={s ? (s.complete ? t("필수 항목을 모두 마쳤습니다.") : t("필수 항목 {total}개 중 {done}개 완료", { total: s.required, done: s.required_done })) : undefined}
        footer={<a href={SETUP_GUIDE_URL} target="_blank" rel="noreferrer"><Button size="sm"><BookOpen className="size-3.5" />{t("설치 가이드")}</Button></a>}
      >
        {s && (
          <label className="flex items-center justify-between gap-4 text-sm">
            <span>{t("로그인할 때 초기 설정 안내 띄우기")}<span className="block text-xs text-gray-500">{t("필수 항목이 남아 있는 동안 서버 owner에게 한 번씩 보여 줍니다.")}</span></span>
            <Toggle checked={!s.dismissed} disabled={dismiss.busy} onChange={(on) => void dismiss.set(!on)} label={t("초기 설정 안내")} />
          </label>
        )}
      </Card>
      <ErrorBox error={query.error} />
      {!s ? query.isPending && <CenterSpinner /> : <>
        {section(t("필수"), s.items.filter((i) => !i.optional))}
        {section(t("선택"), s.items.filter((i) => i.optional))}
      </>}
    </div>
  );
}

/* ── 첫 로그인 안내 ─────────────────────────────────────────────────── */

const laterKey = "keel.setup-onboarding.later";

/**
 * 서버 owner가 로그인했을 때 필수 초기 설정이 남아 있으면 한 번 안내한다. "나중에"는 이 브라우저 세션 동안만,
 * "다시 보지 않기"는 서버에 저장해 모든 owner에게 다시 띄우지 않는다(어드민 › 초기 설정에서 다시 켤 수 있다).
 * 이 안내가 뜨지 않을 때만 GitHub 연결 안내를 보여 줘서 두 창이 겹치지 않게 한다.
 */
export function FirstRunOnboarding({ me }: { me: Me | undefined }) {
  const pathname = usePathname() || "/";
  const eligible = !!me && me.auth === "session" && me.instance_owner === true && !me.mfa_setup_required && !me.view_as;
  const query = useSetupStatus(eligible);
  const dismiss = useDismiss();
  const [later, setLater] = useState(() => {
    try {
      return typeof window !== "undefined" && window.sessionStorage.getItem(laterKey) === "1";
    } catch {
      return false;
    }
  });
  const s = query.data;
  const open = eligible && !!s && !s.complete && !s.dismissed && !later && !pathname.startsWith("/admin") && !pathname.startsWith("/account");
  if (eligible && (query.isPending || open)) {
    const close = () => {
      try {
        window.sessionStorage.setItem(laterKey, "1");
      } catch {
        /* ignore */
      }
      setLater(true);
    };
    if (!s) return null;
    return (
      <Dialog
        open={open}
        onClose={close}
        title={t("Aven 초기 설정")}
        footer={
          <>
            <Button loading={dismiss.busy} onClick={async () => { if (await dismiss.set(true)) close(); }}>{t("다시 보지 않기")}</Button>
            <Button onClick={close}>{t("나중에")}</Button>
            <Link href="/admin/?section=setup" onClick={close}><Button variant="primary">{t("초기 설정 열기")}</Button></Link>
          </>
        }
      >
        <div className="space-y-4 text-sm text-gray-600">
          <p>{t("서버 owner로 로그인했습니다. 운영을 시작하기 전에 필수 항목 {required}개 중 {remaining}개가 남아 있습니다.", { required: s.required, remaining: s.required - s.required_done })}</p>
          <ul className="space-y-2">
            {s.items.filter((i) => !i.optional).map((it) => (
              <li key={it.id} className="flex items-center gap-2">
                <Check done={it.done} />
                <span className={cx(it.done ? "text-gray-500 line-through" : "text-gray-900")}>{COPY[it.id].title}</span>
              </li>
            ))}
          </ul>
          <p className="text-xs text-gray-500">{t("언제든 어드민 › 초기 설정에서 다시 볼 수 있습니다.")}</p>
        </div>
      </Dialog>
    );
  }
  return <GitHubOnboarding me={me} />;
}
