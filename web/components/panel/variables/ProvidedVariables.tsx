"use client";

import { useId, useState } from "react";
import { ChevronRight } from "lucide-react";
import type { ResolvedVar } from "@/lib/api";
import { t, tr } from "@/lib/i18n";
import { CopyButton, cx } from "../../ui";

export function ProvidedVariables({
  keys,
  resolved,
  pending,
}: {
  keys: string[];
  resolved: Record<string, ResolvedVar> | undefined;
  pending: boolean;
}) {
  const [open, setOpen] = useState(false);
  const id = useId();
  return (
    <div className="border-t border-neutral-light pt-6">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        aria-controls={id}
        data-state={open ? "open" : "closed"}
        className="group flex w-full cursor-pointer items-center gap-x-2 rounded-lg px-6 py-4 pl-0 pt-0 text-left text-neutral-secondary focus-visible:text-pink-500 focus-visible:outline-none"
      >
        <ChevronRight aria-hidden className={cx("size-4 shrink-0 transition-transform", open ? "rotate-90" : "rotate-0")} />
        <p className="text-sm">{t("aven이 추가한 변수 {count}개", { count: keys.length })}</p>
      </button>
      {open && (
        <div id={id} className="animate-fade-in pb-2">
          <p className="mb-2 text-xs leading-[18px] text-gray-500">
            {tr("배포할 때 자동으로 주입됩니다. 같은 이름으로 서비스 변수를 만들면 그 값이 우선합니다. 다른 서비스에서는 {ref}처럼 참조합니다.", {
              ref: <span className="font-mono text-gray-600">{t("${{서비스.AVEN_PRIVATE_DOMAIN}}")}</span>,
            })}
          </p>
          <ul className="space-y-1">
            {keys.map((k) => {
              const v = resolved?.[k]?.value;
              return (
                <li
                  key={k}
                  className="group grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)_auto] items-center gap-2 break-all rounded-md bg-gray-100 py-2 pl-4 pr-4 font-mono text-sm"
                >
                  <span className="truncate text-gray-600" title={k}>
                    {k}
                  </span>
                  <span className="min-w-0 break-all text-foreground/90">
                    {pending ? <span className="text-gray-400">…</span> : v ? v : <span className="font-sans text-xs text-gray-400">{t("이 환경에서는 비어 있음")}</span>}
                  </span>
                  {v ? (
                    <CopyButton value={v} label={t("{name} 값 복사", { name: k })} className="-my-1 size-6 opacity-0 focus-visible:opacity-100 group-hover:opacity-100 [@media(hover:none)]:opacity-100" />
                  ) : (
                    <span className="size-6" />
                  )}
                </li>
              );
            })}
          </ul>
        </div>
      )}
    </div>
  );
}
