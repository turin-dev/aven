"use client";

import { useState } from "react";
import { Stethoscope } from "lucide-react";
import { api, type ID, type NetworkDiagnosticCheck, type NetworkDiagnosticResult } from "@/lib/api";
import { t, tr } from "@/lib/i18n";
import { Button, Dialog, ErrorBox, Field, Input, cx } from "../ui";

function Result({ label, result }: { label: string; result: NetworkDiagnosticCheck }) {
  const state = !result.available ? t("도구 없음") : result.ok ? t("성공") : t("실패");
  return (
    <div className="rounded-lg border border-gray-200 p-3">
      <div className="flex items-center justify-between gap-3">
        <span className="font-medium text-foreground">{label}</span>
        <span className={cx("tabular-nums", !result.available ? "text-warn" : result.ok ? "text-success" : "text-danger")}>
          {state} · {result.duration_ms.toFixed(0)}ms
        </span>
      </div>
      {result.output && <pre className="mt-2 max-h-36 overflow-auto whitespace-pre-wrap break-all font-mono text-xs text-gray-500">{result.output}</pre>}
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
  const portNumber = port.trim() === "" ? 0 : Number(port);
  const validPort = Number.isInteger(portNumber) && portNumber >= 0 && portNumber <= 65535;
  const valid = host.trim() !== "" && validPort;

  const run = async () => {
    setBusy(true);
    setError(null);
    setResult(null);
    try {
      setResult(await api.networkDiagnostics(sid, eid, { host: host.trim(), port: portNumber }));
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };

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
            if (valid && !busy) run();
          }}
        >
          <p className="text-sm text-gray-500">
            {t("실행 중인 replica 안에서 이름을 조회하고(getent/nslookup) TCP 연결을 시험합니다(nc/bash). 이미지에 도구가 없으면 “도구 없음”으로 표시됩니다.")}
          </p>
          <Field label={t("호스트")} hint={t("사설 도메인(서비스.환경.keel.internal), 서비스 이름 또는 외부 호스트")}>
            <Input mono value={host} onChange={(e) => setHost(e.target.value)} placeholder="postgres.production.keel.internal" autoFocus />
          </Field>
          <Field label={t("포트")} hint={t("비우면 DNS만 확인합니다")}>
            <Input type="number" min={1} max={65535} value={port} onChange={(e) => setPort(e.target.value)} placeholder="5432" invalid={!validPort} />
          </Field>
          <button type="submit" hidden />
          {error ? <ErrorBox error={error} /> : null}
          {result && (
            <div className="space-y-2">
              <div className="text-xs text-gray-500">
                {tr("검사 replica: {replica}", { replica: <code className="font-mono">{result.replica}</code> })}
              </div>
              <Result label={t("DNS 조회 · {host}", { host: result.host })} result={result.dns} />
              {!result.tcp.skipped && <Result label={t("TCP 연결 · {host}:{port}", { host: result.host, port: result.port })} result={result.tcp} />}
            </div>
          )}
        </form>
      </Dialog>
    </>
  );
}
