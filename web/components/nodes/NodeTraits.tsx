"use client";

import { useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Building2, Cloud, House, Shapes } from "lucide-react";
import { api, type AvenNode, type NodeConnection, type NodeSite, type NodeTraits } from "@/lib/api";
import { qk } from "@/lib/queries";
import { getLocale, t } from "@/lib/i18n";
import { Button, Dialog, ErrorBox, Field, Input, Select, Toggle, cx } from "../ui";

/*
 * 노드 특성: 클라우드 콘솔에는 없는 그 기계의 사정(집 PC인지, 전기요금, 회선 품질)을 운영자가 적는다.
 * 노드 추가 온보딩과 노드 메뉴의 "기계 정보"에서 같은 폼을 쓴다. 모든 칸은 선택이고 빈 칸은 "모름"이다.
 */

export const SITES: { value: NodeSite; label: () => string; hint: () => string; icon: ReactNode }[] = [
  { value: "datacenter", label: () => t("데이터센터·클라우드"), hint: () => t("Hetzner, AWS 같은 호스팅"), icon: <Cloud className="size-5" /> },
  { value: "home", label: () => t("집"), hint: () => t("미니PC, NAS, 데스크톱"), icon: <House className="size-5" /> },
  { value: "office", label: () => t("사무실"), hint: () => t("회사·학교 안의 서버"), icon: <Building2 className="size-5" /> },
  { value: "other", label: () => t("기타"), hint: () => t("그 밖의 곳"), icon: <Shapes className="size-5" /> },
];

const CONNECTIONS: { value: NodeConnection; label: () => string }[] = [
  { value: "fiber", label: () => t("광랜(FTTH)") },
  { value: "cable", label: () => t("케이블") },
  { value: "dsl", label: () => t("DSL") },
  { value: "mobile", label: () => t("모바일(LTE/5G)") },
  { value: "satellite", label: () => t("위성") },
  { value: "datacenter", label: () => t("데이터센터 회선") },
  { value: "other", label: () => t("기타") },
];

const siteLabel = (s?: NodeSite) => SITES.find((x) => x.value === s)?.label() ?? "";
const connectionLabel = (c?: NodeConnection) => CONNECTIONS.find((x) => x.value === c)?.label() ?? "";

/** 폼 상태. 숫자 칸은 입력 중인 문자열 그대로 둔다. */
export type TraitsDraft = {
  site: NodeSite | "";
  price: string;
  currency: string;
  idleW: string;
  maxW: string;
  ups: boolean;
  connection: NodeConnection | "";
  down: string;
  up: string;
  cap: string;
  dynamicIP: boolean;
  cgnat: boolean;
  note: string;
};

const defaultCurrency = () => (getLocale() === "ko" ? "KRW" : "USD");

export function traitsToDraft(tr?: NodeTraits): TraitsDraft {
  const num = (v?: number) => (v ? String(v) : "");
  return {
    site: tr?.site ?? "",
    price: tr?.power_price_per_kwh != null ? String(tr.power_price_per_kwh) : "",
    currency: tr?.power_currency || defaultCurrency(),
    idleW: num(tr?.idle_watts),
    maxW: num(tr?.max_watts),
    ups: !!tr?.ups,
    connection: tr?.connection ?? "",
    down: num(tr?.downlink_mbps),
    up: num(tr?.uplink_mbps),
    cap: num(tr?.data_cap_gb),
    dynamicIP: !!tr?.dynamic_ip,
    cgnat: !!tr?.cgnat,
    note: tr?.note ?? "",
  };
}

/** 폼 상태를 API 값으로 바꾼다. 잘못된 칸이 있으면 오류 문장을 던진다. */
export function draftToTraits(d: TraitsDraft): NodeTraits {
  const int = (v: string, label: string) => {
    const s = v.trim();
    if (!s) return undefined;
    if (!/^\d+$/.test(s) || +s > 10_000_000) throw new Error(t("{label}: 0 이상의 정수를 적으세요.", { label }));
    return +s || undefined;
  };
  const out: NodeTraits = {};
  if (d.site) out.site = d.site;
  const price = d.price.trim().replace(",", ".");
  if (price) {
    if (!/^\d+(\.\d+)?$/.test(price)) throw new Error(t("전기요금: 숫자를 적으세요(예: 180 또는 0.31)."));
    const cur = d.currency.trim().toUpperCase();
    if (!/^[A-Z]{3}$/.test(cur)) throw new Error(t("통화는 KRW, EUR, USD 같은 세 글자 코드로 적으세요."));
    out.power_price_per_kwh = +price;
    out.power_currency = cur;
  }
  out.idle_watts = int(d.idleW, t("대기 전력"));
  out.max_watts = int(d.maxW, t("최대 전력"));
  if (out.idle_watts && out.max_watts && out.idle_watts > out.max_watts) throw new Error(t("대기 전력이 최대 전력보다 클 수 없습니다."));
  if (d.ups) out.ups = true;
  if (d.connection) out.connection = d.connection;
  out.downlink_mbps = int(d.down, t("다운로드"));
  out.uplink_mbps = int(d.up, t("업로드"));
  out.data_cap_gb = int(d.cap, t("월 데이터 상한"));
  if (d.dynamicIP) out.dynamic_ip = true;
  if (d.cgnat) out.cgnat = true;
  const note = d.note.trim();
  if ([...note].length > 200) throw new Error(t("메모는 200자 이하로 적으세요."));
  if (note) out.note = note;
  for (const k of Object.keys(out) as (keyof NodeTraits)[]) if (out[k] === undefined) delete out[k];
  return out;
}

function ToggleRow({ checked, onChange, label, hint }: { checked: boolean; onChange: (v: boolean) => void; label: string; hint: string }) {
  return (
    <label className="flex cursor-pointer items-start justify-between gap-4 py-1.5">
      <span className="min-w-0">
        <span className="block text-sm text-foreground">{label}</span>
        <span className="block text-xs text-gray-500">{hint}</span>
      </span>
      <Toggle checked={checked} onChange={onChange} label={label} />
    </label>
  );
}

/**
 * 기계 정보 폼. 먼저 놓인 곳(데이터센터/집/사무실)을 고르게 하고, 집·사무실이면 전기와 회선 칸을 펼쳐 둔다.
 * 데이터센터는 대개 몰라도 되는 값이라 "자세히"를 눌러야 펼친다.
 */
export function NodeTraitsFields({ draft, onChange }: { draft: TraitsDraft; onChange: (d: TraitsDraft) => void }) {
  const set = <K extends keyof TraitsDraft>(k: K, v: TraitsDraft[K]) => onChange({ ...draft, [k]: v });
  const [more, setMore] = useState(false);
  const selfHosted = draft.site === "home" || draft.site === "office";
  const open = selfHosted || more;
  return (
    <div className="space-y-4">
      <div role="radiogroup" aria-label={t("이 서버가 놓인 곳")} className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        {SITES.map((s) => {
          const on = draft.site === s.value;
          return (
            <button
              key={s.value}
              type="button"
              role="radio"
              aria-checked={on}
              onClick={() => set("site", on ? "" : s.value)}
              className={cx(
                "flex cursor-pointer flex-col items-start gap-1.5 rounded-lg border px-3 py-2.5 text-left transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-500",
                on ? "border-pink-500 bg-pink-50 text-foreground dark:bg-pink-950/30" : "border-gray-200 text-gray-600 hover:border-gray-300 hover:bg-gray-50",
              )}
            >
              <span className={cx(on ? "text-pink-600" : "text-gray-500")}>{s.icon}</span>
              <span className="text-sm font-medium text-foreground">{s.label()}</span>
              <span className="text-xs leading-snug text-gray-500">{s.hint()}</span>
            </button>
          );
        })}
      </div>

      {!open ? (
        <button type="button" onClick={() => setMore(true)} className="cursor-pointer text-xs text-gray-500 underline underline-offset-2 hover:text-foreground">
          {t("전기·회선 정보도 적기")}
        </button>
      ) : (
        <>
          <fieldset className="space-y-2">
            <legend className="mb-1 text-xs font-semibold uppercase tracking-wide text-gray-500">{t("전기")}</legend>
            <div className="grid grid-cols-[1fr_96px] gap-2">
              <Field label={t("전기요금 (1kWh당)")} hint={selfHosted ? t("고지서의 kWh당 단가. 누진제라면 평소 구간 단가를 적으세요.") : undefined}>
                <Input inputMode="decimal" value={draft.price} onChange={(e) => set("price", e.target.value)} placeholder={draft.currency === "KRW" ? "180" : "0.31"} />
              </Field>
              <Field label={t("통화")}>
                <Input mono maxLength={3} value={draft.currency} onChange={(e) => set("currency", e.target.value.toUpperCase())} />
              </Field>
            </div>
            <div className="grid grid-cols-2 gap-2">
              <Field label={t("대기 전력 (W)")} hint={t("거의 놀 때 기계 전체가 쓰는 전력")}>
                <Input inputMode="numeric" value={draft.idleW} onChange={(e) => set("idleW", e.target.value)} placeholder="8" />
              </Field>
              <Field label={t("최대 전력 (W)")} hint={t("CPU를 다 쓸 때. 모르면 어댑터 정격의 70% 정도")}>
                <Input inputMode="numeric" value={draft.maxW} onChange={(e) => set("maxW", e.target.value)} placeholder="45" />
              </Field>
            </div>
            <p className="text-xs text-gray-500">{t("전기요금과 소비전력을 적으면 최근 CPU 사용률로 월 전기료를 어림해 노드 목록에 보여 줍니다.")}</p>
            <ToggleRow checked={draft.ups} onChange={(v) => set("ups", v)} label={t("UPS(무정전 전원) 있음")} hint={t("정전 때 잠깐이라도 버틸 수 있는지")} />
          </fieldset>

          <fieldset className="space-y-2">
            <legend className="mb-1 text-xs font-semibold uppercase tracking-wide text-gray-500">{t("회선")}</legend>
            <Field label={t("회선 종류")}>
              <Select value={draft.connection} onChange={(e) => set("connection", e.target.value as NodeConnection | "")} aria-label={t("회선 종류")}>
                <option value="">{t("모름")}</option>
                {CONNECTIONS.map((c) => (
                  <option key={c.value} value={c.value}>
                    {c.label()}
                  </option>
                ))}
              </Select>
            </Field>
            <div className="grid grid-cols-3 gap-2">
              <Field label={t("다운로드 (Mbps)")}>
                <Input inputMode="numeric" value={draft.down} onChange={(e) => set("down", e.target.value)} placeholder="1000" />
              </Field>
              <Field label={t("업로드 (Mbps)")} hint={selfHosted ? t("서비스 속도는 대개 업로드가 정합니다") : undefined}>
                <Input inputMode="numeric" value={draft.up} onChange={(e) => set("up", e.target.value)} placeholder="100" />
              </Field>
              <Field label={t("월 데이터 상한 (GB)")} hint={t("비우면 무제한")}>
                <Input inputMode="numeric" value={draft.cap} onChange={(e) => set("cap", e.target.value)} />
              </Field>
            </div>
            <ToggleRow checked={draft.dynamicIP} onChange={(v) => set("dynamicIP", v)} label={t("공인 IP가 바뀔 수 있음 (유동 IP)")} hint={t("공유기를 재시작하면 주소가 바뀌는 가정용 회선")} />
            <ToggleRow checked={draft.cgnat} onChange={(v) => set("cgnat", v)} label={t("통신사 NAT(CGNAT) 뒤에 있음")} hint={t("밖에서 이 서버로 직접 들어올 수 없는 회선")} />
            {draft.cgnat ? (
              <p className="rounded-md bg-gray-50 px-3 py-2 text-xs text-gray-600">
                {t("CGNAT 뒤의 서버는 80/443으로 직접 들어오는 요청을 받을 수 없습니다. 연결한 뒤 노드 메뉴 › 공개 진입 설정에서 Cloudflare 터널을 고르세요.")}
              </p>
            ) : null}
          </fieldset>

          <Field label={t("메모")} hint={t("서버 관리자에게만 보입니다.")}>
            <Input value={draft.note} maxLength={200} onChange={(e) => set("note", e.target.value)} placeholder={t("예: 거실 미니PC, 밤에는 꺼짐")} />
          </Field>
        </>
      )}
    </div>
  );
}

const nf = (n: number) => new Intl.NumberFormat(getLocale() === "ko" ? "ko-KR" : "en-US").format(n);

function fmtPrice(tr: NodeTraits) {
  if (tr.power_price_per_kwh == null || !tr.power_currency) return "";
  try {
    const s = new Intl.NumberFormat(getLocale() === "ko" ? "ko-KR" : "en-US", { style: "currency", currency: tr.power_currency, maximumFractionDigits: 4 }).format(tr.power_price_per_kwh);
    return `${s}/kWh`;
  } catch {
    return `${tr.power_price_per_kwh} ${tr.power_currency}/kWh`;
  }
}

/** 노드 줄에 붙는 기계 정보 배지들. 적힌 것만 보인다. */
export function NodeTraitsBadges({ n }: { n: AvenNode }) {
  const tr = n.traits;
  if (!tr) return null;
  const chips: { key: string; text: string; tone?: "warn" }[] = [];
  if (tr.site) chips.push({ key: "site", text: siteLabel(tr.site) });
  const price = fmtPrice(tr);
  if (price) chips.push({ key: "power", text: `⚡ ${price}` });
  if (tr.ups) chips.push({ key: "ups", text: "UPS" });
  const speed = tr.downlink_mbps || tr.uplink_mbps ? `↓${tr.downlink_mbps ? nf(tr.downlink_mbps) : "?"} ↑${tr.uplink_mbps ? nf(tr.uplink_mbps) : "?"} Mbps` : "";
  const line = [connectionLabel(tr.connection), speed].filter(Boolean).join(" ");
  if (line) chips.push({ key: "line", text: line });
  if (tr.data_cap_gb) chips.push({ key: "cap", text: t("월 {gb}GB 상한", { gb: nf(tr.data_cap_gb) }), tone: "warn" });
  if (tr.dynamic_ip) chips.push({ key: "dyn", text: t("유동 IP"), tone: "warn" });
  if (tr.cgnat) chips.push({ key: "cgnat", text: "CGNAT", tone: "warn" });
  if (chips.length === 0 && !tr.note) return null;
  return (
    <div className="flex flex-wrap items-center gap-1.5 text-xs">
      {chips.map((c) => (
        <span key={c.key} className={cx("rounded-md px-1.5 py-0.5", c.tone === "warn" ? "bg-yellow-50 text-yellow-700 dark:bg-yellow-950/30 dark:text-yellow-400" : "bg-pink-50 text-pink-700 dark:bg-pink-950/30 dark:text-pink-300")}>
          {c.text}
        </span>
      ))}
      {tr.note ? <span className="truncate text-gray-500">“{tr.note}”</span> : null}
    </div>
  );
}

/** 노드 메뉴 › 기계 정보. 로컬 노드처럼 온보딩을 거치지 않은 노드도 여기서 적는다. */
export function NodeTraitsDialog({ node, onClose }: { node: AvenNode | null; onClose: () => void }) {
  const qc = useQueryClient();
  const [draft, setDraft] = useState(() => traitsToDraft(node?.traits));
  const [prev, setPrev] = useState(node);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  if (prev !== node) {
    setPrev(node);
    setDraft(traitsToDraft(node?.traits));
    setError(null);
  }
  const save = async () => {
    if (!node || busy) return;
    let traits: NodeTraits;
    try {
      traits = draftToTraits(draft);
    } catch (e) {
      return setError(e);
    }
    setBusy(true);
    setError(null);
    try {
      await api.updateNode(node.id, { traits });
      await qc.invalidateQueries({ queryKey: qk.nodes });
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={!!node}
      onClose={() => !busy && onClose()}
      title={t("{name} 기계 정보", { name: node?.name ?? "" })}
      wide
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("취소")}
          </Button>
          <Button variant="primary" loading={busy} onClick={save}>
            {t("저장")}
          </Button>
        </>
      }
    >
      <div className="space-y-4 pt-1">
        <p className="text-sm text-gray-600">{t("클라우드 콘솔에는 없는 이 기계의 사정을 적어 두세요. 모르는 칸은 비워 두면 됩니다.")}</p>
        <NodeTraitsFields draft={draft} onChange={setDraft} />
        {error ? <ErrorBox error={error} /> : null}
      </div>
    </Dialog>
  );
}

function fmtMoney(v: number, currency: string) {
  try {
    return new Intl.NumberFormat(getLocale() === "ko" ? "ko-KR" : "en-US", { style: "currency", currency, maximumFractionDigits: v < 10 ? 2 : 0 }).format(v);
  } catch {
    return `${v.toFixed(2)} ${currency}`;
  }
}

/** 1 GB = 10^9 bytes(회선 약관 표기). */
const fmtGB = (b: number) => (b >= 1e12 ? `${nf(Math.round(b / 1e10) / 100)} TB` : b >= 1e9 ? `${nf(Math.round(b / 1e7) / 100)} GB` : `${nf(Math.round(b / 1e6))} MB`);

/** 노드 줄의 월 전기료 추정과 이번 달 트래픽(상한이 있으면 막대). 둘 다 없으면 아무것도 그리지 않는다. */
export function NodeUsageLine({ n }: { n: AvenNode }) {
  const pe = n.power_estimate;
  const tr = n.traffic;
  if (!pe && !tr) return null;
  const pct = tr?.cap_percent;
  return (
    <div className="flex flex-wrap items-center gap-x-5 gap-y-1.5 text-xs text-gray-600">
      {pe ? (
        <span title={t("최근 {days}일 평균 CPU {cpu}% 기준, {model}", { days: Math.max(1, Math.round(pe.window_s / 86400)), cpu: pe.avg_cpu_percent, model: pe.model === "linear" ? t("대기~최대 전력 사이를 CPU 사용률로 보간") : t("적힌 전력을 늘 쓴다고 가정") })}>
          ⚡{" "}
          {pe.cost_month != null && pe.currency
            ? t("월 전기료 약 {cost} ({kwh}kWh, 평균 {w}W)", { cost: fmtMoney(pe.cost_month, pe.currency), kwh: pe.kwh_month, w: pe.avg_watts })
            : t("월 약 {kwh}kWh (평균 {w}W)", { kwh: pe.kwh_month, w: pe.avg_watts })}
        </span>
      ) : null}
      {tr ? (
        <span className="flex min-w-0 items-center gap-2" title={t("받음 {rx} · 보냄 {tx} ({month}, UTC)", { rx: fmtGB(tr.rx_bytes), tx: fmtGB(tr.tx_bytes), month: tr.month })}>
          <span>
            {tr.cap_bytes
              ? t("이번 달 트래픽 {used} / {cap}", { used: fmtGB(tr.total_bytes), cap: fmtGB(tr.cap_bytes) })
              : t("이번 달 트래픽 {used}", { used: fmtGB(tr.total_bytes) })}
          </span>
          {pct != null ? (
            <>
              <span className="h-1.5 w-24 overflow-hidden rounded-full bg-gray-100" role="meter" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.min(100, pct)} aria-label={t("월 데이터 상한 사용률")}>
                <span className={cx("block h-full rounded-full", pct >= 100 ? "bg-red-500" : pct >= 80 ? "bg-yellow-500" : "bg-pink-500")} style={{ width: `${Math.min(100, pct)}%` }} />
              </span>
              <span className={cx("tabular-nums", pct >= 100 ? "text-danger" : pct >= 80 ? "text-yellow-700" : "")}>{pct}%</span>
            </>
          ) : null}
        </span>
      ) : null}
    </div>
  );
}
