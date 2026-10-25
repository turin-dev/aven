"use client";

import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ArrowLeft, Check, Globe, HardDrive, Network, X } from "lucide-react";
import { api, type ComposePreview, type ComposeRequest, type Environment } from "@/lib/api";
import { qk } from "@/lib/queries";
import { BrandIcon } from "./ServiceIcon";
import { Button, ErrorBox, IconButton, Input, Segmented, Textarea, cx, useEscape } from "./ui";
import { t, tr } from "@/lib/i18n";

const ACTION_LABEL: Record<string, string> = {
  create: t("새로 만들기"),
  update: t("변경|action"),
  unchanged: t("변경 없음"),
  remove: t("삭제"),
  keep: t("유지"),
};

export function ComposeImport({ pid, env, onBack, onClose }: { pid: string; env: Environment; onBack: () => void; onClose: () => void }) {
  const qc = useQueryClient();
  const [source, setSource] = useState<"yaml" | "repo">("yaml");
  const [yaml, setYaml] = useState("");
  const [repo, setRepo] = useState("");
  const [branch, setBranch] = useState("main");
  const [path, setPath] = useState("");
  const [name, setName] = useState("");
  const [envText, setEnvText] = useState("");
  const [allowBind, setAllowBind] = useState(false);
  const [busy, setBusy] = useState<"preview" | "apply" | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [preview, setPreview] = useState<ComposePreview | null>(null);
  const [expose, setExpose] = useState<Record<string, boolean>>({});
  const [prune, setPrune] = useState(false);
  useEscape(() => (preview ? setPreview(null) : onBack()));

  const request = (): ComposeRequest => ({
    source,
    yaml: source === "yaml" ? yaml : undefined,
    repo_url: source === "repo" ? repo.trim() : undefined,
    branch: source === "repo" ? branch.trim() || "main" : undefined,
    path: source === "repo" ? path.trim() : undefined,
    name: name.trim() || undefined,
    env: envText,
    allow_bind_mounts: allowBind,
  });
  const ready = source === "yaml" ? yaml.trim().length > 0 : repo.trim().length > 0;

  const runPreview = async () => {
    setBusy("preview");
    setError(null);
    try {
      const p = await api.composePreview(pid, env.id, request());
      setPreview(p);
      const ex: Record<string, boolean> = {};
      for (const s of p.plan.services) ex[s.compose_name] = false;
      setExpose(ex);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(null);
    }
  };

  const apply = async () => {
    if (!preview) return;
    setBusy("apply");
    setError(null);
    try {
      const domains: Record<string, { target_port?: number }[]> = {};
      for (const s of preview.plan.services) {
        if (expose[s.compose_name]) domains[s.compose_name] = [s.domain_candidates[0] ? { target_port: s.domain_candidates[0].port } : {}];
      }
      await api.composeApply(pid, env.id, { ...request(), domains, prune, deploy: true });
      await Promise.all([qc.invalidateQueries({ queryKey: qk.canvas(env.id) }), qc.invalidateQueries({ queryKey: qk.project(pid) })]);
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(null);
    }
  };

  const plan = preview?.plan;
  const errs = plan?.errors ?? [];
  const warns = plan?.warnings.filter((w) => w.severity === "warning") ?? [];
  const diffOf = (n: string) => preview?.diff.find((d) => d.compose_name === n);

  return (
    <div
      className="fixed inset-0 z-50 flex animate-fade-in items-start justify-center bg-black/[.45] px-4 pt-[6vh] dark:bg-black/80"
      onMouseDown={(e) => e.target === e.currentTarget && !busy && onClose()}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label={t("Docker Compose 가져오기")}
        className="flex max-h-[88vh] w-full max-w-[759px] animate-pop-in flex-col overflow-hidden rounded-lg border border-gray-200 bg-background shadow-[0_32px_80px_-16px_rgba(0,0,0,0.75)] dark:bg-secondaryBg"
      >
        <div className="flex h-14 shrink-0 items-center gap-2 border-b border-gray-200 px-3">
          <IconButton label={t("뒤로")} size="sm" onClick={() => (preview ? setPreview(null) : onBack())}>
            <ArrowLeft />
          </IconButton>
          <span className="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-medium text-gray-600">Docker Compose</span>
          <span className="text-sm font-medium text-foreground">{preview ? t("미리보기") : t("compose 파일 가져오기")}</span>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto p-4">
          {!preview ? (
            <div className="flex flex-col gap-3">
              <Segmented
                label={t("가져오기 방식")}
                value={source}
                onChange={(v) => setSource(v)}
                options={[
                  { value: "yaml", label: t("YAML 붙여넣기") },
                  { value: "repo", label: t("Git 저장소") },
                ]}
              />
              {source === "yaml" ? (
                <Textarea
                  value={yaml}
                  onChange={(e) => setYaml(e.target.value)}
                  placeholder={"services:\n  web:\n    image: nginx:alpine\n    ports: [\"8080:80\"]"}
                  rows={12}
                  spellCheck={false}
                  className="w-full font-mono text-sm"
                  aria-label="compose YAML"
                />
              ) : (
                <div className="grid grid-cols-[1fr_120px] gap-2">
                  <Input mono value={repo} onChange={(e) => setRepo(e.target.value)} placeholder="https://github.com/user/repo" aria-label={t("저장소 URL")} />
                  <Input mono value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="main" aria-label={t("브랜치")} />
                  <Input
                    mono
                    className="col-span-2"
                    value={path}
                    onChange={(e) => setPath(e.target.value)}
                    placeholder={t("compose 파일 경로 (비우면 루트의 docker-compose.yml / compose.yaml)")}
                    aria-label={t("compose 파일 경로")}
                  />
                </div>
              )}
              <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t("스택 이름 (선택, 비우면 compose의 name)")} aria-label={t("스택 이름")} />
              <Textarea
                value={envText}
                onChange={(e) => setEnvText(e.target.value)}
                rows={4}
                spellCheck={false}
                placeholder={`${t("변수 (.env 형식, 선택)")}\nMAILU_VERSION=2024.06`}
                className="w-full font-mono text-sm"
                aria-label=".env"
              />
              <label className="flex cursor-pointer items-center gap-2 text-sm text-gray-600">
                <input type="checkbox" checked={allowBind} onChange={(e) => setAllowBind(e.target.checked)} className="accent-pink-500" />
                {t("bind mount 허용 (호스트 경로에 묶이므로 이 노드에서만 동작합니다)")}
              </label>
            </div>
          ) : (
            <div className="flex flex-col gap-4">
              <div className="flex items-center gap-2 text-sm text-gray-600">
                {tr("스택 {stack}", { stack: <span className="font-mono text-foreground">{plan!.stack}</span> })}
                {preview.exists && <span className="rounded bg-gray-100 px-1.5 py-0.5 text-xs">{t("기존 스택 업데이트")}</span>}
                <span className="ml-auto">{t("{env} 환경", { env: env.name })}</span>
              </div>

              {errs.length > 0 && (
                <div className="rounded-md border border-red-200 bg-red-50 p-3 text-sm text-danger" role="alert">
                  <div className="mb-1 flex items-center gap-1.5 font-medium">
                    <X className="size-4" /> {t("가져올 수 없습니다 ({count})", { count: errs.length })}
                  </div>
                  <ul className="list-disc space-y-1 pl-5">
                    {errs.map((e, i) => (
                      <li key={i}>
                        {e.service && <span className="font-mono">{e.service}: </span>}
                        {e.message}
                      </li>
                    ))}
                  </ul>
                </div>
              )}
              {warns.length > 0 && (
                <details className="rounded-md border border-gray-200 bg-gray-50 p-3 text-sm text-gray-600">
                  <summary className="flex cursor-pointer items-center gap-1.5 text-foreground">
                    <AlertTriangle className="size-4 text-warn" /> {t("경고 {count}개", { count: warns.length })}
                  </summary>
                  <ul className="mt-2 list-disc space-y-1 pl-5">
                    {warns.map((w, i) => (
                      <li key={i}>
                        {w.service && <span className="font-mono">{w.service}: </span>}
                        {w.message}
                      </li>
                    ))}
                  </ul>
                </details>
              )}

              <div>
                <div className="mb-1.5 text-xs font-medium tabular-nums text-gray-400">{t("서비스 {count}개 · 배포 순서", { count: plan!.services.length })}</div>
                <div className="flex flex-col gap-2">
                  {plan!.services.map((s) => {
                    const d = diffOf(s.compose_name);
                    const level = plan!.levels.findIndex((l) => l.includes(s.compose_name)) + 1;
                    return (
                      <div key={s.compose_name} className="rounded-md border border-gray-200 bg-gray-50 p-3">
                        <div className="flex items-center gap-2">
                          <BrandIcon name={s.icon} className="size-[18px]" />
                          <span className="text-sm font-medium text-foreground">{s.name}</span>
                          {s.name !== s.compose_name && <span className="font-mono text-xs text-gray-400">({s.compose_name})</span>}
                          <span className="rounded bg-gray-100 px-1.5 py-0.5 text-xs tabular-nums text-gray-600">{t("{level}단계", { level })}</span>
                          {d && (
                            <span
                              className={cx(
                                "ml-auto rounded px-1.5 py-0.5 text-xs",
                                d.action === "create" ? "bg-pink-100 text-pink-600" : d.action === "update" ? "bg-gray-100 text-foreground" : "bg-gray-100 text-gray-400",
                              )}
                            >
                              {ACTION_LABEL[d.action]}
                            </span>
                          )}
                        </div>
                        <div className="mt-1.5 truncate font-mono text-xs text-gray-600">
                          {s.build ? `build ${s.build.context} (${s.build.dockerfile}${s.build.target ? `, target ${s.build.target}` : ""})` : s.image}
                        </div>
                        <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-gray-600">
                          {s.port && <span>{t("포트 {port}", { port: s.port })}</span>}
                          <span>{s.healthcheck_type === "none" ? t("헬스체크 없음") : t("헬스체크 {type}", { type: s.healthcheck_type })}</span>
                          <span>{t("재시작 {policy}", { policy: s.restart_policy })}</span>
                          {s.replicas > 1 && <span>{t("레플리카 {count}", { count: s.replicas })}</span>}
                          <span>{t("변수 {count}개", { count: s.env_keys.length })}</span>
                          {s.depends_on.length > 0 && <span>{t("의존 {services}", { services: s.depends_on.map((x) => x.service).join(", ") })}</span>}
                        </div>
                        {(s.volumes.length > 0 || s.aliases.length > 0) && (
                          <div className="mt-1.5 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-gray-600">
                            {s.volumes.map((v) => (
                              <span key={v.mount_path} className="flex items-center gap-1">
                                <HardDrive className="size-3.5" />
                                <span className="font-mono">{v.name}</span> → {v.mount_path}
                              </span>
                            ))}
                            {s.aliases.length > 0 && (
                              <span className="flex items-center gap-1">
                                <Network className="size-3.5" /> DNS {s.aliases.join(", ")}
                              </span>
                            )}
                          </div>
                        )}
                        {s.domain_candidates.length > 0 && (
                          <label className="mt-2 flex cursor-pointer items-center gap-2 text-xs text-gray-600">
                            <input
                              type="checkbox"
                              checked={!!expose[s.compose_name]}
                              onChange={(e) => setExpose((x) => ({ ...x, [s.compose_name]: e.target.checked }))}
                              className="accent-pink-500"
                            />
                            <Globe className="size-3.5" />
                            {s.domain_candidates[0].host
                              ? t("도메인 공개 (포트 {port}, 기존 {host}) · 기본은 비공개", { port: s.domain_candidates[0].port, host: s.domain_candidates[0].host })
                              : t("도메인 공개 (포트 {port}) · 기본은 비공개", { port: s.domain_candidates[0].port })}
                          </label>
                        )}
                        {d && d.changes.length > 0 && d.action !== "create" && (
                          <ul className="mt-2 list-disc space-y-0.5 pl-5 text-xs text-gray-600">
                            {d.changes.map((c, i) => (
                              <li key={i}>{c}</li>
                            ))}
                          </ul>
                        )}
                      </div>
                    );
                  })}
                </div>
              </div>
              {preview.diff.some((d) => d.action === "remove" || d.action === "keep") && (
                <label className="flex cursor-pointer items-center gap-2 text-sm text-gray-600">
                  <input type="checkbox" checked={prune} onChange={(e) => setPrune(e.target.checked)} className="accent-pink-500" />
                  {t("compose에서 사라진 서비스 삭제 ({services})", { services: preview.diff.filter((d) => d.action === "remove" || d.action === "keep").map((d) => d.name).join(", ") })}
                </label>
              )}
            </div>
          )}
          {error ? <ErrorBox className="mt-3" error={error} /> : null}
        </div>

        <div className="flex shrink-0 items-center gap-2 border-t border-gray-200 px-4 py-3">
          <span className="text-xs text-gray-400">{t("비밀값은 변수로 저장됩니다. 서비스는 의존 순서대로 배포됩니다.")}</span>
          <span className="ml-auto flex gap-2">
            {!preview ? (
              <Button variant="primary" loading={busy === "preview"} disabled={!ready || busy !== null} onClick={runPreview}>
                {t("미리보기")}
              </Button>
            ) : (
              <Button variant="primary" loading={busy === "apply"} disabled={!preview.ok || busy !== null} onClick={apply}>
                {busy !== "apply" && <Check />} {preview.exists ? t("적용하고 배포") : t("가져오고 배포")}
              </Button>
            )}
          </span>
        </div>
      </div>
    </div>
  );
}
