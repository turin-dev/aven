"use client";

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { HardDrive, KeyRound, Settings2, Variable } from "lucide-react";
import { api, type Environment, type SyncInclude, type SyncServiceDiff, type SyncVarChange } from "@/lib/api";
import { qk } from "@/lib/queries";
import { toast } from "@/lib/uiState";
import { Button, CenterSpinner, Dialog, ErrorBox, Field, Select, cx } from "./ui";
import { t } from "@/lib/i18n";

const INCLUDE_LABELS: [keyof SyncInclude, string][] = [
  ["settings", t("설정")],
  ["variables", t("서비스 변수")],
  ["shared_variables", t("공유 변수")],
  ["volumes", t("볼륨|plural")],
];

function show(v: unknown): string {
  if (v === null || v === undefined) return t("없음");
  if (typeof v === "string") return v === "" ? '""' : v;
  return JSON.stringify(v);
}

function VarRows({ list }: { list: SyncVarChange[] }) {
  return (
    <ul className="space-y-0.5">
      {list.map((c) => (
        <li key={c.key} className="flex min-w-0 items-baseline gap-2 font-mono text-xs">
          <span
            className={cx(
              "w-4 shrink-0 text-center",
              c.action === "added" ? "text-success" : c.action === "removed" ? "text-danger" : "text-warn",
            )}
            aria-label={c.action === "added" ? t("추가") : c.action === "removed" ? t("삭제") : t("변경|diff")}
          >
            {c.action === "added" ? "+" : c.action === "removed" ? "−" : "~"}
          </span>
          <span className="shrink-0 text-foreground">{c.key}</span>
          <span className="min-w-0 truncate text-gray-500">
            {c.action === "changed" ? `${c.from} → ${c.to}` : c.action === "added" ? c.to : c.from}
          </span>
        </li>
      ))}
    </ul>
  );
}

function ServiceDiff({
  d,
  include,
  checked,
  onToggle,
}: {
  d: SyncServiceDiff;
  include: SyncInclude;
  checked: boolean;
  onToggle: (v: boolean) => void;
}) {
  const id = `sync-${d.service_id}`;
  const parts = [
    include.settings && d.settings.length > 0,
    include.variables && d.variables.length > 0,
    include.volumes && d.volumes.length > 0,
  ];
  const empty = !parts.some(Boolean);
  return (
    <li className={cx("rounded-md border border-gray-200 px-3 py-2.5", (empty || !checked) && "opacity-60")}>
      <div className="flex items-center gap-2">
        <input
          id={id}
          type="checkbox"
          className="size-4 cursor-pointer accent-pink-500"
          checked={checked && !empty}
          disabled={empty}
          onChange={(e) => onToggle(e.target.checked)}
        />
        <label htmlFor={id} className="cursor-pointer text-sm font-medium text-foreground">
          {d.service_name}
        </label>
        {d.action === "create" && (
          <span className="rounded bg-green-100 px-1.5 py-0.5 text-xs font-medium text-green-600">{t("새로 구성")}</span>
        )}
        {empty && <span className="text-xs text-gray-400">{t("선택한 항목에는 변경이 없습니다")}</span>}
      </div>
      <div className="mt-2 space-y-2 pl-6">
        {include.settings && d.settings.length > 0 && (
          <div>
            <div className="mb-0.5 flex items-center gap-1 text-xs text-gray-400">
              <Settings2 className="size-3" aria-hidden /> {t("설정")}
            </div>
            <ul className="space-y-0.5">
              {d.settings.map((s) => (
                <li key={s.field} className="flex min-w-0 items-baseline gap-2 font-mono text-xs">
                  <span className="shrink-0 text-foreground">{s.field}</span>
                  <span className="min-w-0 truncate text-gray-500">
                    {show(s.from)} → {show(s.to)}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}
        {include.variables && d.variables.length > 0 && (
          <div>
            <div className="mb-0.5 flex items-center gap-1 text-xs text-gray-400">
              <Variable className="size-3" aria-hidden /> {t("변수")}
            </div>
            <VarRows list={d.variables} />
          </div>
        )}
        {include.volumes && d.volumes.length > 0 && (
          <div>
            <div className="mb-0.5 flex items-center gap-1 text-xs text-gray-400">
              <HardDrive className="size-3" aria-hidden /> {t("새 볼륨(빈 볼륨, 데이터는 복사하지 않음)")}
            </div>
            <ul className="font-mono text-xs text-foreground">
              {d.volumes.map((v) => (
                <li key={v.mount_path}>{v.mount_path}</li>
              ))}
            </ul>
          </div>
        )}
        {d.protected_variables.length > 0 && include.variables && (
          <div className="flex items-start gap-1 text-xs text-gray-400">
            <KeyRound className="mt-0.5 size-3 shrink-0" aria-hidden />
            <span>{t("DB 비밀번호 {names}는 환경마다 달라서 옮기지 않습니다.", { names: d.protected_variables.join(", ") })}</span>
          </div>
        )}
      </div>
    </li>
  );
}

/** "다른 환경에서 동기화": shows what would change in `target` to match the chosen source and applies the selection. */
export function SyncEnvironmentDialog({
  target,
  envs,
  open,
  onClose,
}: {
  target: Environment | undefined;
  envs: Environment[];
  open: boolean;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const sources = envs.filter((e) => e.id !== target?.id);
  const [from, setFrom] = useState("");
  const source = from || sources[0]?.id || "";
  const [include, setInclude] = useState<SyncInclude>({ settings: true, variables: true, shared_variables: true, volumes: true });
  const [unchecked, setUnchecked] = useState<Record<string, boolean>>({});
  const [deploy, setDeploy] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  const plan = useQuery({
    queryKey: ["sync-preview", target?.id ?? "", source],
    queryFn: () => api.syncPreview(target!.id, source),
    enabled: open && !!target && !!source,
    staleTime: 0,
  });

  const selected = (plan.data?.services ?? []).filter((d) => !unchecked[d.service_id]);
  const hasShared = include.shared_variables && (plan.data?.shared_variables.length ?? 0) > 0;
  const hasWork =
    hasShared ||
    selected.some(
      (d) =>
        (include.settings && d.settings.length > 0) ||
        (include.variables && d.variables.length > 0) ||
        (include.volumes && d.volumes.length > 0),
    );

  const close = () => {
    if (busy) return;
    setError(null);
    setUnchecked({});
    onClose();
  };

  const apply = async () => {
    if (!target || !plan.data) return;
    setBusy(true);
    setError(null);
    try {
      const res = await api.syncEnvironment(target.id, {
        from: source,
        services: selected.map((d) => d.service_id),
        include,
        deploy,
      });
      await Promise.all([
        qc.invalidateQueries({ queryKey: qk.canvas(target.id) }),
        qc.invalidateQueries({ queryKey: qk.shared(target.id) }),
        qc.invalidateQueries({ queryKey: ["instance"] }),
        qc.invalidateQueries({ queryKey: ["resolved"] }),
      ]);
      const skipped = res.services.filter((s) => s.deploy_skipped);
      const head = res.shared_variables
        ? t("{from}에서 서비스 {count}개, 공유 변수 {shared}개를 동기화했습니다", { from: res.from.name, count: res.services.length, shared: res.shared_variables })
        : t("{from}에서 서비스 {count}개를 동기화했습니다", { from: res.from.name, count: res.services.length });
      const tail = !deploy
        ? ""
        : skipped.length
          ? t(" (배포 못 함: {names})", { names: skipped.map((s) => s.service_name).join(", ") })
          : t(" · 배포를 시작했습니다");
      toast(head + tail);
      setUnchecked({});
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onClose={close}
      wide
      title={t("{name} 환경 동기화", { name: target?.name ?? "" })}
      footer={
        <>
          <label className="mr-auto flex cursor-pointer items-center gap-2 text-sm text-gray-600">
            <input
              type="checkbox"
              className="size-4 cursor-pointer accent-pink-500"
              checked={deploy}
              onChange={(e) => setDeploy(e.target.checked)}
            />
            {t("적용 후 바뀐 서비스 배포")}
          </label>
          <Button variant="ghost" onClick={close} disabled={busy}>
            {t("취소")}
          </Button>
          <Button variant="primary" onClick={apply} loading={busy} disabled={!hasWork || plan.isFetching}>
            {deploy ? t("적용하고 배포") : t("변경 사항 저장")}
          </Button>
        </>
      }
    >
      <div className="space-y-4 pt-1">
        <p>
          {t("선택한 환경의 설정과 변수를 이 환경으로 가져옵니다. 도메인, TCP 프록시, 노드 배치, deploy key, DB 비밀번호는 환경마다 다르므로 건드리지 않습니다. 배포하지 않으면 변경 사항은 저장만 되고 다음 배포부터 적용됩니다.")}
        </p>
        {sources.length === 0 ? (
          <div className="text-sm text-gray-500">{t("동기화할 다른 환경이 없습니다.")}</div>
        ) : (
          <>
            <Field label={t("가져올 환경")}>
              <Select
                value={source}
                onChange={(e) => {
                  setFrom(e.target.value);
                  setUnchecked({});
                }}
              >
                {sources.map((e) => (
                  <option key={e.id} value={e.id}>
                    {e.name}
                    {e.kind === "preview" && e.pr_number != null ? ` (PR #${e.pr_number})` : ""}
                  </option>
                ))}
              </Select>
            </Field>
            <fieldset className="flex flex-wrap gap-x-4 gap-y-1.5">
              <legend className="mb-1.5 text-sm font-medium text-gray-600">{t("가져올 항목")}</legend>
              {INCLUDE_LABELS.map(([k, label]) => (
                <label key={k} className="flex cursor-pointer items-center gap-1.5 text-sm text-foreground">
                  <input
                    type="checkbox"
                    className="size-4 cursor-pointer accent-pink-500"
                    checked={include[k]}
                    onChange={(e) => setInclude((x) => ({ ...x, [k]: e.target.checked }))}
                  />
                  {label}
                </label>
              ))}
            </fieldset>
            {plan.error ? (
              <ErrorBox error={plan.error} />
            ) : plan.isPending ? (
              <CenterSpinner />
            ) : plan.data.services.length === 0 && plan.data.shared_variables.length === 0 ? (
              <div className="rounded-md border border-dashed border-gray-200 px-4 py-6 text-center text-sm text-gray-500">
                {t("두 환경이 이미 같습니다.")}
              </div>
            ) : (
              <div className="space-y-3" aria-live="polite">
                {plan.data.services.length > 0 && (
                  <ul className="space-y-2" aria-label={t("서비스별 변경")}>
                    {plan.data.services.map((d) => (
                      <ServiceDiff
                        key={d.service_id}
                        d={d}
                        include={include}
                        checked={!unchecked[d.service_id]}
                        onToggle={(v) => setUnchecked((u) => ({ ...u, [d.service_id]: !v }))}
                      />
                    ))}
                  </ul>
                )}
                {plan.data.shared_variables.length > 0 && (
                  <div className={cx("rounded-md border border-gray-200 px-3 py-2.5", !include.shared_variables && "opacity-60")}>
                    <div className="mb-1 text-sm font-medium text-foreground">{t("공유 변수")}</div>
                    <VarRows list={plan.data.shared_variables} />
                  </div>
                )}
              </div>
            )}
          </>
        )}
        {error ? <ErrorBox error={error} /> : null}
      </div>
    </Dialog>
  );
}
