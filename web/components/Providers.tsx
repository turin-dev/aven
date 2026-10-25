"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useEffect, useState, type ReactNode } from "react";
import { ApiError } from "@/lib/api";
import { getLocale } from "@/lib/i18n";

// Dev-only fake backend. Both checks are inlined at build time, so production builds drop the import.
const MOCK = process.env.NODE_ENV === "development" && process.env.NEXT_PUBLIC_KEEL_MOCK === "1";

function MockGate({ children }: { children: ReactNode }) {
  const [ready, setReady] = useState(!MOCK);
  useEffect(() => {
    // Keep the condition inline so the bundler can drop the dynamic import as dead code.
    if (process.env.NODE_ENV === "development" && process.env.NEXT_PUBLIC_KEEL_MOCK === "1") {
      import("@/lib/mock").then((m) => {
        m.install();
        setReady(true);
      });
    }
  }, []);
  return ready ? children : null;
}

// 미리 렌더된 HTML은 한국어라, 언어가 다르면 hydration이 어긋난다. 화면은 브라우저에서 마운트한 뒤에 그린다
// (대시보드는 어차피 API 응답을 받아야 내용이 나온다).
function LocaleGate({ children }: { children: ReactNode }) {
  const [mounted, setMounted] = useState(false);
  useEffect(() => {
    document.documentElement.lang = getLocale();
    setMounted(true);
  }, []);
  return mounted ? children : null;
}

export function Providers({ children }: { children: ReactNode }) {
  const [client] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            staleTime: 2000,
            refetchOnWindowFocus: true,
            retry: (count, err) => !(err instanceof ApiError && err.status >= 400 && err.status < 500) && count < 2,
          },
        },
      }),
  );
  return (
    <QueryClientProvider client={client}>
      <LocaleGate>
        <MockGate>{children}</MockGate>
      </LocaleGate>
    </QueryClientProvider>
  );
}
