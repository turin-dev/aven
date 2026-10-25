"use client";

import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Layers, RefreshCw } from "lucide-react";
import { api, type CanvasService, type StackView } from "@/lib/api";
import { qk, useCaps } from "@/lib/queries";
import { lock } from "../Perm";
import { CARD_W, cardSpan } from "../ServiceCard";
import { useConfirm } from "../ui";
import { useContextTrigger } from "../ContextMenu";
import { StackContextMenu } from "./CanvasMenus";
import { t } from "@/lib/i18n";

const PAD = 25;
const HEAD = 35;

export function StackFrames({
  stacks,
  services,
  posOf,
  pid,
  envId,
}: {
  stacks: StackView[];
  services: CanvasService[];
  posOf: (s: CanvasService, i: number) => { x: number; y: number };
  pid: string;
  envId: string;
}) {
  const qc = useQueryClient();
  const canEdit = useCaps(pid).canEdit;
  const { confirm, node } = useConfirm();
  const [busy, setBusy] = useState<string | null>(null);
  const [ctx, setCtx] = useState<{ x: number; y: number; id: string } | null>(null);
  const trigger = useContextTrigger((x, y, e) => {
    const id = (e.target as HTMLElement | null)?.closest?.<HTMLElement>("[data-stack-frame]")?.dataset.stackFrame;
    if (id) setCtx({ x, y, id });
  });
  const ctxStack = ctx ? stacks.find((s) => s.id === ctx.id) : undefined;
  return (
    <>
      {node}
      {ctx && ctxStack && (
        <StackContextMenu
          key={`${ctx.id}${ctx.x}${ctx.y}`}
          x={ctx.x}
          y={ctx.y}
          name={ctxStack.name}
          deploying={ctxStack.deploying || busy === ctxStack.id}
          canEdit={canEdit}
          onRedeploy={async () => {
            await api.redeployStack(pid, envId, ctxStack.id);
            qc.invalidateQueries({ queryKey: qk.canvas(envId) });
          }}
          onDelete={async () => {
            await api.deleteStack(pid, envId, ctxStack.id);
            qc.invalidateQueries({ queryKey: qk.canvas(envId) });
            qc.invalidateQueries({ queryKey: ["project"] });
          }}
          onDone={() => setCtx(null)}
        />
      )}
      {stacks.map((st) => {
        const members = services.map((s, i) => ({ s, p: posOf(s, i) })).filter((m) => m.s.stack_id === st.id);
        if (members.length === 0) return null;
        const minX = Math.min(...members.map((m) => m.p.x)) - PAD;
        const minY = Math.min(...members.map((m) => m.p.y)) - PAD - HEAD;
        const maxX = Math.max(...members.map((m) => m.p.x + CARD_W)) + PAD;
        const maxY = Math.max(...members.map((m) => m.p.y + cardSpan(m.s))) + PAD;
        const deploying = st.deploying || busy === st.id;
        return (
          <div
            key={st.id}
            className="pointer-events-none absolute rounded-xl border border-gray-200 bg-[hsla(246,6%,55%,0.05)] pt-3"
            style={{ left: minX, top: minY, width: maxX - minX, height: maxY - minY, paddingLeft: PAD, paddingRight: PAD }}
          >
            <div className="pointer-events-auto flex h-6 w-full items-center justify-between gap-3" data-nodrag data-stack-frame={st.id} {...trigger}>
              <span className="flex min-w-0 items-center gap-3">
                <Layers className="size-6 shrink-0 text-gray-500" aria-hidden />
                <span className="truncate font-semibold">{st.name}</span>
                <span className="shrink-0 text-sm text-gray-500">compose · {t("{count}개 서비스", { count: members.length })}</span>
              </span>
              <button
                type="button"
                disabled={deploying}
                {...lock(canEdit)}
                onPointerDown={(e) => e.stopPropagation()}
                onClick={() =>
                  confirm({
                    title: t("스택 {name} 전체 재배포", { name: st.name }),
                    message: t("모든 서비스를 의존 순서대로 다시 배포합니다."),
                    confirmLabel: t("재배포"),
                    run: async () => {
                      setBusy(st.id);
                      try {
                        await api.redeployStack(pid, envId, st.id);
                      } finally {
                        setBusy(null);
                        qc.invalidateQueries({ queryKey: qk.canvas(envId) });
                      }
                    },
                  })
                }
                className="-mr-1 flex h-[26px] shrink-0 cursor-pointer items-center gap-1.5 rounded-md border border-gray-200 bg-secondaryBg px-1.5 text-xs text-gray-600 hover:border-gray-300 hover:text-foreground disabled:cursor-default disabled:opacity-50"
              >
                <RefreshCw className={deploying ? "size-3.5 animate-spin" : "size-3.5"} /> {t("전체 재배포")}
              </button>
            </div>
          </div>
        );
      })}
    </>
  );
}
