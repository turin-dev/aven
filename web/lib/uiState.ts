"use client";

import { useSyncExternalStore } from "react";
import { t } from "@/lib/i18n";

/** Tiny shared store for shell UI state that the top bar and canvas both read. */
let activityOpen = false;
const subs = new Set<() => void>();

export function setActivityOpen(v: boolean | ((cur: boolean) => boolean)) {
  const next = typeof v === "function" ? v(activityOpen) : v;
  if (next === activityOpen) return;
  activityOpen = next;
  subs.forEach((f) => f());
}

export function useActivityOpen(): boolean {
  return useSyncExternalStore(
    (f) => {
      subs.add(f);
      return () => subs.delete(f);
    },
    () => activityOpen,
    () => false,
  );
}

/* ── toasts ─────────────────────────────────────────────────────────── */

export type Toast = { id: number; msg: string; tone: "info" | "success" | "warning" | "error" };
const NO_TOASTS: Toast[] = [];
let toasts: Toast[] = [];
let toastSeq = 0;

function emit() {
  subs.forEach((f) => f());
}

export function toast(msg: string, tone: Toast["tone"] = "info") {
  const id = ++toastSeq;
  toasts = [...toasts, { id, msg, tone }].slice(-4);
  emit();
  setTimeout(() => {
    toasts = toasts.filter((t) => t.id !== id);
    emit();
  }, tone === "error" ? 6000 : 2600);
}

export function dismissToast(id: number) {
  toasts = toasts.filter((t) => t.id !== id);
  emit();
}

export function useToasts(): Toast[] {
  return useSyncExternalStore(
    (f) => {
      subs.add(f);
      return () => subs.delete(f);
    },
    () => toasts,
    () => NO_TOASTS,
  );
}

/* ── where the next created service should land (canvas coordinates) ── */

let pendingPlace: { x: number; y: number; at: number } | null = null;

export function setPendingPlace(p: { x: number; y: number } | null) {
  pendingPlace = p ? { ...p, at: Date.now() } : null;
}

/** Returns the pending spot once, if it was set in the last few minutes. */
export function takePendingPlace(): { x: number; y: number } | null {
  const p = pendingPlace;
  pendingPlace = null;
  return p && Date.now() - p.at < 5 * 60_000 ? { x: p.x, y: p.y } : null;
}

export function peekPendingPlace(): boolean {
  return !!pendingPlace && Date.now() - pendingPlace.at < 5 * 60_000;
}

/** Copy text, falling back to execCommand where the Clipboard API needs a secure origin. */
export async function copyText(text: string, label: string) {
  try {
    if (navigator.clipboard && window.isSecureContext) await navigator.clipboard.writeText(text);
    else {
      const ta = document.createElement("textarea");
      ta.value = text;
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.select();
      const ok = document.execCommand("copy");
      ta.remove();
      if (!ok) throw new Error("copy failed");
    }
    toast(t("{label} 복사됨", { label }));
  } catch {
    toast(t("클립보드에 복사하지 못했습니다"), "error");
  }
}
