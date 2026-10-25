"use client";

/* Shared building blocks for the service Settings tab (layout modeled on Railway's dashboard, design spec §4.5).
   Every setting is a SettingItem so the sticky filter can show/hide it individually. */

import type { ReactNode } from "react";
import { AlertTriangle, Plus } from "lucide-react";
import { t } from "@/lib/i18n";
import { Button, Toggle, cx } from "../../ui";

/**
 * One setting: section header, modeled on Railway's dashboard (`grid gap-1 mb-3`, title `text-sm font-semibold`, description `text-sm text-gray-600`).
 * `data-setting` + `data-keywords` feed the filter; the header text is matched too.
 */
export function SettingItem({
  label,
  description,
  children,
  badge,
  keywords,
  className,
  danger,
  id,
}: {
  label: string;
  description?: ReactNode;
  children: ReactNode;
  badge?: ReactNode;
  /** Extra search terms (English aliases etc.) that are not visible in the header. */
  keywords?: string;
  className?: string;
  danger?: boolean;
  id?: string;
}) {
  return (
    <div id={id} data-setting="" data-keywords={keywords} className={cx("min-w-0", className)}>
      <header data-setting-head="" className="mb-3 grid gap-1">
        <h3 className={cx("flex cursor-default items-center gap-2 text-sm font-semibold", danger ? "text-red-500" : "text-foreground")}>
          {label}
          {badge}
        </h3>
        {description && <p className={cx("text-sm", danger ? "text-red-600" : "text-gray-600")}>{description}</p>}
      </header>
      {children}
    </div>
  );
}

/** Pink dot next to a setting label while it has an unapplied (staged) change. */
export function Changed({ on }: { on: boolean }) {
  return on ? <span className="size-1.5 shrink-0 rounded-full bg-pink-600" title={t("적용하지 않은 변경")} aria-label={t("적용하지 않은 변경")} /> : null;
}

export function Hint({ children, tone, className }: { children: ReactNode; tone?: "warn" | "danger"; className?: string }) {
  return (
    <p
      className={cx(
        "mt-2 text-xs leading-[18px]",
        tone === "warn" ? "text-yellow-600" : tone === "danger" ? "text-red-600" : "text-gray-600",
        className,
      )}
    >
      {children}
    </p>
  );
}

export const CARD = "px-4 py-2 bg-secondaryBg dark:bg-[#201F2D] border border-gray-100 rounded-md shadow-sm";

/**
 * Toggle card, modeled on Railway's dashboard: switch + label inside the raised surface (e.g. outbound IPv6 or serverless).
 * The label wraps the switch so clicking the text toggles it.
 */
export function ToggleCard({
  checked,
  onChange,
  label,
  disabled,
  className,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: string;
  disabled?: boolean;
  className?: string;
}) {
  return (
    <div className={cx(CARD, className)}>
      <label className="flex w-full items-center space-x-4">
        <Toggle checked={checked} onChange={onChange} label={label} disabled={disabled} />
        <span className={cx("w-full select-none", disabled ? "cursor-not-allowed opacity-60" : "cursor-pointer")}>{label}</span>
      </label>
    </div>
  );
}

export function AddButton({ children, onClick, disabled }: { children: ReactNode; onClick: () => void; disabled?: boolean }) {
  return (
    <Button variant="outline" onClick={onClick} disabled={disabled} className="w-max">
      <span className="text-gray-500">
        <Plus />
      </span>
      <span className="inline-block min-w-0">{children}</span>
    </Button>
  );
}

export function WarnBanner({ title, children, className }: { title: ReactNode; children?: ReactNode; className?: string }) {
  return (
    <div data-setting-note="" role="note" className={cx("relative flex items-center gap-3 rounded-md bg-warn-surface p-3 text-sm text-warn", className)}>
      <span className="icon-container icon-md mr-3 shrink-0 text-warn">
        <AlertTriangle className="size-5" />
      </span>
      <div className="grid gap-1">
        <p className="font-semibold">{title}</p>
        {children && <p>{children}</p>}
      </div>
    </div>
  );
}

export function PrimaryNotice({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cx("relative flex items-center gap-3 rounded-md bg-primary-surface p-3 text-sm text-primary", className)}>{children}</div>;
}

/**
 * Settings controls are 34px tall so an Input and an inline Button (keel md = Railway sm, 34px) line up.
 * `Input size="sm"` + FIELD gives Railway's sm input height with the md horizontal padding.
 */
export const FIELD = "px-3";
