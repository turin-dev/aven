"use client";

/* Shared pieces of Settings > Networking, modeled on Railway's dashboard:
   section header link, 26px/18px ghost icon buttons, the sliding edit container, the bordered input with a suffix
   and the edit-panel footer (info line + Cancel/Update). */

import { useEffect, useRef, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ArrowRight, Check, Copy, Info } from "lucide-react";
import type { CanvasService, Environment, ID } from "@/lib/api";
import { qk } from "@/lib/queries";
import { Button, cx } from "../../ui";
import { t } from "@/lib/i18n";


/**
 * Networking sub-section, modeled on Railway's dashboard: `<div id>` + `header.grid.gap-1.mb-3` with the title as an anchor link
 * (`text-sm font-semibold cursor-pointer`) and, for Private/Outbound, the `h2.text-sm.text-gray-600` description.
 * `data-setting` / `data-setting-head` keep it filterable by the Settings filter.
 */
export function NetSection({
  id,
  title,
  description,
  keywords,
  badge,
  children,
}: {
  id: string;
  title: string;
  description?: ReactNode;
  keywords?: string;
  badge?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div id={id} data-setting="" data-keywords={keywords} className="min-w-0">
      <header data-setting-head="" className="mb-3 grid gap-1">
        <a
          href={`#${id}`}
          onClick={(e) => {
            e.preventDefault();
            document.getElementById(id)?.scrollIntoView({ behavior: "smooth", block: "start" });
          }}
          className="flex cursor-pointer items-center gap-2 text-sm font-semibold focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-700"
        >
          {title}
          {badge}
        </a>
        {description && <h2 className="text-sm text-gray-600">{description}</h2>}
      </header>
      {children}
    </div>
  );
}


/* ── buttons ─────────────────────────────────────────────────────────── */
const GHOST =
  "group/button flex items-center justify-center border transform transition-transform duration-50 active:scale-95 focus:outline-none focus-visible:ring-2 disabled:cursor-not-allowed disabled:opacity-50 disabled:active:scale-100 bg-transparent border-transparent text-foreground hover:border-gray-100 disabled:bg-transparent disabled:border-transparent focus-visible:ring-gray-600 focus-visible:bg-gray-100 px-0 rounded-md hover:bg-gray-200 cursor-pointer";

export function IconBtn({
  title,
  onClick,
  children,
  disabled,
  small,
  pressed,
}: {
  title: string;
  onClick?: () => void;
  children: ReactNode;
  disabled?: boolean;
  small?: boolean;
  pressed?: boolean;
}) {
  return (
    <button
      type="button"
      title={title}
      aria-label={title}
      aria-pressed={pressed}
      onClick={onClick}
      disabled={disabled}
      className={cx(GHOST, small ? "h-[18px] w-[18px] space-x-1.5 py-1 text-[10px] leading-4" : "h-[26px] w-[26px] space-x-1.5 py-1 text-xs leading-4")}
    >
      <span className={cx("icon-container icon-md text-2xl text-foreground", small ? "!h-[12px] !w-[12px] [&_svg]:!size-[12px]" : "!h-[14px] !w-[14px] [&_svg]:!size-[14px]")} aria-hidden="true">
        {children}
      </span>
    </button>
  );
}

export function CopyBtn({ value, title = t("복사"), small }: { value: string; title?: string; small?: boolean }) {
  const [ok, setOk] = useState(false);
  return (
    <IconBtn
      title={title}
      small={small}
      onClick={() => {
        void navigator.clipboard?.writeText(value);
        setOk(true);
        setTimeout(() => setOk(false), 1500);
      }}
    >
      {ok ? <Check /> : <Copy />}
    </IconBtn>
  );
}


export function HostLink({ host, href, block }: { host: string; href?: string; block?: boolean }) {
  return (
    <a
      href={href ?? `https://${host}/`}
      target="_blank"
      rel="noreferrer"
      className={cx(
        "group/link overflow-hidden overflow-ellipsis truncate focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-700",
        block && "block text-left text-sm",
      )}
    >
      <span className="group-hover/link:underline">{host}</span>
      <span aria-hidden="true" className="invisible ml-1 text-gray-500 group-hover/link:visible">
        ↗
      </span>
    </a>
  );
}

/**
 * Port row meta line, modeled on Railway's dashboard: `p.text-xs.text-gray-600.font-medium` > `span.inline-flex` (→ icon + "Port N"), followed by
 * ` · ` separated extras. Children are spans, not block elements, so the markup stays valid inside the `<p>`.
 */
export function PortMeta({ port, label = t("포트"), children }: { port: number | string; label?: string; children?: ReactNode }) {
  return (
    <p className="text-xs font-medium text-gray-600">
      <span className="inline-flex items-center space-x-1 align-middle text-gray-600">
        <span aria-hidden="true" className="icon-container icon-sm text-lg text-current">
          <ArrowRight />
        </span>
        <span className="block">
          {label} <span className="font-mono">{port}</span>
        </span>
      </span>
      {children}
    </p>
  );
}


/**
 * Sliding container: a list child that animates its height open (closed = `height:0; opacity:0`,
 * `pointer-events-none`). Content mounts on open and unmounts after the close animation, so forms start fresh.
 */
export function Sliding({ open, children, id }: { open: boolean; children: ReactNode; id?: string }) {
  const [mounted, setMounted] = useState(open);
  const [shown, setShown] = useState(open);
  useEffect(() => {
    if (open) {
      setMounted(true);
      const f = requestAnimationFrame(() => setShown(true));
      return () => cancelAnimationFrame(f);
    }
    setShown(false);
    const timer = setTimeout(() => setMounted(false), 240);
    return () => clearTimeout(timer);
  }, [open]);
  return (
    <div
      aria-controls={id}
      aria-expanded={open}
      className={cx("grid transition-[grid-template-rows,opacity] duration-[240ms] ease-[cubic-bezier(0.22,1,0.36,1)]", !open && "pointer-events-none")}
      style={{ margin: 0, gridTemplateRows: shown ? "1fr" : "0fr", opacity: shown ? 1 : 0 }}
    >
      <div className="min-h-0 overflow-hidden">{mounted && <div className={cx("space-y-4 p-4", !open && "pointer-events-none")}>{children}</div>}</div>
    </div>
  );
}


/* ── inputs ──────────────────────────────────────────────────────────── */
export const INPUT =
  "w-full min-w-0 rounded-md text-foreground bg-transparent border border-gray-200 placeholder-gray-500 focus:outline-none relative z-10 hover:border-gray-400 focus:border-pink-400 text-base leading-6 px-3 py-2";
export const INPUT_DISABLED =
  "w-full min-w-0 rounded-md border border-gray-200 placeholder-gray-500 focus:outline-none relative z-10 bg-gray-100 text-gray-600 cursor-not-allowed text-base leading-6 px-3 py-2";
export const SR_LABEL = "block text-xs font-semibold leading-[18px] text-gray-600 mb-2 sr-only";

export function SuffixInput({
  label,
  name,
  value,
  onChange,
  suffix,
  disabled,
  placeholder,
  autoFocus,
  invalid,
  pl4,
  className,
}: {
  label: string;
  name: string;
  value: string;
  onChange?: (v: string) => void;
  suffix?: string;
  disabled?: boolean;
  placeholder?: string;
  autoFocus?: boolean;
  invalid?: boolean;
  /** DNS name input (`pl-4`). */
  pl4?: boolean;
  className?: string;
}) {
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (autoFocus) ref.current?.focus({ preventScroll: true });
  }, [autoFocus]);
  return (
    <div className={cx("flex items-center justify-between rounded-md border border-gray-200 focus-within:border-pink-400", suffix && "pr-4", invalid && "!border-red-500", className)}>
      <div className="flex w-full flex-col">
        <div className="flex items-center justify-between">
          <div className="flex space-x-1">
            <label className={SR_LABEL} htmlFor={`input-${name}`}>
              {label}
            </label>
          </div>
        </div>
        <div className="relative flex w-full items-center">
          <input
            ref={ref}
            id={`input-${name}`}
            name={name}
            value={value}
            disabled={disabled}
            placeholder={placeholder}
            spellCheck={false}
            autoComplete="off"
            onChange={(e) => onChange?.(e.target.value)}
            className={cx(disabled ? INPUT_DISABLED : INPUT, "border-none", pl4 && "pl-4")}
          />
        </div>
      </div>
      {suffix && <p className="whitespace-nowrap text-base text-gray-500">{suffix}</p>}
    </div>
  );
}

/* ── panel footer ────────────────────────────────────────────────────── */

export function PanelFooter({
  message,
  tone = "info",
  icon,
  onCancel,
  submitLabel = t("업데이트"),
  canSubmit,
  busy,
}: {
  message: ReactNode;
  tone?: "info" | "ok" | "bad";
  icon?: ReactNode;
  onCancel: () => void;
  submitLabel?: string;
  canSubmit: boolean;
  busy?: boolean;
}) {
  return (
    <div className="flex items-center justify-between space-x-4">
      <div className={cx("flex min-w-0 items-center", tone === "info" ? "space-x-3 text-gray-500" : "space-x-1.5", tone === "ok" && "text-green-500", tone === "bad" && "text-red-500")}>
        <span aria-hidden="true" className="icon-container icon-md shrink-0 text-2xl text-current">
          {icon ?? <Info />}
        </span>
        <p className="text-sm">{message}</p>
      </div>
      <div className="flex shrink-0 items-center space-x-2">
        <Button variant="outline" className="dark:text-white" onClick={onCancel}>
          <span className="inline-block min-w-0">{t("취소")}</span>
        </Button>
        <Button type="submit" variant="primary" disabled={!canSubmit} loading={busy}>
          <span className="inline-block min-w-0">{submitLabel}</span>
        </Button>
      </div>
    </div>
  );
}

/* ── mutations ───────────────────────────────────────────────────────── */

/** Runs a networking mutation and refreshes the views that show its result. */
export function useNetRun(svc: CanvasService, env: Environment) {
  const qc = useQueryClient();
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const run = async (key: string, fn: () => Promise<unknown>) => {
    setBusy(key);
    setError(null);
    try {
      await fn();
      await Promise.all([
        qc.invalidateQueries({ queryKey: qk.networking(svc.id, env.id) }),
        qc.invalidateQueries({ queryKey: qk.canvas(env.id) }),
        qc.invalidateQueries({ queryKey: qk.resolved(svc.id, env.id) }),
      ]);
      return true;
    } catch (e) {
      setError(e);
      return false;
    } finally {
      setBusy(null);
    }
  };
  return { run, busy, error, setError };
}

export type NetCtx = { svc: CanvasService; env: Environment; sid: ID; eid: ID };

export const portOk = (value: string) => /^\d{1,5}$/.test(value) && Number(value) >= 1 && Number(value) <= 65535;
