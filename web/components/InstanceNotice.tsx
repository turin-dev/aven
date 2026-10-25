"use client";

import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { AlertTriangle, Eye, Info, Megaphone, Wrench } from "lucide-react";
import { api } from "@/lib/api";
import { qk, useMe } from "@/lib/queries";
import { dateLocale, t, tr } from "@/lib/i18n";
import { cx } from "./ui";

/** 서버 owner가 "사용자로 보기" 중일 때의 배너(읽기 전용, 기한, 끝내기). */
function ViewAsBanner() {
  const v = useMe().data;
  const [busy, setBusy] = useState(false);
  if (!v?.view_as) return null;
  const until = new Date(v.view_as.until).toLocaleTimeString(dateLocale(), { hour: "2-digit", minute: "2-digit" });
  return (
    <div role="status" className="flex items-center gap-2 border-b border-gray-200 bg-info-surface px-4 py-2 text-sm text-info">
      <Eye className="size-4 shrink-0" />
      <span className="min-w-0 flex-1">
        {tr("{name} 사용자로 보는 중 · 읽기 전용 · {until}까지", { name: <span className="font-semibold">{v.name || v.email}</span>, until })}
        <span className="ml-1 opacity-75">{t("({by}, 감사 기록에 남음)", { by: v.view_as.by })}</span>
      </span>
      <button
        type="button"
        disabled={busy}
        className="shrink-0 font-medium underline disabled:opacity-50"
        onClick={async () => {
          setBusy(true);
          try {
            await api.stopViewAs();
          } finally {
            window.location.href = "/admin/?section=users";
          }
        }}
      >
        {t("보기 끝내기")}
      </button>
    </div>
  );
}

/** 서버 owner가 켠 점검 모드와 전체 공지 배너(GET /api/instance/notice). 모든 로그인 사용자에게 보인다. */
export function InstanceNotice() {
  const q = useQuery({ queryKey: qk.instanceNotice, queryFn: api.instanceNotice, refetchInterval: 60_000, staleTime: 30_000, retry: false });
  const n = q.data;
  const a = n?.announcement;
  return (
    <div className="sticky top-0 z-20">
      <ViewAsBanner />
      {n?.maintenance.enabled && (
        <div role="status" className="flex items-center gap-2 border-b border-gray-200 bg-warn-surface px-4 py-2 text-sm text-warn">
          <Wrench className="size-4 shrink-0" />
          <span className="min-w-0 flex-1">
            <span className="font-semibold">{t("점검 중")}</span> · {n.maintenance.message}
            {n.exempt && <span className="ml-1 opacity-75">{t("(서버 owner는 계속 변경할 수 있습니다)")}</span>}
          </span>
          {n.exempt && <Link href="/admin/?section=owner" className="shrink-0 underline">{t("점검 설정")}</Link>}
        </div>
      )}
      {a && (
        <div role="status" className={cx(
          "flex items-center gap-2 border-b border-gray-200 px-4 py-2 text-sm",
          a.level === "critical" ? "bg-danger-surface text-danger" : a.level === "warning" ? "bg-warn-surface text-warn" : "bg-info-surface text-info",
        )}>
          {a.level === "critical" ? <AlertTriangle className="size-4 shrink-0" /> : a.level === "warning" ? <Megaphone className="size-4 shrink-0" /> : <Info className="size-4 shrink-0" />}
          <span className="min-w-0 flex-1 whitespace-pre-line">{a.message}</span>
        </div>
      )}
    </div>
  );
}
