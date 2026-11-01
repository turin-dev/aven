"use client";

import { LanguageSelectCompact } from "@/components/LanguageSelect";
import { Suspense, useEffect, useRef, useState, useSyncExternalStore, type FormEvent } from "react";
import { useSearchParams } from "next/navigation";
import { api, ApiError } from "@/lib/api";
import { getPasskey, passkeysSupported } from "@/lib/passkeys";
import { safeNextPath } from "@/lib/safeRedirect";
import { ArrowLeft, Building2, KeyRound } from "lucide-react";
import { t } from "@/lib/i18n";
import { Button, ErrorBox, Field, Input } from "@/components/ui";
import { AuthHeader, AuthShell, OrDivider, PasswordInput } from "@/components/AuthKit";

const noopSubscribe = () => () => {};
const PRIMARY_GLOW = { boxShadow: "0px 4px 24px 0px rgba(133, 59, 206, 0.27)" };
const TOTP_RE = /^\d{6}$/;

type Step = "password" | "code" | "passkey";

/** Turns a failed login request into a message for the user. Server messages are Korean-only, so known cases are mapped here. */
function loginError(err: unknown, step: Step, hasEmail: boolean): unknown {
  if (err instanceof TypeError) return t("서버에 연결할 수 없습니다. 네트워크 연결을 확인하세요.");
  if (!(err instanceof ApiError)) return err;
  if (err.status === 429) {
    const secs = /(\d+)\s*초/.exec(err.message)?.[1];
    return secs
      ? t("로그인 시도가 너무 많습니다. {seconds}초 후 다시 시도하세요.", { seconds: secs })
      : t("로그인 시도가 너무 많습니다. 잠시 후 다시 시도하세요.");
  }
  if (err.status === 403) {
    if (err.message === "sso_required") return t("이 계정은 조직의 SAML SSO로 로그인해야 합니다.");
    if (err.message === "cross-origin request rejected") return t("다른 주소에서 보낸 로그인 요청이라 거부했습니다. 대시보드 주소로 직접 접속해 다시 시도하세요.");
    if (err.message.includes("비활성화")) return t("비활성화된 계정입니다. 관리자에게 문의하세요.");
  }
  if (err.status === 401) {
    if (step === "code") return t("인증 코드가 올바르지 않습니다. 다시 입력하세요.");
    if (step === "passkey") return t("패스키를 확인하지 못했습니다. 이 계정에 등록된 패스키인지 확인하세요.");
    return hasEmail
      ? t("이메일 또는 비밀번호가 맞지 않습니다.")
      : t("비밀번호가 맞지 않습니다. 이메일 없이 로그인하면 서버의 AVEN_ADMIN_PASSWORD 값을 씁니다.");
  }
  return err;
}

function LoginForm() {
  const params = useSearchParams();
  const rawNext = params.get("next");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  // 비밀번호가 맞고 2단계 인증이 켜져 있으면 서버가 401 totp_required와 쓸 수 있는 수단(methods)을 돌려준다 → 두 번째 단계.
  const [needCode, setNeedCode] = useState(false);
  const [methods, setMethods] = useState<string[]>([]);
  const supported = useSyncExternalStore(noopSubscribe, passkeysSupported, () => false);
  const canUsePasskey = supported && methods.includes("passkey");
  const hasTotp = methods.length === 0 || methods.includes("totp");
  const [busy, setBusy] = useState<"password" | "passkey" | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [samlAvailable, setSamlAvailable] = useState(false);
  const passwordRef = useRef<HTMLInputElement>(null);
  const codeRef = useRef<HTMLInputElement>(null);
  const submitted = useRef("");

  useEffect(() => {
    api.samlStatus().then((status) => setSamlAvailable(status.configured)).catch(() => setSamlAvailable(false));
  }, []);

  // Only same-origin paths survive; ?next=//evil.example or /\t/evil.example fall back to "/".
  // The base origin only anchors the parse (the result is always a path), so a fixed one keeps render pure.
  const safeNext = safeNextPath(rawNext, "https://aven.invalid");

  const finishLogin = async () => {
    const me = await api.me();
    // replace(): the login page should not stay in history behind the destination.
    window.location.replace(me.mfa_setup_required ? "/account/?section=security" : safeNext);
  };

  const loginWithPasskey = async (passwordless: boolean) => {
    setBusy("passkey");
    setError(null);
    try {
      const begin = await api.beginPasskeyLogin(passwordless ? {} : { email: email.trim() || undefined, password });
      const credential = await getPasskey(begin.options);
      await api.finishPasskeyLogin(begin.flow, begin.challenge_id, credential);
      await finishLogin();
    } catch (err) {
      setError(loginError(err, "passkey", !!email.trim()));
      setBusy(null);
    }
  };

  const login = async (totp?: string) => {
    setBusy("password");
    setError(null);
    try {
      await api.login({ email: email.trim() || undefined, password, totp_code: totp });
      await finishLogin();
    } catch (err) {
      if (err instanceof ApiError && err.status === 401 && err.message === "totp_required") {
        const m = (err.data as { methods?: unknown } | undefined)?.methods;
        setMethods(Array.isArray(m) ? m.filter((x): x is string => typeof x === "string") : []);
        setNeedCode(true);
        setBusy(null);
        return;
      }
      setError(loginError(err, totp ? "code" : "password", !!email.trim()));
      setBusy(null);
      if (totp) {
        setCode("");
        submitted.current = "";
        requestAnimationFrame(() => codeRef.current?.focus());
      } else if (err instanceof ApiError && err.status === 401) {
        requestAnimationFrame(() => passwordRef.current?.select());
      }
    }
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (busy) return;
    if (needCode) {
      submitted.current = code.trim();
      void login(code.trim());
    } else void login();
  };

  // A full 6-digit TOTP code submits by itself (also when pasted or autofilled from SMS/keychain).
  const onCode = (value: string) => {
    setCode(value);
    const v = value.replace(/\s/g, "");
    if (hasTotp && TOTP_RE.test(v) && !busy && submitted.current !== v) {
      submitted.current = v;
      void login(v);
    }
  };

  const back = () => {
    setNeedCode(false);
    setCode("");
    setError(null);
    submitted.current = "";
  };

  if (needCode) {
    return (
      <form onSubmit={submit} className="grid gap-4" method="post" aria-label={t("2단계 인증")}>
        <p className="text-center text-sm text-gray-600">
          {canUsePasskey ? t("등록한 패스키나 인증 앱으로 본인임을 한 번 더 확인하세요.") : t("인증 앱으로 본인임을 한 번 더 확인하세요.")}
        </p>
        {canUsePasskey && (
          <>
            <Button type="button" variant="primary" loading={busy === "passkey"} disabled={!!busy} onClick={() => loginWithPasskey(false)} size="lg" className="w-full text-sm font-medium" style={PRIMARY_GLOW}>
              <KeyRound aria-hidden /> {t("패스키로 확인")}
            </Button>
            <OrDivider />
          </>
        )}
        <Field
          label={hasTotp ? t("인증 코드") : t("복구 코드")}
          hint={hasTotp ? t("인증 앱의 6자리 코드 또는 복구 코드(xxxxx-xxxxx)를 입력하세요.") : t("패스키를 쓸 수 없으면 복구 코드(xxxxx-xxxxx)를 입력하세요.")}
        >
          <Input
            ref={codeRef}
            name="totp"
            autoFocus={!canUsePasskey}
            inputMode={hasTotp ? "numeric" : "text"}
            autoComplete="one-time-code"
            maxLength={32}
            value={code}
            onChange={(e) => onCode(e.target.value)}
            placeholder={hasTotp ? "123456" : "xxxxx-xxxxx"}
            className="text-center font-mono tracking-[0.3em]"
            aria-invalid={!!error || undefined}
          />
        </Field>
        <ErrorBox error={error} />
        <Button
          type="submit"
          variant={canUsePasskey ? "default" : "primary"}
          loading={busy === "password"}
          disabled={!code.trim() || !!busy}
          size="lg"
          className="w-full text-sm font-medium"
          style={canUsePasskey ? undefined : PRIMARY_GLOW}
        >
          {t("확인")}
        </Button>
        <button
          type="button"
          className="mx-auto flex cursor-pointer items-center gap-1 rounded px-2 py-1 text-xs text-gray-600 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-500"
          onClick={back}
        >
          <ArrowLeft className="size-3.5" aria-hidden /> {t("돌아가기")}
        </button>
      </form>
    );
  }

  return (
    // method="post": if the form is submitted before hydration, the password must not end up in the URL.
    <form onSubmit={submit} className="grid gap-4" method="post" aria-label={t("로그인")}>
      <Field label={t("이메일")} hint={t("비워 두면 기본 admin 계정(AVEN_ADMIN_PASSWORD)으로 로그인합니다.")}>
        <Input
          type="email"
          name="email"
          autoFocus
          autoComplete="username"
          autoCapitalize="none"
          inputMode="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          placeholder="you@example.com"
        />
      </Field>
      <Field label={t("비밀번호")}>
        <PasswordInput
          ref={passwordRef}
          name="password"
          autoComplete="current-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          aria-invalid={(error instanceof ApiError && error.status === 401) || undefined}
        />
      </Field>
      <ErrorBox error={error} />
      <Button type="submit" variant="primary" loading={busy === "password"} disabled={!password || !!busy} size="lg" className="w-full text-sm font-medium" style={PRIMARY_GLOW}>
        {t("로그인")}
      </Button>
      {(samlAvailable || supported) && <OrDivider />}
      {samlAvailable && (
        <a
          href={`/api/auth/saml/login${safeNext !== "/" ? `?next=${encodeURIComponent(safeNext)}` : ""}`}
          className="flex h-[42px] w-full items-center justify-center gap-2 rounded-md border border-gray-200 px-3 text-sm font-medium text-foreground hover:bg-gray-100 focus:outline-none focus-visible:ring-2 focus-visible:ring-gray-600"
        >
          <Building2 className="size-4" aria-hidden /> {t("조직 SSO로 로그인")}
        </a>
      )}
      {supported && (
        <Button type="button" variant="outline" loading={busy === "passkey"} disabled={!!busy} onClick={() => loginWithPasskey(true)} size="lg" className="w-full text-sm font-medium">
          <KeyRound aria-hidden /> {t("패스키로 로그인")}
        </Button>
      )}
    </form>
  );
}

export default function LoginPage() {
  useEffect(() => {
    document.title = `${t("로그인")} · Aven`;
  }, []);
  return (
    <AuthShell footer={<LanguageSelectCompact />}>
      <AuthHeader title={t("Aven에 오신 것을 환영합니다")} subtitle={t("계정 이메일과 비밀번호를 입력하세요")} />
      <div className="px-8 pb-8">
        <Suspense fallback={null}>
          <LoginForm />
        </Suspense>
      </div>
    </AuthShell>
  );
}
