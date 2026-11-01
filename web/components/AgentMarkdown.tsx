"use client";

import { Fragment, type ReactNode } from "react";
import { t } from "@/lib/i18n";
import { CopyButton, cx } from "./ui";

/**
 * Markdown subset for agent replies, rendered as React nodes (never as HTML, so model output cannot
 * inject markup): fenced code blocks with a language label and copy button, headings, bullet and
 * numbered lists, block quotes, tables-as-preformatted, paragraphs; inline code, bold, italic and
 * http(s) links. An unclosed fence while streaming renders as a code block that is still growing.
 */

type Block =
  | { kind: "code"; lang: string; text: string; open: boolean }
  | { kind: "heading"; level: number; text: string }
  | { kind: "list"; ordered: boolean; start: number; items: string[] }
  | { kind: "quote"; text: string }
  | { kind: "table"; text: string }
  | { kind: "para"; text: string }
  | { kind: "rule" };

const FENCE = /^\s{0,3}(```|~~~)\s*([\w+#.-]*)/;
const BULLET = /^\s{0,3}[-*+]\s+(.*)$/;
const NUMBER = /^\s{0,3}(\d{1,9})[.)]\s+(.*)$/;

export function parseBlocks(src: string): Block[] {
  const lines = src.replace(/\r\n?/g, "\n").split("\n");
  const out: Block[] = [];
  let para: string[] = [];
  const flush = () => {
    if (para.length) out.push({ kind: "para", text: para.join("\n") });
    para = [];
  };
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const fence = FENCE.exec(line);
    if (fence) {
      flush();
      const body: string[] = [];
      let j = i + 1;
      for (; j < lines.length && !lines[j].trimStart().startsWith(fence[1]); j++) body.push(lines[j]);
      out.push({ kind: "code", lang: fence[2], text: body.join("\n"), open: j >= lines.length });
      i = j;
      continue;
    }
    if (!line.trim()) {
      flush();
      continue;
    }
    const h = /^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$/.exec(line);
    if (h) {
      flush();
      out.push({ kind: "heading", level: h[1].length, text: h[2] });
      continue;
    }
    if (/^\s{0,3}([-*_])(\s*\1){2,}\s*$/.test(line)) {
      flush();
      out.push({ kind: "rule" });
      continue;
    }
    const b = BULLET.exec(line);
    const n = NUMBER.exec(line);
    if (b || n) {
      flush();
      const ordered = !b;
      const items: string[] = [];
      let j = i;
      for (; j < lines.length; j++) {
        const m = ordered ? NUMBER.exec(lines[j]) : BULLET.exec(lines[j]);
        if (m) items.push(ordered ? m[2] : m[1]);
        // Indented continuation of the previous item.
        else if (items.length && /^\s{2,}\S/.test(lines[j])) items[items.length - 1] += `\n${lines[j].trim()}`;
        else break;
      }
      out.push({ kind: "list", ordered, start: n ? Number(n[1]) : 1, items });
      i = j - 1;
      continue;
    }
    if (/^\s{0,3}>/.test(line)) {
      flush();
      const body: string[] = [];
      let j = i;
      for (; j < lines.length && /^\s{0,3}>/.test(lines[j]); j++) body.push(lines[j].replace(/^\s{0,3}>\s?/, ""));
      out.push({ kind: "quote", text: body.join("\n") });
      i = j - 1;
      continue;
    }
    if (/^\s*\|.*\|\s*$/.test(line)) {
      flush();
      const body: string[] = [];
      let j = i;
      for (; j < lines.length && /^\s*\|.*\|\s*$/.test(lines[j]); j++) body.push(lines[j].trim());
      out.push({ kind: "table", text: body.join("\n") });
      i = j - 1;
      continue;
    }
    para.push(line);
  }
  flush();
  return out;
}

const INLINE = /(`[^`\n]+`|\*\*[^*\n]+\*\*|__[^_\n]+__|\*[^*\s][^*\n]*\*|\[[^\]\n]+\]\([^)\s]+\)|https?:\/\/[^\s<>()]+[^\s<>().,;:!?'"])/g;

function safeHref(url: string): string | null {
  try {
    const u = new URL(url, window.location.origin);
    if (u.protocol === "http:" || u.protocol === "https:") return u.href;
  } catch {
    /* not a URL */
  }
  return null;
}

function Inline({ text }: { text: string }) {
  const parts = text.split(INLINE);
  return (
    <>
      {parts.map((part, i) => {
        if (i % 2 === 0) return <Fragment key={i}>{part}</Fragment>;
        if (part.startsWith("`"))
          return (
            <code key={i} className="rounded bg-gray-100 px-1 py-px font-mono text-[12px] text-foreground">
              {part.slice(1, -1)}
            </code>
          );
        if (part.startsWith("**") || part.startsWith("__"))
          return (
            <strong key={i} className="font-semibold text-foreground">
              <Inline text={part.slice(2, -2)} />
            </strong>
          );
        if (part.startsWith("*"))
          return (
            <em key={i}>
              <Inline text={part.slice(1, -1)} />
            </em>
          );
        const link = /^\[([^\]]+)\]\(([^)]+)\)$/.exec(part);
        const label = link ? link[1] : part;
        const href = safeHref(link ? link[2] : part);
        if (!href) return <Fragment key={i}>{part}</Fragment>;
        const internal = href.startsWith(window.location.origin);
        return (
          <a key={i} href={href} target={internal ? undefined : "_blank"} rel={internal ? undefined : "noreferrer noopener"} className="text-pink-600 underline-offset-2 hover:underline">
            {label}
          </a>
        );
      })}
    </>
  );
}

function Lines({ text }: { text: string }) {
  const lines = text.split("\n");
  return (
    <>
      {lines.map((l, i) => (
        <Fragment key={i}>
          {i > 0 && <br />}
          <Inline text={l} />
        </Fragment>
      ))}
    </>
  );
}

export function CodeBlock({ lang, text, open }: { lang?: string; text: string; open?: boolean }) {
  return (
    <div className="group/code my-2 overflow-hidden rounded-md border border-gray-200 bg-gray-50 dark:border-white/10">
      <div className="flex h-7 items-center justify-between border-b border-gray-200 pl-2.5 pr-0.5 text-[11px] text-gray-500 dark:border-white/10">
        <span className="font-mono">{lang || t("코드")}</span>
        {!open && <CopyButton value={text} label={t("코드 복사")} className="!size-6 [&_svg]:!size-3.5" />}
      </div>
      <pre className="max-h-[360px] overflow-auto p-2.5 font-mono text-[12px] leading-[18px] text-foreground">
        <code>{text}</code>
      </pre>
    </div>
  );
}

export function Markdown({ text, streaming }: { text: string; streaming?: boolean }) {
  const blocks = parseBlocks(text);
  const caret = streaming ? <span aria-hidden className="ml-0.5 inline-block h-[1em] w-[2px] translate-y-[2px] bg-pink-500 motion-safe:animate-pulse" /> : null;
  const nodes: ReactNode[] = blocks.map((b, i) => {
    const last = i === blocks.length - 1;
    switch (b.kind) {
      case "code":
        return <CodeBlock key={i} lang={b.lang} text={b.text} open={b.open && streaming} />;
      case "heading":
        return (
          <div key={i} role="heading" aria-level={Math.min(b.level + 2, 6)} className={cx("mb-1 mt-3 font-semibold text-foreground first:mt-0", b.level <= 2 ? "text-[15px]" : "text-sm")}>
            <Inline text={b.text} />
          </div>
        );
      case "list": {
        const Tag = b.ordered ? "ol" : "ul";
        return (
          <Tag key={i} start={b.ordered && b.start !== 1 ? b.start : undefined} className={cx("my-1.5 space-y-0.5 pl-5", b.ordered ? "list-decimal" : "list-disc", "marker:text-gray-400")}>
            {b.items.map((it, j) => (
              <li key={j}>
                <Lines text={it} />
                {last && j === b.items.length - 1 && caret}
              </li>
            ))}
          </Tag>
        );
      }
      case "quote":
        return (
          <blockquote key={i} className="my-2 border-l-2 border-gray-300 pl-3 text-gray-600">
            <Lines text={b.text} />
          </blockquote>
        );
      case "table":
        return <CodeBlock key={i} text={b.text} />;
      case "rule":
        return <hr key={i} className="my-3 border-gray-200" />;
      default:
        return (
          <p key={i} className="my-1.5 break-words first:mt-0 last:mb-0">
            <Lines text={b.text} />
            {last && caret}
          </p>
        );
    }
  });
  const lastKind = blocks[blocks.length - 1]?.kind;
  return (
    <>
      {nodes}
      {/* The caret sits after the text; code blocks and empty replies get it on its own line. */}
      {streaming && (!lastKind || lastKind === "code" || lastKind === "table" || lastKind === "quote" || lastKind === "heading" || lastKind === "rule") && <div>{caret}</div>}
    </>
  );
}
