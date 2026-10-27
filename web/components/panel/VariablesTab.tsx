"use client";

import { useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Braces, CornerDownRight, Network, Plus } from "lucide-react";
import { api, INJECTED_VARS, SEALED_VALUE, type CanvasService, type Environment } from "@/lib/api";
import { qk, useCanvas, useCaps, useResolvedVariables, useSharedVariables } from "@/lib/queries";
import { lock, NO_PERM } from "../Perm";
import { toast } from "@/lib/uiState";
import { useStaged } from "@/lib/staged";
import { useCtxHref } from "@/lib/nav";
import { BrandIcon, iconKey } from "../ServiceIcon";
import { Button, ErrorBox, cx, errorMessage, useConfirm } from "../ui";
import { type Suggestion } from "./VarInput";
import { RawEditor } from "./variables/RawEditor";
import { EditVariableRow, KEY_RE, VariableRow, type Row } from "./variables/VariableRow";
import { NewVariableForm, type RefOption } from "./variables/NewVariableForm";
import { SharedVariablesPanel, sharedRef } from "./variables/SharedVariablesPanel";
import { ProvidedVariables } from "./variables/ProvidedVariables";
import { t, tr } from "@/lib/i18n";

// Kept here for existing importers (settings page).
export { RawEditor, CodeEditor } from "./variables/RawEditor";
export { RefValue } from "./variables/VariableRow";

function quote(v: string): string {
  if (v === "" || /^[^\s#"'\\]+$/.test(v)) return v;
  return `"${v.replace(/"/g, '\\"').replace(/\n/g, "\\n")}"`;
}

export function toDotenv(env: Record<string, string>): string {
  return Object.entries(env)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([k, v]) => `${k}=${quote(v)}`)
    .join("\n");
}

/** RAW editor check: the dashboard only creates POSIX-style names. */
function validateRawKeys(next: Record<string, string>): string | null {
  const bad = Object.keys(next).filter((k) => !KEY_RE.test(k));
  return bad.length ? t("쓸 수 없는 변수 이름: {names}. 영문, 숫자, 밑줄(_)만 쓰고 숫자로 시작하지 마세요.", { names: bad.join(", ") }) : null;
}

const focusSoon = (sel: string) => requestAnimationFrame(() => document.querySelector<HTMLElement>(sel)?.focus());
/* ── suggestions ─────────────────────────────────────────────────────── */
function useSuggestions(svc: CanvasService, env: Environment) {
  const canvas = useCanvas(env.id);
  const shared = useSharedVariables(env.id);
  return useMemo(() => {
    const out: Suggestion[] = [];
    const sealedHint = (sealed: boolean) => (sealed ? { hint: t("봉인됨") } : {});
    for (const k of Object.keys(shared.data ?? {}).sort())
      out.push({ ref: `shared.${k}`, group: t("공유 변수"), ...sealedHint(shared.data?.[k] === SEALED_VALUE) });
    for (const s of canvas.data?.services ?? []) {
      if (s.id === svc.id) continue;
      const groupIcon = <BrandIcon name={iconKey(s)} className="size-3.5" />;
      const sealed = new Set(s.instance.sealed_variables ?? []);
      for (const k of Object.keys(s.instance.variables).sort()) out.push({ ref: `${s.name}.${k}`, group: s.name, groupIcon, ...sealedHint(sealed.has(k)) });
      for (const k of ["AVEN_PRIVATE_DOMAIN", "AVEN_PUBLIC_DOMAIN", "PORT", "AVEN_TCP_PROXY_DOMAIN", "AVEN_TCP_PROXY_PORT"])
        if (!(k in s.instance.variables)) out.push({ ref: `${s.name}.${k}`, group: s.name, groupIcon, hint: t("aven 제공") });
    }
    const own = new Set(svc.instance.sealed_variables ?? []);
    for (const k of Object.keys(svc.instance.variables).sort()) out.push({ ref: k, group: t("이 서비스"), ...sealedHint(own.has(k)) });
    for (const k of INJECTED_VARS) if (!(k in svc.instance.variables)) out.push({ ref: k, group: t("aven 제공 변수") });
    return out;
  }, [canvas.data, shared.data, svc]);
}
/* ── Tab ─────────────────────────────────────────────────────────────── */
export function VariablesTab({ svc, env }: { svc: CanvasService; env: Environment }) {
  const server = svc.instance.variables;
  const { staged, setVar } = useStaged(svc.id, env.id);
  const href = useCtxHref();
  const suggestions = useSuggestions(svc, env);
  const shared = useSharedVariables(env.id);
  const canvas = useCanvas(env.id);
  const [revealed, setRevealed] = useState<Set<string>>(new Set());
  const [editing, setEditing] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [raw, setRaw] = useState(false);
  const [sharedOpen, setSharedOpen] = useState(false);
  const newBtn = useRef<HTMLButtonElement>(null);
  const qc = useQueryClient();
  const { confirm, node: confirmNode } = useConfirm();
  const sealedKeys = useMemo(() => new Set(svc.instance.sealed_variables ?? []), [svc.instance.sealed_variables]);
  // 봉인은 저장된 값에 바로 적용된다(배포 대기 변경이 아니다). 되돌릴 수 없으므로 확인을 받는다.
  const askSeal = (k: string) =>
    confirm({
      title: t("{key} 봉인", { key: k }),
      confirmLabel: t("봉인"),
      danger: true,
      message: (
        <div className="space-y-2 text-sm text-gray-600">
          <p>{t("봉인하면 이 값은 대시보드, API, CLI 어디서도 다시 볼 수 없습니다. 배포할 때는 그대로 주입되고 다른 서비스가 참조할 수 있습니다.")}</p>
          <p>{t("봉인은 풀 수 없습니다. 값을 바꾸는 것은 가능하며, 봉인을 없애려면 변수를 지우고 다시 만들어야 합니다.")}</p>
          <p>{t("봉인은 배포를 기다리지 않고 바로 저장됩니다.")}</p>
        </div>
      ),
      run: async () => {
        try {
          await api.updateInstance(svc.id, env.id, { sealed_variables: [k] });
          await Promise.all([
            qc.invalidateQueries({ queryKey: qk.canvas(env.id) }),
            qc.invalidateQueries({ queryKey: qk.instance(svc.id, env.id) }),
            qc.invalidateQueries({ queryKey: qk.resolved(svc.id, env.id) }),
          ]);
          setRevealed((s) => {
            const n = new Set(s);
            n.delete(k);
            return n;
          });
          toast(t("{key}를 봉인했습니다", { key: k }));
        } catch (e) {
          toast(errorMessage(e), "error");
        }
      },
    });
  const resolved = useResolvedVariables(svc.id, env.id, true);
  // v1.5: 노드 간 사설 네트워크(mesh)로 닿는 참조는 정보로만 보여 주고, 닿지 않는 참조만 경고한다.
  const crossEntries = Object.entries(resolved.data ?? {}).filter(([k, r]) => r.cross_node && k in server);
  const crossNode = crossEntries.filter(([, r]) => r.mesh !== true).map(([k]) => k);
  const viaMesh = crossEntries.filter(([, r]) => r.mesh === true).map(([k]) => k);
  const rows: Row[] = useMemo(() => {
    const keys = new Set([...Object.keys(server), ...Object.keys(staged.vars)]);
    return [...keys]
      .sort((a, b) => a.localeCompare(b))
      .map((k) => {
        const st = staged.vars[k];
        const deleted = st === null;
        return { key: k, value: st === undefined || st === null ? (server[k] ?? "") : st, server: server[k], deleted, sealed: sealedKeys.has(k) && k in server };
      });
  }, [server, staged.vars, sealedKeys]);
  const draft = useMemo(() => Object.fromEntries(rows.filter((r) => !r.deleted).map((r) => [r.key, r.value])), [rows]);
  const canEdit = useCaps(svc.project_id).canEdit;
  const why = canEdit ? undefined : NO_PERM;
  const put = (k: string, v: string | null) => canEdit && setVar(k, v, server);

  const putMany = (pairs: [string, string][], done?: () => void) => {
    const clash = pairs.filter(([k, v]) => k in draft && draft[k] !== v).map(([k]) => k);
    const go = () => {
      for (const [k, v] of pairs) put(k, v);
      done?.();
    };
    if (!clash.length) return go();
    confirm({
      title: t("같은 이름의 변수가 있습니다"),
      confirmLabel: t("덮어쓰기"),
      message: (
        <div className="space-y-2 text-sm text-gray-600">
          <p>{t("다음 변수의 값이 바뀝니다. 배포하기 전까지는 되돌릴 수 있습니다.")}</p>
          <p className="break-all font-mono text-foreground">{clash.join(", ")}</p>
        </div>
      ),
      run: go,
    });
  };

  const closeAdd = () => {
    setAdding(false);
    requestAnimationFrame(() => newBtn.current?.focus());
  };

  const toggleReveal = (k: string) =>
    setRevealed((s) => {
      const n = new Set(s);
      if (n.has(k)) n.delete(k);
      else n.add(k);
      return n;
    });

  const endEdit = (k: string) => {
    setEditing(null);
    focusSoon(`[data-var-menu="${CSS.escape(k)}"]`);
  };
  const commitEdit = (r: Row, k: string, v: string) => {
    if (r.sealed && v === "") return endEdit(r.key); // 빈 칸이면 기존 봉인 값 유지
    if (k !== r.key) put(r.key, null);
    put(k, v);
    endEdit(k);
  };

  const others = (canvas.data?.services ?? []).filter((s) => s.id !== svc.id);
  const provided = INJECTED_VARS.filter((k) => !(k in draft));
  const refOptions: RefOption[] = [
    ...others.flatMap((s) =>
      [...Object.keys(s.instance.variables).sort(), "AVEN_PRIVATE_DOMAIN", ...(s.instance.domains.length ? ["AVEN_PUBLIC_DOMAIN"] : [])].map((k) => {
        const value = `\${{${s.name}.${k}}}`;
        const prefix = s.name.toUpperCase().replace(/[^A-Z0-9_]/g, "_");
        const base = k.startsWith("AVEN_") ? `${prefix}_${k.replace(/^AVEN_/, "")}` : k;
        // 이름이 다른 값과 겹치면 서비스 이름을 앞에 붙인다. 이미 같은 참조가 있으면 비활성.
        const name = base in draft && draft[base] !== value ? `${prefix}_${k}` : base;
        return { name, value, hint: s.name, icon: <BrandIcon name={iconKey(s)} />, exists: Object.values(draft).includes(value) };
      }),
    ),
    ...Object.keys(shared.data ?? {})
      .sort()
      .map((k) => ({ name: k, value: sharedRef(k), hint: t("공유 변수"), icon: <CornerDownRight />, exists: draft[k] === sharedRef(k) })),
  ];
  const ghostCls = "[&_svg]:text-pink-500";

  return (
    <div className="variables flex flex-col space-y-4 pb-12">
      <div className="flex flex-col border-b border-gray-100">
        <div className="flex items-center justify-between">
          <div className="flex items-center space-x-3">
            <h2 className="font-medium">{t("서비스 변수")}</h2>
          </div>
          <div className="flex items-center space-x-1">
            <Button
              variant={sharedOpen ? "outline" : "link"}
              className={ghostCls}
              name="Insert Shared Variable"
              aria-expanded={sharedOpen}
              onClick={() => setSharedOpen((o) => !o)}
            >
              <CornerDownRight /> <span className="text-gray-600">{sharedOpen ? t("공유 변수 닫기") : t("공유 변수")}</span>
            </Button>
            <Button variant="link" onClick={() => setRaw(true)} className={cx("hidden md:flex", ghostCls)} {...lock(canEdit)}>
              <Braces /> <span className="text-gray-600">{t("RAW 편집")}</span>
            </Button>
            <Button ref={newBtn} variant="accent" onClick={() => setAdding(true)} className="new-variable [&_svg]:text-pink-500" aria-expanded={adding} {...lock(canEdit)}>
              <Plus /> {t("새 변수")}
            </Button>
          </div>
        </div>
        <div className="mt-6 flex flex-col">
          {sharedOpen && (
            <SharedVariablesPanel
              shared={shared.data}
              pending={shared.isPending}
              draft={draft}
              envName={env.name}
              manageHref={href("/settings/", { section: "shared" })}
              canEdit={canEdit}
              onAdd={(keys) => putMany(keys.map((k) => [k, sharedRef(k)]))}
              onClose={() => setSharedOpen(false)}
            />
          )}
        </div>
      </div>

      {crossNode.length > 0 && (
        <div role="status" className="flex items-start gap-3 rounded-md bg-warn-surface p-3 text-sm text-warn">
          <AlertTriangle className="mt-0.5 size-4 shrink-0" aria-hidden />
          <div>
            <div className="font-medium">{t("다른 노드의 서비스를 참조하지만 노드 간 사설 네트워크로 닿지 않습니다: {names}", { names: crossNode.join(", ") })}</div>
            <div className="mt-0.5 text-sm text-yellow-700">
              {t("두 노드 중 하나에 사설 주소(Tailscale/WireGuard)나 mesh가 없어 비공개 주소가 이 노드에서 풀리지 않습니다. 노드에 사설 주소를 설정하거나, 같은 노드로 옮기거나, 상대 서비스의 TCP 프록시 주소를 쓰세요.")}
            </div>
          </div>
        </div>
      )}
      {viaMesh.length > 0 && (
        <p role="status" className="flex items-center gap-2 text-sm text-gray-500">
          <Network className="size-3.5 shrink-0" aria-hidden />
          <span className="min-w-0">
            {tr("다른 노드의 서비스를 노드 간 사설 네트워크로 연결합니다: {names}", { names: <span className="break-all font-mono">{viaMesh.join(", ")}</span> })}
          </span>
        </p>
      )}

      {adding && (
        <NewVariableForm
          suggestions={suggestions}
          refOptions={refOptions}
          onCancel={closeAdd}
          onAdd={(k, v) => putMany([[k, v]], closeAdd)}
          onBulk={(pairs) => {
            const ok = pairs.filter(([k]) => KEY_RE.test(k));
            const bad = pairs.length - ok.length;
            putMany(ok, () => {
              toast(
                bad
                  ? t("변수 {count}개를 추가했습니다. 이름이 잘못된 {bad}개는 건너뛰었습니다", { count: ok.length, bad })
                  : t("변수 {count}개를 추가했습니다", { count: ok.length }),
                bad ? "error" : undefined,
              );
              closeAdd();
            });
          }}
        />
      )}

      <div className="flex flex-col space-y-4">
        <div className="grid gap-1">
          {rows.length === 0 && !adding ? (
            <div className="flex flex-col justify-center space-y-1 rounded-lg border border-dashed border-gray-200 py-14 text-center">
              <p className="text-sm font-semibold leading-6 text-gray-600">{t("환경 변수가 없습니다")}</p>
              <p className="mx-auto max-w-lg text-sm leading-5 text-gray-500">
                {tr("{raw}로 모든 변수를 한 번에 가져오세요", {
                  raw: (
                    <button type="button" onClick={() => setRaw(true)} {...lock(canEdit)} className="cursor-pointer underline hover:text-pink-600 disabled:cursor-not-allowed">
                      {t("RAW 편집기")}
                    </button>
                  ),
                })}
              </p>
            </div>
          ) : (
            <ul className="relative w-full" aria-label={t("서비스 변수")}>
              {rows.map((r) =>
                editing === r.key ? (
                  <EditVariableRow
                    key={r.key}
                    r={r}
                    initialValue={r.sealed && r.value === SEALED_VALUE ? "" : r.value}
                    existing={draft}
                    suggestions={suggestions}
                    onCommit={(k, v) => commitEdit(r, k, v)}
                    onCancel={() => endEdit(r.key)}
                  />
                ) : (
                  <VariableRow
                    key={r.key}
                    r={r}
                    resolved={resolved.data?.[r.key]}
                    resolvedPending={resolved.isPending}
                    savedNotDeployed={!!svc.instance.pending_changes?.includes(`variables.${r.key}`)}
                    shown={revealed.has(r.key)}
                    onToggleReveal={() => toggleReveal(r.key)}
                    onEdit={() => setEditing(r.key)}
                    onSeal={() => askSeal(r.key)}
                    onRevert={() => put(r.key, r.server === undefined ? null : r.server)}
                    onDelete={() => put(r.key, null)}
                    onRestore={() => put(r.key, r.server ?? "")}
                    canEdit={canEdit}
                    why={why}
                  />
                ),
              )}
            </ul>
          )}
        </div>

        <ProvidedVariables keys={provided} resolved={resolved.data} pending={resolved.isPending} />
        {resolved.error ? <ErrorBox error={resolved.error} /> : null}
      </div>

      {raw && (
        <RawEditor
          vars={draft}
          sealed={rows.filter((r) => r.sealed && !r.deleted && r.value === SEALED_VALUE).map((r) => r.key)}
          validate={validateRawKeys}
          onClose={() => setRaw(false)}
          onApplyVars={(next) => {
            // 편집기에 나오지 않은 봉인 변수는 그대로 둔다. 봉인 변수에 새 값을 적으면 값만 교체된다(봉인 유지).
            for (const k of Object.keys(draft)) if (!(k in next) && !sealedKeys.has(k)) put(k, null);
            for (const [k, v] of Object.entries(next)) if (!(sealedKeys.has(k) && v === SEALED_VALUE)) put(k, v);
            setRaw(false);
          }}
        />
      )}
      {confirmNode}
    </div>
  );
}
