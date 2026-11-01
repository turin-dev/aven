"use client";

/* Create forms behind the networking buttons, modeled on Railway's dashboard: Generate Domain (asks the port first, detected ports as candidates),
   Custom Domain (host + port; DNS records are shown on the new row) and TCP Proxy (container port). Same parts as the
   row edit panel: bordered input, port dropdown, info line + Cancel/primary. */

import { useState } from "react";
import { api, type CanvasService, type DetectedPort, type Environment } from "@/lib/api";
import { ErrorBox } from "../../ui";
import { PortPicker, initialPortChoice, type PortChoice } from "./PortPicker";
import { PanelFooter, SuffixInput, portOk, useNetRun } from "./parts";
import { checkDomain, normalizeHostInput, type HostProblem } from "./validate";
import { t, tr } from "@/lib/i18n";

export type FormKind = "generate" | "custom" | "tcp";

/** Why a custom domain is rejected, in words. */
export function hostProblemText(p: HostProblem): string {
  switch (p) {
    case "empty":
      return t("도메인을 입력하세요");
    case "port":
      return t("포트 없이 도메인만 입력하세요. 대상 포트는 아래에서 고릅니다");
    case "ip":
      return t("IP 주소는 도메인으로 추가할 수 없습니다. DNS 이름을 입력하세요");
    case "chars":
      return t("도메인에는 영문 소문자, 숫자, 하이픈(-), 점(.)만 쓸 수 있습니다");
    case "label_empty":
      return t("점(.)이 연달아 있거나 앞뒤에 있습니다");
    case "label_long":
      return t("점 사이의 각 부분은 63자를 넘을 수 없습니다");
    case "hyphen":
      return t("각 부분은 하이픈(-)으로 시작하거나 끝날 수 없습니다");
    case "tld":
      return t("마지막 부분(최상위 도메인)은 영문자 2자 이상이어야 합니다");
    case "single_label":
      return t("example.com처럼 점(.)이 있는 전체 도메인을 입력하세요");
    case "wildcard_position":
      return t("와일드카드(*)는 맨 앞에 *. 형태로만 쓸 수 있습니다");
    case "wildcard_short":
      return t("와일드카드는 *.example.com처럼 *. 뒤에 두 부분 이상이 필요합니다");
    case "too_long":
      return t("도메인은 253자를 넘을 수 없습니다");
    case "duplicate":
      return t("이 서비스에 이미 추가된 도메인입니다");
  }
}

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
  existing = [],
  onDone,
}: {
  kind: FormKind;
  svc: CanvasService;
  env: Environment;
  detected: DetectedPort[];
  servicePort: number;
  baseDomain?: string | null;
  /** Hosts already on this service (duplicate check). */
  existing?: string[];
  onDone: () => void;
}) {
  const [host, setHost] = useState("");
  const [port, setPort] = useState<PortChoice>(() => initialPortChoice(kind === "tcp" ? null : defaultPort(detected, servicePort), detected));
  const { run, busy, error } = useNetRun(svc, env);
  const h = normalizeHostInput(host);
  const problem = kind === "custom" ? checkDomain(h, existing) : null;
  // Show "enter a domain" only after the user typed something; an empty field just keeps the button disabled.
  const shownProblem = problem && problem !== "empty" ? problem : null;
  const hostValid = !problem;
  const canSubmit = portOk(port.text) && hostValid;
  const p = Number(port.text);
  // The user typed something the server will store differently (scheme, path, upper case, IDN): show what is saved.
  const rewritten = kind === "custom" && hostValid && h !== host.trim();
  const generatedSuffix = kind === "custom" && hostValid && !!baseDomain && h.endsWith(`.${baseDomain}`) && !h.startsWith("*.");
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
          <SuffixInput
            label={t("사용자 지정 도메인")}
            name="custom-domain"
            value={host}
            onChange={setHost}
            placeholder="app.example.com"
            autoFocus
            invalid={!!shownProblem}
            describedBy="custom-domain-help"
            className="grow"
          />
        </div>
      )}
      {kind === "custom" && (rewritten || generatedSuffix || h.startsWith("*.")) && (
        <ul id="custom-domain-help" className="space-y-1 text-xs leading-[18px] text-gray-600">
          {rewritten && <li>{tr("{host}으로 저장합니다", { host: <span className="font-mono text-foreground">{h}</span> })}</li>}
          {generatedSuffix && <li>{t("기본 도메인({domain}) 아래 이름이라 DNS 레코드 없이 생성 도메인으로 동작합니다", { domain: baseDomain ?? "" })}</li>}
          {hostValid && h.startsWith("*.") && <li>{t("와일드카드 도메인은 DNS에 * 레코드가 필요하고, 같은 이름의 구체적인 도메인이 있으면 그쪽이 우선합니다")}</li>}
        </ul>
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
              ? shownProblem
                ? hostProblemText(shownProblem)
                : t("추가하면 DNS 제공자에 넣을 레코드를 안내합니다")
              : t("서버의 공개 포트 하나가 이 컨테이너 포트로 연결됩니다")
        }
        tone={shownProblem ? "bad" : "info"}
        live
        onCancel={onDone}
        submitLabel={kind === "generate" ? t("도메인 생성") : kind === "custom" ? t("도메인 추가") : t("프록시 추가")}
        canSubmit={canSubmit}
        busy={busy === kind}
      />
    </form>
  );
}
