// Aven Agent API (dashboard AI assistant). Kept apart from lib/api.ts so the feature stays self-contained.

import { ApiError, type ID } from "./api";
import { t } from "./i18n";

/** endpoint/key_hint are only filled in for workspace admins. */
export type AgentSettings = { endpoint: string; model: string; key_set: boolean; key_hint?: string; configured: boolean };
/** A thread message as shown in the UI; role "tool" only carries the tool name. */
export type AgentMessage = { role: "user" | "assistant" | "tool"; content?: string; tool?: string };
export type AgentPendingAction = { call_id: string; name: string; arguments: Record<string, unknown>; summary: string };
export type AgentThreadItem = { id: ID; project_id?: ID; title: string; created_at: string; updated_at: string };
export type AgentThread = AgentThreadItem & { messages: AgentMessage[]; pending_action?: AgentPendingAction };

/** SSE events of POST /ai/chat and /ai/confirm. */
export type AgentStreamEvent =
  | { event: "thread"; data: { thread_id: ID } }
  | { event: "token"; data: { text: string } }
  | { event: "tool"; data: { call_id: string; name: string; arguments: Record<string, unknown>; result?: unknown; is_error: boolean; declined?: boolean } }
  | { event: "confirmation_required"; data: AgentPendingAction & { thread_id: ID } }
  | { event: "done"; data: { thread_id: ID; response: string } }
  | { event: "error"; data: { message: string } };

function redirectToLogin() {
  if (typeof window === "undefined" || window.location.pathname.startsWith("/login")) return;
  const next = window.location.pathname + window.location.search;
  window.location.href = `/login/?next=${encodeURIComponent(next)}`;
}

async function failed(res: Response): Promise<never> {
  if (res.status === 401) {
    redirectToLogin();
    throw new ApiError(401, t("인증이 필요합니다"));
  }
  let message = `${res.status} ${res.statusText}`;
  try {
    const body = (await res.json()) as { error?: string };
    if (body.error) message = body.error;
  } catch {
    /* plain-text error body */
  }
  throw new ApiError(res.status, message);
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`/api${path}`, {
    method,
    credentials: "include",
    headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) return failed(res);
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

/** POSTs JSON and hands each SSE event of the response to onEvent until the stream ends. */
async function stream(path: string, body: unknown, onEvent: (e: AgentStreamEvent) => void, signal?: AbortSignal): Promise<void> {
  const res = await fetch(`/api${path}`, {
    method: "POST",
    credentials: "include",
    headers: { "Content-Type": "application/json", Accept: "text/event-stream" },
    body: JSON.stringify(body),
    signal,
  });
  if (!res.ok) return failed(res);
  if (!res.body) throw new Error(t("스트림 응답을 읽을 수 없습니다."));
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  let name = "message";
  let data: string[] = [];
  const dispatch = () => {
    if (data.length) {
      try {
        onEvent({ event: name, data: JSON.parse(data.join("\n")) } as AgentStreamEvent);
      } catch {
        /* skip malformed events */
      }
    }
    name = "message";
    data = [];
  };
  for (;;) {
    const { value, done } = await reader.read();
    buffer += decoder.decode(value, { stream: !done });
    const lines = buffer.split("\n");
    buffer = lines.pop() ?? "";
    for (const line of lines) {
      if (line === "") dispatch();
      else if (line.startsWith("event: ")) name = line.slice(7);
      else if (line.startsWith("data: ")) data.push(line.slice(6));
    }
    if (done) {
      if (buffer.startsWith("data: ")) data.push(buffer.slice(6));
      dispatch();
      return;
    }
  }
}

const enc = encodeURIComponent;

export const agentApi = {
  settings: () => call<AgentSettings>("GET", "/ai/settings"),
  updateSettings: (input: { endpoint: string; model: string; api_key?: string; clear_api_key?: boolean }) =>
    call<AgentSettings>("PUT", "/ai/settings", input),
  testSettings: () => call<{ ok: boolean; reply?: string; error?: string }>("POST", "/ai/settings/test"),
  threads: (projectId?: ID | null) => call<AgentThreadItem[]>("GET", projectId ? `/ai/threads?project_id=${enc(projectId)}` : "/ai/threads"),
  thread: (id: ID) => call<AgentThread>("GET", `/ai/threads/${enc(id)}`),
  deleteThread: (id: ID) => call<void>("DELETE", `/ai/threads/${enc(id)}`),
  chat: (
    input: { thread_id?: ID; prompt: string; project_id?: ID; environment_id?: ID; service_id?: ID },
    onEvent: (e: AgentStreamEvent) => void,
    signal?: AbortSignal,
  ) => stream("/ai/chat", input, onEvent, signal),
  confirm: (input: { thread_id: ID; call_id: string; approve: boolean }, onEvent: (e: AgentStreamEvent) => void, signal?: AbortSignal) =>
    stream("/ai/confirm", input, onEvent, signal),
};
