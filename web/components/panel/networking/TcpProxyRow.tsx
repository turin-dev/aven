"use client";

/* TCP proxy row in the public networking list (modeled on Railway's dashboard: "host:port" + "→ Port N", copy/edit/delete) with the
   sliding edit panel that changes the container port behind the same public port. */

import { useState } from "react";
import { Plug, SquarePen, Trash2 } from "lucide-react";
import { api, type CanvasService, type DetectedPort, type Environment, type TcpProxyView } from "@/lib/api";
import { t } from "@/lib/i18n";
import { ErrorBox } from "../../ui";
import { PortPicker, initialPortChoice, type PortChoice } from "./PortPicker";
import { CopyBtn, IconBtn, PanelFooter, PortMeta, Sliding, portOk, useNetRun } from "./parts";

export const tcpAddress = (t: Pick<TcpProxyView, "host" | "public_port">) => `${t.host.includes(":") ? `[${t.host}]` : t.host || "—"}:${t.public_port ?? "—"}`;

export function TcpProxyRow({
  svc,
  env,
  proxy,
  detected,
  servicePort,
  onDelete,
}: {
  svc: CanvasService;
  env: Environment;
  proxy: TcpProxyView;
  detected: DetectedPort[];
  servicePort: number;
  onDelete: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const addr = tcpAddress(proxy);
  return (
    <>
      <div className="group flex w-full items-center px-4 py-3 first:rounded-t-lg last:rounded-b-lg hover:bg-secondaryBg dark:hover:bg-gray-100">
        <div className="mr-3">
          <span className="icon-container icon-md text-2xl text-gray-500" aria-hidden="true">
            <Plug />
          </span>
        </div>
        <div className="grid min-w-0 flex-1 gap-1">
          <span className="block truncate text-left text-sm" title={addr}>
            {addr}
          </span>
          <PortMeta port={proxy.container_port ?? "—"}>
            {" · "}{t("TCP 프록시")}
            {!proxy.available && proxy.reason ? <span className="text-yellow-600"> · {proxy.reason}</span> : null}
          </PortMeta>
        </div>
        <div className="ml-auto flex flex-shrink-0 space-x-1">
          <CopyBtn value={addr} />
          <IconBtn title={t("수정")} pressed={editing} onClick={() => setEditing((e) => !e)}>
            <SquarePen />
          </IconBtn>
          <IconBtn title={t("삭제")} onClick={onDelete}>
            <Trash2 />
          </IconBtn>
        </div>
      </div>
      <Sliding open={editing}>
        <TcpEditForm svc={svc} env={env} proxy={proxy} detected={detected} servicePort={servicePort} onDone={() => setEditing(false)} />
      </Sliding>
    </>
  );
}

function TcpEditForm({
  svc,
  env,
  proxy,
  detected,
  servicePort,
  onDone,
}: {
  svc: CanvasService;
  env: Environment;
  proxy: TcpProxyView;
  detected: DetectedPort[];
  servicePort: number;
  onDone: () => void;
}) {
  const [port, setPort] = useState<PortChoice>(() => initialPortChoice(proxy.container_port, detected));
  const { run, busy, error } = useNetRun(svc, env);
  const changed = portOk(port.text) && Number(port.text) !== proxy.container_port;
  return (
    <form
      className="space-y-4"
      onSubmit={async (e) => {
        e.preventDefault();
        if (changed && (await run("save", () => api.patchTcpProxy(svc.id, env.id, Number(port.text))))) onDone();
      }}
    >
      <PortPicker id="tcp-edit" value={port} onChange={setPort} detected={detected} servicePort={servicePort} />
      {error ? <ErrorBox error={error} /> : null}
      <PanelFooter message={t("공개 포트는 그대로 두고 연결할 컨테이너 포트를 바꿉니다")} onCancel={onDone} canSubmit={changed} busy={busy === "save"} />
    </form>
  );
}
