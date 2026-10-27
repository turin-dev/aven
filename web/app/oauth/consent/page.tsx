"use client";

import { useEffect, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowLeftRight, Check, ExternalLink } from "lucide-react";
import { api, type ID, type OAuthConsentRequest } from "@/lib/api";
import { Avatar, Button, ErrorBox, AvenMark, Spinner, cx } from "@/components/ui";
import { t, tr } from "@/lib/i18n";

/**
 * OAuth 동의 화면: /oauth/consent/#request=keel_req_…
 * 요청 토큰은 fragment에 두어 서버 로그·Referer에 남지 않게 한다(/oauth/auth가 여기로 보낸다).
 */
/**
 * OAuth 동의 화면: /oauth/consent/#request=aven_req_…
 * 요청 토큰은 fragment에 두어 서버 로그·Referer에 남지 않게 한다(/oauth/auth가 여기로 보낸다).
 */
export default function OAuthConsentPage() {
  const [requestID, setRequestID] = useState<string | null>(null);
  useEffect(() => {
    const m = /(?:^|[#&])request=([^&]+)/.exec(window.location.hash);
    setRequestID(m ? decodeURIComponent(m[1]) : "");
    document.title = t("앱 연결 · Aven");
  }, []);
  if (requestID === null) return <Shell><Loading /></Shell>;
  if (!requestID) return <ConsentError message={t("OAuth 요청을 찾을 수 없습니다. 앱에서 다시 연결해 주세요.")} />;
  return <ConsentForm requestID={requestID} />;
}

function ConsentForm({ requestID }: { requestID: string }) {
  const query = useQuery({ queryKey: ["oauth-consent", requestID], queryFn: () => api.oauthConsentRequest(requestID), retry: false, staleTime: Infinity });
  const [scopes, setScopes] = useState<string[]>([]);
  const [projects, setProjects] = useState<ID[]>([]);
  const [busy, setBusy] = useState<"approve" | "deny" | null>(null);
  const [error, setError] = useState<unknown>(null);

  useEffect(() => {
    if (!query.data) return;
    setScopes(query.data.scopes.map((s) => s.name));
    const available = new Set(query.data.projects.map((p) => p.id));
    setProjects(query.data.previous_project_ids.filter((id) => available.has(id)));
  }, [query.data]);

  if (query.isPending) return <Shell><Loading /></Shell>;
  if (!query.data) return <ConsentError message={t("요청이 만료되었거나 이미 처리되었습니다. 앱에서 다시 연결해 주세요.")} />;
  const data: OAuthConsentRequest = query.data;
  const projectScope = scopes.some((s) => s.startsWith("project:"));
  const workspaceScope = scopes.find((s) => s.startsWith("workspace:"));
  const canApprove = scopes.includes("openid") && (!projectScope || projects.length > 0);
  const toggle = <T,>(list: T[], item: T) => (list.includes(item) ? list.filter((v) => v !== item) : [...list, item]);

  const finish = async (decision: "approve" | "deny") => {
    setBusy(decision);
    setError(null);
    try {
      const res = await api.finishOAuthConsent(requestID, decision === "approve" ? { decision, scopes, project_ids: projectScope ? projects : [] } : { decision });
      window.location.replace(res.redirect_uri);
    } catch (err) {
      setError(err);
      setBusy(null);
    }
  };

  return (
    <Shell>
      <div className="mb-6 flex flex-col items-center text-center">
        <div className="mb-4 flex items-center gap-3 text-gray-400">
          <span className="flex size-12 items-center justify-center rounded-xl border border-gray-200 bg-gray-100 text-lg font-semibold text-foreground">
            {(data.app.name.trim()[0] ?? "?").toUpperCase()}
          </span>
          <ArrowLeftRight className="size-4" />
          <AvenMark className="size-12" />
        </div>
        <h1 className="text-xl font-semibold">{t("{app}이(가) Aven 계정 접근을 요청합니다", { app: data.app.name })}</h1>
        {data.app.description && <p className="mt-2 max-w-md text-sm text-gray-600">{data.app.description}</p>}
      </div>

      <div className="mb-5 flex items-center gap-3 rounded-md border border-gray-200 px-3 py-2">
        <Avatar name={data.user.name || data.user.email} size={28} />
        <div className="min-w-0 flex-1 text-sm">
          <div className="truncate font-medium">{data.user.name}</div>
          <div className="truncate text-xs text-gray-500">{data.user.email}</div>
        </div>
      </div>

      <Section title={t("요청한 권한")}>
        {[...data.scopes].sort((a, b) => scopeRank(a.name) - scopeRank(b.name)).map((s) => {
          const required = s.name === "openid";
          const on = scopes.includes(s.name);
          return (
            <label key={s.name} className={cx("flex items-start gap-3 rounded-md border border-gray-200 p-3", required ? "cursor-default" : "cursor-pointer hover:bg-gray-100")}>
              <input type="checkbox" className="mt-1 accent-pink-500" checked={on} disabled={required || !!busy} onChange={() => setScopes((cur) => toggle(cur, s.name))} />
              <span className="min-w-0 flex-1">
                <span className="block text-sm text-foreground">{s.description}</span>
                <span className="mt-0.5 block font-mono text-xs text-gray-500">
                  {s.name}
                  {required && t(" · 필수")}
                </span>
              </span>
            </label>
          );
        })}
      </Section>

      {workspaceScope && (
        <p className="mt-4 rounded-md bg-gray-100 p-3 text-sm text-gray-600">
          {t("작업 공간 scope는 내가 멤버인 작업 공간에 적용됩니다. 앱의 실제 권한은 각 공간에서의 내 역할을 넘지 않으며, 인스턴스 운영 권한은 부여되지 않습니다.")}
        </p>
      )}

      {projectScope && (
        <Section title={t("공유할 프로젝트")} hint={t("선택한 프로젝트만 앱에 공유됩니다. 권한은 각 프로젝트에서의 내 역할 이하로 적용됩니다.")}>
          {data.projects.length ? (
            data.projects.map((p) => (
              <label key={p.id} className="flex cursor-pointer items-center gap-3 rounded-md border border-gray-200 p-3 hover:bg-gray-100">
                <input type="checkbox" className="accent-pink-500" checked={projects.includes(p.id)} disabled={!!busy} onChange={() => setProjects((cur) => toggle(cur, p.id))} />
                <span className="min-w-0 flex-1 truncate text-sm font-medium">{p.name}</span>
                <span className="text-xs text-gray-500">{p.role}</span>
              </label>
            ))
          ) : (
            <p className="rounded-md border border-gray-200 p-3 text-sm text-gray-600">{t("공유할 수 있는 프로젝트가 없습니다. 프로젝트 권한 없이 승인하려면 프로젝트 scope를 해제하세요.")}</p>
          )}
        </Section>
      )}

      <ul className="mt-5 space-y-1.5 text-xs leading-5 text-gray-500">
        <li className="flex gap-2">
          <Check className="mt-0.5 size-3.5 shrink-0" />
          {t("계정 설정 → 승인된 앱에서 언제든 접근을 폐기할 수 있습니다.")}
        </li>
        <li className="flex gap-2">
          <ExternalLink className="mt-0.5 size-3.5 shrink-0" />
          <span>
            {tr("승인하거나 거부하면 {host}(으)로 이동합니다.", { host: <span className="font-mono text-gray-600">{data.app.redirect_host}</span> })}
          </span>
        </li>
      </ul>
      <ErrorBox error={error} className="mt-4" />
      <div className="mt-6 grid grid-cols-2 gap-3">
        <Button size="lg" variant="outline" disabled={!!busy} loading={busy === "deny"} onClick={() => finish("deny")}>
          {t("거부")}
        </Button>
        <Button size="lg" variant="primary" disabled={!!busy || !canApprove} loading={busy === "approve"} onClick={() => finish("approve")}>
          {t("승인")}
        </Button>
      </div>
    </Shell>
  );
}

const SCOPE_ORDER = ["openid", "profile", "email", "offline_access"];
const scopeRank = (s: string) => (SCOPE_ORDER.includes(s) ? SCOPE_ORDER.indexOf(s) : s.startsWith("workspace:") ? 10 : 20);

function Section({ title, hint, children }: { title: string; hint?: string; children: ReactNode }) {
  return (
    <section className="mt-5 space-y-2 first:mt-0">
      <div>
        <h2 className="text-sm font-semibold">{title}</h2>
        {hint && <p className="mt-1 text-xs text-gray-600">{hint}</p>}
      </div>
      {children}
    </section>
  );
}

function Shell({ children }: { children: ReactNode }) {
  return (
    <main className="flex min-h-dvh items-center justify-center bg-background px-4 py-10">
      <div className="w-full max-w-[519px] rounded-xl border border-gray-100 bg-background p-6 sm:p-8">{children}</div>
    </main>
  );
}

function Loading() {
  return (
    <div className="flex items-center justify-center gap-2 py-12 text-sm text-gray-500">
      <Spinner /> {t("요청 확인 중")}
    </div>
  );
}

function ConsentError({ message }: { message: string }) {
  return (
    <Shell>
      <div className="space-y-4">
        <AvenMark className="size-10" />
        <h1 className="text-lg font-semibold">{t("접근 요청을 처리할 수 없습니다")}</h1>
        <p className="text-sm text-gray-600">{message}</p>
        <a href="/" className="inline-block text-sm font-medium text-pink-600 hover:underline">
          {t("Aven으로 돌아가기")}
        </a>
      </div>
    </Shell>
  );
}
