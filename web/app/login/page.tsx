"use client";

import { LanguageSelectCompact } from "@/components/LanguageSelect";
import { Suspense, useEffect, useState, useSyncExternalStore, type FormEvent } from "react";
import { useSearchParams } from "next/navigation";
import { api, ApiError } from "@/lib/api";
import { getPasskey, passkeysSupported } from "@/lib/passkeys";
import { KeyRound } from "lucide-react";
import { t } from "@/lib/i18n";
import { Button, ErrorBox, Field, Input, AvenMark } from "@/components/ui";

const noopSubscribe = () => () => {};

function LoginForm() {
  const params = useSearchParams();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  // 비밀번호가 맞고 2단계 인증이 켜져 있으면 서버가 401 totp_required와 쓸 수 있는 수단(methods)을 돌려준다 → 두 번째 단계.
  const [needCode, setNeedCode] = useState(false);
  const [methods, setMethods] = useState<string[]>([]);
  const supported = useSyncExternalStore(noopSubscribe, passkeysSupported, () => false);
  const canUsePasskey = supported && methods.includes("passkey");
  const hasTotp = methods.length === 0 || methods.includes("totp");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [samlAvailable, setSamlAvailable] = useState(false);

  useEffect(() => {
    api.samlStatus().then((status) => setSamlAvailable(status.configured)).catch(() => setSamlAvailable(false));
  }, []);

  const finishLogin = async () => {
    const me = await api.me();
    const next = params.get("next");
    const dest = me.mfa_setup_required
      ? "/account/?section=security"
      : next && next.startsWith("/") && !next.startsWith("//") && !next.includes("\\")
        ? next
        : "/";
    window.location.href = dest;
  };

  const loginWithPasskey = async (passwordless: boolean) => {
    setLoading(true);
    setError(null);
    try {
      const begin = await api.beginPasskeyLogin(passwordless ? {} : { email: email.trim() || undefined, password });
      const credential = await getPasskey(begin.options);
      await api.finishPasskeyLogin(begin.flow, begin.challenge_id, credential);
      await finishLogin();
    } catch (err) {
      setError(err);
      setLoading(false);
    }
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setLoading(true);
    setError(null);
    try {
      await api.login({ email: email.trim() || undefined, password, totp_code: needCode ? code.trim() : undefined });
      await finishLogin();
    } catch (err) {
      if (err instanceof ApiError && err.status === 401 && err.message === "totp_required") {
        const m = (err.data as { methods?: unknown } | undefined)?.methods;
        setMethods(Array.isArray(m) ? m.filter((x): x is string => typeof x === "string") : []);
        setNeedCode(true);
        setLoading(false);
        return;
      }
      setError(
        err instanceof ApiError && err.status === 403 && err.message === "sso_required"
          ? t("이 계정은 조직의 SAML SSO로 로그인해야 합니다.")
          : err instanceof ApiError && err.status === 401 && !needCode
          ? email.trim()
            ? t("이메일 또는 비밀번호가 맞지 않습니다.")
            : t("비밀번호가 맞지 않습니다. 이메일 없이 로그인하면 서버의 AVEN_ADMIN_PASSWORD 값을 씁니다.")
          : err,
      );
      setLoading(false);
    }
  };

  if (needCode) {
    return (
      <form onSubmit={submit} className="grid gap-4">
        {canUsePasskey && (
          <>
            <Button type="button" variant="primary" loading={loading} onClick={() => loginWithPasskey(false)} size="lg" className="w-full text-sm font-medium">
              {t("패스키로 확인")}
            </Button>
            <div className="text-center text-xs text-gray-500">{t("또는")}</div>
          </>
        )}
        <Field
          label={hasTotp ? t("인증 코드") : t("복구 코드")}
          hint={hasTotp ? t("인증 앱의 6자리 코드 또는 복구 코드(xxxxx-xxxxx)를 입력하세요.") : t("패스키를 쓸 수 없으면 복구 코드(xxxxx-xxxxx)를 입력하세요.")}
        >
          <Input
            autoFocus={!canUsePasskey}
            inputMode={hasTotp ? "numeric" : "text"}
            autoComplete="one-time-code"
            value={code}
            onChange={(e) => setCode(e.target.value)}
            className="font-mono tracking-[0.2em]"
            aria-label={t("인증 코드")}
          />
        </Field>
        {error ? <ErrorBox error={error} /> : null}
        <Button
          type="submit"
          variant={canUsePasskey ? "default" : "primary"}
          loading={loading}
          disabled={!code.trim()}
          size="lg"
          className="w-full text-sm font-medium"
          style={canUsePasskey ? undefined : { boxShadow: "0px 4px 24px 0px rgba(133, 59, 206, 0.27)" }}
        >
          {t("확인")}
        </Button>
        <button
          type="button"
          className="z-0 mt-0 w-full cursor-pointer text-center text-xs text-gray-600 hover:text-gray-800"
          onClick={() => (setNeedCode(false), setCode(""), setError(null))}
        >
          {t("돌아가기")}
        </button>
      </form>
    );
  }

  return (
    <form onSubmit={submit} className="grid gap-4">
      <Field label={t("이메일")} hint={t("비워 두면 기본 admin 계정(AVEN_ADMIN_PASSWORD)으로 로그인합니다.")}>
        <Input
          type="email"
          autoFocus
          autoComplete="username"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          placeholder="you@example.com"
        />
      </Field>
      <Field label={t("비밀번호")}>
        <Input
          type="password"
          autoComplete="current-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
      </Field>
      {error ? <ErrorBox error={error} /> : null}
      <Button type="submit" variant="primary" loading={loading} disabled={!password} size="lg" className="w-full text-sm font-medium" style={{ boxShadow: "0px 4px 24px 0px rgba(133, 59, 206, 0.27)" }}>
        {t("로그인")}
      </Button>
      {samlAvailable && (
        <a
          href={`/api/auth/saml/login${params.get("next") ? `?next=${encodeURIComponent(params.get("next")!)}` : ""}`}
          className="flex h-[42px] w-full items-center justify-center rounded-md border border-gray-200 px-3 text-sm font-medium text-foreground hover:bg-gray-100 focus:outline-none focus-visible:ring-2 focus-visible:ring-gray-600"
        >
          {t("조직 SSO로 로그인")}
        </a>
      )}
      {supported && (
        <Button type="button" variant="default" loading={loading} onClick={() => loginWithPasskey(true)} size="lg" className="w-full text-sm font-medium">
          <KeyRound className="size-4" aria-hidden /> {t("패스키로 로그인")}
        </Button>
      )}
    </form>
  );
}

export default function LoginPage() {
  return (
    <div className="flex min-h-dvh items-center justify-center bg-background px-4">
      <div className="flex min-h-[454px] w-full flex-col items-center">
        <div className="relative flex min-h-[428px] w-full max-w-[379px] flex-col overflow-hidden md:w-[379px]">
          <div className="flex w-full flex-1 flex-col rounded-xl border border-gray-100 bg-background">
            <div className="flex flex-1 flex-col items-center justify-center space-y-4 py-8">
              <AvenMark className="size-16" />
              <div className="flex flex-col items-center space-y-1">
                <h1 className="text-lg font-medium">{t("aven에 오신 것을 환영합니다")}</h1>
                <p className="text-base text-gray-500">{t("계정 이메일과 비밀번호를 입력하세요")}</p>
              </div>
            </div>
            <div className="mb-8 flex flex-col px-8">
              <Suspense fallback={null}>
                <LoginForm />
              </Suspense>
            </div>
          </div>
          <div className="mt-3 flex justify-center">
            <LanguageSelectCompact />
          </div>
        </div>
      </div>
    </div>
  );
}
