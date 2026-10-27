"use client";

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Flag, Pencil, Plus, Trash2 } from "lucide-react";
import { api, type Environment, type FeatureFlag, type FeatureFlagRule, type FeatureFlagType, type ID } from "@/lib/api";
import { toast } from "@/lib/uiState";
import { lock } from "@/components/Perm";
import { Button, Card, CopyButton, Dialog, EmptyState, ErrorBox, Field, IconButton, Input, RowList, Select, Spinner, Textarea, Toggle, useConfirm } from "@/components/ui";
import { t, tr } from "@/lib/i18n";

/* ── values ─────────────────────────────────────────────────────────────
 * Values are edited as text and converted per flag type: bool/number/string get plain inputs, json gets a JSON editor. */
function zeroText(type: FeatureFlagType) {
  return type === "bool" ? "false" : type === "number" ? "0" : type === "json" ? "{}" : "";
}
function toText(type: FeatureFlagType, value: unknown) {
  if (type === "string") return typeof value === "string" ? value : "";
  if (type === "json") return JSON.stringify(value, null, 2);
  return value === undefined || value === null ? zeroText(type) : String(value);
}
function fromText(type: FeatureFlagType, text: string): unknown {
  if (type === "bool") return text === "true";
  if (type === "string") return text;
  if (type === "number") {
    const n = Number(text);
    if (text.trim() === "" || !Number.isFinite(n)) throw new Error(t("숫자 값을 입력하세요."));
    return n;
  }
  try {
    return JSON.parse(text);
  } catch {
    throw new Error(t("JSON 값이 올바르지 않습니다."));
  }
}
function showValue(value: unknown) {
  const s = JSON.stringify(value);
  return s && s.length > 60 ? s.slice(0, 57) + "…" : s;
}
function ValueInput({ type, value, onChange, label }: { type: FeatureFlagType; value: string; onChange: (v: string) => void; label: string }) {
  if (type === "bool")
    return (
      <Select value={value} onChange={(e) => onChange(e.target.value)} aria-label={label}>
        <option value="true">true</option>
        <option value="false">false</option>
      </Select>
    );
  if (type === "json") return <Textarea value={value} onChange={(e) => onChange(e.target.value)} rows={3} mono aria-label={label} />;
  return <Input value={value} onChange={(e) => onChange(e.target.value)} inputMode={type === "number" ? "decimal" : undefined} mono aria-label={label} />;
}

/* ── rule conditions ────────────────────────────────────────────────────
 * The builder writes one condition per rule (like Railway's dashboard): an attribute comparison or a rollout %.
 * Anything else (&&, ||, !) stays an expression, editable as text. */

type Cond =
  | { kind: "attribute"; attr: string; op: string; value: string }
  | { kind: "rollout"; key: string; percent: string }
  | { kind: "expression"; expr: string };

type RuleDraft = { uid: string; id: string; cond: Cond; value: string };

const OPS = ["==", "!=", "in", "contains", "matches", "<", "<=", ">", ">="];
const IDENT = "[A-Za-z_][A-Za-z0-9_]*";
const LITERAL = String.raw`"(?:[^"\\]|\\.)*"|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?|true|false|\[[^\]]*\]`;
const ATTR_RE = new RegExp(`^(${IDENT})\\s*(==|!=|<=|>=|<|>|in|contains|matches)\\s*(${LITERAL})$`);
const ROLLOUT_RE = new RegExp(`^bucket\\((${IDENT})\\)\\s*<\\s*(\\d*\\.?\\d+)$`);

/** Turns a stored expression back into builder fields when it is a single condition. */
export function parseCond(when: string): Cond {
  const w = when.trim();
  const r = ROLLOUT_RE.exec(w);
  if (r) return { kind: "rollout", key: r[1], percent: String(Math.round(Number(r[2]) * 10000) / 100) };
  const a = ATTR_RE.exec(w);
  if (a) {
    let value = a[3];
    if (value.startsWith('"')) {
      try {
        value = JSON.parse(value) as string;
      } catch {
        return { kind: "expression", expr: w };
      }
    }
    return { kind: "attribute", attr: a[1], op: a[2], value };
  }
  return { kind: "expression", expr: w };
}

/** Writes builder fields as an expression. Attribute values that look like numbers/booleans/lists stay unquoted. */
export function condToWhen(c: Cond): string {
  if (c.kind === "expression") return c.expr.trim();
  if (c.kind === "rollout") {
    const pct = Number(c.percent);
    if (!c.key.trim() || c.percent.trim() === "" || !Number.isFinite(pct) || pct < 0 || pct > 100) throw new Error(t("롤아웃 비율은 0~100 사이로 입력하세요."));
    return `bucket(${c.key.trim()}) < ${Math.round(pct * 100) / 10000}`;
  }
  if (!new RegExp(`^${IDENT}$`).test(c.attr.trim())) throw new Error(t("속성 이름은 문자, 숫자, 밑줄만 쓸 수 있습니다."));
  const v = c.value.trim();
  const literal =
    c.op === "in" || /^-?\d+(\.\d+)?([eE][+-]?\d+)?$/.test(v) || v === "true" || v === "false"
      ? v
      : JSON.stringify(c.value);
  return `${c.attr.trim()} ${c.op} ${literal}`;
}

let uidSeq = 0;
const uid = () => `r${++uidSeq}`;

function toDraft(type: FeatureFlagType, r: FeatureFlagRule): RuleDraft {
  return { uid: uid(), id: r.id, cond: parseCond(r.when), value: toText(type, r.value) };
}

function RuleRow({ rule, index, type, onChange, onRemove }: { rule: RuleDraft; index: number; type: FeatureFlagType; onChange: (next: RuleDraft) => void; onRemove: () => void }) {
  const c = rule.cond;
  const setKind = (kind: Cond["kind"]) => {
    if (kind === c.kind) return;
    let current = "";
    try {
      current = condToWhen(c);
    } catch {
      /* incomplete builder fields */
    }
    const next: Cond =
      kind === "attribute"
        ? { kind, attr: "plan", op: "==", value: "" }
        : kind === "rollout"
          ? { kind, key: "key", percent: "10" }
          : { kind, expr: current };
    onChange({ ...rule, cond: next });
  };
  return (
    <div className="rounded-lg border border-gray-200 p-4">
      <div className="mb-3 flex items-center gap-3">
        <span className="text-sm font-medium">{t("규칙 {n}", { n: index + 1 })}</span>
        <span className="truncate font-mono text-xs text-gray-500" title={t("규칙 ID (CLI: aven flag unset --rule-id)")}>{rule.id}</span>
        <IconButton label={t("규칙 {n} 삭제", { n: index + 1 })} onClick={onRemove} className="ml-auto">
          <Trash2 className="size-4" />
        </IconButton>
      </div>
      <div className="grid gap-3 md:grid-cols-[150px_1fr]">
        <Field label={t("조건 (When)")}>
          <Select value={c.kind} onChange={(e) => setKind(e.target.value as Cond["kind"])}>
            <option value="attribute">{t("속성")}</option>
            <option value="rollout">{t("롤아웃 %")}</option>
            <option value="expression">{t("표현식")}</option>
          </Select>
        </Field>
        {c.kind === "attribute" ? (
          <div className="grid grid-cols-[1fr_120px_1fr] gap-2">
            <Field label={t("속성")}>
              <Input value={c.attr} onChange={(e) => onChange({ ...rule, cond: { ...c, attr: e.target.value } })} placeholder="plan" mono />
            </Field>
            <Field label={t("연산자")}>
              <Select value={c.op} onChange={(e) => onChange({ ...rule, cond: { ...c, op: e.target.value } })}>
                {OPS.map((op) => (
                  <option key={op} value={op}>{op}</option>
                ))}
              </Select>
            </Field>
            <Field label={t("값")} hint={c.op === "in" ? `${t("목록")}: ["pro", "enterprise"]` : undefined}>
              <Input value={c.value} onChange={(e) => onChange({ ...rule, cond: { ...c, value: e.target.value } })} placeholder={c.op === "in" ? '["pro"]' : "enterprise"} mono />
            </Field>
          </div>
        ) : c.kind === "rollout" ? (
          <div className="grid grid-cols-[1fr_140px] gap-2">
            <Field label={t("키 (Key)")} hint={t("이 속성 값으로 해시해 같은 대상은 항상 같은 결과를 받습니다.")}>
              <Input value={c.key} onChange={(e) => onChange({ ...rule, cond: { ...c, key: e.target.value } })} placeholder="key" mono />
            </Field>
            <Field label={t("비율 (%)")}>
              <Input value={c.percent} onChange={(e) => onChange({ ...rule, cond: { ...c, percent: e.target.value } })} inputMode="decimal" mono />
            </Field>
          </div>
        ) : (
          <Field label={t("표현식")} hint={t("예: {example}", { example: 'plan == "enterprise" && bucket(key) < 0.25' })}>
            <Input value={c.expr} onChange={(e) => onChange({ ...rule, cond: { ...c, expr: e.target.value } })} mono />
          </Field>
        )}
      </div>
      <div className="mt-3 md:ml-[162px]">
        <Field label={t("제공할 값 (Serve)")}>
          <ValueInput type={type} value={rule.value} onChange={(value) => onChange({ ...rule, value })} label={t("규칙 {n} 값", { n: index + 1 })} />
        </Field>
      </div>
    </div>
  );
}

/* ── create / edit dialog ───────────────────────────────────────────── */

function FeatureFlagDialog({ pid, environment, flag, onClose }: { pid: ID; environment: Environment; flag: FeatureFlag | null; onClose: () => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState(flag?.name ?? "");
  const [type, setType] = useState<FeatureFlagType>(flag?.type ?? "bool");
  const [defaultValue, setDefaultValue] = useState(() => (flag ? toText(flag.type, flag.default_value) : "false"));
  const [rules, setRules] = useState<RuleDraft[]>(() => (flag?.rules ?? []).map((r) => toDraft(flag!.type, r)));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  const changeType = (ft: FeatureFlagType) => {
    setType(ft);
    setDefaultValue(zeroText(ft));
    setRules((all) => all.map((r) => ({ ...r, value: zeroText(ft) })));
  };

  const save = async () => {
    let body: { default_value: unknown; rules: FeatureFlagRule[] };
    try {
      if (!flag && !/^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(name.trim())) throw new Error(t("이름은 문자, 숫자, 점, 대시, 밑줄로 128자까지 쓸 수 있습니다."));
      body = {
        default_value: fromText(type, defaultValue),
        rules: rules.map((r, i) => {
          const when = condToWhen(r.cond);
          if (!when) throw new Error(t("규칙 {n}의 조건을 입력하세요.", { n: i + 1 }));
          return { id: r.id, when, value: fromText(type, r.value) };
        }),
      };
    } catch (e) {
      setError(e);
      return;
    }
    setBusy(true);
    setError(null);
    try {
      if (flag) await api.updateFeatureFlag(pid, flag.name, { environment_id: environment.id, ...body });
      else await api.createFeatureFlag(pid, { name: name.trim(), type, environment_id: environment.id, ...body });
      await qc.invalidateQueries({ queryKey: ["feature-flags", pid] });
      toast(flag ? t("{name} 플래그를 저장했습니다.", { name: flag.name }) : t("{name} 플래그를 만들었습니다.", { name: name.trim() }));
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open
      onClose={() => !busy && onClose()}
      title={flag ? t("{name} 수정", { name: flag.name }) : t("플래그 만들기")}
      wide
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={busy}>{t("취소")}</Button>
          <Button variant="primary" onClick={save} loading={busy}>{flag ? t("저장") : t("만들기")}</Button>
        </>
      }
    >
      <div className="space-y-5">
        <p className="text-sm text-gray-600">
          {flag
            ? tr("{env} 환경의 기본값과 규칙을 수정합니다.", { env: <strong>{environment.name}</strong> })
            : tr("{env} 환경의 기본값과 규칙을 정합니다. 다른 환경은 같은 기본값에 규칙 없이 시작합니다.", { env: <strong>{environment.name}</strong> })}
        </p>
        <div className="grid gap-4 md:grid-cols-2">
          <Field label={t("이름")} hint={t("문자, 숫자, 점, 대시, 밑줄 (예: checkout.v2)")}>
            <Input value={name} onChange={(e) => setName(e.target.value)} disabled={!!flag} autoFocus={!flag} mono />
          </Field>
          <Field label={t("형식")} hint={flag ? t("형식은 CLI의 --force --type으로만 바꿀 수 있습니다(규칙이 지워짐).") : t("만든 뒤에는 바꿀 수 없습니다.")}>
            <Select value={type} onChange={(e) => changeType(e.target.value as FeatureFlagType)} disabled={!!flag}>
              <option value="bool">bool</option>
              <option value="string">string</option>
              <option value="number">number</option>
              <option value="json">json</option>
            </Select>
          </Field>
        </div>
        <Field label={t("기본값")} hint={t("일치하는 규칙이 없거나, 일치한 규칙들의 값이 서로 다를 때 제공합니다.")}>
          <ValueInput type={type} value={defaultValue} onChange={setDefaultValue} label={t("기본값")} />
        </Field>
        <div className="space-y-3">
          <div className="flex items-center justify-between gap-4">
            <div>
              <h3 className="text-sm font-semibold">{t("타기팅 규칙")}</h3>
              <p className="mt-1 text-xs text-gray-500">{t("일치하는 모든 규칙이 같은 값을 제공할 때만 그 값이 적용됩니다. 키 속성이 없는 요청에는 롤아웃이 적용되지 않습니다.")}</p>
            </div>
            <Button
              size="sm"
              variant="outline"
              onClick={() => setRules((all) => [...all, { uid: uid(), id: `rule-${Date.now().toString(36)}`, cond: { kind: "attribute", attr: "plan", op: "==", value: "" }, value: zeroText(type) }])}
            >
              <Plus className="size-3.5" /> {t("규칙 추가")}
            </Button>
          </div>
          {rules.map((rule, index) => (
            <RuleRow
              key={rule.uid}
              rule={rule}
              index={index}
              type={type}
              onChange={(next) => setRules((all) => all.map((r) => (r.uid === rule.uid ? next : r)))}
              onRemove={() => setRules((all) => all.filter((r) => r.uid !== rule.uid))}
            />
          ))}
        </div>
        {error ? <ErrorBox error={error} /> : null}
      </div>
    </Dialog>
  );
}

/* ── list ───────────────────────────────────────────────────────────── */

const SDK_SNIPPET = `import { flags } from "aven-flags";

await flags.init(); // AVEN_URL, AVEN_TOKEN(프로젝트·환경 토큰)
const on = flags.getBoolean("checkout-v2", { key: userId, plan: user.plan }, false);`;

export function FeatureFlags({ pid, environments, currentEnvironmentID, canAdmin }: {
  pid: ID;
  environments: Environment[];
  currentEnvironmentID: ID;
  canAdmin: boolean;
}) {
  const qc = useQueryClient();
  const [picked, setPicked] = useState<ID | null>(null);
  const environmentID = picked ?? currentEnvironmentID;
  const environment = environments.find((e) => e.id === environmentID);
  const [editing, setEditing] = useState<FeatureFlag | null | undefined>(undefined);
  const [error, setError] = useState<unknown>(null);
  const { confirm, node } = useConfirm();
  const query = useQuery({
    queryKey: ["feature-flags", pid, environmentID],
    queryFn: () => api.featureFlags(pid, environmentID),
    enabled: !!environmentID,
  });

  const toggle = async (flag: FeatureFlag, value: boolean) => {
    setError(null);
    try {
      await api.updateFeatureFlag(pid, flag.name, { environment_id: environmentID, default_value: value });
      await qc.invalidateQueries({ queryKey: ["feature-flags", pid] });
      toast(value ? t("{name} 기본값을 켰습니다.", { name: flag.name }) : t("{name} 기본값을 껐습니다.", { name: flag.name }));
    } catch (e) {
      setError(e);
    }
  };

  const remove = (flag: FeatureFlag) =>
    confirm({
      title: t("플래그 삭제"),
      message: (
        <span>
          {tr("{name} 플래그를 모든 환경에서 삭제할까요? 이 플래그를 읽는 앱은 코드의 대체값(fallback)을 받게 됩니다.", { name: <strong>{flag.name}</strong> })}
        </span>
      ),
      confirmLabel: t("삭제"),
      danger: true,
      run: async () => {
        try {
          await api.deleteFeatureFlag(pid, flag.name);
          await qc.invalidateQueries({ queryKey: ["feature-flags", pid] });
          toast(t("{name} 플래그를 삭제했습니다.", { name: flag.name }));
        } catch (e) {
          setError(e);
        }
      },
    });

  return (
    <>
      <Card
        title={t("기능 플래그")}
        description={t("배포 없이 기능을 켜고 끄거나 일부 사용자에게만 내보냅니다. 기본값과 타기팅 규칙은 환경마다 따로 있습니다. 정적인 설정과 비밀 값은 변수를 쓰세요.")}
        action={
          <>
            <Select aria-label={t("환경")} value={environmentID} onChange={(e) => setPicked(e.target.value)} className="w-40">
              {environments.map((env) => (
                <option key={env.id} value={env.id}>{env.name}</option>
              ))}
            </Select>
            <Button variant="primary" onClick={() => setEditing(null)} disabled={!environment} {...lock(canAdmin)}>
              <Plus className="size-3.5" /> {t("플래그 만들기")}
            </Button>
          </>
        }
      >
        {!canAdmin && <p className="mb-4 text-sm text-gray-500">{t("플래그를 만들고 바꾸는 것은 프로젝트 admin만 할 수 있습니다. 보기 권한으로는 내용을 볼 수만 있습니다.")}</p>}
        {error ? <ErrorBox error={error} className="mb-4" /> : null}
        {query.isPending ? (
          <Spinner />
        ) : query.error ? (
          <ErrorBox error={query.error} />
        ) : !query.data?.flags.length ? (
          <EmptyState icon={<Flag className="size-5" />} title={t("아직 기능 플래그가 없습니다")}>
            {tr("{cmd}로도 만들 수 있습니다.", { cmd: <span className="font-mono">aven flag set checkout.v2 true</span> })}
          </EmptyState>
        ) : (
          <RowList>
            {query.data.flags.map((flag) => (
              <li key={flag.id} className="flex flex-wrap items-center gap-4 px-4 py-3">
                <div className="min-w-[200px] flex-1">
                  <div className="flex items-center gap-2">
                    <span className="font-mono text-sm font-semibold text-foreground">{flag.name}</span>
                    <span className="rounded bg-gray-100 px-1.5 py-0.5 font-mono text-xs text-gray-600">{flag.type}</span>
                    {!flag.configured && (
                      <span className="text-xs text-gray-500" title={t("이 환경은 기본 환경의 기본값을 따릅니다. 저장하면 이 환경의 값이 생깁니다.")}>
                        {t("기본 환경 값 사용 중")}
                      </span>
                    )}
                  </div>
                  <div className="mt-1 truncate text-xs text-gray-500">
                    {t("기본값")} <span className="font-mono">{showValue(flag.default_value)}</span>
                    {flag.rules.length > 0 && <> · {t("규칙 {count}개", { count: flag.rules.length })}</>}
                  </div>
                </div>
                {flag.type === "bool" && (
                  <Toggle checked={flag.default_value === true} onChange={(v) => toggle(flag, v)} label={t("{name} 기본값", { name: flag.name })} disabled={!canAdmin} />
                )}
                <Button size="sm" variant="outline" onClick={() => setEditing(flag)} {...lock(canAdmin)}>
                  <Pencil className="size-3.5" /> {t("수정")}
                </Button>
                <IconButton label={t("{name} 삭제", { name: flag.name })} onClick={() => remove(flag)} {...lock(canAdmin)}>
                  <Trash2 className="size-4" />
                </IconButton>
              </li>
            ))}
          </RowList>
        )}
      </Card>
      <Card
        title={t("앱에서 읽기")}
        description={t("SDK는 플래그를 메모리에 두고 백그라운드에서 갱신하므로 읽기가 즉시 끝납니다. 토큰은 계정 → API 토큰에서 프로젝트 또는 환경 범위(viewer면 충분)로 만들고, 서비스 변수 AVEN_URL·AVEN_TOKEN으로 넣으세요. 환경은 배포에 주입되는 AVEN_ENVIRONMENT_ID를 따릅니다.")}
        action={<CopyButton value={SDK_SNIPPET} />}
      >
        <pre className="overflow-x-auto rounded-lg border border-gray-200 bg-gray-50 p-4 font-mono text-xs leading-5 text-gray-700">{SDK_SNIPPET}</pre>
      </Card>
      {editing !== undefined && environment && (
        <FeatureFlagDialog key={`${editing?.id ?? "new"}-${environmentID}`} pid={pid} environment={environment} flag={editing} onClose={() => setEditing(undefined)} />
      )}
      {node}
    </>
  );
}
