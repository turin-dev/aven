"use client";

import Link from "next/link";
import { t } from "@/lib/i18n";
import { Spinner } from "./ui";

export function RedirectNotice({ error }: { error?: string | null }) {
  return (
    <div className="dot-grid flex min-h-full flex-col items-center justify-center gap-3 px-6 text-center">
      {error ? (
        <>
          <div className="text-base font-semibold">{t("페이지를 열 수 없습니다")}</div>
          <div className="max-w-sm text-sm text-gray-600">{error}</div>
          <Link href="/" className="text-sm text-pink-600 hover:underline">
            {t("프로젝트로 돌아가기")}
          </Link>
        </>
      ) : (
        <Spinner />
      )}
    </div>
  );
}
