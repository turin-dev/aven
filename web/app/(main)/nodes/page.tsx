"use client";

import { Suspense } from "react";
import { usePlacedCount } from "@/lib/queries";
import { useCtxHref } from "@/lib/nav";
import { useWorkspace } from "@/lib/workspace";
import { NodesView } from "@/components/nodes/NodesView";
import { PageBody, PageShell } from "@/components/ui";
import { t } from "@/lib/i18n";

function NodesInner() {
  const ws = useWorkspace();
  const href = useCtxHref();
  const placed = usePlacedCount(ws.envs.map((e) => e.id));
  return (
    <PageShell
      title={t("노드")}
      close={{ href: href("/"), label: t("노드 닫기") }}
    >
      <PageBody>
        <div className="max-w-[1000px]">
          <NodesView placedCount={placed} titled={false} />
        </div>
      </PageBody>
    </PageShell>
  );
}

export default function NodesPage() {
  return (
    <Suspense fallback={<div className="dot-grid min-h-full" />}>
      <NodesInner />
    </Suspense>
  );
}
