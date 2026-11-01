"use client";

import {
  Children,
  Fragment,
  cloneElement,
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

// Every variant uses the accent focus ring except the red ones, so keyboard focus looks the same everywhere.
const variants: Record<Variant, string> = {
  primary:
    "bg-pink-500 border-pink-500 text-white shadow-raised hover:bg-(--primary-hover) hover:border-(--primary-hover) disabled:bg-pink-500 disabled:border-pink-500 focus-visible:ring-pink-600",
  default:
    "bg-gray-100 border-gray-200 text-foreground hover:bg-gray-200 hover:border-gray-300 disabled:bg-gray-100 disabled:border-gray-200 focus-visible:ring-pink-500",
  danger:
    "bg-red-500 border-red-500 text-white shadow-raised hover:bg-(--danger-hover) hover:border-(--danger-hover) disabled:bg-red-500 disabled:border-red-500 focus-visible:ring-red-600",
  "danger-outline":
    "bg-transparent border-red-200 text-red-500 dark:text-red-700 hover:bg-red-100 hover:border-red-200 disabled:bg-transparent disabled:border-red-200 focus-visible:ring-red-600 focus-visible:bg-red-100",
  ghost:
    "bg-transparent border-transparent text-foreground hover:bg-gray-100 hover:border-gray-100 disabled:bg-transparent disabled:border-transparent focus-visible:ring-pink-500 focus-visible:bg-gray-100",
  accent:
    "bg-transparent border-pink-200 text-pink-500 dark:text-pink-700 hover:bg-pink-100 hover:border-pink-200 disabled:bg-transparent disabled:border-pink-200 disabled:text-pink-700 focus-visible:ring-pink-600 focus-visible:bg-pink-100",
  link:
    "bg-transparent border-transparent text-pink-500 dark:text-pink-700 hover:bg-pink-100 hover:border-pink-100 disabled:bg-transparent disabled:border-transparent focus-visible:ring-pink-600 focus-visible:bg-pink-100",
  success:
    "bg-transparent border-green-200 text-green-600 dark:text-green-700 hover:bg-green-100 hover:border-green-200 disabled:bg-transparent disabled:border-green-200 disabled:text-green-700 focus-visible:ring-green-600 focus-visible:bg-green-100",
};

const OUTLINE_GRAY =
  "bg-transparent border-gray-200 text-foreground hover:bg-gray-100 hover:border-gray-300 disabled:bg-transparent disabled:border-gray-200 disabled:text-foreground focus-visible:ring-pink-500 focus-visible:bg-gray-100";

const BUTTON_BASE =
  "group/button flex shrink-0 cursor-pointer select-none items-center justify-center whitespace-nowrap border transition-[color,background-color,border-color,box-shadow,transform,opacity] duration-150 ease-out active:scale-[0.97] focus:outline-none focus-visible:ring-2 disabled:cursor-not-allowed disabled:opacity-50 disabled:shadow-none disabled:active:scale-100 aria-disabled:active:scale-100 aria-busy:cursor-progress motion-reduce:active:scale-100";

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
  /** Disables the button and shows a spinner before the label; announced as busy. */
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
      aria-busy={loading || undefined}
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
      {loading && <Loader2 aria-hidden="true" className={cx("animate-spin", size === "sm" ? "size-3" : "size-4")} />}
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
        "bg-transparent border-transparent text-gray-500 hover:bg-gray-100 hover:border-gray-100 hover:text-foreground disabled:bg-transparent disabled:border-transparent disabled:hover:text-gray-500 focus-visible:ring-pink-500 focus-visible:bg-gray-100",
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
  "min-w-0 rounded-md text-foreground bg-transparent border border-gray-200 placeholder-gray-500 transition-[border-color,box-shadow] duration-150 focus:outline-none relative z-10 hover:border-gray-400 focus:border-pink-400 focus:glow-accent disabled:bg-gray-100 disabled:text-gray-600 disabled:cursor-not-allowed disabled:hover:border-gray-200 read-only:text-gray-600 aria-[invalid=true]:border-red-500 aria-[invalid=true]:focus:glow-danger";
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
      aria-invalid={invalid || undefined}
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
  TextareaHTMLAttributes<HTMLTextAreaElement> & { mono?: boolean; invalid?: boolean }
>(function Textarea({ className, mono, invalid, ...rest }, ref) {
  return (
    <textarea
      ref={ref}
      spellCheck={false}
      aria-invalid={invalid || undefined}
      {...rest}
      className={cx(inputCls, INPUT_SIZES.md, "w-full", invalid && "!border-red-500", mono && "font-mono !text-sm !leading-5", className)}
    />
  );
});

export const LABEL_CLS = "block text-xs font-semibold leading-[18px] text-gray-600 mb-2";

export function Field({
  label,
  hint,
  error,
  required,
  children,
  className,
}: {
  label: string;
  hint?: ReactNode;
  /** Validation message shown under the control in red (replaces the hint while set). */
  error?: ReactNode;
  required?: boolean;
  children: ReactNode;
  className?: string;
}) {
  return (
    <label className={cx("block w-full", className)}>
      <div className="flex items-center space-x-1">
        <span className={LABEL_CLS}>{label}</span>
        {required && (
          <span aria-hidden="true" className="mb-2 text-xs text-red-600">
            *
          </span>
        )}
      </div>
      {children}
      {error ? (
        <span role="alert" className="mt-2 block text-xs font-normal leading-[18px] text-danger">
          {error}
        </span>
      ) : (
        hint && <span className="mt-2 block text-xs font-normal leading-[18px] text-gray-600">{hint}</span>
      )}
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
  return <Loader2 role="status" aria-label={t("불러오는 중")} className={cx("size-4 animate-spin text-gray-600", className)} />;
}
export function CenterSpinner() {
  return (
    <div className="flex animate-fade-in justify-center py-16">
      <Spinner />
    </div>
  );
}
/* ── Focus trap / scroll lock ────────────────────────────────────────── */
const FOCUSABLE =
  'a[href],area[href],button:not([disabled]),input:not([disabled]):not([type="hidden"]),select:not([disabled]),textarea:not([disabled]),iframe,summary,[contenteditable="true"],[tabindex]:not([tabindex="-1"])';

/** Focusable, visible descendants of `root` in tab order (positive tabindex is not used in this app). */
export function focusableIn(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
    (el) => !el.closest("[inert]") && (el.offsetWidth > 0 || el.offsetHeight > 0 || el.getClientRects().length > 0),
  );
}

const trapStack: { current: HTMLElement | null }[] = [];
let trapBound = false;
function onTrapKey(e: KeyboardEvent) {
  if (e.key !== "Tab" || e.defaultPrevented) return;
  const root = trapStack[trapStack.length - 1]?.current;
  if (!root) return;
  const items = focusableIn(root);
  if (items.length === 0) {
    e.preventDefault();
    root.focus();
    return;
  }
  const first = items[0];
  const last = items[items.length - 1];
  const active = document.activeElement as HTMLElement | null;
  const inside = !!active && root.contains(active);
  if (e.shiftKey && (!inside || active === first || active === root)) {
    e.preventDefault();
    last.focus();
  } else if (!e.shiftKey && (!inside || active === last)) {
    e.preventDefault();
    first.focus();
  }
}

/**
 * Keeps Tab / Shift+Tab inside `ref` while `active` (only the topmost trap reacts), moves focus in on open
 * (an autofocus element, else the first field, else the container) and gives it back to the opener on close.
 */
export function useFocusTrap(ref: { current: HTMLElement | null }, active: boolean) {
  useEffect(() => {
    if (!active) return;
    if (!trapBound) {
      document.addEventListener("keydown", onTrapKey);
      trapBound = true;
    }
    const opener = document.activeElement as HTMLElement | null;
    const entry = { current: ref.current };
    trapStack.push(entry);
    const root = ref.current;
    if (root && !root.contains(document.activeElement)) {
      const auto = root.querySelector<HTMLElement>("[autofocus],[data-autofocus]");
      const field = root.querySelector<HTMLElement>(
        'input:not([disabled]):not([type="hidden"]):not([readonly]),textarea:not([disabled]):not([readonly])',
      );
      (auto ?? field ?? root).focus({ preventScroll: true });
    }
    return () => {
      const i = trapStack.indexOf(entry);
      if (i >= 0) trapStack.splice(i, 1);
      // Return focus only if it is still inside the closing layer (or lost to <body>), never steal it from elsewhere.
      const now = document.activeElement;
      const lost = !now || now === document.body || (!!root && root.contains(now));
      if (lost && opener && opener.isConnected && typeof opener.focus === "function") opener.focus({ preventScroll: true });
    };
  }, [active, ref]);
}

let scrollLocks = 0;
let savedOverflow = "";
let savedPadding = "";
/** Locks page scrolling while `active`; nested locks are counted. Compensates for the scrollbar width. */
export function useScrollLock(active: boolean) {
  useEffect(() => {
    if (!active || typeof document === "undefined") return;
    const body = document.body;
    if (scrollLocks === 0) {
      const gap = window.innerWidth - document.documentElement.clientWidth;
      savedOverflow = body.style.overflow;
      savedPadding = body.style.paddingRight;
      body.style.overflow = "hidden";
      if (gap > 0) body.style.paddingRight = `${gap}px`;
    }
    scrollLocks++;
    return () => {
      scrollLocks--;
      if (scrollLocks === 0) {
        body.style.overflow = savedOverflow;
        body.style.paddingRight = savedPadding;
      }
    };
  }, [active]);
}

/* ── Dialog ──────────────────────────────────────────────────────────── */
export function Dialog({
  open,
  onClose,
  title,
  description,
  children,
  footer,
  wide,
  className,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  /** Optional line under the title, linked as the dialog's accessible description. */
  description?: ReactNode;
  children: ReactNode;
  footer: ReactNode;
  /** Wider dialog with a scrollable body (diffs, long lists). */
  wide?: boolean;
  className?: string;
}) {
  const id = useId();
  const panel = useRef<HTMLDivElement>(null);
  // Close on a click that both starts and ends on the backdrop, so drag-selecting text out of a field does not close.
  const downOnBackdrop = useRef(false);
  useEscape(onClose, open);
  useFocusTrap(panel, open);
  useScrollLock(open);
  if (!open || typeof document === "undefined") return null;
  return createPortal(
    <div
      className="fixed inset-0 z-[70] flex animate-modal-overlay-in items-end justify-stretch bg-black/[.45] backdrop-blur-[2px] dark:bg-black/75 sm:items-center sm:justify-center sm:px-5"
      onMouseDown={(e) => {
        downOnBackdrop.current = e.target === e.currentTarget;
      }}
      onMouseUp={(e) => {
        if (downOnBackdrop.current && e.target === e.currentTarget) onClose();
        downOnBackdrop.current = false;
      }}
    >
      <div
        ref={panel}
        role="dialog"
        aria-modal="true"
        aria-labelledby={`${id}-t`}
        aria-describedby={description ? `${id}-d` : undefined}
        tabIndex={-1}
        className={cx(
          "relative w-full max-w-full animate-modal-from-bottom-in overflow-hidden rounded-t-lg border border-gray-200 bg-background p-6 shadow-dialog outline-none dark:bg-secondaryBg sm:w-auto sm:animate-modal-scale-in sm:rounded-lg",
          wide ? "flex max-h-[88dvh] flex-col" : "max-h-[calc(100dvh-2.5rem)] overflow-y-auto",
          className,
        )}
      >
        <div className={cx("pr-10", description ? "pb-4" : "pb-5")}>
          <h2 id={`${id}-t`} className="text-left text-xl font-semibold leading-7 text-foreground">
            {title}
          </h2>
          {description && (
            <p id={`${id}-d`} className="mt-1 text-sm text-gray-600">
              {description}
            </p>
          )}
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
function menuItems(root: HTMLElement | null): HTMLElement[] {
  if (!root) return [];
  return Array.from(root.querySelectorAll<HTMLElement>('[role="menuitem"],[role="menuitemradio"],[role="menuitemcheckbox"]')).filter(
    (el) => !(el as HTMLButtonElement).disabled,
  );
}

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
  const list = useRef<HTMLDivElement>(null);
  const triggerEl = () => ref.current?.querySelector<HTMLElement>('[aria-haspopup="menu"]') ?? null;
  // Closing from inside the menu (Esc, picking an item) returns focus to the trigger; clicking elsewhere does not.
  const close = (restore = true) => {
    const inside = !!list.current?.contains(document.activeElement);
    setOpen(false);
    if (restore && inside) triggerEl()?.focus({ preventScroll: true });
  };
  useEscape(() => close(), open);
  useEffect(() => {
    if (!open) return;
    const h = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", h);
    return () => document.removeEventListener("mousedown", h);
  }, [open]);
  // Move focus into the menu so arrow keys work right away (focus-visible keeps it quiet for mouse users).
  useEffect(() => {
    if (open && !list.current?.contains(document.activeElement)) menuItems(list.current)[0]?.focus({ preventScroll: true });
  }, [open]);
  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Tab") {
      // Menus may hold links and buttons too; close only once Tab has moved focus out.
      setTimeout(() => {
        if (ref.current && !ref.current.contains(document.activeElement)) setOpen(false);
      });
      return;
    }
    // Leave arrow keys to text fields inside the menu (filters, search).
    if ((e.target as HTMLElement).matches?.("input,textarea,select,[contenteditable='true']")) return;
    const items = menuItems(list.current);
    if (!items.length) return;
    const i = items.indexOf(document.activeElement as HTMLElement);
    const go = (n: number) => {
      e.preventDefault();
      items[(n + items.length) % items.length]?.focus();
    };
    if (e.key === "ArrowDown") go(i + 1);
    else if (e.key === "ArrowUp") go(i < 0 ? items.length - 1 : i - 1);
    else if (e.key === "Home") go(0);
    else if (e.key === "End") go(items.length - 1);
  };
  return (
    <div ref={ref} className="relative">
      {trigger({ onClick: () => setOpen((o) => !o), "aria-expanded": open, "aria-haspopup": "menu", "data-state": open ? "open" : "closed" })}
      {open && (
        <div
          ref={list}
          role="menu"
          aria-label={label}
          onKeyDown={onKeyDown}
          className={cx(
            "absolute z-[60] max-h-[70dvh] min-w-[219px] animate-pop-in overflow-y-auto rounded-lg border border-gray-200 bg-background p-1 shadow-popover dark:bg-secondaryBg",
            side === "right" ? "bottom-0 left-full ml-2 origin-bottom-left" : cx("top-full mt-2", align === "end" ? "right-0 origin-top-right" : "left-0 origin-top-left"),
          )}
        >
          {children(() => close())}
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
        "group relative mb-0.5 flex h-9 w-full cursor-pointer select-none items-center rounded-md px-3 py-2 text-left text-sm font-medium leading-4 transition-colors duration-100 last:mb-0 focus:outline-none",
        "disabled:cursor-default disabled:opacity-40",
        soft && "cursor-not-allowed opacity-40",
        className,
        danger
          ? "text-danger hover:bg-danger-surface-hover focus-visible:bg-danger-surface-hover"
          : "text-neutral-secondary hover:bg-neutral-surface-hover hover:text-neutral focus-visible:bg-neutral-surface-hover focus-visible:text-neutral",
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
  const hiOpt = shown[hi];
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
  /** Next enabled option from `from` in direction `dir`, staying put at the ends. */
  const step = (from: number, dir: 1 | -1) => {
    for (let i = from + dir; i >= 0 && i < shown.length; i += dir) if (!shown[i].disabled) return i;
    return from;
  };
  useEffect(() => {
    if (!open) return;
    const away = (e: MouseEvent) => {
      if (!btn.current?.contains(e.target as Node) && !list.current?.contains(e.target as Node)) close();
    };
    // Scrolling the list itself must not close it.
    const move = (e: Event) => {
      if (!list.current?.contains(e.target as Node)) close();
    };
    const resize = () => close();
    document.addEventListener("mousedown", away);
    window.addEventListener("scroll", move, true);
    window.addEventListener("resize", resize);
    return () => {
      document.removeEventListener("mousedown", away);
      window.removeEventListener("scroll", move, true);
      window.removeEventListener("resize", resize);
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
      setHi((h) => step(h, 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setHi((h) => step(h, -1));
    } else if (e.key === "Home") {
      e.preventDefault();
      setHi(step(-1, 1));
    } else if (e.key === "End") {
      e.preventDefault();
      setHi(step(shown.length, -1));
    } else if (e.key === "Enter" || (e.key === " " && !q)) {
      e.preventDefault();
      if (hiOpt) pick(hiOpt);
    } else if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      close();
    } else if (e.key === "Backspace") {
      setQ((x) => x.slice(0, -1));
      setHi(0);
    } else if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
      e.preventDefault();
      setQ((x) => x + e.key);
      setHi(0);
    } else if (e.key === "Tab") close();
  };
  // Open upwards when there is clearly more room above the trigger than below.
  const view = typeof window === "undefined" ? 0 : window.innerHeight;
  const below = rect ? view - rect.bottom - 16 : 0;
  const above = rect ? rect.top - 16 : 0;
  const up = !!rect && below < 200 && above > below;
  const maxH = Math.max(120, Math.min(320, up ? above : below));
  const optId = (i: number) => `${lid}-o${i}`;
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
        aria-activedescendant={open && hiOpt ? optId(hi) : undefined}
        aria-label={rest["aria-label"]}
        aria-labelledby={rest["aria-labelledby"]}
        aria-describedby={rest["aria-describedby"]}
        aria-invalid={rest["aria-invalid"]}
        aria-required={required}
        title={rest.title}
        autoFocus={rest.autoFocus}
        disabled={disabled}
        onClick={() => (open ? close() : openList())}
        onKeyDown={onKey}
        className={cx(
          "flex w-full min-w-0 cursor-pointer items-center justify-between gap-2 rounded-md border border-gray-200 bg-transparent px-3 text-left text-foreground transition-[border-color,box-shadow] duration-150",
          size === "sm" ? "h-[34px] py-1 text-sm leading-5" : "h-[42px] py-2 text-base leading-6",
          "hover:border-gray-400 focus:outline-none focus-visible:border-pink-400 focus-visible:glow-accent disabled:cursor-not-allowed aria-[invalid=true]:border-red-500",
          open && "border-pink-400",
        )}
      >
        <span className={cx("min-w-0 flex-1 truncate", !cur && "text-gray-500")}>{cur ? cur.label : t("선택…")}</span>
        <ChevronDown aria-hidden="true" className={cx("size-4 shrink-0 text-gray-500 transition-transform duration-150", open && "rotate-180")} />
      </button>
      {open &&
        rect &&
        createPortal(
          <div
            ref={list}
            id={lid}
            role="listbox"
            aria-label={rest["aria-label"]}
            style={{
              position: "fixed",
              left: rect.left,
              minWidth: rect.width,
              maxWidth: `max(${rect.width}px, min(480px, calc(100vw - ${rect.left + 8}px)))`,
              maxHeight: maxH,
              ...(up ? { bottom: view - rect.top + 4 } : { top: rect.bottom + 4 }),
            }}
            className={cx(
              "z-[10001] overflow-y-auto rounded-lg border border-gray-200 bg-background p-1 shadow-popover animate-pop-in dark:bg-secondaryBg",
              up ? "origin-bottom" : "origin-top",
            )}
          >
            {q && (
              <div className="mb-1 border-b border-gray-200 px-2 pb-1.5 pt-1 text-xs text-gray-500" aria-live="polite">
                {q}
              </div>
            )}
            {shown.length === 0 && <div className="px-2 py-2 text-sm text-gray-500">{t("일치하는 항목이 없습니다")}</div>}
            {shown.map((o, i) => {
              const selected = o.value === String(value ?? "");
              const groupStart = !!o.group && (i === 0 || shown[i - 1].group !== o.group);
              return (
                <Fragment key={o.value + i}>
                  {groupStart && (
                    <div role="presentation" className="px-2 pb-1 pt-2 text-xs font-medium text-gray-500 first:pt-1">
                      {o.group}
                    </div>
                  )}
                  <div
                    id={optId(i)}
                    data-i={i}
                    role="option"
                    aria-selected={selected}
                    aria-disabled={o.disabled || undefined}
                    onMouseEnter={() => !o.disabled && setHi(i)}
                    onMouseDown={(e) => e.preventDefault()}
                    onClick={() => pick(o)}
                    className={cx(
                      "flex cursor-pointer items-center justify-between gap-2 rounded-md px-2 py-1.5",
                      size === "sm" ? "text-sm" : "text-base",
                      i === hi && !o.disabled ? "bg-gray-100" : "bg-transparent",
                      selected && "font-medium",
                      o.disabled && "cursor-not-allowed opacity-50",
                    )}
                  >
                    <span className="min-w-0 flex-1 truncate">{o.label}</span>
                    {selected && <Check aria-hidden="true" className="size-4 shrink-0 text-pink-600" />}
                  </div>
                </Fragment>
              );
            })}
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
  id,
  className,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: string;
  disabled?: boolean;
  id?: string;
  className?: string;
}) {
  return (
    <button
      type="button"
      role="switch"
      id={id}
      data-state={checked ? "checked" : "unchecked"}
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cx(
        "h-6 w-10 min-w-10 shrink-0 cursor-pointer rounded-full p-0 shadow-[inset_0_1px_2px_rgba(0,0,0,0.15)] transition-colors duration-150 focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-500 focus-visible:ring-offset-2 focus-visible:ring-offset-background disabled:cursor-not-allowed disabled:opacity-50",
        checked ? "bg-pink-500" : "bg-gray-400 hover:bg-gray-500 dark:bg-gray-300 dark:hover:bg-gray-400",
        className,
      )}
    >
      <span
        aria-hidden="true"
        data-state={checked ? "checked" : "unchecked"}
        className="block h-5 w-5 min-w-5 translate-x-0.5 transform rounded-full bg-white shadow-[0_1px_3px_rgba(0,0,0,0.3)] transition-transform duration-150 ease-out"
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
  size = "md",
  className,
}: {
  value: T;
  options: { value: T; label: ReactNode; disabled?: boolean }[];
  onChange: (v: T) => void;
  label: string;
  size?: "sm" | "md";
  className?: string;
}) {
  const group = useRef<HTMLDivElement>(null);
  const enabled = options.filter((o) => !o.disabled);
  // Radio-group keyboard model: one tab stop, arrows move the selection.
  const onKeyDown = (e: React.KeyboardEvent) => {
    const keys: Record<string, number> = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 };
    let next: T | undefined;
    const i = enabled.findIndex((o) => o.value === value);
    if (e.key in keys) next = enabled[(i + keys[e.key] + enabled.length) % enabled.length]?.value;
    else if (e.key === "Home") next = enabled[0]?.value;
    else if (e.key === "End") next = enabled[enabled.length - 1]?.value;
    if (next === undefined) return;
    e.preventDefault();
    onChange(next);
    const idx = options.findIndex((o) => o.value === next);
    group.current?.querySelectorAll<HTMLElement>('[role="radio"]')[idx]?.focus();
  };
  const hasValue = options.some((o) => o.value === value);
  return (
    <div ref={group} role="radiogroup" aria-label={label} onKeyDown={onKeyDown} className={cx("flex max-w-max rounded-md text-gray-300", className)}>
      {options.map((o, i) => {
        const on = value === o.value;
        return (
          <button
            key={o.value}
            type="button"
            role="radio"
            aria-checked={on}
            data-state={on ? "on" : "off"}
            disabled={o.disabled}
            tabIndex={on || (!hasValue && i === 0) ? 0 : -1}
            onClick={() => onChange(o.value)}
            className={cx(
              "relative -ml-px flex cursor-pointer items-center justify-center border transition-colors duration-100 first:ml-0 first:rounded-l-md last:rounded-r-md hover:z-50 hover:border-gray-300 hover:bg-secondaryBg focus:outline-none focus-visible:z-[100] focus-visible:ring-2 focus-visible:ring-pink-500 disabled:cursor-not-allowed disabled:opacity-50",
              size === "sm" ? "h-[26px] px-2 py-1 text-xs" : "h-[34px] px-3 py-2 text-sm",
              on ? "z-50 border-gray-300 bg-secondaryBg font-medium text-pink-500 dark:text-pink-600" : "border-gray-200 text-gray-500 hover:text-gray-700",
            )}
          >
            {o.label}
          </button>
        );
      })}
    </div>
  );
}

/* ── Copy helpers ───────────────────────────────────────────────────── */
export function CopyButton({ value, label = t("복사"), className }: { value: string; label?: string; className?: string }) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current);
  }, []);
  return (
    <button
      type="button"
      aria-label={copied ? t("복사됨") : label}
      title={copied ? t("복사됨") : label}
      onClick={async (e) => {
        e.stopPropagation();
        try {
          await navigator.clipboard.writeText(value);
          setCopied(true);
          if (timer.current) clearTimeout(timer.current);
          timer.current = setTimeout(() => setCopied(false), 1400);
        } catch {
          /* clipboard blocked */
        }
      }}
      className={cx(
        "flex size-8 shrink-0 cursor-pointer items-center justify-center rounded text-gray-600 transition-colors hover:bg-gray-200 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-500",
        className,
      )}
    >
      {copied ? <Check aria-hidden="true" className="size-4 animate-pop-in text-success" /> : <Copy aria-hidden="true" className="size-4" />}
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
          aria-pressed={shown}
          onClick={() => setShown((s) => !s)}
          className="mr-1 h-7 shrink-0 cursor-pointer rounded px-2 text-xs text-gray-600 transition-colors hover:bg-gray-200 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-500"
        >
          {shown ? t("숨기기") : t("보기")}
        </button>
      )}
      <CopyButton value={value} label={label ? t("{label} 복사", { label }) : t("복사")} />
    </div>
  );
}
export function Kbd({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <kbd
      className={cx(
        "inline-flex h-5 min-w-5 items-center justify-center rounded border border-b-2 border-gray-200 bg-gray-100 px-1 font-mono text-xs leading-none text-gray-600",
        className,
      )}
    >
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

export function EmptyState({
  icon,
  title,
  children,
  action,
  className,
}: {
  icon?: ReactNode;
  title: string;
  children?: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cx("flex animate-fade-in flex-col items-center gap-2 rounded-lg border border-dashed border-gray-200 px-6 py-10 text-center", className)}>
      {icon && (
        <div
          aria-hidden="true"
          className="mb-2 flex size-10 items-center justify-center rounded-lg border border-gray-200 bg-gray-50 text-gray-500 shadow-raised [&_svg]:size-5"
        >
          {icon}
        </div>
      )}
      <div className="text-sm font-semibold text-foreground">{title}</div>
      {children && <div className="max-w-[420px] text-sm text-gray-500">{children}</div>}
      {action && <div className="mt-2">{action}</div>}
    </div>
  );
}

/* ── Badge / Skeleton / Tooltip / Tabs ───────────────────────────────── */
export type BadgeTone = "neutral" | "accent" | "success" | "warning" | "danger" | "info";

// Light: -700 text on -100 fill; dark: -700 text on -200 fill. All pairs are 5.5:1 or better.
const BADGE_TONES: Record<BadgeTone, string> = {
  neutral: "bg-gray-100 text-gray-700 dark:bg-gray-200",
  accent: "bg-pink-100 text-pink-700 dark:bg-pink-200",
  success: "bg-green-100 text-green-700 dark:bg-green-200",
  warning: "bg-yellow-100 text-yellow-700 dark:bg-yellow-200",
  danger: "bg-red-100 text-red-700 dark:bg-red-200",
  info: "bg-blue-100 text-blue-700 dark:bg-blue-200",
};

/** Small inline label (counts, "New", plan names). `outline` draws a border instead of a fill. */
export function Badge({
  tone = "neutral",
  outline,
  children,
  className,
  title,
}: {
  tone?: BadgeTone;
  outline?: boolean;
  children: ReactNode;
  className?: string;
  title?: string;
}) {
  return (
    <span
      title={title}
      className={cx(
        "inline-flex h-5 shrink-0 items-center gap-1 whitespace-nowrap rounded-badge px-1.5 text-xs font-medium leading-none [&_svg]:size-3",
        outline ? cx("border border-current/25 bg-transparent", BADGE_TONES[tone].split(" ").find((c) => c.startsWith("text-"))) : BADGE_TONES[tone],
        className,
      )}
    >
      {children}
    </span>
  );
}

/** Loading placeholder with a soft shimmer (static under prefers-reduced-motion). Size it with className. */
export function Skeleton({ className, rounded = "md" }: { className?: string; rounded?: "sm" | "md" | "lg" | "full" }) {
  return (
    <span
      aria-hidden="true"
      className={cx(
        "skeleton block",
        { sm: "rounded-sm", md: "rounded-md", lg: "rounded-lg", full: "rounded-full" }[rounded],
        !/(^|\s)h-/.test(className ?? "") && "h-4",
        className,
      )}
    />
  );
}

/** A few skeleton text lines; the last one is shorter. Announced once as loading. */
export function SkeletonText({ lines = 3, className }: { lines?: number; className?: string }) {
  return (
    <div role="status" aria-label={t("불러오는 중")} className={cx("flex flex-col gap-2", className)}>
      {Array.from({ length: lines }, (_, i) => (
        <Skeleton key={i} className={cx("h-3.5", i === lines - 1 && lines > 1 ? "w-3/5" : "w-full")} />
      ))}
    </div>
  );
}

/** Content for screen readers only. */
export function VisuallyHidden({ children }: { children: ReactNode }) {
  return <span className="sr-only">{children}</span>;
}

type TooltipSide = "top" | "bottom" | "left" | "right";

/**
 * Hover / focus tooltip in a portal. Wraps its child in an inline-flex span; the child gets aria-describedby
 * when it is a single element. Opens after `delay` ms on hover, immediately on keyboard focus; Esc hides it.
 */
export function Tooltip({
  content,
  children,
  side = "top",
  delay = 350,
  className,
}: {
  content: ReactNode;
  children: ReactNode;
  side?: TooltipSide;
  delay?: number;
  className?: string;
}) {
  const id = useId();
  const anchor = useRef<HTMLSpanElement>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [rect, setRect] = useState<DOMRect | null>(null);
  const show = (wait: number) => {
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => setRect(anchor.current?.getBoundingClientRect() ?? null), wait);
  };
  const hide = () => {
    if (timer.current) clearTimeout(timer.current);
    timer.current = null;
    setRect(null);
  };
  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current);
  }, []);
  useEscape(hide, !!rect);
  useEffect(() => {
    if (!rect) return;
    window.addEventListener("scroll", hide, true);
    return () => window.removeEventListener("scroll", hide, true);
  }, [rect]);
  if (content === null || content === undefined || content === "" || content === false) return <>{children}</>;
  const only = Children.count(children) === 1 && isValidElement(children) ? (children as ReactElement<{ "aria-describedby"?: string }>) : null;
  const child = only
    ? cloneElement(only, { "aria-describedby": cx(only.props["aria-describedby"], rect ? id : undefined) || undefined })
    : children;
  const gap = 6;
  const pos: React.CSSProperties = !rect
    ? {}
    : side === "top"
      ? { left: rect.left + rect.width / 2, top: rect.top - gap, transform: "translate(-50%, -100%)" }
      : side === "bottom"
        ? { left: rect.left + rect.width / 2, top: rect.bottom + gap, transform: "translateX(-50%)" }
        : side === "left"
          ? { left: rect.left - gap, top: rect.top + rect.height / 2, transform: "translate(-100%, -50%)" }
          : { left: rect.right + gap, top: rect.top + rect.height / 2, transform: "translateY(-50%)" };
  return (
    <span
      ref={anchor}
      className={cx("inline-flex", className)}
      onPointerEnter={(e) => e.pointerType !== "touch" && show(delay)}
      onPointerLeave={hide}
      onFocus={(e) => (e.target as HTMLElement).matches?.(":focus-visible") && show(0)}
      onBlur={hide}
      onPointerDown={hide}
    >
      {child}
      {rect &&
        typeof document !== "undefined" &&
        createPortal(
          <span
            id={id}
            role="tooltip"
            style={{ position: "fixed", ...pos }}
            className="pointer-events-none z-[100001] max-w-[280px] animate-fade-in rounded-md border border-gray-200 bg-gray-50 px-2 py-1 text-xs leading-[18px] text-foreground shadow-popover"
          >
            {content}
          </span>,
          document.body,
        )}
    </span>
  );
}

export type TabItem<T extends string> = { id: T; label: ReactNode; icon?: ReactNode; count?: number; disabled?: boolean };

/** Tab ids for linking a Tabs list to TabPanel elements that share `idBase`. */
export const tabIds = (idBase: string, id: string) => ({ tab: `${idBase}-tab-${id}`, panel: `${idBase}-panel-${id}` });

/**
 * Tab list with the ARIA tabs keyboard model (arrows / Home / End move and select, one tab stop).
 * `underline` matches the service panel header; `pill` is the compact segmented look.
 */
export function Tabs<T extends string>({
  items,
  value,
  onChange,
  label,
  variant = "underline",
  idBase,
  className,
}: {
  items: TabItem<T>[];
  value: T;
  onChange: (id: T) => void;
  label: string;
  variant?: "underline" | "pill";
  /** Set together with TabPanel to wire aria-controls / aria-labelledby. */
  idBase?: string;
  className?: string;
}) {
  const list = useRef<HTMLDivElement>(null);
  const onKeyDown = (e: React.KeyboardEvent) => {
    const enabled = items.filter((it) => !it.disabled);
    const i = enabled.findIndex((it) => it.id === value);
    let next: TabItem<T> | undefined;
    if (e.key === "ArrowRight") next = enabled[(i + 1) % enabled.length];
    else if (e.key === "ArrowLeft") next = enabled[(i - 1 + enabled.length) % enabled.length];
    else if (e.key === "Home") next = enabled[0];
    else if (e.key === "End") next = enabled[enabled.length - 1];
    if (!next) return;
    e.preventDefault();
    onChange(next.id);
    list.current?.querySelectorAll<HTMLElement>('[role="tab"]')[items.indexOf(next)]?.focus();
  };
  const hasValue = items.some((it) => it.id === value);
  return (
    <div
      ref={list}
      role="tablist"
      aria-label={label}
      aria-orientation="horizontal"
      onKeyDown={onKeyDown}
      className={cx(
        "flex min-w-0 items-center overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden",
        variant === "underline" ? "gap-6 border-b border-gray-200" : "gap-1",
        className,
      )}
    >
      {items.map((it, i) => {
        const on = it.id === value;
        const ids = idBase ? tabIds(idBase, it.id) : null;
        return (
          <button
            key={it.id}
            type="button"
            role="tab"
            id={ids?.tab}
            aria-controls={ids?.panel}
            aria-selected={on}
            disabled={it.disabled}
            tabIndex={on || (!hasValue && i === 0) ? 0 : -1}
            onClick={() => onChange(it.id)}
            className={cx(
              "group relative flex shrink-0 cursor-pointer items-center gap-2 whitespace-nowrap text-sm transition-colors duration-100 focus:outline-none disabled:cursor-not-allowed disabled:opacity-50 [&_svg]:size-4",
              variant === "underline"
                ? cx(
                    "-mb-px h-10 border-b-2 focus-visible:bg-gray-100",
                    on ? "border-foreground font-medium text-foreground" : "border-transparent text-gray-500 hover:text-foreground",
                  )
                : cx(
                    "h-7 rounded-md px-2.5 focus-visible:ring-2 focus-visible:ring-pink-500",
                    on ? "bg-gray-100 font-medium text-foreground" : "text-gray-500 hover:bg-gray-50 hover:text-foreground",
                  ),
            )}
          >
            {it.icon && <span aria-hidden="true" className={cx(on ? "text-foreground" : "text-gray-500 group-hover:text-foreground")}>{it.icon}</span>}
            {it.label}
            {it.count !== undefined && (
              <span className="rounded-full bg-gray-100 px-1.5 text-xs leading-[18px] text-gray-600 tabular-nums group-aria-selected:bg-gray-200">
                {it.count}
              </span>
            )}
          </button>
        );
      })}
    </div>
  );
}

/** Panel for a Tabs list sharing the same `idBase`. Render only the active one. */
export function TabPanel({ idBase, id, children, className }: { idBase: string; id: string; children: ReactNode; className?: string }) {
  const ids = tabIds(idBase, id);
  return (
    <div role="tabpanel" id={ids.panel} aria-labelledby={ids.tab} tabIndex={0} className={cx("focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-500/50 rounded-md", className)}>
      {children}
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
