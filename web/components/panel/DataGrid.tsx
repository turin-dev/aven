"use client";

// Result grid for the data browser: sticky header and row numbers, resizable columns, page-local sort and search,
// keyboard navigation, a cell inspector for JSON / long text and staged (pending) inline edits.

import { useEffect, useMemo, useRef, useState, type KeyboardEvent, type PointerEvent as ReactPointerEvent, type ReactNode } from "react";
import { ArrowDown, ArrowUp, ArrowUpDown, Braces, KeyRound, Maximize2, Undo2 } from "lucide-react";
import type { DataCell } from "@/lib/api";
import { asJson, cellText, rowMatches, sortRows } from "@/lib/dataSql";
import { t } from "@/lib/i18n";
import { Button, CopyButton, Dialog, Textarea, cx } from "../ui";

export type GridSort = { col: number; desc: boolean } | null;
/** Pending (not yet saved) values of one row by column name; null means SQL NULL. */
export type PendingRow = { set: Record<string, string | null>; error?: string };

const MIN_W = 64;
const MAX_W = 960;
const ROWNUM_W = 56;
const LONG = 120;

const isNull = (v: DataCell | undefined) => v === null || v === undefined;

function defaultWidth(name: string, type: string | null | undefined, rows: DataCell[][], col: number): number {
  let len = Math.max(name.length + 3, (type ?? "").length);
  const sample = rows.slice(0, 30).map((r) => Math.min(48, cellText(r[col]).length));
  if (sample.length) {
    sample.sort((a, b) => a - b);
    len = Math.max(len, sample[Math.floor(sample.length * 0.75)]);
  }
  return Math.max(MIN_W + 16, Math.min(360, Math.round(len * 7.4 + 28)));
}

function CellView({ v, pending }: { v: DataCell; pending?: boolean }) {
  if (isNull(v)) return <span className="italic text-gray-400">NULL</span>;
  const s = cellText(v);
  if (s === "") return <span className="text-gray-400" title={t("빈 문자열")}>{"''"}</span>;
  const short = s.length > 300 ? `${s.slice(0, 300)}…` : s;
  return <span className={cx(!pending && typeof v !== "string" && "text-pink-600")}>{short.replace(/\s*\n\s*/g, " ↵ ")}</span>;
}

type Props = {
  columns: string[];
  rows: DataCell[][];
  caption: string;
  types?: (string | null)[] | null;
  primaryKey?: string[];
  /** Number shown for the first row (offset + 1). */
  firstRow?: number;
  /** Page-local search text (rows that do not match are hidden). */
  search?: string;
  sort?: GridSort;
  onSort?: (s: GridSort) => void;
  /** Enables inline editing. Edits are staged with onStage and saved by the caller. */
  editable?: boolean;
  pending?: (row: number) => PendingRow | undefined;
  onStage?: (row: number, column: string, value: string | null) => void;
  onUnstage?: (row: number, column: string) => void;
  rowActions?: (row: number) => ReactNode;
  className?: string;
};

type Editing = { row: number; col: number; value: string; isNull: boolean };

export function DataGrid({
  columns,
  rows,
  caption,
  types,
  primaryKey,
  firstRow = 1,
  search = "",
  sort: sortProp,
  onSort,
  editable,
  pending,
  onStage,
  onUnstage,
  rowActions,
  className,
}: Props) {
  const [ownSort, setOwnSort] = useState<GridSort>(null);
  const sort = onSort ? (sortProp ?? null) : ownSort;
  const setSort = onSort ?? setOwnSort;
  const [widths, setWidths] = useState<Record<string, number>>({});
  const [active, setActive] = useState<{ r: number; c: number }>({ r: 0, c: 0 });
  const [editing, setEditing] = useState<Editing | null>(null);
  const [inspect, setInspect] = useState<{ row: number; col: number } | null>(null);
  const tableRef = useRef<HTMLTableElement>(null);
  const pk = useMemo(() => new Set(primaryKey ?? []), [primaryKey]);

  const order = useMemo(() => {
    let idx = rows.map((_, i) => i);
    if (search.trim()) idx = idx.filter((i) => rowMatches(rows[i], search));
    if (sort && sort.col < columns.length) {
      const sorted = sortRows(idx.map((i) => rows[i]), sort.col, sort.desc);
      idx = sorted.map((k) => idx[k]);
    }
    return idx;
  }, [rows, search, sort, columns.length]);

  const width = (c: number) => widths[columns[c]] ?? defaultWidth(columns[c], types?.[c], rows, c);
  const total = ROWNUM_W + columns.reduce((n, _, c) => n + width(c), 0);

  // Keep the active cell inside the grid when the page or filter changes.
  const r0 = Math.min(active.r, Math.max(0, order.length - 1));
  const c0 = Math.min(active.c, Math.max(0, columns.length - 1));

  const focusCell = (r: number, c: number) => {
    setActive({ r, c });
    requestAnimationFrame(() => tableRef.current?.querySelector<HTMLElement>(`[data-cell="${r}:${c}"]`)?.focus());
  };

  const valueAt = (row: number, col: number): DataCell => {
    const p = pending?.(row)?.set;
    if (p && columns[col] in p) return p[columns[col]];
    return rows[row][col];
  };

  const startEdit = (row: number, col: number) => {
    if (!editable) return;
    const v = valueAt(row, col);
    const s = isNull(v) ? "" : cellText(v);
    // Long or multi-line values and JSON are easier to edit in the inspector.
    if (s.length > LONG || s.includes("\n") || typeof v === "object") return setInspect({ row, col });
    setEditing({ row, col, value: s, isNull: isNull(v) });
  };

  const stage = (row: number, col: number, value: string | null) => {
    const orig = rows[row][col];
    const same = value === null ? isNull(orig) : !isNull(orig) && cellText(orig) === value;
    if (same) onUnstage?.(row, columns[col]);
    else onStage?.(row, columns[col], value);
  };

  const commitEditor = (move?: "down" | "right") => {
    if (!editing) return;
    stage(editing.row, editing.col, editing.isNull ? null : editing.value);
    setEditing(null);
    const vr = order.indexOf(editing.row);
    if (move === "down") focusCell(Math.min(order.length - 1, vr + 1), editing.col);
    else if (move === "right") focusCell(vr, Math.min(columns.length - 1, editing.col + 1));
    else focusCell(vr, editing.col);
  };

  const onCellKey = (e: KeyboardEvent<HTMLElement>, vr: number, c: number) => {
    const row = order[vr];
    const moves: Record<string, [number, number]> = {
      ArrowUp: [-1, 0],
      ArrowDown: [1, 0],
      ArrowLeft: [0, -1],
      ArrowRight: [0, 1],
    };
    if (moves[e.key]) {
      e.preventDefault();
      const [dr, dc] = moves[e.key];
      focusCell(Math.max(0, Math.min(order.length - 1, vr + dr)), Math.max(0, Math.min(columns.length - 1, c + dc)));
    } else if (e.key === "Home") {
      e.preventDefault();
      focusCell(e.ctrlKey ? 0 : vr, 0);
    } else if (e.key === "End") {
      e.preventDefault();
      focusCell(e.ctrlKey ? order.length - 1 : vr, columns.length - 1);
    } else if (e.key === " " || (e.key === "Enter" && e.shiftKey)) {
      e.preventDefault();
      setInspect({ row, col: c });
    } else if ((e.key === "Enter" || e.key === "F2") && editable) {
      e.preventDefault();
      startEdit(row, c);
    } else if (e.key === "Enter") {
      e.preventDefault();
      setInspect({ row, col: c });
    } else if ((e.key === "Delete" || e.key === "Backspace") && editable && pending?.(row)?.set && columns[c] in pending(row)!.set) {
      // Delete on a pending cell drops that change.
      e.preventDefault();
      onUnstage?.(row, columns[c]);
    } else if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "c" && !window.getSelection()?.toString()) {
      const v = valueAt(row, c);
      void navigator.clipboard?.writeText(isNull(v) ? "" : cellText(v)).catch(() => {});
    }
  };

  const startResize = (e: ReactPointerEvent, c: number) => {
    e.preventDefault();
    e.stopPropagation();
    const startX = e.clientX;
    const startW = width(c);
    const name = columns[c];
    const move = (ev: PointerEvent) => setWidths((w) => ({ ...w, [name]: Math.max(MIN_W, Math.min(MAX_W, startW + ev.clientX - startX)) }));
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      document.body.style.removeProperty("cursor");
    };
    document.body.style.cursor = "col-resize";
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  };
  const resizeKey = (e: KeyboardEvent, c: number) => {
    const step = e.shiftKey ? 64 : 16;
    if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
    e.preventDefault();
    e.stopPropagation();
    const w = width(c) + (e.key === "ArrowRight" ? step : -step);
    setWidths((x) => ({ ...x, [columns[c]]: Math.max(MIN_W, Math.min(MAX_W, w)) }));
  };

  const toggleSort = (c: number) => {
    if (!sort || sort.col !== c) setSort({ col: c, desc: false });
    else if (!sort.desc) setSort({ col: c, desc: true });
    else setSort(null);
  };

  if (columns.length === 0) return <div className="px-4 py-6 text-center text-sm text-gray-500">{t("행이 없습니다.")}</div>;

  const help = editable
    ? t("방향키로 칸을 옮기고, Enter나 F2로 편집하고, Space로 값을 크게 봅니다. 편집한 값은 저장하기 전까지 대기 중으로 남습니다.")
    : t("방향키로 칸을 옮기고, Enter나 Space로 값을 크게 봅니다.");

  return (
    <>
      <div
        className={cx("max-h-[min(560px,70vh)] overflow-auto rounded-lg border border-gray-200 bg-background", className)}
        role="region"
        aria-label={caption}
      >
        <table ref={tableRef} role="grid" aria-rowcount={order.length + 1} aria-colcount={columns.length} className="table-fixed border-separate border-spacing-0 text-sm tabular-nums" style={{ width: total }}>
          <caption className="sr-only">
            {caption}. {help}
          </caption>
          <colgroup>
            <col style={{ width: ROWNUM_W }} />
            {columns.map((c, i) => (
              <col key={`${c}-${i}`} style={{ width: width(i) }} />
            ))}
          </colgroup>
          <thead>
            <tr>
              <th scope="col" className="sticky left-0 top-0 z-[3] border-b border-r border-gray-200 bg-secondaryBg px-2 py-2 text-right text-xs font-normal text-gray-400">
                #
              </th>
              {columns.map((name, i) => {
                const s = sort?.col === i ? (sort.desc ? "descending" : "ascending") : "none";
                return (
                  <th key={`${name}-${i}`} scope="col" aria-sort={s} className="group/th sticky top-0 z-[2] border-b border-r border-gray-200 bg-secondaryBg p-0 text-left align-bottom">
                    <button
                      type="button"
                      onClick={() => toggleSort(i)}
                      title={t("이 페이지 정렬")}
                      className="flex w-full min-w-0 cursor-pointer flex-col items-start px-3 py-1.5 text-left hover:bg-gray-100 focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-pink-500"
                    >
                      <span className="flex w-full min-w-0 items-center gap-1 text-xs font-semibold text-gray-600">
                        {pk.has(name) && <KeyRound className="size-3 shrink-0 text-pink-600" aria-label={t("기본 키")} />}
                        <span className="min-w-0 truncate">{name}</span>
                        {s === "ascending" ? (
                          <ArrowUp className="ml-auto size-3 shrink-0 text-pink-600" aria-hidden />
                        ) : s === "descending" ? (
                          <ArrowDown className="ml-auto size-3 shrink-0 text-pink-600" aria-hidden />
                        ) : (
                          <ArrowUpDown className="ml-auto size-3 shrink-0 text-gray-300 opacity-0 group-hover/th:opacity-100" aria-hidden />
                        )}
                      </span>
                      {types?.[i] && <span className="block w-full truncate font-mono text-[11px] font-normal text-gray-400">{types[i]}</span>}
                    </button>
                    <span
                      role="separator"
                      aria-orientation="vertical"
                      aria-label={t("{column} 열 너비", { column: name })}
                      aria-valuenow={width(i)}
                      aria-valuemin={MIN_W}
                      aria-valuemax={MAX_W}
                      tabIndex={0}
                      onPointerDown={(e) => startResize(e, i)}
                      onKeyDown={(e) => resizeKey(e, i)}
                      onDoubleClick={() => setWidths(({ [name]: _, ...rest }) => rest)}
                      title={t("끌어서 너비 조절 · 두 번 클릭하면 원래대로")}
                      className="absolute -right-1 top-0 z-[4] h-full w-2 cursor-col-resize touch-none after:absolute after:inset-y-1 after:left-1/2 after:w-px after:-translate-x-1/2 after:bg-transparent hover:after:bg-pink-400 focus:outline-none focus-visible:after:bg-pink-500"
                    />
                  </th>
                );
              })}
            </tr>
          </thead>
          <tbody>
            {order.length === 0 ? (
              <tr>
                <td colSpan={columns.length + 1} className="px-3 py-6 text-center text-gray-500">
                  {rows.length === 0 ? t("행이 없습니다.") : t("이 페이지에 맞는 행이 없습니다.")}
                </td>
              </tr>
            ) : (
              order.map((row, vr) => {
                const p = pending?.(row);
                return (
                  <tr key={row} className={cx("group/row", p?.error ? "bg-danger-surface" : vr % 2 === 1 && "bg-gray-50/40")}>
                    <th
                      scope="row"
                      className={cx(
                        "sticky left-0 z-[1] border-b border-r border-gray-200/70 px-1 text-right align-middle text-xs font-normal text-gray-400",
                        p?.error ? "bg-danger-surface" : p ? "bg-warn-surface" : "bg-secondaryBg",
                      )}
                      title={p?.error}
                    >
                      <span className="flex items-center justify-end gap-0.5">
                        {rowActions ? rowActions(row) : null}
                        <span className={cx("min-w-6 tabular-nums", p && "font-semibold text-warn", p?.error && "text-danger")}>{firstRow + row}</span>
                      </span>
                    </th>
                    {columns.map((name, c) => {
                      const changed = !!p && name in p.set;
                      const v = changed ? p!.set[name] : rows[row][c];
                      const isActive = vr === r0 && c === c0;
                      if (editing && editing.row === row && editing.col === c)
                        return (
                          <td key={c} className="border-b border-r border-gray-200/70 p-0 align-middle">
                            <div className="flex items-center">
                              <input
                                autoFocus
                                aria-label={t("{column} 새 값", { column: name })}
                                value={editing.value}
                                placeholder={editing.isNull ? "NULL" : ""}
                                onChange={(e) => setEditing({ ...editing, value: e.target.value, isNull: false })}
                                onKeyDown={(e) => {
                                  if (e.key === "Escape") {
                                    e.preventDefault();
                                    e.stopPropagation();
                                    setEditing(null);
                                    focusCell(vr, c);
                                  } else if (e.key === "Enter") {
                                    e.preventDefault();
                                    commitEditor("down");
                                  } else if (e.key === "Tab" && !e.shiftKey) {
                                    e.preventDefault();
                                    commitEditor("right");
                                  }
                                }}
                                onBlur={() => commitEditor()}
                                className="h-8 w-full min-w-0 border-0 bg-background px-2 font-mono text-xs text-foreground outline-2 -outline-offset-2 outline-pink-500 focus:outline"
                              />
                              <button
                                type="button"
                                // mousedown keeps the input from blurring (and staging the typed text) first
                                onMouseDown={(e) => e.preventDefault()}
                                onClick={() => setEditing({ ...editing, value: "", isNull: true })}
                                className={cx("mr-1 h-6 shrink-0 cursor-pointer rounded px-1.5 font-mono text-[11px]", editing.isNull ? "bg-pink-100 text-pink-600" : "text-gray-500 hover:bg-gray-100")}
                                title={t("NULL로 설정")}
                                aria-pressed={editing.isNull}
                              >
                                NULL
                              </button>
                            </div>
                          </td>
                        );
                      return (
                        <td
                          key={c}
                          data-cell={`${vr}:${c}`}
                          tabIndex={isActive ? 0 : -1}
                          aria-label={`${name}: ${isNull(v) ? "NULL" : cellText(v).slice(0, 200)}${changed ? ` (${t("저장 대기")})` : ""}`}
                          onFocus={() => (vr !== r0 || c !== c0) && setActive({ r: vr, c })}
                          onClick={() => setActive({ r: vr, c })}
                          onDoubleClick={() => (editable ? startEdit(row, c) : setInspect({ row, col: c }))}
                          onKeyDown={(e) => onCellKey(e, vr, c)}
                          className={cx(
                            "group/cell relative truncate border-b border-r border-gray-200/70 px-3 py-1.5 align-top text-foreground focus:outline-none focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-pink-500",
                            changed && "bg-warn-surface",
                            editable ? "cursor-cell" : "cursor-default",
                          )}
                        >
                          <CellView v={v} pending={changed} />
                          {changed && <span className="absolute right-0 top-0 size-0 border-l-[6px] border-t-[6px] border-l-transparent border-t-yellow-500" aria-hidden />}
                          <button
                            type="button"
                            tabIndex={-1}
                            aria-hidden
                            onClick={(e) => {
                              e.stopPropagation();
                              setInspect({ row, col: c });
                            }}
                            className="absolute right-1 top-1 hidden size-5 cursor-pointer items-center justify-center rounded bg-gray-100 text-gray-500 hover:text-foreground group-hover/cell:flex"
                            title={t("값 크게 보기 (Space)")}
                          >
                            <Maximize2 className="size-3" />
                          </button>
                        </td>
                      );
                    })}
                  </tr>
                );
              })
            )}
          </tbody>
        </table>
      </div>
      {inspect && (
        <CellInspector
          key={`${inspect.row}:${inspect.col}`}
          column={columns[inspect.col]}
          type={types?.[inspect.col] ?? null}
          original={rows[inspect.row][inspect.col]}
          value={valueAt(inspect.row, inspect.col)}
          editable={!!editable}
          onClose={() => {
            const vr = order.indexOf(inspect.row);
            setInspect(null);
            if (vr >= 0) focusCell(vr, inspect.col);
          }}
          onStage={(val) => stage(inspect.row, inspect.col, val)}
        />
      )}
    </>
  );
}

/** Full value of one cell: pretty JSON, long text, copy; for editable tables a multi-line editor with NULL. */
function CellInspector({
  column,
  type,
  original,
  value,
  editable,
  onClose,
  onStage,
}: {
  column: string;
  type: string | null;
  original: DataCell;
  value: DataCell;
  editable: boolean;
  onClose: () => void;
  onStage: (v: string | null) => void;
}) {
  const raw = isNull(value) ? "" : cellText(value);
  const json = useMemo(() => (typeof value === "object" && value !== null ? value : asJson(raw)), [value, raw]);
  const [text, setText] = useState(raw);
  const [nul, setNul] = useState(isNull(value));
  const [pretty, setPretty] = useState(json !== undefined);
  const shown = pretty && json !== undefined ? JSON.stringify(json, null, 2) : raw;
  const changed = nul ? !isNull(value) : isNull(value) || text !== raw;
  const editedJson = !nul && text.trim() ? asJson(text) : undefined;
  const looksJson = /^\s*[[{]/.test(text);
  const ta = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    if (editable) ta.current?.focus();
  }, [editable]);
  const apply = () => {
    onStage(nul ? null : text);
    onClose();
  };
  return (
    <Dialog
      open
      onClose={onClose}
      wide
      title={column}
      footer={
        editable ? (
          <>
            {(isNull(original) ? !isNull(value) : isNull(value) || cellText(original) !== raw) && (
              <Button
                variant="ghost"
                onClick={() => {
                  onStage(isNull(original) ? null : cellText(original));
                  onClose();
                }}
              >
                <Undo2 /> {t("원래 값으로")}
              </Button>
            )}
            <Button variant="outline" onClick={onClose}>
              {t("취소")}
            </Button>
            <Button variant="primary" disabled={!changed} onClick={apply}>
              {t("변경 대기에 추가")}
            </Button>
          </>
        ) : (
          <Button variant="outline" onClick={onClose}>
            {t("닫기")}
          </Button>
        )
      }
    >
      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-2 text-xs text-gray-500">
          {type && <span className="rounded bg-gray-100 px-1.5 py-0.5 font-mono">{type}</span>}
          <span className="tabular-nums">{isNull(value) ? "NULL" : t("{count}자", { count: raw.length.toLocaleString() })}</span>
          {json !== undefined && !editable && (
            <button type="button" onClick={() => setPretty((p) => !p)} aria-pressed={pretty} className="flex cursor-pointer items-center gap-1 rounded px-1.5 py-0.5 hover:bg-gray-100 hover:text-foreground">
              <Braces className="size-3" /> {t("JSON 정리")}
            </button>
          )}
          <span className="ml-auto">
            <CopyButton value={editable ? text : shown} label={t("값 복사")} />
          </span>
        </div>
        {editable ? (
          <>
            <Textarea
              ref={ta}
              mono
              rows={12}
              value={nul ? "" : text}
              placeholder={nul ? "NULL" : ""}
              onChange={(e) => {
                setText(e.target.value);
                setNul(false);
              }}
              onKeyDown={(e) => {
                if (e.key === "Enter" && (e.ctrlKey || e.metaKey) && changed) {
                  e.preventDefault();
                  apply();
                }
              }}
              aria-label={t("{column} 새 값", { column })}
              className="max-h-[50vh] min-h-[160px] resize-y"
            />
            <div className="flex flex-wrap items-center gap-2 text-xs">
              <button
                type="button"
                aria-pressed={nul}
                onClick={() => setNul((n) => !n)}
                className={cx("cursor-pointer rounded border px-2 py-1 font-mono", nul ? "border-pink-400 bg-pink-100 text-pink-600" : "border-gray-200 text-gray-600 hover:bg-gray-100")}
              >
                NULL
              </button>
              {json !== undefined && !nul && (
                <button
                  type="button"
                  onClick={() => editedJson !== undefined && setText(JSON.stringify(editedJson, null, 2))}
                  disabled={editedJson === undefined}
                  className="flex cursor-pointer items-center gap-1 rounded border border-gray-200 px-2 py-1 text-gray-600 hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-50"
                >
                  <Braces className="size-3" /> {t("JSON 정리")}
                </button>
              )}
              {looksJson && !nul && editedJson === undefined && <span className="text-warn">{t("JSON으로 읽을 수 없습니다. 글자 그대로 저장됩니다.")}</span>}
              <span className="ml-auto text-gray-500">{t("Ctrl+Enter로 추가 · 저장 버튼을 눌러야 데이터베이스에 씁니다")}</span>
            </div>
          </>
        ) : isNull(value) ? (
          <p className="font-mono italic text-gray-400">NULL</p>
        ) : (
          <pre className="max-h-[60vh] overflow-auto whitespace-pre-wrap break-all rounded-md border border-gray-200 bg-gray-50 p-3 font-mono text-xs text-foreground">{shown}</pre>
        )}
      </div>
    </Dialog>
  );
}
