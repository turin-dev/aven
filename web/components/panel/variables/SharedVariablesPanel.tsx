"use client";

import Link from "next/link";
import { Check, ChevronRight, Plus, X } from "lucide-react";
import { Button, IconButton, Spinner, cx } from "../../ui";
import { ROW_GRID } from "./VariableRow";
import { t } from "@/lib/i18n";

export const sharedRef = (k: string) => `\${{shared.${k}}}`;

/**
 * Shared-variables panel, modeled on Railway's dashboard (opened from the header):
 * `rounded-md bg-secondaryBg dark:bg-gray-100 my-4`, header "N of M shared variables" + Add All + close.
 * Each shared variable becomes `NAME=${{shared.NAME}}` in this service.
 */
export function SharedVariablesPanel({
  shared,
  pending,
  draft,
  envName,
  manageHref,
  canEdit,
  onAdd,
  onClose,
}: {
  shared: Record<string, string> | undefined;
  pending: boolean;
  /** This service's current (staged) variables. */
  draft: Record<string, string>;
  envName: string;
  manageHref: string;
  canEdit: boolean;
  /** Adds `${{shared.K}}` for each key. The parent asks before overwriting. */
  onAdd: (keys: string[]) => void;
  onClose: () => void;
}) {
  const keys = Object.keys(shared ?? {}).sort();
  const used = keys.filter((k) => draft[k] === sharedRef(k));
  // "모두 추가"는 같은 이름의 다른 값을 덮어쓰지 않는다. 겹치는 이름은 줄마다 따로 추가(덮어쓰기 확인)한다.
  const free = keys.filter((k) => !(k in draft));
  return (
    <section aria-label={t("공유 변수")} className="my-4 rounded-md bg-secondaryBg dark:bg-gray-100">
      <div className="mx-4 flex items-center justify-between gap-2 border-b border-gray-100 py-3 dark:border-gray-200">
        <p className="min-w-0 text-sm">
          {pending ? (
            t("공유 변수")
          ) : (
            <>
              <span className="font-medium tabular-nums">
                {t("공유 변수 {total}개 중 {used}개 사용 중", { total: keys.length, used: used.length })}
              </span>
              <span className="ml-2 hidden text-gray-500 sm:inline">{t("{env} 환경", { env: envName })}</span>
            </>
          )}
        </p>
        <div className="flex shrink-0 items-center gap-1">
          {free.length > 0 && (
            <Button variant="ghost" size="sm" disabled={!canEdit} onClick={() => onAdd(free)}>
              {t("모두 추가")}
            </Button>
          )}
          <IconButton size="sm" label={t("공유 변수 닫기")} onClick={onClose}>
            <X />
          </IconButton>
        </div>
      </div>
      {pending ? (
        <div className="flex justify-center py-6">
          <Spinner />
        </div>
      ) : keys.length === 0 ? (
        <p className="px-4 py-6 text-center text-sm text-gray-500">{t("이 환경에 공유 변수가 없습니다.")}</p>
      ) : (
        <ul className="max-h-[320px] overflow-y-auto px-2 py-2">
          {keys.map((k) => {
            const isUsed = draft[k] === sharedRef(k);
            const clash = !isUsed && k in draft;
            return (
              <li key={k} className={cx(ROW_GRID, "min-h-[2.25rem] items-center rounded px-2 font-mono text-sm hover:bg-gray-100 dark:hover:bg-gray-200")}>
                <span className="truncate" title={k}>
                  {k}
                </span>
                <span className="truncate font-sans text-xs text-gray-500">{clash ? t("같은 이름의 서비스 변수가 있습니다. 추가하면 덮어씁니다.") : ""}</span>
                <span className="flex justify-end">
                  {isUsed ? (
                    <span className="inline-flex h-[26px] items-center gap-1 px-1.5 font-sans text-xs text-gray-500">
                      <Check className="size-3.5 text-success" aria-hidden /> {t("추가됨")}
                    </span>
                  ) : (
                    <Button variant="ghost" size="sm" className="font-sans text-gray-600" disabled={!canEdit} onClick={() => onAdd([k])} aria-label={t("{name} 추가", { name: k })}>
                      <Plus className="text-gray-500" /> {t("추가")}
                    </Button>
                  )}
                </span>
              </li>
            );
          })}
        </ul>
      )}
      <div className="border-t border-gray-100 px-4 py-2 dark:border-gray-200">
        <Link href={manageHref} className="inline-flex items-center gap-1 rounded text-sm text-pink-600 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-500">
          {t("공유 변수 관리")} <ChevronRight className="size-3.5" aria-hidden />
        </Link>
      </div>
    </section>
  );
}
