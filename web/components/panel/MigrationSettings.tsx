"use client";

// v1.3 설정 UI: 도메인 고급 설정(경로 규칙, TLS 모드, 리다이렉트, basic auth), 자원 제한, SSH deploy key, 빌드 비밀, DB 논리 백업.
// SettingsTab에서 작은 조각으로 끼워 쓴다.

import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive, Download, KeyRound, Plus, RotateCcw, ShieldCheck, Trash2 } from "lucide-react";
import {
  api,
  type BackupDestination,
  type CanvasService,
  type DbBackup,
  type Environment,
  type ServiceInstance,
} from "@/lib/api";
import { useCanvas, useCaps } from "@/lib/queries";
import { lock } from "../Perm";
import { cronError, describeCron } from "@/lib/cron";
import { fmtBytes, fmtRelative } from "@/lib/format";
import { t, tr } from "@/lib/i18n";
import { Button, CopyField, ErrorBox, IconButton, Input, LABEL_CLS, Notice, Select, Spinner, Toggle, cx, useConfirm } from "../ui";
import { FIELD } from "./settings/primitives";

/* ── 도메인 고급 설정 ─────────────────────────────────────────────────── */

// Moved to the networking folder with the rest of the domain row (P7); re-exported so existing imports keep working.
export { DomainAdvanced } from "./networking/DomainRow";

/* ── 자원 제한 ───────────────────────────────────────────────────────── */

export function ResourceLimits({
  draft,
  setMemory,
  setCpu,
}: {
  draft: ServiceInstance;
  setMemory: (v: number | null) => void;
  setCpu: (v: number | null) => void;
}) {
  const [mem, setMem] = useState(draft.memory_limit_mb ? String(draft.memory_limit_mb) : "");
  const [cpu, setCpuText] = useState(draft.cpu_limit ? String(draft.cpu_limit) : "");
  // Re-sync when the staged value changes from outside (discarded or deployed changes).
  useEffect(() => {
    setMem((prev) => ((prev ? Number(prev) : null) === (draft.memory_limit_mb ?? null) ? prev : draft.memory_limit_mb ? String(draft.memory_limit_mb) : ""));
  }, [draft.memory_limit_mb]);
  useEffect(() => {
    setCpuText((prev) => ((Number(prev) > 0 ? Number(prev) : null) === (draft.cpu_limit ?? null) ? prev : draft.cpu_limit ? String(draft.cpu_limit) : ""));
  }, [draft.cpu_limit]);
  const memLow = !!mem && Number(mem) < 6;
  return (
    <div className="grid max-w-[420px] grid-cols-2 gap-2">
      <label className="block">
        <span className={LABEL_CLS}>{t("메모리 (MiB)")}</span>
        <Input
          size="sm"
          mono
          inputMode="numeric"
          className="px-3 tabular-nums"
          invalid={memLow}
          value={mem}
          placeholder={t("제한 없음")}
          aria-label={t("메모리 제한 (MiB)")}
          onChange={(e) => {
            const v = e.target.value.replace(/[^0-9]/g, "");
            setMem(v);
            setMemory(v ? Number(v) : null);
          }}
        />
      </label>
      <label className="block">
        <span className={LABEL_CLS}>{t("CPU (코어)")}</span>
        <Input
          size="sm"
          mono
          inputMode="decimal"
          className="px-3 tabular-nums"
          value={cpu}
          placeholder={t("제한 없음")}
          aria-label={t("CPU 제한 (코어)")}
          onChange={(e) => {
            const v = e.target.value.replace(/[^0-9.]/g, "");
            setCpuText(v);
            const n = Number(v);
            setCpu(v && Number.isFinite(n) && n > 0 ? n : null);
          }}
        />
      </label>
      {memLow && <p className="col-span-2 text-xs leading-[18px] text-red-600">{t("메모리는 6MiB 이상이어야 컨테이너가 시작됩니다.")}</p>}
    </div>
  );
}

/* ── SSH deploy key ──────────────────────────────────────────────────── */

export function DeployKeyPanel({ svc }: { svc: CanvasService }) {
  const qc = useQueryClient();
  const key = useQuery({
    queryKey: ["deploy-key", svc.id],
    queryFn: async () => {
      try {
        return await api.deployKey(svc.id);
      } catch {
        return null;
      }
    },
  });
  const { confirm, node } = useConfirm();
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const run = async (k: string, fn: () => Promise<unknown>) => {
    setBusy(k);
    setError(null);
    try {
      await fn();
      await qc.invalidateQueries({ queryKey: ["deploy-key", svc.id] });
    } catch (e) {
      setError(e);
    } finally {
      setBusy(null);
    }
  };
  if (key.isPending) return <Spinner />;
  return (
    <div className="space-y-2">
      {key.data ? (
        <>
          <CopyField value={key.data.public_key} label={t("공개키")} />
          <div className="text-xs leading-[18px] text-gray-600">
            {tr("지문 {fingerprint} · GitHub 저장소의 Settings → Deploy keys에 이 공개키를 추가하세요(읽기 전용). 저장소 URL은 {url} 형식을 쓰세요.", {
              fingerprint: <span className="font-mono">{key.data.fingerprint}</span>,
              url: <span className="font-mono">{t("git@github.com:소유자/저장소.git")}</span>,
            })}
          </div>
          <div className="flex gap-2">
            <Button
              variant="outline"
              onClick={() =>
                confirm({
                  title: t("Deploy key 교체"),
                  message: t("새 키를 만들고 이전 키는 폐기합니다. GitHub에 새 공개키를 등록하기 전까지 clone이 실패합니다."),
                  confirmLabel: t("교체"),
                  danger: true,
                  run: () => run("rotate", () => api.createDeployKey(svc.id, true)),
                })
              }
              loading={busy === "rotate"}
            >
              <RotateCcw /> {t("교체")}
            </Button>
            <Button
              variant="outline"
              onClick={() =>
                confirm({
                  title: t("Deploy key 삭제"),
                  message: t("키를 지웁니다. SSH(git@…) 저장소는 새 키를 만들어 GitHub에 등록하기 전까지 clone이 실패합니다."),
                  confirmLabel: t("삭제"),
                  danger: true,
                  run: () => run("del", () => api.deleteDeployKey(svc.id)),
                })
              }
              loading={busy === "del"}
            >
              <Trash2 /> {t("삭제")}
            </Button>
          </div>
        </>
      ) : (
        <Button variant="outline" onClick={() => run("create", () => api.createDeployKey(svc.id))} loading={busy === "create"}>
          <KeyRound /> {t("Deploy key 생성")}
        </Button>
      )}
      {error ? <ErrorBox error={error} /> : null}
      {node}
    </div>
  );
}

/* ── 빌드 비밀 ───────────────────────────────────────────────────────── */

export function BuildSecretsEditor({ value, variableNames, onChange }: { value: string[]; variableNames: string[]; onChange: (v: string[]) => void }) {
  const toggle = (n: string) => onChange(value.includes(n) ? value.filter((x) => x !== n) : [...value, n].sort());
  const names = Array.from(new Set([...variableNames, ...value])).sort();
  if (!names.length) return <div className="text-sm text-gray-600">{t("선택할 변수가 없습니다. 변수 탭에서 먼저 추가하세요.")}</div>;
  return (
    <div className="flex flex-wrap gap-1.5">
      {names.map((n) => (
        <button
          key={n}
          type="button"
          onClick={() => toggle(n)}
          aria-pressed={value.includes(n)}
          className={cx(
            "inline-flex cursor-pointer items-center gap-1 rounded border px-2 py-1 font-mono text-xs",
            value.includes(n) ? "border-pink-500 bg-pink-500/15 text-foreground" : "border-gray-200 text-gray-600 hover:text-foreground",
          )}
        >
          {value.includes(n) && <ShieldCheck className="size-3" />}
          {n}
        </button>
      ))}
    </div>
  );
}

/* ── DB 논리 백업 ────────────────────────────────────────────────────── */

const DESTS = ["db-destinations"] as const;

function DestinationsEditor({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const [f, setF] = useState({ name: "", endpoint: "", region: "us-east-1", bucket: "", prefix: "", access_key: "", secret_key: "" });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const set = (k: keyof typeof f) => (e: React.ChangeEvent<HTMLInputElement>) => setF((p) => ({ ...p, [k]: e.target.value }));
  return (
    <form
      className="space-y-3 rounded-lg border border-gray-200 bg-secondaryBg p-4"
      onSubmit={async (e) => {
        e.preventDefault();
        setBusy(true);
        setError(null);
        try {
          const d = await api.createBackupDestination({ type: "s3", ...f });
          const res = await api.testBackupDestination(d.id);
          if (!res.ok) {
            await api.deleteBackupDestination(d.id);
            throw new Error(t("접속 확인 실패: {error}", { error: res.error ?? t("알 수 없는 오류") }));
          }
          await qc.invalidateQueries({ queryKey: DESTS });
          onClose();
        } catch (err) {
          setError(err);
        } finally {
          setBusy(false);
        }
      }}
    >
      <div className="text-sm font-semibold">{t("S3 호환 저장소 추가")}</div>
      <div className="grid grid-cols-2 gap-2">
        <Input size="sm" className={FIELD} value={f.name} onChange={set("name")} placeholder={t("이름")} aria-label={t("이름")} />
        <Input size="sm" className={FIELD} value={f.endpoint} onChange={set("endpoint")} placeholder="https://s3.example.com" aria-label={t("엔드포인트")} />
        <Input size="sm" className={FIELD} value={f.region} onChange={set("region")} placeholder={t("리전")} aria-label={t("리전")} />
        <Input size="sm" className={FIELD} value={f.bucket} onChange={set("bucket")} placeholder={t("버킷")} aria-label={t("버킷")} />
        <Input size="sm" className={FIELD} value={f.prefix} onChange={set("prefix")} placeholder={t("경로 접두어(선택)")} aria-label={t("접두어")} />
        <Input size="sm" className={FIELD} value={f.access_key} onChange={set("access_key")} placeholder="Access key" aria-label="Access key" autoComplete="off" />
        <Input size="sm" type="password" value={f.secret_key} onChange={set("secret_key")} placeholder="Secret key" aria-label="Secret key" autoComplete="new-password" className={cx(FIELD, "col-span-2")} />
      </div>
      <div className="text-xs leading-[18px] text-gray-600">{t("Secret key는 서버 마스터 키로 암호화해 저장하고 다시 보여주지 않습니다.")}</div>
      {error ? <ErrorBox error={error} /> : null}
      <div className="flex justify-end gap-2">
        <Button variant="outline" onClick={onClose}>
          {t("취소")}
        </Button>
        <Button type="submit" variant="primary" loading={busy} disabled={!f.name || !f.endpoint || !f.bucket || !f.access_key || !f.secret_key}>
          {t("추가")}
        </Button>
      </div>
    </form>
  );
}

export function DbBackups({ svc, env }: { svc: CanvasService; env: Environment }) {
  const qc = useQueryClient();
  const { confirm, node } = useConfirm();
  const backups = useQuery({
    queryKey: ["db-backups", svc.id, env.id],
    queryFn: () => api.dbBackups(svc.id, env.id),
    refetchInterval: (q) => (q.state.data?.some((b) => b.status === "running") ? 2000 : 15000),
  });
  const cfg = useQuery({ queryKey: ["db-backup-config", svc.id, env.id], queryFn: () => api.dbBackupConfig(svc.id, env.id) });
  const dests = useQuery({ queryKey: DESTS, queryFn: api.backupDestinations });
  const wsAdmin = useCaps(svc.project_id).instanceAdmin; // 백업 저장소는 작업 공간 admin
  const canvas = useCanvas(env.id);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [addDest, setAddDest] = useState(false);
  const [cron, setCron] = useState<string | null>(null);
  const [keep, setKeep] = useState<string | null>(null);
  const [dest, setDest] = useState<string | null>(null);
  const c = cfg.data;
  const cronText = cron ?? c?.cron ?? "";
  const keepText = keep ?? String(c?.retention ?? 7);
  const destId = dest ?? c?.destination_id ?? "";
  const dbPeers = (canvas.data?.services ?? []).filter((s) => s.source_type === "database" && s.database_kind === svc.database_kind);
  const destName = (id: string) => (id ? (dests.data ?? []).find((d: BackupDestination) => d.id === id)?.name ?? id : t("로컬"));

  const run = async (key: string, fn: () => Promise<unknown>) => {
    setBusy(key);
    setError(null);
    try {
      await fn();
      await qc.invalidateQueries({ queryKey: ["db-backups", svc.id, env.id] });
      await qc.invalidateQueries({ queryKey: ["db-backup-config", svc.id, env.id] });
      return true;
    } catch (e) {
      setError(e);
      return false;
    } finally {
      setBusy(null);
    }
  };

  const restore = (b: DbBackup, targetId: string) => {
    const target = dbPeers.find((s) => s.id === targetId) ?? svc;
    return confirm({
      title: t("DB 복원"),
      message: (
        <>
          {tr("{target}의 현재 데이터를 백업 {file}의 내용으로 {overwrite}.", {
            target: <span className="font-mono text-foreground">{target.name}</span>,
            file: <span className="font-mono text-foreground">{b.file_name}</span>,
            overwrite: <b>{t("덮어씁니다")}</b>,
          })}
          {svc.database_kind === "redis" && ` ${t("복원하는 동안 Redis가 잠시 멈춥니다.")}`}
        </>
      ),
      confirmLabel: t("복원"),
      danger: true,
      run: () =>
        run("restore", async () => {
          await api.restoreDbBackup(b.id, target.id === svc.id ? {} : { service_id: target.id });
          setNotice(t("복원을 시작했습니다. 이벤트에서 완료 여부를 확인하세요."));
          for (let i = 0; i < 600; i++) {
            await new Promise((r) => setTimeout(r, 2000));
            const s = await api.dbRestoreStatus(b.id);
            if (s.status === "done") return setNotice(t("{name} 복원이 끝났습니다.", { name: target.name }));
            if (s.status === "failed") throw new Error(s.error || t("복원에 실패했습니다."));
          }
        }),
    });
  };

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <Select value={destId} onChange={(e) => setDest(e.target.value)} aria-label={t("저장 위치")} size="sm" className="w-[200px]">
          <option value="">{t("로컬 (AVEN_DATA)")}</option>
          {(dests.data ?? []).filter((d) => d.type === "s3").map((d) => (
            <option key={d.id} value={d.id}>
              S3 · {d.name}
            </option>
          ))}
        </Select>
        <Button variant="outline" onClick={() => setAddDest((a) => !a)} {...lock(wsAdmin)}>
          <Plus /> {t("S3 저장소")}
        </Button>
        <Button className="ml-auto" loading={busy === "now"} onClick={() => run("now", () => api.createDbBackup(svc.id, env.id, destId))}>
          <Archive /> {t("지금 백업")}
        </Button>
      </div>
      {addDest && <DestinationsEditor onClose={() => setAddDest(false)} />}

      <div className="flex flex-wrap items-end gap-2">
        <label className="min-w-[180px] flex-1">
          <span className={LABEL_CLS}>{t("정기 백업 (크론)")}</span>
          <span className="flex items-center gap-3">
            <Toggle label={t("정기 백업")} checked={!!c?.enabled} onChange={(v) => run("cfg", () => api.setDbBackupConfig(svc.id, env.id, { enabled: v, cron: cronText, retention: Number(keepText) || 7, destination_id: destId }))} />
            <Input size="sm" mono className={FIELD} invalid={!!cronText.trim() && !!cronError(cronText)} value={cronText} onChange={(e) => setCron(e.target.value)} placeholder="0 3 * * *" aria-label={t("백업 cron")} />
          </span>
        </label>
        <label className="w-[90px]">
          <span className={LABEL_CLS}>{t("보관 개수")}</span>
          <Input size="sm" mono className={cx(FIELD, "tabular-nums")} inputMode="numeric" value={keepText} onChange={(e) => setKeep(e.target.value.replace(/[^0-9]/g, ""))} aria-label={t("보관 개수")} />
        </label>
        <Button
          loading={busy === "cfg"}
          disabled={!!cronError(cronText)}
          onClick={() => run("cfg", () => api.setDbBackupConfig(svc.id, env.id, { cron: cronText, retention: Number(keepText) || 7, destination_id: destId, enabled: !!c?.enabled }))}
        >
          {t("설정 저장")}
        </Button>
        <div className="basis-full text-xs leading-[18px] text-gray-600">{!cronText.trim() ? "" : cronError(cronText) ? <span className="text-red-600">{cronError(cronText)}</span> : describeCron(cronText)}</div>
      </div>

      {backups.isPending ? (
        <Spinner />
      ) : !backups.data?.length ? (
        <div className="rounded-lg border border-dashed border-gray-200 px-4 py-3 text-sm text-gray-600">{tr("아직 DB 백업이 없습니다. {kind}를 만들어 두면 다른 DB 서비스에도 복원할 수 있습니다.", { kind: svc.database_kind === "redis" ? "RDB" : t("논리 덤프") })}</div>
      ) : (
        <ul className="divide-y divide-gray-200 rounded-lg border border-gray-200 bg-secondaryBg">
          {backups.data.map((b) => (
            <li key={b.id} className="flex flex-wrap items-center gap-3 px-3 py-2 text-sm">
              <span className={cx("size-2 shrink-0 rounded-full", b.status === "done" ? "bg-green-600" : b.status === "running" ? "animate-soft-pulse bg-yellow-600" : "bg-red-600")} />
              <span className="font-mono text-xs text-foreground">{b.file_name || b.id}</span>
              <span className="tabular-nums text-gray-500">{b.status === "done" ? fmtBytes(b.size_bytes) : b.status === "running" ? t("진행 중") : t("실패")}</span>
              <span className="tabular-nums text-gray-500">{destName(b.destination_id)} · {b.trigger === "schedule" ? t("정기") : t("수동")} · {fmtRelative(b.created_at)}</span>
              {b.error && <span className="min-w-0 truncate text-danger" title={b.error}>{b.error}</span>}
              <span className="ml-auto flex items-center gap-1">
                {b.status === "done" && (
                  <>
                    {dbPeers.length > 1 && (
                      <Select
                        defaultValue=""
                        aria-label={t("복원 대상")}
                        size="sm" className="w-[150px]"
                        onChange={(e) => {
                          if (e.target.value) restore(b, e.target.value);
                          e.target.value = "";
                        }}
                      >
                        <option value="">{t("다른 DB로 복원…")}</option>
                        {dbPeers.filter((s) => s.id !== svc.id).map((s) => (
                          <option key={s.id} value={s.id}>
                            {s.name}
                          </option>
                        ))}
                      </Select>
                    )}
                    <Button size="sm" variant="outline" onClick={() => restore(b, svc.id)} disabled={busy === "restore"}>
                      <RotateCcw className="size-3.5" /> {t("복원")}
                    </Button>
                    <a href={api.dbBackupDownloadUrl(b.id)} aria-label={t("다운로드")} title={t("다운로드")} className="inline-flex size-[26px] items-center justify-center rounded-md text-gray-500 hover:bg-gray-100 hover:text-foreground">
                      <Download className="size-3.5" />
                    </a>
                  </>
                )}
                {b.status !== "running" && (
                  <IconButton
                    label={t("백업 삭제")}
                    size="sm"
                    onClick={() =>
                      confirm({
                        title: t("백업 삭제"),
                        message: t("백업 기록과 저장소의 파일을 지웁니다."),
                        confirmLabel: t("삭제"),
                        danger: true,
                        run: () => run("del", () => api.deleteDbBackup(b.id)),
                      })
                    }
                  >
                    <Trash2 className="size-3.5" />
                  </IconButton>
                )}
              </span>
            </li>
          ))}
        </ul>
      )}
      {notice && <Notice onDismiss={() => setNotice(null)}>{notice}</Notice>}
      {error ? <ErrorBox error={error} /> : null}
      {node}
    </div>
  );
}
