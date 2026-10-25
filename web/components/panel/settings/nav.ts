"use client";

import { useCallback, useEffect, useLayoutEffect, useRef, useState, type RefObject } from "react";

/** Normalises text for the settings filter: lower case, no extra whitespace. */
const norm = (s: string) => s.toLowerCase().replace(/\s+/g, " ");

/**
 * Settings filter (modeled on Railway's dashboard): hides individual `[data-setting]` items whose header text and keywords
 * don't contain the term, and whole `[data-settings-section]` groups that end up empty. When a group title or its own
 * keywords match, the whole group stays visible. `[data-setting-note]` blocks (group intros, notes) only show unfiltered.
 *
 * DOM based on purpose: sub-sections (edge, networking, backups) render their items asynchronously, and hiding with the
 * `hidden` attribute keeps half-typed inputs alive while filtering. A MutationObserver re-applies the filter when they appear.
 */
export function useSettingsFilter(root: RefObject<HTMLElement | null>, term: string) {
  const [visible, setVisible] = useState<string>("*");
  useLayoutEffect(() => {
    const el = root.current;
    if (!el) return;
    const t = norm(term.trim());
    let frame = 0;
    const apply = () => {
      frame = 0;
      const ids: string[] = [];
      el.querySelectorAll<HTMLElement>("[data-settings-section]").forEach((sec) => {
        const own = norm(`${sec.dataset.title ?? ""} ${sec.dataset.keywords ?? ""}`);
        const all = !t || own.includes(t);
        let shown = 0;
        sec.querySelectorAll<HTMLElement>("[data-setting]").forEach((it) => {
          const head = it.querySelector("[data-setting-head]")?.textContent ?? "";
          const hit = all || norm(`${head} ${it.dataset.keywords ?? ""}`).includes(t);
          if (it.hidden === hit) it.hidden = !hit;
          if (hit) shown++;
        });
        sec.querySelectorAll<HTMLElement>("[data-setting-note]").forEach((n) => {
          if (n.hidden !== !all) n.hidden = !all;
        });
        // A group with no items yet (still loading) stays visible while unfiltered.
        const on = all || shown > 0;
        if (sec.hidden === on) sec.hidden = !on;
        if (on) ids.push(sec.dataset.settingsSection ?? "");
      });
      setVisible(t ? ids.join(",") : "*");
    };
    apply();
    const mo = new MutationObserver(() => {
      if (!frame) frame = requestAnimationFrame(apply);
    });
    mo.observe(el, { childList: true, subtree: true });
    return () => {
      mo.disconnect();
      if (frame) cancelAnimationFrame(frame);
    };
  }, [root, term]);
  const isVisible = useCallback((id: string) => visible === "*" || visible.split(",").includes(id), [visible]);
  return { isVisible, empty: visible === "" };
}

/**
 * Right-hand table of contents: highlights the group at the top of the pane while scrolling and scrolls smoothly on click.
 * The scroll container is the metro pane content (`.metro-pane-content`).
 */
export function useScrollSpy(root: RefObject<HTMLElement | null>, ids: string[], offset = 140) {
  const [active, setActive] = useState<string | null>(ids[0] ?? null);
  const lockUntil = useRef(0);
  const key = ids.join(",");
  useEffect(() => {
    const el = root.current;
    const scroller = el?.closest<HTMLElement>(".metro-pane-content");
    if (!el || !scroller) return;
    let frame = 0;
    const update = () => {
      frame = 0;
      if (Date.now() < lockUntil.current) return;
      const top = scroller.getBoundingClientRect().top;
      const secs = ids
        .map((id) => el.querySelector<HTMLElement>(`[data-settings-section="${id}"]`))
        .filter((s): s is HTMLElement => !!s && !s.hidden);
      if (!secs.length) return;
      let cur = secs[0];
      for (const s of secs) if (s.getBoundingClientRect().top - top <= offset) cur = s;
      // At the very bottom the last group can't reach the top of the pane: treat it as current.
      if (scroller.scrollTop + scroller.clientHeight >= scroller.scrollHeight - 4) cur = secs[secs.length - 1];
      setActive(cur.dataset.settingsSection ?? null);
    };
    const onScroll = () => {
      if (!frame) frame = requestAnimationFrame(update);
    };
    const onEnd = () => {
      lockUntil.current = 0;
      onScroll();
    };
    update();
    scroller.addEventListener("scroll", onScroll, { passive: true });
    scroller.addEventListener("scrollend", onEnd);
    return () => {
      scroller.removeEventListener("scroll", onScroll);
      scroller.removeEventListener("scrollend", onEnd);
      if (frame) cancelAnimationFrame(frame);
    };
  }, [root, key, offset]); // eslint-disable-line react-hooks/exhaustive-deps

  const jump = useCallback(
    (id: string) => {
      const sec = root.current?.querySelector<HTMLElement>(`[data-settings-section="${id}"]`);
      if (!sec) return;
      setActive(id);
      // Keep the clicked entry highlighted while the smooth scroll passes the groups in between (scrollend unlocks).
      lockUntil.current = Date.now() + 1200;
      const reduce = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
      sec.scrollIntoView({ behavior: reduce ? "auto" : "smooth", block: "start" });
    },
    [root],
  );
  return { active, jump };
}
