"use client";

import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { Button, Dialog, Kbd } from "@/components/ui";
import { t } from "@/lib/i18n";

export type KeyboardShortcut = {
  keys: string;
  label: string;
  group: string;
  onTrigger?: () => void;
  enabled?: boolean;
  allowInDialog?: boolean;
};

type ShortcutSource = () => KeyboardShortcut[];
type ShortcutContextValue = { register: (source: ShortcutSource) => () => void };

const ShortcutContext = createContext<ShortcutContextValue | null>(null);

const BUILT_IN_HELP: KeyboardShortcut[] = [
  { keys: "escape", label: t("뒤로 가기, 대화상자 닫기 또는 선택 해제"), group: t("전역") },
];

function isEditableTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  return !!target.closest('input, textarea, select, [contenteditable="true"], [role="textbox"]');
}

function comboParts(combo: string) {
  const parts = combo.toLowerCase().split("+");
  const key = parts.pop() ?? "";
  return { key, mods: new Set(parts) };
}

function matchesKey(combo: string, event: KeyboardEvent): boolean {
  const { key, mods } = comboParts(combo);
  const wantsMod = mods.has("mod");
  if (wantsMod ? !(event.metaKey || event.ctrlKey) : event.metaKey || event.ctrlKey) return false;
  if (mods.has("alt") !== event.altKey) return false;
  if (mods.has("shift") !== event.shiftKey && !(key === "?" && event.key === "?" && event.shiftKey)) {
    // Slash is produced with Shift on some keyboard layouts; `code` keeps Cmd+/ usable there.
    if (!(key === "/" && event.code === "Slash" && event.shiftKey && !mods.has("shift"))) return false;
  }
  if (key === "/") return event.key === "/" || event.code === "Slash";
  if (key === "arrowup") return event.key === "ArrowUp";
  if (key === "arrowdown") return event.key === "ArrowDown";
  if (key === "arrowright") return event.key === "ArrowRight";
  return event.key.toLowerCase() === key;
}

function sequenceKeys(keys: string): string[] {
  return keys.trim().split(/\s+/);
}

function displayKeys(keys: string): string {
  return keys
    .split(/\s+/)
    .map((combo) =>
      combo
        .split("+")
        .map((part) => {
          const key = part.toLowerCase();
          if (key === "mod") return "⌘ / Ctrl";
          if (key === "ctrl") return "Ctrl";
          if (key === "meta") return "⌘";
          if (key === "shift") return "Shift";
          if (key === "alt") return "Alt";
          if (key === "arrowup") return "↑";
          if (key === "arrowdown") return "↓";
          if (key === "arrowright") return "→";
          if (key === "escape") return "Esc";
          if (key === "enter") return "Enter";
          if (key === "backspace") return "Backspace";
          return part.length === 1 ? part.toUpperCase() : part;
        })
        .join(" + "),
    )
    .join(" then ");
}

export function useKeyboardShortcuts(shortcuts: KeyboardShortcut[]) {
  const context = useContext(ShortcutContext);
  if (!context) throw new Error("useKeyboardShortcuts must be used inside KeyboardShortcutProvider");
  const current = useRef(shortcuts);
  current.current = shortcuts;

  useEffect(() => context.register(() => current.current), [context]);
}

export function KeyboardShortcutProvider({ children }: { children: ReactNode }) {
  const sources = useRef(new Map<symbol, ShortcutSource>());
  const [, setRevision] = useState(0);
  const [helpOpen, setHelpOpen] = useState(false);
  const sequence = useRef<{ first: string; at: number } | null>(null);

  const register = useCallback((source: ShortcutSource) => {
    const id = Symbol("keyboard shortcut source");
    sources.current.set(id, source);
    setRevision((n) => n + 1);
    return () => {
      sources.current.delete(id);
      setRevision((n) => n + 1);
    };
  }, []);

  const entries = useCallback(() => [...sources.current.values()].flatMap((source) => source()), []);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.isComposing || isEditableTarget(event.target)) return;

      const all = entries().filter((shortcut) => shortcut.enabled !== false);
      const inDialog = event.target instanceof HTMLElement && !!event.target.closest('[role="dialog"], [aria-modal="true"]');
      const available = all.filter((shortcut) => !inDialog || shortcut.allowInDialog);
      const help = {
        keys: "?",
        label: t("단축키 도움말 열기"),
        group: t("전역"),
        allowInDialog: true,
        onTrigger: () => setHelpOpen(true),
      } satisfies KeyboardShortcut;
      const shortcuts = [...available, help];

      const pending = sequence.current;
      if (pending && Date.now() - pending.at < 1000) {
        const match = shortcuts.find((shortcut) => {
          const keys = sequenceKeys(shortcut.keys);
          return keys.length > 1 && keys[0] === pending.first && matchesKey(keys[keys.length - 1], event);
        });
        sequence.current = null;
        if (match?.onTrigger) {
          event.preventDefault();
          match.onTrigger();
          return;
        }
      } else {
        sequence.current = null;
      }

      const sequenceStart = shortcuts.some((shortcut) => {
        const keys = sequenceKeys(shortcut.keys);
        return keys.length > 1 && matchesKey(keys[0], event);
      });
      if (sequenceStart) {
        sequence.current = { first: event.key.toLowerCase(), at: Date.now() };
        return;
      }

      const match = shortcuts.find((shortcut) => sequenceKeys(shortcut.keys).length === 1 && matchesKey(shortcut.keys, event));
      if (match?.onTrigger) {
        event.preventDefault();
        match.onTrigger();
      }
    };

    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [entries]);

  const context = useRef<ShortcutContextValue>({ register });
  context.current.register = register;
  const visible = [...entries(), ...BUILT_IN_HELP, { keys: "?", label: t("단축키 도움말 열기"), group: t("전역") }]
    .filter((shortcut) => shortcut.enabled !== false)
    .filter((shortcut, index, all) => all.findIndex((other) => other.keys === shortcut.keys && other.label === shortcut.label) === index);
  const groups = [...new Set(visible.map((shortcut) => shortcut.group))];

  return (
    <ShortcutContext.Provider value={context.current}>
      {children}
      <Dialog
        open={helpOpen}
        onClose={() => setHelpOpen(false)}
        title={t("키보드 단축키")}
        footer={
          <div className="flex w-full items-center justify-between gap-4">
            <span className="text-xs text-gray-500">{t("⌘는 macOS, Ctrl은 Windows/Linux에서 사용합니다.")}</span>
            <Button onClick={() => setHelpOpen(false)}>{t("닫기")}</Button>
          </div>
        }
        wide
      >
        <div className="space-y-5 pb-1">
          {groups.map((group) => (
            <section key={group} aria-label={group}>
              <h2 className="mb-2 text-xs font-semibold text-gray-500">{group}</h2>
              <ul className="divide-y divide-gray-100 rounded-md border border-gray-100">
                {visible
                  .filter((shortcut) => shortcut.group === group)
                  .map((shortcut) => (
                    <li key={`${shortcut.keys}:${shortcut.label}`} className="flex items-center justify-between gap-4 px-3 py-2.5">
                      <span className="text-sm text-foreground">{shortcut.label}</span>
                      <span className="shrink-0">
                        {displayKeys(shortcut.keys)
                          .split(" then ")
                          .map((key, index) => (
                            <span key={`${key}-${index}`}>
                              {index > 0 && <span className="px-1 text-xs text-gray-400">{t("다음|shortcut")}</span>}
                              <Kbd>{key}</Kbd>
                            </span>
                          ))}
                      </span>
                    </li>
                  ))}
              </ul>
            </section>
          ))}
        </div>
      </Dialog>
    </ShortcutContext.Provider>
  );
}
