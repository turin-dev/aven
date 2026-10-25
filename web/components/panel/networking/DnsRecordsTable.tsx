"use client";

/* DNS records for a custom domain (table modeled on Railway's dashboard): type, name, value, what resolves now,
   per-record status and copy. CNAME to the node's ingress hostname is the recommended record; TXT proves ownership;
   A/AAAA are the alternative for apex domains. */

import { AlertTriangle, CheckCircle2, Clock, XCircle } from "lucide-react";
import type { DnsRecord, Domain } from "@/lib/api";
import { fmtRelative } from "@/lib/format";
import { dateLocale, t, tr } from "@/lib/i18n";
import { Button, cx } from "../../ui";
import { CopyBtn } from "./parts";

const STATUS: Record<NonNullable<DnsRecord["status"]>, { label: string; cls: string; icon: React.ReactNode }> = {
  ok: { label: t("확인됨"), cls: "text-green-600", icon: <CheckCircle2 /> },
  pending: { label: t("대기 중"), cls: "text-yellow-600", icon: <Clock /> },
  proxied: { label: t("프록시 뒤에서 정상"), cls: "text-green-600", icon: <CheckCircle2 /> },
  mismatch: { label: t("값이 다름"), cls: "text-red-600", icon: <XCircle /> },
};

const currentText = (c: DnsRecord["current"]) => (Array.isArray(c) ? c.join(", ") : c ?? "");

const CERT: Record<string, string> = {
  valid: t("인증서 발급됨"),
  pending: t("인증서 발급 대기 중"),
  error: t("인증서 발급 실패"),
  none: t("인증서 없음 (HTTP만)"),
};

export function DnsRecordsTable({
  d,
  onVerify,
  verifying,
  ipv6Only,
}: {
  d: Domain;
  onVerify?: () => void;
  verifying?: boolean;
  /** The node has no IPv4 address: IPv4 clients need the Cloudflare proxy in front. */
  ipv6Only?: boolean;
}) {
  const main = d.dns.filter((r) => r.type === "CNAME" || r.type === "TXT");
  const alt = d.dns.filter((r) => r.type === "A" || r.type === "AAAA");
  const rows = main.length ? main : alt;
  const tunnel = d.proxy === "tunnel";
  const notes = [...new Set(d.dns.map((r) => r.note).filter((n): n is string => !!n))];
  return (
    <div className="mt-2 space-y-3">
      <p className="text-sm text-gray-600">
        {tunnel
          ? t("이 노드는 Cloudflare 터널로만 들어옵니다. DNS 제공자에 아래 CNAME을 프록시(주황 구름)로 추가하세요.")
          : t("DNS 제공자에 아래 레코드를 추가하세요. CNAME을 권장하고, TXT 레코드로 도메인 소유를 확인합니다.")}
      </p>
      <RecordTable rows={rows} />
      {main.length > 0 && alt.length > 0 && (
        <details className="group/alt">
          <summary className="cursor-pointer select-none text-xs font-medium text-gray-600 hover:text-foreground">{t("루트 도메인(@)처럼 CNAME을 쓸 수 없다면 A/AAAA 레코드")}</summary>
          <div className="mt-2">
            <RecordTable rows={alt} />
          </div>
        </details>
      )}
      {d.ipv6 && !tunnel && (
        <p className="text-xs leading-[18px] text-gray-600">
          {tr("AAAA 레코드는 이 도메인 전용 IPv6 {addr}를 가리킵니다.", { addr: <span className="font-mono">{d.ipv6}</span> })}
        </p>
      )}
      {notes.map((n) => (
        <p key={n} className="text-xs leading-[18px] text-gray-600">
          {n}
        </p>
      ))}
      {ipv6Only && !tunnel && (
        <p className="flex items-center gap-1.5 text-xs leading-[18px] text-yellow-600">
          <AlertTriangle className="size-3.5 shrink-0" />{t("이 노드는 IPv6 전용입니다. IPv4 클라이언트가 접속하려면 Cloudflare 프록시가 필요합니다.")}
        </p>
      )}
      <div className="flex items-center justify-between gap-4">
        <p className="min-w-0 text-xs leading-[18px] text-gray-600">
          {d.cert?.status ? CERT[d.cert.status] ?? t("인증서: {status}", { status: d.cert.status }) : null}
          {d.cert?.status === "valid" && d.cert.not_after ? ` · ${t("{date}까지", { date: new Date(d.cert.not_after).toLocaleDateString(dateLocale()) })}` : null}
          {d.verification?.verified_at ? `${d.cert?.status ? " · " : ""}${t("{time} 소유 확인", { time: fmtRelative(d.verification.verified_at) })}` : null}
        </p>
        {onVerify && (
          <Button size="sm" variant="outline" onClick={onVerify} loading={verifying}>
            {t("지금 확인")}
          </Button>
        )}
      </div>
    </div>
  );
}

function RecordTable({ rows }: { rows: DnsRecord[] }) {
  return (
    <div className="overflow-hidden rounded-md border border-gray-200">
      <table className="w-full table-fixed text-left text-xs">
        <thead className="bg-secondaryBg text-gray-600">
          <tr>
            <th className="w-[56px] px-2.5 py-2 font-semibold">{t("유형")}</th>
            <th className="w-[22%] px-2.5 py-2 font-semibold">{t("이름")}</th>
            <th className="px-2.5 py-2 font-semibold">{t("값")}</th>
            <th className="w-[24%] px-2.5 py-2 font-semibold">{t("현재 값")}</th>
            <th className="w-[30px] py-2" aria-label={t("상태")} />
            <th className="w-[32px]" aria-label={t("복사")} />
          </tr>
        </thead>
        <tbody className="font-mono">
          {rows.map((r, i) => {
            const st = r.status ? STATUS[r.status] : null;
            return (
              <tr key={i} className="border-t border-gray-200 align-middle">
                <td className="px-2.5 py-2">{r.type}</td>
                <td className="truncate px-2.5 py-2" title={r.name}>
                  {r.name}
                </td>
                <td className="truncate px-2.5 py-2" title={r.value}>
                  {r.value}
                </td>
                <td className={cx("truncate px-2.5 py-2", r.status === "mismatch" ? "text-red-600" : "text-gray-500")} title={currentText(r.current) || undefined}>
                  {currentText(r.current) || "—"}
                </td>
                <td className="py-2">
                  {st && (
                    <span title={st.label} aria-label={st.label} className={cx("icon-container icon-sm", st.cls)}>
                      {st.icon}
                    </span>
                  )}
                </td>
                <td className="pr-1">
                  <CopyBtn value={r.value} title={t("{type} 값 복사", { type: r.type })} />
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
