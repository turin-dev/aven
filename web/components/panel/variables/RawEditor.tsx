"use client";

import { useEffect, useId, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Copy, X } from "lucide-react";
import { SEALED_VALUE } from "@/lib/api";
import { DotenvError, parseRaw, renderRaw } from "@/lib/dotenv";
import { toast } from "@/lib/uiState";
import { t } from "@/lib/i18n";
import { Button, cx, useEscape } from "../../ui";

/** Server/parser messages (English) → Korean UI copy. Unknown messages pass through. */
export function koParseError(msg: string): string {
  let m = /^line (\d+): (.*)$/.exec(msg);
  const at = m ? t("{n}번째 줄: ", { n: m[1] }) : "";
  const body = m ? m[2] : msg;
  if ((m = /^expected KEY=VALUE, got "(.*)"$/.exec(body))) return `${at}${t("KEY=VALUE 형식이 아닙니다 (\"{value}\")", { value: m[1] })}`;
  if ((m = /^invalid variable name "(.*)"$/.exec(body))) return `${at}${t("쓸 수 없는 변수 이름입니다 (\"{name}\")", { name: m[1] })}`;
  if ((m = /^unterminated (.) quoted value$/.exec(body))) return `${at}${t("{quote} 따옴표가 닫히지 않았습니다", { quote: m[1] })}`;
  if ((m = /^unexpected characters after closing quote of (.*)$/.exec(body))) return `${at}${t("{name}: 닫는 따옴표 뒤에 다른 글자가 있습니다", { name: m[1] })}`;
  if (body === "content must be a JSON object of strings") return `${at}${t("JSON은 {\"이름\": \"문자열 값\"} 형태의 객체여야 합니다")}`;
  return at + body;
}

export function CodeEditor({
  value,
  onChange,
  placeholder,
  ariaLabel,
  invalid,
  describedBy,
  errorLine,
  onSubmit,
}: {
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  ariaLabel: string;
  invalid?: boolean;
  describedBy?: string;
  /** 1-based line to mark in the gutter. */
  errorLine?: number;
  /** ⌘/Ctrl+Enter. */
  onSubmit?: () => void;
}) {
  const gutter = useRef<HTMLDivElement>(null);
  const lines = Math.max(1, value.split("\n").length);
  return (
    <div className="flex h-full min-h-0 w-full overflow-hidden text-base sm:text-[13px]">
      <div
        ref={gutter}
        aria-hidden="true"
        className="min-w-[2.75rem] select-none overflow-hidden border-r border-gray-100 bg-secondaryBg py-2 pl-2 pr-2 text-right font-mono leading-[1.5] text-gray-400 dark:bg-gray-50"
      >
        {Array.from({ length: lines }, (_, i) => (
          <div key={i} className={cx(errorLine === i + 1 && "font-semibold text-danger")}>
            {i + 1}
          </div>
        ))}
        <div className="h-4" />
      </div>
      <textarea
        autoFocus
        spellCheck={false}
        autoCapitalize="off"
        autoCorrect="off"
        wrap="off"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && onSubmit) {
            e.preventDefault();
            onSubmit();
          }
        }}
        onScroll={(e) => {
          if (gutter.current) gutter.current.scrollTop = e.currentTarget.scrollTop;
        }}
        placeholder={placeholder}
        aria-label={ariaLabel}
        aria-invalid={invalid}
        aria-describedby={describedBy}
        className="h-full w-full min-w-0 flex-1 resize-none overflow-auto bg-transparent px-3 py-2 font-mono leading-[1.5] text-foreground placeholder-gray-500 focus:outline-none"
      />
    </div>
  );
}
type Diff = { added: number; changed: number; removed: number };
function diffOf(prev: Record<string, string>, next: Record<string, string>, keep: Set<string>): Diff {
  let added = 0;
  let changed = 0;
  let removed = 0;
  for (const [k, v] of Object.entries(next)) {
    if (!(k in prev)) added++;
    else if (prev[k] !== v && !(keep.has(k) && v === SEALED_VALUE)) changed++;
  }
  for (const k of Object.keys(prev)) if (!(k in next) && !keep.has(k)) removed++;
  return { added, changed, removed };
}

/**
 * RAW editor, modeled on Railway's dashboard. Two modes:
 * - text mode (initial/onApply): free .env text, the caller parses it (shared variables page).
 * - structured mode (vars/onApplyVars): .env ↔ JSON toggle, server-compatible parsing checked while typing with
 *   line-numbered errors and a change summary; sealed variables shown only as comments and kept on apply.
 */
export function RawEditor({
  initial = "",
  onClose,
  onApply,
  vars,
  sealed = [],
  onApplyVars,
  validate,
  title = t("RAW 편집"),
}: {
  initial?: string;
  onClose: () => void;
  onApply?: (text: string) => void;
  vars?: Record<string, string>;
  sealed?: string[];
  onApplyVars?: (next: Record<string, string>) => void;
  /** Extra check on the parsed result (structured mode). Return a message to block applying. */
  validate?: (next: Record<string, string>) => string | null;
  title?: string;
}) {
  const structured = !!vars && !!onApplyVars;
  const [format, setFormat] = useState<"env" | "json">("env");
  const [text, setText] = useState(() => (structured ? renderRaw("env", vars!, sealed) : initial));
  const [applyErr, setApplyErr] = useState<string | null>(null);
  const uid = useId();
  const titleId = `${uid}-title`;
  const errId = `${uid}-err`;
  const sumId = `${uid}-sum`;
  const keep = useMemo(() => new Set(sealed), [sealed]);
  useEscape(onClose);

  // Parse while typing (structured mode only; text mode is parsed by the caller).
  const parsed = useMemo((): { ok: true; next: Record<string, string> } | { ok: false; msg: string; line?: number } | null => {
    if (!structured) return null;
    try {
      const next = parseRaw(format, text);
      const v = validate?.(next);
      if (v) return { ok: false, msg: v };
      return { ok: true, next };
    } catch (e) {
      return { ok: false, msg: koParseError((e as Error).message), line: e instanceof DotenvError ? e.line : undefined };
    }
  }, [structured, format, text, validate]);
  const diff = parsed?.ok && vars ? diffOf(vars, parsed.next, keep) : null;
  const nChanges = diff ? diff.added + diff.changed + diff.removed : 0;

  // Show parse errors only after the user pauses typing, so half-typed lines don't flash red.
  const [settled, setSettled] = useState(true);
  useEffect(() => {
    setSettled(false);
    const timer = setTimeout(() => setSettled(true), 450);
    return () => clearTimeout(timer);
  }, [text]);
  const liveErr = parsed && !parsed.ok && settled ? parsed : null;
  const err = applyErr ?? liveErr?.msg ?? null;

  const switchFormat = (f: "env" | "json") => {
    if (f === format) return;
    try {
      const cur = parseRaw(format, text);
      // 편집기에 없는 봉인 변수는 형식을 바꿔도 주석으로만 다시 보여 준다.
      const hidden = sealed.filter((k) => !(k in cur));
      setText(renderRaw(f, { ...Object.fromEntries(hidden.map((k) => [k, SEALED_VALUE])), ...cur }, hidden));
      setFormat(f);
      setApplyErr(null);
    } catch (e) {
      setApplyErr(t("형식을 바꾸기 전에 오류를 고치세요. {error}", { error: koParseError((e as Error).message) }));
    }
  };
  const apply = () => {
    if (!structured) return onApply?.(text);
    if (!parsed) return;
    if (!parsed.ok) {
      setSettled(true);
      setApplyErr(parsed.msg);
      return;
    }
    try {
      onApplyVars!(parsed.next);
    } catch (e) {
      setApplyErr(koParseError((e as Error).message));
    }
  };
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      toast(format === "json" ? t("JSON을 복사했습니다") : t("ENV를 복사했습니다"));
    } catch {
      toast(t("클립보드에 복사하지 못했습니다"), "error");
    }
  };

  return createPortal(
    <div
      className="pointer-events-auto fixed inset-0 z-[10000] flex animate-fade-in items-end justify-stretch bg-black/[.45] dark:bg-black/80 sm:items-center sm:justify-center sm:px-5"
      onMouseDown={(e) => e.target === e.currentTarget && onClose()}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className="relative flex max-h-[100dvh] w-full max-w-full animate-pop-in flex-col overflow-hidden rounded-t-lg bg-background p-6 focus:outline-none dark:bg-secondaryBg sm:max-h-[calc(100dvh-40px)] sm:w-[639px] sm:rounded-lg lg:w-[759px]"
      >
        <button
          type="button"
          onClick={onClose}
          aria-label={t("닫기")}
          title={t("닫기 (Esc)")}
          className="absolute right-6 top-6 z-10 flex size-[26px] cursor-pointer items-center justify-center rounded-md text-foreground hover:bg-gray-100"
        >
          <X className="size-4" />
        </button>
        <h1 id={titleId} className="flex pb-2 pr-10 text-left text-xl font-semibold text-foreground">
          {title}
        </h1>
        <p className="pb-4 text-sm leading-5 text-gray-600">
          {format === "json"
            ? t("JSON 객체(이름 → 문자열 값)로 붙여넣거나 고치세요. 여기서 지운 변수는 삭제됩니다.")
            : t(".env 형식으로 붙여넣거나 고치세요. 여기서 지운 변수는 삭제됩니다.")}
          {structured && sealed.length > 0 && ` ${t("봉인된 변수는 주석으로만 보이고 그대로 남습니다.")}`}
        </p>
        <form
          name="Raw Editor"
          className="flex min-h-0 flex-1 flex-col"
          onSubmit={(e) => {
            e.preventDefault();
            apply();
          }}
        >
          <div
            className={cx(
              "grid h-[min(60dvh,480px)] min-h-[240px] grid-rows-[auto_minmax(0px,_1fr)] overflow-hidden rounded-md border",
              err ? "border-red-500" : "border-gray-200",
            )}
          >
            {structured ? (
              <div role="tablist" aria-label={t("RAW 형식")} className="grid grid-cols-2">
                {(["env", "json"] as const).map((f) => (
                  <button
                    key={f}
                    type="button"
                    role="tab"
                    aria-selected={format === f}
                    onClick={() => switchFormat(f)}
                    className={cx(
                      "h-10 cursor-pointer px-3 py-2 text-center text-sm leading-5 hover:text-foreground focus:outline-none focus-visible:bg-gray-100",
                      format === f ? "border-b-2 border-foreground font-semibold text-foreground" : "border-b border-gray-200 text-gray-500",
                    )}
                  >
                    {f === "env" ? "ENV" : "JSON"}
                  </button>
                ))}
              </div>
            ) : (
              <div className="flex h-10 items-center border-b border-gray-200 px-3 text-xs font-medium text-gray-500">.env</div>
            )}
            <CodeEditor
              value={text}
              onChange={(v) => {
                setText(v);
                setApplyErr(null);
              }}
              onSubmit={apply}
              placeholder={format === "json" ? '{\n  "PORT": "3000"\n}' : "DATABASE_URL=${{postgres.DATABASE_URL}}\nPORT=3000\n# " + t("주석 줄은 무시됩니다")}
              ariaLabel={format === "json" ? t("JSON 형식으로 변수 편집") : t(".env 형식으로 변수 편집")}
              invalid={!!err}
              describedBy={err ? errId : structured ? sumId : undefined}
              errorLine={applyErr ? undefined : liveErr?.line}
            />
          </div>
          <div className="mt-2 min-h-[18px] text-xs leading-[18px]">
            {err ? (
              <p id={errId} role="alert" className="text-red-600">
                {err}
              </p>
            ) : diff ? (
              <p id={sumId} aria-live="polite" className="text-gray-500">
                {nChanges === 0 ? (
                  t("바뀐 내용이 없습니다")
                ) : (
                  <>
                    {[
                      diff.added && <span key="a" className="text-success">{t("추가 {n}", { n: diff.added })}</span>,
                      diff.changed && <span key="c" className="text-pink-600">{t("변경 {n}", { n: diff.changed })}</span>,
                      diff.removed && <span key="r" className="text-danger">{t("삭제 {n}", { n: diff.removed })}</span>,
                    ]
                      .filter(Boolean)
                      .flatMap((n, i) => (i ? [<span key={`s${i}`}> · </span>, n] : [n]))}
                    <span> · {t("적용하면 배포 대기 변경으로 들어갑니다")}</span>
                  </>
                )}
              </p>
            ) : null}
          </div>
          <div className="mt-4 flex flex-wrap items-center gap-x-4 gap-y-2">
            <Button type="button" variant="ghost" className="mr-auto -ml-3 text-gray-600" onClick={copy}>
              <Copy className="text-gray-500" /> {format === "json" ? t("JSON 복사") : t("ENV 복사")}
            </Button>
            <Button type="button" variant="outline" onClick={onClose}>
              {t("취소")}
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={structured && (!parsed?.ok || nChanges === 0)}
              title={structured ? "⌘/Ctrl + Enter" : undefined}
            >
              {t("변수 업데이트")}
            </Button>
          </div>
        </form>
      </div>
    </div>,
    document.body,
  );
}
