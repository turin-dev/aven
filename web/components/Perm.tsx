"use client";

/* v1.5 role-based UI: shared helpers to disable mutating actions the current role may not run.
   The server enforces the same rules (403); this only keeps viewers from hitting dead ends. */

import type { CSSProperties, ReactNode, SyntheticEvent } from "react";
import { Lock } from "lucide-react";
import type { MenuEntry } from "./ContextMenu";
import { cx } from "./ui";
import { t } from "@/lib/i18n";

export const NO_PERM = t("권한이 없습니다");

const swallow = (e: SyntheticEvent) => {
  e.preventDefault();
  e.stopPropagation();
};

const lockedStyle: CSSProperties = { opacity: 0.45, cursor: "not-allowed" };

type LockProps = {
  "aria-disabled"?: true;
  title?: string;
  onClickCapture?: (e: SyntheticEvent) => void;
  onPointerDownCapture?: (e: SyntheticEvent) => void;
  style?: CSSProperties;
};

/**
 * Spread onto a button (Button, IconButton, raw <button>) after its own `title`/`style` so these win.
 * When not allowed the button stays focusable but is aria-disabled, shows the "권한이 없습니다" tooltip
 * and swallows clicks (including implicit form submission, which clicks the submit button).
 */
export function lock(allowed: boolean): LockProps {
  if (allowed) return {};
  return { "aria-disabled": true, title: NO_PERM, onClickCapture: swallow, onPointerDownCapture: swallow, style: lockedStyle };
}

/** Context menu entries: disable with the permission reason when not allowed (separators and labels pass through). */
export function lockItems(allowed: boolean, items: MenuEntry[]): MenuEntry[] {
  if (allowed) return items;
  return items.map((e) => (e.kind === "sep" || e.kind === "label" ? e : { ...e, disabled: true, reason: NO_PERM, children: undefined }));
}

/** Disables every form control inside when `readOnly` (role without edit permission) and explains why. */
export function PermFieldset({ readOnly, children, note, className }: { readOnly: boolean; children: ReactNode; note?: ReactNode; className?: string }) {
  if (!readOnly) return <>{children}</>;
  return (
    <>
      <p role="note" className={cx("mb-4 flex items-center gap-2 rounded-md border border-gray-200 bg-gray-50 px-3 py-2 text-sm text-gray-500", className)}>
        <Lock className="size-3.5 shrink-0" aria-hidden />
        <span>
          {NO_PERM}. {note ?? t("보기 권한으로는 내용을 볼 수만 있습니다.")}
        </span>
      </p>
      <fieldset disabled aria-disabled title={NO_PERM} className="contents m-0 min-w-0 border-0 p-0">
        {children}
      </fieldset>
    </>
  );
}
