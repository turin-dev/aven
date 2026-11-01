"use client";

/* One readable state per domain, from the fields the server reports (status, cert, verification, last check):
   waiting for DNS → DNS verified → certificate issued, or an error with its reason. */

import type { ReactNode } from "react";
import { AlertTriangle, CheckCircle2, Clock, Loader2, ShieldCheck, XCircle } from "lucide-react";
import type { Domain } from "@/lib/api";
import { t } from "@/lib/i18n";
import { cx } from "../../ui";

/** Fields the server sends but lib/api's Domain type does not list yet. */
export type DomainExtra = Domain & { checked_at?: string | null; last_error?: string; warnings?: string[] };

export type DomainState = "pending" | "verified" | "tls_pending" | "tls_issued" | "http_only" | "proxied" | "error" | "tls_error";

export function domainState(d: DomainExtra): DomainState {
  if (d.status === "error") return "error";
  if (d.cert?.status === "error") return "tls_error";
  if (d.status === "pending_dns") return "pending";
  if (d.tls === "off") return "http_only";
  if (d.status === "proxied") return "proxied";
  if (d.cert?.status === "valid") return "tls_issued";
  if (d.cert?.status === "pending") return "tls_pending";
  return "verified";
}

const META: Record<DomainState, { label: () => string; cls: string; icon: ReactNode }> = {
  pending: { label: () => t("DNS 레코드 대기 중"), cls: "text-yellow-600", icon: <Clock /> },
  verified: { label: () => t("DNS 확인됨"), cls: "text-green-600", icon: <CheckCircle2 /> },
  tls_pending: { label: () => t("인증서 발급 대기 중"), cls: "text-yellow-600", icon: <Loader2 className="motion-safe:animate-spin" /> },
  tls_issued: { label: () => t("인증서 발급됨"), cls: "text-green-600", icon: <ShieldCheck /> },
  http_only: { label: () => t("HTTP만"), cls: "text-gray-600", icon: <AlertTriangle /> },
  proxied: { label: () => t("프록시 뒤에서 정상"), cls: "text-green-600", icon: <CheckCircle2 /> },
  error: { label: () => t("DNS 확인 실패"), cls: "text-red-600", icon: <XCircle /> },
  tls_error: { label: () => t("인증서 발급 실패"), cls: "text-red-600", icon: <XCircle /> },
};

export const domainStateLabel = (s: DomainState) => META[s].label();
export const domainStateClass = (s: DomainState) => META[s].cls;
export const isProblem = (s: DomainState) => s === "pending" || s === "error" || s === "tls_error";

/** Status text with its icon, used in the domain row meta line (a button when it toggles the DNS panel). */
export function DomainStateLabel({ d, onClick, expanded }: { d: DomainExtra; onClick?: () => void; expanded?: boolean }) {
  const s = domainState(d);
  const m = META[s];
  const body = (
    <>
      <span aria-hidden="true" className="icon-container icon-xs text-lg text-current [&_svg]:!size-3">
        {m.icon}
      </span>
      {m.label()}
    </>
  );
  const cls = cx("inline-flex items-center gap-1 align-middle", m.cls);
  if (!onClick) return <span className={cls}>{body}</span>;
  return (
    <button type="button" onClick={onClick} aria-expanded={expanded} title={d.last_error || undefined} className={cx(cls, "cursor-pointer rounded hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-700")}>
      {body}
    </button>
  );
}
