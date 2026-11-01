"use client";

// Small building blocks shared by the data, backups, PITR, HA and bucket panels.

import { useId, useState, type ReactNode } from "react";
import { Eye, EyeOff } from "lucide-react";
import { maskSecrets } from "@/lib/dataSql";
import { t, tr } from "@/lib/i18n";
import { Button, CopyButton, Dialog, ErrorBox, Input, cx } from "../ui";

type TypedConfirmState = {
  title: string;
  message: ReactNode;
  confirmLabel: string;
  /** Text the user must type exactly before the action is enabled (usually the service name). */
  expected: string;
  run: () => Promise<unknown> | unknown;
};

/**
 * Confirmation for destructive, hard-to-reverse actions (restore over live data, revert clusters):
 * the confirm button stays disabled until the resource name is typed. Errors from run() stay in the dialog.
 */
export function useTypedConfirm() {
  const [state, setState] = useState<TypedConfirmState | null>(null);
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const inputId = useId();
  const close = () => {
    if (busy) return;
    setState(null);
    setTyped("");
    setError(null);
  };
  const ok = !!state && typed.trim() === state.expected;
  const submit = async () => {
    if (!state || !ok || busy) return;
    setBusy(true);
    setError(null);
    try {
      await state.run();
      setState(null);
      setTyped("");
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  const node = (
    <Dialog
      open={!!state}
      onClose={close}
      title={state?.title ?? ""}
      footer={
        <>
          <Button variant="outline" onClick={close} disabled={busy}>
            {t("취소")}
          </Button>
          <Button variant="danger" loading={busy} disabled={!ok} onClick={submit}>
            {state?.confirmLabel}
          </Button>
        </>
      }
    >
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <div className="space-y-2">{state?.message}</div>
        <div>
          <label htmlFor={inputId} className="mb-2 block text-xs font-semibold text-gray-600">
            {tr("계속하려면 {name}을(를) 입력하세요", { name: <span className="select-all font-mono text-foreground">{state?.expected}</span> })}
          </label>
          <Input id={inputId} mono autoFocus autoComplete="off" value={typed} onChange={(e) => setTyped(e.target.value)} disabled={busy} invalid={typed !== "" && !ok && !state?.expected.startsWith(typed)} />
        </div>
        {error ? <ErrorBox error={error} /> : null}
      </form>
    </Dialog>
  );
  return {
    confirm: (s: TypedConfirmState) => {
      setTyped("");
      setError(null);
      setState(s);
    },
    node,
  };
}

/**
 * Read-only value with copy that hides only the secret part (the password inside a connection URL or command).
 * Copy always copies the real value; "보기" reveals it on screen.
 */
export function MaskedField({ value, secrets, label, className }: { value: string; secrets?: (string | undefined)[]; label: string; className?: string }) {
  const [shown, setShown] = useState(false);
  const masked = maskSecrets(value, secrets);
  const hasSecret = masked !== value;
  return (
    <div className={cx("flex h-11 min-w-0 items-center rounded-md border border-gray-200 bg-gray-100 pl-3.5 pr-1.5", className)}>
      <span className="min-w-0 flex-1 truncate font-mono text-sm" title={shown || !hasSecret ? value : masked}>
        {shown || !hasSecret ? value : masked}
      </span>
      {hasSecret && (
        <button
          type="button"
          onClick={() => setShown((s) => !s)}
          aria-pressed={shown}
          aria-label={shown ? t("{label} 비밀번호 숨기기", { label }) : t("{label} 비밀번호 보기", { label })}
          className="mr-1 flex h-7 shrink-0 cursor-pointer items-center gap-1 rounded px-2 text-xs text-gray-600 hover:bg-gray-200 hover:text-foreground"
        >
          {shown ? <EyeOff className="size-3.5" aria-hidden /> : <Eye className="size-3.5" aria-hidden />}
          {shown ? t("숨기기") : t("보기")}
        </button>
      )}
      <CopyButton value={value} label={t("{label} 복사", { label })} />
    </div>
  );
}
