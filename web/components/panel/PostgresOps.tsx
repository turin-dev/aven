"use client";


import { useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ArrowUpCircle, CheckCircle2, Info, RefreshCw, RotateCcw, Trash2, XCircle } from "lucide-react";
import type { CanvasService, Environment } from "@/lib/api";
import { qk, useCaps } from "@/lib/queries";
import { fmtBytes, fmtTime } from "@/lib/format";
import { pgKeys, postgresApi, type PoolMode, type PostgresCheck, type PostgresPooling, type PostgresUpgrade } from "@/lib/postgres";
import { lock } from "../Perm";
import { Button, Card, ErrorBox, Field, Input, Notice, Select, Spinner, cx, useConfirm } from "../ui";
import { t, tr } from "@/lib/i18n";

const MODES: { value: PoolMode; label: string; desc: string }[] = [
  { value: "transaction", label: t("Transaction (기본)"), desc: t("트랜잭션마다 서버 연결을 다시 씁니다. 다중화가 가장 좋지만 LISTEN/NOTIFY, advisory lock, 문장 사이에 유지되는 SET 같은 세션 기능은 쓸 수 없습니다.") },
  { value: "session", label: "Session", desc: t("클라이언트가 연결을 끊을 때까지 서버 연결 하나를 씁니다. 모든 PostgreSQL 기능이 되지만 클라이언트가 연결을 끊어야 풀링 효과가 있습니다.") },
  { value: "statement", label: "Statement", desc: t("문장마다 서버 연결을 다시 씁니다. 여러 문장으로 된 트랜잭션은 쓸 수 없습니다.") },
];

const POOL_VARS: [string, string, string][] = [
  ["DATABASE_URL", t("PgBouncer · 비공개 네트워크"), t("일반 애플리케이션 쿼리")],
  ["DATABASE_PUBLIC_URL", t("PgBouncer · TCP 프록시"), t("aven 밖에서 연결")],
  ["DATABASE_UNPOOLED_URL", t("PostgreSQL · 비공개 네트워크"), t("마이그레이션, CREATE INDEX CONCURRENTLY, LISTEN/NOTIFY, advisory lock")],
  ["DATABASE_PUBLIC_UNPOOLED_URL", t("PostgreSQL · TCP 프록시"), t("aven 밖에서 풀링 없이 연결")],
];

function Section({ title, desc, children }: { title: string; desc: ReactNode; children: ReactNode }) {
  return (
    <Card title={title} description={desc || undefined}>
      {children}
    </Card>
  );
}

function useRun() {
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const run = async (key: string, fn: () => Promise<unknown>) => {
    setBusy(key);
    setError(null);
    try {
      await fn();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(null);
    }
  };
  return { busy, error, run };
}

export function PostgresOps({ svc, env }: { svc: CanvasService; env: Environment }) {
  return (
    <>
      <PostgresPoolingSection svc={svc} env={env} />
      <PostgresUpgradeSection svc={svc} env={env} />
    </>
  );
}

/* ── Connection pooling ─────────────────────────────────────────────── */

function PostgresPoolingSection({ svc, env }: { svc: CanvasService; env: Environment }) {
  const qc = useQueryClient();
  const canEdit = useCaps(svc.project_id).canEdit;
  const { confirm, node } = useConfirm();
  const { busy, error, run } = useRun();
  const q = useQuery({
    queryKey: pgKeys.pooling(svc.id, env.id),
    queryFn: () => postgresApi.pooling(svc.id, env.id),
    refetchInterval: (x) => (x.state.data?.status === "deploying" || x.state.data?.status === "updating" ? 2000 : false),
  });
  const [mode, setMode] = useState<PoolMode>("transaction");
  const p = q.data;

  const refresh = async (data?: PostgresPooling) => {
    if (data) qc.setQueryData(pgKeys.pooling(svc.id, env.id), data);
    await Promise.all([
      qc.invalidateQueries({ queryKey: pgKeys.pooling(svc.id, env.id) }),
      qc.invalidateQueries({ queryKey: qk.canvas(env.id) }),
      qc.invalidateQueries({ queryKey: qk.instance(svc.id, env.id) }),
      qc.invalidateQueries({ queryKey: qk.resolved(svc.id, env.id) }),
    ]);
  };

  const add = () =>
    confirm({
      title: t("PgBouncer 추가"),
      message: (
        <ul className="list-disc space-y-1 pl-5">
          <li>
            {tr("{svc} 서비스를 만들고 {mode} 모드로 배포합니다.", { svc: <span className="font-mono text-foreground">{svc.name}-pgbouncer</span>, mode: MODES.find((m) => m.value === mode)?.label })}
          </li>
          <li>{tr("배포가 끝나면 {url}이 PgBouncer를 가리키고, 직접 연결은 {unpooled}로 옮깁니다.", { url: <span className="font-mono">DATABASE_URL</span>, unpooled: <span className="font-mono">DATABASE_UNPOOLED_URL</span> })}</li>
          <li>{t("이 데이터베이스의 연결 변수를 참조하는 실행 중인 서비스는 다시 배포됩니다. aven 밖에 직접 적어 둔 연결 문자열은 직접 바꿔야 합니다.")}</li>
        </ul>
      ),
      confirmLabel: t("PgBouncer 추가"),
      run: () => run("add", async () => refresh(await postgresApi.setPooling(svc.id, env.id, { enabled: true, pool_mode: mode }))),
    });

  const remove = () =>
    confirm({
      title: t("PgBouncer 제거"),
      message: tr("활성 PgBouncer 연결이 모두 끊기고 {url}이 다시 PostgreSQL을 직접 가리킵니다. {unpooled} 변수는 지워지고, 참조하는 서비스는 다시 배포됩니다.", {
        url: <span className="font-mono text-foreground">DATABASE_URL</span>,
        unpooled: <span className="font-mono">DATABASE_UNPOOLED_URL</span>,
      }),
      confirmLabel: t("제거"),
      danger: true,
      run: () => run("remove", async () => refresh(await postgresApi.removePooling(svc.id, env.id))),
    });

  return (
    <Section
      title={t("연결 풀링")}
      desc={t("PgBouncer가 많은 애플리케이션 연결을 적은 수의 PostgreSQL 서버 연결로 묶어, max_connections보다 훨씬 많은 동시 클라이언트를 받을 수 있게 합니다.")}
    >
      {node}
      {q.error ? (
        <ErrorBox error={q.error} />
      ) : !p ? (
        <Spinner />
      ) : p.status === "off" ? (
        <div className="space-y-4">
          <div role="radiogroup" aria-label={t("풀 모드")} className="space-y-2">
            {MODES.map((m) => (
              <label
                key={m.value}
                className={cx(
                  "flex cursor-pointer gap-3 rounded-lg border px-4 py-3 text-sm",
                  mode === m.value ? "border-pink-400 bg-pink-100/40" : "border-gray-200 hover:border-gray-300",
                )}
              >
                <input type="radio" name={`pool-mode-${svc.id}`} className="mt-1 accent-pink-500" checked={mode === m.value} onChange={() => setMode(m.value)} />
                <span>
                  <span className="font-medium text-foreground">{m.label}</span>
                  <span className="mt-0.5 block text-gray-500">{m.desc}</span>
                </span>
              </label>
            ))}
          </div>
          <Button variant="primary" loading={busy === "add"} onClick={add} {...lock(canEdit)}>
            {t("PgBouncer 추가")}
          </Button>
        </div>
      ) : (
        <PoolingOverview svc={svc} env={env} p={p} canEdit={canEdit} busy={busy} run={run} refresh={refresh} onRemove={remove} />
      )}
      {error ? <ErrorBox error={error} className="mt-3" /> : null}
    </Section>
  );
}

function PoolingOverview({
  svc,
  env,
  p,
  canEdit,
  busy,
  run,
  refresh,
  onRemove,
}: {
  svc: CanvasService;
  env: Environment;
  p: PostgresPooling;
  canEdit: boolean;
  busy: string | null;
  run: (key: string, fn: () => Promise<unknown>) => Promise<void>;
  refresh: (d?: PostgresPooling) => Promise<void>;
  onRemove: () => void;
}) {
  const [mode, setMode] = useState<PoolMode | null>(null);
  const [size, setSize] = useState<string | null>(null);
  const [clients, setClients] = useState<string | null>(null);
  const [prepared, setPrepared] = useState<string | null>(null);
  const deploying = p.status === "deploying" || p.status === "updating";
  const curMode = mode ?? p.pool_mode;
  const sizeText = size ?? String(p.default_pool_size);
  const clientsText = clients ?? String(p.max_client_conn);
  const preparedText = prepared ?? (p.max_prepared_statements === null ? "" : String(p.max_prepared_statements));
  const dirty = curMode !== p.pool_mode || sizeText !== String(p.default_pool_size) || clientsText !== String(p.max_client_conn) ||
    preparedText !== (p.max_prepared_statements === null ? "" : String(p.max_prepared_statements));
  const digits = (v: string) => v.replace(/[^0-9]/g, "");

  const save = () =>
    run("save", async () => {
      const d = await postgresApi.setPooling(svc.id, env.id, {
        enabled: true,
        pool_mode: curMode,
        default_pool_size: Number(sizeText) || p.default_pool_size,
        max_client_conn: Number(clientsText) || p.max_client_conn,
        ...(preparedText === "" ? { reset_max_prepared_statements: true } : { max_prepared_statements: Number(preparedText) }),
      });
      setMode(null);
      setSize(null);
      setClients(null);
      setPrepared(null);
      await refresh(d);
    });

  return (
    <div className="space-y-4">
      <div className="rounded-lg border border-gray-200 bg-secondaryBg">
        <div className="flex flex-wrap items-center gap-3 border-b border-gray-200 px-4 py-3 text-sm">
          <span
            className={cx(
              "size-2 shrink-0 rounded-full",
              p.status === "enabled" ? "bg-green-600" : deploying ? "animate-soft-pulse bg-yellow-600" : "bg-red-600",
            )}
          />
          <span className="font-medium text-foreground">PgBouncer</span>
          {p.pooler_service_name && <span className="font-mono text-xs text-gray-500">{p.pooler_service_name}</span>}
          <span className="text-gray-500">
            {p.status === "enabled" ? t("사용 중") : p.status === "deploying" ? t("배포 중…") : p.status === "updating" ? t("새 설정 배포 중…") : t("배포 실패")}
          </span>
          <span className="ml-auto flex gap-2">
            {p.status === "failed" && (
              <Button size="sm" variant="accent" loading={busy === "retry"} onClick={() => run("retry", async () => refresh(await postgresApi.setPooling(svc.id, env.id, { enabled: true })))} {...lock(canEdit)}>
                <RefreshCw className="size-3.5" /> {t("다시 배포")}
              </Button>
            )}
            <Button size="sm" variant="outline" loading={busy === "remove"} disabled={deploying} onClick={onRemove} {...lock(canEdit)}>
              <Trash2 className="size-3.5" /> {t("제거")}
            </Button>
          </span>
        </div>
        {p.error && <div className="border-b border-gray-200 px-4 py-2 text-sm text-danger">{p.error}</div>}
        <div className="grid gap-4 px-4 py-4 sm:grid-cols-2">
          <Field label={t("풀 모드")}>
            <Select value={curMode} onChange={(e) => setMode(e.target.value as PoolMode)} aria-label={t("풀 모드")} disabled={!canEdit}>
              {MODES.map((m) => (
                <option key={m.value} value={m.value}>
                  {m.label}
                </option>
              ))}
            </Select>
          </Field>
          <Field label={t("DEFAULT_POOL_SIZE · 서버 연결 수")}>
            <Input mono inputMode="numeric" value={sizeText} onChange={(e) => setSize(digits(e.target.value))} disabled={!canEdit} />
          </Field>
          <Field label={t("MAX_CLIENT_CONN · 최대 클라이언트 연결")}>
            <Input mono inputMode="numeric" value={clientsText} onChange={(e) => setClients(digits(e.target.value))} disabled={!canEdit} />
          </Field>
          <Field label={t("MAX_PREPARED_STATEMENTS · 0이면 끔")}>
            <Input mono inputMode="numeric" placeholder={t("기본값 (200)")} value={preparedText} onChange={(e) => setPrepared(digits(e.target.value))} disabled={!canEdit} />
          </Field>
        </div>
        <div className="flex items-center gap-3 border-t border-gray-200 px-4 py-3">
          <p className="text-xs text-gray-500">
            {tr("서버 연결 수가 PostgreSQL {var}(기본 100)보다 넉넉히 작아야 마이그레이션·관리용 직접 연결을 쓸 수 있습니다. 바꾸면 PgBouncer만 다시 배포하고 데이터베이스는 재시작하지 않습니다.", { var: <span className="font-mono">max_connections</span> })}
          </p>
          <Button size="sm" variant="primary" className="ml-auto" loading={busy === "save"} disabled={!dirty || deploying || !sizeText || !clientsText} onClick={save} {...lock(canEdit)}>
            {t("적용")}
          </Button>
        </div>
      </div>

      <div className="overflow-x-auto rounded-lg border border-gray-200">
        <table className="w-full text-left text-sm">
          <thead className="bg-gray-50 text-xs text-gray-600">
            <tr>
              <th className="px-4 py-2 font-semibold">{t("변수")}</th>
              <th className="px-4 py-2 font-semibold">{t("가리키는 곳")}</th>
              <th className="px-4 py-2 font-semibold">{t("용도")}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-200">
            {POOL_VARS.map(([k, to, use]) => {
              const has = p.variables.includes(k);
              return (
                <tr key={k} className={cx(!has && "text-gray-400")}>
                  <td className="px-4 py-2 font-mono text-xs">{k}</td>
                  <td className="px-4 py-2">{to}</td>
                  <td className="px-4 py-2 text-gray-500">{has ? use : k.includes("PUBLIC") ? t("공개 접속(TCP 프록시)을 켜야 생깁니다") : t("배포가 끝나면 생깁니다")}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <p className="text-xs text-gray-500">
        {tr("Prisma는 {url}, {direct}로 쓰고, transaction 모드에서는 연결 문자열에 {pgb}를 붙이세요.", { url: <span className="font-mono">url = env(&quot;DATABASE_URL&quot;)</span>, direct: <span className="font-mono">directUrl = env(&quot;DATABASE_UNPOOLED_URL&quot;)</span>, pgb: <span className="font-mono">?pgbouncer=true</span> })}
      </p>
    </div>
  );
}

/* ── Major version upgrade ──────────────────────────────────────────── */

const PHASES: { key: string; label: string }[] = [
  { key: "preparing", label: t("데이터베이스를 멈추고 볼륨 백업") },
  { key: "upgrading", label: t("pg_upgrade 실행 후 새 메이저로 시작") },
  { key: "done", label: t("버전·데이터 확인과 업그레이드 뒤 백업") },
];

function CheckIcon({ c }: { c: PostgresCheck }) {
  if (c.passed && c.severity === "info") return <Info aria-label={t("정보")} className="mt-0.5 size-4 shrink-0 text-gray-500" />;
  if (c.passed) return <CheckCircle2 aria-label={t("통과")} className="mt-0.5 size-4 shrink-0 text-green-600" />;
  if (c.severity === "blocker") return <XCircle aria-label={t("차단|severity")} className="mt-0.5 size-4 shrink-0 text-red-600" />;
  return <AlertTriangle aria-label={t("경고")} className="mt-0.5 size-4 shrink-0 text-yellow-600" />;
}

function PostgresUpgradeSection({ svc, env }: { svc: CanvasService; env: Environment }) {
  const qc = useQueryClient();
  const canEdit = useCaps(svc.project_id).canEdit;
  const { confirm, node } = useConfirm();
  const { busy, error, run } = useRun();
  const st = useQuery({
    queryKey: pgKeys.upgrade(svc.id, env.id),
    queryFn: () => postgresApi.upgrade(svc.id, env.id),
    refetchInterval: (x) => (x.state.data?.in_progress ? 3000 : false),
  });
  const u = st.data;
  const [target, setTarget] = useState<number | null>(null);
  const targets = u?.available_targets ?? [];
  const to = target ?? targets[targets.length - 1] ?? 0;
  const pf = useQuery({
    queryKey: pgKeys.preflight(svc.id, env.id, to),
    queryFn: () => postgresApi.preflight(svc.id, env.id, to),
    enabled: !!u && !u.in_progress && to > 0,
    staleTime: 60_000,
    retry: false,
  });

  const refresh = async (data?: PostgresUpgrade) => {
    if (data) qc.setQueryData(pgKeys.upgrade(svc.id, env.id), data);
    await Promise.all([
      qc.invalidateQueries({ queryKey: pgKeys.upgrade(svc.id, env.id) }),
      qc.invalidateQueries({ queryKey: ["pg-preflight", svc.id, env.id] }),
      qc.invalidateQueries({ queryKey: qk.canvas(env.id) }),
      qc.invalidateQueries({ queryKey: qk.instance(svc.id, env.id) }),
    ]);
  };

  const start = () =>
    confirm({
      title: t("PostgreSQL {version}로 업그레이드", { version: to }),
      message: (
        <ul className="list-disc space-y-1 pl-5">
          <li>{t("pg_upgrade가 도는 동안 데이터베이스가 멈춰 잠시 쓸 수 없습니다(데이터 크기에 따라 보통 몇 분).")}</li>
          <li>{t("업그레이드 전에 볼륨 백업을, 새 메이저로 실행된 뒤 논리 백업을 하나씩 만듭니다.")}</li>
          <li>{t("preflight에 차단 항목이 없을 때만 시작합니다. 실패하면 업그레이드 전 백업으로 자동 복구합니다.")}</li>
        </ul>
      ),
      confirmLabel: t("PostgreSQL {version}로 업그레이드", { version: to }),
      run: () => run("start", async () => refresh(await postgresApi.startUpgrade(svc.id, env.id, to))),
    });

  const revert = () =>
    confirm({
      title: t("되돌리고 변경 사항 버리기"),
      message: tr("업그레이드 전 백업을 복원하고 PostgreSQL {major}({image})로 되돌립니다. 백업 이후에 쓴 데이터는 {gone}.", {
        major: u?.from_major,
        image: <span className="font-mono">{u?.from_image}</span>,
        gone: <b>{t("사라집니다")}</b>,
      }),
      confirmLabel: t("되돌리기"),
      danger: true,
      run: () => run("revert", async () => refresh(await postgresApi.revertUpgrade(svc.id, env.id))),
    });

  if (st.error || !u)
    return (
      <Section title={t("메이저 버전 업그레이드")} desc="">
        {st.error ? <ErrorBox error={st.error} /> : <Spinner />}
      </Section>
    );
  const phase = u.status === "preparing" ? 0 : u.status === "upgrading" ? 1 : 2;

  return (
    <Section
      title={t("메이저 버전 업그레이드")}
      desc={tr("pg_upgrade로 데이터를 그대로 둔 채 새 메이저 버전으로 옮깁니다. 지금 버전: {version} {image}", {
        version: <span className="font-medium text-foreground">{u.current_major ? t("PostgreSQL {major}", { major: String(u.current_major) }) : t("알 수 없음")}</span>,
        image: u.image && <span className="font-mono text-xs">({u.image})</span>,
      })}
    >
      {node}
      {u.in_progress && (
        <div className="mb-4 rounded-lg border border-gray-200 px-4 py-3 text-sm" aria-live="polite">
          <div className="mb-2 font-medium text-foreground">
            {u.status === "reverting" ? t("업그레이드 전 백업으로 되돌리는 중…") : t("PostgreSQL {from} → {to} 업그레이드 중…", { from: String(u.from_major), to: String(u.target_major) })}
          </div>
          {u.status === "reverting" ? (
            <div className="flex items-center gap-2 text-gray-500">
              <Spinner /> {t("볼륨을 복원하고 원래 이미지로 다시 배포합니다")}
            </div>
          ) : (
            <ol className="space-y-1.5">
              {PHASES.map((ph, i) => (
                <li key={ph.key} className={cx("flex items-center gap-2", i > phase ? "text-gray-400" : "text-gray-600")}>
                  {i < phase ? <CheckCircle2 className="size-4 text-green-600" /> : i === phase ? <Spinner /> : <span className="size-4 rounded-full border border-gray-300" />}
                  {ph.label}
                </li>
              ))}
            </ol>
          )}
        </div>
      )}
      {u.status === "succeeded" && (
        <Notice
          className="mb-4"
          action={
            u.can_revert && (
              <Button size="sm" variant="outline" loading={busy === "revert"} onClick={revert} {...lock(canEdit)}>
                <RotateCcw className="size-3.5" /> {t("되돌리고 변경 사항 버리기")}
              </Button>
            )
          }
        >
          {t("PostgreSQL {major}로 업그레이드했습니다.", { major: String(u.target_major) })}
          {u.can_revert && u.revert_deadline && t(" {time}까지 업그레이드 전 상태로 되돌릴 수 있습니다.", { time: fmtTime(u.revert_deadline) })}
        </Notice>
      )}
      {(u.status === "failed" || u.status === "revert_failed" || (u.status === "reverted" && u.error)) && (
        <div role="alert" className="mb-4 flex flex-wrap items-center gap-3 rounded-md bg-danger-surface p-3 text-sm text-danger">
          <XCircle className="size-4 shrink-0" />
          <span className="min-w-0 flex-1 break-words">{u.error || t("업그레이드에 실패했습니다.")}</span>
          {u.can_revert && (
            <Button size="sm" variant="outline" loading={busy === "revert"} onClick={revert} {...lock(canEdit)}>
              <RotateCcw className="size-3.5" /> {t("업그레이드 전 백업으로 되돌리기")}
            </Button>
          )}
        </div>
      )}
      {u.status === "reverted" && !u.error && <Notice className="mb-4">{t("PostgreSQL {major}로 되돌렸습니다.", { major: String(u.from_major) })}</Notice>}

      {!u.in_progress &&
        (targets.length === 0 ? (
          <p className="text-sm text-gray-500">
            {u.current_major ? t("PostgreSQL {major}은(는) 지원하는 최신 메이저이거나 업그레이드할 수 없는 버전입니다.", { major: String(u.current_major) }) : t("공식 postgres alpine 이미지(메이저 고정 태그)에서만 업그레이드할 수 있습니다.")}
          </p>
        ) : (
          <div className="space-y-4">
            <div className="flex flex-wrap items-center gap-2">
              <Select value={String(to)} onChange={(e) => setTarget(Number(e.target.value))} aria-label={t("대상 버전")} className="w-[180px]">
                {targets.map((ver) => (
                  <option key={ver} value={String(ver)}>
                    {t("PostgreSQL {version}", { version: ver })}
                  </option>
                ))}
              </Select>
              <Button size="lg" variant="outline" loading={pf.isFetching} onClick={() => pf.refetch()}>
                <RefreshCw /> {t("다시 검사")}
              </Button>
              <Button size="lg" variant="primary" className="ml-auto" loading={busy === "start"} disabled={!pf.data?.ready} onClick={start} {...lock(canEdit)}>
                <ArrowUpCircle /> {t("PostgreSQL {version}로 업그레이드", { version: to })}
              </Button>
            </div>
            {pf.error ? (
              <ErrorBox error={pf.error} />
            ) : !pf.data ? (
              <div className="flex items-center gap-2 text-sm text-gray-500">
                <Spinner /> {t("preflight 검사 중…")}
              </div>
            ) : (
              <div className="rounded-lg border border-gray-200">
                <div className="flex items-center gap-2 border-b border-gray-200 px-4 py-2.5 text-sm">
                  <span className="font-medium text-foreground">{t("Preflight 보고서")}</span>
                  <span className={cx("rounded px-1.5 py-0.5 text-xs", pf.data.ready ? "bg-success-surface text-success" : "bg-danger-surface text-danger")}>
                    {pf.data.ready ? t("차단 항목 없음") : t("차단 {count}개", { count: pf.data.checks.filter((c) => !c.passed && c.severity === "blocker").length })}
                  </span>
                  {pf.data.data_size_bytes != null && <span className="ml-auto text-xs tabular-nums text-gray-500">{t("데이터 {size}", { size: fmtBytes(pf.data.data_size_bytes) })}</span>}
                </div>
                <ul className="divide-y divide-gray-200">
                  {pf.data.checks.map((c) => (
                    <li key={c.key} className="flex gap-3 px-4 py-2.5 text-sm">
                      <CheckIcon c={c} />
                      <span className="min-w-0">
                        <span className="text-foreground">{c.label}</span>
                        {(!c.passed || c.severity === "info") && <span className="mt-0.5 block text-xs text-gray-500">{c.message}</span>}
                      </span>
                    </li>
                  ))}
                </ul>
              </div>
            )}
            <p className="text-xs text-gray-500">
              {t("이미지 태그를 직접 바꿔 메이저를 올리면 데이터 디렉터리가 옮겨지지 않아 데이터베이스가 시작되지 않을 수 있습니다. 이 업그레이드를 쓰세요.")}
            </p>
          </div>
        ))}
      {error ? <ErrorBox error={error} className="mt-3" /> : null}
    </Section>
  );
}
