"use client";

import Link from "next/link";
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useSearchParams } from "next/navigation";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ArrowUp, History, MessageSquare, Plus, ShieldAlert, Sparkles, Square, Trash2, Wrench, X } from "lucide-react";
import { agentApi, type AgentMessage, type AgentPendingAction, type AgentStreamEvent } from "@/lib/agentApi";
import { useCaps } from "@/lib/queries";
import type { Workspace } from "@/lib/workspace";
import { t, dateLocale } from "@/lib/i18n";
import { Button, EmptyState, IconButton, Menu, MenuItem, Spinner, cx, errorMessage, useEscape } from "./ui";


const OPEN_EVENT = "keel:agent-open";
type OpenDetail = { prompt?: string; serviceId?: string; environmentId?: string };
export function openAvenAgent(detail: OpenDetail = {}) {
  window.dispatchEvent(new CustomEvent<OpenDetail>(OPEN_EVENT, { detail }));
}
/* ── message model ───────────────────────────────────────────────────── */
type Item =
  | { kind: "user"; text: string }
  | { kind: "assistant"; text: string }
  | { kind: "tool"; name: string; isError?: boolean; declined?: boolean }
  | { kind: "error"; text: string };
const TOOL_LABELS: Record<string, string> = {
  list_projects: t("프로젝트 목록 확인"),
  list_services: t("서비스 상태 확인"),
  list_deployments: t("배포 기록 확인"),
  get_logs: t("로그 읽기"),
  get_metrics: t("지표 확인"),
  list_variables: t("변수 이름 확인"),
  restart_service: t("재시작"),
  redeploy_service: t("재배포"),
  set_variables: t("변수 변경"),
  rollback_deployment: t("롤백"),
};
const ACTION_TITLES: Record<string, string> = {
  restart_service: t("서비스 재시작"),
  redeploy_service: t("서비스 재배포"),
  set_variables: t("변수 변경"),
  rollback_deployment: t("배포 롤백"),
};
// 화면 언어로 보이고 그대로 질문으로 보낸다(에이전트는 받은 언어로 답한다).
const SUGGESTIONS = [t("서비스 상태를 요약해 줘"), t("최근 실패한 배포의 원인을 찾아 줘"), t("CPU와 메모리 사용량을 보여 줘"), t("에러 로그가 있는지 확인해 줘")];
function fromThread(messages: AgentMessage[]): Item[] {
  return messages.map((m): Item =>
    m.role === "tool" ? { kind: "tool", name: m.tool ?? "" } : { kind: m.role, text: m.content ?? "" },
  );
}
/* ── tiny markdown (code fences, inline code, bold); rendered as React nodes, never as HTML ── */
function inline(text: string, key: string): ReactNode[] {
  return text.split(/(`[^`\n]+`|\*\*[^*\n]+\*\*)/g).map((part, i) => {
    if (part.startsWith("`") && part.endsWith("`") && part.length > 2)
      return (
        <code key={`${key}-${i}`} className="rounded bg-gray-100 px-1 py-px font-mono text-xs">
          {part.slice(1, -1)}
        </code>
      );
    if (part.startsWith("**") && part.endsWith("**") && part.length > 4)
      return (
        <strong key={`${key}-${i}`} className="font-semibold text-foreground">
          {part.slice(2, -2)}
        </strong>
      );
    return part;
  });
}
function Markdown({ text }: { text: string }) {
  const blocks = text.split(/```[^\n]*\n?/);
  return (
    <>
      {blocks.map((block, i) =>
        i % 2 === 1 ? (
          <pre key={i} className="my-2 overflow-x-auto rounded-md bg-gray-100 p-2 font-mono text-xs leading-[18px] text-foreground">
            {block.replace(/\n$/, "")}
          </pre>
        ) : (
          <span key={i} className="whitespace-pre-wrap break-words">
            {inline(block, String(i))}
          </span>
        ),
      )}
    </>
  );
}
/* ── panel ───────────────────────────────────────────────────────────── */
function useAgentContext(ws: Workspace) {
  const params = useSearchParams();
  return { project_id: ws.pid ?? undefined, environment_id: ws.env?.id, service_id: params.get("svc") ?? undefined };
}
function AgentPanel({ ws, onClose, request }: { ws: Workspace; onClose: () => void; request: (OpenDetail & { seq: number }) | null }) {
  const qc = useQueryClient();
  const caps = useCaps(ws.pid);
  const ctx = useAgentContext(ws);
  const settings = useQuery({ queryKey: ["agent-settings"], queryFn: agentApi.settings, staleTime: 30_000 });
  const threads = useQuery({ queryKey: ["agent-threads", ws.pid], queryFn: () => agentApi.threads(ws.pid), staleTime: 10_000 });
  const [threadId, setThreadId] = useState<string | null>(null);
  const [items, setItems] = useState<Item[]>([]);
  const [pending, setPending] = useState<AgentPendingAction | null>(null);
  const [busy, setBusy] = useState(false);
  const [loadingThread, setLoadingThread] = useState(false);
  const [input, setInput] = useState("");
  const abort = useRef<AbortController | null>(null);
  const scroller = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  useEscape(onClose);
  useEffect(() => () => abort.current?.abort(), []);
  useEffect(() => {
    scroller.current?.scrollTo({ top: scroller.current.scrollHeight });
  }, [items, pending]);
  const onEvent = useCallback(
    (e: AgentStreamEvent) => {
      switch (e.event) {
        case "thread":
          setThreadId(e.data.thread_id);
          break;
        case "token":
          setItems((cur) => {
            const last = cur[cur.length - 1];
            if (last?.kind === "assistant") return [...cur.slice(0, -1), { kind: "assistant", text: last.text + e.data.text }];
            return [...cur, { kind: "assistant", text: e.data.text }];
          });
          break;
        case "tool":
          setItems((cur) => [...cur, { kind: "tool", name: e.data.name, isError: e.data.is_error, declined: e.data.declined }]);
          if (e.data.name in ACTION_TITLES && !e.data.declined) {
            // The action changed something: refresh canvas, instances and deployments.
            for (const key of ["canvas", "instance", "deployments", "project"]) qc.invalidateQueries({ queryKey: [key] });
          }
          break;
        case "confirmation_required":
          setPending(e.data);
          break;
        case "error":
          setItems((cur) => [...cur, { kind: "error", text: e.data.message }]);
          break;
      }
    },
    [qc],
  );
  const runStream = useCallback(
    async (fn: (signal: AbortSignal) => Promise<void>) => {
      abort.current?.abort();
      const ac = new AbortController();
      abort.current = ac;
      setBusy(true);
      try {
        await fn(ac.signal);
      } catch (err) {
        if (!ac.signal.aborted) setItems((cur) => [...cur, { kind: "error", text: errorMessage(err) }]);
      } finally {
        if (abort.current === ac) {
          abort.current = null;
          setBusy(false);
        }
        qc.invalidateQueries({ queryKey: ["agent-threads"] });
      }
    },
    [qc],
  );
  const send = useCallback(
    (prompt: string, opts: { fresh?: boolean; context?: typeof ctx } = {}) => {
      const text = prompt.trim();
      if (!text) return;
      const tid = opts.fresh ? undefined : (threadId ?? undefined);
      if (opts.fresh) {
        setThreadId(null);
        setPending(null);
        setItems([{ kind: "user", text }]);
      } else setItems((cur) => [...cur, { kind: "user", text }]);
      setInput("");
      void runStream((signal) => agentApi.chat({ ...(opts.context ?? ctx), thread_id: tid, prompt: text }, onEvent, signal));
    },
    [ctx, onEvent, runStream, threadId],
  );
  const decide = (approve: boolean) => {
    if (!pending || !threadId) return;
    const callId = pending.call_id;
    setPending(null);
    void runStream((signal) => agentApi.confirm({ thread_id: threadId, call_id: callId, approve }, onEvent, signal));
  };
  const openThread = async (id: string) => {
    abort.current?.abort();
    setLoadingThread(true);
    try {
      const th = await agentApi.thread(id);
      setThreadId(th.id);
      setItems(fromThread(th.messages));
      setPending(th.pending_action ?? null);
    } catch (err) {
      setItems([{ kind: "error", text: errorMessage(err) }]);
    } finally {
      setLoadingThread(false);
    }
  };
  const newChat = () => {
    abort.current?.abort();
    setThreadId(null);
    setItems([]);
    setPending(null);
    inputRef.current?.focus();
  };
  const removeThread = async (id: string) => {
    await agentApi.deleteThread(id).catch(() => undefined);
    if (id === threadId) newChat();
    qc.invalidateQueries({ queryKey: ["agent-threads"] });
  };
  // External open requests ("Aven Agent로 진단") start a fresh conversation.
  const handled = useRef(0);
  useEffect(() => {
    if (!request || request.seq === handled.current || !settings.data?.configured) return;
    handled.current = request.seq;
    if (request.prompt)
      send(request.prompt, {
        fresh: true,
        context: { ...ctx, service_id: request.serviceId ?? ctx.service_id, environment_id: request.environmentId ?? ctx.environment_id },
      });
  }, [request, send, ctx, settings.data?.configured]);
  const configured = settings.data?.configured;
  return (
    <aside
      role="dialog"
      aria-label="Aven Agent"
      className="fixed bottom-3 right-3 top-[60px] z-[60] flex w-[min(440px,calc(100vw-24px))] animate-modal-scale-in flex-col overflow-hidden rounded-xl border border-neutral bg-background shadow-lg"
    >
      <header className="flex h-12 shrink-0 items-center gap-2 border-b border-gray-200 pl-4 pr-2">
        <Sparkles className="size-4 text-pink-500" />
        <div className="min-w-0 flex-1">
          <div className="text-sm font-semibold leading-4">Aven Agent</div>
          {configured && <div className="truncate text-xs leading-4 text-gray-500">{settings.data?.model}</div>}
        </div>
        <Menu
          label={t("이전 대화")}
          trigger={(p) => (
            <IconButton {...p} label={t("이전 대화")} size="sm">
              <History />
            </IconButton>
          )}
        >
          {(close) => (
            <div className="w-[299px]">
              <div className="px-3 pb-1 pt-2 text-xs font-medium text-gray-500">{ws.detail ? t("이전 대화 · {name}", { name: ws.detail.name }) : t("이전 대화")}</div>
              {threads.isPending ? (
                <div className="flex justify-center py-4">
                  <Spinner />
                </div>
              ) : !threads.data?.length ? (
                <div className="px-3 py-4 text-center text-xs text-gray-500">{t("대화 기록이 없습니다")}</div>
              ) : (
                <div className="max-h-[360px] overflow-y-auto">
                  {threads.data.map((th) => (
                    <div key={th.id} className="group/thread flex items-center">
                      <MenuItem
                        className={cx("min-w-0 flex-1", th.id === threadId && "bg-gray-100")}
                        onSelect={() => {
                          close();
                          void openThread(th.id);
                        }}
                      >
                        <span className="min-w-0 flex-1 truncate">{th.title || t("제목 없음")}</span>
                        <span className="shrink-0 text-xs tabular-nums text-gray-500">{new Date(th.updated_at).toLocaleDateString(dateLocale())}</span>
                      </MenuItem>
                      <IconButton label={t("대화 삭제")} size="sm" className="mr-1 opacity-0 group-hover/thread:opacity-100 focus-visible:opacity-100" onClick={() => removeThread(th.id)}>
                        <Trash2 />
                      </IconButton>
                    </div>
                  ))}
                </div>
              )}
            </div>
          )}
        </Menu>
        <IconButton label={t("새 대화")} size="sm" onClick={newChat}>
          <Plus />
        </IconButton>
        <IconButton label={t("닫기")} size="sm" onClick={onClose}>
          <X />
        </IconButton>
      </header>
      <div ref={scroller} className="min-h-0 flex-1 overflow-y-auto px-4 py-4" aria-live="polite">
        {settings.isPending || loadingThread ? (
          <div className="flex justify-center py-10">
            <Spinner />
          </div>
        ) : !configured ? (
          <EmptyState
            icon={<Sparkles className="size-5" />}
            title={t("Aven Agent가 아직 연결되지 않았습니다")}
            action={
              caps.instanceAdmin && (
                <Link href="/admin/?section=agent" onClick={onClose} className="text-sm font-medium text-pink-500 hover:underline">
                  {t("설정 > Aven Agent로 이동")}
                </Link>
              )
            }
          >
            {t("작업 공간 admin이 OpenAI 호환 모델(OpenAI, OpenRouter, Ollama 등)의 endpoint와 API key를 설정하면 쓸 수 있습니다.")}
          </EmptyState>
        ) : items.length === 0 && !pending ? (
          <div className="space-y-4 py-6">
            <div className="text-center">
              <div className="text-base font-semibold">{t("무엇을 도와드릴까요?")}</div>
              <p className="mt-1 text-xs leading-5 text-gray-500">
                {t("프로젝트, 서비스, 배포, 로그, 지표를 살펴보고 재시작·재배포·변수 변경·롤백을 제안합니다. 바꾸는 작업은 항상 먼저 확인을 받습니다.")}
              </p>
            </div>
            <div className="grid gap-2">
              {SUGGESTIONS.map((s) => (
                <button
                  key={s}
                  type="button"
                  onClick={() => send(s)}
                  className="cursor-pointer rounded-md border border-gray-200 bg-gray-50 px-3 py-2 text-left text-sm text-gray-700 hover:border-gray-300 hover:bg-gray-100"
                >
                  {s}
                </button>
              ))}
            </div>
          </div>
        ) : (
          <div className="space-y-3">
            {items.map((item, i) =>
              item.kind === "user" ? (
                <div key={i} className="ml-8 flex justify-end">
                  <div className="whitespace-pre-wrap break-words rounded-lg bg-pink-500 px-3 py-2 text-sm text-white">{item.text}</div>
                </div>
              ) : item.kind === "assistant" ? (
                <div key={i} className="mr-4 text-sm leading-6 text-foreground">
                  <Markdown text={item.text} />
                </div>
              ) : item.kind === "tool" ? (
                <div key={i} className={cx("flex items-center gap-1.5 text-xs", item.isError ? "text-danger" : "text-gray-500")}>
                  <Wrench className="size-3" />
                  {TOOL_LABELS[item.name] ?? item.name}
                  {item.declined ? ` · ${t("거절됨")}` : item.isError ? ` · ${t("실패")}` : ""}
                </div>
              ) : (
                <div key={i} role="alert" className="flex items-start gap-2 rounded-md bg-danger-surface px-3 py-2 text-xs text-danger">
                  <AlertTriangle className="mt-px size-3.5 shrink-0" />
                  <span className="min-w-0 break-words">{item.text}</span>
                </div>
              ),
            )}
            {busy && !pending && (
              <div className="flex items-center gap-2 text-xs text-gray-500">
                <Spinner className="size-3" /> {t("생각하는 중…")}
              </div>
            )}
            {pending && <ConfirmCard action={pending} busy={busy} onDecide={decide} />}
          </div>
        )}
      </div>
      {configured && (
        <form
          className="shrink-0 border-t border-gray-200 p-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (!busy && !pending) send(input);
          }}
        >
          <div className="flex items-end gap-2 rounded-lg border border-gray-200 px-3 py-2 focus-within:border-pink-400">
            <textarea
              ref={inputRef}
              autoFocus
              rows={1}
              value={input}
              onChange={(e) => setInput(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
                  e.preventDefault();
                  if (!busy && !pending) send(input);
                }
              }}
              aria-label={t("Aven Agent에게 메시지")}
              placeholder={pending ? t("먼저 위 작업을 승인하거나 거절하세요") : t("메시지를 입력하세요 (Shift+Enter 줄바꿈)")}
              disabled={!!pending}
              className="max-h-40 min-h-[24px] flex-1 resize-none bg-transparent text-sm leading-6 text-foreground placeholder-gray-500 outline-none disabled:cursor-not-allowed"
              style={{ fieldSizing: "content" } as React.CSSProperties}
            />
            {busy ? (
              <IconButton label={t("응답 중지")} size="sm" onClick={() => abort.current?.abort()}>
                <Square />
              </IconButton>
            ) : (
              <IconButton label={t("보내기")} size="sm" type="submit" disabled={!input.trim() || !!pending} className="!text-pink-500 disabled:!text-gray-400">
                <ArrowUp />
              </IconButton>
            )}
          </div>
          {ctx.service_id && ws.detail && (
            <div className="mt-1.5 truncate px-1 text-xs text-gray-500">
              {t("문맥: {project} · {env} · {service}", { project: ws.detail.name, env: ws.env?.name ?? "", service: ws.detail.services?.find((s) => s.id === ctx.service_id)?.name ?? t("서비스") })}
            </div>
          )}
        </form>
      )}
    </aside>
  );
}
function ConfirmCard({ action, busy, onDecide }: { action: AgentPendingAction; busy: boolean; onDecide: (approve: boolean) => void }) {
  const args = action.arguments && Object.keys(action.arguments).length ? JSON.stringify(action.arguments, null, 2) : null;
  const danger = action.name === "rollback_deployment" || action.name === "set_variables";
  return (
    <div role="alertdialog" aria-label={t("작업 확인")} className="rounded-lg border border-yellow-200 bg-warn-surface p-3">
      <div className="flex items-center gap-1.5 text-sm font-semibold text-foreground">
        <ShieldAlert className="size-4 text-warn" />
        {t("{action} 확인", { action: ACTION_TITLES[action.name] ?? action.name })}
      </div>
      <p className="mt-1 text-sm leading-5 text-gray-700">{action.summary}</p>
      {args && (
        <details className="mt-2 text-xs text-gray-500">
          <summary className="cursor-pointer select-none">{t("인자 보기")}</summary>
          <pre className="mt-1 max-h-40 overflow-auto rounded bg-gray-100 p-2 font-mono text-xs leading-4 text-foreground">{args}</pre>
        </details>
      )}
      <div className="mt-3 flex justify-end gap-2">
        <Button size="sm" variant="outline" onClick={() => onDecide(false)} disabled={busy}>
          {t("거절")}
        </Button>
        <Button size="sm" variant={danger ? "danger" : "primary"} onClick={() => onDecide(true)} disabled={busy}>
          {t("승인하고 실행")}
        </Button>
      </div>
    </div>
  );
}

export function AgentButton({ ws }: { ws: Workspace }) {
  const [open, setOpen] = useState(false);
  const [request, setRequest] = useState<(OpenDetail & { seq: number }) | null>(null);
  useEffect(() => {
    let seq = 0;
    const onOpen = (e: Event) => {
      setOpen(true);
      const detail = (e as CustomEvent<OpenDetail>).detail ?? {};
      if (detail.prompt) setRequest({ ...detail, seq: ++seq });
    };
    window.addEventListener(OPEN_EVENT, onOpen);
    return () => window.removeEventListener(OPEN_EVENT, onOpen);
  }, []);
  return (
    <>
      <button
        type="button"
        aria-label={t("Aven 에이전트")}
        aria-pressed={open}
        title={t("Aven 에이전트")}
        onClick={() => setOpen((o) => !o)}
        className={cx(
          "flex h-9 shrink-0 cursor-pointer items-center gap-1.5 rounded-md border border-transparent px-2 text-sm font-medium text-neutral-secondary hover:bg-gray-100 hover:text-gray-900 sm:h-[30px] dark:hover:bg-white/10 dark:hover:text-white",
          open && "bg-gray-100 text-gray-800 dark:bg-white/10 dark:text-white",
        )}
      >
        <span className="icon-container icon-sm text-lg text-current">
          <MessageSquare />
        </span>
        <span className="hidden sm:inline">{t("에이전트")}</span>
      </button>
      {open && typeof document !== "undefined" && createPortal(
          <AgentPanel
            ws={ws}
            request={request}
            onClose={() => {
              setOpen(false);
              setRequest(null);
            }}
          />,
          document.body,
        )}
    </>
  );
}
