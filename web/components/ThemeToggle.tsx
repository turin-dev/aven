"use client";

import { t } from "@/lib/i18n";
import { Segmented } from "@/components/ui";
import { THEME_INIT_SCRIPT, useTheme, type ThemePreference } from "@/lib/theme";

const OPTIONS: { value: ThemePreference; label: string }[] = [
  { value: "system", label: t("시스템") },
  { value: "light", label: t("라이트") },
  { value: "dark", label: t("다크") },
];

export function ThemeToggle() {
  const { theme, setTheme } = useTheme();
  return <Segmented label={t("테마")} value={theme} options={OPTIONS} onChange={setTheme} />;
}

/**
 * No-flash theme script for the root layout's <head>. It runs during HTML
 * parsing (full page loads); on the client it renders as text/plain so React
 * does not warn about or re-run it.
 */
export function ThemeScript() {
  return (
    <script
      type={typeof window === "undefined" ? "text/javascript" : "text/plain"}
      suppressHydrationWarning
      dangerouslySetInnerHTML={{ __html: THEME_INIT_SCRIPT }}
    />
  );
}
