"use client";

import { AlertTriangle, CheckCircle2, Info, X, XCircle } from "lucide-react";
import { dismissToast, useToasts } from "@/lib/uiState";
import { t } from "@/lib/i18n";
import { cx } from "./ui";

const SURFACE = {
  info: "border-gray-200",
  success: "border-green-300",
  warning: "border-yellow-300",
  error: "border-red-300",
} as const;
const ICON = { info: Info, success: CheckCircle2, warning: AlertTriangle, error: XCircle } as const;
const ICON_TONE = { info: "text-gray-500", success: "text-green-600", warning: "text-yellow-600", error: "text-red-600" } as const;

/**
 * `shifted`: a floating side panel (Activity, 320px) is open, so the stack moves to 320+24px from the right,
 * sliding with a `transition: right .2s ease-out` timing, modeled on Railway's dashboard.
 *
 * The live region is always mounted: screen readers skip announcements from a region that is inserted
 * together with its first message. Errors use role="alert" (assertive); the rest are read politely.
 */
export function Toaster({ shifted = false }: { shifted?: boolean }) {
  const list = useToasts();
  return (
    <div
      aria-live="polite"
      aria-relevant="additions"
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
              role={tone === "error" ? "alert" : undefined}
              className={cx(
                "pointer-events-auto relative max-w-[320px] animate-toast-in rounded-lg border bg-background p-4 shadow-toast dark:bg-gray-100 md:w-80",
                SURFACE[tone],
              )}
            >
              <div className="flex items-start gap-3">
                <Icon aria-hidden="true" className={cx("mt-1 size-4 shrink-0", ICON_TONE[tone])} />
                <div className="flex w-full min-w-0 items-start justify-between gap-2">
                  <p className="min-w-0 break-words text-sm font-medium leading-6">{item.msg}</p>
                  <button
                    type="button"
                    title={t("닫기")}
                    aria-label={t("닫기")}
                    onClick={() => dismissToast(item.id)}
                    className="mt-0.5 flex size-5 shrink-0 cursor-pointer items-center justify-center rounded-md border border-transparent text-gray-500 opacity-60 transition-opacity hover:bg-gray-200 hover:opacity-100 focus:outline-none focus-visible:opacity-100 focus-visible:ring-2 focus-visible:ring-pink-500"
                  >
                    <X aria-hidden="true" className="size-4" />
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
