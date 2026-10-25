"use client";

import Link from "next/link";
import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Ban, Pause, Play, RefreshCw } from "lucide-react";
import { api, type CanvasService, type Environment, type OwnerHold, type OwnerHoldScope, type ServiceAdminContainer } from "@/lib/api";
import { qk, useMe } from "@/lib/queries";
import { dateLocale, t } from "@/lib/i18n";
import { Button, CopyButton, Dialog, ErrorBox, Input, Spinner, cx } from "../ui";
import { HoldControl, HoldDialog } from "../AdminOverview";
import { CARD, Hint, SettingItem } from "./settings/primitives";

/**
 * 서비스 패널의 "어드민" 탭. 서버 관리자(users.role owner/admin)에게만 보인다(ServicePanel이 거르고 API도 403).
 * 작업 공간 관리자와는 무관하다: 서비스 배포 차단·정지, 식별자, 배치 노드, 노드의 실제 컨테이너, 서버 owner 정지, 관련 감사 기록.
 */
export function AdminTab({ svc, env }: { svc: CanvasService; env: Environment }) {
  const me = useMe().data;
  const owner = me?.instance_role === "owner";
  const q = useQuery({ queryKey: qk.serviceAdmin(svc.id, env.id), queryFn: () => api.serviceAdmin(svc.id, env.id), refetchInterval: 15_000 });
  const [holding, setHolding] = useState<{ scope: OwnerHoldScope; id: string; name: string } | null>(null);
  const d = q.data;
  if (q.isLoading) return <div className="flex justify-center py-10"><Spinner /></div>;
  if (!d) return <ErrorBox error={q.error} />;
  const holdOf = (scope: OwnerHoldScope, id: string) => d.holds.find((h) => h.scope === scope && h.target_id === id);
  const fmt = (s: string) => new Date(s).toLocaleString(dateLocale());
  const ids: [string, string, string?][] = [
    [t("작업 공간"), d.workspace.id, d.workspace.name],
    [t("프로젝트"), d.project.id, d.project.name],
    [t("환경"), d.environment.id, d.environment.name],
    [t("서비스"), d.service_id, svc.name],
    [t("인스턴스"), d.instance_id],
    ...(d.current_deployment_id ? [[t("현재 배포"), d.current_deployment_id] as [string, string]] : []),
  ];
  return (
    <div className="flex flex-col gap-10 pb-16">
      <div className="flex flex-wrap items-center justify-between gap-3 text-xs text-gray-500">
        <span>{t("서버 관리자에게만 보이는 탭입니다. 작업 공간 관리자 권한과는 별개입니다.")}</span>
        <Button size="sm" loading={q.isFetching} onClick={() => void q.refetch()}><RefreshCw className="size-3.5" />{t("새로고침")}</Button>
      </div>
      <ErrorBox error={q.error} />

      <SettingItem
        label={t("서비스 제어")}
        description={t("모든 환경에 적용되며 서버 owner를 포함한 모든 사용자의 배포를 막습니다. 멤버에게는 차단 사실과 사유가 보입니다.")}
      >
        {!d.can_control && <Hint className="mb-2 mt-0">{t("서버 owner가 서버 관리자의 서비스 정지·배포 차단 기능을 막아 두었습니다.")}</Hint>}
        <div className={cx(CARD, "divide-y divide-gray-100")}>
          <ServiceHoldRow
            svc={svc}
            scope="deploy"
            title={t("배포 차단")}
            help={t("도는 컨테이너는 그대로 두고 새 배포(GitHub push, 이미지 업데이트, 재배포, 롤백 포함)를 막습니다.")}
            hold={holdOf("deploy", svc.id)}
            disabled={!d.can_control}
            onChanged={() => void q.refetch()}
          />
          {!svc.instance.self && (
            <ServiceHoldRow
              svc={svc}
              scope="service"
              title={t("서비스 정지")}
              help={t("모든 환경의 컨테이너를 멈추고 배포·재시작을 막습니다. 해제하면 멈췄던 인스턴스를 다시 띄웁니다.")}
              hold={holdOf("service", svc.id)}
              disabled={!d.can_control}
              onChanged={() => void q.refetch()}
            />
          )}
        </div>
      </SettingItem>

      <SettingItem label={t("식별자")} description={t("API·CLI와 감사 기록에서 쓰는 ID입니다.")}>
        <div className={cx(CARD, "divide-y divide-gray-100")}>
          {ids.map(([label, id, name]) => (
            <div key={label} className="flex min-w-0 items-center gap-3 py-1.5 text-sm">
              <span className="w-24 shrink-0 text-gray-600">{label}</span>
              <span className="min-w-0 flex-1 truncate">
                <span className="font-mono">{id}</span>
                {name && <span className="ml-2 text-gray-500">{name}</span>}
              </span>
              <CopyButton value={id} label={t("{name} ID 복사", { name: label })} />
            </div>
          ))}
        </div>
      </SettingItem>

      <SettingItem
        label={t("배치 노드")}
        description={d.node ? undefined : t("노드 정보를 찾을 수 없습니다.")}
      >
        {d.node && (
          <div className={cx(CARD, "flex flex-wrap items-center gap-x-6 gap-y-1 py-3 text-sm")}>
            <span className="font-semibold">{d.node.name}</span>
            <span className="font-mono text-gray-600">{d.node.id}</span>
            <span className={d.node.status === "online" ? "text-success" : "text-danger"}>{d.node.status}</span>
            {d.node.version && <span className="text-gray-600">aven {d.node.version}</span>}
            <Link href="/admin/?section=nodes" className="ml-auto text-pink-500 hover:underline">{t("노드 관리")}</Link>
          </div>
        )}
      </SettingItem>

      <SettingItem label={t("컨테이너")} description={t("노드에서 이 인스턴스에 속한 컨테이너를 직접 조회한 결과입니다(멈춘 컨테이너 포함).")}>
        {d.containers_error ? (
          <Hint tone="warn" className="mt-0">{t("컨테이너를 조회하지 못했습니다: {error}", { error: d.containers_error })}</Hint>
        ) : d.containers.length === 0 ? (
          <div className={cx(CARD, "py-3 text-sm text-gray-600")}>{t("이 인스턴스의 컨테이너가 없습니다.")}</div>
        ) : (
          <div className="flex flex-col gap-2">
            {d.containers.map((c) => <ContainerRow key={c.name} c={c} fmt={fmt} />)}
          </div>
        )}
      </SettingItem>

      {owner && (
        <SettingItem
          label={t("프로젝트·작업 공간 정지")}
          description={t("서버 owner 권한으로 프로젝트나 작업 공간 전체를 멈추고 배포를 막습니다. 서버 owner만 재개할 수 있습니다.")}
        >
          <div className={cx(CARD, "divide-y divide-gray-100")}>
            {([["project", d.project], ["workspace", d.workspace]] as const).map(([scope, ref]) => {
              const hold = holdOf(scope, ref.id);
              return (
                <div key={scope} className="flex min-w-0 flex-wrap items-center gap-3 py-2 text-sm">
                  <span className="w-24 shrink-0 text-gray-600">{scope === "project" ? t("프로젝트") : t("작업 공간")}</span>
                  <span className="min-w-0 flex-1 truncate">
                    {ref.name}
                    {hold?.reason && <span className="ml-2 text-gray-500">{hold.reason}</span>}
                  </span>
                  <HoldControl
                    scope={scope}
                    id={ref.id}
                    name={ref.name}
                    hold={hold}
                    onHold={() => setHolding({ scope, id: ref.id, name: ref.name })}
                    onChanged={() => void q.refetch()}
                  />
                </div>
              );
            })}
          </div>
          <HoldDialog target={holding} onClose={() => setHolding(null)} onChanged={() => void q.refetch()} />
        </SettingItem>
      )}
      {!owner && d.holds.some((h) => h.scope === "project" || h.scope === "workspace") && (
        <SettingItem label={t("프로젝트·작업 공간 정지")}>
          <Hint tone="danger" className="mt-0">{t("서버 owner가 이 서비스의 프로젝트 또는 작업 공간을 정지했습니다.")}</Hint>
        </SettingItem>
      )}

      <SettingItem label={t("감사 기록")} description={t("이 서비스·인스턴스·환경·프로젝트를 대상으로 남은 최근 기록입니다.")}>
        {d.audit_hidden ? (
          <Hint className="mt-0">{t("서버 owner가 서버 관리자의 감사 기록 기능을 막아 두었습니다.")}</Hint>
        ) : d.audit.length === 0 ? (
          <div className={cx(CARD, "py-3 text-sm text-gray-600")}>{t("기록이 없습니다.")}</div>
        ) : (
          <div className={cx(CARD, "divide-y divide-gray-100")}>
            {d.audit.map((a) => (
              <div key={a.id} className="grid gap-0.5 py-2 text-sm">
                <div className="flex flex-wrap items-center gap-x-3 text-xs text-gray-500">
                  <span className="tabular-nums">{fmt(a.created_at)}</span>
                  <span className="font-mono">{a.kind}</span>
                  {a.actor && <span>{a.actor}</span>}
                  {a.ip && <span className="font-mono">{a.ip}</span>}
                </div>
                {a.message && <div className="break-words">{a.message}</div>}
              </div>
            ))}
          </div>
        )}
      </SettingItem>
    </div>
  );
}

function ContainerRow({ c, fmt }: { c: ServiceAdminContainer; fmt: (s: string) => string }) {
  const ips = Object.entries(c.ips ?? {}).flatMap(([net, list]) => list.map((ip) => `${net} ${ip}`));
  return (
    <div className={cx(CARD, "grid gap-1 py-3 text-sm")}>
      <div className="flex min-w-0 items-center gap-3">
        <span className={cx("size-2 shrink-0 rounded-full", c.running ? "bg-success" : "bg-gray-400")} aria-hidden />
        <span className="min-w-0 flex-1 truncate font-mono">{c.name}</span>
        <CopyButton value={c.name} label={t("컨테이너 이름 복사")} />
      </div>
      <div className="flex flex-wrap gap-x-4 gap-y-0.5 pl-5 text-xs text-gray-600">
        <span>{c.status}{!c.running && c.exit_code !== 0 ? ` (${t("종료 코드 {code}", { code: c.exit_code })})` : ""}</span>
        {c.role && <span>{t("역할 {role}", { role: c.role })}</span>}
        {c.replica && <span>{t("레플리카 {n}", { n: c.replica })}</span>}
        <span className={c.restart_count > 0 ? "text-warn" : undefined}>{t("재시작 {count}회", { count: c.restart_count })}</span>
        {c.started_at && <span>{t("시작 {time}", { time: fmt(c.started_at) })}</span>}
        {c.deployment_id && <span className="font-mono">{c.deployment_id}</span>}
      </div>
      {(ips.length > 0 || c.volumes.length > 0) && (
        <div className="flex flex-wrap gap-x-4 gap-y-0.5 pl-5 font-mono text-xs text-gray-500">
          {ips.map((ip) => <span key={ip}>{ip}</span>)}
          {c.volumes.map((v) => <span key={v}>vol {v}</span>)}
        </div>
      )}
    </div>
  );
}

/** 서비스 배포 차단·정지 한 줄: 상태와 걸기(사유 입력)·풀기 버튼. */
function ServiceHoldRow({ svc, scope, title, help, hold, disabled, onChanged }: {
  svc: CanvasService;
  scope: "service" | "deploy";
  title: string;
  help: string;
  hold?: OwnerHold;
  disabled: boolean;
  onChanged: () => void;
}) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const refresh = async () => {
    // 캔버스·인스턴스의 admin_hold 표시도 바뀐다.
    await Promise.all([qc.invalidateQueries({ queryKey: ["canvas"] }), qc.invalidateQueries({ queryKey: ["instance", svc.id] })]);
    onChanged();
  };
  const run = async (f: () => Promise<unknown>, after?: () => void) => {
    setBusy(true);
    setError(null);
    try {
      await f();
      after?.();
      await refresh();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  const close = () => {
    if (busy) return;
    setOpen(false);
    setReason("");
    setError(null);
  };
  return (
    <div className="grid gap-1 py-3 text-sm">
      <div className="flex min-w-0 flex-wrap items-center gap-3">
        <span className="font-medium">{title}</span>
        {hold && <span className="rounded bg-danger-surface px-1.5 py-0.5 text-xs font-medium text-danger">{scope === "deploy" ? t("차단됨") : t("정지됨")}</span>}
        <span className="ml-auto">
          {hold ? (
            <Button size="sm" variant="outline" loading={busy} disabled={disabled} onClick={() => void run(() => api.releaseServiceHold(svc.id, scope))}>
              <Play className="size-3.5" />{scope === "deploy" ? t("차단 해제") : t("재개")}
            </Button>
          ) : (
            <Button size="sm" variant="outline" disabled={disabled} onClick={() => setOpen(true)}>
              {scope === "deploy" ? <Ban className="size-3.5" /> : <Pause className="size-3.5" />}{scope === "deploy" ? t("배포 차단") : t("정지")}
            </Button>
          )}
        </span>
      </div>
      <p className="text-xs text-gray-600">{help}</p>
      {hold && (
        <p className="text-xs text-gray-500">
          {t("{who} · {when}", { who: hold.created_by || "-", when: new Date(hold.created_at).toLocaleString(dateLocale()) })}
          {hold.reason && ` · ${hold.reason}`}
        </p>
      )}
      {!open && <ErrorBox error={error} />}
      <Dialog
        open={open}
        onClose={close}
        title={scope === "deploy" ? t("{name} 배포 차단", { name: svc.name }) : t("{name} 정지", { name: svc.name })}
        footer={
          <>
            <Button variant="outline" onClick={close} disabled={busy}>{t("취소")}</Button>
            <Button variant="danger" loading={busy} onClick={() => void run(() => api.createServiceHold(svc.id, scope, reason.trim()), () => { setOpen(false); setReason(""); })}>
              {scope === "deploy" ? t("배포 차단") : t("정지")}
            </Button>
          </>
        }
      >
        <div className="space-y-3 pt-1">
          <p>{help}</p>
          <Input value={reason} onChange={(e) => setReason(e.target.value)} maxLength={200} placeholder={t("사유 (멤버에게 보입니다, 선택)")} aria-label={t("사유")} />
          <ErrorBox error={error} />
        </div>
      </Dialog>
    </div>
  );
}
