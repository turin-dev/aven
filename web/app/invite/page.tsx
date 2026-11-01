"use client";

import { useEffect, useState, type FormEvent } from "react";
import { api, ApiError } from "@/lib/api";
import { Button, ErrorBox, Field, Input } from "@/components/ui";
import { AuthHeader, AuthShell, PasswordInput, PasswordStrengthMeter } from "@/components/AuthKit";
import { LanguageSelectCompact } from "@/components/LanguageSelect";
import { t } from "@/lib/i18n";

/**
 * The server checks name, then password policy (non-empty, at most 256 bytes), then the token, and
 * answers 400 for each. Name and password are validated here first, so any other 400 means the link is bad.
 */
function isInvalidToken(err: unknown, password: string): boolean {
  if (!(err instanceof ApiError) || err.status !== 400) return false;
  if (err.message === "invalid json" || err.message.startsWith("name")) return false;
  return !!password && new TextEncoder().encode(password).length <= 256;
}

/** Invite link: /invite/#token=… (the token stays in the fragment so it never reaches server logs or Referer headers). */
export default function InvitePage() {
  const [token, setToken] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<unknown>(null);

  useEffect(() => {
    const m = /(?:^|[#&])token=([^&]+)/.exec(window.location.hash);
    let value = "";
    try {
      value = m ? decodeURIComponent(m[1]).trim() : "";
    } catch {
      value = ""; // malformed %-escape: treat like a missing token
    }
    setToken(value);
    document.title = `${t("Aven 초대 수락")} · Aven`;
  }, []);

  const mismatch = confirm.length > 0 && password !== confirm;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!password) return setError(t("비밀번호를 입력하세요."));
    if (password !== confirm) return setError(t("비밀번호 확인이 일치하지 않습니다."));
    if (new TextEncoder().encode(password).length > 256) return setError(t("비밀번호가 너무 깁니다."));
    setLoading(true);
    setError(null);
    try {
      await api.acceptInvite({ token: token ?? "", name: name.trim(), password });
      // 새 계정은 먼저 GitHub 연결 안내로 보낸다(연결은 사용자별이다). GitHub 앱이 없으면 GitHubOnboarding이 홈으로 넘긴다.
      // replace(): the invite URL (with its token) should not stay in the back/forward history.
      window.location.replace("/?welcome=1");
    } catch (err) {
      setError(
        isInvalidToken(err, password)
          ? t("초대 링크가 올바르지 않거나 만료되었습니다. 초대한 사람에게 새 링크를 요청하세요.")
          : err instanceof TypeError
            ? t("서버에 연결할 수 없습니다. 네트워크 연결을 확인하세요.")
            : err,
      );
      setLoading(false);
    }
  };

  return (
    <AuthShell footer={<LanguageSelectCompact />}>
      <AuthHeader title={t("Aven 초대 수락")} subtitle={t("이름과 비밀번호를 정하면 계정이 만들어집니다")} />
      <div className="px-8 pb-8">
        {token === null ? null : token === "" ? (
          <ErrorBox error={t("초대 링크에 토큰이 없습니다. 받은 링크 전체를 주소창에 붙여 넣으세요.")} />
        ) : (
          <form onSubmit={submit} method="post" className="grid gap-4" aria-label={t("계정 만들기")}>
            <Field label={t("이름")}>
              <Input name="name" autoFocus autoComplete="name" maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />
            </Field>
            <Field label={t("비밀번호")}>
              <PasswordInput name="new-password" autoComplete="new-password" maxLength={256} value={password} onChange={(e) => setPassword(e.target.value)} aria-describedby="invite-strength" />
              <PasswordStrengthMeter id="invite-strength" password={password} personal={[name]} />
            </Field>
            <Field label={t("비밀번호 확인")} hint={mismatch ? <span className="text-danger">{t("비밀번호 확인이 일치하지 않습니다.")}</span> : undefined}>
              <PasswordInput name="confirm-password" autoComplete="new-password" maxLength={256} value={confirm} onChange={(e) => setConfirm(e.target.value)} invalid={mismatch} aria-invalid={mismatch || undefined} />
            </Field>
            <ErrorBox error={error} />
            <Button type="submit" variant="primary" loading={loading} disabled={!token || !name.trim() || !password || mismatch} size="lg" className="w-full text-sm font-medium">
              {t("계정 만들기")}
            </Button>
            <p className="text-center text-xs text-gray-500">
              {t("이미 계정이 있나요?")}{" "}
              <a href="/login/" className="font-medium text-pink-600 hover:underline focus:outline-none focus-visible:underline">
                {t("로그인")}
              </a>
            </p>
          </form>
        )}
      </div>
    </AuthShell>
  );
}
