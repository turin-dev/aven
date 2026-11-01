"use client";

import { Fragment, useEffect, useRef, useState, type KeyboardEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ChevronLeft,
  ChevronRight,
  ChevronsLeft,
  Copy,
  Download,
  History,
  KeyRound,
  Play,
  Plus,
  RotateCw,
  Save,
  Search,
  Table2,
  Terminal,
  Trash2,
  Undo2,
  X,
} from "lucide-react";
import {
  api,
  type CanvasService,
  type DataCell,
  type DataQueryResult,
  type DataRowValues,
  type DataRows,
  type DataTable,
  type DatabaseKind,
  type Environment,
} from "@/lib/api";
import { buildSelect, cellText, dangerOf, pushHistory, quoteIdent, toCsv, type SqlKind } from "@/lib/dataSql";
import { useCaps } from "@/lib/queries";
import { toast } from "@/lib/uiState";
import { t, tr } from "@/lib/i18n";
import { useKeyboardShortcuts } from "@/components/KeyboardShortcuts";
import {
  Button,
  CenterSpinner,
  Dialog,
  EmptyState,
  ErrorBox,
  Field,
  IconButton,
  Input,
  Kbd,
  Menu,
  MenuItem,
  Select,
  Toggle,
  cx,
  errorMessage,
  useConfirm,
} from "../ui";
import { DataGrid, type GridSort, type PendingRow } from "./DataGrid";

export { DataGrid } from "./DataGrid";

const PAGE_SIZES = [50, 100, 200, 500];

const QUERY_HINT: Record<DatabaseKind, string> = {
  postgres: t("SQL을 실행합니다. 여러 문장은 ;로 나누고, 마지막 SELECT 결과만 표로 보입니다."),
  mysql: t("SQL을 실행합니다. 결과가 하나면 표로, 여러 개면 글자로 보입니다."),
  mongo: t("mongosh 식을 실행합니다. 커서는 .toArray()로 끝내면 표로 보입니다."),
  redis: t("redis 명령을 한 줄에 하나씩 실행합니다(redis-cli 문법)."),
  bucket: "",
};

const sqlKind = (k: DatabaseKind): SqlKind | null => (k === "postgres" ? "postgres" : k === "mysql" ? "mysql" : null);

/** One-line example for the query bar above the table tiles. */
function barPlaceholder(kind: DatabaseKind, table: DataTable | null): string {
  if (kind === "redis") return "GET session:123";
  if (kind === "mongo") return table ? `db.${table.name}.find().limit(20).toArray()` : "db.users.find().limit(20).toArray()";
  const sk = sqlKind(kind);
  if (table && sk) return buildSelect(sk, { schema: table.schema, name: table.name, limit: 50 });
  return "SELECT * FROM table_name LIMIT 50;";
}

function tableLabel(t: DataTable): string {
  return t.schema && t.schema !== "public" ? `${t.schema}.${t.name}` : t.name;
}

/** Explains why a console query needs confirmation (codes from dangerOf). */
function dangerText(reason: string): string {
  switch (reason) {
    case "drop":
      return t("DROP 문은 표나 데이터베이스 객체를 통째로 지웁니다.");
    case "truncate":
      return t("TRUNCATE 문은 표의 모든 행을 지웁니다.");
    case "delete-all":
      return t("WHERE 없는 DELETE 문은 표의 모든 행을 지웁니다.");
    case "update-all":
      return t("WHERE 없는 UPDATE 문은 표의 모든 행을 바꿉니다.");
    case "delete":
      return t("이 쿼리는 데이터를 지웁니다.");
    case "update":
      return t("이 쿼리는 행을 바꿉니다.");
    case "alter":
      return t("ALTER 문은 표 구조를 바꿉니다.");
    case "flush":
      return t("FLUSHALL/FLUSHDB는 데이터베이스의 모든 키를 지웁니다.");
    case "mongo-drop":
      return t("drop()은 컬렉션이나 데이터베이스를 통째로 지웁니다.");
    case "mongo-delete":
      return t("이 식은 문서를 지웁니다.");
  }
  return t("이 쿼리는 데이터를 바꿉니다.");
}

function download(name: string, text: string, type: string) {
  const url = URL.createObjectURL(new Blob([text], { type }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

/* ── Query history (per service, this browser only) ─────────────────── */

const historyKey = (sid: string) => `aven.data.history.${sid}`;
function readHistory(sid: string): string[] {
  try {
    const v = JSON.parse(localStorage.getItem(historyKey(sid)) ?? "[]");
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string").slice(0, 30) : [];
  } catch {
    return [];
  }
}
function writeHistory(sid: string, list: string[]) {
  try {
    if (list.length) localStorage.setItem(historyKey(sid), JSON.stringify(list));
    else localStorage.removeItem(historyKey(sid));
  } catch {
    /* storage full or blocked */
  }
}

/* ── Insert ─────────────────────────────────────────────────────────── */

/** Insert dialog: one field per column; blank fields are left out (the database default applies), NULL is explicit. */
function AddRowDialog({
  open,
  data,
  onClose,
  onInsert,
}: {
  open: boolean;
  data: DataRows;
  onClose: () => void;
  onInsert: (set: DataRowValues) => Promise<void>;
}) {
  const [values, setValues] = useState<Record<string, string | null>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const close = () => {
    if (busy) return;
    setValues({});
    setError(null);
    onClose();
  };
  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      const set: DataRowValues = {};
      for (const [k, v] of Object.entries(values)) if (v === null || v !== "") set[k] = v;
      await onInsert(set);
      setValues({});
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onClose={close}
      title={t("행 추가")}
      wide
      footer={
        <>
          <Button variant="ghost" onClick={close} disabled={busy}>
            {t("취소")}
          </Button>
          <Button variant="primary" loading={busy} onClick={submit}>
            {t("추가")}
          </Button>
        </>
      }
    >
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <p>{t("비워 둔 열은 데이터베이스 기본값을 씁니다.")}</p>
        {data.columns.map((c, i) => {
          const v = values[c];
          return (
            <Field key={c} label={c} hint={data.column_types?.[i] ?? undefined}>
              <span className="flex items-center gap-2">
                <Input
                  mono
                  value={v ?? ""}
                  placeholder={v === null ? "NULL" : t("기본값")}
                  onChange={(e) => setValues({ ...values, [c]: e.target.value })}
                  disabled={busy}
                  className="w-full"
                />
                <button
                  type="button"
                  aria-pressed={v === null}
                  aria-label={t("{column}을 NULL로", { column: c })}
                  onClick={() => setValues({ ...values, [c]: v === null ? "" : null })}
                  className={cx(
                    "h-[42px] shrink-0 cursor-pointer rounded-md border px-2 font-mono text-xs",
                    v === null ? "border-pink-400 bg-pink-100 text-pink-600" : "border-gray-200 text-gray-500 hover:bg-gray-100",
                  )}
                >
                  NULL
                </button>
              </span>
            </Field>
          );
        })}
        <button type="submit" hidden aria-hidden tabIndex={-1} />
        {error ? <ErrorBox error={error} /> : null}
      </form>
    </Dialog>
  );
}

/* ── Table rows with staged edits ───────────────────────────────────── */

type Pending = PendingRow & { pk: DataRowValues };

function RowsView({
  svc,
  env,
  table,
  kind,
  onDirty,
  onOpenSql,
}: {
  svc: CanvasService;
  env: Environment;
  table: DataTable;
  kind: DatabaseKind;
  onDirty: (dirty: boolean) => void;
  onOpenSql: (q: string) => void;
}) {
  const canEdit = useCaps(svc.project_id).canEdit;
  const [offset, setOffset] = useState(0);
  const [limit, setLimit] = useState(PAGE_SIZES[0]);
  const [search, setSearch] = useState("");
  const [sort, setSort] = useState<GridSort>(null);
  const [adding, setAdding] = useState(false);
  const [pending, setPending] = useState<Record<string, Pending>>({});
  const [saving, setSaving] = useState(false);
  const { confirm, node: confirmNode } = useConfirm();
  const changes = Object.values(pending).reduce((n, p) => n + Object.keys(p.set).length, 0);
  const dirty = changes > 0;

  useEffect(() => onDirty(dirty), [dirty, onDirty]);
  useEffect(() => {
    if (!dirty) return;
    const h = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", h);
    return () => window.removeEventListener("beforeunload", h);
  }, [dirty]);

  const q = useQuery({
    queryKey: ["data-rows", svc.id, env.id, table.schema, table.name, limit, offset],
    queryFn: () => api.dataRows(svc.id, env.id, table, limit, offset),
    placeholderData: (p) => p,
    // A background refetch must not reshuffle rows under staged edits.
    refetchOnWindowFocus: !dirty,
  });
  const data = q.data;
  // Rows from the previous page stay visible while the next one loads; they are never editable.
  const editable = !!data?.editable && canEdit && !q.isPlaceholderData;

  const pkOf = (row: number): DataRowValues => {
    const out: DataRowValues = {};
    for (const k of data?.primary_key ?? []) {
      const v = data!.rows[row][data!.columns.indexOf(k)];
      out[k] = v === null || v === undefined ? null : typeof v === "object" ? JSON.stringify(v) : v;
    }
    return out;
  };
  const keyOf = (row: number) => JSON.stringify(pkOf(row));

  /** Runs fn now, or after confirming that staged edits may be thrown away. */
  const guard = (fn: () => void) => {
    if (!dirty) return fn();
    confirm({
      title: t("저장하지 않은 변경을 버릴까요?"),
      message: t("저장하지 않은 변경 {count}건이 사라집니다.", { count: changes }),
      confirmLabel: t("버리고 계속"),
      danger: true,
      run: () => {
        setPending({});
        fn();
      },
    });
  };

  const stage = (row: number, column: string, value: string | null) => {
    const k = keyOf(row);
    const pk = pkOf(row);
    setPending((p) => ({ ...p, [k]: { pk, set: { ...p[k]?.set, [column]: value } } }));
  };
  const unstage = (row: number, column: string) => {
    const k = keyOf(row);
    setPending((p) => {
      if (!p[k]) return p;
      const { [column]: _, ...rest } = p[k].set;
      const next = { ...p };
      if (Object.keys(rest).length) next[k] = { ...p[k], set: rest, error: undefined };
      else delete next[k];
      return next;
    });
  };

  const save = async () => {
    setSaving(true);
    let ok = 0;
    let failed = 0;
    const next: Record<string, Pending> = {};
    for (const [k, p] of Object.entries(pending)) {
      try {
        const r = await api.dataRowUpdate(svc.id, env.id, table, p.pk, p.set);
        if (r.affected === 0) throw new Error(t("행을 찾지 못했습니다. 다른 곳에서 바뀌었거나 지워졌을 수 있습니다."));
        ok++;
      } catch (e) {
        failed++;
        next[k] = { ...p, error: errorMessage(e) };
      }
    }
    setPending(next);
    setSaving(false);
    if (failed) toast(t("{ok}개 행을 저장했고 {failed}개 행은 실패했습니다. 실패한 행에 마우스를 올리면 이유가 보입니다.", { ok, failed }), "error");
    else toast(t("{count}개 행을 저장했습니다", { count: ok }), "success");
    await q.refetch();
  };

  const onDelete = (row: number) => {
    const pk = pkOf(row);
    const k = keyOf(row);
    confirm({
      title: t("행을 지울까요?"),
      message: tr("{table}에서 기본 키가 {keys}인 행을 지웁니다. 되돌릴 수 없습니다.", {
        table: <span className="font-mono">{tableLabel(table)}</span>,
        keys: <span className="break-all font-mono">{Object.entries(pk).map(([c, v]) => `${c}=${v === null ? "NULL" : JSON.stringify(v)}`).join(", ")}</span>,
      }),
      confirmLabel: t("지우기"),
      danger: true,
      run: async () => {
        try {
          const r = await api.dataRowDelete(svc.id, env.id, table, pk);
          toast(r.affected === 0 ? t("지울 행을 찾지 못했습니다") : t("행을 지웠습니다"));
          setPending(({ [k]: _, ...rest }) => rest);
          await q.refetch();
        } catch (e) {
          toast(errorMessage(e), "error");
        }
      },
    });
  };

  const sk = sqlKind(kind);
  const toSql = () => {
    if (!sk || !data) return;
    onOpenSql(
      buildSelect(sk, {
        schema: table.schema,
        name: table.name,
        sort: sort ? { column: data.columns[sort.col], desc: sort.desc } : null,
        filter: search.trim() ? { columns: data.columns, value: search } : null,
        limit,
      }),
    );
  };

  const caption = t("{table} 행 {from}부터", { table: tableLabel(table), from: offset + 1 });
  const shown = data?.rows.length ?? 0;
  return (
    <div className="min-w-0 space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative min-w-[180px] max-w-xs flex-1">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 z-20 size-3.5 -translate-y-1/2 text-gray-400" aria-hidden />
          <Input
            size="sm"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            onKeyDown={(e) => e.key === "Escape" && search && (e.stopPropagation(), setSearch(""))}
            placeholder={t("이 페이지에서 찾기")}
            aria-label={t("이 페이지에서 찾기")}
            className="!pl-8"
          />
        </div>
        {sk && (sort || search.trim()) && (
          <Button size="sm" variant="ghost" onClick={toSql} title={t("정렬과 검색을 표 전체에 적용한 SELECT를 실행합니다")}>
            <Terminal /> {t("표 전체에 적용")}
          </Button>
        )}
        <div className="ml-auto flex items-center gap-1.5">
          <span className="text-xs tabular-nums text-gray-500" aria-live="polite">
            {data
              ? shown
                ? t("{from}–{to}행", { from: (offset + 1).toLocaleString(), to: (offset + shown).toLocaleString() })
                : t("행 없음")
              : ""}
            {table.rows != null && data && ` · ${t("약 {count}행", { count: table.rows.toLocaleString() })}`}
          </span>
          <Select
            size="sm"
            aria-label={t("페이지당 행 수")}
            value={String(limit)}
            onChange={(e) => {
              const n = Number(e.target.value);
              guard(() => {
                setOffset(Math.floor(offset / n) * n);
                setLimit(n);
              });
            }}
            className="w-[84px]"
          >
            {PAGE_SIZES.map((n) => (
              <option key={n} value={String(n)}>
                {t("{n}개", { n })}
              </option>
            ))}
          </Select>
          <IconButton size="sm" label={t("첫 페이지")} disabled={offset === 0 || q.isFetching} onClick={() => guard(() => setOffset(0))}>
            <ChevronsLeft className="size-4" />
          </IconButton>
          <IconButton size="sm" label={t("이전 페이지")} disabled={offset === 0 || q.isFetching} onClick={() => guard(() => setOffset(Math.max(0, offset - limit)))}>
            <ChevronLeft className="size-4" />
          </IconButton>
          <IconButton size="sm" label={t("다음 페이지")} disabled={!data?.has_more || q.isFetching} onClick={() => guard(() => setOffset(offset + limit))}>
            <ChevronRight className="size-4" />
          </IconButton>
          <IconButton size="sm" label={t("새로 고침")} onClick={() => guard(() => void q.refetch())} disabled={q.isFetching}>
            <RotateCw className={cx("size-3.5", q.isFetching && "animate-spin motion-reduce:animate-none")} />
          </IconButton>
        </div>
      </div>
      {dirty && (
        <div role="status" className="sticky top-0 z-[5] flex flex-wrap items-center gap-2 rounded-md border border-yellow-300 bg-warn-surface px-3 py-2 text-sm text-warn">
          <span className="font-medium">{t("저장하지 않은 변경 {count}건", { count: changes })}</span>
          <span className="text-xs">{t("{rows}개 행 · 노란 칸이 바뀐 값입니다", { rows: Object.keys(pending).length })}</span>
          <span className="ml-auto flex gap-2">
            <Button size="sm" variant="ghost" disabled={saving} onClick={() => setPending({})}>
              <Undo2 /> {t("모두 버리기")}
            </Button>
            <Button size="sm" variant="primary" loading={saving} onClick={save}>
              {!saving && <Save />} {t("변경 저장")}
            </Button>
          </span>
        </div>
      )}
      {q.error ? (
        <ErrorBox error={q.error} />
      ) : !data ? (
        <CenterSpinner />
      ) : (
        <div className={cx(q.isPlaceholderData && "opacity-60 transition-opacity")} aria-busy={q.isFetching}>
          <DataGrid
            columns={data.columns}
            rows={data.rows}
            types={data.column_types}
            primaryKey={data.primary_key}
            caption={caption}
            firstRow={offset + 1}
            search={search}
            sort={sort}
            onSort={setSort}
            editable={editable}
            pending={(i) => pending[keyOf(i)]}
            onStage={stage}
            onUnstage={unstage}
            rowActions={
              editable
                ? (i) => (
                    <button
                      type="button"
                      onClick={() => onDelete(i)}
                      aria-label={t("행 {n} 삭제", { n: offset + i + 1 })}
                      title={t("행 삭제")}
                      className="flex size-5 shrink-0 cursor-pointer items-center justify-center rounded text-gray-400 opacity-0 hover:bg-danger-surface hover:text-danger focus:opacity-100 group-hover/row:opacity-100"
                    >
                      <Trash2 className="size-3" />
                    </button>
                  )
                : undefined
            }
          />
        </div>
      )}
      <div className="flex flex-wrap items-center gap-3">
        {editable && (
          <Button variant="accent" onClick={() => setAdding(true)}>
            <Plus /> {t("행")}
          </Button>
        )}
        {data && sort && <span className="text-xs text-gray-500">{t("정렬과 검색은 지금 페이지의 행에만 적용됩니다.")}</span>}
        {data?.truncated && <span className="text-xs text-warn">{t("결과가 커서 일부 행만 받았습니다.")}</span>}
      </div>
      {data?.editable === false && data.column_types && (
        <div className="text-xs text-gray-500">{t("기본 키가 없어 이 표의 행은 여기서 편집할 수 없습니다. 쿼리 탭을 쓰세요.")}</div>
      )}
      {data?.editable && !canEdit && <div className="text-xs text-gray-500">{t("보기 권한으로는 행을 편집할 수 없습니다.")}</div>}
      {data?.editable && (
        <AddRowDialog
          open={adding}
          data={data}
          onClose={() => setAdding(false)}
          onInsert={async (set) => {
            await api.dataRowInsert(svc.id, env.id, table, set);
            toast(t("행을 추가했습니다"));
            await q.refetch();
          }}
        />
      )}
      {confirmNode}
    </div>
  );
}

/* ── Redis ──────────────────────────────────────────────────────────── */

function RedisBrowser({ svc, env, dbs }: { svc: CanvasService; env: Environment; dbs: DataTable[] }) {
  const [db, setDb] = useState(0);
  const [pattern, setPattern] = useState("*");
  const patternInput = useRef<HTMLInputElement>(null);
  const [applied, setApplied] = useState("*");
  const [pages, setPages] = useState<string[]>(["0"]);
  const [key, setKey] = useState<string | null>(null);
  const cursor = pages[pages.length - 1];
  const keys = useQuery({
    queryKey: ["data-keys", svc.id, env.id, db, applied, cursor],
    queryFn: () => api.dataKeys(svc.id, env.id, { pattern: applied, cursor, db }),
  });
  const detail = useQuery({
    queryKey: ["data-key", svc.id, env.id, db, key],
    queryFn: () => api.dataKey(svc.id, env.id, key!, db),
    enabled: key !== null,
  });
  const reset = (nextDb = db, p = applied) => {
    setDb(nextDb);
    setApplied(p);
    setPages(["0"]);
    setKey(null);
  };
  useKeyboardShortcuts([
    { keys: "/", label: t("키 패턴 검색에 포커스"), group: t("데이터베이스"), onTrigger: () => patternInput.current?.focus() },
  ]);
  return (
    <div className="grid gap-4 md:grid-cols-[260px_minmax(0,1fr)]">
      <div className="min-w-0 space-y-2">
        <form
          className="flex gap-1.5"
          role="search"
          onSubmit={(e) => {
            e.preventDefault();
            reset(db, pattern.trim() || "*");
          }}
        >
          <Input ref={patternInput} mono value={pattern} onChange={(e) => setPattern(e.target.value)} aria-label={t("키 패턴")} placeholder="user:*" size="sm" className="min-w-0 flex-1 !px-3" />
          <IconButton size="md" label={t("키 찾기")} type="submit">
            <Search className="size-4" />
          </IconButton>
        </form>
        {dbs.length > 1 && (
          <Select
            aria-label={t("데이터베이스")}
            value={String(db)}
            onChange={(e) => reset(Number(e.target.value))}
            className="w-full"
          >
            {dbs.map((d) => (
              <option key={d.name} value={String(Number(d.name.replace(/^db/, "")))}>
                {d.name} (~{d.rows ?? 0} keys)
              </option>
            ))}
          </Select>
        )}
        {keys.error ? (
          <ErrorBox error={keys.error} />
        ) : keys.isPending ? (
          <CenterSpinner />
        ) : (
          <>
            {keys.data.keys.length === 0 ? (
              <div className="px-2 py-3 text-sm text-gray-500">{t("이 범위에 맞는 키가 없습니다.")}</div>
            ) : (
              <ul className="max-h-[420px] space-y-0.5 overflow-y-auto" aria-label={t("키 목록")}>
                {keys.data.keys.map((k) => (
                  <li key={k.key}>
                    <button
                      type="button"
                      aria-current={key === k.key ? "true" : undefined}
                      onClick={() => setKey(k.key)}
                      className={cx(
                        "flex w-full cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm",
                        key === k.key ? "bg-pink-100/50 text-foreground" : "text-gray-600 hover:bg-gray-100 hover:text-foreground",
                      )}
                    >
                      <KeyRound className="size-3.5 shrink-0 text-gray-400" aria-hidden />
                      <span className="min-w-0 flex-1 truncate font-mono">{k.key}</span>
                      <span className="shrink-0 text-xs text-gray-400">{k.type}</span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
            <div className="flex gap-1.5">
              <Button size="sm" variant="ghost" disabled={pages.length <= 1} onClick={() => setPages((p) => p.slice(0, -1))}>
                <ChevronLeft /> {t("이전")}
              </Button>
              <Button size="sm" variant="ghost" disabled={keys.data.cursor === "0"} onClick={() => setPages((p) => [...p, keys.data.cursor])}>
                {t("다음")} <ChevronRight />
              </Button>
            </div>
          </>
        )}
      </div>
      <div className="min-w-0">
        {key === null ? (
          <EmptyState icon={<KeyRound className="size-5" />} title={t("왼쪽에서 키를 고르세요.")} />
        ) : detail.error ? (
          <ErrorBox error={detail.error} />
        ) : detail.isPending ? (
          <CenterSpinner />
        ) : (
          <div className="space-y-2">
            <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
              <span className="min-w-0 break-all font-mono text-sm font-medium text-foreground">{detail.data.key}</span>
              <span className="text-xs text-gray-500">{detail.data.type}</span>
              {detail.data.length != null && <span className="text-xs tabular-nums text-gray-500">{t("길이 {length}", { length: detail.data.length.toLocaleString() })}</span>}
              <span className="text-xs text-gray-500">{detail.data.ttl < 0 ? t("만료 없음") : t("{ttl}초 뒤 만료", { ttl: detail.data.ttl })}</span>
            </div>
            <RedisValueView type={detail.data.type} value={detail.data.value} />
            {detail.data.truncated && <div className="text-xs text-gray-400">{t("앞부분만 보여 줍니다.")}</div>}
          </div>
        )}
      </div>
    </div>
  );
}
function RedisValueView({ type, value }: { type: string; value: unknown }) {
  if (type === "string" || typeof value === "string") {
    return (
      <pre className="max-h-[420px] overflow-auto whitespace-pre-wrap break-all rounded-md border border-gray-200 bg-gray-50 p-3 font-mono text-xs text-foreground">
        {String(value ?? "")}
      </pre>
    );
  }
  if (Array.isArray(value) && value.every((v) => typeof v === "string")) {
    return <DataGrid columns={["#", "value"]} rows={value.map((v, i) => [String(i), v as string])} caption={t("{type} 값", { type })} />;
  }
  if (Array.isArray(value) && value.length > 0 && typeof value[0] === "object") {
    const cols = Object.keys(value[0] as object);
    return (
      <DataGrid
        columns={cols}
        rows={value.map((v) => cols.map((c) => (v as Record<string, DataCell>)[c] ?? null))}
        caption={t("{type} 값", { type })}
      />
    );
  }
  if (value && typeof value === "object" && !Array.isArray(value)) {
    // RESP3 map → key/value table
    const entries = Object.entries(value as Record<string, unknown>);
    return (
      <DataGrid
        columns={["key", "value"]}
        rows={entries.map(([k, v]) => [k, v === null || v === undefined ? null : typeof v === "string" ? v : JSON.stringify(v)])}
        caption={t("{type} 값", { type })}
      />
    );
  }
  return <pre className="font-mono text-xs text-foreground">{JSON.stringify(value, null, 2)}</pre>;
}

/* ── Create table ───────────────────────────────────────────────────── */

type ColDraft = { n: string; t: string; d: string; c: string };

/** CREATE TABLE text for the form. Names are quoted for the engine; type, default and constraint are SQL as typed. */
function createTableSql(kind: SqlKind, name: string, cols: ColDraft[]): string {
  const body = cols
    .filter((c) => c.n.trim())
    .map((c) => {
      let s = `${quoteIdent(kind, c.n.trim())} ${c.t.trim() || "text"}`;
      if (c.d.trim()) s += ` DEFAULT ${c.d.trim()}`;
      if (c.c.trim()) s += ` ${c.c.trim()}`;
      return s;
    })
    .join(",\n  ");
  return `CREATE TABLE ${quoteIdent(kind, name.trim())} (\n  ${body}\n);`;
}

function CreateTable({ svc, env, kind, onDone, onCancel }: { svc: CanvasService; env: Environment; kind: SqlKind; onDone: () => void; onCancel: () => void }) {
  const [name, setName] = useState("");
  const [cols, setCols] = useState<ColDraft[]>([{ n: "id", t: kind === "mysql" ? "bigint auto_increment" : "serial", d: "", c: "PRIMARY KEY" }]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const upd = (i: number, k: keyof ColDraft, v: string) => setCols((cs) => cs.map((c, j) => (j === i ? { ...c, [k]: v } : c)));
  const lbl = "text-xs font-medium text-gray-600";
  const named = cols.filter((c) => c.n.trim());
  const dupes = new Set(named.map((c) => c.n.trim()).filter((n, i, a) => a.indexOf(n) !== i));
  const valid = !!name.trim() && named.length > 0 && dupes.size === 0;
  const sql = createTableSql(kind, name || "my_table", cols);
  const create = async () => {
    if (!valid) return;
    setBusy(true);
    setError(null);
    try {
      await api.dataQuery(svc.id, env.id, createTableSql(kind, name, cols));
      toast(t("표 {name}을 만들었습니다", { name: name.trim() }), "success");
      onDone();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  const types =
    kind === "mysql"
      ? ["varchar(255)", "text", "int", "bigint", "bigint auto_increment", "decimal(10,2)", "boolean", "date", "datetime", "timestamp", "json"]
      : ["text", "varchar(255)", "integer", "bigint", "serial", "bigserial", "numeric", "real", "boolean", "date", "timestamptz", "jsonb", "uuid"];
  return (
    <form
      name="Create Table"
      className="pt-8"
      onSubmit={(e) => {
        e.preventDefault();
        void create();
      }}
    >
      <h3 className="text-[17px] font-semibold leading-6">{t("표 만들기")}</h3>
      <label htmlFor={`ct-name-${svc.id}`} className="mb-2 mt-6 block text-xs font-semibold leading-[18px] text-gray-600">
        {t("표 이름")}
      </label>
      <Input id={`ct-name-${svc.id}`} className="max-w-xs" value={name} onChange={(e) => setName(e.target.value)} placeholder="my_table" autoFocus mono />
      <div className="mt-4 grid grid-cols-[repeat(4,_minmax(0,1fr))_44px] gap-x-2 gap-y-1">
        <p className={lbl}>{t("열 이름")}</p>
        <p className={lbl}>{t("타입")}</p>
        <p className={lbl} title={t("SQL 식입니다. 문자열은 'text'처럼 작은따옴표로 감싸세요.")}>
          {t("기본값")}
        </p>
        <p className={lbl}>{t("제약")}</p>
        <p />
        {cols.map((c, i) => (
          <Fragment key={i}>
            <Input value={c.n} onChange={(e) => upd(i, "n", e.target.value)} placeholder="my_column" aria-label={t("열 {n} 이름", { n: i + 1 })} mono invalid={dupes.has(c.n.trim())} />
            <Input value={c.t} onChange={(e) => upd(i, "t", e.target.value)} list={`sql-types-${svc.id}`} aria-label={t("열 {n} 타입", { n: i + 1 })} mono />
            <Input value={c.d} onChange={(e) => upd(i, "d", e.target.value)} placeholder={t("기본값 없음")} aria-label={t("열 {n} 기본값", { n: i + 1 })} mono />
            <Input value={c.c} onChange={(e) => upd(i, "c", e.target.value)} placeholder="NOT NULL" aria-label={t("열 {n} 제약", { n: i + 1 })} mono />
            <IconButton label={t("열 삭제")} className="self-center" onClick={() => setCols((cs) => cs.filter((_, j) => j !== i))} disabled={cols.length === 1}>
              <X />
            </IconButton>
          </Fragment>
        ))}
      </div>
      <datalist id={`sql-types-${svc.id}`}>
        {types.map((ty) => (
          <option key={ty} value={ty} />
        ))}
      </datalist>
      <Button variant="outline" className="mt-3" onClick={() => setCols((cs) => [...cs, { n: "", t: "text", d: "", c: "" }])}>
        <Plus /> {t("열 추가")}
      </Button>
      {dupes.size > 0 && <p className="mt-2 text-xs text-danger">{t("열 이름이 겹칩니다: {names}", { names: [...dupes].join(", ") })}</p>}
      <details className="mt-4 text-xs text-gray-500">
        <summary className="cursor-pointer select-none">{t("실행할 SQL 보기")}</summary>
        <pre className="mt-2 overflow-auto rounded-md border border-gray-200 bg-gray-50 p-3 font-mono text-xs text-foreground">{sql}</pre>
      </details>
      {error ? <ErrorBox className="mt-3" error={error} /> : null}
      <div className="mt-6 flex justify-end gap-2">
        <Button type="button" variant="outline" onClick={onCancel} disabled={busy}>
          {t("취소")}
        </Button>
        <Button type="submit" variant="primary" loading={busy} disabled={!valid}>
          {t("만들기")}
        </Button>
      </div>
    </form>
  );
}

/* ── Query result ───────────────────────────────────────────────────── */

function QueryResult({ res, svc, onClose }: { res: DataQueryResult; svc: CanvasService; onClose: () => void }) {
  return (
    <div className="space-y-2" aria-live="polite">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm font-semibold">{t("쿼리 결과")}</span>
        <span className="text-xs tabular-nums text-gray-500">
          {res.kind === "table" ? t("{count}행", { count: res.row_count.toLocaleString() }) : t("글자 결과")} · {res.duration_ms}ms
          {res.truncated && t(" · 결과가 커서 일부만 보여 줍니다")}
        </span>
        <span className="ml-auto flex items-center gap-1">
          {res.kind === "table" && res.columns.length > 0 && (
            <>
              <Button size="sm" variant="ghost" onClick={() => download(`${svc.name}-query.csv`, toCsv(res.columns, res.rows), "text/csv;charset=utf-8")}>
                <Download /> CSV
              </Button>
              <Button
                size="sm"
                variant="ghost"
                onClick={() =>
                  void navigator.clipboard
                    ?.writeText(JSON.stringify(res.rows.map((r) => Object.fromEntries(res.columns.map((c, i) => [c, r[i] ?? null]))), null, 2))
                    .then(() => toast(t("JSON으로 복사했습니다")))
                    .catch(() => {})
                }
              >
                <Copy /> JSON
              </Button>
            </>
          )}
          <Button size="sm" variant="ghost" onClick={onClose}>
            <X /> {t("닫기")}
          </Button>
        </span>
      </div>
      {res.kind === "table" ? (
        <DataGrid columns={res.columns} rows={res.rows} caption={t("쿼리 결과")} />
      ) : (
        <pre className="max-h-[480px] overflow-auto whitespace-pre-wrap break-all rounded-lg border border-gray-200 bg-secondaryBg p-3 font-mono text-xs">{res.text || t("(출력 없음)")}</pre>
      )}
    </div>
  );
}

/* ── Browser ────────────────────────────────────────────────────────── */

export function DataBrowser({ svc, env }: { svc: CanvasService; env: Environment }) {
  const qc = useQueryClient();
  const kind = svc.database_kind ?? "postgres";
  const sk = sqlKind(kind);
  const [table, setTable] = useState<DataTable | null>(null);
  const [sql, setSql] = useState("");
  const [res, setRes] = useState<DataQueryResult | null>(null);
  const [qerr, setQerr] = useState<unknown>(null);
  const [running, setRunning] = useState(false);
  const [resp3, setResp3] = useState(false);
  const [creating, setCreating] = useState(false);
  const [filter, setFilter] = useState<string | null>(null);
  const [dirty, setDirty] = useState(false);
  const [history, setHistory] = useState<string[]>([]);
  const [histPos, setHistPos] = useState(-1);
  const filterInput = useRef<HTMLInputElement>(null);
  const bar = useRef<HTMLTextAreaElement>(null);
  const { confirm, node: confirmNode } = useConfirm();
  useEffect(() => setHistory(readHistory(svc.id)), [svc.id]);

  const tables = useQuery({
    queryKey: ["data-tables", svc.id, env.id],
    queryFn: () => api.dataTables(svc.id, env.id),
    staleTime: 10_000,
    retry: false,
  });
  const noun = kind === "redis" ? t("키") : kind === "mongo" ? t("컬렉션") : t("표");
  const shownTables = (tables.data?.tables ?? []).filter((t) => !filter || tableLabel(t).toLowerCase().includes(filter.toLowerCase()));
  useKeyboardShortcuts([
    {
      keys: "/",
      label: t("표 또는 컬렉션 검색에 포커스"),
      group: t("데이터베이스"),
      enabled: kind !== "redis" && !table && !res && !creating,
      onTrigger: () => {
        if (filter === null) setFilter("");
        else filterInput.current?.focus();
      },
    },
  ]);

  /** Leaves the current table, asking first when it has unsaved edits. */
  const leave = (fn: () => void) => {
    if (!dirty) return fn();
    confirm({
      title: t("저장하지 않은 변경을 버릴까요?"),
      message: t("이 표에서 편집한 값이 아직 저장되지 않았습니다. 나가면 사라집니다."),
      confirmLabel: t("버리고 나가기"),
      danger: true,
      run: () => {
        setDirty(false);
        fn();
      },
    });
  };

  const exec = async (text: string) => {
    setQerr(null);
    setRunning(true);
    const next = pushHistory(history, text);
    setHistory(next);
    writeHistory(svc.id, next);
    setHistPos(-1);
    try {
      setRes(await api.dataQuery(svc.id, env.id, text, undefined, kind === "redis" && resp3));
    } catch (e) {
      setQerr(e);
      setRes(null);
    } finally {
      setRunning(false);
      // The query may have changed rows or tables.
      void qc.invalidateQueries({ queryKey: ["data-tables", svc.id, env.id] });
      if (!dirty) void qc.invalidateQueries({ queryKey: ["data-rows", svc.id, env.id] });
      if (kind === "redis") void qc.invalidateQueries({ queryKey: ["data-keys", svc.id, env.id] });
    }
  };
  const run = (text = sql) => {
    if (running) return;
    if (!text.trim()) return setRes(null);
    const d = dangerOf(kind, text);
    if (!d) return void exec(text);
    confirm({
      title: d.level === "severe" ? t("데이터를 크게 바꾸는 쿼리입니다") : t("데이터를 바꾸는 쿼리입니다"),
      message: (
        <div className="space-y-3">
          <p>{dangerText(d.reason)} {t("관리자 권한으로 바로 실행되고 되돌릴 수 없습니다.")}</p>
          <pre className="max-h-40 overflow-auto whitespace-pre-wrap break-all rounded-md border border-gray-200 bg-gray-50 p-2 font-mono text-xs text-foreground">{text.trim()}</pre>
        </div>
      ),
      confirmLabel: t("실행"),
      danger: true,
      run: () => exec(text),
    });
  };
  const openSql = (text: string) => {
    setSql(text);
    run(text);
    bar.current?.focus();
  };

  if (creating && sk)
    return (
      <CreateTable
        svc={svc}
        env={env}
        kind={sk}
        onCancel={() => setCreating(false)}
        onDone={() => {
          setCreating(false);
          void tables.refetch();
        }}
      />
    );
  const head = (
    <div className="mb-4 flex items-center gap-3">
      <h3 className="flex min-w-0 items-center gap-2 text-[17px] font-semibold">
        <button type="button" onClick={() => leave(() => setTable(null))} className="cursor-pointer hover:text-pink-600" aria-label={table ? t("{noun} 목록으로", { noun }) : undefined}>
          {noun}
        </button>
        {table && (
          <>
            <span className="font-normal text-gray-400" aria-hidden>
              /
            </span>
            <span className="truncate font-mono">{tableLabel(table)}</span>
          </>
        )}
      </h3>
      {!table && kind !== "redis" && (
        <IconButton size="sm" label={t("검색 (/)")} onClick={() => setFilter((f) => (f === null ? "" : null))} aria-pressed={filter !== null}>
          <Search />
        </IconButton>
      )}
      <span className="ml-auto flex items-center gap-2">
        {!table && (
          <IconButton label={t("목록 새로 고침")} className="border border-gray-200" onClick={() => tables.refetch()} disabled={tables.isFetching}>
            <RotateCw className={cx(tables.isFetching && "animate-spin motion-reduce:animate-none")} />
          </IconButton>
        )}
        {!table && sk && (
          <Button variant="accent" onClick={() => setCreating(true)}>
            <Plus /> {t("새 {noun}", { noun })}
          </Button>
        )}
      </span>
    </div>
  );
  const onBarKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    const el = e.currentTarget;
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      run();
    } else if (e.key === "ArrowUp" && history.length && !el.value.slice(0, el.selectionStart).includes("\n")) {
      // Up on the first line walks back through the history, like a shell.
      const pos = Math.min(history.length - 1, histPos + 1);
      if (pos === histPos) return;
      e.preventDefault();
      setHistPos(pos);
      setSql(history[pos]);
    } else if (e.key === "ArrowDown" && histPos >= 0 && !el.value.slice(el.selectionEnd).includes("\n")) {
      e.preventDefault();
      const pos = histPos - 1;
      setHistPos(pos);
      setSql(pos >= 0 ? history[pos] : "");
    } else if (e.key === "Escape" && res) {
      e.stopPropagation();
      setRes(null);
    }
  };
  return (
    <section className="pt-8" aria-label={t("데이터")}>
      {head}
      {filter !== null && !table && (
        <Input ref={filterInput} size="sm" autoFocus value={filter} onChange={(e) => setFilter(e.target.value)} onKeyDown={(e) => e.key === "Escape" && (e.stopPropagation(), setFilter(null))} placeholder={t("{noun} 검색", { noun })} aria-label={t("{noun} 검색", { noun })} className="mb-4 !px-3" />
      )}
      <div className="flex items-start gap-1.5">
        <textarea
          ref={bar}
          rows={1}
          value={sql}
          onChange={(e) => {
            setSql(e.target.value);
            setHistPos(-1);
          }}
          onKeyDown={onBarKey}
          spellCheck={false}
          placeholder={barPlaceholder(kind, table)}
          aria-label={kind === "redis" ? t("redis 명령") : kind === "mongo" ? t("mongosh 식") : "SQL"}
          aria-describedby={`sql-hint-${svc.id}`}
          title={`${QUERY_HINT[kind]} ${t("Enter로 실행, Shift+Enter로 줄바꿈.")}`}
          className="block max-h-[240px] min-h-[42px] w-full min-w-0 flex-1 resize-y rounded-md border border-gray-200 bg-transparent px-4 py-3 font-mono text-xs leading-4 text-foreground placeholder-gray-500 [field-sizing:content] hover:border-gray-400 focus:border-pink-400 focus:outline-none"
        />
        <Button variant="primary" className="h-[42px] shrink-0" onClick={() => run()} loading={running} disabled={!sql.trim()} aria-label={t("실행")}>
          {!running && <Play />}
        </Button>
        <Menu
          label={t("쿼리 기록")}
          trigger={(p) => (
            <IconButton {...p} label={t("쿼리 기록")} className="h-[42px] w-[42px] shrink-0 border border-gray-200" disabled={!history.length}>
              <History />
            </IconButton>
          )}
        >
          {(close) => (
            <>
              {history.map((h, i) => (
                <MenuItem
                  key={i}
                  onSelect={() => {
                    close();
                    setSql(h);
                    setHistPos(i);
                    bar.current?.focus();
                  }}
                >
                  <span className="block max-w-[420px] truncate font-mono text-xs" title={h}>
                    {h.replace(/\s+/g, " ")}
                  </span>
                </MenuItem>
              ))}
              <MenuItem
                danger
                icon={<Trash2 className="size-3.5" />}
                onSelect={() => {
                  close();
                  setHistory([]);
                  writeHistory(svc.id, []);
                }}
              >
                {t("기록 지우기")}
              </MenuItem>
            </>
          )}
        </Menu>
      </div>
      <p id={`sql-hint-${svc.id}`} className="mb-4 mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-gray-500">
        <span>
          {tr("{enter}로 실행, {shift}+{enter}로 줄바꿈, {up}으로 이전 쿼리 · 관리자 권한으로 바로 실행되고 활동 기록에 남습니다.", {
            enter: <Kbd>Enter</Kbd>,
            shift: <Kbd>Shift</Kbd>,
            up: <Kbd>↑</Kbd>,
          })}
        </span>
        {kind === "redis" && (
          <span className="flex items-center gap-1.5" title={t("redis-cli -3: map 응답은 key/value 표로 보입니다 (Redis 6+)")}>
            <Toggle checked={resp3} onChange={setResp3} label="RESP3" />
            RESP3
          </span>
        )}
      </p>
      {qerr ? <ErrorBox error={qerr} className="mb-4" /> : null}
      {res ? (
        <QueryResult res={res} svc={svc} onClose={() => setRes(null)} />
      ) : tables.error ? (
        <ErrorBox error={tables.error} />
      ) : tables.isPending ? (
        <CenterSpinner />
      ) : kind === "redis" ? (
        <RedisBrowser svc={svc} env={env} dbs={tables.data.tables} />
      ) : table ? (
        <RowsView key={`${table.schema}.${table.name}`} svc={svc} env={env} table={table} kind={kind} onDirty={setDirty} onOpenSql={openSql} />
      ) : tables.data.tables.length === 0 ? (
        <EmptyState
          icon={<Table2 className="size-5" />}
          title={kind === "mongo" ? t("{noun}이 없습니다", { noun }) : t("{noun}가 없습니다", { noun })}
          action={
            sk ? (
              <Button variant="primary" onClick={() => setCreating(true)}>
                {t("{noun} 만들기", { noun })}
              </Button>
            ) : undefined
          }
        />
      ) : shownTables.length === 0 ? (
        <EmptyState icon={<Search className="size-5" />} title={kind === "mongo" ? t("\"{filter}\"와 맞는 {noun}이 없습니다", { filter: filter ?? "", noun }) : t("\"{filter}\"와 맞는 {noun}가 없습니다", { filter: filter ?? "", noun })} />
      ) : (
        <ul className="grid grid-cols-[repeat(auto-fill,minmax(144px,1fr))] gap-4" aria-label={t("{noun} 목록", { noun })}>
          {shownTables.map((tbl) => (
            <li key={`${tbl.schema}.${tbl.name}`}>
              <button
                type="button"
                onClick={() => setTable(tbl)}
                title={tableLabel(tbl)}
                className="flex h-[100px] w-full cursor-pointer flex-col items-center justify-center gap-2 rounded-lg border border-gray-200 bg-secondaryBg px-3 text-center text-sm transition-colors hover:border-gray-400 hover:bg-gray-100 focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-500"
              >
                <Table2 className={cx("size-6", tbl.kind === "view" ? "text-gray-500" : "text-blue-500")} aria-hidden />
                <span className="max-w-full truncate font-medium">{tableLabel(tbl)}</span>
                <span className="text-xs tabular-nums text-gray-500">
                  {tbl.kind === "view" ? "view" : tbl.rows != null ? t("~{count}행", { count: tbl.rows.toLocaleString() }) : "\u00a0"}
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
      {confirmNode}
    </section>
  );
}
