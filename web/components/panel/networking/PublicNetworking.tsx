"use client";

/* Public networking, modeled on Railway's dashboard: the bordered
   `divide-y` list (domains + TCP proxy, each with a sliding edit panel) and the Generate Domain / Custom Domain /
   TCP Proxy buttons. */

import { useState } from "react";
import { Plus, Zap } from "lucide-react";
import { api, type CanvasService, type Environment, type InstanceNetworking, type ServiceInstance } from "@/lib/api";
import { useNodes, useSystem } from "@/lib/queries";
import { Button, ErrorBox, useConfirm } from "../../ui";
import { NetworkingGuardrailNotice, useGuardrails } from "../../AccessControlsSettings";
import { DomainForm, type FormKind } from "./DomainForm";
import { DomainRow } from "./DomainRow";
import { PortDetectionContext } from "./PortPicker";
import { TcpProxyRow } from "./TcpProxyRow";
import { NetSection, useNetRun } from "./parts";
import { t, tr } from "@/lib/i18n";

const ACCENT_ICON = "icon-container icon-md text-2xl !w-4 !h-4 text-pink-500 [&_svg]:!size-4";

export function PublicNetworking({ svc, env, inst, view }: { svc: CanvasService; env: Environment; inst: ServiceInstance; view: InstanceNetworking }) {
  const sys = useSystem();
  const nodes = useNodes();
  const guard = useGuardrails(); // 작업 공간 guardrail: 생성 도메인·TCP 프록시는 작업 공간 admin만
  const { confirm, node: confirmNode } = useConfirm();
  const { run, error } = useNetRun(svc, env);
  const [form, setForm] = useState<FormKind | null>(null);
  const baseDomain = sys.data?.base_domain;
  const host = nodes.data?.find((n) => n.id === inst.node_id);
  const ipv6Only = !!host && !host.address.ipv4 && !!host.address.ipv6;
  const tcp = view.tcp_proxy;
  const rows = view.domains.length + (tcp.enabled ? 1 : 0);
  const detected = view.detected_ports;
  const servicePort = view.effective_port;
  const genDisabled = (sys.data ? !baseDomain : false) || guard.generatedDomains;
  const tcpDisabled = guard.tcpProxy || !tcp.available;
  const tcpReason = guard.tcpProxy ? t("작업 공간 정책으로 제한되어 있습니다") : !tcp.available ? (tcp.reason ?? t("이 노드에서는 TCP 프록시를 열 수 없습니다")) : undefined;

  return (
    <PortDetectionContext.Provider value={view.port_detection}>
      <NetSection id="networking-public" title={t("공개 네트워킹")} keywords="public networking domain 도메인 tcp proxy 프록시 dns https 인증서 cloudflare port 포트">
        <div className="flex flex-col space-y-4">
          <h2 className="text-sm text-gray-600">{rows > 0 ? t("다음 도메인으로 HTTP를 통해 애플리케이션에 접속합니다") : t("도메인을 생성하거나 연결해 HTTP로 애플리케이션을 공개하세요")}</h2>
          {rows > 0 && (
            <div className="divide-y divide-gray-200 overflow-hidden rounded-lg border border-gray-200">
              {view.domains.map((d) => (
                <DomainRow
                  key={d.host}
                  svc={svc}
                  env={env}
                  inst={inst}
                  d={d}
                  detected={detected}
                  servicePort={servicePort}
                  ipv6Only={ipv6Only}
                  onDelete={() =>
                    confirm({
                      title: t("도메인 삭제"),
                      message: tr("{host}로 들어오는 요청을 더 이상 이 서비스로 보내지 않습니다.", {
                        host: <span className="font-mono text-foreground">{d.host}</span>,
                      }),
                      confirmLabel: t("삭제"),
                      danger: true,
                      run: () => run(`del-${d.host}`, () => api.deleteDomain(svc.id, env.id, d.host)),
                    })
                  }
                />
              ))}
              {tcp.enabled && (
                <TcpProxyRow
                  svc={svc}
                  env={env}
                  proxy={tcp}
                  detected={detected}
                  servicePort={servicePort}
                  onDelete={() =>
                    confirm({
                      title: t("TCP 프록시 삭제"),
                      message: t("공개 포트를 닫습니다. 외부에서 이 포트로 접속하던 클라이언트는 연결이 끊깁니다."),
                      confirmLabel: t("삭제"),
                      danger: true,
                      run: () => run("tcp-del", () => api.deleteTcpProxy(svc.id, env.id)),
                    })
                  }
                />
              )}
            </div>
          )}
          {form && <DomainForm key={form} kind={form} svc={svc} env={env} detected={detected} servicePort={servicePort} baseDomain={baseDomain} onDone={() => setForm(null)} />}
          <NetworkingGuardrailNotice domains={guard.generatedDomains} tcp={guard.tcpProxy && !tcp.enabled} />
          {form === null && (
            <div className="flex flex-wrap gap-2">
              <div className="flex flex-col space-y-2">
                <div className="flex space-x-2">
                  <Button
                    variant="accent"
                    onClick={() => setForm("generate")}
                    disabled={genDisabled}
                    title={guard.generatedDomains ? t("작업 공간 정책으로 제한되어 있습니다") : sys.data && !baseDomain ? t("서버에 KEEL_BASE_DOMAIN이 설정되지 않았습니다") : undefined}
                  >
                    <span className={ACCENT_ICON} aria-hidden="true">
                      <Zap />
                    </span>
                    <span className="inline-block min-w-0">{t("도메인 생성")}</span>
                  </Button>
                  <Button variant="accent" name="사용자 지정 도메인 추가" onClick={() => setForm("custom")}>
                    <span className={ACCENT_ICON} aria-hidden="true">
                      <Plus />
                    </span>
                    <span className="inline-block min-w-0">{t("사용자 지정 도메인")}</span>
                  </Button>
                </div>
              </div>
              {!tcp.enabled && (
                <Button variant="accent" disabled={tcpDisabled} title={tcpReason} onClick={() => setForm("tcp")}>
                  <span className={ACCENT_ICON} aria-hidden="true">
                    <Plus />
                  </span>
                  <span className="inline-block min-w-0">{t("TCP 프록시")}</span>
                </Button>
              )}
            </div>
          )}
          {form === null && !tcp.enabled && !tcp.available && tcp.reason && <p className="text-xs leading-[18px] text-gray-600">{t("TCP 프록시: {reason}", { reason: tcp.reason })}</p>}
          {sys.data && !baseDomain && <p className="text-xs leading-[18px] text-gray-600">{t("서버에 KEEL_BASE_DOMAIN이 없어 도메인을 자동 생성할 수 없습니다. 사용자 지정 도메인은 쓸 수 있습니다.")}</p>}
          {error ? <ErrorBox error={error} /> : null}
          {confirmNode}
        </div>
      </NetSection>
    </PortDetectionContext.Provider>
  );
}
