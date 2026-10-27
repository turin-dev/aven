/* Installs the fake backend by patching window.fetch and window.EventSource. Dev only. */

import { handle, openStream } from "./server";
import "./sandboxes";
import "./traces";

const latency = () => 90 + Math.random() * 160;

type Listener = (e: MessageEvent) => void;

class FakeEventSource {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  readonly CONNECTING = 0;
  readonly OPEN = 1;
  readonly CLOSED = 2;
  url: string;
  withCredentials: boolean;
  readyState = 0;
  onopen: ((e: Event) => void) | null = null;
  onerror: ((e: Event) => void) | null = null;
  onmessage: Listener | null = null;
  private listeners = new Map<string, Set<Listener>>();
  private stop: (() => void) | null = null;

  constructor(url: string | URL, init?: EventSourceInit) {
    this.url = String(url);
    this.withCredentials = !!init?.withCredentials;
    setTimeout(() => this.start(), 60 + Math.random() * 120);
  }

  private start() {
    if (this.readyState === 2) return;
    const u = new URL(this.url, window.location.origin);
    this.readyState = 1;
    this.onopen?.(new Event("open"));
    this.stop = openStream(
      u.pathname.replace(/^\/api/, ""),
      u.searchParams,
      (type, data) => {
        if (this.readyState === 2) return;
        const ev = new MessageEvent(type, { data });
        if (type === "message") this.onmessage?.(ev);
        this.listeners.get(type)?.forEach((fn) => fn(ev));
      },
      () => {
        // Servers close the stream after `done`; EventSource would report an error and reconnect.
        // Consumers close on `done`, so nothing to do here.
      },
    );
  }

  addEventListener(type: string, fn: Listener) {
    let s = this.listeners.get(type);
    if (!s) this.listeners.set(type, (s = new Set()));
    s.add(fn);
  }
  removeEventListener(type: string, fn: Listener) {
    this.listeners.get(type)?.delete(fn);
  }
  dispatchEvent() {
    return true;
  }
  close() {
    this.readyState = 2;
    this.stop?.();
  }
}

/** Stub of the service shell WebSocket: a tiny line-editing fake shell. */
class FakeExecSocket {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;
  readonly CONNECTING = 0;
  readonly OPEN = 1;
  readonly CLOSING = 2;
  readonly CLOSED = 3;
  url: string;
  readyState = 0;
  binaryType: BinaryType = "blob";
  onopen: ((e: Event) => void) | null = null;
  onmessage: ((e: MessageEvent) => void) | null = null;
  onclose: ((e: CloseEvent) => void) | null = null;
  onerror: ((e: Event) => void) | null = null;
  private line = "";
  private replica: string;
  private enc = new TextEncoder();
  private dec = new TextDecoder();

  constructor(url: string) {
    this.url = url;
    this.replica = new URL(url).searchParams.get("replica") || "aven-mock-1";
    setTimeout(() => {
      this.readyState = 1;
      this.onopen?.(new Event("open"));
      this.out(`\x1b[2mmock shell on ${this.replica}\x1b[0m\r\n`);
      this.prompt();
    }, 200);
  }

  private out(s: string) {
    if (this.readyState !== 1) return;
    const data = this.enc.encode(s).buffer;
    setTimeout(() => this.onmessage?.(new MessageEvent("message", { data })), 10);
  }
  private prompt() {
    this.out("\x1b[32mroot@app\x1b[0m:\x1b[34m/app\x1b[0m# ");
  }
  private run(cmd: string) {
    const [name, ...args] = cmd.trim().split(/\s+/);
    if (!name) return;
    const outputs: Record<string, string> = {
      ls: "\x1b[34mnode_modules\x1b[0m  package.json  \x1b[32mserver.js\x1b[0m  README.md",
      pwd: "/app",
      whoami: "root",
      env: "NODE_ENV=production\r\nPORT=8080",
      uname: "Linux",
    };
    if (name === "echo") this.out(args.join(" ") + "\r\n");
    else if (name === "clear") this.out("\x1b[H\x1b[2J");
    else if (outputs[name]) this.out(outputs[name] + "\r\n");
    else this.out(`sh: ${name}: not found\r\n`);
  }

  send(data: string | ArrayBufferLike | Blob | ArrayBufferView) {
    if (typeof data === "string") return; // resize etc.
    const bytes = data instanceof ArrayBuffer ? new Uint8Array(data) : ArrayBuffer.isView(data) ? new Uint8Array(data.buffer, data.byteOffset, data.byteLength) : null;
    if (!bytes) return;
    for (const ch of this.dec.decode(bytes)) {
      if (ch === "\r") {
        this.out("\r\n");
        if (this.line.trim() === "exit") {
          this.onmessage?.(new MessageEvent("message", { data: JSON.stringify({ type: "exit", code: 0 }) }));
          this.close(1000);
          return;
        }
        this.run(this.line);
        this.line = "";
        this.prompt();
      } else if (ch === "\x7f") {
        if (this.line) {
          this.line = this.line.slice(0, -1);
          this.out("\b \b");
        }
      } else if (ch === "\x03") {
        this.line = "";
        this.out("^C\r\n");
        this.prompt();
      } else if (ch >= " ") {
        this.line += ch;
        this.out(ch);
      }
    }
  }

  close(code = 1000) {
    if (this.readyState === 3) return;
    this.readyState = 3;
    this.onclose?.(new CloseEvent("close", { code }));
  }
  addEventListener() {}
  removeEventListener() {}
  dispatchEvent() {
    return true;
  }
}

export function install() {
  if (typeof window === "undefined" || (window as unknown as { __avenMock?: boolean }).__avenMock) return;
  (window as unknown as { __avenMock?: boolean }).__avenMock = true;
  const orig = window.fetch.bind(window);
  window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
    const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const u = new URL(raw, window.location.origin);
    if (u.origin !== window.location.origin || !u.pathname.startsWith("/api/")) return orig(input, init);
    const method = (init?.method ?? (input instanceof Request ? input.method : "GET")).toUpperCase();
    let body: unknown;
    if (typeof init?.body === "string" && init.body) body = JSON.parse(init.body);
    else if (init?.body instanceof FormData) body = init.body; // bucket upload
    await new Promise((r) => setTimeout(r, latency()));
    const res = handle(method, u.pathname.replace(/^\/api/, "").replace(/\/$/, ""), u.searchParams, body);
    if (res.status === 204 || res.status === 202 && res.body === undefined) return new Response(null, { status: res.status });
    return new Response(res.body === undefined ? null : JSON.stringify(res.body), {
      status: res.status,
      headers: { "Content-Type": "application/json" },
    });
  };
  (window as unknown as { EventSource: unknown }).EventSource = FakeEventSource;
  const OrigWS = window.WebSocket;
  const PatchedWS = function (url: string | URL, protocols?: string | string[]) {
    const u = new URL(String(url), window.location.href);
    if (u.host === window.location.host && /^\/api\/services\/[^/]+\/instances\/[^/]+\/exec$/.test(u.pathname)) {
      return new FakeExecSocket(u.href);
    }
    return new OrigWS(url, protocols);
  } as unknown as typeof WebSocket;
  Object.assign(PatchedWS, { CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 });
  window.WebSocket = PatchedWS;
  console.info("%caven mock backend active", "color:#b367e4");
}
