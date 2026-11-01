"use client";

import { useMemo, useState } from "react";
import { CheckCircle2, CircleSlash, Stethoscope, XCircle } from "lucide-react";
import { api, type ID, type NetworkDiagnosticCheck, type NetworkDiagnosticResult } from "@/lib/api";
import { fmtTime } from "@/lib/format";
import { t, tr } from "@/lib/i18n";
import { useCanvas } from "@/lib/queries";
import { Button, Dialog, ErrorBox, Field, Input, cx } from "../ui";
import { cleanDiagHost, isPort, parseResolved } from "./networking/validate";

type Kind = "dns" | "tcp";

/** One step of the result: state icon, a sentence saying what happened, and the raw tool output folded away. */
function Result({ kind, label, result, host, port }: { kind: Kind; label: string; result: NetworkDiagnosticCheck; host: string; port: number }) {
  const addrs = kind === "dns" && result.ok ? parseResolved(result.output) : [];
  const tone = !result.available ? "text-warn" : result.ok ? "text-success" : "text-danger";
  const Icon = !result.available ? CircleSlash : result.ok ? CheckCircle2 : XCircle;
  const summary = !result.available
    ? kind === "dns"
      ? t("이 이미지에는 getent나 nslookup이 없어 DNS를 확인하지 못했습니다.")
      : t("이 이미지에는 nc나 bash가 없어 TCP 연결을 확인하지 못했습니다.")
    : kind === "dns"
      ? result.ok
        ? addrs.length
          ? t("{host} → {count}개 주소로 풀립니다.", { host, count: addrs.length })
          : t("{host} 이름을 찾았습니다.", { host })
        : t("{host} 이름을 찾지 못했습니다. 서비스 이름, 환경, 철자를 확인하세요.", { host })
      : result.ok
        ? t("{host}:{port}에 연결했습니다.", { host, port })
        : t("{host}:{port}에 연결하지 못했습니다. 대상이 이 포트에서 0.0.0.0이나 ::로 listen하는지, 같은 환경인지 확인하세요.", { host, port });
  return (
    <div className="rounded-lg border border-gray-200 p-3" role="group" aria-label={label}>
      <div className="flex items-center justify-between gap-3">
        <span className="flex items-center gap-2 font-medium text-foreground">
          <Icon className={cx("size-4 shrink-0", tone)} aria-hidden="true" />
          {label}
        </span>
        <span className="text-xs tabular-nums text-gray-500">
          {result.duration_ms.toFixed(0)}ms{result.exit_code !== 0 && result.available ? ` · exit ${result.exit_code}` : ""}
        </span>
      </div>
      <p className={cx("mt-1.5 text-sm", result.ok ? "text-gray-600" : tone)}>{summary}</p>
      {addrs.length > 0 && (
        <ul className="mt-2 flex flex-wrap gap-1.5">
          {addrs.map((a) => (
            <li key={a} className="rounded border border-gray-200 bg-secondaryBg px-1.5 py-0.5 font-mono text-xs">
              {a}
            </li>
          ))}
        </ul>
      )}
      {result.output && (
        <details className="mt-2" open={!result.ok && result.available}>
          <summary className="cursor-pointer select-none text-xs text-gray-500 hover:text-foreground">{t("도구 출력")}</summary>
          <pre className="mt-1 max-h-36 overflow-auto whitespace-pre-wrap break-all font-mono text-xs text-gray-500">{result.output}</pre>
        </details>
      )}
    </div>
  );
}

/** Resolve a name and test a TCP connection from inside a running replica of the service. */
export function NetworkDiagnostics({ sid, eid }: { sid: ID; eid: ID }) {
  const [open, setOpen] = useState(false);
  const [host, setHost] = useState("");
  const [port, setPort] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [result, setResult] = useState<NetworkDiagnosticResult | null>(null);
  const canvas = useCanvas(open ? eid : null);
  const clean = cleanDiagHost(host);
  const hostBad = host.trim() !== "" && !clean;
  const portBad = port.trim() !== "" && !isPort(port.trim());
  const portNumber = port.trim() === "" ? 0 : Number(port);
  const valid = !!clean && !portBad;
  // Other services of the environment as suggestions: their private name and port.
  const suggestions = useMemo(
    () =>
      (canvas.data?.services ?? [])
        .filter((s) => s.id !== sid && s.instance)
        .map((s) => ({ host: s.instance.private_domain || s.name, port: s.instance.port ?? (Number(s.instance.variables?.PORT) || 0) })),
    [canvas.data, sid],
  );

  const run = async () => {
    if (!clean) return;
    setBusy(true);
    setError(null);
    setResult(null);
    try {
      setResult(await api.networkDiagnostics(sid, eid, { host: clean, port: portNumber }));
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  // A result belongs to the inputs it was run with; hide it once they change.
  const current = result && result.host === clean && result.port === portNumber ? result : null;

  return (
    <>
      <Button variant="outline" onClick={() => setOpen(true)} title={t("서비스 안에서 DNS 조회와 TCP 연결을 시험합니다")}>
        <Stethoscope />
        {t("네트워크 진단")}
      </Button>
      <Dialog
        open={open}
        onClose={() => !busy && setOpen(false)}
        title={t("네트워크 진단")}
        footer={
          <>
            <Button variant="ghost" onClick={() => setOpen(false)} disabled={busy}>
              {t("닫기")}
            </Button>
            <Button variant="primary" onClick={run} loading={busy} disabled={!valid}>
              {t("진단 실행")}
            </Button>
          </>
        }
      >
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            if (valid && !busy) void run();
          }}
        >
          <p className="text-sm text-gray-500">
            {t("실행 중인 replica 안에서 이름을 조회하고(getent/nslookup) TCP 연결을 시험합니다(nc/bash). 이미지에 도구가 없으면 “도구 없음”으로 표시됩니다.")}
          </p>
          <Field label={t("호스트")} hint={hostBad ? t("호스트 이름이나 IP 주소만 입력하세요(공백, 경로, 특수 문자 없이)") : t("사설 도메인(서비스.환경.aven.internal), 서비스 이름 또는 외부 호스트")}>
            <Input mono value={host} onChange={(e) => setHost(e.target.value)} placeholder="postgres.production.aven.internal" autoFocus invalid={hostBad} list={`diag-hosts-${sid}`} spellCheck={false} autoComplete="off" />
          </Field>
          <datalist id={`diag-hosts-${sid}`}>
            {suggestions.map((s) => (
              <option key={s.host} value={s.host} />
            ))}
          </datalist>
          {suggestions.length > 0 && !host && (
            <div className="flex flex-wrap gap-1.5">
              {suggestions.slice(0, 6).map((s) => (
                <button
                  key={s.host}
                  type="button"
                  onClick={() => {
                    setHost(s.host);
                    if (s.port) setPort(String(s.port));
                  }}
                  className="cursor-pointer rounded border border-gray-200 px-1.5 py-0.5 font-mono text-xs text-gray-600 hover:border-gray-400 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-700"
                >
                  {s.host}
                  {s.port ? `:${s.port}` : ""}
                </button>
              ))}
            </div>
          )}
          <Field label={t("포트")} hint={portBad ? t("포트는 1부터 65535 사이의 숫자입니다") : t("비우면 DNS만 확인합니다")}>
            <Input inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value.replace(/[^0-9]/g, "").slice(0, 5))} placeholder="5432" invalid={portBad} />
          </Field>
          <button type="submit" hidden />
          {error ? <ErrorBox error={error} /> : null}
          {current && (
            <div className="space-y-2" aria-live="polite">
              <div className="text-xs text-gray-500">
                {tr("검사 replica: {replica}", { replica: <code className="font-mono">{current.replica}</code> })}
                {" · "}
                {fmtTime(current.checked_at)}
              </div>
              <Result kind="dns" label={t("DNS 조회 · {host}", { host: current.host })} result={current.dns} host={current.host} port={current.port} />
              {!current.tcp.skipped && <Result kind="tcp" label={t("TCP 연결 · {host}:{port}", { host: current.host, port: current.port })} result={current.tcp} host={current.host} port={current.port} />}
            </div>
          )}
        </form>
      </Dialog>
    </>
  );
}
