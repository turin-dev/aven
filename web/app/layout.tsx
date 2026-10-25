import type { Metadata, Viewport } from "next";
import type { ReactNode } from "react";
import { Providers } from "@/components/Providers";
import { KeyboardShortcutProvider } from "@/components/KeyboardShortcuts";
import { ThemeScript } from "@/components/ThemeToggle";
import "@fontsource-variable/inter";
import "@fontsource-variable/jetbrains-mono";
import "pretendard/dist/web/variable/pretendardvariable-dynamic-subset.css";
import "./globals.css";

export const metadata: Metadata = {
  title: "Aven",
  description: "A self-hosted platform for deploying and operating applications.",
};

export const viewport: Viewport = {
  themeColor: "#13111C",
};

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    // Server default is dark; ThemeScript swaps the class before first paint, hence suppressHydrationWarning.
    <html lang="ko" className="dark" suppressHydrationWarning>
      <head>
        <ThemeScript />
      </head>
      <body className="min-h-screen">
        <KeyboardShortcutProvider>
          <Providers>{children}</Providers>
        </KeyboardShortcutProvider>
      </body>
    </html>
  );
}
