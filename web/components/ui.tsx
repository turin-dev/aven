"use client";

import {
  Children,
  Fragment,
  forwardRef,
  isValidElement,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type ReactElement,
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
} from "react";
import { createPortal } from "react-dom";
import Link from "next/link";
import { t } from "@/lib/i18n";
import { AlertCircle, Check, CheckCircle2, ChevronDown, Copy, Loader2, X } from "lucide-react";

export function cx(...c: (string | false | null | undefined)[]) {
  return c.filter(Boolean).join(" ");
}


/* ── Escape-key stack: only the topmost layer reacts ─────────────────── */
const escStack: { current: () => void }[] = [];
let escBound = false;
function onEscKey(e: KeyboardEvent) {
  if (e.key !== "Escape" || escStack.length === 0) return;
  e.preventDefault();
  escStack[escStack.length - 1].current();
}
export function useEscape(handler: () => void, enabled = true) {
  const ref = useRef(handler);
  ref.current = handler;
  useEffect(() => {
    if (!enabled) return;
    if (!escBound) {
      window.addEventListener("keydown", onEscKey);
      escBound = true;
    }
    const entry = { current: () => ref.current() };
    escStack.push(entry);
    return () => {
      const i = escStack.indexOf(entry);
      if (i >= 0) escStack.splice(i, 1);
    };
  }, [enabled]);
}


type Variant = "primary" | "default" | "danger" | "danger-outline" | "ghost" | "accent" | "success" | "link";

const variants: Record<Variant, string> = {
  primary:
    "bg-pink-500 border-pink-500 text-white hover:bg-pink-600 hover:border-pink-600 disabled:bg-pink-500 disabled:border-pink-500 focus-visible:ring-pink-600",
  default:
    "bg-gray-100 border-gray-200 text-foreground hover:bg-gray-200 hover:border-gray-300 disabled:bg-gray-100 disabled:border-gray-200 focus-visible:ring-gray-600",
  danger:
    "bg-red-500 border-red-500 text-white hover:bg-red-600 hover:border-red-600 disabled:bg-red-500 disabled:border-red-500 focus-visible:ring-red-600",
  "danger-outline":
    "bg-transparent border-red-200 text-red-500 dark:text-red-700 hover:bg-red-100 hover:border-red-200 disabled:bg-transparent disabled:border-red-200 focus-visible:ring-red-600 focus-visible:bg-red-100",
  ghost:
    "bg-transparent border-transparent text-foreground hover:bg-gray-100 hover:border-gray-100 disabled:bg-transparent disabled:border-transparent focus-visible:ring-gray-600 focus-visible:bg-gray-100",
  accent:
    "bg-transparent border-pink-200 text-pink-500 dark:text-pink-700 hover:bg-pink-100 hover:border-pink-200 disabled:bg-transparent disabled:border-pink-200 disabled:text-pink-700 focus-visible:ring-pink-600 focus-visible:bg-pink-100",
  link:
    "bg-transparent border-transparent text-pink-500 dark:text-pink-700 hover:bg-pink-100 hover:border-pink-100 disabled:bg-transparent disabled:border-transparent focus-visible:ring-pink-600 focus-visible:bg-pink-100",
  success:
    "bg-transparent border-green-200 text-green-500 dark:text-green-700 hover:bg-green-100 hover:border-green-200 disabled:bg-transparent disabled:border-green-200 disabled:text-green-700 focus-visible:ring-green-600 focus-visible:bg-green-100",
};

const OUTLINE_GRAY =
  "bg-transparent border-gray-200 text-foreground hover:bg-gray-100 hover:border-gray-200 disabled:bg-transparent disabled:border-gray-200 disabled:text-foreground focus-visible:ring-gray-600 focus-visible:bg-gray-100";

const BUTTON_BASE =
  "group/button flex shrink-0 cursor-pointer items-center justify-center whitespace-nowrap border transform transition-transform duration-50 active:scale-95 focus:outline-none focus-visible:ring-2 disabled:cursor-not-allowed disabled:opacity-50 disabled:active:scale-100";

const BUTTON_SIZES = {
  lg: "h-[42px] py-2 px-3 rounded-md text-base leading-6 gap-3",
  md: "h-[34px] py-1.5 px-3 rounded-md text-sm leading-5 gap-2",
  sm: "h-[26px] py-1 px-1.5 rounded-md text-xs leading-4 gap-1.5",
};
const BUTTON_ICON_SIZES = {
  lg: "[&_svg]:!size-5 [&_svg]:shrink-0",
  md: "[&_svg]:!size-4 [&_svg]:shrink-0",
  sm: "[&_svg]:!size-[14px] [&_svg]:shrink-0",
};

export type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: Variant | "outline";
  size?: "sm" | "md" | "lg";
  loading?: boolean;
};

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = "default", size = "md", loading, className, children, disabled, type = "button", ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      type={type}
      {...rest}
      disabled={disabled || loading}
      className={cx(
        BUTTON_BASE,
        BUTTON_SIZES[size],
        BUTTON_ICON_SIZES[size],
        variant === "outline" ? OUTLINE_GRAY : variants[variant],
        className,
      )}
    >
      {loading && <Loader2 className={cx("animate-spin", size === "sm" ? "size-3" : "size-4")} />}
      {children}
    </button>
  );
});

export const IconButton = forwardRef<
  HTMLButtonElement,
  ButtonHTMLAttributes<HTMLButtonElement> & { label: string; size?: "sm" | "md" }
>(function IconButton({ label, size = "md", className, children, type = "button", ...rest }, ref) {
  return (
    <button
      ref={ref}
      type={type}
      aria-label={label}
      title={label}
      {...rest}
      className={cx(
        BUTTON_BASE,
        "bg-transparent border-transparent text-gray-500 hover:bg-gray-100 hover:border-gray-100 disabled:bg-transparent disabled:border-transparent focus-visible:ring-gray-600 focus-visible:bg-gray-100",
        "px-0",
        size === "sm" ? "h-[26px] w-[26px] rounded-md" : "h-[34px] w-[34px] rounded-md",
        BUTTON_ICON_SIZES[size],
        className,
      )}
    >
      {children}
    </button>
  );
});


const inputCls =
  "min-w-0 rounded-md text-foreground bg-transparent border border-gray-200 placeholder-gray-500 focus:outline-none relative z-10 hover:border-gray-400 focus:border-pink-400 disabled:bg-gray-100 disabled:text-gray-600 disabled:cursor-not-allowed disabled:hover:border-gray-200 read-only:text-gray-600";
const INPUT_SIZES = {
  md: "text-base leading-6 px-3 py-2",
  sm: "text-sm leading-5 px-1.5 py-1.5",
};

export const Input = forwardRef<
  HTMLInputElement,
  Omit<InputHTMLAttributes<HTMLInputElement>, "size"> & { mono?: boolean; size?: "sm" | "md"; invalid?: boolean }
>(function Input({ className, mono, size = "md", invalid, ...rest }, ref) {
  return (
    <input
      ref={ref}
      spellCheck={false}
      {...rest}
      className={cx(
        inputCls,
        !/(^|\s)w-/.test(className ?? "") && "w-full",
        INPUT_SIZES[size],
        invalid && "!border-red-500",
        mono && "font-mono !text-sm !leading-5",
        className,
      )}
    />
  );
});

export const Textarea = forwardRef<
  HTMLTextAreaElement,
  TextareaHTMLAttributes<HTMLTextAreaElement> & { mono?: boolean }
>(function Textarea({ className, mono, ...rest }, ref) {
  return (
    <textarea
      ref={ref}
      spellCheck={false}
      {...rest}
      className={cx(inputCls, INPUT_SIZES.md, "w-full", mono && "font-mono !text-sm !leading-5", className)}
    />
  );
});

export const LABEL_CLS = "block text-xs font-semibold leading-[18px] text-gray-600 mb-2";

export function Field({
  label,
  hint,
  required,
  children,
  className,
}: {
  label: string;
  hint?: ReactNode;
  required?: boolean;
  children: ReactNode;
  className?: string;
}) {
  return (
    <label className={cx("block w-full", className)}>
      <div className="flex items-center space-x-1">
        <span className={LABEL_CLS}>{label}</span>
        {required && <span className="mb-2 text-xs text-red-500">*</span>}
      </div>
      {children}
      {hint && <span className="mt-2 block text-xs font-normal leading-[18px] text-gray-600">{hint}</span>}
    </label>
  );
}


/* ── Feedback ────────────────────────────────────────────────────────── */
export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
export function ErrorBox({ error, className }: { error: unknown; className?: string }) {
  if (!error) return null;
  return (
    <div
      role="alert"
      className={cx("relative flex items-center rounded-md bg-danger-surface p-3 text-sm text-danger", className)}
    >
      <AlertCircle className="mr-3 size-4 shrink-0 text-danger" />
      <span className="min-w-0 break-words">{errorMessage(error)}</span>
    </div>
  );
}
export function Notice({
  children,
  action,
  onDismiss,
  className,
}: {
  children: ReactNode;
  action?: ReactNode;
  onDismiss?: () => void;
  className?: string;
}) {
  return (
    <div
      role="status"
      className={cx("relative flex items-center rounded-md bg-success-surface p-3 text-sm text-success", className)}
    >
      <CheckCircle2 className="mr-3 size-4 shrink-0 text-success" />
      <span className="min-w-0 flex-1">{children}</span>
      {action}
      {onDismiss && (
        <IconButton label={t("닫기")} size="sm" onClick={onDismiss}>
          <X />
        </IconButton>
      )}
    </div>
  );
}
export function Spinner({ className }: { className?: string }) {
  return <Loader2 aria-label={t("불러오는 중")} className={cx("size-4 animate-spin text-gray-600", className)} />;
}
export function CenterSpinner() {
  return (
    <div className="flex justify-center py-16">
      <Spinner />
    </div>
  );
}
/* ── Dialog ──────────────────────────────────────────────────────────── */
export function Dialog({
  open,
  onClose,
  title,
  children,
  footer,
  wide,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  footer: ReactNode;
  /** Wider dialog with a scrollable body (diffs, long lists). */
  wide?: boolean;
}) {
  const id = useId();
  useEscape(onClose, open);
  if (!open || typeof document === "undefined") return null;
  return createPortal(
    <div
      className="fixed inset-0 z-[70] flex animate-modal-overlay-in items-end justify-stretch bg-black/[.45] dark:bg-black/80 sm:items-center sm:justify-center sm:px-5"
      onMouseDown={(e) => e.target === e.currentTarget && onClose()}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby={id}
        className={cx(
          "relative w-full max-w-full animate-modal-from-bottom-in overflow-hidden rounded-t-lg bg-background p-6 outline-none dark:bg-secondaryBg sm:w-auto sm:animate-modal-scale-in sm:rounded-lg",
          wide ? "flex max-h-[88dvh] flex-col" : "max-h-full",
        )}
      >
        <div id={id} className="flex pb-5 pr-10 text-left text-xl font-semibold text-foreground">
          {title}
        </div>
        <IconButton label={t("닫기")} size="sm" onClick={onClose} className="absolute right-6 top-6 z-50 rounded-sm">
          <X />
        </IconButton>
        <div
          className={cx(
            "w-full max-w-full text-sm text-gray-600",
            wide ? "min-h-0 flex-1 overflow-y-auto sm:w-[759px]" : "sm:w-[431px] md:w-[499px]",
          )}
        >
          {children}
        </div>
        <footer className="mt-6 flex w-full">
          <div className="ml-auto flex w-full justify-end gap-x-4 sm:w-auto sm:whitespace-nowrap">{footer}</div>
        </footer>
      </div>
    </div>,
    document.body,
  );
}
/** Button that asks for confirmation in a dialog before running onConfirm. */
export function useConfirm() {
  const [state, setState] = useState<{
    title: string;
    message: ReactNode;
    confirmLabel: string;
    danger?: boolean;
    run: () => Promise<unknown> | unknown;
  } | null>(null);
  const [busy, setBusy] = useState(false);
  const node = (
    <Dialog
      open={!!state}
      onClose={() => !busy && setState(null)}
      title={state?.title ?? ""}
      footer={
        <>
          <Button variant="outline" onClick={() => setState(null)} disabled={busy}>
            {t("취소")}
          </Button>
          <Button
            variant={state?.danger ? "danger" : "primary"}
            loading={busy}
            autoFocus
            onClick={async () => {
              if (!state) return;
              setBusy(true);
              try {
                await state.run();
              } finally {
                setBusy(false);
                setState(null);
              }
            }}
          >
            {state?.confirmLabel}
          </Button>
        </>
      }
    >
      {state?.message}
    </Dialog>
  );
  return { confirm: setState, node };
}
/* ── Menu (popover) ──────────────────────────────────────────────────── */
export function Menu({
  trigger,
  children,
  align = "end",
  side = "bottom",
  label,
}: {
  trigger: (props: { onClick: () => void; "aria-expanded": boolean; "aria-haspopup": "menu"; "data-state": "open" | "closed" }) => ReactNode;
  children: (close: () => void) => ReactNode;
  align?: "start" | "end";
  /** "right" opens beside the trigger (left rail account button). */
  side?: "bottom" | "right";
  label: string;
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useEscape(() => setOpen(false), open);
  useEffect(() => {
    if (!open) return;
    const h = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", h);
    return () => document.removeEventListener("mousedown", h);
  }, [open]);
  return (
    <div ref={ref} className="relative">
      {trigger({ onClick: () => setOpen((o) => !o), "aria-expanded": open, "aria-haspopup": "menu", "data-state": open ? "open" : "closed" })}
      {open && (
        <div
          role="menu"
          aria-label={label}
          className={cx(
            "absolute z-[60] max-h-[70dvh] min-w-[219px] animate-pop-in overflow-y-auto rounded-md border border-black/10 bg-background p-1 shadow dark:border-white/10",
            side === "right" ? "bottom-0 left-full ml-2" : cx("top-full mt-2", align === "end" ? "right-0" : "left-0"),
          )}
        >
          {children(() => setOpen(false))}
        </div>
      )}
    </div>
  );
}
export function MenuItem({
  children,
  onSelect,
  danger,
  disabled,
  reason,
  icon,
  className,
}: {
  className?: string;
  children: ReactNode;
  onSelect: () => void;
  danger?: boolean;
  disabled?: boolean;
  /** When set with `disabled`, the item stays focusable (aria-disabled) and shows this as a tooltip. */
  reason?: string;
  icon?: ReactNode;
}) {
  const soft = !!(disabled && reason);
  return (
    <button
      type="button"
      role="menuitem"
      disabled={disabled && !soft}
      aria-disabled={soft || undefined}
      title={soft ? reason : undefined}
      onClick={soft ? undefined : onSelect}
      className={cx(
        "group relative mb-0.5 flex h-9 w-full cursor-pointer select-none items-center rounded px-3 py-2 text-left text-sm font-medium leading-4 last:mb-0 focus:outline-none",
        "disabled:cursor-default disabled:opacity-40",
        soft && "cursor-not-allowed opacity-40",
        className,
        danger
          ? "text-danger hover:bg-danger-surface-hover focus:bg-danger-surface-hover"
          : "text-neutral-secondary hover:bg-neutral-surface-hover hover:text-neutral focus:bg-neutral-surface-hover",
      )}
    >
      {icon && <span className={cx("mr-3 shrink-0 [&_svg]:size-4", danger ? "text-danger" : "text-neutral-secondary")}>{icon}</span>}
      {children}
    </button>
  );
}


export function AvenMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" aria-hidden="true" className={cx("size-6", className)}>
      <rect width="24" height="24" rx="6" fill="#9C6BFF" />
      <path d="M12 4.25 4.6 19.5h4.05L12 12.35l3.35 7.15h4.05L12 4.25Zm0 5.4 1.3 2.8h-2.6l1.3-2.8Z" fill="#fff" fillRule="evenodd" />
    </svg>
  );
}


/* ── Select / Toggle / Segmented ────────────────────────────────────── */
type SelOpt = { value: string; label: ReactNode; disabled?: boolean; group?: string };
function flattenOptions(children: ReactNode, group?: string): SelOpt[] {
  const out: SelOpt[] = [];
  Children.forEach(children, (c) => {
    if (!isValidElement(c)) return;
    const el = c as ReactElement<{ value?: string | number; children?: ReactNode; disabled?: boolean; label?: string }>;
    if (el.type === "optgroup") out.push(...flattenOptions(el.props.children, el.props.label));
    else if (el.type === Fragment) out.push(...flattenOptions(el.props.children, group));
    else if (el.type === "option") {
      const label = el.props.children ?? el.props.value;
      out.push({ value: String(el.props.value ?? (typeof label === "string" ? label : "")), label, disabled: el.props.disabled, group });
    }
  });
  return out;
}

/**
 * Dropdown in the react-select style, modeled on Railway's dashboard. Keeps the native <select> API (<option> children, onChange with e.target.value)
 * but renders a bordered trigger and a portal list with check mark, keyboard navigation and type-ahead search.
 */
export function Select({
  className,
  children,
  value,
  onChange,
  disabled,
  required,
  id,
  name,
  size = "md",
  ...rest
}: Omit<SelectHTMLAttributes<HTMLSelectElement>, "onChange" | "size"> & {
  size?: "sm" | "md";
 onChange?: (e: { target: { value: string; name?: string }; currentTarget: { value: string } }) => void }) {
  const opts = useMemo(() => flattenOptions(children), [children]);
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState("");
  const [hi, setHi] = useState(0);
  const [rect, setRect] = useState<DOMRect | null>(null);
  const btn = useRef<HTMLButtonElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const lid = useId();
  const cur = opts.find((o) => o.value === String(value ?? ""));
  const shown = q ? opts.filter((o) => String(typeof o.label === "string" ? o.label : o.value).toLowerCase().includes(q.toLowerCase())) : opts;
  const close = () => {
    setOpen(false);
    setQ("");
  };
  const pick = (o: SelOpt) => {
    if (o.disabled) return;
    onChange?.({ target: { value: o.value, name }, currentTarget: { value: o.value } });
    close();
    btn.current?.focus();
  };
  const openList = () => {
    if (disabled) return;
    setRect(btn.current?.getBoundingClientRect() ?? null);
    setHi(Math.max(0, opts.findIndex((o) => o.value === String(value ?? ""))));
    setOpen(true);
  };
  useEffect(() => {
    if (!open) return;
    const away = (e: MouseEvent) => {
      if (!btn.current?.contains(e.target as Node) && !list.current?.contains(e.target as Node)) close();
    };
    const move = () => close();
    document.addEventListener("mousedown", away);
    window.addEventListener("scroll", move, true);
    window.addEventListener("resize", move);
    return () => {
      document.removeEventListener("mousedown", away);
      window.removeEventListener("scroll", move, true);
      window.removeEventListener("resize", move);
    };
  }, [open]);
  useEffect(() => {
    if (open) list.current?.querySelector<HTMLElement>(`[data-i="${hi}"]`)?.scrollIntoView({ block: "nearest" });
  }, [hi, open]);
  const onKey = (e: React.KeyboardEvent) => {
    if (!open) {
      if (["ArrowDown", "ArrowUp", "Enter", " "].includes(e.key)) {
        e.preventDefault();
        openList();
      }
      return;
    }
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setHi((h) => Math.min(shown.length - 1, h + 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setHi((h) => Math.max(0, h - 1));
    } else if (e.key === "Enter") {
      e.preventDefault();
      if (shown[hi]) pick(shown[hi]);
    } else if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      close();
    } else if (e.key === "Backspace") setQ((x) => x.slice(0, -1));
    else if (e.key.length === 1 && !e.ctrlKey && !e.metaKey) {
      e.preventDefault();
      setQ((x) => x + e.key);
      setHi(0);
    } else if (e.key === "Tab") close();
  };
  return (
    <span className={cx("relative flex w-auto flex-col", disabled && "opacity-60", className)}>
      <button
        ref={btn}
        id={id}
        type="button"
        role="combobox"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? lid : undefined}
        aria-label={rest["aria-label"]}
        aria-required={required}
        disabled={disabled}
        onClick={() => (open ? close() : openList())}
        onKeyDown={onKey}
        className={cx(
          "flex w-full min-w-0 cursor-pointer items-center justify-between gap-2 rounded-md border border-gray-200 bg-transparent px-3 text-left text-foreground",
          size === "sm" ? "h-[34px] py-1 text-sm leading-5" : "h-[42px] py-2 text-base leading-6",
          " hover:border-gray-400 focus:outline-none focus-visible:border-pink-400 disabled:cursor-not-allowed",
        )}
      >
        <span className={cx("min-w-0 flex-1 truncate", !cur && "text-gray-500")}>{cur ? cur.label : t("선택…")}</span>
        <ChevronDown className="size-4 shrink-0 text-gray-500" />
      </button>
      {open &&
        rect &&
        createPortal(
          <div
            ref={list}
            id={lid}
            role="listbox"
            style={{ position: "fixed", left: rect.left, top: rect.bottom + 4, minWidth: rect.width, maxHeight: Math.min(320, window.innerHeight - rect.bottom - 16) }}
            className="z-[10001] overflow-y-auto rounded-md border border-gray-200 bg-background shadow"
          >
            {q && <div className="border-b border-gray-200 px-2 py-1 text-xs text-gray-500">{q}</div>}
            {shown.length === 0 && <div className="p-2 text-sm text-gray-500">{t("일치하는 항목이 없습니다")}</div>}
            {shown.map((o, i) => (
              <div
                key={o.value + i}
                data-i={i}
                role="option"
                aria-selected={o.value === String(value ?? "")}
                aria-disabled={o.disabled}
                onMouseEnter={() => setHi(i)}
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => pick(o)}
                className={cx(
                  "flex cursor-pointer items-center justify-between p-2 text-base",
                  i === hi ? "bg-gray-100" : "bg-transparent",
                  o.disabled && "cursor-not-allowed opacity-50",
                )}
              >
                <span className="min-w-0 flex-1 truncate">{o.label}</span>
                {o.value === String(value ?? "") && <Check className="size-4 shrink-0" />}
              </div>
            ))}
          </div>,
          document.body,
        )}
      {name && <input type="hidden" name={name} value={String(value ?? "")} />}
    </span>
  );
}

export function Toggle({
  checked,
  onChange,
  label,
  disabled,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: string;
  disabled?: boolean;
}) {
  return (
    <button
      type="button"
      role="switch"
      data-state={checked ? "checked" : "unchecked"}
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cx(
        "h-6 w-10 min-w-10 shrink-0 cursor-pointer rounded-full shadow focus:outline-none focus:ring-2 focus:ring-pink-400 disabled:cursor-not-allowed disabled:opacity-50",
        checked ? "bg-pink-500" : "bg-gray-300",
      )}
    >
      <span
        data-state={checked ? "checked" : "unchecked"}
        className="block h-5 w-5 min-w-5 translate-x-0.5 transform rounded-full bg-white shadow transition-transform duration-100"
        style={checked ? { transform: "translateX(18px)" } : undefined}
      />
    </button>
  );
}

export function Segmented<T extends string>({
  value,
  options,
  onChange,
  label,
}: {
  value: T;
  options: { value: T; label: ReactNode }[];
  onChange: (v: T) => void;
  label: string;
  size?: "sm" | "md";
}) {
  return (
    <div role="radiogroup" aria-label={label} className="flex max-w-max rounded-md text-gray-300">
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          role="radio"
          aria-checked={value === o.value}
          data-state={value === o.value ? "on" : "off"}
          onClick={() => onChange(o.value)}
          className={cx(
            "relative -ml-px flex h-[34px] cursor-pointer items-center justify-center border px-3 py-2 text-sm first:ml-0 first:rounded-l-md last:rounded-r-md hover:z-50 hover:border-gray-300 hover:bg-secondaryBg focus:outline-none focus-visible:z-[100] focus-visible:ring-2 focus-visible:ring-pink-500",
            value === o.value ? "z-50 border-gray-300 bg-secondaryBg text-pink-500" : "border-gray-200 text-gray-500 hover:text-gray-700",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

/* ── Copy helpers ───────────────────────────────────────────────────── */
export function CopyButton({ value, label = t("복사"), className }: { value: string; label?: string; className?: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      aria-label={label}
      title={copied ? t("복사됨") : label}
      onClick={async (e) => {
        e.stopPropagation();
        try {
          await navigator.clipboard.writeText(value);
          setCopied(true);
          setTimeout(() => setCopied(false), 1400);
        } catch {
          /* clipboard blocked */
        }
      }}
      className={cx(
        "flex size-8 shrink-0 cursor-pointer items-center justify-center rounded text-gray-600 transition-colors hover:bg-gray-200 hover:text-foreground",
        className,
      )}
    >
      {copied ? <Check className="size-4 text-success" /> : <Copy className="size-4" />}
    </button>
  );
}
/** Read-only value with a copy button (IDs, URLs, connection strings). */
export function CopyField({
  value,
  mono = true,
  secret,
  label,
  className,
}: {
  value: string;
  mono?: boolean;
  secret?: boolean;
  label?: string;
  className?: string;
}) {
  const [shown, setShown] = useState(!secret);
  return (
    <div className={cx("flex h-11 min-w-0 items-center rounded-md border border-gray-200 bg-gray-100 pl-3.5 pr-1.5", className)}>
      <span
        className={cx("min-w-0 flex-1 truncate text-sm", mono && "font-mono text-sm", !shown && "cursor-pointer")}
        onClick={() => setShown(true)}
        title={shown ? value : t("클릭해서 보기")}
      >
        {shown ? value : "•".repeat(Math.min(28, value.length || 8))}
      </span>
      {secret && (
        <button
          type="button"
          onClick={() => setShown((s) => !s)}
          className="mr-1 h-7 shrink-0 cursor-pointer rounded px-2 text-xs text-gray-600 hover:bg-gray-200 hover:text-foreground"
        >
          {shown ? t("숨기기") : t("보기")}
        </button>
      )}
      <CopyButton value={value} label={label ? t("{label} 복사", { label }) : t("복사")} />
    </div>
  );
}
export function Kbd({ children }: { children: ReactNode }) {
  return (
    <kbd className="inline-flex h-5 min-w-5 items-center justify-center rounded border border-gray-200 bg-gray-100 px-1 font-mono text-xs text-gray-500">
      {children}
    </kbd>
  );
}




export function Card({
  title,
  description,
  action,
  footer,
  footerNote,
  danger,
  flush,
  className,
  children,
}: {
  title?: ReactNode;
  description?: ReactNode;
  action?: ReactNode;
  /** Right-aligned buttons in the tinted footer. */
  footer?: ReactNode;
  /** Left-aligned hint in the footer. */
  footerNote?: ReactNode;
  danger?: boolean;
  /** Body without padding (lists / tables that span the full card width). */
  flush?: boolean;
  className?: string;
  children?: ReactNode;
}) {
  if (danger)
    return (
      <section className={cx("rounded-lg border border-red-200 bg-transparent", className)}>
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2 px-5 py-4">
          <div className="min-w-0 flex-1">
            {title && <h3 className="text-sm font-semibold">{title}</h3>}
            {description && <p className="mt-1 text-sm text-gray-600">{description}</p>}
          </div>
          {action && <div className="flex shrink-0 items-center gap-2">{action}</div>}
        </div>
        {children && <div className="px-5 pb-5">{children}</div>}
      </section>
    );
  return (
    <section className={cx("max-w-3xl [&+&]:mt-8 [&+&]:border-t [&+&]:border-gray-200 [&+&]:pt-8", className)}>
      {(title || action) && (
        <div className={cx("flex flex-wrap items-center gap-x-4 gap-y-2", !!children && "mb-4")}>
          <div className="min-w-0 flex-1">
            {title && <h3 className="text-[17px] font-semibold leading-6">{title}</h3>}
            {description && <p className="mt-1 text-sm text-gray-600">{description}</p>}
          </div>
          {action && <div className="flex shrink-0 items-center gap-2">{action}</div>}
        </div>
      )}
      {children && <div className={flush ? "overflow-hidden rounded-lg border border-gray-200" : ""}>{children}</div>}
      {(footer || footerNote) && (
        <div className="mt-4 flex flex-wrap items-center gap-3">
          {footer && <div className="flex items-center gap-2">{footer}</div>}
          <div className="min-w-0 flex-1 text-sm text-gray-500">{footerNote}</div>
        </div>
      )}
    </section>
  );
}
/** A list inside / instead of a card body: bordered rows separated by dividers. */
export function RowList({ children, className, bare }: { children: ReactNode; className?: string; bare?: boolean }) {
  return (
    <ul className={cx("divide-y divide-gray-200", bare ? "border-t border-gray-200" : "rounded-lg border border-gray-200 bg-secondaryBg", className)}>{children}</ul>
  );
}

const AVATAR_COLORS = ["#863bce", "#306ee8", "#2f8f6b", "#c2602d", "#b0397f", "#6b6cd9", "#a78a1e"];

export function Avatar({ name, size = 32 }: { name: string; size?: number }) {
  let h = 0;
  for (const ch of name) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  return (
    <span
      aria-hidden="true"
      className="inline-flex shrink-0 select-none items-center justify-center rounded-full font-semibold uppercase text-white"
      style={{ width: size, height: size, background: AVATAR_COLORS[h % AVATAR_COLORS.length], fontSize: Math.round(size * 0.42) }}
    >
      {(name.trim()[0] ?? "?").toUpperCase()}
    </span>
  );
}

export function EmptyState({ icon, title, children, action }: { icon?: ReactNode; title: string; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-2 rounded-lg border border-dashed border-gray-200 px-6 py-10 text-center">
      {icon && <div className="mb-1 text-gray-500">{icon}</div>}
      <div className="text-sm font-semibold">{title}</div>
      {children && <div className="max-w-[420px] text-sm text-gray-500">{children}</div>}
      {action && <div className="mt-2">{action}</div>}
    </div>
  );
}


/** Full-height page inside the canvas frame: h3 title bar with optional actions and close link, then the body. */

export function PageShell({
  title,
  actions,
  close,
  children,
}: {
  title: ReactNode;
  actions?: ReactNode;
  /** Where the close (X) button goes, usually back to the canvas. */
  close?: { href: string; label: string };
  children: ReactNode;
}) {
  return (
    <div className="flex h-full min-h-full flex-col">
      <div className="flex min-h-full shrink-0 flex-col bg-background md:rounded-t-xl dark:bg-secondaryBg">
        <header className="flex items-center justify-between gap-4 border-b border-gray-100 px-6 py-8 md:border-gray-200 md:px-12">
          <h1 className="min-w-0 flex-1 truncate text-h3 font-semibold">{title}</h1>
          {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
          {close && (
            <Link href={close.href} aria-label={close.label} className="flex size-8 shrink-0 items-center justify-center rounded-md text-gray-600 hover:bg-gray-100 hover:text-foreground">
              <X className="size-4" />
            </Link>
          )}
        </header>
        {children}
      </div>
    </div>
  );
}

/** Body of a PageShell with a left section nav (settings, account). */
export function SideNavLayout<T extends string>({
  label,
  items,
  value,
  onChange,
  children,
}: {
  label: string;
  items: { id: T; label: ReactNode; icon: ReactNode; danger?: boolean }[];
  value: T;
  onChange: (id: T) => void;
  children: ReactNode;
}) {
  return (
    <div className="grid w-full flex-1 grid-rows-[auto_minmax(0px,_1fr)] md:grid-cols-[auto_minmax(0px,_1fr)] md:grid-rows-none">
      <nav aria-label={label} className="flex gap-1 overflow-x-auto px-6 pt-4 md:ml-12 md:mt-12 md:block md:min-w-[195px] md:overflow-visible md:p-0">
        {items.map((n) => (
          <button
            key={n.id}
            type="button"
            onClick={() => onChange(n.id)}
            aria-current={value === n.id ? "page" : undefined}
            className={cx(
              "group relative mx-1 mb-2 flex shrink-0 cursor-pointer items-center space-x-4 whitespace-nowrap rounded py-2 text-left hover:bg-gray-100 hover:text-foreground md:w-[calc(100%-0.5rem)] md:px-3",
              value === n.id ? "font-medium text-foreground" : "text-gray-600",
              n.danger && value !== n.id && "hover:text-danger",
            )}
          >
            <span className={cx("icon-container icon-md text-2xl", value === n.id ? "text-pink-500" : "text-gray-400 group-hover:text-gray-800")}>{n.icon}</span>
            <span className="text-base">{n.label}</span>
          </button>
        ))}
      </nav>
      <div className="min-w-0 px-6 pb-16 pt-8 md:px-12 md:pr-16 md:pt-12">{children}</div>
    </div>
  );
}

/** Plain body of a PageShell (no side nav). */
export function PageBody({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cx("min-w-0 px-6 pb-16 pt-8 md:px-12 md:pt-12", className)}>{children}</div>;
}
