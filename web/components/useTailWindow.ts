"use client";

import { useEffect, useLayoutEffect, useRef, useState } from "react";

const OVERSCAN = 30;

/**
 * Windowing for log lists with fixed row height: only rows near the viewport are rendered, the list follows the tail
 * until the user scrolls up, and follows again once they scroll back to the bottom.
 */
export function useTailWindow(count: number, rowH: number) {
  const ref = useRef<HTMLDivElement>(null);
  const [stick, setStick] = useState(true);
  const [vp, setVp] = useState({ top: 0, h: 600 });
  const last = useRef({ top: 0, height: 0 });

  useEffect(() => {
    const el = ref.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => setVp((v) => (v.h === el.clientHeight ? v : { top: el.scrollTop, h: el.clientHeight })));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  useLayoutEffect(() => {
    const el = ref.current;
    if (el && stick) el.scrollTop = el.scrollHeight;
  });

  const onScroll = () => {
    const el = ref.current;
    if (!el) return;
    const dist = el.scrollHeight - el.scrollTop - el.clientHeight;
    const prev = last.current;
    const up = el.scrollTop < prev.top - 2 && el.scrollHeight >= prev.height;
    last.current = { top: el.scrollTop, height: el.scrollHeight };
    if (dist < 32) setStick(true);
    else if (up) setStick(false);
    setVp((v) => (v.top === el.scrollTop ? v : { ...v, top: el.scrollTop }));
  };

  const total = count * rowH;
  const top = stick ? Math.max(0, total - vp.h) : vp.top;
  const first = Math.max(0, Math.floor(top / rowH) - OVERSCAN);
  const end = Math.min(count, Math.ceil((top + vp.h) / rowH) + OVERSCAN);
  return {
    ref,
    onScroll,
    stick,
    first,
    end,
    padTop: first * rowH,
    padBottom: Math.max(0, (count - end) * rowH),
    follow: () => setStick(true),
    toTop: () => {
      setStick(false);
      ref.current?.scrollTo({ top: 0 });
    },
  };
}
