"use client";

import { useEffect, useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";
import {
  api,
  type CanvasService,
  type Environment,
  type ImageUpdate,
  type ImageUpdatePolicy,
  type ImageUpdateWindow,
  type ServiceInstance,
} from "@/lib/api";
import { qk, useCaps, useImageUpdates } from "@/lib/queries";
import { fmtTime } from "@/lib/format";
import { t } from "@/lib/i18n";
import { Button, ErrorBox, Field, LABEL_CLS, Select, cx } from "../ui";

/** Hours are only meaningful for the custom window; 0 is a valid start hour, so default only when unset or not custom. */
const startHour = (inst: ServiceInstance) => (inst.image_update_window === "custom" ? (inst.image_update_start_hour ?? 2) : 2);
const endHour = (inst: ServiceInstance) => (inst.image_update_window === "custom" && inst.image_update_end_hour ? inst.image_update_end_hour : 6);

const DAYS = [t("일"), t("월"), t("화"), t("수"), t("목"), t("금"), t("토")];
const DEFAULT_DAYS = [1, 2, 3, 4, 5];
const STATUS_LABEL: Record<ImageUpdate["status"], string> = {
  available: t("업데이트 가능"),
  deploying: t("배포 중"),
  applied: t("적용됨"),
  failed: t("실패"),
  skipped: t("건너뜀"),
  superseded: t("이전 항목"),
};
const STATUS_TONE: Record<ImageUpdate["status"], string> = {
  available: "text-warn",
  deploying: "text-pink-500",
  applied: "text-success",
  failed: "text-danger",
  skipped: "text-gray-500",
  superseded: "text-gray-400",
};

export function ImageUpdateSettings({ svc, env, inst }: { svc: CanvasService; env: Environment; inst: ServiceInstance }) {
  const qc = useQueryClient();
  const canEdit = useCaps(svc.project_id).canEdit;
  const updates = useImageUpdates(svc.id, env.id);
  const [policy, setPolicy] = useState<ImageUpdatePolicy>(inst.image_update_policy || "disabled");
  const [win, setWin] = useState<ImageUpdateWindow>(inst.image_update_window || "anytime");
  const [days, setDays] = useState<number[]>(inst.image_update_days?.length ? inst.image_update_days : DEFAULT_DAYS);
  const [start, setStart] = useState(startHour(inst));
  const [end, setEnd] = useState(endHour(inst));
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    setPolicy(inst.image_update_policy || "disabled");
    setWin(inst.image_update_window || "anytime");
    setDays(inst.image_update_days?.length ? inst.image_update_days : DEFAULT_DAYS);
    setStart(startHour(inst));
    setEnd(endHour(inst));
  }, [inst.image_update_policy, inst.image_update_window, inst.image_update_days, inst.image_update_start_hour, inst.image_update_end_hour]);

  const list = updates.data ?? [];
  const deploying = list.some((u) => u.status === "deploying");
  const dirty =
    policy !== (inst.image_update_policy || "disabled") ||
    win !== (inst.image_update_window || "anytime") ||
    (win === "custom" &&
      (days.join(",") !== (inst.image_update_days ?? []).join(",") || start !== inst.image_update_start_hour || end !== inst.image_update_end_hour));

  const refresh = (deployments = false) =>
    Promise.all([
      qc.invalidateQueries({ queryKey: qk.canvas(env.id) }),
      qc.invalidateQueries({ queryKey: qk.imageUpdates(svc.id, env.id) }),
      deployments ? qc.invalidateQueries({ queryKey: qk.deployments(svc.id, env.id) }) : null,
    ]);

  const run = async (key: string, fn: () => Promise<unknown>, deployments = false) => {
    setBusy(key);
    setError(null);
    try {
      const res = await fn();
      if (Array.isArray(res)) qc.setQueryData(qk.imageUpdates(svc.id, env.id), res);
      await refresh(deployments);
      return true;
    } catch (err) {
      setError(err);
      return false;
    } finally {
      setBusy(null);
    }
  };

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setSaved(false);
    const ok = await run("save", () =>
      api.updateInstance(svc.id, env.id, {
        image_update_policy: policy,
        image_update_window: win,
        image_update_days: win === "custom" ? days : [],
        image_update_start_hour: win === "custom" ? start : 0,
        image_update_end_hour: win === "custom" ? end : 0,
      }),
    );
    setSaved(ok);
  };

  const toggleDay = (day: number) =>
    setDays((old) => (old.includes(day) ? old.filter((d) => d !== day) : [...old, day].sort((a, b) => a - b)));

  return (
    <div>
      <form onSubmit={save} className="space-y-4">
        <Field label={t("업데이트 범위")}>
          <Select value={policy} disabled={!canEdit} onChange={(e) => setPolicy(e.target.value as ImageUpdatePolicy)} size="sm" className="w-full" aria-label={t("이미지 업데이트 범위")}>
            <option value="disabled">{t("사용 안 함")}</option>
            <option value="patch">{t("패치만")}</option>
            <option value="minor">{t("마이너 및 패치")}</option>
          </Select>
        </Field>
        {policy !== "disabled" && (
          <>
            <Field label={t("유지보수 시간 (UTC)")}>
              <Select value={win} disabled={!canEdit} onChange={(e) => setWin(e.target.value as ImageUpdateWindow)} size="sm" className="w-full" aria-label={t("이미지 업데이트 유지보수 시간")}>
                <option value="anytime">{t("언제든지 (감지 즉시)")}</option>
                <option value="weekends">{t("주말 (토·일 종일)")}</option>
                <option value="night">{t("야간 (매일 02:00–06:00)")}</option>
                <option value="custom">{t("사용자 지정")}</option>
              </Select>
            </Field>
            {win === "custom" && (
              <div className="space-y-3 rounded-lg border border-gray-200 bg-secondaryBg p-4">
                <div className="flex flex-wrap gap-1.5" role="group" aria-label={t("업데이트 요일")}>
                  {DAYS.map((label, day) => (
                    <button key={day} type="button" disabled={!canEdit} aria-pressed={days.includes(day)} onClick={() => toggleDay(day)}
                      className={cx("size-[34px] cursor-pointer rounded-md border text-sm hover:border-gray-300", days.includes(day) ? "border-gray-300 bg-secondaryBg text-pink-500" : "border-gray-200 text-gray-500", !canEdit && "cursor-not-allowed opacity-60")}>
                      {label}
                    </button>
                  ))}
                </div>
                <div className="flex items-center gap-2 text-sm text-gray-600">
                  <Select aria-label={t("시작 시각")} value={String(start)} disabled={!canEdit} onChange={(e) => setStart(Number(e.target.value))} size="sm" className="w-28 tabular-nums">
                    {Array.from({ length: 24 }, (_, h) => <option key={h} value={h}>{String(h).padStart(2, "0")}:00</option>)}
                  </Select>
                  <span>{t("부터")}</span>
                  <Select aria-label={t("종료 시각")} value={String(end)} disabled={!canEdit} onChange={(e) => setEnd(Number(e.target.value))} size="sm" className="w-28 tabular-nums">
                    {Array.from({ length: 24 }, (_, h) => h + 1).map((h) => <option key={h} value={h}>{h === 24 ? "24:00" : `${String(h).padStart(2, "0")}:00`}</option>)}
                  </Select>
                  <span>{t("까지")}</span>
                </div>
                {(days.length === 0 || start >= end) && <p className="text-xs leading-[18px] text-red-600">{t("요일을 하나 이상 고르고, 종료 시각을 시작 시각보다 늦게 정하세요.")}</p>}
              </div>
            )}
            <p className="text-xs leading-[18px] text-gray-600">
              {t("v1.2.3 같은 SemVer 태그는 선택한 범위 안에서 새 태그로 바뀝니다(메이저 버전은 바꾸지 않음). latest·canary 같은 태그는 이름을 유지하고 새 이미지가 푸시되면 다시 배포합니다. digest로 고정한 이미지는 확인하지 않습니다. 자동 업데이트 전에 연결된 볼륨을 백업합니다.")}
            </p>
          </>
        )}
        <div className="flex flex-wrap items-center gap-2">
          <Button type="submit" variant="primary" disabled={!canEdit || !dirty || (win === "custom" && policy !== "disabled" && (days.length === 0 || start >= end))} loading={busy === "save"}>
            {t("저장")}
          </Button>
          <Button variant="outline" disabled={!canEdit || !!busy || !inst.image} loading={busy === "check"}
            onClick={() => void run("check", () => api.checkImageUpdates(svc.id, env.id))}>
            <RefreshCw aria-hidden /> {t("업데이트 확인")}
          </Button>
          {saved && !dirty && <span className="text-xs text-gray-500">{t("저장했습니다.")}</span>}
          {deploying && <span className="text-xs text-gray-500">{t("이미지 업데이트를 배포하고 있습니다.")}</span>}
        </div>
      </form>

      {updates.error && <ErrorBox className="mt-3" error={updates.error} />}
      {!!error && <ErrorBox className="mt-3" error={error} />}

      {list.length > 0 && (
        <div className="mt-5 border-t border-gray-200 pt-4">
          <h3 className={LABEL_CLS}>{t("업데이트 기록")}</h3>
          <ul className="space-y-2">
            {list.slice(0, 10).map((u) => (
              <li key={u.id} className="flex min-w-0 items-start justify-between gap-3 text-xs">
                <span className="min-w-0 break-all font-mono text-gray-600" title={u.digest}>
                  {u.current_image === u.target_image ? `${u.target_image} @${u.digest.replace("sha256:", "").slice(0, 12)}` : `${u.current_image} → ${u.target_image}`}
                </span>
                <span className="shrink-0 text-right tabular-nums text-gray-500" title={u.error ?? undefined}>
                  <span className={STATUS_TONE[u.status]}>{STATUS_LABEL[u.status]}</span>
                  {u.status === "applied" || u.status === "failed" ? (u.automatic ? t(" · 자동") : t(" · 수동")) : ""} · {fmtTime(u.finished_at ?? u.detected_at)}
                </span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
