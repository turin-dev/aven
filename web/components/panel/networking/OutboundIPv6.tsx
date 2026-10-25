"use client";

/* Outbound IPv6 switch card and static outbound IPs, modeled on Railway's dashboard (the addresses this
   service's outbound traffic leaves from — on keel they are the node's public addresses, so they don't change). */

import { api, type CanvasService, type Environment, type InstanceNetworking } from "@/lib/api";
import { t } from "@/lib/i18n";
import { ErrorBox } from "../../ui";
import { Hint, ToggleCard } from "../settings/primitives";
import { CopyBtn, NetSection, useNetRun } from "./parts";

export function OutboundIPv6({ svc, env, view }: { svc: CanvasService; env: Environment; view: InstanceNetworking }) {
  const { run, busy, error } = useNetRun(svc, env);
  const o = view.outbound;
  // 끄기는 노드가 차단 규칙(ip6tables DOCKER-USER)을 지원할 때만 된다. 다시 켜는 것은 항상 된다.
  const cannotBlock = o.ipv6_enabled && o.enforce_supported === false;
  return (
    <NetSection id="ipv6-egress" title={t("아웃바운드 IPv6")} keywords="outbound ipv6 egress 외부 연결" description={t("이 서비스가 IPv6 주소로 외부에 연결할 수 있게 합니다.")}>
      <ToggleCard
        checked={o.ipv6_enabled}
        disabled={cannotBlock || busy === "ipv6"}
        onChange={(v) => void run("ipv6", () => api.updateInstance(svc.id, env.id, { outbound_ipv6: v }))}
        label={t("아웃바운드 IPv6 사용")}
      />
      {o.node_ipv6_egress === false && (
        <Hint tone="warn">{t("이 노드의 컨테이너는 IPv6로 외부에 연결할 수 없습니다{reason}", { reason: o.egress_reason ? `: ${o.egress_reason}` : "." })}</Hint>
      )}
      {o.node_ipv6_egress === null && (
        <Hint>{t("노드의 IPv6 연결 상태를 확인하지 못했습니다{reason}", { reason: o.egress_reason ? `: ${o.egress_reason}` : "." })}</Hint>
      )}
      {cannotBlock && <Hint>{t("이 노드에서는 아웃바운드 IPv6를 끌 수 없습니다{reason}", { reason: o.enforce_reason ? `: ${o.enforce_reason}` : "." })}</Hint>}
      {!o.ipv6_enabled && <Hint>{t("IPv6로는 사설 주소(fc00::/7, 같은 환경과 노드 간 네트워크)에만 연결할 수 있습니다. IPv4는 그대로입니다.")}</Hint>}
      {error ? <ErrorBox className="mt-2" error={error} /> : null}
    </NetSection>
  );
}

export function StaticOutboundIPs({ view }: { view: InstanceNetworking }) {
  const ips = [view.outbound.static_ips.ipv6, view.outbound.static_ips.ipv4].filter((x): x is string => !!x);
  return (
    <NetSection id="static-ips" title={t("고정 아웃바운드 IP")} keywords="static outbound ip egress 고정 ip 화이트리스트 allowlist" description={t("이 서비스가 외부로 연결할 때 쓰는 주소입니다. 노드의 공인 IP라 바뀌지 않으니 외부 방화벽 허용 목록에 넣을 수 있습니다.")}>
      {ips.length ? (
        <div className="divide-y divide-gray-200 rounded-lg border border-gray-200">
          {ips.map((ip) => (
            <div key={ip} className="group flex items-center px-4 py-3 first:rounded-t-lg last:rounded-b-lg hover:bg-secondaryBg dark:hover:bg-gray-100">
              <span className="mr-3 w-9 shrink-0 text-xs font-semibold text-gray-500">{ip.includes(":") ? "IPv6" : "IPv4"}</span>
              <span className="min-w-0 flex-1 truncate font-mono text-sm">{ip}</span>
              <CopyBtn value={ip} />
            </div>
          ))}
        </div>
      ) : (
        <p className="text-sm text-gray-500">{t("이 노드에는 공인 IP가 없습니다. 외부로 나가는 연결은 노드의 NAT나 터널을 거칩니다.")}</p>
      )}
    </NetSection>
  );
}
