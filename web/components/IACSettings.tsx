"use client";

import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowDownToLine, ClipboardCheck, Copy, RefreshCw, ShieldCheck, Terminal, TriangleAlert } from "lucide-react";
import { api, type IACChange, type IACDefinition, type IACPlan } from "@/lib/api";
import { qk } from "@/lib/queries";
import { toast } from "@/lib/uiState";
import type { Workspace } from "@/lib/workspace";
import { Button, Card, Dialog, ErrorBox, Spinner, Textarea, Toggle } from "@/components/ui";
import { t, tr } from "@/lib/i18n";

function pretty(definition: IACDefinition) {
  return JSON.stringify(definition, null, 2);
}

function show(v: unknown) {
  if (v === undefined) return "";
  return typeof v === "string" ? v : JSON.stringify(v);
}

/** Groups plan changes by resource: create / destroy lines and the changed fields of updates. */
function groupChanges(changes: IACChange[]) {
  const out = new Map<string, IACChange[]>();
  for (const c of changes) out.set(c.resource, [...(out.get(c.resource) ?? []), c]);
  return [...out.entries()].sort(([a], [b]) => a.localeCompare(b));
}

function ChangeList({ plan }: { plan: IACPlan }) {
  return (
    <div className="space-y-2">
      {groupChanges(plan.changes).map(([resource, list]) => {
        const head = list.find((c) => c.action !== "change") ?? list[0];
        const label = head.action === "create" ? t("생성") : head.action === "destroy" ? t("삭제") : t("변경|action");
        return (
          <div key={resource} className="rounded-md border border-gray-200 px-3 py-2 text-sm">
            <div className="flex items-center gap-2">
              <span className={head.action === "destroy" ? "font-medium text-danger" : head.action === "create" ? "font-medium text-green-700" : "font-medium text-pink-600"}>
                {label}
              </span>
              <span className="font-mono">{resource}</span>
              {head.action === "destroy" && <span className="text-xs text-danger">{t("모든 환경에서 삭제")}</span>}
              {head.action === "create" && head.detail && <span className="text-xs text-gray-500">{head.detail}</span>}
              {list.some((c) => c.destructive) && <TriangleAlert className="ml-auto size-4 shrink-0 text-danger" aria-label={t("되돌릴 수 없는 변경")} />}
            </div>
            {list
              .filter((c) => c.action === "change")
              .map((c) => (
                <div key={c.field} className={c.destructive ? "mt-1 font-mono text-xs text-danger" : "mt-1 font-mono text-xs text-gray-600"}>
                  {c.from === undefined ? "+" : c.to === undefined ? "−" : "~"} {c.field}
                  {c.from !== undefined && c.to !== undefined && (
                    <span className="text-gray-500">
                      : {show(c.from)} → {show(c.to)}
                    </span>
                  )}
                  {c.from === undefined && c.to !== undefined && <span className="text-gray-500"> = {show(c.to)}</span>}
                </div>
              ))}
          </div>
        );
      })}
    </div>
  );
}

function Ownership({ pid, eid, canAdmin }: { pid: string; eid: string; canAdmin: boolean }) {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["iac-partials", eid], queryFn: () => api.iacPartials(pid, eid) });
  const [busy, setBusy] = useState<string | null>(null);
  const [confirmPartial, setConfirmPartial] = useState<string | null>(null);
  const byPartial = new Map<string, string[]>();
  for (const [addr, owner] of Object.entries(q.data?.iac_partials ?? {})) byPartial.set(owner, [...(byPartial.get(owner) ?? []), addr].sort());

  const release = async (partial: string) => {
    setBusy(partial);
    try {
      const res = await api.iacReleasePartial(pid, eid, { partial, base_config_etag: q.data?.config_etag });
      toast(t("partial {partial}의 소유권 {count}개를 해제했습니다. 다음 적용 전에 다시 미리 보세요.", { partial, count: res.affected_resources.length }));
      setConfirmPartial(null);
      await qc.invalidateQueries({ queryKey: ["iac-partials", eid] });
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e), "error");
    } finally {
      setBusy(null);
    }
  };

  return (
    <Card
      title={t("Named partial 소유권")}
      description={t("저장소마다 서비스를 나눠 관리할 때 partial 이름별로 어떤 서비스를 소유하는지 기록합니다. 해제는 소유권 기록만 지우며 서비스는 바꾸지 않습니다.")}
    >
      {q.isLoading ? (
        <div className="flex justify-center py-6"><Spinner /></div>
      ) : q.error ? (
        <ErrorBox error={q.error} />
      ) : byPartial.size === 0 ? (
        <p className="text-sm text-gray-600">{t("named partial이 없습니다. 하나의 파일로 프로젝트 전체를 관리할 수 있습니다.")}</p>
      ) : (
        <div className="space-y-2">
          {[...byPartial.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([partial, addrs]) => (
            <div key={partial} className="flex items-start gap-3 rounded-md border border-gray-200 px-3 py-2 text-sm">
              <div className="min-w-0 flex-1">
                <div className="font-medium">{partial}</div>
                <div className="mt-1 font-mono text-xs text-gray-600">{addrs.join(", ")}</div>
              </div>
              {canAdmin && (
                <Button variant="outline" size="sm" onClick={() => setConfirmPartial(partial)} disabled={busy !== null}>
                  {t("해제|iac")}
                </Button>
              )}
            </div>
          ))}
        </div>
      )}
      <Dialog
        open={confirmPartial !== null}
        onClose={() => busy === null && setConfirmPartial(null)}
        title={t("partial {name} 소유권 해제", { name: confirmPartial ?? "" })}
        footer={
          <>
            <Button variant="outline" onClick={() => setConfirmPartial(null)} disabled={busy !== null}>{t("취소")}</Button>
            <Button variant="danger" onClick={() => confirmPartial && release(confirmPartial)} loading={busy !== null}>{t("해제|iac")}</Button>
          </>
        }
      >
        <p>
          {tr("{partials}의 소유권 기록을 지웁니다. 서비스는 계속 실행됩니다. 예전 partial 설정을 다시 적용하면 소유권을 다시 가져갑니다.", { partials: (byPartial.get(confirmPartial ?? "") ?? []).join(", ") })}
        </p>
      </Dialog>
    </Card>
  );
}

export function IACSettings({ ws, canEdit, canAdmin = false }: { ws: Workspace; canEdit: boolean; canAdmin?: boolean }) {
  const qc = useQueryClient();
  const pid = ws.pid;
  const env = ws.env;
  const [definitionText, setDefinitionText] = useState("");
  const [loadedText, setLoadedText] = useState("");
  const [plan, setPlan] = useState<IACPlan | null>(null);
  const [plannedText, setPlannedText] = useState("");
  const [loading, setLoading] = useState(false);
  const [planning, setPlanning] = useState(false);
  const [applying, setApplying] = useState(false);
  const [deployAfter, setDeployAfter] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [error, setError] = useState<unknown>(null);

  const load = async () => {
    if (!pid || !env) return;
    setLoading(true);
    setError(null);
    setPlan(null);
    try {
      const next = pretty(await api.iacExport(pid, env.id));
      setDefinitionText(next);
      setLoadedText(next);
      setPlannedText("");
    } catch (e) {
      setError(e);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    let cancelled = false;
    if (!pid || !env) return;
    setLoading(true);
    setError(null);
    setPlan(null);
    api
      .iacExport(pid, env.id)
      .then((definition) => {
        if (cancelled) return;
        const next = pretty(definition);
        setDefinitionText(next);
        setLoadedText(next);
        setPlannedText("");
      })
      .catch((e) => !cancelled && setError(e))
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pid, env?.id]);

  const parse = (): IACDefinition => {
    try {
      return JSON.parse(definitionText) as IACDefinition;
    } catch (e) {
      throw new Error(t("JSON 형식이 올바르지 않습니다: {message}", { message: e instanceof Error ? e.message : String(e) }));
    }
  };

  const preview = async () => {
    if (!pid || !env) return;
    setPlanning(true);
    setError(null);
    setPlan(null);
    try {
      const next = await api.iacPlan(pid, env.id, { definition: parse() });
      setPlan(next);
      setPlannedText(definitionText);
    } catch (e) {
      setError(e);
    } finally {
      setPlanning(false);
    }
  };

  const apply = async () => {
    if (!pid || !env || !plan || plan.errors.length > 0 || plannedText !== definitionText) return;
    setApplying(true);
    setError(null);
    try {
      const res = await api.iacApply(pid, env.id, {
        definition: parse(),
        plan_hash: plan.plan_hash,
        confirm_destructive: plan.destructive,
        deploy: deployAfter,
      });
      setConfirmOpen(false);
      await Promise.all([
        qc.invalidateQueries({ queryKey: qk.project(pid) }),
        qc.invalidateQueries({ queryKey: qk.canvas(env.id) }),
        qc.invalidateQueries({ queryKey: ["iac-partials", env.id] }),
      ]);
      const skipped = res.services.filter((s) => s.deploy_skipped).length;
      toast(
        res.staged
          ? t("설정을 저장했습니다. 변경된 서비스는 다음 배포부터 적용됩니다.")
          : skipped
            ? t("설정을 저장하고 배포를 요청했습니다 ({count}개는 배포하지 못함).", { count: skipped })
            : t("설정을 저장하고 배포를 요청했습니다."),
      );
      await load();
    } catch (e) {
      setError(e);
      setConfirmOpen(false);
    } finally {
      setApplying(false);
    }
  };

  const copyCLI = async () => {
    await navigator.clipboard?.writeText("aven config import && aven config plan");
    toast(t("명령을 복사했습니다"));
  };

  const dirty = definitionText !== loadedText;
  const currentPlan = !!plan && plannedText === definitionText;
  const pending = !!plan && (plan.changes.length > 0 || plan.claims.length > 0);

  return (
    <div className="space-y-6">
      <Card
        title={t("코드형 인프라")}
        description={t("{env} 환경의 서비스, 설정, 변수를 하나의 정의로 관리합니다. 변경 사항을 미리 본 뒤 적용하면 일반 편집처럼 배포 대기 상태로 저장됩니다. 생략한 설정은 그대로 두고, 서비스에 변수 목록을 적으면 그 목록이 전부가 됩니다.", { env: env?.name ?? t("현재") })}
      />

      <Card
        title={t("CLI로 관리하기")}
        description={tr("저장소의 {ts}(TypeScript, bun 또는 node로 실행) 또는 {json}·{yaml}로 관리하세요.{imp}로 현재 설정을 가져오고 {plan}, {apply}로 적용합니다.", {
          ts: <code className="font-mono">.keel/aven.ts</code>,
          json: <code className="font-mono">aven.json</code>,
          yaml: <code className="font-mono">aven.yaml</code>,
          imp: <code className="font-mono"> aven config import</code>,
          plan: <code className="font-mono">aven config plan</code>,
          apply: <code className="font-mono">aven config apply</code>,
        })}
        action={
          <Button variant="outline" size="sm" onClick={copyCLI}>
            <Copy /> {t("명령 복사")}
          </Button>
        }
      >
        <pre className="overflow-x-auto rounded-md bg-gray-50 px-3 py-2 font-mono text-xs leading-5 text-gray-700">
          <Terminal className="mr-1 inline size-3.5 align-[-2px]" />
          aven config import{"\n"}  aven config plan{"\n"}  aven config apply --yes
        </pre>
      </Card>

      <Card
        title={t("환경 정의")}
        description={t("JSON 정의를 편집한 뒤 변경 사항을 미리 보고 적용하세요. partial을 적으면 그 partial이 소유한 서비스만 관리하고, 생략한 서비스 중 그 partial이 소유한 것만 삭제합니다.")}
        action={
          <Button variant="outline" size="sm" onClick={load} disabled={loading || !pid || !env}>
            <ArrowDownToLine /> {t("현재 설정 가져오기")}
          </Button>
        }
      >
        {loading ? (
          <div className="flex justify-center py-12"><Spinner /></div>
        ) : (
          <Textarea
            value={definitionText}
            onChange={(e) => {
              setDefinitionText(e.target.value);
              setPlan(null);
            }}
            rows={24}
            spellCheck={false}
            aria-label={t("코드형 인프라 JSON 정의")}
            className="min-h-[440px] w-full resize-y font-mono !text-xs !leading-5"
            disabled={!canEdit}
          />
        )}
        <div className="mt-3 flex flex-wrap items-center gap-2">
          <Button variant="default" onClick={preview} loading={planning} disabled={!canEdit || loading || !definitionText.trim()}>
            <RefreshCw /> {t("변경 미리보기")}
          </Button>
          <Button
            variant="primary"
            onClick={() => setConfirmOpen(true)}
            disabled={!canEdit || !currentPlan || plan!.errors.length > 0 || !pending || applying}
          >
            <ClipboardCheck /> {t("변경 적용")}
          </Button>
          {dirty && <span className="text-xs text-gray-500">{t("저장되지 않은 편집")}</span>}
        </div>
      </Card>

      {error ? <ErrorBox error={error} className="max-w-3xl" /> : null}

      {currentPlan && (
        <Card
          title={t("계획")}
          description={
            plan!.errors.length > 0
              ? t("{env} 환경 · 오류 {count}개", { env: plan!.environment, count: plan!.errors.length })
              : plan!.partial
                ? t("{env} 환경 · 생성 {create}, 변경 {change}, 삭제 {destroy} · partial {partial}", { env: plan!.environment, create: plan!.summary.create, change: plan!.summary.change, destroy: plan!.summary.destroy, partial: plan!.partial })
                : t("{env} 환경 · 생성 {create}, 변경 {change}, 삭제 {destroy}", { env: plan!.environment, create: plan!.summary.create, change: plan!.summary.change, destroy: plan!.summary.destroy })
          }
        >
          {plan!.warnings.map((w, i) => (
            <p key={i} className="mb-2 text-sm text-warn">{w}</p>
          ))}
          {plan!.errors.length > 0 ? (
            <div className="space-y-2">
              {plan!.errors.map((problem, i) => <ErrorBox key={i} error={problem} />)}
            </div>
          ) : !pending ? (
            <p className="text-sm text-gray-600">{t("설정이 이미 최신 상태입니다.")}</p>
          ) : (
            <>
              {plan!.changes.length > 0 ? <ChangeList plan={plan!} /> : <p className="text-sm text-gray-600">{t("설정 차이는 없습니다.")}</p>}
              {plan!.claims.length > 0 && (
                <p className="mt-3 text-sm text-gray-600">
                  {tr("적용하면 partial {partial}이 {claims}의 소유권을 가져옵니다.", { partial: <span className="font-medium">{plan!.partial}</span>, claims: <span className="font-mono text-xs">{plan!.claims.join(", ")}</span> })}
                </p>
              )}
            </>
          )}
          {plan!.destructive && (
            <div className="mt-4 flex items-start gap-2 rounded-md bg-danger-surface p-3 text-sm text-danger">
              <TriangleAlert className="mt-0.5 size-4 shrink-0" />
              {t("서비스 또는 변수를 삭제합니다. 서비스 삭제는 모든 환경의 배포와 연결된 볼륨 데이터를 제거합니다.")}
            </div>
          )}
        </Card>
      )}

      {pid && env && <Ownership key={env.id} pid={pid} eid={env.id} canAdmin={canAdmin} />}

      <div className="flex max-w-3xl items-start gap-2 text-xs text-gray-500">
        <ShieldCheck className="mt-0.5 size-3.5 shrink-0" />
        {t("변수 값은 보존 표시(preserve)로 내보내고 계획에서도 가려집니다. sealed 변수는 값을 바꿀 수 없습니다. 서비스별 aven.json·keel.json·railway.toml은 배포할 때 계속 이 설정 위에 적용됩니다.")}
      </div>

      <Dialog
        open={confirmOpen}
        onClose={() => !applying && setConfirmOpen(false)}
        title={plan?.destructive ? t("삭제가 포함된 변경 적용") : t("변경 적용")}
        wide
        footer={
          <>
            <Button variant="outline" onClick={() => setConfirmOpen(false)} disabled={applying}>{t("취소")}</Button>
            <Button variant={plan?.destructive ? "danger" : "primary"} onClick={apply} loading={applying}>
              {t("설정 적용")}
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          <p>
            {plan?.changes.length
              ? t("생성 {create}, 변경 {change}, 삭제 {destroy}개를 {env} 환경에 적용합니다.", { create: plan.summary.create, change: plan.summary.change, destroy: plan.summary.destroy, env: env?.name ?? t("현재") })
              : t("설정 변경은 없고 partial 소유권만 기록합니다.")}
          </p>
          {plan?.destructive && <p className="font-medium text-danger">{t("서비스 삭제는 모든 환경에서 실행되며 서비스 데이터도 제거됩니다.")}</p>}
          {plan && plan.changes.length > 0 && <ChangeList plan={plan} />}
          <label className="flex items-center gap-3 text-sm">
            <Toggle checked={deployAfter} onChange={setDeployAfter} label={t("적용 후 배포")} />
            {t("적용 후 바뀐 서비스를 바로 배포합니다 (끄면 배포 대기 상태로 저장만 합니다)")}
          </label>
        </div>
      </Dialog>
    </div>
  );
}
