"use client";

import { useMemo, useState } from "react";
import type { DotenvError } from "@/lib/dotenv";
import { t } from "@/lib/i18n";
import { Button, Dialog, cx } from "../../ui";
import { koParseError } from "./RawEditor";
import { KEY_RE } from "./VariableRow";

type Status = "new" | "overwrite" | "same" | "invalid";

const BADGE: Record<Status, { label: () => string; cls: string }> = {
  new: { label: () => t("새 변수"), cls: "text-success" },
  overwrite: { label: () => t("덮어씀"), cls: "text-pink-600" },
  same: { label: () => t("같은 값"), cls: "text-gray-500" },
  invalid: { label: () => t("쓸 수 없는 이름"), cls: "text-danger" },
};

/**
 * Review step for pasted .env text: every parsed entry with what adding it would do (new, overwrite, unchanged,
 * unusable name), a checkbox per entry, and the lines that couldn't be read. Values stay masked.
 * Duplicate keys in the paste: the last one wins, like the server's parser.
 */
export function BulkPasteDialog({
  pairs,
  errors,
  existing,
  onAdd,
  onClose,
}: {
  pairs: [string, string][];
  errors: DotenvError[];
  existing: Record<string, string>;
  onAdd: (pairs: [string, string][]) => void;
  onClose: () => void;
}) {
  const rows = useMemo(() => {
    const last = new Map(pairs);
    return [...last].map(([k, v]) => {
      const status: Status = !KEY_RE.test(k) ? "invalid" : !(k in existing) ? "new" : existing[k] === v ? "same" : "overwrite";
      return { k, v, status };
    });
  }, [pairs, existing]);
  const [on, setOn] = useState(() => new Set(rows.filter((r) => r.status === "new" || r.status === "overwrite").map((r) => r.k)));
  const picked = rows.filter((r) => on.has(r.k));
  const overwrites = picked.filter((r) => r.status === "overwrite").length;
  const toggle = (k: string) =>
    setOn((s) => {
      const n = new Set(s);
      if (n.has(k)) n.delete(k);
      else n.add(k);
      return n;
    });

  return (
    <Dialog
      open
      wide
      onClose={onClose}
      title={t("붙여넣은 변수 {count}개", { count: rows.length })}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("취소")}
          </Button>
          <Button variant="primary" disabled={!picked.length} onClick={() => onAdd(picked.map((r) => [r.k, r.v]))}>
            {overwrites ? t("{count}개 추가 ({n}개 덮어씀)", { count: picked.length, n: overwrites }) : t("{count}개 추가", { count: picked.length })}
          </Button>
        </>
      }
    >
      <p className="mb-3">{t("추가할 변수를 고르세요. 배포하기 전까지는 되돌릴 수 있습니다.")}</p>
      <ul className="divide-y divide-gray-100 rounded-md border border-gray-200" aria-label={t("붙여넣은 변수")}>
        {rows.map((r) => {
          const b = BADGE[r.status];
          const disabled = r.status === "invalid";
          return (
            <li key={r.k}>
              <label className={cx("flex items-center gap-3 px-3 py-2", disabled ? "cursor-not-allowed opacity-60" : "cursor-pointer hover:bg-secondaryBg")}>
                <input type="checkbox" className="size-4 shrink-0 accent-pink-600" checked={on.has(r.k)} disabled={disabled} onChange={() => toggle(r.k)} />
                <span className="min-w-0 flex-1 truncate font-mono text-foreground" title={r.k}>
                  {r.k}
                </span>
                <span className="hidden w-48 shrink-0 truncate font-mono text-gray-500 sm:inline" aria-hidden>
                  {r.v === "" ? t("<빈 문자열>") : /^\$\{\{[^}]*\}\}$/.test(r.v.trim()) ? r.v.trim() : "*******"}
                </span>
                <span className={cx("w-28 shrink-0 text-right text-xs font-medium", b.cls)}>{b.label()}</span>
              </label>
            </li>
          );
        })}
      </ul>
      {errors.length > 0 && (
        <div className="mt-3 rounded-md bg-warn-surface p-3 text-xs text-warn">
          <p className="font-semibold">{t("읽지 못한 줄 {count}개는 건너뜁니다", { count: errors.length })}</p>
          <ul className="mt-1 list-inside list-disc">
            {errors.slice(0, 5).map((e, i) => (
              <li key={i}>{koParseError(e.message)}</li>
            ))}
            {errors.length > 5 && <li>{t("외 {count}개", { count: errors.length - 5 })}</li>}
          </ul>
        </div>
      )}
    </Dialog>
  );
}
