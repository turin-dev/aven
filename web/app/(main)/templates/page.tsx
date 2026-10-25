"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { Suspense, useEffect, useMemo, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Download, Rocket, Search, Trash2, X } from "lucide-react";
import { api, type Template, type TemplateVisibility } from "@/lib/api";
import { qk, useMe, useProject, useProjects, useTemplateDetail, useTemplateSearch } from "@/lib/queries";
import { canvasHref, useCtxHref } from "@/lib/nav";
import { toast } from "@/lib/uiState";
import { t } from "@/lib/i18n";
import { useWorkspace } from "@/lib/workspace";
import { BrandIcon } from "@/components/ServiceIcon";
import { lock } from "@/components/Perm";
import { MarkdownLite, TemplateInputsForm, inputErrorsFrom, inputValues, validateInputs, visibilityLabel } from "@/components/TemplateParts";
import { Button, CenterSpinner, Dialog, EmptyState, ErrorBox, Field, Input, Segmented, Select, cx, useConfirm } from "@/components/ui";

type SortKey = "" | "popular" | "new";

const CATEGORY_LABEL: Record<string, string> = {
  databases: t("데이터베이스|plural"),
  storage: t("스토리지"),
  automation: t("자동화"),
  analytics: t("분석"),
  monitoring: t("모니터링"),
  cms: "CMS",
};

const catLabel = (c: string) => CATEGORY_LABEL[c] ?? c;

function TemplateCard({ t: tpl, onOpen }: { t: Template; onOpen: () => void }) {
  return (
    <button
      type="button"
      onClick={onOpen}
      className="flex h-full w-full cursor-pointer flex-col rounded-lg border border-gray-200 bg-secondaryBg p-4 text-left transition-colors hover:border-gray-300 hover:bg-gray-100/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-pink-500"
    >
      <div className="flex items-center gap-3">
        <span className="flex size-10 shrink-0 items-center justify-center rounded-lg border border-gray-200 bg-gray-50">
          <BrandIcon name={tpl.icon || tpl.services[0]?.icon || "docker"} className="size-5" />
        </span>
        <span className="min-w-0 flex-1">
          <span className="block truncate text-base font-semibold text-foreground">{tpl.name}</span>
          <span className="block truncate font-mono text-xs text-gray-500">{tpl.key}</span>
        </span>
        {!tpl.builtin && (
          <span className="shrink-0 rounded bg-gray-100 px-1.5 py-0.5 text-xs font-medium text-gray-600">{visibilityLabel(tpl.visibility)}</span>
        )}
      </div>
      <p className="mt-3 line-clamp-2 min-h-[2.6em] text-sm leading-snug text-gray-600">{tpl.description || t("설명 없음")}</p>
      <div className="mt-3 flex items-center gap-2 text-xs text-gray-500">
        {tpl.category && <span className="rounded bg-gray-100 px-1.5 py-0.5 font-medium text-gray-600">{catLabel(tpl.category)}</span>}
        <span>{t("서비스 {n}개", { n: tpl.services.length })}</span>
        <span className="ml-auto tabular-nums" aria-label={t("배포 {n}회", { n: tpl.deploy_count })}>
          <Rocket className="mr-1 inline size-3" aria-hidden="true" />
          {tpl.deploy_count.toLocaleString()}
        </span>
      </div>
    </button>
  );
}

function TemplateDetailDialog({ tkey, onClose }: { tkey: string | null; onClose: () => void }) {
  const qc = useQueryClient();
  const router = useRouter();
  const ws = useWorkspace();
  const me = useMe().data;
  const detail = useTemplateDetail(tkey);
  const tpl = detail.data;
  const projects = useProjects();
  const [pid, setPid] = useState<string>("");
  const targetPid = pid || ws.pid || projects.data?.[0]?.id || "";
  const project = useProject(targetPid || null);
  const [eid, setEid] = useState<string>("");
  const envs = project.data?.environments ?? [];
  const targetEid =
    (envs.find((e) => e.id === eid)?.id ?? (targetPid === ws.pid ? ws.env?.id : undefined)) || project.data?.default_environment_id || "";
  const [values, setValues] = useState<Record<string, string>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const confirm = useConfirm();

  useEffect(() => {
    setValues({});
    setErrors({});
    setError(null);
  }, [tkey]);

  const role = me?.projects?.[targetPid];
  const canDeploy = !me || role === "admin" || role === "member";

  const close = () => {
    if (!busy) onClose();
  };

  const deploy = async () => {
    if (!tpl || !targetPid || !targetEid) return;
    const errs = validateInputs(tpl.inputs, values);
    setErrors(errs);
    if (Object.keys(errs).length) return;
    setBusy(true);
    setError(null);
    try {
      const svcs = await api.deployTemplate(targetPid, tpl.key, targetEid, inputValues(values));
      await Promise.all([qc.invalidateQueries({ queryKey: qk.templates }), qc.invalidateQueries({ queryKey: qk.project(targetPid) })]);
      const env = envs.find((e) => e.id === targetEid);
      toast(t("템플릿 {name}을(를) {env} 환경에 배포합니다", { name: tpl.name, env: env?.name ?? "" }));
      onClose();
      router.push(canvasHref({ svc: svcs[0]?.id ?? null }, { project: targetPid, env: env?.name }));
    } catch (e) {
      const fieldErrs = inputErrorsFrom(e);
      if (fieldErrs) setErrors(fieldErrs);
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  const setVisibility = async (v: TemplateVisibility) => {
    if (!tpl) return;
    try {
      await api.updateTemplate(tpl.key, { visibility: v });
      await qc.invalidateQueries({ queryKey: qk.templates });
    } catch (e) {
      setError(e);
    }
  };

  const remove = () => {
    if (!tpl) return;
    confirm.confirm({
      title: t("{name} 템플릿 삭제", { name: tpl.name }),
      message: t("템플릿만 지웁니다. 이 템플릿으로 이미 만든 서비스는 그대로 남습니다."),
      confirmLabel: t("삭제"),
      danger: true,
      run: async () => {
        try {
          await api.deleteTemplate(tpl.key);
          await qc.invalidateQueries({ queryKey: qk.templates });
          toast(t("템플릿 {name}을(를) 삭제했습니다", { name: tpl.name }));
          onClose();
        } catch (e) {
          setError(e);
        }
      },
    });
  };

  return (
    <>
      <Dialog
        open={!!tkey}
        onClose={close}
        wide
        title={tpl ? tpl.name : t("템플릿")}
        footer={
          <>
            {tpl && (
              <a
                href={api.templateExportUrl(tpl.key)}
                download={`${tpl.key}.json`}
                className="mr-auto inline-flex h-[34px] items-center gap-2 rounded-md px-3 text-sm text-foreground hover:bg-gray-100 focus:outline-none focus-visible:ring-2 focus-visible:ring-gray-600"
              >
                <Download className="size-4 text-gray-500" aria-hidden="true" /> {t("JSON 내려받기")}
              </a>
            )}
            <Button variant="outline" onClick={close} disabled={busy}>
              {t("닫기")}
            </Button>
            <Button variant="primary" onClick={deploy} loading={busy} disabled={!tpl || !targetEid} {...lock(canDeploy)}>
              {t("배포")}
            </Button>
          </>
        }
      >
        {detail.error ? (
          <ErrorBox error={detail.error} />
        ) : !tpl ? (
          <CenterSpinner />
        ) : (
          <div className="space-y-5 pt-1">
            <div className="flex items-start gap-3">
              <span className="flex size-11 shrink-0 items-center justify-center rounded-lg border border-gray-200 bg-gray-50">
                <BrandIcon name={tpl.icon || "docker"} className="size-6" />
              </span>
              <div className="min-w-0 flex-1">
                <p className="text-sm text-foreground">{tpl.description}</p>
                <div className="mt-1 flex flex-wrap items-center gap-1.5 text-xs tabular-nums text-gray-500">
                  {tpl.builtin ? <span>{t("내장 템플릿")}</span> : <span>{visibilityLabel(tpl.visibility)}</span>}
                  {tpl.category && <span>· {catLabel(tpl.category)}</span>}
                  <span>· {t("배포 {n}회", { n: tpl.deploy_count })}</span>
                  {tpl.tags.map((tag) => (
                    <span key={tag} className="rounded bg-gray-100 px-1.5 py-0.5 font-medium text-gray-600">
                      {tag}
                    </span>
                  ))}
                </div>
              </div>
            </div>

            {tpl.readme && (
              <section aria-label={t("설명서")} className="rounded-md border border-gray-200 bg-gray-50/40 px-4 py-3">
                <MarkdownLite text={tpl.readme} />
              </section>
            )}

            <section>
              <h3 className="mb-1.5 text-sm font-semibold text-foreground">{t("서비스|plural")}</h3>
              <ul className="divide-y divide-gray-200 rounded-md border border-gray-200">
                {tpl.services.map((s) => (
                  <li key={s.name} className="flex items-center gap-2.5 px-3 py-2">
                    <BrandIcon name={s.icon || s.database_kind || (s.source_type === "repo" ? "github" : "docker")} className="size-4" />
                    <span className="text-sm font-medium text-foreground">{s.name}</span>
                    <span className="min-w-0 truncate font-mono text-xs text-gray-500">
                      {s.source_type === "database" ? s.database_kind : s.source_type === "repo" ? s.repo_url : s.image}
                    </span>
                  </li>
                ))}
              </ul>
            </section>

            <section className="space-y-3">
              <h3 className="text-sm font-semibold text-foreground">{t("배포할 곳")}</h3>
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label={t("프로젝트")}>
                  <Select
                    value={targetPid}
                    onChange={(e) => {
                      setPid(e.target.value);
                      setEid("");
                    }}
                  >
                    {(projects.data ?? []).map((p) => (
                      <option key={p.id} value={p.id}>
                        {p.name}
                      </option>
                    ))}
                  </Select>
                </Field>
                <Field label={t("환경")}>
                  <Select value={targetEid} onChange={(e) => setEid(e.target.value)} disabled={!envs.length}>
                    {envs.map((e) => (
                      <option key={e.id} value={e.id}>
                        {e.name}
                      </option>
                    ))}
                  </Select>
                </Field>
              </div>
              {!canDeploy && <p className="text-xs text-gray-500">{t("이 프로젝트에 배포하려면 member 이상 권한이 필요합니다.")}</p>}
              <TemplateInputsForm
                inputs={tpl.inputs}
                values={values}
                errors={errors}
                onChange={(k, v) => {
                  setValues((x) => ({ ...x, [k]: v }));
                  setErrors((x) => {
                    const n = { ...x };
                    delete n[k];
                    return n;
                  });
                }}
              />
            </section>

            {tpl.can_edit && !tpl.builtin && (
              <section className="flex flex-wrap items-center gap-3 border-t border-gray-200 pt-4">
                <span className="text-sm font-medium text-gray-600">{t("공개 범위")}</span>
                <Segmented
                  size="sm"
                  label={t("공개 범위")}
                  value={tpl.visibility}
                  onChange={setVisibility}
                  options={[
                    { value: "private", label: t("나만") },
                    { value: "workspace", label: t("작업 공간") },
                    { value: "public", label: t("공개") },
                  ]}
                />
                <Button variant="danger" size="sm" className="ml-auto" onClick={remove}>
                  <Trash2 className="size-3.5" aria-hidden="true" /> {t("삭제")}
                </Button>
              </section>
            )}
            {error ? <ErrorBox error={error} /> : null}
          </div>
        )}
      </Dialog>
      {confirm.node}
    </>
  );
}

function TemplatesInner() {
  const href = useCtxHref();
  const [q, setQ] = useState("");
  const [debounced, setDebounced] = useState("");
  const [category, setCategory] = useState("");
  const [sort, setSort] = useState<SortKey>("");
  const [open, setOpen] = useState<string | null>(null);
  useEffect(() => {
    const id = setTimeout(() => setDebounced(q.trim()), 200);
    return () => clearTimeout(id);
  }, [q]);
  useEffect(() => {
    document.title = `${t("템플릿")} · aven`;
    const k = new URLSearchParams(window.location.search).get("template");
    if (k) setOpen(k);
    return () => {
      document.title = "aven";
    };
  }, []);
  const all = useTemplateSearch({ sort: sort || undefined });
  const list = useTemplateSearch({ q: debounced || undefined, category: category || undefined, sort: sort || undefined });
  const categories = useMemo(() => [...new Set((all.data ?? []).map((tp) => tp.category).filter(Boolean))].sort(), [all.data]);

  return (
    <div className="min-h-full">
      <div className="mx-auto flex min-h-full max-w-[1120px] flex-col pt-12">
        <div className="flex items-center px-6 pb-4 sm:px-12">
          <h1 className="flex-1 text-h2 font-normal">{t("템플릿")}</h1>
        </div>
        <p className="px-6 pb-4 text-sm text-gray-600 sm:px-12">{t("템플릿을 찾아 환경에 배포하거나, 내 템플릿을 공유·수정·삭제합니다.")}</p>
        <div className="space-y-5 px-6 py-8 sm:px-12">
          <div className="flex flex-wrap items-center gap-3">
            <div className="relative min-w-[220px] flex-1">
              <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-gray-400" aria-hidden="true" />
              <Input
                type="search"
                aria-label={t("템플릿 검색")}
                placeholder={t("이름, 설명, 태그로 검색")}
                value={q}
                onChange={(e) => setQ(e.target.value)}
                className="w-full pl-9"
              />
            </div>
            <Segmented
              label={t("정렬")}
              value={sort}
              onChange={setSort}
              options={[
                { value: "", label: t("기본") },
                { value: "popular", label: t("인기") },
                { value: "new", label: t("최신") },
              ]}
            />
          </div>
          <div role="radiogroup" aria-label={t("분류")} className="flex flex-wrap gap-1.5">
            {["", ...categories].map((c) => (
              <button
                key={c || "all"}
                type="button"
                role="radio"
                aria-checked={category === c}
                onClick={() => setCategory(c)}
                className={cx(
                  "h-7 cursor-pointer rounded-full border px-3 text-xs font-medium transition-colors",
                  category === c ? "border-pink-500 bg-pink-100/40 text-foreground" : "border-gray-200 text-gray-500 hover:border-gray-300 hover:text-foreground",
                )}
              >
                {c ? catLabel(c) : t("전체")}
              </button>
            ))}
          </div>
          <ErrorBox error={list.error} />
          {list.isPending ? (
            <CenterSpinner />
          ) : (list.data ?? []).length === 0 ? (
            <EmptyState icon={<Search className="size-5" />} title={t("일치하는 템플릿이 없습니다")} />
          ) : (
            <ul className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3" aria-label={t("템플릿 목록")}>
              {(list.data ?? []).map((tp) => (
                <li key={tp.key}>
                  <TemplateCard t={tp} onOpen={() => setOpen(tp.key)} />
                </li>
              ))}
            </ul>
          )}
          <p className="text-xs text-gray-500">
            {t("환경 메뉴의 ‘템플릿으로 게시…’로 지금 환경을 템플릿으로 만들 수 있습니다. 정의 형식은 docs/api-v1.md의 Templates 절에 있습니다.")}
          </p>
        </div>
      </div>
      <TemplateDetailDialog tkey={open} onClose={() => setOpen(null)} />
    </div>
  );
}

export default function TemplatesPage() {
  return (
    <Suspense fallback={<div className="dot-grid min-h-full" />}>
      <TemplatesInner />
    </Suspense>
  );
}
