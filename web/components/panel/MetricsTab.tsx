"use client";

import { useRef, useState, type ReactNode } from "react";
import { Clock, Pause, Play } from "lucide-react";
import { METRIC_RANGES, type CanvasService, type Environment, type MetricRange, type ServiceMetrics } from "@/lib/api";
import { useMetrics } from "@/lib/queries";
import { fmtMem, fmtRate, pts, rate } from "@/lib/metrics";
import { t } from "@/lib/i18n";
import { Legend, LineChart } from "../charts/LineChart";
import { Button, ErrorBox, Menu, cx } from "../ui";

/** Short range names (also used by the observability page and dashboards). */
export const RANGE_LABEL: Record<MetricRange, string> = { "1h": t("1시간"), "6h": t("6시간"), "24h": t("24시간"), "7d": t("7일") };
const TRIGGER_LABEL: Record<MetricRange, string> = { "1h": t("최근 1시간"), "6h": t("최근 6시간"), "24h": t("최근 하루"), "7d": t("최근 일주일") };
const QUICK_LABEL: Record<MetricRange, string> = { "1h": t("1시간"), "6h": t("6시간"), "24h": t("1일"), "7d": t("7일") };

const CHART_H = 300;
const CARD = "flex flex-col group p-4 pb-3 transition-colors ease-out bg-background dark:bg-secondaryBg border border-gray-200 rounded-lg";

export function ChartCard({ title, aside, children, footer }: { title: string; aside?: ReactNode; children: ReactNode; footer?: ReactNode }) {
  return (
    <div className={cx(CARD, "min-w-0 gap-2")}>
      <div className="flex items-center justify-between">
        <p className="text-h6 font-semibold">{title}</p>
        {aside}
      </div>
      <div style={{ height: CHART_H }}>
        <div className="flex flex-col">
          {children}
          {footer}
        </div>
      </div>
    </div>
  );
}

function EmptyCard({ title, heading, children }: { title: string; heading: string; children: ReactNode }) {
  return (
    <div className={cx(CARD, "min-w-0")}>
      <p className="text-h6 font-semibold">{title}</p>
      <div className="flex h-full flex-col justify-center space-y-1 px-6 py-24 text-center">
        <p className="text-sm font-semibold leading-6 text-neutral-secondary">{heading}</p>
        <p className="mx-auto max-w-lg text-sm leading-5 text-neutral-secondary">{children}</p>
      </div>
    </div>
  );
}

function SumLegend({ replicas }: { replicas: number }) {
  const item = "shrink-0 select-none text-foreground transition data-[active=true]:text-pink-500 data-[active=false]:opacity-50";
  return (
    <div className="flex flex-wrap justify-start gap-x-3 gap-y-1.5 px-4">
      <button type="button" data-active="true" aria-pressed="true" title={t("레플리카 {count}개 합계", { count: replicas })} className={cx(item, "cursor-pointer")}>
        <div className="flex items-center space-x-1">
          <span data-active="true" className="h-2 w-2 rounded-full border border-current data-[active=true]:bg-current" />
          <span className="text-xs text-neutral">{t("합계")}</span>
        </div>
      </button>
      <button type="button" data-active="false" aria-disabled="true" title={t("레플리카별 지표는 아직 제공되지 않습니다")} className={cx(item, "cursor-not-allowed")}>
        <div className="flex items-center space-x-1">
          <span data-active="false" className="h-2 w-2 rounded-full border border-current data-[active=true]:bg-current" />
          <span className="text-xs text-neutral">{t("레플리카")}</span>
        </div>
      </button>
    </div>
  );
}

const fmtVcpu = (v: number) => `${v.toFixed(v !== 0 && Math.abs(v) < 0.1 ? 2 : 1)} vCPU`;

const ListIcon = () => (
  <svg width="1em" height="1em" viewBox="0 0 16 16" fill="none" aria-hidden>
    <path
      d="M1.6 3a1 1 0 0 1 1-1h10.8a1 1 0 0 1 1 1v2.4a1 1 0 0 1-1 1H2.6a1 1 0 0 1-1-1V3Zm0 7.6a1 1 0 0 1 1-1h10.8a1 1 0 0 1 1 1V13a1 1 0 0 1-1 1H2.6a1 1 0 0 1-1-1v-2.4Z"
      stroke="currentColor"
      strokeWidth="1.5"
    />
  </svg>
);
const GridIcon = () => (
  <svg width="1em" height="1em" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
    <path d="M3 3h7v7H3zm11 0h7v7h-7zm0 11h7v7h-7zM3 14h7v7H3z" />
  </svg>
);

type Layout = "list" | "2up";

export function MetricsTab({ svc, env }: { svc: CanvasService; env: Environment }) {
  const [range, setRange] = useState<MetricRange>("1h");
  const [layout, setLayout] = useState<Layout>("2up");
  const [live, setLive] = useState(true);
  const m = useMetrics(svc.id, env.id, range);
  // Pausing live updates freezes what is on screen; a new range still loads once, then stays frozen.
  const snap = useRef<{ range: MetricRange; data: ServiceMetrics } | null>(null);
  if (m.data && !m.isPlaceholderData && (live || snap.current?.range !== range)) snap.current = { range, data: m.data };
  const data = live ? m.data : (snap.current?.data ?? m.data);

  const ps = data?.points ?? [];
  const replicas = data?.replicas ?? 1;
  const vol = svc.volumes[0];
  const net = [
    { name: t("송신"), color: "var(--yellow-400)", points: rate(ps, (p) => p.net_tx_bytes) },
    { name: t("수신"), color: "var(--blue-500)", points: rate(ps, (p) => p.net_rx_bytes) },
  ];
  const step = data ? (data.step_s >= 60 ? t("{n}분", { n: Math.round(data.step_s / 60) }) : t("{n}초", { n: data.step_s })) : null;
  const offline = svc.instance.status === "offline" || !svc.instance.current_deployment_id;
  const emptyMsg = offline ? t("서비스가 실행되면 지표가 여기에 나옵니다") : t("이 기간에 수집된 지표가 없습니다");

  const toggleItem =
    "relative -ml-px flex h-[34px] cursor-pointer items-center justify-center border border-gray-200 px-3 py-2 text-sm first:ml-0 first:rounded-l-md last:rounded-r-md hover:z-50 hover:border-gray-300 hover:bg-secondaryBg focus:outline-none focus-visible:z-[100] focus-visible:ring-2 focus-visible:ring-pink-500 data-[state=on]:z-50 data-[state=on]:border-gray-300 data-[state=on]:bg-secondaryBg data-[state=on]:text-pink-500 [&_svg]:size-5";

  return (
    <div className="flex h-full flex-col pb-12">
      <div className="flex flex-col space-y-8">
        <div className="flex w-full items-center justify-end space-x-2">
          <div role="radiogroup" aria-label={t("차트 배치")}className="hidden max-w-max rounded-md text-gray-300 md:flex">
            {(
              [
                ["list", t("한 줄에 하나"), <ListIcon key="l" />],
                ["2up", t("한 줄에 둘"), <GridIcon key="g" />],
              ] as const
            ).map(([v, label, icon]) => (
              <button
                key={v}
                type="button"
                role="radio"
                aria-checked={layout === v}
                aria-label={label}
                title={label}
                data-state={layout === v ? "on" : "off"}
                onClick={() => setLayout(v)}
                className={toggleItem}
              >
                {icon}
              </button>
            ))}
          </div>
          <div className="flex gap-2">
            <Menu
              label={t("기간")}
              trigger={(p) => (
                <Button {...p} aria-haspopup="dialog" variant="outline" className="grow [&_svg]:text-gray-500">
                  <Clock /> {TRIGGER_LABEL[range]}
                </Button>
              )}
            >
              {(close) => (
                <div className="-m-1 w-[348px] max-w-[90vw] p-5">
                  <div className="space-y-2">
                    <div className="text-xs font-medium text-neutral-secondary">{t("빠른 범위")}</div>
                    <div className="grid grid-cols-3 gap-3 font-mono">
                      {METRIC_RANGES.map((r) => (
                        <Button
                          key={r}
                          size="sm"
                          role="menuitemradio"
                          aria-checked={r === range}
                          variant={r === range ? "default" : "outline"}
                          onClick={() => {
                            setRange(r);
                            close();
                          }}
                        >
                          {QUICK_LABEL[r]}
                        </Button>
                      ))}
                    </div>
                  </div>
                  {step && <p className="mt-4 text-xs text-neutral-secondary">{t("{step} 간격으로 수집한 값입니다.", { step })}</p>}
                </div>
              )}
            </Menu>
            <Button
              variant={live ? "success" : "outline"}
              aria-label={live ? t("실시간 업데이트 일시정지") : t("실시간 업데이트 재개")}
              title={live ? t("실시간 업데이트 일시정지") : t("실시간 업데이트 재개")}
              aria-pressed={!live}
              onClick={() => setLive((l) => !l)}
              className={cx("w-[34px] px-0", live ? "[&_svg]:text-green-500" : "[&_svg]:text-gray-500")}
            >
              {live ? <Pause /> : <Play />}
            </Button>
          </div>
        </div>

        {m.error ? (
          <ErrorBox error={m.error} />
        ) : (
          <div className={cx("grid grid-cols-1 gap-4 pb-10 md:gap-6", layout === "2up" && "md:grid-cols-2")} aria-busy={m.isPending || undefined}>
            {m.isPending ? (
              [0, 1, 2, 3].map((i) => <div key={i} className="animate-pulse rounded-lg border border-gray-200 bg-gray-100/50" style={{ height: CHART_H + 62 }} />)
            ) : ps.length === 0 ? (
              <>
                <EmptyCard title="CPU" heading={t("CPU 지표가 없습니다")}>
                  {emptyMsg}
                </EmptyCard>
                <EmptyCard title={t("메모리")} heading={t("메모리 지표가 없습니다")}>
                  {emptyMsg}
                </EmptyCard>
                <EmptyCard title={t("네트워크 트래픽")} heading={t("네트워크 지표가 없습니다")}>
                  {emptyMsg}
                </EmptyCard>
              </>
            ) : (
              <>
                <ChartCard title="CPU" aside={<SumLegend replicas={replicas} />}>
                  <LineChart
                    series={[{ name: "CPU", color: "var(--blue-500)", points: pts(ps, (p) => p.cpu_percent / 100) }]}
                    format={fmtVcpu}
                    minMax={1}
                    step
                    height={CHART_H}
                  />
                </ChartCard>
                <ChartCard title={t("메모리")} aside={<SumLegend replicas={replicas} />}>
                  <LineChart series={[{ name: t("메모리"),color: "var(--pink-500)", points: pts(ps, (p) => p.memory_bytes) }]} format={fmtMem} minMax={50e6} step height={CHART_H} />
                </ChartCard>
                <ChartCard title={t("네트워크 트래픽")} footer={<Legend series={net} className="px-4 pb-2 pt-4" />}>
                  <LineChart series={net} format={fmtRate} minMax={1000} step grid={false} height={CHART_H - 31} />
                </ChartCard>
              </>
            )}
            {!m.isPending &&
              (vol && ps.some((p) => p.disk_bytes !== null) ? (
                <ChartCard title={t("볼륨")} aside={<span className="truncate text-xs text-gray-500">{vol.mount_path}</span>}>
                  <LineChart series={[{ name: vol.name, color: "var(--pink-500)", points: pts(ps, (p) => p.disk_bytes) }]} format={fmtMem} minMax={50e6} step height={CHART_H} />
                </ChartCard>
              ) : (
                <EmptyCard title={t("볼륨")} heading={t("볼륨 지표가 없습니다")}>
                  {vol ? emptyMsg : t("볼륨을 연결하면 사용량이 여기에 나옵니다")}
                </EmptyCard>
              ))}
          </div>
        )}
      </div>
    </div>
  );
}
