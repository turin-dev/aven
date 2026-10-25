"use client";

import { Suspense, useCallback } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { useCtxHref } from "@/lib/nav";
import { useWorkspace } from "@/lib/workspace";
import { ErrorBox, PageBody, PageShell, Spinner } from "@/components/ui";
import { SandboxesView } from "@/components/sandboxes/SandboxesView";
import { t } from "@/lib/i18n";

function SandboxesInner() {
  const ws = useWorkspace();
  const href = useCtxHref();
  const router = useRouter();
  const selected = useSearchParams().get("sbx");
  const select = useCallback((id: string | null) => router.replace(href("/sandboxes/", { sbx: id }), { scroll: false }), [router, href]);
  return (
    <PageShell
      title={t("샌드박스")}
      close={{ href: href("/"), label: t("샌드박스 닫기") }}
    >
      <PageBody>
          <ErrorBox error={ws.error} />
          {ws.isPending ? (
            <div className="flex justify-center py-16">
              <Spinner />
            </div>
          ) : ws.env ? (
            <SandboxesView pid={ws.pid} eid={ws.env.id} envName={ws.env.name} selected={selected} onSelect={select} />
          ) : (
            <p className="text-sm text-gray-500">{t("프로젝트를 먼저 만드세요.")}</p>
          )}
      </PageBody>
    </PageShell>
  );
}

export default function SandboxesPage() {
  return (
    <Suspense fallback={<div className="dot-grid min-h-full" />}>
      <SandboxesInner />
    </Suspense>
  );
}
