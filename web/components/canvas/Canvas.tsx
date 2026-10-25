"use client";

import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
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
import { CARD_H, CARD_W, ServiceCard, VOL_H, cardSpan } from "../ServiceCard";
import { Button, cx } from "../ui";
import { lock } from "../Perm";
import { useContextTrigger } from "../ContextMenu";
import { CanvasContextMenu, ServiceContextMenu, VolumeContextMenu } from "./CanvasMenus";
import { setPendingPlace, takePendingPlace } from "@/lib/uiState";
import type { NewView } from "@/lib/nav";
import { useKeyboardShortcuts } from "@/components/KeyboardShortcuts";

type Pos = { x: number; y: number };
type View = { x: number; y: number; z: number };
type Rect = { x: number; y: number; w: number; h: number };

const VIEW_KEY = "keel.canvas.view.v3";
const MIN_Z = 0.2;
/** The first view of an environment never zooms out below this, even if that crops far-away cards. */
const READABLE_Z = 0.65;
const GRID = 24;
const MAX_Z = 2;
const GAP_X = 96;
const GAP_Y = 72;

function loadView(eid: string): View | null {
  if (typeof window === "undefined") return null;
  try {
    const raw = window.localStorage.getItem(`${VIEW_KEY}:${eid}`);
    return raw ? (JSON.parse(raw) as View) : null;
  } catch {
    return null;
  }
}
function defaultPos(i: number, n: number): Pos {
  const cols = Math.min(3, Math.max(1, Math.ceil(Math.sqrt(n))));
  return { x: (i % cols) * (CARD_W + GAP_X), y: Math.floor(i / cols) * (CARD_H + VOL_H + GAP_Y) };
}
const clampZ = (z: number) => Math.min(MAX_Z, Math.max(MIN_Z, z));
/** Orthogonal path between two cards: side-to-side when they sit apart horizontally, else top-to-bottom. */
function edgePath(a: Rect, b: Rect): string {
  const acx = a.x + a.w / 2;
  const bcx = b.x + b.w / 2;
  const acy = a.y + a.h / 2;
  const bcy = b.y + b.h / 2;
  if (b.x >= a.x + a.w + 24 || a.x >= b.x + b.w + 24) {
    const right = bcx > acx;
    const x1 = right ? a.x + a.w : a.x;
    const x2 = right ? b.x - 2 : b.x + b.w + 2;
    const xm = Math.round((x1 + x2) / 2);
    return `M${x1} ${acy} H${xm} V${bcy} H${x2}`;
  }
  const down = bcy > acy;
  const y1 = down ? a.y + a.h : a.y;
  const y2 = down ? b.y - 2 : b.y + b.h + 2;
  const ym = Math.round((y1 + y2) / 2);
  return `M${acx} ${y1} V${ym} H${bcx} V${y2}`;
}
function CtrlButton({
  label,
  onClick,
  children,
  disabled,
  pressed,
  single,
  allowed = true,
}: {
  label: string;
  onClick: () => void;
  children: ReactNode;
  disabled?: boolean;
  pressed?: boolean;
  single?: boolean;
  /** false면 권한 없음(aria-disabled + "권한이 없습니다"). */
  allowed?: boolean;
}) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
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
  const [view, setView] = useState<View | null>(() => loadView(envId));
  const viewRef = useRef(view);
  viewRef.current = view;
  const [panning, setPanning] = useState(false);
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
    (s: CanvasService, i: number): Pos => drag[s.id] ?? s.instance.canvas ?? defaultPos(i, services.length),
    [drag, services.length],
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
    const out: { from: string; to: string; d: string }[] = [];
    services.forEach((s, i) => {
      for (const name of referencedServices(s.instance.variables)) {
        const tgt = byName.get(name);
        if (!tgt || tgt.s.id === s.id) continue;
        out.push({ from: s.id, to: tgt.s.id, d: edgePath(rectOf(s, i), rectOf(tgt.s, tgt.i)) });
      }
    });
    return out;
  }, [services, rectOf]);

  const fit = useCallback(
    (inset = rightInset, floor = 0) => {
      const el = vp.current;
      if (!el || services.length === 0) return;
      const pts = services.map((s, i) => ({ ...posOf(s, i), h: cardSpan(s) }));
      const minX = Math.min(...pts.map((p) => p.x));
      const minY = Math.min(...pts.map((p) => p.y));
      const maxX = Math.max(...pts.map((p) => p.x + CARD_W));
      const maxY = Math.max(...pts.map((p) => p.y + p.h));
      const w = Math.max(200, el.clientWidth - inset);
      const h = el.clientHeight;
      const pad = 72;
      const z = clampZ(Math.max(floor, Math.min(1, (w - pad * 2) / (maxX - minX), (h - pad * 2) / (maxY - minY))));
      const ch = (maxY - minY) * z;
      setView({
        z,
        x: (w - (maxX - minX) * z) / 2 - minX * z,
        // When the floor crops the content vertically, start at the top instead of the middle.
        y: ch > h - 48 ? 48 - minY * z : (h - ch) / 2 - minY * z,
      });
    },
    [services, posOf, rightInset],
  );

  // First visit of an environment: frame all services.
  useLayoutEffect(() => {
    // Ignore an open service panel here; it would shrink the first view to a sliver.
    if (!viewRef.current && services.length) fit(0, READABLE_Z);
  }, [services.length, fit, rightInset]);

  useEffect(() => {
    if (!view) return;
    const timer = setTimeout(() => {
      try {
        window.localStorage.setItem(`${VIEW_KEY}:${envId}`, JSON.stringify(view));
      } catch {
        /* ignore */
      }
    }, 300);
    return () => clearTimeout(timer);
  }, [view, envId]);

  const zoomAt = useCallback((factor: number, cx?: number, cy?: number) => {
    const el = vp.current;
    const v = viewRef.current;
    if (!el || !v) return;
    const px = cx ?? el.clientWidth / 2;
    const py = cy ?? el.clientHeight / 2;
    const z = clampZ(v.z * factor);
    const k = z / v.z;
    setView({ z, x: px - (px - v.x) * k, y: py - (py - v.y) * k });
  }, []);

  // Wheel: ctrl/cmd zooms around the cursor, otherwise pans (trackpad friendly).
  useEffect(() => {
    const el = vp.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      if ((e.target as HTMLElement).closest("[data-noscroll]")) return;
      e.preventDefault();
      const v = viewRef.current;
      if (!v) return;
      if (e.ctrlKey || e.metaKey) {
        const r = el.getBoundingClientRect();
        zoomAt(Math.exp(-e.deltaY * 0.0085), e.clientX - r.left, e.clientY - r.top);
      } else {
        setView({ ...v, x: v.x - e.deltaX, y: v.y - e.deltaY });
      }
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, [zoomAt]);

  /* ── persist positions ── */
  const persist = useCallback(
    (moves: Record<string, Pos>) => {
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
      for (const [id, p] of Object.entries(moves)) api.setCanvasPos(id, envId, p).catch(() => qc.invalidateQueries({ queryKey: qk.canvas(envId) }));
    },
    [qc, envId, canEdit],
  );

  // Services without a stored position get the first free grid slot, saved so the layout stays put.
  const placing = useRef(new Set<string>());
  useEffect(() => {
    const missing = services.filter((s) => !s.instance.canvas && !drag[s.id] && !placing.current.has(s.id));
    if (!missing.length) return;
    const taken: Rect[] = services
      .filter((s) => s.instance.canvas)
      .map((s) => ({ x: s.instance.canvas!.x, y: s.instance.canvas!.y, w: CARD_W, h: cardSpan(s) }));
    const hit = (r: Rect) => taken.some((o) => r.x < o.x + o.w + 40 && o.x < r.x + r.w + 40 && r.y < o.y + o.h + 40 && o.y < r.y + r.h + 40);
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
  }, [services, drag, persist]);

  /* ── background pan ── */
  const pan = useRef<{ sx: number; sy: number; vx: number; vy: number; moved: boolean } | null>(null);
  const marqueeRef = useRef<{ x0: number; y0: number; x1: number; y1: number; add: boolean } | null>(null);
  const onBgDown = (e: RPointerEvent<HTMLDivElement>) => {
    if (e.button !== 0 || e.target !== e.currentTarget || !viewRef.current) return;
    e.currentTarget.setPointerCapture(e.pointerId);
    if (e.shiftKey) {
      // Shift+drag: rubber-band selection of cards (for grouping).
      const r = e.currentTarget.getBoundingClientRect();
      const x = e.clientX - r.left;
      const y = e.clientY - r.top;
      marqueeRef.current = { x0: x, y0: y, x1: x, y1: y, add: e.ctrlKey || e.metaKey || multi.length > 0 };
      setMarquee({ x0: x, y0: y, x1: x, y1: y });
      return;
    }
    pan.current = { sx: e.clientX, sy: e.clientY, vx: viewRef.current.x, vy: viewRef.current.y, moved: false };
    setPanning(true);
  };
  const onBgMove = (e: RPointerEvent<HTMLDivElement>) => {
    if (dragRef.current?.group) return onCardMove(e); // group header drag (captured by the header, bubbles here)
    const m = marqueeRef.current;
    if (m) {
      const r = e.currentTarget.getBoundingClientRect();
      m.x1 = e.clientX - r.left;
      m.y1 = e.clientY - r.top;
      setMarquee({ x0: m.x0, y0: m.y0, x1: m.x1, y1: m.y1 });
      return;
    }
    const p = pan.current;
    if (!p || !viewRef.current) return;
    if (Math.hypot(e.clientX - p.sx, e.clientY - p.sy) > 4) p.moved = true;
    setView({ ...viewRef.current, x: p.vx + e.clientX - p.sx, y: p.vy + e.clientY - p.sy });
  };
  const onBgUp = () => {
    if (dragRef.current?.group) return onCardUp();
    const m = marqueeRef.current;
    if (m) {
      marqueeRef.current = null;
      setMarquee(null);
      const v0 = viewRef.current;
      if (v0 && Math.hypot(m.x1 - m.x0, m.y1 - m.y0) > 4) {
        const toC = (x: number, y: number) => ({ x: (x - v0.x) / v0.z, y: (y - v0.y) / v0.z });
        const a = toC(Math.min(m.x0, m.x1), Math.min(m.y0, m.y1));
        const b = toC(Math.max(m.x0, m.x1), Math.max(m.y0, m.y1));
        const hit = services.filter((s, i) => {
          const p = posOf(s, i);
          return p.x < b.x && p.x + CARD_W > a.x && p.y < b.y && p.y + CARD_H > a.y;
        });
        setMulti((cur) => [...new Set([...(m.add ? cur : []), ...hit.map((s) => s.id)])]);
      }
      return;
    }
    if (pan.current && !pan.current.moved) {
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
  const onCardDown = (e: RPointerEvent<HTMLDivElement>, id: string) => {
    suppressUp.current = false;
    if (e.button !== 0 || (e.target as HTMLElement).closest("[data-nodrag]")) return;
    e.stopPropagation();
    e.currentTarget.setPointerCapture(e.pointerId);
    const ids = multiSet.has(id) && multi.length > 1 ? multi : [id];
    const origins = originsOf(ids);
    dragRef.current = { primary: id, group: null, sx: e.clientX, sy: e.clientY, origins, moved: false, last: origins, lastRect: null, mod: e.shiftKey || e.ctrlKey || e.metaKey };
  };
  const onGroupDown = (e: RPointerEvent<HTMLDivElement>, g: CanvasGroup, r: GroupRect) => {
    suppressUp.current = false;
    if (e.button !== 0 || (e.target as HTMLElement).closest("button")) return;
    e.stopPropagation();
    e.currentTarget.setPointerCapture(e.pointerId);
    const origins = originsOf(g.service_ids);
    dragRef.current = { primary: null, group: { g, r }, sx: e.clientX, sy: e.clientY, origins, moved: false, last: origins, lastRect: null, mod: false };
  };
  const onCardMove = (e: RPointerEvent<HTMLDivElement>) => {
    const d = dragRef.current;
    const v = viewRef.current;
    if (!d || !v || !canEdit) return; // viewer는 카드를 옮길 수 없다(클릭으로 열기만)
    const dx = e.clientX - d.sx;
    const dy = e.clientY - d.sy;
    if (!d.moved && Math.hypot(dx, dy) < 4) return;
    d.moved = true;
    // Snap to the dot grid: the grabbed card snaps, the others keep their offsets to it.
    const anchor = (d.primary && d.origins[d.primary]) || { x: 0, y: 0 };
    const ddx = Math.round((anchor.x + dx / v.z) / GRID) * GRID - anchor.x;
    const ddy = Math.round((anchor.y + dy / v.z) / GRID) * GRID - anchor.y;
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
    history.current.undo.push({ prev, next });
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
  useKeyboardShortcuts([
    { keys: "mod+z", label: t("캔버스 변경 실행 취소"), group: t("프로젝트 캔버스"), enabled: !selected && canEdit, onTrigger: undo },
    { keys: "mod+shift+z", label: t("캔버스 변경 다시 실행"), group: t("프로젝트 캔버스"), enabled: !selected && canEdit, onTrigger: redo },
  ]);

  const onCardUp = () => {
    const d = dragRef.current;
    dragRef.current = null;
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
    requestAnimationFrame(() => fit());
  };

  const [showEdges, setShowEdges] = useState(true);

  /* ── context menus ── */
  type Ctx =
    | { kind: "svc"; x: number; y: number; id: string }
    | { kind: "vol"; x: number; y: number; id: string; vid: string }
    | { kind: "bg"; x: number; y: number; at: Pos };
  const [ctx, setCtx] = useState<Ctx | null>(null);
  const trigger = useContextTrigger((x, y, e) => {
    const t = e.target as HTMLElement | null;
    if (!t?.closest || t.closest("[data-noctx],[data-stack-frame],[data-group-frame]")) return;
    suppressUp.current = true;
    dragRef.current = null;
    const card = t.closest<HTMLElement>("[data-svc-id]");
    if (card) {
      const id = card.dataset.svcId!;
      const vol = t.closest<HTMLElement>("[data-vol]");
      setCtx(vol ? { kind: "vol", x, y, id, vid: vol.dataset.vol! } : { kind: "svc", x, y, id });
      return;
    }
    const el = vp.current;
    const v0 = viewRef.current;
    if (!el || !v0) return;
    const r = el.getBoundingClientRect();
    const snap = (n: number) => Math.round(n / GRID) * GRID;
    setCtx({
      kind: "bg",
      x,
      y,
      at: { x: snap((x - r.left - v0.x) / v0.z - CARD_W / 2), y: snap((y - r.top - v0.y) / v0.z - CARD_H / 2) },
    });
  });
  // Only clear the menu that asked: a newer one may already have replaced it.
  const closeCtx = (mine: Ctx) => () => setCtx((c) => (c === mine ? null : c));
  const ctxSvc = ctx && ctx.kind !== "bg" ? services.find((s) => s.id === ctx.id) : undefined;
  const v = view ?? { x: 0, y: 0, z: 1 };
  const grid = GRID * v.z;

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
        className="pointer-events-none absolute left-0 top-0 origin-top-left"
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
        {/* Variable references between services, drawn under the cards. */}
        <svg className="absolute left-0 top-0 overflow-visible" width="1" height="1" aria-hidden="true">
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
          {showEdges && edges.map((e) => {
            const hl = selected === e.from || selected === e.to;
            return (
              <path
                key={`${e.from}-${e.to}`}
                d={e.d}
                fill="none"
                stroke={hl ? "var(--blue-500)" : "var(--gray-400)"}
                strokeWidth={hl ? 2 : 1}
                strokeDasharray="5 5"
                markerEnd={`url(#${hl ? "keel-arrow-hl" : "keel-arrow"})`}
              />
            );
          })}
        </svg>
        {services.map((s, i) => {
          const p = posOf(s, i);
          return (
            <div
              key={s.id}
              role="button"
              tabIndex={0}
              aria-label={multiSet.has(s.id) ? t("{name} 서비스 열기 (그룹용으로 선택됨). Shift+Enter로 선택", { name: s.name }) : t("{name} 서비스 열기. Shift+Enter로 선택", { name: s.name })}
              data-svc-id={s.id}
              aria-pressed={s.id === selected}
              onPointerDown={(e) => onCardDown(e, s.id)}
              onPointerMove={onCardMove}
              onPointerUp={onCardUp}
              onPointerCancel={() => (dragRef.current = null)}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  if (e.shiftKey || e.ctrlKey || e.metaKey) toggleMulti(s.id);
                  else onOpen(s.id);
                }
              }}
              className={cx(
                "group pointer-events-auto absolute cursor-pointer rounded-xl focus-visible:outline-offset-4",
                drag[s.id] && "z-10",
                multiSet.has(s.id) && "ring-2 ring-pink-500 ring-offset-4 ring-offset-background",
              )}
              style={{ left: p.x, top: p.y }}
            >
              <ServiceCard svc={s} envId={envId} selected={s.id === selected} />
            </div>
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
          <CtrlButton label={t("확대")} onClick={() => zoomAt(1.2)}>
            <Plus />
          </CtrlButton>
          <CtrlButton label={t("축소")} onClick={() => zoomAt(1 / 1.2)}>
            <Minus />
          </CtrlButton>
          <CtrlButton label={t("화면에 맞추기")} onClick={() => fit()}>
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
          className="absolute bottom-[16px] left-1/2 flex -translate-x-1/2 items-center gap-2 rounded-lg border border-gray-200 bg-secondaryBg px-3 py-2 text-sm shadow-[0_12px_32px_-8px_rgba(0,0,0,0.15)] dark:shadow-[0_12px_32px_-8px_rgba(0,0,0,0.6)]"
          style={{ marginLeft: -rightInset / 2 }}
        >
          <span className="tabular-nums text-gray-600">{t("서비스 {count}개 선택", { count: multi.length })}</span>
          {canEdit && (
            <Button size="sm" variant="primary" onClick={() => setCreating(true)}>
              <Boxes className="size-3.5" /> {t("그룹 만들기")}
            </Button>
          )}
          <Button size="sm" variant="ghost" onClick={() => setMulti([])} aria-label={t("선택 해제")}>
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
          onToggleEdges={() => setShowEdges((x) => !x)}
          onDone={closeCtx(ctx)}
        />
      )}
    </div>
  );
}
