"use client";

/* Unsaved-changes guard for settings pages.
   Forms call useUnsavedGuard(dirty). The settings page wraps its sections in <UnsavedChangesProvider> and asks
   confirmLeave() before switching sections, so a half-edited form is not thrown away silently.
   A dirty form also arms the browser's "leave site?" prompt on reload or tab close. Without a provider the hook
   only does the latter, so components stay usable outside the settings page. */

import { createContext, useCallback, useContext, useEffect, useId, useMemo, useRef, type ReactNode } from "react";
import { useConfirm } from "./ui";
import { t } from "@/lib/i18n";

type Registry = { set: (id: string, dirty: boolean) => void };

const Ctx = createContext<Registry | null>(null);

export function useUnsavedGuard(dirty: boolean) {
  const reg = useContext(Ctx);
  const id = useId();
  useEffect(() => {
    reg?.set(id, dirty);
    return () => reg?.set(id, false);
  }, [reg, id, dirty]);
  useEffect(() => {
    if (!dirty) return;
    const h = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      // Older browsers need returnValue set to show the prompt.
      e.returnValue = "";
    };
    window.addEventListener("beforeunload", h);
    return () => window.removeEventListener("beforeunload", h);
  }, [dirty]);
}

/** Returns `guard(run)`: runs `run` right away when nothing is dirty, otherwise after the user confirms leaving. */
export function UnsavedChangesProvider({ children, render }: { children?: ReactNode; render: (guard: (run: () => void) => void) => ReactNode }) {
  const dirty = useRef(new Set<string>());
  const { confirm, node } = useConfirm();
  const reg = useMemo<Registry>(
    () => ({
      set: (id, d) => {
        if (d) dirty.current.add(id);
        else dirty.current.delete(id);
      },
    }),
    [],
  );
  const guard = useCallback(
    (run: () => void) => {
      if (dirty.current.size === 0) return run();
      confirm({
        title: t("저장하지 않은 변경 사항"),
        message: t("이 화면에 저장하지 않은 변경 사항이 있습니다. 나가면 변경 사항이 사라집니다."),
        confirmLabel: t("저장하지 않고 나가기"),
        danger: true,
        run: () => {
          dirty.current.clear();
          run();
        },
      });
    },
    [confirm],
  );
  return (
    <Ctx.Provider value={reg}>
      {render(guard)}
      {children}
      {node}
    </Ctx.Provider>
  );
}
