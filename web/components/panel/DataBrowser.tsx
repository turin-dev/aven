"use client";

import { Fragment, useRef, useState, type KeyboardEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { Check, MoreVertical, ChevronLeft, ChevronRight, KeyRound, Play, Plus, RotateCw, Search, Table2, Trash2, X } from "lucide-react";
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
  Segmented,
  Select,
  Textarea,
  Toggle,
  cx,
  errorMessage,
  useConfirm,
} from "../ui";

const PAGE = 50;

const PLACEHOLDER: Record<DatabaseKind, string> = {
  postgres: "SELECT * FROM users ORDER BY id DESC LIMIT 20;",
  mysql: "SELECT * FROM users ORDER BY id DESC LIMIT 20;",
  mongo: 'db.users.find({ active: true }).limit(20).toArray()',
  redis: "GET session:123\nHGETALL user:1",
  bucket: "", // bucket은 Bucket 탭을 쓴다(데이터 탭 없음)
};

const QUERY_HINT: Record<DatabaseKind, string> = {
  postgres: t("SQL을 실행합니다. 여러 문장은 ;로 나누고, 마지막 SELECT 결과만 표로 보입니다."),
  mysql: t("SQL을 실행합니다. 결과가 하나면 표로, 여러 개면 글자로 보입니다."),
  mongo: t("mongosh 식을 실행합니다. 커서는 .toArray()로 끝내면 표로 보입니다."),
  redis: t("redis 명령을 한 줄에 하나씩 실행합니다(redis-cli 문법)."),
  bucket: "",
};

/** One-line example for the query bar above the table tiles. */
function barPlaceholder(kind: DatabaseKind, table: DataTable | null): string {
  if (kind === "redis") return "GET session:123";
  if (kind === "mongo") return table ? `db.${table.name}.find().limit(20).toArray()` : "db.users.find().limit(20).toArray()";
  if (table) return `SELECT * FROM ${tableLabel(table)} LIMIT 50`;
  return "SELECT * FROM table_name";
}

function cellText(v: DataCell): string {
  if (v === null || v === undefined) return "";
  if (typeof v === "string") return v;
  return JSON.stringify(v);
}
function Cell({ v }: { v: DataCell }) {
  if (v === null || v === undefined) return <span className="text-gray-400">NULL</span>;
  const t = cellText(v);
  const short = t.length > 160 ? `${t.slice(0, 160)}…` : t;
  return (
    <span title={t.length > 160 ? t : undefined} className={cx(typeof v !== "string" && "text-pink-600")}>
      {short}
    </span>
  );
}
/** Column header: name, optional SQL type and a primary-key marker. */
function ColumnHead({ name, type, pk }: { name: string; type?: string | null; pk?: boolean }) {
  return (
    <th scope="col" className="whitespace-nowrap border-b border-gray-200 px-3 py-2 text-left align-bottom text-xs font-semibold text-gray-600">
      <span className="flex items-center gap-1">
        {pk && <KeyRound className="size-3 shrink-0 text-pink-600" aria-label={t("기본 키")} />}
        {name}
      </span>
      {type && <span className="block font-mono text-[11px] font-normal text-gray-400">{type}</span>}
    </th>
  );
}
/** Accessible result grid (real table markup, sticky header, horizontal scroll). */
export function DataGrid({
  columns,
  rows,
  caption,
  types,
}: {
  columns: string[];
  rows: DataCell[][];
  caption: string;
  types?: (string | null)[] | null;
}) {
  if (columns.length === 0) return <div className="px-4 py-6 text-center text-sm text-gray-500">{t("행이 없습니다.")}</div>;
  return (
    <div className="max-h-[480px] overflow-auto rounded-lg border border-gray-200" tabIndex={0} role="region" aria-label={caption}>
      <table className="w-full min-w-full border-collapse text-sm tabular-nums">
        <caption className="sr-only">{caption}</caption>
        <thead className="sticky top-0 z-[1] bg-secondaryBg">
          <tr>
            {columns.map((c, i) => (
              <ColumnHead key={`${c}-${i}`} name={c} type={types?.[i]} />
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 ? (
            <tr>
              <td colSpan={columns.length} className="px-3 py-4 text-center text-gray-500">
                {t("행이 없습니다.")}
              </td>
            </tr>
          ) : (
            rows.map((r, i) => (
              <tr key={i} className="odd:bg-gray-50/40 hover:bg-gray-100/60">
                {r.map((v, j) => (
                  <td key={j} className="max-w-[420px] truncate border-b border-gray-200/60 px-3 py-1 align-top text-foreground">
                    <Cell v={v} />
                  </td>
                ))}
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  );
}
function tableLabel(t: DataTable): string {
  return t.schema && t.schema !== "public" ? `${t.schema}.${t.name}` : t.name;
}
function TableList({
  tables,
  current,
  onPick,
}: {
  tables: DataTable[];
  current: DataTable | null;
  onPick: (t: DataTable) => void;
}) {
  if (tables.length === 0) return <div className="px-3 py-4 text-sm text-gray-500">{t("표가 없습니다.")}</div>;
  return (
    <ul className="space-y-0.5" aria-label={t("표 목록")}>
      {tables.map((t) => {
        const active = current?.schema === t.schema && current?.name === t.name;
        return (
          <li key={`${t.schema}.${t.name}`}>
            <button
              type="button"
              aria-current={active ? "true" : undefined}
              onClick={() => onPick(t)}
              className={cx(
                "flex w-full cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm transition-colors",
                active ? "bg-pink-100/50 text-foreground" : "text-gray-600 hover:bg-gray-100 hover:text-foreground",
              )}
            >
              <Table2 className="size-3.5 shrink-0 text-gray-400" aria-hidden />
              <span className="min-w-0 flex-1 truncate font-mono">{tableLabel(t)}</span>
              {t.kind !== "table" && t.kind !== "collection" && <span className="shrink-0 text-xs text-gray-400">{t.kind === "view" ? "view" : t.kind}</span>}
              {t.rows != null && <span className="shrink-0 text-xs tabular-nums text-gray-400">~{t.rows.toLocaleString()}</span>}
            </button>
          </li>
        );
      })}
    </ul>
  );
}
type EditingCell = { row: number; col: number; value: string; isNull: boolean };
/** Grid with inline cell editing and per-row delete (SQL tables with a primary key). */
function EditableGrid({
  data,
  caption,
  onSave,
  onDelete,
}: {
  data: DataRows;
  caption: string;
  onSave: (row: number, column: string, value: string | null) => Promise<void>;
  onDelete: (row: number) => void;
}) {
  const [editing, setEditing] = useState<EditingCell | null>(null);
  const [saving, setSaving] = useState(false);
  const pk = new Set(data.primary_key ?? []);
  const start = (row: number, col: number) => {
    const v = data.rows[row][col];
    setEditing({ row, col, value: v === null || v === undefined ? "" : cellText(v), isNull: v === null || v === undefined });
  };
  const save = async (value: string | null) => {
    if (!editing) return;
    setSaving(true);
    try {
      await onSave(editing.row, data.columns[editing.col], value);
      setEditing(null);
    } catch {
      /* onSave shows the error; keep the editor open */
    } finally {
      setSaving(false);
    }
  };
  if (data.columns.length === 0) return <div className="px-4 py-6 text-center text-sm text-gray-500">{t("열이 없습니다.")}</div>;
  return (
    <div className="max-h-[480px] overflow-auto rounded-lg border border-gray-200" role="region" aria-label={caption}>
      <table className="w-full min-w-full border-collapse text-sm tabular-nums">
        <caption className="sr-only">{t("{caption}. 칸을 클릭하거나 Enter를 눌러 편집합니다. Enter나 포커스 이동으로 저장합니다.", { caption })}</caption>
        <thead className="sticky top-0 z-[1] bg-secondaryBg">
          <tr>
            {data.columns.map((c, i) => (
              <ColumnHead key={`${c}-${i}`} name={c} type={data.column_types?.[i]} pk={pk.has(c)} />
            ))}
            <th scope="col" className="border-b border-gray-200 px-2 py-1.5">
              <span className="sr-only">{t("작업")}</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {data.rows.length === 0 ? (
            <tr>
              <td colSpan={data.columns.length + 1} className="px-3 py-4 text-center text-gray-500">
                {t("행이 없습니다.")}
              </td>
            </tr>
          ) : (
            data.rows.map((r, i) => (
              <tr key={i} className="odd:bg-gray-50/40 hover:bg-gray-100/60">
                {r.map((v, j) =>
                  editing?.row === i && editing.col === j ? (
                    <td key={j} className="border-b border-gray-200/60 px-2 py-1 align-middle">
                      <input
                        autoFocus
                        aria-label={t("{column} 새 값", { column: data.columns[j] })}
                        value={editing.value}
                        placeholder={editing.isNull ? "NULL" : ""}
                        disabled={saving}
                        onChange={(e) => setEditing({ ...editing, value: e.target.value, isNull: false })}
                        onKeyDown={(e) => {
                          if (e.key === "Escape") {
                            e.preventDefault();
                            setEditing(null);
                          } else if (e.key === "Enter") {
                            e.preventDefault();
                            void save(editing.isNull ? null : editing.value);
                          }
                        }}
                        onBlur={() => {
                          const orig = data.rows[i][j];
                          const same = (orig === null || orig === undefined ? "" : cellText(orig)) === editing.value;
                          if (same || saving) setEditing(null);
                          else void save(editing.isNull ? null : editing.value);
                        }}
                        className="h-8 w-full min-w-[120px] rounded-md border border-pink-400 bg-transparent px-2 font-mono text-xs text-foreground focus:outline-none"
                      />
                    </td>
                  ) : (
                    <td
                      key={j}
                      tabIndex={0}
                      aria-label={`${data.columns[j]}: ${v === null || v === undefined ? "NULL" : cellText(v)}`}
                      title={t("클릭하거나 Enter로 편집")}
                      onClick={() => start(i, j)}
                      onKeyDown={(e) => {
                        if (e.key === "Enter" || e.key === "F2") {
                          e.preventDefault();
                          start(i, j);
                        }
                      }}
                      className="max-w-[420px] cursor-text truncate border-b border-gray-200/60 px-3 py-1 align-top text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-500/40"
                    >
                      <Cell v={v} />
                    </td>
                  ),
                )}
                <td className="w-8 border-b border-gray-200/60 px-1 py-0.5 text-right align-middle">
                  <Menu
                    label={t("행 {n} 옵션", { n: i + 1 })}
                    trigger={(p) => (
                      <button type="button" {...p} title={t("행 옵션")} aria-label={t("행 {n} 옵션", { n: i + 1 })} className="flex size-7 cursor-pointer items-center justify-center rounded-md text-gray-600 hover:bg-gray-100">
                        <MoreVertical className="size-4" />
                      </button>
                    )}
                  >
                    {(close) => (
                      <MenuItem
                        danger
                        icon={<Trash2 className="size-3.5" />}
                        onSelect={() => {
                          close();
                          onDelete(i);
                        }}
                      >
                        {t("삭제")}
                      </MenuItem>
                    )}
                  </Menu>
                </td>
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  );
}
/** Insert dialog: one field per column; blank fields are left out (the database default applies). */
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
  const [values, setValues] = useState<Record<string, string>>({});
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
      for (const [k, v] of Object.entries(values)) if (v !== "") set[k] = v;
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
          submit();
        }}
      >
        <p>{t("비워 둔 열은 데이터베이스 기본값을 씁니다.")}</p>
        {data.columns.map((c, i) => (
          <Field key={c} label={c} hint={data.column_types?.[i] ?? undefined}>
            <Input mono value={values[c] ?? ""} onChange={(e) => setValues({ ...values, [c]: e.target.value })} disabled={busy} className="w-full" />
          </Field>
        ))}
        <button type="submit" hidden aria-hidden tabIndex={-1} />
        {error ? <ErrorBox error={error} /> : null}
      </form>
    </Dialog>
  );
}
function RowsView({ svc, env, table }: { svc: CanvasService; env: Environment; table: DataTable }) {
  const [offset, setOffset] = useState(0);
  const [prev, setPrev] = useState(table);
  const [adding, setAdding] = useState(false);
  const { confirm, node: confirmNode } = useConfirm();
  if (prev !== table) {
    setPrev(table);
    setOffset(0);
  }
  const q = useQuery({
    queryKey: ["data-rows", svc.id, env.id, table.schema, table.name, offset],
    queryFn: () => api.dataRows(svc.id, env.id, table, PAGE, offset),
    placeholderData: (p) => p,
  });
  const data = q.data;
  const pkOf = (row: number): DataRowValues => {
    const out: DataRowValues = {};
    for (const k of data?.primary_key ?? []) {
      const v = data!.rows[row][data!.columns.indexOf(k)];
      out[k] = v === null || v === undefined ? null : typeof v === "object" ? JSON.stringify(v) : v;
    }
    return out;
  };
  const onSave = async (row: number, column: string, value: string | null) => {
    try {
      const r = await api.dataRowUpdate(svc.id, env.id, table, pkOf(row), { [column]: value });
      toast(r.affected === 0 ? t("바뀐 값이 없습니다") : t("{column} 값을 바꿨습니다", { column }));
      await q.refetch();
    } catch (e) {
      toast(errorMessage(e), "error");
      throw e;
    }
  };
  const onDelete = (row: number) => {
    const pk = pkOf(row);
    confirm({
      title: t("행을 지울까요?"),
      message: tr("{table}에서 기본 키가 {keys}인 행을 지웁니다. 되돌릴 수 없습니다.", {
        table: <span className="font-mono">{tableLabel(table)}</span>,
        keys: <span className="font-mono">{Object.entries(pk).map(([k, v]) => `${k}=${v}`).join(", ")}</span>,
      }),
      confirmLabel: t("지우기"),
      danger: true,
      run: async () => {
        try {
          await api.dataRowDelete(svc.id, env.id, table, pk);
          toast(t("행을 지웠습니다"));
          await q.refetch();
        } catch (e) {
          toast(errorMessage(e), "error");
        }
      },
    });
  };
  return (
    <div className="min-w-0 space-y-2">
      <div className="flex items-center gap-2">
        <div className="mr-auto" />
        <span className="text-xs tabular-nums text-gray-500" aria-live="polite">
          {q.data ? (q.data.rows.length ? t("{from}–{to}행", { from: (offset + 1).toLocaleString(), to: (offset + q.data.rows.length).toLocaleString() }) : t("행 없음")) : ""}
        </span>
        <IconButton size="sm" label={t("이전 페이지")} disabled={offset === 0 || q.isFetching} onClick={() => setOffset(Math.max(0, offset - PAGE))}>
          <ChevronLeft className="size-4" />
        </IconButton>
        <IconButton size="sm" label={t("다음 페이지")} disabled={!q.data?.has_more || q.isFetching} onClick={() => setOffset(offset + PAGE)}>
          <ChevronRight className="size-4" />
        </IconButton>
        <IconButton size="sm" label={t("새로 고침")} onClick={() => q.refetch()} disabled={q.isFetching}>
          <RotateCw className={cx("size-3.5", q.isFetching && "animate-spin")} />
        </IconButton>
      </div>
      {q.error ? (
        <ErrorBox error={q.error} />
      ) : !data ? (
        <CenterSpinner />
      ) : data.editable ? (
        <EditableGrid data={data} caption={t("{table} 행 {from}부터", { table: tableLabel(table), from: offset + 1 })} onSave={onSave} onDelete={onDelete} />
      ) : (
        <DataGrid columns={data.columns} rows={data.rows} types={data.column_types} caption={t("{table} 행 {from}부터", { table: tableLabel(table), from: offset + 1 })} />
      )}
      {data?.editable && (
        <Button variant="accent" onClick={() => setAdding(true)}>
          <Plus /> {t("행")}
        </Button>
      )}
      {data?.editable === false && data.column_types && (
        <div className="text-xs text-gray-500">{t("기본 키가 없어 이 표의 행은 여기서 편집할 수 없습니다. 쿼리 탭을 쓰세요.")}</div>
      )}
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
function QueryEditor({ svc, env, kind }: { svc: CanvasService; env: Environment; kind: DatabaseKind }) {
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [res, setRes] = useState<DataQueryResult | null>(null);
  const [resp3, setResp3] = useState(false);
  const run = async () => {
    if (!text.trim() || busy) return;
    setBusy(true);
    setError(null);
    try {
      setRes(await api.dataQuery(svc.id, env.id, text, undefined, kind === "redis" && resp3));
    } catch (e) {
      setError(e);
      setRes(null);
    } finally {
      setBusy(false);
    }
  };
  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      run();
    }
  };
  return (
    <div className="space-y-3">
      <div>
        <label htmlFor={`q-${svc.id}`} className="mb-1.5 block text-sm font-semibold text-gray-600">
          {t("쿼리")}
        </label>
        <Textarea
          id={`q-${svc.id}`}
          mono
          rows={6}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={onKey}
          placeholder={PLACEHOLDER[kind]}
          spellCheck={false}
          aria-describedby={`q-hint-${svc.id}`}
        />
        <div id={`q-hint-${svc.id}`} className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-gray-500">
          <span>{QUERY_HINT[kind]}</span>
          <span>
            {tr("{ctrl} + {enter}로 실행 · 최대 1000행, 30초", { ctrl: <Kbd>Ctrl</Kbd>, enter: <Kbd>Enter</Kbd> })}
          </span>
        </div>
      </div>
      <div className="flex items-center gap-2">
        <Button variant="primary" size="sm" onClick={run} loading={busy} disabled={!text.trim()}>
          <Play className="size-3.5" /> {t("실행")}
        </Button>
        {kind === "redis" && (
          <span className="flex items-center gap-1.5 text-xs text-gray-500" title={t("redis-cli -3: map 응답은 key/value 표로 보입니다 (Redis 6+)")}>
            <Toggle checked={resp3} onChange={setResp3} label="RESP3" />
            RESP3
          </span>
        )}
        <span className="text-xs text-gray-400">{t("관리자 권한으로 바로 실행됩니다. 실행 기록이 활동에 남습니다.")}</span>
      </div>
      {error ? <ErrorBox error={error} /> : null}
      {res && (
        <div className="space-y-1.5" aria-live="polite">
          <div className="text-xs tabular-nums text-gray-500">
            {res.kind === "table" ? t("{count}행", { count: res.row_count.toLocaleString() }) : t("결과")} · {res.duration_ms}ms
            {res.truncated && t(" · 결과가 커서 일부만 보여 줍니다")}
          </div>
          {res.kind === "table" ? (
            <DataGrid columns={res.columns} rows={res.rows} caption={t("쿼리 결과")} />
          ) : (
            <pre className="max-h-[480px] overflow-auto whitespace-pre-wrap break-all rounded-md border border-gray-200 bg-gray-50 p-3 font-mono text-xs text-foreground">
              {res.text}
            </pre>
          )}
        </div>
      )}
    </div>
  );
}
function CreateTable({ svc, env, onDone, onCancel }: { svc: CanvasService; env: Environment; onDone: () => void; onCancel: () => void }) {
  const [name, setName] = useState("");
  const [cols, setCols] = useState([{ n: "id", t: "serial", d: "", c: "Primary Key" }]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const upd = (i: number, k: "n" | "t" | "d" | "c", v: string) => setCols((cs) => cs.map((c, j) => (j === i ? { ...c, [k]: v } : c)));
  const lbl = "text-xs font-medium text-gray-600";
  const create = async () => {
    const id = (x: string) => `"${x.replace(/"/g, '""')}"`;
    const body = cols
      .filter((c) => c.n.trim())
      .map((c) => `${id(c.n.trim())} ${c.t.trim() || "text"}${c.c ? ` ${c.c.toUpperCase()}` : ""}${c.d ? ` DEFAULT ${c.d}` : ""}`)
      .join(", ");
    setBusy(true);
    setError(null);
    try {
      await api.dataQuery(svc.id, env.id, `CREATE TABLE ${id(name.trim())} (${body})`);
      onDone();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <form
      name="Create Table"
      className="pt-8"
      onSubmit={(e) => {
        e.preventDefault();
        if (name.trim()) void create();
      }}
    >
      <h3 className="text-[17px] font-semibold leading-6">{t("표 만들기")}</h3>
      <label className="mb-2 mt-6 block text-xs font-semibold leading-[18px] text-gray-600">{t("표 이름")}</label>
      <Input className="max-w-xs" value={name} onChange={(e) => setName(e.target.value)} placeholder="my-table" autoFocus />
      <div className="mt-4 grid grid-cols-[repeat(4,_1fr)_44px] gap-x-2 gap-y-1">
        <p className={lbl}>{t("열 이름")}</p>
        <p className={lbl}>{t("타입")}</p>
        <p className={lbl}>{t("기본값")}</p>
        <p className={lbl}>{t("제약")}</p>
        <p />
        {cols.map((c, i) => (
          <Fragment key={i}>
            <Input value={c.n} onChange={(e) => upd(i, "n", e.target.value)} placeholder="my-column" />
            <Input value={c.t} onChange={(e) => upd(i, "t", e.target.value)} list="pg-types" />
            <Input value={c.d} onChange={(e) => upd(i, "d", e.target.value)} placeholder={t("기본값 없음")} />
            <Input value={c.c} onChange={(e) => upd(i, "c", e.target.value)} />
            <IconButton label={t("열 삭제")} className="self-center" onClick={() => setCols((cs) => cs.filter((_, j) => j !== i))}>
              <X />
            </IconButton>
          </Fragment>
        ))}
      </div>
      <datalist id="pg-types">
        {["text", "integer", "bigint", "real", "boolean", "character", "date", "json", "serial", "uuid", "money"].map((t) => (
          <option key={t} value={t} />
        ))}
      </datalist>
      <Button variant="outline" className="mt-3" onClick={() => setCols((cs) => [...cs, { n: "", t: "text", d: "", c: "" }])}>
        <Plus /> {t("열 추가")}
      </Button>
      {error ? <ErrorBox className="mt-3" error={error} /> : null}
      <div className="mt-6 flex justify-end gap-2">
        <Button type="button" variant="outline" onClick={onCancel}>
          {t("취소")}
        </Button>
        <Button type="submit" variant="primary" loading={busy} disabled={!name.trim()}>
          {t("만들기")}
        </Button>
      </div>
    </form>
  );
}

export function DataBrowser({ svc, env }: { svc: CanvasService; env: Environment }) {
  const kind = svc.database_kind ?? "postgres";
  const [table, setTable] = useState<DataTable | null>(null);
  const [sql, setSql] = useState("");
  const [res, setRes] = useState<DataQueryResult | null>(null);
  const [qerr, setQerr] = useState<unknown>(null);
  const [creating, setCreating] = useState(false);
  const [filter, setFilter] = useState<string | null>(null);
  const filterInput = useRef<HTMLInputElement>(null);
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
  const run = async () => {
    if (!sql.trim()) return setRes(null);
    setQerr(null);
    try {
      setRes(await api.dataQuery(svc.id, env.id, sql));
    } catch (e) {
      setQerr(e);
      setRes(null);
    }
  };
  if (creating)
    return (
      <CreateTable
        svc={svc}
        env={env}
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
        <button type="button" onClick={() => setTable(null)} className="cursor-pointer hover:text-pink-600">
          {noun}
        </button>
        {table && (
          <>
            <span className="font-normal text-gray-400">/</span>
            <span className="truncate font-mono">{tableLabel(table)}</span>
          </>
        )}
      </h3>
      <IconButton size="sm" label={t("검색 (/)")} onClick={() => setFilter((f) => (f === null ? "" : null))}>
        <Search />
      </IconButton>
      <span className="ml-auto flex items-center gap-2">
        {!table && (
          <IconButton label={t("목록 새로 고침")} className="border border-gray-200" onClick={() => tables.refetch()} disabled={tables.isFetching}>
            <RotateCw className={cx(tables.isFetching && "animate-spin")} />
          </IconButton>
        )}
        {!table && kind !== "redis" && kind !== "mongo" && (
          <Button variant="accent" onClick={() => setCreating(true)}>
            <Plus /> {t("새 {noun}", { noun })}
          </Button>
        )}
      </span>
    </div>
  );
  return (
    <section className="pt-8" aria-label={t("데이터")}>
      {head}
      {filter !== null && (
        <Input ref={filterInput} size="sm" autoFocus value={filter} onChange={(e) => setFilter(e.target.value)} onKeyDown={(e) => e.key === "Escape" && (e.stopPropagation(), setFilter(null))} placeholder={t("{noun} 검색", { noun })} aria-label={t("{noun} 검색", { noun })} className="mb-4 !px-3" />
      )}
      <textarea
        rows={1}
        value={sql}
        onChange={(e) => setSql(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && !e.shiftKey) {
            e.preventDefault();
            void run();
          }
        }}
        spellCheck={false}
        placeholder={barPlaceholder(kind, table)}
        aria-label={kind === "redis" ? t("redis 명령") : kind === "mongo" ? t("mongosh 식") : "SQL"}
        aria-describedby={`sql-hint-${svc.id}`}
        title={`${QUERY_HINT[kind]} ${t("Enter로 실행, Shift+Enter로 줄바꿈.")}`}
        className="block max-h-[200px] min-h-[42px] w-full resize-none rounded-md border border-gray-200 bg-transparent px-4 py-3 font-mono text-xs leading-4 text-foreground placeholder-gray-500 [field-sizing:content] hover:border-gray-400 focus:border-pink-400 focus:outline-none"
      />
      <p id={`sql-hint-${svc.id}`} className="mb-4 mt-1.5 text-xs text-gray-500">
        {t("Enter로 실행, Shift+Enter로 줄바꿈 · 관리자 권한으로 바로 실행되고 활동 기록에 남습니다.")}
      </p>
      {qerr ? <ErrorBox error={qerr} className="mb-4" /> : null}
      {res ? (
        <div className="space-y-2" aria-live="polite">
          <div className="flex items-center gap-2">
            <span className="text-sm font-semibold">{t("쿼리 결과")}</span>
            <span className="text-xs tabular-nums text-gray-500">
              {res.kind === "table" ? t("{count}행", { count: res.row_count.toLocaleString() }) : t("글자 결과")} · {res.duration_ms}ms
              {res.truncated && t(" · 결과가 커서 일부만 보여 줍니다")}
            </span>
            <Button size="sm" variant="ghost" className="ml-auto" onClick={() => setRes(null)}>
              <X /> {t("닫기")}
            </Button>
          </div>
          {res.kind === "table" ? (
            <DataGrid columns={res.columns} rows={res.rows} caption={t("쿼리 결과")} />
          ) : (
            <pre className="max-h-[480px] overflow-auto whitespace-pre-wrap break-all rounded-lg border border-gray-200 bg-secondaryBg p-3 font-mono text-xs">{res.text}</pre>
          )}
        </div>
      ) : tables.error ? (
        <ErrorBox error={tables.error} />
      ) : tables.isPending ? (
        <CenterSpinner />
      ) : kind === "redis" ? (
        <RedisBrowser svc={svc} env={env} dbs={tables.data.tables} />
      ) : table ? (
        <RowsView svc={svc} env={env} table={table} />
      ) : tables.data.tables.length === 0 ? (
        <EmptyState
          icon={<Table2 className="size-5" />}
          title={kind === "mongo" ? t("{noun}이 없습니다", { noun }) : t("{noun}가 없습니다", { noun })}
          action={
            <Button variant="primary" onClick={() => setCreating(true)}>
              {t("{noun} 만들기", { noun })}
            </Button>
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
    </section>
  );
}
