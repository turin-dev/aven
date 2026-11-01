"use client";

import { useMemo, useState } from "react";
import { AlertTriangle, Eye, EyeOff } from "lucide-react";
import { INJECTED_VARS, SEALED_VALUE, type CanvasService, type Environment } from "@/lib/api";
import { hasRef, refProblems, resolvePreview, type RefProblem, type RefScope } from "@/lib/refs";
import { t, tr } from "@/lib/i18n";
import { cx } from "../../ui";

const mono = (s: string) => <span className="break-all font-mono">{s}</span>;

/** Values aven injects that the dashboard can fill in itself; the rest are known names with values set at deploy. */
function injected(s: CanvasService, env: Environment): Record<string, string | undefined> {
  const out: Record<string, string | undefined> = Object.fromEntries(INJECTED_VARS.map((k) => [k, undefined]));
  // aven injects every name with the AVEN_ prefix and the legacy KEEL_ prefix.
  const both = (suffix: string, v: string) => {
    out[`AVEN_${suffix}`] = v;
    out[`KEEL_${suffix}`] = v;
  };
  both("SERVICE_NAME", s.name);
  both("SERVICE_ID", s.id);
  both("ENVIRONMENT_NAME", env.name);
  both("ENVIRONMENT_ID", env.id);
  out.PORT = String(s.instance.port ?? 8080);
  if (s.instance.domains?.[0]?.host) both("PUBLIC_DOMAIN", s.instance.domains[0].host);
  return out;
}

function userVars(vars: Record<string, string>, sealed: Iterable<string>): Record<string, string | undefined> {
  const hidden = new Set(sealed);
  return Object.fromEntries(Object.entries(vars).map(([k, v]) => [k, hidden.has(k) || v === SEALED_VALUE ? undefined : v]));
}

/**
 * Everything a reference from `svc` can point at: shared variables, every service of the environment (user variables over
 * injected ones, like the server) and `svc` itself with its current draft (staged) values.
 */
export function useRefScope(
  svc: CanvasService,
  env: Environment,
  services: CanvasService[] | undefined,
  shared: Record<string, string> | undefined,
  draft: Record<string, string>,
): RefScope {
  return useMemo(() => {
    const scopes: RefScope["scopes"] = { shared: userVars(shared ?? {}, []) };
    for (const s of services ?? []) if (s.id !== svc.id) scopes[s.name] = { ...injected(s, env), ...userVars(s.instance.variables, s.instance.sealed_variables ?? []) };
    scopes[svc.name] = { ...injected(svc, env), ...userVars(draft, svc.instance.sealed_variables ?? []) };
    return { self: svc.name, scopes };
  }, [svc, env, services, shared, draft]);
}

function problemText(p: RefProblem) {
  switch (p.kind) {
    case "service":
      return tr("{ref}: 이 환경에 {service} 서비스가 없습니다. 배포하면 빈 값이 됩니다.", { ref: mono(p.raw), service: mono(p.ns) });
    case "variable":
      return p.ns === "shared"
        ? tr("{ref}: 이런 공유 변수가 없습니다. 배포하면 빈 값이 됩니다.", { ref: mono(p.raw) })
        : tr("{ref}: 이런 변수가 없습니다. 배포하면 빈 값이 됩니다.", { ref: mono(p.raw) });
    case "malformed":
      return tr("{ref}: 참조 형식이 아닙니다. {example}처럼 쓰세요.", { ref: mono(p.raw), example: mono("${{ service.VAR }}") });
    case "shell":
      return tr("{ref}는 치환되지 않고 글자 그대로 들어갑니다. 참조라면 {example}로 쓰세요.", { ref: mono(p.raw), example: mono(`\${{ ${p.key} }}`) });
  }
}

/**
 * Inline check under a variable value: reference problems (unknown service or variable, malformed `${{`, shell-style
 * `${VAR}`, cycles) and, on demand, the value with references resolved the way the next deploy will see them.
 * Problems are warnings, not errors: a referenced service may be added later.
 */
export function RefCheck({ value, name, scope, id }: { value: string; name?: string; scope: RefScope; id?: string }) {
  const [show, setShow] = useState(false);
  const problems = useMemo(() => refProblems(value, scope), [value, scope]);
  const refs = hasRef(value);
  const preview = useMemo(() => (refs ? resolvePreview(value, scope, name) : null), [refs, value, scope, name]);
  if (!problems.length && !preview) return null;
  return (
    <div id={id} className="mt-2 grid gap-1 font-sans text-xs leading-[18px]">
      {problems.map((p) => (
        <p key={p.raw} className="flex items-start gap-1.5 text-yellow-600">
          <AlertTriangle className="mt-0.5 size-3 shrink-0" aria-hidden />
          <span className="min-w-0">{problemText(p)}</span>
        </p>
      ))}
      {preview?.cycle && (
        <p role="alert" className="flex items-start gap-1.5 text-red-600">
          <AlertTriangle className="mt-0.5 size-3 shrink-0" aria-hidden />
          <span className="min-w-0">{tr("참조가 순환합니다: {path}. 이대로 배포하면 실패합니다.", { path: mono(preview.cycle.join(" → ")) })}</span>
        </p>
      )}
      {preview && !preview.cycle && (
        <div className="min-w-0 text-gray-600">
          <button
            type="button"
            onClick={() => setShow((v) => !v)}
            aria-expanded={show}
            className="inline-flex cursor-pointer items-center gap-1 rounded hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-700"
          >
            {show ? <EyeOff className="size-3" aria-hidden /> : <Eye className="size-3" aria-hidden />}
            {show ? t("풀어 쓴 값 숨기기") : t("풀어 쓴 값 보기")}
          </button>
          {show && (
            <>
              <div className={cx("mt-1 max-h-32 overflow-auto whitespace-pre-wrap break-all rounded border border-gray-200 px-2 py-1 font-mono text-foreground", preview.value === "" && "italic text-gray-500")}>
                {preview.value === "" ? t("<빈 문자열>") : preview.value}
              </div>
              {preview.unknown.length > 0 && (
                <p className="mt-1 text-gray-500">
                  {tr("봉인됐거나 배포할 때 정해지는 값은 참조 그대로 두었습니다: {names}", { names: mono(preview.unknown.join(", ")) })}
                </p>
              )}
            </>
          )}
        </div>
      )}
    </div>
  );
}
