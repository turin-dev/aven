"use client";

/* Settings layout: grouped left nav with a search box.
   Search matches section names, card titles and a few English keywords; picking a result opens the section and
   scrolls to the card whose heading matches. Same look as SideNavLayout (components/ui), with group headings. */

import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { Search, X } from "lucide-react";
import { cx } from "@/components/ui";
import { t } from "@/lib/i18n";

export type NavItem<T extends string> = { id: T; label: string; icon: ReactNode; danger?: boolean };
export type NavGroup<T extends string> = { label: string | null; items: NavItem<T>[] };
/** One searchable setting: the card title it lives under (translated) and extra match words. */
export type SearchEntry<T extends string> = { section: T; title: string; keywords?: string };

const norm = (s: string) => s.toLocaleLowerCase().normalize("NFC").replace(/\s+/g, " ").trim();

/** Scrolls to the first card heading (h3) in `root` whose text contains `title`, and flashes it. */
export function focusHeading(root: HTMLElement | null, title: string) {
  if (!root) return false;
  const want = norm(title);
  const h = [...root.querySelectorAll("h3")].find((el) => norm(el.textContent ?? "").includes(want));
  if (!h) return false;
  const reduce = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
  h.scrollIntoView({ block: "start", behavior: reduce ? "auto" : "smooth" });
  h.setAttribute("tabindex", "-1");
  h.focus({ preventScroll: true });
  const hit = ["rounded-sm", "outline", "outline-2", "outline-offset-4", "outline-pink-500/60"];
  h.classList.add(...hit);
  window.setTimeout(() => h.classList.remove(...hit), 1600);
  return true;
}

export function SettingsNav<T extends string>({
  label,
  groups,
  value,
  onChange,
  index,
  children,
}: {
  label: string;
  groups: NavGroup<T>[];
  value: T;
  /** `title` is set when the user picked a search result inside the section. */
  onChange: (id: T, title?: string) => void;
  index: SearchEntry<T>[];
  children: ReactNode;
}) {
  const [q, setQ] = useState("");
  const [active, setActive] = useState(0);
  const listId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const visible = useMemo(() => new Set(groups.flatMap((g) => g.items.map((i) => i.id))), [groups]);
  const labelOf = useMemo(() => new Map(groups.flatMap((g) => g.items.map((i) => [i.id, i.label] as const))), [groups]);

  const results = useMemo(() => {
    const needle = norm(q);
    if (!needle) return [];
    const words = needle.split(" ");
    const out: { section: T; title?: string; text: string }[] = [];
    for (const g of groups)
      for (const it of g.items)
        if (words.every((w) => norm(it.label).includes(w))) out.push({ section: it.id, text: it.label });
    for (const e of index) {
      if (!visible.has(e.section)) continue;
      const hay = norm(`${e.title} ${labelOf.get(e.section) ?? ""} ${e.keywords ?? ""}`);
      if (words.every((w) => hay.includes(w))) out.push({ section: e.section, title: e.title, text: e.title });
    }
    return out.slice(0, 12);
  }, [q, groups, index, visible, labelOf]);

  useEffect(() => setActive(0), [q]);

  // "/" focuses the search box unless the user is typing somewhere.
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if (e.key !== "/" || e.metaKey || e.ctrlKey || e.altKey) return;
      const el = e.target as HTMLElement | null;
      if (el && (el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName))) return;
      e.preventDefault();
      inputRef.current?.focus();
    };
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, []);

  const pick = (r: { section: T; title?: string }) => {
    setQ("");
    onChange(r.section, r.title);
  };

  return (
    <div className="grid w-full flex-1 grid-cols-[minmax(0px,_1fr)] grid-rows-[auto_minmax(0px,_1fr)] md:grid-cols-[auto_minmax(0px,_1fr)] md:grid-rows-none">
      <div className="min-w-0 px-6 pt-4 md:sticky md:top-0 md:ml-12 md:w-[215px] md:self-start md:p-0 md:pt-12">
        <div className="relative mb-3 md:mx-1">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-gray-400" aria-hidden />
          <input
            ref={inputRef}
            type="search"
            role="combobox"
            aria-label={t("설정 검색")}
            aria-expanded={results.length > 0}
            aria-controls={listId}
            aria-activedescendant={results.length ? `${listId}-${active}` : undefined}
            aria-autocomplete="list"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "ArrowDown") {
                e.preventDefault();
                setActive((a) => Math.min(a + 1, Math.max(0, results.length - 1)));
              } else if (e.key === "ArrowUp") {
                e.preventDefault();
                setActive((a) => Math.max(0, a - 1));
              } else if (e.key === "Enter" && results[active]) {
                e.preventDefault();
                pick(results[active]);
              } else if (e.key === "Escape" && q) {
                e.preventDefault();
                e.stopPropagation();
                setQ("");
              }
            }}
            placeholder={t("설정 검색")}
            className="h-8 w-full rounded-md border border-gray-200 bg-transparent pl-8 pr-7 text-sm text-foreground placeholder-gray-500 hover:border-gray-400 focus:border-pink-400 focus:outline-none [&::-webkit-search-cancel-button]:hidden"
          />
          {q ? (
            <button
              type="button"
              aria-label={t("검색어 지우기")}
              onClick={() => (setQ(""), inputRef.current?.focus())}
              className="absolute right-1.5 top-1/2 flex size-5 -translate-y-1/2 cursor-pointer items-center justify-center rounded text-gray-500 hover:bg-gray-100 hover:text-foreground"
            >
              <X className="size-3.5" />
            </button>
          ) : (
            <kbd className="pointer-events-none absolute right-2 top-1/2 hidden -translate-y-1/2 rounded border border-gray-200 px-1 font-mono text-[10px] text-gray-400 md:block" aria-hidden>
              /
            </kbd>
          )}
          {q && (
            <ul
              id={listId}
              role="listbox"
              aria-label={t("검색 결과")}
              className="absolute left-0 right-0 top-full z-40 mt-1 max-h-80 overflow-y-auto rounded-md border border-gray-200 bg-background p-1 shadow-lg dark:bg-secondaryBg"
            >
              {results.length === 0 ? (
                <li className="px-2.5 py-2 text-sm text-gray-500" role="presentation">
                  {t("일치하는 설정이 없습니다")}
                </li>
              ) : (
                results.map((r, i) => (
                  <li
                    key={`${r.section}-${r.title ?? ""}-${i}`}
                    id={`${listId}-${i}`}
                    role="option"
                    aria-selected={i === active}
                    onMouseDown={(e) => e.preventDefault()}
                    onMouseEnter={() => setActive(i)}
                    onClick={() => pick(r)}
                    className={cx("cursor-pointer rounded px-2.5 py-1.5", i === active && "bg-gray-100")}
                  >
                    <div className="truncate text-sm text-foreground">{r.text}</div>
                    {r.title && <div className="truncate text-xs text-gray-500">{labelOf.get(r.section)}</div>}
                  </li>
                ))
              )}
            </ul>
          )}
        </div>
        <nav aria-label={label} className="flex gap-1 overflow-x-auto md:block md:overflow-visible">
          {groups.map((g, gi) => (
            <div key={g.label ?? `g${gi}`} role="group" aria-label={g.label ?? undefined} className={cx("flex shrink-0 gap-1 md:block", gi > 0 && "md:mt-4", !g.label && gi > 0 && "md:border-t md:border-gray-200 md:pt-3")}>
              {g.label && <div className="mb-1 hidden px-4 text-xs font-semibold uppercase tracking-wide text-gray-500 md:block">{g.label}</div>}
              {g.items.map((n) => (
                <button
                  key={n.id}
                  type="button"
                  onClick={() => onChange(n.id)}
                  aria-current={value === n.id ? "page" : undefined}
                  className={cx(
                    "group relative mx-1 mb-1 flex shrink-0 cursor-pointer items-center space-x-3 whitespace-nowrap rounded py-1.5 text-left hover:bg-gray-100 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-500 md:w-[calc(100%-0.5rem)] md:px-3",
                    value === n.id ? "bg-gray-100 font-medium text-foreground md:bg-transparent" : "text-gray-600",
                    n.danger && value !== n.id && "hover:text-danger",
                  )}
                >
                  <span className={cx("icon-container icon-md text-2xl", value === n.id ? "text-pink-500" : "text-gray-400 group-hover:text-gray-800")}>{n.icon}</span>
                  <span className="text-[15px]">{n.label}</span>
                </button>
              ))}
            </div>
          ))}
        </nav>
      </div>
      <div className="min-w-0 px-6 pb-16 pt-8 md:px-12 md:pr-16 md:pt-12">{children}</div>
    </div>
  );
}
