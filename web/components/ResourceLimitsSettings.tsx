"use client";

import { useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type ResourceLimits, type WorkspaceResources } from "@/lib/api";
import { qk, useMe } from "@/lib/queries";
import { toast } from "@/lib/uiState";
import { t, dateLocale } from "@/lib/i18n";
import { Button, Card, ErrorBox, Field, Input, Select, Spinner } from "./ui";

const fmt = (n: number) => n.toLocaleString(dateLocale(), { maximumFractionDigits: 2 });

function Allocated({ data }: { data: WorkspaceResources }) {
  const a = data.allocated;
  return (
    <dl className="grid gap-3 sm:grid-cols-3">
      {[
        ["CPU", a.cpu_cores, data.limits.cpu_cores, t("코어"), a.unlimited_cpu],
        [t("메모리"), a.memory_mb / 1024, data.limits.memory_mb === null ? null : data.limits.memory_mb / 1024, "GiB", a.unlimited_memory],
        [t("볼륨 할당량"), a.volume_mb / 1024, data.limits.volume_mb === null ? null : data.limits.volume_mb / 1024, "GiB", a.unlimited_volume],
      ].map(([label, used, limit, unit, unlimited]) => (
        <div key={String(label)} className="rounded-md border border-gray-200 p-3">
          <dt className="text-xs text-gray-500">{label}</dt>
          <dd className="mt-1 text-sm tabular-nums">{fmt(Number(used))} {unit} / {limit === null ? t("한도 없음") : `${fmt(Number(limit))} ${unit}`}</dd>
          {Number(unlimited) > 0 && <dd className="mt-1 text-xs text-warn">{t("개별 제한 없음 {count}개", { count: Number(unlimited) })}</dd>}
        </div>
      ))}
    </dl>
  );
}

function ResourceLimitForm({ data }: { data: WorkspaceResources }) {
  const qc = useQueryClient();
  const [cpu, setCPU] = useState(data.limits.cpu_cores?.toString() ?? "");
  const [memory, setMemory] = useState(data.limits.memory_mb === null ? "" : String(data.limits.memory_mb / 1024));
  const [volume, setVolume] = useState(data.limits.volume_mb === null ? "" : String(data.limits.volume_mb / 1024));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const value = (s: string, scale = 1) => s.trim() === "" ? null : Number(s) * scale;
  const limits: ResourceLimits = { cpu_cores: value(cpu), memory_mb: value(memory, 1024), volume_mb: value(volume, 1024) };
  const valid = Object.values(limits).every((v) => v === null || (Number.isFinite(v) && v >= 0))
    && (limits.cpu_cores === null || limits.cpu_cores === 0 || (limits.cpu_cores >= .01 && limits.cpu_cores <= 65536))
    && (limits.memory_mb === null || (Number.isInteger(limits.memory_mb) && limits.memory_mb <= 2 ** 30 && (limits.memory_mb === 0 || limits.memory_mb >= 6)))
    && (limits.volume_mb === null || (Number.isInteger(limits.volume_mb) && limits.volume_mb <= 2 ** 30));
  const cpuBad = limits.cpu_cores !== null && !(Number.isFinite(limits.cpu_cores) && (limits.cpu_cores === 0 || (limits.cpu_cores >= .01 && limits.cpu_cores <= 65536)));
  const memBad = limits.memory_mb !== null && !(Number.isInteger(limits.memory_mb) && limits.memory_mb <= 2 ** 30 && (limits.memory_mb === 0 || limits.memory_mb >= 6));
  const volBad = limits.volume_mb !== null && !(Number.isInteger(limits.volume_mb) && limits.volume_mb >= 0 && limits.volume_mb <= 2 ** 30);
  const dirty = Object.keys(limits).some((key) => limits[key as keyof ResourceLimits] !== data.limits[key as keyof ResourceLimits]);

  return (
    <form onSubmit={async (e) => {
      e.preventDefault();
      if (!valid || busy) return;
      setBusy(true); setError(null);
      try {
        const updated = await api.setResourceLimits(data.workspace.id, limits);
        qc.setQueryData<WorkspaceResources[]>(qk.resourceLimits, (rows) => rows?.map((row) => row.workspace.id === updated.workspace.id ? updated : row));
        qc.setQueryData(qk.workspaceResources(data.workspace.id), updated);
        void qc.invalidateQueries({ queryKey: qk.audit });
        toast(t("자원 한도를 저장했습니다."));
      } catch (e) { setError(e); }
      finally { setBusy(false); }
    }}>
      <Card title={data.workspace.name} description={t("이 작업 공간의 모든 프로젝트·환경·샌드박스를 합산합니다.")}
        footer={<Button type="submit" variant="primary" loading={busy} disabled={!valid || !dirty}>{t("한도 저장")}</Button>}>
        <div className="space-y-5">
          <Allocated data={data} />
          <div className="grid gap-4 sm:grid-cols-3">
            <Field label={t("CPU 한도 (코어)")}><Input aria-label={t("CPU 한도")} className="tabular-nums" invalid={cpuBad} type="number" min={0} step="any" value={cpu} onChange={(e) => setCPU(e.target.value)} placeholder={t("무제한")} disabled={busy} /></Field>
            <Field label={t("메모리 한도 (GiB)")}><Input aria-label={t("메모리 한도")} className="tabular-nums" invalid={memBad} type="number" min={0} step="any" value={memory} onChange={(e) => setMemory(e.target.value)} placeholder={t("무제한")} disabled={busy} /></Field>
            <Field label={t("볼륨 할당 한도 (GiB)")}><Input aria-label={t("볼륨 할당 한도")} className="tabular-nums" invalid={volBad} type="number" min={0} step="any" value={volume} onChange={(e) => setVolume(e.target.value)} placeholder={t("무제한")} disabled={busy} /></Field>
          </div>
          {(cpuBad || memBad || volBad) && (
            <p className="text-xs leading-[18px] text-red-600">
              {cpuBad ? t("CPU는 0 또는 0.01~65536 코어여야 합니다. ") : ""}
              {memBad ? t("메모리는 0 또는 6 MiB 이상이고 MiB 단위로 나누어 떨어져야 합니다. ") : ""}
              {volBad ? t("볼륨 한도는 MiB 단위로 나누어 떨어지는 0 이상의 값이어야 합니다.") : ""}
            </p>
          )}
          <p className="text-sm text-gray-600">{t("빈칸은 무제한, 0은 새 할당 금지입니다. 현재 할당량보다 낮은 한도는 저장할 수 없습니다.")}</p>
          <div className="space-y-2 text-sm text-gray-600">
            <p>{t("CPU·메모리는 서비스별 최대치로 계산합니다. 중지된 서비스도 포함하며, 자동 확장은 최대 개수로 계산합니다.")}</p>
            <p>{t("빌드와 배포 교체 중의 임시 자원은 합산하지 않습니다.")}</p>
            <p>{t("한도가 있는 작업 공간의 새 서비스는 기본 1코어·512 MiB, 새 볼륨은 1 GiB를 배정합니다. 작업 공간 한도가 더 작으면 그 값에 맞춥니다.")}</p>
            <p>{t("볼륨은 설정된 용량의 합계를 제한합니다. 실제 파일 쓰기를 차단하지는 않습니다.")}</p>
            {(data.allocated.unlimited_cpu > 0 || data.allocated.unlimited_memory > 0 || data.allocated.unlimited_volume > 0) && (
              <p className="text-warn">{t("한도를 적용할 항목에 개별 제한이 없으면 먼저 서비스·볼륨 설정에서 지정하세요. 실행 중인 서비스는 재배포해야 적용됩니다.")}</p>
            )}
          </div>
          <ErrorBox error={error} />
        </div>
      </Card>
    </form>
  );
}

export function ResourceLimitsSettings() {
  const me = useMe().data;
  const router = useRouter();
  const selected = useSearchParams().get("workspace");
  const rows = useQuery({ queryKey: qk.resourceLimits, queryFn: api.resourceLimits, refetchInterval: 30_000 });
  const current = selected ? rows.data?.find((row) => row.workspace.id === selected)
    : rows.data?.find((row) => row.workspace.personal && row.workspace.owner_id === me?.id) ?? rows.data?.[0];
  return (
    <div className="max-w-4xl space-y-6">
      <Card title={t("자원 제한")} description={t("한도를 넘는 생성·확장·프로젝트 이동은 서버에서 차단합니다.")}>
        <Field label={t("작업 공간")}>
          <Select aria-label={t("자원 제한 작업 공간")} value={current?.workspace.id ?? ""} onChange={(e) => router.replace(`/admin/?section=resources&workspace=${encodeURIComponent(e.target.value)}`, { scroll: false })} disabled={!rows.data?.length}>
            {!current && <option value="" disabled>{t("작업 공간 선택")}</option>}
            {rows.data?.map(({ workspace: ws }) => <option key={ws.id} value={ws.id}>{ws.name} · {ws.personal ? t("개인") : ws.default ? t("기본") : t("팀")}</option>)}
          </Select>
        </Field>
        <ErrorBox error={rows.error} className="mt-3" />
        {selected && rows.data && !current && <ErrorBox error={t("선택한 작업 공간을 찾을 수 없습니다. 목록에서 다시 선택하세요.")} className="mt-3" />}
      </Card>
      {rows.isPending ? <Spinner /> : current && <ResourceLimitForm key={`${current.workspace.id}:${JSON.stringify(current.limits)}`} data={current} />}
    </div>
  );
}

export function WorkspaceResourceSummary({ wsid }: { wsid: string }) {
  const data = useQuery({ queryKey: qk.workspaceResources(wsid), queryFn: () => api.workspaceResources(wsid), refetchInterval: 30_000 });
  if (!data.data || Object.values(data.data.limits).every((v) => v === null)) return null;
  return <Card title={t("자원 한도")} description={t("이 작업 공간의 할당량입니다. 한도는 서버 관리자가 변경합니다.")} className="mt-8 border-t border-gray-200 pt-8"><Allocated data={data.data} /></Card>;
}
