"use client";

import {
  memo,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent as RKeyboardEvent,
  type PointerEvent as RPointerEvent,
  type ReactNode,
} from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Boxes, Expand, Grip, Layers, Minus, Plus, Redo, Undo, X } from "lucide-react";
import { api, type CanvasData, type CanvasGroup, type CanvasService, type StackView } from "@/lib/api";
import { qk, useCanvas, useCaps } from "@/lib/queries";
import { referencedServices } from "@/lib/refs";
import { layoutApi } from "@/lib/layouts";
import { toast } from "@/lib/uiState";
import { t } from "@/lib/i18n";
import { StackFrames } from "./StackFrames";
import { GroupDialog, GroupFrames, patchGroupsCache, type GroupRect } from "./GroupFrames";
import {
  MAX_Z,
  MIN_Z,
  alignSnap,
  clampZ,
  easeOutCubic,
  edgeRoute,
  fitView,
  lerpView,
  overlaps,
  snapTo,
  toCanvas,
  zoomAround,
  type Guide,
  type Pos,
  type Rect,
  type View,
} from "./geometry";
import { CARD_H, CARD_W, ServiceCard, VOL_H, cardSpan } from "../ServiceCard";
import { Button, cx } from "../ui";
import { lock } from "../Perm";
import { useContextTrigger } from "../ContextMenu";
import { CanvasContextMenu, ServiceContextMenu, VolumeContextMenu } from "./CanvasMenus";
import { setPendingPlace, takePendingPlace } from "@/lib/uiState";
import type { NewView } from "@/lib/nav";
import { useKeyboardShortcuts } from "@/components/KeyboardShortcuts";
import { readStorage, writeStorage } from "@/lib/legacyStorage";

const VIEW_KEY = "aven.canvas.view.v3";
const LEGACY_VIEW_KEY = "keel.canvas.view.v3";
/** The first view of an environment never zooms out below this, even if that crops far-away cards. */
const READABLE_Z = 0.65;
const GRID = 24;
const GAP_X = 96;
const GAP_Y = 72;
/** Screen pixels within which a dragged card snaps to another card's edge or centre. */
const ALIGN_PX = 8;

function loadView(eid: string): View | null {
  if (typeof window === "undefined") return null;
  try {
    const raw = readStorage(`${VIEW_KEY}:${eid}`, `${LEGACY_VIEW_KEY}:${eid}`);
    if (!raw) return null;
    const v = JSON.parse(raw) as Partial<View>;
    // A corrupted entry (NaN, strings, out-of-range zoom) would hide the whole canvas; ignore it instead.
    if (![v.x, v.y, v.z].every((n) => typeof n === "number" && Number.isFinite(n))) return null;
    return { x: v.x!, y: v.y!, z: clampZ(v.z!) };
  } catch {
    return null;
  }
}
function defaultPos(i: number, n: number): Pos {
  const cols = Math.min(3, Math.max(1, Math.ceil(Math.sqrt(n))));
  return { x: (i % cols) * (CARD_W + GAP_X), y: Math.floor(i / cols) * (CARD_H + VOL_H + GAP_Y) };
}
const reducedMotion = () => typeof window !== "undefined" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
/** Wheel delta in pixels whatever the device reports (lines on some mice, pages rarely). */
const wheelPx = (d: number, mode: number) => (mode === 1 ? d * 16 : mode === 2 ? d * 400 : d);
const samePos = (a: Pos | undefined, b: Pos | undefined) => !!a && !!b && a.x === b.x && a.y === b.y;

function CtrlButton({
  label,
  onClick,
  children,
  disabled,
  pressed,
  single,
  allowed = true,
  keys,
}: {
  label: string;
  onClick: () => void;
  children: ReactNode;
  disabled?: boolean;
  pressed?: boolean;
  single?: boolean;
  /** false면 권한 없음(aria-disabled + "권한이 없습니다"). */
  allowed?: boolean;
  /** Keyboard shortcut shown in the tooltip, e.g. "Shift+1". */
  keys?: string;
}) {
  return (
    <button
      type="button"
      aria-label={label}
      title={keys ? `${label} (${keys})` : label}
      aria-keyshortcuts={keys}
      aria-pressed={pressed}
      disabled={disabled}
      {...lock(allowed)}
      onClick={onClick}
      className={cx(
        "group/button flex transform cursor-pointer items-center justify-center border py-1.5 text-sm leading-5 transition-transform duration-50 active:scale-95 focus:outline-none focus-visible:ring-2 disabled:cursor-not-allowed disabled:opacity-50 disabled:active:scale-100 rounded-md space-x-2 px-0",
        single
          ? "border-gray-200 text-foreground dark:text-white hover:bg-gray-100 hover:border-gray-200 disabled:bg-transparent disabled:border-gray-200 disabled:text-foreground focus-visible:ring-gray-600 focus-visible:bg-gray-100 bg-secondaryBg w-[36px] h-[36px]"
          : "bg-transparent border-transparent text-foreground hover:bg-gray-100 hover:border-gray-100 disabled:bg-transparent disabled:border-transparent focus-visible:ring-gray-600 focus-visible:bg-gray-100 h-[34px] w-[34px] flex-shrink-0",
        pressed && "bg-gray-100",
      )}
    >
      <span className="icon-container icon-md !h-4 !w-4 text-2xl text-gray-500">{children}</span>
    </button>
  );
}

const ctrlGroup = "rounded-md border border-neutral bg-secondaryBg";

type NodeHandlers = {
  down: (e: RPointerEvent<HTMLDivElement>, id: string) => void;
  move: (e: RPointerEvent<HTMLDivElement>) => void;
  up: () => void;
  cancel: () => void;
  key: (e: RKeyboardEvent<HTMLDivElement>, id: string) => void;
  focus: (id: string) => void;
  hover: (id: string | null) => void;
};

/**
 * One positioned card. Memoised so that panning, zooming, dragging another card or an unrelated event only
 * re-renders the cards whose props changed (React Query keeps unchanged services referentially equal).
 */
const CanvasNode = memo(function CanvasNode({
  svc,
  envId,
  x,
  y,
  selected,
  picked,
  dragging,
  related,
  dimmed,
  h,
}: {
  svc: CanvasService;
  envId: string;
  x: number;
  y: number;
  selected: boolean;
  picked: boolean;
  dragging: boolean;
  /** Connected by a reference to the hovered/selected card. */
  related: boolean;
  /** Another card is focused and this one is not connected to it. */
  dimmed: boolean;
  h: NodeHandlers;
}) {
  return (
    <div
      role="button"
      tabIndex={0}
      aria-label={
        picked
          ? t("{name} 서비스 열기 (그룹용으로 선택됨). Shift+Enter로 선택", { name: svc.name })
          : t("{name} 서비스 열기. Shift+Enter로 선택", { name: svc.name })
      }
      aria-roledescription={t("캔버스 카드")}
      aria-keyshortcuts="Enter Shift+Enter ArrowUp ArrowDown ArrowLeft ArrowRight Shift+F10"
      data-svc-id={svc.id}
      aria-pressed={selected}
      onPointerDown={(e) => h.down(e, svc.id)}
      onPointerMove={h.move}
      onPointerUp={h.up}
      onPointerCancel={h.cancel}
      onPointerEnter={() => h.hover(svc.id)}
      onPointerLeave={() => h.hover(null)}
      onKeyDown={(e) => h.key(e, svc.id)}
      // Keyboard focus only: a click also focuses the card, and panning then would fight the drag.
      onFocus={(e) => e.target === e.currentTarget && e.currentTarget.matches(":focus-visible") && h.focus(svc.id)}
      className={cx(
        "group pointer-events-auto absolute left-0 top-0 cursor-pointer rounded-xl transition-[opacity,box-shadow] duration-150 focus-visible:outline-offset-4",
        dragging && "z-10 cursor-grabbing shadow-[0_18px_40px_-12px_rgba(0,0,0,0.35)] dark:shadow-[0_18px_40px_-12px_rgba(0,0,0,0.8)]",
        picked && "ring-2 ring-pink-500 ring-offset-4 ring-offset-background",
        related && !selected && !picked && "ring-1 ring-blue-400/60 ring-offset-2 ring-offset-background",
        dimmed && !dragging && "opacity-70",
      )}
      style={{ transform: `translate(${x}px, ${y}px)` }}
    >
      <ServiceCard svc={svc} envId={envId} selected={selected} />
    </div>
  );
});

type Edge = { key: string; from: string; to: string; both: boolean; d: string };

/** Variable references between services, drawn under the cards. */
const EdgeLayer = memo(function EdgeLayer({ edges, focus, guides }: { edges: Edge[]; focus: string | null; guides: Guide[] }) {
  const hot = (e: Edge) => !!focus && (e.from === focus || e.to === focus);
  const anyHot = !!focus && edges.some(hot);
  // Highlighted edges draw last so they sit on top of the others.
  const ordered = anyHot ? [...edges.filter((e) => !hot(e)), ...edges.filter(hot)] : edges;
  return (
    <svg className="absolute left-0 top-0 overflow-visible" width="1" height="1" aria-hidden="true">
      <style>{`@media (prefers-reduced-motion: no-preference){.keel-edge-flow{animation:keel-edge-flow 0.8s linear infinite}}@keyframes keel-edge-flow{to{stroke-dashoffset:-10}}`}</style>
      <defs>
        {/* React Flow arrow marker: closed polyline, round joins, fill = stroke = edge colour. */}
        {(
          [
            ["keel-arrow", "var(--gray-400)"],
            ["keel-arrow-hl", "var(--blue-500)"],
          ] as const
        ).map(([id, c]) => (
          <marker
            key={id}
            id={id}
            viewBox="-10 -10 20 20"
            refX="0"
            refY="0"
            markerWidth="12.5"
            markerHeight="12.5"
            markerUnits="userSpaceOnUse"
            orient="auto-start-reverse"
          >
            <polyline points="-5,-4 0,0 -5,4 -5,-4" strokeLinecap="round" strokeLinejoin="round" strokeWidth="1" fill={c} stroke={c} />
          </marker>
        ))}
      </defs>
      {ordered.map((e) => {
        const hl = hot(e);
        const marker = `url(#${hl ? "keel-arrow-hl" : "keel-arrow"})`;
        return (
          <g key={e.key} opacity={anyHot && !hl ? 0.45 : 1}>
            {hl && <path d={e.d} fill="none" stroke="var(--blue-500)" strokeOpacity={0.18} strokeWidth={6} strokeLinecap="round" />}
            <path
              d={e.d}
              fill="none"
              className={hl ? "keel-edge-flow" : undefined}
              stroke={hl ? "var(--blue-500)" : "var(--gray-400)"}
              strokeWidth={hl ? 1.75 : 1}
              strokeDasharray="5 5"
              markerEnd={marker}
              markerStart={e.both ? marker : undefined}
            />
          </g>
        );
      })}
      {guides.map((g, i) =>
        g.axis === "x" ? (
          <line key={i} x1={g.at} x2={g.at} y1={g.from - 16} y2={g.to + 16} stroke="var(--pink-500)" strokeWidth={1} vectorEffect="non-scaling-stroke" />
        ) : (
          <line key={i} y1={g.at} y2={g.at} x1={g.from - 16} x2={g.to + 16} stroke="var(--pink-500)" strokeWidth={1} vectorEffect="non-scaling-stroke" />
        ),
      )}
    </svg>
  );
});

export function Canvas({
  envId,
  services,
  selected,
  onOpen,
  onBackground,
  rightInset,
  stacks,
  pid,
  envName = "",
  onCreate,
}: {
  envName?: string;
  /** Opens the create flow (palette) at the given view. */
  onCreate?: (view: NewView) => void;
  envId: string;
  stacks?: StackView[];
  pid?: string;
  services: CanvasService[];
  selected: string | null;
  onOpen: (id: string) => void;
  onBackground?: () => void;
  /** Width (px) covered by an overlay on the right; fit-view centers in the remaining area. */
  rightInset: number;
}) {
  const qc = useQueryClient();
  const vp = useRef<HTMLDivElement>(null);
  const [drag, setDrag] = useState<Record<string, Pos>>({});
  /** Positions written optimistically whose PUT has not finished; they win over older canvas refetches. */
  const [saving, setSaving] = useState<Record<string, Pos>>({});
  const [view, setViewState] = useState<View | null>(() => loadView(envId));
  const viewRef = useRef(view);
  // Several wheel/pointer events can arrive between two renders; each must build on the previous one.
  const setView = useCallback((v: View) => {
    viewRef.current = v;
    setViewState(v);
  }, []);
  const [panning, setPanning] = useState(false);
  const [hover, setHover] = useState<string | null>(null);
  const [guides, setGuides] = useState<Guide[]>([]);
  // v1.5 canvas groups: multi-selection (Shift/Ctrl+click, Shift+drag on the background) and live rects of empty groups.
  const groups: CanvasGroup[] = useCanvas(envId).data?.groups ?? [];
  const canEdit = useCaps(pid).canEdit;
  const [multi, setMulti] = useState<string[]>([]);
  const multiSet = useMemo(() => new Set(multi), [multi]);
  const [rectDrag, setRectDrag] = useState<Record<string, GroupRect>>({});
  const [creating, setCreating] = useState(false);
  const [marquee, setMarquee] = useState<{ x0: number; y0: number; x1: number; y1: number } | null>(null);
  useEffect(() => {
    // 지워진 서비스는 선택에서 뺀다.
    setMulti((m) => (m.every((id) => services.some((s) => s.id === id)) ? m : m.filter((id) => services.some((s) => s.id === id))));
  }, [services]);
  useEffect(() => {
    if (!multi.length) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setMulti([]);
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [multi.length]);

  const posOf = useCallback(
    (s: CanvasService, i: number): Pos => drag[s.id] ?? saving[s.id] ?? s.instance.canvas ?? defaultPos(i, services.length),
    [drag, saving, services.length],
  );
  const rectOf = useCallback(
    (s: CanvasService, i: number): Rect => {
      const p = posOf(s, i);
      return { x: p.x, y: p.y, w: CARD_W, h: CARD_H };
    },
    [posOf],
  );

  const edges = useMemo(() => {
    const byName = new Map(services.map((s, i) => [s.name, { s, i }]));
    const pairs = new Map<string, Edge>();
    services.forEach((s, i) => {
      for (const name of referencedServices(s.instance.variables)) {
        const tgt = byName.get(name);
        if (!tgt || tgt.s.id === s.id) continue;
        // A references B and B references A: one line with arrows on both ends instead of two overlapping ones.
        const back = pairs.get(`${tgt.s.id}>${s.id}`);
        if (back) {
          back.both = true;
          continue;
        }
        const { d } = edgeRoute(rectOf(s, i), rectOf(tgt.s, tgt.i));
        pairs.set(`${s.id}>${tgt.s.id}`, { key: `${s.id}>${tgt.s.id}`, from: s.id, to: tgt.s.id, both: false, d });
      }
    });
    return [...pairs.values()];
  }, [services, rectOf]);

  /* ── view: animated moves, zoom, fit ── */
  const anim = useRef<{ raf: number; target: View } | null>(null);
  const stopAnim = useCallback(() => {
    if (anim.current) cancelAnimationFrame(anim.current.raf);
    anim.current = null;
  }, []);
  useEffect(() => stopAnim, [stopAnim]);
  const animateTo = useCallback(
    (target: View, ms = 260) => {
      stopAnim();
      const from = viewRef.current;
      if (!from || reducedMotion()) return setView(target);
      const t0 = performance.now();
      const step = (now: number) => {
        const k = Math.min(1, (now - t0) / ms);
        setView(lerpView(from, target, easeOutCubic(k)));
        if (k < 1) anim.current = { raf: requestAnimationFrame(step), target };
        else anim.current = null;
      };
      anim.current = { raf: requestAnimationFrame(step), target };
    },
    [setView, stopAnim],
  );

  const rectsFor = useCallback(
    (pos?: Record<string, Pos>) => services.map((s, i) => ({ ...(pos?.[s.id] ?? posOf(s, i)), w: CARD_W, h: cardSpan(s) })),
    [services, posOf],
  );
  const fitTarget = useCallback(
    (inset = rightInset, floor = 0, pos?: Record<string, Pos>) => {
      const el = vp.current;
      if (!el || services.length === 0) return null;
      return fitView(rectsFor(pos), Math.max(200, el.clientWidth - inset), el.clientHeight, { floor });
    },
    [services.length, rectsFor, rightInset],
  );
  const fit = useCallback(
    (pos?: Record<string, Pos>) => {
      const v = fitTarget(rightInset, 0, pos);
      if (v) animateTo(v);
    },
    [fitTarget, rightInset, animateTo],
  );

  // First visit of an environment: frame all services.
  useLayoutEffect(() => {
    // Ignore an open service panel here; it would shrink the first view to a sliver.
    if (viewRef.current || !services.length) return;
    const v = fitTarget(0, READABLE_Z);
    if (v) setView(v);
  }, [services.length, fitTarget, setView]);

  useEffect(() => {
    if (!view) return;
    const timer = setTimeout(() => {
      try {
        writeStorage(`${VIEW_KEY}:${envId}`, JSON.stringify(view));
      } catch {
        /* ignore */
      }
    }, 300);
    return () => clearTimeout(timer);
  }, [view, envId]);

  /** Zoom by `factor` around a viewport point (default: centre of the visible area), animated for buttons/keys. */
  const zoomAt = useCallback(
    (factor: number, px?: number, py?: number, animate = true) => {
      const el = vp.current;
      const v = anim.current?.target ?? viewRef.current;
      if (!el || !v) return;
      const next = zoomAround(v, factor, px ?? (el.clientWidth - rightInset) / 2, py ?? el.clientHeight / 2);
      if (animate) animateTo(next, 180);
      else {
        stopAnim();
        setView(next);
      }
    },
    [rightInset, animateTo, stopAnim, setView],
  );
  const zoomTo = useCallback((z: number) => {
    const v = anim.current?.target ?? viewRef.current;
    if (v) zoomAt(z / v.z);
  }, [zoomAt]);

  // Wheel: ctrl/cmd (and trackpad pinch, which browsers report as ctrl+wheel) zooms around the cursor, otherwise pans.
  useEffect(() => {
    const el = vp.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      if ((e.target as Element).closest?.("[data-noscroll]")) return;
      e.preventDefault();
      const v = viewRef.current;
      if (!v) return;
      stopAnim();
      const dx = wheelPx(e.deltaX, e.deltaMode);
      const dy = wheelPx(e.deltaY, e.deltaMode);
      if (e.ctrlKey || e.metaKey) {
        const r = el.getBoundingClientRect();
        // A mouse wheel notch is ~100px; cap it so one notch is a comfortable step, while pinch deltas stay smooth.
        const step = Math.max(-60, Math.min(60, dy));
        zoomAt(Math.exp(-step * 0.0085), e.clientX - r.left, e.clientY - r.top, false);
      } else if (e.shiftKey && dx === 0) {
        setView({ ...v, x: v.x - dy });
      } else {
        setView({ ...v, x: v.x - dx, y: v.y - dy });
      }
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, [zoomAt, stopAnim, setView]);

  /* ── persist positions ── */
  const persist = useCallback(
    (moves: Record<string, Pos>) => {
      // Stop a canvas refetch that started before this change from putting the cards back.
      void qc.cancelQueries({ queryKey: qk.canvas(envId) });
      qc.setQueryData<CanvasData>(qk.canvas(envId), (old) =>
        old
          ? {
              ...old,
              services: old.services.map((s) => (moves[s.id] ? { ...s, instance: { ...s.instance, canvas: moves[s.id] } } : s)),
            }
          : old,
      );
      setDrag((d) => {
        const n = { ...d };
        for (const id of Object.keys(moves)) delete n[id];
        return n;
      });
      // viewer는 위치를 저장할 수 없다(서버도 403). 자동 배치는 화면에만 둔다.
      if (!canEdit) return;
      setSaving((s) => ({ ...s, ...moves }));
      const settle = (id: string, p: Pos) =>
        setSaving((s) => {
          if (!samePos(s[id], p)) return s; // a newer move of the same card is still in flight
          const n = { ...s };
          delete n[id];
          return n;
        });
      let failed = false;
      for (const [id, p] of Object.entries(moves))
        api
          .setCanvasPos(id, envId, p)
          .catch((err) => {
            if (!failed) toast(t("카드 위치를 저장하지 못했습니다: {error}", { error: err instanceof Error ? err.message : String(err) }), "error");
            failed = true;
            qc.invalidateQueries({ queryKey: qk.canvas(envId) });
          })
          .finally(() => settle(id, p));
    },
    [qc, envId, canEdit],
  );

  // Services without a stored position get the first free grid slot, saved so the layout stays put.
  const placing = useRef(new Set<string>());
  useEffect(() => {
    const missing = services.filter((s) => !s.instance.canvas && !placing.current.has(s.id));
    if (!missing.length) return;
    const taken: Rect[] = services
      .filter((s) => s.instance.canvas)
      .map((s) => ({ x: s.instance.canvas!.x, y: s.instance.canvas!.y, w: CARD_W, h: cardSpan(s) }));
    const hit = (r: Rect) => taken.some((o) => overlaps(r, o, 40));
    const order = [...missing].sort((a, b) => Number(a.source_type === "database") - Number(b.source_type === "database") || a.created_at.localeCompare(b.created_at));
    const moves: Record<string, Pos> = {};
    let slot = 0;
    // A service created from the context menu lands where the user right-clicked.
    const wanted = takePendingPlace();
    if (wanted) {
      const first = order.shift()!;
      moves[first.id] = wanted;
      taken.push({ ...wanted, w: CARD_W, h: cardSpan(first) });
      placing.current.add(first.id);
    }
    for (const s of order) {
      let p = defaultPos(slot, Math.max(services.length, 4));
      while (hit({ ...p, w: CARD_W, h: cardSpan(s) })) p = defaultPos(++slot, Math.max(services.length, 4));
      slot++;
      moves[s.id] = p;
      taken.push({ ...p, w: CARD_W, h: cardSpan(s) });
      placing.current.add(s.id);
    }
    persist(moves);
  }, [services, persist]);

  /* ── background: pan, pinch, marquee ── */
  const pan = useRef<{ sx: number; sy: number; vx: number; vy: number; moved: boolean } | null>(null);
  const touches = useRef(new Map<number, Pos>());
  const pinch = useRef<{ d: number; mid: Pos; v: View } | null>(null);
  const marqueeRef = useRef<{ x0: number; y0: number; x1: number; y1: number; add: boolean } | null>(null);
  const local = (e: { clientX: number; clientY: number }) => {
    const r = vp.current!.getBoundingClientRect();
    return { x: e.clientX - r.left, y: e.clientY - r.top };
  };
  const startPinch = () => {
    const [a, b] = [...touches.current.values()];
    const v = viewRef.current;
    if (!a || !b || !v) return;
    pan.current = null;
    pinch.current = { d: Math.max(1, Math.hypot(a.x - b.x, a.y - b.y)), mid: { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 }, v };
  };
  const onBgDown = (e: RPointerEvent<HTMLDivElement>) => {
    if (e.pointerType === "touch" && e.target === e.currentTarget) {
      touches.current.set(e.pointerId, local(e));
      if (touches.current.size === 2) {
        e.currentTarget.setPointerCapture(e.pointerId);
        return startPinch();
      }
    }
    if (e.button !== 0 || e.target !== e.currentTarget || !viewRef.current) return;
    stopAnim();
    e.currentTarget.setPointerCapture(e.pointerId);
    if (e.shiftKey) {
      // Shift+drag: rubber-band selection of cards (for grouping).
      const { x, y } = local(e);
      marqueeRef.current = { x0: x, y0: y, x1: x, y1: y, add: e.ctrlKey || e.metaKey || multi.length > 0 };
      setMarquee({ x0: x, y0: y, x1: x, y1: y });
      return;
    }
    pan.current = { sx: e.clientX, sy: e.clientY, vx: viewRef.current.x, vy: viewRef.current.y, moved: false };
    setPanning(true);
  };
  const onBgMove = (e: RPointerEvent<HTMLDivElement>) => {
    if (dragRef.current?.group) return onCardMove(e); // group header drag (captured by the header, bubbles here)
    if (touches.current.has(e.pointerId)) touches.current.set(e.pointerId, local(e));
    const pz = pinch.current;
    if (pz && touches.current.size >= 2) {
      const [a, b] = [...touches.current.values()];
      const d = Math.max(1, Math.hypot(a.x - b.x, a.y - b.y));
      const mid = { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 };
      // Keep the canvas point that was under the starting midpoint under the current midpoint.
      const z = clampZ(pz.v.z * (d / pz.d));
      const c = toCanvas(pz.v, pz.mid.x, pz.mid.y);
      setView({ z, x: mid.x - c.x * z, y: mid.y - c.y * z });
      return;
    }
    const m = marqueeRef.current;
    if (m) {
      const p = local(e);
      m.x1 = p.x;
      m.y1 = p.y;
      setMarquee({ x0: m.x0, y0: m.y0, x1: m.x1, y1: m.y1 });
      return;
    }
    const p = pan.current;
    if (!p || !viewRef.current) return;
    if (Math.hypot(e.clientX - p.sx, e.clientY - p.sy) > 4) p.moved = true;
    setView({ ...viewRef.current, x: p.vx + e.clientX - p.sx, y: p.vy + e.clientY - p.sy });
  };
  const onBgUp = (e: RPointerEvent<HTMLDivElement>) => {
    if (dragRef.current?.group) return onCardUp();
    touches.current.delete(e.pointerId);
    if (pinch.current) {
      if (touches.current.size < 2) pinch.current = null;
      pan.current = null;
      setPanning(false);
      return;
    }
    const m = marqueeRef.current;
    if (m) {
      marqueeRef.current = null;
      setMarquee(null);
      const v0 = viewRef.current;
      if (v0 && Math.hypot(m.x1 - m.x0, m.y1 - m.y0) > 4) {
        const a = toCanvas(v0, Math.min(m.x0, m.x1), Math.min(m.y0, m.y1));
        const b = toCanvas(v0, Math.max(m.x0, m.x1), Math.max(m.y0, m.y1));
        const hit = services.filter((s, i) => {
          const p = posOf(s, i);
          return p.x < b.x && p.x + CARD_W > a.x && p.y < b.y && p.y + CARD_H > a.y;
        });
        setMulti((cur) => [...new Set([...(m.add ? cur : []), ...hit.map((s) => s.id)])]);
      }
      return;
    }
    if (pan.current && !pan.current.moved && e.type === "pointerup") {
      if (multi.length) setMulti([]);
      onBackground?.();
    }
    pan.current = null;
    setPanning(false);
  };

  /* ── card drag ── */
  const suppressUp = useRef(false);
  /** One drag moves one card, the multi-selection, or every member of a group (header drag). */
  const dragRef = useRef<{
    primary: string | null;
    group: { g: CanvasGroup; r: GroupRect } | null;
    sx: number;
    sy: number;
    origins: Record<string, Pos>;
    moved: boolean;
    last: Record<string, Pos>;
    lastRect: GroupRect | null;
    mod: boolean;
    /** Cards that stay put, used for alignment guides. */
    still: Rect[];
  } | null>(null);
  const posById = (id: string): Pos | null => {
    const i = services.findIndex((s) => s.id === id);
    return i < 0 ? null : posOf(services[i], i);
  };
  const originsOf = (ids: string[]) => {
    const o: Record<string, Pos> = {};
    for (const id of ids) {
      const p = posById(id);
      if (p) o[id] = p;
    }
    return o;
  };
  const stillRects = (moving: Record<string, Pos>) =>
    services.flatMap((s, i) => (moving[s.id] ? [] : [{ ...posOf(s, i), w: CARD_W, h: CARD_H }]));
  const onCardDown = (e: RPointerEvent<HTMLDivElement>, id: string) => {
    suppressUp.current = false;
    if (e.button !== 0 || (e.target as HTMLElement).closest("[data-nodrag]")) return;
    e.stopPropagation();
    stopAnim();
    e.currentTarget.setPointerCapture(e.pointerId);
    const ids = multiSet.has(id) && multi.length > 1 ? multi : [id];
    const origins = originsOf(ids);
    dragRef.current = {
      primary: id,
      group: null,
      sx: e.clientX,
      sy: e.clientY,
      origins,
      moved: false,
      last: origins,
      lastRect: null,
      mod: e.shiftKey || e.ctrlKey || e.metaKey,
      still: stillRects(origins),
    };
  };
  const onGroupDown = (e: RPointerEvent<HTMLDivElement>, g: CanvasGroup, r: GroupRect) => {
    suppressUp.current = false;
    if (e.button !== 0 || (e.target as HTMLElement).closest("button")) return;
    e.stopPropagation();
    e.currentTarget.setPointerCapture(e.pointerId);
    const origins = originsOf(g.service_ids);
    dragRef.current = { primary: null, group: { g, r }, sx: e.clientX, sy: e.clientY, origins, moved: false, last: origins, lastRect: null, mod: false, still: [] };
  };
  const onCardMove = (e: RPointerEvent<HTMLDivElement>) => {
    const d = dragRef.current;
    const v = viewRef.current;
    if (!d || !v || !canEdit) return; // viewer는 카드를 옮길 수 없다(클릭으로 열기만)
    const dx = e.clientX - d.sx;
    const dy = e.clientY - d.sy;
    if (!d.moved && Math.hypot(dx, dy) < 4) return;
    d.moved = true;
    // The grabbed card snaps (the others keep their offsets to it): first to the edges and centres of nearby cards,
    // else to the dot grid. Alt drags freely.
    const anchor = (d.primary && d.origins[d.primary]) || (d.group ? { x: d.group.r.x, y: d.group.r.y } : { x: 0, y: 0 });
    const raw = { x: anchor.x + dx / v.z, y: anchor.y + dy / v.z };
    let nx = e.altKey ? raw.x : snapTo(raw.x, GRID);
    let ny = e.altKey ? raw.y : snapTo(raw.y, GRID);
    let found: Guide[] = [];
    if (!e.altKey && d.primary && d.still.length) {
      const a = alignSnap({ x: raw.x, y: raw.y, w: CARD_W, h: CARD_H }, d.still, ALIGN_PX / v.z);
      if (a.guides.some((g) => g.axis === "x")) nx = raw.x + a.dx;
      if (a.guides.some((g) => g.axis === "y")) ny = raw.y + a.dy;
      found = a.guides;
    }
    setGuides((cur) => (cur.length === 0 && found.length === 0 ? cur : found));
    const ddx = Math.round(nx - anchor.x);
    const ddy = Math.round(ny - anchor.y);
    const next: Record<string, Pos> = {};
    for (const [id, o] of Object.entries(d.origins)) next[id] = { x: o.x + ddx, y: o.y + ddy };
    d.last = next;
    if (Object.keys(next).length) setDrag((ps) => ({ ...ps, ...next }));
    if (d.group && !Object.keys(d.origins).length) {
      const r = { ...d.group.r, x: d.group.r.x + ddx, y: d.group.r.y + ddy };
      d.lastRect = r;
      setRectDrag((m) => ({ ...m, [d.group!.g.id]: r }));
    }
  };
  /** A cancelled pointer (system gesture, lost capture) puts the cards back instead of leaving them half-dragged. */
  const onCardCancel = () => {
    const d = dragRef.current;
    dragRef.current = null;
    setGuides([]);
    if (!d) return;
    setDrag((cur) => {
      const n = { ...cur };
      for (const id of Object.keys(d.origins)) delete n[id];
      return n;
    });
    if (d.group)
      setRectDrag((m) => {
        const n = { ...m };
        delete n[d.group!.g.id];
        return n;
      });
  };
  /** Stores the frame of an empty group (members define the frame otherwise). */
  const saveGroupRect = (g: CanvasGroup, r: GroupRect) => {
    const rect = { x: Math.round(r.x), y: Math.round(r.y), w: Math.round(r.w), h: Math.round(r.h) };
    qc.setQueryData<CanvasData>(qk.canvas(envId), (d) => patchGroupsCache(d, { ...g, rect }));
    setRectDrag((m) => {
      const n = { ...m };
      delete n[g.id];
      return n;
    });
    layoutApi.updateGroup(envId, g.id, { rect }).catch((err) => {
      toast(err instanceof Error ? err.message : String(err), "error");
      qc.invalidateQueries({ queryKey: qk.canvas(envId) });
    });
  };
  const toggleMulti = (id: string) => setMulti((m) => (m.includes(id) ? m.filter((x) => x !== id) : [...m, id]));
  /* ── undo / redo of layout changes ── */
  const history = useRef<{ undo: { prev: Record<string, Pos>; next: Record<string, Pos> }[]; redo: { prev: Record<string, Pos>; next: Record<string, Pos> }[] }>({
    undo: [],
    redo: [],
  });
  const [, bump] = useState(0);
  const commit = (prev: Record<string, Pos>, next: Record<string, Pos>) => {
    // A drag that ends where it started is not a change.
    if (Object.keys(next).every((id) => samePos(prev[id], next[id]))) {
      setDrag((d) => {
        const n = { ...d };
        for (const id of Object.keys(next)) delete n[id];
        return n;
      });
      return;
    }
    history.current.undo.push({ prev, next });
    if (history.current.undo.length > 100) history.current.undo.shift();
    history.current.redo = [];
    bump((n) => n + 1);
    persist(next);
  };
  const undo = () => {
    const e = history.current.undo.pop();
    if (!e) return;
    history.current.redo.push(e);
    bump((n) => n + 1);
    persist(e.prev);
  };
  const redo = () => {
    const e = history.current.redo.pop();
    if (!e) return;
    history.current.undo.push(e);
    bump((n) => n + 1);
    persist(e.next);
  };

  const onCardUp = () => {
    const d = dragRef.current;
    dragRef.current = null;
    setGuides([]);
    if (!d) return;
    if (suppressUp.current) {
      suppressUp.current = false;
      return;
    }
    if (d.moved) {
      if (Object.keys(d.last).length) commit(d.origins, d.last);
      if (d.group && d.lastRect) saveGroupRect(d.group.g, d.lastRect);
    } else if (d.primary) {
      if (d.mod) toggleMulti(d.primary);
      else onOpen(d.primary);
    }
  };
  /** Keyboard move of a group (Alt+arrows on the header). */
  const nudgeGroup = (g: CanvasGroup, r: GroupRect, dx: number, dy: number) => {
    const origins = originsOf(g.service_ids);
    if (!Object.keys(origins).length) return saveGroupRect(g, { ...r, x: r.x + dx, y: r.y + dy });
    const next: Record<string, Pos> = {};
    for (const [id, o] of Object.entries(origins)) next[id] = { x: o.x + dx, y: o.y + dy };
    commit(origins, next);
  };

  /** Pans so that the card is fully visible (keyboard focus on an off-screen card). */
  const reveal = (id: string) => {
    const el = vp.current;
    const v = viewRef.current;
    const p = posById(id);
    const s = services.find((x) => x.id === id);
    if (!el || !v || !p || !s) return;
    const w = el.clientWidth - rightInset;
    const h = el.clientHeight;
    const m = 48;
    const x0 = p.x * v.z + v.x;
    const y0 = p.y * v.z + v.y;
    const x1 = x0 + CARD_W * v.z;
    const y1 = y0 + cardSpan(s) * v.z;
    let dx = 0;
    let dy = 0;
    if (x0 < m) dx = m - x0;
    else if (x1 > w - m) dx = w - m - x1;
    if (y0 < m) dy = m - y0;
    else if (y1 > h - m) dy = h - m - y1;
    if (dx || dy) animateTo({ ...v, x: v.x + dx, y: v.y + dy }, 200);
  };
  const onCardKey = (e: RKeyboardEvent<HTMLDivElement>, id: string) => {
    // Keys inside the card (the domain link) keep their own behaviour.
    if (e.target !== e.currentTarget) return;
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      if (e.shiftKey || e.ctrlKey || e.metaKey) toggleMulti(id);
      else onOpen(id);
      return;
    }
    if (e.key === "ContextMenu" || (e.shiftKey && e.key === "F10")) {
      e.preventDefault();
      const b = e.currentTarget.getBoundingClientRect();
      setCtx({ kind: "svc", x: b.left + 24, y: b.top + 24, id });
      return;
    }
    const step = e.shiftKey ? GRID * 4 : GRID;
    const dir = ({ ArrowLeft: [-step, 0], ArrowRight: [step, 0], ArrowUp: [0, -step], ArrowDown: [0, step] } as Record<string, number[]>)[e.key];
    if (dir && canEdit && !e.ctrlKey && !e.metaKey) {
      e.preventDefault();
      const origins = originsOf(multiSet.has(id) && multi.length > 1 ? multi : [id]);
      const next: Record<string, Pos> = {};
      for (const [k, o] of Object.entries(origins)) next[k] = { x: o.x + dir[0], y: o.y + dir[1] };
      commit(origins, next);
    }
  };

  const createGroup = async (v: { name: string; color: string }) => {
    const g = await layoutApi.createGroup(envId, { ...v, service_ids: multi });
    qc.setQueryData<CanvasData>(qk.canvas(envId), (d) => patchGroupsCache(d, g));
    setMulti([]);
    toast(t("그룹 {name}을 만들었습니다", { name: g.name }));
  };

  const resetLayout = () => {
    const next: Record<string, Pos> = {};
    const prev: Record<string, Pos> = {};
    services.forEach((s, i) => (prev[s.id] = posOf(s, i)));
    [...services]
      .sort((a, b) => Number(a.source_type === "database") - Number(b.source_type === "database") || a.name.localeCompare(b.name))
      .forEach((s, i) => (next[s.id] = defaultPos(i, services.length)));
    commit(prev, next);
    // Frame the new positions (the closure's posOf still has the old ones).
    fit(next);
  };

  const [showEdges, setShowEdges] = useState(true);
  const selectAll = () => setMulti(services.map((s) => s.id));

  useKeyboardShortcuts([
    { keys: "mod+z", label: t("캔버스 변경 실행 취소"), group: t("프로젝트 캔버스"), enabled: !selected && canEdit, onTrigger: undo },
    { keys: "mod+shift+z", label: t("캔버스 변경 다시 실행"), group: t("프로젝트 캔버스"), enabled: !selected && canEdit, onTrigger: redo },
    { keys: "=", label: t("캔버스 확대"), group: t("프로젝트 캔버스"), onTrigger: () => zoomAt(1.25) },
    { keys: "-", label: t("캔버스 축소"), group: t("프로젝트 캔버스"), onTrigger: () => zoomAt(1 / 1.25) },
    { keys: "shift+1", label: t("모든 서비스가 보이게 맞추기"), group: t("프로젝트 캔버스"), onTrigger: () => fit() },
    { keys: "shift+0", label: t("100%로 보기"), group: t("프로젝트 캔버스"), onTrigger: () => zoomTo(1) },
    { keys: "mod+a", label: t("캔버스의 모든 서비스 선택"), group: t("프로젝트 캔버스"), enabled: !selected && services.length > 0, onTrigger: selectAll },
  ]);

  /* ── context menus ── */
  type Ctx =
    | { kind: "svc"; x: number; y: number; id: string }
    | { kind: "vol"; x: number; y: number; id: string; vid: string }
    | { kind: "bg"; x: number; y: number; at: Pos };
  const [ctx, setCtx] = useState<Ctx | null>(null);
  const trigger = useContextTrigger((x, y, e) => {
    const el0 = e.target as HTMLElement | null;
    if (!el0?.closest || el0.closest("[data-noctx],[data-stack-frame],[data-group-frame]")) return;
    suppressUp.current = true;
    onCardCancel();
    const card = el0.closest<HTMLElement>("[data-svc-id]");
    if (card) {
      const id = card.dataset.svcId!;
      const vol = el0.closest<HTMLElement>("[data-vol]");
      setCtx(vol ? { kind: "vol", x, y, id, vid: vol.dataset.vol! } : { kind: "svc", x, y, id });
      return;
    }
    const el = vp.current;
    const v0 = viewRef.current;
    if (!el || !v0) return;
    const r = el.getBoundingClientRect();
    const c = toCanvas(v0, x - r.left, y - r.top);
    setCtx({ kind: "bg", x, y, at: { x: snapTo(c.x - CARD_W / 2, GRID), y: snapTo(c.y - CARD_H / 2, GRID) } });
  });
  // Only clear the menu that asked: a newer one may already have replaced it.
  const closeCtx = (mine: Ctx) => () => setCtx((c) => (c === mine ? null : c));
  const ctxSvc = ctx && ctx.kind !== "bg" ? services.find((s) => s.id === ctx.id) : undefined;

  // Stable handler object for the memoised cards; it always calls the latest closures.
  const live = useRef<NodeHandlers>(null!);
  live.current = { down: onCardDown, move: onCardMove, up: onCardUp, cancel: onCardCancel, key: onCardKey, focus: reveal, hover: setHover };
  const handlers = useMemo<NodeHandlers>(
    () => ({
      down: (e, id) => live.current.down(e, id),
      move: (e) => live.current.move(e),
      up: () => live.current.up(),
      cancel: () => live.current.cancel(),
      key: (e, id) => live.current.key(e, id),
      focus: (id) => live.current.focus(id),
      hover: (id) => live.current.hover(id),
    }),
    [],
  );

  // Cards connected to the hovered (else selected) card; nothing is dimmed while dragging.
  const dragging = Object.keys(drag).length > 0;
  const focus = dragging ? null : (hover ?? selected);
  const related = useMemo(() => {
    const out = new Set<string>();
    if (!focus || !showEdges) return out;
    for (const e of edges) {
      if (e.from === focus) out.add(e.to);
      if (e.to === focus) out.add(e.from);
    }
    return out;
  }, [edges, focus, showEdges]);
  const dimOthers = !!hover && !dragging && showEdges && related.size > 0;

  const v = view ?? { x: 0, y: 0, z: 1 };
  const grid = GRID * v.z;
  const pct = Math.round(v.z * 100);

  return (
    <div
      ref={vp}
      onPointerDown={onBgDown}
      onPointerMove={onBgMove}
      onPointerUp={onBgUp}
      onPointerCancel={onBgUp}
      {...trigger}
      aria-label={t("서비스 캔버스. 배경을 끌어 이동하고 Ctrl과 휠로 확대합니다.")}
      className={cx("absolute inset-0 touch-none select-none overflow-hidden", panning ? "cursor-grabbing" : "cursor-grab")}
      style={{
        backgroundColor: "var(--canvas-bg)",
        // React Flow dot background: 24px grid, r=0.5 dots in gray-400, anchored to the viewport origin.
        backgroundImage: `radial-gradient(circle, var(--color-gray-400) ${0.5 * v.z}px, transparent ${0.5 * v.z + 0.5}px)`,
        backgroundSize: `${grid}px ${grid}px`,
        backgroundPosition: `${v.x - grid / 2}px ${v.y - grid / 2}px`,
      }}
    >
      <div
        className={cx("pointer-events-none absolute left-0 top-0 origin-top-left", panning && "will-change-transform")}
        style={{ transform: `translate(${v.x}px, ${v.y}px) scale(${v.z})`, visibility: view ? "visible" : "hidden" }}
      >
        {stacks && pid && stacks.length > 0 && <StackFrames stacks={stacks} services={services} posOf={posOf} pid={pid} envId={envId} />}
        {groups.length > 0 && (
          <GroupFrames
            groups={groups}
            services={services}
            posOf={posOf}
            envId={envId}
            canEdit={canEdit}
            selection={multi}
            rectOverride={rectDrag}
            onHeaderDown={onGroupDown}
            onNudge={nudgeGroup}
            onSelect={setMulti}
          />
        )}
        <EdgeLayer edges={showEdges ? edges : NO_EDGES} focus={showEdges ? focus : null} guides={guides} />
        {services.map((s, i) => {
          const p = posOf(s, i);
          return (
            <CanvasNode
              key={s.id}
              svc={s}
              envId={envId}
              x={p.x}
              y={p.y}
              selected={s.id === selected}
              picked={multiSet.has(s.id)}
              dragging={!!drag[s.id]}
              related={related.has(s.id)}
              dimmed={dimOthers && s.id !== hover && !related.has(s.id)}
              h={handlers}
            />
          );
        })}
      </div>

      {/* Bottom-left controls: layout, zoom, history, layers. */}
      <div data-noctx role="toolbar" aria-label={t("캔버스 옵션")} className="absolute bottom-4 left-4 z-20 grid gap-2" onPointerDown={(e) => e.stopPropagation()}>
        <div className="contents">
          <CtrlButton single label={t("자동 정렬")} onClick={resetLayout} allowed={canEdit}>
            <Grip />
          </CtrlButton>
        </div>
        <div className={ctrlGroup}>
          <CtrlButton label={t("확대")} onClick={() => zoomAt(1.25)} disabled={v.z >= MAX_Z} keys="=">
            <Plus />
          </CtrlButton>
          <button
            type="button"
            onClick={() => zoomTo(1)}
            title={t("100%로 보기")}
            aria-label={t("확대 비율 {pct}%. 눌러서 100%로 보기", { pct })}
            aria-keyshortcuts="Shift+0"
            className="flex h-[22px] w-[34px] cursor-pointer items-center justify-center rounded text-[10px] font-medium tabular-nums text-gray-500 hover:bg-gray-100 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-gray-600"
          >
            {pct}%
          </button>
          <CtrlButton label={t("축소")} onClick={() => zoomAt(1 / 1.25)} disabled={v.z <= MIN_Z} keys="-">
            <Minus />
          </CtrlButton>
          <CtrlButton label={t("화면에 맞추기")} onClick={() => fit()} keys="Shift+1">
            <Expand />
          </CtrlButton>
        </div>
        <div className={ctrlGroup}>
          <CtrlButton label={t("실행 취소")} onClick={undo} disabled={history.current.undo.length === 0} allowed={canEdit}>
            <Undo />
          </CtrlButton>
          <CtrlButton label={t("다시 실행")} onClick={redo} disabled={history.current.redo.length === 0} allowed={canEdit}>
            <Redo />
          </CtrlButton>
        </div>
        <div className="contents">
          <CtrlButton single label={showEdges ? t("연결선 숨기기") : t("연결선 표시")} onClick={() => setShowEdges((x) => !x)} pressed={showEdges}>
            <Layers />
          </CtrlButton>
        </div>
      </div>
      {marquee && (
        <div
          aria-hidden
          className="pointer-events-none absolute rounded border border-pink-500 bg-pink-500/10"
          style={{
            left: Math.min(marquee.x0, marquee.x1),
            top: Math.min(marquee.y0, marquee.y1),
            width: Math.abs(marquee.x1 - marquee.x0),
            height: Math.abs(marquee.y1 - marquee.y0),
          }}
        />
      )}
      {multi.length > 0 && (
        <div
          data-noctx
          role="toolbar"
          aria-label={t("선택한 서비스")}
          onPointerDown={(e) => e.stopPropagation()}
          className="absolute bottom-[16px] left-1/2 flex animate-pop-in -translate-x-1/2 items-center gap-2 rounded-lg border border-gray-200 bg-secondaryBg px-3 py-2 text-sm shadow-[0_12px_32px_-8px_rgba(0,0,0,0.15)] dark:shadow-[0_12px_32px_-8px_rgba(0,0,0,0.6)]"
          style={{ marginLeft: -rightInset / 2 }}
        >
          <span className="tabular-nums text-gray-600" aria-live="polite">
            {t("서비스 {count}개 선택", { count: multi.length })}
          </span>
          {multi.length < services.length && (
            <Button size="sm" variant="ghost" onClick={selectAll} aria-keyshortcuts="Control+A">
              {t("모두 선택")}
            </Button>
          )}
          {canEdit && (
            <Button size="sm" variant="primary" onClick={() => setCreating(true)}>
              <Boxes className="size-3.5" /> {t("그룹 만들기")}
            </Button>
          )}
          <Button size="sm" variant="ghost" onClick={() => setMulti([])} aria-label={t("선택 해제")} aria-keyshortcuts="Escape">
            <X className="size-3.5" /> {t("해제|selection")}
          </Button>
        </div>
      )}
      {creating && (
        <GroupDialog
          open
          title={t("서비스 {count}개로 그룹 만들기", { count: multi.length })}
          submitLabel={t("만들기")}
          initial={{ name: t("그룹 {n}", { n: groups.length + 1 }), color: (["gray", "blue", "green", "pink", "yellow"] as const)[groups.length % 5] }}
          onClose={() => setCreating(false)}
          onSubmit={createGroup}
        />
      )}
      {ctx?.kind === "svc" && ctxSvc && (
        <ServiceContextMenu key={`s${ctx.id}${ctx.x}${ctx.y}`} x={ctx.x} y={ctx.y} svc={ctxSvc} envId={envId} envName={envName} onDone={closeCtx(ctx)} />
      )}
      {ctx?.kind === "vol" && ctxSvc && ctxSvc.volumes.find((vv) => vv.id === ctx.vid) && (
        <VolumeContextMenu
          key={`v${ctx.vid}${ctx.x}${ctx.y}`}
          x={ctx.x}
          y={ctx.y}
          svc={ctxSvc}
          vol={ctxSvc.volumes.find((vv) => vv.id === ctx.vid)!}
          onDone={closeCtx(ctx)}
        />
      )}
      {ctx?.kind === "bg" && (
        <CanvasContextMenu
          key={`b${ctx.x}${ctx.y}`}
          x={ctx.x}
          y={ctx.y}
          at={ctx.at}
          pid={pid}
          envId={envId}
          showEdges={showEdges}
          selection={multi}
          onNewGroup={() => setCreating(true)}
          onDeleted={() => setMulti([])}
          onCreate={(view) => onCreate?.(view)}
          onPlaceNext={setPendingPlace}
          onAutoLayout={resetLayout}
          onFit={() => fit()}
          onSelectAll={services.length ? selectAll : undefined}
          onToggleEdges={() => setShowEdges((x) => !x)}
          onDone={closeCtx(ctx)}
        />
      )}
    </div>
  );
}

const NO_EDGES: Edge[] = [];
