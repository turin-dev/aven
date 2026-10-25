"use client";

import { useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { cx } from "../../ui";

export type HeaderTab<T extends string> = { id: T; label: ReactNode };

/** DOM ids shared by a tab and its panel (aria-controls / aria-labelledby). */
export const tabId = (prefix: string, id: string) => `${prefix}-tab-${id}`;
export const tabPanelId = (prefix: string) => `${prefix}-panel`;

/**
 * Header tabs, modeled on Railway's dashboard (§4.2).
 * - md+: underline tabs, 16px, inactive gray-500, active foreground, one sliding 1px indicator.
 * - mobile: pill tabs `h-10 px-3 py-2 rounded-md`, active bg-gray-100, inactive bg-secondaryBg gray-600.
 * - WAI-ARIA tabs: roving tabindex, ←/→ wrap, Home/End, automatic activation.
 * - Narrow widths scroll horizontally (scrollbar hidden) and keep the active tab in view.
 */
export function HeaderTabs<T extends string>({
  tabs,
  value,
  onChange,
  label,
  idPrefix,
  className,
}: {
  tabs: HeaderTab<T>[];
  value: T;
  onChange: (t: T) => void;
  label: string;
  idPrefix: string;
  className?: string;
}) {
  const list = useRef<HTMLDivElement>(null);
  const ids = tabs.map((t) => t.id).join();

  // Keep the active tab visible when the bar overflows (mobile, narrow panes).
  useEffect(() => {
    const btn = list.current?.querySelector<HTMLElement>(`[data-tab="${value}"]`);
    const el = list.current;
    if (!btn || !el || el.scrollWidth <= el.clientWidth) return;
    const left = btn.offsetLeft - 24;
    const right = btn.offsetLeft + btn.offsetWidth + 24 - el.clientWidth;
    if (el.scrollLeft > left) el.scrollTo({ left, behavior: "smooth" });
    else if (el.scrollLeft < right) el.scrollTo({ left: right, behavior: "smooth" });
  }, [value, ids]);

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const i = tabs.findIndex((t) => t.id === value);
    let next = -1;
    if (e.key === "ArrowRight") next = (i + 1) % tabs.length;
    else if (e.key === "ArrowLeft") next = (i - 1 + tabs.length) % tabs.length;
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = tabs.length - 1;
    if (next < 0 || !tabs[next]) return;
    e.preventDefault();
    onChange(tabs[next].id);
    list.current?.querySelector<HTMLElement>(`[data-tab="${tabs[next].id}"]`)?.focus();
  };

  return (
    <div
      ref={list}
      role="tablist"
      aria-label={label}
      aria-orientation="horizontal"
      onKeyDown={onKeyDown}
      className={cx(
        "header-tabs relative flex w-full items-center gap-3 overflow-x-auto overflow-y-hidden [scrollbar-width:none] md:min-h-[39px] md:gap-8 [&::-webkit-scrollbar]:hidden",
        className,
      )}
    >
      {tabs.map((t) => {
        const on = t.id === value;
        return (
          <button
            key={t.id}
            type="button"
            role="tab"
            id={tabId(idPrefix, t.id)}
            aria-selected={on}
            aria-controls={tabPanelId(idPrefix)}
            tabIndex={on ? 0 : -1}
            data-tab={t.id}
            onClick={() => onChange(t.id)}
            className={cx(
              "tab-item relative inline-flex h-10 shrink-0 cursor-pointer items-center whitespace-nowrap rounded-md px-3 py-2 text-base transition-colors",
              "focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-pink-500",
              "md:h-auto md:rounded-sm md:bg-transparent md:p-0 md:pb-3 md:hover:text-foreground md:focus-visible:ring-offset-0",
              on ? "bg-gray-100 text-foreground md:text-foreground" : "bg-secondaryBg text-gray-600 md:text-gray-500",
            )}
          >
            {t.label}
          </button>
        );
      })}
      <TabIndicator cur={value} tabs={ids} />
    </div>
  );
}

/**
 * 헤더 탭의 단일 인디케이터(Railway 대시보드를 본뜸). framer `layoutId` spring(stiffness 500, damping 30)을
 * left/width 200ms transition(살짝 overshoot하는 곡선)으로 근사한다. 첫 측정은 transition 없이 놓고,
 * prefers-reduced-motion이면 transition을 끈다.
 */
function TabIndicator({ cur, tabs }: { cur: string; tabs: string }) {
  const [box, setBox] = useState<{ left: number; width: number } | null>(null);
  const [animate, setAnimate] = useState(false);
  // Measure from our own parent: a child's layout effect runs before the parent's ref is attached.
  const self = useRef<HTMLDivElement>(null);

  useLayoutEffect(() => {
    const el = self.current?.parentElement;
    if (!el) return;
    const measure = () => {
      const btn = el.querySelector<HTMLElement>(`[data-tab="${cur}"]`);
      if (!btn) return setBox(null);
      setBox((b) => (b && b.left === btn.offsetLeft && b.width === btn.offsetWidth ? b : { left: btn.offsetLeft, width: btn.offsetWidth }));
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    el.querySelectorAll("[data-tab]").forEach((n) => ro.observe(n));
    return () => ro.disconnect();
  }, [cur, tabs]);

  // 첫 위치가 잡힌 뒤에만 transition을 켠다(마운트 때 0에서 미끄러져 오지 않도록).
  useEffect(() => {
    if (!box || animate) return;
    const id = requestAnimationFrame(() => setAnimate(true));
    return () => cancelAnimationFrame(id);
  }, [box, animate]);

  return (
    <div
      ref={self}
      aria-hidden
      className={cx(
        "pointer-events-none absolute bottom-0 hidden h-px bg-foreground md:block",
        !box && "invisible",
        animate && "transition-[left,width] duration-200 ease-[cubic-bezier(0.3,1.25,0.5,1)] motion-reduce:transition-none",
      )}
      style={box ? { left: box.left, width: box.width } : undefined}
    />
  );
}

/**
 * Esc guard for panes: the first Esc leaves a focused text field (or is ignored inside a terminal),
 * only a second Esc closes the pane. Prevents losing a half-typed value or killing vim in the shell tab.
 */
export function escCloses(root: HTMLElement | null): boolean {
  const a = typeof document !== "undefined" ? (document.activeElement as HTMLElement | null) : null;
  if (!a || !root?.contains(a)) return true;
  if (a.closest(".xterm")) return false;
  const tag = a.tagName;
  if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || a.isContentEditable) {
    a.blur();
    root.focus({ preventScroll: true });
    return false;
  }
  return true;
}
