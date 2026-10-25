"use client";

import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Box, ChevronRight, CornerDownLeft, Database, GitBranch, HardDrive, LayoutTemplate, RefreshCw, Settings, Sparkles, SquareDashed } from "lucide-react";
import Link from "next/link";
import { ApiError, api, type CanvasService, type DatabaseKind, type Environment, type GithubRepo, type Template } from "@/lib/api";
import { qk, useGithub, useTemplates } from "@/lib/queries";
import { useCtxHref, type NewView } from "@/lib/nav";
import { BrandIcon } from "./ServiceIcon";
import { useGuardrails } from "./AccessControlsSettings";
import { ComposeImport } from "./ComposeImport";
import { useKeyboardShortcuts } from "./KeyboardShortcuts";
import { RegistryHint } from "./panel/SettingsTab";
import { TemplateInputsForm, inputErrorsFrom, inputValues, validateInputs } from "./TemplateParts";
import { Button, ErrorBox, IconButton, Spinner, cx, useEscape } from "./ui";
import { t } from "@/lib/i18n";

type Entry = {
  id: string;
  label: string;
  desc?: string;
  icon: ReactNode;
  group?: string;
  /** Opens a sub view instead of running. */
  view?: NewView;
  run?: () => Promise<string | null | void>;
  keywords?: string;
  /** Template entries with inputs open an inputs form instead of deploying right away. */
  tpl?: Template;
  /** Repo entries open the branch step. */
  repo?: PickedRepo;
};

type PickedRepo = { url: string; name: string; branch: string };

/** Create options whose source can't be verified against an approved GitHub organization (deployment source guardrail). */
const UNVERIFIED_SOURCES = new Set(["function", "template", "image", "compose"]);

function templateMatch(q: string, tp: Template) {
  return match(q, tp.name, tp.key, tp.description, tp.category, ...tp.tags, ...tp.services.map((s) => s.name));
}

const DBS: { kind: DatabaseKind; label: string; desc: string }[] = [
  { kind: "postgres", label: "PostgreSQL", desc: t("관계형 데이터베이스. 볼륨과 접속 변수가 함께 만들어집니다") },
  { kind: "mysql", label: "MySQL", desc: t("관계형 데이터베이스") },
  { kind: "redis", label: "Redis", desc: t("인메모리 키-값 저장소") },
  { kind: "mongo", label: "MongoDB", desc: t("문서 데이터베이스") },
];

const IMAGES = ["nginx:alpine", "traefik/whoami", "caddy:2", "ghcr.io/umami-software/umami:postgresql-latest", "louislam/uptime-kuma:1", "minio/minio"];

const TITLES: Record<NewView, string> = {
  "1": t("무엇을 만들까요?"),
  repo: t("GitHub 저장소"),
  database: t("데이터베이스"),
  template: t("템플릿|item"),
  image: t("Docker 이미지"),
  compose: "Docker Compose",
  volume: t("볼륨"),
};

const PLACEHOLDER: Record<NewView, string> = {
  "1": t("무엇을 만들까요?"),
  repo: t("저장소 검색 또는 URL 붙여넣기…"),
  database: t("데이터베이스 검색"),
  template: t("템플릿 검색"),
  image: t("이미지 이름, 예: nginx:alpine"),
  compose: "",
  volume: t("볼륨을 연결할 서비스 검색"),
};

function match(q: string, ...parts: (string | undefined)[]) {
  const needle = q.trim().toLowerCase();
  return !needle || parts.some((p) => p?.toLowerCase().includes(needle));
}

function repoOk(url: string) {
  return /^(https?:\/\/|git@)[^\s]+$/.test(url.trim()) || /^[\w.-]+\/[\w.-]+$/.test(url.trim());
}
function normRepo(url: string) {
  const u = url.trim();
  return /^[\w.-]+\/[\w.-]+$/.test(u) ? `https://github.com/${u}` : u;
}
/** "owner/repo" out of a GitHub URL or shorthand, lowercased, for matching against the repo list. */
function repoKey(url: string) {
  return normRepo(url)
    .replace(/^(https?:\/\/|git@)github\.com[/:]/i, "")
    .replace(/\.git$|\/+$/g, "")
    .toLowerCase();
}

export function CreatePalette({
  pid,
  env,
  view,
  services,
  onView,
  onClose,
  onCreated,
}: {
  pid: string;
  env: Environment;
  view: NewView;
  services: CanvasService[];
  onView: (v: NewView) => void;
  onClose: () => void;
  onCreated: (serviceId: string) => void;
}) {
  const qc = useQueryClient();
  const [q, setQ] = useState("");
  const [active, setActive] = useState(0);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [picked, setPicked] = useState<PickedRepo | null>(null);
  const [volTarget, setVolTarget] = useState<CanvasService | null>(null);
  const [tpl, setTpl] = useState<Template | null>(null);
  const [tplValues, setTplValues] = useState<Record<string, string>>({});
  const [tplErrors, setTplErrors] = useState<Record<string, string>>({});
  const href = useCtxHref();
  const input = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const templates = useTemplates(view === "template" || (view === "1" && q.trim().length > 0));
  const gh = useGithub();
  const repos = useQuery({ queryKey: qk.githubRepos, queryFn: () => api.githubRepos(), enabled: view === "repo", staleTime: 60_000, retry: false });
  const ghDisconnected = repos.error instanceof ApiError && repos.error.status === 409;
  const sourcesRestricted = useGuardrails().deploymentSources;

  const back = () => {
    if (busy) return;
    if (tpl) {
      setTpl(null);
      setTplErrors({});
    } else if (picked) {
      setPicked(null);
      setQ("");
    } else if (volTarget) {
      setVolTarget(null);
      setQ("");
    } else if (view !== "1") {
      onView("1");
      setQ("");
    } else onClose();
  };
  useEscape(back);

  useEffect(() => {
    setActive(0);
    setError(null);
    if (!tpl) input.current?.focus();
  }, [view, volTarget, tpl, picked]);

  const done = async () => {
    await Promise.all([
      qc.invalidateQueries({ queryKey: qk.canvas(env.id) }),
      qc.invalidateQueries({ queryKey: qk.project(pid) }),
      qc.invalidateQueries({ queryKey: qk.templates }),
    ]);
  };

  const create = async (key: string, fn: () => Promise<string | null | void>) => {
    setBusy(key);
    setError(null);
    try {
      const id = await fn();
      await done();
      if (id) onCreated(id);
      else onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(null);
    }
  };

  const deployTpl = async () => {
    if (!tpl || busy) return;
    const errs = validateInputs(tpl.inputs, tplValues);
    setTplErrors(errs);
    if (Object.keys(errs).length) return;
    setBusy(`tpl-${tpl.key}`);
    setError(null);
    try {
      const id = (await api.deployTemplate(pid, tpl.key, env.id, inputValues(tplValues)))[0]?.id ?? null;
      await done();
      if (id) onCreated(id);
      else onClose();
    } catch (e) {
      const fe = inputErrorsFrom(e);
      if (fe) setTplErrors(fe);
      setError(e);
    } finally {
      setBusy(null);
    }
  };

  const dbEntry = (d: (typeof DBS)[number], group?: string): Entry => ({
    id: `db-${d.kind}`,
    label: d.label,
    desc: d.desc,
    group,
    icon: <BrandIcon name={d.kind} className="size-[18px]" />,
    keywords: `database db 데이터베이스 ${d.kind}`,
    run: async () => (await api.createService(pid, { source_type: "database", database_kind: d.kind, environment_id: env.id })).id,
  });

  const entries: Entry[] = useMemo(() => {
    if (view === "1") {
      const root: Entry[] = [
        { id: "repo", label: t("GitHub 저장소"), desc: t("저장소를 빌드해서 배포합니다"), icon: <BrandIcon name="github" className="size-[18px]" />, view: "repo", keywords: "git github repo 저장소" },
        { id: "database", label: t("데이터베이스"), desc: "Postgres, MySQL, Redis, MongoDB", icon: <Database className="size-[18px] text-gray-600" />, view: "database", keywords: "db postgres mysql redis mongo" },
        {
          id: "bucket",
          label: "Bucket",
          desc: t("S3 호환 객체 저장소. 볼륨과 접속 변수(BUCKET, ENDPOINT, ACCESS_KEY_ID …)가 함께 만들어집니다"),
          icon: <BrandIcon name="bucket" className="size-[18px]" />,
          keywords: "bucket s3 object storage minio 버킷 객체 저장소 파일",
          run: async () => (await api.createService(pid, { source_type: "database", database_kind: "bucket", environment_id: env.id })).id,
        },
        {
          id: "function",
          label: "Function",
          desc: t("Bun으로 실행되는 TypeScript 파일 하나. 대시보드의 코드 탭에서 고치고 바로 배포합니다"),
          icon: <BrandIcon name="function" className="size-[18px]" />,
          keywords: "function serverless bun typescript javascript 함수 코드 cron",
          run: async () => (await api.createService(pid, { source_type: "function", environment_id: env.id })).id,
        },
        { id: "template", label: t("템플릿|item"), desc: t("미리 구성된 서비스 묶음"), icon: <LayoutTemplate className="size-[18px] text-gray-600" />, view: "template", keywords: "template 템플릿" },
        { id: "image", label: t("Docker 이미지"), desc: t("레지스트리의 이미지를 그대로 실행합니다"), icon: <BrandIcon name="docker" className="size-[18px]" />, view: "image", keywords: "docker image 이미지" },
        { id: "compose", label: "Docker Compose", desc: t("compose 파일의 서비스를 한 번에 가져옵니다"), icon: <BrandIcon name="docker" className="size-[18px]" />, view: "compose", keywords: "compose stack yaml 스택" },
        {
          id: "empty",
          label: t("빈 서비스"),
          desc: t("소스 없이 만들고 나중에 연결합니다"),
          icon: <SquareDashed className="size-[18px] text-gray-600" />,
          keywords: "empty 빈",
          run: async () => (await api.createService(pid, { source_type: "image" })).id,
        },
        { id: "volume", label: t("볼륨"), desc: t("서비스에 영구 디스크를 연결합니다"), icon: <HardDrive className="size-[18px] text-gray-600" />, view: "volume", keywords: "volume disk 볼륨 디스크" },
      ];
      // 배포 출처 guardrail: 출처를 GitHub 조직으로 확인할 수 없는 항목은 숨긴다(서버도 403).
      const top = root.filter((e) => !(sourcesRestricted && UNVERIFIED_SOURCES.has(e.id)) && match(q, e.label, e.desc, e.keywords));
      if (!q.trim()) return top;
      const dbs = DBS.filter((d) => match(q, d.label, d.kind)).map((d) => dbEntry(d, t("데이터베이스")));
      const tpls = (sourcesRestricted ? [] : (templates.data ?? []))
        .filter((tp) => templateMatch(q, tp))
        .map<Entry>((tp) => ({
          id: `tpl-${tp.key}`,
          label: tp.name,
          desc: tp.description,
          group: t("템플릿"),
          icon: <BrandIcon name={tp.icon} className="size-[18px]" />,
          tpl: tp,
          run: async () => (await api.deployTemplate(pid, tp.key, env.id))[0]?.id ?? null,
        }));
      return [...top, ...dbs, ...tpls];
    }
    if (view === "database") return DBS.filter((d) => match(q, d.label, d.kind)).map((d) => dbEntry(d));
    if (view === "template")
      return (templates.data ?? [])
        .filter((tp) => templateMatch(q, tp))
        .map((tp) => ({
          id: `tpl-${tp.key}`,
          label: tp.name,
          desc: tp.builtin
            ? t("{description} · 서비스 {count}개", { description: tp.description, count: tp.services.length })
            : t("{description} · 서비스 {count}개 · 사용자 템플릿", { description: tp.description, count: tp.services.length }),
          icon: <BrandIcon name={tp.icon} className="size-[18px]" />,
          tpl: tp,
          run: async () => (await api.deployTemplate(pid, tp.key, env.id))[0]?.id ?? null,
        }));
    if (view === "image") {
      const typed = q.trim();
      const list: Entry[] = [];
      if (typed)
        list.push({
          id: `img-typed`,
          label: typed,
          desc: t("이 이미지로 서비스 만들기"),
          icon: <BrandIcon name="docker" className="size-[18px]" />,
          run: async () => (await api.createService(pid, { source_type: "image", image: typed, environment_id: env.id })).id,
        });
      for (const img of IMAGES.filter((i) => i !== typed && match(q, i)))
        list.push({
          id: `img-${img}`,
          label: img,
          icon: <Box className="size-[18px] text-gray-600" />,
          run: async () => (await api.createService(pid, { source_type: "image", image: img, environment_id: env.id })).id,
        });
      return list;
    }
    if (view === "repo") {
      if (picked) {
        const b = q.trim() || picked.branch;
        return [
          {
            id: "repo-go",
            label: t("{branch} 브랜치 배포", { branch: b }),
            desc: picked.name,
            icon: <GitBranch className="size-[18px] text-gray-600" />,
            run: async () => (await api.createService(pid, { source_type: "repo", repo_url: picked.url, branch: b, environment_id: env.id })).id,
          },
        ];
      }
      const typed = q.trim();
      const key = repoOk(typed) ? repoKey(typed) : "";
      const list = (repos.data ?? [])
        .filter((r) => (key && r.full_name.toLowerCase() === key) || match(typed, r.full_name))
        .map<Entry>((r: GithubRepo) => ({
          id: `repo-${r.full_name}`,
          label: r.full_name,
          desc: r.private ? t("비공개 저장소") : undefined,
          icon: <BrandIcon name="github" className="size-[18px]" />,
          repo: { url: r.url, name: r.full_name, branch: r.default_branch || "main" },
        }));
      // 목록에 없는 URL·user/repo를 붙여넣으면 그대로 쓸 수 있게 맨 위에 둔다.
      if (key && !list.some((e) => e.label.toLowerCase() === key))
        list.unshift({
          id: "repo-url",
          label: normRepo(typed).replace(/^https?:\/\//, ""),
          desc: t("이 URL로 배포"),
          icon: <BrandIcon name="github" className="size-[18px]" />,
          repo: { url: normRepo(typed), name: normRepo(typed).replace(/^https?:\/\//, ""), branch: "main" },
        });
      return list;
    }
    if (view === "volume") {
      if (!volTarget)
        return services
          .filter((s) => match(q, s.name))
          .map((s) => ({
            id: `vol-${s.id}`,
            label: s.name,
            desc: s.volumes.length ? t("이미 {path}에 볼륨이 있습니다", { path: s.volumes[0].mount_path }) : t("이 서비스에 볼륨 연결"),
            icon: <BrandIcon name={s.icon ?? s.database_kind ?? (s.source_type === "repo" ? "github" : "docker")} className="size-[18px]" />,
          }));
      const path = q.trim() || "/data";
      return [
        {
          id: "vol-go",
          label: t("{path}에 마운트", { path }),
          desc: t("{name}에 볼륨을 만듭니다. 다음 배포부터 적용됩니다", { name: volTarget.name }),
          icon: <HardDrive className="size-[18px] text-gray-600" />,
          run: async () => {
            if (!path.startsWith("/")) throw new Error(t("마운트 경로는 /로 시작해야 합니다."));
            await api.createVolume(env.id, { service_id: volTarget.id, mount_path: path });
            return volTarget.id;
          },
        },
      ];
    }
    return [];
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [view, q, templates.data, repos.data, picked, services, volTarget, pid, env.id, sourcesRestricted]);

  const idx = Math.min(active, Math.max(0, entries.length - 1));

  const choose = (e: Entry | undefined) => {
    if (!e || busy) return;
    if (e.view) {
      onView(e.view);
      setQ("");
      return;
    }
    if (e.repo) {
      setPicked(e.repo);
      setQ(e.repo.branch);
      return;
    }
    if (e.tpl && e.tpl.inputs.length > 0) {
      setTpl(e.tpl);
      setTplValues({});
      setTplErrors({});
      return;
    }
    if (view === "volume" && !volTarget) {
      const s = services.find((x) => `vol-${x.id}` === e.id);
      if (s) {
        setVolTarget(s);
        setQ("/data");
      }
      return;
    }
    if (e.run) create(e.id, e.run);
  };

  useEffect(() => {
    listRef.current?.querySelector(`[data-idx="${idx}"]`)?.scrollIntoView({ block: "nearest" });
  }, [idx]);

  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setActive((a) => Math.min(entries.length - 1, a + 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setActive((a) => Math.max(0, a - 1));
    } else if (e.key === "ArrowRight" && (entries[idx]?.view || entries[idx]?.repo)) {
      e.preventDefault();
      choose(entries[idx]);
    } else if (e.key === "Enter") {
      e.preventDefault();
      choose(entries[idx]);
    } else if (e.key === "Backspace" && q === "" && view !== "1") {
      e.preventDefault();
      back();
    }
  };
  useKeyboardShortcuts([
    { keys: "arrowup", label: t("명령 팔레트 결과 위로 이동"), group: t("명령 팔레트") },
    { keys: "arrowdown", label: t("명령 팔레트 결과 아래로 이동"), group: t("명령 팔레트") },
    { keys: "enter", label: t("선택한 명령 실행"), group: t("명령 팔레트") },
    { keys: "arrowright", label: t("선택한 하위 메뉴 열기"), group: t("명령 팔레트"), enabled: !!(entries[idx]?.view || entries[idx]?.repo) },
    { keys: "backspace", label: t("이전 메뉴로 돌아가기"), group: t("명령 팔레트"), enabled: q === "" && view !== "1" },
  ]);

  if (view === "compose")
    return <ComposeImport pid={pid} env={env} onBack={() => onView("1")} onClose={onClose} />;

  let lastGroup: string | undefined;
  const title = tpl ? t("템플릿 · {name}", { name: tpl.name }) : picked ? picked.name : volTarget ? t("볼륨 · {name}", { name: volTarget.name }) : TITLES[view];

  return (
    <div
      className="fixed inset-0 z-50 flex animate-fade-in items-start justify-center bg-black/[.45] px-4 pt-[25vh] dark:bg-black/80"
      onMouseDown={(e) => e.target === e.currentTarget && !busy && onClose()}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className="palette-root w-full max-w-[419px] animate-pop-in overflow-hidden rounded-lg border border-gray-200 bg-white text-sm dark:border-white/5 dark:bg-secondaryBg shadow-[0px_13px_29px_0px_rgba(20,17,29,0.15),0px_53px_53px_0px_rgba(20,17,29,0.13),0px_119px_71px_0px_rgba(20,17,29,0.08),0px_211px_84px_0px_rgba(20,17,29,0.02)]"
      >
        <div className="relative flex w-full items-center gap-2 p-1.5 pb-1">
          {view === "1" && !volTarget && !tpl ? (
            <Sparkles className="pointer-events-none absolute left-4 top-1/2 z-10 size-4 -translate-y-1/2 text-gray-400" />
          ) : (
            <IconButton label={t("뒤로")} size="sm" onClick={back} className="shrink-0">
              <ArrowLeft />
            </IconButton>
          )}
          {((view !== "1" && view !== "repo") || tpl || picked) && (
            <span className="shrink-0 rounded bg-gray-100 px-1.5 py-0.5 text-xs font-medium text-gray-600">{title}</span>
          )}
          {tpl ? (
            <span className="min-w-0 flex-1 truncate text-sm text-gray-500">{t("배포할 때 쓸 값을 입력하세요")}</span>
          ) : (
          <input
            ref={input}
            value={q}
            onChange={(e) => {
              setQ(e.target.value);
              setActive(0);
            }}
            onKeyDown={onKey}
            placeholder={volTarget ? t("마운트 경로, 예: /data") : picked ? t("브랜치, 예: {branch}", { branch: picked.branch }) : PLACEHOLDER[view]}
            aria-label={title}
            role="combobox"
            aria-expanded="true"
            aria-controls="create-list"
            aria-activedescendant={entries[idx] ? `create-${entries[idx].id}` : undefined}
            spellCheck={false}
            autoComplete="off"
            className={cx(
              "palette-input w-full min-w-0 flex-1 rounded-md border border-gray-200 bg-secondaryBg p-3 text-sm text-foreground outline-none placeholder:text-gray-400 focus:border-pink-400 focus:bg-pink-50 dark:border-white/20",
              view === "1" && !volTarget && !tpl && "pl-9",
              (view === "image" || volTarget || picked) && "font-mono text-sm",
            )}
          />
          )}
        </div>

        {tpl ? (
          <form
            className="max-h-[min(460px,64vh)] space-y-3 overflow-y-auto px-4 py-3"
            onSubmit={(e) => {
              e.preventDefault();
              deployTpl();
            }}
          >
            {tpl.description && <p className="text-sm text-gray-600">{tpl.description}</p>}
            <TemplateInputsForm
              inputs={tpl.inputs}
              values={tplValues}
              errors={tplErrors}
              idPrefix="palette-in"
              onChange={(k, v) => {
                setTplValues((x) => ({ ...x, [k]: v }));
                setTplErrors((x) => {
                  const n = { ...x };
                  delete n[k];
                  return n;
                });
              }}
            />
            <div className="flex items-center gap-2 pt-1">
              <Link href={href("/templates/", { template: tpl.key })} className="text-xs text-pink-500 hover:underline">
                {t("자세히 보기")}
              </Link>
              <Button type="button" variant="ghost" className="ml-auto" onClick={back} disabled={!!busy}>
                {t("뒤로")}
              </Button>
              <Button type="submit" variant="primary" loading={!!busy}>
                {t("배포")}
              </Button>
            </div>
          </form>
        ) : (
        <div ref={listRef} id="create-list" role="listbox" aria-label={title} className="palette-list max-h-[min(480px,60vh)] overflow-y-auto overflow-x-hidden py-2">
          {view === "repo" && !picked && (
            <div className="mx-2 mb-1 flex items-center gap-x-4 rounded-lg px-3 py-1.5 text-gray-500">
              <Settings className="size-4 shrink-0" />
              <Link href={href("/settings/", { section: "integrations" })} className="min-w-0 flex-1 truncate hover:text-foreground">
                {ghDisconnected ? t("GitHub 계정 연결") : t("GitHub App 설정")}
              </Link>
              <button
                type="button"
                onClick={() => repos.refetch()}
                disabled={repos.isFetching}
                className="flex shrink-0 cursor-pointer items-center gap-1.5 rounded-md border border-gray-200 px-2 py-1 text-xs font-medium dark:border-white/10 text-gray-600 hover:bg-gray-100 hover:text-foreground disabled:cursor-default disabled:opacity-60"
              >
                <RefreshCw className={cx("size-3.5", repos.isFetching && "animate-spin")} /> {t("새로고침")}
              </button>
            </div>
          )}
          {(view === "template" && templates.isPending) || (view === "repo" && !picked && repos.isPending && !q.trim()) ? (
            <div className="flex justify-center py-8">
              <Spinner />
            </div>
          ) : entries.length === 0 ? (
            <div className="px-3 py-6 text-center text-sm text-gray-500">
              {view === "repo"
                ? ghDisconnected
                  ? t("GitHub 계정을 연결하면 저장소 목록이 보입니다. 공개 저장소는 URL이나 user/repo를 붙여넣으면 됩니다.")
                  : q.trim()
                    ? t("‘{query}’와 일치하는 저장소가 없습니다. URL이나 user/repo를 붙여넣어 보세요.", { query: q })
                    : t("접근할 수 있는 저장소가 없습니다.")
                : view === "volume" && !services.length
                  ? t("볼륨을 연결할 서비스가 없습니다.")
                  : t("‘{query}’와 일치하는 항목이 없습니다.", { query: q })}
            </div>
          ) : (
            entries.map((e, i) => {
              const header = e.group && e.group !== lastGroup ? e.group : null;
              lastGroup = e.group;
              return (
                <div key={e.id}>
                  {header && <div className="px-5 pb-1 pt-2.5 text-xs font-medium text-gray-500">{header}</div>}
                  <button
                    type="button"
                    id={`create-${e.id}`}
                    role="option"
                    aria-selected={i === idx}
                    data-idx={i}
                    onMouseMove={() => setActive(i)}
                    onClick={() => choose(e)}
                    title={e.desc}
                    className={cx(
                      "group mx-2 grid w-[calc(100%-1rem)] cursor-pointer grid-cols-[auto_1fr_auto] items-center gap-x-4 rounded-lg p-3 text-left",
                      i === idx ? "bg-gray-100 text-foreground" : "text-gray-500",
                    )}
                  >
                    <span className="flex size-4 shrink-0 items-center justify-center text-lg text-current [&_svg]:size-4">{e.icon}</span>
                    <span className={cx("min-w-0 truncate", view === "image" && "font-mono")}>{e.label}</span>
                    <span className="flex items-center gap-2">
                      {busy === e.id ? (
                        <Spinner />
                      ) : e.view || e.repo || (view === "volume" && !volTarget) ? (
                        <ChevronRight className="size-4 shrink-0 text-current" />
                      ) : i === idx ? (
                        <CornerDownLeft className="size-4 shrink-0 text-gray-400" />
                      ) : null}
                    </span>
                  </button>
                </div>
              );
            })
          )}
          {view === "image" && q.trim() && (
            <div className="px-3 pb-2">
              <RegistryHint pid={pid} image={q} />
            </div>
          )}
          {view === "repo" && repos.error && !ghDisconnected && <ErrorBox className="mx-3 mb-2" error={repos.error} />}
          {view === "repo" && ghDisconnected && gh.data && !gh.data.token_configured && (
            <p className="px-3 pb-2 pt-1 text-xs text-gray-400">
              {t("비공개 저장소는 서버에 KEEL_GITHUB_TOKEN을 설정해야 클론할 수 있습니다.")}
            </p>
          )}
          {view === "template" && (
            <div className="px-3 pb-2 pt-1 text-xs">
              <Link href={href("/templates/")} className="text-pink-500 hover:underline">
                {t("템플릿 갤러리 열기")}
              </Link>
            </div>
          )}
        </div>
        )}
        {error ? <ErrorBox className="mx-3 mb-3" error={error} /> : null}
      </div>
    </div>
  );
}
