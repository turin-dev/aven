"use client";

import { useWorkspace } from "@/lib/workspace";
import { Fragment, useEffect, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import {
  ApiError,
  api,
  type Environment,
  type ID,
  type TemplateDefinition,
  type TemplateInput,
  type TemplateInputProblems,
  type TemplateVisibility,
} from "@/lib/api";
import { qk } from "@/lib/queries";
import { toast } from "@/lib/uiState";
import { Button, Dialog, ErrorBox, Field, Input, LABEL_CLS, Segmented, Textarea, cx } from "./ui";
import { t, tr } from "@/lib/i18n";

/* ── Markdown-lite (no HTML, no dependencies) ───────────────────────── */

/** Inline: `code`, **bold**, *italic*, [text](https://url). Everything else is plain text. */
function inline(text: string, key: string): ReactNode[] {
  const out: ReactNode[] = [];
  const re = /(`[^`]+`)|(\*\*[^*]+\*\*)|(\*[^*\s][^*]*\*)|(\[[^\]]+\]\((https?:\/\/[^)\s]+)\))/g;
  let last = 0;
  let m: RegExpExecArray | null;
  let i = 0;
  while ((m = re.exec(text))) {
    if (m.index > last) out.push(text.slice(last, m.index));
    const k = `${key}-${i++}`;
    if (m[1]) out.push(<code key={k} className="rounded bg-gray-100 px-1 py-px font-mono text-xs text-foreground">{m[1].slice(1, -1)}</code>);
    else if (m[2]) out.push(<strong key={k} className="font-semibold text-foreground">{m[2].slice(2, -2)}</strong>);
    else if (m[3]) out.push(<em key={k}>{m[3].slice(1, -1)}</em>);
    else if (m[4]) {
      const label = m[4].slice(1, m[4].indexOf("]"));
      out.push(
        <a key={k} href={m[5]} target="_blank" rel="noopener noreferrer nofollow" className="text-pink-600 hover:underline">
          {label}
        </a>,
      );
    }
    last = m.index + m[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

/** Renders a small subset of Markdown as React elements: headings, paragraphs, lists, fenced code, inline marks. */
export function MarkdownLite({ text, className }: { text: string; className?: string }) {
  const lines = text.replace(/\r\n/g, "\n").split("\n");
  const blocks: ReactNode[] = [];
  let para: string[] = [];
  let list: { ordered: boolean; items: string[] } | null = null;
  let code: string[] | null = null;
  const flushPara = () => {
    if (para.length) {
      const k = `p${blocks.length}`;
      blocks.push(
        <p key={k} className="leading-relaxed">
          {inline(para.join(" "), k)}
        </p>,
      );
      para = [];
    }
  };
  const flushList = () => {
    if (list) {
      const k = `l${blocks.length}`;
      const items = list.items.map((it, j) => <li key={j}>{inline(it, `${k}-${j}`)}</li>);
      blocks.push(
        list.ordered ? (
          <ol key={k} className="list-decimal space-y-0.5 pl-5">
            {items}
          </ol>
        ) : (
          <ul key={k} className="list-disc space-y-0.5 pl-5">
            {items}
          </ul>
        ),
      );
      list = null;
    }
  };
  for (const raw of lines) {
    if (code) {
      if (raw.trim().startsWith("```")) {
        blocks.push(
          <pre key={`c${blocks.length}`} className="overflow-x-auto rounded-md border border-gray-200 bg-gray-50 p-3 font-mono text-xs text-foreground">
            {code.join("\n")}
          </pre>,
        );
        code = null;
      } else code.push(raw);
      continue;
    }
    const line = raw.trimEnd();
    if (line.trim().startsWith("```")) {
      flushPara();
      flushList();
      code = [];
      continue;
    }
    const h = /^(#{1,4})\s+(.*)$/.exec(line);
    if (h) {
      flushPara();
      flushList();
      const k = `h${blocks.length}`;
      const cls = h[1].length === 1 ? "text-lg font-semibold text-foreground" : h[1].length === 2 ? "text-base font-semibold text-foreground" : "text-sm font-semibold text-foreground";
      blocks.push(
        <div key={k} role="heading" aria-level={Math.min(6, h[1].length + 2)} className={cls}>
          {inline(h[2], k)}
        </div>,
      );
      continue;
    }
    const li = /^\s*([-*]|\d+\.)\s+(.*)$/.exec(line);
    if (li) {
      flushPara();
      const ordered = /\d/.test(li[1]);
      if (!list || list.ordered !== ordered) {
        flushList();
        list = { ordered, items: [] };
      }
      list.items.push(li[2]);
      continue;
    }
    if (!line.trim()) {
      flushPara();
      flushList();
      continue;
    }
    flushList();
    para.push(line.trim());
  }
  if (code) blocks.push(<pre key="c-end" className="overflow-x-auto rounded-md border border-gray-200 bg-gray-50 p-3 font-mono text-xs">{(code as string[]).join("\n")}</pre>);
  flushPara();
  flushList();
  return <div className={cx("space-y-2.5 text-sm text-gray-600", className)}>{blocks.map((b, i) => <Fragment key={i}>{b}</Fragment>)}</div>;
}

/* ── Inputs ─────────────────────────────────────────────────────────── */

/** Client-side check mirroring the server (required, full-match pattern). Unknown regex syntax is left to the server. */
export function validateInputs(inputs: TemplateInput[], values: Record<string, string>): Record<string, string> {
  const errs: Record<string, string> = {};
  for (const inp of inputs) {
    const v = values[inp.key] ?? "";
    const effective = v || inp.default || "";
    if (inp.required && !effective.trim()) {
      errs[inp.key] = t("필수 입력입니다");
      continue;
    }
    if (inp.pattern && v) {
      try {
        if (!new RegExp(`^(?:${inp.pattern})$`).test(v)) errs[inp.key] = t("형식이 맞지 않습니다 ({pattern})", { pattern: inp.pattern });
      } catch {
        /* the server validates it */
      }
    }
  }
  return errs;
}

/** Maps a 400 from a template deploy to per-input messages (null if it is not an input error). */
export function inputErrorsFrom(e: unknown): Record<string, string> | null {
  if (!(e instanceof ApiError) || e.status !== 400 || !e.data || typeof e.data !== "object") return null;
  const d = e.data as Partial<TemplateInputProblems>;
  if (!d.missing && !d.invalid && !d.unknown) return null;
  const out: Record<string, string> = {};
  for (const k of d.missing ?? []) out[k] = t("필수 입력입니다");
  for (const k of d.invalid ?? []) out[k] = t("형식이 맞지 않습니다");
  return out;
}

/** Only non-empty values are sent; empty ones fall back to the template default on the server. */
export function inputValues(values: Record<string, string>): Record<string, string> {
  return Object.fromEntries(Object.entries(values).filter(([, v]) => v !== ""));
}

export function TemplateInputsForm({
  inputs,
  values,
  errors,
  onChange,
  idPrefix = "tpl-in",
}: {
  inputs: TemplateInput[];
  values: Record<string, string>;
  errors: Record<string, string>;
  onChange: (key: string, value: string) => void;
  idPrefix?: string;
}) {
  if (inputs.length === 0) return null;
  return (
    <div className="space-y-3">
      {inputs.map((inp) => {
        const id = `${idPrefix}-${inp.key}`;
        const err = errors[inp.key];
        const fnDefault = !!inp.default && inp.default.includes("${{");
        return (
          <div key={inp.key}>
            <label htmlFor={id} className="mb-2 block text-xs font-semibold leading-[18px] text-gray-600">
              {inp.label || inp.key}
              {inp.required && <span className="ml-0.5 text-red-500" aria-hidden="true">*</span>}
              <span className="ml-2 font-mono text-xs font-normal text-gray-500">{inp.key}</span>
            </label>
            <Input
              id={id}
              mono
              type={inp.secret ? "password" : "text"}
              autoComplete={inp.secret ? "new-password" : "off"}
              value={values[inp.key] ?? ""}
              required={inp.required}
              aria-invalid={err ? true : undefined}
              aria-describedby={err || inp.description ? `${id}-hint` : undefined}
              placeholder={fnDefault ? t("비우면 배포할 때 생성합니다") : inp.default || ""}
              onChange={(e) => onChange(inp.key, e.target.value)}
              className="w-full"
            />
            {(err || inp.description) && (
              <div id={`${id}-hint`} className={cx("mt-2 text-xs leading-[18px]", err ? "text-danger" : "text-gray-600")}>
                {err || inp.description}
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}

/* ── Publish as template (environment menu) ─────────────────────────── */

const VIS_OPTIONS: { value: TemplateVisibility; label: string }[] = [
  { value: "private", label: t("나만") },
  { value: "workspace", label: t("작업 공간") },
  { value: "public", label: t("공개") },
];

export function visibilityLabel(v: TemplateVisibility): string {
  return VIS_OPTIONS.find((o) => o.value === v)?.label ?? v;
}

/**
 * "템플릿으로 게시": generates a definition from the environment (POST .../export-template), lets the user edit
 * name/description/visibility and the JSON, then saves it with POST /templates.
 */
export function PublishTemplateDialog({ pid, env, open, onClose }: { pid: ID | null; env: Environment | undefined; open: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const workspace = useWorkspace();
  const [secrets, setSecrets] = useState<"generate" | "inputs">("generate");
  const [json, setJson] = useState("");
  const [warnings, setWarnings] = useState<string[]>([]);
  const [name, setName] = useState("");
  const [key, setKey] = useState("");
  const [description, setDescription] = useState("");
  const [visibility, setVisibility] = useState<TemplateVisibility>("private");
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  useEffect(() => {
    if (!open || !pid || !env) return;
    let cancelled = false;
    setLoading(true);
    setError(null);
    api
      .exportEnvironmentTemplate(pid, env.id, secrets)
      .then((r) => {
        if (cancelled) return;
        setJson(JSON.stringify(r.definition, null, 2));
        setWarnings(r.warnings);
        setName((n) => n || r.definition.name || "");
        setKey((k) => k || r.definition.key || "");
        setDescription((d) => d || r.definition.description || "");
      })
      .catch((e) => !cancelled && setError(e))
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
  }, [open, pid, env, secrets]);

  const close = () => {
    if (busy) return;
    setJson("");
    setWarnings([]);
    setName("");
    setKey("");
    setDescription("");
    setError(null);
    onClose();
  };

  const save = async () => {
    let definition: TemplateDefinition;
    try {
      definition = JSON.parse(json) as TemplateDefinition;
    } catch (e) {
      setError(new Error(t("JSON을 읽을 수 없습니다: {error}", { error: e instanceof Error ? e.message : String(e) })));
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const created = await api.createTemplate({
        workspace_id: workspace.wsid ?? undefined,
        definition,
        key: key.trim() || undefined,
        name: name.trim() || undefined,
        description: description.trim(),
        visibility,
      });
      await qc.invalidateQueries({ queryKey: qk.templates });
      toast(t("템플릿 {name}을(를) 게시했습니다", { name: created.name }));
      close();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onClose={close}
      wide
      title={t("{name} 환경을 템플릿으로 게시", { name: env?.name ?? "" })}
      footer={
        <>
          <Button variant="outline" onClick={close} disabled={busy}>
            {t("취소")}
          </Button>
          <Button variant="primary" onClick={save} loading={busy} disabled={loading || !json}>
            {t("템플릿 저장")}
          </Button>
        </>
      }
    >
      <div className="space-y-4 pt-1">
        <p>
          {t("이 환경의 서비스, 설정, 변수(참조 유지), 볼륨, 생성된 도메인으로 템플릿 정의를 만듭니다. custom domain, TCP 프록시 공개 포트, deploy key, 레지스트리 자격 증명은 넣지 않고, DB 비밀번호는 배포할 때 새로 만듭니다. 저장하기 전에 JSON을 검토하세요.")}
        </p>
        <div className="flex flex-wrap items-center gap-3">
          <span className="text-sm font-medium text-gray-600">{t("비밀처럼 보이는 값")}</span>
          <Segmented
            size="sm"
            label={t("비밀 처리")}
            value={secrets}
            onChange={setSecrets}
            options={[
              { value: "generate", label: t("배포할 때 생성") },
              { value: "inputs", label: t("입력으로 받기") },
            ]}
          />
        </div>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={t("이름")}>
            <Input value={name} onChange={(e) => setName(e.target.value)} className="w-full" />
          </Field>
          <Field label={t("키")} hint={t("URL과 CLI에서 쓰는 이름(소문자, 숫자, -)")}>
            <Input mono value={key} onChange={(e) => setKey(e.target.value)} className="w-full" />
          </Field>
        </div>
        <Field label={t("설명")}>
          <Input value={description} onChange={(e) => setDescription(e.target.value)} className="w-full" />
        </Field>
        <div>
          <div className={LABEL_CLS}>{t("공개 범위")}</div>
          <Segmented label={t("공개 범위")} value={visibility} onChange={setVisibility} options={VIS_OPTIONS} />
          <p className="mt-1.5 text-xs text-gray-500">
            {t("나만: 나와 작업 공간 admin · 작업 공간/공개: 로그인한 모든 사용자")}
          </p>
        </div>
        {warnings.length > 0 && (
          <ul className="space-y-0.5 rounded-md bg-warn-surface px-3 py-2 text-xs text-warn" aria-label={t("경고")}>
            {warnings.map((w) => (
              <li key={w}>{w}</li>
            ))}
          </ul>
        )}
        <Field label={t("정의 (JSON)")}>
          <Textarea
            mono
            spellCheck={false}
            value={loading ? "" : json}
            placeholder={loading ? t("만드는 중…") : ""}
            onChange={(e) => setJson(e.target.value)}
            rows={16}
            className="w-full text-xs"
          />
        </Field>
        {error ? <ErrorBox error={error} /> : null}
      </div>
    </Dialog>
  );
}
