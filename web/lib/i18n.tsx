"use client";

import { Fragment, isValidElement, type ReactNode } from "react";
import { EN } from "./i18n/en";
import { readStorage, writeStorage } from "./legacyStorage";

// UI 번역. 한국어 원문이 키이고 영어 사전(lib/i18n/en/*.ts)이 그 키를 옮긴다. 사전에 없으면 원문을 그대로 쓴다.
// 언어는 localStorage(keel.locale)에 두고, 바꾸면 페이지를 새로 불러 React 밖의 문자열(시간 표기, toast)까지 한 번에 바꾼다.

export type Locale = "ko" | "en";
export const LOCALES: { value: Locale; label: string }[] = [
  { value: "ko", label: "한국어" },
  { value: "en", label: "English" },
];
const STORAGE_KEY = "aven.locale";
const LEGACY_STORAGE_KEY = "keel.locale";

function detect(): Locale {
  if (typeof window === "undefined") return "ko";
  try {
    const v = readStorage(STORAGE_KEY, LEGACY_STORAGE_KEY);
    if (v === "ko" || v === "en") return v;
  } catch {
    /* ignore */
  }
  return (navigator.language || "").toLowerCase().startsWith("ko") ? "ko" : "en";
}

let current: Locale = detect();

export function getLocale(): Locale {
  return current;
}

export function setLocale(locale: Locale) {
  try {
    writeStorage(STORAGE_KEY, locale);
  } catch {
    /* ignore */
  }
  current = locale;
  window.location.reload();
}

type Vars = Record<string, string | number>;

// 같은 한국어가 문맥에 따라 다르게 옮겨져야 하면 키에 "|문맥"을 붙인다(t("다음|shortcut")).
// 한국어 화면과 사전에 없는 문맥 키는 "|" 앞의 원문을 쓴다.
function lookup(key: string): string {
  const bar = key.indexOf("|");
  const ko = bar < 0 ? key : key.slice(0, bar);
  if (current !== "en") return ko;
  return EN[key] ?? EN[ko] ?? ko;
}

// 번역문의 {count|change|changes}는 count가 1이면 앞, 아니면 뒤 형태를 쓴다(영어 단수·복수). 숫자 자체는 {count}로 따로 적는다.
// tr()에서 <b>{n}</b>처럼 감싼 값은 children의 숫자를 본다.
function amount(v: unknown): number {
  if (isValidElement<{ children?: unknown }>(v)) return amount(v.props.children);
  return typeof v === "number" || typeof v === "string" ? Number(v) : NaN;
}

function plural(s: string, vars: Record<string, unknown>): string {
  return s.replace(/\{(\w+)\|([^|{}]*)\|([^|{}]*)\}/g, (m, k: string, one: string, other: string) =>
    k in vars ? (amount(vars[k]) === 1 ? one : other) : m,
  );
}

/** t("필수 항목 {total}개 중 {done}개 완료", { total, done }): 문자열 번역과 {이름} 치환. */
export function t(ko: string, vars?: Vars): string {
  const s = lookup(ko);
  return vars ? plural(s, vars).replace(/\{(\w+)\}/g, (m, k: string) => (k in vars ? String(vars[k]) : m)) : s;
}

/** tr("{env}에 https:// 주소를 넣으세요", { env: <code>AVEN_PUBLIC_URL</code> }): 문장 안에 JSX가 들어갈 때. */
export function tr(ko: string, parts: Record<string, ReactNode>): ReactNode {
  const s = plural(lookup(ko), parts);
  const out: ReactNode[] = [];
  let last = 0;
  for (const m of s.matchAll(/\{(\w+)\}/g)) {
    out.push(s.slice(last, m.index));
    out.push(m[1] in parts ? <Fragment key={out.length}>{parts[m[1]]}</Fragment> : m[0]);
    last = (m.index ?? 0) + m[0].length;
  }
  out.push(s.slice(last));
  return <>{out}</>;
}

/** toLocaleString 등에 넘길 BCP 47 locale. */
export function dateLocale(): string {
  return current === "en" ? "en-US" : "ko-KR";
}
