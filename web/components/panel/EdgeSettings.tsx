"use client";

import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowDown, ArrowUp, Braces, Pencil, Plus, RefreshCw, Shield, ShieldOff, Trash2, X } from "lucide-react";
import type { CanvasService, Environment } from "@/lib/api";
import {
  ATTR_OPS,
  TTL_OPTIONS,
  edgeApi,
  edgeKey,
  flatConditions,
  joinConditions,
  type EdgeActionName,
  type EdgeActionParams,
  type EdgeAttr,
  type EdgeClause,
  type EdgeExpr,
  type EdgeOp,
  type EdgePatch,
  type EdgeRule,
  type EdgeRuleset,
  type EdgeSettings as EdgeSettingsT,
  type HTMLCaching,
  type PurgeOnDeploy,
} from "@/lib/edge";
import { fmtRelative, fmtTime, fmtUntil } from "@/lib/format";
import { toast } from "@/lib/uiState";
import { Button, Dialog, ErrorBox, IconButton, Input, Segmented, Select, Spinner, Textarea, Toggle, cx, errorMessage, useConfirm } from "../ui";
import { FIELD, SettingItem as Item, ToggleCard, WarnBanner } from "./settings/primitives";
import { t, tr } from "@/lib/i18n";


export function EdgeSettings({ svc, env }: { svc: CanvasService; env: Environment }) {
  const qc = useQueryClient();
  const key = edgeKey(svc.id, env.id);
  const q = useQuery({ queryKey: key, queryFn: () => edgeApi.get(svc.id, env.id) });
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const lastError = useRef<unknown>(null);

  const run = async (k: string, fn: () => Promise<EdgeSettingsT>, ok?: string) => {
    setBusy(k);
    setError(null);
    lastError.current = null;
    try {
      const next = await fn();
      qc.setQueryData(key, next);
      if (ok) toast(ok);
      return true;
    } catch (e) {
      setError(e);
      lastError.current = e;
      return false;
    } finally {
      setBusy(null);
    }
  };
  const patch = (k: string, p: EdgePatch, ok?: string) => run(k, () => edgeApi.patch(svc.id, env.id, p), ok);

  if (q.isLoading || q.error || !q.data)
    return (
      <Item label={t("엣지")} keywords="edge cdn cache waf firewall rules under attack 캐시 방화벽 규칙 공격">
        {q.isLoading ? <Spinner /> : <ErrorBox error={q.error ?? t("엣지 설정을 불러오지 못했습니다")} />}
      </Item>
    );
  const s = q.data;

  return (
    <>
      {error ? <ErrorBox error={error} /> : null}
      <UnderAttackSection
        s={s}
        busy={busy}
        start={(d) => run("ua", () => edgeApi.underAttack(svc.id, env.id, d), t("공격 방어 모드를 켰습니다"))}
        stop={() => run("ua", () => edgeApi.stopUnderAttack(svc.id, env.id), t("공격 방어 모드를 껐습니다"))}
      />
      <CDNSection s={s} busy={busy} patch={patch} purge={(scope) => run(`purge-${scope}`, () => edgeApi.purge(svc.id, env.id, scope), scope === "html" ? t("HTML 캐시를 비웠습니다") : t("캐시 전체를 비웠습니다"))} />
      <WAFSection key={JSON.stringify(s.waf)} s={s} busy={busy} patch={patch} />
      <RulesSection key={JSON.stringify(s.rules)} s={s} busy={busy} lastError={() => lastError.current} save={(rs) => run("rules", () => edgeApi.putRules(svc.id, env.id, rs), t("엣지 규칙을 저장했습니다"))} />
    </>
  );
}

/* ── CDN ─────────────────────────────────────────────────────────────── */

function CDNSection({
  s,
  busy,
  patch,
  purge,
}: {
  s: EdgeSettingsT;
  busy: string | null;
  patch: (k: string, p: EdgePatch, ok?: string) => Promise<boolean>;
  purge: (scope: "html" | "all") => Promise<boolean>;
}) {
  const { confirm, node } = useConfirm();
  const c = s.cdn;
  return (
    <>
      <Item
        label={t("CDN 캐싱")}
        keywords="edge cdn cache caching x-cache 캐시"
        badge={busy === "cdn-enabled" ? <Spinner className="size-3" /> : null}
        description={t("정적 자산과 선택적으로 HTML을 엣지(aven-proxy)에 캐시합니다. 이 서비스 도메인의 지연 시간과 원본 부하를 줄입니다. 응답의 x-cache 헤더(HIT · STALE · MISS · DYNAMIC)로 확인하세요.")}
      >
        <ToggleCard
          checked={c.enabled}
          disabled={!s.available && !c.enabled}
          onChange={(v) => patch("cdn-enabled", { cdn: { enabled: v } }, v ? t("CDN 캐싱을 켰습니다") : t("CDN 캐싱을 껐습니다"))}
          label={t("CDN 캐싱 사용")}
        />
      </Item>
      {!s.available && (
        <WarnBanner title={t("공개 도메인이 없습니다")}>
          {t("CDN 캐싱, 공격 방어 모드, 엣지 규칙은 공개 도메인으로 들어오는 요청에 적용됩니다. 먼저 네트워킹에서 생성 도메인이나 사용자 지정 도메인을 추가하세요.")}
        </WarnBanner>
      )}
      {c.enabled && (
        <>
          <Item label={t("HTML 캐싱")} keywords="html caching cache-control" description={t("엣지에서 HTML 응답을 언제 캐시할지 정합니다. 정적 자산(이미지, CSS, JS, 글꼴)은 이 설정과 상관없이 항상 캐시합니다.")}>
            <Select aria-label={t("HTML 캐싱")} value={c.html_caching} onChange={(e) => patch("cdn-html", { cdn: { html_caching: e.target.value as HTMLCaching } })}>
              <option value="auto">{t("자동 - 원본 Cache-Control을 안전 검사와 함께 따름")}</option>
              <option value="force">{t("강제 - 헤더가 없어도 기본 TTL로 캐시")}</option>
              <option value="never">{t("안 함 - HTML은 캐시하지 않음")}</option>
            </Select>
          </Item>
          <Item label={t("기본 TTL")} keywords="default ttl cache duration" description={t("원본이 Cache-Control 헤더를 보내지 않을 때 정적 자산에 쓰는 캐시 기간입니다. HTML 캐싱에는 영향을 주지 않습니다.")}>
            <Select aria-label={t("기본 TTL")} value={String(c.default_ttl_seconds)} onChange={(e) => patch("cdn-ttl", { cdn: { default_ttl_seconds: Number(e.target.value) } })}>
              {TTL_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </Select>
          </Item>
          <Item label="Stale-While-Revalidate" keywords="swr stale while revalidate stale-if-error">
            <ToggleCard checked={c.honor_swr} onChange={(v) => patch("cdn-swr", { cdn: { honor_swr: v } })} label={t("SWR 지시어 따르기")} />
          </Item>
          <Item label={t("배포 시 캐시 비우기")} keywords="purge on deploy invalidate" description={t("배포가 성공할 때마다 캐시된 콘텐츠를 무효화해 다음 요청이 원본에서 다시 받아 오게 합니다.")}>
            <Select aria-label={t("배포 시 캐시 비우기")} value={c.purge_on_deploy} onChange={(e) => patch("cdn-pod", { cdn: { purge_on_deploy: e.target.value as PurgeOnDeploy } })}>
              <option value="html">{t("배포에 성공할 때마다 HTML 비우기(기본)")}</option>
              <option value="all">{t("배포에 성공할 때마다 전부 비우기")}</option>
              <option value="off">{t("비우지 않음")}</option>
            </Select>
          </Item>
          <Item label={t("캐시 비우기")} keywords="purge cache invalidate">
            <div className="flex flex-col gap-3">
              <div className="flex gap-2">
                <Button variant="outline" loading={busy === "purge-html"} onClick={() => purge("html")}>
                  {busy !== "purge-html" && (
                    <span className="text-gray-500">
                      <RefreshCw />
                    </span>
                  )}
                  <span className="inline-block min-w-0">{t("HTML 비우기")}</span>
                </Button>
                <Button
                  variant="outline"
                  loading={busy === "purge-all"}
                  onClick={() =>
                    confirm({
                      title: t("캐시 전체 비우기"),
                      message: t("정적 자산을 포함한 캐시 전체를 비웁니다. 다음 요청부터 서비스가 다시 응답합니다."),
                      confirmLabel: t("전체 비우기"),
                      run: () => purge("all"),
                    })
                  }
                >
                  {busy !== "purge-all" && (
                    <span className="text-gray-500">
                      <RefreshCw />
                    </span>
                  )}
                  <span className="inline-block min-w-0">{t("전체 비우기")}</span>
                </Button>
              </div>
              <div className="grid gap-1">
                <p className="text-sm text-gray-600">
                  {tr("HTML 마지막 비움: {last}", { last: c.last_purged_html ? <span className="tabular-nums" title={fmtTime(c.last_purged_html)}>{fmtRelative(c.last_purged_html)}</span> : t("없음") })}
                </p>
                <p className="text-sm text-gray-600">
                  {tr("전체 마지막 비움: {last}", { last: c.last_purged_all ? <span className="tabular-nums" title={fmtTime(c.last_purged_all)}>{fmtRelative(c.last_purged_all)}</span> : t("없음") })}
                </p>
              </div>
            </div>
          </Item>
        </>
      )}
      {node}
    </>
  );
}

/* ── Under Attack Mode ───────────────────────────────────────────────── */

type Duration = "" | "1h" | "3h" | "12h" | "24h";

function UnderAttackSection({ s, busy, start, stop }: { s: EdgeSettingsT; busy: string | null; start: (d: Duration) => Promise<boolean>; stop: () => Promise<boolean> }) {
  const [duration, setDuration] = useState<Duration>("");
  const [, tick] = useState(0);
  useEffect(() => {
    if (!s.under_attack_active) return;
    const timer = setInterval(() => tick((n) => n + 1), 30_000);
    return () => clearInterval(timer);
  }, [s.under_attack_active]);
  const ua = s.waf.under_attack;
  return (
    <Item
      label={t("공격 방어 모드")}
      keywords="under attack mode ddos bot challenge 공격 봇"
      description={t("DDoS 공격을 받고 있나요? 한 번에 도메인 앞에 브라우저 확인을 두어 엣지에서 봇 폭주를 걸러 냅니다. 실제 방문자는 한 번 통과하면 평소처럼 이용합니다.")}
    >
      {s.under_attack_active && ua ? (
        <div className="grid gap-3 rounded-md border border-red-200 bg-red-50 px-4 py-3">
          <div className="flex items-center gap-2.5">
            <span className="relative flex size-2.5">
              <span className="absolute inline-flex size-full animate-ping rounded-full bg-red-400" />
              <span className="relative inline-flex size-2.5 rounded-full bg-red-500" />
            </span>
            <p className="text-base font-medium text-red-600">{ua.until ? t("{until} 꺼짐 ({time})", { until: fmtUntil(ua.until), time: fmtTime(ua.until) }) : t("끌 때까지 켜져 있습니다")}</p>
          </div>
          <p className="text-sm text-gray-600">
            {tr("새 방문자는 모두 브라우저 확인을 통과해야 합니다. 확인된 브라우저 세션이 없는 API 클라이언트와 웹훅은 막힙니다. {since}부터 켜져 있습니다.", { since: <span className="tabular-nums">{fmtTime(ua.since)}</span> })}
          </p>
          <div>
            <Button variant="danger" loading={busy === "ua"} onClick={() => stop()}>
              {busy !== "ua" && <ShieldOff className="text-white" />}
              <span className="inline-block min-w-0">{t("비활성화")}</span>
            </Button>
          </div>
        </div>
      ) : (
        <div className="flex flex-wrap items-center gap-2">
          <Select aria-label={t("유지 기간")} size="sm" className="w-44" value={duration} onChange={(e) => setDuration(e.target.value as Duration)}>
            <option value="">{t("끌 때까지")}</option>
            <option value="1h">{t("1시간")}</option>
            <option value="3h">{t("3시간")}</option>
            <option value="12h">{t("12시간")}</option>
            <option value="24h">{t("24시간")}</option>
          </Select>
          <Button variant="danger" disabled={!s.available} loading={busy === "ua"} onClick={() => start(duration)}>
            {busy !== "ua" && <Shield className="text-white" />}
            <span className="inline-block min-w-0">{t("활성화")}</span>
          </Button>
        </div>
      )}
    </Item>
  );
}

/* ── WAF: IP 목록, 요청 제한, 관리형 규칙 ──────────────────────────────── */

const WINDOWS = [
  { value: "1", label: t("1초") },
  { value: "10", label: t("10초") },
  { value: "60", label: t("1분") },
  { value: "300", label: t("5분") },
  { value: "3600", label: t("1시간") },
];

const splitList = (v: string) =>
  v
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter(Boolean);

function WAFSection({ s, busy, patch }: { s: EdgeSettingsT; busy: string | null; patch: (k: string, p: EdgePatch, ok?: string) => Promise<boolean> }) {
  const w = s.waf;
  const [allow, setAllow] = useState(w.allow_ips.join("\n"));
  const [deny, setDeny] = useState(w.deny_ips.join("\n"));
  const [rlOn, setRlOn] = useState(!!w.rate_limit);
  const [rlReq, setRlReq] = useState(String(w.rate_limit?.requests ?? 100));
  const [rlWin, setRlWin] = useState(String(w.rate_limit?.window_seconds ?? 60));
  const [managed, setManaged] = useState(w.managed_rules);
  const rate = rlOn ? { requests: Number(rlReq), window_seconds: Number(rlWin) } : null;
  const dirty =
    splitList(allow).join(",") !== w.allow_ips.join(",") ||
    splitList(deny).join(",") !== w.deny_ips.join(",") ||
    JSON.stringify(rate) !== JSON.stringify(w.rate_limit) ||
    managed !== w.managed_rules;
  const reqInvalid = rlOn && (!/^\d+$/.test(rlReq) || Number(rlReq) < 1);
  const reset = () => {
    setAllow(w.allow_ips.join("\n"));
    setDeny(w.deny_ips.join("\n"));
    setRlOn(!!w.rate_limit);
    setRlReq(String(w.rate_limit?.requests ?? 100));
    setRlWin(String(w.rate_limit?.window_seconds ?? 60));
    setManaged(w.managed_rules);
  };
  return (
    <Item label={t("방화벽(WAF)")} keywords="waf firewall ip block allow rate limit 차단 허용 요청 제한" description={t("엣지에서 IP 차단, 클라이언트 IP별 요청 제한, 흔한 공격 패턴(경로 조작, SQL 주입, XSS, Log4Shell, 스캐너) 차단을 적용합니다.")}>
      <form
        className="grid gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (!dirty || reqInvalid) return;
          void patch("waf", { waf: { allow_ips: splitList(allow), deny_ips: splitList(deny), rate_limit: rate, managed_rules: managed } }, t("방화벽 설정을 저장했습니다"));
        }}
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <label className="grid content-start gap-1.5 text-xs font-semibold text-gray-600">
            {t("허용 IP / CIDR")}
            <Textarea mono rows={3} value={allow} onChange={(e) => setAllow(e.target.value)} placeholder={"203.0.113.7\n198.51.100.0/24"} />
            <span className="font-normal text-gray-500">{t("WAF 검사(차단 목록, 요청 제한, 관리형 규칙, 공격 방어 모드)를 건너뜁니다")}</span>
          </label>
          <label className="grid content-start gap-1.5 text-xs font-semibold text-gray-600">
            {t("차단 IP / CIDR")}
            <Textarea mono rows={3} value={deny} onChange={(e) => setDeny(e.target.value)} placeholder={"192.0.2.0/24\n2001:db8::/32"} />
            <span className="font-normal text-gray-500">{t("403으로 거절합니다. 한 줄에 하나 또는 쉼표로 구분")}</span>
          </label>
        </div>
        <div className="grid gap-3">
          <ToggleCard checked={rlOn} onChange={setRlOn} label={t("요청 제한 사용")} />
          {rlOn && (
            <div className="flex flex-wrap items-center gap-2 text-sm text-gray-600">
              <Input size="sm" mono className={cx(FIELD, "w-24 tabular-nums")} inputMode="numeric" aria-label={t("허용 요청 수")} invalid={reqInvalid} value={rlReq} onChange={(e) => setRlReq(e.target.value)} />
              <span>{t("회 /")}</span>
              <Select aria-label={t("창 길이")} size="sm" className="w-28" value={rlWin} onChange={(e) => setRlWin(e.target.value)}>
                {(WINDOWS.some((o) => o.value === rlWin) ? WINDOWS : [...WINDOWS, { value: rlWin, label: t("{seconds}초", { seconds: rlWin }) }]).map((o) => (
                  <option key={o.value} value={o.value}>
                    {o.label}
                  </option>
                ))}
              </Select>
              <span className="text-xs text-gray-500">{t("클라이언트 IP(IPv6는 /64)마다, 넘으면 429")}</span>
            </div>
          )}
          <ToggleCard checked={managed} onChange={setManaged} label={t("관리형 규칙으로 흔한 공격 요청 차단")} />
        </div>
        {dirty && (
          <div className="flex gap-2">
            <Button type="submit" variant="primary" loading={busy === "waf"} disabled={reqInvalid}>
              {t("저장")}
            </Button>
            <Button variant="outline" onClick={reset}>
              {t("되돌리기")}
            </Button>
          </div>
        )}
      </form>
    </Item>
  );
}

/* ── 엣지 규칙 ───────────────────────────────────────────────────────── */

const ATTR_LABEL: Record<EdgeAttr, string> = {
  "ipv4.src": t("클라이언트 IPv4"),
  "ip.src": t("클라이언트 IP(v4/v6)"),
  "http.host": t("호스트"),
  "http.path": t("경로"),
  "http.header": t("헤더"),
  "http.method": t("메서드"),
};
const OP_LABEL: Record<EdgeOp, string> = { eq: t("같음"), neq: t("같지 않음"), in: t("목록에 있음"), not_in: t("목록에 없음"), contains: t("포함"), matches: t("패턴 일치(*)") };
const ACTION_LABEL: Record<EdgeActionName, string> = {
  block: t("차단|action"),
  allow: t("허용(이후 규칙 건너뜀)"),
  challenge: t("브라우저 확인"),
  redirect: t("리다이렉트"),
  cache_override: t("캐시 재정의"),
  rewrite: t("경로 다시 쓰기"),
  set_header: t("헤더 설정"),
};

function clauseText(c: EdgeClause) {
  const v = Array.isArray(c.value) ? c.value.join(", ") : c.value;
  return `${ATTR_LABEL[c.attr] ?? c.attr}${c.key ? ` ${c.key}` : ""} ${OP_LABEL[c.op] ?? c.op} ${v}`;
}

function exprText(e: EdgeExpr): string {
  const flat = flatConditions(e);
  if (flat) return flat.clauses.map(clauseText).join(flat.join === "and" ? t(" 그리고 ") : t(" 또는 "));
  return t("중첩 조건(JSON)");
}

function actionText(r: EdgeRule) {
  const p = r.then.params ?? {};
  switch (r.then.action) {
    case "block":
      return t("차단 {status}", { status: p.status ?? 403 });
    case "redirect":
      return `${p.status ?? 302} → ${p.location ?? p.host}`;
    case "cache_override":
      return p.bypass ? t("캐시 우회") : t("{seconds}초 캐시", { seconds: p.ttl_seconds ?? "" });
    case "rewrite":
      return `→ ${p.path}`;
    case "set_header":
      return `${p.target === "request" ? t("요청") : t("응답")} ${p.name}: ${p.value || t("(삭제)")}`;
    default:
      return ACTION_LABEL[r.then.action];
  }
}

const NEW_RULE: EdgeRule = { priority: 0, enabled: true, if: { attr: "http.path", op: "matches", value: "/admin*" }, then: { action: "block" } };

function RulesSection({ s, busy, save, lastError }: { s: EdgeSettingsT; busy: string | null; save: (rs: EdgeRuleset) => Promise<boolean>; lastError: () => unknown }) {
  const [rules, setRules] = useState<EdgeRule[]>(s.rules.rules);
  const [editing, setEditing] = useState<{ index: number; rule: EdgeRule } | null>(null);
  const [json, setJson] = useState<string | null>(null);
  const [jsonErr, setJsonErr] = useState<unknown>(null);
  const dirty = JSON.stringify(rules) !== JSON.stringify(s.rules.rules);
  const ordered = (list: EdgeRule[]): EdgeRuleset => ({ version: 1, rules: list.map((r, i) => ({ ...r, priority: (i + 1) * 10 })) });
  const move = (i: number, d: number) => {
    const next = [...rules];
    const [r] = next.splice(i, 1);
    next.splice(i + d, 0, r);
    setRules(next);
  };
  return (
    <Item
      label={t("엣지 규칙")}
      keywords="edge rules redirect rewrite header block 리다이렉트 헤더 차단"
      description={t("서비스에 닿기 전에 조건(IP, 호스트, 경로, 헤더, 메서드)에 맞는 요청을 차단, 리다이렉트, 브라우저 확인하거나 캐시를 재정의합니다. 규칙은 위에서부터 차례로 적용되며, 화살표로 순서를 바꿉니다.")}
    >
      {rules.length > 0 ? (
        <ul className="divide-y divide-gray-200 rounded-lg border border-gray-200">
          {rules.map((r, i) => (
            <li key={r.id ?? i} className={cx("flex items-center gap-3 px-3 py-2", r.enabled === false && "opacity-60")}>
              <Toggle checked={r.enabled !== false} onChange={(v) => setRules(rules.map((x, j) => (j === i ? { ...x, enabled: v } : x)))} label={t("규칙 켜기")} />
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium">{r.description || t("규칙 {n}", { n: i + 1 })}</div>
                <div className="truncate font-mono text-xs text-gray-500" title={exprText(r.if)}>
                  {exprText(r.if)} → {actionText(r)}
                </div>
              </div>
              <IconButton size="sm" label={t("위로")} disabled={i === 0} onClick={() => move(i, -1)}>
                <ArrowUp />
              </IconButton>
              <IconButton size="sm" label={t("아래로")} disabled={i === rules.length - 1} onClick={() => move(i, 1)}>
                <ArrowDown />
              </IconButton>
              <IconButton size="sm" label={t("편집")} onClick={() => setEditing({ index: i, rule: r })}>
                <Pencil />
              </IconButton>
              <IconButton size="sm" label={t("삭제")} onClick={() => setRules(rules.filter((_, j) => j !== i))}>
                <Trash2 />
              </IconButton>
            </li>
          ))}
        </ul>
      ) : (
        <div className="grid justify-items-start gap-3 rounded-md border border-dashed border-gray-200 p-4">
          <p className="text-sm text-gray-600">{t("설정된 엣지 규칙이 없습니다.")}</p>
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" disabled={!s.available} onClick={() => setEditing({ index: -1, rule: NEW_RULE })}>
              {t("규칙 추가")}
            </Button>
            <Button variant="outline" onClick={() => (setJsonErr(null), setJson(JSON.stringify(ordered(rules), null, 2)))}>
              {t("JSON으로 편집")}
            </Button>
          </div>
        </div>
      )}
      <div className={cx("mt-3 flex flex-wrap gap-2", rules.length === 0 && !dirty && "hidden")}>
        <Button
          variant="outline"
          disabled={!s.available}
          onClick={() =>
            setEditing({ index: -1, rule: NEW_RULE })
          }
        >
          <Plus /> {t("규칙 추가")}
        </Button>
        <Button
          variant="outline"
          onClick={() => {
            setJsonErr(null);
            setJson(JSON.stringify(ordered(rules), null, 2));
          }}
        >
          <Braces /> {t("JSON으로 편집")}
        </Button>
        {dirty && (
          <>
            <Button variant="primary" loading={busy === "rules"} onClick={() => save(ordered(rules))}>
              {t("저장")}
            </Button>
            <Button variant="outline" onClick={() => setRules(s.rules.rules)}>
              {t("되돌리기")}
            </Button>
          </>
        )}
      </div>
      {editing && (
        <RuleDialog
          rule={editing.rule}
          cdn={s.cdn.enabled}
          onClose={() => setEditing(null)}
          onDone={(r) => {
            setRules(editing.index < 0 ? [...rules, r] : rules.map((x, j) => (j === editing.index ? r : x)));
            setEditing(null);
          }}
        />
      )}
      <Dialog
        open={json !== null}
        wide
        onClose={() => setJson(null)}
        title={t("엣지 규칙 JSON")}
        footer={
          <>
            <Button variant="outline" onClick={() => setJson(null)}>
              {t("취소")}
            </Button>
            <Button
              variant="primary"
              loading={busy === "rules"}
              onClick={async () => {
                let parsed: EdgeRuleset;
                try {
                  parsed = JSON.parse(json ?? "");
                } catch (e) {
                  setJsonErr(t("JSON 형식 오류: {error}", { error: errorMessage(e) }));
                  return;
                }
                setJsonErr(null);
                if (await save(parsed)) setJson(null);
                // The save error is rendered behind the dialog, so show it here (saved rules are unchanged).
                else setJsonErr(t("서버가 규칙을 거부했습니다. 저장된 규칙은 바뀌지 않았습니다. {error}", { error: errorMessage(lastError() ?? "") }).trim());
              }}
            >
              {t("검사 후 저장")}
            </Button>
          </>
        }
      >
        <p className="mb-3">
          {tr("Railway 규칙 형식({format})입니다. 저장할 때 전체를 검사하고, 하나라도 틀리면 아무것도 바꾸지 않습니다. {priority}가 작은 규칙부터 평가합니다.", {
            format: <span className="font-mono">{`{"version":1,"rules":[…]}`}</span>,
            priority: <span className="font-mono">priority</span>,
          })}
        </p>
        <ErrorBox error={jsonErr} className="mb-3" />
        <Textarea mono rows={20} className="w-full" value={json ?? ""} onChange={(e) => setJson(e.target.value)} aria-label={t("엣지 규칙 JSON")} />
      </Dialog>
    </Item>
  );
}

const ACTIONS: EdgeActionName[] = ["block", "allow", "challenge", "redirect", "cache_override", "rewrite", "set_header"];

function RuleDialog({ rule, cdn, onClose, onDone }: { rule: EdgeRule; cdn: boolean; onClose: () => void; onDone: (r: EdgeRule) => void }) {
  const flat = flatConditions(rule.if);
  const [desc, setDesc] = useState(rule.description ?? "");
  const [join, setJoin] = useState<"and" | "or">(flat?.join ?? "and");
  const [clauses, setClauses] = useState<EdgeClause[]>(flat?.clauses ?? []);
  const [rawIf, setRawIf] = useState(flat ? "" : JSON.stringify(rule.if, null, 2));
  const [action, setAction] = useState<EdgeActionName>(rule.then.action);
  const [p, setP] = useState<EdgeActionParams>(rule.then.params ?? {});
  const [err, setErr] = useState<string | null>(null);
  const setClause = (i: number, c: Partial<EdgeClause>) => setClauses(clauses.map((x, j) => (j === i ? { ...x, ...c } : x)));
  const setParam = (k: keyof EdgeActionParams, v: unknown) => setP((cur) => ({ ...cur, [k]: v === "" || v === undefined ? undefined : v }));

  const submit = () => {
    let cond: EdgeExpr;
    if (flat) {
      if (clauses.length === 0) return setErr(t("조건을 하나 이상 추가하세요"));
      cond = joinConditions(join, clauses);
    } else {
      try {
        cond = JSON.parse(rawIf);
      } catch (e) {
        return setErr(t("조건 JSON 형식 오류: {error}", { error: errorMessage(e) }));
      }
    }
    // 동작에 맞는 파라미터만 남긴다.
    const keep: Record<EdgeActionName, (keyof EdgeActionParams)[]> = {
      block: ["status", "body"],
      allow: [],
      challenge: [],
      redirect: p.location ? ["status", "location"] : ["status", "host", "preserve_path", "preserve_query"],
      cache_override: p.bypass ? ["bypass"] : ["ttl_seconds"],
      rewrite: ["path"],
      set_header: ["name", "value", "target"],
    };
    const params: EdgeActionParams = {};
    for (const k of keep[action]) if (p[k] !== undefined) (params as Record<string, unknown>)[k] = p[k];
    if (action === "cache_override" && !cdn) return setErr(t("캐시 재정의는 CDN 캐싱을 켠 뒤에 쓸 수 있습니다"));
    onDone({
      ...rule,
      description: desc.trim() || undefined,
      if: cond,
      then: Object.keys(params).length ? { action, params } : { action },
    });
  };

  return (
    <Dialog
      open
      wide
      onClose={onClose}
      title={rule.id ? t("규칙 편집") : t("규칙 추가")}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("취소")}
          </Button>
          <Button variant="primary" onClick={submit}>
            {t("적용")}
          </Button>
        </>
      }
    >
      <div className="grid gap-4">
        <label className="grid content-start gap-1.5 text-xs font-semibold text-gray-600">
          {t("이름(선택)")}
          <Input size="sm" className={FIELD} value={desc} maxLength={256} onChange={(e) => setDesc(e.target.value)} placeholder={t("관리자 페이지 차단")} />
        </label>
        <div className="grid gap-2">
          <div className="flex items-center justify-between gap-2">
            <span className="text-xs font-semibold text-gray-600">{t("조건")}</span>
            {flat && clauses.length > 1 && (
              <Segmented<"and" | "or">
                label={t("조건 결합")}
                value={join}
                onChange={setJoin}
                options={[
                  { value: "and", label: t("모두 맞을 때") },
                  { value: "or", label: t("하나라도 맞을 때") },
                ]}
              />
            )}
          </div>
          {flat ? (
            <>
              {clauses.map((c, i) => {
                const list = c.op === "in" || c.op === "not_in";
                return (
                  <div key={i} className="flex flex-wrap items-center gap-2">
                    <Select
                      aria-label={t("속성")}
                      size="sm" className="w-44"
                      value={c.attr}
                      onChange={(e) => {
                        const attr = e.target.value as EdgeAttr;
                        const op = ATTR_OPS[attr].includes(c.op) ? c.op : ATTR_OPS[attr][0];
                        setClause(i, { attr, op, key: attr === "http.header" ? (c.key ?? "") : undefined });
                      }}
                    >
                      {(Object.keys(ATTR_LABEL) as EdgeAttr[]).map((a) => (
                        <option key={a} value={a}>
                          {ATTR_LABEL[a]}
                        </option>
                      ))}
                    </Select>
                    {c.attr === "http.header" && (
                      <Input size="sm" mono className={cx(FIELD, "w-36")} aria-label={t("헤더 이름")} placeholder="x-client" value={c.key ?? ""} onChange={(e) => setClause(i, { key: e.target.value })} />
                    )}
                    <Select
                      aria-label={t("연산자")}
                      size="sm" className="w-40"
                      value={c.op}
                      onChange={(e) => {
                        const op = e.target.value as EdgeOp;
                        const toList = op === "in" || op === "not_in";
                        const cur = Array.isArray(c.value) ? c.value : splitList(c.value);
                        setClause(i, { op, value: toList ? cur : cur.join(",") });
                      }}
                    >
                      {ATTR_OPS[c.attr].map((o) => (
                        <option key={o} value={o}>
                          {OP_LABEL[o]}
                        </option>
                      ))}
                    </Select>
                    <Input
                      size="sm"
                      mono
                      className={cx(FIELD, "min-w-40 flex-1")}
                      aria-label={t("값")}
                      placeholder={list ? t("쉼표로 구분") : c.attr === "http.path" ? "/admin*" : ""}
                      value={Array.isArray(c.value) ? c.value.join(", ") : c.value}
                      onChange={(e) => setClause(i, { value: list ? splitList(e.target.value) : e.target.value })}
                    />
                    <IconButton size="sm" label={t("조건 삭제")} onClick={() => setClauses(clauses.filter((_, j) => j !== i))}>
                      <X />
                    </IconButton>
                  </div>
                );
              })}
              <div>
                <Button size="sm" variant="outline" onClick={() => setClauses([...clauses, { attr: "http.path", op: "eq", value: "" }])}>
                  <Plus /> {t("조건 추가")}
                </Button>
              </div>
              <p className="text-xs text-gray-500">{t("패턴 일치의 *는 0개 이상의 아무 문자입니다(/admin*은 /admin, /admin/users와 맞습니다). 값이 없는 헤더나 IP는 부정 비교에도 맞지 않습니다.")}</p>
            </>
          ) : (
            <>
              <p className="text-xs text-gray-500">{t("중첩된 조건(and/or/not)은 JSON으로 편집합니다.")}</p>
              <Textarea mono rows={8} value={rawIf} onChange={(e) => setRawIf(e.target.value)} aria-label={t("조건 JSON")} />
            </>
          )}
        </div>
        <div className="grid gap-2">
          <span className="text-xs font-semibold text-gray-600">{t("동작")}</span>
          <Select aria-label={t("동작")} size="sm" className="w-60" value={action} onChange={(e) => setAction(e.target.value as EdgeActionName)}>
            {ACTIONS.map((a) => (
              <option key={a} value={a} disabled={a === "cache_override" && !cdn}>
                {ACTION_LABEL[a]}
                {a === "cache_override" && !cdn ? t(" (CDN 필요)") : ""}
              </option>
            ))}
          </Select>
          <ActionParams action={action} p={p} setParam={setParam} setP={setP} />
        </div>
        {err && <ErrorBox error={err} />}
      </div>
    </Dialog>
  );
}

function ActionParams({
  action,
  p,
  setParam,
  setP,
}: {
  action: EdgeActionName;
  p: EdgeActionParams;
  setParam: (k: keyof EdgeActionParams, v: unknown) => void;
  setP: (fn: (cur: EdgeActionParams) => EdgeActionParams) => void;
}) {
  const num = (s: string) => (s.trim() === "" ? undefined : Number(s));
  switch (action) {
    case "block":
      return (
        <div className="grid gap-2 sm:grid-cols-[8rem_1fr]">
          <Input size="sm" mono className={FIELD} aria-label={t("상태 코드")} placeholder="403" value={p.status ?? ""} onChange={(e) => setParam("status", num(e.target.value))} />
          <Input size="sm" className={FIELD} aria-label={t("응답 본문")} placeholder={t("응답 본문(선택)")} value={p.body ?? ""} onChange={(e) => setParam("body", e.target.value)} />
        </div>
      );
    case "redirect": {
      const toHost = p.location === undefined;
      return (
        <div className="grid gap-2">
          <Segmented<"host" | "location">
            label={t("리다이렉트 대상")}
            value={toHost ? "host" : "location"}
            onChange={(v) => setP((cur) => (v === "host" ? { status: cur.status, host: "" } : { status: cur.status, location: "https://" }))}
            options={[
              { value: "host", label: t("다른 호스트로") },
              { value: "location", label: t("정확한 URL로") },
            ]}
          />
          <div className="flex flex-wrap items-center gap-2">
            {toHost ? (
              <Input size="sm" mono className={cx(FIELD, "flex-1")} aria-label={t("호스트")} placeholder="www.example.com" value={p.host ?? ""} onChange={(e) => setParam("host", e.target.value)} />
            ) : (
              <Input size="sm" mono className={cx(FIELD, "flex-1")} aria-label="URL" placeholder="https://example.com/new" value={p.location ?? ""} onChange={(e) => setP((cur) => ({ ...cur, location: e.target.value }))} />
            )}
            <Select aria-label={t("상태 코드")} size="sm" className="w-28" value={String(p.status ?? 302)} onChange={(e) => setParam("status", Number(e.target.value))}>
              {[301, 302, 307, 308].map((c) => (
                <option key={c} value={c}>
                  {c}
                </option>
              ))}
            </Select>
          </div>
          {toHost && (
            <div className="flex flex-wrap gap-4 text-sm text-gray-600">
              <label className="flex items-center gap-2">
                <Toggle checked={p.preserve_path !== false} onChange={(v) => setParam("preserve_path", v ? undefined : false)} label={t("경로 유지")} />
                {t("경로 유지")}
              </label>
              <label className="flex items-center gap-2">
                <Toggle checked={p.preserve_query !== false} onChange={(v) => setParam("preserve_query", v ? undefined : false)} label={t("쿼리 유지")} />
                {t("쿼리 유지")}
              </label>
            </div>
          )}
        </div>
      );
    }
    case "cache_override":
      return (
        <div className="flex flex-wrap items-center gap-3 text-sm text-gray-600">
          <label className="flex items-center gap-2">
            <Toggle checked={!!p.bypass} onChange={(v) => setP(() => (v ? { bypass: true } : { ttl_seconds: 3600 }))} label={t("캐시 우회")} />
            {t("캐시 우회")}
          </label>
          {!p.bypass && (
            <>
              <Input size="sm" mono className={cx(FIELD, "w-28")} aria-label={t("캐시 기간(초)")} value={p.ttl_seconds ?? ""} onChange={(e) => setParam("ttl_seconds", num(e.target.value))} />
              <span>{t("초 동안 캐시(1초 ~ 30일)")}</span>
            </>
          )}
        </div>
      );
    case "rewrite":
      return <Input size="sm" mono className={FIELD} aria-label={t("새 경로")} placeholder="/v2/index.html" value={p.path ?? ""} onChange={(e) => setParam("path", e.target.value)} />;
    case "set_header":
      return (
        <div className="grid gap-2 sm:grid-cols-[8rem_1fr_1fr]">
          <Select aria-label={t("대상")} size="sm" value={p.target ?? "response"} onChange={(e) => setParam("target", e.target.value === "response" ? undefined : e.target.value)}>
            <option value="response">{t("응답")}</option>
            <option value="request">{t("요청")}</option>
          </Select>
          <Input size="sm" mono className={FIELD} aria-label={t("헤더 이름")} placeholder="X-Frame-Options" value={p.name ?? ""} onChange={(e) => setParam("name", e.target.value)} />
          <Input size="sm" mono className={FIELD} aria-label={t("헤더 값")} placeholder={t("DENY (비우면 삭제)")} value={p.value ?? ""} onChange={(e) => setParam("value", e.target.value)} />
        </div>
      );
    case "challenge":
      return <p className="text-xs text-gray-500">{t("확인을 통과하지 않은 방문자에게 브라우저 확인 페이지를 보여 주고, 통과한 방문자는 다음 규칙으로 계속 갑니다.")}</p>;
    default:
      return <p className="text-xs text-gray-500">{t("맞는 요청을 서비스로 보내고 이후 규칙은 건너뜁니다.")}</p>;
  }
}
