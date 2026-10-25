"use client";

import { useState, type CSSProperties } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { AlertCircle, MoreVertical, Save, Undo2 } from "lucide-react";
import { api, type CanvasService, type ServiceInstance } from "@/lib/api";
import { cronError } from "@/lib/cron";
import { clearStaged, pendingSummary, toPatch, useStagedEnv, type Staged } from "@/lib/staged";
import { qk, useNodes } from "@/lib/queries";
import { useCanvasState } from "@/lib/nav";
import { toast } from "@/lib/uiState";
import { Button, Dialog, IconButton, Menu, MenuItem, cx, errorMessage, useConfirm } from "../ui";
import { ServiceIcon } from "../ServiceIcon";
import { lock, NO_PERM } from "../Perm";
import { useKeyboardShortcuts } from "@/components/KeyboardShortcuts";
import { t } from "@/lib/i18n";

export const SETTING_LABEL: Record<string, string> = {
  "function.code": t("function 코드"),
  repo_url: t("소스 저장소"),
  branch: t("브랜치"),
  root_dir: t("루트 디렉터리"),
  image: t("이미지"),
  builder: t("빌더"),
  dockerfile_path: t("Dockerfile 경로"),
  watch_paths: t("감시 경로"),
  auto_deploy: t("자동 배포"),
  wait_for_ci: t("CI 대기"),
  skip_builds: t("빌드 건너뛰기"),
  build_node: t("빌드 위치"),
  start_command: t("시작 명령"),
  pre_deploy_command: t("사전 배포 명령"),
  port: t("포트"),
  healthcheck_type: t("헬스체크 방식"),
  healthcheck_path: t("헬스체크 경로"),
  healthcheck_timeout_s: t("헬스체크 제한 시간"),
  restart_policy: t("재시작 정책"),
  restart_max_retries: t("최대 재시도"),
  replicas: t("레플리카"),
  cron_schedule: t("크론 일정"),
  sleep_when_idle: t("유휴 시 슬립"),
  sleep_after_s: t("슬립 대기 시간"),
  overlap_seconds: t("오버랩 시간"),
  draining_seconds: t("드레이닝 시간"),
  private_ports: t("노드 간 포트"),
  private_dns_name: t("비공개 DNS 이름"),
  node_id: t("배포 노드"),
};

function show(v: unknown): string {
  if (v === null || v === undefined || v === "") return t("없음");
  if (Array.isArray(v)) return v.length ? v.join(", ") : t("없음");
  if (typeof v === "boolean") return v ? t("켬") : t("끔");
  return String(v);
}

export function validateStaged(s: Staged): string | null {
  const st = s.settings;
  if (st.replicas !== undefined && (!Number.isInteger(st.replicas) || st.replicas < 1 || st.replicas > 10))
    return t("레플리카는 1부터 10 사이로 정하세요.");
  if (st.port !== undefined && st.port !== null && (!Number.isInteger(st.port) || st.port < 1 || st.port > 65535))
    return t("포트는 1부터 65535 사이의 숫자로 입력하세요.");
  if (st.sleep_after_s !== undefined && (!Number.isInteger(st.sleep_after_s) || st.sleep_after_s < 10 || st.sleep_after_s > 604800))
    return t("슬립 대기 시간은 10초부터 604800초(7일) 사이로 정하세요.");
  if (st.cron_schedule) {
    const e = cronError(st.cron_schedule);
    if (e) return t("크론 일정이 올바르지 않습니다: {error}", { error: e });
  }
  if (st.healthcheck_path !== undefined && st.healthcheck_path && !st.healthcheck_path.startsWith("/"))
    return t("헬스체크 경로는 /로 시작해야 합니다. 예: /healthz");
  if (st.healthcheck_timeout_s !== undefined && (!Number.isInteger(st.healthcheck_timeout_s) || st.healthcheck_timeout_s < 1))
    return t("헬스체크 제한 시간은 1초 이상이어야 합니다.");
  for (const k of Object.keys(s.vars)) if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(k)) return `${k}: ${t("변수 이름은 영문, 숫자, 밑줄(_)만 쓸 수 있습니다.")}`;
  return null;
}

function pendingLabel(key: string) {
  return key.startsWith("variables.") ? key.slice(10) : (SETTING_LABEL[key] ?? key);
}

type Item = {
  svc: CanvasService;
  staged: Staged;
  pend: ReturnType<typeof pendingSummary>;
  hasLocal: boolean;
  problem: string | null;
};

/** Per-service change list (Details dialog). */
function ChangeList({ item, nodeName }: { item: Item; nodeName: (id: unknown) => string }) {
  const { svc, staged, pend } = item;
  const inst = svc.instance;
  const vars = Object.entries(staged.vars).sort(([a], [b]) => a.localeCompare(b));
  const settings = Object.entries(staged.settings);
  const serverOnly = pend.server.filter((k) => !pend.local.includes(k));
  return (
    <ul className="space-y-1 font-mono text-xs">
      {vars.map(([k, v]) => (
        <li key={k} className="flex gap-2">
          <span className={cx("w-3 shrink-0", v === null ? "text-danger" : k in inst.variables ? "text-yellow-600" : "text-success")}>
            {v === null ? "−" : k in inst.variables ? "~" : "+"}
          </span>
          <span className="text-foreground">{k}</span>
          <span className="truncate text-gray-400">{v === null ? t("삭제") : k in inst.variables ? t("값 변경") : t("추가")} · {t("저장 전")}</span>
        </li>
      ))}
      {settings.map(([k, v]) => (
        <li key={k} className="flex gap-2 font-sans text-sm">
          <span className="w-3 shrink-0 font-mono text-yellow-600">~</span>
          <span className="text-foreground">{SETTING_LABEL[k] ?? k}</span>
          <span className="min-w-0 truncate text-gray-400">
            {k === "node_id" ? nodeName(inst.node_id) : show(inst[k as keyof ServiceInstance])} →{" "}
            <span className="text-gray-600">{k === "node_id" ? nodeName(v) : show(v)}</span> · {t("저장 전")}
          </span>
        </li>
      ))}
      {serverOnly.map((k) => (
        <li key={k} className="flex gap-2">
          <span className="w-3 shrink-0 text-pink-600">•</span>
          <span className={cx("text-foreground", !k.startsWith("variables.") && "font-sans text-sm")}>{pendingLabel(k)}</span>
          <span className="truncate font-sans text-sm text-gray-400">{t("저장됨 · 배포 전")}</span>
        </li>
      ))}
    </ul>
  );
}

/**
 * Environment-wide staged-changes banner ("Apply N changes"), modeled on Railway's dashboard and floating at the top centre of the canvas.
 * Local edits are applied per service with PATCH ?deploy=1 (variables_patch + settings);
 * changes already saved on the server (`pending_changes`) only need a deploy.
 */
export function StagedBar({
  envId,
  services,
  canEdit = true,
  className,
  style,
}: {
  envId: string;
  services: CanvasService[];
  /** false(viewer)면 저장/배포 버튼을 aria-disabled로 잠근다. */
  canEdit?: boolean;
  className?: string;
  style?: CSSProperties;
}) {
  const qc = useQueryClient();
  const { svc: openSvc, go } = useCanvasState();
  const local = useStagedEnv(envId);
  const nodes = useNodes();
  const { confirm, node: confirmNode } = useConfirm();
  const [busy, setBusy] = useState<"save" | "apply" | null>(null);
  const [details, setDetails] = useState(false);
  const [errors, setErrors] = useState<{ name: string; error: unknown }[]>([]);
  const nodeName = (id: unknown) => nodes.data?.find((n) => n.id === id)?.name ?? String(id);

  const items: Item[] = [];
  for (const svc of services) {
    const staged = local[svc.id] ?? { vars: {}, settings: {} };
    const pend = pendingSummary(svc.instance, staged);
    if (pend.count === 0) continue;
    const hasLocal = pend.local.length > 0;
    const moving = svc.volumes.length > 0 && staged.settings.node_id !== undefined && staged.settings.node_id !== svc.instance.node_id;
    const problem = moving ? t("볼륨 데이터의 노드 간 이전은 아직 지원하지 않습니다. 배포 노드 변경을 되돌려 주세요.") : hasLocal ? validateStaged(staged) : null;
    items.push({ svc, staged, pend, hasLocal, problem });
  }
  const count = items.reduce((n, it) => n + it.pend.count, 0);
  const anyLocal = items.some((it) => it.hasLocal);
  const problems = items.filter((it) => it.problem);
  const blocked = problems.length > 0;

  const run = async (deploy: boolean) => {
    if (busy || blocked || !canEdit) return;
    const targets = deploy ? items : items.filter((it) => it.hasLocal);
    if (!targets.length) return;
    setBusy(deploy ? "apply" : "save");
    setErrors([]);
    const results = await Promise.allSettled(
      targets.map(async ({ svc, staged, hasLocal }) => {
        try {
          if (hasLocal) {
            const next = await api.updateInstance(svc.id, envId, toPatch(staged), deploy);
            qc.setQueryData(qk.instance(svc.id, envId), next);
            clearStaged(svc.id, envId);
          } else {
            await api.deploy(svc.id, envId);
          }
        } finally {
          qc.invalidateQueries({ queryKey: qk.deployments(svc.id, envId) });
          qc.invalidateQueries({ queryKey: qk.instance(svc.id, envId) });
          qc.invalidateQueries({ queryKey: qk.resolved(svc.id, envId) });
        }
      }),
    );
    qc.invalidateQueries({ queryKey: qk.canvas(envId) });
    qc.invalidateQueries({ queryKey: ["events"] });
    const failed = results.flatMap((r, i) => (r.status === "rejected" ? [{ name: targets[i].svc.name, error: r.reason as unknown }] : []));
    setErrors(failed);
    setBusy(null);
    const ok = targets.length - failed.length;
    if (ok > 0) {
      toast(deploy ? t("서비스 {count}개에 배포를 시작했습니다", { count: ok }) : t("서비스 {count}개의 변경 사항을 저장했습니다", { count: ok }));
      if (deploy && openSvc && targets.some((tg, i) => tg.svc.id === openSvc && results[i].status === "fulfilled")) go({ tab: "deployments", dep: null }, "replace");
    }
    if (!failed.length) setDetails(false);
  };

  const discard = () =>
    confirm({
      title: t("변경 사항 취소"),
      message: (
        <>
          {t("저장하지 않은 변경 사항을 모두 버립니다.")}
          {items.some((it) => it.pend.server.some((k) => !it.pend.local.includes(k))) && (
            <> {t("이미 저장되어 배포만 남은 변경 사항은 남아 있으며, 다음 배포 때 적용됩니다.")}</>
          )}
        </>
      ),
      confirmLabel: t("변경 사항 취소"),
      danger: true,
      run: () => {
        for (const it of items) if (it.hasLocal) clearStaged(it.svc.id, envId);
        setErrors([]);
      },
    });

  useKeyboardShortcuts([
    {
      keys: "shift+enter",
      label: t("변경 사항 적용 및 배포"),
      group: t("변경 사항"),
      enabled: count > 0 && canEdit && !busy && !blocked,
      onTrigger: () => void run(true),
    },
    {
      keys: "alt+shift+enter",
      label: t("변경 사항 저장만 하기"),
      group: t("변경 사항"),
      enabled: count > 0 && canEdit && anyLocal && !busy && !blocked,
      onTrigger: () => void run(false),
    },
  ]);
  if (count === 0) return confirmNode;

  const deployButton = (
    <Button variant="primary" onClick={() => run(true)} loading={busy === "apply"} disabled={blocked || busy === "save"} aria-keyshortcuts="Shift+Enter" {...lock(canEdit)}>
      {t("배포")}
      <span className="ml-1 hidden text-xs font-medium text-pink-100 lg:inline dark:text-pink-800">⇧+Enter</span>
    </Button>
  );

  return (
    <>
      <div
        role="region"
        aria-label={t("적용하지 않은 변경 사항")}
        className={cx(
          "staged-changes-banner-shadow pointer-events-auto z-20 flex min-h-[52px] flex-col justify-center gap-2 rounded-md border border-pink-200 bg-[#FAF6FD] px-4 py-4 lg:min-w-[420px] lg:max-w-[620px] lg:py-2 dark:bg-[#130b19]",
          className,
        )}
        style={style}
      >
        <div className="flex items-center justify-between gap-4">
          <span className="font-medium text-pink-600">{t("변경 사항 {count}개 적용", { count })}</span>
          <div className="flex items-center space-x-2">
            <Button variant="outline" onClick={() => setDetails(true)}>
              {t("세부 정보")}
            </Button>
            {deployButton}
            <Menu
              label={t("변경 사항 작업")}
              trigger={(p) => (
                <IconButton {...p} label={t("변경 사항 작업 더 보기")}>
                  <MoreVertical />
                </IconButton>
              )}
            >
              {(close) => (
                <>
                  <MenuItem
                    icon={<Save />}
                    disabled={!canEdit || !anyLocal || blocked || !!busy}
                    reason={!canEdit ? NO_PERM : undefined}
                    onSelect={() => {
                      close();
                      void run(false);
                    }}
                  >
                    {t("저장만 하기 (배포 안 함)")}
                  </MenuItem>
                  <MenuItem
                    danger
                    icon={<Undo2 />}
                    disabled={!anyLocal || !!busy}
                    onSelect={() => {
                      close();
                      discard();
                    }}
                  >
                    {t("변경 사항 취소")}
                  </MenuItem>
                </>
              )}
            </Menu>
          </div>
        </div>
        {(problems.length > 0 || errors.length > 0) && (
          <ul className="space-y-0.5">
            {problems.map((it) => (
              <li key={`p-${it.svc.id}`} className="flex items-start gap-1.5 text-xs text-red-500">
                <AlertCircle className="mt-px size-3.5 shrink-0" />
                <span>
                  <b className="font-medium">{it.svc.name}</b>: {it.problem}
                </span>
              </li>
            ))}
            {errors.map((e) => (
              <li key={`e-${e.name}`} className="flex items-start gap-1.5 text-xs text-red-500">
                <AlertCircle className="mt-px size-3.5 shrink-0" />
                <span>
                  <b className="font-medium">{e.name}</b>: {errorMessage(e.error)}
                </span>
              </li>
            ))}
          </ul>
        )}
      </div>
      <Dialog
        open={details}
        onClose={() => !busy && setDetails(false)}
        title={t("변경 사항 {count}개", { count })}
        wide
        footer={
          <>
            <Button variant="outline" onClick={() => setDetails(false)} disabled={!!busy}>
              {t("닫기")}
            </Button>
            {anyLocal && (
              <Button onClick={() => run(false)} loading={busy === "save"} disabled={blocked || busy === "apply"} {...lock(canEdit)}>
                {t("저장만 하기")}
              </Button>
            )}
            {deployButton}
          </>
        }
      >
        <p className="mb-4">{t("배포하면 아래 서비스에 변경 사항이 적용됩니다. 저장 전 변경 사항은 이 브라우저 탭에만 있습니다.")}</p>
        <div className="space-y-3">
          {items.map((it) => (
            <section key={it.svc.id} className="rounded-lg border border-gray-200 bg-background px-4 py-3">
              <div className="mb-2 flex items-center gap-2">
                <ServiceIcon service={it.svc} className="size-4" />
                <span className="min-w-0 truncate text-sm font-semibold text-foreground">{it.svc.name}</span>
                <span className="ml-auto shrink-0 text-xs text-blue-600">{t("변경 {count}개", { count: it.pend.count })}</span>
              </div>
              <ChangeList item={it} nodeName={nodeName} />
              {(it.problem || errors.find((e) => e.name === it.svc.name)) && (
                <p className="mt-2 flex items-start gap-1.5 text-xs text-red-500">
                  <AlertCircle className="mt-px size-3.5 shrink-0" />
                  {it.problem ?? errorMessage(errors.find((e) => e.name === it.svc.name)?.error)}
                </p>
              )}
            </section>
          ))}
        </div>
      </Dialog>
      {confirmNode}
    </>
  );
}
