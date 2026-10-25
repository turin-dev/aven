"use client";

/* Create forms behind the networking buttons, modeled on Railway's dashboard: Generate Domain (asks the port first, detected ports as candidates),
   Custom Domain (host + port; DNS records are shown on the new row) and TCP Proxy (container port). Same parts as the
   row edit panel: bordered input, port dropdown, info line + Cancel/primary. */

import { useState } from "react";
import { api, type CanvasService, type DetectedPort, type Environment } from "@/lib/api";
import { ErrorBox } from "../../ui";
import { PortPicker, initialPortChoice, type PortChoice } from "./PortPicker";
import { PanelFooter, SuffixInput, portOk, useNetRun } from "./parts";
import { t } from "@/lib/i18n";

export type FormKind = "generate" | "custom" | "tcp";

const HOST_RE = /^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/;

/** Best first guess: a port the app is really listening on, else the service port. */
export function defaultPort(detected: DetectedPort[], servicePort: number) {
  return detected.find((d) => d.source === "listening")?.port ?? servicePort;
}

export function DomainForm({
  kind,
  svc,
  env,
  detected,
  servicePort,
  baseDomain,
  onDone,
}: {
  kind: FormKind;
  svc: CanvasService;
  env: Environment;
  detected: DetectedPort[];
  servicePort: number;
  baseDomain?: string | null;
  onDone: () => void;
}) {
  const [host, setHost] = useState("");
  const [port, setPort] = useState<PortChoice>(() => initialPortChoice(kind === "tcp" ? null : defaultPort(detected, servicePort), detected));
  const { run, busy, error } = useNetRun(svc, env);
  const h = host.trim().toLowerCase().replace(/^https?:\/\//, "").replace(/\/.*$/, "");
  const hostValid = kind !== "custom" || HOST_RE.test(h);
  const canSubmit = portOk(port.text) && hostValid && (kind !== "custom" || !!h);
  const p = Number(port.text);
  return (
    <form
      className="space-y-4 rounded-lg border border-gray-200 p-4"
      onSubmit={async (e) => {
        e.preventDefault();
        if (!canSubmit) return;
        const ok = await run(kind, () =>
          kind === "tcp" ? api.addTcpProxy(svc.id, env.id, p) : api.addDomain(svc.id, env.id, kind === "custom" ? { host: h, target_port: p } : { target_port: p }),
        );
        if (ok) onDone();
      }}
    >
      {kind === "custom" && (
        <div className="flex items-center gap-4">
          <SuffixInput label={t("사용자 지정 도메인")} name="custom-domain" value={host} onChange={setHost} placeholder="example.com" autoFocus invalid={!!h && !hostValid} className="grow" />
        </div>
      )}
      {kind !== "custom" && <p className="text-sm font-semibold">{kind === "generate" ? t("도메인 생성") : t("TCP 프록시")}</p>}
      <PortPicker id={`new-${kind}`} value={port} onChange={setPort} detected={detected} servicePort={servicePort} />
      {error ? <ErrorBox error={error} /> : null}
      <PanelFooter
        message={
          kind === "generate"
            ? baseDomain
              ? t("*.{domain} 와일드카드 DNS가 이 서버를 가리켜야 접속됩니다", { domain: baseDomain })
              : t("앱이 listen하는 포트를 고르세요")
            : kind === "custom"
              ? !!h && !hostValid
                ? t("올바른 도메인을 입력하세요")
                : t("추가하면 DNS 제공자에 넣을 레코드를 안내합니다")
              : t("서버의 공개 포트 하나가 이 컨테이너 포트로 연결됩니다")
        }
        tone={kind === "custom" && !!h && !hostValid ? "bad" : "info"}
        onCancel={onDone}
        submitLabel={kind === "generate" ? t("도메인 생성") : kind === "custom" ? t("도메인 추가") : t("프록시 추가")}
        canSubmit={canSubmit}
        busy={busy === kind}
      />
    </form>
  );
}
