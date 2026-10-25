"use client";

import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Archive, Check, ChevronDown, Clock, Cloud, Code, Flag, Flame, Globe, Hammer, HardDrive, Network, Plus, RotateCcw, Search, Server, Trash2, X, Zap, ServerCog } from "lucide-react";
import {
  api,
  type Builder,
  type CanvasService,
  type Domain,
  type Environment,
  type HealthcheckType,
  type RestartPolicy,
  type ServiceInstance,
  type Volume,
} from "@/lib/api";
import { qk, useBackupSchedule, useBackups, useCaps, useEnvVolumes, useNodes, useRegistries } from "@/lib/queries";
import { t, tr } from "@/lib/i18n";
import { PermFieldset } from "../Perm";
import { imageHost } from "../nodes/RegistriesView";
import { NODE_STATUS } from "../nodes/NodesView";
import { cronError, describeCron, nextRuns } from "@/lib/cron";
import { fmtBytes, fmtRelative, fmtTime } from "@/lib/format";
import { useStaged, type SettingsPatch } from "@/lib/staged";
import { useKeyboardShortcuts } from "@/components/KeyboardShortcuts";
import type { ServiceActions } from "@/lib/useServiceActions";
import { PublicNetworking } from "./networking/PublicNetworking";
import { PrivateNetworking } from "./networking/PrivateNetworking";
import { OutboundIPv6, StaticOutboundIPs } from "./networking/OutboundIPv6";
import { useNetworkingView } from "./networking/useNetworkingView";
import { EdgeSettings } from "./EdgeSettings";
import { BuildSecretsEditor, DbBackups, DeployKeyPanel } from "./MigrationSettings";
import { ImageUpdateSettings } from "./ImageUpdateSettings";
import { AddButton, CARD, Changed, FIELD, Hint, PrimaryNotice, SettingItem as Item, ToggleCard } from "./settings/primitives";
import { ServerConfigSettings } from "./settings/ServerConfig";
import { ReplicaLimits } from "./settings/ReplicaLimits";
import { ImageSourceCard, RepoSourceCard, BranchCard, SourceRow } from "./settings/SourceCard";
import { DangerZone } from "./settings/DangerZone";
import { useScrollSpy, useSettingsFilter } from "./settings/nav";
import { Button, ErrorBox, IconButton, Input, LABEL_CLS, Menu, MenuItem, Select, Spinner, Textarea, Toggle, cx, useConfirm } from "../ui";

type SectionId = "server" | "source" | "networking" | "edge" | "scaling" | "build" | "deploy" | "volumes" | "flags" | "danger";

const SECTIONS: { id: SectionId; title: string; icon: ReactNode; keywords: string }[] = [
  // aven-server 자신(self 서비스)에만 있다: env 파일의 서버 설정.
  { id: "server", title: t("서버 설정"), icon: <ServerCog />, keywords: "server config env keel_" },
  { id: "source", title: t("소스"), icon: <Code />, keywords: "source" },
  { id: "networking", title: t("네트워킹"), icon: <Network />, keywords: "networking network" },
  { id: "edge", title: t("엣지"), icon: <Zap />, keywords: "edge" },
  { id: "scaling", title: t("확장"), icon: <Globe />, keywords: "scale scaling" },
  { id: "build", title: t("빌드"), icon: <Hammer />, keywords: "build" },
  { id: "deploy", title: t("배포"), icon: <Cloud />, keywords: "deploy" },
  { id: "volumes", title: t("볼륨|plural"), icon: <HardDrive />, keywords: "volume volumes storage" },
  { id: "flags", title: t("기능 플래그"), icon: <Flag />, keywords: "feature flags" },
  { id: "danger", title: t("위험"), icon: <Flame />, keywords: "danger" },
];

/**
 * Settings group, modeled on Railway's dashboard: anchor (`absolute -top-24`), timeline icon circle (40px, `left-[-22px] top-[-6px]`) on the rail,
 * group title (`text-h4 text-gray-600 font-medium mb-8 sm:pl-10`, none for Danger) and the item grid (`grid gap-8 max-w-xl sm:pl-10`).
 */
function Section({ id, danger, wide, children }: { id: SectionId; danger?: boolean; wide?: boolean; children: ReactNode }) {
  const meta = SECTIONS.find((s) => s.id === id)!;
  return (
    <div data-settings-section={id} data-title={meta.title} data-keywords={meta.keywords} role="region" aria-label={meta.title} className="relative scroll-mt-28">
      <div id={`settings-${id}`} className="absolute -top-24" aria-hidden="true" />
      <div
        aria-hidden
        className={cx(
          "icon-container icon-40 absolute left-[-22px] top-[-6px] !hidden rounded-full border bg-background p-[10px] text-2xl sm:!flex dark:bg-secondaryBg [&_svg]:!size-[18px]",
          danger ? "border-red-200 text-red-500" : "border-gray-200 text-gray-500",
        )}
      >
        {meta.icon}
      </div>
      {!danger && <h2 className="text-h4 mb-8 font-medium text-gray-600 sm:pl-10">{meta.title}</h2>}
      <div className={cx("grid gap-8 sm:pl-10 [&>*]:min-w-0", wide ? "max-w-3xl" : "max-w-xl")}>{children}</div>
    </div>
  );
}

/**
 * Start command, modeled on Railway's dashboard: an outline add button until the setting has a value (or the button was pressed),
 * then the control itself.
 */
function Reveal({ has, label, children }: { has: boolean; label: string; children: (autoFocus: boolean) => ReactNode }) {
  const [open, setOpen] = useState(false);
  if (!has && !open) return <AddButton onClick={() => setOpen(true)}>{label}</AddButton>;
  return <>{children(open)}</>;
}

const strOrNull = (s: string) => (s.trim() ? s : null);


/* ── service name ────────────────────────────────────────────────────── */
function ServiceName({ svc, env }: { svc: CanvasService; env: Environment }) {
  const qc = useQueryClient();
  const [name, setName] = useState(svc.name);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => setName(svc.name), [svc.name]);
  const dirty = name.trim() !== svc.name;
  const invalid = !!name.trim() && !/^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$/.test(name.trim());
  return (
    <Item
      label={t("서비스 이름")}
      keywords="name service rename"
      description={
        <>
          {tr("모든 환경에 적용됩니다. 비공개 도메인과 다른 서비스의 {ref} 참조도 바뀌니 함께 고치세요.", {
            ref: <span className="font-mono">{"${{"}{t("이름.변수")}{"}}"}</span>,
          })}
        </>
      }
    >
      <form
        className="flex gap-2"
        onSubmit={async (e) => {
          e.preventDefault();
          if (!dirty || invalid || !name.trim()) return;
          setBusy(true);
          setError(null);
          try {
            await api.updateService(svc.id, { name: name.trim() });
            await qc.invalidateQueries({ queryKey: qk.canvas(env.id) });
            await qc.invalidateQueries({ queryKey: ["project"] });
          } catch (err) {
            setError(err);
          } finally {
            setBusy(false);
          }
        }}
      >
        <Input size="sm" mono className={FIELD} invalid={invalid} value={name} onChange={(e) => setName(e.target.value.toLowerCase())} aria-label={t("서비스 이름")} />
        <Button type="submit" disabled={!dirty || invalid || !name.trim()} loading={busy}>
          {t("저장")}
        </Button>
      </form>
      {invalid && <Hint tone="danger">{t("소문자, 숫자, 하이픈(-)으로 40자까지 쓸 수 있고, 하이픈으로 시작하거나 끝날 수 없습니다.")}</Hint>}
      {error ? <ErrorBox className="mt-2" error={error} /> : null}
    </Item>
  );
}
/* ── networking ──────────────────────────────────────────────────────── */
function useSyncedText<T>(value: T, toText: (v: T) => string, parse: (t: string) => T) {
  const [text, setText] = useState(() => toText(value));
  const key = JSON.stringify(value);
  useEffect(() => {
    setText((cur) => (JSON.stringify(parse(cur)) === key ? cur : toText(value)));
  }, [key]); // eslint-disable-line react-hooks/exhaustive-deps
  return [text, setText] as const;
}
const parsePort = (v: string) => (v ? Number(v) : null);

/**
 * Networking group, modeled on Railway's dashboard: Public networking (domains, TCP proxy), Private Networking (endpoint rename, cross-node
 * ports), Outbound IPv6 and static outbound IPs. The service port stays as keel's last item: the Railway-style layout only has the
 * per-domain target port, but keel's port also drives PORT, healthchecks and the mesh.
 */
function Networking({ svc, env, inst, draft, stage }: { svc: CanvasService; env: Environment; inst: ServiceInstance; draft: ServiceInstance; stage: Stage }) {
  const { view } = useNetworkingView(svc, env, inst);
  const [portText, setPortText] = useSyncedText(draft.port, (v) => (v == null ? "" : String(v)), parsePort);
  const portInvalid = !!portText && (Number(portText) < 1 || Number(portText) > 65535);

  return (
    <>
      <PublicNetworking svc={svc} env={env} inst={inst} view={view} />
      <PrivateNetworking svc={svc} env={env} inst={inst} draft={draft} view={view} stage={stage} />
      <OutboundIPv6 svc={svc} env={env} view={view} />
      <StaticOutboundIPs view={view} />

      <Item label={t("포트")} keywords="port listen" badge={<Changed on={stage.changed("port")} />} description={t("앱이 컨테이너 안에서 listen하는 포트입니다. 비워 두면 PORT 변수(기본 8080)를 씁니다. 도메인마다 다른 포트로 보내려면 도메인의 수정에서 고르세요.")}>
        <Input
          size="sm"
          mono
          inputMode="numeric"
          value={portText}
          placeholder={String(view.effective_port || 8080)}
          invalid={portInvalid}
          onChange={(e) => {
            const txt = e.target.value.replace(/[^0-9]/g, "").slice(0, 5);
            setPortText(txt);
            stage.set("port", parsePort(txt));
          }}
          className="tabular-nums"
          aria-label={t("포트")}
        />
        {portInvalid && <Hint tone="danger">{t("포트는 1부터 65535 사이여야 합니다.")}</Hint>}
      </Item>
    </>
  );
}


/* ── volumes ─────────────────────────────────────────────────────────── */
function Backups({ vol, bare }: { vol: Volume; bare?: boolean }) {
  const qc = useQueryClient();
  const backups = useBackups(vol.id);
  const schedule = useBackupSchedule(vol.id);
  const { confirm, node } = useConfirm();
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [cron, setCron] = useState("");
  const [keep, setKeep] = useState("7");
  const [saved, setSaved] = useState(false);
  const [loaded, setLoaded] = useState(false);
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
  return (
    <div className={bare ? undefined : "border-t border-gray-200 px-4 py-3"}>
      <div className="flex items-center justify-between gap-2">
        <span className={cx("text-sm font-semibold text-gray-600", bare && "sr-only")}>{t("백업")}</span>
        <Button size="sm" className="ml-auto" onClick={() => run("now", () => api.createBackup(vol.id))} loading={busy === "now"}>
          <Archive className="size-3.5" /> {t("지금 백업")}
        </Button>
      </div>
      {backups.isPending ? (
        <div className="flex justify-center py-3">
          <Spinner />
        </div>
      ) : !backups.data?.length ? (
        bare ? (
          <div className="flex flex-col items-center justify-center pt-8">
            <span className="icon-container icon-40 mb-5 text-2xl text-gray-600">
              <Clock />
            </span>
            <p className="text-base font-semibold">{t("백업이 없습니다")}</p>
            <p className="text-base text-gray-600">{t("이 서비스의 볼륨에는 아직 백업이 없습니다.")}</p>
          </div>
        ) : (
          <div className="py-3 text-sm text-gray-500">{t("아직 백업이 없습니다.")}</div>
        )
      ) : (
        <ul className="mt-2 divide-y divide-gray-200 rounded-md border border-gray-200">
          {backups.data.map((b) => (
            <li key={b.id} className="flex items-center gap-3 px-3 py-2 text-sm">
              <span
                className={cx(
                  "size-2 shrink-0 rounded-full",
                  b.status === "done" ? "bg-green-600" : b.status === "running" ? "animate-soft-pulse bg-yellow-600" : "bg-red-600",
                )}
              />
              <span className="w-[120px] shrink-0 tabular-nums text-foreground/90" title={fmtTime(b.created_at)}>
                {fmtRelative(b.created_at)}
              </span>
              <span className="min-w-0 flex-1 truncate tabular-nums text-gray-500" title={b.error ?? undefined}>
                {b.status === "running" ? t("백업 중…") : b.status === "failed" ? <span className="text-danger">{t("실패: {error}", { error: b.error ?? "" })}</span> : fmtBytes(b.size_bytes)}
              </span>
              {b.status === "done" && (
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() =>
                    confirm({
                      title: t("백업에서 복원"),
                      message: t("{time} 백업으로 볼륨을 덮어씁니다. 서비스를 멈추고 복원한 뒤 다시 배포합니다. 그 이후의 데이터는 사라집니다.", { time: fmtTime(b.created_at) }),
                      confirmLabel: t("복원"),
                      danger: true,
                      run: () => run(`restore-${b.id}`, () => api.restoreBackup(b.id)),
                    })
                  }
                >
                  <RotateCcw className="size-3.5" /> {t("복원")}
                </Button>
              )}
              <IconButton
                size="sm"
                label={t("백업 삭제")}
                disabled={b.status === "running"}
                onClick={() =>
                  confirm({ title: t("백업 삭제"), message: t("이 백업 파일을 지웁니다."), confirmLabel: t("삭제"), danger: true, run: () => run(`del-${b.id}`, () => api.deleteBackup(b.id)) })
                }
              >
                <Trash2 className="size-3.5" />
              </IconButton>
            </li>
          ))}
        </ul>
      )}
      <form
        className="mt-3 flex flex-wrap items-end gap-2"
        onSubmit={async (e) => {
          e.preventDefault();
          if (cronErr) return;
          const ok = await run("sched", () => api.setBackupSchedule(vol.id, { cron: cron.trim() || null, keep: Math.max(1, Number(keep) || 7) }));
          setSaved(ok);
        }}
      >
        <label className="min-w-[180px] flex-1">
          <span className={LABEL_CLS}>{t("자동 백업 일정 (크론, 비우면 끔)")}</span>
          <Input size="sm" mono className={FIELD} invalid={!!cronErr} value={cron} onChange={(e) => (setCron(e.target.value), setSaved(false))} placeholder="0 4 * * *" />
        </label>
        <label className="w-[90px]">
          <span className={LABEL_CLS}>{t("보관 개수")}</span>
          <Input size="sm" mono className={cx(FIELD, "tabular-nums")} inputMode="numeric" value={keep} onChange={(e) => (setKeep(e.target.value.replace(/[^0-9]/g, "")), setSaved(false))} />
        </label>
        <Button type="submit" loading={busy === "sched"} disabled={!!cronErr || saved}>
          {saved ? <><Check /> {t("저장됨")}</> : t("일정 저장")}
        </Button>
        <Hint tone={cronErr ? "danger" : undefined} className="!mt-0 basis-full">
          {cronErr ?? (cron.trim() ? describeCron(cron) : t("자동 백업이 꺼져 있습니다."))}
        </Hint>
      </form>
      {error ? <ErrorBox className="mt-2" error={error} /> : null}
      {node}
    </div>
  );
}
export { VolumeBackupsTab } from "./BackupsTab";
/** 크기 상한이 있으면 사용량 막대를 보여준다(90% 이상 경고, 100% 이상 위험). */
function VolumeUsage({ vol }: { vol: Volume }) {
  if (vol.size_limit_mb == null) return null;
  const limitBytes = vol.size_limit_mb * 1024 * 1024;
  const pct = vol.size_bytes === null ? null : (vol.size_bytes / limitBytes) * 100;
  const tone = pct === null ? "bg-gray-400" : pct >= 100 ? "bg-red-600" : pct >= 90 ? "bg-yellow-600" : "bg-pink-500";
  const label =
    pct === null
      ? t("상한 {limit} · 크기 측정 전", { limit: fmtBytes(limitBytes) })
      : pct >= 100
        ? t("{used} / {limit} ({percent}%) · 상한 초과", { used: fmtBytes(vol.size_bytes ?? 0), limit: fmtBytes(limitBytes), percent: Math.round(pct) })
        : pct >= 90
          ? t("{used} / {limit} ({percent}%) · 90% 넘음", { used: fmtBytes(vol.size_bytes ?? 0), limit: fmtBytes(limitBytes), percent: Math.round(pct) })
          : t("{used} / {limit} ({percent}%)", { used: fmtBytes(vol.size_bytes ?? 0), limit: fmtBytes(limitBytes), percent: Math.round(pct) });
  return (
    <div className="border-t border-gray-200 px-4 py-3">
      <div
        role="progressbar"
        aria-label={t("{name} 사용량", { name: vol.name })}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={pct === null ? undefined : Math.min(100, Math.round(pct))}
        aria-valuetext={label}
        className="h-2 overflow-hidden rounded-full bg-gray-100"
      >
        <div className={cx("h-full rounded-full transition-[width]", tone)} style={{ width: `${Math.min(100, pct ?? 0)}%` }} />
      </div>
      <div className={cx("mt-1.5 text-xs tabular-nums", pct !== null && pct >= 100 ? "text-danger" : pct !== null && pct >= 90 ? "text-yellow-600" : "text-gray-500")}>{label}</div>
    </div>
  );
}
function VolumeCard({ vol, env }: { vol: Volume; env: Environment }) {
  const qc = useQueryClient();
  const { confirm, node } = useConfirm();
  const [path, setPath] = useState(vol.mount_path);
  const [limit, setLimit] = useState(vol.size_limit_mb != null ? String(vol.size_limit_mb) : "");
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const limitValue = limit.trim() === "" ? null : Number(limit);
  const limitChanged = limitValue !== (vol.size_limit_mb ?? null);
  const run = async (key: string, fn: () => Promise<unknown>) => {
    setBusy(key);
    setError(null);
    try {
      await fn();
      await qc.invalidateQueries({ queryKey: qk.canvas(env.id) });
      await qc.invalidateQueries({ queryKey: qk.envVolumes(env.id) });
    } catch (e) {
      setError(e);
    } finally {
      setBusy(null);
    }
  };
  return (
    <div className="rounded-lg border border-gray-200">
      <div className="flex items-center gap-3 px-4 py-3">
        <HardDrive className="size-5 shrink-0 text-gray-600" />
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm font-semibold">{vol.name}</div>
          <div className="text-xs tabular-nums text-gray-500">
            {vol.size_bytes !== null ? t("{size} 사용", { size: fmtBytes(vol.size_bytes) }) : t("크기 측정 전 (5분마다 갱신)")} · {t("{time} 생성", { time: fmtRelative(vol.created_at) })}
          </div>
        </div>
        <Button
          size="sm"
          variant="outline"
          loading={busy === "detach"}
          onClick={() =>
            confirm({
              title: t("볼륨 연결 해제"),
              message: t("다음 배포부터 이 서비스에 마운트하지 않습니다. 데이터는 지워지지 않습니다."),
              confirmLabel: t("연결 해제"),
              run: () => run("detach", () => api.updateVolume(vol.id, { service_instance_id: null })),
            })
          }
        >
          {t("연결 해제")}
        </Button>
      </div>
      <VolumeUsage vol={vol} />
      <form
        className="flex items-end gap-2 border-t border-gray-200 px-4 py-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (!path.startsWith("/")) return setError(t("마운트 경로는 /로 시작해야 합니다."));
          run("path", () => api.updateVolume(vol.id, { mount_path: path }));
        }}
      >
        <label className="flex-1">
          <span className={LABEL_CLS}>{t("마운트 경로 (다음 배포부터 적용)")}</span>
          <Input size="sm" mono className={FIELD} value={path} onChange={(e) => setPath(e.target.value)} />
        </label>
        <Button type="submit" disabled={path === vol.mount_path} loading={busy === "path"}>
          {t("저장")}
        </Button>
      </form>
      <form
        className="flex flex-wrap items-end gap-2 border-t border-gray-200 px-4 py-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (limitValue !== null && (!Number.isInteger(limitValue) || limitValue < 1 || limitValue > 16777216))
            return setError(t("크기 상한은 1 이상 16777216 이하의 MB 정수이거나 비워 두어야 합니다."));
          run("limit", () => api.updateVolume(vol.id, { size_limit_mb: limitValue }));
        }}
      >
        <label className="flex-1">
          <span className={LABEL_CLS}>{t("크기 상한 (MB, 비우면 없음)")}</span>
          <Input size="sm" mono className={cx(FIELD, "tabular-nums")} inputMode="numeric" value={limit} placeholder={t("예: 10240")} onChange={(e) => setLimit(e.target.value.replace(/[^0-9]/g, ""))} />
        </label>
        <Button type="submit" disabled={!limitChanged} loading={busy === "limit"}>
          {t("저장")}
        </Button>
        <Hint className="!mt-0 basis-full">{t("알림용 상한입니다. 90%와 100%를 넘으면 이벤트를 남기지만, 쓰기를 막지는 않습니다.")}</Hint>
      </form>
      {error ? <ErrorBox className="mx-4 mb-3" error={error} /> : null}
      <Backups vol={vol} />
      {node}
    </div>
  );
}
function DetachedVolumes({ svc, env }: { svc: CanvasService; env: Environment }) {
  const qc = useQueryClient();
  const vols = useEnvVolumes(env.id);
  const { confirm, node } = useConfirm();
  const [error, setError] = useState<unknown>(null);
  const detached = (vols.data ?? []).filter((v) => !v.service_instance_id);
  if (!detached.length) return null;
  const run = async (fn: () => Promise<unknown>) => {
    setError(null);
    try {
      await fn();
      await qc.invalidateQueries({ queryKey: qk.envVolumes(env.id) });
      await qc.invalidateQueries({ queryKey: qk.canvas(env.id) });
    } catch (e) {
      setError(e);
    }
  };
  return (
    <Item label={t("연결되지 않은 볼륨")} description={t("이 환경에서 어떤 서비스에도 연결되지 않은 볼륨입니다. 연결하면 다음 배포부터 마운트됩니다.")}>
      <ul className="divide-y divide-gray-200 rounded-md border border-gray-200">
        {detached.map((v) => (
          <li key={v.id} className="flex items-center gap-3 px-3 py-2 text-sm">
            <HardDrive className="size-4 shrink-0 text-gray-600" />
            <span className="min-w-0 flex-1 truncate">
              {v.name} <span className="font-mono text-xs text-gray-500">{v.mount_path}</span>
            </span>
            <span className="shrink-0 text-xs tabular-nums text-gray-400">{v.size_bytes === null ? t("측정 전") : fmtBytes(v.size_bytes)}</span>
            <Button size="sm" onClick={() => run(() => api.updateVolume(v.id, { service_instance_id: svc.instance.id }))}>
              {t("연결")}
            </Button>
            <IconButton
              size="sm"
              label={t("{name} 삭제", { name: v.name })}
              onClick={() =>
                confirm({ title: t("볼륨 삭제"), message: t("{name}의 데이터와 백업 목록이 지워집니다.", { name: v.name }), confirmLabel: t("삭제"), danger: true, run: () => run(() => api.deleteVolume(v.id)) })
              }
            >
              <Trash2 className="size-3.5" />
            </IconButton>
          </li>
        ))}
      </ul>
      {error ? <ErrorBox className="mt-2" error={error} /> : null}
      {node}
    </Item>
  );
}
function Volumes({ svc, env }: { svc: CanvasService; env: Environment }) {
  const qc = useQueryClient();
  const [path, setPath] = useState("/data");
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  return (
    <>
      {svc.volumes.map((v) => (
        <div key={v.id} className="max-w-[560px]">
          <VolumeCard vol={v} env={env} />
        </div>
      ))}
      {svc.volumes.length === 0 && <DetachedVolumes svc={svc} env={env} />}
      {svc.volumes.length === 0 && (
        <Item label={t("볼륨 추가")} description={t("컨테이너가 다시 만들어져도 남는 디스크입니다. 다음 배포부터 마운트됩니다.")}>
          <form
            className="flex flex-wrap items-end gap-2"
            onSubmit={async (e) => {
              e.preventDefault();
              if (!path.startsWith("/")) return setError(t("마운트 경로는 /로 시작해야 합니다."));
              setBusy(true);
              setError(null);
              try {
                await api.createVolume(env.id, { service_id: svc.id, mount_path: path, name: name.trim() || undefined });
                await qc.invalidateQueries({ queryKey: qk.canvas(env.id) });
              } catch (err) {
                setError(err);
              } finally {
                setBusy(false);
              }
            }}
          >
            <label className="min-w-[160px] flex-1">
              <span className={LABEL_CLS}>{t("마운트 경로")}</span>
              <Input size="sm" mono className={FIELD} value={path} onChange={(e) => setPath(e.target.value)} placeholder="/data" />
            </label>
            <label className="w-[180px]">
              <span className={LABEL_CLS}>{t("이름 (선택)")}</span>
              <Input size="sm" className={FIELD} value={name} onChange={(e) => setName(e.target.value)} placeholder={`${svc.name}-volume`} />
            </label>
            <Button type="submit" variant="primary" loading={busy} disabled={!path.trim()}>
              <Plus /> {t("볼륨 추가")}
            </Button>
          </form>
          {error ? <ErrorBox className="mt-2" error={error} /> : null}
        </Item>
      )}
    </>
  );
}
function fmtSec(s: number) {
  if (s < 60) return t("{n}초", { n: s });
  if (s < 3600) return t("{n}분", { n: Math.round(s / 6) / 10 });
  return t("{n}시간", { n: Math.round(s / 360) / 10 });
}
export function RegistryHint({ pid, image }: { pid: string; image: string }) {
  const regs = useRegistries(pid);
  const host = imageHost(image);
  if (!host) return null;
  if (regs.isPending || regs.error) return null;
  const hit = regs.data?.find((r) => r.host === host || (host === "docker.io" && ["index.docker.io", "registry-1.docker.io"].includes(r.host)));
  return hit ? (
    <p className="mt-2 flex items-center gap-1.5 text-xs text-success">
      <Check className="size-3.5" />{" "}
      {tr("{host} 레지스트리의 {username} 계정으로 받습니다.", { host: <span className="font-mono">{host}</span>, username: hit.username })}
    </p>
  ) : (
    <p className="mt-2 text-xs text-gray-400">
      {tr("비공개 이미지라면 프로젝트 설정 › 레지스트리에 {host}을 추가하세요.", { host: <span className="font-mono text-gray-500">{host}</span> })}
    </p>
  );
}
/* ── scaling ─────────────────────────────────────────────────────────── */
const numOrNull = (s: string): number | null => {
  const v = Number(s);
  return s.trim() && Number.isFinite(v) && v > 0 ? v : null;
};
type AutoscaleForm = {
  enabled: boolean;
  min: string;
  max: string;
  cpu: string;
  memory: string;
  rps: string;
  up: string;
  down: string;
};
function autoscaleForm(inst: ServiceInstance): AutoscaleForm {
  const a = inst.autoscale;
  const s = (v: number | null | undefined) => (v == null ? "" : String(v));
  return {
    enabled: !!a?.enabled,
    min: s(a?.min_replicas ?? 1),
    max: s(a?.max_replicas ?? Math.max(3, inst.replicas)),
    cpu: a ? s(a.target_cpu_percent) : "70",
    memory: s(a?.target_memory_percent),
    rps: s(a?.target_rps_per_replica),
    up: s(a?.scale_up_cooldown_s ?? 60),
    down: s(a?.scale_down_cooldown_s ?? 300),
  };
}

/**
 * Scale group, modeled on Railway's dashboard: regions and replicas (keel nodes: a node dropdown + replica count per row, more rows = multi-node
 * placement), replica limits (sliders) and keel's autoscaling.
 */
function Scaling({ svc, env, inst, draft, stage }: { svc: CanvasService; env: Environment; inst: ServiceInstance; draft: ServiceInstance; stage: Stage }) {
  const qc = useQueryClient();
  const nodes = useNodes();
  const [form, setForm] = useState<AutoscaleForm>(() => autoscaleForm(inst));
  const [busy, setBusy] = useState<"scale" | "auto" | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => setForm(autoscaleForm(inst)), [inst.autoscale]); // eslint-disable-line react-hooks/exhaustive-deps

  const autoOn = !!inst.autoscale?.enabled;
  const autoDirty = JSON.stringify(form) !== JSON.stringify(autoscaleForm(inst));
  const autoMinMaxErr = !!form.min && !!form.max && Number(form.min) > Number(form.max);
  const live = inst.status === "online" && !!inst.current_deployment_id;
  const current = inst.current_replicas ?? 0;
  const placement = draft.placement ?? [];
  const multi = placement.length > 1;
  const list = nodes.data ?? [];
  const st = inst.autoscale_status;
  const fmtPct = (v: number | null | undefined) => (v == null ? "—" : `${v.toFixed(1)}%`);

  const run = async (kind: "scale" | "auto", fn: () => Promise<unknown>) => {
    setBusy(kind);
    setError(null);
    try {
      await fn();
      await qc.invalidateQueries({ queryKey: qk.canvas(env.id) });
      await qc.invalidateQueries({ queryKey: qk.deployments(svc.id, env.id) });
    } catch (e) {
      setError(e);
    } finally {
      setBusy(null);
    }
  };

  const applyNow = () =>
    run("scale", async () => {
      await api.scale(svc.id, env.id, draft.replicas);
      stage.set("replicas", draft.replicas); // 서버 값이 같아지면 staged 항목이 정리된다
    });

  const saveAutoscale = (enabled: boolean) =>
    run("auto", () => {
      const min = Number(form.min) || 1;
      const body = {
        enabled,
        min_replicas: min,
        max_replicas: Number(form.max) || min,
        target_cpu_percent: numOrNull(form.cpu),
        target_memory_percent: numOrNull(form.memory),
        target_rps_per_replica: numOrNull(form.rps),
        scale_up_cooldown_s: Number(form.up) || 60,
        scale_down_cooldown_s: Number(form.down) || 300,
      };
      return api.updateInstance(svc.id, env.id, { autoscale: body });
    });

  const setPlacement = (next: { node_id: string; replicas: number }[]) => {
    if (next.length <= 1) {
      stage.set("placement", null);
      if (next[0]) {
        stage.set("node_id", next[0].node_id);
        stage.set("replicas", next[0].replicas);
      }
      return;
    }
    stage.set("placement", next);
    stage.set("node_id", next[0].node_id);
    stage.set("replicas", next.reduce((n, p) => n + p.replicas, 0));
  };
  const rows = multi ? placement : [{ node_id: draft.node_id, replicas: draft.replicas }];
  const total = rows.reduce((n, p) => n + p.replicas, 0);
  const setRow = (i: number, next: { node_id: string; replicas: number }) => {
    if (!multi) {
      if (next.node_id !== draft.node_id) stage.set("node_id", next.node_id);
      if (next.replicas !== draft.replicas) stage.set("replicas", next.replicas);
      return;
    }
    setPlacement(rows.map((p, j) => (j === i ? next : p)));
  };
  const candidates = list.filter((n) => !rows.some((p) => p.node_id === n.id) && n.schedulable && n.status === "online");
  const multiBlocked = svc.volumes.length > 0 ? t("볼륨이 붙은 서비스는 여러 노드에 나눌 수 없습니다.") : draft.cron_schedule ? t("cron 서비스는 여러 노드에 나눌 수 없습니다.") : draft.sleep_when_idle ? t("서버리스를 켠 서비스는 여러 노드에 나눌 수 없습니다.") : null;
  const moved = !multi && draft.node_id !== inst.node_id;

  return (
    <>
      <Item
        label={t("노드와 레플리카")}
        keywords="regions replicas node region server placement multi 노드 서버 리전 레플리카 배치 멀티"
        badge={<Changed on={stage.changed("node_id") || stage.changed("replicas") || stage.changed("placement")} />}
        description={t("노드마다 레플리카를 배치해 수평으로 확장합니다.")}
      >
        <div className="flex w-full flex-col space-y-4">
          <div className="flex flex-col space-y-2">
            {nodes.isPending ? (
              <Spinner />
            ) : (
              rows.map((r, i) => (
                <div key={r.node_id + i} className="flex items-center space-x-2">
                  <div className="w-full min-w-0">
                    <Select aria-label={multi ? t("{index}번째 노드", { index: i + 1 }) : t("배포 노드")} value={r.node_id} onChange={(e) => setRow(i, { ...r, node_id: e.target.value })}>
                      {list
                        .filter((n) => n.id === r.node_id || !rows.some((p) => p.node_id === n.id))
                        .map((n) => (
                          <option key={n.id} value={n.id} disabled={(!n.schedulable || n.status !== "online") && n.id !== inst.node_id}>
                            <span className="grid grid-cols-[24px_1fr_auto] items-center gap-2 py-1.5">
                              <Server className="size-5 text-gray-500" />
                              <span className="truncate text-base">
                                {n.name}
                                {n.is_local && <span className="ml-2 text-xs text-gray-500">{t("이 서버")}</span>}
                                {i === 0 && multi && <span className="ml-2 text-xs text-pink-600">{t("주 노드")}</span>}
                              </span>
                              <span className={cx("size-2 rounded-full", NODE_STATUS[n.status].dot)} title={NODE_STATUS[n.status].label} />
                            </span>
                          </option>
                        ))}
                    </Select>
                  </div>
                  <div className="relative flex w-28 shrink-0 items-center">
                    <Input
                      type="number"
                      min={1}
                      max={10}
                      value={String(r.replicas)}
                      aria-label={t("{node} 레플리카", { node: list.find((n) => n.id === r.node_id)?.name ?? t("노드") })}
                      onChange={(e) => {
                        const v = Math.max(1, Math.min(10 - (total - r.replicas), Number(e.target.value.replace(/[^0-9]/g, "")) || 1));
                        setRow(i, { ...r, replicas: v });
                      }}
                      className="pr-[64px] tabular-nums"
                    />
                    <div className="pointer-events-none absolute inset-y-0 right-0 z-10 flex items-center pr-2 text-sm text-gray-500">{t("레플리카")}</div>
                  </div>
                  {multi && (
                    <IconButton label={t("{node} 빼기", { node: list.find((n) => n.id === r.node_id)?.name ?? t("노드") })} onClick={() => setPlacement(rows.filter((_, j) => j !== i))}>
                      <Trash2 />
                    </IconButton>
                  )}
                </div>
              ))
            )}
          </div>
          {!multiBlocked && candidates.length > 0 && total < 10 && (
            <Menu
              align="start"
              label={t("노드 추가")}
              trigger={(p) => (
                <button {...p} type="button" className="group grid w-max cursor-pointer grid-cols-[min-content_auto] gap-1 text-sm text-gray-500">
                  <span className="icon-container icon-sm mt-0.5 text-lg group-hover:text-pink-600">
                    <Plus />
                  </span>
                  <span className="underline group-hover:text-pink-600">{t("다른 노드에도 배치")}</span>
                </button>
              )}
            >
              {(close) =>
                candidates.map((n) => (
                  <MenuItem
                    key={n.id}
                    icon={<Server />}
                    onSelect={() => {
                      close();
                      setPlacement([...rows, { node_id: n.id, replicas: 1 }]);
                    }}
                  >
                    {n.name}
                  </MenuItem>
                ))
              }
            </Menu>
          )}
        </div>
        {(multiBlocked || (multi && !multiBlocked)) && (
          <div className="mt-3">
            <PrimaryNotice className="mb-3">
              <p>{multiBlocked ? (svc.volumes.length > 0 && draft.replicas > 1 ? t("{message} 레플리카는 같은 디스크를 공유합니다.", { message: multiBlocked }) : multiBlocked) : t("노드마다 같은 커밋으로 빌드하고, 모든 노드가 헬스체크를 통과해야 배포가 끝납니다. 도메인 DNS에 모든 노드의 주소를 등록하세요. TCP 프록시는 주 노드에서만 열립니다.")}</p>
            </PrimaryNotice>
          </div>
        )}
        {moved && (
          <div className="mt-3 space-y-1 rounded-md border border-yellow-200 bg-yellow-50 px-3 py-2 text-sm text-yellow-600">
            {svc.volumes.length > 0 && (
              <p>
                {tr("볼륨 {volumes}의 데이터는 자동으로 이전되지 않습니다. 현재 볼륨이 연결된 서비스의 노드 변경은 지원하지 않습니다. 먼저 백업을 만들고 데이터 이전 방법을 확인하세요.", { volumes: svc.volumes.map((v) => v.name).join(", ") })}
              </p>
            )}
            <p className="text-xs text-yellow-700">{t("다음 배포부터 새 노드에서 빌드하고 실행합니다. 다른 노드의 서비스와는 비공개 네트워크로 연결되지 않습니다.")}</p>
          </div>
        )}
        <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2 text-sm text-gray-600">
          <span>
            {tr("실행 중 {count}개", { count: <span className="font-mono tabular-nums text-foreground">{current}</span> })}
            {autoOn && st?.desired != null && <> · {tr("자동 확장 목표 {count}개", { count: <span className="font-mono tabular-nums">{st.desired}</span> })}</>}
          </span>
          {live && !autoOn && !multi && draft.replicas !== current && (
            <Button size="sm" variant="outline" loading={busy === "scale"} onClick={applyNow}>
              {t("재배포 없이 지금 적용")}
            </Button>
          )}
        </div>
      </Item>

      <Item
        label={t("레플리카 제한")}
        keywords="replica limits resource limits memory cpu vcpu 자원 제한 메모리 코어"
        badge={<Changed on={stage.changed("memory_limit_mb") || stage.changed("cpu_limit")} />}
        description={t("레플리카마다 쓸 수 있는 최대 vCPU와 메모리를 정합니다. 노드 한도까지 올리면 제한하지 않습니다.")}
      >
        <ReplicaLimits draft={draft} setMemory={(v) => stage.set("memory_limit_mb", v)} setCpu={(v) => stage.set("cpu_limit", v)} />
      </Item>

      <Item
        label={t("자동 확장")}
        keywords="autoscale autoscaling hpa cpu memory rps 메모리"
        description={t("CPU·메모리·요청 수가 목표를 넘으면 레플리카를 늘리고, 줄어들면 천천히 줄입니다(30초마다 평가). 저장하면 바로 적용됩니다.")}
      >
        <ToggleCard
          checked={form.enabled}
          label={t("자동 확장 사용")}
          disabled={busy === "auto" || !!draft.cron_schedule}
          onChange={(v) => {
            setForm({ ...form, enabled: v });
            if (!v && autoOn) saveAutoscale(false);
          }}
        />
        {draft.cron_schedule && <Hint>{t("cron 서비스는 자동 확장하지 않습니다.")}</Hint>}
        {svc.volumes.length > 0 && form.enabled && <Hint tone="warn">{t("볼륨이 붙은 서비스는 자동 확장하지 않습니다(설정은 저장되지만 평가를 건너뜁니다).")}</Hint>}
        {autoOn && st && (
          <div className="mt-3 space-y-1 rounded-md border border-gray-200 px-3 py-2 text-xs tabular-nums text-gray-600">
            <div className="flex flex-wrap gap-x-5">
              <span>CPU {fmtPct(st.metrics.cpu)}</span>
              <span>{t("메모리")} {fmtPct(st.metrics.memory)}</span>
              <span>RPS {st.metrics.rps == null ? "—" : st.metrics.rps.toFixed(2)}</span>
              <span className="text-gray-500">{t("(레플리카당, 최근 2분 평균)")}</span>
            </div>
            {st.reason && <div>{st.reason}</div>}
            {st.last_scale_at && (
              <div className="text-gray-500">
                {t("마지막 변경 {time}: {reason}", { time: fmtRelative(st.last_scale_at), reason: st.last_reason ?? "" })}
              </div>
            )}
          </div>
        )}
        {form.enabled && (
          <div className="mt-4 grid grid-cols-2 gap-3 sm:grid-cols-4">
            {(
              [
                ["min", t("최소"), t("개")],
                ["max", t("최대"), t("개")],
                ["cpu", t("CPU 목표"), "%"],
                ["memory", t("메모리 목표"), "%"],
                ["rps", t("RPS 목표"), t("/레플리카")],
                ["up", t("확장 대기"), t("초")],
                ["down", t("축소 대기"), t("초")],
              ] as [keyof AutoscaleForm, string, string][]
            ).map(([k, label, unit]) => (
              <label key={k} className="block">
                <span className={LABEL_CLS}>{label}</span>
                <span className="flex items-center gap-1.5">
                  <Input
                    size="sm"
                    mono
                    inputMode="decimal"
                    className={cx(FIELD, "w-full tabular-nums")}
                    invalid={(k === "min" || k === "max") && autoMinMaxErr}
                    value={form[k] as string}
                    placeholder={k === "memory" || k === "rps" ? t("없음") : undefined}
                    onChange={(e) => setForm({ ...form, [k]: e.target.value.replace(/[^0-9.]/g, "") })}
                    aria-label={t("자동 확장 {label}", { label })}
                  />
                  <span className="shrink-0 text-xs text-gray-500">{unit}</span>
                </span>
              </label>
            ))}
          </div>
        )}
        {form.enabled && (
          <>
            <Hint>{t("CPU는 코어 1개(또는 CPU 제한) 대비, 메모리는 메모리 상한 대비입니다.")}</Hint>
            {autoMinMaxErr && <Hint tone="danger">{t("최소 레플리카가 최대보다 클 수 없습니다.")}</Hint>}
            <div className="mt-3 flex items-center gap-2">
              <Button variant="primary" loading={busy === "auto"} disabled={!!autoMinMaxErr || (autoOn && !autoDirty)} onClick={() => saveAutoscale(true)}>
                {autoOn ? t("저장") : t("자동 확장 켜기")}
              </Button>
              {autoOn && autoDirty && (
                <Button variant="outline" onClick={() => setForm(autoscaleForm(inst))}>
                  {t("되돌리기")}
                </Button>
              )}
            </div>
          </>
        )}
      </Item>
      {error != null && <ErrorBox error={error} />}
    </>
  );
}


/* ── staging helper ──────────────────────────────────────────────────── */
type Stage = { set: <K extends keyof SettingsPatch>(k: K, v: SettingsPatch[K]) => void; changed: (k: keyof SettingsPatch) => boolean };
/* ── Tab ─────────────────────────────────────────────────────────────── */
const parseLines = (t: string) =>
  t
    .split("\n")
    .map((x) => x.trim())
    .filter(Boolean);
const CRON_EXAMPLES = ["*/15 * * * *", "0 * * * *", "0 3 * * *", "0 9 * * 1-5", "0 0 1 * *"];
export function SettingsTab({ svc, env, actions }: { svc: CanvasService; env: Environment; actions: ServiceActions }) {
  const inst = svc.instance;
  const canEdit = useCaps(svc.project_id).canEdit;
  const { staged, setSetting } = useStaged(svc.id, env.id);
  const draft = useMemo(() => ({ ...inst, ...staged.settings }) as ServiceInstance, [inst, staged.settings]);
  const stage: Stage = {
    set: (k, v) => setSetting(k, v, inst),
    changed: (k) => k in staged.settings || (inst.pending_changes ?? []).includes(k),
  };
  const [q, setQ] = useState("");
  const [autoUpdates, setAutoUpdates] = useState(false);
  const [watchText, setWatchText] = useSyncedText(draft.watch_paths, (v) => v.join("\n"), parseLines);
  const body = useRef<HTMLDivElement>(null);
  const search = useRef<HTMLInputElement>(null);
  useKeyboardShortcuts([{ keys: "/", label: t("설정 필터에 포커스"), group: t("서비스 설정"), onTrigger: () => search.current?.focus() }]);
  const isRepo = svc.source_type === "repo";
  // aven-server 자신(self 서비스)은 소스와 빌드만 정한다. 나머지는 컨테이너 스펙(코드)과 env 파일이 정한다.
  const self = !!inst.self;
  const sections = SECTIONS.filter((s) => ((s.id !== "build" && s.id !== "flags") || isRepo) && (self ? s.id === "server" || s.id === "source" || s.id === "build" : s.id !== "server"));
  const { isVisible, empty } = useSettingsFilter(body, q);
  const { jump } = useScrollSpy(body, sections.map((s) => s.id));
  const cronErr = draft.cron_schedule ? cronError(draft.cron_schedule) : null;
  const runs = draft.cron_schedule && !cronErr ? nextRuns(draft.cron_schedule, 3) : [];
  const running = ["online", "crashed", "failed", "sleeping", "scheduled"].includes(inst.status);
  const hcTimeoutErr = draft.healthcheck_type !== "none" && (draft.healthcheck_timeout_s < 1 || draft.healthcheck_timeout_s > 3600);
  const teardownOn = (draft.overlap_seconds ?? 0) > 0 || draft.draining_seconds != null;
  const [teardownOpen, setTeardownOpen] = useState(false);
  const restart = RESTART[draft.restart_policy] ?? RESTART.on_failure;
  return (
    <div className="relative -mx-5 -mt-8 md:-mx-12">
      <div className="sticky top-[var(--sticky-offset,0px)] z-[99] border-b border-gray-100 bg-gray-50 px-9 py-3 dark:bg-secondaryBg">
        <div className="relative">
          <Input
            ref={search}
            size="sm"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape" && q) {
                e.stopPropagation();
                setQ("");
              } else if (e.key === "Escape") search.current?.blur();
            }}
            placeholder={t("설정 필터링...")}
            aria-label={t("설정 필터링")}
            className="bg-background dark:bg-secondaryBg"
          />
          {q ? (
            <button
              type="button"
              onClick={() => (setQ(""), search.current?.focus())}
              aria-label={t("필터 지우기")}
              title={t("필터 지우기")}
              className="absolute right-1 top-[3px] z-20 flex size-7 cursor-pointer items-center justify-center rounded border border-gray-100 bg-gray-50 text-gray-600 hover:text-foreground dark:bg-background"
            >
              <X className="size-3.5" />
            </button>
          ) : (
            <button
              type="button"
              tabIndex={-1}
              onClick={() => search.current?.focus()}
              aria-hidden
              className="absolute right-1 top-[3px] z-20 flex size-7 items-center justify-center rounded border border-gray-100 bg-gray-50 text-gray-600 dark:bg-background"
            >
              <span>/</span>
            </button>
          )}
        </div>
      </div>
      <div className="relative grid grid-cols-1 gap-16 lg:grid-cols-[1fr_auto]">
        {/* Timeline rail: the group icon circles (40px, left -22px) sit on the 2px rail. */}
        <div
          ref={body}
          className={cx("mx-6 mt-12 grid gap-16 pb-40 sm:mx-12 sm:pb-12", !empty && "sm:border-l-2 sm:border-black/[0.08] dark:sm:border-white/[0.08]")}
        >
          <PermFieldset readOnly={!canEdit} note={t("서비스 설정, 도메인, 볼륨, 백업을 바꾸려면 member 이상 역할이 필요합니다.")}>
            {self && (
              <Section id="server" wide>
                <ServerConfigSettings svc={svc} env={env} canEdit={canEdit} />
              </Section>
            )}
            <Section id="source">
              {isRepo ? (
                <>
                  <Item label={t("소스 저장소")} keywords="source repo repository git github url" badge={<Changed on={stage.changed("repo_url")} />}>
                    <RepoSourceCard url={draft.repo_url ?? ""} onUrl={(v) => stage.set("repo_url", strOrNull(v))} />
                  </Item>
                  {/^(git@|ssh:\/\/)/.test(draft.repo_url ?? "") && (
                    <Item label="SSH deploy key" keywords="ssh key 키" description={t("비공개 저장소를 SSH(git@…)로 클론할 때 쓰는 서비스 전용 키입니다. 개인키는 암호화되어 서버에만 있고 화면에 나오지 않습니다.")}>
                      <DeployKeyPanel svc={svc} />
                    </Item>
                  )}
                  <Item label={t("루트 디렉터리")} keywords="root directory monorepo 모노레포" badge={<Changed on={stage.changed("root_dir")} />} description={t("빌드와 배포 명령을 실행할 디렉터리입니다. 모노레포라면 이 서비스의 하위 폴더를 지정하세요.")}>
                    <Reveal has={!!draft.root_dir && draft.root_dir !== "/"} label={t("루트 디렉터리 추가")}>
                      {(focus) => <Input autoFocus={focus} mono value={draft.root_dir} onChange={(e) => stage.set("root_dir", e.target.value || "/")} placeholder="/" aria-label={t("루트 디렉터리")} />}
                    </Reveal>
                  </Item>
                  <Item label={t("{env}에 연결된 브랜치", { env: env.name })} keywords="branch git auto deploy push webhook 자동 배포 웹훅" badge={<Changed on={stage.changed("branch") || stage.changed("auto_deploy")} />}>
                    <BranchCard branch={draft.branch} autoDeploy={draft.auto_deploy} onBranch={(v) => stage.set("branch", v)} onAutoDeploy={(v) => stage.set("auto_deploy", v)} />
                  </Item>
                  <Item
                    label={t("CI 대기")}
                    keywords="wait for ci checks github actions"
                    badge={<Changed on={stage.changed("wait_for_ci")} />}
                    description={t("GitHub에서 CI가 통과할 때까지 배포를 기다립니다. 웹훅에 Check suites 또는 Workflow runs 이벤트가 필요하고, CI가 실패하면 배포를 건너뜁니다.")}
                  >
                    <ToggleCard checked={draft.wait_for_ci ?? false} onChange={(v) => stage.set("wait_for_ci", v)} label={t("CI 대기 사용")} />
                  </Item>
                </>
              ) : svc.source_type === "function" ? (
                <Item label={t("소스")} keywords="function code bun" description={t("function 코드는 코드 탭에서 고칩니다. 배포할 때 oven/bun 이미지로 빌드합니다.")}>
                  <SourceRow icon={<Code />} title="index.ts (Bun)" actions={null} />
                </Item>
              ) : (
                <Item label={t("소스 이미지")} keywords="source image docker registry tag auto update 태그 자동 업데이트" badge={<Changed on={stage.changed("image")} />}>
                  <ImageSourceCard
                    svc={svc}
                    env={env}
                    image={draft.image ?? ""}
                    onImage={(v) => stage.set("image", strOrNull(v))}
                    onConfigure={svc.source_type === "image" ? () => setAutoUpdates((o) => !o) : undefined}
                    configureOpen={autoUpdates}
                    hint={<RegistryHint pid={svc.project_id} image={draft.image ?? ""} />}
                  />
                  {autoUpdates && svc.source_type === "image" && (
                    <div className="mt-4 rounded-md border border-gray-200 p-4">
                      <ImageUpdateSettings svc={svc} env={env} inst={inst} />
                    </div>
                  )}
                </Item>
              )}
              <ServiceName svc={svc} env={env} />
            </Section>
            {!self && (
              <>
                <Section id="networking">
                  <Networking svc={svc} env={env} inst={inst} draft={draft} stage={stage} />
                </Section>
                <Section id="edge">
                  <EdgeSettings svc={svc} env={env} />
                </Section>
                <Section id="scaling">
                  <Scaling svc={svc} env={env} inst={inst} draft={draft} stage={stage} />
                </Section>
              </>
            )}
            {isRepo && (
              <Section id="build">
                {!self && (
                <Item label={t("빌더")} keywords="builder dockerfile railpack nixpacks" badge={<Changed on={stage.changed("builder")} />} description={t("자동은 Dockerfile이 있으면 쓰고, 없으면 Railpack으로 언어를 감지해 빌드합니다.")}>
                  <Select aria-label={t("빌더")} value={draft.builder} onChange={(e) => stage.set("builder", e.target.value as Builder)}>
                    <option value="auto">{t("자동")}</option>
                    <option value="dockerfile">Dockerfile</option>
                    <option value="railpack">Railpack</option>
                  </Select>
                </Item>
                )}
                {draft.builder !== "railpack" && (
                  <Item label={t("Dockerfile 경로")} keywords="dockerfile path" badge={<Changed on={stage.changed("dockerfile_path")} />} description={t("루트 디렉터리 기준 경로입니다.")}>
                    <Input mono value={draft.dockerfile_path} onChange={(e) => stage.set("dockerfile_path", e.target.value)} placeholder="Dockerfile" aria-label={t("Dockerfile 경로")} />
                  </Item>
                )}
                {!self && (
                <Item
                  label={t("빌드 위치")}
                  keywords="build node 노드 원격"
                  badge={<Changed on={stage.changed("build_node")} />}
                  description={t("원격 노드에 배포하는 서비스를 어디서 빌드할지 정합니다. keel 서버 노드에서 빌드하면 이미지를 압축해 배포 노드로 보냅니다.")}
                >
                  <Select aria-label={t("빌드 위치")} value={draft.build_node ?? ""} onChange={(e) => stage.set("build_node", e.target.value as "" | "local")}>
                    <option value="">{t("배포 노드")}</option>
                    <option value="local">{t("keel 서버 노드")}</option>
                  </Select>
                </Item>
                )}
                <Item
                  label={t("감시 경로")}
                  keywords="watch paths gitignore"
                  badge={<Changed on={stage.changed("watch_paths")} />}
                  description={t("gitignore 형식 패턴입니다. 푸시가 이 경로를 건드리지 않으면 새 배포를 건너뜁니다.")}
                >
                  <Reveal has={draft.watch_paths.length > 0} label={t("패턴 추가")}>
                    {(focus) => (
                      <Textarea
                        autoFocus={focus}
                        mono
                        rows={3}
                        value={watchText}
                        placeholder={"src/**\npackage.json"}
                        aria-label={t("감시 경로")}
                        onChange={(e) => {
                          setWatchText(e.target.value);
                          stage.set("watch_paths", parseLines(e.target.value));
                        }}
                      />
                    )}
                  </Reveal>
                </Item>
                {!self && (
                <Item
                  label={t("빌드 비밀")}
                  keywords="build secrets buildkit secret 변수"
                  badge={<Changed on={stage.changed("build_secrets")} />}
                  description={
                    <>
                      {tr("선택한 변수는 BuildKit {flag}으로만 빌드에 전달됩니다. Dockerfile에서는 {run}으로 읽으세요.", {
                        flag: <span className="font-mono">--secret</span>,
                        run: <span className="font-mono">RUN --mount=type=secret,id={t("시크릿 이름")}</span>,
                      })}
                    </>
                  }
                >
                  <BuildSecretsEditor value={draft.build_secrets ?? []} variableNames={Object.keys(inst.variables)} onChange={(v) => stage.set("build_secrets", v.length ? v : [])} />
                </Item>
                )}
                <Item label={t("설정 파일")} keywords="config as code keel.json railway.json">
                  <p className="text-sm text-gray-600">
                    {tr("저장소에 {keel} 또는 {railway}이 있으면 배포할 때 그 값이 여기 설정보다 우선합니다.", {
                      keel: <span className="font-mono text-foreground">keel.json</span>,
                      railway: <span className="font-mono text-foreground">railway.json</span>,
                    })}
                  </p>
                </Item>
              </Section>
            )}
            {!self && (
              <>
                <Section id="deploy">
                  <div className="flex flex-col">
                    <Item label={t("사용자 지정 시작 명령")} keywords="custom start command cmd entrypoint" badge={<Changed on={stage.changed("start_command")} />} description={t("새 배포를 시작할 때 실행할 명령입니다. 이미지의 기본 CMD 대신 씁니다.")}>
                      <Reveal has={!!draft.start_command} label={t("시작 명령")}>
                        {(focus) => (
                          <Input autoFocus={focus} mono value={draft.start_command ?? ""} onChange={(e) => stage.set("start_command", strOrNull(e.target.value))} placeholder="npm run start" aria-label={t("시작 명령")} />
                        )}
                      </Reveal>
                    </Item>
                    <PreDeploy draft={draft} stage={stage} />
                  </div>
                  <Item
                    label={t("티어다운")}
                    keywords="teardown overlap draining sigterm zero downtime 무중단 오버랩 드레이닝 배포 전환"
                    badge={<Changed on={stage.changed("overlap_seconds") || stage.changed("draining_seconds")} />}
                    description={t("새 배포가 시작될 때 이전 배포를 어떻게 끝낼지 정합니다.")}
                  >
                    <div className="flex flex-col space-y-2">
                      <ToggleCard
                        checked={teardownOn || teardownOpen}
                        label={t("티어다운 설정 사용")}
                        onChange={(v) => {
                          setTeardownOpen(v);
                          if (!v) {
                            stage.set("overlap_seconds", 0);
                            stage.set("draining_seconds", null);
                          }
                        }}
                      />
                    </div>
                    {(teardownOn || teardownOpen) && (
                      <div className="mt-4 grid grid-cols-2 gap-4">
                        <label className="block">
                          <span className={LABEL_CLS}>{t("오버랩 (0~600초)")}</span>
                          <Input
                            mono
                            inputMode="numeric"
                            className="tabular-nums"
                            value={String(draft.overlap_seconds ?? 0)}
                            onChange={(e) => stage.set("overlap_seconds", Math.min(600, Number(e.target.value.replace(/[^0-9]/g, "")) || 0))}
                            aria-label={t("오버랩 시간(초)")}
                          />
                          <Hint>{t("새 배포가 트래픽을 받은 뒤 이전 배포를 남겨 두는 시간입니다.")}</Hint>
                        </label>
                        <label className="block">
                          <span className={LABEL_CLS}>{t("드레이닝 (0~300초)")}</span>
                          <Input
                            mono
                            inputMode="numeric"
                            className="tabular-nums"
                            value={draft.draining_seconds == null ? "" : String(draft.draining_seconds)}
                            placeholder={svc.volumes.length > 0 ? "30" : "10"}
                            onChange={(e) => {
                              const v = e.target.value.replace(/[^0-9]/g, "");
                              stage.set("draining_seconds", v === "" ? null : Math.min(300, Number(v)));
                            }}
                            aria-label={t("드레이닝 시간(초, 비우면 기본값)")}
                          />
                          <Hint>{t("SIGTERM 뒤 강제 종료까지 기다리는 시간입니다. 비우면 기본값을 씁니다.")}</Hint>
                        </label>
                        {svc.volumes.length > 0 && <Hint className="col-span-2 !mt-0">{t("볼륨이 있는 서비스는 새 컨테이너를 띄우기 전에 이전 컨테이너를 멈추므로 오버랩이 적용되지 않습니다.")}</Hint>}
                      </div>
                    )}
                  </Item>
                  <Item label={t("크론 일정")} keywords="cron schedule job 작업 예약" badge={<Changed on={stage.changed("cron_schedule")} />} description={t("지정한 크론 일정에 따라 서비스를 실행합니다. 시각은 UTC 기준입니다.")}>
                    <Reveal has={!!draft.cron_schedule} label={t("일정 추가")}>
                      {(focus) => (
                        <>
                          <Input
                            autoFocus={focus}
                            mono
                            value={draft.cron_schedule ?? ""}
                            onChange={(e) => stage.set("cron_schedule", strOrNull(e.target.value))}
                            placeholder="0 3 * * *"
                            aria-label={t("크론 일정")}
                            aria-invalid={!!cronErr}
                            invalid={!!cronErr}
                          />
                          <div className="mt-2 flex flex-wrap gap-1.5">
                            {CRON_EXAMPLES.map((c) => (
                              <button
                                key={c}
                                type="button"
                                onClick={() => stage.set("cron_schedule", c)}
                                aria-pressed={draft.cron_schedule === c}
                                className={cx(
                                  "cursor-pointer rounded border px-1.5 py-0.5 font-mono text-xs hover:border-gray-300 hover:text-foreground",
                                  draft.cron_schedule === c ? "border-gray-300 bg-gray-100 text-foreground" : "border-gray-200 text-gray-500",
                                )}
                                title={describeCron(c)}
                              >
                                {c}
                              </button>
                            ))}
                          </div>
                          {draft.cron_schedule &&
                            (cronErr ? (
                              <Hint tone="danger">{cronErr}</Hint>
                            ) : (
                              <div className={cx(CARD, "mt-3 text-sm")}>
                                <div className="font-medium text-foreground">{describeCron(draft.cron_schedule)}</div>
                                <div className="mt-0.5 text-xs tabular-nums text-gray-600">{t("다음 실행: {times} (현지 시각)", { times: runs.map((d) => fmtTime(d.toISOString())).join(", ") })}</div>
                              </div>
                            ))}
                        </>
                      )}
                    </Reveal>
                  </Item>
                  <Item
                    label={t("헬스체크 경로")}
                    keywords="healthcheck health check path http tcp timeout 헬스체크"
                    badge={<Changed on={stage.changed("healthcheck_type") || stage.changed("healthcheck_path") || stage.changed("healthcheck_timeout_s")} />}
                    description={t("배포가 끝나기 전에 호출해 새 배포가 살아 있는지 확인하는 엔드포인트입니다. 응답이 없으면 이전 배포가 계속 서비스합니다.")}
                  >
                    {draft.healthcheck_type === "none" ? (
                      <AddButton onClick={() => stage.set("healthcheck_type", "http")}>{t("헬스체크 경로")}</AddButton>
                    ) : (
                      <>
                        <div className="flex items-center gap-2">
                          <Select aria-label={t("헬스체크 방식")} className="w-[104px] shrink-0" value={draft.healthcheck_type} onChange={(e) => stage.set("healthcheck_type", e.target.value as HealthcheckType)}>
                            <option value="http">HTTP</option>
                            <option value="tcp">TCP</option>
                          </Select>
                          {draft.healthcheck_type === "http" ? (
                            <Input mono value={draft.healthcheck_path} onChange={(e) => stage.set("healthcheck_path", e.target.value)} placeholder="/healthz" aria-label={t("헬스체크 경로")} className="min-w-0 flex-1" />
                          ) : (
                            <span className="min-w-0 flex-1 text-sm text-gray-600">{t("앱 포트로 TCP 연결이 열리면 통과합니다.")}</span>
                          )}
                          <IconButton label={t("헬스체크 끄기")} onClick={() => stage.set("healthcheck_type", "none")}>
                            <Trash2 />
                          </IconButton>
                        </div>
                        <label className="mt-3 flex items-center gap-2 text-sm text-gray-600">
                          {t("제한 시간")}
                          <Input
                            size="sm"
                            mono
                            inputMode="numeric"
                            className={cx(FIELD, "w-[80px] tabular-nums")}
                            invalid={hcTimeoutErr}
                            value={String(draft.healthcheck_timeout_s)}
                            onChange={(e) => stage.set("healthcheck_timeout_s", Number(e.target.value.replace(/[^0-9]/g, "").slice(0, 4)))}
                            aria-label={t("헬스체크 제한 시간(초)")}
                          />
                          {t("초")}
                        </label>
                        {hcTimeoutErr && <Hint tone="danger">{t("제한 시간은 1초 이상이어야 합니다.")}</Hint>}
                      </>
                    )}
                  </Item>
                  <Item
                    label={t("서버리스")}
                    keywords="serverless sleep idle 슬립 유휴"
                    badge={<Changed on={stage.changed("sleep_when_idle") || stage.changed("sleep_after_s")} />}
                    description={t("요청이 없으면 컨테이너를 0개로 줄였다가 트래픽이 오면 다시 띄웁니다. 잠든 동안 들어온 요청은 기다렸다가 컨테이너가 깨어나면 처리합니다(최대 30초).")}
                  >
                    <ToggleCard checked={draft.sleep_when_idle} onChange={(v) => stage.set("sleep_when_idle", v)} label={t("서버리스 사용")} />
                    {draft.sleep_when_idle && (
                      <label className="mt-3 flex items-center gap-2 text-sm text-gray-600">
                        <Input
                          size="sm"
                          mono
                          inputMode="numeric"
                          value={String(draft.sleep_after_s ?? 600)}
                          onChange={(e) => stage.set("sleep_after_s", Number(e.target.value.replace(/[^0-9]/g, "").slice(0, 6)))}
                          aria-label={t("슬립까지 대기 시간(초)")}
                          className={cx(FIELD, "w-[96px] tabular-nums")}
                        />
                        {t("초 동안 요청이 없으면 슬립")}
                        <span className="tabular-nums text-gray-500">({fmtSec(draft.sleep_after_s ?? 600)})</span>
                      </label>
                    )}
                    {draft.sleep_when_idle && svc.instance.domains.length === 0 && <Hint tone="warn">{t("공개 도메인이 없으면 깨울 요청이 들어오지 않습니다.")}</Hint>}
                  </Item>
                  <Item label={t("재시작 정책")} keywords="restart policy retries crash" badge={<Changed on={stage.changed("restart_policy") || stage.changed("restart_max_retries")} />} description={t("프로세스가 종료됐을 때 어떻게 할지 정합니다.")}>
                    <div className={cx(CARD, "flex items-center justify-between gap-4")}>
                      <div className="flex w-full flex-col space-y-1">
                        <p className="text-base">{restart.label}</p>
                        <p className="text-xs font-medium text-gray-600">{restart.description}</p>
                      </div>
                      <Menu
                        label={t("재시작 정책")}
                        trigger={(p) => (
                          <Button {...p} size="sm" variant="ghost" title={t("재시작 정책")} aria-label={t("재시작 정책 바꾸기")} className="w-[26px] px-0">
                            <ChevronDown className="text-foreground" />
                          </Button>
                        )}
                      >
                        {(close) =>
                          (Object.keys(RESTART) as RestartPolicy[]).map((k) => (
                            <MenuItem
                              key={k}
                              icon={draft.restart_policy === k ? <Check /> : <span className="block size-4" />}
                              onSelect={() => {
                                close();
                                stage.set("restart_policy", k);
                              }}
                            >
                              {RESTART[k].label}
                            </MenuItem>
                          ))
                        }
                      </Menu>
                    </div>
                    {draft.restart_policy === "on_failure" && (
                      <div className="mt-4">
                        <header className="mb-3 grid gap-1">
                          <p className="text-sm text-gray-600">{t("오류로 멈췄을 때 서비스를 다시 시작할 최대 횟수입니다.")}</p>
                        </header>
                        <Input
                          mono
                          inputMode="numeric"
                          className="tabular-nums"
                          value={String(draft.restart_max_retries)}
                          onChange={(e) => stage.set("restart_max_retries", Number(e.target.value.replace(/[^0-9]/g, "").slice(0, 3)))}
                          aria-label={t("최대 재시작 횟수")}
                        />
                      </div>
                    )}
                  </Item>
                </Section>
                <Section id="volumes">
                  <Volumes svc={svc} env={env} />
                  {svc.source_type === "database" && svc.database_kind && ["postgres", "mysql", "mariadb", "redis", "mongo"].includes(svc.database_kind) && (
                    <Item
                      label={t("데이터베이스 백업")}
                      keywords="database backup dump restore s3 pg_dump mysqldump 덤프 복원"
                      description={t("pg_dump, mysqldump, RDB, mongodump로 만드는 논리 백업입니다(볼륨 스냅샷과 별개). 로컬 또는 S3 호환 저장소에 저장하고, 같은 DB나 다른 DB 서비스에 복원할 수 있습니다.")}
                    >
                      <DbBackups svc={svc} env={env} />
                    </Item>
                  )}
                </Section>
                {isRepo && (
                  <Section id="flags">
                    <div data-setting="" data-keywords="skipped builds skip build cache feature flag 캐시 재사용" className="grid gap-6">
                      <div className="group relative">
                        <label className="flex w-full items-center space-x-4">
                          <Toggle checked={draft.skip_builds ?? false} onChange={(v) => stage.set("skip_builds", v)} label={t("빌드 건너뛰기")} />
                          <span data-setting-head="" className="grid w-full cursor-pointer select-none gap-1">
                            <span className="flex items-center gap-2 text-base font-semibold">
                              {t("빌드 건너뛰기")} <Changed on={stage.changed("skip_builds")} />
                            </span>
                            <span className="text-sm text-gray-600">{t("같은 소스 코드를 이전 배포에서 이미 빌드했다면 빌드 단계를 건너뜁니다. 배포 메뉴에서 강제로 다시 빌드할 수 있습니다.")}</span>
                          </span>
                        </label>
                      </div>
                    </div>
                  </Section>
                )}
                <Section id="danger" danger>
                  <DangerZone svc={svc} env={env} actions={actions} running={running} />
                </Section>
              </>
            )}
          </PermFieldset>
          {empty && (
            <div className="flex flex-col items-center py-16 text-center">
              <span className="mb-4 flex size-10 items-center justify-center rounded-full border border-gray-200 text-gray-500">
                <Search className="size-[18px]" />
              </span>
              <p className="text-sm font-semibold text-foreground">{t("‘{query}’와 일치하는 설정이 없습니다", { query: q.trim() })}</p>
              <p className="mt-1 text-sm text-gray-600">{t("다른 단어로 찾아보세요. 영어 이름(port, domain, cron 등)으로도 찾을 수 있습니다.")}</p>
              <Button variant="outline" className="mt-4" onClick={() => (setQ(""), search.current?.focus())}>
                {t("필터 지우기")}
              </Button>
            </div>
          )}
        </div>
        <aside className="hidden lg:block" aria-label={t("설정 목차")}>
          <ul className="sticky top-28 mr-12 mt-12 grid gap-3">
            {sections.map((s) => {
              const on = isVisible(s.id);
              return (
                <li key={s.id} className={cx("text-sm text-gray-500", !on && "opacity-40")}>
                  <a
                    href={`#settings-${s.id}`}
                    onClick={(e) => {
                      e.preventDefault();
                      if (on) jump(s.id);
                    }}
                    aria-disabled={!on || undefined}
                    className={cx("focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-700", on ? "hover:text-pink-600" : "cursor-default")}
                  >
                    {s.title}
                  </a>
                </li>
              );
            })}
          </ul>
        </aside>
      </div>
    </div>
  );
}
const RESTART: Record<RestartPolicy, { label: string; description: string }> = {
  on_failure: { label: t("실패했을 때"), description: t("컨테이너가 0이 아닌 종료 코드로 끝나면 다시 시작합니다.") },
  always: { label: t("항상"), description: t("컨테이너가 종료되면 항상 다시 시작합니다.") },
  never: { label: t("재시작 안 함"), description: t("컨테이너가 종료되어도 다시 시작하지 않습니다.") },
};

function PreDeploy({ draft, stage }: { draft: ServiceInstance; stage: Stage }) {
  const [open, setOpen] = useState(false);
  if (!draft.pre_deploy_command && !open)
    return (
      <button type="button" onClick={() => setOpen(true)} className="group mt-3 grid w-max cursor-pointer grid-cols-[min-content_auto] gap-1 text-left text-sm text-gray-500">
        <span className="icon-container icon-sm mt-0.5 text-lg text-current group-hover:text-pink-600" aria-hidden>
          <Plus />
        </span>
        <span>
          <span className="underline group-hover:text-pink-600">{t("사전 배포 단계 추가")}</span>
        </span>
      </button>
    );
  return (
    <Item
      className="mt-8"
      label={t("사전 배포 명령")}
      keywords="pre-deploy predeploy migrate migration 마이그레이션"
      badge={<Changed on={stage.changed("pre_deploy_command")} />}
      description={t("새 버전을 띄우기 전에 한 번 실행합니다. 마이그레이션에 쓰세요. 실패하면 배포를 멈춥니다.")}
    >
      <Input autoFocus={open} mono value={draft.pre_deploy_command ?? ""} onChange={(e) => stage.set("pre_deploy_command", strOrNull(e.target.value))} placeholder="npm run migrate" aria-label={t("사전 배포 명령")} />
    </Item>
  );
}
