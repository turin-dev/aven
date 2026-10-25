"use client";

import { AlertTriangle, CheckCircle2, Info, X } from "lucide-react";
import { dismissToast, useToasts } from "@/lib/uiState";
import { t } from "@/lib/i18n";
import { cx } from "./ui";

const SURFACE = {
  info: "border-gray-200",
  success: "border-green-200",
  warning: "border-yellow-200",
  error: "border-red-200",
} as const;
const ICON = { info: Info, success: CheckCircle2, warning: AlertTriangle, error: AlertTriangle } as const;
const ICON_TONE = { info: "text-gray-500", success: "text-green-500", warning: "text-yellow-500", error: "text-red-500" } as const;

/**
 * `shifted`: a floating side panel (Activity, 320px) is open, so the stack moves to 320+24px from the right,
 * sliding with a `transition: right .2s ease-out` timing, modeled on Railway's dashboard.
 */
export function Toaster({ shifted = false }: { shifted?: boolean }) {
  const list = useToasts();
  if (list.length === 0) return null;
  return (
    <div
      aria-live="polite"
      className={cx(
        "pointer-events-none fixed bottom-14 left-6 z-[100000] md:left-auto md:transition-[right] md:duration-200 md:ease-out motion-reduce:transition-none",
        shifted ? "md:right-[344px]" : "md:right-6",
      )}
    >
      <div className="flex flex-col space-y-2">
        {list.map((item) => {
          const tone = (item.tone in SURFACE ? item.tone : "info") as keyof typeof SURFACE;
          const Icon = ICON[tone];
          return (
            <div
              key={item.id}
              role={tone === "error" ? "alert" : "status"}
              className={cx(
                "pointer-events-auto relative max-w-[320px] animate-pop-in rounded-lg border bg-background p-4 dark:bg-gray-100 shadow-[0px_2px_12px_rgba(0,0,0,0.04)] md:w-80",
                SURFACE[tone],
              )}
            >
              <div className="flex items-start space-x-4">
                <Icon className={cx("mt-0.5 size-4 shrink-0", ICON_TONE[tone])} />
                <div className="flex w-full items-center justify-between">
                  <p className="text-base font-medium">{item.msg}</p>
                  <button
                    type="button"
                    title={t("닫기")}
                    aria-label={t("닫기")}
                    onClick={() => dismissToast(item.id)}
                    className="flex !h-5 !w-5 shrink-0 cursor-pointer items-center justify-center rounded-md border border-transparent text-gray-500 opacity-40 hover:bg-gray-100 hover:opacity-100"
                  >
                    <X className="size-4" />
                  </button>
                </div>
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}
