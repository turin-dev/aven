"use client";

import { useMemo, useRef, useState, type ReactNode } from "react";
import { useRouter } from "next/navigation";
import { useQueryClient } from "@tanstack/react-query";
import {
  AlignHorizontalDistributeCenter,
  Box,
  Boxes,
  Copy,
  Database,
  ExternalLink,
  FileText,
  Globe,
  HardDrive,
  History,
  Layers,
  LayoutTemplate,
  Maximize,
  Play,
  PauseCircle,
  RotateCw,
  Rocket,
  Server,
  Settings,
  Braces,
  Activity,
  Trash2,
  Archive,
  KeyRound,
  Link2,
  SquareDashed,
} from "lucide-react";
import { api, type CanvasService, type DatabaseKind, type Volume } from "@/lib/api";
import { qk, useCaps, useDeployments, useNodes } from "@/lib/queries";
import { useCtxHref, useCanvasState, type NewView, type PanelTab } from "@/lib/nav";
import { useServiceActions } from "@/lib/useServiceActions";
import { copyText, toast } from "@/lib/uiState";
import { t, tr } from "@/lib/i18n";
import { ContextMenu, type MenuEntry } from "../ContextMenu";
import { lockItems, NO_PERM } from "../Perm";
import { canRollback } from "../panel/DeploymentsTab";
import { BrandIcon } from "../ServiceIcon";
import { Button, Dialog, Input, errorMessage } from "../ui";

type Confirm = {
  title: string;
  message: ReactNode;
  confirmLabel: string;
  danger?: boolean;
  /** The user must type this to enable the button. */
  typed?: string;
  run: () => Promise<unknown>;
};
/** Confirm dialog that outlives the menu that spawned it. Calls onDone when closed. */
function ConfirmDialog({ c, onDone }: { c: Confirm | null; onDone: () => void }) {
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  return (
    <Dialog
      open={!!c}
      onClose={() => !busy && onDone()}
      title={c?.title ?? ""}
      footer={
        <>
          <Button variant="ghost" onClick={onDone} disabled={busy}>
            {t("취소")}
          </Button>
          <Button
            variant={c?.danger ? "danger" : "primary"}
            loading={busy}
            disabled={!!c?.typed && typed !== c.typed}
            onClick={async () => {
              if (!c) return;
              setBusy(true);
              setError(null);
              try {
                await c.run();
                onDone();
              } catch (e) {
                setError(e);
              } finally {
                setBusy(false);
              }
            }}
          >
            {c?.confirmLabel}
          </Button>
        </>
      }
    >
      <div className="space-y-3 pt-1">
        <div>{c?.message}</div>
        {c?.typed && (
          <label className="block text-sm text-gray-600">
            {tr("확인을 위해 {name}을(를) 입력하세요", { name: <span className="font-mono text-foreground">{c.typed}</span> })}
            <Input mono autoFocus className="mt-2 w-full" value={typed} onChange={(e) => setTyped(e.target.value)} />
          </label>
        )}
        {error ? <div className="text-sm text-danger">{errorMessage(error)}</div> : null}
      </div>
    </Dialog>
  );
}
/** Menu + its confirm dialog. `onDone` fires once neither is showing. */
function useMenuShell(onDone: () => void) {
  const [menuOpen, setMenuOpen] = useState(true);
  const [confirm, setConfirm] = useState<Confirm | null>(null);
  const confirmRef = useRef(false);
  const closeMenu = () => {
    setMenuOpen(false);
    // Give a select handler the chance to queue a dialog before we tear everything down.
    setTimeout(() => {
      if (!confirmRef.current) onDone();
    }, 0);
  };
  const ask = (c: Confirm) => {
    confirmRef.current = true;
    setConfirm(c);
  };
  const dialog = (
    <ConfirmDialog
      c={confirm}
      onDone={() => {
        confirmRef.current = false;
        setConfirm(null);
        onDone();
      }}
    />
  );
  return { menuOpen, closeMenu, ask, dialog };
}
function fail(e: unknown) {
  toast(errorMessage(e), "error");
}
async function attempt(label: string, fn: () => Promise<unknown>, done?: () => void) {
  try {
    await fn();
    toast(label);
  } catch (e) {
    fail(e);
  } finally {
    done?.();
  }
}
/* ── service card ─────────────────────────────────────────────────── */
export function ServiceContextMenu({
  x,
  y,
  svc,
  envId,
  envName,
  onDone,
}: {
  x: number;
  y: number;
  svc: CanvasService;
  envId: string;
  envName: string;
  onDone: () => void;
}) {
  const { menuOpen, closeMenu, ask, dialog } = useMenuShell(onDone);
  const router = useRouter();
  const href = useCtxHref();
  const { go } = useCanvasState();
  const actions = useServiceActions(svc.id, envId);
  const canEdit = useCaps(svc.project_id).canEdit;
  const qc = useQueryClient();
  const deps = useDeployments(svc.id, envId);
  const nodes = useNodes();
  const inst = svc.instance;
  const domain = inst.domains[0]?.host;
  const running = ["online", "crashed", "failed", "sleeping", "scheduled"].includes(inst.status);
  const stopped = inst.status === "offline";
  const redeployId = inst.current_deployment_id ?? svc.latest_deployment?.id ?? null;
  const rollbackTo = (deps.data ?? []).find((d) => canRollback(d, inst.current_deployment_id));
  const items: MenuEntry[] = useMemo(() => {
    const openTab = (tab: PanelTab) => () => go({ svc: svc.id, tab, dep: null });
    const deployItem: MenuEntry = redeployId
      ? {
          label: t("재배포"),
          icon: <Rocket className="size-[16px]" />,
          disabled: inst.status === "deploying",
          reason: t("배포가 진행 중입니다"),
          onSelect: () => attempt(t("재배포를 시작했습니다"), () => api.redeploy(redeployId), actions.refresh),
        }
      : {
          label: t("배포"),
          icon: <Rocket className="size-[16px]" />,
          onSelect: () => attempt(t("배포를 시작했습니다"), () => api.deploy(svc.id, envId), actions.refresh),
        };
    const rollbackItem: MenuEntry = {
      label: t("이전 배포로 롤백"),
      icon: <History className="size-[16px]" />,
      disabled: !rollbackTo,
      reason: deps.isPending ? t("배포 기록을 불러오는 중입니다") : t("롤백할 수 있는 이전 배포가 없습니다"),
      onSelect: () =>
        rollbackTo &&
        ask({
          title: t("이전 배포로 롤백"),
          message: t("{name}을(를) 이전 성공 배포({image})로 되돌립니다.", { name: svc.name, image: rollbackTo.image ?? rollbackTo.id.slice(0, 8) }),
          confirmLabel: t("롤백"),
          run: async () => {
            await api.rollback(rollbackTo.id);
            actions.refresh();
            toast(t("롤백을 시작했습니다"));
          },
        }),
    };
    // aven-server 자신(self 서비스): 컨테이너·변수·도메인·노드가 Aven 밖에 있어 재시작·중지·로그·노드 이동·삭제는 없다.
    if (inst.self) {
      return [
        { label: t("서비스 열기"), icon: <Box className="size-[16px]" />, onSelect: () => go({ svc: svc.id, tab: "deployments", dep: null }) },
        { label: t("설정"), icon: <Settings className="size-[16px]" />, onSelect: openTab("settings") },
        { kind: "sep" },
        ...lockItems(canEdit, [deployItem, rollbackItem]),
        { kind: "sep" },
        {
          label: t("복사"),
          icon: <Copy className="size-[16px]" />,
          children: [
            { label: t("서비스 이름"), icon: <Box className="size-[16px]" />, onSelect: () => copyText(svc.name, t("서비스 이름")) },
            { label: t("서비스 ID"), icon: <KeyRound className="size-[16px]" />, onSelect: () => copyText(svc.id, t("서비스 ID")) },
          ],
        },
      ];
    }
    const list: MenuEntry[] = [
      { label: t("서비스 열기"), icon: <Box className="size-[16px]" />, onSelect: () => go({ svc: svc.id, tab: "deployments", dep: null }) },
      { label: t("배포"), icon: <Rocket className="size-[16px]" />, onSelect: openTab("deployments") },
      { label: t("변수"), icon: <Braces className="size-[16px]" />, onSelect: openTab("variables") },
      { label: t("지표"), icon: <Activity className="size-[16px]" />, onSelect: openTab("metrics") },
      ...(svc.source_type === "database"
        ? [{ label: t("데이터"), icon: <Database className="size-[16px]" />, onSelect: openTab("data") } as MenuEntry]
        : []),
      { label: t("로그"), icon: <FileText className="size-[16px]" />, onSelect: () => router.push(href("/logs/", { services: svc.id })) },
      { label: t("설정"), icon: <Settings className="size-[16px]" />, onSelect: openTab("settings") },
      { kind: "sep" },
      // 배포/재시작/중지/롤백은 member 이상(viewer는 비활성 + "권한이 없습니다")
      ...lockItems(canEdit, [
      deployItem,
      {
        label: t("재시작"),
        icon: <RotateCw className="size-[16px]" />,
        disabled: !inst.current_deployment_id || stopped,
        reason: stopped ? t("서비스가 중지되어 있습니다") : t("실행 중인 배포가 없습니다"),
        onSelect: () => attempt(t("재시작을 요청했습니다"), () => api.restart(svc.id, envId), actions.refresh),
      },
      stopped
        ? {
            label: t("시작"),
            icon: <Play className="size-[16px]" />,
            onSelect: () => attempt(t("배포를 시작했습니다"), () => api.deploy(svc.id, envId), actions.refresh),
          }
        : {
            label: t("중지"),
            icon: <PauseCircle className="size-[16px]" />,
            disabled: !running,
            reason: t("실행 중인 컨테이너가 없습니다"),
            onSelect: () =>
              ask({
                title: t("서비스 중지"),
                message: t("{env} 환경에서 실행 중인 컨테이너를 멈춥니다. 다시 배포할 때까지 접속할 수 없습니다.", { env: envName }),
                confirmLabel: t("중지"),
                danger: true,
                run: async () => {
                  await api.stop(svc.id, envId);
                  actions.refresh();
                  toast(t("서비스를 중지했습니다"));
                },
              }),
          },
      rollbackItem,
      ]),
      { kind: "sep" },
      {
        label: t("도메인 열기"),
        icon: <ExternalLink className="size-[16px]" />,
        disabled: !domain,
        reason: t("연결된 도메인이 없습니다"),
        onSelect: () => domain && window.open(`https://${domain}`, "_blank", "noopener"),
      },
      {
        label: t("복사"),
        icon: <Copy className="size-[16px]" />,
        children: [
          { label: t("서비스 이름"), icon: <Box className="size-[16px]" />, onSelect: () => copyText(svc.name, t("서비스 이름")) },
          { label: t("서비스 ID"), icon: <KeyRound className="size-[16px]" />, onSelect: () => copyText(svc.id, t("서비스 ID")) },
          {
            label: t("사설 도메인"),
            icon: <Link2 className="size-[16px]" />,
            disabled: !inst.private_domain,
            reason: t("사설 도메인이 없습니다"),
            onSelect: () => copyText(inst.private_domain, t("사설 도메인")),
          },
          {
            label: t("공개 URL"),
            icon: <Globe className="size-[16px]" />,
            disabled: !domain,
            reason: t("연결된 도메인이 없습니다"),
            onSelect: () => domain && copyText(`https://${domain}`, t("공개 URL")),
          },
        ],
      },
    ];
    const ns = nodes.data ?? [];
    if (ns.length > 1) {
      list.push({
        label: t("노드 이동"),
        icon: <Server className="size-[16px]" />,
        disabled: !canEdit,
        reason: NO_PERM,
        children: ns.map((n) => {
          const cur = n.id === inst.node_id;
          const off = n.status !== "online" || !n.schedulable;
          return {
            label: n.name,
            checked: cur,
            disabled: cur || off,
            reason: cur ? t("현재 노드입니다") : n.status !== "online" ? t("노드가 오프라인입니다") : t("스케줄할 수 없는 노드입니다"),
            onSelect: () => {
              if (svc.volumes.length > 0) {
                toast(t("볼륨이 있는 서비스는 설정에서 노드를 옮기세요"));
                go({ svc: svc.id, tab: "settings", dep: null });
                return;
              }
              attempt(t("다음 배포부터 {node} 노드에서 실행됩니다", { node: n.name }), () => api.updateInstance(svc.id, envId, { node_id: n.id }, false), actions.refresh);
            },
          } as MenuEntry;
        }),
      });
    }
    list.push(
      { kind: "sep" },
      {
        label: t("삭제"),
        icon: <Trash2 className="size-[16px]" />,
        danger: true,
        disabled: !canEdit,
        reason: NO_PERM,
        onSelect: () =>
          ask({
            title: t("{name} 삭제", { name: svc.name }),
            message: (
              <>
                {tr("{name} 서비스를 {scope} 삭제합니다. 컨테이너, 도메인, 배포 기록이 지워지고 되돌릴 수 없습니다.", {
                  name: <span className="font-mono text-foreground">{svc.name}</span>,
                  scope: <b className="text-foreground">{t("모든 환경에서")}</b>,
                })}
              </>
            ),
            confirmLabel: t("삭제"),
            danger: true,
            typed: svc.name,
            run: async () => {
              await api.deleteService(svc.id);
              qc.invalidateQueries({ queryKey: ["project"] });
              actions.refresh();
              go({ svc: null, dep: null, tab: "deployments" }, "replace");
              toast(t("{name}을(를) 삭제했습니다", { name: svc.name }));
            },
          }),
      },
    );
    return list;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [svc, envId, envName, rollbackTo?.id, deps.isPending, nodes.data, redeployId, canEdit]);
  return (
    <>
      {menuOpen && <ContextMenu x={x} y={y} items={items} onClose={closeMenu} />}
      {dialog}
    </>
  );
}
/* ── volume row ───────────────────────────────────────────────────── */
export function VolumeContextMenu({
  x,
  y,
  svc,
  vol,
  onDone,
}: {
  x: number;
  y: number;
  svc: CanvasService;
  vol: Volume;
  onDone: () => void;
}) {
  const { menuOpen, closeMenu, dialog } = useMenuShell(onDone);
  const { go } = useCanvasState();
  const qc = useQueryClient();
  const canEdit = useCaps(svc.project_id).canEdit;
  const items: MenuEntry[] = [
    {
      label: t("백업 만들기"),
      icon: <Archive className="size-[16px]" />,
      disabled: !canEdit,
      reason: NO_PERM,
      onSelect: () =>
        attempt(
          t("백업을 시작했습니다"),
          () => api.createBackup(vol.id),
          () => qc.invalidateQueries({ queryKey: qk.backups(vol.id) }),
        ),
    },
    { label: t("백업 목록"), icon: <HardDrive className="size-[16px]" />, onSelect: () => go({ svc: svc.id, tab: "settings", dep: null }) },
    { kind: "sep" },
    { label: t("볼륨 ID 복사"), icon: <Copy className="size-[16px]" />, onSelect: () => copyText(vol.id, t("볼륨 ID")) },
  ];
  return (
    <>
      {menuOpen && <ContextMenu x={x} y={y} items={items} onClose={closeMenu} />}
      {dialog}
    </>
  );
}
/* ── empty canvas ─────────────────────────────────────────────────── */
const DB_ITEMS: { kind: DatabaseKind; label: string }[] = [
  { kind: "postgres", label: "PostgreSQL" },
  { kind: "redis", label: "Redis" },
  { kind: "mysql", label: "MySQL" },
  { kind: "mongo", label: "MongoDB" },
];
export function CanvasContextMenu({
  x,
  y,
  at,
  pid,
  envId,
  showEdges,
  selection = [],
  onCreate,
  onPlaceNext,
  onAutoLayout,
  onFit,
  onToggleEdges,
  onNewGroup,
  onDeleted,
  onDone,
}: {
  x: number;
  y: number;
  /** Canvas-space point under the cursor. */
  at: { x: number; y: number };
  pid?: string;
  envId: string;
  showEdges: boolean;
  selection?: string[];
  onCreate: (view: NewView) => void;
  /** Remember `at` for the next service the create flow produces. */
  onPlaceNext: (at: { x: number; y: number } | null) => void;
  onAutoLayout: () => void;
  onFit: () => void;
  onToggleEdges: () => void;
  /** Opens the group dialog for `selection`. The entry is hidden without it. */
  onNewGroup?: () => void;
  /** Called after the selected services were deleted (e.g. to clear the selection). */
  onDeleted?: () => void;
  onDone: () => void;
}) {
  const { menuOpen, closeMenu, ask, dialog } = useMenuShell(onDone);
  const qc = useQueryClient();
  const { go } = useCanvasState();
  const canEdit = useCaps(pid).canEdit;
  const flow = (v: NewView, placed = true) => () => {
    onPlaceNext(placed ? at : null);
    onCreate(v);
  };
  /** Creates a service at the cursor. `openTab` opens its panel afterwards. */
  const createAt = (input: Parameters<typeof api.createService>[1], label: string, openTab?: PanelTab) => async () => {
    if (!pid) return;
    try {
      const s = await api.createService(pid, input);
      await api.setCanvasPos(s.id, envId, at).catch(() => undefined);
      await Promise.all([qc.invalidateQueries({ queryKey: qk.canvas(envId) }), qc.invalidateQueries({ queryKey: qk.project(pid) })]);
      if (openTab) go({ isNew: null, svc: s.id, tab: openTab, dep: null });
      else toast(t("{label} 서비스를 만들었습니다", { label }));
    } catch (e) {
      fail(e);
    }
  };
  const names = new Map((qc.getQueryData<{ services: CanvasService[] }>(qk.canvas(envId))?.services ?? []).map((s) => [s.id, s.name]));
  const selected = selection.filter((id) => names.has(id));
  const multi: MenuEntry[] =
    selected.length > 0
      ? [
          ...lockItems(canEdit, [
            ...(onNewGroup ? [{ label: t("새 그룹"), icon: <Boxes className="size-[16px]" />, onSelect: onNewGroup } as MenuEntry] : []),
            {
              label: t("서비스 삭제"),
              icon: <Trash2 className="size-[16px]" />,
              danger: true,
              onSelect: () =>
                ask({
                  title: t("서비스 {n}개 삭제", { n: selected.length }),
                  message: (
                    <>
                      {tr("{names}을(를) {scope} 삭제합니다. 컨테이너, 도메인, 배포 기록이 지워지고 되돌릴 수 없습니다.", {
                        names: <span className="font-mono text-foreground">{selected.map((id) => names.get(id)).join(", ")}</span>,
                        scope: <b className="text-foreground">{t("모든 환경에서")}</b>,
                      })}
                    </>
                  ),
                  confirmLabel: t("삭제"),
                  danger: true,
                  typed: t("삭제|typed"),
                  run: async () => {
                    const results = await Promise.allSettled(selected.map((id) => api.deleteService(id)));
                    await Promise.all([qc.invalidateQueries({ queryKey: qk.canvas(envId) }), qc.invalidateQueries({ queryKey: ["project"] })]);
                    onDeleted?.();
                    const failed = results.find((r): r is PromiseRejectedResult => r.status === "rejected");
                    if (failed) throw failed.reason;
                    toast(t("서비스 {n}개를 삭제했습니다", { n: selected.length }));
                  },
                }),
            },
          ]),
          { kind: "sep" },
        ]
      : [];
  const items: MenuEntry[] = [
    ...multi,
    { kind: "label", label: t("서비스 추가") },
    ...lockItems(!!pid && canEdit, [
      { label: t("GitHub 저장소"), icon: <BrandIcon name="github" className="size-[16px]" mono />, onSelect: flow("repo") },
      { label: t("템플릿|item"), icon: <LayoutTemplate className="size-[16px]" />, onSelect: flow("template") },
      {
        label: t("데이터베이스"),
        icon: <Database className="size-[16px]" />,
        children: DB_ITEMS.map((d) => ({
          label: d.label,
          icon: <BrandIcon name={d.kind} className="size-[16px]" />,
          onSelect: createAt({ source_type: "database", database_kind: d.kind, environment_id: envId }, d.label),
        })),
      },
      { label: t("Docker 이미지"), icon: <BrandIcon name="docker" className="size-[16px]" mono />, onSelect: flow("image") },
      { label: t("볼륨"), icon: <HardDrive className="size-[16px]" />, onSelect: flow("volume", false) },
      { label: t("함수"), icon: <BrandIcon name="function" className="size-[16px]" />, onSelect: createAt({ source_type: "function", environment_id: envId }, t("함수"), "code") },
      {
        label: t("버킷"),
        icon: <BrandIcon name="bucket" className="size-[16px]" />,
        onSelect: createAt({ source_type: "database", database_kind: "bucket", environment_id: envId }, t("버킷")),
      },
      { label: t("빈 서비스"), icon: <SquareDashed className="size-[16px]" />, onSelect: createAt({ source_type: "image" }, t("빈"), "deployments") },
      { label: "Docker Compose", icon: <Layers className="size-[16px]" />, onSelect: flow("compose", false) },
    ]),
    { kind: "sep" },
    { label: t("자동 정렬"), icon: <AlignHorizontalDistributeCenter className="size-[16px]" />, disabled: !canEdit, reason: NO_PERM, onSelect: onAutoLayout },
    { label: t("화면에 맞추기"), icon: <Maximize className="size-[16px]" />, onSelect: onFit },
    { label: t("연결선 표시"), icon: <Link2 className="size-[16px]" />, checked: showEdges, onSelect: onToggleEdges },
  ];
  return (
    <>
      {menuOpen && <ContextMenu x={x} y={y} items={items} onClose={closeMenu} />}
      {dialog}
    </>
  );
}


/* ── stack frame ──────────────────────────────────────────────────── */
export function StackContextMenu({
  x,
  y,
  name,
  deploying,
  canEdit = true,
  onRedeploy,
  onDelete,
  onDone,
}: {
  x: number;
  y: number;
  name: string;
  deploying: boolean;
  /** member 이상만 재배포/삭제할 수 있다. */
  canEdit?: boolean;
  onRedeploy: () => Promise<unknown>;
  onDelete: () => Promise<unknown>;
  onDone: () => void;
}) {
  const { menuOpen, closeMenu, ask, dialog } = useMenuShell(onDone);
  const items: MenuEntry[] = lockItems(canEdit, [
    {
      label: t("스택 재배포"),
      icon: <Rocket className="size-[16px]" />,
      disabled: deploying,
      reason: t("스택 배포가 진행 중입니다"),
      onSelect: () =>
        ask({
          title: t("스택 {name} 전체 재배포", { name }),
          message: t("모든 서비스를 의존 순서대로 다시 배포합니다."),
          confirmLabel: t("재배포"),
          run: async () => {
            await onRedeploy();
            toast(t("스택 재배포를 시작했습니다"));
          },
        }),
    },
    { kind: "sep" },
    {
      label: t("스택 삭제"),
      icon: <Trash2 className="size-[16px]" />,
      danger: true,
      onSelect: () =>
        ask({
          title: t("스택 {name} 삭제", { name }),
          message: t("스택의 모든 서비스를 컨테이너, 볼륨, 도메인과 함께 삭제합니다. 되돌릴 수 없습니다."),
          confirmLabel: t("삭제"),
          danger: true,
          typed: name,
          run: async () => {
            await onDelete();
            toast(t("스택 {name}을(를) 삭제했습니다", { name }));
          },
        }),
    },
  ]);
  return (
    <>
      {menuOpen && <ContextMenu x={x} y={y} items={items} onClose={closeMenu} />}
      {dialog}
    </>
  );
}

