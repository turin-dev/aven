"use client";

import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronRight, KeyRound, Lock, MoreVertical, Pencil, Plus, Trash2, Undo2, Variable, X } from "lucide-react";
import { api, type CanvasService, type ServerConfigEntry } from "@/lib/api";
import { qk } from "@/lib/queries";
import { toast } from "@/lib/uiState";
import { t, tr } from "@/lib/i18n";
import { Button, ErrorBox, IconButton, Input, Menu, MenuItem, cx, useEscape } from "@/components/ui";
// 변수 탭(ROW_GRID)과 같은 모양이지만 AVEN_GITHUB_WEBHOOK_SECRET 같은 긴 이름이 잘리지 않게 키 열을 넓힌다.
const ROW_GRID = "grid grid-cols-[minmax(15.5rem,auto)_minmax(0,1fr)_2.5rem] gap-3";

// aven-server 자신(self 서비스)의 서버 설정: env 파일의 AVEN_* 값(GET/PUT /api/owner/server-config).
// 변수 탭과 같은 행 모양으로 보여 주고, 고친 값은 모아서 저장한다. 저장은 파일만 바꾸고 다음 aven-server 배포에 적용된다.

type Group = ServerConfigEntry["group"];

const GROUPS: { id: Group; title: string }[] = [
  { id: "general", title: t("일반") },
  { id: "network", title: t("네트워크") },
  { id: "github", title: "GitHub" },
  { id: "smtp", title: t("메일(SMTP)") },
  { id: "advanced", title: t("고급") },
  { id: "other", title: t("기타") },
];

const DESC: Record<string, string> = {
  AVEN_PUBLIC_URL: t("대시보드 주소(https://…). GitHub OAuth·웹훅과 agent 설치 명령이 씁니다."),
  AVEN_BASE_DOMAIN: t("서비스에 자동으로 붙는 도메인의 뿌리입니다."),
  AVEN_ACME_EMAIL: t("TLS 인증서를 발급할 때 쓰는 이메일입니다."),
  AVEN_SUPERADMINS: t("서버 owner 권한을 줄 이메일 목록(쉼표로 구분)."),
  AVEN_ADMIN_PASSWORD: t("기본 계정 admin@local의 비밀번호(8자 이상)."),
  AVEN_API_TOKEN: t("모든 작업 공간에 접근하는 옛 운영 토큰입니다. 쓰지 않으면 비워 두세요."),
  AVEN_DEBUG: t("값이 있으면 디버그 로그를 남깁니다."),
  AVEN_LISTEN: t("대시보드·API가 listen할 주소(host:port). 잘못 바꾸면 새 서버가 뜨지 않아 되돌아갑니다."),
  AVEN_LISTEN_UNIX: t("추가로 listen할 unix socket 경로."),
  AVEN_TRUSTED_PROXIES: t("X-Forwarded-*를 믿을 프록시 주소(쉼표로 구분)."),
  AVEN_INSECURE_COOKIE: t("값이 있으면 세션 쿠키에 Secure를 붙이지 않습니다(http 개발용)."),
  AVEN_PUBLIC_IPV4: t("이 서버의 공개 IPv4. 비우면 자동으로 찾습니다."),
  AVEN_PUBLIC_IPV6: t("이 서버의 공개 IPv6. 비우면 자동으로 찾습니다."),
  AVEN_IPV6_POOL: t("도메인마다 하나씩 배정할 공개 IPv6 대역(CIDR)."),
  AVEN_IPV6_IFACE: t("도메인 IPv6를 붙일 네트워크 인터페이스."),
  AVEN_INGRESS_MODE: t("이 노드의 공개 진입 방식: direct 또는 cloudflare_tunnel."),
  AVEN_INGRESS_HOSTNAME: t("커스텀 도메인이 CNAME으로 가리킬 이 노드의 이름."),
  AVEN_PRIVATE_ADDRESS: t("노드 사이 사설망(mesh)에서 이 서버의 주소."),
  AVEN_MESH: t("노드 간 사설망: on, off, auto."),
  AVEN_EXTERNAL_UPSTREAMS: t("aven-proxy가 보낼 수 있는 Aven 밖의 upstream(host:port, 쉼표로 구분)."),
  AVEN_PROXY_NETWORKS: t("aven-proxy를 더 붙일 docker 네트워크(쉼표로 구분)."),
  AVEN_GITHUB_CLIENT_ID: t("GitHub OAuth App의 Client ID."),
  AVEN_GITHUB_CLIENT_SECRET: t("GitHub OAuth App의 Client secret."),
  AVEN_GITHUB_WEBHOOK_SECRET: t("GitHub 웹훅 서명을 확인하는 비밀값."),
  AVEN_GITHUB_TOKEN: t("GitHub 연결이 없을 때 쓰는 대체 토큰."),
  AVEN_SMTP_HOST: t("메일 서버 주소."),
  AVEN_SMTP_PORT: t("메일 서버 포트."),
  AVEN_SMTP_USERNAME: t("메일 서버 사용자 이름."),
  AVEN_SMTP_PASSWORD: t("메일 서버 비밀번호."),
  AVEN_SMTP_FROM: t("보내는 사람 주소."),
  AVEN_SELF_UPDATE_INTERVAL: t("aven-server 업데이트를 확인하는 주기(예: 30m, 6h). off면 확인하지 않습니다."),
  AVEN_DATA: t("데이터 디렉터리. 바꾸면 서버가 데이터를 잃으므로 여기서 바꿀 수 없습니다."),
  AVEN_AGENT_BIN_DIR: t("컨테이너에서는 이미지 안의 파일을 씁니다."),
  AVEN_RAILPACK_BIN: t("컨테이너에서는 이미지 안의 파일을 씁니다."),
  AVEN_SELF_SERVICE: t("aven-server를 배포하는 서비스를 고정합니다."),
  AVEN_SELF_ENV_FILE: t("이 설정 파일의 경로입니다."),
};

const MASK = "*******";
const KEY_RE = /^[A-Z][A-Z0-9_]{0,127}$/;

/** 저장하지 않은 변경: 문자열은 새 값, null은 파일에서 지우기. */
type Draft = Record<string, string | null>;

export function ServerConfigSettings({ svc, env, canEdit }: { svc: CanvasService; env: { id: string }; canEdit: boolean }) {
  const qc = useQueryClient();
  const query = useQuery({ queryKey: qk.serverConfig, queryFn: api.serverConfig });
  const [draft, setDraft] = useState<Draft>({});
  const [editing, setEditing] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [showFixed, setShowFixed] = useState(false);
  const [busy, setBusy] = useState<"" | "save" | "deploy">("");
  const cfg = query.data;
  const changes = Object.keys(draft).length;
  const readOnly = !canEdit || !cfg?.writable;

  const rows = useMemo(() => {
    const m = new Map<Group, ServerConfigEntry[]>();
    for (const e of cfg?.entries ?? []) {
      if (e.set || e.name in draft || e.pending) m.set(e.group, [...(m.get(e.group) ?? []), e]);
    }
    return m;
  }, [cfg, draft]);
  const unsetKeys = useMemo(() => (cfg?.entries ?? []).filter((e) => !e.set && !e.fixed && !(e.name in draft)), [cfg, draft]);
  const fixed = (cfg?.entries ?? []).filter((e) => e.fixed && e.set);
  const setCount = (cfg?.entries ?? []).filter((e) => e.set && !e.fixed).length;

  const stage = (name: string, v: string | null) => setDraft((d) => ({ ...d, [name]: v }));
  const revert = (name: string) =>
    setDraft((d) => {
      const n = { ...d };
      delete n[name];
      return n;
    });

  const save = async (deploy: boolean) => {
    const set: Record<string, string> = {};
    const unset: string[] = [];
    for (const [k, v] of Object.entries(draft)) {
      if (v === null) unset.push(k);
      else set[k] = v;
    }
    setBusy(deploy ? "deploy" : "save");
    try {
      if (changes) {
        qc.setQueryData(qk.serverConfig, await api.updateServerConfig({ set, unset }));
        setDraft({});
      }
      if (deploy) {
        // 같은 이미지로 다시 배포하면 빌드 없이 새 설정으로 aven-server만 바꾼다.
        const cur = svc.instance.current_deployment_id;
        await (cur ? api.redeploy(cur) : api.deploy(svc.id, env.id));
        toast(t("서버 설정을 저장하고 aven-server를 다시 배포합니다. 잠시 대시보드가 끊깁니다."));
        void qc.invalidateQueries({ queryKey: ["deployments"] });
      } else {
        toast(t("서버 설정을 저장했습니다. aven-server를 다시 배포하면 적용됩니다."));
      }
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e), "error");
    } finally {
      setBusy("");
    }
  };

  if (!cfg) return query.error ? <ErrorBox error={query.error} /> : <div className="h-40 animate-pulse rounded-md bg-gray-100" />;
  return (
    <div data-setting="" data-keywords={cfg.entries.map((e) => e.name).join(" ")} className="min-w-0">
      <header data-setting-head="" className="mb-4 flex items-center justify-between gap-4">
        <p className="text-sm text-gray-600">
          {tr("{count}개 설정 · {file}", {
            count: <b className="font-medium text-foreground">{setCount}</b>,
            file: <span className="font-mono text-xs">{cfg.file}</span>,
          })}
        </p>
        {!readOnly && (
          <Button size="sm" variant="outline" onClick={() => (setAdding(true), setEditing(null))}>
            <Plus className="size-3.5" />
            {t("새 설정")}
          </Button>
        )}
      </header>

      {!cfg.writable && (
        <p className="mb-4 rounded-md border border-yellow-200 bg-yellow-50 px-3 py-2 text-sm text-yellow-800 dark:border-yellow-800 dark:bg-yellow-950/30 dark:text-yellow-200">
          {t("지금 서버는 설정 파일을 읽기 전용으로 열었습니다. aven-server를 한 번 다시 배포하면 여기서 고칠 수 있습니다.")}
        </p>
      )}

      {adding && (
        <NewSetting
          options={unsetKeys.map((e) => e.name)}
          taken={new Set([...cfg.entries.filter((e) => e.set).map((e) => e.name), ...Object.keys(draft)])}
          onAdd={(k, v) => (stage(k, v), setAdding(false))}
          onCancel={() => setAdding(false)}
        />
      )}

      <div className="grid gap-5">
        {GROUPS.map((g) => {
          const list = rows.get(g.id);
          if (!list?.length) return null;
          return (
            <section key={g.id} aria-label={g.title}>
              <h4 className="mb-1 px-4 text-xs font-medium text-gray-500">{g.title}</h4>
              <ul>
                {list.map((e) =>
                  editing === e.name ? (
                    <EditRow
                      key={e.name}
                      e={e}
                      initial={draft[e.name] ?? (e.secret ? "" : (e.value ?? ""))}
                      onCommit={(v) => (stage(e.name, v), setEditing(null))}
                      onCancel={() => setEditing(null)}
                    />
                  ) : (
                    <ConfigRow
                      key={e.name}
                      e={e}
                      draft={draft}
                      readOnly={readOnly}
                      onEdit={() => (setEditing(e.name), setAdding(false))}
                      onRemove={() => stage(e.name, null)}
                      onRevert={() => revert(e.name)}
                    />
                  ),
                )}
              </ul>
            </section>
          );
        })}

        {fixed.length > 0 && (
          <section aria-label={t("고정")}>
            <button type="button" onClick={() => setShowFixed((v) => !v)} className="flex items-center gap-1 px-4 text-xs font-medium text-gray-500 hover:text-foreground">
              <ChevronRight className={cx("size-3.5 transition-transform", showFixed && "rotate-90")} aria-hidden />
              {t("고정 값 {count}개", { count: fixed.length })}
            </button>
            {showFixed && (
              <ul className="mt-1">
                {fixed.map((e) => (
                  <ConfigRow key={e.name} e={e} draft={draft} readOnly onEdit={() => {}} onRemove={() => {}} onRevert={() => {}} />
                ))}
              </ul>
            )}
          </section>
        )}
      </div>

      {(changes > 0 || cfg.pending > 0) && (
        <div className="sticky bottom-4 z-10 mt-6 flex flex-wrap items-center gap-3 rounded-lg border border-pink-200 bg-pink-50 px-4 py-3 text-sm shadow-sm dark:border-pink-900 dark:bg-[#2a1830]">
          <span className="size-2 shrink-0 rounded-full bg-pink-600" aria-hidden />
          <span className="min-w-0 flex-1 text-foreground">
            {changes
              ? t("저장하지 않은 변경 {count}개", { count: changes })
              : t("다음 배포에 적용될 변경 {count}개", { count: cfg.pending })}
          </span>
          {changes > 0 && (
            <>
              <Button size="sm" variant="ghost" onClick={() => setDraft({})} disabled={!!busy}>{t("취소")}</Button>
              <Button size="sm" variant="outline" loading={busy === "save"} disabled={!!busy || readOnly} onClick={() => void save(false)}>{t("저장")}</Button>
            </>
          )}
          <Button size="sm" variant="primary" loading={busy === "deploy"} disabled={!!busy || !canEdit || (changes > 0 && readOnly)} onClick={() => void save(true)}>
            {changes ? t("저장하고 배포") : t("지금 배포해 적용")}
          </Button>
        </div>
      )}
    </div>
  );
}

function ConfigRow({
  e,
  draft,
  readOnly,
  onEdit,
  onRemove,
  onRevert,
}: {
  e: ServerConfigEntry;
  draft: Draft;
  readOnly: boolean;
  onEdit: () => void;
  onRemove: () => void;
  onRevert: () => void;
}) {
  const staged = e.name in draft;
  const removing = draft[e.name] === null;
  const added = staged && !removing && !e.set;
  const value = staged && !removing ? (draft[e.name] as string) : (e.value ?? "");
  const masked = e.secret && !(staged && !removing);
  const locked = readOnly || e.fixed;
  const desc = DESC[e.name];
  const state = removing ? t("삭제 예정") : added ? t("추가 예정") : staged ? t("변경 예정") : e.pending ? t("적용 대기") : null;
  const Icon = e.fixed ? Lock : e.secret ? KeyRound : Variable;
  return (
    <li className="pb-1">
      <div
        className={cx(
          "group rounded px-4 font-mono",
          ROW_GRID,
          "min-h-[2.25rem] items-center",
          staged && !removing ? "bg-pink-50 text-pink-600" : "hover:bg-secondaryBg dark:hover:bg-gray-100",
          removing && "opacity-50",
          !locked && "cursor-pointer",
        )}
        onClick={() => !locked && !removing && onEdit()}
        title={desc ? `${e.name}\n${desc}` : e.name}
      >
        <div className="flex min-w-0 items-center gap-3 text-sm">
          <Icon className="size-4 shrink-0 text-gray-400" aria-hidden />
          <span className={cx("truncate", removing && "line-through")}>{e.name}</span>
          {state && (
            <span
              className={cx("size-1.5 shrink-0 rounded-full", staged ? "bg-pink-600" : "border border-pink-600")}
              role="img"
              aria-label={state}
              title={state === t("적용 대기") ? t("파일 값이 지금 서버와 다릅니다. 다시 배포하면 적용됩니다.") : state}
            />
          )}
        </div>
        <div className="min-w-0 truncate text-sm">
          {removing || (!e.set && !staged) ? (
            <span className="font-sans italic text-gray-400">{t("설정 안 됨")}</span>
          ) : masked ? (
            MASK
          ) : value === "" ? (
            <span className="font-sans italic text-gray-400">{t("<빈 문자열>")}</span>
          ) : (
            value
          )}
        </div>
        <div className="flex items-center justify-end" onClick={(ev) => ev.stopPropagation()}>
          {!locked && (
            <Menu
              label={t("{key} 작업", { key: e.name })}
              trigger={(p) => (
                <IconButton {...p} label={t("설정 작업")} className="p-1 pr-0 text-gray-600">
                  <MoreVertical />
                </IconButton>
              )}
            >
              {(close) => (
                <>
                  {!removing && (
                    <MenuItem icon={<Pencil />} onSelect={() => (close(), onEdit())}>
                      {e.secret ? t("값 교체") : t("편집")}
                    </MenuItem>
                  )}
                  {staged && (
                    <MenuItem icon={<Undo2 />} onSelect={() => (close(), onRevert())}>
                      {added ? t("추가 취소") : removing ? t("삭제 취소") : t("변경 되돌리기")}
                    </MenuItem>
                  )}
                  {e.set && !removing && e.name !== "AVEN_ADMIN_PASSWORD" && (
                    <MenuItem danger icon={<Trash2 />} onSelect={() => (close(), onRemove())}>
                      {t("삭제")}
                    </MenuItem>
                  )}
                </>
              )}
            </Menu>
          )}
        </div>
      </div>
    </li>
  );
}

function EditRow({ e, initial, onCommit, onCancel }: { e: ServerConfigEntry; initial: string; onCommit: (v: string) => void; onCancel: () => void }) {
  const [val, setVal] = useState(initial);
  useEscape(onCancel);
  const desc = DESC[e.name];
  return (
    <li className="mb-1 rounded bg-secondaryBg px-4 py-2 dark:bg-gray-100">
      <form
        className={cx(ROW_GRID, "items-center")}
        onSubmit={(ev) => {
          ev.preventDefault();
          onCommit(val);
        }}
      >
        <span className="truncate font-mono text-sm">{e.name}</span>
        <Input
          autoFocus
          mono
          type={e.secret ? "password" : "text"}
          autoComplete="off"
          value={val}
          onChange={(ev) => setVal(ev.target.value)}
          placeholder={e.secret && e.set ? t("새 값") : ""}
          aria-label={t("{key} 값", { key: e.name })}
          className="h-[34px] !px-1.5"
        />
        <div className="flex items-center justify-end gap-1">
          <IconButton type="submit" label={t("저장 (Enter)")} disabled={e.secret && val === ""}>
            <Check />
          </IconButton>
          <IconButton label={t("취소 (Esc)")} onClick={onCancel}>
            <X />
          </IconButton>
        </div>
      </form>
      {desc && <p className="mt-2 font-sans text-xs text-gray-500">{desc}</p>}
    </li>
  );
}

function NewSetting({ options, taken, onAdd, onCancel }: { options: string[]; taken: Set<string>; onAdd: (k: string, v: string) => void; onCancel: () => void }) {
  const [key, setKey] = useState("");
  const [val, setVal] = useState("");
  useEscape(onCancel);
  const err = !key ? null : !KEY_RE.test(key) ? t("대문자, 숫자, _만 쓸 수 있습니다.") : taken.has(key) ? t("{name}은 이미 있습니다. 목록에서 편집하세요.", { name: key }) : null;
  const desc = DESC[key];
  return (
    <div className="mb-5 rounded-lg border border-gray-200 bg-secondaryBg p-4 dark:bg-gray-100">
      <form
        className={cx(ROW_GRID, "items-start")}
        onSubmit={(ev) => {
          ev.preventDefault();
          if (key && !err) onAdd(key, val);
        }}
      >
        <Input
          autoFocus
          mono
          list="aven-server-config-keys"
          value={key}
          onChange={(ev) => setKey(ev.target.value.toUpperCase())}
          placeholder="AVEN_…"
          aria-label={t("설정 이름")}
          aria-invalid={!!err}
          className="h-[34px] !px-1.5"
        />
        <datalist id="aven-server-config-keys">
          {options.map((o) => (
            <option key={o} value={o}>{DESC[o]}</option>
          ))}
        </datalist>
        <Input mono value={val} onChange={(ev) => setVal(ev.target.value)} placeholder={t("값")} aria-label={t("값")} className="h-[34px] !px-1.5" />
        <div className="flex items-center justify-end gap-1">
          <IconButton type="submit" label={t("추가 (Enter)")} disabled={!key || !!err}>
            <Check />
          </IconButton>
          <IconButton label={t("취소 (Esc)")} onClick={onCancel}>
            <X />
          </IconButton>
        </div>
      </form>
      {(err || desc) && <p className={cx("mt-2 text-xs", err ? "text-red-600" : "text-gray-500")}>{err ?? desc}</p>}
    </div>
  );
}
