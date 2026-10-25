"use client";

import { useCallback, useSyncExternalStore } from "react";

/**
 * Railway-style theming: `<html class="dark|light">`, preference stored under
 * localStorage `theme` as system | light | dark. keel defaults to dark (not
 * system) so nothing changes for users until they pick a theme.
 */
export type ThemePreference = "system" | "light" | "dark";
export type ResolvedTheme = "light" | "dark";

export const THEME_STORAGE_KEY = "theme";
export const DEFAULT_THEME: ThemePreference = "dark";
export const THEME_COLORS: Record<ResolvedTheme, string> = { dark: "#13111C", light: "#ffffff" };

const PREFERENCES: readonly ThemePreference[] = ["system", "light", "dark"];
const CHANGE_EVENT = "aven:theme";
const MEDIA = "(prefers-color-scheme: dark)";

export function isThemePreference(v: unknown): v is ThemePreference {
  return typeof v === "string" && (PREFERENCES as readonly string[]).includes(v);
}

export function getThemePreference(): ThemePreference {
  if (typeof window === "undefined") return DEFAULT_THEME;
  try {
    const v = window.localStorage.getItem(THEME_STORAGE_KEY);
    return isThemePreference(v) ? v : DEFAULT_THEME;
  } catch {
    return DEFAULT_THEME;
  }
}

function systemTheme(): ResolvedTheme {
  if (typeof window === "undefined" || !window.matchMedia) return "dark";
  return window.matchMedia(MEDIA).matches ? "dark" : "light";
}

export function resolveTheme(pref: ThemePreference): ResolvedTheme {
  return pref === "system" ? systemTheme() : pref;
}

/** Puts the resolved theme on <html> (class + color-scheme) and the theme-color meta. */
export function applyTheme(pref: ThemePreference = getThemePreference()): ResolvedTheme {
  const resolved = resolveTheme(pref);
  if (typeof document === "undefined") return resolved;
  const root = document.documentElement;
  root.classList.remove(resolved === "dark" ? "light" : "dark");
  root.classList.add(resolved);
  root.style.colorScheme = resolved;
  document.querySelector('meta[name="theme-color"]')?.setAttribute("content", THEME_COLORS[resolved]);
  return resolved;
}

export function setThemePreference(pref: ThemePreference) {
  try {
    window.localStorage.setItem(THEME_STORAGE_KEY, pref);
  } catch {
    /* storage unavailable: still apply for this page view */
  }
  applyTheme(pref);
  window.dispatchEvent(new Event(CHANGE_EVENT));
}

/**
 * Inline <head> script for the root layout. Runs before first paint so a saved
 * light theme never flashes dark. Keep it in sync with applyTheme().
 */
export const THEME_INIT_SCRIPT = `(function(){try{var p=localStorage.getItem(${JSON.stringify(THEME_STORAGE_KEY)});if(p!=="system"&&p!=="light"&&p!=="dark")p=${JSON.stringify(DEFAULT_THEME)};var t=p==="system"?(window.matchMedia&&matchMedia(${JSON.stringify(MEDIA)}).matches?"dark":"light"):p;var r=document.documentElement;r.classList.remove(t==="dark"?"light":"dark");r.classList.add(t);r.style.colorScheme=t;var m=document.querySelector('meta[name="theme-color"]');if(m)m.setAttribute("content",t==="dark"?${JSON.stringify(THEME_COLORS.dark)}:${JSON.stringify(THEME_COLORS.light)})}catch(e){}})()`;

function subscribe(onChange: () => void) {
  const mq = window.matchMedia?.(MEDIA);
  const onSystem = () => {
    if (getThemePreference() === "system") applyTheme("system");
    onChange();
  };
  const onStorage = (e: StorageEvent) => {
    if (e.key !== THEME_STORAGE_KEY && e.key !== null) return;
    applyTheme();
    onChange();
  };
  window.addEventListener(CHANGE_EVENT, onChange);
  window.addEventListener("storage", onStorage);
  mq?.addEventListener("change", onSystem);
  return () => {
    window.removeEventListener(CHANGE_EVENT, onChange);
    window.removeEventListener("storage", onStorage);
    mq?.removeEventListener("change", onSystem);
  };
}

const serverPreference = () => DEFAULT_THEME;
const serverResolved = (): ResolvedTheme => "dark";

/**
 * Current theme preference and resolved theme. Follows other tabs (storage
 * event) and OS changes while on `system`.
 */
export function useTheme() {
  const preference = useSyncExternalStore(subscribe, getThemePreference, serverPreference);
  const resolved = useSyncExternalStore(subscribe, () => resolveTheme(getThemePreference()), serverResolved);
  const setTheme = useCallback((p: ThemePreference) => setThemePreference(p), []);
  return { theme: preference, resolvedTheme: resolved, setTheme };
}
