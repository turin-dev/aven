"use client";

import { Crown, ShieldCheck } from "lucide-react";
import { haMemberOf, useEnvHA } from "@/lib/ha";
import { cx } from "./ui";
import { t } from "@/lib/i18n";

export function HABadge({ svcId, envId }: { svcId: string; envId: string }) {
  const clusters = useEnvHA(envId);
  const found = haMemberOf(clusters.data, svcId);
  if (!found) return null;
  const { cluster, member } = found;
  const primary = member.db_role === "primary";
  const label =
    member.role === "edge" ? t("HA 프록시") : member.role === "internal" ? t("HA 조정") : primary ? t("주 노드") : member.db_role === "replica" ? "replica" : "HA";
  const title = `${t("{kind} HA 클러스터", { kind: cluster.kind })} · ${label}${member.healthy ? "" : ` · ${t("비정상")}`}${member.lag_bytes ? ` · ${t("지연 {lag} B", { lag: member.lag_bytes })}` : ""}`;
  return (
    <span
      title={title}
      className={cx(
        "flex items-center gap-1 rounded px-1.5 py-0.5 text-xs font-medium",
        primary ? "bg-pink-100 text-pink-600" : "bg-gray-100 text-gray-600",
        !member.healthy && "ring-1 ring-red-300",
      )}
    >
      {primary ? <Crown className="size-3" /> : <ShieldCheck className="size-3" />}
      {label}
    </span>
  );
}
