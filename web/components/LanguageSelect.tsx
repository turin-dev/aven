"use client";

import { Languages } from "lucide-react";
import { LOCALES, getLocale, setLocale, t, type Locale } from "@/lib/i18n";
import { Select } from "./ui";

/** 화면 언어 선택. 이 브라우저에 저장하고 페이지를 새로 불러온다. 언어 이름은 번역하지 않는다(자기 언어로 보여야 찾는다). */
export function LanguageSelect({ size = "md", className }: { size?: "sm" | "md"; className?: string }) {
  return (
    <Select size={size} className={className} value={getLocale()} aria-label={t("언어")}
      onChange={(e) => { const v = e.target.value as Locale; if (v !== getLocale()) setLocale(v); }}>
      {LOCALES.map((l) => <option key={l.value} value={l.value}>{l.label}</option>)}
    </Select>
  );
}

/** 로그인 화면처럼 좁은 곳에 두는 작은 언어 선택. */
export function LanguageSelectCompact() {
  return (
    <div className="flex items-center gap-1.5 text-gray-500">
      <Languages className="size-3.5" aria-hidden />
      <LanguageSelect size="sm" className="w-28" />
    </div>
  );
}
