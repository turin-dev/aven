"use client";

import { toast } from "@/lib/uiState";
import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { KeyRound, Plus, Trash2 } from "lucide-react";
import { api, type RegistryInput } from "@/lib/api";
import { qk, useRegistries } from "@/lib/queries";
import { Button, Card, ErrorBox, IconButton, Input, Spinner } from "../ui";
import { t } from "@/lib/i18n";

type Row = RegistryInput & { key: string; storedHost: string | null };
let nextKey = 1;

/** Project registries: host, username, write-only password. PUT replaces the list; null keeps a stored password. */
export function RegistriesView({ pid }: { pid: string }) {
  const qc = useQueryClient();
  const regs = useRegistries(pid);
  const [rows, setRows] = useState<Row[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [, setSaved] = useState(false);
  const base: Row[] = (regs.data ?? []).map((r) => ({ key: `stored:${r.host}`, host: r.host, username: r.username, password: null, storedHost: r.has_password ? r.host : null }));
  const cur = rows ?? base;
  const dirty = rows !== null;
  const set = (next: Row[]) => {
    setRows(next);
    setSaved(false);
  };
  const upd = (key: string, patch: Partial<Row>) => set(cur.map((r) => (r.key === key ? { ...r, ...patch } : r)));

  if (regs.isPending) return <Spinner />;
  if (regs.error) return <ErrorBox error={regs.error} />;

  return (
    <div className="max-w-3xl">
      <div className="mb-5">
        <h2 className="text-xl font-semibold">{t("레지스트리")}</h2>
        <p className="mt-1 text-sm text-gray-600">
          {t("비공개 이미지를 받을 때 쓰는 인증 정보입니다. 이미지 이름이 이 호스트로 시작하면 자동으로 사용합니다. 비밀번호는 저장한 뒤에는 다시 볼 수 없습니다.")}
        </p>
      </div>
      <Card
        flush
        title={t("등록된 레지스트리")}
        action={
          <Button size="sm" variant="primary" disabled={busy} onClick={() => set([...cur, { key: `new:${nextKey++}`, host: "", username: "", password: "", storedHost: null }])}>
            <Plus className="size-3.5" /> {t("레지스트리 추가")}
          </Button>
        }
        footerNote={
          <span className="flex items-start gap-2">
            <KeyRound className="mt-0.5 size-3.5 shrink-0" />
            {t("GitHub Container Registry는 read:packages 권한이 있는 토큰을, Docker Hub는 액세스 토큰을 비밀번호로 쓰세요.")}
          </span>
        }
        footer={
          <>
            {dirty && (
              <Button variant="ghost" disabled={busy} onClick={() => { setRows(null); setError(null); }}>
                {t("되돌리기")}
              </Button>
            )}
        <Button
          variant="primary"
          disabled={!dirty}
          loading={busy}
          onClick={async () => {
            const bad = cur.find((r) => !r.host || !r.username.trim() || (r.storedHost !== r.host && !r.password));
            if (bad) return setError(t("{host}: 호스트, 사용자 이름, 비밀번호를 모두 입력하세요. 호스트를 변경하면 새 비밀번호가 필요합니다.", { host: bad.host || t("새 레지스트리") }));
            if (new Set(cur.map((r) => r.host)).size !== cur.length) return setError(t("같은 호스트는 한 번만 등록할 수 있습니다."));
            if (cur.some((r) => !/^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$/.test(r.host))) return setError(t("호스트는 ghcr.io 또는 registry.example.com:5000 형식으로 입력하세요. URL 경로는 포함하지 마세요."));
            setBusy(true);
            setError(null);
            try {
              await api.setRegistries(pid, cur.map((r) => ({ host: r.host, username: r.username, password: r.password })));
              await qc.invalidateQueries({ queryKey: qk.registries(pid) });
              setRows(null);
              setSaved(true);
              toast(t("저장했습니다. 다음 배포부터 이 인증 정보로 이미지를 받습니다."));
            } catch (e) {
              setError(e);
            } finally {
              setBusy(false);
            }
          }}
        >
          {t("저장")}
        </Button>
          </>
        }
      >
      <fieldset disabled={busy} className="min-w-0 border-t border-gray-200">
        <div className="hidden grid-cols-[1.2fr_1fr_1fr_40px] gap-3 border-b border-gray-200 px-4 py-2.5 text-xs text-gray-500 sm:grid">
          <span>{t("호스트")}</span>
          <span>{t("사용자 이름")}</span>
          <span>{t("비밀번호 또는 토큰")}</span>
          <span />
        </div>
        {cur.length === 0 && <div className="px-4 py-6 text-center text-sm text-gray-500">{t("등록한 레지스트리가 없습니다.")}</div>}
        {cur.map((r) => (
          <div key={r.key} className="grid gap-2 border-b border-gray-200 px-4 py-3 last:border-0 sm:grid-cols-[1.2fr_1fr_1fr_40px] sm:gap-3">
            <Input mono value={r.host} onChange={(e) => upd(r.key, { host: e.target.value.trim().toLowerCase() })} placeholder="ghcr.io" aria-label={t("레지스트리 호스트")} />
            <Input mono value={r.username} onChange={(e) => upd(r.key, { username: e.target.value })} placeholder="user" aria-label={t("사용자 이름")} />
            <Input
              mono
              type="password"
              autoComplete="new-password"
              value={r.password ?? ""}
              onChange={(e) => upd(r.key, { password: e.target.value || null })}
              placeholder={r.storedHost === r.host ? t("저장됨 · 바꾸려면 입력") : t("토큰")}
              aria-label={t("비밀번호")}
            />
            <IconButton label={t("{host} 삭제", { host: r.host || t("레지스트리") })} onClick={() => set(cur.filter((x) => x.key !== r.key))} className="size-9">
              <Trash2 className="size-4" />
            </IconButton>
          </div>
        ))}
      </fieldset>
      </Card>
      {error ? <ErrorBox className="mt-3" error={error} /> : null}
    </div>
  );
}

/** 이미지 참조의 레지스트리 호스트. 생략하면 Docker Hub를 사용한다. */
export function imageHost(image: string): string | null {
  const value = image.trim();
  if (!value) return null;
  const first = value.split("/")[0].toLowerCase();
  return value.includes("/") && (first.includes(".") || first.includes(":") || first === "localhost") ? first : "docker.io";
}
