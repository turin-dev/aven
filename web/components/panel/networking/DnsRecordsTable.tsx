"use client";

/* DNS records for a custom domain (table modeled on Railway's dashboard): type, name, value, what resolves now,
   per-record status and copy. CNAME to the node's ingress hostname is the recommended record; A/AAAA are the
   alternative for apex domains; the `_keel-verify` TXT record optionally proves ownership. Below the table: the
   overall state (waiting / verified / certificate), the reason of the last failure and when DNS was last checked. */

import { AlertTriangle, CheckCircle2, Clock, XCircle } from "lucide-react";
import type { DnsRecord } from "@/lib/api";
import { fmtRelative, fmtTime } from "@/lib/format";
import { dateLocale, t, tr } from "@/lib/i18n";
import { Button, cx } from "../../ui";
import { DomainStateLabel, type DomainExtra } from "./DomainStatus";
import { CopyBtn } from "./parts";

const STATUS: Record<NonNullable<DnsRecord["status"]>, { label: () => string; cls: string; icon: React.ReactNode }> = {
  ok: { label: () => t("확인됨"), cls: "text-green-600", icon: <CheckCircle2 /> },
  pending: { label: () => t("대기 중"), cls: "text-yellow-600", icon: <Clock /> },
  proxied: { label: () => t("프록시 뒤에서 정상"), cls: "text-green-600", icon: <CheckCircle2 /> },
  mismatch: { label: () => t("값이 다름"), cls: "text-red-600", icon: <XCircle /> },
};

const currentText = (c: DnsRecord["current"]) => (Array.isArray(c) ? c.join(", ") : (c ?? ""));

/** Records to show: the server's list plus, when it does not carry one, the optional ownership TXT record. */
export function domainRecords(d: DomainExtra): { main: DnsRecord[]; alt: DnsRecord[] } {
  const isAlt = (r: DnsRecord) => (d.dns.some((x) => x.alternative) ? !!r.alternative : (r.type === "A" || r.type === "AAAA") && d.dns.some((x) => x.type === "CNAME"));
  const main = d.dns.filter((r) => !isAlt(r));
  const alt = d.dns.filter(isAlt);
  const txt = d.verification?.txt;
  if (txt?.name && txt.value && d.proxy !== "tunnel" && !d.dns.some((r) => r.type === "TXT")) {
    main.push({ type: "TXT", name: txt.name, value: txt.value, status: d.verification?.verified_at ? "ok" : "pending", current: null });
  }
  return { main, alt };
}

export function DnsRecordsTable({
  d,
  onVerify,
  verifying,
  ipv6Only,
}: {
  d: DomainExtra;
  onVerify?: () => void;
  verifying?: boolean;
  /** The node has no IPv4 address: IPv4 clients need the Cloudflare proxy in front. */
  ipv6Only?: boolean;
}) {
  const { main, alt } = domainRecords(d);
  const rows = main.length ? main : alt;
  const tunnel = d.proxy === "tunnel";
  const notes = [...new Set([...d.dns.map((r) => r.note), ...(d.warnings ?? [])].filter((n): n is string => !!n))];
  const optionalTxt = main.some((r) => r.type === "TXT");
  return (
    <div className="mt-2 space-y-3" data-testid="dns-records">
      <p className="text-sm text-gray-600">
        {tunnel
          ? t("이 노드는 Cloudflare 터널로만 들어옵니다. DNS 제공자에 아래 CNAME을 프록시(주황 구름)로 추가하세요.")
          : t("DNS 제공자에 아래 레코드를 추가하세요. CNAME을 권장하고, TXT 레코드로 도메인 소유를 확인합니다.")}
      </p>
      <RecordTable rows={rows} />
      {optionalTxt && <p className="text-xs leading-[18px] text-gray-600">{t("TXT 레코드는 선택입니다. 위 주소 레코드가 맞으면 소유 확인 없이도 연결됩니다.")}</p>}
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
          <AlertTriangle className="size-3.5 shrink-0" aria-hidden="true" />
          {t("이 노드는 IPv6 전용입니다. IPv4 클라이언트가 접속하려면 Cloudflare 프록시가 필요합니다.")}
        </p>
      )}
      {d.last_error && (d.status === "error" || d.cert?.status === "error") && (
        <p role="alert" className="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-xs leading-[18px] text-red-600">
          {t("마지막 확인 오류: {reason}", { reason: d.last_error })}
        </p>
      )}
      <div className="flex items-center justify-between gap-4">
        <div className="min-w-0 text-xs leading-[18px] text-gray-600" aria-live="polite">
          <DomainStateLabel d={d} />
          {d.cert?.status === "valid" && d.cert.not_after ? ` · ${t("{date}까지", { date: new Date(d.cert.not_after).toLocaleDateString(dateLocale()) })}` : null}
          {d.verification?.verified_at ? ` · ${t("{time} 소유 확인", { time: fmtRelative(d.verification.verified_at) })}` : null}
          {d.checked_at ? (
            <span title={fmtTime(d.checked_at)}>
              {" · "}
              {t("{time}에 마지막 확인", { time: fmtRelative(d.checked_at) })}
            </span>
          ) : null}
          {d.status === "pending_dns" && <span className="block text-gray-500">{t("DNS가 퍼지는 데 몇 분에서 몇 시간이 걸릴 수 있습니다. 이 화면은 10초마다 다시 확인합니다.")}</span>}
        </div>
        {onVerify && (
          <Button size="sm" variant="outline" onClick={onVerify} loading={verifying} className="shrink-0">
            {t("지금 확인")}
          </Button>
        )}
      </div>
    </div>
  );
}

function RecordTable({ rows }: { rows: DnsRecord[] }) {
  return (
    <div className="overflow-x-auto rounded-md border border-gray-200">
      <table className="w-full min-w-[480px] table-fixed text-left text-xs">
        <thead className="bg-secondaryBg text-gray-600">
          <tr>
            <th scope="col" className="w-[56px] px-2.5 py-2 font-semibold">
              {t("유형")}
            </th>
            <th scope="col" className="w-[26%] px-2.5 py-2 font-semibold">
              {t("이름")}
            </th>
            <th scope="col" className="px-2.5 py-2 font-semibold">
              {t("값")}
            </th>
            <th scope="col" className="w-[22%] px-2.5 py-2 font-semibold">
              {t("현재 값")}
            </th>
            <th scope="col" className="w-[30px] py-2">
              <span className="sr-only">{t("상태")}</span>
            </th>
          </tr>
        </thead>
        <tbody className="font-mono">
          {rows.map((r, i) => {
            const st = r.status ? STATUS[r.status] : null;
            const cur = currentText(r.current);
            return (
              <tr key={`${r.type}-${r.name}-${i}`} className="border-t border-gray-200 align-middle">
                <td className="px-2.5 py-2">
                  {r.type}
                  {r.type === "TXT" && <span className="block font-sans text-[10px] text-gray-500">{t("선택")}</span>}
                </td>
                <td className="px-2.5 py-2">
                  <CopyCell value={r.name} title={t("이름 복사")} />
                </td>
                <td className="px-2.5 py-2">
                  <CopyCell value={r.value} title={t("{type} 값 복사", { type: r.type })} />
                </td>
                <td className={cx("truncate px-2.5 py-2", r.status === "mismatch" ? "text-red-600" : "text-gray-500")} title={cur || undefined}>
                  {cur || "—"}
                </td>
                <td className="py-2">
                  {st && (
                    <span role="img" title={st.label()} aria-label={st.label()} className={cx("icon-container icon-sm", st.cls)}>
                      {st.icon}
                    </span>
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function CopyCell({ value, title }: { value: string; title: string }) {
  return (
    <span className="flex min-w-0 items-center gap-1">
      <span className="min-w-0 truncate" title={value}>
        {value}
      </span>
      <span className="shrink-0">
        <CopyBtn value={value} title={title} small />
      </span>
    </span>
  );
}
