"use client";

import { useId, useState, type ReactNode } from "react";
import { AlertTriangle } from "lucide-react";
import { t, tr } from "@/lib/i18n";
import { Button, Dialog, ErrorBox, Input } from "../../ui";
import { FIELD } from "./primitives";

/**
 * Confirmation for destructive actions that can't be undone: the button stays disabled until the exact name is typed.
 * The dialog stays open on failure and shows the error (the shared useConfirm closes either way).
 */
export function TypeToConfirm({
  title,
  name,
  message,
  confirmLabel,
  inputLabel,
  run,
  onClose,
}: {
  title: string;
  /** What has to be typed (service or volume name). */
  name: string;
  message: ReactNode;
  confirmLabel: string;
  inputLabel: string;
  run: () => Promise<unknown> | unknown;
  onClose: () => void;
}) {
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const hintId = useId();
  const ok = text.trim() === name;
  const submit = async () => {
    if (!ok || busy) return;
    setBusy(true);
    setError(null);
    try {
      await run();
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open
      onClose={() => !busy && onClose()}
      title={title}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={busy}>
            {t("취소")}
          </Button>
          <Button variant="danger" disabled={!ok} loading={busy} onClick={submit}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      <div className="flex gap-4">
        <span className="flex size-10 shrink-0 items-center justify-center rounded-full bg-red-500/10">
          <AlertTriangle className="size-5 text-red-500" aria-hidden />
        </span>
        <div className="min-w-0 space-y-3">
          <div>{message}</div>
          <label className="block">
            <span id={hintId} className="mb-2 block text-xs font-semibold leading-[18px] text-gray-600">
              {tr("확인을 위해 {name}을(를) 입력하세요", {
                name: <span className="required-confirm-text select-all font-mono font-bold text-foreground">{name}</span>,
              })}
            </span>
            <Input
              size="sm"
              mono
              autoFocus
              className={FIELD}
              value={text}
              onChange={(e) => setText(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !e.nativeEvent.isComposing) {
                  e.preventDefault();
                  void submit();
                }
              }}
              aria-label={inputLabel}
              aria-describedby={hintId}
              autoComplete="off"
              spellCheck={false}
            />
          </label>
          {error ? <ErrorBox error={error} /> : null}
        </div>
      </div>
    </Dialog>
  );
}
