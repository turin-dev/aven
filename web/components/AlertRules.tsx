"use client";

import { useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { BellRing, Pencil, Plus, Trash2 } from "lucide-react";
import { ALERT_METRICS, api, type AlertMetric, type AlertRule, type AlertRuleInput, type Environment, type ID, type Service } from "@/lib/api";
import { qk, useAlertRules, useCaps } from "@/lib/queries";
import { t, tr } from "@/lib/i18n";
import { fmtRelative, fmtTime } from "@/lib/format";
import { Button, Dialog, EmptyState, ErrorBox, Field, IconButton, Input, Select, Spinner, Toggle, cx, errorMessage, useConfirm } from "./ui";
import { toast } from "@/lib/uiState";

const STATE_LABEL: Record<AlertRule["last_state"], string> = { ok: t("정상"), pending: t("대기"), firing: t("발생") };

const BADGE = {
  danger: "text-red-500 bg-red-100 dark:text-red-700 dark:bg-red-200",
  warning: "text-yellow-500 bg-yellow-100 dark:text-yellow-700 dark:bg-yellow-200",
  success: "text-green-500 bg-green-100 dark:text-green-700 dark:bg-green-200",
  neutral: "bg-gray-200 text-gray-500",
} as const;

function Badge({ tone, children }: { tone: keyof typeof BADGE; children: ReactNode }) {
  return <span className={cx("inline-flex items-center whitespace-nowrap rounded-md px-1.5 py-0.5 text-xs font-medium", BADGE[tone])}>{children}</span>;
}

function metricLabel(m: AlertMetric) {
  return ALERT_METRICS.find((x) => x.value === m)?.label ?? m;
}

function fmtValue(m: AlertMetric, v: number | null): string {
  if (v === null) return "—";
  if (m === "memory_bytes" || m === "disk_bytes") {
    const units = ["B", "KB", "MB", "GB", "TB"];
    let x = v;
    let i = 0;
    while (x >= 1024 && i < units.length - 1) {
      x /= 1024;
      i++;
    }
    return `${x.toFixed(x >= 10 || i === 0 ? 0 : 1)} ${units[i]}`;
  }
  if (m.endsWith("_percent") || m === "http_5xx_rate") return `${v.toFixed(1)}%`;
  if (m === "http_p95_ms") return `${Math.round(v)} ms`;
  return String(Math.round(v * 100) / 100);
}

type Draft = { name: string; metric: AlertMetric; comparator: ">" | "<"; threshold: string; for_seconds: string; environment_id: string; service_id: string; enabled: boolean };

function toDraft(r?: AlertRule): Draft {
  return {
    name: r?.name ?? "",
    metric: r?.metric ?? "cpu_percent",
    comparator: r?.comparator ?? ">",
    threshold: r ? String(r.threshold) : "80",
    for_seconds: r ? String(r.for_seconds) : "120",
    environment_id: r?.environment_id ?? "",
    service_id: r?.service_id ?? "",
    enabled: r?.enabled ?? true,
  };
}

function fmtDuration(sec: number) {
  if (sec <= 0) return t("즉시");
  if (sec % 3600 === 0) return t("{n}시간", { n: sec / 3600 });
  if (sec % 60 === 0) return t("{n}분", { n: sec / 60 });
  return t("{n}초", { n: sec });
}

/** Plain-language summary of a rule draft, shown live in the dialog. */
function RulePreview({ d, envs, services }: { d: Draft; envs: Environment[]; services: Service[] }) {
  const threshold = Number(d.threshold);
  const forS = Number(d.for_seconds);
  if (d.threshold.trim() === "" || !Number.isFinite(threshold)) return null;
  const svc = d.service_id ? (services.find((s) => s.id === d.service_id)?.name ?? d.service_id) : t("모든 서비스");
  const env = d.environment_id ? (envs.find((e) => e.id === d.environment_id)?.name ?? d.environment_id) : t("모든 환경");
  return (
    <div className="rounded-md border border-gray-200 bg-secondaryBg px-3 py-2.5 text-sm" role="status" aria-live="polite">
      <div className="mb-1 text-xs font-semibold text-gray-500">{t("미리보기")}</div>
      <p className="text-gray-700">
        {(() => {
          const parts = {
          target: <b className="text-foreground">{`${svc} · ${env}`}</b>,
          metric: <b className="text-foreground">{metricLabel(d.metric)}</b>,
          cmp: <span className="font-mono">{d.comparator}</span>,
          value: <b className="tabular-nums text-foreground">{fmtValue(d.metric, threshold)}</b>,
          dur: <b className="text-foreground">{Number.isFinite(forS) ? fmtDuration(forS) : "?"}</b>,
          firing: <code className="font-mono text-xs">alert.firing</code>,
          resolved: <code className="font-mono text-xs">alert.resolved</code>,
        };
          return forS > 0
            ? tr("{target}에서 {metric}이(가) {cmp} {value} 상태로 {dur} 지속되면 {firing}을 보내고, 돌아오면 {resolved}를 보냅니다.", parts)
            : tr("{target}에서 {metric}이(가) {cmp} {value}이(가) 되는 즉시 {firing}을 보내고, 돌아오면 {resolved}를 보냅니다.", parts);
        })()}
      </p>
      {!d.enabled && <p className="mt-1 text-xs text-warn">{t("꺼 둔 규칙은 평가하지 않습니다.")}</p>}
    </div>
  );
}

const isPercent = (m: AlertMetric) => m.endsWith("_percent") || m === "http_5xx_rate";

function RuleDialog({
  pid,
  rule,
  envs,
  services,
  onClose,
}: {
  pid: ID;
  rule: AlertRule | null | undefined;
  envs: Environment[];
  services: Service[];
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [d, setD] = useState<Draft>(() => toDraft(rule ?? undefined));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const set = (p: Partial<Draft>) => setD((x) => ({ ...x, ...p }));
  const threshold = Number(d.threshold);
  const forS = Number(d.for_seconds);
  const thresholdBad = d.threshold.trim() === "" || !Number.isFinite(threshold) || threshold < 0;
  const forBad = d.for_seconds.trim() === "" || !Number.isInteger(forS) || forS < 0 || forS > 86400;
  const valid = !thresholdBad && !forBad;
  const pctOver = isPercent(d.metric) && threshold > 100;
  const save = async () => {
    setBusy(true);
    setError(null);
    const input: AlertRuleInput = {
      name: d.name.trim(),
      metric: d.metric,
      comparator: d.comparator,
      threshold,
      for_seconds: forS,
      environment_id: d.environment_id || null,
      service_id: d.service_id || null,
      enabled: d.enabled,
    };
    try {
      if (rule) await api.updateAlertRule(pid, rule.id, input);
      else await api.createAlertRule(pid, input);
      await qc.invalidateQueries({ queryKey: qk.alertRules(pid) });
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open
      onClose={() => !busy && onClose()}
      title={rule ? t("알림 규칙 편집") : t("알림 규칙 추가")}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={busy}>
            {t("취소")}
          </Button>
          <Button variant="primary" loading={busy} disabled={!valid} onClick={save}>
            {t("저장")}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Field label={t("이름")} hint={t("비우면 조건을 이름으로 씁니다.")}>
          <Input value={d.name} onChange={(e) => set({ name: e.target.value })} placeholder={t("CPU 높음")} maxLength={100} />
        </Field>
        <Field label={t("지표")}>
          <Select value={d.metric} onChange={(e) => set({ metric: e.target.value as AlertMetric })}>
            {ALERT_METRICS.map((m) => (
              <option key={m.value} value={m.value}>
                {m.label}
              </option>
            ))}
          </Select>
        </Field>
        <div className="grid grid-cols-[90px_1fr] gap-3">
          <Field label={t("조건")}>
            <Select value={d.comparator} onChange={(e) => set({ comparator: e.target.value as ">" | "<" })} aria-label={t("비교")}>
              <option value=">">&gt;</option>
              <option value="<">&lt;</option>
            </Select>
          </Field>
          <Field
            label={t("기준 값")}
            hint={
              pctOver ? (
                <span className="text-warn">{t("100%를 넘는 기준은 발생하지 않습니다.")}</span>
              ) : d.metric.endsWith("_bytes") ? (
                !thresholdBad && threshold > 0 ? `= ${fmtValue(d.metric, threshold)}` : t("bytes (예: 536870912 = 512 MB)")
              ) : undefined
            }
          >
            <Input type="number" min={0} step="any" value={d.threshold} invalid={thresholdBad} onChange={(e) => set({ threshold: e.target.value })} />
          </Field>
        </div>
        <Field label={t("지속 시간(초)")} hint={t("이 시간 동안 계속 기준을 넘으면 알림을 냅니다. 0이면 바로. 30초마다 평가합니다.")}>
          <Input type="number" min={0} max={86400} step={30} value={d.for_seconds} invalid={forBad} onChange={(e) => set({ for_seconds: e.target.value })} />
        </Field>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={t("환경")}>
            <Select value={d.environment_id} onChange={(e) => set({ environment_id: e.target.value })}>
              <option value="">{t("모든 환경")}</option>
              {envs.map((e) => (
                <option key={e.id} value={e.id}>
                  {e.name}
                </option>
              ))}
            </Select>
          </Field>
          <Field label={t("서비스")}>
            <Select value={d.service_id} onChange={(e) => set({ service_id: e.target.value })}>
              <option value="">{t("모든 서비스")}</option>
              {services.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                </option>
              ))}
            </Select>
          </Field>
        </div>
        <div className="flex items-center gap-3">
          <Toggle checked={d.enabled} onChange={(v) => set({ enabled: v })} label={t("사용")} />
          <span className="text-sm text-gray-600">{t("사용")}</span>
        </div>
        <RulePreview d={d} envs={envs} services={services} />
        <ErrorBox error={error} />
      </div>
    </Dialog>
  );
}

/** Metric alert rules of a project (v1.5). Firing/resolved events go to notification channels and inboxes. */
export function AlertRulesSection({ pid, envs, services }: { pid: ID; envs: Environment[]; services: Service[] }) {
  const qc = useQueryClient();
  const caps = useCaps(pid);
  const rules = useAlertRules(pid);
  const [editing, setEditing] = useState<AlertRule | null | undefined>(undefined);
  const { confirm, node } = useConfirm();
  const envName = (id: ID | null) => (id ? (envs.find((e) => e.id === id)?.name ?? t("삭제된 환경")) : t("모든 환경"));
  const svcName = (id: ID | null) => (id ? (services.find((s) => s.id === id)?.name ?? t("삭제된 서비스")) : t("모든 서비스"));
  const list = rules.data ?? [];
  const firing = list.filter((r) => r.last_state === "firing").length;
  return (
    <section className="flex min-w-0 flex-col rounded-lg border border-gray-200 bg-secondaryBg lg:col-span-2" aria-labelledby="alert-rules-title">
      {node}
      {editing !== undefined && <RuleDialog pid={pid} rule={editing} envs={envs} services={services} onClose={() => setEditing(undefined)} />}
      <header className="flex h-11 shrink-0 items-center gap-2 px-4">
        <h2 id="alert-rules-title" className="text-sm font-semibold">
          {t("알림 규칙")}
        </h2>
        {firing > 0 && <Badge tone="danger">{t("{count}개 발생 중", { count: firing })}</Badge>}
        <span className="ml-auto flex items-center gap-2">
          {caps.canAdmin && (
            <Button size="sm" variant="outline" onClick={() => setEditing(null)}>
              <Plus /> {t("규칙 추가")}
            </Button>
          )}
        </span>
      </header>
      <div className="px-4 pb-4">
        <ErrorBox error={rules.error} className="mb-3" />
        {rules.isPending ? (
          <Spinner />
        ) : list.length === 0 ? (
          <EmptyState icon={<BellRing className="size-5" />} title={t("알림 규칙이 없습니다")}>
            {tr("지표가 기준을 넘으면 {firing}, 돌아오면 {resolved} 이벤트를 냅니다. 이벤트는 알림 채널과 알림함으로 갑니다.", {
              firing: <code className="font-mono text-xs">alert.firing</code>,
              resolved: <code className="font-mono text-xs">alert.resolved</code>,
            })}
          </EmptyState>
        ) : (
          <div className="overflow-x-auto rounded-md border border-gray-200">
            <table className="w-full min-w-[640px] text-sm">
              <thead>
                <tr className="bg-gray-50 text-left text-xs text-gray-600">
                  <th className="px-3 py-2 font-semibold">{t("상태")}</th>
                  <th className="px-3 py-2 font-semibold">{t("규칙")}</th>
                  <th className="px-3 py-2 font-semibold">{t("대상")}</th>
                  <th className="px-3 py-2 font-semibold">{t("현재 값")}</th>
                  <th className="px-3 py-2 font-semibold">{t("마지막 발생")}</th>
                  <th className="px-3 py-2" />
                </tr>
              </thead>
              <tbody>
                {list.map((r) => (
                  <tr key={r.id} className={cx("border-t border-gray-200 hover:bg-gray-100", !r.enabled && "opacity-55")}>
                    <td className="px-3 py-2">
                      <Badge tone={!r.enabled ? "neutral" : r.last_state === "firing" ? "danger" : r.last_state === "pending" ? "warning" : "success"}>
                        {r.enabled ? STATE_LABEL[r.last_state] : t("꺼짐")}
                      </Badge>
                    </td>
                    <td className="px-3 py-2">
                      <div className="font-medium">{r.name || metricLabel(r.metric)}</div>
                      <div className="font-mono text-xs tabular-nums text-gray-500">
                        {r.metric} {r.comparator} {fmtValue(r.metric, r.threshold)}
                        {r.for_seconds > 0 && ` · ${fmtDuration(r.for_seconds)}`}
                      </div>
                    </td>
                    <td className="px-3 py-2 text-gray-600">
                      {svcName(r.service_id)} · {envName(r.environment_id)}
                    </td>
                    <td className="px-3 py-2 tabular-nums" title={r.last_evaluated_at ? t("평가: {time}", { time: fmtTime(r.last_evaluated_at) }) : t("아직 데이터가 없습니다")}>
                      {fmtValue(r.metric, r.last_value)}
                    </td>
                    <td className="px-3 py-2 tabular-nums text-gray-500">{r.last_fired_at ? fmtRelative(r.last_fired_at) : "—"}</td>
                    <td className="px-2 py-2 text-right">
                      {caps.canAdmin && (
                        <span className="inline-flex">
                          <IconButton label={t("편집")} size="sm" onClick={() => setEditing(r)}>
                            <Pencil className="size-3.5" />
                          </IconButton>
                          <IconButton
                            label={t("삭제")}
                            size="sm"
                            onClick={() =>
                              confirm({
                                title: t("알림 규칙 삭제"),
                                message: t("{name} 규칙을 삭제합니다.", { name: r.name || `${r.metric} ${r.comparator} ${r.threshold}` }),
                                confirmLabel: t("삭제"),
                                danger: true,
                                run: async () => {
                                  try {
                                    await api.deleteAlertRule(pid, r.id);
                                  } catch (e) {
                                    toast(errorMessage(e), "error");
                                  } finally {
                                    await qc.invalidateQueries({ queryKey: qk.alertRules(pid) });
                                  }
                                },
                              })
                            }
                          >
                            <Trash2 className="size-3.5" />
                          </IconButton>
                        </span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </section>
  );
}
