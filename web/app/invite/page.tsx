"use client";

import { useEffect, useState, type FormEvent } from "react";
import { api } from "@/lib/api";
import { Button, ErrorBox, Field, Input, AvenMark } from "@/components/ui";
import { t } from "@/lib/i18n";

/** Invite link: /invite/#token=… (the token stays in the fragment so it never reaches server logs). */
export default function InvitePage() {
  const [token, setToken] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<unknown>(null);

  useEffect(() => {
    const m = /(?:^|[#&])token=([^&]+)/.exec(window.location.hash);
    setToken(m ? decodeURIComponent(m[1]) : "");
  }, []);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (password !== confirm) return setError(t("비밀번호 확인이 일치하지 않습니다."));
    if (!password) return setError(t("비밀번호를 입력하세요."));
    setLoading(true);
    setError(null);
    try {
      await api.acceptInvite({ token: token ?? "", name: name.trim(), password });
      // 새 계정은 먼저 GitHub 연결 안내로 보낸다(연결은 사용자별이다). GitHub 앱이 없으면 GitHubOnboarding이 홈으로 넘긴다.
      window.location.href = "/?welcome=1";
    } catch (err) {
      setError(err);
      setLoading(false);
    }
  };

  return (
    <div className="flex min-h-dvh items-center justify-center bg-background px-4">
      <div className="flex min-h-[428px] w-full max-w-[379px] flex-col rounded-xl border border-gray-100 bg-background md:w-[379px]">
        <div className="flex flex-1 flex-col items-center justify-center space-y-4 py-8">
          <AvenMark className="size-16" />
          <div className="flex flex-col items-center space-y-1 text-center">
            <h1 className="text-lg font-medium">{t("aven 초대 수락")}</h1>
            <p className="text-base text-gray-500">{t("이름과 비밀번호를 정하면 계정이 만들어집니다")}</p>
          </div>
        </div>
        <div className="mb-8 px-8">
        {token === "" ? (
          <ErrorBox error={t("초대 링크에 토큰이 없습니다. 받은 링크 전체를 주소창에 붙여 넣으세요.")} />
        ) : (
          <form onSubmit={submit} className="grid gap-4">
            <Field label={t("이름")}>
              <Input autoFocus autoComplete="name" value={name} onChange={(e) => setName(e.target.value)} />
            </Field>
            <Field label={t("비밀번호")}>
              <Input type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />
            </Field>
            <Field label={t("비밀번호 확인")}>
              <Input type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} />
            </Field>
            {error ? <ErrorBox error={error} /> : null}
            <Button type="submit" variant="primary" loading={loading} disabled={!token || !name.trim() || !password} size="lg" className="w-full">
              {t("계정 만들기")}
            </Button>
          </form>
        )}
        </div>
      </div>
    </div>
  );
}
