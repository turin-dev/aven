"use client";

// 서비스 패널 "백업" 탭: 서비스에 연결된 볼륨마다 백업 목록과 자동 백업 일정. 데이터베이스(PostgreSQL/MySQL)는 위에 시점 복구(PitrSection)가 붙는다.

import { useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Clock, MoreVertical, Plus, RotateCcw, Trash2 } from "lucide-react";
import { api, type Backup, type CanvasService, type Volume } from "@/lib/api";
import { qk, useBackupSchedule, useBackups, useCaps } from "@/lib/queries";
import { cronError, describeCron } from "@/lib/cron";
import { fmtBytes, fmtRelative, fmtTime } from "@/lib/format";
import { lock, NO_PERM } from "../Perm";
import { ToneBadge, type Tone } from "../StatusBadge";
import { Button, ErrorBox, Field, Input, Menu, MenuItem, Spinner, cx, useConfirm } from "../ui";
import { t, tr } from "@/lib/i18n";

/**
 * Backups tab, modeled on Railway's dashboard: `section.flex.flex-col.gap-y-3` with the primary info banner, the action row
 * (`flex justify-between items-center` › `flex gap-2`) and the backup list, or the clock "No Backups" empty state.
 * keel extras (manual backup, cron schedule, several volumes) live in the action row and the banner.
 */
export function VolumeBackupsTab({ svc }: { svc: CanvasService }) {
  if (!svc.volumes.length)
    return (
      <section className="flex flex-col gap-y-3 pb-8">
        <NoBackups title={t("볼륨 없음")}>{t("백업은 볼륨 단위로 만듭니다. 설정에서 볼륨을 연결하세요.")}</NoBackups>
      </section>
    );
  const many = svc.volumes.length > 1;
  return (
    <div className="flex flex-col gap-y-8 pb-8">
      {svc.volumes.map((v) => (
        <VolumeBackups key={v.id} svc={svc} vol={v} many={many} />
      ))}
    </div>
  );
}

function NoBackups({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center pt-8 text-center">
      <span className="icon-container icon-40 mb-5 text-2xl text-gray-600" aria-hidden>
        <Clock />
      </span>
      <p className="text-base font-bold">{title}</p>
      <p className="text-base text-gray-600">{children}</p>
    </div>
  );
}

const STATUS_BADGE: Record<Backup["status"], { tone: Tone; label: string }> = {
  done: { tone: "neutral", label: t("완료") },
  running: { tone: "progress", label: t("백업 중") },
  failed: { tone: "danger", label: t("실패") },
};

function VolumeBackups({ svc, vol, many }: { svc: CanvasService; vol: Volume; many: boolean }) {
  const qc = useQueryClient();
  const canEdit = useCaps(svc.project_id).canEdit;
  const backups = useBackups(vol.id);
  const schedule = useBackupSchedule(vol.id);
  const { confirm, node } = useConfirm();
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [cron, setCron] = useState("");
  const [keep, setKeep] = useState("7");
  const [loaded, setLoaded] = useState(false);
  const [editing, setEditing] = useState(false);
  if (schedule.data && !loaded) {
    setLoaded(true);
    setCron(schedule.data.cron ?? "");
    setKeep(String(schedule.data.keep));
  }
  const run = async (key: string, fn: () => Promise<unknown>) => {
    setBusy(key);
    setError(null);
    try {
      await fn();
      await qc.invalidateQueries({ queryKey: qk.backups(vol.id) });
      await qc.invalidateQueries({ queryKey: qk.backupSchedule(vol.id) });
      return true;
    } catch (e) {
      setError(e);
      return false;
    } finally {
      setBusy(null);
    }
  };
  const cronErr = cron.trim() ? cronError(cron) : null;
  const keepN = Math.max(1, Number(keep) || 7);
  const dirty = loaded && ((schedule.data?.cron ?? "") !== cron.trim() || schedule.data?.keep !== keepN);
  const list = backups.data ?? [];
  const running = list.some((b) => b.status === "running");
  const savedCron = schedule.data?.cron ?? "";
  const formId = `bk-sched-${vol.id}`;

  return (
    <section className="flex flex-col gap-y-3" aria-label={t("{name} 백업", { name: vol.name })}>
      {many && <p className="text-xs font-medium uppercase text-foreground">{vol.name}</p>}
      <div className="relative flex items-center gap-3 rounded-md bg-primary-surface p-3 text-sm text-primary">
        <div className="flex w-full flex-wrap items-center justify-between gap-2">
          <p>
            {tr("볼륨 {path} 전체를 스냅샷으로 저장합니다. 백업을 보고, 복원하고, 지울 수 있습니다. 복원하면 서비스를 멈추고 볼륨을 덮어쓴 뒤 다시 배포합니다.", {
              path: <span className="font-mono">{vol.mount_path}</span>,
            })}
          </p>
        </div>
      </div>

      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex gap-2">
          <Button variant="outline" onClick={() => run("now", () => api.createBackup(vol.id))} loading={busy === "now"} disabled={running} {...lock(canEdit)}>
            {busy !== "now" && <Plus />} {t("백업")}
          </Button>
          <Button variant="outline" onClick={() => setEditing((o) => !o)} aria-expanded={editing} aria-controls={formId}>
            <Clock /> {t("일정 편집")}
          </Button>
        </div>
        <p className="text-sm text-gray-500">
          {savedCron
            ? t("{cron} · {count}개 보관", { cron: describeCron(savedCron), count: schedule.data?.keep ?? keepN })
            : t("자동 백업 꺼짐")}
        </p>
      </div>

      {editing && (
        <form
          id={formId}
          className="flex flex-col gap-y-4 rounded-lg border border-gray-200 p-4"
          onSubmit={async (e) => {
            e.preventDefault();
            if (cronErr || !dirty) return;
            if (await run("sched", () => api.setBackupSchedule(vol.id, { cron: cron.trim() || null, keep: keepN }))) setEditing(false);
          }}
        >
          <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_140px]">
            <Field label={t("일정 (크론, 비우면 끔)")}>
              <Input mono value={cron} invalid={!!cronErr} onChange={(e) => setCron(e.target.value)} placeholder="0 4 * * *" />
            </Field>
            <Field label={t("보관 개수")}>
              <Input mono inputMode="numeric" value={keep} onChange={(e) => setKeep(e.target.value.replace(/[^0-9]/g, ""))} />
            </Field>
          </div>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className={cx("text-sm", cronErr ? "text-danger" : "text-gray-500")}>
              {cronErr ??
                (cron.trim()
                  ? t("{cron} · 보관 개수를 넘는 오래된 자동 백업은 지웁니다. 직접 만든 백업은 지우지 않습니다.", { cron: describeCron(cron) })
                  : t("자동 백업이 꺼집니다."))}
            </p>
            <div className="flex gap-2">
              <Button variant="ghost" onClick={() => setEditing(false)}>
                {t("취소")}
              </Button>
              <Button type="submit" variant="primary" loading={busy === "sched"} disabled={!!cronErr || !dirty} {...lock(canEdit)}>
                {t("일정 저장")}
              </Button>
            </div>
          </div>
        </form>
      )}

      {error ? <ErrorBox error={error} /> : null}

      {backups.error ? (
        <ErrorBox error={backups.error} />
      ) : backups.isPending ? (
        <div className="flex justify-center pt-8">
          <Spinner />
        </div>
      ) : !list.length ? (
        <NoBackups title={t("백업 없음")}>{t("이 서비스의 볼륨에 백업이 없습니다.")}</NoBackups>
      ) : (
        <ul className="flex flex-col gap-y-2" aria-label={t("{name} 백업 목록", { name: vol.name })}>
          {list.map((b) => (
            <li key={b.id} className="relative select-none rounded-md border border-gray-200">
              <div className="m-1 flex items-center gap-x-4 rounded-md bg-gray-100 px-3 py-4">
                <div className="flex sm:min-w-20">
                  <ToneBadge tone={STATUS_BADGE[b.status].tone}>{STATUS_BADGE[b.status].label}</ToneBadge>
                </div>
                <div className="flex min-w-0 flex-1 flex-col gap-y-1">
                  <p className="truncate text-sm text-foreground">{fmtTime(b.created_at)}</p>
                  <p className="truncate text-sm text-gray-500" title={b.status === "failed" ? (b.error ?? undefined) : undefined}>
                    <time dateTime={b.created_at}>{fmtRelative(b.created_at)}</time>
                    {b.status === "done" && <> · {fmtBytes(b.size_bytes)}</>}
                    {b.status === "failed" && <span className="text-danger"> · {b.error ?? t("알 수 없는 오류")}</span>}
                  </p>
                </div>
                <Menu
                  label={t("백업 작업")}
                  trigger={(p) => (
                    <button
                      type="button"
                      {...p}
                      title={t("백업 작업")}
                      aria-label={t("백업 작업 더 보기")}
                      className="flex shrink-0 cursor-pointer items-center justify-center rounded-md text-foreground hover:text-gray-700 focus:outline-none focus-visible:ring-2 focus-visible:ring-gray-500"
                    >
                      <span className="icon-container icon-md text-2xl text-current">
                        <MoreVertical aria-hidden />
                      </span>
                    </button>
                  )}
                >
                  {(close) => (
                    <>
                      <MenuItem
                        icon={<RotateCcw />}
                        disabled={!canEdit || b.status !== "done" || busy !== null}
                        reason={canEdit ? undefined : NO_PERM}
                        onSelect={() => {
                          close();
                          confirm({
                            title: t("백업에서 복원"),
                            message: t("{time} 백업으로 볼륨을 덮어씁니다. 서비스를 멈추고 복원한 뒤 다시 배포합니다. 그 이후의 데이터는 사라집니다.", {
                              time: fmtTime(b.created_at),
                            }),
                            confirmLabel: t("복원"),
                            danger: true,
                            run: () => run(`restore-${b.id}`, () => api.restoreBackup(b.id)),
                          });
                        }}
                      >
                        {t("복원")}
                      </MenuItem>
                      <MenuItem
                        danger
                        icon={<Trash2 />}
                        disabled={!canEdit || b.status === "running"}
                        reason={canEdit ? undefined : NO_PERM}
                        onSelect={() => {
                          close();
                          confirm({
                            title: t("백업 삭제"),
                            message: t("{time} 백업 파일을 지웁니다. 되돌릴 수 없습니다.", { time: fmtTime(b.created_at) }),
                            confirmLabel: t("삭제"),
                            danger: true,
                            run: () => run(`del-${b.id}`, () => api.deleteBackup(b.id)),
                          });
                        }}
                      >
                        {t("삭제")}
                      </MenuItem>
                    </>
                  )}
                </Menu>
              </div>
            </li>
          ))}
        </ul>
      )}
      {node}
    </section>
  );
}
