"use client";

import { useState } from "react";
import { AlertTriangle } from "lucide-react";
import type { CanvasService, Environment } from "@/lib/api";
import type { ServiceActions } from "@/lib/useServiceActions";
import { Button, Dialog, ErrorBox, Input, useConfirm } from "../../ui";
import { FIELD, SettingItem } from "./primitives";
import { t, tr } from "@/lib/i18n";

export function DangerZone({ svc, env, actions, running }: { svc: CanvasService; env: Environment; actions: ServiceActions; running: boolean }) {
  const { confirm, node } = useConfirm();
  const [deleting, setDeleting] = useState(false);
  return (
    <>
      <SettingItem
        danger
        label={t("서비스 중지")}
        keywords="stop pause 멈춤"
        description={tr("{env} 환경에서 실행 중인 컨테이너를 멈춥니다. 다시 배포할 때까지 접속할 수 없고, 설정과 데이터는 그대로 남습니다.", {
          env: <b className="font-semibold">{env.name}</b>,
        })}
      >
        <Button
          variant="danger-outline"
          disabled={!running}
          title={running ? undefined : t("실행 중인 컨테이너가 없습니다")}
          onClick={() =>
            confirm({
              title: t("서비스 중지"),
              message: tr("{service}의 {env} 환경 컨테이너를 멈춥니다. 다시 배포하면 다시 시작됩니다.", {
                service: <span className="font-mono text-foreground">{svc.name}</span>,
                env: env.name,
              }),
              confirmLabel: t("서비스 중지"),
              danger: true,
              run: actions.stop,
            })
          }
        >
          <span className="inline-block min-w-0">{t("서비스 중지")}</span>
        </Button>
      </SettingItem>
      <SettingItem
        danger
        label={t("서비스 삭제")}
        keywords="delete service remove 제거"
        description={tr("이 서비스를 삭제하면 모든 배포가 {permanent}되고 {scope}에서 사라집니다. 되돌릴 수 없습니다.", {
          permanent: <b className="font-semibold">{t("영구적으로 삭제")}</b>,
          scope: <b className="font-semibold">{t("모든 환경")}</b>,
        })}
      >
        <div className="group relative">
          <Button variant="danger" onClick={() => setDeleting(true)}>
            <span className="inline-block min-w-0">{t("서비스 삭제")}</span>
          </Button>
        </div>
      </SettingItem>
      {actions.error ? <ErrorBox error={actions.error} /> : null}
      {deleting && <DeleteServiceDialog svc={svc} onClose={() => setDeleting(false)} run={actions.remove} />}
      {node}
    </>
  );
}

function DeleteServiceDialog({ svc, onClose, run }: { svc: CanvasService; onClose: () => void; run: () => Promise<unknown> | unknown }) {
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const ok = text.trim() === svc.name;
  const submit = async () => {
    if (!ok || busy) return;
    setBusy(true);
    setError(null);
    try {
      await run();
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open
      onClose={() => !busy && onClose()}
      title={t("서비스 삭제")}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={busy}>
            {t("취소")}
          </Button>
          <Button variant="danger" disabled={!ok} loading={busy} onClick={submit}>
            {t("서비스 삭제")}
          </Button>
        </>
      }
    >
      <div className="flex gap-4">
        <span className="flex size-10 shrink-0 items-center justify-center rounded-full bg-red-500/10">
          <AlertTriangle className="size-5 text-red-500" />
        </span>
        <div className="min-w-0 space-y-3">
          <p>
            {tr("{service} 서비스를 모든 환경에서 삭제합니다. 컨테이너, 도메인, 배포 기록이 함께 지워지며 되돌릴 수 없습니다.", {
              service: <span className="font-mono text-foreground">{svc.name}</span>,
            })}
          </p>
          <label className="block">
            <span className="mb-2 block text-xs font-semibold leading-[18px] text-gray-600">
              {tr("확인을 위해 {name}을(를) 입력하세요", {
                name: <span className="required-confirm-text font-mono font-bold text-foreground">{svc.name}</span>,
              })}
            </span>
            <Input
              size="sm"
              mono
              autoFocus
              className={FIELD}
              value={text}
              onChange={(e) => setText(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && submit()}
              aria-label={t("삭제할 서비스 이름")}
              autoComplete="off"
            />
          </label>
          {error ? <ErrorBox error={error} /> : null}
        </div>
      </div>
    </Dialog>
  );
}
