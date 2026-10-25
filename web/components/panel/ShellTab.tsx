"use client";

import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import { Eraser, Info, Plug, TerminalSquare, Unplug } from "lucide-react";
import { execUrl, type CanvasService, type Environment, type ExecControl } from "@/lib/api";
import { Terminal, keyToBytes, type Run, type Style } from "@/lib/terminal";
import { Button, Kbd, Select, cx } from "../ui";
import { t, tr } from "@/lib/i18n";

type Conn = "idle" | "connecting" | "open" | "closed";

function styleOf(st: Style): CSSProperties | undefined {
  let fg = st.fg;
  let bg = st.bg;
  if (st.inverse) [fg, bg] = [bg ?? "#e5e5e5", fg ?? "#0f0e14"];
  if (!fg && !bg && !st.bold && !st.dim && !st.italic && !st.underline) return undefined;
  return {
    color: fg ?? undefined,
    backgroundColor: bg ?? undefined,
    fontWeight: st.bold ? 700 : undefined,
    opacity: st.dim ? 0.7 : undefined,
    fontStyle: st.italic ? "italic" : undefined,
    textDecoration: st.underline ? "underline" : undefined,
  };
}

const CURSOR: CSSProperties = { backgroundColor: "rgba(229,229,229,0.75)", color: "#0f0e14" };

/** One line as styled spans; the cell at cursorX (if >= 0) is drawn as a block cursor. */
export function renderLine(line: Run[], cursorX: number): ReactNode[] {
  const out: ReactNode[] = [];
  let x = 0;
  line.forEach((r, i) => {
    const end = x + r.text.length;
    if (cursorX >= x && cursorX < end) {
      const k = cursorX - x;
      if (k > 0) out.push(<span key={`${i}a`} style={styleOf(r.st)}>{r.text.slice(0, k)}</span>);
      out.push(<span key={`${i}c`} style={CURSOR}>{r.text[k]}</span>);
      if (k + 1 < r.text.length) out.push(<span key={`${i}b`} style={styleOf(r.st)}>{r.text.slice(k + 1)}</span>);
    } else {
      out.push(<span key={i} style={styleOf(r.st)}>{r.text}</span>);
    }
    x = end;
  });
  if (cursorX >= x) {
    out.push(<span key="pad">{" ".repeat(cursorX - x)}</span>);
    out.push(<span key="cur" style={CURSOR}> </span>);
  }
  return out;
}

/** Service shell: an interactive TTY in a running replica over the exec WebSocket. */
export function ShellTab({ svc, env }: { svc: CanvasService; env: Environment }) {
  const inst = svc.instance;
  const dep = svc.latest_deployment && svc.latest_deployment.id === inst.current_deployment_id ? svc.latest_deployment : null;
  const replicas = dep?.replicas.filter((r) => r.status === "running" || r.status === "starting") ?? [];
  const [replica, setReplica] = useState("");
  const [conn, setConn] = useState<Conn>("idle");
  const [notice, setNotice] = useState<string | null>(null);
  const [, setTick] = useState(0);
  const term = useRef(new Terminal());
  const ws = useRef<WebSocket | null>(null);
  const screen = useRef<HTMLDivElement>(null);
  const probe = useRef<HTMLSpanElement>(null);
  const raf = useRef(0);
  const enc = useRef(new TextEncoder());

  const redraw = useCallback(() => {
    if (raf.current) return;
    raf.current = requestAnimationFrame(() => {
      raf.current = 0;
      setTick((t) => t + 1);
      const el = screen.current;
      if (el) el.scrollTop = el.scrollHeight;
    });
  }, []);

  /** Terminal size from the element size and the width of one character. */
  const measure = useCallback((): [number, number] => {
    const el = screen.current;
    const p = probe.current;
    if (!el || !p) return [80, 24];
    const r = p.getBoundingClientRect();
    const cw = r.width / 10 || 8;
    const ch = r.height || 18;
    const cols = Math.max(20, Math.floor((el.clientWidth - 24) / cw));
    const rows = Math.max(5, Math.floor((el.clientHeight - 24) / ch));
    return [Math.min(cols, 1000), Math.min(rows, 1000)];
  }, []);

  const send = useCallback((s: string) => {
    const w = ws.current;
    if (w && w.readyState === WebSocket.OPEN) w.send(enc.current.encode(s));
  }, []);

  const disconnect = useCallback(() => {
    ws.current?.close(1000);
    ws.current = null;
  }, []);

  const connect = useCallback(() => {
    disconnect();
    const [cols, rows] = measure();
    term.current.resize(cols, rows);
    setNotice(null);
    setConn("connecting");
    const w = new WebSocket(execUrl(svc.id, env.id, { replica: replica || undefined, cols, rows }));
    w.binaryType = "arraybuffer";
    const dec = new TextDecoder();
    ws.current = w;
    w.onopen = () => {
      if (ws.current !== w) return;
      setConn("open");
      screen.current?.focus();
    };
    w.onmessage = (ev) => {
      if (ws.current !== w) return;
      if (typeof ev.data === "string") {
        try {
          const c = JSON.parse(ev.data) as ExecControl;
          if (c.type === "exit") setNotice(t("셸이 종료되었습니다 (exit {code})", { code: c.code }));
          else if (c.type === "error") setNotice(c.message);
        } catch {
          /* ignore */
        }
        return;
      }
      term.current.write(dec.decode(new Uint8Array(ev.data as ArrayBuffer), { stream: true }));
      redraw();
    };
    w.onclose = (ev) => {
      if (ws.current !== w && ws.current !== null) return;
      setConn("closed");
      if (ev.code !== 1000) setNotice((n) => n ?? t("연결이 끊겼습니다. 서비스가 실행 중인지 확인하세요."));
      ws.current = null;
    };
  }, [disconnect, env.id, measure, redraw, replica, svc.id]);

  useEffect(() => () => disconnect(), [disconnect]);

  // 크기가 바뀌면 resize 제어 메시지를 보낸다
  useLayoutEffect(() => {
    const el = screen.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    let t: ReturnType<typeof setTimeout> | undefined;
    const ro = new ResizeObserver(() => {
      clearTimeout(t);
      t = setTimeout(() => {
        const [cols, rows] = measure();
        if (cols === term.current.cols && rows === term.current.rows) return;
        term.current.resize(cols, rows);
        const w = ws.current;
        if (w && w.readyState === WebSocket.OPEN) w.send(JSON.stringify({ type: "resize", cols, rows }));
      }, 120);
    });
    ro.observe(el);
    return () => {
      clearTimeout(t);
      ro.disconnect();
    };
  }, [measure]);

  const onKeyDown = (e: KeyboardEvent) => {
    if (conn !== "open") return;
    const b = keyToBytes(e);
    if (b === null) return;
    e.preventDefault();
    e.stopPropagation(); // Esc가 패널을 닫지 않게
    send(b);
  };

  const runs = term.current.runs();
  const tm = term.current;
  const cursorLine = tm.cy;

  return (
    <div className="flex h-full flex-col gap-3 pb-8">
      <div className="flex flex-wrap items-center gap-2">
        <label className="mr-1 flex items-center gap-2 text-sm text-gray-600">
          {t("레플리카|one")}
          <Select
            value={replica}
            onChange={(e) => setReplica(e.target.value)}
            className="w-auto min-w-[180px] [&_button]:!h-[34px] [&_button]:!py-1.5 [&_button]:!text-sm"
            disabled={conn === "open" || conn === "connecting"}
          >
            <option value="">{replicas.length ? t("첫 번째") : t("자동")}</option>
            {replicas.map((r) => (
              <option key={r.name} value={r.name}>
                {r.name}
              </option>
            ))}
          </Select>
        </label>
        {conn === "open" || conn === "connecting" ? (
          <Button variant="outline" onClick={disconnect}>
            <Unplug /> {t("연결 끊기")}
          </Button>
        ) : (
          <Button variant="primary" onClick={connect} disabled={!inst.current_deployment_id}>
            <Plug /> {conn === "closed" ? t("다시 연결") : t("연결")}
          </Button>
        )}
        <Button
          variant="ghost"
          onClick={() => {
            term.current.reset();
            redraw();
            screen.current?.focus();
          }}
        >
          <Eraser /> {t("화면 지우기")}
        </Button>
        <span className="ml-auto flex items-center gap-2 text-sm text-gray-500" aria-live="polite">
          {conn !== "idle" && (
            <span
              aria-hidden
              className={cx(
                "size-2 rounded-full",
                conn === "open" ? "bg-green-600" : conn === "connecting" ? "animate-soft-pulse bg-yellow-600" : "bg-gray-400",
              )}
            />
          )}
          {conn === "connecting" ? t("연결 중…") : conn === "open" ? t("연결됨") : conn === "closed" ? t("연결 끊김") : ""}
        </span>
      </div>
      {notice && (
        <div role="status" className="flex items-center rounded-md bg-neutral-surface p-3 text-sm text-gray-600">
          <Info className="mr-3 size-4 shrink-0 text-gray-500" />
          {notice}
        </div>
      )}
      <div
        ref={screen}
        tabIndex={0}
        role="application"
        aria-label={t("{service} 셸 터미널 ({env}). Shift+Tab으로 빠져나갑니다", { service: svc.name, env: env.name })}
        aria-roledescription={t("터미널")}
        onKeyDown={onKeyDown}
        onPaste={(e) => {
          if (conn !== "open") return;
          e.preventDefault();
          send(e.clipboardData.getData("text").replace(/\r?\n/g, "\r"));
        }}
        onClick={() => {
          if (!window.getSelection()?.toString()) screen.current?.focus();
        }}
        data-noscroll
        className={cx(
          "relative min-h-[320px] flex-1 overflow-auto rounded-lg border border-gray-200 bg-[#0f0e14] p-3 font-mono text-sm leading-[1.4] text-[#e5e5e5] outline-none",
          "focus-visible:border-pink-500 focus-visible:ring-2 focus-visible:ring-pink-500/25",
        )}
      >
        <span ref={probe} aria-hidden="true" className="invisible absolute left-0 top-0 whitespace-pre">
          MMMMMMMMMM
        </span>
        <pre aria-live="off" className="m-0 whitespace-pre">
          {runs.map((line, y) => (
            <div key={y} className="min-h-[1.4em]">
              {renderLine(line, y === cursorLine && conn === "open" ? tm.cx : -1)}
            </div>
          ))}
        </pre>
        {conn === "idle" && (
          <div className="absolute inset-0 flex flex-col items-center justify-center gap-1.5 px-6 text-center font-sans">
            <TerminalSquare className="mb-1 size-6 text-gray-500" aria-hidden />
            {inst.current_deployment_id ? (
              <>
                <p className="text-sm font-semibold text-[#e5e5e5]">{t("연결을 누르면 {replica}에서 셸이 열립니다", { replica: replica || t("첫 번째 레플리카") })}</p>
                <p className="max-w-[420px] text-xs text-gray-500">{t("입력한 명령은 기록되지 않고, 셸을 연 사실만 활동 기록에 남습니다.")}</p>
              </>
            ) : (
              <>
                <p className="text-sm font-semibold text-[#e5e5e5]">{t("실행 중인 배포가 없습니다")}</p>
                <p className="max-w-[420px] text-xs text-gray-500">{t("서비스를 배포하면 실행 중인 레플리카에서 셸을 열 수 있습니다.")}</p>
              </>
            )}
          </div>
        )}
      </div>
      <div className="text-xs text-gray-500">
        {tr("{ctrlC} 등은 셸로 전달됩니다. 복사는 선택 후 {copy}, 붙여넣기는 {paste}. 30분 동안 입출력이 없으면 연결이 끊깁니다. {cli}", {
          ctrlC: <><Kbd>Ctrl</Kbd>+<Kbd>C</Kbd></>,
          copy: <><Kbd>Ctrl</Kbd>+<Kbd>Shift</Kbd>+<Kbd>C</Kbd></>,
          paste: <><Kbd>Ctrl</Kbd>+<Kbd>Shift</Kbd>+<Kbd>V</Kbd></>,
          cli: <span className="whitespace-nowrap">CLI: <code className="font-mono text-pink-600">aven ssh</code></span>,
        })}
      </div>
    </div>
  );
}
