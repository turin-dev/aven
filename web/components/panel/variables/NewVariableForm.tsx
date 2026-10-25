"use client";

import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { Check, Link2 } from "lucide-react";
import { parseDotenv } from "@/lib/format";
import { t, tr } from "@/lib/i18n";
import { Button, cx, useEscape } from "../../ui";
import { REF_BUTTON_CLS, REF_POPUP_CLS, VAR_INPUT_CLS, VarInput, type Suggestion } from "../VarInput";
import { KEY_RE, KEY_RULE } from "./VariableRow";

export type RefOption = { name: string; value: string; hint: string; icon?: ReactNode; exists?: boolean };

const MIN_QUERY = 5;

/**
 * Inline "New variable" form, modeled on Railway's dashboard: `grid auto-rows-auto md:grid-cols-[1fr_1fr_auto] gap-2`.
 * Name field: Autocomplete with the "Add Reference" button inside (picks NAME + `${{svc.NAME}}` in one go).
 * Value field: `${{` autocomplete. Add (primary, check icon) / Cancel (gray). Enter submits, Esc cancels.
 * Pasting `KEY=value` into the name field splits it; pasting several .env lines adds them all.
 */
export function NewVariableForm({
  suggestions,
  refOptions,
  onAdd,
  onBulk,
  onCancel,
}: {
  suggestions: Suggestion[];
  refOptions: RefOption[];
  /** Adds one variable. The parent asks before overwriting an existing name. */
  onAdd: (key: string, value: string) => void;
  onBulk: (pairs: [string, string][]) => void;
  onCancel: () => void;
}) {
  const [k, setK] = useState("");
  const [v, setV] = useState("");
  const [open, setOpen] = useState(false);
  const [forced, setForced] = useState(false);
  const [active, setActive] = useState(0);
  const nameRef = useRef<HTMLInputElement>(null);
  const valRef = useRef<HTMLInputElement>(null);
  const uid = useId();
  const key = k.trim();
  const keyErr = key === "" ? null : !KEY_RE.test(key) ? KEY_RULE : null;

  const list = useMemo(() => {
    const q = key.toLowerCase();
    if (!forced && q.length < MIN_QUERY) return [];
    return q ? refOptions.filter((o) => o.name.toLowerCase().includes(q) || o.hint.toLowerCase().includes(q)) : refOptions;
  }, [key, forced, refOptions]);
  const showList = open && (forced || list.length > 0);
  const idx = Math.max(0, Math.min(active, list.length - 1));
  useEscape(() => (showList ? setOpen(false) : onCancel()));
  useEffect(() => {
    if (showList) document.getElementById(`${uid}-opt-${idx}`)?.scrollIntoView({ block: "nearest" });
  }, [showList, idx, uid]);

  const pick = (o: RefOption) => {
    setK(o.name);
    setV(o.value);
    setOpen(false);
    setForced(false);
    requestAnimationFrame(() => valRef.current?.focus());
  };
  const submit = () => {
    if (!key || keyErr) return;
    onAdd(key, v);
  };

  return (
    <form
      name="Add Variable"
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <div className="grid auto-rows-auto gap-2 md:grid-cols-[1fr_1fr_auto]">
        <div className="relative w-full md:w-auto lg:flex-grow">
          <input
            ref={nameRef}
            autoFocus
            spellCheck={false}
            autoComplete="off"
            value={k}
            role="combobox"
            aria-expanded={showList}
            aria-haspopup="listbox"
            aria-controls={`${uid}-list`}
            aria-autocomplete="list"
            aria-activedescendant={showList && list.length ? `${uid}-opt-${idx}` : undefined}
            onChange={(e) => {
              setK(e.target.value);
              setActive(0);
              setOpen(true);
            }}
            onBlur={() => setTimeout(() => (setOpen(false), setForced(false)), 120)}
            onKeyDown={(e) => {
              if (e.nativeEvent.isComposing || e.keyCode === 229) return;
              if (showList && list.length) {
                if (e.key === "ArrowDown" || e.key === "ArrowUp") {
                  e.preventDefault();
                  setActive((a) => Math.max(0, Math.min(list.length - 1, a + (e.key === "ArrowDown" ? 1 : -1))));
                  return;
                }
                if (e.key === "Enter" || e.key === "Tab") {
                  e.preventDefault();
                  pick(list[idx]);
                  return;
                }
              }
              if (e.key === "Enter" && key && !keyErr && v === "") {
                e.preventDefault();
                valRef.current?.focus();
              }
            }}
            placeholder="VARIABLE_NAME"
            aria-label={t("변수 이름")}
            aria-invalid={!!keyErr}
            aria-describedby={keyErr ? `${uid}-err` : undefined}
            className={cx(VAR_INPUT_CLS, "!pr-7", keyErr && "!border-red-500")}
            onPaste={(e) => {
              const text = e.clipboardData.getData("text");
              if (!text.includes("=")) return;
              const pairs = parseDotenv(text);
              if (!pairs.length) return;
              e.preventDefault();
              if (pairs.length === 1 && !text.trim().includes("\n")) {
                setK(pairs[0][0]);
                setV(pairs[0][1]);
                requestAnimationFrame(() => valRef.current?.focus());
                return;
              }
              onBulk(pairs);
            }}
          />
          <button
            type="button"
            tabIndex={-1}
            aria-hidden
            title={t("참조 추가")}
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => {
              const next = !(open && forced);
              setForced(next);
              setOpen(next);
              setActive(0);
              nameRef.current?.focus();
            }}
            className={REF_BUTTON_CLS}
          >
            <Link2 />
            <span className={cx("overflow-hidden whitespace-nowrap transition-all", k ? "ml-0 w-0 opacity-0" : "ml-[0.2rem]")}>{t("참조 추가")}</span>
          </button>
          {showList && (
            <div id={`${uid}-list`} role="listbox" aria-label={t("참조할 변수")} className={REF_POPUP_CLS}>
              {list.length === 0 ? (
                <div className="px-2 py-1.5 text-sm italic text-gray-500">{refOptions.length ? t("일치하는 변수가 없습니다") : t("참조할 수 있는 변수가 없습니다")}</div>
              ) : (
                list.map((o, i) => (
                  <div
                    key={o.value}
                    id={`${uid}-opt-${i}`}
                    role="option"
                    aria-selected={i === idx}
                    aria-disabled={o.exists || undefined}
                    onMouseDown={(e) => {
                      e.preventDefault();
                      if (!o.exists) pick(o);
                    }}
                    onMouseMove={() => setActive(i)}
                    className={cx(
                      "autocomplete-item flex min-h-8 cursor-pointer items-center rounded px-2 py-1.5 text-sm",
                      i === idx && "bg-gray-100 dark:bg-gray-200",
                      o.exists && "cursor-default opacity-50",
                    )}
                  >
                    {o.icon && <span className="mr-2 flex shrink-0 text-gray-500 [&_svg]:size-4">{o.icon}</span>}
                    <span className="min-w-0 truncate font-mono text-gray-600" title={o.value}>
                      {o.name}
                    </span>
                    <span className="ml-auto shrink-0 truncate pl-8 text-gray-500">{o.exists ? t("추가됨") : o.hint}</span>
                  </div>
                ))
              )}
            </div>
          )}
        </div>
        <div className="w-full">
          <VarInput ref={valRef} refButton value={v} onChange={setV} suggestions={suggestions} placeholder="VALUE or ${{REF}}" ariaLabel={t("변수 값")} onEnter={submit} />
          <p className="mt-2 hidden text-xs leading-[18px] text-gray-600 md:block">
            {tr("{ref}를 입력하면 참조할 변수를 고를 수 있습니다", { ref: <span className="font-mono">{"${{"}</span> })}
          </p>
        </div>
        <div className="flex space-x-2">
          <Button type="submit" variant="primary" disabled={!key || !!keyErr} className="w-full md:max-w-max">
            <Check /> {t("추가")}
          </Button>
          <Button type="button" variant="default" onClick={onCancel} className="w-full md:max-w-max">
            {t("취소")}
          </Button>
        </div>
      </div>
      {keyErr && (
        <p id={`${uid}-err`} role="alert" className="mt-2 flex items-center text-xs leading-[18px] text-red-600">
          {keyErr}
        </p>
      )}
    </form>
  );
}
