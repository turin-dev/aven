"use client";

import { useState, type PointerEvent as RPointerEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Boxes, Check, MoreVertical, Palette, PencilLine, Plus, Ungroup, UserMinus } from "lucide-react";
import type { CanvasData, CanvasGroup, CanvasService } from "@/lib/api";
import { GROUP_COLOR_LABEL, groupHex, layoutApi, type GroupInput } from "@/lib/layouts";
import { qk } from "@/lib/queries";
import { toast } from "@/lib/uiState";
import { t } from "@/lib/i18n";
import { CARD_H, CARD_W, cardSpan } from "../ServiceCard";
import { Button, Dialog, ErrorBox, Field, Input, cx, useConfirm } from "../ui";
import { ContextMenu, useContextTrigger, type MenuEntry } from "../ContextMenu";

/**
 * Group frame geometry, modeled on Railway's dashboard: `border rounded-xl pt-3` with 25px side padding (1px border + one 24px grid cell).
 * Above the cards: pt-3 (12) + 24px header row + one grid cell (24) = 60px.
 */
export const GROUP_PAD = 25;
export const GROUP_HEAD = 35;
const EMPTY_W = CARD_W + GROUP_PAD * 2;
const EMPTY_H = CARD_H + GROUP_HEAD + GROUP_PAD * 2;

const RAILWAY_COLORS = ["gray", "blue", "green", "pink", "yellow"] as const;
const FRAME: Record<string, { hsl: string; scale?: string }> = {
  gray: { hsl: "246,6%,55%", scale: "gray" },
  blue: { hsl: "220,80%,55%", scale: "blue" },
  pink: { hsl: "271,60%,52%", scale: "pink" },
  purple: { hsl: "270,60%,52%", scale: "pink" },
  green: { hsl: "152,38%,42%", scale: "green" },
  yellow: { hsl: "44,74%,52%", scale: "yellow" },
  red: { hsl: "1,70%,55%", scale: "red" },
  teal: { hsl: "190,70%,45%", scale: "cyan" },
};
function frameStyle(color: string): React.CSSProperties {
  const f = FRAME[color];
  if (!f?.scale) {
    const hex = groupHex(color);
    return { "--frame-bg": `${hex}0d`, "--frame-bd": `${hex}4d`, "--frame-bg-hi": `${hex}1f`, "--frame-bd-hi": `${hex}80` } as React.CSSProperties;
  }
  return {
    "--frame-bg": `hsla(${f.hsl},0.05)`,
    "--frame-bd": `var(--${f.scale}-200)`,
    "--frame-bg-hi": `var(--${f.scale}-100)`,
    "--frame-bd-hi": `var(--${f.scale}-300)`,
  } as React.CSSProperties;
}
const swatch = (c: string) => (FRAME[c]?.scale && c !== "red" && c !== "teal" ? `var(--${FRAME[c].scale}-500)` : groupHex(c));
const offeredColors = (current: string) => [...RAILWAY_COLORS, ...((RAILWAY_COLORS as readonly string[]).includes(current) ? [] : [current])];
const colorLabel = (c: string) => GROUP_COLOR_LABEL[c as keyof typeof GROUP_COLOR_LABEL] ?? c;

type Pos = { x: number; y: number };
export type GroupRect = { x: number; y: number; w: number; h: number };

/** Frame of a group: the padded bounding box of its cards, or its stored rect while it has none. */
export function groupRect(g: CanvasGroup, services: CanvasService[], posOf: (s: CanvasService, i: number) => Pos, rectOverride?: GroupRect): GroupRect | null {
  const ids = new Set(g.service_ids);
  const members = services.map((s, i) => ({ s, p: posOf(s, i) })).filter((m) => ids.has(m.s.id));
  if (members.length === 0) {
    const r = rectOverride ?? g.rect;
    return r ? { x: r.x, y: r.y, w: Math.max(r.w, EMPTY_W), h: Math.max(r.h, EMPTY_H) } : null;
  }
  const minX = Math.min(...members.map((m) => m.p.x)) - GROUP_PAD;
  const minY = Math.min(...members.map((m) => m.p.y)) - GROUP_PAD - GROUP_HEAD;
  const maxX = Math.max(...members.map((m) => m.p.x + CARD_W)) + GROUP_PAD;
  const maxY = Math.max(...members.map((m) => m.p.y + cardSpan(m.s))) + GROUP_PAD;
  return { x: minX, y: minY, w: maxX - minX, h: maxY - minY };
}

/** Applies a group change to the canvas cache (a service belongs to one group only). */
export function patchGroupsCache(data: CanvasData | undefined, g: CanvasGroup | { id: string; deleted: true }): CanvasData | undefined {
  if (!data) return data;
  const groups = data.groups ?? [];
  if ("deleted" in g) return { ...data, groups: groups.filter((x) => x.id !== g.id) };
  const claimed = new Set(g.service_ids);
  const others = groups.filter((x) => x.id !== g.id).map((x) => ({ ...x, service_ids: x.service_ids.filter((id) => !claimed.has(id)) }));
  const exists = groups.some((x) => x.id === g.id);
  const next = exists ? groups.map((x) => (x.id === g.id ? g : others.find((o) => o.id === x.id)!)) : [...others, g];
  return { ...data, groups: next };
}
/** Name + color dialog used to create and rename groups. */
export function GroupDialog({
  open,
  title,
  initial,
  submitLabel,
  onClose,
  onSubmit,
}: {
  open: boolean;
  title: string;
  initial: { name: string; color: string };
  submitLabel: string;
  onClose: () => void;
  onSubmit: (v: { name: string; color: string }) => Promise<void>;
}) {
  const [name, setName] = useState(initial.name);
  const [color, setColor] = useState(initial.color);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const valid = name.trim().length > 0 && [...name.trim()].length <= 60;
  const submit = async () => {
    if (!valid || busy) return;
    setBusy(true);
    setError(null);
    try {
      await onSubmit({ name: name.trim(), color });
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
      onClose={() => !busy && onClose()}
      title={title}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            {t("취소")}
          </Button>
          <Button variant="primary" loading={busy} disabled={!valid} onClick={submit}>
            {submitLabel}
          </Button>
        </>
      }
    >
      <form
        className="space-y-4 pt-1"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <Field label={t("이름")}>
          <Input autoFocus value={name} maxLength={60} onChange={(e) => setName(e.target.value)} placeholder={t("예: backend")} />
        </Field>
        <fieldset>
          <legend className="mb-2 text-sm font-medium text-foreground">{t("색")}</legend>
          <div className="flex flex-wrap gap-2" role="radiogroup" aria-label={t("그룹 색")}>
            {offeredColors(initial.color).map((c) => (
              <button
                key={c}
                type="button"
                role="radio"
                aria-checked={color === c}
                aria-label={colorLabel(c)}
                title={colorLabel(c)}
                onClick={() => setColor(c)}
                className={cx(
                  "flex size-7 cursor-pointer items-center justify-center rounded-full border-2 transition-transform",
                  color === c ? "scale-110 border-foreground" : "border-transparent hover:scale-105",
                )}
                style={{ backgroundColor: swatch(c) }}
              >
                {color === c && <Check className="size-3.5 text-white" aria-hidden />}
              </button>
            ))}
          </div>
        </fieldset>
        <ErrorBox error={error} />
      </form>
    </Dialog>
  );
}

export function GroupFrames({
  groups,
  services,
  posOf,
  envId,
  canEdit,
  selection,
  rectOverride,
  onHeaderDown,
  onNudge,
  onSelect,
}: {
  groups: CanvasGroup[];
  services: CanvasService[];
  posOf: (s: CanvasService, i: number) => Pos;
  envId: string;
  canEdit: boolean;
  /** Services currently multi-selected on the canvas (offered as "add to group"). */
  selection: string[];
  /** Live rect of an empty group being dragged. */
  rectOverride: Record<string, GroupRect>;
  onHeaderDown: (e: RPointerEvent<HTMLDivElement>, g: CanvasGroup, r: GroupRect) => void;
  /** Keyboard move by (dx, dy) canvas units. */
  onNudge: (g: CanvasGroup, r: GroupRect, dx: number, dy: number) => void;
  onSelect: (ids: string[]) => void;
}) {
  const qc = useQueryClient();
  const { confirm, node } = useConfirm();
  const [menu, setMenu] = useState<{ x: number; y: number; id: string } | null>(null);
  const [rename, setRename] = useState<CanvasGroup | null>(null);
  const trigger = useContextTrigger((x, y, e) => {
    const id = (e.target as HTMLElement | null)?.closest?.<HTMLElement>("[data-group-frame]")?.dataset.groupFrame;
    if (id) setMenu({ x, y, id });
  });

  const save = async (g: CanvasGroup, input: GroupInput) => {
    const prev = qc.getQueryData<CanvasData>(qk.canvas(envId));
    qc.setQueryData<CanvasData>(qk.canvas(envId), (d) => patchGroupsCache(d, { ...g, ...input } as CanvasGroup));
    try {
      const next = await layoutApi.updateGroup(envId, g.id, input);
      qc.setQueryData<CanvasData>(qk.canvas(envId), (d) => patchGroupsCache(d, next));
    } catch (e) {
      qc.setQueryData(qk.canvas(envId), prev);
      throw e;
    }
  };
  const run = (p: Promise<unknown>) => p.catch((e) => toast(e instanceof Error ? e.message : String(e), "error"));

  const names = new Map(services.map((s) => [s.id, s.name]));
  const items = (g: CanvasGroup): MenuEntry[] => {
    const add = selection.filter((id) => !g.service_ids.includes(id));
    const members = g.service_ids.filter((id) => names.has(id));
    return [
      { kind: "label", label: g.name },
      ...(canEdit
        ? ([
            { label: t("이름 바꾸기"), icon: <PencilLine className="size-3.5" />, onSelect: () => setRename(g) },
            {
              label: t("색"),
              icon: <Palette className="size-3.5" />,
              children: offeredColors(g.color).map((c) => ({
                label: colorLabel(c),
                checked: g.color === c,
                icon: <span className="size-4 rounded-full" style={{ backgroundColor: swatch(c) }} />,
                onSelect: () => run(save(g, { color: c })),
              })),
            },
            { kind: "sep" },
          ] as MenuEntry[])
        : []),
      {
        label: t("서비스 선택"),
        icon: <Boxes className="size-3.5" />,
        disabled: members.length === 0,
        onSelect: () => onSelect(members),
      },
      ...(canEdit
        ? ([
            {
              label: add.length ? t("선택한 서비스 {n}개 추가", { n: add.length }) : t("선택한 서비스 추가"),
              icon: <Plus className="size-3.5" />,
              disabled: add.length === 0,
              reason: t("Shift를 누른 채 카드를 눌러 서비스를 고르세요"),
              onSelect: () => run(save(g, { service_ids: [...g.service_ids, ...add] })),
            },
            {
              label: t("서비스 빼기"),
              icon: <UserMinus className="size-3.5" />,
              disabled: members.length === 0,
              children: members.map((id) => ({
                label: names.get(id) ?? id,
                onSelect: () => {
                  const r = groupRect(g, services, posOf);
                  // 마지막 서비스를 빼면 빈 틀이 지금 자리에 남도록 rect를 같이 저장한다.
                  run(save(g, { service_ids: g.service_ids.filter((x) => x !== id), ...(r && members.length === 1 ? { rect: r } : {}) }));
                },
              })),
            },
            { kind: "sep" },
            {
              label: t("그룹 해제"),
              icon: <Ungroup className="size-3.5" />,
              danger: true,
              onSelect: () =>
                confirm({
                  title: t("그룹 {name} 해제", { name: g.name }),
                  message: t("틀만 지웁니다. 서비스와 위치는 그대로 남습니다."),
                  confirmLabel: t("해제|group"),
                  danger: true,
                  run: async () => {
                    try {
                      await layoutApi.deleteGroup(envId, g.id);
                      qc.setQueryData<CanvasData>(qk.canvas(envId), (d) => patchGroupsCache(d, { id: g.id, deleted: true }));
                    } catch (e) {
                      toast(e instanceof Error ? e.message : String(e), "error");
                    }
                  },
                }),
            },
          ] as MenuEntry[])
        : []),
    ];
  };

  const menuGroup = menu ? groups.find((g) => g.id === menu.id) : undefined;
  return (
    <>
      {node}
      {menu && menuGroup && <ContextMenu key={`${menu.id}${menu.x}${menu.y}`} x={menu.x} y={menu.y} items={items(menuGroup)} onClose={() => setMenu(null)} />}
      {rename && (
        <GroupDialog
          open
          title={t("그룹 이름 바꾸기")}
          submitLabel={t("저장")}
          initial={{ name: rename.name, color: rename.color }}
          onClose={() => setRename(null)}
          onSubmit={(v) => save(rename, v)}
        />
      )}
      {groups.map((g) => {
        const r = groupRect(g, services, posOf, rectOverride[g.id]);
        if (!r) return null;
        const count = g.service_ids.filter((id) => names.has(id)).length;
        const openMenu = (el: HTMLElement) => {
          const b = el.getBoundingClientRect();
          setMenu({ x: b.right - 8, y: b.bottom, id: g.id });
        };
        const active = menu?.id === g.id;
        return (
          <div
            key={g.id}
            className={cx(
              "group/frame pointer-events-none absolute rounded-xl border pt-3 transition-colors",
              "border-(--frame-bd) bg-(--frame-bg) has-[[data-group-frame]:hover]:border-(--frame-bd-hi) has-[[data-group-frame]:hover]:bg-(--frame-bg-hi)",
              active && "!border-(--frame-bd-hi) !bg-(--frame-bg-hi)",
            )}
            style={{ left: r.x, top: r.y, width: r.w, height: r.h, paddingLeft: GROUP_PAD, paddingRight: GROUP_PAD, ...frameStyle(g.color) }}
          >
            <div
              role="group"
              tabIndex={0}
              aria-label={
                canEdit
                  ? t("그룹 {name}, 서비스 {count}개. 끌거나 Alt와 방향키로 옮기고, Enter로 메뉴를 엽니다.", { name: g.name, count })
                  : t("그룹 {name}, 서비스 {count}개. Enter로 메뉴를 엽니다.", { name: g.name, count })
              }
              data-group-frame={g.id}
              {...trigger}
              onPointerDown={(e) => canEdit && onHeaderDown(e, g, r)}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " " || e.key === "ContextMenu" || (e.shiftKey && e.key === "F10")) {
                  e.preventDefault();
                  openMenu(e.currentTarget);
                  return;
                }
                const step = e.shiftKey ? 96 : 24;
                const d = { ArrowLeft: [-step, 0], ArrowRight: [step, 0], ArrowUp: [0, -step], ArrowDown: [0, step] }[e.key];
                if (d && e.altKey && canEdit) {
                  e.preventDefault();
                  onNudge(g, r, d[0], d[1]);
                }
              }}
              className={cx(
                "pointer-events-auto flex h-6 w-full items-center justify-between gap-3 rounded-md outline-none focus-visible:ring-2 focus-visible:ring-pink-500",
                canEdit ? "cursor-move" : "cursor-default",
              )}
            >
              <span className="flex min-w-0 items-center gap-3 font-semibold">
                <Boxes className="size-6 shrink-0" style={{ color: swatch(g.color) }} aria-hidden />
                <span className="truncate">{g.name}</span>
              </span>
              <button
                type="button"
                title={t("서비스 그룹 작업")}
                aria-label={t("그룹 {name} 작업", { name: g.name })}
                tabIndex={-1}
                onPointerDown={(e) => e.stopPropagation()}
                onClick={(e) => openMenu(e.currentTarget)}
                className="-mr-2 flex size-[26px] shrink-0 cursor-pointer items-center justify-center rounded-md text-gray-500 hover:bg-gray-100 hover:text-foreground"
              >
                <MoreVertical className="size-4" />
              </button>
            </div>
          </div>
        );
      })}
    </>
  );
}
