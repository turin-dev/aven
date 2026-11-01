"use client";

import { useState } from "react";
import type { CanvasService, Environment } from "@/lib/api";
import type { ServiceActions } from "@/lib/useServiceActions";
import { Button, ErrorBox, useConfirm } from "../../ui";
import { SettingItem } from "./primitives";
import { TypeToConfirm } from "./TypeToConfirm";
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
  const vols = svc.volumes ?? [];
  return (
    <TypeToConfirm
      title={t("서비스 삭제")}
      name={svc.name}
      confirmLabel={t("서비스 삭제")}
      inputLabel={t("삭제할 서비스 이름")}
      run={run}
      onClose={onClose}
      message={
        <div className="space-y-2">
          <p>
            {tr("{service} 서비스를 모든 환경에서 삭제합니다. 컨테이너, 도메인, 배포 기록이 함께 지워지며 되돌릴 수 없습니다.", {
              service: <span className="font-mono text-foreground">{svc.name}</span>,
            })}
          </p>
          {vols.length > 0 && (
            <p className="text-red-600">
              {tr("이 환경에 연결된 볼륨: {volumes}. 데이터가 남아 있다면 먼저 백업하세요.", {
                volumes: <span className="font-mono">{vols.map((v) => v.name).join(", ")}</span>,
              })}
            </p>
          )}
        </div>
      }
    />
  );
}
