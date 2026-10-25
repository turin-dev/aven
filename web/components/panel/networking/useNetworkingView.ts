"use client";

import { useMemo } from "react";
import type { CanvasService, Environment, InstanceNetworking, ServiceInstance } from "@/lib/api";
import { useNetworking, useSystem } from "@/lib/queries";

/**
 * GET …/networking, or — on an older server that answers 404 / while it loads — the same shape built from the instance
 * the canvas already has, so the section renders at once and never depends on the new endpoint.
 */
export function useNetworkingView(svc: CanvasService, env: Environment, inst: ServiceInstance) {
  const q = useNetworking(svc.id, env.id);
  const sys = useSystem();
  const fallback = useMemo<InstanceNetworking>(() => {
    const eff = inst.port ?? (Number(inst.variables?.PORT) || 8080);
    const dns = inst.private_dns_name || svc.name;
    const host = sys.data?.tcp_proxy_domain ?? sys.data?.base_domain ?? sys.data?.public_ipv6 ?? "";
    return {
      detected_ports: [],
      port_detection: { source: "unavailable", containers: 0 },
      effective_port: eff,
      domains: inst.domains,
      tcp_proxy: {
        enabled: !!inst.tcp_proxy,
        host,
        public_port: inst.tcp_proxy?.public_port ?? null,
        container_port: inst.tcp_proxy?.container_port ?? null,
        available: true,
      },
      private: {
        dns_name: dns,
        fqdn: inst.private_domain.replace(/^[^.]+/, dns),
        aliases: [svc.name, inst.private_domain],
        ipv4: null,
        ipv6: null,
        mesh_ports: inst.private_ports?.length ? inst.private_ports : [eff],
      },
      outbound: {
        ipv6_enabled: inst.outbound_ipv6 ?? true,
        node_ipv6_egress: null,
        static_ips: { ipv4: null, ipv6: null },
      },
    };
  }, [inst, svc.name, sys.data]);
  return { view: q.data ?? fallback, live: !!q.data, query: q };
}
