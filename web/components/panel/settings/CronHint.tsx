"use client";

import { cronError, describeCron, nextRuns } from "@/lib/cron";
import { fmtTime } from "@/lib/format";
import { t } from "@/lib/i18n";
import { cx } from "../../ui";

/**
 * One line under a cron input: the parse error, or the human-readable schedule plus the next runs in local time.
 * `empty` is shown when the input is blank (for example "automatic backups are off").
 */
export function CronHint({ expr, empty, count = 3, className, id }: { expr: string; empty?: string; count?: number; className?: string; id?: string }) {
  const e = expr.trim();
  const err = e ? cronError(e) : null;
  const runs = e && !err ? nextRuns(e, count) : [];
  return (
    <div id={id} aria-live="polite" className={cx("text-xs leading-[18px]", err ? "text-red-600" : "text-gray-600", className)}>
      {!e ? (
        empty ?? null
      ) : err ? (
        err
      ) : (
        <>
          <span className="font-medium text-foreground">{describeCron(e)}</span>
          {runs.length > 0 ? (
            <span className="tabular-nums"> · {t("다음 실행: {times} (현지 시각)", { times: runs.map((d) => fmtTime(d.toISOString())).join(", ") })}</span>
          ) : (
            <span> · {t("5년 안에 실행될 날짜가 없습니다")}</span>
          )}
        </>
      )}
    </div>
  );
}
