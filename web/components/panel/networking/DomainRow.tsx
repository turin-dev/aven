"use client";

/* Domain row, modeled on Railway's dashboard (generated: globe + link + "→ Port N"; custom: status cloud + link + meta line) with its sliding
   edit panel (domain input + suffix, target-port dropdown, Cancel/Update). The keel-only domain options (TLS mode,
   path rules, redirect, basic auth) live in the custom/generated edit panel below the form. */

import { useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ChevronDown, Cloud, Globe, Lock, Plus, SquarePen, Trash2 } from "lucide-react";
import { api, type CanvasService, type DetectedPort, type Domain, type DomainTLS, type Environment, type PathRule, type ServiceInstance } from "@/lib/api";
import { t, tr } from "@/lib/i18n";
import { qk, useCanvas } from "@/lib/queries";
import { Button, ErrorBox, IconButton, Input, LABEL_CLS, Segmented, Select, Toggle, cx } from "../../ui";
import { FIELD } from "../settings/primitives";
import { DnsRecordsTable } from "./DnsRecordsTable";
import { PortPicker, initialPortChoice, type PortChoice } from "./PortPicker";
import { CopyBtn, HostLink, IconBtn, PanelFooter, PortMeta, Sliding, SuffixInput, portOk, useNetRun } from "./parts";

const CF_DOCS = "https://developers.cloudflare.com/ssl/origin-configuration/ssl-modes/full-strict/";
const TUNNEL_DOCS = "https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/";
const LABEL_RE = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;

/** Status icon colour: green working, yellow waiting for DNS, red error. */
const ICON_TONE: Record<Domain["status"], string> = {
  active: "text-green-600",
  proxied: "text-green-600",
  pending_dns: "text-yellow-600",
  error: "text-red-600",
};

export function DomainRow({
  svc,
  env,
  inst,
  d,
  detected,
  servicePort,
  ipv6Only,
  onDelete,
}: {
  svc: CanvasService;
  env: Environment;
  inst: ServiceInstance;
  d: Domain;
  detected: DetectedPort[];
  servicePort: number;
  ipv6Only?: boolean;
  onDelete: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const [showDns, setShowDns] = useState(d.status === "pending_dns" || d.status === "error");
  const { run, busy, error } = useNetRun(svc, env);
  const custom = d.kind === "custom";
  const port = d.effective_port ?? d.target_port ?? servicePort;
  const extras = [
    d.redirect && t("리다이렉트"),
    d.basic_auth && "basic auth",
    d.paths?.length ? t("경로 규칙 {count}", { count: d.paths.length }) : null,
    d.tls === "off" && d.proxy !== "tunnel" ? "HTTP만" : null,
  ].filter(Boolean) as string[];
  const actions = (
    <>
      <CopyBtn value={`https://${d.host}`} />
      <IconBtn title={t("수정")} pressed={editing} onClick={() => setEditing((e) => !e)}>
        <SquarePen />
      </IconBtn>
      <IconBtn title={t("삭제")} onClick={onDelete}>
        <Trash2 />
      </IconBtn>
    </>
  );
  const sep = " · ";
  const meta = (
    <PortMeta port={port}>
      {custom && d.proxy === "cloudflare" && (
        <>
          {sep}
          <button type="button" onClick={() => setShowDns((v) => !v)} aria-expanded={showDns} className="cursor-pointer text-pink-600 hover:underline">
            {t("Cloudflare 프록시 감지됨")}
          </button>
          <button
            type="button"
            aria-label={t("Cloudflare 설정 경고 보기")}
            title={t("Cloudflare SSL/TLS 모드를 Full(strict)로 두세요. Flexible이면 리다이렉트가 반복됩니다. 캐시 규칙은 앱 응답 헤더를 따르도록 두는 편이 안전합니다.")}
            className="ml-1 inline-flex cursor-pointer items-center rounded p-0.5 align-middle text-yellow-600 hover:bg-gray-200 hover:text-yellow-700"
          >
            <span aria-hidden="true" className="icon-container icon-xs text-lg text-current">
              <AlertTriangle />
            </span>
          </button>
          {sep}
          <a href={CF_DOCS} target="_blank" rel="noreferrer" className="text-pink-600 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-700">
            {t("문서 보기")}
          </a>
        </>
      )}
      {custom && d.proxy === "tunnel" && (
        <>
          {sep}
          <button type="button" onClick={() => setShowDns((v) => !v)} aria-expanded={showDns} className="cursor-pointer text-pink-600 hover:underline">
            {t("Cloudflare 터널")}
          </button>
          {sep}
          <a href={TUNNEL_DOCS} target="_blank" rel="noreferrer" className="text-pink-600 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-700">
            {t("문서 보기")}
          </a>
        </>
      )}
      {custom && !d.proxy && (d.status === "pending_dns" || d.status === "error") && (
        <>
          {sep}
          <button type="button" onClick={() => setShowDns((v) => !v)} aria-expanded={showDns} className={cx("cursor-pointer hover:underline", d.status === "error" ? "text-red-600" : "text-pink-600")}>
            {d.status === "error" ? t("DNS 확인 실패") : t("DNS 레코드 대기 중")}
          </button>
          <button
            type="button"
            aria-label={t("DNS 레코드 안내 보기")}
            title={t("DNS 제공자에 레코드를 추가해야 합니다")}
            onClick={() => setShowDns((v) => !v)}
            className="ml-1 inline-flex cursor-pointer items-center rounded p-0.5 align-middle text-yellow-600 hover:bg-gray-200 hover:text-yellow-700"
          >
            <span aria-hidden="true" className="icon-container icon-xs text-lg text-current">
              <AlertTriangle />
            </span>
          </button>
        </>
      )}
      {custom && !d.proxy && d.status === "active" && d.dns.length > 0 && (
        <>
          {sep}
          <button type="button" onClick={() => setShowDns((v) => !v)} aria-expanded={showDns} className="cursor-pointer text-pink-600 hover:underline">
            {t("DNS 레코드")}
          </button>
        </>
      )}
      {d.ipv6 && (
        <span className="inline-flex items-center" data-testid="domain-ipv6">
          {sep}
          <span title={t("이 도메인 전용 공개 IPv6입니다. AAAA 레코드를 이 주소로 두면 도메인마다 다른 주소를 씁니다.")} className="ml-1 font-mono">
            IPv6 {d.ipv6}
          </span>
          <span className="ml-0.5 inline-flex">
            <CopyBtn value={d.ipv6} title={t("전용 IPv6 복사")} small />
          </span>
        </span>
      )}
      {extras.map((x) => (
        <span key={x}>
          {sep}
          {x}
        </span>
      ))}
    </PortMeta>
  );
  const dns = custom && showDns && d.dns.length > 0 && (
    <DnsRecordsTable
      d={d}
      ipv6Only={ipv6Only}
      verifying={busy === "verify"}
      onVerify={() => void run("verify", () => api.verifyDomain(svc.id, env.id, d.host))}
    />
  );
  return (
    <>
      {custom ? (
        <div className="group flex items-center space-x-3 px-4 py-3 first:rounded-t-lg last:rounded-b-lg hover:bg-secondaryBg dark:hover:bg-gray-100">
          <span className={cx("icon-container icon-md self-center text-2xl", ICON_TONE[d.status])} aria-hidden="true" title={d.status}>
            <Cloud />
          </span>
          <div className="grid min-w-0 flex-grow gap-1">
            <div className="flex min-w-0 items-center justify-between gap-4">
              <div className="flex min-w-0 items-center gap-1 text-sm">
                <HostLink host={d.host} />
              </div>
              <div className="ml-auto flex items-center gap-1">{actions}</div>
            </div>
            {meta}
            {dns}
            {error && !editing ? <ErrorBox error={error} /> : null}
          </div>
        </div>
      ) : (
        <div className="group flex w-full items-center px-4 py-3 first:rounded-t-lg last:rounded-b-lg hover:bg-secondaryBg dark:hover:bg-gray-100">
          <div className="mr-3">
            <span className="icon-container icon-md text-2xl text-gray-500" aria-hidden="true">
              <Globe />
            </span>
          </div>
          <div className="grid min-w-0 flex-1 gap-1">
            <HostLink host={d.host} block />
            {meta}
          </div>
          <div className="ml-auto flex flex-shrink-0 space-x-1">{actions}</div>
        </div>
      )}
      <Sliding open={editing}>
        <DomainEditForm svc={svc} env={env} d={d} detected={detected} servicePort={servicePort} onDone={() => setEditing(false)} />
        <DomainAdvanced svc={svc} env={env} inst={inst} d={d} embedded />
      </Sliding>
    </>
  );
}

function DomainEditForm({
  svc,
  env,
  d,
  detected,
  servicePort,
  onDone,
}: {
  svc: CanvasService;
  env: Environment;
  d: Domain;
  detected: DetectedPort[];
  servicePort: number;
  onDone: () => void;
}) {
  const custom = d.kind === "custom";
  const dot = d.host.indexOf(".");
  const suffix = custom ? "" : d.host.slice(dot);
  const [prefix, setPrefix] = useState(custom ? d.host : d.host.slice(0, dot));
  const cur = d.target_port ?? d.effective_port ?? servicePort;
  const [port, setPort] = useState<PortChoice>(() => initialPortChoice(cur, detected));
  const { run, busy, error } = useNetRun(svc, env);
  const nextHost = custom ? d.host : `${prefix.trim().toLowerCase()}${suffix}`;
  const hostChanged = nextHost !== d.host;
  const hostValid = custom || LABEL_RE.test(prefix.trim().toLowerCase());
  const portChanged = portOk(port.text) && Number(port.text) !== cur;
  const canSubmit = hostValid && (hostChanged || portChanged) && (port.mode === "pick" || portOk(port.text));
  return (
    <form
      className="space-y-4"
      onSubmit={async (e) => {
        e.preventDefault();
        if (!canSubmit) return;
        const input = { ...(hostChanged ? { host: nextHost } : {}), ...(portChanged ? { target_port: Number(port.text) } : {}) };
        if (await run("save", () => api.patchDomain(svc.id, env.id, d.host, input))) onDone();
      }}
    >
      <div className="flex items-center gap-4">
        <SuffixInput
          label={t("도메인")}
          name={`domain-${d.host}`}
          value={prefix}
          onChange={custom ? undefined : (v) => setPrefix(v.toLowerCase())}
          suffix={suffix || undefined}
          disabled={custom}
          placeholder="example.com"
          invalid={!hostValid}
          className="grow"
        />
      </div>
      <PortPicker id={`dom-${d.host}`} value={port} onChange={setPort} detected={detected} servicePort={servicePort} />
      {error ? <ErrorBox error={error} /> : null}
      <PanelFooter
        message={!hostValid ? t("소문자, 숫자, 하이픈(-)만 쓸 수 있습니다") : custom ? t("대상 포트를 수정하세요") : t("도메인이나 대상 포트를 수정하세요")}
        tone={hostValid ? "info" : "bad"}
        onCancel={onDone}
        canSubmit={canSubmit}
        busy={busy === "save"}
      />
    </form>
  );
}

/* ── 도메인 고급 설정 (aven 전용: TLS 모드, 경로 규칙, 리다이렉트, basic auth) ─────────── */

function Row({ label, hint, children }: { label: string; hint?: ReactNode; children: ReactNode }) {
  return (
    <div>
      <div className={LABEL_CLS}>{label}</div>
      {children}
      {hint && <div className="mt-2 text-xs leading-[18px] text-gray-600">{hint}</div>}
    </div>
  );
}

/* ── 도메인 고급 설정 ─────────────────────────────────────────────────── */

const TLS_OPTIONS: { value: DomainTLS | "default"; label: string }[] = [
  { value: "default", label: t("서버 기본값") },
  { value: "auto", label: t("자동(ACME)") },
  { value: "internal", label: t("내부 CA") },
  { value: "off", label: t("HTTP만") },
];

export function DomainAdvanced({ svc, env, inst, d, embedded }: { svc: CanvasService; env: Environment; inst: ServiceInstance; d: Domain; embedded?: boolean }) {
  const qc = useQueryClient();
  const canvas = useCanvas(env.id);
  const [open, setOpen] = useState(!!embedded);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [paths, setPaths] = useState<PathRule[]>(d.paths ?? []);
  const [redirectTo, setRedirectTo] = useState(d.redirect?.to ?? "");
  const [permanent, setPermanent] = useState(d.redirect?.permanent ?? true);
  const [authUser, setAuthUser] = useState(d.basic_auth?.username ?? "");
  const [authPw, setAuthPw] = useState("");
  const others = (canvas.data?.services ?? []).filter((s) => s.id !== svc.id);
  const summary = [
    d.paths?.length ? t("경로 {count}", { count: d.paths.length }) : "",
    d.tls ? `TLS ${d.tls}` : "",
    d.redirect ? t("리다이렉트") : "",
    d.basic_auth ? "basic auth" : "",
  ].filter(Boolean);

  const save = async (key: string, input: Parameters<typeof api.patchDomain>[3]) => {
    setBusy(key);
    setError(null);
    try {
      await api.patchDomain(svc.id, env.id, d.host, input);
      await qc.invalidateQueries({ queryKey: qk.canvas(env.id) });
      await qc.invalidateQueries({ queryKey: qk.networking(svc.id, env.id) });
      return true;
    } catch (e) {
      setError(e);
      return false;
    } finally {
      setBusy(null);
    }
  };
  const setPath = (i: number, patch: Partial<PathRule>) => setPaths((p) => p.map((r, j) => (j === i ? { ...r, ...patch } : r)));

  return (
    <div className={embedded ? "" : "border-t border-gray-200 px-3.5 py-2"}>
      <button type="button" hidden={embedded} onClick={() => setOpen((o) => !o)} className="flex w-full cursor-pointer items-center gap-1.5 text-sm text-gray-600 hover:text-foreground" aria-expanded={open}>
        <ChevronDown className={cx("size-3.5 transition-transform", !open && "-rotate-90")} />
        {t("고급 설정")}
        {summary.length > 0 && <span className="text-gray-500">· {summary.join(", ")}</span>}
      </button>
      {open && (
        <div className="mt-3 space-y-6 pb-2">
          <Row
            label={t("TLS 모드")}
            hint={tr("{mode}은 인증서를 만들지 않고 https로 리다이렉트하지도 않습니다. cloudflared 터널처럼 앞단이 HTTP로 접속할 때 쓰세요.", {
              mode: <b>{t("HTTP만")}</b>,
            })}
          >
            <Segmented
              size="sm"
              label={t("TLS 모드")}
              value={(d.tls || "default") as DomainTLS | "default"}
              options={TLS_OPTIONS}
              onChange={(v) => save("tls", { tls: v === "default" ? "" : v })}
            />
            {d.tls === "off" && (
              <div className="flex items-center gap-2 pt-1 text-sm text-gray-600">
                <Toggle label={t("앞단 X-Forwarded-Proto 신뢰")} checked={!!d.trust_forwarded_proto} onChange={(v) => save("trust", { trust_forwarded_proto: v })} />
                {tr("앞단 프록시가 보낸 {header}를 앱에 그대로 전달", { header: <span className="font-mono">X-Forwarded-Proto</span> })}
              </div>
            )}
          </Row>

          <Row
            label={t("경로 규칙")}
            hint={tr("가장 구체적인 경로가 먼저 적용됩니다. {prefixEx}는 접두어, {exactEx}는 정확히 일치입니다. 규칙에 걸리지 않은 요청은 이 서비스로 갑니다.", {
              prefixEx: <span className="font-mono">/api/*</span>,
              exactEx: <span className="font-mono">/healthz</span>,
            })}
          >
            <div className="space-y-2">
              {paths.map((r, i) => (
                <div key={i} className="flex flex-wrap items-center gap-2">
                  <Input size="sm" mono value={r.path} onChange={(e) => setPath(i, { path: e.target.value })} placeholder="/api/*" aria-label={t("경로")} className={cx(FIELD, "w-[150px]")} />
                  <Select value={r.service ?? ""} onChange={(e) => setPath(i, { service: e.target.value || undefined })} aria-label={t("대상 서비스")} size="sm" className="w-[150px]">
                    <option value="">{t("{name} (이 서비스)", { name: svc.name })}</option>
                    {others.map((s) => (
                      <option key={s.id} value={s.name}>
                        {s.name}
                      </option>
                    ))}
                  </Select>
                  <Input
                    size="sm"
                    mono
                    inputMode="numeric"
                    value={r.target_port ?? ""}
                    onChange={(e) => setPath(i, { target_port: e.target.value ? Number(e.target.value.replace(/[^0-9]/g, "")) : undefined })}
                    placeholder={t("포트")}
                    aria-label={t("대상 포트")}
                    className={cx(FIELD, "w-[80px] tabular-nums")}
                  />
                  <span className="flex items-center gap-2 text-sm text-gray-600">
                    <Toggle label={t("접두어 제거")} checked={!!r.strip_prefix} onChange={(v) => setPath(i, { strip_prefix: v })} /> {t("접두어 제거")}
                  </span>
                  <IconButton label={t("경로 규칙 삭제")} size="sm" onClick={() => setPaths((p) => p.filter((_, j) => j !== i))}>
                    <Trash2 className="size-3.5" />
                  </IconButton>
                </div>
              ))}
              <div className="flex gap-2">
                <Button variant="outline" onClick={() => setPaths((p) => [...p, { path: "" }])}>
                  <Plus /> {t("규칙 추가")}
                </Button>
                <Button variant="primary" disabled={JSON.stringify(paths) === JSON.stringify(d.paths ?? [])} loading={busy === "paths"} onClick={() => save("paths", { paths: paths.filter((p) => p.path.trim()).map((p) => ({ ...p, path: p.path.trim() })) })}>
                  {t("경로 규칙 저장")}
                </Button>
              </div>
            </div>
          </Row>

          <Row label={t("리다이렉트")} hint={t("켜면 이 도메인은 앱으로 보내지 않고 대상으로 이동시킵니다(예: www → 루트 도메인). 경로와 쿼리는 유지됩니다.")}>
            <div className="flex flex-wrap items-center gap-2">
              <Input size="sm" value={redirectTo} onChange={(e) => setRedirectTo(e.target.value)} placeholder={t("example.com 또는 https://example.com/base")} aria-label={t("리다이렉트 대상")} className={cx(FIELD, "max-w-[320px]")} />
              <span className="flex items-center gap-2 text-sm text-gray-600">
                <Toggle label={t("영구(308)")} checked={permanent} onChange={setPermanent} /> {t("영구(308)")}
              </span>
              <Button variant="primary" loading={busy === "redirect"} disabled={!redirectTo.trim()} onClick={() => save("redirect", { redirect: { to: redirectTo.trim(), permanent } })}>
                {t("저장")}
              </Button>
              {d.redirect && (
                <Button variant="outline" loading={busy === "redirect-off"} onClick={async () => (await save("redirect-off", { redirect: null })) && setRedirectTo("")}>
                  {t("해제")}
                </Button>
              )}
            </div>
          </Row>

          <Row label="Basic auth" hint={t("호스트 전체(모든 경로 규칙 포함)에 HTTP basic auth를 겁니다. 비밀번호는 bcrypt로 저장되고 API로 다시 나오지 않습니다.")}>
            <div className="flex flex-wrap items-center gap-2">
              <Input size="sm" value={authUser} onChange={(e) => setAuthUser(e.target.value)} placeholder={t("사용자 이름")} aria-label={t("basic auth 사용자")} className={cx(FIELD, "w-[160px]")} autoComplete="off" />
              <Input
                size="sm"
                type="password"
                value={authPw}
                onChange={(e) => setAuthPw(e.target.value)}
                placeholder={d.basic_auth?.has_password ? t("(저장됨, 바꾸려면 입력)") : t("비밀번호")}
                aria-label={t("basic auth 비밀번호")}
                className={cx(FIELD, "w-[200px]")}
                autoComplete="new-password"
              />
              <Button
                variant="primary"
                loading={busy === "auth"}
                disabled={!authUser.trim() || (!authPw && !d.basic_auth?.has_password)}
                onClick={async () => {
                  if (await save("auth", { basic_auth: { username: authUser.trim(), ...(authPw ? { password: authPw } : {}) } })) setAuthPw("");
                }}
              >
                <Lock /> {t("저장")}
              </Button>
              {d.basic_auth && (
                <Button variant="outline" loading={busy === "auth-off"} onClick={async () => (await save("auth-off", { basic_auth: null })) && setAuthUser("")}>
                  {t("해제")}
                </Button>
              )}
            </div>
          </Row>
          {error ? <ErrorBox error={error} /> : null}
        </div>
      )}
    </div>
  );
}
