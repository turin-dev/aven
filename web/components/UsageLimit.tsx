"use client";

/* v1.5 hard monthly usage limits: project settings form, usage page banner and resume button. */

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertOctagon, PlayCircle } from "lucide-react";
import { api, type Project, type UsageLimit } from "@/lib/api";
import { layoutApi, layoutKeys, type UsageLimitStatus } from "@/lib/layouts";
import { fmtTime } from "@/lib/format";
import { t } from "@/lib/i18n";
import { qk } from "@/lib/queries";
import { toast } from "@/lib/uiState";
import { useUnsavedGuard } from "./SettingsGuard";
import { Button, Card, ErrorBox, Field, Input, cx, useConfirm } from "./ui";

const LABEL: Record<UsageLimitStatus["exceeded"][number], string> = { cpu_core_hours: "CPU", memory_gb_hours: t("메모리"), egress_gb: t("송신 트래픽") };
const num = (v: number) => (v < 10 ? v.toFixed(2) : v < 1000 ? v.toFixed(1) : Math.round(v).toLocaleString());

/** Thin usage bar: pink below 80%, amber from 80%, red at or above 100%. */
export function UsageMeter({ used, limit, label }: { used: number; limit: number; label: string }) {
  const ratio = limit > 0 ? used / limit : 1;
  const pct = Math.min(100, Math.max(0, ratio * 100));
  return (
    <div
      className="mt-2 h-1.5 overflow-hidden rounded-full bg-gray-100"
      role="progressbar"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round(pct)}
      aria-valuetext={`${Math.round(ratio * 100)}%`}
    >
      <div className={cx("h-full rounded-full transition-[width] motion-reduce:transition-none", ratio >= 1 ? "bg-red-600" : ratio >= 0.8 ? "bg-yellow-600" : "bg-pink-500")} style={{ width: `${pct}%` }} />
    </div>
  );
}

/** Polled every minute: the server re-checks limits every 5 minutes. */
export function useUsageLimit(pid: string | null | undefined) {
  return useQuery({ queryKey: layoutKeys.usageLimit(pid ?? ""), queryFn: () => layoutApi.usageLimit(pid!), enabled: !!pid, refetchInterval: 60_000 });
}

function invalidate(qc: ReturnType<typeof useQueryClient>, pid: string) {
  qc.invalidateQueries({ queryKey: layoutKeys.usageLimit(pid) });
  qc.invalidateQueries({ queryKey: qk.project(pid) });
  qc.invalidateQueries({ queryKey: ["usage", pid] });
  qc.invalidateQueries({ queryKey: ["canvas"] });
}

/** Project settings › usage: hard limit with action stop|notify. */
export function UsageLimitSettings({ project, canEdit }: { project: Project; canEdit: boolean }) {
  const qc = useQueryClient();
  const status = useUsageLimit(project.id);
  const cur = project.usage_limit ?? null;
  const [cpu, setCpu] = useState(cur?.cpu_core_hours?.toString() ?? "");
  const [mem, setMem] = useState(cur?.memory_gb_hours?.toString() ?? "");
  const [egress, setEgress] = useState(cur?.egress_gb?.toString() ?? "");
  const [action, setAction] = useState<UsageLimit["action"]>(cur?.action ?? "stop");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [saved, setSaved] = useState(false);
  const parse = (s: string) => (s.trim() === "" ? undefined : Number(s));
  const vals = [parse(cpu), parse(mem), parse(egress)];
  const valid = vals.every((v) => v === undefined || (Number.isFinite(v) && v > 0 && v <= 1e9));
  const mtd = status.data?.month_to_date;
  const dirty =
    vals[0] !== (cur?.cpu_core_hours ?? undefined) ||
    vals[1] !== (cur?.memory_gb_hours ?? undefined) ||
    vals[2] !== (cur?.egress_gb ?? undefined) ||
    (!!cur && action !== cur.action) ||
    (!cur && vals.some((v) => v !== undefined));
  useUnsavedGuard(dirty && canEdit);
  const field = (label: string, unit: string, v: string, set: (s: string) => void, used?: number) => {
    const lim = parse(v);
    const bad = lim !== undefined && !(Number.isFinite(lim) && lim > 0 && lim <= 1e9);
    const pct = used !== undefined && lim !== undefined && !bad ? Math.round((used / lim) * 100) : null;
    return (
      <div>
        <Field
          label={label}
          hint={
            bad ? (
              <span className="text-danger">{t("0보다 큰 값을 입력하거나 비워 두세요.")}</span>
            ) : used !== undefined ? (
              <span className="tabular-nums">
                {unit} · {t("이번 달 {used}", { used: num(used) })}
                {pct !== null && <span className={cx(pct >= 100 ? "text-danger" : pct >= 80 && "text-warn")}> · {pct}%</span>}
              </span>
            ) : (
              unit
            )
          }
        >
          <Input type="number" min={0} step="any" value={v} invalid={bad} disabled={!canEdit} onChange={(e) => (set(e.target.value), setSaved(false))} placeholder={t("없음")} />
        </Field>
        {pct !== null && <UsageMeter used={used!} limit={lim!} label={t("{label} 한도 사용률", { label })} />}
      </div>
    );
  };
  return (
    <form
      onSubmit={async (e) => {
        e.preventDefault();
        setBusy(true);
        setError(null);
        const ul: UsageLimit = { action };
        if (vals[0] !== undefined) ul.cpu_core_hours = vals[0];
        if (vals[1] !== undefined) ul.memory_gb_hours = vals[1];
        if (vals[2] !== undefined) ul.egress_gb = vals[2];
        const off = vals.every((v) => v === undefined);
        try {
          await api.updateProject(project.id, { usage_limit: off ? null : ul });
          invalidate(qc, project.id);
          setSaved(true);
          toast(t("저장했습니다. 한도를 바로 다시 검사했습니다."));
        } catch (err) {
          setError(err);
        } finally {
          setBusy(false);
        }
      }}
    >
      <Card
        title={t("사용량 한도")}
        description={t("한도에 닿으면 서비스를 멈추거나 알림만 보낼 수 있습니다.")}
        footer={
          canEdit ? (
            <Button type="submit" variant="primary" loading={busy} disabled={!valid || !dirty}>
              {t("저장")}
            </Button>
          ) : undefined
        }
      >
        <div className="space-y-5">
          <ErrorBox error={status.error} />
          <div className="grid gap-4 sm:grid-cols-3">
            {field("CPU", t("코어·시간 / 월"), cpu, setCpu, mtd?.cpu_core_hours)}
            {field(t("메모리"), t("GB·시간 / 월"), mem, setMem, mtd?.memory_gb_hours)}
            {field(t("송신 트래픽"), t("GB / 월"), egress, setEgress, mtd?.net_egress_gb)}
          </div>
          <fieldset>
            <legend className="mb-2 block text-xs font-semibold leading-[18px] text-gray-600">{t("한도에 닿으면")}</legend>
            <div className="space-y-2" role="radiogroup">
              {(
                [
                  ["stop", t("서비스 정지"), t("실행 중인 서비스를 모두 멈추고(삭제하지 않음) 새 배포를 막습니다. 다음 달이 되거나 한도를 올리면 풀리고, 사용량 페이지에서 다시 시작합니다.")],
                  ["notify", t("알림만"), t("usage.limit_reached 이벤트만 냅니다.")],
                ] as const
              ).map(([v, label, desc]) => (
                <label
                  key={v}
                  className={cx(
                    "flex cursor-pointer gap-2.5 rounded-md border px-3 py-2.5",
                    action === v ? "border-pink-500 bg-pink-500/10" : "border-gray-200 hover:border-gray-300",
                    !canEdit && "cursor-default opacity-70",
                  )}
                >
                  <input type="radio" name="usage-limit-action" className="mt-0.5 accent-pink-500" checked={action === v} disabled={!canEdit} onChange={() => (setAction(v), setSaved(false))} />
                  <span>
                    <span className="block text-sm font-medium text-foreground">{label}</span>
                    <span className="block text-xs text-gray-500">{desc}</span>
                  </span>
                </label>
              ))}
            </div>
          </fieldset>
          <p className="text-sm text-gray-500">
            {t("UTC 달력 월 기준이며 5분마다 아직 집계하지 않은 시간까지 포함해 검사합니다(초과량은 대략 5분치 사용량 이내). 모든 칸을 비우고 저장하면 한도를 끕니다.")}
          </p>
          <ErrorBox error={error} />
        </div>
      </Card>
    </form>
  );
}

/** Usage page: suspension banner with the resume button, or the limit progress while it is set. */
export function UsageLimitBanner({ pid, canAdmin }: { pid: string; canAdmin: boolean }) {
  const qc = useQueryClient();
  const status = useUsageLimit(pid);
  const { confirm, node } = useConfirm();
  const s = status.data;
  if (!s || (!s.usage_limit && !s.usage_suspension)) return null;
  const sp = s.usage_suspension;
  const l = s.usage_limit;
  const resume = () =>
    confirm({
      title: t("서비스 다시 시작"),
      message: sp?.active
        ? t("정지를 풀고 한도 때문에 멈춘 서비스를 다시 배포합니다. 이번 달 사용량이 아직 한도를 넘으면 먼저 한도를 올리거나 지워야 합니다.")
        : t("한도 때문에 멈춘 서비스 {count}개를 다시 배포합니다.", { count: sp?.stopped_instances.length ?? 0 }),
      confirmLabel: t("다시 시작"),
      run: async () => {
        try {
          const r = await layoutApi.resumeUsage(pid);
          const failed = Object.keys(r.failed).length;
          toast(t("서비스 {count}개를 다시 시작합니다{failedNote}", { count: r.restarted.length, failedNote: failed ? ` ${t("({failed}개 실패)", { failed })}` : "" }), failed ? "error" : "info");
        } catch (e) {
          toast(e instanceof Error ? e.message : String(e), "error");
        } finally {
          invalidate(qc, pid);
        }
      },
    });
  const bars = l
    ? (
        [
          ["cpu_core_hours", l.cpu_core_hours, s.month_to_date.cpu_core_hours, t("코어·h")],
          ["memory_gb_hours", l.memory_gb_hours, s.month_to_date.memory_gb_hours, t("GB·h")],
          ["egress_gb", l.egress_gb, s.month_to_date.net_egress_gb, "GB"],
        ] as const
      ).filter(([, lim]) => lim)
    : [];
  return (
    <section
      aria-label={t("사용량 한도")}
      className={cx("rounded-lg border px-4 py-3.5", sp?.active ? "border-red-200 bg-danger-surface" : sp ? "border-gray-200 bg-warn-surface" : "border-gray-200 bg-secondaryBg")}
    >
      {node}
      {sp && (
        <div role={sp.active ? "alert" : "status"} className="mb-3 flex flex-wrap items-start gap-3">
          <AlertOctagon className={cx("mt-0.5 size-5 shrink-0", sp.active ? "text-danger" : "text-warn")} aria-hidden />
          <div className="min-w-0 flex-1 text-sm">
            <div className={cx("font-semibold", sp.active ? "text-danger" : "text-warn")}>
              {sp.active ? t("사용량 한도로 프로젝트가 정지되었습니다") : t("사용량 한도 정지가 풀렸습니다")}
            </div>
            <div className="mt-0.5 tabular-nums text-gray-600">
              {sp.reason}
              {sp.active
                ? ` · ${t("{time}까지 새 배포를 할 수 없습니다. 한도를 올리거나 지우면 풀립니다.", { time: fmtTime(sp.until) })}`
                : ` · ${t("멈춘 서비스 {count}개는 자동으로 다시 시작하지 않습니다.", { count: sp.stopped_instances.length })}`}
            </div>
            <div className="mt-0.5 text-xs tabular-nums text-gray-500">{t("정지 {time}", { time: fmtTime(sp.suspended_at) })}</div>
          </div>
          {canAdmin && (
            <Button size="sm" variant={sp.active ? "default" : "primary"} onClick={resume}>
              <PlayCircle className="size-3.5" /> {t("서비스 다시 시작")}
            </Button>
          )}
        </div>
      )}
      {bars.length > 0 && (
        <div>
          <div className="mb-2 text-sm text-gray-500">
            {t("{month} 사용량 한도 · 한도에 닿으면 {action}", { month: s.month, action: l!.action === "stop" ? t("서비스 정지") : t("알림만") })}
          </div>
          <div className="grid gap-3 sm:grid-cols-3">
            {bars.map(([k, lim, used, unit]) => {
              const over = s.exceeded.includes(k);
              return (
                <div key={k}>
                  <div className="flex items-baseline justify-between text-xs">
                    <span className="text-gray-600">{LABEL[k]}</span>
                    <span className={cx("tabular-nums", over ? "text-danger" : "text-gray-500")}>
                      {num(used)} / {num(lim!)} {unit}
                    </span>
                  </div>
                  <UsageMeter used={over ? Math.max(used, lim!) : used} limit={lim!} label={t("{label} 한도 사용률", { label: LABEL[k] })} />
                </div>
              );
            })}
          </div>
        </div>
      )}
    </section>
  );
}
