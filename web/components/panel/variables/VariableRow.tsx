"use client";

import { useState } from "react";
import {
  AlertTriangle,
  Check,
  Copy,
  CornerDownRight,
  Eye,
  EyeOff,
  Link2,
  Lock,
  MoreVertical,
  Network,
  Pencil,
  Trash2,
  Undo2,
  Variable,
  X,
} from "lucide-react";
import type { ResolvedVar } from "@/lib/api";
import { hasRef, tokenize } from "@/lib/refs";
import { toast } from "@/lib/uiState";
import { t } from "@/lib/i18n";
import { IconButton, Input, Menu, MenuItem, cx, useEscape } from "../../ui";
import { VarInput, type Suggestion } from "../VarInput";

export const KEY_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;
export const KEY_RULE = t("이름은 영문, 숫자, 밑줄(_)만 쓸 수 있고 숫자로 시작할 수 없습니다.");

export const ROW_GRID = "grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)_minmax(4rem,auto)] gap-2";

const MASK = "*******";

/** Value with ${{ refs }} highlighted. */
export function RefValue({ value }: { value: string }) {
  return (
    <>
      {tokenize(value).map((seg, i) =>
        seg.ref ? (
          <span key={i} className="rounded-[3px] bg-pink-100/60 px-0.5 text-pink-600">
            {seg.text}
          </span>
        ) : (
          <span key={i}>{seg.text}</span>
        ),
      )}
    </>
  );
}

export async function copyText(text: string, what: string) {
  try {
    await navigator.clipboard.writeText(text);
    toast(t("{what}를 복사했습니다", { what }));
  } catch {
    toast(t("클립보드에 복사하지 못했습니다"), "error");
  }
}
export type Row = { key: string; value: string; server: string | undefined; deleted: boolean; sealed: boolean };
function KindIcon({ r }: { r: Row }) {
  const shared = /^\s*\$\{\{\s*shared\./.test(r.value);
  const ref = hasRef(r.value);
  const [Icon, tip] = r.sealed
    ? [Lock, t("봉인된 변수입니다. 봉인된 값은 볼 수 없고 배포할 때만 주입됩니다.")]
    : shared
      ? [CornerDownRight, t("공유 변수를 참조합니다")]
      : ref
        ? [Link2, t("다른 변수를 참조합니다")]
        : [Variable, t("서비스 변수")];
  return (
    <span title={tip} className="flex shrink-0 text-gray-400">
      <Icon className="size-4" aria-hidden />
      <span className="sr-only">{tip}</span>
    </span>
  );
}
/** One variable (display mode). */
export function VariableRow({
  r,
  resolved,
  resolvedPending,
  savedNotDeployed,
  shown,
  onToggleReveal,
  onEdit,
  onSeal,
  onRevert,
  onDelete,
  onRestore,
  canEdit,
  why,
}: {
  r: Row;
  resolved: ResolvedVar | undefined;
  resolvedPending: boolean;
  savedNotDeployed: boolean;
  shown: boolean;
  onToggleReveal: () => void;
  onEdit: () => void;
  onSeal: () => void;
  onRevert: () => void;
  onDelete: () => void;
  onRestore: () => void;
  canEdit: boolean;
  why?: string;
}) {
  const added = r.server === undefined;
  const changed = !r.deleted && !added && r.server !== r.value;
  const staged = added || changed;
  const isRef = hasRef(r.value);
  // 봉인 값(또는 봉인 값을 참조한 값)은 서버가 돌려주지 않는다. 직접 입력한 새 값만 보여 줄 수 있다.
  const hidden = !staged && r.sealed;
  const resolvedSealed = resolved?.sealed === true;
  const resolvedValue = resolved?.value;
  // 복사할 값: 배포 대기 중이면 입력한 값 그대로, 아니면 참조를 푼 값(가능할 때).
  const copyable = hidden ? null : staged ? r.value : resolvedSealed ? null : (resolvedValue ?? (resolvedPending ? null : r.value));
  const stateLabel = r.deleted ? t("삭제 예정") : added ? t("추가 예정") : changed ? t("변경 예정") : null;
  // Revealed value: the resolved value when the API gave one, otherwise what was typed.
  const revealed = staged ? r.value : resolvedPending ? "…" : resolvedValue === undefined ? r.value : resolvedSealed ? null : resolvedValue;

  return (
    <li className="pb-1">
      <div
        className={cx(
          "variable-row group",
          staged && !r.deleted && "bg-pink-50 text-pink-600",
          "rounded hover:bg-secondaryBg focus-within:bg-secondaryBg dark:hover:bg-gray-100 dark:focus-within:bg-gray-100",
          ROW_GRID,
          "min-h-[2.25rem] items-center justify-items-stretch px-4 font-mono",
          r.deleted && "opacity-50",
        )}
      >
        <div className="flex min-w-0 items-center space-x-4 text-sm">
          <KindIcon r={r} />
          <span className={cx("truncate", r.deleted && "line-through")} title={stateLabel ? t("{key} ({state})", { key: r.key, state: stateLabel }) : r.key}>
            {r.key}
          </span>
          {resolved?.cross_node &&
            (resolved.mesh ? (
              <span title={t("다른 노드의 서비스를 노드 간 사설 네트워크로 참조합니다")} className="!ml-2 shrink-0 text-gray-500">
                <Network className="size-3.5" aria-hidden />
              </span>
            ) : (
              <span title={t("다른 노드의 서비스를 참조하지만 사설 네트워크로 닿지 않습니다")} className="!ml-2 shrink-0 text-yellow-600">
                <AlertTriangle className="size-3.5" aria-hidden />
              </span>
            ))}
          {stateLabel ? (
            <span className="sr-only">{stateLabel}</span>
          ) : (
            savedNotDeployed && (
              <span className="!ml-2 size-1.5 shrink-0 rounded-full border border-pink-600" title={t("저장됨, 아직 배포 전")} role="img" aria-label={t("저장됨, 아직 배포 전")} />
            )
          )}
        </div>
        <div className="flex h-full min-w-0 items-center text-sm">
          <div className="h-full max-h-[15vh] min-w-[60px] whitespace-pre font-mono">
            {shown && !hidden ? (
              <div
                className="max-h-[15vh] overflow-y-auto whitespace-pre-wrap break-all rounded border px-2 py-1"
                title={isRef && !staged ? r.value : undefined}
              >
                {revealed === null ? (
                  <span className="inline-flex items-center gap-1.5 font-sans text-gray-400">
                    <Lock className="size-3" aria-hidden /> {t("봉인된 값을 참조합니다")}
                  </span>
                ) : revealed === "" ? (
                  <span aria-hidden className="select-none italic text-gray-500">{t("<빈 문자열>")}</span>
                ) : staged || resolvedValue === undefined ? (
                  <RefValue value={revealed} />
                ) : (
                  revealed
                )}
              </div>
            ) : (
              <span className="flex h-full items-center">{MASK}</span>
            )}
          </div>
          {!hidden && !r.deleted && (
            <div className="ml-2 flex flex-nowrap justify-end space-x-0.5 whitespace-nowrap">
              <IconButton
                size="sm"
                label={shown ? t("값 숨기기") : t("값 보기")}
                onClick={onToggleReveal}
                className="reveal lg:opacity-0 lg:focus:opacity-100 lg:group-hover:opacity-100"
              >
                {shown ? <EyeOff /> : <Eye />}
              </IconButton>
              {copyable !== null && (
                <IconButton size="sm" label={t("값 복사")} onClick={() => void copyText(copyable, t("{key} 값", { key: r.key }))} className="lg:opacity-0 lg:focus:opacity-100 lg:group-hover:opacity-100">
                  <Copy />
                </IconButton>
              )}
            </div>
          )}
        </div>
        <div className="flex items-center justify-end">
          <div className="actions flex items-center space-x-2">
            <Menu
              label={t("{key} 작업", { key: r.key })}
              trigger={(p) => (
                <IconButton {...p} label={t("변수 작업")} data-var-menu={r.key} className="p-1 pr-0 text-gray-600">
                  <MoreVertical />
                </IconButton>
              )}
            >
              {(close) =>
                r.deleted ? (
                  <MenuItem icon={<Undo2 />} disabled={!canEdit} reason={why} onSelect={() => (close(), onRestore())}>
                    {t("삭제 취소")}
                  </MenuItem>
                ) : (
                  <>
                    <MenuItem icon={<Pencil />} disabled={!canEdit} reason={why} onSelect={() => (close(), onEdit())} className="edit-variable">
                      {r.sealed ? t("값 교체") : t("편집")}
                    </MenuItem>
                    {!r.sealed && !staged && (
                      <MenuItem icon={<Lock />} disabled={!canEdit} reason={why} onSelect={() => (close(), onSeal())}>
                        {t("봉인")}
                      </MenuItem>
                    )}
                    {isRef && (
                      <MenuItem icon={<Link2 />} onSelect={() => (close(), void copyText(r.value, t("참조식")))}>
                        {t("참조식 복사")}
                      </MenuItem>
                    )}
                    {staged && (
                      <MenuItem icon={<Undo2 />} disabled={!canEdit} reason={why} onSelect={() => (close(), onRevert())}>
                        {added ? t("추가 취소") : t("변경 되돌리기")}
                      </MenuItem>
                    )}
                    <MenuItem danger icon={<Trash2 />} disabled={!canEdit} reason={why} onSelect={() => (close(), onDelete())}>
                      {t("삭제")}
                    </MenuItem>
                  </>
                )
              }
            </Menu>
          </div>
        </div>
      </div>
    </li>
  );
}
/** One variable in edit mode. Enter saves, Esc cancels. */
export function EditVariableRow({
  r,
  initialValue,
  existing,
  suggestions,
  onCommit,
  onCancel,
}: {
  r: Row;
  initialValue: string;
  /** Names already used (for rename collisions). */
  existing: Record<string, string>;
  suggestions: Suggestion[];
  onCommit: (key: string, value: string) => void;
  onCancel: () => void;
}) {
  const [key, setKey] = useState(r.key);
  const [val, setVal] = useState(initialValue);
  useEscape(onCancel);
  const k = r.sealed ? r.key : key.trim(); // 봉인 변수는 이름을 바꿀 수 없다(값만 교체)
  const keyErr = !k ? t("이름을 입력하세요.") : !KEY_RE.test(k) ? KEY_RULE : k !== r.key && k in existing ? t("{name} 변수가 이미 있습니다. 다른 이름을 쓰세요.", { name: k }) : null;
  const errId = `edit-${r.key}-err`;
  const commit = () => {
    if (keyErr) return;
    onCommit(k, val);
  };
  return (
    <li className="rounded bg-secondaryBg px-4 py-2 dark:bg-gray-100">
      <form
        className={cx(ROW_GRID, "items-start")}
        onSubmit={(e) => {
          e.preventDefault();
          commit();
        }}
      >
        <Input
          mono
          value={r.sealed ? r.key : key}
          readOnly={r.sealed}
          title={r.sealed ? t("봉인된 변수는 이름을 바꿀 수 없습니다") : undefined}
          aria-label={t("변수 이름")}
          aria-invalid={!!keyErr}
          aria-describedby={keyErr ? errId : undefined}
          invalid={!!keyErr && key !== ""}
          onChange={(e) => setKey(e.target.value)}
          className="h-[34px] !px-1.5"
        />
        <VarInput
          autoFocus
          refButton
          value={val}
          onChange={setVal}
          suggestions={suggestions}
          ariaLabel={r.sealed ? t("{key} 새 값(봉인 유지)", { key: r.key }) : t("{key} 값", { key: r.key })}
          placeholder={r.sealed ? t("새 값 (비워 두면 기존 값 유지)") : t("값 또는 ${{참조}}")}
          onEnter={commit}
        />
        <div className="flex items-center gap-1 pt-0.5">
          <IconButton type="submit" label={t("저장 (Enter)")} disabled={!!keyErr}>
            <Check />
          </IconButton>
          <IconButton label={t("취소 (Esc)")} onClick={onCancel}>
            <X />
          </IconButton>
        </div>
      </form>
      {keyErr && key !== "" && (
        <p id={errId} className="mt-2 flex items-center font-sans text-xs leading-[18px] text-red-600">
          {keyErr}
        </p>
      )}
    </li>
  );
}
