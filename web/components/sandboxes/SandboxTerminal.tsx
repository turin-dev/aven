"use client";

import { useCallback, useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent } from "react";
import { Eraser, Plug, Unplug } from "lucide-react";
import type { ExecControl } from "@/lib/api";
import { sandboxShellUrl } from "@/lib/sandboxes";
import { Terminal, keyToBytes } from "@/lib/terminal";
import { renderLine } from "../panel/ShellTab";
import { Button, Kbd, cx } from "../ui";
import { t, tr } from "@/lib/i18n";

type Conn = "idle" | "connecting" | "open" | "closed";

/** Interactive shell in a sandbox over its shell WebSocket. The open connection keeps the sandbox from going idle. */
export function SandboxTerminal({ id, disabled }: { id: string; disabled?: boolean }) {
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
      setTick((n) => n + 1);
      const el = screen.current;
      if (el) el.scrollTop = el.scrollHeight;
    });
  }, []);

  const measure = useCallback((): [number, number] => {
    const el = screen.current;
    const p = probe.current;
    if (!el || !p) return [80, 24];
    const r = p.getBoundingClientRect();
    const cw = r.width / 10 || 8;
    const ch = r.height || 18;
    return [Math.min(1000, Math.max(20, Math.floor((el.clientWidth - 24) / cw))), Math.min(1000, Math.max(5, Math.floor((el.clientHeight - 24) / ch)))];
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
    const w = new WebSocket(sandboxShellUrl(id, cols, rows));
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
      if (ev.code !== 1000) setNotice((n) => n ?? t("연결이 끊겼습니다. 샌드박스가 실행 중인지 확인하세요."));
      ws.current = null;
    };
  }, [disconnect, id, measure, redraw]);

  useEffect(() => () => disconnect(), [disconnect]);
  // 다른 샌드박스로 바뀌면 연결과 화면을 새로 시작한다.
  useEffect(() => {
    disconnect();
    term.current.reset();
    setConn("idle");
    setNotice(null);
    redraw();
  }, [id, disconnect, redraw]);

  useLayoutEffect(() => {
    const el = screen.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const ro = new ResizeObserver(() => {
      clearTimeout(timer);
      timer = setTimeout(() => {
        const [cols, rows] = measure();
        if (cols === term.current.cols && rows === term.current.rows) return;
        term.current.resize(cols, rows);
        const w = ws.current;
        if (w && w.readyState === WebSocket.OPEN) w.send(JSON.stringify({ type: "resize", cols, rows }));
      }, 120);
    });
    ro.observe(el);
    return () => {
      clearTimeout(timer);
      ro.disconnect();
    };
  }, [measure]);

  const onKeyDown = (e: KeyboardEvent) => {
    if (conn !== "open") return;
    const b = keyToBytes(e);
    if (b === null) return;
    e.preventDefault();
    e.stopPropagation();
    send(b);
  };

  const tm = term.current;
  const runs = tm.runs();

  return (
    <div className="flex min-h-[360px] flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        {conn === "open" || conn === "connecting" ? (
          <Button size="sm" onClick={disconnect}>
            <Unplug /> {t("연결 끊기")}
          </Button>
        ) : (
          <Button size="sm" variant="primary" onClick={connect} disabled={disabled}>
            <Plug /> {conn === "closed" ? t("다시 연결") : t("연결")}
          </Button>
        )}
        <Button
          size="sm"
          variant="ghost"
          onClick={() => {
            term.current.reset();
            redraw();
            screen.current?.focus();
          }}
        >
          <Eraser /> {t("화면 지우기")}
        </Button>
        <span className="ml-auto text-xs text-gray-400" aria-live="polite">
          {conn === "connecting" ? t("연결 중…") : conn === "open" ? t("연결됨") : conn === "closed" ? t("연결 끊김") : ""}
        </span>
      </div>
      {notice && (
        <div role="status" className="text-sm text-gray-600">
          {notice}
        </div>
      )}
      <div
        ref={screen}
        tabIndex={0}
        role="application"
        aria-label={t("샌드박스 {id} 터미널. Shift+Tab으로 빠져나갑니다", { id })}
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
          "relative h-[420px] overflow-auto rounded-lg border border-gray-200 bg-[#0f0e14] p-3 font-mono text-sm leading-[1.4] text-[#e5e5e5] outline-none",
          "focus-visible:border-pink-500 focus-visible:ring-2 focus-visible:ring-pink-500/25",
        )}
      >
        <span ref={probe} aria-hidden="true" className="invisible absolute left-0 top-0 whitespace-pre">
          MMMMMMMMMM
        </span>
        <pre aria-live="off" className="m-0 whitespace-pre">
          {runs.map((line, y) => (
            <div key={y} className="min-h-[1.4em]">
              {renderLine(line, y === tm.cy && conn === "open" ? tm.cx : -1)}
            </div>
          ))}
        </pre>
        {conn === "idle" && (
          <div className="absolute inset-0 flex items-center justify-center px-6 text-center text-sm text-gray-500">
            {t("연결을 누르면 샌드박스에서 셸이 열립니다. 연결되어 있는 동안에는 유휴 시간으로 삭제되지 않습니다.")}
          </div>
        )}
      </div>
      <div className="text-xs text-gray-400">
        {tr("{ctrlC} 등은 셸로 전달됩니다. 붙여넣기는 {paste}. CLI: {cli}", {
          ctrlC: <><Kbd>Ctrl</Kbd>+<Kbd>C</Kbd></>,
          paste: <><Kbd>Ctrl</Kbd>+<Kbd>Shift</Kbd>+<Kbd>V</Kbd></>,
          cli: <code className="font-mono">aven sandbox ssh --id {id}</code>,
        })}
      </div>
    </div>
  );
}
