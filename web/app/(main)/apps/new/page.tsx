"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";
import { RedirectNotice } from "@/components/Redirect";

// Legacy route: /apps/new/  ->  /?new=1
export default function LegacyNewApp() {
  const router = useRouter();
  useEffect(() => {
    router.replace("/?new=1");
  }, [router]);
  return <RedirectNotice />;
}
