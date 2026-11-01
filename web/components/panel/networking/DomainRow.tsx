"use client";

/* Domain row, modeled on Railway's dashboard (generated: globe + link + "→ Port N"; custom: status cloud + link + meta line) with its sliding
   edit panel (domain input + suffix, target-port dropdown, Cancel/Update). The keel-only domain options (TLS mode,
   path rules, redirect, basic auth) live in the custom/generated edit panel below the form. */

import { useState, type ReactNode } from "react";
import { AlertTriangle, ChevronDown, Cloud, Globe, Lock, Plus, SquarePen, Trash2 } from "lucide-react";
import { api, type CanvasService, type DetectedPort, type Domain, type DomainTLS, type Environment, type PathRule, type ServiceInstance } from "@/lib/api";
import { t, tr } from "@/lib/i18n";
import { useCanvas } from "@/lib/queries";
import { Button, ErrorBox, IconButton, Input, LABEL_CLS, Segmented, Select, Toggle, cx, useConfirm } from "../../ui";
import { FIELD } from "../settings/primitives";
import { DnsRecordsTable } from "./DnsRecordsTable";
import { DomainStateLabel, domainState, isProblem, type DomainExtra } from "./DomainStatus";
import { PortPicker, initialPortChoice, type PortChoice } from "./PortPicker";
import { CopyBtn, HostLink, IconBtn, PanelFooter, PortMeta, Sliding, SuffixInput, portOk, useNetRun } from "./parts";
import { checkRedirectTarget, isAuthUser, isLabel, pathRuleProblems, type PathProblem } from "./validate";

const CF_DOCS = "https://developers.cloudflare.com/ssl/origin-configuration/ssl-modes/full-strict/";
const TUNNEL_DOCS = "https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/";

/** Status icon colour: green working, yellow waiting, red error. */
const ICON_TONE: Record<ReturnType<typeof domainState>, string> = {
  pending: "text-yellow-600",
  verified: "text-green-600",
  tls_pending: "text-yellow-600",
  tls_issued: "text-green-600",
  http_only: "text-gray-500",
  proxied: "text-green-600",
  error: "text-red-600",
  tls_error: "text-red-600",
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
  d: DomainExtra;
  detected: DetectedPort[];
  servicePort: number;
  ipv6Only?: boolean;
  onDelete: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const state = domainState(d);
  // DNS panel: open while the domain needs attention; re-opens when the state turns into a problem later
  // (e.g. a record was removed), but the user's own toggle wins until the state changes again.
  const [dnsToggle, setDnsToggle] = useState<{ state: typeof state; open: boolean } | null>(null);
  const showDns = dnsToggle && dnsToggle.state === state ? dnsToggle.open : isProblem(state);
  const setShowDns = (fn: (v: boolean) => boolean) => setDnsToggle({ state, open: fn(showDns) });
  const { run, busy, error } = useNetRun(svc, env);
  const custom = d.kind === "custom";
  const port = d.effective_port ?? d.target_port ?? servicePort;
  const extras = [
    d.redirect && t("리다이렉트"),
    d.basic_auth && "basic auth",
    d.paths?.length ? t("경로 규칙 {count}", { count: d.paths.length }) : null,
    d.tls === "off" && d.proxy !== "tunnel" && state !== "http_only" ? t("HTTP만") : null,
  ].filter(Boolean) as string[];
  const actions = (
    <>
      <CopyBtn value={`https://${d.host}`} />
      <IconBtn title={t("수정")} pressed={editing} onClick={() => setEditing((e) => !e)}>
        <SquarePen />
      </IconBtn>
      <IconBtn title={t("도메인 삭제")} onClick={onDelete}>
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
      {custom && !d.proxy && (
        <>
          {sep}
          <DomainStateLabel d={d} onClick={d.dns.length > 0 || d.verification ? () => setShowDns((v) => !v) : undefined} expanded={showDns} />
        </>
      )}
      {!custom && (state === "error" || state === "tls_error" || state === "tls_pending" || state === "http_only") && (
        <>
          {sep}
          <DomainStateLabel d={d} />
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
  const dns = custom && showDns && (d.dns.length > 0 || !!d.verification) && (
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
          <span className={cx("icon-container icon-md self-center text-2xl", ICON_TONE[state])} aria-hidden="true">
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
            {error && !editing ? <ErrorBox error={error} /> : null}
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
  const hostValid = custom || isLabel(prefix.trim().toLowerCase());
  const portChanged = portOk(port.text) && Number(port.text) !== cur;
  const canSubmit = hostValid && portOk(port.text) && (hostChanged || portChanged);
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
        live
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

/**
 * Local edits of one server value. Shows the server value until the user edits, and goes back to following it after
 * `reset()` (called after a successful save), so a refetch never leaves stale text behind and unsaved edits survive polling.
 */
function useDraft<T>(server: T) {
  const [draft, setDraft] = useState<{ v: T } | null>(null);
  const value = draft ? draft.v : server;
  return {
    value,
    dirty: !!draft && JSON.stringify(draft.v) !== JSON.stringify(server),
    set: (next: T | ((cur: T) => T)) => setDraft({ v: typeof next === "function" ? (next as (cur: T) => T)(value) : next }),
    reset: () => setDraft(null),
  };
}

function pathProblemText(p: PathProblem) {
  switch (p) {
    case "path":
      return t("경로는 /로 시작하고 공백 없이 256자 이하여야 합니다");
    case "duplicate":
      return t("같은 경로 규칙이 이미 있습니다");
    case "strip":
      return t("접두어 제거는 /*로 끝나는 경로에만 쓸 수 있습니다");
    case "port":
      return t("포트는 1부터 65535 사이의 숫자입니다");
  }
}

export function DomainAdvanced({ svc, env, d, embedded }: { svc: CanvasService; env: Environment; inst?: ServiceInstance; d: Domain; embedded?: boolean }) {
  const canvas = useCanvas(env.id);
  const { confirm, node: confirmNode } = useConfirm();
  const { run, busy, error } = useNetRun(svc, env);
  const [open, setOpen] = useState(!!embedded);
  const paths = useDraft<PathRule[]>(d.paths ?? []);
  const redirect = useDraft({ to: d.redirect?.to ?? "", permanent: d.redirect?.permanent ?? true });
  const authUser = useDraft(d.basic_auth?.username ?? "");
  const [authPw, setAuthPw] = useState("");
  const others = (canvas.data?.services ?? []).filter((s) => s.id !== svc.id);
  const summary = [
    d.paths?.length ? t("경로 {count}", { count: d.paths.length }) : "",
    d.tls ? `TLS ${d.tls}` : "",
    d.redirect ? t("리다이렉트") : "",
    d.basic_auth ? "basic auth" : "",
  ].filter(Boolean);

  const save = (key: string, input: Parameters<typeof api.patchDomain>[3]) => run(key, () => api.patchDomain(svc.id, env.id, d.host, input));
  const setPath = (i: number, patch: Partial<PathRule>) => paths.set((p) => p.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const pathProblems = pathRuleProblems(paths.value);
  const pathsInvalid = pathProblems.some(Boolean);
  const redirectTo = redirect.value.to.trim();
  const redirectProblem = redirectTo ? checkRedirectTarget(redirectTo, d.host) : null;
  const userTrim = authUser.value.trim();
  const userInvalid = !!userTrim && !isAuthUser(userTrim);

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
              onChange={(v) => {
                const input = { tls: v === "default" ? ("" as const) : v };
                if ((d.tls || "default") === v) return;
                // Turning TLS off changes what visitors get (no certificate, no https redirect): ask first.
                if (v === "off")
                  confirm({
                    title: t("HTTP만 사용"),
                    message: tr("{host}의 인증서를 쓰지 않고 https 리다이렉트도 하지 않습니다. 앞단(Cloudflare, 터널)이 TLS를 끝내는 경우에만 쓰세요.", {
                      host: <span className="font-mono text-foreground">{d.host}</span>,
                    }),
                    confirmLabel: t("HTTP만 사용"),
                    danger: true,
                    run: () => save("tls", input),
                  });
                else void save("tls", input);
              }}
            />
            {busy === "tls" && <p className="mt-1 text-xs text-gray-500">{t("저장 중…")}</p>}
            {d.tls === "off" && (
              <div className="flex items-center gap-2 pt-1 text-sm text-gray-600">
                <Toggle label={t("앞단 X-Forwarded-Proto 신뢰")} checked={!!d.trust_forwarded_proto} disabled={busy === "trust"} onChange={(v) => void save("trust", { trust_forwarded_proto: v })} />
                {tr("앞단 프록시가 보낸 {header}를 앱에 그대로 전달", { header: <span className="font-mono">X-Forwarded-Proto</span> })}
              </div>
            )}
          </Row>

          <Row
            label={t("경로 규칙")}
            hint={
              d.redirect
                ? t("리다이렉트 도메인에는 경로 규칙을 둘 수 없습니다. 먼저 리다이렉트를 해제하세요.")
                : tr("가장 구체적인 경로가 먼저 적용됩니다. {prefixEx}는 접두어, {exactEx}는 정확히 일치입니다. 규칙에 걸리지 않은 요청은 이 서비스로 갑니다.", {
                    prefixEx: <span className="font-mono">/api/*</span>,
                    exactEx: <span className="font-mono">/healthz</span>,
                  })
            }
          >
            <div className="space-y-2">
              {paths.value.map((r, i) => {
                const problem = pathProblems[i];
                return (
                  <div key={i} className="space-y-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <Input size="sm" mono value={r.path} onChange={(e) => setPath(i, { path: e.target.value })} placeholder="/api/*" aria-label={t("경로")} invalid={problem === "path" || problem === "duplicate"} className={cx(FIELD, "w-[150px]")} />
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
                        onChange={(e) => {
                          const digits = e.target.value.replace(/[^0-9]/g, "").slice(0, 5);
                          setPath(i, { target_port: digits ? Number(digits) : undefined });
                        }}
                        placeholder={t("포트")}
                        aria-label={t("대상 포트")}
                        invalid={problem === "port"}
                        className={cx(FIELD, "w-[80px] tabular-nums")}
                      />
                      <span className="flex items-center gap-2 text-sm text-gray-600">
                        <Toggle label={t("접두어 제거")} checked={!!r.strip_prefix} onChange={(v) => setPath(i, { strip_prefix: v })} /> {t("접두어 제거")}
                      </span>
                      <IconButton label={t("경로 규칙 삭제")} size="sm" onClick={() => paths.set((p) => p.filter((_, j) => j !== i))}>
                        <Trash2 className="size-3.5" />
                      </IconButton>
                    </div>
                    {problem && <p className="text-xs leading-[18px] text-red-600">{pathProblemText(problem)}</p>}
                  </div>
                );
              })}
              <div className="flex flex-wrap gap-2">
                <Button variant="outline" disabled={!!d.redirect || paths.value.length >= 50} onClick={() => paths.set((p) => [...p, { path: "" }])}>
                  <Plus /> {t("규칙 추가")}
                </Button>
                <Button
                  variant="primary"
                  disabled={!paths.dirty || pathsInvalid}
                  loading={busy === "paths"}
                  onClick={async () => {
                    const clean = paths.value.filter((p) => p.path.trim()).map((p) => ({ ...p, path: p.path.trim() }));
                    if (await save("paths", { paths: clean })) paths.reset();
                  }}
                >
                  {t("경로 규칙 저장")}
                </Button>
                {paths.dirty && (
                  <Button variant="outline" onClick={paths.reset}>
                    {t("되돌리기")}
                  </Button>
                )}
              </div>
            </div>
          </Row>

          <Row label={t("리다이렉트")} hint={t("켜면 이 도메인은 앱으로 보내지 않고 대상으로 이동시킵니다(예: www → 루트 도메인). 경로와 쿼리는 유지됩니다.")}>
            <div className="space-y-1">
              <div className="flex flex-wrap items-center gap-2">
                <Input
                  size="sm"
                  value={redirect.value.to}
                  onChange={(e) => redirect.set((r) => ({ ...r, to: e.target.value }))}
                  placeholder={t("example.com 또는 https://example.com/base")}
                  aria-label={t("리다이렉트 대상")}
                  invalid={!!redirectProblem}
                  className={cx(FIELD, "max-w-[320px]")}
                />
                <span className="flex items-center gap-2 text-sm text-gray-600">
                  <Toggle label={t("영구(308)")} checked={redirect.value.permanent} onChange={(v) => redirect.set((r) => ({ ...r, permanent: v }))} /> {t("영구(308)")}
                </span>
                <Button
                  variant="primary"
                  loading={busy === "redirect"}
                  disabled={!redirectTo || !!redirectProblem || (!!d.redirect && !redirect.dirty)}
                  onClick={async () => {
                    if (await save("redirect", { redirect: { to: redirectTo, permanent: redirect.value.permanent } })) redirect.reset();
                  }}
                >
                  {t("저장")}
                </Button>
                {d.redirect && (
                  <Button
                    variant="outline"
                    loading={busy === "redirect-off"}
                    onClick={() =>
                      confirm({
                        title: t("리다이렉트 해제"),
                        message: tr("{host}로 오는 요청을 다시 이 서비스로 보냅니다.", { host: <span className="font-mono text-foreground">{d.host}</span> }),
                        confirmLabel: t("해제"),
                        danger: true,
                        run: async () => {
                          if (await save("redirect-off", { redirect: null })) redirect.reset();
                        },
                      })
                    }
                  >
                    {t("해제")}
                  </Button>
                )}
              </div>
              {redirectProblem && (
                <p className="text-xs leading-[18px] text-red-600">
                  {redirectProblem === "loop" ? t("자기 자신으로 리다이렉트하면 무한 반복됩니다") : t("호스트 이름이나 http(s) 주소를 입력하세요(공백 없이 300자 이하)")}
                </p>
              )}
              {d.paths?.length && !d.redirect ? <p className="text-xs leading-[18px] text-gray-600">{t("경로 규칙이 있는 도메인은 리다이렉트할 수 없습니다. 먼저 경로 규칙을 지우세요.")}</p> : null}
            </div>
          </Row>

          <Row label="Basic auth" hint={t("호스트 전체(모든 경로 규칙 포함)에 HTTP basic auth를 겁니다. 비밀번호는 bcrypt로 저장되고 API로 다시 나오지 않습니다.")}>
            <form
              className="space-y-1"
              onSubmit={async (e) => {
                e.preventDefault();
                if (!userTrim || userInvalid || (!authPw && !d.basic_auth?.has_password)) return;
                if (await save("auth", { basic_auth: { username: userTrim, ...(authPw ? { password: authPw } : {}) } })) {
                  setAuthPw("");
                  authUser.reset();
                }
              }}
            >
              <div className="flex flex-wrap items-center gap-2">
                <Input size="sm" value={authUser.value} onChange={(e) => authUser.set(e.target.value)} placeholder={t("사용자 이름")} aria-label={t("basic auth 사용자")} invalid={userInvalid} className={cx(FIELD, "w-[160px]")} autoComplete="off" />
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
                <Button type="submit" variant="primary" loading={busy === "auth"} disabled={!userTrim || userInvalid || (!authPw && !d.basic_auth?.has_password) || (!authPw && !authUser.dirty)}>
                  <Lock /> {t("저장")}
                </Button>
                {d.basic_auth && (
                  <Button
                    variant="outline"
                    loading={busy === "auth-off"}
                    onClick={() =>
                      confirm({
                        title: t("basic auth 해제"),
                        message: tr("{host}에 누구나 비밀번호 없이 접속할 수 있게 됩니다.", { host: <span className="font-mono text-foreground">{d.host}</span> }),
                        confirmLabel: t("해제"),
                        danger: true,
                        run: async () => {
                          if (await save("auth-off", { basic_auth: null })) {
                            authUser.reset();
                            setAuthPw("");
                          }
                        },
                      })
                    }
                  >
                    {t("해제")}
                  </Button>
                )}
              </div>
              {userInvalid && <p className="text-xs leading-[18px] text-red-600">{t("사용자 이름은 영문, 숫자, . _ @ - 로 64자까지 쓸 수 있습니다")}</p>}
            </form>
          </Row>
          {error ? <ErrorBox error={error} /> : null}
        </div>
      )}
      {confirmNode}
    </div>
  );
}
