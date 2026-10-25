"use client";

import { useState, useSyncExternalStore } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound } from "lucide-react";
import { api } from "@/lib/api";
import { createPasskey, passkeysSupported } from "@/lib/passkeys";
import { fmtRelative } from "@/lib/format";
import { qk } from "@/lib/queries";
import { Button, Card, ErrorBox, Field, Input, RowList, useConfirm } from "@/components/ui";
import { t } from "@/lib/i18n";

const noopSubscribe = () => () => {};

/** 계정 보안 설정의 패스키(WebAuthn) 카드. 추가·삭제 모두 현재 비밀번호를 다시 확인한다. */
export function PasskeysSettings({ onRecoveryCodes }: { onRecoveryCodes: (codes: string[]) => void }) {
  const qc = useQueryClient();
  const passkeys = useQuery({ queryKey: qk.passkeys, queryFn: api.passkeys });
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const { confirm, node } = useConfirm();
  const supported = useSyncExternalStore(noopSubscribe, passkeysSupported, () => true);

  // 2FA 강제로 막혀 있던 쿼리도 풀릴 수 있으므로 모두 다시 불러온다.
  const refresh = () => qc.invalidateQueries();

  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  const add = () =>
    run(async () => {
      const begin = await api.beginPasskeyRegistration(name.trim() || "패스키", password);
      const credential = await createPasskey(begin.options);
      const result = await api.finishPasskeyRegistration(begin.challenge_id, credential);
      setName("");
      setPassword("");
      if (result.recovery_codes?.length) onRecoveryCodes(result.recovery_codes);
      await refresh();
    });

  return (
    <>
      {node}
      <Card title={t("패스키")} description={t("기기의 지문, 얼굴 인식 또는 PIN으로 두 번째 인증을 하거나, 비밀번호 없이 로그인합니다.")}>
        <div className="space-y-4">
          {!supported && <p className="text-sm text-gray-600">{t("이 브라우저나 주소(HTTPS 또는 localhost가 아닌 곳)에서는 패스키를 쓸 수 없습니다.")}</p>}
          {passkeys.isPending ? (
            <div className="py-2 text-sm text-gray-500">{t("패스키를 불러오는 중입니다.")}</div>
          ) : passkeys.data?.length ? (
            <RowList bare>
              {passkeys.data.map((pk) => (
                <li key={pk.id} className="flex flex-wrap items-center gap-3 py-3 first:pt-0">
                  <KeyRound className="size-4 text-gray-500" aria-hidden />
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-sm font-medium">{pk.name}</div>
                    <div className="text-xs tabular-nums text-gray-500">
                      {t("{rp} · 추가 {created} · 마지막 사용 {used}", { rp: pk.rp_id, created: fmtRelative(pk.created_at), used: pk.last_used_at ? fmtRelative(pk.last_used_at) : t("사용 안 함") })}
                    </div>
                  </div>
                  <Button
                    variant="danger"
                    size="sm"
                    loading={busy}
                    disabled={!password}
                    title={password ? undefined : t("아래에 현재 비밀번호를 입력하세요")}
                    onClick={() =>
                      confirm({
                        title: t("패스키 삭제"),
                        message: t("{name}을(를) 이 계정에서 삭제합니다. 기기에 저장된 패스키는 기기 설정에서 따로 지워야 합니다.", { name: pk.name }),
                        confirmLabel: t("삭제"),
                        danger: true,
                        run: () =>
                          run(async () => {
                            await api.deletePasskey(pk.id, password);
                            setPassword("");
                            await refresh();
                          }),
                      })
                    }
                  >
                    {t("삭제")}
                  </Button>
                </li>
              ))}
            </RowList>
          ) : (
            <p className="text-sm text-gray-500">{t("등록된 패스키가 없습니다.")}</p>
          )}
          <form
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault();
              add();
            }}
          >
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label={t("패스키 이름")} hint={t("예: 개인 노트북, 업무용 휴대전화")}>
                <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t("패스키")} maxLength={80} />
              </Field>
              <Field label={t("현재 비밀번호")} hint={t("패스키를 추가하거나 삭제할 때 확인합니다.")}>
                <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
              </Field>
            </div>
            <Button type="submit" variant="primary" loading={busy} disabled={!supported || !password}>
              {t("패스키 추가")}
            </Button>
          </form>
          <ErrorBox error={passkeys.error ?? error} />
        </div>
      </Card>
    </>
  );
}
