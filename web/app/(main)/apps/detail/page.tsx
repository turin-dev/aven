"use client";

import { Suspense, useEffect } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { canvasHref, parseTab } from "@/lib/nav";
import { RedirectNotice } from "@/components/Redirect";

// Legacy v0 route: /apps/detail/?id=X&tab=T  ->  /?svc=X&tab=T (falls back to the canvas if X is not a v1 service)
function Go() {
  const params = useSearchParams();
  const router = useRouter();
  const id = params.get("id");
  const tab = params.get("tab");
  useEffect(() => {
    router.replace(id ? canvasHref({ svc: id, tab: parseTab(tab) }) : "/");
  }, [id, tab, router]);
  return <RedirectNotice />;
}

export default function LegacyAppDetail() {
  return (
    <Suspense fallback={<RedirectNotice />}>
      <Go />
    </Suspense>
  );
}
