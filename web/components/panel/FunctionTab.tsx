"use client";

/* v1.5 function services: edit the single Bun file (index.ts) and deploy it. */

import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Rocket, Save } from "lucide-react";
import type { CanvasService, Environment } from "@/lib/api";
import { layoutApi, layoutKeys } from "@/lib/layouts";
import type { ServiceActions } from "@/lib/useServiceActions";
import { fmtBytes, fmtRelative } from "@/lib/format";
import { t } from "@/lib/i18n";
import { Button, CenterSpinner, ErrorBox, cx } from "../ui";

const bytes = (s: string) => new TextEncoder().encode(s).length;

export function FunctionTab({ svc, env, actions }: { svc: CanvasService; env: Environment; actions: ServiceActions }) {
  const qc = useQueryClient();
  const key = layoutKeys.functionCode(svc.id, env.id);
  const fn = useQuery({ queryKey: key, queryFn: () => layoutApi.functionCode(svc.id, env.id), refetchOnWindowFocus: false });
  const [draft, setDraft] = useState<string | null>(null);
  const [busy, setBusy] = useState<"save" | "deploy" | null>(null);
  const [error, setError] = useState<unknown>(null);
  const ref = useRef<HTMLTextAreaElement>(null);
  const escaped = useRef(false);
  const d = fn.data;
  const code = draft ?? d?.code ?? "";
  const dirty = draft !== null && draft !== d?.code;
  const size = bytes(code);
  const tooBig = !!d && size > d.max_bytes;
  const undeployed = !!d && d.deployed_sha256 !== d.sha256;

  // 저장하지 않은 편집이 있으면 창을 닫기 전에 묻는다.
  useEffect(() => {
    if (!dirty) return;
    const h = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", h);
    return () => window.removeEventListener("beforeunload", h);
  }, [dirty]);

  const save = async (deploy: boolean) => {
    if (busy || tooBig) return;
    setBusy(deploy ? "deploy" : "save");
    setError(null);
    try {
      const next = await layoutApi.saveFunctionCode(svc.id, env.id, code, deploy);
      qc.setQueryData(key, next);
      setDraft(null);
      actions.refresh();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(null);
    }
  };

  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "s") {
      e.preventDefault();
      save(e.shiftKey);
      return;
    }
    // Tab은 들여쓰기(Esc 다음 Tab으로 편집기를 빠져나간다).
    if (e.key === "Tab" && !e.shiftKey && !e.altKey && !e.ctrlKey && !e.metaKey && !escaped.current) {
      e.preventDefault();
      const el = e.currentTarget;
      const { selectionStart: a, selectionEnd: b } = el;
      const next = code.slice(0, a) + "  " + code.slice(b);
      setDraft(next);
      requestAnimationFrame(() => el.setSelectionRange(a + 2, a + 2));
      return;
    }
    escaped.current = e.key === "Escape";
  };

  if (fn.error) return <ErrorBox error={fn.error} />;
  if (!d) return <CenterSpinner />;
  const lines = code.split("\n").length;

  return (
    <div className="flex h-full flex-col pb-8">
      <div className="mb-3 flex flex-wrap items-center gap-2 text-sm text-gray-500">
        <span className="font-mono text-foreground">index.ts</span>
        <span>{t("· Bun · PORT에서 HTTP 요청을 받습니다")}</span>
        <span className="ml-auto flex flex-wrap items-center gap-2">
          {dirty ? (
            <span className="text-warn">{t("저장하지 않은 변경")}</span>
          ) : undeployed ? (
            <span className="text-pink-600">{d.deployed_sha256 ? t("배포된 코드와 다릅니다") : t("아직 배포하지 않았습니다")}</span>
          ) : (
            <span>{t("배포된 코드와 같습니다")}</span>
          )}
          {d.updated_at && <span className="tabular-nums text-gray-400">{t("· 저장 {time}", { time: fmtRelative(d.updated_at) })}</span>}
        </span>
      </div>
      <div className="relative flex min-h-[320px] flex-1 overflow-hidden rounded-lg border border-gray-200 bg-background focus-within:border-pink-500">
        <div aria-hidden className="select-none overflow-hidden border-r border-gray-200 px-2 py-3 text-right font-mono text-xs leading-[20px] text-gray-400">
          {Array.from({ length: lines }, (_, i) => (
            <div key={i}>{i + 1}</div>
          ))}
        </div>
        <textarea
          ref={ref}
          aria-label={t("{name} function 코드 (index.ts)", { name: svc.name })}
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          wrap="off"
          data-noscroll
          value={code}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={onKey}
          onScroll={(e) => {
            const gutter = e.currentTarget.previousElementSibling as HTMLElement | null;
            if (gutter) gutter.scrollTop = e.currentTarget.scrollTop;
          }}
          className="min-w-0 flex-1 resize-none bg-transparent px-3 py-3 font-mono text-xs leading-[20px] text-foreground outline-none"
        />
      </div>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <span className={cx("text-xs tabular-nums", tooBig ? "text-danger" : "text-gray-400")}>
          {fmtBytes(size)} / {fmtBytes(d.max_bytes)}
        </span>
        <span className="hidden text-xs text-gray-500 sm:inline" title={t("Tab은 들여쓰기입니다. Esc 다음 Tab으로 편집기를 빠져나갑니다.")}>
          {t("· Ctrl+S 저장 · Ctrl+Shift+S 저장 후 배포")}
        </span>
        <span className="ml-auto flex gap-2">
          {dirty && (
            <Button variant="ghost" onClick={() => setDraft(null)} disabled={!!busy}>
              {t("되돌리기")}
            </Button>
          )}
          <Button variant="outline" onClick={() => save(false)} loading={busy === "save"} disabled={!dirty || !!busy || tooBig}>
            <Save /> {t("저장")}
          </Button>
          <Button variant="primary" onClick={() => save(true)} loading={busy === "deploy"} disabled={(!dirty && !undeployed) || !!busy || tooBig}>
            <Rocket /> {dirty ? t("저장 후 배포") : t("배포")}
          </Button>
        </span>
      </div>
      <ErrorBox error={error} className="mt-3" />
      <p className="mt-3 text-xs text-gray-500">
        {t("코드는 환경마다 따로 저장되고 배포할 때 이미지로 빌드됩니다. cron 일정을 정하면 일정마다 이 파일을 실행하는 job이 됩니다(설정 탭).")}
      </p>
    </div>
  );
}
