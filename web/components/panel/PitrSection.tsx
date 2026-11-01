"use client";


import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive, RotateCcw, Trash2 } from "lucide-react";
import { api, type BackupDestination, type CanvasService, type Environment, type PitrStatus } from "@/lib/api";
import { useCaps } from "@/lib/queries";
import { fmtBytes, fmtRelative } from "@/lib/format";
import { dateLocale, t, tr } from "@/lib/i18n";
import { lock } from "../Perm";
import { Button, Card, ErrorBox, Field, Input, LABEL_CLS, Notice, Segmented, Select, cx, useConfirm } from "../ui";
import { useTypedConfirm } from "./dataKit";

const SERVICE_NAME = /^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$/;

const DESTS = ["db-destinations"] as const;

/** Date → datetime-local 입력값(로컬 시각, 초 단위). */
function toLocalInput(d: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

function fmtAbs(iso: string | null | undefined): string {
  if (!iso) return "-";
  return new Date(iso).toLocaleString(dateLocale(), { dateStyle: "medium", timeStyle: "medium" });
}

function defaultForkName(src: string, d: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  const suffix = `-restored-${d.getUTCFullYear()}${p(d.getUTCMonth() + 1)}${p(d.getUTCDate())}-${p(d.getUTCHours())}${p(d.getUTCMinutes())}`;
  return src.slice(0, 40 - suffix.length).replace(/-+$/, "") + suffix;
}

export function PitrSection({ svc, env }: { svc: CanvasService; env: Environment }) {
  const qc = useQueryClient();
  const { confirm, node } = useConfirm();
  const typed = useTypedConfirm();
  const canEdit = useCaps(svc.project_id).canEdit;
  const key = ["pitr", svc.id, env.id];
  const q = useQuery({
    queryKey: key,
    queryFn: () => api.pitr(svc.id, env.id),
    refetchInterval: (qq) => (qq.state.data?.running.length || qq.state.data?.restore?.status === "running" ? 3000 : 30000),
  });
  const dests = useQuery({ queryKey: DESTS, queryFn: api.backupDestinations, retry: false });
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [keep, setKeep] = useState<string | null>(null);
  const [dest, setDest] = useState<string | null>(null);
  const [at, setAt] = useState<string>("");
  const [mode, setMode] = useState<"new" | "inplace">("new");
  const [forkName, setForkName] = useState("");

  const run = async (k: string, fn: () => Promise<unknown>) => {
    setBusy(k);
    setError(null);
    setNotice(null);
    try {
      await fn();
      await qc.invalidateQueries({ queryKey: key });
      return true;
    } catch (e) {
      setError(e);
      return false;
    } finally {
      setBusy(null);
    }
  };

  if (q.isPending)
    return (
      <div className="mb-8 border-t border-gray-200 pt-8">
        <div className="h-6 w-32 animate-pulse rounded bg-gray-100" />
        <div className="mt-2 h-4 w-2/3 animate-pulse rounded bg-gray-100" />
        <div className="mt-4 h-[148px] animate-pulse rounded-lg bg-gray-100" />
      </div>
    );
  if (q.error) return <ErrorBox className="mb-8" error={q.error} />;
  const st: PitrStatus = q.data;
  if (!st.supported) return null;
  const c = st.config;
  const keepText = keep ?? String(c.retention_days || 7);
  const destId = dest ?? c.destination_id ?? "";
  const engine = st.kind === "mysql" ? "MySQL" : "PostgreSQL";
  const logName = st.kind === "mysql" ? "binlog" : "WAL";
  const w = st.window;
  const hasArchive = w.bases + w.logs > 0;
  const restore = st.restore;
  const restoring = restore?.status === "running";
  const latest = w.latest ? new Date(w.latest) : null;
  const earliest = w.earliest ? new Date(w.earliest) : null;
  const atDate = at ? new Date(at) : latest;
  const atValid = !!atDate && !!earliest && !!latest && !isNaN(atDate.getTime()) && atDate >= earliest && atDate <= new Date();
  const forkDefault = atDate && !isNaN(atDate.getTime()) ? defaultForkName(svc.name, atDate) : `${svc.name}-restored`;
  const forkFinal = forkName.trim() || forkDefault;
  const forkValid = mode !== "new" || (SERVICE_NAME.test(forkFinal) && forkFinal !== "shared");

  const settings = (
    <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_140px]">
      <Field label={t("보관 위치")}>
        <Select value={destId} onChange={(e) => setDest(e.target.value)} aria-label={t("보관 위치")} className="w-full">
          <option value="">{t("로컬 (AVEN_DATA)")}</option>
          {(dests.data ?? [])
            .filter((d: BackupDestination) => d.type === "s3")
            .map((d) => (
              <option key={d.id} value={d.id}>
                S3 · {d.name}
              </option>
            ))}
        </Select>
      </Field>
      <Field label={t("보관 기간 (일)")}>
        <Input mono inputMode="numeric" value={keepText} onChange={(e) => setKeep(e.target.value.replace(/[^0-9]/g, ""))} aria-label={t("보관 기간(일)")} />
      </Field>
    </div>
  );
  const settingsDirty = keepText !== String(c.retention_days || 7) || destId !== (c.destination_id ?? "");

  const enable = () =>
    confirm({
      title: t("시점 복구 켜기"),
      message: (
        <>
          {tr("{logName}를 1분마다 백업 저장소로 보관하고 하루에 한 번 기준 백업을 만듭니다. 켠 뒤 첫 기준 백업이 끝난 시점부터 복원할 수 있습니다.", { logName })}
          {st.kind === "postgres"
            ? t(" PostgreSQL에 archive_mode를 켜려고 데이터베이스를 한 번 재시작할 수 있습니다.")
            : t(" MySQL 바이너리 로그를 보관한 뒤 데이터베이스 볼륨에서 지웁니다(PURGE BINARY LOGS).")}
        </>
      ),
      confirmLabel: t("켜기"),
      run: () =>
        run("enable", async () => {
          const res = await api.setPitr(svc.id, env.id, { enabled: true, retention_days: Number(keepText) || 7, destination_id: destId });
          setNotice(res.warning ? t("켰습니다. 경고: {warning}", { warning: res.warning }) : t("시점 복구를 켰습니다. 1분 안에 첫 기준 백업을 시작합니다."));
        }),
    });

  const doRestore = () => {
    if (!atDate || !atValid || !forkValid) return;
    const iso = atDate.toISOString();
    const name = forkFinal;
    const start = async () => {
      await api.pitrRestore(svc.id, env.id, mode === "inplace" ? { target_time: iso, in_place: true } : { target_time: iso, new_service_name: name });
      setNotice(t("복원을 시작했습니다. 진행 상황은 아래와 이벤트에서 볼 수 있습니다."));
      await qc.invalidateQueries({ queryKey: key });
    };
    if (mode === "inplace") {
      // Overwriting live data: the service name has to be typed. Errors stay in the dialog.
      typed.confirm({
        title: t("이 서비스의 데이터를 덮어쓸까요?"),
        message: (
          <>
            <p>
              {tr("{name}의 현재 데이터를 {time} 시점의 데이터로 {overwrite}.", {
                name: <span className="font-mono text-foreground">{svc.name}</span>,
                time: <b>{fmtAbs(iso)}</b>,
                overwrite: <b>{t("덮어씁니다")}</b>,
              })}
              {st.kind === "postgres" ? t(" 복원하는 동안 데이터베이스가 멈춥니다.") : t(" 복원 대상 데이터베이스의 사용자 DB를 지운 뒤 다시 만듭니다.")}
            </p>
            <p className="text-sm">
              <b>{t("그 시각 이후에 쓴 데이터는 사라집니다.")}</b> {t("확실하지 않으면 새 서비스로 복원해 먼저 확인하세요.")}
            </p>
          </>
        ),
        confirmLabel: t("덮어쓰고 복원"),
        expected: svc.name,
        run: async () => {
          setBusy("restore");
          setError(null);
          setNotice(null);
          try {
            await start();
          } finally {
            setBusy(null);
          }
        },
      });
      return;
    }
    confirm({
      title: t("이 시점으로 복원"),
      message: tr("새 서비스 {name}를 만들고 {time} 시점의 데이터로 채웁니다. 원본 {source}은 그대로입니다.", {
        name: <span className="font-mono text-foreground">{name}</span>,
        time: <b>{fmtAbs(iso)}</b>,
        source: <span className="font-mono text-foreground">{svc.name}</span>,
      }),
      confirmLabel: t("복원"),
      run: () => run("restore", start),
    });
  };

  const statusBadge = (
    <span className="flex flex-wrap items-center gap-2">
      {t("시점 복구")}
      {c.enabled && <span className="rounded bg-success-surface px-1.5 py-0.5 text-xs font-medium text-success">{t("켜짐")}</span>}
      {st.running.length > 0 && (
        <span className="flex items-center gap-1.5 text-xs font-normal text-gray-500">
          <span className="size-2 animate-soft-pulse rounded-full bg-yellow-600" />
          {st.running.map((r) => ({ base: t("기준 백업 중"), sync: t("{logName} 보관 중", { logName }), restore: t("복원 중") })[r] ?? r).join(" · ")}
        </span>
      )}
    </span>
  );

  const stat = (label: string, value: string, sub?: string) => (
    <div className="min-w-0 px-4 py-3">
      <div className="text-xs font-semibold text-gray-600">{label}</div>
      <div className="mt-1 truncate text-sm tabular-nums text-foreground" title={value}>
        {value}
        {sub && <span className="ml-1.5 text-xs text-gray-500">{sub}</span>}
      </div>
    </div>
  );

  return (
    <Card
      className="mb-8 !max-w-none border-t border-gray-200 pt-8"
      title={statusBadge}
      description={t("{engine} {logName}를 계속 보관해 두면 보관 기간 안의 아무 시각으로나 복원할 수 있습니다. 실수로 지운 테이블이나 잘못된 마이그레이션처럼 정기 백업 사이에 생긴 문제에 씁니다.", { engine, logName })}
      action={
        c.enabled && (
          <>
            <Button variant="outline" loading={busy === "base"} disabled={st.running.includes("base")} onClick={() => run("base", () => api.pitrBase(svc.id, env.id))} {...lock(canEdit)}>
              <Archive /> {t("기준 백업 지금")}
            </Button>
            <Button
              variant="ghost"
              loading={busy === "disable"}
              onClick={() =>
                confirm({
                  title: t("시점 복구 끄기"),
                  message: t("{logName} 보관을 멈춥니다. 이미 보관한 파일은 지우기 전까지 남아 있고, 그 범위로는 계속 복원할 수 있습니다.", { logName }),
                  confirmLabel: t("끄기"),
                  danger: true,
                  run: () => run("disable", () => api.setPitr(svc.id, env.id, { enabled: false })),
                })
              }
              {...lock(canEdit)}
            >
              {t("끄기")}
            </Button>
          </>
        )
      }
    >
      <div className="flex flex-col gap-y-4">
        <div className="overflow-hidden rounded-lg border border-gray-200 bg-secondaryBg">
          {c.enabled ? (
            <div className="grid divide-y divide-gray-200 border-b border-gray-200 sm:grid-cols-3 sm:divide-x sm:divide-y-0">
              {stat(t("마지막 기준 백업"), c.last_base_at ? fmtRelative(c.last_base_at) : t("아직 없음"), c.last_base_at ? t("보관 {days}일", { days: c.retention_days }) : undefined)}
              {stat(t("마지막 {logName} 보관", { logName }), c.last_sync_at ? fmtRelative(c.last_sync_at) : t("아직 없음"))}
              {stat(t("보관소"), fmtBytes(w.bytes), t("기준 {bases} · {logName} {logs}", { bases: w.bases, logName, logs: w.logs.toLocaleString() }))}
            </div>
          ) : (
            <div className="border-b border-gray-200 px-4 py-3">
              <p className="text-sm font-semibold">{t("시점 복구가 꺼져 있습니다")}</p>
              <p className="mt-0.5 text-sm text-gray-600">
                {t("켜면 {logName}를 1분마다 보관하고 하루에 한 번 기준 백업을 만듭니다.", { logName })}
              </p>
            </div>
          )}
          {c.last_error && (
            <p role="alert" className="border-b border-gray-200 px-4 py-2.5 text-sm text-danger">
              {c.last_error}
            </p>
          )}
          <div className="px-4 py-4">{settings}</div>
          <div className="flex items-center justify-end gap-2 border-t border-gray-200 px-4 py-3">
            {c.enabled ? (
              <Button
                variant={settingsDirty ? "primary" : "outline"}
                loading={busy === "save"}
                disabled={!settingsDirty}
                onClick={() => run("save", () => api.setPitr(svc.id, env.id, { retention_days: Number(keepText) || 7, destination_id: destId }))}
                {...lock(canEdit)}
              >
                {t("설정 저장")}
              </Button>
            ) : (
              <Button variant="primary" loading={busy === "enable"} onClick={enable} {...lock(canEdit)}>
                {t("시점 복구 켜기")}
              </Button>
            )}
          </div>
        </div>

        {(c.enabled || hasArchive) && (
          <div className="overflow-hidden rounded-lg border border-gray-200 bg-secondaryBg">
            <div className="border-b border-gray-200 px-4 py-3">
              <p className="text-sm font-semibold">{t("특정 시점으로 복원")}</p>
              <p className="mt-0.5 text-sm text-gray-600">
                {earliest && latest ? (
                  <>
                    {tr("복원 가능 범위 {earliest} ~ {latest}", {
                      earliest: <span className="tabular-nums text-foreground">{fmtAbs(w.earliest)}</span>,
                      latest: <span className="tabular-nums text-foreground">{fmtAbs(w.latest)}</span>,
                    })}
                    {c.enabled && t(" · 최근 시각은 복원할 때 밀린 로그를 먼저 보관합니다")}
                  </>
                ) : (
                  t("첫 기준 백업과 {logName} 보관이 끝나면 복원할 수 있는 범위가 여기에 나옵니다.", { logName })
                )}
              </p>
            </div>
            {earliest && latest && (
              <div className="grid gap-4 px-4 py-4 sm:grid-cols-2">
                <Field label={t("복원할 시각")} hint={t("브라우저 시간대({timeZone})", { timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone })}>
                  <Input
                    type="datetime-local"
                    step={1}
                    value={at || toLocalInput(latest)}
                    min={toLocalInput(earliest)}
                    max={toLocalInput(new Date())}
                    onChange={(e) => setAt(e.target.value)}
                    aria-label={t("복원할 시각")}
                    invalid={!atValid}
                    className="tabular-nums"
                  />
                </Field>
                <Timeline
                  className="sm:col-span-2"
                  earliest={earliest}
                  latest={latest}
                  value={atDate && !isNaN(atDate.getTime()) ? atDate : latest}
                  lastBase={c.last_base_at ? new Date(c.last_base_at) : null}
                  onChange={(d) => setAt(toLocalInput(d))}
                />
                <div>
                  <span className={LABEL_CLS}>{t("복원 대상")}</span>
                  <div className="flex h-[42px] items-center">
                  <Segmented
                    label={t("복원 대상")}
                    value={mode}
                    onChange={setMode}
                    options={[
                      { value: "new", label: t("새 서비스") },
                      { value: "inplace", label: t("이 서비스에 덮어쓰기") },
                    ]}
                  />
                  </div>
                </div>
                {mode === "new" && (
                  <Field
                    label={t("새 서비스 이름")}
                    className="sm:col-span-2"
                    hint={forkValid ? undefined : <span className="text-danger">{t("소문자, 숫자, -만 쓸 수 있고 40자까지입니다.")}</span>}
                  >
                    <Input mono value={forkName} onChange={(e) => setForkName(e.target.value.toLowerCase())} placeholder={forkDefault} aria-label={t("새 서비스 이름")} invalid={!forkValid} />
                  </Field>
                )}
              </div>
            )}
            {restore && (
              <div className={cx("border-t border-gray-200 px-4 py-2.5 text-sm tabular-nums", restore.status === "failed" ? "text-danger" : "text-gray-600")}>
                {restore.status === "running" && <span className="mr-2 inline-block size-2 animate-soft-pulse rounded-full bg-yellow-600 align-middle" />}
                {tr("마지막 복원: {time} 시점 → {target} · {status}", {
                  time: fmtAbs(restore.target_time),
                  target: restore.in_place ? svc.name : t("새 서비스"),
                  status: restore.status === "running" ? `${restore.step ?? t("진행 중")}…` : restore.status === "done" ? t("완료") : t("실패: {error}", { error: restore.error ?? "" }),
                })}
              </div>
            )}
            <div className="flex flex-wrap items-center gap-2 border-t border-gray-200 px-4 py-3">
              <p className="min-w-0 flex-1 text-xs text-gray-500">
                {st.kind === "mysql" ? t("binlog 이벤트는 초 단위라 고른 초가 시작되기 직전 상태로 복원합니다.") : t("고른 시각까지 커밋된 트랜잭션을 포함합니다.")}
              </p>
              {!c.enabled && hasArchive && (
                <Button
                  variant="ghost"
                  loading={busy === "purge"}
                  onClick={() =>
                    confirm({
                      title: t("보관소 삭제"),
                      message: t("보관한 기준 백업 {bases}개와 {logName} {logs}개({size})를 저장소에서 지웁니다. 되돌릴 수 없습니다.", { bases: w.bases, logName, logs: w.logs, size: fmtBytes(w.bytes) }),
                      confirmLabel: t("삭제"),
                      danger: true,
                      run: () => run("purge", () => api.deletePitrArchive(svc.id, env.id)),
                    })
                  }
                  {...lock(canEdit)}
                >
                  <Trash2 /> {t("보관소 삭제")}
                </Button>
              )}
              {earliest && latest && (
                <Button
                  variant={mode === "inplace" ? "danger" : "primary"}
                  disabled={!atValid || restoring || !forkValid}
                  loading={busy === "restore"}
                  onClick={doRestore}
                  {...lock(canEdit)}
                >
                  <RotateCcw /> {t("이 시점으로 복원")}
                </Button>
              )}
            </div>
          </div>
        )}
        {notice && <Notice onDismiss={() => setNotice(null)}>{notice}</Notice>}
        {error ? <ErrorBox error={error} /> : null}
        {node}
        {typed.node}
      </div>
    </Card>
  );
}

/**
 * Restore window as a bar: archived range (earliest → latest) and the part not yet archived (latest → now, archived
 * on restore). A range slider and presets pick the time; the datetime input above stays the precise control.
 */
function Timeline({
  earliest,
  latest,
  value,
  lastBase,
  onChange,
  className,
}: {
  earliest: Date;
  latest: Date;
  value: Date;
  lastBase: Date | null;
  onChange: (d: Date) => void;
  className?: string;
}) {
  const min = earliest.getTime();
  const now = Math.max(Date.now(), latest.getTime());
  const span = Math.max(1000, now - min);
  const pct = (ms: number) => `${Math.max(0, Math.min(100, ((ms - min) / span) * 100))}%`;
  const v = Math.max(min, Math.min(now, value.getTime()));
  const clampSet = (ms: number) => onChange(new Date(Math.max(min, Math.min(now, Math.floor(ms / 1000) * 1000))));
  const presets: [string, number][] = [
    [t("5분 전"), 5 * 60_000],
    [t("1시간 전"), 3_600_000],
    [t("6시간 전"), 6 * 3_600_000],
    [t("1일 전"), 86_400_000],
  ];
  const rel = (ms: number) => {
    const d = Math.round((Date.now() - ms) / 60_000);
    if (d < 1) return t("지금");
    if (d < 60) return t("{n}분 전", { n: d });
    if (d < 60 * 48) return t("{n}시간 전", { n: Math.round(d / 60) });
    return t("{n}일 전", { n: Math.round(d / 1440) });
  };
  return (
    <div className={cx("space-y-2", className)}>
      <div className="flex flex-wrap items-baseline justify-between gap-2 text-xs text-gray-500">
        <span>
          {tr("선택: {abs} · {rel} · {utc}", {
            abs: <b className="tabular-nums text-foreground">{fmtAbs(new Date(v).toISOString())}</b>,
            rel: rel(v),
            utc: <span className="font-mono tabular-nums">{new Date(v).toISOString().replace("T", " ").slice(0, 19)} UTC</span>,
          })}
        </span>
        <span className="flex flex-wrap gap-1">
          {presets.map(([label, back]) => {
            const ms = Date.now() - back;
            return (
              <button
                key={label}
                type="button"
                disabled={ms < min}
                onClick={() => clampSet(ms)}
                className="cursor-pointer rounded border border-gray-200 px-1.5 py-0.5 hover:bg-gray-100 hover:text-foreground disabled:cursor-not-allowed disabled:opacity-40"
              >
                {label}
              </button>
            );
          })}
          <button type="button" onClick={() => clampSet(latest.getTime())} className="cursor-pointer rounded border border-gray-200 px-1.5 py-0.5 hover:bg-gray-100 hover:text-foreground">
            {t("마지막 보관 시각")}
          </button>
        </span>
      </div>
      <div className="relative h-8">
        <div className="absolute inset-x-0 top-1/2 h-2 -translate-y-1/2 overflow-hidden rounded-full bg-gray-100" aria-hidden>
          <div className="absolute inset-y-0 left-0 bg-pink-500/60" style={{ width: pct(latest.getTime()) }} />
          <div
            className="absolute inset-y-0 right-0 bg-[repeating-linear-gradient(135deg,var(--color-pink-300)_0_4px,transparent_4px_8px)] opacity-60"
            style={{ left: pct(latest.getTime()) }}
          />
        </div>
        {lastBase && lastBase.getTime() >= min && (
          <span className="absolute top-1/2 h-4 w-0.5 -translate-y-1/2 bg-foreground/50" style={{ left: pct(lastBase.getTime()) }} title={t("마지막 기준 백업 {time}", { time: fmtAbs(lastBase.toISOString()) })} aria-hidden />
        )}
        <input
          type="range"
          min={min}
          max={now}
          step={1000}
          value={v}
          onChange={(e) => clampSet(Number(e.target.value))}
          aria-label={t("복원할 시각")}
          aria-valuetext={fmtAbs(new Date(v).toISOString())}
          className="absolute inset-0 h-full w-full cursor-pointer appearance-none bg-transparent focus:outline-none [&::-moz-range-thumb]:size-4 [&::-moz-range-thumb]:rounded-full [&::-moz-range-thumb]:border-2 [&::-moz-range-thumb]:border-white [&::-moz-range-thumb]:bg-pink-500 [&::-moz-range-track]:bg-transparent [&::-webkit-slider-runnable-track]:bg-transparent [&::-webkit-slider-thumb]:size-4 [&::-webkit-slider-thumb]:appearance-none [&::-webkit-slider-thumb]:rounded-full [&::-webkit-slider-thumb]:border-2 [&::-webkit-slider-thumb]:border-white [&::-webkit-slider-thumb]:bg-pink-500 [&::-webkit-slider-thumb]:shadow focus-visible:[&::-webkit-slider-thumb]:ring-4 focus-visible:[&::-webkit-slider-thumb]:ring-pink-500/40"
        />
      </div>
      <div className="flex justify-between text-[11px] tabular-nums text-gray-500">
        <span>{fmtAbs(earliest.toISOString())}</span>
        <span className="flex items-center gap-3">
          <span className="flex items-center gap-1">
            <span className="inline-block size-2 rounded-sm bg-pink-500/60" aria-hidden /> {t("보관됨")}
          </span>
          <span className="flex items-center gap-1">
            <span className="inline-block size-2 rounded-sm bg-[repeating-linear-gradient(135deg,var(--color-pink-300)_0_2px,transparent_2px_4px)]" aria-hidden /> {t("복원할 때 보관")}
          </span>
          <span>{t("지금")}</span>
        </span>
      </div>
    </div>
  );
}
