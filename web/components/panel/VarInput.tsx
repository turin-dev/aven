"use client";

import { forwardRef, useEffect, useId, useImperativeHandle, useMemo, useRef, useState, type ChangeEvent, type ClipboardEvent, type KeyboardEvent, type ReactNode, type Ref, type SyntheticEvent } from "react";
import { Braces } from "lucide-react";
import { t } from "@/lib/i18n";
import { cx } from "../ui";

/** `groupIcon` is shown next to the group header (service brand icon). */
export type Suggestion = { ref: string; group: string; hint?: string; groupIcon?: ReactNode };

export const VAR_INPUT_CLS =
  "h-[34px] w-full min-w-0 rounded-md border border-gray-200 bg-transparent px-1.5 py-1.5 font-mono text-sm leading-5 text-foreground outline-none transition-colors placeholder-gray-500 hover:border-gray-400 focus:border-pink-400";

export const REF_BUTTON_CLS =
  "absolute right-0 top-0 z-10 mr-1 mt-1 flex h-[26px] min-w-[26px] cursor-pointer items-center justify-center rounded-md border-0 bg-gray-100 px-1.5 text-xs leading-4 text-gray-600 transition-transform duration-50 hover:text-gray-900 active:scale-95 focus:outline-none focus-visible:ring-2 focus-visible:ring-gray-600 [&_svg]:size-[14px] [&_svg]:shrink-0";

export const REF_POPUP_CLS =
  "absolute left-0 right-0 top-full z-[65] mt-0.5 max-h-[280px] min-w-[260px] animate-pop-in overflow-y-auto rounded border border-gray-200 bg-background p-2 shadow-md dark:bg-gray-100";

const OPEN_RE = /\$\{\{\s*([A-Za-z0-9_.-]*)$/;

export type VarInputElement = HTMLInputElement | HTMLTextAreaElement;

/**
 * Value input with Railway-style reference autocomplete: typing `${{` lists shared variables,
 * other services' variables and keel-provided variables.
 *
 * ARIA combobox pattern: focus stays in the input, the popup is a grouped listbox, the active option is exposed with
 * aria-activedescendant. ↑/↓ move, Enter/Tab insert, Escape closes.
 *
 * Multi-line values (PEM keys, JSON) switch to a textarea: a single-line input would silently drop the line breaks.
 * Pasting text with line breaks switches too. In the textarea Enter adds a line and ⌘/Ctrl+Enter submits.
 */
export const VarInput = forwardRef<
  VarInputElement,
  {
    value: string;
    onChange: (v: string) => void;
    suggestions: Suggestion[];
    onEnter?: () => void;
    placeholder?: string;
    ariaLabel: string;
    autoFocus?: boolean;
    className?: string;
    id?: string;
    invalid?: boolean;
    describedBy?: string;
    refButton?: boolean;
  }
>(function VarInput({ value, onChange, suggestions, onEnter, placeholder, ariaLabel, autoFocus, className, id, invalid, describedBy, refButton }, fwd) {
  const ref = useRef<VarInputElement>(null);
  useImperativeHandle(fwd, () => ref.current!);
  const uid = useId();
  const listId = `${uid}-list`;
  const optId = (i: number) => `${uid}-opt-${i}`;
  const [caret, setCaret] = useState(0);
  const [active, setActive] = useState(0);
  const [dismissed, setDismissed] = useState(false);

  const multiline = value.includes("\n");
  // Keep the caret where it was when the input turns into a textarea (or back).
  const wasMulti = useRef(multiline);
  /** Set by the user's own edits: only those move focus to the swapped element. */
  const typed = useRef(false);
  useEffect(() => {
    if (wasMulti.current === multiline) return;
    wasMulti.current = multiline;
    const el = ref.current;
    if (el && typed.current && document.activeElement !== el) {
      el.focus();
      el.setSelectionRange(caret, caret);
    }
    typed.current = false;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [multiline]);
  const before = value.slice(0, caret);
  const m = OPEN_RE.exec(before);
  const partial = m?.[1] ?? null;
  const list = useMemo(() => {
    if (partial === null) return [];
    const p = partial.toLowerCase();
    const rank = (r: string) => {
      const l = r.toLowerCase();
      if (l.startsWith(p)) return 0;
      if (l.split(".").pop()!.startsWith(p)) return 1;
      if (l.includes(p)) return 2;
      return 3;
    };
    // Keep the original grouping order inside each rank so group headers stay together.
    return suggestions
      .map((s, i) => ({ s, i, r: rank(s.ref) }))
      .filter((x) => x.r < 3)
      .sort((a, b) => a.r - b.r || a.i - b.i)
      .map((x) => x.s)
      .slice(0, 60);
  }, [partial, suggestions]);
  const open = !dismissed && partial !== null && list.length > 0;
  const idx = Math.max(0, Math.min(active, list.length - 1));

  // Contiguous runs of the same group become role="group" sections.
  const groups = useMemo(() => {
    const out: { group: string; items: { s: Suggestion; i: number }[] }[] = [];
    list.forEach((s, i) => {
      const last = out[out.length - 1];
      if (last && last.group === s.group) last.items.push({ s, i });
      else out.push({ group: s.group, items: [{ s, i }] });
    });
    return out;
  }, [list]);

  useEffect(() => {
    if (!open) return;
    document.getElementById(optId(idx))?.scrollIntoView({ block: "nearest" });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, idx]);

  const insert = (s: Suggestion) => {
    const start = before.length - m![0].length;
    let rest = value.slice(caret);
    rest = rest.replace(/^[A-Za-z0-9_.-]*\s*\}\}/, "");
    const ins = `\${{${s.ref}}}`;
    const next = value.slice(0, start) + ins + rest;
    onChange(next);
    const pos = start + ins.length;
    setActive(0);
    requestAnimationFrame(() => {
      ref.current?.setSelectionRange(pos, pos);
      setCaret(pos);
    });
  };

  /** Inserts `${{` at the caret (or selection) and keeps focus, which opens the suggestion list. */
  const startRef = () => {
    const el = ref.current;
    const a = el?.selectionStart ?? value.length;
    const b = el?.selectionEnd ?? a;
    const next = value.slice(0, a) + "${{" + value.slice(b);
    onChange(next);
    setDismissed(false);
    setActive(0);
    const pos = a + 3;
    requestAnimationFrame(() => {
      el?.focus();
      el?.setSelectionRange(pos, pos);
      setCaret(pos);
    });
  };

  const fieldProps = {
    id,
    value,
    autoFocus,
    spellCheck: false,
    autoComplete: "off",
    "aria-label": ariaLabel,
    placeholder,
    role: "combobox",
    "aria-expanded": open,
    "aria-haspopup": "listbox" as const,
    "aria-controls": listId,
    "aria-autocomplete": "list" as const,
    "aria-activedescendant": open ? optId(idx) : undefined,
    "aria-invalid": invalid || undefined,
    "aria-describedby": describedBy,
    onChange: (e: ChangeEvent<VarInputElement>) => {
      typed.current = true;
      onChange(e.target.value);
      setCaret(e.target.selectionStart ?? e.target.value.length);
      setActive(0);
      setDismissed(false);
    },
    onSelect: (e: SyntheticEvent<VarInputElement>) => setCaret(e.currentTarget.selectionStart ?? 0),
    onBlur: () => setTimeout(() => setDismissed(true), 120),
    onFocus: () => setDismissed(false),
    onPaste: (e: ClipboardEvent<VarInputElement>) => {
      // A single-line input drops line breaks from pasted text; insert it ourselves so the value stays intact.
      if (multiline) return;
      const text = e.clipboardData.getData("text").replace(/\r\n/g, "\n");
      if (!text.includes("\n")) return;
      e.preventDefault();
      typed.current = true;
      const el = e.currentTarget;
      const a = el.selectionStart ?? value.length;
      const b = el.selectionEnd ?? a;
      onChange(value.slice(0, a) + text + value.slice(b));
      setCaret(a + text.length);
    },
    onKeyDown: (e: KeyboardEvent<VarInputElement>) => {
      // 한글 IME 조합 중의 Enter는 글자 확정용이다. 여기서 제출하면 마지막 글자가 빠진다.
      if (e.nativeEvent.isComposing || e.keyCode === 229) return;
      if (open) {
        if (e.key === "ArrowDown") {
          e.preventDefault();
          setActive((a) => Math.min(list.length - 1, a + 1));
          return;
        }
        if (e.key === "ArrowUp") {
          e.preventDefault();
          setActive((a) => Math.max(0, a - 1));
          return;
        }
        if (e.key === "Home" || e.key === "End") {
          e.preventDefault();
          setActive(e.key === "Home" ? 0 : list.length - 1);
          return;
        }
        if (e.key === "Enter" || e.key === "Tab") {
          e.preventDefault();
          insert(list[idx]);
          return;
        }
        if (e.key === "Escape") {
          e.preventDefault();
          e.stopPropagation();
          setDismissed(true);
          return;
        }
      }
      if (e.key === "Enter" && onEnter && (!multiline || e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        onEnter();
      }
    },
  };

  return (
    <div className={cx("relative min-w-0 flex-1", className)}>
      {multiline ? (
        <textarea
          ref={ref as Ref<HTMLTextAreaElement>}
          rows={Math.min(8, value.split("\n").length)}
          wrap="off"
          {...fieldProps}
          className={cx(VAR_INPUT_CLS, "!h-auto resize-y whitespace-pre", refButton && "!pr-9", invalid && "!border-red-500")}
        />
      ) : (
        <input ref={ref as Ref<HTMLInputElement>} {...fieldProps} className={cx(VAR_INPUT_CLS, refButton && "!pr-9", invalid && "!border-red-500")} />
      )}
      {refButton && (
        <button
          type="button"
          tabIndex={-1}
          aria-label={t("참조 변수 넣기")}
          title={t("참조 변수 넣기 (${{ 입력)")}
          onMouseDown={(e) => e.preventDefault()}
          onClick={startRef}
          className={REF_BUTTON_CLS}
        >
          <Braces className="size-3.5" />
        </button>
      )}
      {open && (
        <div
          id={listId}
          role="listbox"
          aria-label={t("참조할 변수")}
          className={REF_POPUP_CLS}
        >
          {groups.map((g, gi) => (
            <div key={`${g.group}-${gi}`} role="group" aria-labelledby={`${uid}-grp-${gi}`}>
              <div id={`${uid}-grp-${gi}`} role="presentation" className="flex items-center gap-1.5 px-2.5 pb-0.5 pt-2 text-xs font-medium text-gray-600">
                {g.items[0]?.s.groupIcon}
                {g.group}
              </div>
              {g.items.map(({ s, i }) => (
                <div
                  key={s.ref}
                  id={optId(i)}
                  role="option"
                  aria-selected={i === idx}
                  onMouseDown={(e) => {
                    e.preventDefault();
                    insert(s);
                  }}
                  onMouseMove={() => setActive(i)}
                  className={cx(
                    "flex min-h-8 w-full cursor-pointer items-center rounded px-2 py-1.5 text-left text-sm",
                    i === idx && "bg-gray-100 dark:bg-gray-200",
                  )}
                >
                  <span className="min-w-0 truncate font-mono text-gray-600" title={s.ref}>
                    {s.ref}
                  </span>
                  {s.hint && <span className="ml-auto shrink-0 truncate pl-8 text-gray-500">{s.hint}</span>}
                </div>
              ))}
            </div>
          ))}
        </div>
      )}
    </div>
  );
});
