"use client";


import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ArrowDown, Crown, Network, Server } from "lucide-react";
import type { CanvasService, Environment } from "@/lib/api";
import { qk, useCaps, useNodes } from "@/lib/queries";
import { fmtBytes } from "@/lib/format";
import { toast } from "@/lib/uiState";
import { HA_ENGINE_LABEL, HA_ROLE_LABEL, haApi, haKeys, useHAStatus, type HACluster, type HAMember, type HASelector } from "@/lib/ha";
import { lock } from "../Perm";
import { Button, Card, CopyField, ErrorBox, LABEL_CLS, Segmented, Spinner, Toggle, cx, errorMessage, useConfirm } from "../ui";
import { t, tr } from "@/lib/i18n";
import { useTypedConfirm } from "./dataKit";

const STATUS_LABEL: Record<HACluster["status"], string> = { provisioning: t("구성 중"), active: t("정상"), switching: t("주 노드 전환 중") };

function Selector({ sel, value, onChange }: { sel: HASelector; value: number; onChange: (n: number) => void }) {
  return (
    <li className="flex flex-wrap items-center justify-between gap-3 px-4 py-3">
      <div className="min-w-0">
        <div className="text-sm font-medium">{sel.label}</div>
        {sel.description && <div className="mt-0.5 text-sm text-gray-500">{sel.description}</div>}
      </div>
      <Segmented
        label={sel.label}
        value={String(value)}
        onChange={(v) => onChange(Number(v))}
        options={sel.options.map((n) => ({ value: String(n), label: String(n) }))}
      />
    </li>
  );
}

export function HASection({ svc, env }: { svc: CanvasService; env: Environment }) {
  const qc = useQueryClient();
  const caps = useCaps(svc.project_id);
  const st = useHAStatus(svc.id, env.id);
  const [counts, setCounts] = useState<Record<string, number>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const { confirm, node: confirmNode } = useConfirm();

  const companion = st.data?.companion;
  useEffect(() => {
    if (companion) setCounts((c) => (Object.keys(c).length ? c : Object.fromEntries(companion.selectors.map((s) => [s.role, s.default]))));
  }, [companion]);

  const refresh = async () => {
    await Promise.all([
      qc.invalidateQueries({ queryKey: ["ha"] }),
      qc.invalidateQueries({ queryKey: haKeys.env(env.id) }),
      qc.invalidateQueries({ queryKey: qk.canvas(env.id) }),
    ]);
  };

  if (st.isPending) {
    return (
      <Card title={t("고가용성")}>
        <Spinner />
      </Card>
    );
  }
  if (st.error)
    return (
      <Card title={t("고가용성")}>
        <ErrorBox error={st.error} />
      </Card>
    );
  const data = st.data;
  if (!data.cluster && !data.companion && data.problems.length === 0) return null;

  if (data.cluster) return <ClusterOverview svc={svc} env={env} cluster={data.cluster} canEdit={caps.canEdit} refresh={refresh} />;

  const convert = () =>
    confirm({
      title: t("고가용성 클러스터로 전환"),
      confirmLabel: t("HA로 전환"),
      message: (
        <div className="space-y-3">
          <p>
            {tr("{service}을(를) {target}로 전환합니다.", {
              service: <b>{svc.name}</b>,
              target: companion?.name ?? t("HA 클러스터"),
            })}
          </p>
          <ul className="list-disc space-y-1.5 pl-5">
            <li>
              {tr("전환하는 동안 {bold}(원본이 클러스터 설정으로 다시 시작합니다).", { bold: <b>{t("현재 연결이 끊깁니다")}</b> })}
            </li>
            <li>
              {tr("{bold} 이 환경에서 {svc}의 연결 변수를 참조하는 서비스는 클러스터 접속 지점을 보도록 바뀌며, 다시 배포하면 적용됩니다. 직접 적어 둔 연결 문자열은 직접 바꾸세요.", {
                bold: <b>{t("접속 지점이 바뀝니다.")}</b>,
                svc: svc.name,
              })}
            </li>
            <li>{t("전환 전에 데이터 볼륨을 백업합니다.")}</li>
            <li>{t("replica는 이 환경에만 만들어지고, 가능한 한 서로 다른 노드에 놓입니다.")}</li>
          </ul>
        </div>
      ),
      run: async () => {
        setBusy(true);
        setError(null);
        try {
          const res = await haApi.convert(svc.id, env.id, counts);
          toast(
            res.migrated_services.length
              ? t("HA로 전환하는 중입니다. 참조를 바꾼 서비스: {services}", { services: res.migrated_services.join(", ") })
              : t("HA로 전환하는 중입니다"),
            "success",
          );
          await refresh();
        } catch (e) {
          setError(e);
        } finally {
          setBusy(false);
        }
      },
    });

  return (
    <Card
      title={t("고가용성")}
      description={companion?.description ?? t("이 데이터베이스를 여러 노드에 복제하고 주 노드가 멈추면 자동으로 다른 노드로 넘어가게 합니다.")}
      footer={
        companion && (
          <Button variant="primary" onClick={convert} loading={busy} disabled={!data.eligible} {...lock(caps.canEdit)}>
            {t("HA로 전환")}
          </Button>
        )
      }
      footerNote={companion?.version && t("버전 {version} · 지원 {versions}", { version: companion.version, versions: companion.supported_versions.join(", ") })}
    >
      {data.problems.length > 0 && (
        <div className="mb-4 space-y-2 rounded-md bg-warn-surface p-3 text-sm text-warn">
          {data.problems.map((p) => (
            <div key={p} className="flex gap-2">
              <AlertTriangle className="mt-0.5 size-4 shrink-0" />
              <span>{p}</span>
            </div>
          ))}
        </div>
      )}
      {companion && (
        <ul className="divide-y divide-gray-200 rounded-lg border border-gray-200 bg-secondaryBg">
          {companion.selectors.map((s) => (
            <Selector key={s.role} sel={s} value={counts[s.role] ?? s.default} onChange={(n) => setCounts({ ...counts, [s.role]: n })} />
          ))}
        </ul>
      )}
      {error ? <ErrorBox className="mt-3" error={error} /> : null}
      {confirmNode}
    </Card>
  );
}

function roleBadge(m: HAMember) {
  if (m.role === "edge") return <span className="rounded bg-gray-100 px-1.5 py-0.5 text-xs text-gray-600">{t("프록시")}</span>;
  if (m.role === "internal") return <span className="rounded bg-gray-100 px-1.5 py-0.5 text-xs text-gray-600">{t("조정 노드")}</span>;
  if (m.db_role === "primary")
    return (
      <span className="inline-flex items-center gap-1 rounded bg-pink-100 px-1.5 py-0.5 text-xs font-medium text-pink-600">
        <Crown className="size-3" /> {t("주 노드")}
      </span>
    );
  if (m.db_role === "replica") return <span className="rounded bg-gray-100 px-1.5 py-0.5 text-xs text-gray-600">replica</span>;
  return <span className="rounded bg-gray-100 px-1.5 py-0.5 text-xs text-gray-500">{m.db_role === "uninitialized" ? t("초기화 전") : t("알 수 없음")}</span>;
}

function ClusterOverview({
  svc,
  env,
  cluster,
  canEdit,
  refresh,
}: {
  svc: CanvasService;
  env: Environment;
  cluster: HACluster;
  canEdit: boolean;
  refresh: () => Promise<void>;
}) {
  const nodes = useNodes();
  const { confirm, node: confirmNode } = useConfirm();
  const typed = useTypedConfirm();
  const [error, setError] = useState<unknown>(null);
  const root = cluster.members.find((m) => m.role === "root");
  const replicas = cluster.members.filter((m) => m.role === "replica");
  const rootIsPrimary = cluster.primary_service_id === cluster.root_service_id && root?.db_role === "primary";
  const nodeName = (id: string) => nodes.data?.find((n) => n.id === id)?.name ?? id;
  const multiNode = new Set(cluster.members.map((m) => m.node_id)).size > 1;

  const run = async (f: () => Promise<unknown>, ok: string) => {
    setError(null);
    try {
      await f();
      toast(ok, "success");
      await refresh();
    } catch (e) {
      setError(e);
      toast(errorMessage(e), "error");
    }
  };

  const makeLeader = (m: HAMember) =>
    confirm({
      title: t("{name}를 주 노드로", { name: m.name }),
      confirmLabel: t("주 노드로 만들기"),
      message: (
        <p>
          {tr("주 노드를 {name}(으)로 옮깁니다(switchover). failover처럼 연결이 잠깐 끊기고, 클라이언트가 다시 연결하면 새 주 노드로 이어집니다.", { name: <b>{m.name}</b> })}
        </p>
      ),
      run: () => run(() => haApi.switchover(svc.id, env.id, m.service_id), t("{name}로 주 노드를 옮기는 중입니다", { name: m.name })),
    });

  const revert = () =>
    typed.confirm({
      title: t("단독 데이터베이스로 되돌리기"),
      confirmLabel: t("되돌리기"),
      expected: svc.name,
      message: (
        <ul className="list-disc space-y-1.5 pl-5">
          <li>{tr("replica, 프록시, 조정 노드 서비스를 모두 지웁니다. {bold}(확인한 뒤 직접 지우세요).", { bold: <b>{t("그 볼륨은 지우지 않고 남깁니다")}</b> })}</li>
          <li>{t("공개 접속(TCP 프록시)이 있었다면 원본 서비스로 옮깁니다.")}</li>
          <li>{t("클러스터 접속 지점을 참조하던 변수를 원본 서비스로 되돌립니다(다시 배포하면 적용).")}</li>
        </ul>
      ),
      // Errors stay in the dialog.
      run: async () => {
        await haApi.revert(svc.id, env.id);
        toast(t("단독 데이터베이스로 되돌렸습니다"), "success");
        await refresh();
      },
    });

  const setFailover = (v: boolean) => {
    if (v) return void run(() => haApi.update(svc.id, env.id, { auto_failover: true }), t("자동 failover를 켰습니다"));
    confirm({
      title: t("자동 failover를 끌까요?"),
      message: t("끄면 주 노드가 멈춰도 replica를 승격하지 않습니다. 직접 주 노드를 바꿀 때까지 데이터베이스에 쓸 수 없습니다."),
      confirmLabel: t("끄기"),
      danger: true,
      run: () => run(() => haApi.update(svc.id, env.id, { auto_failover: false }), t("자동 failover를 껐습니다")),
    });
  };

  const scale = (n: number) => {
    const doIt = () => run(() => haApi.update(svc.id, env.id, { replicas: n }), t("replica를 {count}개로 바꿨습니다", { count: n }));
    if (n >= replicas.length) return void doIt();
    const gone = [...replicas].sort((a, b) => b.ordinal - a.ordinal).slice(0, replicas.length - n);
    confirm({
      title: t("replica를 {count}개로 줄일까요?", { count: n }),
      message: (
        <div className="space-y-2">
          <p>{t("다음 replica 서비스를 지웁니다. 볼륨은 남깁니다.")}</p>
          <ul className="list-disc pl-5 font-mono text-xs text-foreground">
            {gone.map((m) => (
              <li key={m.service_id}>
                {m.name}
                {m.db_role === "primary" && <span className="ml-1 font-sans text-danger">({t("지금 주 노드")})</span>}
              </li>
            ))}
          </ul>
          <p>{t("남는 노드가 적을수록 장애를 견디는 힘이 줄어듭니다.")}</p>
        </div>
      ),
      confirmLabel: t("줄이기"),
      danger: true,
      run: doIt,
    });
  };

  const replicaSel = cluster.counts.replica;

  return (
    <Card
      title={
        <span className="flex flex-wrap items-center gap-2">
          {t("고가용성 클러스터")}
          <span
            className={cx(
              "rounded px-1.5 py-0.5 text-xs font-medium",
              cluster.status === "active" ? "bg-success-surface text-success" : "bg-warn-surface text-warn",
            )}
          >
            {STATUS_LABEL[cluster.status]}
          </span>
          {cluster.busy && <Spinner className="size-3.5" />}
        </span>
      }
      description={HA_ENGINE_LABEL[cluster.kind]}
    >
      {cluster.message && <div className="mb-4 rounded-md bg-gray-50 px-3 py-2 text-sm text-gray-600">{cluster.message}</div>}

      {cluster.endpoint && (
        <div className="mb-5">
          <div className={LABEL_CLS}>{t("접속 지점 (비공개 네트워크)")}</div>
          <CopyField value={`\${{ ${cluster.endpoint.name}.${cluster.endpoint.url_variable} }}`} label={t("변수 참조")} />
          <div className="mt-1.5 text-xs text-gray-500">
            {t("{host} · 개별 노드 대신 이 접속 지점에 연결하면 failover 뒤에도 그대로 이어집니다.", { host: cluster.endpoint.private_host })}
          </div>
        </div>
      )}

      <Topology cluster={cluster} nodeName={multiNode ? nodeName : undefined} />

      <div className="overflow-x-auto rounded-lg border border-gray-200">
        <table className="w-full text-sm">
          <caption className="sr-only">{t("클러스터 구성원")}</caption>
          <thead className="bg-gray-50 text-left text-xs text-gray-600">
            <tr>
              <th className="px-3 py-2 font-semibold">{t("구성원")}</th>
              <th className="px-3 py-2 font-semibold">{t("역할")}</th>
              <th className="px-3 py-2 font-semibold">{t("상태")}</th>
              <th className="px-3 py-2 font-semibold">{t("복제 지연")}</th>
              {multiNode && <th className="px-3 py-2 font-semibold">{t("노드")}</th>}
              <th className="px-3 py-2" />
            </tr>
          </thead>
          <tbody>
            {cluster.members.map((m) => (
              <tr key={m.service_id} className="border-t border-gray-200">
                <td className="px-3 py-2">
                  <div className="font-medium">{m.name}</div>
                  <div className="text-xs text-gray-500">{HA_ROLE_LABEL[m.role]}</div>
                </td>
                <td className="px-3 py-2">{roleBadge(m)}</td>
                <td className="px-3 py-2">
                  <span className={cx("inline-flex items-center gap-1.5", m.healthy ? "text-success" : "text-red-500")}>
                    <span className={cx("size-2 rounded-full", m.healthy ? "bg-success" : "bg-red-500")} />
                    {m.healthy ? t("정상") : m.instance_status === "deploying" ? t("배포 중") : t("비정상")}
                  </span>
                  {m.error && !m.healthy && <div className="max-w-[260px] truncate text-xs text-gray-500" title={m.error}>{m.error}</div>}
                </td>
                <td className="px-3 py-2 tabular-nums text-gray-600">{m.lag_bytes !== undefined ? fmtBytes(m.lag_bytes) : "—"}</td>
                {multiNode && <td className="px-3 py-2 text-gray-600">{nodeName(m.node_id)}</td>}
                <td className="px-3 py-2 text-right">
                  {(m.role === "root" || m.role === "replica") && m.db_role === "replica" && m.healthy && (
                    <Button size="sm" variant="outline" disabled={cluster.busy} onClick={() => makeLeader(m)} {...lock(canEdit)}>
                      {t("주 노드로 만들기")}
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <div className="mt-5 divide-y divide-gray-200 rounded-lg border border-gray-200 bg-secondaryBg">
        {(cluster.aven_failover ?? cluster.keel_failover) && (
          <div className="flex items-center justify-between gap-3 px-4 py-3">
            <div>
              <div className="text-sm font-medium">{t("자동 failover")}</div>
              <div className="mt-0.5 text-sm text-gray-500">{t("끄면 주 노드가 멈춰도 Aven이 replica를 승격하지 않습니다(직접 주 노드로 만들어야 합니다).")}</div>
            </div>
            <Toggle
              label={t("자동 failover")}
              checked={cluster.auto_failover}
              disabled={!canEdit}
              onChange={setFailover}
            />
          </div>
        )}
        {replicaSel !== undefined && cluster.replica_options.length > 0 && (
          <ReplicaScale cluster={cluster} canEdit={canEdit} onScale={scale} count={replicas.length} />
        )}
        <div className="flex items-center justify-between gap-3 px-4 py-3">
          <div>
            <div className="text-sm font-medium">{t("단독으로 되돌리기")}</div>
            <div className="mt-0.5 text-sm text-gray-500">
              {rootIsPrimary
                ? t("원본 서비스만 남기고 클러스터 서비스를 지웁니다.")
                : t("원본 서비스({name})가 주 노드일 때만 되돌릴 수 있습니다. 먼저 원본을 주 노드로 만드세요.", { name: root?.name ?? "root" })}
            </div>
          </div>
          <div className="flex gap-2">
            {!rootIsPrimary && root && root.db_role === "replica" && root.healthy && (
              <Button size="sm" variant="outline" disabled={cluster.busy} onClick={() => makeLeader(root)} {...lock(canEdit)}>
                {t("원본을 주 노드로")}
              </Button>
            )}
            <Button size="sm" variant="danger" disabled={!rootIsPrimary || cluster.busy} onClick={revert} {...lock(canEdit)}>
              {t("되돌리기")}
            </Button>
          </div>
        </div>
      </div>
      {error ? <ErrorBox className="mt-3" error={error} /> : null}
      {confirmNode}
      {typed.node}
    </Card>
  );
}

/** Lag above this many bytes is shown as a warning. */
const LAG_WARN = 16 << 20;

/**
 * Cluster at a glance: the endpoint clients use, the primary, and the replicas streaming from it with their lag.
 * Proxies and coordinators sit beside the data path. The member table below has the same data in text form.
 */
function Topology({ cluster, nodeName }: { cluster: HACluster; nodeName?: (id: string) => string }) {
  const data = cluster.members.filter((m) => m.role === "root" || m.role === "replica");
  const primary = data.find((m) => m.db_role === "primary") ?? data.find((m) => m.service_id === cluster.primary_service_id);
  const followers = data.filter((m) => m !== primary);
  const side = cluster.members.filter((m) => m.role === "edge" || m.role === "internal");
  const maxLag = Math.max(LAG_WARN, ...followers.map((m) => m.lag_bytes ?? 0));
  const box = (m: HAMember, big?: boolean) => (
    <div
      key={m.service_id}
      className={cx(
        "min-w-0 rounded-lg border bg-secondaryBg px-3 py-2",
        big ? "border-pink-400" : "border-gray-200",
        !m.healthy && "border-red-400 bg-danger-surface",
      )}
    >
      <div className="flex min-w-0 items-center gap-1.5 text-sm font-medium">
        <span className={cx("size-2 shrink-0 rounded-full", m.healthy ? "bg-success" : "bg-red-500")} aria-hidden />
        <span className="truncate">{m.name}</span>
      </div>
      <div className="mt-1 flex flex-wrap items-center gap-1.5">
        {roleBadge(m)}
        {nodeName && (
          <span className="inline-flex items-center gap-1 text-xs text-gray-500">
            <Server className="size-3" aria-hidden /> {nodeName(m.node_id)}
          </span>
        )}
      </div>
    </div>
  );
  return (
    <figure className="mb-5 rounded-lg border border-gray-200 p-4" aria-label={t("클러스터 구성도")}>
      <div className="flex flex-col items-center gap-1">
        {cluster.endpoint && (
          <>
            <div className="flex items-center gap-1.5 rounded-full border border-gray-200 px-3 py-1 font-mono text-xs text-gray-600">
              <Network className="size-3.5" aria-hidden /> {cluster.endpoint.private_host}
            </div>
            <ArrowDown className="size-4 text-gray-400" aria-hidden />
          </>
        )}
        <div className="w-full max-w-[260px]">{primary ? box(primary, true) : <div className="rounded-lg border border-dashed border-red-400 px-3 py-2 text-center text-sm text-danger">{t("주 노드 없음")}</div>}</div>
      </div>
      {followers.length > 0 && (
        <>
          <div className="mx-auto mt-1 h-3 w-px bg-gray-300" aria-hidden />
          <ul className="grid gap-3 border-t border-gray-300 pt-3 sm:grid-cols-[repeat(auto-fit,minmax(180px,1fr))]">
            {followers.map((m) => {
              const lag = m.lag_bytes;
              const high = lag !== undefined && lag > LAG_WARN;
              return (
                <li key={m.service_id} className="min-w-0 space-y-1.5">
                  {box(m)}
                  <div className="px-1 text-xs text-gray-500">
                    <div className="flex items-center justify-between gap-2">
                      <span className={cx(!m.streaming && "text-warn")}>{m.streaming ? t("복제 중") : t("복제 끊김")}</span>
                      <span className={cx("tabular-nums", high && "text-warn")}>{lag !== undefined ? t("지연 {lag}", { lag: fmtBytes(lag) }) : "—"}</span>
                    </div>
                    {lag !== undefined && (
                      <div className="mt-1 h-1 overflow-hidden rounded-full bg-gray-100" aria-hidden>
                        <div className={cx("h-full", high ? "bg-yellow-500" : "bg-success")} style={{ width: `${Math.max(2, Math.min(100, (lag / maxLag) * 100))}%` }} />
                      </div>
                    )}
                  </div>
                </li>
              );
            })}
          </ul>
        </>
      )}
      {side.length > 0 && (
        <div className="mt-4 flex flex-wrap items-center gap-2 border-t border-gray-200 pt-3 text-xs text-gray-500">
          <span>{t("함께 도는 서비스")}</span>
          {side.map((m) => (
            <span key={m.service_id} className={cx("inline-flex items-center gap-1.5 rounded border px-1.5 py-0.5", m.healthy ? "border-gray-200" : "border-red-400 text-danger")}>
              <span className={cx("size-1.5 rounded-full", m.healthy ? "bg-success" : "bg-red-500")} aria-hidden />
              <span className="font-mono">{m.name}</span>
              <span>{HA_ROLE_LABEL[m.role]}</span>
            </span>
          ))}
        </div>
      )}
    </figure>
  );
}

function ReplicaScale({ cluster, count, canEdit, onScale }: { cluster: HACluster; count: number; canEdit: boolean; onScale: (n: number) => void }) {
  const [n, setN] = useState(count);
  useEffect(() => setN(count), [count]);
  // 선택지는 동반 템플릿(haConversionConfig.replica.options)에서 온다.
  const options = cluster.replica_options.includes(count) ? cluster.replica_options : [...cluster.replica_options, count].sort((a, b) => a - b);
  return (
    <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-3">
      <div>
        <div className="text-sm font-medium">{t("replica 수")}</div>
        <div className="mt-0.5 text-sm text-gray-500">{t("늘리면 새 replica가 주 노드에서 복제를 시작합니다. 줄이면 번호가 큰 replica부터 지웁니다(볼륨은 남김).")}</div>
      </div>
      <div className="flex items-center gap-2">
        <Segmented label={t("replica 수")} value={String(n)} onChange={(v) => setN(Number(v))} options={options.map((o) => ({ value: String(o), label: String(o) }))} />
        <Button size="sm" variant="outline" disabled={n === count || cluster.busy} onClick={() => onScale(n)} {...lock(canEdit)}>
          {t("적용")}
        </Button>
      </div>
    </div>
  );
}
