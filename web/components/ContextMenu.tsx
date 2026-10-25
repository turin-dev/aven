"use client";

import { useCallback, useEffect, useLayoutEffect, useRef, useState, type PointerEvent as RPointerEvent, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { Check, ChevronRight } from "lucide-react";
import { cx } from "./ui";

export type MenuEntry =
  | { kind: "sep" }
  | { kind: "label"; label: string }
  | {
      kind?: "item";
      label: ReactNode;
      icon?: ReactNode;
      onSelect?: () => void;
      disabled?: boolean;
      /** Shown as a tooltip when the item is disabled. */
      reason?: string;
      danger?: boolean;
      shortcut?: string;
      checked?: boolean;
      children?: MenuEntry[];
    };

type Item = Extract<MenuEntry, { kind?: "item" }>;
const isItem = (e: MenuEntry): e is Item => e.kind === undefined || e.kind === "item";

function itemsAt(root: MenuEntry[], path: number[], level: number): MenuEntry[] {
  let list = root;
  for (let i = 0; i < level; i++) {
    const it = list[path[i]];
    if (!it || !isItem(it) || !it.children) return [];
    list = it.children;
  }
  return list;
}

const panelCls =
  "z-50 min-w-[219px] max-w-[319px] rounded-md border border-gray-200 bg-background p-2 shadow-[0px_2px_6px_rgba(0,0,0,0.04),0px_6px_24px_rgba(0,0,0,0.04)]";

function Panel({
  items,
  level,
  path,
  setPath,
  choose,
  className,
  style,
  prefLeft = false,
}: {
  prefLeft?: boolean;
  items: MenuEntry[];
  level: number;
  path: number[];
  setPath: (p: number[]) => void;
  choose: (it: Item, level: number, idx: number) => void;
  className?: string;
  style?: React.CSSProperties;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [flip, setFlip] = useState<{ left: boolean; dy: number }>({ left: prefLeft, dy: 0 });
  // Submenus open to the right and flip to the left or shift up when they would leave the viewport.
  useLayoutEffect(() => {
    if (level === 0) return;
    const el = ref.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    // Keep the direction of the parent level so nested menus never cover their ancestors.
    const left = r.right > window.innerWidth - 8 ? true : r.left < 8 ? false : prefLeft;
    setFlip({ left, dy: r.bottom > window.innerHeight - 8 ? window.innerHeight - 8 - r.bottom : 0 });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [level]);
  const active = path[level];
  return (
    <div
      ref={ref}
      role="menu"
      className={cx(panelCls, className)}
      style={level > 0 ? { position: "absolute", top: -9 + flip.dy, ...(flip.left ? { right: "calc(100% + 2px)" } : { left: "calc(100% + 2px)" }) } : style}
    >
      {items.map((e, i) => {
        if (!isItem(e)) {
          return e.kind === "sep" ? (
            <div key={i} role="separator" className="m-2 h-px bg-gray-100" />
          ) : (
            <div key={i} className="px-2 py-1 text-xs text-gray-600">
              {e.label}
            </div>
          );
        }
        const isActive = active === i;
        const open = !!e.children && path.length > level + 1 && isActive;
        return (
          // 열린 하위 메뉴가 뒤따르는 형제 행들 위에 그려지도록 그 행을 올린다(행마다 relative라 기본은 뒤 행이 위).
          <div key={i} className={cx("relative", open && "z-10")}>
            <button
              type="button"
              role="menuitem"
              aria-disabled={e.disabled || undefined}
              aria-haspopup={e.children ? "menu" : undefined}
              aria-expanded={e.children ? open : undefined}
              title={e.disabled ? e.reason : undefined}
              data-active={isActive || undefined}
              onPointerEnter={() => setPath([...path.slice(0, level), i, ...(e.children && !e.disabled ? [-1] : [])])}
              onClick={() => choose(e, level, i)}
              className={cx(
                "relative mb-0.5 flex w-full select-none items-center rounded px-3 py-2 text-left text-sm leading-4 last:mb-0",
                e.disabled ? "cursor-not-allowed text-gray-400" : e.danger ? "cursor-pointer text-danger" : "cursor-pointer text-gray-600",
                isActive && !e.disabled && (e.danger ? "bg-red-600/10" : "bg-pink-50 text-pink-500 dark:text-pink-600"),
                isActive && e.disabled && "bg-gray-100",
              )}
            >
              <span className="mr-3 flex size-[16px] shrink-0 items-center justify-center text-current">{e.icon}</span>
              <span className="min-w-0 flex-1 truncate">{e.label}</span>
              {e.checked && <Check className="size-[14px] text-pink-600" />}
              {e.shortcut && <span className="ml-[12px] shrink-0 text-xs text-gray-400">{e.shortcut}</span>}
              {e.children && <ChevronRight className="size-[14px] shrink-0 text-gray-400" />}
            </button>
            {open && e.children && (
              <Panel items={e.children} level={level + 1} path={path} setPath={setPath} choose={choose} prefLeft={flip.left} />
            )}
          </div>
        );
      })}
    </div>
  );
}

/**
 * Cursor-anchored context menu. Arrow keys move, Enter or Right opens a submenu, Left or Esc goes back.
 * Closes on outside press, scroll, wheel (canvas zoom/pan), resize and blur.
 */
export function ContextMenu({ x, y, items, onClose }: { x: number; y: number; items: MenuEntry[]; onClose: () => void }) {
  const root = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState({ left: x, top: y });
  const [path, setPath] = useState<number[]>([-1]);
  const [mounted, setMounted] = useState(false);

  useEffect(() => setMounted(true), []);

  // Flip against the viewport edges once the real size is known.
  useLayoutEffect(() => {
    const el = root.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    const pad = 8;
    setPos({
      left: x + r.width > window.innerWidth - pad ? Math.max(pad, x - r.width) : x,
      top: y + r.height > window.innerHeight - pad ? Math.max(pad, y - r.height) : y,
    });
    el.focus({ preventScroll: true });
  }, [x, y, mounted]);

  useEffect(() => {
    const down = (e: PointerEvent) => {
      if (!(e.target as HTMLElement | null)?.closest?.("[data-ctxmenu]")) onClose();
    };
    const away = (e: Event) => {
      if (!(e.target as HTMLElement | null)?.closest?.("[data-ctxmenu]")) onClose();
    };
    const hard = () => onClose();
    window.addEventListener("pointerdown", down, true);
    window.addEventListener("wheel", away, { capture: true, passive: true });
    window.addEventListener("scroll", away, true);
    window.addEventListener("resize", hard);
    window.addEventListener("blur", hard);
    return () => {
      window.removeEventListener("pointerdown", down, true);
      window.removeEventListener("wheel", away, true);
      window.removeEventListener("scroll", away, true);
      window.removeEventListener("resize", hard);
      window.removeEventListener("blur", hard);
    };
  }, [onClose]);

  const choose = useCallback(
    (it: Item, level: number, idx: number) => {
      if (it.disabled) return;
      if (it.children) {
        setPath((p) => [...p.slice(0, level), idx, -1]);
        return;
      }
      onClose();
      it.onSelect?.();
    },
    [onClose],
  );

  const onKeyDown = (e: React.KeyboardEvent) => {
    const level = path.length - 1;
    const list = itemsAt(items, path, level);
    const idxs = list.map((it, i) => (isItem(it) ? i : -1)).filter((i) => i >= 0);
    const cur = path[level];
    const move = (dir: 1 | -1) => {
      if (!idxs.length) return;
      const pos = idxs.indexOf(cur);
      const next = idxs[(pos + dir + idxs.length) % idxs.length] ?? idxs[0];
      setPath([...path.slice(0, level), next]);
    };
    const cur_item = list[cur] && isItem(list[cur]) ? (list[cur] as Item) : null;
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        if (cur === -1) setPath([...path.slice(0, level), idxs[0] ?? -1]);
        else move(1);
        break;
      case "ArrowUp":
        e.preventDefault();
        if (cur === -1) setPath([...path.slice(0, level), idxs[idxs.length - 1] ?? -1]);
        else move(-1);
        break;
      case "ArrowRight":
        e.preventDefault();
        if (cur_item?.children && !cur_item.disabled) {
          const kids = cur_item.children;
          const first = kids.findIndex(isItem);
          setPath([...path.slice(0, level), cur, first]);
        }
        break;
      case "ArrowLeft":
        e.preventDefault();
        if (level > 0) setPath(path.slice(0, level));
        else onClose();
        break;
      case "Enter":
      case " ":
        e.preventDefault();
        if (cur_item) choose(cur_item, level, cur);
        break;
      case "Escape":
        e.preventDefault();
        e.stopPropagation();
        if (level > 0) setPath(path.slice(0, level));
        else onClose();
        break;
      case "Tab":
        e.preventDefault();
        onClose();
        break;
    }
  };

  if (typeof document === "undefined") return null;
  return createPortal(
    <div
      ref={root}
      data-ctxmenu
      tabIndex={-1}
      onKeyDown={onKeyDown}
      onContextMenu={(e) => e.preventDefault()}
      onPointerDown={(e) => e.stopPropagation()}
      className="fixed z-[80] animate-pop-in outline-none"
      style={{ left: pos.left, top: pos.top }}
    >
      <Panel items={items} level={0} path={path} setPath={setPath} choose={choose} />
    </div>,
    document.body,
  );
}

/** Pointer handlers that open a context menu on right-click and on touch long-press. */
export function useContextTrigger(open: (x: number, y: number, e: { target: EventTarget | null }) => void) {
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const start = useRef<{ x: number; y: number } | null>(null);
  const cancel = () => {
    if (timer.current) clearTimeout(timer.current);
    timer.current = null;
    start.current = null;
  };
  useEffect(() => cancel, []);
  return {
    onContextMenu: (e: React.MouseEvent) => {
      e.preventDefault();
      e.stopPropagation();
      cancel();
      open(e.clientX, e.clientY, e);
    },
    onPointerDownCapture: (e: RPointerEvent<HTMLElement>) => {
      if (e.pointerType !== "touch") return;
      cancel();
      start.current = { x: e.clientX, y: e.clientY };
      const t = e.target;
      const x = e.clientX;
      const y = e.clientY;
      timer.current = setTimeout(() => {
        timer.current = null;
        open(x, y, { target: t });
      }, 520);
    },
    onPointerMoveCapture: (e: RPointerEvent<HTMLElement>) => {
      const s = start.current;
      if (s && Math.hypot(e.clientX - s.x, e.clientY - s.y) > 8) cancel();
    },
    onPointerUpCapture: cancel,
    onPointerCancelCapture: cancel,
  };
}
